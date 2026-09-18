package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/identity"
	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
	"github.com/Alfonsxh/codex-cpa-pool/internal/sitetime"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	defaultProxySecretName = "cpa_default_proxy_url"
	quotaResetSettingKey   = "user_quota.reset_personal_weekly_on_new_week"
	quotaRetentionFieldKey = "user_quota.preserve_personal_weekly_on_new_week"
)

var (
	configurationDurationPattern = regexp.MustCompile(`^([1-9][0-9]*)([smhd])$`)
	configurationColorPattern    = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	configurationImagePattern    = regexp.MustCompile(`^[A-Za-z0-9._:/@-]+$`)
	configurationDigestImage     = regexp.MustCompile(`^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$`)
	configurationTimePattern     = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

type configurationDefinition struct {
	Key            string
	Label          string
	LabelParams    i18n.Params
	ValueType      string
	ApplyMode      string
	Default        any
	Minimum        float64
	Maximum        float64
	HasMinimum     bool
	HasMaximum     bool
	MinimumLength  int
	MaximumLength  int
	Choices        map[string]struct{}
	ChoiceOrder    []string
	DigestRequired bool
}

type ConfigurationChange struct {
	Before   map[string]any
	After    map[string]any
	Changed  []string
	Modes    []string
	Rollback bool
}

// ConfigurationApplier owns side effects that cannot be represented by the
// settings rows alone: account projection/runtime refresh, Collector restart,
// quota snapshot refresh and deployment-environment projection.
type ConfigurationApplier interface {
	ApplyConfiguration(context.Context, ConfigurationChange) error
}

type configurationUpdateRequest struct {
	Confirm string         `json:"confirm"`
	Values  map[string]any `json:"values"`
}

type configurationUpdateResponse struct {
	Message           string   `json:"message"`
	Changed           []string `json:"changed"`
	Applied           []string `json:"applied"`
	PendingDeployment bool     `json:"pending_deployment"`
}

var configurationDefinitions = buildConfigurationDefinitions()

var configurationDefinitionByKey = func() map[string]configurationDefinition {
	result := make(map[string]configurationDefinition, len(configurationDefinitions))
	for _, definition := range configurationDefinitions {
		result[definition.Key] = definition
	}
	return result
}()

var retiredConfigurationKeys = map[string]struct{}{
	"gost.enabled": {}, "gost.remote_hosts": {}, "gost.remote_host": {},
	"gost.port_start": {}, "gost.port_end": {}, "runtime.gost_image": {},
	"runtime.admin_base_image": {}, "runtime.gateway_image": {},
	"gateway.listen_address": {}, "gateway.port": {}, "gateway.internal_port": {},
	"management.listen_address": {}, "management.port": {},
	"delivery.gateway_drain_timeout_seconds": {},
	"delivery.release_metadata_image":        {},
}

// The control-plane settings table is shared by the configuration catalog and
// focused Admin features. Preserve explicitly owned non-catalog rows across a
// configuration read/update instead of treating them as corrupt catalog keys.
var configurationPassthroughSettingKeys = map[string]struct{}{
	onboardingSkippedSetting: {},
}

func buildConfigurationDefinitions() []configurationDefinition {
	text := func(key, label, fallback string, minimum, maximum int, mode string, optional bool) configurationDefinition {
		valueType := "text"
		if optional {
			valueType = "optional_text"
		}
		return configurationDefinition{
			Key: key, Label: label, ValueType: valueType, ApplyMode: mode, Default: fallback,
			MinimumLength: minimum, MaximumLength: maximum,
		}
	}
	boolean := func(key, label string, fallback bool, mode string) configurationDefinition {
		return configurationDefinition{Key: key, Label: label, ValueType: "boolean", ApplyMode: mode, Default: fallback}
	}
	integer := func(key, label string, fallback int64, minimum, maximum int64, mode string) configurationDefinition {
		return configurationDefinition{
			Key: key, Label: label, ValueType: "integer", ApplyMode: mode, Default: fallback,
			Minimum: float64(minimum), Maximum: float64(maximum), HasMinimum: true, HasMaximum: true,
		}
	}
	number := func(key, label string, fallback, minimum, maximum float64, mode string) configurationDefinition {
		return configurationDefinition{
			Key: key, Label: label, ValueType: "number", ApplyMode: mode, Default: fallback,
			Minimum: minimum, Maximum: maximum, HasMinimum: true, HasMaximum: true,
		}
	}
	choice := func(key, label, fallback, mode string, values ...string) configurationDefinition {
		choices := make(map[string]struct{}, len(values))
		for _, value := range values {
			choices[value] = struct{}{}
		}
		return configurationDefinition{
			Key: key, Label: label, ValueType: "choice", ApplyMode: mode, Default: fallback,
			Choices: choices, ChoiceOrder: append([]string(nil), values...),
		}
	}
	simple := func(key, label, valueType string, fallback any, mode string) configurationDefinition {
		return configurationDefinition{Key: key, Label: label, ValueType: valueType, ApplyMode: mode, Default: fallback}
	}

	definitions := []configurationDefinition{
		text("branding.product_name", "admin.product_name", defaultProductName, 2, 64, "live", false),
		text("branding.short_name", "admin.short_name", "CCPA", 2, 32, "live", false),
		text("branding.environment_label", "admin.environment_label", "Self-hosted service", 0, 64, "live", true),
		simple("branding.public_base_url", "admin.public_url", "base_url", "", "live"),
		simple("identity.allowed_email_domains", "admin.allowed_email_domains", "domain_list", []string{}, "live"),
		simple("identity.key_prefix", "admin.new_key_prefix", "key_prefix", identity.DefaultUserKeyPrefix, "live"),
		text("portal.provider_name", "admin.client_provider_name", "Codex CPA Pool", 2, 48, "live", false),
		simple("portal.api_key_env", "admin.client_key_environment_variable", "env_name", "CCPA_API_KEY", "live"),
		text("portal.default_model", "admin.default_client_model", "gpt-5.6-sol", 1, 128, "live", false),
		choice(i18n.SettingKey, "configuration.system_language", "en", "live", "en", "zh-CN"),
		simple(sitetime.SettingKey, "admin.system_timezone", "timezone", sitetime.DefaultName, "collector"),
		boolean("cpa.proxy_enabled", "admin.enable_default_upstream_proxy", false, "accounts"),
		simple("cpa.proxy_url", "admin.default_upstream_proxy_url", "proxy_url_secret", "", "accounts"),
		integer("cpa.request_retry", "admin.request_retries", 2, 0, 10, "accounts"),
		choice("cpa.disable_image_generation", "admin.image_tool_policy", "chat", "accounts", "chat", "true", "false"),
		integer("cpa.max_retry_credentials", "admin.maximum_credential_retries", 1, 1, 10, "accounts"),
		integer("cpa.max_retry_interval", "admin.maximum_retry_wait", 12, 1, 300, "accounts"),
		integer("cpa.transient_error_cooldown_seconds", "admin.transient_error_cooldown", 10, 1, 300, "accounts"),
		boolean("cpa.session_affinity", "admin.session_affinity", true, "accounts"),
		boolean("cpa.passthrough_headers", "configuration.passthrough_headers", false, "live"),
		simple("cpa.session_affinity_ttl", "admin.session_affinity_duration", "duration", "1h", "accounts"),
		boolean("cpa.debug", "admin.debug_logging", false, "accounts"),
		boolean("cpa.logging_to_file", "admin.write_cpa_log_files", true, "accounts"),
		integer("cpa.logs_max_total_size_mb", "admin.log_capacity_per_cpa", 64, 16, 1024, "accounts"),
		integer("cpa.error_logs_max_files", "admin.error_file_limit_per_cpa", 10, 1, 100, "accounts"),
		boolean("cpa.usage_statistics_enabled", "admin.official_usage_events", true, "accounts"),
		integer("cpa.usage_queue_retention_seconds", "admin.usage_queue_retention", 3600, 60, 604800, "accounts"),
		integer(usage.ActiveUserWindowSettingKey, "admin.active_user_window", int64(usage.DefaultActiveUserWindow/time.Second), 60, 86400, "live"),
		integer("usage.quota_cache_seconds", "admin.official_quota_cache", 60, 30, 3600, "live"),
		integer("usage.upstream_timeout_seconds", "admin.official_api_timeout", 20, 5, 120, "live"),
		choice("account_failover.mode", "admin.automatic_switching_mode", "active", "live", "off", "active"),
		integer("account_failover.poll_seconds", "admin.quota_check_interval", 60, 30, 3600, "live"),
		number("account_failover.reserve_percent", "admin.target_account_safety_reserve", 5, 0, 50, "live"),
		integer("account_failover.stale_after_seconds", "admin.quota_data_expiry", 120, 60, 7200, "live"),
		{Key: "user_quota.default_weekly_tokens", Label: "admin.default_weekly_quota_per_user", ValueType: "nullable_integer", ApplyMode: "quota", Default: nil, Minimum: 1, Maximum: 1_000_000_000_000, HasMinimum: true, HasMaximum: true},
		boolean(quotaResetSettingKey, "admin.preserve_personal_quota_changes", true, "quota"),
		integer("user_quota.fail_open_after_seconds", "admin.quota_failure_grace_period", 300, 30, 3600, "quota"),
	}
	for _, model := range usage.ModelMultiplierDefinitions() {

		definitions = append(definitions, number(
			usage.ModelMultiplierSettingKey(model.Model),
			"configuration.model_multiplier",
			model.Default, 0.1, 10, "quota",
		))
	}
	for _, effort := range usage.ReasoningMultiplierDefinitions() {
		definitions = append(definitions, number(
			usage.ReasoningMultiplierSettingKey(effort.Effort),
			"configuration.reasoning_multiplier",
			effort.Default, 0.1, 10, "quota",
		))
	}
	for _, effort := range []struct {
		name, fallback string
	}{
		{"none", "#7d8490"}, {"minimal", "#84929a"}, {"low", "#4b8ccf"},
		{"medium", "#7653a6"}, {"high", "#2f73d9"}, {"xhigh", "#5965c7"},
		{"max", "#b2731e"}, {"ultra", "#9b5f9d"}, {"auto", "#5e708a"},
		{"unknown", "#687287"},
	} {
		definitions = append(definitions, simple(
			"admin.account_usage.reasoning_effort_color."+effort.name,
			"configuration.reasoning_color",
			"color", effort.fallback, "live",
		))
	}
	definitions = append(definitions,
		boolean("notification.enabled", "admin.enable_wecom_notifications", false, "live"),
		simple("notification.daily_times", "admin.daily_delivery_times", "time_list", "09:00,14:00,18:00", "live"),
		integer("notification.schedule_grace_minutes", "admin.missed_delivery_window", 15, 0, 120, "live"),
		boolean("notification.quota_alert_enabled", "admin.enable_weekly_quota_alerts", true, "live"),
		number("notification.weekly_threshold_percent", "admin.weekly_quota_alert_threshold", 90, 1, 100, "live"),
		integer("portal.session_ttl_seconds", "admin.usage_center_session_duration", 43200, 3600, 43200, "live"),
		number("collector.interval_seconds", "admin.collection_polling_interval", 2, 0.5, 60, "collector"),
		integer("collector.batch_size", "admin.events_per_batch", 100, 1, 500, "collector"),
		integer("accounts.port_start", "admin.new_account_port_start", 18319, 1024, 65535, "future"),
		integer("accounts.port_end", "admin.new_account_port_end", 18999, 1024, 65535, "future"),
		simple("accounts.listen_address", "admin.cpa_listen_address", "ip", "127.0.0.1", "deployment"),
		simple("runtime.cliproxy_image", "admin.cliproxyapi_image", "image", runtimeops.DefaultCPAImageUpdateChannel, "deployment"),
	)
	return definitions
}

func (server *Server) updateConfiguration(c *gin.Context) {
	var body configurationUpdateRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_saving_configuration"), "invalid_request")
		return
	}
	if body.Values == nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.configuration_values_must_be_an_object"), "invalid_request")
		return
	}
	changes := make(map[string]any, len(body.Values))
	for key, value := range body.Values {
		changes[key] = value
	}
	// The catalog presents retention positively while existing settings and
	// runtime readers keep their reset flag. Do not persist two opposing flags.
	if raw, found := changes[quotaRetentionFieldKey]; found {
		if _, duplicate := changes[quotaResetSettingKey]; duplicate {
			writeError(c, http.StatusBadRequest, i18n.M("admin.set_only_the_quota_retention_option_do_not_also_submit"), "invalid_request")
			return
		}
		value, err := normalizeConfigurationValue(configurationDefinitionByKey[quotaResetSettingKey], raw)
		if err != nil {
			writeError(c, http.StatusBadRequest, err, "invalid_request")
			return
		}
		changes[quotaResetSettingKey] = !value.(bool)
		delete(changes, quotaRetentionFieldKey)
	}
	// v1 deliberately treats an empty proxy field as "leave unchanged" so a
	// masked secret rendered by the browser cannot accidentally clear it.
	if raw, found := changes["cpa.proxy_url"]; found && strings.TrimSpace(valueString(raw)) == "" {
		delete(changes, "cpa.proxy_url")
	}
	if len(changes) == 0 {
		httpi18n.JSON(c, http.StatusOK, noConfigurationChanges(httpi18n.Locale(c)))
		return
	}

	server.configurationLock.Lock()
	defer server.configurationLock.Unlock()
	ctx := c.Request.Context()
	storedBefore, current, proxyBefore, proxyFound, err := server.currentConfiguration(ctx)
	if err != nil {
		server.internalError(c, "read configuration", err)
		return
	}
	updated := cloneConfiguration(current)
	unknown := make([]string, 0)
	for key, raw := range changes {
		definition, found := configurationDefinitionByKey[key]
		if !found {
			unknown = append(unknown, key)
			continue
		}
		value, normalizeError := normalizeConfigurationValue(definition, raw)
		if normalizeError != nil {
			writeError(c, http.StatusBadRequest, normalizeError, "invalid_request")
			return
		}
		updated[key] = value
	}
	if len(unknown) != 0 {
		sort.Strings(unknown)
		writeError(c, http.StatusBadRequest, httpi18n.Text(c, "admin.unsupported_setting")+strings.Join(unknown, ", "), "invalid_request")
		return
	}
	if err := validateConfiguration(updated); err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	if enabled, _ := updated["notification.enabled"].(bool); enabled {
		statuses, statusError := server.store.SecretStatuses(ctx)
		if statusError != nil {
			server.internalError(c, "read notification webhook status", statusError)
			return
		}
		if !hasSecretStatus(statuses, "wecom_webhook") {
			writeError(c, http.StatusBadRequest, i18n.M("admin.configure_a_webhook_before_enabling_wecom_notifications"), "invalid_request")
			return
		}
	}

	changed := changedConfigurationKeys(current, updated, storedBefore, changes)
	if len(changed) == 0 {
		httpi18n.JSON(c, http.StatusOK, noConfigurationChanges(httpi18n.Locale(c)))
		return
	}
	storedAfter := cloneConfiguration(storedBefore)
	for _, key := range changed {
		if key != "cpa.proxy_url" {
			storedAfter[key] = updated[key]
		}
	}
	for key := range retiredConfigurationKeys {
		delete(storedAfter, key)
	}
	proxyAfter := optionalSecretValue(updated["cpa.proxy_url"])
	if err := server.store.ReplaceSettingsAndSecret(ctx, storedAfter, defaultProxySecretName, proxyAfter); err != nil {
		server.internalError(c, "save configuration", err)
		return
	}
	modes := configurationModes(changed)
	change := ConfigurationChange{Before: current, After: updated, Changed: changed, Modes: modes}
	if err := server.applyConfiguration(ctx, change); err != nil {
		proxyRestore := (*string)(nil)
		if proxyFound {
			value := proxyBefore
			proxyRestore = &value
		}
		storeRollbackError := server.store.ReplaceSettingsAndSecret(
			context.WithoutCancel(ctx), storedBefore, defaultProxySecretName, proxyRestore,
		)
		rollback := ConfigurationChange{
			Before: updated, After: current, Changed: changed, Modes: modes, Rollback: true,
		}
		applyRollbackError := server.applyConfiguration(context.WithoutCancel(ctx), rollback)
		server.logger.Error("configuration apply failed and rollback was attempted",
			zapError("apply_error_class", err),
			zapError("store_rollback_error_class", storeRollbackError),
			zapError("apply_rollback_error_class", applyRollbackError))
		writeError(c, http.StatusBadGateway, i18n.M("admin.configuration_could_not_be_applied_restoration_of_the_previous_configuration"), "configuration_apply_failed")
		return
	}

	server.clearConfigurationCaches()
	applied := make([]string, 0, len(modes))
	pendingDeployment := false
	for _, mode := range modes {
		switch mode {
		case "deployment":
			pendingDeployment = true
		case "future":
		default:
			applied = append(applied, mode)
		}
	}
	message := i18n.M("admin.saved_settings", i18n.Params{"Count": len(changed)}).Render(httpi18n.Locale(c))
	if pendingDeployment {
		message += httpi18n.Text(c, "admin.cpa_parameters_were_written_to_the_private_compose_configuration_and")
	}
	responseChanged := append([]string(nil), changed...)
	if _, retentionRequested := body.Values[quotaRetentionFieldKey]; retentionRequested {
		for index, key := range responseChanged {
			if key == quotaResetSettingKey {
				responseChanged[index] = quotaRetentionFieldKey
			}
		}
	}
	httpi18n.JSON(c, http.StatusOK, configurationUpdateResponse{
		Message: message, Changed: responseChanged, Applied: applied, PendingDeployment: pendingDeployment,
	})
}

func (server *Server) applyConfiguration(ctx context.Context, change ConfigurationChange) error {
	if server.configurationApplier != nil {
		return server.configurationApplier.ApplyConfiguration(ctx, change)
	}
	if configurationLiveCPAChanged(change) {
		return errors.New("configuration account projector is unavailable")
	}
	for _, mode := range change.Modes {
		if mode == "accounts" || mode == "collector" || mode == "deployment" {
			return errors.New("configuration runtime applier is unavailable")
		}
	}
	return nil
}

func (server *Server) currentConfiguration(
	ctx context.Context,
) (stored map[string]any, effective map[string]any, proxy string, proxyFound bool, err error) {
	stored, err = server.store.ReadSettings(ctx)
	if err != nil {
		return nil, nil, "", false, err
	}
	originalStored := cloneConfiguration(stored)
	if err := sitetime.Migrate(stored); err != nil {
		return nil, nil, "", false, err
	}
	legacyProxy := ""
	cleaned := make(map[string]any, len(stored))
	for key, value := range stored {
		if _, retired := retiredConfigurationKeys[key]; retired {
			continue
		}
		if _, passthrough := configurationPassthroughSettingKeys[key]; passthrough {
			cleaned[key] = value
			continue
		}
		if _, found := configurationDefinitionByKey[key]; !found {
			return nil, nil, "", false, i18n.M("admin.unknown_configuration_parameter", i18n.Params{"Key": key})
		}
		if key == "cpa.proxy_url" {
			legacyProxy = strings.TrimSpace(valueString(value))
			continue
		}
		cleaned[key] = value
	}
	stored = cleaned
	if stored["account_failover.mode"] == "observe" {
		stored["account_failover.mode"] = "off"
	}
	for _, key := range []string{"accounts.listen_address"} {
		if stored[key] == "0.0.0.0" || stored[key] == "::" {
			stored[key] = "127.0.0.1"
		}
	}
	proxy, proxyFound, err = server.store.ReadSecret(ctx, defaultProxySecretName)
	if err != nil {
		return nil, nil, "", false, err
	}
	if !proxyFound && legacyProxy != "" {
		proxy = legacyProxy
		proxyFound = true
	}
	effective = make(map[string]any, len(configurationDefinitions))
	for _, definition := range configurationDefinitions {
		raw := definition.Default
		if definition.Key == "cpa.proxy_url" {
			raw = proxy
		} else if value, found := stored[definition.Key]; found {
			raw = value
		}
		value, normalizeError := normalizeConfigurationValue(definition, raw)
		if normalizeError != nil {
			return nil, nil, "", false, fmt.Errorf("stored setting %s: %w", definition.Key, normalizeError)
		}
		effective[definition.Key] = value
	}
	if validationError := validateConfiguration(effective); validationError != nil {
		return nil, nil, "", false, validationError
	}
	if !reflect.DeepEqual(originalStored, stored) {
		var proxyValue *string
		if proxyFound {
			value := proxy
			proxyValue = &value
		}
		if err := server.store.ReplaceSettingsAndSecret(
			ctx,
			stored,
			defaultProxySecretName,
			proxyValue,
		); err != nil {
			return nil, nil, "", false, fmt.Errorf("normalize stored configuration: %w", err)
		}
	}
	return stored, effective, proxy, proxyFound, nil
}

func normalizeConfigurationValue(definition configurationDefinition, raw any) (any, error) {
	switch definition.ValueType {
	case "boolean":
		if value, ok := raw.(bool); ok {
			return value, nil
		}
		if value, ok := raw.(string); ok {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true", "1", "yes", "on":
				return true, nil
			case "false", "0", "no", "off":
				return false, nil
			}
		}
		return nil, i18n.M("admin.must_be_a_boolean", i18n.Params{"Field": definition.label()})
	case "integer", "nullable_integer":
		if definition.ValueType == "nullable_integer" && (raw == nil || strings.TrimSpace(valueString(raw)) == "") {
			return nil, nil
		}
		value, err := configurationInteger(raw)
		if err != nil {
			return nil, i18n.M("admin.must_be_an_integer", i18n.Params{"Field": definition.label()})
		}
		if definition.HasMinimum && float64(value) < definition.Minimum ||
			definition.HasMaximum && float64(value) > definition.Maximum {
			return nil, i18n.M("admin.must_be_between_and", i18n.Params{"Field": definition.label(), "Minimum": numberLabel(definition.Minimum), "Maximum": numberLabel(definition.Maximum)})
		}
		return value, nil
	case "number":
		value, err := configurationNumber(raw)
		if err != nil {
			return nil, i18n.M("admin.must_be_a_number", i18n.Params{"Field": definition.label()})
		}
		if !math.IsInf(value, 0) && !math.IsNaN(value) {
			if definition.HasMinimum && value < definition.Minimum || definition.HasMaximum && value > definition.Maximum {
				return nil, i18n.M("admin.must_be_between_and", i18n.Params{"Field": definition.label(), "Minimum": numberLabel(definition.Minimum), "Maximum": numberLabel(definition.Maximum)})
			}
			return value, nil
		}
		return nil, i18n.M("admin.must_be_a_finite_number", i18n.Params{"Field": definition.label()})
	}

	value := strings.TrimSpace(valueString(raw))
	if definition.Key == "branding.product_name" {
		value = normalizeProductName(value)
	}
	switch definition.ValueType {
	case "text", "optional_text":
		if definition.ValueType == "text" && value == "" {
			return nil, i18n.M("admin.is_required", i18n.Params{"Field": definition.label()})
		}
		length := utf8.RuneCountInString(value)
		if length < definition.MinimumLength {
			return nil, i18n.M("admin.must_contain_at_least_characters", i18n.Params{"Field": definition.label(), "Minimum": definition.MinimumLength})
		}
		if definition.MaximumLength > 0 && length > definition.MaximumLength {
			return nil, i18n.M("admin.must_not_exceed_characters", i18n.Params{"Field": definition.label(), "Maximum": definition.MaximumLength})
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return nil, i18n.M("admin.must_not_contain_control_characters", i18n.Params{"Field": definition.label()})
			}
		}
		return value, nil
	case "domain_list":
		values := make([]string, 0)
		switch typed := raw.(type) {
		case []string:
			values = append(values, typed...)
		case []any:
			for _, item := range typed {
				values = append(values, valueString(item))
			}
		default:
			values = strings.FieldsFunc(value, func(character rune) bool {
				return character == ',' || character == '，' || unicode.IsSpace(character)
			})
		}
		return normalizeDomains(values)
	case "key_prefix":
		value = strings.ToLower(value)
		if !keyPrefixPattern.MatchString(value) {
			return nil, i18n.M("admin.must_contain_3_32_lowercase_letters_digits_or_underscores_and", i18n.Params{"Field": definition.label()})
		}
		return value, nil
	case "env_name":
		if !envNamePattern.MatchString(value) {
			return nil, i18n.M("admin.must_be_a_valid_uppercase_environment_variable_name", i18n.Params{"Field": definition.label()})
		}
		return value, nil
	case "choice":
		if _, found := definition.Choices[value]; !found {
			allowed := make([]string, 0, len(definition.Choices))
			for choice := range definition.Choices {
				allowed = append(allowed, choice)
			}
			sort.Strings(allowed)
			return nil, i18n.M("admin.must_be_one_of", i18n.Params{"Field": definition.label(), "Choices": strings.Join(allowed, ", ")})
		}
		return value, nil
	case "color":
		if !configurationColorPattern.MatchString(value) {
			return nil, i18n.M("admin.must_use_the_rrggbb_color_format", i18n.Params{"Field": definition.label()})
		}
		return strings.ToLower(value), nil
	case "base_url", "proxy_url_secret":
		return normalizeConfigurationURL(definition, value)
	case "duration":
		match := configurationDurationPattern.FindStringSubmatch(strings.ToLower(value))
		if match == nil {
			return nil, i18n.M("admin.use_a_duration_such_as_30s_5m_1h_or_7d")
		}
		amount, _ := strconv.ParseInt(match[1], 10, 64)
		scale := map[string]int64{"s": 1, "m": 60, "h": 3600, "d": 86400}[match[2]]
		seconds := amount * scale
		if amount <= 0 || seconds < 30 || seconds > 30*24*60*60 {
			return nil, i18n.M("admin.must_be_between_30_seconds_and_30_days", i18n.Params{"Field": definition.label()})
		}
		return strings.ToLower(value), nil
	case "timezone":
		if value == "" || len(value) > 64 {
			return nil, i18n.M("admin.must_be_a_valid_iana_timezone", i18n.Params{"Field": definition.label()})
		}
		if _, err := sitetime.Validate(value); err != nil {
			return nil, i18n.M("admin.must_be_a_valid_iana_timezone", i18n.Params{"Field": definition.label()})
		}
		return value, nil
	case "time_list":
		return normalizeConfigurationTimes(definition, value)
	case "image", "optional_image":
		if definition.ValueType == "optional_image" && value == "" {
			return "", nil
		}
		if value == "" || len(value) > 255 || !configurationImagePattern.MatchString(value) {
			return nil, i18n.M("admin.has_an_invalid_image_name", i18n.Params{"Field": definition.label()})
		}
		if definition.DigestRequired && !configurationDigestImage.MatchString(value) {
			return nil, i18n.M("admin.must_pin_the_image_using_name_tag_sha256_digest", i18n.Params{"Field": definition.label()})
		}
		return value, nil
	case "ip":
		address := net.ParseIP(value)
		if address == nil || address.To4() == nil {
			return nil, i18n.M("admin.must_be_a_valid_ipv4_address", i18n.Params{"Field": definition.label()})
		}
		return address.To4().String(), nil
	default:
		return nil, i18n.M("admin.unknown_configuration_type", i18n.Params{"Key": definition.ValueType, "Type": definition.Key})
	}
}

func normalizeConfigurationURL(definition configurationDefinition, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return "", i18n.M("admin.must_not_contain_whitespace_or_control_characters", i18n.Params{"Field": definition.label()})
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", i18n.M("admin.must_be_a_valid_http_s_url", i18n.Params{"Field": definition.label()})
	}
	scheme := strings.ToLower(parsed.Scheme)
	allowed := scheme == "http" || scheme == "https"
	if definition.ValueType == "proxy_url_secret" {
		allowed = allowed || scheme == "socks5"
	}
	if !allowed {
		return "", i18n.M("admin.must_be_a_valid_http_s_url", i18n.Params{"Field": definition.label()})
	}
	if definition.ValueType != "proxy_url_secret" && parsed.User != nil {
		return "", i18n.M("admin.must_not_contain_a_username_or_password", i18n.Params{"Field": definition.label()})
	}
	if port := parsed.Port(); port != "" {
		value, parseError := strconv.Atoi(port)
		if parseError != nil || value < 1 || value > 65535 {
			return "", i18n.M("admin.contains_an_invalid_port", i18n.Params{"Field": definition.label()})
		}
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", i18n.M("admin.must_not_contain_a_query_or_fragment", i18n.Params{"Field": definition.label()})
	}
	return strings.TrimRight(value, "/"), nil
}

func normalizeConfigurationTimes(definition configurationDefinition, value string) (string, error) {
	parts := strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == '，' || unicode.IsSpace(character)
	})
	if len(parts) == 0 || len(parts) > 12 {
		return "", i18n.M("admin.must_contain_1_12_times", i18n.Params{"Field": definition.label()})
	}
	times := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		match := configurationTimePattern.FindStringSubmatch(part)
		if match == nil {
			return "", i18n.M("admin.must_use_the_hh_mm_format", i18n.Params{"Field": definition.label()})
		}
		hour, _ := strconv.Atoi(match[1])
		minute, _ := strconv.Atoi(match[2])
		if hour > 23 || minute > 59 {
			return "", i18n.M("admin.contains_an_invalid_time", i18n.Params{"Field": definition.label()})
		}
		times[fmt.Sprintf("%02d:%02d", hour, minute)] = struct{}{}
	}
	result := make([]string, 0, len(times))
	for value := range times {
		result = append(result, value)
	}
	sort.Strings(result)
	return strings.Join(result, ","), nil
}

func validateConfiguration(values map[string]any) error {
	for _, key := range []string{"accounts.listen_address"} {
		address, _ := values[key].(string)
		parsed := net.ParseIP(address)
		if parsed == nil || !parsed.IsLoopback() {
			return i18n.M("admin.the_cpa_listen_address_must_be_a_host_loopback_address")
		}
	}
	portStart := values["accounts.port_start"].(int64)
	portEnd := values["accounts.port_end"].(int64)
	if portStart > portEnd {
		return i18n.M("admin.the_start_port_must_not_exceed_the_end_port")
	}
	if values["account_failover.stale_after_seconds"].(int64) < values["account_failover.poll_seconds"].(int64) {
		return i18n.M("admin.quota_data_expiry_must_not_be_shorter_than_the_automatic")
	}
	if values["cpa.proxy_enabled"].(bool) && strings.TrimSpace(values["cpa.proxy_url"].(string)) == "" {
		return i18n.M("admin.configure_a_default_proxy_url_before_enabling_the_default_upstream")
	}
	return nil
}

func changedConfigurationKeys(before, after, stored, requested map[string]any) []string {
	result := make([]string, 0)
	for _, definition := range configurationDefinitions {
		_, explicitlyRequested := requested[definition.Key]
		_, alreadyStored := stored[definition.Key]
		// Selecting the effective timezone default is still a durable onboarding decision;
		// without the row, the recommended timezone step would remain incomplete.
		// An explicit brand save also replaces a legacy built-in value whose
		// effective display already uses the new product name.
		brandNeedsSave := explicitlyRequested && alreadyStored && definition.Key == "branding.product_name" &&
			!reflect.DeepEqual(stored[definition.Key], after[definition.Key])
		if !reflect.DeepEqual(before[definition.Key], after[definition.Key]) || brandNeedsSave ||
			(explicitlyRequested && !alreadyStored && definition.Key == sitetime.SettingKey) {
			result = append(result, definition.Key)
		}
	}
	return result
}

func configurationModes(changed []string) []string {
	seen := make(map[string]struct{})
	for _, key := range changed {
		seen[configurationDefinitionByKey[key].ApplyMode] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for mode := range seen {
		result = append(result, mode)
	}
	sort.Strings(result)
	return result
}

func noConfigurationChanges(languages ...i18n.Language) configurationUpdateResponse {
	return configurationUpdateResponse{
		Message: i18n.Text(i18n.Selected(languages), "admin.no_configuration_changes"), Changed: []string{}, Applied: []string{}, PendingDeployment: false,
	}
}

func optionalSecretValue(raw any) *string {
	value := strings.TrimSpace(valueString(raw))
	if value == "" {
		return nil
	}
	return &value
}

func cloneConfiguration(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case []string:
			cloned := make([]string, len(typed))
			copy(cloned, typed)
			result[key] = cloned
		default:
			result[key] = value
		}
	}
	return result
}

func configurationInteger(raw any) (int64, error) {
	switch value := raw.(type) {
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case float64:
		if math.IsInf(value, 0) || math.IsNaN(value) || math.Trunc(value) != value || value < math.MinInt64 || value > math.MaxInt64 {
			return 0, errors.New("not an integer")
		}
		return int64(value), nil
	case json.Number:
		return value.Int64()
	case string:
		return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	default:
		return 0, errors.New("not an integer")
	}
}

func configurationNumber(raw any) (float64, error) {
	switch value := raw.(type) {
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case float64:
		return value, nil
	case json.Number:
		return value.Float64()
	case string:
		return strconv.ParseFloat(strings.TrimSpace(value), 64)
	default:
		return 0, errors.New("not numeric")
	}
}

func valueString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func numberLabel(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// zapError keeps configuration rollback logging concise without letting any
// submitted value or encrypted proxy URL enter the structured log.
func zapError(field string, err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	classification := fmt.Sprintf("%T", err)
	switch {
	case errors.Is(err, runtimeops.ErrRuntimeTarget):
		classification = "runtime_target"
	case errors.Is(err, runtimeops.ErrRuntimeConflict):
		classification = "runtime_conflict"
	case errors.Is(err, runtimeops.ErrRuntimeReadOnly):
		classification = "runtime_read_only"
	case errors.Is(err, runtimeops.ErrUnsafeDockerHost):
		classification = "unsafe_docker_host"
	}
	return zap.String(field, classification)
}

var _ sync.Locker = (*sync.Mutex)(nil)

func (definition configurationDefinition) label() *i18n.Message {
	params := definition.LabelParams
	switch definition.Label {
	case "configuration.model_multiplier":
		model := strings.TrimPrefix(definition.Key, "user_quota.model_multiplier.")
		// The setting prefix comes from the usage contract.
		model = strings.TrimPrefix(definition.Key, usage.ModelMultiplierSettingKey(""))
		var value any = model
		if model == "unknown" {
			value = i18n.Ref("admin.other_unmatched_models")
		}
		params = i18n.Params{"Model": value}
	case "configuration.reasoning_multiplier":
		effort := strings.TrimPrefix(definition.Key, usage.ReasoningMultiplierSettingKey(""))
		params = i18n.Params{"Effort": effort}
	case "configuration.reasoning_color":
		params = i18n.Params{"Effort": strings.TrimPrefix(definition.Key, "admin.account_usage.reasoning_effort_color.")}
	}
	return i18n.M(definition.Label, params)
}
