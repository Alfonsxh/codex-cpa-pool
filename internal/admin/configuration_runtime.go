package admin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/cpaplugin"
	"github.com/Alfonsxh/codex-cpa-pool/internal/identity"
	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
	"github.com/google/renameio/v2"
)

// ConfigurationAccountProjector refreshes account configs while retaining file
// watches, and atomically replaces routing projections after settings change.
type ConfigurationAccountProjector interface {
	RefreshAccounts(context.Context) error
}

// ConfigurationRuntime restarts only an explicitly selected service in the
// Compose project owned by this Admin process. Implementations must enforce
// exact project labels and reject unknown services.
type ConfigurationRuntime interface {
	RestartConfigurationTarget(context.Context, string) error
}

// ConfigurationAccountEnvironmentProjector refreshes the private business
// account Compose environment. It never changes formal target.env settings,
// applies a control-plane release, or switches the active Edge slot.
type ConfigurationAccountEnvironmentProjector interface {
	ProjectConfiguration(context.Context, map[string]any) error
}

type ConfigurationRuntimeApplier struct {
	Accounts interface {
		ReadAccounts(context.Context) ([]controlplane.Account, error)
	}
	GatewaySnapshots   identity.SnapshotPublisher
	Projection         ConfigurationAccountProjector
	Runtime            ConfigurationRuntime
	ControlRuntime     ConfigurationRuntime
	AccountEnvironment ConfigurationAccountEnvironmentProjector
}

func (applier *ConfigurationRuntimeApplier) ApplyConfiguration(
	ctx context.Context,
	change ConfigurationChange,
) error {
	if applier == nil {
		return errors.New("configuration runtime applier is unavailable")
	}
	modes := make(map[string]struct{}, len(change.Modes))
	for _, mode := range change.Modes {
		modes[mode] = struct{}{}
	}
	_, deploymentChanged := modes["deployment"]
	_, accountsChanged := modes["accounts"]
	_, collectorChanged := modes["collector"]
	_, quotaChanged := modes["quota"]
	liveCPAChanged := configurationLiveCPAChanged(change)
	if liveCPAChanged && applier.Accounts != nil {
		plugin, err := cpaplugin.Parse(change.After)
		if err != nil {
			return err
		}
		accounts, err := applier.Accounts.ReadAccounts(ctx)
		if err != nil {
			return err
		}
		known := map[string]bool{}
		for _, account := range accounts {
			known[account.ID] = true
		}
		for _, id := range plugin.Accounts {
			if !known[id] {
				return fmt.Errorf("unknown plugin account %s", id)
			}
		}
	}
	gatewayChanged := configurationGatewayPolicyChanged(change)
	if gatewayChanged && applier.GatewaySnapshots == nil {
		return errors.New("gateway policy publisher is unavailable")
	}

	// Validate every dependency before the first file or runtime mutation so a
	// partially configured Admin always fails closed.
	if deploymentChanged && applier.AccountEnvironment == nil {
		return errors.New("account Compose environment projector is unavailable")
	}
	if accountsChanged && (applier.Accounts == nil || applier.Projection == nil || applier.Runtime == nil) {
		return errors.New("account configuration runtime is unavailable")
	}
	if liveCPAChanged && applier.Projection == nil {
		return errors.New("account configuration projector is unavailable")
	}
	if (collectorChanged || quotaChanged) && applier.ControlRuntime == nil {
		return errors.New("collector configuration runtime is unavailable")
	}

	if deploymentChanged {
		if err := applier.AccountEnvironment.ProjectConfiguration(ctx, change.After); err != nil {
			return fmt.Errorf("project account Compose environment: %w", err)
		}
	}
	// CLIProxyAPI hot-loads response headers. These changes
	// must not start stopped accounts or interrupt existing account streams.
	if liveCPAChanged && !accountsChanged {
		if err := applier.Projection.RefreshAccounts(ctx); err != nil {
			return fmt.Errorf("refresh live account configuration: %w", err)
		}
	}
	if accountsChanged {
		accounts, err := applier.Accounts.ReadAccounts(ctx)
		if err != nil {
			return fmt.Errorf("read accounts before configuration refresh: %w", err)
		}
		if err := applier.Projection.RefreshAccounts(ctx); err != nil {
			return fmt.Errorf("refresh account configuration projection: %w", err)
		}
		sort.Slice(accounts, func(left, right int) bool { return accounts[left].ID < accounts[right].ID })
		for _, account := range accounts {
			if !account.GroupEnabled {
				continue
			}
			if err := applier.Runtime.RestartConfigurationTarget(ctx, account.ID); err != nil {
				return fmt.Errorf("restart configured account %s: %w", account.ID, err)
			}
		}
	}
	if collectorChanged || quotaChanged {
		if err := applier.ControlRuntime.RestartConfigurationTarget(ctx, "usage-collector"); err != nil {
			return fmt.Errorf("restart usage collector: %w", err)
		}
	}
	if gatewayChanged {
		if _, err := applier.GatewaySnapshots.PublishAuthSnapshot(ctx, true); err != nil {
			return fmt.Errorf("publish gateway request policy: %w", err)
		}
	}
	return nil
}

func configurationGatewayPolicyChanged(change ConfigurationChange) bool {
	for _, key := range change.Changed {
		if key == reasoningpolicy.SettingKey {
			return true
		}
	}
	return false
}

func configurationLiveCPAChanged(change ConfigurationChange) bool {
	for _, key := range change.Changed {
		if key == "cpa.passthrough_headers" || strings.HasPrefix(key, cpaplugin.Prefix) {
			return true
		}
	}
	return false
}

// AccountComposeEnvironmentProjector preserves the already-applied CPA image identity
// while regenerating only the business-account settings owned by the
// Configuration Center. Formal control-plane deployment settings live only in
// the operator-owned target.env and are never projected from SQLite.
type AccountComposeEnvironmentProjector struct {
	Root string
}

// ProjectCPAImage atomically replaces only CLIPROXY_IMAGE in the private
// account-Compose projection. The projection is derived state, so a missing
// file is rebuilt from the caller's already-validated image and listener.
func (projector *AccountComposeEnvironmentProjector) ProjectCPAImage(
	ctx context.Context,
	resolvedReference string,
	listenAddress string,
) error {
	resolvedReference = strings.TrimSpace(resolvedReference)
	if resolvedReference == "" || len(resolvedReference) > 512 || strings.ContainsAny(resolvedReference, "\r\n\t \x00") {
		return errors.New("resolved CPA image reference is invalid")
	}
	path, raw, found, err := projector.readExisting(ctx)
	if err != nil {
		return err
	}
	if found {
		if _, err := parseComposeEnvironment(raw); err != nil {
			return err
		}
	}
	return writeAccountComposeEnvironment(path, resolvedReference, listenAddress)
}

func (projector *AccountComposeEnvironmentProjector) ProjectConfiguration(
	ctx context.Context,
	values map[string]any,
) error {
	path, raw, found, err := projector.readExisting(ctx)
	if err != nil {
		return err
	}
	existing := map[string]string{}
	if found {
		existing, err = parseComposeEnvironment(raw)
		if err != nil {
			return err
		}
	}

	return writeAccountComposeEnvironment(
		path,
		firstNonEmpty(existing["CLIPROXY_IMAGE"], configurationText(values, "runtime.cliproxy_image")),
		configurationText(values, "accounts.listen_address"),
	)
}

func writeAccountComposeEnvironment(path string, image string, listenAddress string) error {
	projected := []struct {
		key   string
		value string
	}{
		{key: "CLIPROXY_IMAGE", value: image},
		{key: "BUSINESS_CPA_LISTEN_ADDRESS", value: listenAddress},
	}
	var content strings.Builder
	content.WriteString("# Generated from state/control-plane.sqlite3; do not edit.\n")
	for _, item := range projected {
		value := strings.TrimSpace(item.value)
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("account Compose environment value %s is empty or invalid", item.key)
		}
		content.WriteString(item.key)
		content.WriteByte('=')
		content.WriteString(value)
		content.WriteByte('\n')
	}
	return replaceComposeEnvironment(path, []byte(content.String()))
}

func (projector *AccountComposeEnvironmentProjector) readExisting(ctx context.Context) (string, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, false, err
	}
	root := strings.TrimSpace(projector.Root)
	if root == "" {
		return "", nil, false, errors.New("Compose environment root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", nil, false, fmt.Errorf("resolve Compose environment root: %w", err)
	}
	stateDirectory := filepath.Join(filepath.Clean(absoluteRoot), "state")
	state, err := os.Lstat(stateDirectory)
	if err != nil {
		return "", nil, false, fmt.Errorf("inspect account Compose state directory: %w", err)
	}
	if !state.IsDir() || state.Mode()&os.ModeSymlink != 0 {
		return "", nil, false, errors.New("account Compose state directory must be a regular directory")
	}
	path := filepath.Join(stateDirectory, "compose.env")
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil, false, nil
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("inspect existing Compose environment: %w", err)
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
		return "", nil, false, errors.New("existing Compose environment must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, false, fmt.Errorf("read existing Compose environment: %w", err)
	}
	return path, raw, true, nil
}

func replaceComposeEnvironment(path string, content []byte) error {
	if err := renameio.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("replace Compose environment: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure Compose environment: %w", err)
	}
	return nil
}

func parseComposeEnvironment(raw []byte) (map[string]string, error) {
	result := make(map[string]string)
	for lineNumber, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || strings.ContainsAny(key, " \t\r\n\x00") {
			return nil, fmt.Errorf("invalid Compose environment line %d", lineNumber+1)
		}
		if _, duplicate := result[key]; duplicate {
			return nil, fmt.Errorf("duplicate Compose environment key %s", key)
		}
		result[key] = strings.TrimSpace(value)
	}
	return result, nil
}

func configurationText(values map[string]any, key string) string {
	value, found := values[key]
	if !found || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
