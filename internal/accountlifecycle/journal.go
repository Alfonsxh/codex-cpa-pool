package accountlifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountprojection"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/google/uuid"
)

const lifecycleJournalStateName = "account_lifecycle_operation"

const (
	operationCreate    = "create"
	operationUpdate    = "update"
	operationDelete    = "delete"
	operationClearAuth = "clear_auth"
	operationPolicy    = "policy"

	phaseAccepted           = "accepted"
	phaseRoutesPrepared     = "routes_prepared"
	phaseRoutesEvacuated    = "routes_evacuated"
	phaseFilesPrepared      = "files_prepared"
	phaseControlApplied     = "control_applied"
	phaseProjectionRendered = "projection_rendered"
	phaseRuntimePrepared    = "runtime_prepared"
	phaseSnapshotActivated  = "snapshot_activated"
)

var ErrLifecycleRecoveryRequired = errors.New("account lifecycle recovery is required")

// Operation is deliberately secret-free. The durable record only contains
// enough identity and phase information to reconcile SQLite, filesystem,
// Docker, and Gateway state after a process interruption.
type Operation struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"`
	Phase           string            `json:"phase"`
	AccountID       string            `json:"account_id"`
	NewAccountID    string            `json:"new_account_id,omitempty"`
	Backup          string            `json:"backup,omitempty"`
	EvacuatedRoutes map[string]string `json:"evacuated_routes,omitempty"`
	StartedAt       int64             `json:"started_at"`
	UpdatedAt       int64             `json:"updated_at"`
}

type operationJournal interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
	DeleteRuntimeState(context.Context, string) error
}

func (manager *Manager) beginOperation(
	ctx context.Context,
	kind string,
	accountID string,
	newAccountID string,
) (Operation, error) {
	var existing Operation
	if found, err := manager.journal.ReadRuntimeState(ctx, lifecycleJournalStateName, &existing); err != nil {
		return Operation{}, fmt.Errorf("read account lifecycle journal: %w", err)
	} else if found {
		return Operation{}, fmt.Errorf("%w: operation %s is still at phase %s", ErrLifecycleRecoveryRequired, existing.ID, existing.Phase)
	}
	now := manager.now().Unix()
	operation := Operation{
		ID: uuid.NewString(), Kind: kind, Phase: phaseAccepted,
		AccountID: accountID, NewAccountID: newAccountID,
		StartedAt: now, UpdatedAt: now,
	}
	if err := validateOperation(operation); err != nil {
		return Operation{}, err
	}
	if err := manager.journal.WriteRuntimeState(ctx, lifecycleJournalStateName, operation); err != nil {
		return Operation{}, fmt.Errorf("start account lifecycle journal: %w", err)
	}
	return operation, nil
}

func (manager *Manager) advanceOperation(
	ctx context.Context,
	operation *Operation,
	phase string,
	backup string,
) error {
	if operation == nil {
		return errors.New("account lifecycle operation is nil")
	}
	operation.Phase = phase
	if strings.TrimSpace(backup) != "" {
		operation.Backup = strings.TrimSpace(backup)
	}
	operation.UpdatedAt = manager.now().Unix()
	if err := validateOperation(*operation); err != nil {
		return err
	}
	if err := manager.journal.WriteRuntimeState(ctx, lifecycleJournalStateName, *operation); err != nil {
		return fmt.Errorf("advance account lifecycle journal to %s: %w", phase, err)
	}
	return nil
}

func (manager *Manager) finishOperation(ctx context.Context, operation Operation) error {
	if err := manager.journal.DeleteRuntimeState(ctx, lifecycleJournalStateName); err != nil {
		return fmt.Errorf("finish account lifecycle journal %s: %w", operation.ID, err)
	}
	return nil
}

func validateOperation(operation Operation) error {
	if _, err := uuid.Parse(operation.ID); err != nil {
		return fmt.Errorf("invalid account lifecycle operation ID: %w", err)
	}
	if _, err := controlplane.NormalizeAccountID(operation.AccountID); err != nil {
		return err
	}
	switch operation.Kind {
	case operationCreate, operationDelete, operationClearAuth, operationPolicy:
		if operation.NewAccountID != "" {
			return errors.New("unexpected new account ID in lifecycle operation")
		}
	case operationUpdate:
		if _, err := controlplane.NormalizeAccountID(operation.NewAccountID); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown account lifecycle operation kind %q", operation.Kind)
	}
	switch operation.Phase {
	case phaseAccepted, phaseRoutesPrepared, phaseRoutesEvacuated, phaseFilesPrepared, phaseControlApplied,
		phaseProjectionRendered, phaseRuntimePrepared, phaseSnapshotActivated:
	default:
		return fmt.Errorf("unknown account lifecycle operation phase %q", operation.Phase)
	}
	if operation.StartedAt <= 0 || operation.UpdatedAt < operation.StartedAt {
		return errors.New("invalid account lifecycle operation timestamps")
	}
	if len(operation.EvacuatedRoutes) > 10000 {
		return errors.New("account lifecycle evacuation journal is too large")
	}
	for user, target := range operation.EvacuatedRoutes {
		if strings.ToLower(strings.TrimSpace(user)) != user || !strings.Contains(user, "@") {
			return fmt.Errorf("invalid evacuated route user %q", user)
		}
		if _, err := controlplane.NormalizeAccountID(target); err != nil {
			return err
		}
	}
	return nil
}

func (manager *Manager) finalizeOperation(
	ctx context.Context,
	operation Operation,
	returnError *error,
) {
	if returnError == nil {
		return
	}
	if *returnError != nil {
		var compensated *compensatedError
		if !errors.As(*returnError, &compensated) || compensated.recovery != nil {
			// Retain the journal. The next writer owner must reconcile it before
			// serving account mutations.
			return
		}
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := manager.finishOperation(finishContext, operation); err != nil {
		*returnError = errors.Join(*returnError, err)
	}
}

// Recover must run while the Admin writer lease is held and before the HTTP
// listener becomes reachable. It reconciles to the authoritative SQLite state;
// it never invents or rotates an API Key and it fails closed when an encrypted
// custom proxy cannot be recovered safely.
func (manager *Manager) Recover(ctx context.Context) error {
	manager.lock.Lock()
	defer manager.lock.Unlock()
	var operation Operation
	found, err := manager.journal.ReadRuntimeState(ctx, lifecycleJournalStateName, &operation)
	if err != nil {
		return fmt.Errorf("read account lifecycle recovery journal: %w", err)
	}
	if !found {
		return nil
	}
	if err := validateOperation(operation); err != nil {
		return fmt.Errorf("%w: %v", ErrLifecycleRecoveryRequired, err)
	}
	// The policy and all affected routes commit in one SQLite transaction.
	// Even an accepted journal can lag that commit. Republish the authoritative
	// state without rebuilding a container or interrupting its old streams.
	if operation.Kind == operationPolicy {
		if _, err := manager.projection.Render(ctx); err != nil {
			return fmt.Errorf("render recovered account policy: %w", err)
		}
		if _, err := manager.snapshots.PublishAuthSnapshot(ctx, true); err != nil {
			return fmt.Errorf("activate recovered account policy: %w", err)
		}
		return manager.finishOperation(ctx, operation)
	}
	desired, err := manager.recoveryAccount(ctx, operation)
	if err != nil {
		return err
	}
	// phaseAccepted is durable before any authoritative SQLite mutation. A
	// create may already have made its empty runtime directories and a delete
	// may already have written its backup, because those reversible preparations
	// happen before phaseFilesPrepared is persisted. Reconcile only when the
	// canonical account row proves that the operation advanced further than its
	// journal. This avoids replacing an active CPA when no authoritative work
	// began while still recovering a lagging journal fail-closed.
	if operation.Phase == phaseAccepted && acceptedStateIsUnchanged(operation, desired) {
		if operation.Kind == operationCreate {
			if err := manager.files.Recover(operation, nil); err != nil {
				return fmt.Errorf("clean accepted account creation files: %w", err)
			}
		}
		return manager.finishOperation(ctx, operation)
	}
	// An update or OAuth clear at routes_prepared may have published the
	// temporary fallback snapshot, but it cannot have touched files, control
	// rows, or Docker yet. For a rename, verify the old account is still the
	// canonical row before taking this fast path; a newer implementation may
	// have committed control state while leaving an older journal phase behind.
	if operation.Phase == phaseRoutesPrepared &&
		(operation.Kind == operationClearAuth ||
			(operation.Kind == operationUpdate && updateStateIsUnchanged(operation, desired))) {
		if err := manager.recoverEvacuatedRoutes(ctx, operation, desired); err != nil {
			return err
		}
		if _, err := manager.snapshots.PublishAuthSnapshot(ctx, true); err != nil {
			return fmt.Errorf("activate early account recovery snapshot: %w", err)
		}
		return manager.finishOperation(ctx, operation)
	}
	if err := manager.recoverProxySecrets(ctx, operation, desired); err != nil {
		return err
	}

	// A committed deletion or an uncommitted creation must first remove the
	// stale route from Gateway. Keep the old container and files available until
	// the replacement snapshot is confirmed active.
	removeOnly := desired == nil && (operation.Kind == operationCreate || operation.Kind == operationDelete)
	if removeOnly {
		if _, err := manager.projection.Render(ctx); err != nil {
			return fmt.Errorf("render account recovery projection: %w", err)
		}
		if _, err := manager.snapshots.PublishAuthSnapshot(ctx, true); err != nil {
			return fmt.Errorf("activate account recovery snapshot: %w", err)
		}
		// A committed deletion may have stopped after the replacement routes
		// became active but before the old container was removed. Preserve every
		// request that was admitted by the previous snapshot: recovery must obey
		// the same drain boundary as the normal deletion path.
		if operation.Kind == operationDelete {
			if manager.drainer == nil {
				return ErrRouteEvacuationUnavailable
			}
			if err := manager.drainer.WaitAccountDrained(ctx, operation.AccountID); err != nil {
				return fmt.Errorf("%w: %v", ErrAccountDrainTimeout, err)
			}
		}
		if err := manager.runtime.ReconcileAccount(ctx, operation.AccountID, nil); err != nil {
			return fmt.Errorf("remove interrupted account runtime: %w", err)
		}
		if err := manager.files.Recover(operation, nil); err != nil {
			return fmt.Errorf("remove interrupted account files: %w", err)
		}
		return manager.finishOperation(ctx, operation)
	}

	if err := manager.files.Recover(operation, desired); err != nil {
		return fmt.Errorf("recover account files: %w", err)
	}
	if _, err := manager.projection.Render(ctx); err != nil {
		return fmt.Errorf("render recovered account projection: %w", err)
	}
	previousID := operation.AccountID
	if desired != nil && desired.ID == previousID {
		previousID = operation.NewAccountID
	}
	if err := manager.runtime.ReconcileAccount(ctx, previousID, desired); err != nil {
		return fmt.Errorf("recover account runtime: %w", err)
	}
	if err := manager.recoverEvacuatedRoutes(ctx, operation, desired); err != nil {
		return err
	}
	if _, err := manager.snapshots.PublishAuthSnapshot(ctx, true); err != nil {
		return fmt.Errorf("activate recovered account snapshot: %w", err)
	}
	return manager.finishOperation(ctx, operation)
}

func acceptedStateIsUnchanged(operation Operation, desired *controlplane.Account) bool {
	switch operation.Kind {
	case operationCreate:
		return desired == nil
	case operationDelete, operationClearAuth:
		return desired != nil && desired.ID == operation.AccountID
	case operationUpdate:
		return updateStateIsUnchanged(operation, desired)
	default:
		return false
	}
}

func updateStateIsUnchanged(operation Operation, desired *controlplane.Account) bool {
	return desired != nil && desired.ID == operation.AccountID
}

func (manager *Manager) recoverEvacuatedRoutes(
	ctx context.Context,
	operation Operation,
	desired *controlplane.Account,
) error {
	if desired == nil || !desired.GroupEnabled || len(operation.EvacuatedRoutes) == 0 {
		return nil
	}
	routes, err := manager.store.ReadRoutes(ctx)
	if err != nil {
		return fmt.Errorf("read interrupted evacuation routes: %w", err)
	}
	assignments := make(map[string]string)
	expected := make(map[string]string)
	for user, fallback := range operation.EvacuatedRoutes {
		current := routes[user]
		if current == desired.ID {
			continue
		}
		if current != fallback && current != operation.AccountID && current != operation.NewAccountID {
			return fmt.Errorf("%w: interrupted evacuation route for %s changed to %s", controlplane.ErrRouteConflict, user, current)
		}
		assignments[user] = desired.ID
		expected[user] = current
	}
	if len(assignments) == 0 {
		return nil
	}
	if _, err := manager.store.ApplyRoutesExpected(ctx, assignments, expected); err != nil {
		return fmt.Errorf("restore interrupted evacuation routes: %w", err)
	}
	return nil
}

func (manager *Manager) recoveryAccount(ctx context.Context, operation Operation) (*controlplane.Account, error) {
	switch operation.Kind {
	case operationCreate, operationDelete, operationClearAuth:
		account, found, err := manager.readRecoveryAccount(ctx, operation.AccountID)
		if err != nil || !found {
			if operation.Kind == operationClearAuth && err == nil {
				return nil, fmt.Errorf("%w: OAuth recovery account no longer exists", ErrLifecycleRecoveryRequired)
			}
			return nil, err
		}
		return &account, nil
	case operationUpdate:
		if account, found, err := manager.readRecoveryAccount(ctx, operation.NewAccountID); err != nil {
			return nil, err
		} else if found {
			return &account, nil
		}
		if account, found, err := manager.readRecoveryAccount(ctx, operation.AccountID); err != nil {
			return nil, err
		} else if found {
			return &account, nil
		}
		return nil, fmt.Errorf("%w: neither side of interrupted account update exists", ErrLifecycleRecoveryRequired)
	default:
		return nil, fmt.Errorf("%w: unknown operation kind", ErrLifecycleRecoveryRequired)
	}
}

func (manager *Manager) readRecoveryAccount(ctx context.Context, accountID string) (controlplane.Account, bool, error) {
	stored, _, err := manager.store.ReadAccountLifecycle(ctx, accountID)
	if errors.Is(err, controlplane.ErrAccountLifecycleNotFound) {
		return controlplane.Account{}, false, nil
	}
	if err != nil {
		return controlplane.Account{}, false, fmt.Errorf("read recovery account %s: %w", accountID, err)
	}
	return stored.Account, true, nil
}

func (manager *Manager) recoverProxySecrets(
	ctx context.Context,
	operation Operation,
	desired *controlplane.Account,
) error {
	oldName := accountProxySecretPrefix + operation.AccountID
	newName := accountProxySecretPrefix + operation.NewAccountID
	switch operation.Kind {
	case operationDelete:
		if desired == nil {
			return manager.store.DeleteSecret(ctx, oldName)
		}
		return nil
	case operationUpdate:
		if desired == nil {
			return nil
		}
		if desired.ID == operation.AccountID {
			if operation.NewAccountID != operation.AccountID {
				return manager.store.DeleteSecret(ctx, newName)
			}
			return manager.requireCustomProxy(ctx, desired, oldName)
		}
		if desired.ID != operation.NewAccountID {
			return fmt.Errorf("%w: unexpected proxy recovery target", ErrLifecycleRecoveryRequired)
		}
		if value, found, err := manager.store.ReadSecret(ctx, newName); err != nil {
			return err
		} else if found {
			if _, err := accountprojection.NormalizeProxyURL(value); err != nil {
				return fmt.Errorf("%w: invalid recovered account proxy", ErrLifecycleRecoveryRequired)
			}
			return manager.store.DeleteSecret(ctx, oldName)
		}
		value, found, err := manager.store.ReadSecret(ctx, oldName)
		if err != nil {
			return err
		}
		if found {
			if _, err := accountprojection.NormalizeProxyURL(value); err != nil {
				return fmt.Errorf("%w: invalid previous account proxy", ErrLifecycleRecoveryRequired)
			}
			if err := manager.store.WriteSecret(ctx, newName, value); err != nil {
				return err
			}
			return manager.store.DeleteSecret(ctx, oldName)
		}
		return manager.requireCustomProxy(ctx, desired, newName)
	case operationCreate:
		if desired != nil {
			return manager.requireCustomProxy(ctx, desired, oldName)
		}
		return manager.store.DeleteSecret(ctx, oldName)
	case operationClearAuth:
		return nil
	default:
		return fmt.Errorf("%w: unknown proxy recovery operation", ErrLifecycleRecoveryRequired)
	}
}

func (manager *Manager) requireCustomProxy(ctx context.Context, account *controlplane.Account, name string) error {
	if account == nil || account.ProxyMode != "custom" {
		return nil
	}
	value, found, err := manager.store.ReadSecret(ctx, name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: custom proxy secret is unavailable for %s", ErrLifecycleRecoveryRequired, account.ID)
	}
	if _, err := accountprojection.NormalizeProxyURL(value); err != nil {
		return fmt.Errorf("%w: custom proxy secret is invalid for %s", ErrLifecycleRecoveryRequired, account.ID)
	}
	return nil
}

type compensatedError struct {
	cause    error
	recovery error
}

func (failure *compensatedError) Error() string {
	return errors.Join(failure.cause, failure.recovery).Error()
}

func (failure *compensatedError) Unwrap() []error {
	return []error{failure.cause, failure.recovery}
}

func newCompensatedError(cause error, recoveryErrors ...error) error {
	recovery := errors.Join(recoveryErrors...)
	return &compensatedError{cause: cause, recovery: recovery}
}

func operationNow(now func() time.Time) func() time.Time {
	if now != nil {
		return now
	}
	return time.Now
}
