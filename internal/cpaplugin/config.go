// Package cpaplugin owns the cluster's allowlisted plugin configuration, not
// the upstream plugin implementation or its opaque turn-state cache.
package cpaplugin

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	Repository      = "Su-cyber-art/cpa-plugin-codex-ticket"
	StatePrefix     = "codex_ticket:"
	DefaultVersion  = "v0.2.0"
	LegacyVersion   = "v0.2.0-ccpa.1"
	Prefix          = "plugins.codex_ticket."
	ProxyURLKey     = Prefix + "proxy_url"
	ProxySecretName = "codex_ticket_proxy_url"
)

var VersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)
var identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type Installation struct {
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	InstalledAt int64  `json:"installed_at"`
	// Staging denies harvest/injection during installation and after a crash.
	Staging bool `json:"staging"`
}

type Config struct {
	ProxySource                                                    string
	Enabled                                                        bool
	Accounts                                                       []string
	Version                                                        string
	Harvest                                                        bool
	Inject                                                         bool
	Models                                                         string
	TTL, RefreshBefore, ScanInterval, Timeout, RetryBase, RetryMax int
}

func Parse(values map[string]any) (Config, error) {
	copyValues := make(map[string]any, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	MigrateSettings(copyValues)
	values = copyValues
	c := Config{ProxySource: "custom", Version: DefaultVersion, Models: "gpt-6-astra", TTL: 3600, RefreshBefore: 600, ScanInterval: 60, Timeout: 25, RetryBase: 300, RetryMax: 3600}
	for key, ptr := range map[string]*bool{"enabled": &c.Enabled, "harvest_enabled": &c.Harvest, "inject_enabled": &c.Inject} {
		if v, ok := values[Prefix+key]; ok {
			b, valid := v.(bool)
			if !valid {
				return c, fmt.Errorf("invalid %s%s", Prefix, key)
			}
			*ptr = b
		}
	}
	for key, ptr := range map[string]*string{"proxy_source": &c.ProxySource, "version": &c.Version, "models": &c.Models} {
		if v, ok := values[Prefix+key]; ok {
			s, valid := v.(string)
			if !valid {
				return c, fmt.Errorf("invalid %s%s", Prefix, key)
			}
			*ptr = strings.TrimSpace(s)
		}
	}
	if c.ProxySource != "account" && c.ProxySource != "custom" {
		return c, fmt.Errorf("invalid plugin proxy source")
	}
	if !VersionPattern.MatchString(c.Version) {
		return c, fmt.Errorf("invalid plugin version")
	}
	c.Version = "v" + strings.TrimPrefix(c.Version, "v")
	if value, ok := values[Prefix+"accounts"]; ok {
		s, valid := value.(string)
		if !valid {
			return c, fmt.Errorf("invalid plugin accounts")
		}
		c.Accounts = split(s)
	}
	for _, list := range [][]string{c.Accounts, split(c.Models)} {
		seen := map[string]bool{}
		if len(list) > 32 {
			return c, fmt.Errorf("too many plugin accounts or models")
		}
		for _, id := range list {
			if !identifierPattern.MatchString(id) || seen[id] {
				return c, fmt.Errorf("invalid or duplicate plugin account/model")
			}
			seen[id] = true
		}
	}
	if len(split(c.Models)) == 0 {
		return c, fmt.Errorf("plugin models are required")
	}
	for key, ptr := range map[string]*int{"ttl_seconds": &c.TTL, "refresh_before_seconds": &c.RefreshBefore, "scan_interval_seconds": &c.ScanInterval, "timeout_seconds": &c.Timeout, "retry_base_seconds": &c.RetryBase, "retry_max_seconds": &c.RetryMax} {
		if v, ok := values[Prefix+key]; ok {
			b, err := json.Marshal(v)
			if err != nil || json.Unmarshal(b, ptr) != nil {
				return c, fmt.Errorf("invalid %s%s", Prefix, key)
			}
		}
	}
	if c.TTL < 300 || c.TTL > 86400 || c.RefreshBefore < 0 || c.RefreshBefore >= c.TTL || c.ScanInterval < 30 || c.ScanInterval > 3600 || c.Timeout < 1 || c.Timeout > 60 || c.RetryBase < 60 || c.RetryMax < c.RetryBase || c.RetryMax > 86400 {
		return c, fmt.Errorf("invalid plugin timing; refresh must be below TTL and retry maximum at least retry base")
	}
	return c, nil
}

func split(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
func (c Config) Selected(id string) bool {
	for _, candidate := range c.Accounts {
		if candidate == id {
			return true
		}
	}
	return false
}

// YAML returns only cluster-owned paths and safety settings. Existing native
// turn state is never replaced; probes are serialized within each account CPA.
func (c Config) YAML(id string, groupEnabled bool, installed Installation) map[string]any {
	if !VersionPattern.MatchString(installed.Version) && installed.Version != LegacyVersion {
		return nil
	}
	enabled := c.Enabled && c.Selected(id) && groupEnabled
	return map[string]any{"enabled": enabled, "dir": "/CLIProxyAPI/account-config/plugins/" + installed.Version, "configs": map[string]any{"codex-ticket": map[string]any{
		"enabled": enabled, "priority": 10, "harvest_enabled": enabled && c.Harvest && !installed.Staging, "inject_enabled": enabled && c.Inject && !installed.Staging,
		"proxy_file": "/CLIProxyAPI/account-config/codex-ticket-proxy.url", "host_config_file": "/CLIProxyAPI/account-config/config.yaml",
		"models": c.Models, "target_length": 292, "ttl_seconds": c.TTL, "refresh_before_seconds": c.RefreshBefore, "scan_interval_seconds": c.ScanInterval,
		"timeout_seconds": c.Timeout, "max_concurrency": 1, "retry_base_seconds": c.RetryBase, "retry_max_seconds": c.RetryMax, "replace_existing": false,
	}}}
}

// Preserve installed versions separately; retired direct settings never silently
// select a new network exit or start harvesting without operator configuration.
func MigrateSettings(values map[string]any) {
	if _, found := values[Prefix+"proxy_source"]; !found {
		for key := range values {
			if strings.HasPrefix(key, Prefix) {
				values[Prefix+"proxy_source"] = "account"
				break
			}
		}
	}
	if version, _ := values[Prefix+"version"].(string); "v"+strings.TrimPrefix(version, "v") == LegacyVersion {
		values[Prefix+"version"] = DefaultVersion
	}
	if values[Prefix+"proxy_source"] == "direct" {
		values[Prefix+"proxy_source"] = "custom"
		values[Prefix+"harvest_enabled"] = false
		values[Prefix+"inject_enabled"] = false
	}
}

// NormalizeProxyURL matches the upstream plugin's accepted proxy schemes and
// reports only a fixed error, never credentials from a malformed URL.
func NormalizeProxyURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	invalid := fmt.Errorf("invalid Ticket proxy URL")
	if value == "" || len(value) > 4096 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", invalid
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", invalid
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", invalid
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", invalid
		}
	}
	u.Path = ""
	return u.String(), nil
}
