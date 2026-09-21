import "../../i18n/admin";
import { t } from "../../i18n";
import { Alert, Button } from "antd";
import { CheckCircleOutlined, CloseCircleOutlined, LoadingOutlined, ReloadOutlined } from "@ant-design/icons";
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
const basicKeys = ["enabled", "models", "harvest_enabled", "inject_enabled", "proxy_source", "proxy_url"];
const advancedKeys = ["ttl_seconds", "refresh_before_seconds", "scan_interval_seconds", "timeout_seconds", "retry_base_seconds", "retry_max_seconds"];
const taskLabels: Record<string, string> = {
  queued: "admin.queued", running: "admin.running", cancelling: "admin.software_cancelling",
  succeeded: "admin.software_succeeded", failed: "admin.task_failed", cancelled: "admin.software_cancelled"
};
export function ticketAccountIDs(value: string) {
  return [...new Set(value.split(",").map(id => id.trim()).filter(Boolean))];
}

export function useSoftwareStatus(account = "") {
  return useQuery({ queryKey: [...softwareVersionsQueryKey, account], queryFn: ({ signal }) => apiRequest<ExtensionStatus>("/admin/api/extensions" + (account ? "?account=" + encodeURIComponent(account) : ""), { signal }), retry: false, refetchInterval: 15000 });
}

export function useSoftwareJob(csrfToken: string) {
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
  saving: boolean;
  focusKey: string;
};

export function TicketPluginSettings({ csrfToken, renderField, tableHeader, saving, focusKey }: TicketSettingsProps) {
  useSiteTimezone();
  const status = useSoftwareStatus();
  const { action, task, job, busy } = useSoftwareJob(csrfToken);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  useEffect(() => {
    if (advancedKeys.some(key => prefix + key === focusKey)) setAdvancedOpen(true);
  }, [focusKey]);

  return <div className="ticket-settings">
    <div className="configuration-fields">{tableHeader}{basicKeys.map(key => <Fragment key={key}>{renderField(prefix + key)}</Fragment>)}</div>
    <details className="ticket-advanced" open={advancedOpen} onToggle={event => setAdvancedOpen(event.currentTarget.open)}>
      <summary>{t("admin.ticket_advanced")}</summary>
      <div className="configuration-fields">{advancedKeys.map(key => <Fragment key={key}>{renderField(prefix + key)}</Fragment>)}</div>
    </details>
    <div className="ticket-version-settings configuration-fields">
      {renderField(prefix + "version")}
      <div className="software-release-inline">
        <ReleaseSummary check={status.data?.checks["codex-ticket"]} loading={status.isPending} />
        <div className="software-check-controls">
          <Button icon={<ReloadOutlined aria-hidden="true" />} disabled={busy || saving} loading={action.isPending && action.variables?.kind === "version-check"} onClick={() => action.mutate({ kind: "version-check", account: "all" })}>{t("admin.ticket_check_updates")}</Button>
          {task?.action === "version-check" ? <VersionCheckFeedback task={task} /> : null}
        </div>
      </div>
      {renderField("software.plugin_auto_check")}
      {renderField("software.check_interval_hours")}
    </div>
    {status.isError ? <Alert type="error" title={t("admin.software_load_failed")} action={<Button onClick={() => void status.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
    {action.isError ? <Alert type="error" title={action.error.message} /> : null}
    {job.isError ? <Alert type="error" title={t("admin.software_job_unavailable")} /> : null}
  </div>;
}

export function CPAReleaseStatus({ csrfToken }: { csrfToken: string }) {
  const status = useSoftwareStatus();
  const { action, task, busy, job } = useSoftwareJob(csrfToken);
  return <div className="cpa-release-status">
    <div className="software-release-inline"><strong>CLIProxyAPI</strong><ReleaseSummary check={status.data?.checks.cpa} loading={status.isPending} /><div className="software-check-controls"><Button disabled={busy} onClick={() => action.mutate({ kind: "version-check", account: "all" })}>{t("admin.software_check")}</Button>{task ? <VersionCheckFeedback task={task} /> : null}</div></div>
    {status.isError ? <Alert type="error" title={t("admin.software_load_failed")} action={<Button onClick={() => void status.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
    {action.isError ? <Alert type="error" title={action.error.message} /> : null}
    {job.isError ? <Alert type="error" title={t("admin.software_job_unavailable")} action={<Button onClick={() => void job.refetch()}>{t("admin.refresh_page")}</Button>} /> : null}
  </div>;
}

function ReleaseSummary({ check, loading }: { check: ExtensionStatus["checks"][string] | undefined; loading: boolean }) {
  return <div className="software-release-summary">
    <span className="software-release-heading">{t("admin.software_upstream_release")} {check?.version ? <a href={check.url} target="_blank" rel="noreferrer">{check.version}</a> : t(loading ? "admin.software_loading" : "admin.software_unchecked")}</span>
    {check?.checked_at ? <time>{t("admin.software_last_success")} {formatSiteTimestamp(check.checked_at)}</time> : null}
    {check?.error ? <span role="status" className="software-release-warning">{t(check.checked_at ? "admin.software_check_failed" : "admin.software_check_no_history")}</span> : null}
  </div>;
}

function VersionCheckFeedback({ task }: { task: RuntimeJob }) {
  const checking = pendingStates.includes(task.status);
  const failed = task.status === "failed";
  const succeeded = task.status === "succeeded";
  const label = checking ? "admin.software_check_running" : failed ? "admin.software_check_error_badge" : succeeded ? "admin.software_check_completed" : taskLabels[task.status] ?? "admin.software_unknown_status";
  return <div className={`software-check-feedback${failed ? " has-error" : ""}`} role={failed ? "alert" : "status"}>
    {checking ? <LoadingOutlined aria-hidden="true" /> : failed ? <CloseCircleOutlined aria-hidden="true" /> : succeeded ? <CheckCircleOutlined aria-hidden="true" /> : null}
    <span>{t(label)}</span>
    {failed && task.error ? <span className="software-check-error">{task.error}</span> : null}
  </div>;
}

export function TaskReport({ task }: { task: RuntimeJob }) {
  return <div className={`software-task ${task.status === "failed" ? "has-error" : ""}`} aria-label={t("admin.software_last_task")}>
    <div className="software-task-summary" role="status"><span>{t(task.action === "version-check" ? "admin.software_check_task" : "admin.software_install_task", [task.target])}</span><span className={`software-state ${task.status === "succeeded" ? "is-ready" : "is-muted"}`}>{t(taskLabels[task.status] ?? "admin.software_unknown_status")}</span></div>
    {task.error ? <p role="alert" className="form-error">{task.error}</p> : null}
    {task.output ? <details className="software-task-details"><summary>{t("admin.software_diagnostic_details")}</summary><pre className="software-task-output">{task.output}</pre></details> : null}
  </div>;
}
