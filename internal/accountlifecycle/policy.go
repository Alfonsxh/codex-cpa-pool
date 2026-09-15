package accountlifecycle

import (
	"context"
	"fmt"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

// updatePolicy runs under the identity lock. Selection policy changes neither
// the upstream configuration nor its container, so admitted requests can finish
// on the old route while the next requests use the new Gateway snapshot.
func (manager *Manager) updatePolicy(ctx context.Context, request UpdateRequest) (result UpdateResult, returnError error) {
	if request.NewAccountID != "" || request.Email != "" || request.ProxyMode != "" ||
		request.ProxyURL != nil || request.AllowUnavailableProxyRepair ||
		(request.Enabled == nil && request.Default == nil) {
		return UpdateResult{}, fmt.Errorf("%w: selection policy cannot change account identity or proxy", controlplane.ErrInvalidCatalogInput)
	}
	accountID, err := controlplane.NormalizeAccountID(request.AccountID)
	if err != nil {
		return UpdateResult{}, err
	}
	stored, _, err := manager.store.ReadAccountLifecycle(ctx, accountID)
	if err != nil {
		return UpdateResult{}, err
	}
	enabled := stored.GroupEnabled
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	defaultState := request.Default
	if !enabled && defaultState == nil {
		value := false
		defaultState = &value
	}
	fallback := ""
	if !enabled || (stored.DefaultGroup && defaultState != nil && !*defaultState) {
		fallback, err = manager.selectEligibleFallback(ctx, accountID, request.FallbackAccount)
		if err != nil {
			return UpdateResult{}, err
		}
	}
	operation, err := manager.beginOperation(ctx, operationPolicy, accountID, "")
	if err != nil {
		return UpdateResult{}, err
	}
	defer manager.finalizeOperation(ctx, operation, &returnError)
	update, err := manager.store.ApplyAccountUpdate(ctx, controlplane.AccountUpdateRequest{
		AccountID: accountID, Email: stored.Email, ProxyMode: stored.ProxyMode,
		GroupEnabled: request.Enabled, DefaultGroup: defaultState, FallbackAccount: fallback,
	})
	if err != nil {
		return UpdateResult{}, newCompensatedError(err)
	}
	if err := manager.advanceOperation(ctx, &operation, phaseControlApplied, ""); err != nil {
		return UpdateResult{}, manager.rollbackPolicy(ctx, update, err)
	}
	if _, err := manager.projection.Render(ctx); err != nil {
		return UpdateResult{}, manager.rollbackPolicy(ctx, update, err)
	}
	snapshot, err := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		return UpdateResult{}, manager.rollbackPolicy(ctx, update, err)
	}
	return UpdateResult{
		Account: update.After.Account, ReroutedUsers: len(update.Routes), SnapshotGeneration: snapshot.Generation,
	}, nil
}

func (manager *Manager) rollbackPolicy(ctx context.Context, update controlplane.AccountUpdate, cause error) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	controlError := manager.store.RestoreAccountUpdate(rollbackContext, update)
	projectionError, snapshotError := manager.restorePublishedState(rollbackContext, controlError)
	return newCompensatedError(cause, controlError, projectionError, snapshotError)
}
