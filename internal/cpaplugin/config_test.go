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

func TestUpstreamDefaultsAndProxyValidation(t *testing.T) {
	c, err := Parse(nil)
	if err != nil || c.Version != DefaultVersion || c.ProxySource != "custom" || c.Enabled || c.Harvest || c.Inject {
		t.Fatal("unsafe upstream defaults")
	}
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		if _, err := NormalizeProxyURL(scheme + "://user:pass@proxy.example.com:1080"); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"", "direct", "socks5://", "http://proxy.example.com:0", "http://proxy.example.com?", "http://user:secret@proxy.example.com/path", "file:///tmp/proxy"} {
		if _, err := NormalizeProxyURL(raw); err == nil {
			t.Fatalf("invalid URL accepted: %q", raw)
		}
	}
}
