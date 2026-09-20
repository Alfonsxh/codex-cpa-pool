package cpaplugin

import "testing"

func TestConfigurationPreservesNativeStateAndAccountIsolation(t *testing.T) {
	c, err := Parse(map[string]any{Prefix + "enabled": true, Prefix + "accounts": "alpha", Prefix + "harvest_enabled": true, Prefix + "inject_enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		id                     string
		group, staging, active bool
	}{{"alpha", true, false, true}, {"beta", true, false, false}, {"alpha", false, false, false}, {"alpha", true, true, false}} {
		y := c.YAML(tt.id, tt.group, Installation{Version: "v0.2.0", Staging: tt.staging})
		p := y["configs"].(map[string]any)["codex-ticket"].(map[string]any)
		if p["inject_enabled"] != tt.active || p["harvest_enabled"] != tt.active || p["replace_existing"] != false || p["max_concurrency"] != 1 {
			t.Fatalf("unsafe config %#v", p)
		}
	}
	if c.YAML("alpha", true, Installation{}) != nil {
		t.Fatal("uninstalled plugin was enabled")
	}
}
func TestInvalidPluginConfiguration(t *testing.T) {
	for _, values := range []map[string]any{{Prefix + "version": "../../payload"}, {Prefix + "accounts": "alpha,alpha"}, {Prefix + "models": ""}, {Prefix + "ttl_seconds": 600, Prefix + "refresh_before_seconds": 600}, {Prefix + "retry_base_seconds": 700, Prefix + "retry_max_seconds": 600}, {Prefix + "accounts": "alpha/../beta"}} {
		if _, err := Parse(values); err == nil {
			t.Fatalf("accepted %#v", values)
		}
	}
}
