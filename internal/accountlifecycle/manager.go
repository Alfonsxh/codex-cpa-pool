package accountlifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountprojection"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
)

const (
	defaultAccountPortStart  = 18319
	defaultAccountPortEnd    = 18999
	rollbackTimeout          = 20 * time.Second
	accountProxySecretPrefix = "cpa_account_proxy_url:"
	managementKeySecretName  = "cpa_management_key"
)

var (
	ErrLifecycleUnavailable           = errors.New("account lifecycle service is unavailable")
	ErrNoAccountPort                  = errors.New("no safe business CPA port is available")
	ErrRuntimeTransition              = errors.New("account runtime transition failed")
	ErrUnavailableProxyRepairRejected = errors.New("unavailable account proxy repair is not allowed")
)

type Store interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadSecret(context.Context, string) (string, bool, error)
	WriteSecret(context.Context, string, string) error
	DeleteSecret(context.Context, string) error
	ReadAccountLifecycle(context.Context, string) (controlplane.StoredAccount, []controlplane.StoredKeyRecord, error)
	ApplyAccountCreation(context.Context, controlplane.Account) (controlplane.AccountCreation, error)
	RestoreAccountCreation(context.Context, controlplane.AccountCreation) error
	ApplyAccountUpdate(context.Context, controlplane.AccountUpdateRequest) (controlplane.AccountUpdate, error)
	RestoreAccountUpdate(context.Context, controlplane.AccountUpdate) error
	ApplyAccountDeletion(context.Context, string, string, bool) (controlplane.AccountDeletion, error)
	RestoreAccountDeletion(context.Context, controlplane.AccountDeletion) error
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
	DeleteRuntimeState(context.Context, string) error
	ReadRoutes(context.Context) (map[string]string, error)
	ApplyRoutesExpected(context.Context, map[string]string, map[string]string) (controlplane.RouteUpdateResult, error)
}

type Projection interface {
	Render(context.Context) (accountprojection.Result, error)
}

type SnapshotPublisher interface {
	PublishAuthSnapshot(context.Context, bool) (failover.Snapshot, error)
}

// RuntimeTransition represents a prepared container change with a retained
// rollback candidate. Commit may remove the old candidate only after the new
// Gateway snapshot is active.
type RuntimeTransition interface {
	Commit(context.Context) error
	Rollback(context.Context) error
}

type Runtime interface {
	ReservedHostPorts(context.Context) (map[int]struct{}, error)
	PrepareCreate(context.Context, controlplane.Account) (RuntimeTransition, error)
	PrepareUpdate(context.Context, controlplane.Account, controlplane.Account) (RuntimeTransition, error)
	PrepareDelete(context.Context, controlplane.Account) (RuntimeTransition, error)
	RestartAccount(context.Context, string) error
	ReconcileAccount(context.Context, string, *controlplane.Account) error
}

type Config struct {
	Store      Store
	Files      Files
	Projection Projection
	Snapshots  SnapshotPublisher
	Runtime    Runtime
	Lock       sync.Locker
	Now        func() time.Time
	Drainer    AccountDrainer
	States     failover.AccountStateProvider
}

type Manager struct {
	store      Store
	files      Files
	projection Projection
	snapshots  SnapshotPublisher
	runtime    Runtime
	lock       sync.Locker
	journal    operationJournal
	now        func() time.Time
	drainer    AccountDrainer
	states     failover.AccountStateProvider
}

type CreateRequest struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	ProxyMode string `json:"proxy_mode"`
	ProxyURL  string `json:"proxy_url"`
}

type CreateResult struct {
	Account            controlplane.Account `json:"account"`
	CreatedKeyRows     int                  `json:"created_key_rows"`
	SnapshotGeneration string               `json:"snapshot_generation"`
}

type UpdateRequest struct {
	AccountID                   string  `json:"account_id"`
	NewAccountID                string  `json:"new_account_id"`
	Email                       string  `json:"email"`
	ProxyMode                   string  `json:"proxy_mode"`
	ProxyURL                    *string `json:"proxy_url"`
	Enabled                     *bool   `json:"enabled"`
	Default                     *bool   `json:"default"`
	FallbackAccount             string  `json:"fallback_account"`
	PolicyOnly                  bool    `json:"-"`
	AllowUnavailableProxyRepair bool    `json:"-"`
}

type UpdateResult struct {
	Account            controlplane.Account `json:"account"`
	RenamedFrom        string               `json:"renamed_from,omitempty"`
	ReroutedUsers      int                  `json:"rerouted_users"`
	Backup             string               `json:"backup,omitempty"`
	SnapshotGeneration string               `json:"snapshot_generation"`
}

type DeleteRequest struct {
	AccountID       string `json:"account_id"`
	FallbackAccount string `json:"fallback_account"`
	RevokeExclusive bool   `json:"revoke_exclusive"`
}

type DeleteResult struct {
	AccountID          string `json:"account_id"`
	RemovedKeyRows     int    `json:"removed_key_rows"`
	RevokedExclusive   int    `json:"revoked_exclusive_keys"`
	ReroutedUsers      int    `json:"rerouted_users"`
	ReplacementAccount string `json:"replacement_account"`
	Backup             string `json:"backup"`
	SnapshotGeneration string `json:"snapshot_generation"`
}

type AuthClearResult struct {
	AccountID string `json:"account_id"`
	Backup    string `json:"backup"`
}

func New(config Config) (*Manager, error) {
	if config.Store == nil || config.Files == nil || config.Projection == nil ||
		config.Snapshots == nil || config.Runtime == nil {
		return nil, ErrLifecycleUnavailable
	}
	if config.Lock == nil {
		config.Lock = &sync.Mutex{}
	}
	return &Manager{
		store: config.Store, files: config.Files, projection: config.Projection,
		snapshots: config.Snapshots, runtime: config.Runtime, lock: config.Lock,
		journal: config.Store, now: operationNow(config.Now), drainer: config.Drainer, states: config.States,
	}, nil
}

func (manager *Manager) Create(ctx context.Context, request CreateRequest) (result CreateResult, returnError error) {
	manager.lock.Lock()
	defer manager.lock.Unlock()
	accountID, err := controlplane.NormalizeAccountID(request.ID)
	if err != nil {
		return CreateResult{}, err
	}
	email, err := controlplane.NormalizeAccountEmail(request.Email)
	if err != nil {
		return CreateResult{}, err
	}
	proxyMode, err := controlplane.NormalizeProxyMode(request.ProxyMode)
	if err != nil {
		return CreateResult{}, err
	}
	proxyURL, err := optionalProxyURL(request.ProxyURL)
	if err != nil {
		return CreateResult{}, err
	}
	if proxyMode == "custom" && proxyURL == "" {
		return CreateResult{}, fmt.Errorf("%w: custom proxy mode requires a proxy URL", controlplane.ErrInvalidCatalogInput)
	}
	secretName := accountProxySecretPrefix + accountID
	_, found, err := manager.store.ReadSecret(ctx, secretName)
	if err != nil {
		return CreateResult{}, fmt.Errorf("inspect stale account proxy secret: %w", err)
	}
	if found {
		return CreateResult{}, fmt.Errorf("%w: stale proxy secret exists for account %s", controlplane.ErrAccountLifecycleConflict, accountID)
	}
	port, err := manager.allocatePort(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	operation, err := manager.beginOperation(ctx, operationCreate, accountID, "")
	if err != nil {
		return CreateResult{}, err
	}
	defer manager.finalizeOperation(ctx, operation, &returnError)
	fileTransition, err := manager.files.PrepareCreate(operation.ID, accountID)
	if err != nil {
		return CreateResult{}, newCompensatedError(err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseFilesPrepared, fileTransition.BackupPath()); err != nil {
		return CreateResult{}, newCompensatedError(err, rollbackFiles(fileTransition))
	}
	account := controlplane.Account{ID: accountID, Email: email, Port: port, ProxyMode: proxyMode}
	creation, err := manager.store.ApplyAccountCreation(ctx, account)
	if err != nil {
		return CreateResult{}, newCompensatedError(err, rollbackFiles(fileTransition))
	}
	secretPlan := newSecretPlan(manager.store, map[string]secretValue{
		secretName: {value: proxyURL, present: proxyURL != ""},
	})
	if err := secretPlan.Capture(ctx); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, nil, secretPlan, err)
	}
	if err := secretPlan.Apply(ctx); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, nil, secretPlan, err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseControlApplied, fileTransition.BackupPath()); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, nil, secretPlan, err)
	}
	if _, err := manager.projection.Render(ctx); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, nil, secretPlan, fmt.Errorf("render created account: %w", err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseProjectionRendered, fileTransition.BackupPath()); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, nil, secretPlan, err)
	}
	runtimeTransition, err := manager.runtime.PrepareCreate(ctx, creation.Account)
	if err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, nil, secretPlan, fmt.Errorf("%w: %v", ErrRuntimeTransition, err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseRuntimePrepared, fileTransition.BackupPath()); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, runtimeTransition, secretPlan, err)
	}
	snapshot, err := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, runtimeTransition, secretPlan, fmt.Errorf("activate created account snapshot: %w", err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseSnapshotActivated, fileTransition.BackupPath()); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, runtimeTransition, secretPlan, err)
	}
	if err := runtimeTransition.Commit(ctx); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, runtimeTransition, secretPlan, fmt.Errorf("commit created account runtime: %w", err))
	}
	if err := fileTransition.Commit(); err != nil {
		return CreateResult{}, manager.rollbackCreation(ctx, creation, fileTransition, runtimeTransition, secretPlan, fmt.Errorf("commit created account files: %w", err))
	}
	return CreateResult{
		Account: creation.Account, CreatedKeyRows: len(creation.CreatedRows),
		SnapshotGeneration: snapshot.Generation,
	}, nil
}

func (manager *Manager) Update(ctx context.Context, request UpdateRequest) (result UpdateResult, returnError error) {
	manager.lock.Lock()
	defer manager.lock.Unlock()
	if request.PolicyOnly {
		return manager.updatePolicy(ctx, request)
	}
	accountID, err := controlplane.NormalizeAccountID(request.AccountID)
	if err != nil {
		return UpdateResult{}, err
	}
	stored, rows, err := manager.store.ReadAccountLifecycle(ctx, accountID)
	if err != nil {
		return UpdateResult{}, err
	}
	newAccountID := strings.TrimSpace(request.NewAccountID)
	if newAccountID == "" {
		newAccountID = accountID
	}
	newAccountID, err = controlplane.NormalizeAccountID(newAccountID)
	if err != nil {
		return UpdateResult{}, err
	}
	email := strings.TrimSpace(request.Email)
	if email == "" {
		email = stored.Email
	}
	email, err = controlplane.NormalizeAccountEmail(email)
	if err != nil {
		return UpdateResult{}, err
	}
	proxyMode := strings.TrimSpace(request.ProxyMode)
	if proxyMode == "" {
		proxyMode = stored.ProxyMode
	}
	proxyMode, err = controlplane.NormalizeProxyMode(proxyMode)
	if err != nil {
		return UpdateResult{}, err
	}
	secretPlan, desiredProxy, err := manager.updateProxyPlan(ctx, accountID, newAccountID, request.ProxyURL)
	if err != nil {
		return UpdateResult{}, err
	}
	if proxyMode == "custom" && desiredProxy == "" {
		return UpdateResult{}, fmt.Errorf("%w: custom proxy mode requires a proxy URL", controlplane.ErrInvalidCatalogInput)
	}
	if request.AllowUnavailableProxyRepair {
		if err := manager.validateUnavailableProxyRepair(
			ctx, request, stored.Account, newAccountID, email, proxyMode, desiredProxy,
		); err != nil {
			return UpdateResult{}, err
		}
	}
	defaultState := request.Default
	if request.Enabled != nil && !*request.Enabled && stored.DefaultGroup && defaultState == nil {
		value := false
		defaultState = &value
	}
	operation, err := manager.beginOperation(ctx, operationUpdate, accountID, newAccountID)
	if err != nil {
		return UpdateResult{}, err
	}
	defer manager.finalizeOperation(ctx, operation, &returnError)
	var routesTransition *routeTransition
	if request.AllowUnavailableProxyRepair {
		routesTransition = &routeTransition{
			manager: manager, source: accountID,
			assignments: map[string]string{}, expected: map[string]string{},
		}
	} else {
		routesTransition, err = manager.planRouteEvacuation(ctx, accountID, request.FallbackAccount)
		if err != nil {
			return UpdateResult{}, newCompensatedError(err)
		}
	}
	effectiveFallback := routesTransition.fallback
	needsFallback := (request.Enabled != nil && !*request.Enabled) ||
		(stored.DefaultGroup && defaultState != nil && !*defaultState)
	if effectiveFallback == "" && (strings.TrimSpace(request.FallbackAccount) != "" || needsFallback) {
		effectiveFallback, err = manager.selectEligibleFallback(ctx, accountID, request.FallbackAccount)
		if err != nil {
			return UpdateResult{}, newCompensatedError(err)
		}
	}
	operation.EvacuatedRoutes = routesTransition.journalRoutes()
	if err := manager.advanceOperation(ctx, &operation, phaseRoutesPrepared, ""); err != nil {
		return UpdateResult{}, newCompensatedError(err)
	}
	if err := routesTransition.Apply(ctx); err != nil {
		return UpdateResult{}, err
	}
	if err := manager.advanceOperation(ctx, &operation, phaseRoutesEvacuated, ""); err != nil {
		rollbackContext, cancel := lifecycleRollbackContext(ctx)
		defer cancel()
		routeError := routesTransition.restoreTo(rollbackContext, accountID)
		var snapshotError error
		if routeError == nil {
			_, snapshotError = manager.snapshots.PublishAuthSnapshot(rollbackContext, true)
		}
		return UpdateResult{}, newCompensatedError(err, routeError, snapshotError)
	}
	fileTransition, err := manager.files.PrepareUpdate(operation.ID, accountID, newAccountID, BackupData{
		Account: stored.Account, Keys: rows,
	})
	if err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, controlplane.AccountUpdate{}, nil, nil, secretPlan, routesTransition, err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseFilesPrepared, fileTransition.BackupPath()); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, controlplane.AccountUpdate{}, fileTransition, nil, secretPlan, routesTransition, err)
	}
	update, err := manager.store.ApplyAccountUpdate(ctx, controlplane.AccountUpdateRequest{
		AccountID: accountID, NewAccountID: newAccountID, Email: email,
		ProxyMode: proxyMode, GroupEnabled: request.Enabled, DefaultGroup: defaultState,
		FallbackAccount: effectiveFallback,
	})
	if err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, controlplane.AccountUpdate{}, fileTransition, nil, secretPlan, routesTransition, err)
	}
	if err := secretPlan.Apply(ctx); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, nil, secretPlan, routesTransition, err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseControlApplied, fileTransition.BackupPath()); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, nil, secretPlan, routesTransition, err)
	}
	if _, err := manager.projection.Render(ctx); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, nil, secretPlan, routesTransition, fmt.Errorf("render updated account: %w", err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseProjectionRendered, fileTransition.BackupPath()); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, nil, secretPlan, routesTransition, err)
	}
	runtimeTransition, err := manager.runtime.PrepareUpdate(ctx, update.Before.Account, update.After.Account)
	if err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, runtimeTransition, secretPlan, routesTransition, fmt.Errorf("%w: %v", ErrRuntimeTransition, err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseRuntimePrepared, fileTransition.BackupPath()); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, runtimeTransition, secretPlan, routesTransition, err)
	}
	if err := runtimeTransition.Commit(ctx); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, runtimeTransition, secretPlan, routesTransition, fmt.Errorf("commit updated account runtime: %w", err))
	}
	if err := fileTransition.Commit(); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, runtimeTransition, secretPlan, routesTransition, fmt.Errorf("commit updated account files: %w", err))
	}
	if update.After.Account.GroupEnabled {
		if err := routesTransition.restoreTo(ctx, newAccountID); err != nil {
			return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, runtimeTransition, secretPlan, routesTransition, err)
		}
	}
	snapshot, err := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, runtimeTransition, secretPlan, routesTransition, fmt.Errorf("activate updated account snapshot: %w", err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseSnapshotActivated, fileTransition.BackupPath()); err != nil {
		return UpdateResult{}, manager.rollbackUpdate(ctx, update, fileTransition, runtimeTransition, secretPlan, routesTransition, err)
	}
	renamedFrom := ""
	if accountID != newAccountID {
		renamedFrom = accountID
	}
	return UpdateResult{
		Account: update.After.Account, RenamedFrom: renamedFrom,
		ReroutedUsers: len(routesTransition.assignments) + len(update.Routes), Backup: fileTransition.BackupPath(),
		SnapshotGeneration: snapshot.Generation,
	}, nil
}

func (manager *Manager) Delete(ctx context.Context, request DeleteRequest) (result DeleteResult, returnError error) {
	manager.lock.Lock()
	defer manager.lock.Unlock()
	accountID, err := controlplane.NormalizeAccountID(request.AccountID)
	if err != nil {
		return DeleteResult{}, err
	}
	stored, rows, err := manager.store.ReadAccountLifecycle(ctx, accountID)
	if err != nil {
		return DeleteResult{}, err
	}
	accounts, err := manager.store.ReadAccounts(ctx)
	if err != nil {
		return DeleteResult{}, err
	}
	if len(accounts) <= 1 {
		return DeleteResult{}, controlplane.ErrAccountDeleteLast
	}
	effectiveFallback, err := manager.selectEligibleFallback(ctx, accountID, request.FallbackAccount)
	if err != nil {
		return DeleteResult{}, err
	}
	operation, err := manager.beginOperation(ctx, operationDelete, accountID, "")
	if err != nil {
		return DeleteResult{}, err
	}
	defer manager.finalizeOperation(ctx, operation, &returnError)
	fileTransition, err := manager.files.PrepareDelete(operation.ID, accountID, BackupData{
		Account: stored.Account, Keys: rows,
	})
	if err != nil {
		return DeleteResult{}, newCompensatedError(err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseFilesPrepared, fileTransition.BackupPath()); err != nil {
		return DeleteResult{}, newCompensatedError(err, rollbackFiles(fileTransition))
	}
	deletion, err := manager.store.ApplyAccountDeletion(
		ctx, accountID, effectiveFallback, request.RevokeExclusive,
	)
	if err != nil {
		return DeleteResult{}, newCompensatedError(err, rollbackFiles(fileTransition))
	}
	secretPlan := newSecretPlan(manager.store, map[string]secretValue{
		accountProxySecretPrefix + accountID: {},
	})
	if err := secretPlan.Capture(ctx); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, nil, secretPlan, err)
	}
	if err := secretPlan.Apply(ctx); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, nil, secretPlan, err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseControlApplied, fileTransition.BackupPath()); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, nil, secretPlan, err)
	}
	if _, err := manager.projection.Render(ctx); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, nil, secretPlan, fmt.Errorf("render deleted account: %w", err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseProjectionRendered, fileTransition.BackupPath()); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, nil, secretPlan, err)
	}
	runtimeTransition, err := manager.runtime.PrepareDelete(ctx, deletion.Account.Account)
	if err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, nil, secretPlan, fmt.Errorf("%w: %v", ErrRuntimeTransition, err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseRuntimePrepared, fileTransition.BackupPath()); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, runtimeTransition, secretPlan, err)
	}
	snapshot, err := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, runtimeTransition, secretPlan, fmt.Errorf("activate account deletion snapshot: %w", err))
	}
	if err := manager.advanceOperation(ctx, &operation, phaseSnapshotActivated, fileTransition.BackupPath()); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, runtimeTransition, secretPlan, err)
	}
	if len(deletion.Routes) > 0 {
		if manager.drainer == nil {
			return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, runtimeTransition, secretPlan, ErrRouteEvacuationUnavailable)
		}
		if err := manager.drainer.WaitAccountDrained(ctx, accountID); err != nil {
			return DeleteResult{}, manager.rollbackDeletion(
				ctx, deletion, fileTransition, runtimeTransition, secretPlan,
				fmt.Errorf("%w: %v", ErrAccountDrainTimeout, err),
			)
		}
	}
	if err := runtimeTransition.Commit(ctx); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, runtimeTransition, secretPlan, fmt.Errorf("commit deleted account runtime: %w", err))
	}
	if err := fileTransition.Commit(); err != nil {
		return DeleteResult{}, manager.rollbackDeletion(ctx, deletion, fileTransition, runtimeTransition, secretPlan, fmt.Errorf("commit deleted account files: %w", err))
	}
	return DeleteResult{
		AccountID: accountID, RemovedKeyRows: len(deletion.Rows),
		RevokedExclusive: deletion.RevokedExclusiveKeys, ReroutedUsers: len(deletion.Routes),
		ReplacementAccount: deletion.FallbackAccount, Backup: fileTransition.BackupPath(),
		SnapshotGeneration: snapshot.Generation,
	}, nil
}

func (manager *Manager) ClearAuth(ctx context.Context, rawAccountID string) (result AuthClearResult, returnError error) {
	manager.lock.Lock()
	defer manager.lock.Unlock()
	accountID, err := controlplane.NormalizeAccountID(rawAccountID)
	if err != nil {
		return AuthClearResult{}, err
	}
	stored, _, err := manager.store.ReadAccountLifecycle(ctx, accountID)
	if err != nil {
		return AuthClearResult{}, err
	}
	operation, err := manager.beginOperation(ctx, operationClearAuth, accountID, "")
	if err != nil {
		return AuthClearResult{}, err
	}
	defer manager.finalizeOperation(ctx, operation, &returnError)
	routeTransition, err := manager.planRouteEvacuation(ctx, accountID, "")
	if err != nil {
		return AuthClearResult{}, newCompensatedError(err)
	}
	operation.EvacuatedRoutes = routeTransition.journalRoutes()
	if err := manager.advanceOperation(ctx, &operation, phaseRoutesPrepared, ""); err != nil {
		return AuthClearResult{}, newCompensatedError(err)
	}
	if err := routeTransition.Apply(ctx); err != nil {
		return AuthClearResult{}, err
	}
	if err := manager.advanceOperation(ctx, &operation, phaseRoutesEvacuated, ""); err != nil {
		rollbackContext, cancel := lifecycleRollbackContext(ctx)
		defer cancel()
		routeError := routeTransition.restoreTo(rollbackContext, accountID)
		var snapshotError error
		if routeError == nil && !routeTransition.Empty() {
			_, snapshotError = manager.snapshots.PublishAuthSnapshot(rollbackContext, true)
		}
		return AuthClearResult{}, newCompensatedError(err, routeError, snapshotError)
	}
	transition, err := manager.files.PrepareAuthClear(operation.ID, accountID, BackupData{Account: stored.Account})
	if err != nil {
		return AuthClearResult{}, manager.rollbackAuthClear(ctx, nil, routeTransition, accountID, err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseFilesPrepared, transition.BackupPath()); err != nil {
		return AuthClearResult{}, manager.rollbackAuthClear(ctx, transition, routeTransition, accountID, err)
	}
	if err := manager.runtime.RestartAccount(ctx, accountID); err != nil {
		return AuthClearResult{}, manager.rollbackAuthClear(
			ctx, transition, routeTransition, accountID,
			fmt.Errorf("restart account after OAuth clear: %w", err),
		)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseRuntimePrepared, transition.BackupPath()); err != nil {
		return AuthClearResult{}, manager.rollbackAuthClear(ctx, transition, routeTransition, accountID, err)
	}
	if err := routeTransition.restoreTo(ctx, accountID); err != nil {
		return AuthClearResult{}, manager.rollbackAuthClear(ctx, transition, routeTransition, accountID, err)
	}
	if !routeTransition.Empty() {
		if _, err := manager.snapshots.PublishAuthSnapshot(ctx, true); err != nil {
			return AuthClearResult{}, manager.rollbackAuthClear(
				ctx, transition, routeTransition, accountID,
				fmt.Errorf("activate OAuth clear route restoration: %w", err),
			)
		}
	}
	if err := transition.Commit(); err != nil {
		return AuthClearResult{}, manager.rollbackAuthClear(ctx, transition, routeTransition, accountID, err)
	}
	return AuthClearResult{AccountID: accountID, Backup: transition.BackupPath()}, nil
}

func (manager *Manager) allocatePort(ctx context.Context) (int, error) {
	settings, err := manager.store.ReadSettings(ctx)
	if err != nil {
		return 0, fmt.Errorf("read account port settings: %w", err)
	}
	start, err := integerSetting(settings, "accounts.port_start", defaultAccountPortStart)
	if err != nil {
		return 0, err
	}
	end, err := integerSetting(settings, "accounts.port_end", defaultAccountPortEnd)
	if err != nil {
		return 0, err
	}
	if start < 1024 || end > 65535 || end < start {
		return 0, fmt.Errorf("%w: invalid account port range", controlplane.ErrInvalidCatalogInput)
	}
	accounts, err := manager.store.ReadAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("read account ports: %w", err)
	}
	used := map[int]struct{}{8317: {}, 8318: {}}
	for _, account := range accounts {
		used[account.Port] = struct{}{}
	}
	runtimePorts, err := manager.runtime.ReservedHostPorts(ctx)
	if err != nil {
		return 0, fmt.Errorf("inspect Docker host ports: %w", err)
	}
	for port := range runtimePorts {
		used[port] = struct{}{}
	}
	for port := start; port <= end; port++ {
		if _, exists := used[port]; !exists {
			return port, nil
		}
	}
	return 0, ErrNoAccountPort
}

func (manager *Manager) updateProxyPlan(
	ctx context.Context,
	oldAccountID string,
	newAccountID string,
	requested *string,
) (*secretPlan, string, error) {
	oldName := accountProxySecretPrefix + oldAccountID
	newName := accountProxySecretPrefix + newAccountID
	oldValue, oldFound, err := manager.store.ReadSecret(ctx, oldName)
	if err != nil {
		return nil, "", fmt.Errorf("read account proxy secret: %w", err)
	}
	if oldFound {
		oldValue, err = accountprojection.NormalizeProxyURL(oldValue)
		if err != nil {
			return nil, "", fmt.Errorf("stored account proxy URL is invalid: %w", err)
		}
	}
	desired := oldValue
	desiredFound := oldFound
	if requested != nil {
		desired, err = optionalProxyURL(*requested)
		if err != nil {
			return nil, "", err
		}
		desiredFound = desired != ""
	}
	desiredValues := map[string]secretValue{
		newName: {value: desired, present: desiredFound},
	}
	if newAccountID != oldAccountID {
		_, newFound, err := manager.store.ReadSecret(ctx, newName)
		if err != nil {
			return nil, "", fmt.Errorf("inspect renamed account proxy target: %w", err)
		}
		if newFound {
			return nil, "", fmt.Errorf("%w: renamed account proxy target already exists", controlplane.ErrAccountLifecycleConflict)
		}
		desiredValues[oldName] = secretValue{}
	}
	plan := newSecretPlan(manager.store, desiredValues)
	if err := plan.Capture(ctx); err != nil {
		return nil, "", err
	}
	return plan, desired, nil
}

// validateUnavailableProxyRepair guards the only lifecycle path that may
// rebuild a routed account without first moving its users. It exists to break
// the deployment bootstrap deadlock where every account is ineligible because
// its upstream proxy projection is missing and therefore no safe fallback can
// be selected. The operation is deliberately limited to one ineligible
// account, one new custom proxy value, and a runtime/config rebuild. As soon as
// any other account is eligible, callers must use the ordinary evacuation and
// drain path instead.
func (manager *Manager) validateUnavailableProxyRepair(
	ctx context.Context,
	request UpdateRequest,
	stored controlplane.Account,
	newAccountID string,
	email string,
	proxyMode string,
	desiredProxy string,
) error {
	reject := func(reason string) error {
		return fmt.Errorf("%w: %s", ErrUnavailableProxyRepairRejected, reason)
	}
	if request.ProxyURL == nil || proxyMode != "custom" || desiredProxy == "" {
		return reject("a new custom proxy URL is required")
	}
	if newAccountID != stored.ID || email != stored.Email || request.Enabled != nil ||
		request.Default != nil || strings.TrimSpace(request.FallbackAccount) != "" {
		return reject("only the proxy projection may change")
	}
	if manager.states == nil {
		return reject("account state provider is unavailable")
	}
	routes, err := manager.store.ReadRoutes(ctx)
	if err != nil {
		return fmt.Errorf("inspect unavailable proxy repair routes: %w", err)
	}
	routed := false
	for _, accountID := range routes {
		if accountID == stored.ID {
			routed = true
			break
		}
	}
	if !routed {
		return reject("the account has no routed users and can use ordinary maintenance")
	}
	states, err := manager.states.AccountStates(ctx)
	if err != nil {
		return fmt.Errorf("inspect unavailable proxy repair states: %w", err)
	}
	source, found := states[stored.ID]
	if !found || source.Eligible {
		return reject("the source account is not proven ineligible")
	}
	for accountID, state := range states {
		if accountID != stored.ID && state.Eligible && state.Headroom > 0 {
			return reject("an eligible fallback is available")
		}
	}
	return nil
}

func (manager *Manager) rollbackCreation(
	ctx context.Context,
	creation controlplane.AccountCreation,
	files FileTransition,
	runtime RuntimeTransition,
	secrets *secretPlan,
	cause error,
) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	fileError := rollbackFiles(files)
	runtimeError := rollbackRuntime(rollbackContext, runtime)
	secretError := rollbackSecrets(rollbackContext, secrets)
	controlError := manager.store.RestoreAccountCreation(rollbackContext, creation)
	projectionError, snapshotError := manager.restorePublishedState(rollbackContext, controlError)
	return newCompensatedError(cause, runtimeError, fileError, secretError, controlError, projectionError, snapshotError)
}

func (manager *Manager) rollbackUpdate(
	ctx context.Context,
	update controlplane.AccountUpdate,
	files FileTransition,
	runtime RuntimeTransition,
	secrets *secretPlan,
	routes *routeTransition,
	cause error,
) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	fileError := rollbackFiles(files)
	runtimeError := rollbackRuntime(rollbackContext, runtime)
	secretError := rollbackSecrets(rollbackContext, secrets)
	var controlError error
	if update.Before.ID != "" {
		controlError = manager.store.RestoreAccountUpdate(rollbackContext, update)
	}
	var routeError error
	if controlError == nil && routes != nil {
		routeError = routes.restoreTo(rollbackContext, routes.source)
	}
	projectionError, snapshotError := manager.restorePublishedState(rollbackContext, errors.Join(controlError, routeError))
	return newCompensatedError(cause, runtimeError, fileError, secretError, controlError, routeError, projectionError, snapshotError)
}

func (manager *Manager) rollbackDeletion(
	ctx context.Context,
	deletion controlplane.AccountDeletion,
	files FileTransition,
	runtime RuntimeTransition,
	secrets *secretPlan,
	cause error,
) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	fileError := rollbackFiles(files)
	runtimeError := rollbackRuntime(rollbackContext, runtime)
	secretError := rollbackSecrets(rollbackContext, secrets)
	controlError := manager.store.RestoreAccountDeletion(rollbackContext, deletion)
	projectionError, snapshotError := manager.restorePublishedState(rollbackContext, controlError)
	return newCompensatedError(cause, runtimeError, fileError, secretError, controlError, projectionError, snapshotError)
}

func (manager *Manager) rollbackAuthClear(
	ctx context.Context,
	files FileTransition,
	routes *routeTransition,
	accountID string,
	cause error,
) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	fileError := rollbackFiles(files)
	var restartError error
	if files != nil && fileError == nil {
		restartError = manager.runtime.RestartAccount(rollbackContext, accountID)
	}
	var routeError error
	routeTarget := accountID
	if files != nil && (fileError != nil || restartError != nil) && routes != nil {
		// The source CPA is not proven usable. Keep traffic on the already
		// activated fallback until startup recovery can finish restoring OAuth
		// and the runtime; never route fresh Codex requests back to it.
		routeTarget = routes.fallback
	}
	if routes != nil {
		routeError = routes.restoreTo(rollbackContext, routeTarget)
	}
	var snapshotError error
	if routeError == nil && routes != nil && !routes.Empty() {
		_, snapshotError = manager.snapshots.PublishAuthSnapshot(rollbackContext, true)
	}
	return newCompensatedError(cause, fileError, restartError, routeError, snapshotError)
}

func (manager *Manager) restorePublishedState(ctx context.Context, controlError error) (error, error) {
	if controlError != nil {
		return nil, nil
	}
	_, projectionError := manager.projection.Render(ctx)
	if projectionError != nil {
		projectionError = fmt.Errorf("restore account projections: %w", projectionError)
		return projectionError, nil
	}
	_, snapshotError := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if snapshotError != nil {
		snapshotError = fmt.Errorf("restore account auth snapshot: %w", snapshotError)
	}
	return projectionError, snapshotError
}

func lifecycleRollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
}

func rollbackRuntime(ctx context.Context, transition RuntimeTransition) error {
	if transition == nil {
		return nil
	}
	if err := transition.Rollback(ctx); err != nil {
		return fmt.Errorf("rollback account runtime: %w", err)
	}
	return nil
}

func rollbackFiles(transition FileTransition) error {
	if transition == nil {
		return nil
	}
	if err := transition.Rollback(); err != nil {
		return fmt.Errorf("rollback account files: %w", err)
	}
	return nil
}

func rollbackSecrets(ctx context.Context, plan *secretPlan) error {
	if plan == nil {
		return nil
	}
	if err := plan.Rollback(ctx); err != nil {
		return fmt.Errorf("rollback account proxy secrets: %w", err)
	}
	return nil
}

type secretStore interface {
	ReadSecret(context.Context, string) (string, bool, error)
	WriteSecret(context.Context, string, string) error
	DeleteSecret(context.Context, string) error
}

type secretValue struct {
	value   string
	present bool
}

type secretPlan struct {
	store   secretStore
	desired map[string]secretValue
	before  map[string]secretValue
	ready   bool
}

func newSecretPlan(store secretStore, desired map[string]secretValue) *secretPlan {
	return &secretPlan{store: store, desired: desired, before: make(map[string]secretValue)}
}

func (plan *secretPlan) Capture(ctx context.Context) error {
	if plan == nil || plan.store == nil {
		return errors.New("account secret plan is incomplete")
	}
	names := sortedSecretNames(plan.desired)
	for _, name := range names {
		value, found, err := plan.store.ReadSecret(ctx, name)
		if err != nil {
			return fmt.Errorf("capture account secret %s: %w", name, err)
		}
		plan.before[name] = secretValue{value: value, present: found}
	}
	plan.ready = true
	return nil
}

func (plan *secretPlan) Apply(ctx context.Context) error {
	if plan == nil || !plan.ready {
		return errors.New("account secret plan was not captured")
	}
	for _, name := range sortedSecretNames(plan.desired) {
		if err := writeSecretValue(ctx, plan.store, name, plan.desired[name]); err != nil {
			return fmt.Errorf("apply account secret %s: %w", name, err)
		}
	}
	return nil
}

func (plan *secretPlan) Rollback(ctx context.Context) error {
	if plan == nil || !plan.ready {
		return nil
	}
	errorsFound := make([]error, 0)
	for _, name := range sortedSecretNames(plan.before) {
		if err := writeSecretValue(ctx, plan.store, name, plan.before[name]); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("restore account secret %s: %w", name, err))
		}
	}
	return errors.Join(errorsFound...)
}

func writeSecretValue(ctx context.Context, store secretStore, name string, value secretValue) error {
	if !value.present {
		return store.DeleteSecret(ctx, name)
	}
	return store.WriteSecret(ctx, name, value.value)
}

func sortedSecretNames(values map[string]secretValue) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func optionalProxyURL(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	value, err := accountprojection.NormalizeProxyURL(raw)
	if err != nil {
		return "", fmt.Errorf("%w: invalid account proxy URL: %v", controlplane.ErrInvalidCatalogInput, err)
	}
	return value, nil
}

func integerSetting(settings map[string]any, key string, fallback int) (int, error) {
	value, err := optionalIntegerSetting(settings, key)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}

func optionalIntegerSetting(settings map[string]any, key string) (int, error) {
	raw, found := settings[key]
	if !found || raw == nil {
		return 0, nil
	}
	var value int64
	switch typed := raw.(type) {
	case int:
		value = int64(typed)
	case int64:
		value = typed
	case float64:
		value = int64(typed)
		if float64(value) != typed {
			return 0, fmt.Errorf("%w: setting %s must be an integer", controlplane.ErrInvalidCatalogInput, key)
		}
	default:
		return 0, fmt.Errorf("%w: setting %s must be an integer", controlplane.ErrInvalidCatalogInput, key)
	}
	if value < 1 || value > 65535 {
		return 0, fmt.Errorf("%w: setting %s is outside the port range", controlplane.ErrInvalidCatalogInput, key)
	}
	return int(value), nil
}
