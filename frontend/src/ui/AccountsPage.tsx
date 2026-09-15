import "../i18n/admin";
import { t, getIntlLocale } from "../i18n";
import { useSiteTimezone, formatSiteTimestamp, getSiteTimezone } from "./site-time";
import { zodResolver } from "@hookform/resolvers/zod";
import {
  CheckOutlined,
  CopyOutlined,
  SafetyCertificateOutlined
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Col,
  Form,
  Input,
  Modal,
  Result,
  Row,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
  type TableColumnsType
} from "antd";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { accountListRefreshOptions, refreshAccountList } from "../api/account-refresh";
import { ApiError } from "../api/client";
import {
  accountQuotaResetQueryKey,
  accountListQueryKey,
  accountsQueryKey,
  clearAccountAuth,
  createAccount,
  deleteAccount,
  listAccounts,
  inspectAccountQuotaReset,
  rebalanceAccount,
  rebalanceAllAccounts,
  resetAccountQuota,
  updateAccount,
  updateAccountPolicy,
  type Account,
  type AccountCatalog,
  type AccountClearAuthResponse,
  type AccountCreateRequestWritable,
  type AccountCreateResponse,
  type AccountDeleteRequest,
  type AccountDeleteResponse,
  type AccountUpdateRequestWritable,
  type AccountUpdateResponse,
  type AccountUsageRange,
  type AccountUsageWindow,
  type RebalanceResponse,
  type ResetAccountQuotaInspection
} from "../api/accounts";
import {
  cancelLegacyRuntimeJob,
  cpaImageStatusQueryKey,
  isActiveRuntimeJob,
  listLegacyRuntimeJobs,
  operationImpactQueryKey,
  readCPAImageStatus,
  readLegacyRuntimeJob,
  readLegacyRuntimeLogs,
  readOperationImpact,
  runtimeJobsQueryKey,
  runtimeLogsQueryKey,
  runtimeServicesQueryKey,
  submitLegacyRuntimeJob,
  type CpaImageStatus,
  type LegacyRuntimeJobView,
  type RuntimeJobRequest,
  type RuntimeLogs
} from "../api/runtime";
import {
  readUsageBreakdown,
  usageBreakdownQueryKey,
  usageBreakdownQueryRoot,
  type UsageBreakdown,
  type UsageCombination
} from "../api/usage";
import { AdminTable } from "./components/AdminTable";
import { ImageUpdateTaskReport, parseImageUpdateOutput } from "./components/ImageUpdateTaskReport";
import {
  CustomUsageRangeModal,
  type CustomUsageRange
} from "./components/CustomUsageRangeModal";
import { useAdminToolbar } from "./AdminToolbarContext";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { ManagementUsageTimeFilter } from "./components/ManagementUsageTimeFilter";
import { AccountModelTestModal } from "./components/AccountModelTestModal";
import { WideSelect } from "./components/WideSelect";
import { formatTokenAmount } from "./formatters";

const { Paragraph, Text } = Typography;

type AccountLifecycleCommand =
  | { kind: "create"; request: AccountCreateRequestWritable }
  | { kind: "update"; request: AccountUpdateRequestWritable }
  | { kind: "policy"; request: AccountUpdateRequestWritable }
  | { kind: "clear-auth"; request: { id: string; confirm: string } }
  | { kind: "delete"; request: AccountDeleteRequest };

type AccountLifecycleResponse =
  | AccountCreateResponse
  | AccountUpdateResponse
  | AccountClearAuthResponse
  | AccountDeleteResponse;

type DestructiveAction = { kind: "clear-auth" | "delete"; account: Account };
type AccountRuntimeFilter = "all" | "running" | "stopped" | "disabled";
type AccountAuthFilter = "all" | "configured" | "pending";
type AccountRuntimeAction = Extract<RuntimeJobRequest["action"], "start" | "stop" | "restart">;
type PendingAccountRuntimeOperation = { action: AccountRuntimeAction; account: Account };
type PendingImageUpdate = "all" | Account;
type AccountSortField = "account" | "runtime" | "auth" | "quota" | "activity" | "tokens" | "last_used";
type AccountSortState = { field: AccountSortField; direction: "asc" | "desc" };
type PendingAccountUpdate = {
  command: Extract<AccountLifecycleCommand, { kind: "update" }>;
  renamed: boolean;
  proxyChanged: boolean;
};

const emptyAccountCatalog: AccountCatalog = {
  accounts: [],
  generated_at: 0,
  window: "today",
  window_seconds: null,
  window_start_at: null,
  window_start_at_by_account: null,
  window_end_at: null,
  window_timezone: getSiteTimezone(),
  active_user_window_seconds: 900,
  quota_generated_at: null,
  quota_cached: false,
  quota_refreshing: false,
  quota_cache_ttl_seconds: 0,
  collector: {
    status: "",
    heartbeat_at: 0,
    last_error: "",
    event_count: 0,
    collection_started_at: 0,
    usage_breakdown_started_at: 0,
    last_event_at: 0
  },
  warnings: []
};

export function AccountsPage({ csrfToken }: { csrfToken: string }) {
  const siteTimezone = useSiteTimezone();
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const reportedCatalogError = useRef<unknown>(null);
  const handledDeepLink = useRef("");
  const [searchParams, setSearchParams] = useSearchParams();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [lastResult, setLastResult] = useState<RebalanceResponse | null>(null);
  const [search, setSearch] = useState("");
  const [runtimeFilter, setRuntimeFilter] = useState<AccountRuntimeFilter>("all");
  const [authFilter, setAuthFilter] = useState<AccountAuthFilter>(() => (
    searchParams.get("auth") === "pending" ? "pending" : "all"
  ));
  const [usageWindow, setUsageWindow] = useState<AccountUsageWindow>("today");
  const [accountSort, setAccountSort] = useState<AccountSortState>({ field: "quota", direction: "asc" });
  const [customUsageRange, setCustomUsageRange] = useState<CustomUsageRange | null>(null);
  const [customUsageRangeOpen, setCustomUsageRangeOpen] = useState(false);
  const [expandedAccountIDs, setExpandedAccountIDs] = useState<string[]>([]);
  const [editorAccount, setEditorAccount] = useState<Account | "create" | null>(() => (
    searchParams.get("create") === "1" ? "create" : null
  ));
  const [pendingAccountUpdate, setPendingAccountUpdate] = useState<PendingAccountUpdate | null>(null);
  const [policyAccount, setPolicyAccount] = useState<Account | null>(null);
  const [destructiveAction, setDestructiveAction] = useState<DestructiveAction | null>(null);
  const [runtimeOperation, setRuntimeOperation] = useState<PendingAccountRuntimeOperation | null>(null);
  const [imageUpdateTarget, setImageUpdateTarget] = useState<PendingImageUpdate | null>(null);
  const [modelTestAccount, setModelTestAccount] = useState<Account | null>(null);
  const [logTarget, setLogTarget] = useState<string | null>(null);
  const [rebalanceTarget, setRebalanceTarget] = useState<Account | null>(null);
  const [oauthAccount, setOAuthAccount] = useState<Account | null>(null);
  const [quotaResetAccount, setQuotaResetAccount] = useState<Account | null>(null);
  const [taskJob, setTaskJob] = useState<LegacyRuntimeJobView | null>(null);
  const [taskPollError, setTaskPollError] = useState<unknown>(null);
  const [completedTaskJobID, setCompletedTaskJobID] = useState("");
  const usageRange = useMemo<AccountUsageRange>(() => ({
    window: usageWindow,
    startAt: usageWindow === "custom" ? customUsageRange?.startAt : undefined,
    endAt: usageWindow === "custom" ? customUsageRange?.endAt : undefined
  }), [customUsageRange?.endAt, customUsageRange?.startAt, usageWindow]);
  const accounts = useQuery({
    queryKey: accountListQueryKey(usageRange),
    queryFn: ({ signal }) => listAccounts(usageRange, signal),
    enabled: usageWindow !== "custom" || customUsageRange !== null,
    ...accountListRefreshOptions
  });
  const imageStatus = useQuery({
    queryKey: cpaImageStatusQueryKey,
    queryFn: ({ signal }) => readCPAImageStatus(signal),
    staleTime: 0,
    retry: false
  });
  const refreshAccountCatalog = useCallback(async () => {
    void queryClient.refetchQueries({ queryKey: cpaImageStatusQueryKey, exact: true });
    try {
      const catalog = await refreshAccountList(
        queryClient, accountsQueryKey, accountListQueryKey(usageRange),
        (signal) => listAccounts(usageRange, signal, true)
      );
      await queryClient.refetchQueries({
        queryKey: [...usageBreakdownQueryRoot, "account"],
        type: "active"
      });
      setRefreshLabel(accountRefreshLabel(catalog));
      showToast(catalog.quota_refreshing
        ? t("admin.table_refreshed_quotas_are_updating_in_the_background")
        : t("admin.data_refreshed"));
    } catch (error) {
      setRefreshLabel(t("admin.refresh_failed"));
      throw error;
    }
  }, [queryClient, setRefreshLabel, showToast, usageRange]);
  useEffect(() => {
    setRefreshAction(refreshAccountCatalog);
    return () => setRefreshAction(null);
  }, [refreshAccountCatalog, setRefreshAction]);
  const accountPageRefreshing = accounts.isFetching || imageStatus.isFetching;
  useEffect(() => setRefreshing(accountPageRefreshing), [accountPageRefreshing, setRefreshing]);
  useEffect(() => {
    if (!accounts.data || accounts.isError) return;
    reportedCatalogError.current = null;
    const catalog = accounts.data;
    const updateLabel = () => {
      const elapsedSeconds = Math.max(0, (Date.now() - accounts.dataUpdatedAt) / 1_000);
      setRefreshLabel(accountRefreshLabel(catalog, catalog.generated_at + elapsedSeconds));
    };
    updateLabel();
    const timer = window.setInterval(updateLabel, 30_000);
    return () => window.clearInterval(timer);
  }, [accounts.data, accounts.dataUpdatedAt, accounts.isError, setRefreshLabel, siteTimezone]);
  useEffect(() => {
    if (!accounts.isError || reportedCatalogError.current === accounts.error) return;
    reportedCatalogError.current = accounts.error;
    setRefreshLabel(t("admin.refresh_failed"));
    showToast(accounts.error instanceof Error ? accounts.error.message : t("admin.unable_to_load_account_data"), "error");
  }, [accounts.error, accounts.isError, setRefreshLabel, showToast]);
  useEffect(() => () => {
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);
  const quotaResetDetails = useQuery({
    queryKey: accountQuotaResetQueryKey(quotaResetAccount?.id ?? ""),
    queryFn: ({ signal }) => inspectAccountQuotaReset(quotaResetAccount?.id ?? "", signal),
    enabled: quotaResetAccount !== null,
    staleTime: 0,
    gcTime: 0,
    retry: false
  });
  const lifecycle = useMutation({
    gcTime: 0,
    mutationFn: (command: AccountLifecycleCommand): Promise<AccountLifecycleResponse> => {
      switch (command.kind) {
        case "create": return createAccount(command.request, csrfToken);
        case "update": return updateAccount(command.request, csrfToken);
        case "policy": return updateAccountPolicy(command.request, csrfToken);
        case "clear-auth": return clearAccountAuth(command.request, csrfToken);
        case "delete": return deleteAccount(command.request, csrfToken);
      }
    },
    onSuccess: async (result) => {
      setEditorAccount(null);
      setPendingAccountUpdate(null);
      setPolicyAccount(null);
      setDestructiveAction(null);
      showToast(result.message);
      lifecycle.reset();
      await queryClient.invalidateQueries({ queryKey: accountsQueryKey });
    },
    onError: (error, command) => {
      if (command.kind !== "clear-auth") return;
      setDestructiveAction(null);
      showToast(errorMessage(error), "error");
    }
  });
  const rebalance = useMutation({
    mutationFn: () => rebalanceAllAccounts(csrfToken),
    onSuccess: async (result) => {
      setConfirmOpen(false);
      setLastResult(result);
      await queryClient.invalidateQueries({ queryKey: accountsQueryKey });
    }
  });
  const accountRebalance = useMutation({
    mutationFn: (accountID: string) => rebalanceAccount(accountID, csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      await queryClient.invalidateQueries({ queryKey: accountsQueryKey });
    },
    onError: (error) => showToast(errorMessage(error), "error")
  });
  const quotaReset = useMutation({
    gcTime: 0,
    mutationFn: ({ account, creditID }: { account: string; creditID: string }) => resetAccountQuota({
      account,
      credit_id: creditID,
      confirm: account
    }, csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      setQuotaResetAccount(null);
      await queryClient.invalidateQueries({ queryKey: accountsQueryKey });
    }
  });
  const runtimeTarget = runtimeOperation?.action === "stop" ? runtimeOperation.account.id : "";
  const runtimeImpact = useQuery({
    queryKey: operationImpactQueryKey("stop", runtimeTarget),
    queryFn: ({ signal }) => readOperationImpact(runtimeTarget, signal),
    enabled: runtimeTarget !== "",
    staleTime: 0,
    gcTime: 0,
    retry: false
  });
  const logs = useQuery({
    queryKey: runtimeLogsQueryKey(logTarget ?? ""),
    queryFn: ({ signal }) => readLegacyRuntimeLogs(logTarget ?? "", signal),
    enabled: logTarget !== null,
    staleTime: 0,
    gcTime: 0,
    refetchInterval: logTarget ? 5_000 : false
  });
  const runtimeMutation = useMutation({
    mutationFn: (operation: PendingAccountRuntimeOperation) => (
      submitLegacyRuntimeJob(operation.action, operation.account.id, csrfToken)
    ),
    onSuccess: async (result) => {
      showToast(result.message);
      setTaskPollError(null);
      setCompletedTaskJobID("");
      setTaskJob(result.job);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: accountsQueryKey }),
        queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: cpaImageStatusQueryKey, exact: true })
      ]);
    },
    onError: (error) => showToast(errorMessage(error), "error")
  });
  const imageMutation = useMutation({
    mutationFn: ({ action, target }: { action: "image-pull" | "image-update"; target: string }) => (
      submitLegacyRuntimeJob(action, target, csrfToken)
    ),
    onSuccess: async (result) => {
      showToast(result.message);
      setTaskPollError(null);
      setCompletedTaskJobID("");
      setTaskJob(result.job);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: accountsQueryKey }),
        queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: cpaImageStatusQueryKey, exact: true })
      ]);
    },
    onError: (error) => showToast(errorMessage(error), "error")
  });
  const oauthPreflight = useMutation({
    gcTime: 0,
    mutationFn: async (account: Account) => ({ account, jobs: await listLegacyRuntimeJobs() }),
    onSuccess: ({ account, jobs }) => {
      const existing = jobs.jobs.find((job) => (
        job.action === "login" && job.target === account.id && ["running", "queued"].includes(job.status)
      ));
      if (existing) {
        showToast(t("admin.an_oauth_task_already_exists_for_this_account_it_has"));
        setTaskPollError(null);
        setCompletedTaskJobID("");
        setTaskJob(existing);
        return;
      }
      setOAuthAccount(account);
    },
    onError: (error) => showToast(errorMessage(error, t("admin.unable_to_read_oauth_task_status")), "error")
  });
  const oauthMutation = useMutation({
    gcTime: 0,
    mutationFn: (account: Account) => submitLegacyRuntimeJob("login", account.id, csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      setTaskPollError(null);
      setCompletedTaskJobID("");
      setTaskJob(result.job);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: accountsQueryKey }),
        queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: cpaImageStatusQueryKey, exact: true })
      ]);
    },
    onError: (error) => showToast(errorMessage(error), "error")
  });
  const taskCancel = useMutation({
    gcTime: 0,
    mutationFn: (jobID: string) => cancelLegacyRuntimeJob(jobID, csrfToken),
    onSuccess: (result) => {
      setTaskJob(result.job);
      showToast(result.message);
    },
    onError: (error) => showToast(errorMessage(error), "error")
  });
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
    setCompletedTaskJobID(taskJob.id);
    showToast(taskJob.status === "succeeded" ? t("admin.task_completed") : t("admin.task_failed"), taskJob.status === "succeeded" ? "success" : "error");
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: accountsQueryKey }),
      queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true }),
      queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
      queryClient.invalidateQueries({ queryKey: cpaImageStatusQueryKey, exact: true })
    ]);
  }, [completedTaskJobID, queryClient, showToast, taskJob]);
  const resetLifecycle = lifecycle.reset;
  const openEditor = useCallback((account: Account | "create") => {
    resetLifecycle();
    setEditorAccount(account);
  }, [resetLifecycle]);
  const openDestructiveAction = useCallback((action: DestructiveAction) => {
    resetLifecycle();
    setDestructiveAction(action);
  }, [resetLifecycle]);
  const openAccountPolicy = useCallback((account: Account) => {
    resetLifecycle();
    setPolicyAccount(account);
  }, [resetLifecycle]);
  const openOAuth = useCallback((account: Account) => {
    oauthMutation.reset();
    oauthPreflight.reset();
    oauthPreflight.mutate(account);
  }, [oauthMutation, oauthPreflight]);
  useEffect(() => {
    const signature = searchParams.toString();
    if (!signature || handledDeepLink.current === signature) return;
    if (searchParams.get("create") === "1") {
      handledDeepLink.current = signature;
      openEditor("create");
      setSearchParams({}, { replace: true });
      return;
    }
    if (searchParams.get("auth") === "pending") {
      handledDeepLink.current = signature;
      setAuthFilter("pending");
      setSearchParams({}, { replace: true });
    }
  }, [openEditor, searchParams, setSearchParams]);
  const closeOAuth = useCallback(() => {
    setOAuthAccount(null);
    oauthMutation.reset();
  }, [oauthMutation]);
  const closeTaskOutput = useCallback(() => {
    setTaskJob(null);
    setTaskPollError(null);
    setCompletedTaskJobID("");
    taskCancel.reset();
  }, [taskCancel]);
  const toggleExpandedAccount = useCallback((accountID: string) => {
    setExpandedAccountIDs((current) => current.includes(accountID) ? [] : [accountID]);
  }, []);
  const changeAccountSort = useCallback((field: AccountSortField) => {
    setAccountSort((current) => current.field === field
      ? { field, direction: current.direction === "asc" ? "desc" : "asc" }
      : { field, direction: ["account", "runtime", "auth", "quota"].includes(field) ? "asc" : "desc" });
  }, []);
  const columns = useMemo(() => accountColumns({
    sort: accountSort,
    windowSeconds: accounts.data?.active_user_window_seconds ?? 900,
    onSort: changeAccountSort,
    onResetQuota: (account) => {
      quotaReset.reset();
      setQuotaResetAccount(account);
    }
  }), [accountSort, accounts.data?.active_user_window_seconds, changeAccountSort, quotaReset]);

  const catalog = accounts.data ?? emptyAccountCatalog;
  const enabledAccounts = catalog.accounts.filter((account) => account.enabled).length;
  const runningEnabledAccounts = catalog.accounts.filter(
    (account) => account.enabled && account.runtime_state === "running"
  ).length;
  const normalizedSearch = search.trim().toLowerCase();
  const filteredAccounts = catalog.accounts.filter((account) => {
    if (normalizedSearch && ![account.id, account.email, String(account.port)]
      .some((value) => value.toLowerCase().includes(normalizedSearch))) return false;
    if (runtimeFilter !== "all" && account.runtime_state !== runtimeFilter) return false;
    const authConfigured = accountOAuthConfigured(account);
    if (authFilter === "configured" && authConfigured !== true) return false;
    if (authFilter === "pending" && authConfigured !== false) return false;
    return true;
  }).sort((left, right) => compareAccountsForSort(left, right, accountSort));
  const visibleImageStatus = accounts.data ? imageStatus.data : undefined;
  const imageTarget = visibleImageStatus?.update_channel || visibleImageStatus?.target_image || t("common.loading_2");
  const localImage = visibleImageStatus?.local_image;
  const imageStatusLabel = !accounts.data
    ? t("common.unknown")
    : imageStatus.isError
    ? t("common.unknown")
    : !localImage?.available
      ? t("admin.not_pulled")
      : (visibleImageStatus?.outdated_count ?? 0) > 0 ? t("admin.update_available") : t("admin.up_to_date");
  const imageSummary = !accounts.data
    ? "—"
    : localImage?.available
      ? t("admin.enabled_cpas_running", [localImage.version || t("admin.image_has_no_recognizable_version"), localImage.short_id || t("admin.unknown_digest"), visibleImageStatus?.current_count ?? 0, visibleImageStatus?.running_count ?? 0])
      : t("admin.enabled_cpas_running_2", [runningEnabledAccounts]);

  const rangeBoundary = (timestamp: number | null | undefined, empty = "—") => {
    if (accounts.isFetching) return "…";
    if (!accounts.data || accounts.isError) return "—";
    return timestamp != null && timestamp > 0 ? formatSiteTimestamp(timestamp) : empty;
  };

  return (
    <section className="page-content account-page">
      <div className="account-management-panel">
        <div className="account-management-toolbar management-toolbar">
          <ManagementUsageTimeFilter
            value={usageWindow} options={usageWindowOptions} label={t("admin.account_usage")}
            onChange={setUsageWindow} onCustomSelect={() => setCustomUsageRangeOpen(true)}
            start={rangeBoundary(usageWindow === "since_reset" ? null : accounts.data?.window_start_at, usageWindow === "since_reset" ? t("admin.each_account_s_reset_time") : usageWindow === "all" ? t("admin.unlimited") : "—")}
            end={rangeBoundary(accounts.data?.window_end_at)} updating={accounts.isFetching}
          />
          <div className="account-time-filter-actions">
            <AccountFilter label={t("admin.search_accounts")}>
              <Input
                className="account-search-input"
                aria-label={t("admin.search_cpa_accounts")}
                prefix={<span className="account-search-legacy-icon" aria-hidden="true" />}
                placeholder={t("admin.account_name_or_email")}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </AccountFilter>
            <div className="account-filter-actions">
              <AccountFilter label={t("admin.runtime_status")}>
                <WideSelect<AccountRuntimeFilter>
                  aria-label={t("admin.runtime_status")}
                  value={runtimeFilter}
                  options={runtimeFilterOptions}
                  onChange={setRuntimeFilter}
                />
              </AccountFilter>
              <AccountFilter label="OAuth">
                <WideSelect<AccountAuthFilter>
                  aria-label="OAuth"
                  value={authFilter}
                  options={authFilterOptions}
                  onChange={setAuthFilter}
                />
              </AccountFilter>
              <Button type="primary" onClick={() => openEditor("create")}>
 {t("admin.add_cpa")} </Button>
            </div>
          </div>
        </div>

        <div className="account-control-strip">
          <div className="account-control-copy">
            <h3>{t("admin.update_channel")}</h3>
            <code>{imageTarget}</code>
            <small>{imageSummary}</small>
          </div>
          <div className="account-control-status-region">
            <Tag className={`account-control-status ${imageStatusLabel === t("admin.up_to_date") ? "success" : imageStatusLabel === t("common.unknown") ? "neutral" : "warning"}`}>{imageStatusLabel}</Tag>
          </div>
          <Space className="account-control-actions" size={8}>
            <Button
              loading={imageMutation.isPending && imageMutation.variables?.action === "image-pull"}
              disabled={imageMutation.isPending}
              onClick={() => {
                imageMutation.reset();
                imageMutation.mutate({ action: "image-pull", target: "all" });
              }}
            >{t("admin.pull_image")}</Button>
            <Button
              type="primary"
              disabled={imageMutation.isPending || Boolean(accounts.data && (
                !localImage?.available || (visibleImageStatus?.outdated_count ?? 0) === 0
              ))}
              onClick={() => {
                imageMutation.reset();
                setImageUpdateTarget("all");
              }}
            >{t("admin.update_all_cpas")}</Button>
            <Button
              className="account-rebalance-all-button"
              disabled={enabledAccounts < 2}
              onClick={() => {
                rebalance.reset();
                setConfirmOpen(true);
              }}
            >{t("admin.balance_all_users")}</Button>
          </Space>
        </div>

        <div className="account-panel-notices">
          {catalog.warnings.map((warning) => (
            <Alert key={warning} className="page-alert" type="warning" showIcon title={warning} />
          ))}
          {lastResult ? (
            <Alert
              className="page-alert"
              type={lastResult.rebalance.warning ? "warning" : "success"}
              showIcon
              closable
              onClose={() => setLastResult(null)}
              title={lastResult.message}
              description={<RebalanceSummary result={lastResult} />}
            />
          ) : null}
        </div>

        <div className={`account-table-state${filteredAccounts.length ? "" : " is-empty"}`}>
          <AdminTable<Account>
            rowKey="id"
            columns={columns}
            className="account-legacy-table"
            dataSource={filteredAccounts}
            minWidth="100%"
            fillAvailable
            size="small"
            locale={{ emptyText: <span className="account-empty-placeholder" aria-hidden="true" /> }}
            rowClassName={(account) => expandedAccountIDs.includes(account.id) ? "account-summary-row expanded" : "account-summary-row"}
            onRow={(account) => ({
              tabIndex: 0,
              "aria-label": `${expandedAccountIDs.includes(account.id) ? t("admin.collapse") : t("admin.expand")} ${account.id}`,
              "aria-expanded": expandedAccountIDs.includes(account.id),
              onClick: (event) => {
                if (!isInteractiveRowTarget(event.target)) toggleExpandedAccount(account.id);
              },
              onKeyDown: (event) => {
                const rowOwnsEvent = event.target === event.currentTarget;
                if ((event.key === "Enter" || event.key === " ") && (rowOwnsEvent || !isInteractiveRowTarget(event.target))) {
                  event.preventDefault();
                  toggleExpandedAccount(account.id);
                }
              }
            })}
            expandable={{
              expandedRowKeys: expandedAccountIDs,
              showExpandColumn: false,
              expandedRowRender: (account) => (
                <AccountExpandedRow
                  account={account}
                  usageRange={usageRange}
                  onModelTest={setModelTestAccount}
                  imageStatus={imageStatus}
                  onEdit={openEditor}
                  onOAuth={openOAuth}
                  onPolicy={openAccountPolicy}
                  onRuntimeOperation={(operation) => {
                    runtimeMutation.reset();
                    if (operation.action === "start") runtimeMutation.mutate(operation);
                    else setRuntimeOperation(operation);
                  }}
                  onOpenLogs={setLogTarget}
                  onRebalance={setRebalanceTarget}
                  onUpdateImage={(account) => {
                    imageMutation.reset();
                    setImageUpdateTarget(account);
                  }}
                />
              )
            }}
          />
          {!accounts.isPending && !accounts.isError && !filteredAccounts.length ? (
            <div className="account-empty-state">
              <div className="account-empty-icon" aria-hidden="true">▣</div>
              <h3>{catalog.accounts.length ? t("admin.no_matching_cpas") : t("admin.no_cpa_accounts_yet")}</h3>
              <Button type="primary" onClick={() => openEditor("create")}>{t("admin.add_cpa")}</Button>
            </div>
          ) : null}
        </div>
      </div>

      {modelTestAccount ? <AccountModelTestModal account={modelTestAccount} csrfToken={csrfToken} onClose={() => setModelTestAccount(null)} /> : null}
      <CustomUsageRangeModal
        open={customUsageRangeOpen}
        title={t("admin.custom_account_usage_range")}
        range={customUsageRange}
        onCancel={() => setCustomUsageRangeOpen(false)}
        onApply={(range) => {
          setCustomUsageRange(range);
          setUsageWindow("custom");
          setCustomUsageRangeOpen(false);
        }}
      />

      <AccountEditorModal
        open={editorAccount !== null}
        account={editorAccount === "create" ? null : editorAccount}
        pending={lifecycle.isPending}
        error={destructiveAction ? null : lifecycle.error}
        onCancel={() => !lifecycle.isPending && setEditorAccount(null)}
        onSubmit={(command) => {
          if (command.kind === "update" && editorAccount && editorAccount !== "create") {
            const renamed = command.request.new_id !== editorAccount.id;
            const proxyChanged = command.request.proxy_mode !== editorAccount.proxy_mode || Boolean(command.request.proxy_url);
            if (renamed || proxyChanged) {
              setPendingAccountUpdate({ command, renamed, proxyChanged });
              return;
            }
          }
          lifecycle.mutate(command);
        }}
        onDestructiveAction={(action) => {
          openDestructiveAction(action);
        }}
      />
      <LegacyConfirmModal
        title={pendingAccountUpdate?.renamed ? t("admin.update_cpa_account") : t("admin.update_outbound_proxy")}
        open={pendingAccountUpdate !== null}
        okText={t("admin.confirm_update")}
        onCancel={() => setPendingAccountUpdate(null)}
        onOk={() => {
          const command = pendingAccountUpdate?.command;
          setPendingAccountUpdate(null);
          if (command) lifecycle.mutate(command);
        }}
      >
        <Paragraph>
          {pendingAccountUpdate ? t("admin.affected_containers_will_restart_briefly_oauth_logs_and_key_associations", [[
            pendingAccountUpdate.renamed
              ? t("admin.will_be_renamed_to", [pendingAccountUpdate.command.request.id, pendingAccountUpdate.command.request.new_id])
              : "",
            pendingAccountUpdate.proxyChanged ? t("admin.outbound_proxy_settings_will_be_updated") : ""
          ].filter(Boolean).join("；")]) : ""}
        </Paragraph>
      </LegacyConfirmModal>
      <AccountPolicyModal
        key={policyAccount?.id ?? "closed"}
        account={policyAccount}
        accounts={catalog.accounts}
        pending={lifecycle.isPending}
        error={lifecycle.error}
        onCancel={() => !lifecycle.isPending && setPolicyAccount(null)}
        onSubmit={(request) => lifecycle.mutate({ kind: "policy", request })}
      />
      <AccountDestructiveModal
        action={destructiveAction}
        accounts={catalog.accounts}
        pending={lifecycle.isPending}
        error={lifecycle.error}
        onCancel={() => !lifecycle.isPending && setDestructiveAction(null)}
        onSubmit={(command) => {
          if (command.kind === "clear-auth") setDestructiveAction(null);
          lifecycle.mutate(command);
        }}
      />
      <OAuthFlowModals
        account={oauthAccount}
        starting={oauthMutation.isPending}
        startError={oauthMutation.error}
        onStart={() => {
          const account = oauthAccount;
          setOAuthAccount(null);
          if (account) oauthMutation.mutate(account);
        }}
        onClose={closeOAuth}
      />
      <TaskOutputModal
        job={taskJob}
        accountEmail={catalog.accounts.find((account) => account.id === taskJob?.target)?.email ?? ""}
        pollError={taskPollError}
        cancelling={taskCancel.isPending}
        onCancelJob={() => taskJob && taskCancel.mutate(taskJob.id)}
        onClose={closeTaskOutput}
      />
      <QuotaResetModal
        account={quotaResetAccount}
        query={quotaResetDetails}
        pending={quotaReset.isPending}
        error={quotaReset.error}
        onClose={() => !quotaReset.isPending && setQuotaResetAccount(null)}
        onSubmit={(creditID) => quotaResetAccount && quotaReset.mutate({ account: quotaResetAccount.id, creditID })}
      />
      <Modal
        title={t("admin.balance_users_across_all_accounts")}
        open={confirmOpen}
        confirmLoading={rebalance.isPending}
        okText={t("admin.start_balancing")}
        cancelText={t("common.cancel")}
        okButtonProps={{ danger: true }}
        onCancel={() => !rebalance.isPending && setConfirmOpen(false)}
        onOk={() => rebalance.mutate()}
        destroyOnHidden
      >
        <Space orientation="vertical" size={16} className="rebalance-confirmation">
          <Alert
            type="warning"
            showIcon
            title={t("admin.this_changes_users_current_routes")}
            description={t("admin.users_will_be_redistributed_according_to_available_account_quotas_with")}
          />
          <Paragraph>
 {t("admin.after_routes_are_saved_the_gateway_must_activate_the_new")} </Paragraph>
          {rebalance.isError ? <MutationError error={rebalance.error} title={t("admin.load_balancing_was_not_performed")} /> : null}
        </Space>
      </Modal>
      <LegacyConfirmModal
        title={rebalanceTarget ? t("admin.move_all_users_from", [rebalanceTarget.id]) : t("admin.move_all_users_3")}
        open={rebalanceTarget !== null}
        confirmLoading={accountRebalance.isPending}
        okText={t("admin.confirm_move")}
        okDisabled={!rebalanceTarget || rebalanceTarget.routed_users <= 0 || catalog.accounts.length < 2}
        onCancel={() => !accountRebalance.isPending && setRebalanceTarget(null)}
        onOk={() => {
          const account = rebalanceTarget;
          setRebalanceTarget(null);
          if (account) accountRebalance.mutate(account.id);
        }}
      >
        <Space orientation="vertical" size={14} className="rebalance-confirmation">
          <Paragraph>
            {rebalanceTarget
              ? t("admin.official_quotas_will_be_refreshed_first_then_users_will_be", [formatNumber(rebalanceTarget.routed_users)])
              : t("admin.official_quotas_will_be_refreshed_first_users_will_then_be")}
          </Paragraph>
        </Space>
      </LegacyConfirmModal>
      <LegacyConfirmModal
        title={runtimeOperation?.action === "stop" ? t("admin.stop_service_2") : t("admin.restart_service_2")}
        open={runtimeOperation !== null}
        okText={runtimeOperation?.action === "stop" ? t("admin.confirm_stop") : t("admin.confirm_restart")}
        danger={runtimeOperation?.action === "stop"}
        confirmLoading={runtimeMutation.isPending}
        onCancel={() => !runtimeMutation.isPending && setRuntimeOperation(null)}
        onOk={() => {
          const operation = runtimeOperation;
          setRuntimeOperation(null);
          if (operation) runtimeMutation.mutate(operation);
        }}
      >
        {runtimeOperation?.action === "stop" ? (
          runtimeImpact.isPending ? <Skeleton active paragraph={{ rows: 2 }} /> : (
            <Paragraph>
              {runtimeImpact.isError || runtimeImpact.data?.routed_users === null
                ? t("admin.will_be_stopped_the_impact_cannot_currently_be_determined", [runtimeOperation.account.id])
                : runtimeImpact.data?.routed_users
                  ? t("admin.will_be_stopped_users_are_currently_routed_to_this_account", [runtimeOperation.account.id, formatNumber(runtimeImpact.data.routed_users)])
                  : t("admin.will_be_stopped_no_users_are_currently_routed_to_this", [runtimeOperation.account.id])}
            </Paragraph>
          )
        ) : (
          <Paragraph>{runtimeOperation ? t("admin.will_be_restarted", [runtimeOperation.account.id]) : ""}</Paragraph>
        )}
      </LegacyConfirmModal>
      <RuntimeLogsModal
        target={logTarget}
        query={logs}
        onClose={() => setLogTarget(null)}
      />
      <LegacyConfirmModal
        title={t("admin.update_cpa_image_2")}
        open={imageUpdateTarget !== null}
        confirmLoading={imageMutation.isPending}
        okText={imageUpdateTarget === "all" ? t("admin.update_all_cpas") : t("admin.update_this_cpa")}
        onCancel={() => !imageMutation.isPending && setImageUpdateTarget(null)}
        onOk={() => {
          const target = imageUpdateTarget === "all" ? "all" : imageUpdateTarget?.id;
          setImageUpdateTarget(null);
          if (target) imageMutation.mutate({ action: "image-update", target });
        }}
      >
        <Paragraph>
          {imageUpdateTarget === "all"
            ? t("admin.running_enabled_cpas_will_be_recreated_one_at_a_time")
            : imageUpdateTarget
              ? t("admin.will_be_recreated_using_the_image_with_its_pinned_version", [imageUpdateTarget.id])
              : ""}
        </Paragraph>
      </LegacyConfirmModal>
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function accountColumns({
  sort,
  windowSeconds,
  onSort,
  onResetQuota
}: {
  sort: AccountSortState;
  windowSeconds: number;
  onSort: (field: AccountSortField) => void;
  onResetQuota: (account: Account) => void;
}): TableColumnsType<Account> {
  return [
    {
      title: t("common.no"),
      width: "4%",
      align: "center",
      render: (_, __, index) => index + 1
    },
    {
      title: <span className="sr-only">{t("admin.expand")}</span>,
      width: "4%",
      align: "center",
      render: () => (
        <div className="account-cell-content account-toggle-content">
          <span className="account-chevron" aria-hidden="true">›</span>
        </div>
      )
    },
    {
      ...accountSortHeader("account", t("common.cpa_account"), sort, onSort),
      dataIndex: "id",
      width: "15%",
      render: (_, account) => (
        <div className="account-cell-content">
          <div className="account-name-cell">
            <span className="table-primary">{account.id}</span>
            <span className="table-secondary">:{account.port}</span>
          </div>
        </div>
      )
    },
    {
      ...accountSortHeader("runtime", t("common.account_status"), sort, onSort),
      align: "center",
      width: "9%",
      render: (_, account) => (
        <div className="account-cell-content">
          <div className="account-tag-stack"><AccountStatus account={account} /></div>
        </div>
      )
    },
    {
      ...accountSortHeader("auth", "OAuth", sort, onSort),
      align: "center",
      width: "8%",
      render: (_, account) => (
        <div className="account-cell-content">
          <div className="account-tag-stack"><AccountOAuthStatus account={account} /></div>
        </div>
      )
    },
    {
      ...accountSortHeader("quota", t("admin.quota_reset"), sort, onSort),
      width: "24%",
      render: (_, account) => {
        const used = account.account_state.used_percent;
        if (!account.state_available || used === null) {
          return (
            <div className="account-cell-content">
              <div className="account-quota-overview">
                <div className="account-quota-main">
                  <div className="account-quota-unavailable">
                    <span className="table-secondary quota-unavailable">{t("admin.unavailable")}</span>
                  </div>
                </div>
                <div className="account-quota-reset-cell">
                  <span>{accountResetCreditLabel(account)}</span>
                  <Button size="small" disabled>{t("admin.reset")}</Button>
                </div>
              </div>
            </div>
          );
        }
        const bounded = Math.max(0, Math.min(100, used));
        const quotaTone = bounded >= 100 ? "danger" : bounded >= 80 ? "warning" : "success";
        return (
          <div className="account-cell-content">
            <div className="account-quota-overview">
              <div className="account-quota-main">
                <div className="account-quota-cell quota-cell">
                  <div><strong>{formatPercent(bounded)}</strong></div>
                  <progress className={quotaTone} max="100" value={bounded} aria-label={t("common.used", [formatPercent(bounded)])} />
                  <small>{account.account_state.reset_at ? t("admin.next_reset_2", [formatSiteTimestamp(account.account_state.reset_at)]) : t("common.unknown_reset_time")}</small>
                </div>
              </div>
              <div className="account-quota-reset-cell quota-reset-cell">
                <span className="quota-reset-count">{accountResetCreditLabel(account)}</span>
                <button
                  className="quota-reset-action"
                  type="button"
                  aria-label={t("admin.reset")}
                  disabled={!account.resettable}
                  title={account.resettable ? t("admin.reset_2", [account.reset_window_labels?.join("、") || t("admin.weekly_limit")]) : account.reset_credit_count === 0 ? t("admin.no_resets_remaining") : t("admin.weekly_quota_is_not_exhausted_or_its_status_needs_refreshing")}
                  onClick={() => onResetQuota(account)}
                >{t("admin.reset")}</button>
              </div>
            </div>
          </div>
        );
      }
    },
    {
      ...accountSortHeader("activity", t("admin.usage"), sort, onSort),
      width: "20%",
      render: (_, account) => (
        <div className="account-cell-content"><AccountActivity account={account} windowSeconds={windowSeconds} /></div>
      )
    },
    {
      ...accountSortHeader("tokens", "Token", sort, onSort),
      align: "right",
      width: "9%",
      render: (_, account) => (
        <div className="account-cell-content account-token-content"><AccountTokenUsage account={account} /></div>
      )
    },
    {
      ...accountSortHeader("last_used", t("common.last_used"), sort, onSort),
      align: "center",
      width: "7%",
      render: (_, account) => {
        const timestamp = account.usage.last_used_at;
        const label = account.usage_available ? formatAccountLastUsed(timestamp) : "—";
        return (
          <div className="account-cell-content">
            {account.usage_available && timestamp > 0 ? (
              <time className="account-last-used" dateTime={new Date(timestamp * 1000).toISOString()} title={label}>
                {label.replace(" ", "\n")}
              </time>
            ) : label}
          </div>
        );
      }
    }
  ];
}

function accountSortHeader(
  field: AccountSortField,
  label: string,
  sort: AccountSortState,
  onSort: (field: AccountSortField) => void
) {
  const active = sort.field === field;
  const ariaSort = active ? (sort.direction === "asc" ? "ascending" : "descending") : "none";
  return {
    title: (
      <button
        className={`sort-button${active ? " active" : ""}`}
        type="button"
        data-account-sort={field}
        data-direction={active ? sort.direction : undefined}
        aria-label={active
          ? t("admin.currently_click_to_reverse_the_sort_order", [label, sort.direction === "asc" ? t("common.ascending") : t("common.descending")])
          : t("admin.click_to_sort", [label])}
        onClick={() => onSort(field)}
      >{label}</button>
    ),
    onHeaderCell: () => ({ "aria-sort": ariaSort as "ascending" | "descending" | "none" })
  };
}

function AccountFilter({
  label,
  className = "",
  children
}: {
  label: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <label className={["account-filter-field", className].filter(Boolean).join(" ")}>
      <span>{label}</span>
      {children}
    </label>
  );
}

function AccountExpandedRow({
  account,
  usageRange,
  imageStatus,
  onEdit,
  onOAuth,
  onPolicy,
  onRuntimeOperation,
  onOpenLogs,
  onModelTest,
  onRebalance,
  onUpdateImage
}: {
  account: Account;
  usageRange: AccountUsageRange;
  imageStatus: UseQueryResult<CpaImageStatus>;
  onEdit: (account: Account) => void;
  onOAuth: (account: Account) => void;
  onPolicy: (account: Account) => void;
  onRuntimeOperation: (operation: PendingAccountRuntimeOperation) => void;
  onOpenLogs: (account: string) => void;
  onModelTest: (account: Account) => void;
  onRebalance: (account: Account) => void;
  onUpdateImage: (account: Account) => void;
}) {
  const usageDetail = useQuery({
    queryKey: [...usageBreakdownQueryKey("account", account.id, usageRange), "inline"],
    queryFn: ({ signal }) => readUsageBreakdown("account", account.id, usageRange, signal),
    staleTime: 0,
    gcTime: 0,
    retry: false
  });
  const image = imageStatus.data?.accounts?.find((item) => item.account === account.id);
  const running = (account.container_state || account.runtime_state) === "running";
  const availableOtherAccounts = imageStatus.data?.accounts?.some(
    (candidate) => candidate.account !== account.id && candidate.enabled
  ) ?? true;
  const imageUpdateDisabled = !account.enabled
    || !image?.running
    || !imageStatus.data?.local_image?.available
    || image.using_target;
  const imageUpdateTitle = !account.enabled
    ? t("admin.this_cpa_account_is_disabled_enable_it_before_updating_the")
    : !image?.running
      ? t("admin.this_cpa_is_not_running_after_pulling_the_target_image")
      : !imageStatus.data?.local_image?.available
        ? t("admin.pull_the_target_image_first")
        : image.using_target
          ? t("admin.this_cpa_already_uses_the_target_image")
          : t("admin.recreate_this_cpa_using_the_target_image");
  return (
    <div className="account-expanded-panel">
      <div className="account-detail-facts account-runtime-facts">
        <AccountDetailFact label={t("admin.upstream_email")} value={account.email} />
        <AccountDetailFact
          label={t("admin.container")}
          value={account.service || image?.service || `cliproxy-${account.id}`}
          note={account.container_status || runtimeStateLabel[account.runtime_state]}
        />
        <AccountDetailFact
          label={t("common.account_status")}
          value={(account.operational_status?.label || accountStatusLabel(account))}
          note={account.operational_status ? accountRuntimeDetail(account) : (stateLabels[account.account_state.reason] ?? account.account_state.reason)}
        />
        <AccountDetailFact
          label={t("admin.oauth_files")}
          value={account.auth_files ?? (account.oauth_configured === null ? "—" : account.oauth_configured ? 1 : 0)}
        />
        <AccountDetailFact
          label={t("admin.image_version")}
          value={image?.version || (imageStatus.isPending ? t("common.loading_3") : "—")}
          note={image?.image_short_id ? `SHA ${image.image_short_id}` : imageStatus.isError ? t("admin.image_status_unavailable") : ""}
        />
        <AccountDetailFact
          label={t("admin.outbound_proxy")}
          value={account.proxy_source === "account" ? t("admin.account_override") : account.proxy_source === "default" ? t("admin.control_plane_default") : t("admin.direct_connection")}
          note={account.proxy_display || "direct"}
        />
      </div>

      {account.usage_available ? <AccountUsageFacts usage={account.usage} /> : (
        <div className="account-usage-unavailable" role="status">
          <strong>{t("admin.usage_for_this_range_is_unavailable")}</strong>
          <span>{t("admin.refresh_account_data_and_try_again")}</span>
        </div>
      )}

      <AccountModelUsage query={usageDetail} />

      <div className="account-detail-actions">
        <div className="account-detail-action-group" role="group" aria-label={t("admin.common_actions")}>
          <button className="button ghost" type="button" disabled={!running} title={!running ? t("admin.start_the_account_container_first") : undefined} onClick={() => onModelTest(account)}>{t("common.test_model_connection")}</button>
          <button className="button ghost" type="button" onClick={() => onOpenLogs(account.id)}>{t("admin.view_logs")}</button>
          <button className="button ghost" type="button" aria-label={t("admin.edit_2", [account.id])} onClick={() => onEdit(account)}>{t("admin.edit_account")}</button>
          <button className="button ghost" type="button" onClick={() => onOAuth(account)}>
            {account.oauth_configured === true ? t("admin.reauthorize_oauth") : t("admin.start_oauth")}
          </button>
        </div>
        <div className="account-detail-action-group" role="group" aria-label={t("admin.container_maintenance")}>
          <button
            className="button ghost"
            type="button"
            disabled={imageUpdateDisabled}
            title={imageUpdateTitle}
            onClick={() => onUpdateImage(account)}
          >{image?.using_target ? t("admin.image_up_to_date") : t("admin.update_image")}</button>
          <button className="button ghost" type="button" onClick={() => onRuntimeOperation({ action: running ? "restart" : "start", account })}>
            {running ? t("admin.restart_container") : t("admin.start_container")}
          </button>
        </div>
        <div className="account-detail-action-group account-detail-action-group-risk" role="group" aria-label={t("admin.sensitive_actions")}>
          <button
            className="button account-rebalance-action"
            type="button"
            disabled={account.routed_users <= 0}
            title={account.routed_users <= 0 ? t("admin.this_account_has_no_users_to_move") : t("admin.assign_routed_users_to_other_available_accounts_using_the_automatic", [formatNumber(account.routed_users)])}
            aria-label={t("admin.move_all_users_2", [account.routed_users <= 0 ? t("admin.this_account_has_no_users_to_move") : t("admin.assign_routed_users_to_other_available_accounts_using_the_automatic", [formatNumber(account.routed_users)])])}
            onClick={() => onRebalance(account)}
          >
 {t("admin.move_all_users")} </button>
          {running ? (
            <button className="button danger-outline" type="button" onClick={() => onRuntimeOperation({ action: "stop", account })}>{t("admin.stop_container")}</button>
          ) : null}
          <button
            className={`button ${account.enabled ? "danger-outline is-enabled" : "secondary"} account-policy-action`}
            type="button"
            disabled={account.enabled && !availableOtherAccounts}
            title={account.enabled && !availableOtherAccounts ? t("admin.keep_at_least_one_available_cpa") : undefined}
            onClick={() => onPolicy(account)}
          >
            {account.enabled ? t("admin.disable_account") : t("admin.enable_account")}
          </button>
        </div>
      </div>
    </div>
  );
}

function AccountDetailFact({ label, value, note = "", className }: {
  label: string;
  value: React.ReactNode;
  note?: string;
  className?: string;
}) {
  const primitive = typeof value === "string" || typeof value === "number";
  return (
    <div className={className}>
      <span>{label}</span>
      {primitive ? <strong title={String(value)}>{value}</strong> : value}
      {note ? <small className="account-runtime-note" title={note}>{note.replaceAll("\n", " · ")}</small> : null}
    </div>
  );
}

function AccountUsageFacts({ usage }: { usage: Account["usage"] }) {
  return (
    <div className="account-detail-facts account-usage-facts">
      <AccountDetailFact className="account-request-fact" label={t("common.successful_requests")} value={formatNumber(usage.success_count)} />
      <AccountDetailFact className="account-request-fact" label={t("common.failed_requests")} value={formatNumber(usage.failed_count)} />
      <AccountDetailFact className="account-token-fact" label={t("common.input_tokens")} value={<LegacyTokenUsage value={usage.input_tokens} />} />
      <AccountDetailFact className="account-token-fact" label={t("common.output_tokens")} value={<LegacyTokenUsage value={usage.output_tokens} />} />
      <AccountDetailFact className="account-token-fact" label={t("common.reasoning_tokens")} value={<LegacyTokenUsage value={usage.reasoning_tokens} />} />
      <div className="account-cache-fact account-token-fact">
        <div className="account-cache-head">
          <small title={t("common.cached_tokens_input_tokens")}>{t("common.cache_rate")} {formatRatio(usage.cached_tokens, usage.input_tokens)}</small>
          <span>{t("common.cached_tokens")}</span>
        </div>
        <LegacyTokenUsage value={usage.cached_tokens} />
      </div>
      <div className="account-token-total-fact account-token-fact">
        <span>{t("admin.total_tokens")}</span>
        <LegacyTokenUsage value={usage.total_tokens} />
      </div>
    </div>
  );
}

function LegacyTokenUsage({ value }: { value: number }) {
  const token = formatLegacyTokenUsage(value);
  return (
    <span className="token-usage">
      <span className="token-usage-main" aria-hidden="true">
        <span className="token-usage-value">{token.amount}</span>
        <small className="token-usage-unit">{token.unit}</small>
      </span>
      {token.compacted ? <small className="token-usage-exact" aria-hidden="true">{token.label}</small> : null}
      <span className="token-usage-sr-only">{token.label}</span>
    </span>
  );
}

function AccountModelUsage({ query }: { query: UseQueryResult<UsageBreakdown> }) {
  const models = groupAccountModels(query.data?.combinations ?? []);
  return (
    <section className="account-model-usage" aria-label={t("admin.tokens_by_model_and_reasoning_effort")}>
      <div className="account-model-usage-title">{t("admin.model_reasoning_effort_tokens")}</div>
      {query.isPending ? (
        <div className="account-model-usage-skeleton" aria-label={t("admin.loading_model_token_details")}><span /><span /></div>
      ) : query.isError ? (
        <div className="account-model-usage-message error" role="alert">
          <span>{query.error instanceof Error ? query.error.message : t("admin.unable_to_load_model_token_details")}</span>
          <Button size="small" onClick={() => void query.refetch()}>{t("common.retry")}</Button>
        </div>
      ) : models.length ? (
        <div className="account-model-usage-list">
          {models.map((model) => (
            <div className="account-model-usage-row" key={model.model}>
              <div className="account-model-usage-head">
                <strong title={model.model}>{model.model}</strong>
                <LegacyTokenUsage value={model.totalTokens} />
              </div>
              <div className="account-model-progress" role="group" aria-label={t("common.token_share_by_reasoning_effort", [model.model])}>
                {model.efforts.map((effort) => {
                  const share = formatModelShare(effort.sharePercent);
                  const shareUnits = Math.max(1, Math.min(100, Math.round(effort.sharePercent)));
                  const tooltip = accountModelTooltip(model.model, effort);
                  return (
                    <button
                      key={effort.reasoning_effort}
                      className={`account-model-progress-segment account-model-effort-${effortColorKey(effort.reasoning_effort)} account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}${effort.sharePercent < 18 ? " compact" : ""}`}
                      type="button"
                      data-tooltip={tooltip.join("\n")}
                      aria-label={tooltip.join("，")}
                    >
                      <span>{effort.reasoning_effort}</span>
                      <em>{share}</em>
                    </button>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      ) : (
        <div className="account-model-usage-message">{t("admin.no_model_token_data_for_this_range")}</div>
      )}
    </section>
  );
}

function accountModelTooltip(model: string, effort: AccountModelEffort) {
  return [
    `${model} · ${effort.reasoning_effort}`,
    t("admin.requests_2", [formatNumber(effort.request_count)]),
    t("admin.input_2", [formatNumber(effort.input_tokens)]),
    t("admin.output_2", [formatNumber(effort.output_tokens)]),
    t("admin.reasoning_2", [formatNumber(effort.reasoning_tokens)]),
    t("admin.cached_2", [formatNumber(effort.cached_tokens)]),
    t("admin.total_tokens_3", [formatNumber(effort.total_tokens)])
  ];
}

function formatLegacyTokenUsage(input: number) {
  const value = Number.isFinite(input) && input >= 0 ? Math.floor(input) : 0;
  let divisor = 1;
  let unit = "Token";
  if (value >= 1_000_000_000) [divisor, unit] = [1_000_000_000, "B"];
  else if (value >= 1_000_000) [divisor, unit] = [1_000_000, "M"];
  else if (value >= 1_000) [divisor, unit] = [1_000, "K"];
  let rounded = Math.round(value / divisor * 10) / 10;
  if (unit === "K" && rounded >= 1000) {
    divisor = 1_000_000;
    unit = "M";
    rounded = Math.round(value / divisor * 10) / 10;
  }
  if (unit === "M" && rounded >= 1000) {
    divisor = 1_000_000_000;
    unit = "B";
    rounded = Math.round(value / divisor * 10) / 10;
  }
  return {
    amount: new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(rounded),
    unit,
    label: `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 }).format(value)} Token`,
    compacted: divisor > 1
  };
}

function formatModelShare(value: number) {
  return `${new Intl.NumberFormat(getIntlLocale(), { maximumFractionDigits: 1 }).format(value)}%`;
}

type AccountModelEffort = UsageCombination & { sharePercent: number };
type AccountModelRow = { model: string; totalTokens: number; efforts: AccountModelEffort[] };

function groupAccountModels(combinations: UsageCombination[]): AccountModelRow[] {
  const grouped = new Map<string, UsageCombination[]>();
  combinations.forEach((item) => {
    const current = grouped.get(item.model) ?? [];
    current.push(item);
    grouped.set(item.model, current);
  });
  return [...grouped.entries()].map(([model, efforts]) => {
    const totalTokens = efforts.reduce((total, effort) => total + effort.total_tokens, 0);
    return {
      model,
      totalTokens,
      efforts: efforts
        .map((effort) => ({
          ...effort,
          sharePercent: totalTokens > 0 ? effort.total_tokens * 100 / totalTokens : 0
        }))
        .sort((left, right) => right.total_tokens - left.total_tokens)
    };
  }).sort((left, right) => right.totalTokens - left.totalTokens || left.model.localeCompare(right.model));
}

function effortColorKey(effort: string) {
  return ["none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max", "auto"].includes(effort)
    ? effort
    : "unknown";
}

function formatRatio(numerator: number, denominator: number) {
  if (!denominator) return "0%";
  return `${new Intl.NumberFormat(getIntlLocale(), { maximumFractionDigits: 1 }).format(numerator * 100 / denominator)}%`;
}

function AccountOAuthStatus({ account }: { account: Account }) {
  const configured = accountOAuthConfigured(account);
  if (configured === null) return <span className="status-chip neutral">{t("common.unknown")}</span>;
  return <span className={`status-chip ${configured ? "success" : "warning"}`}>{configured ? t("admin.authorized") : t("admin.authorization_required")}</span>;
}

function AccountActivity({ account, windowSeconds }: { account: Account; windowSeconds: number }) {
  const activeEmails = [...new Set((account.active_user_emails_1h ?? []).map((email) => email.trim()).filter(Boolean))];
  const activeUsers = account.active_users_1h;
  const activeValue = activeUsers === null ? "—" : formatNumber(activeUsers);
  const windowLabel = formatActiveUserWindow(windowSeconds);
  const activeDetail = activeUsers === null ? t("admin.data_unavailable") : activeUsers === 0 ? t("admin.no_requests_in", [windowLabel]) : windowLabel;
  const activeHelp = t("admin.unique_users_with_at_least_one_request_in_the_past", [windowLabel]);
  return (
    <div className="account-activity account-activity-cell">
      <div className="active">
        <span>
 {t("admin.active")} {activeUsers !== null && activeUsers > 0 && activeEmails.length ? (
            <span
              className="account-active-users"
              tabIndex={0}
              aria-label={t("admin.active_users_in", [windowLabel, activeEmails.join("，")])}
            >
              <strong>{activeValue}</strong>
              <span className="account-active-users-tooltip" role="tooltip">
                <b>{windowLabel}{t("admin.active_users")}{formatNumber(activeUsers)}）</b>
                {activeEmails.map((email) => <span className="account-active-user-email" key={email}>{email}</span>)}
              </span>
            </span>
          ) : <strong>{activeValue}</strong>}
          <Tooltip
            title={activeHelp}
            trigger={["hover", "focus"]}
            placement="top"
            autoAdjustOverflow
            mouseEnterDelay={0.1}
            getPopupContainer={() => document.body}
            rootClassName="account-runtime-status-tooltip"
          >
            <button className="account-activity-help" type="button" aria-label={activeHelp}>?</button>
          </Tooltip>
        </span>
        <small className={activeUsers === null ? "warning" : ""}>{activeDetail}</small>
      </div>
      <div>
        <span>{t("admin.routes")} <strong>{formatNumber(account.routed_users)}</strong></span>
        <small>{formatNumber(account.associated_users)} {t("admin.associated")}</small>
      </div>
      <div>
        <span>{t("common.requests")} <strong>{account.usage_available ? formatNumber(account.usage.request_count) : "—"}</strong></span>
        <small className={!account.usage_available || account.usage.failed_count ? "warning" : ""}>{account.usage_available ? (account.usage.failed_count ? t("admin.failed", [formatNumber(account.usage.failed_count)]) : t("admin.all_successful")) : t("admin.quota_period_unavailable")}</small>
      </div>
    </div>
  );
}

function accountOAuthConfigured(account: Account) {
  return account.oauth_configured;
}

function AccountTokenUsage({ account }: { account: Account }) {
  if (!account.usage_available) return <span className="account-token-unavailable">—</span>;
  const [value, unit = ""] = formatTokenAmount(account.usage.total_tokens).split(" ");
  return (
    <span className="token-usage account-token-value" title={`${formatNumber(account.usage.total_tokens)} Token`}>
      <span className="token-usage-main"><strong className="token-usage-value">{value}</strong>{unit ? <em className="token-usage-unit">{unit}</em> : null}</span>
      <small className="token-usage-exact">{formatNumber(account.usage.total_tokens)} Token</small>
    </span>
  );
}

const accountTableCollator = new Intl.Collator(getIntlLocale(), { numeric: true, sensitivity: "base" });

function compareAccountColumn(
  left: Account,
  right: Account,
  order: "ascend" | "descend" | null | undefined,
  value: (account: Account) => string | number | null | undefined
) {
  const leftValue = value(left);
  const rightValue = value(right);
  const leftMissing = leftValue === null || leftValue === undefined || leftValue === "";
  const rightMissing = rightValue === null || rightValue === undefined || rightValue === "";
  if (leftMissing !== rightMissing) return leftMissing ? 1 : -1;
  if (!leftMissing) {
    const result = typeof leftValue === "string" || typeof rightValue === "string"
      ? accountTableCollator.compare(String(leftValue), String(rightValue))
      : Number(leftValue) - Number(rightValue);
    if (result) return order === "descend" ? -result : result;
  }
  return accountTableCollator.compare(left.id, right.id);
}

function compareAccountsForSort(left: Account, right: Account, sort: AccountSortState) {
  const order = sort.direction === "asc" ? "ascend" : "descend";
  switch (sort.field) {
    case "account":
      return compareAccountColumn(left, right, order, (account) => account.id);
    case "runtime":
      return compareAccountColumn(left, right, order, accountStatusLabel);
    case "auth":
      return compareAccountColumn(left, right, order, (account) => (
        account.oauth_configured === true ? t("admin.authorized") : account.oauth_configured === false ? t("admin.authorization_required") : t("common.unknown")
      ));
    case "quota":
      return compareAccountColumn(left, right, order, (account) => (
        account.state_available ? account.account_state.used_percent : null
      ));
    case "activity":
      return compareAccountColumn(left, right, order, (account) => account.usage_available ? account.usage.request_count : null);
    case "tokens":
      return compareAccountColumn(left, right, order, (account) => account.usage_available ? account.usage.total_tokens : null);
    case "last_used":
      return compareAccountColumn(left, right, order, (account) => account.usage_available ? account.usage.last_used_at : null);
  }
}

function accountStatusLabel(account: Account) {
  if (account.operational_status?.label) return (account.operational_status.label);
  if (!account.enabled) return t("common.disabled");
  if (account.runtime_state === "stopped") return t("common.stopped");
  if (!account.state_available) return t("common.unknown_status");
  if (account.account_state.exhausted) return t("common.quota_exhausted");
  if (runtimeStateReasons.has(account.account_state.reason)) return stateLabels[account.account_state.reason];
  if (account.account_state.eligible) return t("common.available");
  return stateLabels[account.account_state.reason] ?? t("admin.cannot_receive_users");
}

function accountResetCreditLabel(account: Account) {
  return typeof account.reset_credit_count === "number"
    ? t("admin.remaining_2", [account.reset_credit_count])
    : t("common.unknown_quota");
}

function formatPercent(value: number) {
  return `${Number(value.toFixed(value >= 10 ? 0 : 1))}%`;
}

function formatTaskDuration(startedAt?: number | null, finishedAt?: number | null, active = false) {
  if (active) return t("admin.running");
  if (!startedAt || !finishedAt) return "—";
  const seconds = Math.max(0, finishedAt - startedAt);
  if (seconds < 1) return t("admin.1_sec");
  if (seconds < 60) return t("admin.sec", [seconds]);
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return remainder ? t("admin.min_sec", [minutes, remainder]) : t("admin.min", [minutes]);
}

function formatAccountLastUsed(timestamp: number) {
  return timestamp ? formatSiteTimestamp(timestamp) : t("admin.never_used");
}

function accountRefreshLabel(catalog: AccountCatalog, nowSeconds = catalog.generated_at) {
  const observedAt = Math.max(0, ...catalog.accounts.map((account) => account.account_state.observed_at || 0));
  const generatedAt = catalog.quota_generated_at || observedAt;
  if (!Number.isFinite(generatedAt) || generatedAt <= 0) {
    return catalog.quota_refreshing ? t("admin.quotas_are_updating_in_the_background") : t("admin.quota_data_unavailable");
  }
  const cacheSeconds = Number.isFinite(catalog.quota_cache_ttl_seconds) && catalog.quota_cache_ttl_seconds > 0
    ? catalog.quota_cache_ttl_seconds
    : 60;
  // Allow one extra cache period for the scheduled collection round to finish.
  const stale = nowSeconds - generatedAt > cacheSeconds * 2;
  const state = catalog.quota_refreshing
    ? t("admin.updating_in_background")
    : stale
      ? t("admin.outdated_please_refresh")
      : "";
  return t("admin.quotas_updated", [formatSiteTimestamp(generatedAt), state]);
}

function formatNumber(value: number) {
  return new Intl.NumberFormat(getIntlLocale()).format(value);
}

function isInteractiveRowTarget(target: EventTarget | null) {
  return target instanceof Element && Boolean(target.closest(
    "button, a, input, select, textarea, [role='button'], [role='combobox'], [contenteditable='true']"
  ));
}

function parseOAuthDeviceOutput(output: string) {
  const rawURL = output.match(/^Codex device URL:\s*(\S+)\s*$/im)?.[1] ?? "";
  let url = "";
  if (rawURL) {
    try {
      const parsed = new URL(rawURL);
      if (parsed.protocol === "https:" && !parsed.username && !parsed.password) url = parsed.toString();
    } catch {
      url = "";
    }
  }
  const rawCode = output.match(/^Codex device code:\s*([^\r\n]+?)\s*$/im)?.[1]?.trim() ?? "";
  const code = rawCode && rawCode.length <= 128 && !/[\u0000-\u001f\u007f]/.test(rawCode) ? rawCode : "";
  return { url, code };
}

const runtimeFilterOptions = [
  { value: "all", label: t("admin.all") },
  { value: "running", label: t("admin.running_2") },
  { value: "stopped", label: t("common.stopped") },
  { value: "disabled", label: t("common.disabled") }
] satisfies Array<{ value: AccountRuntimeFilter; label: string }>;

const authFilterOptions = [
  { value: "all", label: t("admin.all") },
  { value: "configured", label: t("admin.authorized") },
  { value: "pending", label: t("admin.authorization_required") }
] satisfies Array<{ value: AccountAuthFilter; label: string }>;

const usageWindowOptions = [
  { value: "3600", label: t("common.1h") },
  { value: "today", label: t("common.today") },
  { value: "86400", label: t("common.24h") },
  { value: "604800", label: t("common.7d") },
  { value: "2592000", label: t("common.30d") },
  { value: "since_reset", label: t("admin.quota_cycle") },
  { value: "all", label: t("admin.all") }
] satisfies Array<{ value: AccountUsageWindow; label: string }>;

function formatActiveUserWindow(seconds: number): string {
  const minutes = Math.max(1, Math.round(seconds / 60));
  if (minutes % 60 === 0) return t("common.last_hours", [minutes / 60]);
  return t("common.last_minutes", [minutes]);
}

const runtimeStateLabel: Record<Account["runtime_state"], string> = {
  running: t("admin.running_2"),
  stopped: t("common.stopped"),
  disabled: t("common.disabled"),
  unknown: t("common.unknown")
};

const runtimeJobStatusLabels: Record<string, string> = {
  queued: t("admin.queued"),
  running: t("admin.running_2"),
  cancelling: t("admin.cancelling"),
  succeeded: t("common.succeeded"),
  failed: t("common.failed"),
  cancelled: t("common.cancelled")
};

const proxyModeSchema = z.enum(["inherit", "custom", "direct"]);
const accountEditorSchema = z.object({
  id: z.string().trim().regex(/^[a-z][a-z0-9-]{1,31}$/, t("admin.enter_2_32_lowercase_letters_digits_or_hyphens_starting_with")),
  email: z.string().trim().email(t("admin.enter_a_valid_email_address")),
  proxy_mode: proxyModeSchema,
  proxy_url: z.string().trim().refine((value) => !value || validProxyURL(value), t("admin.only_http_https_or_socks5_proxy_urls_without_a_path"))
});

type AccountEditorValues = z.infer<typeof accountEditorSchema>;

function AccountEditorModal({
  open,
  account,
  pending,
  error,
  onCancel,
  onSubmit,
  onDestructiveAction
}: {
  open: boolean;
  account: Account | null;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (command: AccountLifecycleCommand) => void;
  onDestructiveAction: (action: DestructiveAction) => void;
}) {
  const form = useForm<AccountEditorValues>({ resolver: zodResolver(accountEditorSchema), defaultValues: emptyAccountEditorValues() });
  useEffect(() => {
    if (open) form.reset(account ? accountEditorValues(account) : emptyAccountEditorValues());
  }, [account, form, open]);
  const proxyMode = form.watch("proxy_mode");
  const accountID = form.watch("id").trim().toLowerCase();
  const renamed = account !== null && accountID !== account.id;

  const submit = form.handleSubmit((values) => {
    const proxyURL = values.proxy_url.trim();
    const existingProxyCanBeRetained = account?.proxy_configured;
    if (values.proxy_mode === "custom" && !proxyURL && !existingProxyCanBeRetained) {
      form.setError("proxy_url", { message: t("admin.enter_a_proxy_url_for_the_custom_proxy_mode") });
      return;
    }
    if (!account) {
      onSubmit({
        kind: "create",
        request: {
          id: values.id.trim().toLowerCase(),
          email: values.email,
          proxy_mode: values.proxy_mode,
          proxy_url: proxyURL
        }
      });
      return;
    }
    const request: AccountUpdateRequestWritable = {
      id: account.id,
      new_id: values.id.trim().toLowerCase(),
      email: values.email.trim().toLowerCase(),
      proxy_mode: values.proxy_mode,
      proxy_url: proxyURL,
      confirm: renamed ? account.id : ""
    };
    onSubmit({ kind: "update", request });
  });

  return (
    <Modal
      className={`legacy-account-editor-modal ${account ? "account-edit-modal" : "account-create-modal"}`}
      title={<LegacyDialogTitle title={account ? account.id : t("common.add_cpa_account")} kicker={account ? "BUSINESS CPA" : "NEW BUSINESS CPA"} />}
      open={open}
      width={account ? 720 : 560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={account ? t("admin.save_changes") : pending ? t("admin.creating") : t("admin.create_start")}
      cancelText={t("common.cancel")}
      cancelButtonProps={{ className: "legacy-modal-ghost", tabIndex: -1 }}
      okButtonProps={{ disabled: pending, htmlType: "submit", form: "account-editor-form" }}
      onCancel={onCancel}
      afterOpenChange={(opened) => {
        if (!opened) return;
        const active = document.activeElement;
        const userIsEditing = active instanceof HTMLInputElement ||
          active instanceof HTMLSelectElement ||
          active instanceof HTMLTextAreaElement ||
          (active instanceof HTMLElement && active.isContentEditable);
        if (!userIsEditing) form.setFocus("id");
      }}
      destroyOnHidden
    >
      <Form id="account-editor-form" className="account-editor-form" layout="vertical" requiredMark={false} onFinish={() => void submit()}>
        {account ? (
          <div className="account-editor-facts">
            <AccountDetailFact label={t("admin.port")} value={`:${account.port}`} />
            <AccountDetailFact
              label={t("admin.egress")}
              value={account.proxy_source === "account" ? t("admin.account_proxy") : account.proxy_source === "default" ? t("admin.default_proxy") : t("admin.direct")}
            />
            <AccountDetailFact label={t("admin.container")} value={runtimeStateLabel[account.runtime_state]} />
            <AccountDetailFact label="OAuth" value={account.oauth_configured ? t("admin.authorized") : t("admin.authorization_required")} />
            <AccountDetailFact label={t("admin.current_users")} value={account.routed_users} />
          </div>
        ) : null}
        <Row gutter={16}>
          <Col xs={24}><EditorInput control={form.control} name="id" label={account ? t("admin.cpa_id") : t("admin.account_id")} minLength={account ? undefined : 2} placeholder={account ? t("admin.e_g_account_a") : t("admin.e_g_codex_team_2")} /></Col>
          {!account ? <Col xs={24}><Paragraph className="account-field-help">{t("admin.used_for_container_names_configuration_files_and_key_routing_only")}</Paragraph></Col> : null}
          <Col xs={24}><EditorInput control={form.control} name="email" label={account ? t("admin.display_email") : t("admin.upstream_account_email")} placeholder="account@example.com" /></Col>
          <Col xs={24}>
            <Controller
              control={form.control}
              name="proxy_mode"
              render={({ field, fieldState }) => (
                <Form.Item label={t("admin.outbound_proxy")} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
                  <LegacyEnhancedSelect
                    label={t("admin.outbound_proxy")}
                    value={field.value}
                    options={[
                      { value: "inherit", label: t("admin.inherit_the_control_plane_default_proxy") },
                      { value: "custom", label: t("admin.use_a_custom_account_proxy") },
                      { value: "direct", label: t("admin.direct_connection") }
                    ]}
                    onChange={field.onChange}
                  />
                </Form.Item>
              )}
            />
          </Col>
          {proxyMode === "custom" ? (
            <Col xs={24}>
              <Controller
                control={form.control}
                name="proxy_url"
                render={({ field, fieldState }) => (
                  <Form.Item
                    label={t("admin.account_proxy_url")}
                    validateStatus={fieldState.error ? "error" : undefined}
                    help={fieldState.error?.message}
                  >
                    <Input.Password
                      {...field}
                      aria-label={t("admin.account_proxy_url")}
                      autoComplete="new-password"
                      visibilityToggle={{ tabIndex: -1 }}
                      placeholder={account ? t("admin.leave_blank_to_keep_the_current_proxy_supports_http_https") : t("admin.e_g_socks5_user_pass_host_1080")}
                    />
                  </Form.Item>
                )}
              />
            </Col>
          ) : null}
        </Row>
        {!account ? (
          <>
            <Paragraph className="account-field-help">{t("admin.account_settings_override_the_control_plane_default_proxy_http_https")}</Paragraph>
            <div className="account-provision-list" aria-label={t("admin.automatic_steps")}>
              <span>{t("admin.resolve_outbound_proxy")}</span>
              <span>{t("admin.generate_cpa_configuration")}</span>
              <span>{t("admin.create_authentication_directory")}</span>
              <span>{t("admin.link_existing_user_keys_in_the_background")}</span>
              <span>{t("admin.start_container_refresh_routes")}</span>
            </div>
            <div className="account-inline-notice">{t("admin.after_creation_all_existing_users_unified_keys_are_linked_in")}</div>
          </>
        ) : null}
        {account ? (
          <>
            <Paragraph className="account-field-help">
 {t("admin.currently_effective")}{account.proxy_display || t("admin.direct")}{t("admin.account_proxy_settings_take_priority_only_this_cpa_is_recreated")} </Paragraph>
            <Paragraph className="account-editor-explanation">
 {t("admin.changing_the_cpa_id_migrates_its_container_routes_oauth_logs")} </Paragraph>
            <LegacyFormError error={error} />
            <section className="account-danger-zone" aria-label={t("admin.danger_zone")}>
              <div>
                <strong>{t("admin.danger_zone")}</strong>
                <p>{t("admin.a_local_safety_archive_is_created_before_clearing_authorization_or")}</p>
              </div>
              <Space wrap size={8}>
                <Button danger onClick={() => onDestructiveAction({ kind: "clear-auth", account })}>{t("admin.clear_oauth")}</Button>
                <Button danger onClick={() => onDestructiveAction({ kind: "delete", account })}>{t("admin.delete_cpa")}</Button>
              </Space>
            </section>
          </>
        ) : null}
        {!account ? <LegacyFormError error={error} /> : null}
      </Form>
    </Modal>
  );
}

function AccountPolicyModal({
  account,
  accounts,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  account: Account | null;
  accounts: Account[];
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (request: AccountUpdateRequestWritable) => void;
}) {
  const [fallback, setFallback] = useState("");
  const options = useMemo(() => accounts
    .filter((candidate) => candidate.enabled && candidate.id !== account?.id)
    .map((candidate) => ({ value: candidate.id, label: candidate.id })), [account?.id, accounts]);
  useEffect(() => {
    setFallback((current) => options.some((option) => option.value === current) ? current : options[0]?.value ?? "");
  }, [account?.id, options]);
  if (!account) return null;
  const enabling = !account.enabled;
  const requiresFallback = !enabling && account.routed_users > 0;
  return (
    <Modal
      className="legacy-account-editor-modal account-policy-modal"
      title={<LegacyDialogTitle title={`${enabling ? t("admin.enable") : t("admin.disable")} ${account.id}`} kicker="ROUTING AVAILABILITY" />}
      open
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={pending ? (enabling ? t("admin.enabling") : t("admin.disabling")) : enabling ? t("admin.confirm_enable") : t("admin.confirm_disable")}
      confirmLoading={pending}
      closable={!pending}
      mask={{ closable: !pending }}
      keyboard={!pending}
      cancelText={t("common.cancel")}
      okType={enabling ? "primary" : "default"}
      cancelButtonProps={{ className: "legacy-modal-ghost", disabled: pending }}
      okButtonProps={{
        className: enabling ? undefined : "legacy-modal-danger-outline",
        danger: !enabling,
        disabled: pending || (requiresFallback && !fallback)
      }}
      onCancel={onCancel}
      onOk={() => onSubmit({
        id: account.id,
        group_enabled: enabling,
        default_group: false,
        // The legacy form keeps the first eligible fallback selected even
        // while the field is hidden for an enable action, and includes that
        // value in the request contract.
        fallback_account: fallback || null
      })}
      destroyOnHidden
    >
      <div className="account-policy-form">
        <div className="warning-banner">
          {enabling
            ? t("admin.once_enabled_and_available_users_can_select_existing_user_routes", [account.id])
            : account.routed_users > 0
              ? t("admin.once_disabled_users_cannot_select_this_cpa_its_current_users", [formatNumber(account.routed_users)])
              : t("admin.once_disabled_users_cannot_select_this_cpa_no_users_are")}
        </div>
        {requiresFallback ? (
          <label className="field">
            <span>{t("admin.move_existing_users_to")}</span>
            <LegacyEnhancedSelect label={t("admin.move_existing_users_to")} value={fallback} options={options} onChange={setFallback} disabled={pending} />
          </label>
        ) : null}
        {pending ? <div role="status">{t("admin.updating_account_availability_please_wait")}</div> : null}
        <LegacyFormError error={error} />
      </div>
    </Modal>
  );
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
        <Button
          key="confirm"
          type={danger ? "default" : "primary"}
          danger={danger}
          loading={confirmLoading}
          disabled={okDisabled}
          onClick={onOk}
        >{okText}</Button>
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

function OAuthFlowModals({
  account,
  starting,
  startError,
  onStart,
  onClose
}: {
  account: Account | null;
  starting: boolean;
  startError: unknown;
  onStart: () => void;
  onClose: () => void;
}) {
  if (!account) return null;
  return (
    <LegacyConfirmModal
      title={t("admin.start_oauth_authorization")}
      open
      okText={t("admin.start_authorization")}
      confirmLoading={starting}
      onCancel={onClose}
      onOk={onStart}
    >
      <Paragraph>{t("admin.task_output_will_show_the_device_authorization_url_and_one")}</Paragraph>
      {startError ? <MutationError error={startError} title={t("admin.oauth_task_was_not_submitted")} /> : null}
    </LegacyConfirmModal>
  );
}

function TaskOutputModal({
  job,
  accountEmail,
  pollError,
  cancelling,
  onCancelJob,
  onClose
}: {
  job: LegacyRuntimeJobView | null;
  accountEmail: string;
  pollError: unknown;
  cancelling: boolean;
  onCancelJob: () => void;
  onClose: () => void;
}) {
  const [copyNotice, setCopyNotice] = useState("");
  const [deviceCopyState, setDeviceCopyState] = useState<Record<"url" | "code", "idle" | "copied" | "failed">>({
    url: "idle",
    code: "idle"
  });
  const copyResetTimers = useRef<Partial<Record<"url" | "code", number>>>({});
  const currentJobID = useRef(job?.id ?? "");
  currentJobID.current = job?.id ?? "";
  useEffect(() => {
    setCopyNotice("");
    setDeviceCopyState({ url: "idle", code: "idle" });
    return () => {
      Object.values(copyResetTimers.current).forEach((timer) => window.clearTimeout(timer));
      copyResetTimers.current = {};
    };
  }, [job?.id]);
  if (!job) return null;
  const rawOutput = job.output?.trim() ?? "";
  const output = rawOutput || t("admin.task_is_queued");
  const device = parseOAuthDeviceOutput(output);
  const imageUpdateReport = job.action === "image-update"
    ? parseImageUpdateOutput(rawOutput, job.status)
    : null;
  const active = isActiveRuntimeJob(job);
  const startedAt = job.started_at || job.created_at;
  const outputLineCount = rawOutput ? rawOutput.split(/\r?\n/).filter(Boolean).length : 0;
  const copy = async (value: string, label: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopyNotice(t("common.copied", [label]));
    } catch {
      setCopyNotice(t("admin.unable_to_copy_please_copy_it_manually", [label]));
    }
  };
  const copyDeviceValue = async (target: "url" | "code", value: string) => {
    const sourceJobID = job.id;
    setCopyNotice("");
    const existingTimer = copyResetTimers.current[target];
    if (existingTimer) window.clearTimeout(existingTimer);
    try {
      await navigator.clipboard.writeText(value);
      if (currentJobID.current !== sourceJobID) return;
      setDeviceCopyState((current) => ({ ...current, [target]: "copied" }));
    } catch {
      if (currentJobID.current !== sourceJobID) return;
      setDeviceCopyState((current) => ({ ...current, [target]: "failed" }));
    }
    copyResetTimers.current[target] = window.setTimeout(() => {
      if (currentJobID.current === sourceJobID) {
        setDeviceCopyState((current) => ({ ...current, [target]: "idle" }));
      }
      delete copyResetTimers.current[target];
    }, 1_600);
  };
  const deviceCopyButton = (target: "url" | "code", idleLabel: string) => {
    const state = deviceCopyState[target];
    return {
      label: state === "copied" ? t("common.copied_2") : state === "failed" ? t("common.copy_failed") : idleLabel,
      icon: state === "copied"
        ? <CheckOutlined aria-hidden="true" />
        : <CopyOutlined aria-hidden="true" />
    };
  };
  const addressCopyButton = deviceCopyButton("url", t("admin.copy_url"));
  const codeCopyButton = deviceCopyButton("code", t("admin.copy_device_code"));
  return (
    <Modal
      className="legacy-output-modal"
      title={<LegacyDialogTitle title={(job.name) || t("admin.task_output")} kicker="TASK OUTPUT" />}
      open
      width={900}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      footer={[
        <Button className="legacy-output-secondary" key="copy-output" onClick={() => void copy(output, t("admin.full_output"))}>{t("admin.copy_full_output")}</Button>,
        active ? (
          <Button key="cancel-job" danger loading={cancelling} onClick={onCancelJob}>{t("admin.cancel_task")}</Button>
        ) : null,
        <Button className="legacy-output-ghost" key="close" onClick={onClose}>{t("common.close")}</Button>
      ]}
    >
      <div className="oauth-task-meta task-output-meta" aria-label={t("admin.task_summary")}>
        <div>
          <span>{t("admin.scope")}</span>
          <strong className="oauth-task-account">
            <span>{job.target === "all" ? t("common.all_cpas") : job.target}</span>
            {accountEmail ? <span className="oauth-task-email">{accountEmail}</span> : null}
          </strong>
        </div>
        <div>
          <span>{t("admin.status")}</span>
          <Tag color={job.status === "succeeded" ? "success" : job.status === "failed" ? "error" : active ? "processing" : "default"}>
            {runtimeJobStatusLabels[job.status] ?? job.status}
          </Tag>
        </div>
        <div>
          <span>{t("common.started")}</span>
          <time>{formatSiteTimestamp(startedAt)}</time>
        </div>
        <div>
          <span>{t("admin.finished")}</span>
          <time>{job.finished_at ? formatSiteTimestamp(job.finished_at) : active ? t("admin.running") : "—"}</time>
        </div>
        <div>
          <span>{t("admin.duration")}</span>
          <strong>{formatTaskDuration(startedAt, job.finished_at, active)}</strong>
        </div>
        <div>
          <span>{t("admin.output_lines")}</span>
          <strong>{outputLineCount} {t("common.lines")}</strong>
        </div>
      </div>
      {device.url || device.code ? (
        <section className="oauth-copy-panel" aria-label={t("admin.oauth_device_authorization")}>
          <div className="oauth-copy-grid">
            <div>
              <span>{t("admin.authorization_url")}</span>
              <code>{device.url || "—"}</code>
              <Button
                disabled={!device.url}
                danger={deviceCopyState.url === "failed"}
                icon={addressCopyButton.icon}
                onClick={() => void copyDeviceValue("url", device.url)}
              >{addressCopyButton.label}</Button>
            </div>
            <div>
              <span>{t("admin.device_code")}</span>
              <code className="device-code">{device.code || "—"}</code>
              <Button
                disabled={!device.code}
                danger={deviceCopyState.code === "failed"}
                icon={codeCopyButton.icon}
                onClick={() => void copyDeviceValue("code", device.code)}
              >{codeCopyButton.label}</Button>
            </div>
          </div>
        </section>
      ) : null}
      {copyNotice ? <Alert className="page-alert" type="info" showIcon title={copyNotice} /> : null}
      {pollError ? <MutationError error={pollError} title={t("admin.unable_to_refresh_task_status_retrying")} /> : null}
      {job.error ? <Alert className="page-alert" type="error" showIcon title={t("admin.task_failed")} description={job.error} /> : null}
      {imageUpdateReport ? (
        <ImageUpdateTaskReport output={rawOutput} status={job.status} />
      ) : (
        <pre className="oauth-task-output">{output}</pre>
      )}
    </Modal>
  );
}

function RuntimeLogsModal({
  target,
  query,
  onClose
}: {
  target: string | null;
  query: UseQueryResult<RuntimeLogs>;
  onClose: () => void;
}) {
  const [copyNotice, setCopyNotice] = useState("");
  useEffect(() => setCopyNotice(""), [target]);
  if (!target) return null;
  const output = query.isPending
    ? t("common.loading_2")
    : query.isError
      ? (query.error instanceof Error ? query.error.message : t("admin.unable_to_read_logs"))
      : query.data?.output || t("admin.no_logs");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(output);
      setCopyNotice(t("admin.full_output_copied"));
    } catch {
      setCopyNotice(t("admin.unable_to_copy_full_output_please_copy_it_manually"));
    }
  };
  return (
    <Modal
      className="legacy-output-modal"
      title={<LegacyDialogTitle title={t("admin.logs", [target])} kicker="SERVICE LOGS" />}
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
        <Button className="legacy-output-ghost" key="close" onClick={onClose}>{t("common.close")}</Button>
      ]}
    >
      <div className="oauth-task-meta"><span>{t("admin.last_200_lines")}</span><span>{target}</span></div>
      {copyNotice ? <Alert className="page-alert" type="info" showIcon title={copyNotice} /> : null}
      {query.data?.truncated ? <Alert className="page-alert" type="warning" showIcon title={t("admin.output_truncated_at_2_mib")} /> : null}
      <pre className="runtime-log-output">{output}</pre>
    </Modal>
  );
}

function QuotaResetModal({
  account,
  query,
  pending,
  error,
  onClose,
  onSubmit
}: {
  account: Account | null;
  query: UseQueryResult<ResetAccountQuotaInspection>;
  pending: boolean;
  error: unknown;
  onClose: () => void;
  onSubmit: (creditID: string) => void;
}) {
  const [creditID, setCreditID] = useState("");
  useEffect(() => {
    setCreditID(query.data?.credits[0]?.id ?? "");
  }, [account?.id, query.data]);
  if (!account) return null;
  const details = query.data;
  const available = details?.available_count;
  const creditSummary = details?.details_truncated
    ? t("admin.the_upstream_reports_available_resets_selectable_entries_are_currently_provided", [available ?? "—", details.credits.length])
    : t("admin.resets_available_this_operation_uses_one", [available ?? details?.credits.length ?? "—"]);
  return (
    <Modal
      className="legacy-account-editor-modal account-quota-reset-modal"
      title={<LegacyDialogTitle title={t("admin.reset_weekly_limit", [account.id])} kicker="WEEKLY QUOTA RESET" />}
      open
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={pending ? t("admin.resetting") : t("admin.confirm_reset")}
      okType="default"
      cancelText={t("common.cancel")}
      cancelButtonProps={{ className: "legacy-modal-ghost" }}
      okButtonProps={{
        className: "legacy-modal-danger-outline",
        danger: true,
        disabled: pending || query.isPending || query.isError || !details?.windows.length || !creditID
      }}
      onCancel={onClose}
      onOk={() => onSubmit(creditID)}
      destroyOnHidden
    >
      <div className="account-quota-reset-form">
        <div className="warning-banner">{t("admin.this_uses_one_reset_credit_and_immediately_refreshes_the_exhausted")}</div>
        {query.isPending ? <Skeleton active paragraph={{ rows: 4 }} /> : null}
        {query.isError ? <LegacyFormError error={query.error} /> : null}
        {details ? (
          <>
            <p className="quota-reset-credit-summary">{creditSummary}</p>
            <p className="quota-reset-targets">{t("admin.will_refresh")}{details.windows.map((window) => (window.label) || t("admin.weekly_limit")).join("、") || t("admin.no_weekly_limit_is_eligible_for_reset")}</p>
            <label className="field">
              <span>{t("admin.select_a_reset_credit")}</span>
              <LegacyEnhancedSelect
                label={t("admin.select_a_reset_credit")}
                value={creditID}
                options={details.credits.map((credit, index) => ({
                  value: credit.id,
                  label: `${credit.title || "Full reset"}${details.credits.length > 1 ? ` #${index + 1}` : ""} · ${credit.expires_at ? t("admin.expires", [formatSiteTimestamp(credit.expires_at)]) : t("admin.no_expiry")}`
                }))}
                autoFocus
                required
                onChange={setCreditID}
              />
            </label>
            <p className="field-help">{t("admin.the_date_is_this_full_reset_credit_s_expiry_quota")}</p>
          </>
        ) : null}
        <LegacyFormError error={error} />
      </div>
    </Modal>
  );
}

function LegacyDialogTitle({ title, kicker }: { title: string; kicker: string }) {
  return (
    <div className="legacy-dialog-title">
      <strong>{title}</strong>
      <span>{kicker}</span>
    </div>
  );
}

const destructiveSchema = z.object({
  confirm: z.string().trim().min(1, t("admin.enter_the_cpa_id_to_confirm")),
  fallback_account: z.string()
});
type DestructiveValues = z.infer<typeof destructiveSchema>;

function AccountDestructiveModal({
  action,
  accounts,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  action: DestructiveAction | null;
  accounts: Account[];
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (command: AccountLifecycleCommand) => void;
}) {
  const form = useForm<DestructiveValues>({
    resolver: zodResolver(destructiveSchema),
    defaultValues: { confirm: "", fallback_account: "" }
  });
  const actionAccountID = action?.account.id ?? "";
  const fallbackOptions = useMemo(() => accounts
    .filter((candidate) => candidate.enabled && candidate.id !== actionAccountID)
    .map((candidate) => ({ value: candidate.id, label: candidate.id })), [accounts, actionAccountID]);
  useEffect(() => {
    if (action) {
      form.reset({
        confirm: "",
        fallback_account: action.kind === "delete" ? fallbackOptions[0]?.value ?? "" : ""
      });
    }
  }, [action, fallbackOptions, form]);
  if (!action) return null;

  const account = action.account;
  const deleting = action.kind === "delete";
  if (!deleting) {
    return (
      <LegacyConfirmModal
        title={t("admin.clear_oauth_authorization")}
        open
        okText={t("admin.clear_authorization")}
        danger
        confirmLoading={pending}
        onCancel={onCancel}
        onOk={() => onSubmit({ kind: "clear-auth", request: { id: account.id, confirm: account.id } })}
      >
        <Paragraph>
          {account.id} {t("admin.s_local_authorization_files_will_be_archived_then_removed_and")} </Paragraph>
        {error ? <MutationError error={error} title={t("admin.oauth_cleanup_was_not_performed")} /> : null}
      </LegacyConfirmModal>
    );
  }
  const submit = form.handleSubmit((values) => {
    onSubmit({
      kind: "delete",
      request: {
        id: account.id,
        confirm: values.confirm.trim(),
        revoke_keys: false,
        fallback_account: values.fallback_account || null
      }
    });
  });

  return (
    <Modal
      className="legacy-account-editor-modal account-delete-modal"
      title={<LegacyDialogTitle title={t("admin.delete_cpa_account")} kicker="DESTRUCTIVE ACTION" />}
      open
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={t("admin.confirm_deletion")}
      okType="default"
      cancelText={t("common.cancel")}
      cancelButtonProps={{ className: "legacy-modal-ghost" }}
      okButtonProps={{
        className: "legacy-modal-danger-outline",
        danger: true,
        disabled: pending || (deleting && accounts.length <= 1),
        htmlType: "submit",
        form: "account-delete-form"
      }}
      onCancel={onCancel}
      afterOpenChange={(opened) => {
        if (opened) form.setFocus("confirm");
      }}
      destroyOnHidden
    >
      <form id="account-delete-form" className="account-delete-form" onSubmit={(event) => void submit(event)}>
        <div className="warning-banner">
 {t("admin.the_container_routes_authorization_directory_and_user_associations_will_be")} </div>
        {deleting && accounts.length <= 1 ? <div className="warning-banner">{t("admin.the_last_account_cannot_be_deleted")}</div> : null}
        {deleting && accounts.length > 1 ? (
          <Controller
            control={form.control}
            name="fallback_account"
            render={({ field }) => (
              <label className="field">
                <span>{t("admin.move_users_to")}</span>
                <LegacyEnhancedSelect label={t("admin.move_users_to")} value={field.value} options={fallbackOptions} onChange={field.onChange} />
              </label>
            )}
          />
        ) : null}
        <Controller
          control={form.control}
          name="confirm"
          render={({ field }) => (
            <label className="field">
              <span>{t("admin.enter_cpa_id_to_confirm")}</span>
              <input {...field} aria-label={t("admin.enter_cpa_id_to_confirm")} autoComplete="off" autoFocus placeholder={account.id} required />
            </label>
          )}
        />
        <LegacyFormError error={error} />
      </form>
    </Modal>
  );
}

function EditorInput({
  control,
  name,
  label,
  minLength,
  placeholder
}: {
  control: ReturnType<typeof useForm<AccountEditorValues>>["control"];
  name: "id" | "email";
  label: string;
  minLength?: number;
  placeholder?: string;
}) {
  return (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <Form.Item label={label} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
          <Input
            {...field}
            aria-label={label}
            type={name === "email" ? "email" : "text"}
            required
            minLength={name === "id" ? minLength : undefined}
            maxLength={name === "id" ? 32 : undefined}
            pattern={name === "id" ? "[a-z][a-z0-9\\-]{1,31}" : undefined}
            placeholder={placeholder}
          />
        </Form.Item>
      )}
    />
  );
}

function MutationError({ error, title }: { error: unknown; title: string }) {
  return (
    <Alert
      type="error"
      showIcon
      title={title}
      description={errorMessage(error)}
    />
  );
}

function LegacyFormError({ error }: { error: unknown }) {
  return <p className="legacy-form-error" role="alert">{error ? errorMessage(error) : ""}</p>;
}

function errorMessage(error: unknown, fallback = t("admin.request_failed_please_try_again_later")) {
  return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

function AccountStatus({ account }: { account: Account }) {
  let label: string;
  let tone: string;
  let detail: string;
  if (account.operational_status) {
    const status = account.operational_status;
    label = (status.label);
    tone = status.tone;
    detail = accountRuntimeDetail(account);
  } else {
    label = t("admin.cannot_receive_users");
    tone = "neutral";
    detail = stateLabels[account.account_state.reason] ?? t("admin.this_cpa_cannot_currently_receive_users");
    if (!account.enabled) {
      label = t("common.disabled");
      detail = t("admin.this_account_is_disabled_and_no_longer_accepts_new_users");
    } else if (account.runtime_state === "stopped") {
      label = t("common.stopped");
      tone = "danger";
      detail = t("admin.cpa_container_is_not_running");
    } else if (!account.state_available) {
      label = t("common.unknown_status");
      detail = t("admin.cpa_runtime_or_quota_status_is_unavailable");
    } else if (account.account_state.exhausted) {
      label = t("common.quota_exhausted");
      tone = "danger";
      detail = t("admin.official_weekly_quota_is_exhausted");
    } else if (runtimeStateReasons.has(account.account_state.reason)) {
      label = stateLabels[account.account_state.reason];
      tone = account.account_state.reason === "credential_unavailable" ? "danger" : "warning";
    } else if (account.account_state.eligible) {
      label = t("common.available");
      tone = "success";
      detail = t("admin.cpa_native_credentials_are_healthy");
    } else {
      label = stateLabels[account.account_state.reason] ?? t("admin.cannot_receive_users");
      tone = account.account_state.reason === "quota_stale" ? "warning" : "neutral";
    }
  }
  return (
    <Tooltip
      title={detail}
      trigger={["hover", "focus"]}
      placement="top"
      mouseEnterDelay={0.1}
      rootClassName="account-runtime-status-tooltip"
    >
      <span
        className={`status-chip ${tone} account-runtime-tag account-runtime-status`}
        tabIndex={0}
        aria-label={`${label}：${detail}`}
      >{label}</span>
    </Tooltip>
  );
}

function accountRuntimeDetail(account: Account) {
  const runtime = account.runtime;
  const parts = [(account.operational_status?.reason?.trim() ?? "")].filter((value): value is string => Boolean(value));
  if (!runtime) return parts.join("\n") || t("admin.cpa_native_credentials_are_healthy");
  if (runtime.error_count) {
    parts.push(t("admin.errors_in_the_last_hour", [runtime.error_count, runtime.rate_429_count ? t("admin.including_429", [runtime.rate_429_count]) : ""]));
  }
  if (runtime.affected_users > 0) parts.push(t("admin.affects_users", [runtime.affected_users]));
  if (runtime.last_error_status > 0) {
    parts.push(t("admin.last_http", [runtime.last_error_status]));
    parts.push(t("admin.last_error", [formatSiteTimestamp(runtime.last_error_at)]));
  }
  if (runtime.error_log_status === "ok") parts.push(t("admin.native_error_files", [runtime.error_log_files]));
  return [...new Set(parts)].join("\n") || t("admin.cpa_native_credentials_are_healthy");
}

function RebalanceSummary({ result }: { result: RebalanceResponse }) {
  const destinations = Object.entries(result.rebalance.destinations);
  return (
    <Space orientation="vertical" size={4}>
      <Text>{t("admin.users_moved")}{result.rebalance.moved_users}</Text>
      {destinations.length ? <Text>{t("admin.destination_distribution")}{destinations.map(([account, count]) => `${account} ${count}`).join("，")}</Text> : null}
      {result.rebalance.snapshot_generation ? (
        <Text type="secondary"><SafetyCertificateOutlined aria-hidden="true" /> {t("admin.authentication_snapshot")} {result.rebalance.snapshot_generation.slice(0, 12)}</Text>
      ) : null}
      {result.rebalance.warning ? <Text type="warning">{result.rebalance.warning}</Text> : null}
    </Space>
  );
}

function emptyAccountEditorValues(): AccountEditorValues {
  return {
    id: "",
    email: "",
    proxy_mode: "inherit",
    proxy_url: ""
  };
}

function accountEditorValues(account: Account): AccountEditorValues {
  return {
    ...emptyAccountEditorValues(),
    id: account.id,
    email: account.email,
    proxy_mode: proxyModeSchema.safeParse(account.proxy_mode).success
      ? account.proxy_mode as AccountEditorValues["proxy_mode"]
      : "inherit"
  };
}

function validProxyURL(value: string) {
  try {
    const parsed = new URL(value);
    return ["http:", "https:", "socks5:"].includes(parsed.protocol) &&
      Boolean(parsed.hostname) &&
      (parsed.pathname === "" || parsed.pathname === "/") &&
      !parsed.search && !parsed.hash && !/\s/.test(value);
  } catch {
    return false;
  }
}

const stateLabels: Record<string, string> = {
	credential_unavailable: t("common.credentials_unavailable"),
	transient_cooldown: t("common.temporary_cooldown"),
	rate_limited: t("common.rate_limited"),
	degraded: t("common.recent_errors"),
	runtime_unknown: t("admin.unknown_native_status"),
  quota_stale: t("admin.quota_data_expired"),
  quota_unavailable: t("admin.unknown_quota_status"),
  reserve_reached: t("admin.safety_reserve_reached"),
  oauth_missing: t("admin.oauth_not_configured"),
  container_not_running: t("admin.cpa_container_is_not_running"),
  upstream_disallowed: t("admin.upstream_unavailable"),
  account_disabled: t("common.disabled")
};

const runtimeStateReasons = new Set([
  "credential_unavailable",
  "transient_cooldown",
  "rate_limited",
  "degraded",
  "runtime_unknown"
]);

const proxyModeLabel: Record<string, string> = {
  inherit: t("admin.inherit_default"),
  custom: t("admin.custom_proxy"),
  direct: t("admin.direct")
};

const runtimeActionLabel: Record<AccountRuntimeAction, string> = {
  start: t("admin.start"),
  stop: t("admin.stop"),
  restart: t("admin.restart")
};
