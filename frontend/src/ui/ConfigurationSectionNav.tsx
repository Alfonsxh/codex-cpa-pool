import "../i18n/admin";
import { t } from "../i18n";
const configurationSections = [
  { to: "/configuration", index: "01", label: t("admin.runtime_configuration"), description: t("admin.routing_quotas_account_parameters") },
  { to: "/settings", index: "02", label: t("admin.general_settings"), description: t("admin.brand_identity_security") },
  { to: "/notifications", index: "03", label: t("admin.notifications_2"), description: t("admin.wecom_alert_rules") }
] as const;

export function ConfigurationSectionNav() {
  const pathname = window.location.pathname;

  return (
    <nav className="configuration-section-nav" aria-label={t("admin.configuration_center_page")}>
      {configurationSections.map((section) => {
        const active = pathname.startsWith(`/admin${section.to}`) || pathname.startsWith(section.to);
        return (
          <a
            key={section.to}
            className={active ? "active" : ""}
            href={`/admin${section.to}`}
            aria-current={active ? "page" : undefined}
          >
            <span className="configuration-section-index" aria-hidden="true">{section.index}</span>
            <span className="configuration-section-copy">
              <strong>{section.label}</strong>
              <small>{section.description}</small>
            </span>
            <span className="configuration-section-arrow" aria-hidden="true">›</span>
          </a>
        );
      })}
    </nav>
  );
}
