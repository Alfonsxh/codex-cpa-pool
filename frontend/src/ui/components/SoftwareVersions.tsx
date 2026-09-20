import "../../i18n/admin";
import { t } from "../../i18n";
import { Alert, Button, Input, Modal, Select, Skeleton } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useEffect, useState, type ReactNode } from "react";
import { apiRequest } from "../../api/client";
import type { ExtensionStatus, RuntimeJob } from "../../api/generated";
import { readRuntimeJob, submitRuntimeJob } from "../../api/runtime";
import { formatSiteTimestamp, useSiteTimezone } from "../site-time";
import "./software-versions.css";

const pendingStates = ["queued", "running", "cancelling"];
export const softwareVersionsQueryKey = ["software-versions"];
const prefix = "plugins.codex_ticket.";
const basicKeys = ["enabled", "models", "harvest_enabled", "inject_enabled", "proxy_source"];
const advancedKeys = ["ttl_seconds", "refresh_before_seconds", "scan_interval_seconds", "timeout_seconds", "retry_base_seconds", "retry_max_seconds"];
const taskLabels: Record<string, string> = {
  queued: "admin.queued", running: "admin.running", cancelling: "admin.software_cancelling",
  succeeded: "admin.software_succeeded", failed: "admin.task_failed", cancelled: "admin.software_cancelled"
};
export function ticketAccountIDs(value: string) {
  return [...new Set(value.split(",").map(id => id.trim()).filter(Boolean))];
}

function useSoftwareStatus() {
  return useQuery({ queryKey: softwareVersionsQueryKey, queryFn: ({ signal }) => apiRequest<ExtensionStatus>("/admin/api/extensions", { signal }), refetchInterval: 15000 });
}

function useSoftwareJob(csrfToken: string) {
  const client = useQueryClient();
  const [jobID, setJobID] = useState("");
  const job = useQuery({ queryKey: ["software-job", jobID], queryFn: ({ signal }) => readRuntimeJob(jobID, signal), enabled: !!jobID, refetchInterval: q => pendingStates.includes(q.state.data?.job.status ?? "queued") ? 1000 : false });
  const action = useMutation({
    mutationFn: ({ kind, account }: { kind: "version-check" | "plugin-update"; account: string }) => submitRuntimeJob(kind, account, csrfToken),
    onSuccess: result => { setJobID(result.job.id); client.setQueryData(["software-job", result.job.id], { job: result.job }); void client.invalidateQueries({ queryKey: softwareVersionsQueryKey }); }
  });
  const task = job.data?.job;
  useEffect(() => {
    if (task && !pendingStates.includes(task.status)) void client.invalidateQueries({ queryKey: softwareVersionsQueryKey });
  }, [client, task?.id, task?.status]);
  return { action, task, job, busy: action.isPending || !!jobID && (!task || pendingStates.includes(task.status)) };
}

type TicketSettingsProps = {
  csrfToken: string;
  renderField: (key: string) => ReactNode;
  tableHeader: ReactNode;
  accounts: string;
  accountsError?: string;
  onAccountsChange: (value: string) => void;
  hasUnsavedChanges: boolean;
  saving: boolean;
  focusKey: string;
};

export function TicketPluginSettings({ csrfToken, renderField, tableHeader, accounts, accountsError, onAccountsChange, hasUnsavedChanges, saving, focusKey }: TicketSettingsProps) {
  useSiteTimezone();
  const status = useSoftwareStatus();
  const { action, task, job, busy } = useSoftwareJob(csrfToken);
  const [target, setTarget] = useState("");
  const [search, setSearch] = useState("");
  const [filter, setFilter] = useState("all");
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const selected = ticketAccountIDs(accounts);
  const targetRow = status.data?.accounts.find(row => row.account === target);
  const canInstall = (row: ExtensionStatus["accounts"][number]) => !status.isError && !busy && !saving && !hasUnsavedChanges && row.enabled && row.running && row.selected && selected.includes(row.account);
  useEffect(() => {
    if (advancedKeys.some(key => prefix + key === focusKey)) setAdvancedOpen(true);
    if (focusKey === prefix + "accounts") { setSearch(""); setFilter("all"); }
  }, [focusKey]);
  const rows = status.data?.accounts.filter(row => row.account.toLowerCase().includes(search.trim().toLowerCase()) && (filter === "all" || selected.includes(row.account))) ?? [];
  const missing = status.data ? selected.filter(id => !status.data.accounts.some(row => row.account === id)) : [];
  const toggleAccount = (id: string, checked: boolean) => onAccountsChange((checked ? [...selected, id] : selected.filter(item => item !== id)).join(","));

  return <div className="ticket-settings">
    <div className="configuration-fields">{tableHeader}{basicKeys.map(key => <Fragment key={key}>{renderField(prefix + key)}</Fragment>)}</div>
    <details className="ticket-advanced" open={advancedOpen} onToggle={event => setAdvancedOpen(event.currentTarget.open)}>
      <summary>{t("admin.ticket_advanced")}</summary>
      <div className="configuration-fields">{advancedKeys.map(key => <Fragment key={key}>{renderField(prefix + key)}</Fragment>)}</div>
    </details>
    <div className="ticket-version-settings">
      {renderField(prefix + "version")}
      <div className="software-release-inline">
        <ReleaseSummary check={status.data?.checks["codex-ticket"]} loading={status.isPending} />
        <Button icon={<ReloadOutlined aria-hidden="true" />} disabled={busy || saving} loading={action.isPending && action.variables?.kind === "version-check"} onClick={() => action.mutate({ kind: "version-check", account: "all" })}>{t("admin.ticket_check_updates")}</Button>
      </div>
      {task?.action === "version-check" ? <TaskReport task={task} /> : null}
      {renderField("software.plugin_auto_check")}
      {renderField("software.check_interval_hours")}
    </div>
    <section className="ticket-accounts" aria-label={t("admin.ticket_accounts_label")} data-configuration-field={prefix + "accounts"}>
      <div className="ticket-account-tools">
        <Input aria-label={t("admin.ticket_search_accounts")} placeholder={t("admin.ticket_search_accounts")} value={search} onChange={event => setSearch(event.target.value)} allowClear onKeyDown={event => { if (event.key === "Enter") event.preventDefault(); }} />
        <Select aria-label={t("admin.ticket_filter_accounts")} value={filter} onChange={setFilter} options={[{ value: "all", label: t("admin.ticket_all_accounts") }, { value: "selected", label: t("admin.ticket_selected_accounts") }]} />
      </div>
      {accountsError ? <p role="alert" className="form-error">{accountsError}</p> : null}
      {hasUnsavedChanges ? <p className="ticket-save-hint" role="status">{t("admin.ticket_save_before_install")}</p> : null}
      {status.isPending ? <Skeleton active paragraph={{ rows: 3 }} title={false} /> : null}
      {status.isError ? <Alert type="error" showIcon title={t("admin.software_load_failed")} action={<Button onClick={() => void status.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
      {status.data ? <>
        <table className="ticket-account-table">
          <thead><tr>{["admin.ticket_applies", "admin.software_account", "admin.software_installed_version", "admin.ticket_account_status", "admin.software_operation"].map(key => <th scope="col" key={key}>{t(key)}</th>)}</tr></thead>
          <tbody>{rows.map(row => {
            const current = row.installation.version === status.data.desired_version && !row.installation.staging;
            const label = row.installation.staging ? t("admin.software_resume") : row.installation.version ? t("admin.ticket_upgrade_plugin") : t("admin.ticket_install_plugin");
            const rowTask = task?.action === "plugin-update" && task.target === row.account ? task : undefined;
            return <Fragment key={row.account}>
              <tr className="ticket-account-row" data-account={row.account}>
                <td className="ticket-account-select"><input type="checkbox" aria-label={t("admin.ticket_select_account", [row.account])} checked={selected.includes(row.account)} disabled={saving || busy} onChange={event => toggleAccount(row.account, event.target.checked)} /></td>
                <th scope="row" className="ticket-account-name">{row.account}</th>
                <td className="ticket-account-version" data-label={t("admin.software_installed_version")}>{row.installation.version || t("admin.software_not_installed")}</td>
                <td className="ticket-account-status" data-label={t("admin.ticket_account_status")}><span className={`software-state ${row.enabled && row.running ? "is-ready" : "is-muted"}`}>{t(!row.enabled ? "admin.software_disabled" : !row.running ? "admin.software_stopped" : "admin.ticket_account_running")}</span>{row.installation.staging ? <small>{t("admin.software_recovery_needed")}</small> : null}</td>
                <td className="ticket-account-action">{current ? <span className="ticket-current">{t("admin.software_version_matched")}</span> : <Button disabled={!canInstall(row)} onClick={() => setTarget(row.account)}>{label}</Button>}</td>
              </tr>
              {rowTask ? <tr className="ticket-account-task"><td colSpan={5}><TaskReport task={rowTask} /></td></tr> : null}
            </Fragment>;
          })}</tbody>
        </table>
        {!rows.length ? <p className="ticket-empty">{t(status.data.accounts.length ? "admin.ticket_no_matches" : "admin.software_no_accounts_help")}</p> : null}
        {missing.map(id => <div className="ticket-missing-account" key={id}><span>{id} · {t("admin.ticket_missing_account")}</span><Button size="small" disabled={saving || busy} onClick={() => toggleAccount(id, false)}>{t("admin.ticket_remove_selection")}</Button></div>)}
        {task?.action === "plugin-update" && !rows.some(row => row.account === task.target) ? <TaskReport task={task} /> : null}
      </> : null}
      {action.isError ? <Alert type="error" showIcon title={action.error.message} /> : null}
      {job.isError ? <Alert type="error" showIcon title={t("admin.software_job_unavailable")} action={<Button onClick={() => void job.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
    </section>
    <Modal open={!!target} title={t("admin.ticket_install_plugin")} onCancel={() => !action.isPending && setTarget("")} onOk={() => { if (targetRow && canInstall(targetRow)) action.mutate({ kind: "plugin-update", account: target }, { onSuccess: () => setTarget("") }); }} okButtonProps={{ disabled: !targetRow || !canInstall(targetRow) }} confirmLoading={action.isPending}>
      <p>{t("admin.software_confirm", [target, status.data?.desired_version ?? ""])}</p>
      {hasUnsavedChanges ? <Alert type="warning" title={t("admin.ticket_save_before_install")} /> : null}
    </Modal>
  </div>;
}

export function CPAReleaseStatus({ csrfToken }: { csrfToken: string }) {
  const status = useSoftwareStatus();
  const { action, task, busy, job } = useSoftwareJob(csrfToken);
  return <div className="cpa-release-status">
    <div className="software-release-inline"><strong>CLIProxyAPI</strong><ReleaseSummary check={status.data?.checks.cpa} loading={status.isPending} /><Button disabled={busy} onClick={() => action.mutate({ kind: "version-check", account: "all" })}>{t("admin.software_check")}</Button></div>
    {status.isError ? <Alert type="error" title={t("admin.software_load_failed")} action={<Button onClick={() => void status.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
    {action.isError ? <Alert type="error" title={action.error.message} /> : null}
    {job.isError ? <Alert type="error" title={t("admin.software_job_unavailable")} action={<Button onClick={() => void job.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
    {task ? <TaskReport task={task} /> : null}
  </div>;
}

function ReleaseSummary({ check, loading }: { check: ExtensionStatus["checks"][string] | undefined; loading: boolean }) {
  return <div className="software-release-summary">
    <span>{t("admin.software_upstream_release")} {check?.version ? <a href={check.url} target="_blank" rel="noreferrer">{check.version}</a> : t(loading ? "admin.software_loading" : "admin.software_unchecked")}</span>
    {check?.checked_at ? <time>{t("admin.software_last_success")} {formatSiteTimestamp(check.checked_at)}</time> : null}
    {check?.error ? <span role="status" className="software-release-warning">{t(check.checked_at ? "admin.software_check_failed" : "admin.software_check_no_history")}</span> : null}
  </div>;
}

function TaskReport({ task }: { task: RuntimeJob }) {
  return <div className={`software-task ${task.status === "failed" ? "has-error" : ""}`} aria-label={t("admin.software_last_task")}>
    <div className="software-task-summary" role="status"><span>{t(task.action === "version-check" ? "admin.software_check_task" : "admin.software_install_task", [task.target])}</span><span className={`software-state ${task.status === "succeeded" ? "is-ready" : "is-muted"}`}>{t(taskLabels[task.status] ?? "admin.software_unknown_status")}</span></div>
    {task.error ? <p role="alert" className="form-error">{task.error}</p> : null}
    {task.output ? <details className="software-task-details"><summary>{t("admin.software_diagnostic_details")}</summary><pre className="software-task-output">{task.output}</pre></details> : null}
  </div>;
}
