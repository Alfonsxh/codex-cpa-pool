package accountlifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
)

func TestPolicyDisablePreservesInflightRuntimeOAuthConfigAndKeys(t *testing.T) {
	fixture := newManagerFixture(t)
	ctx := context.Background()
	fixture.drainer.err = errors.New("old streams are still running")
	fixture.runtime.prepareError = errors.New("policy must not rebuild a container")
	authPath := filepath.Join(fixture.root, "auth", "alpha", "oauth.json")
	writeTestFile(t, authPath, "oauth-for-policy-test")
	configPath := filepath.Join(fixture.root, "configs", "alpha", "config.yaml")
	configBefore, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configBody := readTestFile(t, configPath)
	keysBefore, err := fixture.store.ReadKeyRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	result, err := fixture.manager.Update(ctx, UpdateRequest{
		AccountID: "alpha", PolicyOnly: true, Enabled: &disabled, FallbackAccount: "beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Account.GroupEnabled || result.Account.DefaultGroup || result.ReroutedUsers != 1 || result.Backup != "" {
		t.Fatalf("disable policy result = %#v", result)
	}
	routes, err := fixture.store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "beta" {
		t.Fatalf("route was not moved: %v", err)
	}
	keysAfter, err := fixture.store.ReadKeyRecords(ctx)
	if err != nil || !reflect.DeepEqual(keysBefore, keysAfter) {
		t.Fatal("policy changed API Key records")
	}
	configAfter, err := os.Stat(configPath)
	if err != nil || !os.SameFile(configBefore, configAfter) || !configBefore.ModTime().Equal(configAfter.ModTime()) || readTestFile(t, configPath) != configBody {
		t.Fatal("policy rewrote the upstream configuration")
	}
	if readTestFile(t, authPath) != "oauth-for-policy-test" {
		t.Fatal("policy changed OAuth")
	}
	assertPolicyRuntimeUntouched(t, fixture)
	if fixture.snapshots.calls != 1 {
		t.Fatalf("snapshot calls = %d", fixture.snapshots.calls)
	}
	assertPolicyJournalAbsent(t, fixture)

	// The hidden legacy fallback may no longer be eligible when re-enabling.
	// Re-enabling preserves all existing routes and requires no spare capacity.
	fixture.manager.states = fakeAccountStates{states: map[string]failover.AccountState{}}
	enabled := true
	result, err = fixture.manager.Update(ctx, UpdateRequest{
		AccountID: "alpha", PolicyOnly: true, Enabled: &enabled, FallbackAccount: "beta",
	})
	if err != nil || !result.Account.GroupEnabled || result.ReroutedUsers != 0 {
		t.Fatalf("enable result=%#v err=%v", result, err)
	}
	routesAfter, err := fixture.store.ReadRoutes(ctx)
	if err != nil || !reflect.DeepEqual(routes, routesAfter) {
		t.Fatal("enabling moved existing routes")
	}
	assertPolicyRuntimeUntouched(t, fixture)
}

func TestPolicyRejectsUnavailableFallbackAndMixedMaintenanceBeforeMutation(t *testing.T) {
	disabled := false
	proxy := "http://proxy.example.com:8080"
	for _, request := range []UpdateRequest{
		{AccountID: "alpha", PolicyOnly: true, Enabled: &disabled, FallbackAccount: "beta"},
		{AccountID: "alpha", PolicyOnly: true, Enabled: &disabled, NewAccountID: "gamma"},
		{AccountID: "alpha", PolicyOnly: true, Enabled: &disabled, ProxyURL: &proxy},
		{AccountID: "alpha", PolicyOnly: true},
	} {
		fixture := newManagerFixture(t)
		fixture.manager.states = fakeAccountStates{states: map[string]failover.AccountState{}}
		before, _, err := fixture.store.ReadAccountLifecycle(context.Background(), "alpha")
		if err != nil {
			t.Fatal(err)
		}
		_, err = fixture.manager.Update(context.Background(), request)
		if !errors.Is(err, controlplane.ErrAccountDeleteNeedsFallback) && !errors.Is(err, controlplane.ErrInvalidCatalogInput) {
			t.Fatalf("unexpected error: %v", err)
		}
		after, _, err := fixture.store.ReadAccountLifecycle(context.Background(), "alpha")
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("rejected policy changed account")
		}
		assertPolicyRuntimeUntouched(t, fixture)
		if fixture.snapshots.calls != 0 {
			t.Fatal("rejected policy published snapshot")
		}
		assertPolicyJournalAbsent(t, fixture)
	}
}

func TestPolicySnapshotFailureRestoresAccountDefaultsRoutesAndProjection(t *testing.T) {
	fixture := newManagerFixture(t)
	ctx := context.Background()
	before, err := fixture.store.ReadAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	routesBefore, err := fixture.store.ReadRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	publicPath := filepath.Join(fixture.root, "state", "public", "accounts.json")
	publicBefore := readTestFile(t, publicPath)
	fixture.snapshots.failures = []error{errors.New("snapshot activation failed"), nil}
	disabled := false
	_, err = fixture.manager.Update(ctx, UpdateRequest{AccountID: "alpha", PolicyOnly: true, Enabled: &disabled, FallbackAccount: "beta"})
	if err == nil {
		t.Fatal("snapshot failure was hidden")
	}
	after, readErr := fixture.store.ReadAccounts(ctx)
	if readErr != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("account/default state not restored")
	}
	routesAfter, readErr := fixture.store.ReadRoutes(ctx)
	if readErr != nil || !reflect.DeepEqual(routesBefore, routesAfter) {
		t.Fatal("routes not restored")
	}
	if readTestFile(t, publicPath) != publicBefore || fixture.snapshots.calls != 2 {
		t.Fatal("published state not restored")
	}
	assertPolicyRuntimeUntouched(t, fixture)
	assertPolicyJournalAbsent(t, fixture)
}

func TestPolicyRecoveryPublishesCommittedStateEvenWhenJournalLags(t *testing.T) {
	for _, phase := range []string{phaseAccepted, phaseControlApplied} {
		t.Run(phase, func(t *testing.T) {
			fixture := newManagerFixture(t)
			ctx := context.Background()
			operation, err := fixture.manager.beginOperation(ctx, operationPolicy, "alpha", "")
			if err != nil {
				t.Fatal(err)
			}
			stored, _, err := fixture.store.ReadAccountLifecycle(ctx, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			disabled := false
			_, err = fixture.store.ApplyAccountUpdate(ctx, controlplane.AccountUpdateRequest{
				AccountID: "alpha", Email: stored.Email, ProxyMode: stored.ProxyMode,
				GroupEnabled: &disabled, DefaultGroup: &disabled, FallbackAccount: "beta",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.manager.advanceOperation(ctx, &operation, phase, ""); err != nil {
				t.Fatal(err)
			}
			fixture.snapshots.failures = []error{errors.New("Gateway still unavailable"), nil}
			if err := fixture.manager.Recover(ctx); err == nil {
				t.Fatal("recovery discarded snapshot failure")
			}
			var retained Operation
			if found, err := fixture.store.ReadRuntimeState(ctx, lifecycleJournalStateName, &retained); err != nil || !found {
				t.Fatal("failed recovery lost journal")
			}
			if err := fixture.manager.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			after, _, err := fixture.store.ReadAccountLifecycle(ctx, "alpha")
			if err != nil || after.GroupEnabled {
				t.Fatal("recovery reverted committed disable")
			}
			routes, err := fixture.store.ReadRoutes(ctx)
			if err != nil || routes["alice@example.com"] != "beta" {
				t.Fatal("recovery reverted committed routes")
			}
			assertPolicyRuntimeUntouched(t, fixture)
			assertPolicyJournalAbsent(t, fixture)
		})
	}
}

func assertPolicyRuntimeUntouched(t *testing.T, fixture managerFixture) {
	t.Helper()
	if fixture.drainer.calls != 0 || fixture.runtime.lastUpdateBefore.ID != "" || fixture.runtime.restartCalls != 0 || fixture.runtime.reconcileCalls != 0 {
		t.Fatal("selection policy drained or rebuilt the upstream runtime")
	}
}

func assertPolicyJournalAbsent(t *testing.T, fixture managerFixture) {
	t.Helper()
	var operation Operation
	if found, err := fixture.store.ReadRuntimeState(context.Background(), lifecycleJournalStateName, &operation); err != nil || found {
		t.Fatalf("policy journal remains: found=%v err=%v", found, err)
	}
}
