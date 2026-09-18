package accountprojection

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"gopkg.in/yaml.v3"
)

func TestRendererBuildsCompatibleAtomicAccountProjectionsWithoutChangingExternalKeys(t *testing.T) {
	root := t.TempDir()
	store := newProjectionStore(t, root)
	ctx := context.Background()
	before, err := store.ReadKeyRecords(ctx)
	if err != nil {
		t.Fatalf("ReadKeyRecords before render: %v", err)
	}
	result, err := (&Renderer{Root: root, Store: store}).Render(ctx)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Accounts != 2 || result.Users != 2 || len(result.Paths) != 7 {
		t.Fatalf("result = %#v", result)
	}
	after, err := store.ReadKeyRecords(ctx)
	if err != nil {
		t.Fatalf("ReadKeyRecords after render: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("Key rows changed from %d to %d", len(before), len(after))
	}
	for index := range before {
		if before[index].Key != after[index].Key || before[index].Label != after[index].Label {
			t.Fatalf("external Key row changed: before=%#v after=%#v", before[index], after[index])
		}
	}

	alpha := readProjectionFile(t, root, "configs/alpha/config.yaml")
	beta := readProjectionFile(t, root, "configs/beta/config.yaml")
	for _, external := range []string{"cpa_external_alice", "cpa_external_bob"} {
		if strings.Contains(alpha, external) || strings.Contains(beta, external) {
			t.Fatalf("business CPA config leaked external Key %q", external)
		}
	}
	if !strings.Contains(alpha, "proxy-url: direct") ||
		!strings.Contains(beta, "proxy-url: socks5://proxy-user:proxy-pass@127.0.0.1:1080") {
		t.Fatalf("proxy resolution mismatch:\nalpha=%s\nbeta=%s", alpha, beta)
	}
	if !strings.Contains(alpha, "management-key-for-tests") ||
		!strings.Contains(alpha, "cpa_internal_") {
		t.Fatalf("CPA config misses management/internal credentials: %s", alpha)
	}

	keyMap := readProjectionFile(t, root, "state/gateway/key.map")
	if !strings.Contains(keyMap, "Bearer cpa_external_alice") ||
		!strings.Contains(keyMap, "alice@example.com:alpha") ||
		strings.Contains(keyMap, "bob@example.com:alpha") {
		t.Fatalf("Gateway compatibility Key map mismatch: %s", keyMap)
	}
	assertMode(t, filepath.Join(root, "state/gateway/key.map"), 0o600)

	public := readProjectionFile(t, root, "state/public/accounts.json")
	if strings.Contains(public, "@accounts.example.com") || strings.Contains(public, "cpa_") ||
		!strings.Contains(public, `"group_name": "alpha"`) {
		t.Fatalf("public account projection leaked private fields: %s", public)
	}
	assertMode(t, filepath.Join(root, "state/public/accounts.json"), 0o644)

	composeRaw := readProjectionFile(t, root, "compose.accounts.yml")
	var compose map[string]any
	if err := yaml.Unmarshal([]byte(composeRaw), &compose); err != nil {
		t.Fatalf("decode generated Compose: %v", err)
	}
	services, ok := compose["services"].(map[string]any)
	if !ok || len(services) != 2 || services["cliproxy-alpha"] == nil || services["cliproxy-beta"] == nil {
		t.Fatalf("generated Compose services = %#v", compose["services"])
	}
	if !strings.Contains(composeRaw, "${BUSINESS_CPA_LISTEN_ADDRESS:?state/compose.env missing}:18318:8317") {
		t.Fatalf("generated Compose misses bounded host port: %s", composeRaw)
	}
	if !strings.Contains(composeRaw, "./configs/alpha:/CLIProxyAPI/account-config:ro") ||
		!strings.Contains(composeRaw, `command: ["./CLIProxyAPI", "-config", "/CLIProxyAPI/account-config/config.yaml"]`) {
		t.Fatalf("generated Compose misses atomic directory config mount: %s", composeRaw)
	}

	first := make(map[string]string, len(result.Paths))
	for _, relative := range result.Paths {
		first[relative] = readProjectionFile(t, root, relative)
	}
	second, err := (&Renderer{Root: root, Store: store}).Render(ctx)
	if err != nil {
		t.Fatalf("second Render: %v", err)
	}
	for _, relative := range second.Paths {
		if got := readProjectionFile(t, root, relative); got != first[relative] {
			t.Fatalf("projection %s is not deterministic", relative)
		}
	}
}

func TestRendererKeepsLegacyFileForRuntimeMigrationAndRejectsUnsafeConfigDirectory(t *testing.T) {
	root := t.TempDir()
	store := newProjectionStore(t, root)
	legacy := filepath.Join(root, "configs", "alpha.yaml")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o750); err != nil {
		t.Fatalf("create legacy config root: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("legacy\n"), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}
	if _, err := (&Renderer{Root: root, Store: store}).Render(context.Background()); err != nil {
		t.Fatalf("render with legacy config: %v", err)
	}
	if got := readProjectionFile(t, root, "configs/alpha.yaml"); got != "legacy\n" {
		t.Fatalf("legacy config changed before runtime migration: %q", got)
	}
	if got := readProjectionFile(t, root, "configs/alpha/config.yaml"); !strings.Contains(got, "api-keys:") {
		t.Fatalf("directory config was not rendered: %s", got)
	}

	unsafeRoot := t.TempDir()
	unsafeStore := newProjectionStore(t, unsafeRoot)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unsafeRoot, "configs"), 0o750); err != nil {
		t.Fatalf("create unsafe config root: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(unsafeRoot, "configs", "alpha")); err != nil {
		t.Fatalf("create unsafe config symlink: %v", err)
	}
	if _, err := (&Renderer{Root: unsafeRoot, Store: unsafeStore}).Render(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "expected a real directory") {
		t.Fatalf("unsafe account config directory error = %v", err)
	}
}

func TestRendererPersistsCPAContainerSettings(t *testing.T) {
	root := t.TempDir()
	store := newProjectionStore(t, root)
	ctx := context.Background()
	accounts, err := store.ReadAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts[0].GroupEnabled = true
	if err := store.WriteAccounts(ctx, accounts); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSettings(ctx, map[string]any{
		"cpa.debug": true, "cpa.logging_to_file": false, "cpa.usage_statistics_enabled": false,
		"cpa.passthrough_headers": true, "cpa.session_affinity": false, "cpa.session_affinity_ttl": "30m",
	}); err != nil {
		t.Fatal(err)
	}
	renderer := &Renderer{Root: root, Store: store}
	for i := 0; i < 2; i++ {
		if _, err := renderer.Render(ctx); err != nil {
			t.Fatal(err)
		}
		var config cpaConfig
		if err := yaml.Unmarshal([]byte(readProjectionFile(t, root, "configs/"+accounts[0].ID+"/config.yaml")), &config); err != nil {
			t.Fatal(err)
		}
		if !config.Debug || config.LoggingToFile || config.UsageStatisticsEnabled || !config.PassthroughHeaders || config.Routing.SessionAffinity || config.Routing.AffinityTTL != "30m" {
			t.Fatal("persisted CPA settings did not reach the rendered container configuration")
		}
	}
}

func TestRendererPreservesHeaderSettingAcrossRefreshWithoutEnablingDisabledAccounts(t *testing.T) {
	root := t.TempDir()
	store := newProjectionStore(t, root)
	ctx := context.Background()
	accounts, err := store.ReadAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts[0].GroupEnabled, accounts[1].GroupEnabled = true, false
	if err := store.WriteAccounts(ctx, accounts); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSettings(ctx, map[string]any{"cpa.passthrough_headers": true}); err != nil {
		t.Fatal(err)
	}
	renderer := &Renderer{Root: root, Store: store}
	for i := 0; i < 2; i++ {
		if _, err := renderer.Render(ctx); err != nil {
			t.Fatal(err)
		}
		for _, account := range accounts {
			var config cpaConfig
			if err := yaml.Unmarshal([]byte(readProjectionFile(t, root, "configs/"+account.ID+"/config.yaml")), &config); err != nil {
				t.Fatal(err)
			}
			if config.PassthroughHeaders != account.GroupEnabled {
				t.Fatalf("account %s header setting = %v", account.ID, config.PassthroughHeaders)
			}
		}
	}
	after, err := store.ReadAccounts(ctx)
	if err != nil || after[1].GroupEnabled {
		t.Fatalf("disabled account was changed: %v", err)
	}
}

func TestRendererFailsBeforeReplacingOutputsWhenCustomProxyIsMissing(t *testing.T) {
	root := t.TempDir()
	store := newProjectionStore(t, root)
	ctx := context.Background()
	accounts, err := store.ReadAccounts(ctx)
	if err != nil {
		t.Fatalf("ReadAccounts: %v", err)
	}
	accounts[1].ProxyMode = "custom"
	if err := store.WriteAccounts(ctx, accounts); err != nil {
		t.Fatalf("WriteAccounts: %v", err)
	}
	if err := store.DeleteSecret(ctx, accountProxySecretPrefix+"beta"); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	sentinelPath := filepath.Join(root, "compose.accounts.yml")
	if err := os.WriteFile(sentinelPath, []byte("sentinel\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if _, err := (&Renderer{Root: root, Store: store}).Render(ctx); err == nil {
		t.Fatal("Render unexpectedly accepted missing custom proxy")
	}
	if got := readProjectionFile(t, root, "compose.accounts.yml"); got != "sentinel\n" {
		t.Fatalf("invalid render replaced prior output: %q", got)
	}
}

func TestRendererRejectsRouteWithoutUnifiedKeyTarget(t *testing.T) {
	root := t.TempDir()
	store := newProjectionStore(t, root)
	ctx := context.Background()
	records, err := store.ReadKeyRecords(ctx)
	if err != nil {
		t.Fatalf("ReadKeyRecords: %v", err)
	}
	filtered := records[:0]
	for _, record := range records {
		if record.User == "alice@example.com" && record.Account == "alpha" {
			continue
		}
		filtered = append(filtered, record)
	}
	if err := store.WriteKeyRecords(ctx, filtered); err != nil {
		t.Fatalf("WriteKeyRecords: %v", err)
	}
	if _, err := (&Renderer{Root: root, Store: store}).Render(ctx); err == nil ||
		!strings.Contains(err.Error(), "has no unified Key row") {
		t.Fatalf("route/Key mismatch error = %v", err)
	}
}

func newProjectionStore(t *testing.T, root string) *controlplane.Store {
	t.Helper()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("open projection store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, ProxyMode: "direct", CreatedAt: 10, GroupEnabled: true, DefaultGroup: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, ProxyMode: "custom", CreatedAt: 20, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []controlplane.KeyRecord{
		{Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "alice@example.com", Status: "active", Key: "cpa_external_alice", CreatedAt: 100, UpdatedAt: 100},
		{Label: "alice@example.com:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "alice@example.com", Status: "active", Key: "cpa_external_alice", CreatedAt: 100, UpdatedAt: 100},
		{Label: "bob@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "bob@example.com", Status: "active", Key: "cpa_external_bob", CreatedAt: 100, UpdatedAt: 100},
		{Label: "bob@example.com:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "bob@example.com", Status: "active", Key: "cpa_external_bob", CreatedAt: 100, UpdatedAt: 100},
	}); err != nil {
		t.Fatalf("seed Key records: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{
		"alice@example.com": "alpha", "bob@example.com": "beta",
	}); err != nil {
		t.Fatalf("seed routes: %v", err)
	}
	if err := store.WriteSecret(ctx, managementKeySecretName, "management-key-for-tests"); err != nil {
		t.Fatalf("seed management key: %v", err)
	}
	if err := store.WriteSecret(ctx, accountProxySecretPrefix+"beta", "socks5://proxy-user:proxy-pass@127.0.0.1:1080"); err != nil {
		t.Fatalf("seed custom proxy: %v", err)
	}
	return store
}

func readProjectionFile(t *testing.T, root, relative string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read projection %s: %v", relative, err)
	}
	return string(payload)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}
