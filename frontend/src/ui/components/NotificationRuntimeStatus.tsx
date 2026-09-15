import "../../i18n/admin";
import { t } from "../../i18n";
import type { NotificationStatus } from "../../api/notifications";
import { formatSiteTimestamp } from "../site-time";
import "./notification-runtime-status.css";

export function NotificationRuntimeStatus({ status, enabled, unavailable = false }: {
  status: NotificationStatus;
  enabled: boolean;
  unavailable?: boolean;
}) {
  const worker = unavailable ? undefined : status.worker_status;
  const running = worker === "running";
  const ready = running && enabled && status.webhook_configured;
  const label = running ? (enabled ? t("common.heartbeat_healthy") : t("common.idle"))
    : worker === "heartbeat_lost" ? t("common.heartbeat_interrupted")
      : worker === "not_started" ? t("common.no_heartbeat") : t("common.unknown_status");
  const warning = unavailable ? t("common.unable_to_refresh_scheduler_status_please_try_again_later")
    : worker === "heartbeat_lost" ? t("common.the_scheduler_heartbeat_has_stopped_automatic_notifications_may_be_paused")
      : worker === "not_started" ? t("common.no_scheduler_heartbeat_received_check_that_the_notification_service_is")
        : !worker ? t("common.this_backend_does_not_provide_scheduler_status_upgrade_it_to")
          : enabled && !status.webhook_configured ? t("common.no_webhook_configured_automatic_notifications_are_paused") : "";
  return <section className="notification-runtime-status" aria-label={t("common.notification_runtime_status")}>
    <div className="notification-runtime-grid">
      <div><span>{t("common.notifications")}</span><strong>{enabled ? t("common.enabled") : t("common.off")}</strong></div>
      <div><span>{t("common.background_scheduler")}</span><strong className={`status-chip ${running ? "success" : "warning"}`}>{label}</strong></div>
      <div><span>{t("common.last_heartbeat")}</span><strong>{formatSiteTimestamp(status.heartbeat_at)}</strong></div>
      <div title={t("common.includes_manual_and_automatic_delivery")}><span>{t("common.last_successful_delivery")}</span><strong>{formatSiteTimestamp(status.last_success_at)}</strong></div>
      <div><span>{t("common.next_delivery")}</span><strong>{ready ? formatSiteTimestamp(status.next_schedule_at) : "—"}</strong></div>
    </div>
    {warning ? <p className="notification-runtime-warning" role="status">{warning}</p> : null}
    {status.last_error ? <p className="notification-runtime-error">{t("common.last_delivery_error")}{status.last_error}</p> : null}
  </section>;
}
