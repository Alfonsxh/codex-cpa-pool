package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/cpaplugin"
)

func TestTicketProxySecretSaveMaskPreserveAndRollback(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	ctx := context.Background()
	applier := &recordingConfigurationApplier{}
	server, err := New(Config{Store: store, ConfigurationApplier: applier})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key", "Accept-Language": "zh-CN"}
	const business = "http://business:original@business.example.com:8080"
	const ticket = "socks5h://ticket:private-ticket@ticket.example.com:1080"
	if err := store.WriteSecret(ctx, defaultProxySecretName, business); err != nil {
		t.Fatal(err)
	}
	save := func(values map[string]any, status int) {
		t.Helper()
		r := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{"confirm": "save", "values": values}, headers, nil)
		if r.Code != status || strings.Contains(r.Body.String(), "private-ticket") || strings.Contains(r.Body.String(), "do-not-leak") {
			t.Fatalf("save status=%d body=%s", r.Code, r.Body.String())
		}
	}
	save(map[string]any{cpaplugin.Prefix + "proxy_source": "custom", cpaplugin.Prefix + "enabled": true, cpaplugin.Prefix + "accounts": "alpha", cpaplugin.Prefix + "harvest_enabled": true}, http.StatusBadRequest)
	save(map[string]any{cpaplugin.ProxyURLKey: ticket}, http.StatusOK)
	settings, err := store.ReadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := settings[cpaplugin.ProxyURLKey]; found {
		t.Fatal("Ticket secret stored as plain setting")
	}
	if settings[cpaplugin.Prefix+"proxy_source"] != "custom" {
		t.Fatal("new dedicated source was not persisted")
	}
	r := performAdminRequest(server, http.MethodGet, "/admin/api/settings/configuration", nil, headers, nil)
	if r.Code != 200 || strings.Contains(r.Body.String(), "private-ticket") || strings.Contains(r.Body.String(), "ticket.example.com") {
		t.Fatal("catalog exposes proxy credentials")
	}
	var catalog configurationCatalogResponse
	decodeAdminResponse(t, r, &catalog)
	found := false
	for _, group := range catalog.Groups {
		for _, field := range group.Fields {
			if field.Key == cpaplugin.ProxyURLKey {
				found = true
				if field.Value != "" || field.Configured == nil || !*field.Configured {
					t.Fatal("missing masked configured state")
				}
			}
		}
	}
	if !found {
		t.Fatal("Ticket proxy field absent")
	}
	save(map[string]any{cpaplugin.ProxyURLKey: "", cpaplugin.Prefix + "models": "gpt-5.6-sol"}, http.StatusOK)
	assertSecrets := func() {
		t.Helper()
		for name, want := range map[string]string{defaultProxySecretName: business, cpaplugin.ProxySecretName: ticket} {
			value, found, err := store.ReadSecret(ctx, name)
			if err != nil || !found || value != want {
				t.Fatal("proxy secret was not preserved")
			}
		}
	}
	assertSecrets()
	save(map[string]any{cpaplugin.ProxyURLKey: "https://user:do-not-leak@proxy.example.com/path"}, http.StatusBadRequest)
	applier.errors = []error{errors.New("apply failed"), nil}
	save(map[string]any{"cpa.proxy_url": "http://changed:do-not-leak@business.example.com", cpaplugin.ProxyURLKey: "http://changed:do-not-leak@ticket.example.com"}, http.StatusBadGateway)
	assertSecrets()
	save(map[string]any{cpaplugin.Prefix + "version": cpaplugin.LegacyVersion}, http.StatusBadRequest)
}

func TestTicketLegacyDirectMigratesWithoutChangingInstalledVersion(t *testing.T) {
	server, store := newTestAdmin(t)
	ctx := context.Background()
	old := cpaplugin.Installation{Version: cpaplugin.LegacyVersion, SHA256: "existing", InstalledAt: 1}
	if err := store.WriteRuntimeState(ctx, cpaplugin.StatePrefix+"alpha", old); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSettings(ctx, map[string]any{cpaplugin.Prefix + "version": cpaplugin.LegacyVersion, cpaplugin.Prefix + "proxy_source": "direct", cpaplugin.Prefix + "enabled": true, cpaplugin.Prefix + "accounts": "alpha", cpaplugin.Prefix + "harvest_enabled": true, cpaplugin.Prefix + "inject_enabled": true}); err != nil {
		t.Fatal(err)
	}
	_, effective, _, _, err := server.currentConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if effective[cpaplugin.Prefix+"version"] != cpaplugin.DefaultVersion || effective[cpaplugin.Prefix+"proxy_source"] != "custom" || effective[cpaplugin.Prefix+"harvest_enabled"] != false || effective[cpaplugin.Prefix+"inject_enabled"] != false {
		t.Fatal("unsafe legacy migration")
	}
	var got cpaplugin.Installation
	if _, err := store.ReadRuntimeState(ctx, cpaplugin.StatePrefix+"alpha", &got); err != nil || got != old {
		t.Fatal("installed version was changed")
	}
}

func TestTicketProxyUpdateHotAppliesWithoutAccountRestart(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	projection := &recordingConfigurationProjection{}
	runtime := &recordingConfigurationRuntime{}
	server, err := New(Config{Store: store, ConfigurationApplier: &ConfigurationRuntimeApplier{Projection: projection, Runtime: runtime, ControlRuntime: runtime}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	r := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{"confirm": "save", "values": map[string]any{cpaplugin.ProxyURLKey: "http://proxy.example.com:8080"}}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if r.Code != 200 || projection.calls != 1 || len(runtime.targets) != 0 {
		t.Fatalf("unexpected apply: HTTP %d projections %d restarts %v", r.Code, projection.calls, runtime.targets)
	}
}
