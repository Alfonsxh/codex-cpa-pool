import "../../i18n/admin";
import { t } from "../../i18n";
import { Alert, Button, Modal, Space } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiRequest } from "../../api/client";
import type { ExtensionStatus } from "../../api/generated";
import { readRuntimeJob, submitRuntimeJob } from "../../api/runtime";
import { formatSiteTimestamp, useSiteTimezone } from "../site-time";

export function SoftwareVersions({ csrfToken }: { csrfToken: string }) {
  useSiteTimezone();
  const client = useQueryClient();
  const [jobID, setJobID] = useState("");
  const [target, setTarget] = useState("");
  const status = useQuery({ queryKey: ["software-versions"], queryFn: ({ signal }) => apiRequest<ExtensionStatus>("/admin/api/extensions", { signal }), refetchInterval: 15000 });
  const job = useQuery({ queryKey: ["software-job", jobID], queryFn: ({ signal }) => readRuntimeJob(jobID, signal), enabled: !!jobID, refetchInterval: (q) => ["queued", "running", "cancelling"].includes(q.state.data?.job.status ?? "queued") ? 1000 : false });
  const action = useMutation({ mutationFn: ({ kind, account }: { kind: "version-check" | "plugin-update"; account: string }) => submitRuntimeJob(kind, account, csrfToken), onSuccess: (result) => { setJobID(result.job.id); setTarget(""); void client.invalidateQueries({ queryKey: ["software-versions"] }); } });
  const busy = action.isPending || ["queued", "running", "cancelling"].includes(job.data?.job.status ?? "");
  return <div className="software-versions">
    <p>{t("admin.software_scope")}</p>
    <Space wrap><Button onClick={() => action.mutate({ kind: "version-check", account: "all" })} disabled={busy}>{t("admin.software_check")}</Button><Button onClick={() => void status.refetch()} loading={status.isFetching}>{t("admin.refresh_page")}</Button></Space>
    {status.isPending ? <p role="status">{t("admin.software_loading")}</p> : null}
    {status.isError ? <Alert type="error" showIcon title={t("admin.software_load_failed")} /> : null}
    {status.data ? <>
      <dl className="software-release-list">{["cpa", "codex-ticket"].map((name) => {
        const check = status.data.checks[name];
        return <div key={name}><dt>{name === "cpa" ? "CLIProxyAPI" : "Codex Ticket"}</dt><dd>{check?.version ? <a href={check.url} target="_blank" rel="noreferrer">{check.version}</a> : t("admin.software_unchecked")}{check?.checked_at ? <> · {formatSiteTimestamp(check.checked_at)}</> : null}{check?.error ? <p role="status">{t("admin.software_check_failed")}</p> : null}</dd></div>;
      })}</dl>
      <p>{t("admin.software_desired")} <strong>{status.data.desired_version}</strong></p>
      <p>{t("admin.software_bundled")} <strong>{status.data.bundled_version || t("admin.software_unavailable")}</strong></p>
      {status.data.accounts.length === 0 ? <p>{t("admin.software_no_accounts")}</p> : <ul className="software-account-list">{status.data.accounts.map((row) => <li key={row.account}>
        <div><strong>{row.account}</strong><p>{row.installation.version || t("admin.software_not_installed")}{row.installation.staging ? <> · {t("admin.software_staging")}</> : null}{!row.running ? <> · {t("admin.software_stopped")}</> : null}{!row.selected ? <> · {t("admin.software_unselected")}</> : null}</p></div>
        <Button disabled={busy || !row.running || !row.enabled || !row.selected} onClick={() => setTarget(row.account)}>{t("admin.software_install")}</Button>
      </li>)}</ul>}
    </> : null}
    {action.isError ? <Alert type="error" showIcon title={action.error.message} /> : null}
    {job.data ? <div role="status"><strong>{t("admin.software_task")} {job.data.job.status}</strong>{job.data.job.error ? <Alert type="error" showIcon title={job.data.job.error} /> : null}{job.data.job.output ? <pre className="software-task-output">{job.data.job.output}</pre> : null}</div> : null}
    <Modal open={!!target} title={t("admin.software_install")} onCancel={() => setTarget("")} onOk={() => action.mutate({ kind: "plugin-update", account: target })} confirmLoading={action.isPending}><p>{t("admin.software_confirm", [target, status.data?.desired_version ?? ""])}</p></Modal>
  </div>;
}
