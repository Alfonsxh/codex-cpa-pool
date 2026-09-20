package accountprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountconfig"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/cpaplugin"
	"github.com/google/renameio/v2"
	"gopkg.in/yaml.v3"
)

const (
	defaultProxySecretName    = "cpa_default_proxy_url"
	accountProxySecretPrefix  = "cpa_account_proxy_url:"
	managementKeySecretName   = "cpa_management_key"
	defaultSnapshotGroupID    = 65534
	defaultRequestRetry       = 2
	defaultMaxRetryCredential = 1
	defaultMaxRetryInterval   = 12
	defaultCooldownSeconds    = 10
	defaultLogsSizeMiB        = 64
	defaultErrorLogFiles      = 10
	defaultQueueRetention     = 3600
)

var ErrInvalidProjection = errors.New("invalid account projection")

// Store is the bounded control-plane contract required to build every
// account-derived runtime projection. The renderer deliberately does not own
// account mutations, Docker operations, or Gateway activation.
type Store interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
	ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error)
	ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error)
	EnsureInternalKeys(context.Context, []string) (map[string]controlplane.InternalKey, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadSecret(context.Context, string) (string, bool, error)
}

type Renderer struct {
	Root  string
	Store Store
	mu    sync.Mutex
}

type Result struct {
	Accounts int      `json:"accounts"`
	Users    int      `json:"users"`
	Paths    []string `json:"paths"`
}

type renderedFile struct {
	relative      string
	payload       []byte
	mode          os.FileMode
	accountConfig bool
}

// Render validates all outputs first. Existing CPA configs retain their inode
// because the upstream watches the file itself; other projections are replaced
// atomically. Lifecycle orchestration compensates later activation failures.
func (renderer *Renderer) Render(ctx context.Context) (Result, error) {
	if renderer == nil || renderer.Store == nil {
		return Result{}, errors.New("account projection renderer requires a control-plane store")
	}
	renderer.mu.Lock()
	defer renderer.mu.Unlock()
	root, err := renderer.root()
	if err != nil {
		return Result{}, err
	}
	accounts, err := renderer.Store.ReadAccounts(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read projection accounts: %w", err)
	}
	if err := validateAccounts(accounts); err != nil {
		return Result{}, err
	}
	routes, err := renderer.Store.ReadRoutes(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read projection routes: %w", err)
	}
	records, err := renderer.Store.ReadKeyRecords(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read projection Key records: %w", err)
	}
	activeUsers := activeUsers(records)
	if _, err := renderer.Store.EnsureInternalKeys(ctx, activeUsers); err != nil {
		return Result{}, fmt.Errorf("synchronize projection internal keys: %w", err)
	}
	internalKeys, err := renderer.Store.ReadInternalKeys(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read projection internal keys: %w", err)
	}
	settings, err := renderer.Store.ReadSettings(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read projection settings: %w", err)
	}
	managementKey, found, err := renderer.Store.ReadSecret(ctx, managementKeySecretName)
	if err != nil {
		return Result{}, fmt.Errorf("read projection management key: %w", err)
	}
	if !found || !validManagementKey(managementKey) {
		return Result{}, fmt.Errorf("%w: missing or invalid CPA management key", ErrInvalidProjection)
	}

	files, err := renderer.buildFiles(ctx, accounts, routes, records, internalKeys, settings, managementKey)
	if err != nil {
		return Result{}, err
	}
	for _, file := range files {
		if err := validateRenderedFile(file); err != nil {
			return Result{}, err
		}
	}
	if err := prepareAccountConfigDirectories(root, accounts); err != nil {
		return Result{}, err
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.relative))
		write := writeAtomic
		if file.accountConfig {
			write = writeAccountConfig
		}
		if err := write(path, file.payload, file.mode); err != nil {
			return Result{}, fmt.Errorf("write account projection %s: %w", file.relative, err)
		}
		paths = append(paths, file.relative)
	}
	sort.Strings(paths)
	return Result{Accounts: len(accounts), Users: len(activeUsers), Paths: paths}, nil
}

func (renderer *Renderer) buildFiles(
	ctx context.Context,
	accounts []controlplane.Account,
	routes map[string]string,
	records []controlplane.KeyRecord,
	internalKeys map[string]controlplane.InternalKey,
	settings map[string]any,
	managementKey string,
) ([]renderedFile, error) {
	files := make([]renderedFile, 0, len(accounts)+5)
	keys, err := activeInternalKeyList(internalKeys, activeUsers(records))
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		proxyURL, err := renderer.effectiveProxyURL(ctx, account, settings)
		if err != nil {
			return nil, err
		}
		var installed cpaplugin.Installation
		if state, ok := renderer.Store.(interface {
			ReadRuntimeState(context.Context, string, any) (bool, error)
		}); ok {
			if _, err := state.ReadRuntimeState(ctx, cpaplugin.StatePrefix+account.ID, &installed); err != nil {
				return nil, err
			}
		}
		payload, err := renderCPAConfig(account, keys, managementKey, proxyURL, settings, installed)
		if err != nil {
			return nil, fmt.Errorf("render CPA config for %s: %w", account.ID, err)
		}
		files = append(files, renderedFile{
			relative:      filepath.ToSlash(filepath.Join("configs", account.ID, accountconfig.FileName)),
			payload:       payload,
			mode:          0o600,
			accountConfig: true,
		})
		if installed.Version != "" {
			plugin, err := cpaplugin.Parse(settings)
			if err != nil {
				return nil, err
			}
			if plugin.Selected(account.ID) && plugin.ProxySource == "direct" {
				proxyURL = "direct"
			} else if proxyURL == "direct" {
				proxyURL = ""
			}
			if proxyURL == "direct" && installed.Version != cpaplugin.BundledVersion {
				return nil, errors.New("explicit direct requires the bundled plugin compatibility version")
			}
			files = append(files, renderedFile{relative: filepath.ToSlash(filepath.Join("configs", account.ID, "codex-ticket-proxy.url")), payload: []byte(proxyURL), mode: 0o600})
		}
	}
	keyMap, err := renderGatewayKeyMap(accounts, routes, records)
	if err != nil {
		return nil, err
	}
	files = append(files,
		renderedFile{relative: "state/gateway/key.map", payload: keyMap, mode: 0o600},
		renderedFile{relative: "state/gateway/accounts.map", payload: renderGatewayAccountMap(accounts), mode: 0o600},
		renderedFile{relative: "state/gateway/backends.map", payload: renderGatewayBackendMap(accounts), mode: 0o600},
		renderedFile{relative: "state/public/accounts.json", payload: renderPublicAccounts(accounts), mode: 0o644},
		renderedFile{relative: "compose.accounts.yml", payload: renderAccountsCompose(accounts), mode: 0o600},
	)
	return files, nil
}

func (renderer *Renderer) effectiveProxyURL(
	ctx context.Context,
	account controlplane.Account,
	settings map[string]any,
) (string, error) {
	mode, err := controlplane.NormalizeProxyMode(account.ProxyMode)
	if err != nil {
		return "", err
	}
	custom, customFound, err := renderer.Store.ReadSecret(ctx, accountProxySecretPrefix+account.ID)
	if err != nil {
		return "", fmt.Errorf("read custom proxy for %s: %w", account.ID, err)
	}
	if customFound {
		custom, err = NormalizeProxyURL(custom)
		if err != nil {
			return "", fmt.Errorf("%w: account %s custom proxy: %v", ErrInvalidProjection, account.ID, err)
		}
	}
	switch mode {
	case "direct":
		return "direct", nil
	case "custom":
		if !customFound || custom == "" {
			return "", fmt.Errorf("%w: account %s selects a custom proxy without a configured URL", ErrInvalidProjection, account.ID)
		}
		return custom, nil
	}
	enabled, err := boolSetting(settings, "cpa.proxy_enabled", false)
	if err != nil {
		return "", err
	}
	if !enabled {
		return "direct", nil
	}
	value, found, err := renderer.Store.ReadSecret(ctx, defaultProxySecretName)
	if err != nil {
		return "", fmt.Errorf("read default CPA proxy: %w", err)
	}
	if !found || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: default CPA proxy is enabled without a configured URL", ErrInvalidProjection)
	}
	value, err = NormalizeProxyURL(value)
	if err != nil {
		return "", fmt.Errorf("%w: default CPA proxy: %v", ErrInvalidProjection, err)
	}
	return value, nil
}

type cpaConfig struct {
	CommercialMode               bool                `yaml:"commercial-mode,omitempty"`
	Plugins                      map[string]any      `yaml:"plugins,omitempty"`
	Host                         string              `yaml:"host"`
	Port                         int                 `yaml:"port"`
	TLS                          cpaTLS              `yaml:"tls"`
	RemoteManagement             cpaRemoteManagement `yaml:"remote-management"`
	AuthDirectory                string              `yaml:"auth-dir"`
	APIKeys                      []string            `yaml:"api-keys"`
	Debug                        bool                `yaml:"debug"`
	LoggingToFile                bool                `yaml:"logging-to-file"`
	LogsMaxTotalSizeMiB          int                 `yaml:"logs-max-total-size-mb"`
	ErrorLogsMaxFiles            int                 `yaml:"error-logs-max-files"`
	UsageStatisticsEnabled       bool                `yaml:"usage-statistics-enabled"`
	PassthroughHeaders           bool                `yaml:"passthrough-headers,omitempty"`
	DisableImageGeneration       any                 `yaml:"disable-image-generation"`
	UsageQueueRetentionSeconds   int                 `yaml:"redis-usage-queue-retention-seconds"`
	ProxyURL                     string              `yaml:"proxy-url"`
	RequestRetry                 int                 `yaml:"request-retry"`
	MaxRetryCredentials          int                 `yaml:"max-retry-credentials"`
	MaxRetryInterval             int                 `yaml:"max-retry-interval"`
	TransientErrorCooldownSecond int                 `yaml:"transient-error-cooldown-seconds"`
	Routing                      cpaRouting          `yaml:"routing"`
}

type cpaTLS struct {
	Enable bool   `yaml:"enable"`
	Cert   string `yaml:"cert"`
	Key    string `yaml:"key"`
}

type cpaRemoteManagement struct {
	AllowRemote       bool   `yaml:"allow-remote"`
	SecretKey         string `yaml:"secret-key"`
	DisablePanel      bool   `yaml:"disable-control-panel"`
	DisableAutoUpdate bool   `yaml:"disable-auto-update-panel"`
}

type cpaRouting struct {
	Strategy        string `yaml:"strategy"`
	SessionAffinity bool   `yaml:"session-affinity"`
	AffinityTTL     string `yaml:"session-affinity-ttl"`
}

func renderCPAConfig(
	account controlplane.Account,
	keys []string,
	managementKey string,
	proxyURL string,
	settings map[string]any,
	installation ...cpaplugin.Installation,
) ([]byte, error) {
	plugin, err := cpaplugin.Parse(settings)
	if err != nil {
		return nil, err
	}
	disableImages, err := disableImageSetting(settings)
	if err != nil {
		return nil, err
	}
	debug, err := boolSetting(settings, "cpa.debug", false)
	if err != nil {
		return nil, err
	}
	logging, err := boolSetting(settings, "cpa.logging_to_file", true)
	if err != nil {
		return nil, err
	}
	usage, err := boolSetting(settings, "cpa.usage_statistics_enabled", true)
	if err != nil {
		return nil, err
	}
	passthrough, err := boolSetting(settings, "cpa.passthrough_headers", false)
	if err != nil {
		return nil, err
	}
	affinity, err := boolSetting(settings, "cpa.session_affinity", true)
	if err != nil {
		return nil, err
	}
	affinityTTL, err := stringSetting(settings, "cpa.session_affinity_ttl", "1h")
	if err != nil || strings.TrimSpace(affinityTTL) == "" {
		return nil, fmt.Errorf("%w: invalid cpa.session_affinity_ttl", ErrInvalidProjection)
	}
	config := cpaConfig{
		Host: "", Port: 8317,
		TLS: cpaTLS{},
		RemoteManagement: cpaRemoteManagement{
			AllowRemote: true, SecretKey: managementKey,
			DisablePanel: false, DisableAutoUpdate: true,
		},
		AuthDirectory: "~/.cli-proxy-api", APIKeys: append([]string(nil), keys...),
		Debug: debug, LoggingToFile: logging,
		LogsMaxTotalSizeMiB:        mustIntSetting(settings, "cpa.logs_max_total_size_mb", defaultLogsSizeMiB, 16, 1024),
		ErrorLogsMaxFiles:          mustIntSetting(settings, "cpa.error_logs_max_files", defaultErrorLogFiles, 1, 100),
		UsageStatisticsEnabled:     usage,
		PassthroughHeaders:         passthrough && account.GroupEnabled,
		DisableImageGeneration:     disableImages,
		UsageQueueRetentionSeconds: mustIntSetting(settings, "cpa.usage_queue_retention_seconds", defaultQueueRetention, 60, 604800),
		ProxyURL:                   proxyURL,
		RequestRetry:               mustIntSetting(settings, "cpa.request_retry", defaultRequestRetry, 0, 10),
		MaxRetryCredentials:        mustIntSetting(settings, "cpa.max_retry_credentials", defaultMaxRetryCredential, 1, 10),
		MaxRetryInterval:           mustIntSetting(settings, "cpa.max_retry_interval", defaultMaxRetryInterval, 1, 300),
		TransientErrorCooldownSecond: mustIntSetting(
			settings, "cpa.transient_error_cooldown_seconds", defaultCooldownSeconds, 1, 300,
		),
		Routing: cpaRouting{Strategy: "round-robin", SessionAffinity: affinity, AffinityTTL: affinityTTL},
	}
	if len(installation) > 0 && installation[0].Version != "" {
		config.CommercialMode = true
		config.Plugins = plugin.YAML(account.ID, account.GroupEnabled, installation[0])
	}
	for _, value := range []struct {
		name  string
		value int
	}{
		{"cpa.logs_max_total_size_mb", config.LogsMaxTotalSizeMiB},
		{"cpa.error_logs_max_files", config.ErrorLogsMaxFiles},
		{"cpa.usage_queue_retention_seconds", config.UsageQueueRetentionSeconds},
		{"cpa.request_retry", config.RequestRetry},
		{"cpa.max_retry_credentials", config.MaxRetryCredentials},
		{"cpa.max_retry_interval", config.MaxRetryInterval},
		{"cpa.transient_error_cooldown_seconds", config.TransientErrorCooldownSecond},
	} {
		if value.value == invalidIntegerSetting {
			return nil, fmt.Errorf("%w: invalid %s", ErrInvalidProjection, value.name)
		}
	}
	payload, err := yaml.Marshal(config)
	if err != nil {
		return nil, err
	}
	comment := fmt.Sprintf("# Generated by Codex CPA; do not edit. Account: %s, upstream: %s\n", account.ID, account.Email)
	return append([]byte(comment), payload...), nil
}

func renderGatewayKeyMap(
	accounts []controlplane.Account,
	routes map[string]string,
	records []controlplane.KeyRecord,
) ([]byte, error) {
	accountCatalog := make(map[string]controlplane.Account, len(accounts))
	for _, account := range accounts {
		accountCatalog[account.ID] = account
	}
	byUser := make(map[string][]controlplane.KeyRecord)
	for _, record := range records {
		if record.Status != "active" {
			continue
		}
		user := strings.ToLower(strings.TrimSpace(record.User))
		if user == "" || record.Key == "" {
			return nil, fmt.Errorf("%w: active Key record has an empty user or secret", ErrInvalidProjection)
		}
		byUser[user] = append(byUser[user], record)
	}
	users := make([]string, 0, len(byUser))
	for user := range byUser {
		users = append(users, user)
	}
	sort.Strings(users)
	lines := []string{"# Generated; full API Keys are intentionally restricted to this 0600 compatibility map."}
	emitted := make(map[string]struct{})
	for _, user := range users {
		items := byUser[user]
		secrets := make(map[string]struct{})
		for _, item := range items {
			secrets[item.Key] = struct{}{}
		}
		pairs := make([]struct{ key, label string }, 0, len(items))
		if len(secrets) == 1 {
			accountID := routes[user]
			account, found := accountCatalog[accountID]
			if !found || !account.GroupEnabled {
				continue
			}
			matched := false
			for _, item := range items {
				if item.Account == accountID {
					pairs = append(pairs, struct{ key, label string }{item.Key, user + ":" + accountID})
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("%w: route for %s has no unified Key row in account %s", ErrInvalidProjection, user, accountID)
			}
		} else {
			for _, item := range items {
				if _, found := accountCatalog[item.Account]; !found {
					return nil, fmt.Errorf("%w: legacy Key row for %s references unknown account %s", ErrInvalidProjection, user, item.Account)
				}
				pairs = append(pairs, struct{ key, label string }{item.Key, user + ":" + item.Account})
			}
		}
		for _, pair := range pairs {
			if _, duplicate := emitted[pair.key]; duplicate {
				continue
			}
			emitted[pair.key] = struct{}{}
			lines = append(lines, jsonString("Bearer "+pair.key)+" "+jsonString(pair.label)+";")
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func renderGatewayAccountMap(accounts []controlplane.Account) []byte {
	lines := []string{"# Generated; Key label suffix to business CPA."}
	for _, account := range accounts {
		lines = append(lines, "~:"+account.ID+"$ "+account.ID+";")
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func renderGatewayBackendMap(accounts []controlplane.Account) []byte {
	lines := []string{"# Generated; business CPA to container backend."}
	for _, account := range accounts {
		lines = append(lines, account.ID+" cliproxy-"+account.ID+":8317;")
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func renderPublicAccounts(accounts []controlplane.Account) []byte {
	type publicAccount struct {
		ID           string `json:"id"`
		Port         int    `json:"port"`
		GroupName    string `json:"group_name"`
		GroupEnabled bool   `json:"group_enabled"`
	}
	payload := struct {
		Accounts []publicAccount `json:"accounts"`
	}{Accounts: make([]publicAccount, 0, len(accounts))}
	for _, account := range accounts {
		payload.Accounts = append(payload.Accounts, publicAccount{
			ID: account.ID, Port: account.Port, GroupName: account.ID, GroupEnabled: account.GroupEnabled,
		})
	}
	raw, _ := json.MarshalIndent(payload, "", "  ")
	return append(raw, '\n')
}

func renderAccountsCompose(accounts []controlplane.Account) []byte {
	lines := []string{"# Generated by Codex CPA; do not edit."}
	if len(accounts) == 0 {
		return []byte(strings.Join(append(lines, "services: {}"), "\n") + "\n")
	}
	lines = append(lines, "services:")
	for _, account := range accounts {
		service := "cliproxy-" + account.ID
		lines = append(lines,
			"  "+service+":",
			"    image: ${CLIPROXY_IMAGE:?state/compose.env missing; run cpa-control render}",
			"    container_name: ${INSTANCE_NAME:-cliproxy}-"+account.ID,
			"    restart: unless-stopped",
			"    logging:",
			"      driver: json-file",
			"      options:",
			"        max-size: \"20m\"",
			"        max-file: \"3\"",
			"    command: [\"./CLIProxyAPI\", \"-config\", \""+accountconfig.ContainerFile+"\"]",
			"    ports:",
			"      - \"${BUSINESS_CPA_LISTEN_ADDRESS:?state/compose.env missing}:"+strconv.Itoa(account.Port)+":8317\"",
			"    volumes:",
			"      - ./configs/"+account.ID+":"+accountconfig.ContainerDirectory+":ro",
			"      - ./management/config/static:/CLIProxyAPI/configs/static:ro",
			"      - ./auth/"+account.ID+":/root/.cli-proxy-api",
			"      - ./logs/"+account.ID+":/CLIProxyAPI/logs",
			"    networks:",
			"      - backend",
			"",
		)
	}
	return []byte(strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n")
}

func validateRenderedFile(file renderedFile) error {
	if len(file.payload) == 0 {
		return fmt.Errorf("%w: empty output %s", ErrInvalidProjection, file.relative)
	}
	switch {
	case strings.HasSuffix(file.relative, ".yaml"), strings.HasSuffix(file.relative, ".yml"):
		var document any
		if err := yaml.Unmarshal(file.payload, &document); err != nil {
			return fmt.Errorf("%w: invalid YAML %s: %v", ErrInvalidProjection, file.relative, err)
		}
	case strings.HasSuffix(file.relative, ".json"):
		if !json.Valid(file.payload) {
			return fmt.Errorf("%w: invalid JSON %s", ErrInvalidProjection, file.relative)
		}
	}
	return nil
}

func validateAccounts(accounts []controlplane.Account) error {
	seenID := make(map[string]struct{}, len(accounts))
	seenPort := make(map[int]struct{}, len(accounts))
	for _, account := range accounts {
		id, err := controlplane.NormalizeAccountID(account.ID)
		if err != nil || id != account.ID {
			return fmt.Errorf("%w: invalid stored account ID %q", ErrInvalidProjection, account.ID)
		}
		if account.Port < 1 || account.Port > 65535 {
			return fmt.Errorf("%w: invalid port for account %s", ErrInvalidProjection, account.ID)
		}
		if _, duplicate := seenID[account.ID]; duplicate {
			return fmt.Errorf("%w: duplicate account %s", ErrInvalidProjection, account.ID)
		}
		if _, duplicate := seenPort[account.Port]; duplicate {
			return fmt.Errorf("%w: duplicate account port %d", ErrInvalidProjection, account.Port)
		}
		seenID[account.ID] = struct{}{}
		seenPort[account.Port] = struct{}{}
	}
	return nil
}

func activeUsers(records []controlplane.KeyRecord) []string {
	seen := make(map[string]struct{})
	for _, record := range records {
		if record.Status == "active" {
			user := strings.ToLower(strings.TrimSpace(record.User))
			if user != "" {
				seen[user] = struct{}{}
			}
		}
	}
	users := make([]string, 0, len(seen))
	for user := range seen {
		users = append(users, user)
	}
	sort.Strings(users)
	return users
}

func activeInternalKeyList(
	keys map[string]controlplane.InternalKey,
	users []string,
) ([]string, error) {
	result := make([]string, 0, len(users))
	for _, user := range users {
		key, found := keys[user]
		if !found || key.Status != "active" || strings.TrimSpace(key.Key) == "" {
			return nil, fmt.Errorf("%w: active user %s has no active internal Key", ErrInvalidProjection, user)
		}
		result = append(result, key.Key)
	}
	sort.Strings(result)
	return result, nil
}

// NormalizeProxyURL validates the encrypted per-account/default proxy value
// without ever logging or redacting it. Callers may persist the returned value
// directly in the AES-GCM control-plane secret store.
func NormalizeProxyURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return "", errors.New("proxy URL is empty or contains whitespace")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("proxy URL is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks5" {
		return "", errors.New("proxy URL scheme must be HTTP, HTTPS, or SOCKS5")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("proxy URL must not contain a path, query, or fragment")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", errors.New("proxy URL contains an invalid port")
		}
	}
	return value, nil
}

func validManagementKey(value string) bool {
	if len(value) < 12 || len(value) > 128 {
		return false
	}
	return !strings.ContainsAny(value, "\r\n\t ")
}

const invalidIntegerSetting = -1 << 30

func mustIntSetting(settings map[string]any, key string, fallback, minimum, maximum int) int {
	raw, found := settings[key]
	if !found || raw == nil {
		return fallback
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
			return invalidIntegerSetting
		}
	default:
		return invalidIntegerSetting
	}
	if value < int64(minimum) || value > int64(maximum) {
		return invalidIntegerSetting
	}
	return int(value)
}

func boolSetting(settings map[string]any, key string, fallback bool) (bool, error) {
	raw, found := settings[key]
	if !found || raw == nil {
		return fallback, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%w: setting %s must be a boolean", ErrInvalidProjection, key)
	}
	return value, nil
}

func stringSetting(settings map[string]any, key string, fallback string) (string, error) {
	raw, found := settings[key]
	if !found || raw == nil {
		return fallback, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w: setting %s must be a string", ErrInvalidProjection, key)
	}
	return value, nil
}

func disableImageSetting(settings map[string]any) (any, error) {
	value, err := stringSetting(settings, "cpa.disable_image_generation", "chat")
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "chat":
		return "chat", nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return nil, fmt.Errorf("%w: invalid cpa.disable_image_generation", ErrInvalidProjection)
	}
}

func jsonString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func (renderer *Renderer) root() (string, error) {
	if strings.TrimSpace(renderer.Root) == "" {
		return "", errors.New("account projection root is required")
	}
	root, err := filepath.Abs(renderer.Root)
	if err != nil {
		return "", fmt.Errorf("resolve account projection root: %w", err)
	}
	return filepath.Clean(root), nil
}

func prepareAccountConfigDirectories(root string, accounts []controlplane.Account) error {
	configsRoot := filepath.Join(root, "configs")
	if err := ensureRealDirectory(configsRoot, 0o700); err != nil {
		return fmt.Errorf("prepare account config root: %w", err)
	}
	for _, account := range accounts {
		legacy := accountconfig.LegacyFile(root, account.ID)
		if information, err := os.Lstat(legacy); err == nil {
			if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: legacy account config is not a regular file: %s", ErrInvalidProjection, legacy)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect legacy account config %s: %w", legacy, err)
		}
		if err := ensureRealDirectory(accountconfig.Directory(root, account.ID), 0o700); err != nil {
			return fmt.Errorf("prepare account config directory for %s: %w", account.ID, err)
		}
	}
	return nil
}

func ensureRealDirectory(path string, mode os.FileMode) error {
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		information, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: expected a real directory: %s", ErrInvalidProjection, path)
	}
	return os.Chmod(path, mode)
}

func writeAtomic(path string, payload []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	if err := renameio.WriteFile(path, payload, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// SnapshotGID is retained as a named compatibility value for lifecycle code
// that secures the Gateway snapshot directory alongside these projections.
func SnapshotGID() int { return defaultSnapshotGroupID }
