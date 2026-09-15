// Package sitetime defines the single business timezone shared by all services.
// Process and container clocks remain UTC; calendar boundaries use the configured location.
package sitetime

import (
	"fmt"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

const (
	SettingKey  = "system.timezone"
	DefaultName = "Asia/Shanghai"
)

// Name preserves the legacy quota boundary on upgrades. The notification-only
// setting is used only when no quota timezone was selected. An explicit system
// timezone always wins, including over obsolete or invalid legacy values.
func Name(settings map[string]any) (string, error) {
	for _, key := range []string{SettingKey, "user_quota.timezone", "notification.timezone"} {
		raw, found := settings[key]
		if !found {
			continue
		}
		name, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%s must be an IANA timezone", key)
		}
		name = strings.TrimSpace(name)
		if _, err := Validate(name); err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		return name, nil
	}
	return DefaultName, nil
}

func Validate(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, i18n.M("sitetime.select_a_valid_iana_timezone")
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, i18n.M("sitetime.select_a_valid_iana_timezone_2", i18n.Params{"Detail": err}).WithCause(err)
	}
	return location, nil
}

// Migrate changes only timezone keys in the supplied settings snapshot. The
// caller persists this together with its normal settings transaction.
func Migrate(settings map[string]any) error {
	_, quota := settings["user_quota.timezone"]
	_, notification := settings["notification.timezone"]
	if !quota && !notification {
		return nil
	}
	name, err := Name(settings)
	if err != nil {
		return err
	}
	settings[SettingKey] = name
	delete(settings, "user_quota.timezone")
	delete(settings, "notification.timezone")
	return nil
}
