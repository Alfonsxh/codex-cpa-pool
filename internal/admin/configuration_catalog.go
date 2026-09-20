package admin

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

type configurationPresentation struct {
	Group        string
	Description  string
	Unit         string
	ChoiceLabels map[string]string
}

type configurationCatalogChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type configurationCatalogField struct {
	Key            string                       `json:"key"`
	Label          string                       `json:"label"`
	Description    string                       `json:"description"`
	ValueType      string                       `json:"type"`
	Value          any                          `json:"value"`
	Default        any                          `json:"default"`
	ApplyMode      string                       `json:"apply_mode"`
	Editable       bool                         `json:"editable"`
	UnitCode       string                       `json:"unit_code,omitempty"`
	Unit           string                       `json:"unit,omitempty"`
	Minimum        *float64                     `json:"min,omitempty"`
	Maximum        *float64                     `json:"max,omitempty"`
	MinimumLength  *int                         `json:"min_length,omitempty"`
	MaximumLength  *int                         `json:"max_length,omitempty"`
	Choices        []configurationCatalogChoice `json:"choices,omitempty"`
	Configured     *bool                        `json:"configured,omitempty"`
	DigestRequired bool                         `json:"digest_required,omitempty"`
}

type configurationCatalogGroup struct {
	ID          string                      `json:"id"`
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Fields      []configurationCatalogField `json:"fields"`
}

type configurationCatalogResponse struct {
	Version     int                         `json:"version"`
	GeneratedAt int64                       `json:"generated_at"`
	FieldCount  int                         `json:"field_count"`
	Groups      []configurationCatalogGroup `json:"groups"`
}

func (server *Server) readConfiguration(c *gin.Context) {
	server.configurationLock.Lock()
	defer server.configurationLock.Unlock()

	_, values, proxy, proxyFound, err := server.currentConfiguration(c.Request.Context())
	if err != nil {
		server.internalError(c, "read configuration", err)
		return
	}

	groups := make([]configurationCatalogGroup, 0, len(configurationGroupDescriptions))
	groupIndexes := make(map[string]int, len(configurationGroupDescriptions))
	for _, definition := range configurationDefinitions {
		presentation, found := configurationPresentationByKey[definition.Key]
		if !found {
			server.internalError(c, "read configuration", errMissingConfigurationPresentation(definition.Key))
			return
		}
		groupIndex, found := groupIndexes[presentation.Group]
		if !found {
			groupIndex = len(groups)
			groupIndexes[presentation.Group] = groupIndex
			groups = append(groups, configurationCatalogGroup{
				ID: strings.TrimPrefix(presentation.Group, "admin."), Name: httpi18n.Text(c, presentation.Group), Description: httpi18n.Text(c, configurationGroupDescriptions[presentation.Group]),
				Fields: make([]configurationCatalogField, 0),
			})
		}

		field := configurationCatalogField{
			Key: definition.Key, Label: definition.label().Render(httpi18n.Locale(c)), Description: httpi18n.Text(c, presentation.Description),
			ValueType: definition.ValueType, Value: values[definition.Key], Default: definition.Default,
			ApplyMode: definition.ApplyMode, Editable: true, Unit: configurationUnit(presentation.Unit, httpi18n.Locale(c)), UnitCode: configurationUnitCode(presentation.Unit),
			DigestRequired: definition.DigestRequired,
		}
		if definition.Key == quotaResetSettingKey {
			field.Key = quotaRetentionFieldKey
			field.Value = !values[definition.Key].(bool)
			field.Default = !definition.Default.(bool)
		}
		if definition.HasMinimum {
			minimum := definition.Minimum
			field.Minimum = &minimum
		}
		if definition.HasMaximum {
			maximum := definition.Maximum
			field.Maximum = &maximum
		}
		if definition.MinimumLength > 0 {
			minimumLength := definition.MinimumLength
			field.MinimumLength = &minimumLength
		}
		if definition.MaximumLength > 0 {
			maximumLength := definition.MaximumLength
			field.MaximumLength = &maximumLength
		}
		if definition.ValueType == "choice" {
			order := append([]string(nil), definition.ChoiceOrder...)
			if len(order) == 0 {
				for value := range definition.Choices {
					order = append(order, value)
				}
				sort.Strings(order)
			}
			field.Choices = make([]configurationCatalogChoice, 0, len(order))
			for _, value := range order {
				label := presentation.ChoiceLabels[value]
				if label == "" {
					label = value
				} else {
					label = httpi18n.Text(c, label)
				}
				field.Choices = append(field.Choices, configurationCatalogChoice{Value: value, Label: label})
			}
		}
		if definition.Key == "cpa.proxy_url" {
			configured := proxyFound && strings.TrimSpace(proxy) != ""
			field.Value = ""
			field.Configured = &configured
		}
		groups[groupIndex].Fields = append(groups[groupIndex].Fields, field)
	}

	httpi18n.JSON(c, http.StatusOK, configurationCatalogResponse{
		Version: 3, GeneratedAt: time.Now().Unix(), FieldCount: len(configurationDefinitions), Groups: groups,
	})
}

type missingConfigurationPresentationError string

func (err missingConfigurationPresentationError) Error() string {
	return "missing configuration presentation: " + string(err)
}

func errMissingConfigurationPresentation(key string) error {
	return missingConfigurationPresentationError(key)
}

var configurationGroupDescriptions = map[string]string{
	"admin.system_settings":             "admin.shared_time_and_date_boundaries_across_the_site",
	"admin.brand_identity":              "admin.brand_domains_and_client_configuration",
	"admin.cpa_requests":                "admin.cpa_request_and_proxy_settings",
	"admin.usage_quotas":                "admin.quotas_and_usage_collection",
	"admin.automatic_account_switching": "admin.automatic_migration_when_quota_is_low",
	"admin.user_quotas":                 "admin.user_quotas_and_failure_policies",
	"admin.reasoning_effort_policy":     "admin.model_and_reasoning_effort_together_determine_the_user_quota_token",
	"admin.wecom_notifications":         "admin.quota_reports_and_alerts",
	"admin.sessions_collection":         "admin.session_and_collection_settings",
	"admin.account_provisioning":        "admin.port_range_for_new_cpas",
	"admin.accounts_releases":           "admin.cpa_listen_address_and_update_image",
}

var configurationPresentationByKey = map[string]configurationPresentation{
	"plugins.codex_ticket.proxy_source":                  {Group: "admin.cpa_requests", Description: "configuration.ticket_proxy_source_description", ChoiceLabels: map[string]string{"account": "configuration.ticket_proxy_account", "direct": "configuration.ticket_proxy_direct"}},
	"software.cpa_auto_check":                            {Group: "admin.cpa_requests", Description: "configuration.cpa_auto_check_description"},
	"software.plugin_auto_check":                         {Group: "admin.cpa_requests", Description: "configuration.plugin_auto_check_description"},
	"software.check_interval_hours":                      {Group: "admin.cpa_requests", Description: "configuration.check_interval_hours_description"},
	"plugins.codex_ticket.enabled":                       {Group: "admin.cpa_requests", Description: "configuration.ticket_enabled_description"},
	"plugins.codex_ticket.accounts":                      {Group: "admin.cpa_requests", Description: "configuration.ticket_accounts_description"},
	"plugins.codex_ticket.version":                       {Group: "admin.cpa_requests", Description: "configuration.ticket_version_description"},
	"plugins.codex_ticket.harvest_enabled":               {Group: "admin.cpa_requests", Description: "configuration.ticket_harvest_description"},
	"plugins.codex_ticket.inject_enabled":                {Group: "admin.cpa_requests", Description: "configuration.ticket_inject_description"},
	"plugins.codex_ticket.models":                        {Group: "admin.cpa_requests", Description: "configuration.ticket_models_description"},
	"plugins.codex_ticket.ttl_seconds":                   {Group: "admin.cpa_requests", Description: "configuration.ticket_ttl_description"},
	"plugins.codex_ticket.refresh_before_seconds":        {Group: "admin.cpa_requests", Description: "configuration.ticket_refresh_description"},
	"plugins.codex_ticket.scan_interval_seconds":         {Group: "admin.cpa_requests", Description: "configuration.ticket_scan_description"},
	"plugins.codex_ticket.timeout_seconds":               {Group: "admin.cpa_requests", Description: "configuration.ticket_timeout_description"},
	"plugins.codex_ticket.retry_base_seconds":            {Group: "admin.cpa_requests", Description: "configuration.ticket_retry_description"},
	"plugins.codex_ticket.retry_max_seconds":             {Group: "admin.cpa_requests", Description: "configuration.ticket_retry_max_description"},
	i18n.SettingKey:                                      {Group: "admin.system_settings", Description: "configuration.system_language_description", ChoiceLabels: map[string]string{"en": "configuration.language_english", "zh-CN": "configuration.language_chinese"}},
	"branding.product_name":                              {Group: "admin.brand_identity", Description: "admin.product_name_shown_on_pages"},
	"branding.short_name":                                {Group: "admin.brand_identity", Description: "admin.short_name_displayed_in_clients"},
	"branding.environment_label":                         {Group: "admin.brand_identity", Description: "admin.portal_environment_label_may_be_left_blank"},
	"branding.public_base_url":                           {Group: "admin.brand_identity", Description: "admin.url_used_in_notifications_and_exports_leave_blank_to_use"},
	"identity.allowed_email_domains":                     {Group: "admin.brand_identity", Description: "admin.separate_with_commas_configure_at_least_one_domain_before_creating"},
	"identity.key_prefix":                                {Group: "admin.brand_identity", Description: "admin.prefix_for_new_keys_ending_with_an_underscore"},
	"portal.provider_name":                               {Group: "admin.brand_identity", Description: "admin.client_provider_name_2"},
	"portal.api_key_env":                                 {Group: "admin.brand_identity", Description: "admin.shell_variable_for_the_key"},
	"portal.default_model":                               {Group: "admin.brand_identity", Description: "admin.default_client_model_2"},
	reasoningpolicy.SettingKey:                           {Group: "admin.cpa_requests", Description: "configuration.max_reasoning_effort_description", ChoiceLabels: map[string]string{"unlimited": "configuration.no_reasoning_limit"}},
	"cpa.proxy_enabled":                                  {Group: "admin.cpa_requests", Description: "admin.used_only_by_cpas_that_inherit_the_default_proxy"},
	"cpa.proxy_url":                                      {Group: "admin.cpa_requests", Description: "admin.default_proxy_url_http_https_socks5"},
	"cpa.request_retry":                                  {Group: "admin.cpa_requests", Description: "admin.retries_after_upstream_failures"},
	"cpa.disable_image_generation":                       {Group: "admin.cpa_requests", Description: "admin.image_tool_availability_policy", ChoiceLabels: map[string]string{"chat": "admin.disable_in_regular_chats_only_recommended", "true": "admin.disable_all", "false": "admin.enable_all"}},
	"cpa.max_retry_credentials":                          {Group: "admin.cpa_requests", Description: "admin.maximum_credential_switches_per_attempt"},
	"cpa.max_retry_interval":                             {Group: "admin.cpa_requests", Description: "admin.maximum_wait_for_credentials_in_cooldown", Unit: "admin.sec"},
	"cpa.transient_error_cooldown_seconds":               {Group: "admin.cpa_requests", Description: "admin.cooldown_after_transient_errors", Unit: "admin.sec"},
	"cpa.session_affinity":                               {Group: "admin.cpa_requests", Description: "configuration.session_affinity_description"},
	"cpa.passthrough_headers":                            {Group: "admin.cpa_requests", Description: "configuration.passthrough_headers_description"},
	"cpa.session_affinity_ttl":                           {Group: "admin.cpa_requests", Description: "configuration.session_affinity_ttl_description"},
	"cpa.debug":                                          {Group: "admin.cpa_requests", Description: "configuration.debug_description"},
	"cpa.logging_to_file":                                {Group: "admin.cpa_requests", Description: "configuration.logging_to_file_description"},
	"cpa.logs_max_total_size_mb":                         {Group: "admin.cpa_requests", Description: "admin.per_cpa_limit_oldest_logs_are_deleted_when_exceeded", Unit: "MiB"},
	"cpa.error_logs_max_files":                           {Group: "admin.cpa_requests", Description: "admin.maximum_error_log_files_per_cpa", Unit: "admin.items"},
	"cpa.usage_statistics_enabled":                       {Group: "admin.usage_quotas", Description: "configuration.usage_statistics_enabled_description"},
	"cpa.usage_queue_retention_seconds":                  {Group: "admin.usage_quotas", Description: "admin.event_retention_during_interruptions", Unit: "admin.sec"},
	usage.ActiveUserWindowSettingKey:                     {Group: "admin.usage_quotas", Description: "admin.rolling_window_for_counting_unique_active_users_per_account", Unit: "admin.sec"},
	"usage.quota_cache_seconds":                          {Group: "admin.usage_quotas", Description: "admin.official_quota_cache_duration", Unit: "admin.sec"},
	"usage.upstream_timeout_seconds":                     {Group: "admin.usage_quotas", Description: "admin.official_api_request_timeout", Unit: "admin.sec"},
	"account_failover.mode":                              {Group: "admin.automatic_account_switching", Description: "admin.automatically_move_users_when_official_weekly_quota_is_exhausted", ChoiceLabels: map[string]string{"off": "admin.close", "active": "admin.automatic"}},
	"account_failover.poll_seconds":                      {Group: "admin.automatic_account_switching", Description: "admin.official_quota_check_interval", Unit: "admin.sec"},
	"account_failover.reserve_percent":                   {Group: "admin.automatic_account_switching", Description: "admin.accounts_at_or_below_this_remaining_quota_do_not_receive", Unit: "%"},
	"account_failover.stale_after_seconds":               {Group: "admin.automatic_account_switching", Description: "admin.stop_migration_when_quota_data_expires", Unit: "admin.sec"},
	"user_quota.default_weekly_tokens":                   {Group: "admin.user_quotas", Description: "admin.weighted_limit_per_user_per_calendar_week_blank_means_unlimited", Unit: "Token"},
	quotaResetSettingKey:                                 {Group: "admin.user_quotas", Description: "admin.applies_to_all_users_when_off_this_week_s_quota"},
	"system.timezone":                                    {Group: "admin.system_settings", Description: "admin.used_for_page_times_daily_usage_calendar_week_quotas_and"},
	"user_quota.fail_open_after_seconds":                 {Group: "admin.user_quotas", Description: "admin.allow_requests_and_alert_after_a_prolonged_collection_failure", Unit: "admin.sec"},
	"user_quota.reasoning_multiplier.none":               {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.minimal":            {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.low":                {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.medium":             {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.high":               {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.xhigh":              {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.max":                {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.ultra":              {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.auto":               {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"user_quota.reasoning_multiplier.unknown":            {Group: "admin.reasoning_effort_policy", Description: "admin.token_multiplier_for_newly_collected_events", Unit: "admin.label"},
	"admin.account_usage.reasoning_effort_color.none":    {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.minimal": {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.low":     {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.medium":  {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.high":    {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.xhigh":   {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.max":     {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.ultra":   {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.auto":    {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"admin.account_usage.reasoning_effort_color.unknown": {Group: "admin.reasoning_effort_policy", Description: "admin.display_color_in_account_details"},
	"notification.enabled":                               {Group: "admin.wecom_notifications", Description: "admin.send_wecom_notifications"},
	"notification.daily_times":                           {Group: "admin.wecom_notifications", Description: "admin.hh_mm_format_separate_multiple_times_with_commas"},
	"notification.schedule_grace_minutes":                {Group: "admin.wecom_notifications", Description: "admin.how_long_a_missed_scheduled_notification_may_still_be_sent", Unit: "admin.minutes"},
	"notification.quota_alert_enabled":                   {Group: "admin.wecom_notifications", Description: "admin.send_weekly_quota_alerts"},
	"notification.weekly_threshold_percent":              {Group: "admin.wecom_notifications", Description: "admin.alert_when_account_weekly_usage_reaches_this_percentage", Unit: "%"},
	"portal.session_ttl_seconds":                         {Group: "admin.sessions_collection", Description: "admin.affects_only_sessions_created_after_saving", Unit: "admin.sec"},
	"collector.interval_seconds":                         {Group: "admin.sessions_collection", Description: "admin.collection_polling_interval_2", Unit: "admin.sec"},
	"collector.batch_size":                               {Group: "admin.sessions_collection", Description: "admin.maximum_events_collected_per_cpa_in_each_batch"},
	"accounts.port_start":                                {Group: "admin.account_provisioning", Description: "admin.start_port_for_new_cpas"},
	"accounts.port_end":                                  {Group: "admin.account_provisioning", Description: "admin.end_port_for_new_cpas_must_not_be_below_the"},
	"accounts.listen_address":                            {Group: "admin.accounts_releases", Description: "admin.only_host_loopback_addresses_are_allowed"},
	"runtime.cliproxy_image":                             {Group: "admin.accounts_releases", Description: "admin.pull_and_verify_updates_in_accounts"},
}

func init() {
	for _, model := range usage.ModelMultiplierDefinitions() {
		description := "admin.model_token_multiplier_for_newly_collected_events"
		if model.Model == "unknown" {
			description = "admin.token_multiplier_for_newly_collected_events_with_an_unmatched_model"
		}
		configurationPresentationByKey[usage.ModelMultiplierSettingKey(model.Model)] = configurationPresentation{
			Group: "admin.reasoning_effort_policy", Description: description, Unit: "admin.label",
		}
	}
}

func configurationUnit(value string, lang i18n.Language) string {
	if strings.HasPrefix(value, "admin.") {
		return i18n.Text(lang, value)
	}
	return value
}

// Codes describe the unit independently of its translated label.
func configurationUnitCode(value string) string {
	switch value {
	case "admin.sec":
		return "second"
	case "admin.minutes":
		return "minute"
	case "admin.label":
		return "multiplier"
	case "Token":
		return "token"
	case "%":
		return "percent"
	default:
		return value
	}
}
