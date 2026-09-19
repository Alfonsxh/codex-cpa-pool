package admin

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
)

type reasoningSnapshotFunc func(context.Context, bool) (failover.Snapshot, error)

func (f reasoningSnapshotFunc) PublishAuthSnapshot(ctx context.Context, wait bool) (failover.Snapshot, error) {
	return f(ctx, wait)
}

func TestReasoningCeilingConfigurationPersistsPublishesAndRollsBack(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	projection, runtime := &recordingConfigurationProjection{}, &recordingConfigurationRuntime{}
	var published []string
	failNext := false
	applier := &ConfigurationRuntimeApplier{Projection: projection, Runtime: runtime, ControlRuntime: runtime,
		GatewaySnapshots: reasoningSnapshotFunc(func(ctx context.Context, wait bool) (failover.Snapshot, error) {
			if !wait {
				t.Error("save returned without waiting for gateway activation")
			}
			settings, err := store.ReadSettings(ctx)
			if err != nil {
				return failover.Snapshot{}, err
			}
			limit, err := reasoningpolicy.FromSettings(settings)
			if err != nil {
				return failover.Snapshot{}, err
			}
			published = append(published, limit)
			if failNext {
				failNext = false
				return failover.Snapshot{}, errors.New("gateway activation failed")
			}
			return failover.Snapshot{Generation: "activated"}, nil
		}),
	}
	server, err := New(Config{Store: store, ConfigurationApplier: applier})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	save := func(value any, want int) {
		t.Helper()
		response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
			"confirm": "save", "values": map[string]any{reasoningpolicy.SettingKey: value},
		}, headers, nil)
		if response.Code != want {
			t.Fatalf("save %v: %d %s", value, response.Code, response.Body.String())
		}
	}
	for _, locale := range []string{"en", "zh-CN"} {
		headers["Accept-Language"] = locale
		response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/configuration", nil, headers, nil)
		var catalog configurationCatalogResponse
		decodeAdminResponse(t, response, &catalog)
		found := false
		for _, group := range catalog.Groups {
			for _, field := range group.Fields {
				if field.Key != reasoningpolicy.SettingKey {
					continue
				}
				found = true
				if field.Default != "unlimited" || field.Value != "unlimited" || len(field.Choices) != 9 || field.Description == "" {
					t.Fatalf("ceiling catalog = %#v", field)
				}
			}
		}
		if !found {
			t.Fatal("ceiling absent from catalog")
		}
	}
	save("xhigh", http.StatusOK)
	save("xhigh", http.StatusOK) // no-op must not republish
	save("arbitrary", http.StatusBadRequest)
	save(5, http.StatusBadRequest)
	failNext = true
	save("high", http.StatusBadGateway)
	settings, err := store.ReadSettings(context.Background())
	if err != nil || settings[reasoningpolicy.SettingKey] != "xhigh" {
		t.Fatalf("rollback = %v, %v", settings[reasoningpolicy.SettingKey], err)
	}
	save("unlimited", http.StatusOK)
	if !reflect.DeepEqual(published, []string{"xhigh", "high", "xhigh", ""}) {
		t.Fatalf("publications = %v", published)
	}
	if projection.calls != 0 || len(runtime.targets) != 0 {
		t.Fatal("gateway policy restarted or reprojected accounts")
	}
}

func TestReasoningCeilingRequiresPublisherBeforeSideEffects(t *testing.T) {
	projection := &recordingConfigurationProjection{}
	applier := &ConfigurationRuntimeApplier{Projection: projection}
	change := ConfigurationChange{Changed: []string{reasoningpolicy.SettingKey, "cpa.passthrough_headers"}, Modes: []string{"live"}}
	if err := applier.ApplyConfiguration(context.Background(), change); err == nil || projection.calls != 0 {
		t.Fatal("missing publisher must fail before projection")
	}
	server, store := newTestAdmin(t)
	response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/configuration", map[string]any{
		"confirm": "save", "values": map[string]any{reasoningpolicy.SettingKey: "xhigh"},
	}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("missing publisher accepted: %d", response.Code)
	}
	settings, err := store.ReadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if limit, err := reasoningpolicy.FromSettings(settings); err != nil || limit != "" {
		t.Fatalf("unapplied value persisted: %q %v", limit, err)
	}
}
