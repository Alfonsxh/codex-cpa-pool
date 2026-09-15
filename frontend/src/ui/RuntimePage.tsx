import "../i18n/admin";
import { t, getIntlLocale } from "../i18n";
import { useSiteTimezone, formatSiteTimestamp } from "./site-time";
import { Button, Modal, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode
} from "react";

import {
  cancelLegacyRuntimeJob,
  isActiveRuntimeJob,
  listLegacyRuntimeJobs,
  listRuntimeServices,
  readLegacyRuntimeJob,
  readLegacyRuntimeLogs,
  readOperationImpact,
  runtimeJobsQueryKey,
  runtimeLogsQueryKey,
  runtimeServicesQueryKey,
  submitLegacyRuntimeJob,
  type LegacyRuntimeAction,
  type LegacyRuntimeJobView,
  type OperationImpact,
  type RuntimeLogs,
  type RuntimeService
} from "../api/runtime";
import { useAdminToolbar } from "./AdminToolbarContext";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";

const { Paragraph } = Typography;

type PendingOperation = {
  action: "stop" | "restart";
  target: string;
  impact?: OperationImpact;
  impactError?: unknown;
};

const serviceStateRank: Record<string, number> = {
  dead: 0,
  failed: 0,
  exited: 1,
  restarting: 2,
  paused: 3,
  created: 4,
  unknown: 5,
  running: 6
};

export function RuntimePage({ csrfToken }: { csrfToken: string }) {
  const siteTimezone = useSiteTimezone();
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const reportedServiceError = useRef<unknown>(null);
  const reportedJobError = useRef<unknown>(null);
  const operationLock = useRef(false);
  const operationPreparationLock = useRef(false);
  const cancelLock = useRef(false);
  const stopImpactRequest = useRef(0);
  const [pendingOperation, setPendingOperation] = useState<PendingOperation | null>(null);
  const [preparingStop, setPreparingStop] = useState("");
  const [taskJob, setTaskJob] = useState<LegacyRuntimeJobView | null>(null);
  const [taskPollError, setTaskPollError] = useState<unknown>(null);
  const [completedTaskJobID, setCompletedTaskJobID] = useState("");
  const [logTarget, setLogTarget] = useState<string | null>(null);

  const services = useQuery({
    queryKey: runtimeServicesQueryKey,
    queryFn: ({ signal }) => listRuntimeServices(signal),
    retry: false,
    refetchOnWindowFocus: false
  });
  const jobs = useQuery({
    queryKey: runtimeJobsQueryKey,
    queryFn: ({ signal }) => listLegacyRuntimeJobs(signal),
    retry: false,
    refetchOnWindowFocus: false
  });
  const logs = useQuery({
    queryKey: runtimeLogsQueryKey(logTarget ?? ""),
    queryFn: ({ signal }) => readLegacyRuntimeLogs(logTarget ?? "", signal),
    enabled: logTarget !== null,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false
  });

  const sortedServices = useMemo(() => [...(services.data?.services ?? [])].sort((left, right) => (
    (serviceStateRank[left.state] ?? 5) - (serviceStateRank[right.state] ?? 5)
      || left.service.localeCompare(right.service, getIntlLocale(), { numeric: true, sensitivity: "base" })
  )), [services.data?.services]);

  const refreshRuntime = useCallback(async (feedback = true) => {
    setRefreshing(true);
    try {
      const [serviceCatalog, jobCatalog] = await Promise.all([
        listRuntimeServices(),
        listLegacyRuntimeJobs()
      ]);
      queryClient.setQueryData(runtimeServicesQueryKey, serviceCatalog);
      queryClient.setQueryData(runtimeJobsQueryKey, jobCatalog);
      setRefreshLabel(t("admin.runtime_status_updated", [formatSiteTimestamp(Date.now() / 1_000)]));
      if (feedback) showToast(t("admin.data_refreshed"));
    } catch (error) {
      setRefreshLabel(t("admin.refresh_failed"));
      if (feedback) showToast(errorMessage(error), "error");
      throw error;
    } finally {
      setRefreshing(false);
    }
  }, [queryClient, setRefreshLabel, setRefreshing, showToast]);

  useEffect(() => {
    setRefreshAction(() => refreshRuntime(true));
    return () => setRefreshAction(null);
  }, [refreshRuntime, setRefreshAction]);
  useEffect(() => {
    setRefreshing(services.isFetching || jobs.isFetching);
  }, [jobs.isFetching, services.isFetching, setRefreshing]);
  useEffect(() => {
    if (!services.data) return;
    reportedServiceError.current = null;
    setRefreshLabel(t("admin.runtime_status_updated", [formatSiteTimestamp(services.dataUpdatedAt / 1_000)]));
  }, [services.data, services.dataUpdatedAt, setRefreshLabel, siteTimezone]);
  useEffect(() => {
    if (!services.isError || reportedServiceError.current === services.error) return;
    reportedServiceError.current = services.error;
    setRefreshLabel(t("admin.refresh_failed"));
    showToast(errorMessage(services.error, t("admin.unable_to_load_runtime_status")), "error");
  }, [services.error, services.isError, setRefreshLabel, showToast]);
  useEffect(() => {
    if (!jobs.isError || reportedJobError.current === jobs.error) return;
    reportedJobError.current = jobs.error;
    showToast(errorMessage(jobs.error, t("admin.unable_to_load_task_list")), "error");
  }, [jobs.error, jobs.isError, showToast]);
  useEffect(() => () => {
    stopImpactRequest.current += 1;
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);

  const operation = useMutation({
    gcTime: 0,
    mutationFn: ({ action, target }: { action: LegacyRuntimeAction; target: string }) => (
      submitLegacyRuntimeJob(action, target, csrfToken)
    ),
    onSuccess: async (result) => {
      showToast(result.message);
      setTaskPollError(null);
      setCompletedTaskJobID("");
      setLogTarget(null);
      setTaskJob(result.job);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true })
      ]);
    },
    onError: (error) => showToast(errorMessage(error), "error"),
    onSettled: () => {
      operationLock.current = false;
    }
  });

  const submitOperation = useCallback((action: LegacyRuntimeAction, target: string) => {
    if (operationLock.current) return;
    operationLock.current = true;
    operation.mutate({ action, target });
  }, [operation]);

  const prepareOperation = useCallback(async (action: "stop" | "restart", target: string) => {
    if (operationPreparationLock.current || operationLock.current) return;
    if (action === "restart" || target === "all") {
      setPendingOperation({ action, target });
      return;
    }
    operationPreparationLock.current = true;
    const requestID = ++stopImpactRequest.current;
    setPreparingStop(target);
    try {
      const impact = await readOperationImpact(target);
      if (requestID === stopImpactRequest.current) setPendingOperation({ action, target, impact });
    } catch (error) {
      if (requestID === stopImpactRequest.current) setPendingOperation({ action, target, impactError: error });
    } finally {
      if (requestID === stopImpactRequest.current) {
        setPreparingStop("");
        operationPreparationLock.current = false;
      }
    }
  }, []);

  const taskCancel = useMutation({
    gcTime: 0,
    mutationFn: (jobID: string) => cancelLegacyRuntimeJob(jobID, csrfToken),
    onSuccess: (result) => {
      setTaskJob(result.job);
      showToast(result.message);
      if (!isActiveRuntimeJob(result.job)) cancelLock.current = false;
    },
    onError: (error) => {
      cancelLock.current = false;
      showToast(errorMessage(error), "error");
    }
  });
  const cancelTask = useCallback(() => {
    if (!taskJob || taskJob.status === "cancelling" || cancelLock.current) return;
    cancelLock.current = true;
    taskCancel.mutate(taskJob.id);
  }, [taskCancel, taskJob]);

  useEffect(() => {
    if (!taskJob || !isActiveRuntimeJob(taskJob)) return;
    const controller = new AbortController();
    let timer = 0;
    const poll = () => {
      timer = window.setTimeout(() => {
        void readLegacyRuntimeJob(taskJob.id, controller.signal)
          .then((result) => {
            setTaskPollError(null);
            setTaskJob(result.job);
          })
          .catch((error: unknown) => {
            if (controller.signal.aborted) return;
            setTaskPollError(error);
            poll();
          });
      }, 1_200);
    };
    poll();
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [taskJob]);
  useEffect(() => {
    if (!taskJob || isActiveRuntimeJob(taskJob) || completedTaskJobID === taskJob.id) return;
    cancelLock.current = false;
    setCompletedTaskJobID(taskJob.id);
    showToast(
      taskJob.status === "succeeded" ? t("admin.task_completed") : t("admin.task_failed"),
      taskJob.status === "succeeded" ? "success" : "error"
    );
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
      queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true })
    ]);
  }, [completedTaskJobID, queryClient, showToast, taskJob]);

  const openExistingJob = useCallback(async (jobID: string) => {
    try {
      const result = await readLegacyRuntimeJob(jobID);
      setTaskPollError(null);
      setCompletedTaskJobID("");
      setLogTarget(null);
      setTaskJob(result.job);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }, [showToast]);
  const refreshJobs = useCallback(async () => {
    try {
      const catalog = await listLegacyRuntimeJobs();
      queryClient.setQueryData(runtimeJobsQueryKey, catalog);
      showToast(t("admin.task_list_refreshed"));
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }, [queryClient, showToast]);
  const closeOutput = useCallback(() => {
    cancelLock.current = false;
    setTaskJob(null);
    setTaskPollError(null);
    setCompletedTaskJobID("");
    setLogTarget(null);
    taskCancel.reset();
  }, [taskCancel]);

  return (
    <section className="page-content legacy-runtime-page">
      <div className="bulk-actions">
        <div><h3>{t("admin.bulk_actions")}</h3><p className="section-kicker">STACK CONTROL</p></div>
        <div className="button-group">
          <button className="button secondary" type="button" disabled={operation.isPending} onClick={() => submitOperation("up", "all")}>{t("admin.start_all")}</button>
          <button className="button secondary" type="button" disabled={operation.isPending} onClick={() => void prepareOperation("restart", "all")}>{t("admin.restart_all")}</button>
          <button className="button secondary" type="button" onClick={() => { setTaskJob(null); setLogTarget("all"); }}>{t("admin.all_logs")}</button>
          <button className="button danger-outline" type="button" disabled={operation.isPending} onClick={() => void prepareOperation("stop", "all")}>{t("admin.stop_business_services")}</button>
        </div>
      </div>

      <div className="panel table-panel runtime-service-panel">
        <div className="panel-title"><div><h3>{t("admin.container_services")}</h3><p className="section-kicker">SERVICES</p></div></div>
        <NativeTableViewport className="table-wrap" aria-label={t("admin.container_services_table")}>
          <table className="service-table">
            <thead><tr><th className="table-index-column">{t("common.no")}</th><th>{t("admin.service")}</th><th>{t("admin.container")}</th><th>{t("admin.status_2")}</th><th>{t("admin.description")}</th><th>{t("admin.actions")}</th></tr></thead>
            <tbody>
              {services.isPending ? <RuntimeServiceSkeleton /> : null}
              {!services.isPending && sortedServices.map((service, index) => (
                <RuntimeServiceRow
                  key={service.service}
                  index={index}
                  service={service}
                  busy={operation.isPending || preparingStop === serviceTarget(service.service)}
                  onOperate={(action, target) => {
                    if (action === "up") submitOperation(action, target);
                    else void prepareOperation(action, target);
                  }}
                  onLogs={(target) => { setTaskJob(null); setLogTarget(target); }}
                />
              ))}
              {!services.isPending && sortedServices.length === 0 ? (
                <tr className="runtime-empty-row"><td colSpan={6}>{services.isError ? t("admin.unable_to_load_runtime_status_use_refresh_above_to_try") : t("admin.no_visible_container_services")}</td></tr>
              ) : null}
            </tbody>
          </table>
        </NativeTableViewport>
      </div>

      <div className="section-heading">
        <div><h3>{t("admin.diagnostics")}</h3><p className="section-kicker">DIAGNOSTICS</p></div>
        <p>{t("admin.equivalent_terminal_checks")}</p>
      </div>
      <div className="diagnostic-grid">
        <DiagnosticCard index="01" title={t("admin.health_check")} description={t("admin.check_keys_oauth_files_and_available_models")} disabled={operation.isPending} onClick={() => submitOperation("health", "all")} />
        <DiagnosticCard index="02" title={t("admin.verify_routes")} description={t("admin.verify_the_destination_cpa_for_each_active_key")} disabled={operation.isPending} onClick={() => submitOperation("verify-routing", "all")} />
        <DiagnosticCard index="03" title={t("admin.validate_configuration")} description={t("admin.render_and_validate_compose_configuration")} disabled={operation.isPending} onClick={() => submitOperation("render", "all")} />
      </div>

      <div className="section-heading">
        <div><h3>{t("admin.task_history")}</h3><p className="section-kicker">JOB HISTORY</p></div>
        <button className="text-button" type="button" disabled={jobs.isFetching} onClick={() => void refreshJobs()}>{jobs.isFetching ? t("admin.refreshing") : t("admin.refresh_tasks")}</button>
      </div>
      <div className="panel runtime-job-panel">
        {jobs.isPending ? <RuntimeJobSkeleton /> : <RuntimeJobList jobs={jobs.data?.jobs ?? []} onOpen={(jobID) => void openExistingJob(jobID)} />}
      </div>

      <LegacyConfirmModal
        title={pendingOperation?.action === "stop" ? t("admin.stop_service_2") : t("admin.restart_service_2")}
        open={pendingOperation !== null}
        okText={pendingOperation?.action === "stop" ? t("admin.confirm_stop") : t("admin.confirm_restart")}
        danger={pendingOperation?.action === "stop"}
        confirmLoading={operation.isPending}
        okDisabled={Boolean(pendingOperation?.impactError)}
        onCancel={() => !operation.isPending && setPendingOperation(null)}
        onOk={() => {
          const current = pendingOperation;
          setPendingOperation(null);
          if (current) submitOperation(current.action, current.target);
        }}
      >
        <Paragraph>{pendingOperation ? operationMessage(pendingOperation) : ""}</Paragraph>
      </LegacyConfirmModal>

      <RuntimeOutputModal
        job={taskJob}
        logTarget={logTarget}
        logs={logs}
        pollError={taskPollError}
        cancelling={taskCancel.isPending}
        onCancelJob={cancelTask}
        onClose={closeOutput}
        onToast={showToast}
      />
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function RuntimeServiceRow({
  index,
  service,
  busy,
  onOperate,
  onLogs
}: {
  index: number;
  service: RuntimeService;
  busy: boolean;
  onOperate: (action: "up" | "stop" | "restart", target: string) => void;
  onLogs: (target: string) => void;
}) {
  const target = serviceTarget(service.service);
  const logOnly = service.service === "admin" || service.service === "edge" || service.service.startsWith("gateway-");
  return (
    <tr>
      <td className="table-index-cell">{index + 1}</td>
      <td><span className="table-primary">{service.service}</span></td>
      <td><span className="table-secondary">{service.name}</span></td>
      <td><span className={`status-chip ${statusTone(service.state)}`}>{statusLabel(service.state)}</span></td>
      <td>{serviceDescription(service.service)}</td>
      <td>
        <div className="table-actions">
          {!logOnly ? (
            <button className="button ghost" type="button" disabled={busy} onClick={() => onOperate(service.state === "running" ? "restart" : "up", target)}>
              {service.state === "running" ? t("admin.restart") : t("admin.start")}
            </button>
          ) : null}
          <button className="button ghost" type="button" onClick={() => onLogs(target)}>{t("admin.logs_2")}</button>
          {!logOnly ? <button className="button danger-outline" type="button" disabled={busy} onClick={() => onOperate("stop", target)}>{t("admin.stop")}</button> : null}
        </div>
      </td>
    </tr>
  );
}

function RuntimeServiceSkeleton() {
  return Array.from({ length: 5 }, (_, index) => (
    <tr className="runtime-skeleton-row" key={index} aria-hidden="true">
      <td>{index + 1}</td><td><i /></td><td><i /></td><td><i /></td><td><i /></td><td><i /></td>
    </tr>
  ));
}

function DiagnosticCard({ index, title, description, disabled, onClick }: {
  index: string;
  title: string;
  description: string;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <button className="diagnostic-card" type="button" disabled={disabled} onClick={onClick}>
      <span>{index}</span><strong>{title}</strong><small>{description}</small>
    </button>
  );
}

function RuntimeJobList({ jobs, onOpen }: { jobs: LegacyRuntimeJobView[]; onOpen: (jobID: string) => void }) {
  if (jobs.length === 0) {
    return <div className="empty-state"><div className="empty-icon">⌘</div><h3>{t("admin.no_tasks")}</h3><p>{t("admin.startup_authorization_and_diagnostic_tasks_appear_here")}</p></div>;
  }
  return (
    <div className="job-list">
      {jobs.map((job) => (
        <div className="job-row" key={job.id}>
          <div><div className="job-name">{(job.name)}</div><div className="job-target">{job.id}</div></div>
          <div className="job-target">{job.target}</div>
          <div className="job-time">{formatSiteTimestamp(job.created_at)}</div>
          <button className="button ghost" type="button" onClick={() => onOpen(job.id)}>
            <span className={`status-chip ${statusTone(job.status)}`}>{statusLabel(job.status)}</span>
          </button>
        </div>
      ))}
    </div>
  );
}

function RuntimeJobSkeleton() {
  return <div className="job-list runtime-job-skeleton" aria-label={t("admin.loading_task_history")}>{Array.from({ length: 3 }, (_, index) => <div className="job-row" key={index}><i /><i /><i /><i /></div>)}</div>;
}

function LegacyConfirmModal({
  title,
  open,
  children,
  okText,
  danger = false,
  confirmLoading = false,
  okDisabled = false,
  onCancel,
  onOk
}: {
  title: string;
  open: boolean;
  children: ReactNode;
  okText: string;
  danger?: boolean;
  confirmLoading?: boolean;
  okDisabled?: boolean;
  onCancel: () => void;
  onOk: () => void;
}) {
  return (
    <Modal
      className="legacy-confirm-modal"
      title={null}
      open={open}
      width={430}
      centered
      closable={false}
      transitionName=""
      maskTransitionName=""
      onCancel={onCancel}
      destroyOnHidden
      footer={[
        <Button key="cancel" disabled={confirmLoading} onClick={onCancel}>{t("common.cancel")}</Button>,
        <Button key="confirm" type={danger ? "default" : "primary"} danger={danger} loading={confirmLoading} disabled={okDisabled} onClick={onOk}>{okText}</Button>
      ]}
    >
      <div className="legacy-confirm-body">
        <div className="legacy-confirm-icon" aria-hidden="true">!</div>
        <h3>{title}</h3>
        <div className="legacy-confirm-message">{children}</div>
      </div>
    </Modal>
  );
}

function RuntimeOutputModal({
  job,
  logTarget,
  logs,
  pollError,
  cancelling,
  onCancelJob,
  onClose,
  onToast
}: {
  job: LegacyRuntimeJobView | null;
  logTarget: string | null;
  logs: { isPending: boolean; isError: boolean; error: unknown; data?: RuntimeLogs };
  pollError: unknown;
  cancelling: boolean;
  onCancelJob: () => void;
  onClose: () => void;
  onToast: (message: string, kind?: "success" | "error") => void;
}) {
  if (!job && !logTarget) return null;
  const isJob = Boolean(job);
  const output = job
    ? job.output || t("admin.task_is_queued")
    : logs.isPending
      ? t("common.loading_2")
      : logs.isError
        ? errorMessage(logs.error, t("admin.unable_to_read_logs"))
        : logs.data?.output || t("admin.no_logs");
  const active = job ? isActiveRuntimeJob(job) : false;
  const copy = async () => {
    const copied = await copyText(output);
    onToast(copied ? t("admin.copied_to_clipboard") : t("admin.the_browser_blocked_copying_select_the_text_manually"), copied ? "success" : "error");
  };
  return (
    <Modal
      className="legacy-output-modal runtime-output-modal"
      title={<LegacyDialogTitle title={(job?.name ?? "") || t("admin.logs", [logTarget])} kicker={isJob ? "TASK OUTPUT" : "SERVICE LOGS"} />}
      open
      width={900}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      footer={[
        <Button className="legacy-output-secondary" key="copy" onClick={() => void copy()}>{t("admin.copy_full_output")}</Button>,
        active ? <Button key="cancel-job" danger loading={cancelling} onClick={onCancelJob}>{t("admin.cancel_task")}</Button> : null,
        <Button className="legacy-output-ghost" key="close" onClick={onClose}>{t("common.close")}</Button>
      ]}
    >
      <div className="job-meta">
        {job ? <><span>{job.target}</span><span>{statusLabel(job.status)}</span><span>{formatSiteTimestamp(job.started_at || job.created_at)}</span></> : <><span>{t("admin.last_200_lines")}</span><span>{logTarget}</span></>}
      </div>
      {pollError ? <div className="runtime-output-notice error">{errorMessage(pollError, t("admin.unable_to_refresh_task_status_retrying"))}</div> : null}
      {logs.data?.truncated && !isJob ? <div className="runtime-output-notice">{t("admin.output_truncated_at_2_mib")}</div> : null}
      <pre className={isJob ? "oauth-task-output" : "runtime-log-output"}>{output}</pre>
    </Modal>
  );
}

function LegacyDialogTitle({ title, kicker }: { title: string; kicker: string }) {
  return <span className="legacy-dialog-title"><strong>{title}</strong><span className="section-kicker">{kicker}</span></span>;
}

function operationMessage(operation: PendingOperation) {
  if (operation.action === "restart") {
    return operation.target === "all" ? t("admin.all_business_services_will_restart_in_sequence_requests_may_be") : t("admin.will_be_restarted", [operation.target]);
  }
  if (operation.target === "all") {
    return t("admin.all_cpa_accounts_and_plugin_resource_services_will_stop_the");
  }
  if (operation.impactError) return t("admin.unable_to_confirm_the_impact_stopping_is_locked_cancel_and");
  if (operation.impact?.target_type !== "account") return t("admin.will_be_stopped", [operation.target]);
  const routedUsers = operation.impact.routed_users;
  if (!Number.isInteger(routedUsers) || Number(routedUsers) < 0) return t("admin.will_be_stopped_the_impact_cannot_currently_be_determined", [operation.target]);
  return routedUsers
    ? t("admin.will_be_stopped_users_are_currently_routed_to_this_account", [operation.target, routedUsers])
    : t("admin.will_be_stopped_no_users_are_currently_routed_to_this", [operation.target]);
}

function statusTone(status: string) {
  if (["active", "configured", "running", "succeeded"].includes(status)) return "success";
  if (["pending", "queued", "cancelling", "running-job", "restarting"].includes(status)) return "warning";
  if (["failed", "exited", "dead"].includes(status)) return "danger";
  return "neutral";
}

function statusLabel(status: string) {
  return ({
    active: t("admin.enable"),
    inactive: t("common.disabled"),
    configured: t("admin.authorized"),
    pending: t("admin.authorization_required"),
    running: t("admin.running_2"),
    exited: t("common.stopped"),
    missing: t("admin.not_created_2"),
    succeeded: t("common.succeeded"),
    failed: t("common.failed"),
    queued: t("admin.queued"),
    cancelling: t("admin.cancelling"),
    cancelled: t("common.cancelled")
  } as Record<string, string>)[status] || status || t("common.unknown");
}

function serviceDescription(service: string) {
  if (service === "edge") return t("admin.stable_api_entrypoint_and_uninterrupted_route_switching");
  if (service === "web") return t("admin.static_assets_for_portal_usage_center_and_admin");
  if (service === "gateway-blue" || service === "gateway-green") return t("admin.api_key_authentication_quotas_and_cpa_routing");
  if (service === "management") return t("admin.plugin_and_native_interface_assets");
  if (service === "usage-collector") return t("admin.user_request_and_token_usage_collection");
  if (service === "log-maintenance") return t("admin.host_log_capacity_and_backup_management");
  if (service === "admin") return t("admin.current_admin_console");
  return t("admin.isolated_codex_account_proxy");
}

function serviceTarget(service: string) {
  return service.startsWith("cliproxy-") ? service.slice("cliproxy-".length) : service;
}

function errorMessage(error: unknown, fallback = t("admin.action_failed_please_try_again_later")) {
  return error instanceof Error && error.message ? error.message : fallback;
}

async function copyText(value: string) {
  if (!value) return false;
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Use the same legacy selection fallback below.
    }
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  let copied = false;
  try {
    copied = document.execCommand("copy");
  } catch {
    copied = false;
  }
  textarea.remove();
  return copied;
}
