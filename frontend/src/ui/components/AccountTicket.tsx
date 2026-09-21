import "../../i18n/admin";
import { t } from "../../i18n";
import { Alert, Button, Drawer, Modal, Skeleton } from "antd";
import { useState } from "react";
import { Link } from "react-router-dom";
import type { ExtensionStatus } from "../../api/generated";
import { formatSiteTimestamp } from "../site-time";
import { TaskReport, useSoftwareJob, useSoftwareStatus } from "./SoftwareVersions";
import "./account-ticket.css";

type TicketAccount = ExtensionStatus["accounts"][number];
const active = (status?: string) => ["queued", "running", "cancelling"].includes(status ?? "");

function installationState(row: TicketAccount | undefined, loading: boolean, failed: boolean) {
  return failed ? "unknown" : loading ? "loading" : !row ? "unknown"
    : active(row.job?.status) ? "installing" : row.job?.status === "failed" ? "failed"
    : row.installation.staging ? "recovery" : row.installation.version ? "installed" : "missing";
}

function AccountTicketBadge({ row, loading, failed }: { row?: TicketAccount; loading: boolean; failed: boolean }) {
  const key = installationState(row, loading, failed);
  return <span className={`status-chip ${["failed", "recovery", "missing"].includes(key) ? "warning" : "neutral"}`}>
    {t(`admin.account_ticket_${key}`)}
  </span>;
}

export function AccountTicketLink({ account, onOpen }: { account: string; onOpen: () => void }) {
  const status = useSoftwareStatus();
  const row = status.data?.accounts.find(item => item.account === account);
  const key = installationState(row, status.isPending, status.isError);
  return <div>
    <button type="button" className="account-ticket-link" aria-haspopup="dialog" onClick={onOpen}>Codex Ticket</button>
    <small className="account-runtime-note">{t(`admin.account_ticket_${key}`)}{row?.installation.version ? ` · ${row.installation.version}` : ""}</small>
  </div>;
}

export function AccountTicketDrawer({ account, csrfToken, onClose }: {
  account: { id: string; email: string } | null;
  csrfToken: string;
  onClose: () => void;
}) {
  return <Drawer
    className="account-ticket-drawer"
    title={<div className="account-ticket-drawer-title"><strong>{t("admin.account_ticket_details")}</strong><small>{account?.id}{account?.email ? ` · ${account.email}` : ""}</small></div>}
    placement="right"
    size="min(1000px, 100vw)"
    open={account !== null}
    onClose={onClose}
    destroyOnHidden
  >
    {account ? <AccountTicketPanel key={account.id} account={account.id} csrfToken={csrfToken} /> : null}
  </Drawer>;
}

function AccountTicketPanel({ account, csrfToken }: { account: string; csrfToken: string }) {
  const status = useSoftwareStatus(account);
  const { action, task, job, busy } = useSoftwareJob(csrfToken);
  const [confirm, setConfirm] = useState(false);
  const row = status.data?.accounts.find(item => item.account === account);
  const runtime = row?.runtime;
  const installing = busy || active(row?.job?.status);
  const canInstall = !!row?.enabled && row.running && !status.isError && !installing;
  const current = !!row?.installation.version && row.installation.version === status.data?.desired_version;
  const label = row?.installation.staging || row?.job?.status === "failed" ? "admin.account_ticket_retry"
    : !row?.installation.version ? "admin.ticket_install_plugin"
    : current ? "admin.account_ticket_reinstall" : "admin.ticket_upgrade_plugin";
  const injected = runtime?.entries.reduce((sum, entry) => sum + entry.injected_count, 0) ?? 0;
  const runtimeKnown = !status.isError && runtime?.state === "ready";
  return <section className="account-ticket-panel" aria-label={t("admin.account_ticket_details")}>
    <div className="account-ticket-toolbar">
      <Link to="/configuration?section=codex-ticket">{t("admin.account_ticket_settings")}</Link>
      <Button size="small" aria-label={t("admin.refresh_page")} loading={status.isFetching} onClick={() => void status.refetch()}>{t("admin.refresh_page")}</Button>
    </div>
    <div className="account-ticket-table-scroll" tabIndex={0} aria-label={t("admin.account_ticket_details")}>
      <table className="account-ticket-table" aria-label={t("admin.account_ticket_overview")}>
        <thead><tr>{["plugin", "version", "status", "harvest", "cache", "injection", "operation"].map(key => <th scope="col" key={key}>{t(`admin.account_ticket_${key}`)}</th>)}</tr></thead>
        <tbody><tr>
          <th scope="row">Codex Ticket</th>
          <td><code>{row?.installation.version || "—"}</code>{runtimeKnown && runtime.version && runtime.version !== row?.installation.version.replace(/^v/, "") ? <small>{t("admin.account_ticket_loaded")} {runtime.version}</small> : null}</td>
          <td><AccountTicketBadge row={row} loading={status.isPending} failed={status.isError} /></td>
          <td>{runtimeKnown ? t(runtime.harvest_active ? "admin.account_ticket_active" : "admin.account_ticket_inactive") : "—"}</td>
          <td className="is-number">{runtimeKnown ? runtime.cached_count : "—"}</td>
          <td>{runtimeKnown ? `${t(runtime.inject_active ? "admin.account_ticket_active" : "admin.account_ticket_inactive")} · ${injected}` : "—"}</td>
          <td><button type="button" className="button button-quiet account-ticket-action" aria-label={installing ? t("admin.account_ticket_installing") : t(label)} aria-busy={installing} disabled={!canInstall} onClick={() => setConfirm(true)}>{installing ? t("admin.account_ticket_installing") : t(label)}</button></td>
        </tr></tbody>
      </table>
    </div>
    {status.isPending ? <Skeleton active title={false} paragraph={{ rows: 2 }} /> : null}
    {status.isError ? <Alert type="error" showIcon title={t("admin.software_load_failed")} /> : null}
    {row && !row.running ? <p className="account-ticket-note">{t("admin.account_ticket_stopped")}</p> : row && !row.enabled ? <p className="account-ticket-note">{t("admin.account_ticket_disabled")}</p> : null}
    {row && !row.installation.version ? <p className="account-ticket-note">{t("admin.account_ticket_missing_help")}</p> : null}
    {runtime && runtime.state !== "ready" && runtime.state !== "stopped" && row?.installation.version ? <Alert type="warning" showIcon title={t(`admin.account_ticket_runtime_${runtime.state}`)} /> : null}
    {runtimeKnown ? <>
      {!runtime.cached_count ? <p className="account-ticket-note is-warning">{t("admin.account_ticket_empty_cache")}</p> : null}
      {runtime.entries.length ? <div className="account-ticket-table-scroll" tabIndex={0} aria-label={t("admin.account_ticket_models")}>
        <table className="account-ticket-table account-ticket-models" aria-label={t("admin.account_ticket_models")}>
          <thead><tr>{["model", "http", "length", "result", "count", "retry_wait"].map(key => <th scope="col" key={key}>{t(`admin.account_ticket_${key}`)}</th>)}</tr></thead>
          <tbody>{runtime.entries.map((entry, index) => <tr key={`${entry.model}-${index}`}>
            <th scope="row">{entry.model}</th>
            <td className="is-number">{entry.last_http || "—"}</td>
            <td className="is-number">{entry.last_length || "—"}</td>
            <td>{entry.reason === "length_mismatch" ? t("admin.account_ticket_length_mismatch") : entry.reason === "" ? "—" : t("admin.account_ticket_reason", [entry.reason])}</td>
            <td className="is-number" aria-label={t("admin.account_ticket_injected", [entry.injected_count])}>{entry.injected_count}</td>
            <td>{entry.backoff_seconds > 0 ? t("admin.account_ticket_backoff", [entry.backoff_seconds]) : "—"}</td>
          </tr>)}</tbody>
        </table>
      </div> : null}
    </> : null}
    {runtime?.checked_at ? <small className="account-ticket-time">{t("admin.account_ticket_observed", [formatSiteTimestamp(runtime.checked_at)])}</small> : null}
    {task ? <TaskReport task={task} /> : row?.job?.status === "failed" ? <Alert type="error" title={t("admin.account_ticket_failed")} description={row.job.error} /> : null}
    {action.isError ? <Alert type="error" title={action.error.message} /> : null}
    {job.isError ? <Alert type="error" title={t("admin.software_job_unavailable")} action={<Button onClick={() => void job.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
    <Modal open={confirm} title={t(label)} onCancel={() => !action.isPending && setConfirm(false)} confirmLoading={action.isPending} okButtonProps={{ disabled: !canInstall }} onOk={() => { if (canInstall) action.mutate({ kind: "plugin-update", account }, { onSuccess: () => setConfirm(false) }); }}>
      <p>{t("admin.software_confirm", [account, status.data?.desired_version ?? ""])}</p>
    </Modal>
  </section>;
}
