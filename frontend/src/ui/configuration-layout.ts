import "../i18n/admin";
import { t } from "../i18n";
import type { ConfigurationField } from "../api/configuration";

// Presentation only: the catalog remains authoritative for values, validation and effects.
export const configurationCategories = [
  { name: "admin.brand_identity", eyebrow: "BRAND & IDENTITY", description: t("admin.site_branding_organization_identity_client_exports") },
  { name: "admin.system_settings", eyebrow: "SYSTEM SETTINGS", description: t("admin.time_sign_in_security_display_preferences") },
  { name: "admin.requests_accounts", eyebrow: "REQUESTS & ACCOUNTS", description: t("admin.request_behavior_account_switching_runtime_parameters") },
  { name: "admin.usage_quotas", eyebrow: "USAGE & QUOTAS", description: t("admin.user_quotas_billing_multipliers_usage_collection") },
  { name: "admin.notifications_2", eyebrow: "NOTIFICATIONS", description: t("admin.wecom_notifications_schedules_quota_alerts") },
  { name: "admin.data_audit", eyebrow: "DATA & AUDIT", description: t("admin.safety_archives_storage_status_admin_history") }
] as const;
export type ConfigurationCategory = typeof configurationCategories[number]["name"];

// Only the control width varies; the shared configuration grid owns its position.
export function configurationControlWidth(field: ConfigurationField): number | "100%" | "fit-content" {
  if (field.unit === "Token") return "100%";
  if (["integer", "number", "nullable_integer", "time_list", "duration", "boolean"].includes(field.type)) return "fit-content";
  if (field.type === "color") return 144;
  if (field.type === "timezone") return 360;
  if (field.type === "choice") {
    const longestLabel = Math.max(0, ...(field.choices ?? []).map((choice) =>
      [...`${choice.label} · ${choice.value}`].reduce((width, character) => width + (character.charCodeAt(0) <= 127 ? 7.5 : 12), 0)
    ));
    return Math.min(560, Math.max(180, Math.ceil((longestLabel + 56) / 40) * 40));
  }
  return "100%";
}

export const configurationSections: Array<{
  id: string; category: ConfigurationCategory; title: string; description: string;
}> = [
  { id: "brand", category: "admin.brand_identity", title: t("admin.site_branding"), description: t("admin.page_names_logo_public_url") },
  { id: "identity", category: "admin.brand_identity", title: t("admin.organization_identity"), description: t("admin.organization_email_domains_new_api_key_prefix") },
  { id: "client", category: "admin.brand_identity", title: t("admin.client_exports"), description: t("admin.provider_environment_variables_default_model") },
  { id: "general", category: "admin.system_settings", title: t("admin.time_sign_in"), description: t("admin.business_timezone_portal_session_duration") },
  { id: "access", category: "admin.system_settings", title: t("admin.access_credentials"), description: t("admin.management_key_initial_user_password") },
  { id: "appearance", category: "admin.system_settings", title: t("admin.display_preferences"), description: t("admin.reasoning_effort_colors_in_account_details") },
  { id: "requests", category: "admin.requests_accounts", title: t("admin.requests_proxies"), description: t("admin.default_upstream_proxy_retries_image_tools") },
  { id: "software", category: "admin.requests_accounts", title: t("admin.software_title"), description: t("admin.software_description") },
  { id: "cpa-container", category: "admin.requests_accounts", title: t("admin.cpa_container_settings"), description: t("admin.cpa_container_settings_description") },
  { id: "failover", category: "admin.requests_accounts", title: t("admin.automatic_account_switching"), description: t("admin.migration_policy_when_official_quotas_are_exhausted") },
  { id: "provisioning", category: "admin.requests_accounts", title: t("admin.provisioning_runtime"), description: t("admin.new_account_ports_listen_address_update_image") },
  { id: "logging", category: "admin.requests_accounts", title: t("admin.logs_diagnostics"), description: t("admin.debug_settings_file_logs_retention_limits") },
  { id: "quota", category: "admin.usage_quotas", title: t("admin.quota"), description: t("admin.user_quotas_official_quota_queries_usage_maintenance") },
  { id: "model-multipliers", category: "admin.usage_quotas", title: t("admin.model_multipliers"), description: t("admin.user_quota_multipliers_for_each_model_and_unmatched_models") },
  { id: "reasoning-multipliers", category: "admin.usage_quotas", title: t("admin.reasoning_multipliers"), description: t("admin.user_quota_multipliers_for_each_reasoning_effort") },
  { id: "collection", category: "admin.usage_quotas", title: t("admin.usage_collection"), description: t("admin.collection_toggle_polling_batches_event_retention") },
  { id: "notifications", category: "admin.notifications_2", title: t("admin.wecom_notifications"), description: t("admin.notification_toggle_webhook_schedules_quota_alerts") },
  { id: "backups", category: "admin.data_audit", title: t("admin.safety_archives"), description: t("admin.archive_count_latest_archive") },
  { id: "storage", category: "admin.data_audit", title: t("admin.local_data"), description: t("admin.persistent_paths_permissions") },
  { id: "audit", category: "admin.data_audit", title: t("admin.audit_log"), description: t("admin.recent_configuration_maintenance_operations") }
];

const legacySections: Record<string, string> = {
  "affinity": "cpa-container",
  "brand_identity": "brand",
  "system_settings": "general",
  "cpa_requests": "requests",
  "automatic_account_switching": "failover",
  "user_quota": "quota",
  "reasoning_effort_policy": "reasoning-multipliers",
  "usage_quota": "collection",
  "wecom_notifications": "notifications",
  "sessions_collection": "general",
  "account_provisioning": "provisioning",
  "accounts_releases": "provisioning",

  "品牌与身份": "brand", "系统设置": "general", "CPA 请求": "requests", "账号自动切换": "failover",
  "用户额度": "quota", "推理强度策略": "reasoning-multipliers", "用量与额度": "collection", "企业微信通知": "notifications",
  "multipliers": "model-multipliers", "模型与推理倍率": "model-multipliers",
  "quota-cache": "quota", "官方额度查询": "quota",
  "quota-reset": "quota", "用量维护": "quota",
  "schedule": "notifications", "发送计划": "notifications", "alerts": "notifications", "额度预警": "notifications",
  "会话与采集": "general", "账号供应": "provisioning", "账号与发布": "provisioning"
};

export function configurationSectionFor(field: Pick<ConfigurationField, "key">, originalGroup: string) {
  const key = field.key;
  const id = key.startsWith("plugins.") || key.startsWith("software.") ? "software" : ["cpa.debug", "cpa.logging_to_file", "cpa.usage_statistics_enabled", "cpa.passthrough_headers", "cpa.session_affinity", "cpa.session_affinity_ttl"].includes(key) ? "cpa-container"
    : key.startsWith("branding.") ? "brand"
    : key.startsWith("identity.") ? "identity"
    : key === "portal.session_ttl_seconds" || key.startsWith("system.") ? "general"
    : key.startsWith("portal.") ? "client"
    : key.startsWith("admin.account_usage.reasoning_effort_color.") ? "appearance"
    : key.startsWith("user_quota.model_multiplier.") ? "model-multipliers"
    : key.startsWith("user_quota.reasoning_multiplier.") ? "reasoning-multipliers"
    : key.startsWith("user_quota.") ? "quota"
    : key.startsWith("collector.") || key.startsWith("cpa.usage_") ? "collection"
    : key.startsWith("usage.") ? "quota"
    : key.startsWith("account_failover.") ? "failover"
    : key.startsWith("accounts.") || key.startsWith("runtime.") ? "provisioning"
    : ["cpa.logs_max_total_size_mb", "cpa.error_logs_max_files"].includes(key) ? "logging"
    : key.startsWith("cpa.") || key === "gateway.max_reasoning_effort" ? "requests"
    : key.startsWith("notification.") ? "notifications"
    : legacySections[originalGroup] ?? "general";
  return configurationSections.find((section) => section.id === id)!;
}

export function legacyConfigurationSection(group: string) {
  return configurationSections.find((section) => section.id === legacySections[group]);
}
