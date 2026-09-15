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
        ? "表格已刷新，额度正在后台更新"
        : "数据已刷新");
    } catch (error) {
      setRefreshLabel("刷新失败");
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
    setRefreshLabel("刷新失败");
    showToast(accounts.error instanceof Error ? accounts.error.message : "账号数据加载失败", "error");
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
        job.name === "OAuth 授权" && job.target === account.id && ["running", "queued"].includes(job.status)
      ));
      if (existing) {
        showToast("该账号已有 OAuth 授权任务，已直接打开");
        setTaskPollError(null);
        setCompletedTaskJobID("");
        setTaskJob(existing);
        return;
      }
      setOAuthAccount(account);
    },
    onError: (error) => showToast(errorMessage(error, "OAuth 任务状态读取失败"), "error")
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
    showToast(taskJob.status === "succeeded" ? "任务执行成功" : "任务执行失败", taskJob.status === "succeeded" ? "success" : "error");
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
  const imageTarget = visibleImageStatus?.update_channel || visibleImageStatus?.target_image || "正在读取…";
  const localImage = visibleImageStatus?.local_image;
  const imageStatusLabel = !accounts.data
    ? "未知"
    : imageStatus.isError
    ? "未知"
    : !localImage?.available
      ? "尚未拉取"
      : (visibleImageStatus?.outdated_count ?? 0) > 0 ? "待更新" : "已同步";
  const imageSummary = !accounts.data
    ? "—"
    : localImage?.available
      ? `${localImage.version || "镜像未提供可识别版本"} · ${localImage.short_id || "摘要未知"} · ${visibleImageStatus?.current_count ?? 0}/${visibleImageStatus?.running_count ?? 0} 个运行中的已启用 CPA`
      : `${runningEnabledAccounts} 个已启用 CPA 运行中`;

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
            value={usageWindow} options={usageWindowOptions} label="账号用量"
            onChange={setUsageWindow} onCustomSelect={() => setCustomUsageRangeOpen(true)}
            start={rangeBoundary(usageWindow === "since_reset" ? null : accounts.data?.window_start_at, usageWindow === "since_reset" ? "各账号重置时间" : usageWindow === "all" ? "不限" : "—")}
            end={rangeBoundary(accounts.data?.window_end_at)} updating={accounts.isFetching}
          />
          <div className="account-time-filter-actions">
            <AccountFilter label="搜索账号">
              <Input
                className="account-search-input"
                aria-label="搜索 CPA 账号"
                prefix={<span className="account-search-legacy-icon" aria-hidden="true" />}
                placeholder="账号、名称或邮箱"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </AccountFilter>
            <div className="account-filter-actions">
              <AccountFilter label="运行状态">
                <WideSelect<AccountRuntimeFilter>
                  aria-label="运行状态"
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
                添加 CPA
              </Button>
            </div>
          </div>
        </div>

        <div className="account-control-strip">
          <div className="account-control-copy">
            <h3>更新通道</h3>
            <code>{imageTarget}</code>
            <small>{imageSummary}</small>
          </div>
          <div className="account-control-status-region">
            <Tag className={`account-control-status ${imageStatusLabel === "已同步" ? "success" : imageStatusLabel === "未知" ? "neutral" : "warning"}`}>{imageStatusLabel}</Tag>
          </div>
          <Space className="account-control-actions" size={8}>
            <Button
              loading={imageMutation.isPending && imageMutation.variables?.action === "image-pull"}
              disabled={imageMutation.isPending}
              onClick={() => {
                imageMutation.reset();
                imageMutation.mutate({ action: "image-pull", target: "all" });
              }}
            >拉取镜像</Button>
            <Button
              type="primary"
              disabled={imageMutation.isPending || Boolean(accounts.data && (
                !localImage?.available || (visibleImageStatus?.outdated_count ?? 0) === 0
              ))}
              onClick={() => {
                imageMutation.reset();
                setImageUpdateTarget("all");
              }}
            >更新全部 CPA</Button>
            <Button
              className="account-rebalance-all-button"
              disabled={enabledAccounts < 2}
              onClick={() => {
                rebalance.reset();
                setConfirmOpen(true);
              }}
            >一键负载均衡</Button>
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
              "aria-label": `${expandedAccountIDs.includes(account.id) ? "收起" : "展开"} ${account.id}`,
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
              <h3>{catalog.accounts.length ? "没有匹配的 CPA" : "还没有 CPA 账号"}</h3>
              <Button type="primary" onClick={() => openEditor("create")}>添加 CPA</Button>
            </div>
          ) : null}
        </div>
      </div>

      {modelTestAccount ? <AccountModelTestModal account={modelTestAccount} csrfToken={csrfToken} onClose={() => setModelTestAccount(null)} /> : null}
      <CustomUsageRangeModal
        open={customUsageRangeOpen}
        title="账号信息自定义统计范围"
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
        title={pendingAccountUpdate?.renamed ? "修改业务 CPA？" : "修改出口代理？"}
        open={pendingAccountUpdate !== null}
        okText="确认修改"
        onCancel={() => setPendingAccountUpdate(null)}
        onOk={() => {
          const command = pendingAccountUpdate?.command;
          setPendingAccountUpdate(null);
          if (command) lifecycle.mutate(command);
        }}
      >
        <Paragraph>
          {pendingAccountUpdate ? `${[
            pendingAccountUpdate.renamed
              ? `${pendingAccountUpdate.command.request.id} 将迁移为 ${pendingAccountUpdate.command.request.new_id}`
              : "",
            pendingAccountUpdate.proxyChanged ? "出口代理设置将更新" : ""
          ].filter(Boolean).join("；")}。相关容器会短暂重启，OAuth、日志和 Key 关联会保留。` : ""}
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
        title="一键负载均衡所有账号"
        open={confirmOpen}
        confirmLoading={rebalance.isPending}
        okText="确认开始均衡"
        cancelText="取消"
        okButtonProps={{ danger: true }}
        onCancel={() => !rebalance.isPending && setConfirmOpen(false)}
        onOk={() => rebalance.mutate()}
        destroyOnHidden
      >
        <Space orientation="vertical" size={16} className="rebalance-confirmation">
          <Alert
            type="warning"
            showIcon
            title="这会修改用户当前路由"
            description="系统会按账号可用额度重新分布全部有效用户，并尽量减少迁移数量。任一用户不满足统一 Key 安全条件时，整批操作都会拒绝。"
          />
          <Paragraph>
            路由写入后必须等待 Gateway 激活新的鉴权快照；失败时自动恢复原路由并发布回滚快照。成功后会立即重新查询配置窗口内活跃用户数。
          </Paragraph>
          {rebalance.isError ? <MutationError error={rebalance.error} title="负载均衡未执行" /> : null}
        </Space>
      </Modal>
      <LegacyConfirmModal
        title={rebalanceTarget ? `迁移 ${rebalanceTarget.id} 的全部用户？` : "迁移全部用户？"}
        open={rebalanceTarget !== null}
        confirmLoading={accountRebalance.isPending}
        okText="确认迁移"
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
              ? `系统会先刷新所有账号的官方额度，再将当前 ${formatNumber(rebalanceTarget.routed_users)} 位用户按自动切换算法分配到其他可用账号。已经开始的请求不会被重放。`
              : "系统会先刷新所有账号的官方额度，再按自动切换算法迁移用户。已经开始的请求不会被重放。"}
          </Paragraph>
        </Space>
      </LegacyConfirmModal>
      <LegacyConfirmModal
        title={runtimeOperation?.action === "stop" ? "停止服务？" : "重启服务？"}
        open={runtimeOperation !== null}
        okText={runtimeOperation?.action === "stop" ? "确认停止" : "确认重启"}
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
                ? `将停止 ${runtimeOperation.account.id}；影响范围暂不可确认。`
                : runtimeImpact.data?.routed_users
                  ? `将停止 ${runtimeOperation.account.id}，当前有 ${formatNumber(runtimeImpact.data.routed_users)} 个用户路由到该账号。`
                  : `将停止 ${runtimeOperation.account.id}，当前没有用户路由到该账号。`}
            </Paragraph>
          )
        ) : (
          <Paragraph>{runtimeOperation ? `将重启 ${runtimeOperation.account.id}。` : ""}</Paragraph>
        )}
      </LegacyConfirmModal>
      <RuntimeLogsModal
        target={logTarget}
        query={logs}
        onClose={() => setLogTarget(null)}
      />
      <LegacyConfirmModal
        title="更新 CPA 镜像？"
        open={imageUpdateTarget !== null}
        confirmLoading={imageMutation.isPending}
        okText={imageUpdateTarget === "all" ? "更新全部 CPA" : "更新此 CPA"}
        onCancel={() => !imageMutation.isPending && setImageUpdateTarget(null)}
        onOk={() => {
          const target = imageUpdateTarget === "all" ? "all" : imageUpdateTarget?.id;
          setImageUpdateTarget(null);
          if (target) imageMutation.mutate({ action: "image-update", target });
        }}
      >
        <Paragraph>
          {imageUpdateTarget === "all"
            ? "将使用已拉取并锁定版本与摘要的目标镜像逐个重建运行中的已启用 CPA，停用账号会跳过；失败时自动恢复原镜像。"
            : imageUpdateTarget
              ? `将使用已锁定版本与摘要的目标镜像重建 ${imageUpdateTarget.id}；失败时自动恢复原镜像。`
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
      title: "序号",
      width: "4%",
      align: "center",
      render: (_, __, index) => index + 1
    },
    {
      title: <span className="sr-only">展开</span>,
      width: "4%",
      align: "center",
      render: () => (
        <div className="account-cell-content account-toggle-content">
          <span className="account-chevron" aria-hidden="true">›</span>
        </div>
      )
    },
    {
      ...accountSortHeader("account", "CPA 账号", sort, onSort),
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
      ...accountSortHeader("runtime", "账号状态", sort, onSort),
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
      ...accountSortHeader("quota", "额度与重置", sort, onSort),
      width: "24%",
      render: (_, account) => {
        const used = account.account_state.used_percent;
        if (!account.state_available || used === null) {
          return (
            <div className="account-cell-content">
              <div className="account-quota-overview">
                <div className="account-quota-main">
                  <div className="account-quota-unavailable">
                    <span className="table-secondary quota-unavailable">暂不可用</span>
                  </div>
                </div>
                <div className="account-quota-reset-cell">
                  <span>{accountResetCreditLabel(account)}</span>
                  <Button size="small" disabled>重置</Button>
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
                  <progress className={quotaTone} max="100" value={bounded} aria-label={`已使用 ${formatPercent(bounded)}`} />
                  <small>{account.account_state.reset_at ? `下次重置 ${formatSiteTimestamp(account.account_state.reset_at)}` : "重置时间未知"}</small>
                </div>
              </div>
              <div className="account-quota-reset-cell quota-reset-cell">
                <span className="quota-reset-count">{accountResetCreditLabel(account)}</span>
                <button
                  className="quota-reset-action"
                  type="button"
                  aria-label="重置"
                  disabled={!account.resettable}
                  title={account.resettable ? `重置 ${account.reset_window_labels?.join("、") || "周限额"}` : account.reset_credit_count === 0 ? "没有剩余重置次数" : "周额度尚未耗尽或状态待刷新"}
                  onClick={() => onResetQuota(account)}
                >重置</button>
              </div>
            </div>
          </div>
        );
      }
    },
    {
      ...accountSortHeader("activity", "使用情况", sort, onSort),
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
      ...accountSortHeader("last_used", "最后使用", sort, onSort),
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
          ? `${label}，当前${sort.direction === "asc" ? "升序" : "降序"}，点击切换排序方向`
          : `${label}，点击排序`}
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
    ? "CPA 账号已停用；启用后再更新镜像"
    : !image?.running
      ? "CPA 未运行；拉取镜像后下次启动会使用目标镜像"
      : !imageStatus.data?.local_image?.available
        ? "请先拉取目标镜像"
        : image.using_target
          ? "当前 CPA 已使用目标镜像"
          : "使用目标镜像重建此 CPA";
  return (
    <div className="account-expanded-panel">
      <div className="account-detail-facts account-runtime-facts">
        <AccountDetailFact label="上游邮箱" value={account.email} />
        <AccountDetailFact
          label="容器"
          value={account.service || image?.service || `cliproxy-${account.id}`}
          note={account.container_status || runtimeStateLabel[account.runtime_state]}
        />
        <AccountDetailFact
          label="账号状态"
          value={account.operational_status?.label || accountStatusLabel(account)}
          note={account.operational_status ? accountRuntimeDetail(account) : (stateLabels[account.account_state.reason] ?? account.account_state.reason)}
        />
        <AccountDetailFact
          label="OAuth 文件"
          value={account.auth_files ?? (account.oauth_configured === null ? "—" : account.oauth_configured ? 1 : 0)}
        />
        <AccountDetailFact
          label="镜像版本"
          value={image?.version || (imageStatus.isPending ? "读取中" : "—")}
          note={image?.image_short_id ? `SHA ${image.image_short_id}` : imageStatus.isError ? "镜像状态暂不可用" : ""}
        />
        <AccountDetailFact
          label="出口代理"
          value={account.proxy_source === "account" ? "账号自定义" : account.proxy_source === "default" ? "控制面默认" : "强制直连"}
          note={account.proxy_display || "direct"}
        />
      </div>

      {account.usage_available ? <AccountUsageFacts usage={account.usage} /> : (
        <div className="account-usage-unavailable" role="status">
          <strong>当前范围用量暂不可用</strong>
          <span>请刷新账号数据后重试。</span>
        </div>
      )}

      <AccountModelUsage query={usageDetail} />

      <div className="account-detail-actions">
        <div className="account-detail-action-group" role="group" aria-label="常用操作">
          <button className="button ghost" type="button" disabled={!running} title={!running ? "请先启动账号容器" : undefined} onClick={() => onModelTest(account)}>模型通信测试</button>
          <button className="button ghost" type="button" onClick={() => onOpenLogs(account.id)}>查看日志</button>
          <button className="button ghost" type="button" aria-label={`编辑 ${account.id}`} onClick={() => onEdit(account)}>编辑账号</button>
          <button className="button ghost" type="button" onClick={() => onOAuth(account)}>
            {account.oauth_configured === true ? "重新 OAuth" : "开始 OAuth"}
          </button>
        </div>
        <div className="account-detail-action-group" role="group" aria-label="容器维护">
          <button
            className="button ghost"
            type="button"
            disabled={imageUpdateDisabled}
            title={imageUpdateTitle}
            onClick={() => onUpdateImage(account)}
          >{image?.using_target ? "镜像已同步" : "更新镜像"}</button>
          <button className="button ghost" type="button" onClick={() => onRuntimeOperation({ action: running ? "restart" : "start", account })}>
            {running ? "重启容器" : "启动容器"}
          </button>
        </div>
        <div className="account-detail-action-group account-detail-action-group-risk" role="group" aria-label="风险操作">
          <button
            className="button account-rebalance-action"
            type="button"
            disabled={account.routed_users <= 0}
            title={account.routed_users <= 0 ? "当前账号没有需要迁移的用户" : `将 ${formatNumber(account.routed_users)} 个已路由用户按自动切换算法分配到其他可用账号`}
            aria-label={`迁移全部用户：${account.routed_users <= 0 ? "当前账号没有需要迁移的用户" : `将 ${formatNumber(account.routed_users)} 个已路由用户按自动切换算法分配到其他可用账号`}`}
            onClick={() => onRebalance(account)}
          >
            迁移全部用户
          </button>
          {running ? (
            <button className="button danger-outline" type="button" onClick={() => onRuntimeOperation({ action: "stop", account })}>停止容器</button>
          ) : null}
          <button
            className={`button ${account.enabled ? "danger-outline is-enabled" : "secondary"} account-policy-action`}
            type="button"
            disabled={account.enabled && !availableOtherAccounts}
            title={account.enabled && !availableOtherAccounts ? "至少保留一个可用 CPA" : undefined}
            onClick={() => onPolicy(account)}
          >
            {account.enabled ? "停用账号" : "启用账号"}
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
      <AccountDetailFact className="account-request-fact" label="成功请求" value={formatNumber(usage.success_count)} />
      <AccountDetailFact className="account-request-fact" label="失败请求" value={formatNumber(usage.failed_count)} />
      <AccountDetailFact className="account-token-fact" label="输入 Token" value={<LegacyTokenUsage value={usage.input_tokens} />} />
      <AccountDetailFact className="account-token-fact" label="输出 Token" value={<LegacyTokenUsage value={usage.output_tokens} />} />
      <AccountDetailFact className="account-token-fact" label="推理 Token" value={<LegacyTokenUsage value={usage.reasoning_tokens} />} />
      <div className="account-cache-fact account-token-fact">
        <div className="account-cache-head">
          <small title="缓存 Token ÷ 输入 Token">缓存率 {formatRatio(usage.cached_tokens, usage.input_tokens)}</small>
          <span>缓存 Token</span>
        </div>
        <LegacyTokenUsage value={usage.cached_tokens} />
      </div>
      <div className="account-token-total-fact account-token-fact">
        <span>Token 总计</span>
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
    <section className="account-model-usage" aria-label="模型与推理强度 Token 明细">
      <div className="account-model-usage-title">模型 × 推理强度 Token 明细</div>
      {query.isPending ? (
        <div className="account-model-usage-skeleton" aria-label="正在加载模型 Token 明细"><span /><span /></div>
      ) : query.isError ? (
        <div className="account-model-usage-message error" role="alert">
          <span>{query.error instanceof Error ? query.error.message : "模型 Token 明细加载失败"}</span>
          <Button size="small" onClick={() => void query.refetch()}>重试</Button>
        </div>
      ) : models.length ? (
        <div className="account-model-usage-list">
          {models.map((model) => (
            <div className="account-model-usage-row" key={model.model}>
              <div className="account-model-usage-head">
                <strong title={model.model}>{model.model}</strong>
                <LegacyTokenUsage value={model.totalTokens} />
              </div>
              <div className="account-model-progress" role="group" aria-label={`${model.model} 各推理强度 Token 占比`}>
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
        <div className="account-model-usage-message">当前范围暂无可展示的模型 Token 数据。</div>
      )}
    </section>
  );
}

function accountModelTooltip(model: string, effort: AccountModelEffort) {
  return [
    `${model} · ${effort.reasoning_effort}`,
    `调用：${formatNumber(effort.request_count)}`,
    `输入：${formatNumber(effort.input_tokens)}`,
    `输出：${formatNumber(effort.output_tokens)}`,
    `推理：${formatNumber(effort.reasoning_tokens)}`,
    `缓存：${formatNumber(effort.cached_tokens)}`,
    `总 Token：${formatNumber(effort.total_tokens)}`
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
  return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(value)}%`;
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
  return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(numerator * 100 / denominator)}%`;
}

function AccountOAuthStatus({ account }: { account: Account }) {
  const configured = accountOAuthConfigured(account);
  if (configured === null) return <span className="status-chip neutral">未知</span>;
  return <span className={`status-chip ${configured ? "success" : "warning"}`}>{configured ? "已授权" : "待授权"}</span>;
}

function AccountActivity({ account, windowSeconds }: { account: Account; windowSeconds: number }) {
  const activeEmails = [...new Set((account.active_user_emails_1h ?? []).map((email) => email.trim()).filter(Boolean))];
  const activeUsers = account.active_users_1h;
  const activeValue = activeUsers === null ? "—" : formatNumber(activeUsers);
  const windowLabel = formatActiveUserWindow(windowSeconds);
  const activeDetail = activeUsers === null ? "数据暂不可用" : activeUsers === 0 ? `${windowLabel} 无请求` : windowLabel;
  const activeHelp = `过去${windowLabel}内至少发起 1 次业务请求的去重用户；成功和失败请求均计入。`;
  return (
    <div className="account-activity account-activity-cell">
      <div className="active">
        <span>
          活跃 {activeUsers !== null && activeUsers > 0 && activeEmails.length ? (
            <span
              className="account-active-users"
              tabIndex={0}
              aria-label={`${windowLabel}活跃使用者：${activeEmails.join("，")}`}
            >
              <strong>{activeValue}</strong>
              <span className="account-active-users-tooltip" role="tooltip">
                <b>{windowLabel}活跃使用者（{formatNumber(activeUsers)}）</b>
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
        <span>路由 <strong>{formatNumber(account.routed_users)}</strong></span>
        <small>{formatNumber(account.associated_users)} 关联</small>
      </div>
      <div>
        <span>请求 <strong>{account.usage_available ? formatNumber(account.usage.request_count) : "—"}</strong></span>
        <small className={!account.usage_available || account.usage.failed_count ? "warning" : ""}>{account.usage_available ? (account.usage.failed_count ? `${formatNumber(account.usage.failed_count)} 失败` : "全部成功") : "额度周期不可用"}</small>
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

const accountTableCollator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });

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
        account.oauth_configured === true ? "已授权" : account.oauth_configured === false ? "待授权" : "未知"
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
  if (account.operational_status?.label) return account.operational_status.label;
  if (!account.enabled) return "已停用";
  if (account.runtime_state === "stopped") return "已停止";
  if (!account.state_available) return "状态未知";
  if (account.account_state.exhausted) return "额度耗尽";
  if (runtimeStateReasons.has(account.account_state.reason)) return stateLabels[account.account_state.reason];
  if (account.account_state.eligible) return "可用";
  return stateLabels[account.account_state.reason] ?? "暂不可迁入";
}

function accountResetCreditLabel(account: Account) {
  return typeof account.reset_credit_count === "number"
    ? `剩余 ${account.reset_credit_count} 次`
    : "额度未知";
}

function formatPercent(value: number) {
  return `${Number(value.toFixed(value >= 10 ? 0 : 1))}%`;
}

function formatTaskDuration(startedAt?: number | null, finishedAt?: number | null, active = false) {
  if (active) return "执行中";
  if (!startedAt || !finishedAt) return "—";
  const seconds = Math.max(0, finishedAt - startedAt);
  if (seconds < 1) return "< 1 秒";
  if (seconds < 60) return `${seconds} 秒`;
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return remainder ? `${minutes} 分 ${remainder} 秒` : `${minutes} 分钟`;
}

function formatAccountLastUsed(timestamp: number) {
  return timestamp ? formatSiteTimestamp(timestamp) : "从未使用";
}

function accountRefreshLabel(catalog: AccountCatalog, nowSeconds = catalog.generated_at) {
  const observedAt = Math.max(0, ...catalog.accounts.map((account) => account.account_state.observed_at || 0));
  const generatedAt = catalog.quota_generated_at || observedAt;
  if (!Number.isFinite(generatedAt) || generatedAt <= 0) {
    return catalog.quota_refreshing ? "额度正在后台更新" : "额度数据暂不可用";
  }
  const cacheSeconds = Number.isFinite(catalog.quota_cache_ttl_seconds) && catalog.quota_cache_ttl_seconds > 0
    ? catalog.quota_cache_ttl_seconds
    : 60;
  // Allow one extra cache period for the scheduled collection round to finish.
  const stale = nowSeconds - generatedAt > cacheSeconds * 2;
  const state = catalog.quota_refreshing
    ? "（后台更新中）"
    : stale
      ? "（数据较旧，请刷新）"
      : "";
  return `额度更新于 ${formatSiteTimestamp(generatedAt)}${state}`;
}

function formatNumber(value: number) {
  return new Intl.NumberFormat("zh-CN").format(value);
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
  { value: "all", label: "全部" },
  { value: "running", label: "运行中" },
  { value: "stopped", label: "已停止" },
  { value: "disabled", label: "已停用" }
] satisfies Array<{ value: AccountRuntimeFilter; label: string }>;

const authFilterOptions = [
  { value: "all", label: "全部" },
  { value: "configured", label: "已授权" },
  { value: "pending", label: "待授权" }
] satisfies Array<{ value: AccountAuthFilter; label: string }>;

const usageWindowOptions = [
  { value: "3600", label: "1 小时" },
  { value: "today", label: "今日" },
  { value: "86400", label: "24 小时" },
  { value: "604800", label: "7 天" },
  { value: "2592000", label: "30 天" },
  { value: "since_reset", label: "额度周期" },
  { value: "all", label: "全部" }
] satisfies Array<{ value: AccountUsageWindow; label: string }>;

function formatActiveUserWindow(seconds: number): string {
  const minutes = Math.max(1, Math.round(seconds / 60));
  if (minutes % 60 === 0) return `近 ${minutes / 60} 小时`;
  return `近 ${minutes} 分钟`;
}

const runtimeStateLabel: Record<Account["runtime_state"], string> = {
  running: "运行中",
  stopped: "已停止",
  disabled: "已停用",
  unknown: "未知"
};

const runtimeJobStatusLabels: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  cancelling: "取消中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消"
};

const proxyModeSchema = z.enum(["inherit", "custom", "direct"]);
const accountEditorSchema = z.object({
  id: z.string().trim().regex(/^[a-z][a-z0-9-]{1,31}$/, "请输入 2-32 位小写字母、数字或连字符，且以字母开头"),
  email: z.string().trim().email("请输入有效邮箱地址"),
  proxy_mode: proxyModeSchema,
  proxy_url: z.string().trim().refine((value) => !value || validProxyURL(value), "仅支持无路径的 HTTP、HTTPS 或 SOCKS5 代理地址")
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
      form.setError("proxy_url", { message: "独立代理模式必须输入代理地址" });
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
      title={<LegacyDialogTitle title={account ? account.id : "添加业务 CPA"} kicker={account ? "BUSINESS CPA" : "NEW BUSINESS CPA"} />}
      open={open}
      width={account ? 720 : 560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={account ? "保存修改" : pending ? "正在创建…" : "创建并启动"}
      cancelText="取消"
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
            <AccountDetailFact label="端口" value={`:${account.port}`} />
            <AccountDetailFact
              label="出口"
              value={account.proxy_source === "account" ? "账号代理" : account.proxy_source === "default" ? "默认代理" : "直连"}
            />
            <AccountDetailFact label="容器" value={runtimeStateLabel[account.runtime_state]} />
            <AccountDetailFact label="OAuth" value={account.oauth_configured ? "已授权" : "待授权"} />
            <AccountDetailFact label="当前用户" value={account.routed_users} />
          </div>
        ) : null}
        <Row gutter={16}>
          <Col xs={24}><EditorInput control={form.control} name="id" label={account ? "CPA 标识" : "账号标识"} minLength={account ? undefined : 2} placeholder={account ? "例如 account-a" : "例如 codex-team-2"} /></Col>
          {!account ? <Col xs={24}><Paragraph className="account-field-help">用于容器名、配置文件名和 Key 路由，只能填写小写字母、数字和连字符。</Paragraph></Col> : null}
          <Col xs={24}><EditorInput control={form.control} name="email" label={account ? "显示邮箱" : "上游账号邮箱"} placeholder="account@example.com" /></Col>
          <Col xs={24}>
            <Controller
              control={form.control}
              name="proxy_mode"
              render={({ field, fieldState }) => (
                <Form.Item label="出口代理" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
                  <LegacyEnhancedSelect
                    label="出口代理"
                    value={field.value}
                    options={[
                      { value: "inherit", label: "继承控制面默认代理" },
                      { value: "custom", label: "使用账号自定义代理" },
                      { value: "direct", label: "强制直连" }
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
                    label="账号代理 URL"
                    validateStatus={fieldState.error ? "error" : undefined}
                    help={fieldState.error?.message}
                  >
                    <Input.Password
                      {...field}
                      aria-label="账号代理 URL"
                      autoComplete="new-password"
                      visibilityToggle={{ tabIndex: -1 }}
                      placeholder={account ? "留空保持现有代理；支持 HTTP、HTTPS、SOCKS5" : "例如 socks5://user:pass@host:1080"}
                    />
                  </Form.Item>
                )}
              />
            </Col>
          ) : null}
        </Row>
        {!account ? (
          <>
            <Paragraph className="account-field-help">账号设置优先于控制面默认代理；支持 HTTP、HTTPS 和 SOCKS5。</Paragraph>
            <div className="account-provision-list" aria-label="自动执行内容">
              <span>解析出口代理</span>
              <span>生成 CPA 配置</span>
              <span>创建认证目录</span>
              <span>后台补齐已有用户 Key</span>
              <span>启动容器并刷新路由</span>
            </div>
            <div className="account-inline-notice">添加 CPA 后会在后台关联全部已有用户的统一 Key；账号创建后请继续完成 OAuth。</div>
          </>
        ) : null}
        {account ? (
          <>
            <Paragraph className="account-field-help">
              当前生效：{account.proxy_display || "直连"}；账号代理设置优先，修改后只重建当前 CPA。
            </Paragraph>
            <Paragraph className="account-editor-explanation">
              修改 CPA 标识会迁移容器、路由、OAuth、日志和 Key 关联，并短暂重启该业务 CPA；显示邮箱不会替换 OAuth 登录身份。
            </Paragraph>
            <LegacyFormError error={error} />
            <section className="account-danger-zone" aria-label="危险操作">
              <div>
                <strong>危险操作</strong>
                <p>清除授权或删除 CPA 前都会自动创建本地安全归档。</p>
              </div>
              <Space wrap size={8}>
                <Button danger onClick={() => onDestructiveAction({ kind: "clear-auth", account })}>清除 OAuth</Button>
                <Button danger onClick={() => onDestructiveAction({ kind: "delete", account })}>删除 CPA</Button>
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
      title={<LegacyDialogTitle title={`${enabling ? "启用" : "停用"} ${account.id}`} kicker="ROUTING AVAILABILITY" />}
      open
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={pending ? (enabling ? "正在启用…" : "正在停用…") : enabling ? "确认启用" : "确认停用"}
      confirmLoading={pending}
      closable={!pending}
      mask={{ closable: !pending }}
      keyboard={!pending}
      cancelText="取消"
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
            ? `启用后，状态可用时用户可以选择 ${account.id}；已有用户路由不会自动变化。`
            : account.routed_users > 0
              ? `停用后将不再允许用户选择；当前 ${formatNumber(account.routed_users)} 位用户必须迁移到其他已启用 CPA。`
              : "停用后将不再允许用户选择；当前没有用户路由到该账号。"}
        </div>
        {requiresFallback ? (
          <label className="field">
            <span>现有用户切换到</span>
            <LegacyEnhancedSelect label="现有用户切换到" value={fallback} options={options} onChange={setFallback} disabled={pending} />
          </label>
        ) : null}
        {pending ? <div role="status">正在更新账号选择策略，请稍候。</div> : null}
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
        <Button key="cancel" disabled={confirmLoading} onClick={onCancel}>取消</Button>,
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
      title="开始 OAuth 授权？"
      open
      okText="开始授权"
      confirmLoading={starting}
      onCancel={onClose}
      onOk={onStart}
    >
      <Paragraph>任务输出会显示设备授权地址和一次性验证码。完成浏览器授权前请勿关闭任务窗口。</Paragraph>
      {startError ? <MutationError error={startError} title="OAuth 授权任务未提交" /> : null}
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
  const output = rawOutput || "任务正在排队…";
  const device = parseOAuthDeviceOutput(output);
  const imageUpdateReport = /更新.*镜像|镜像.*更新/.test(job.name)
    ? parseImageUpdateOutput(rawOutput, job.status)
    : null;
  const active = isActiveRuntimeJob(job);
  const startedAt = job.started_at || job.created_at;
  const outputLineCount = rawOutput ? rawOutput.split(/\r?\n/).filter(Boolean).length : 0;
  const copy = async (value: string, label: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopyNotice(`${label}已复制`);
    } catch {
      setCopyNotice(`${label}复制失败，请手动复制`);
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
      label: state === "copied" ? "已复制" : state === "failed" ? "复制失败" : idleLabel,
      icon: state === "copied"
        ? <CheckOutlined aria-hidden="true" />
        : <CopyOutlined aria-hidden="true" />
    };
  };
  const addressCopyButton = deviceCopyButton("url", "复制地址");
  const codeCopyButton = deviceCopyButton("code", "复制设备码");
  return (
    <Modal
      className="legacy-output-modal"
      title={<LegacyDialogTitle title={job.name || "任务输出"} kicker="TASK OUTPUT" />}
      open
      width={900}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      footer={[
        <Button className="legacy-output-secondary" key="copy-output" onClick={() => void copy(output, "完整输出")}>复制完整输出</Button>,
        active ? (
          <Button key="cancel-job" danger loading={cancelling} onClick={onCancelJob}>取消任务</Button>
        ) : null,
        <Button className="legacy-output-ghost" key="close" onClick={onClose}>关闭</Button>
      ]}
    >
      <div className="oauth-task-meta task-output-meta" aria-label="任务执行摘要">
        <div>
          <span>执行范围</span>
          <strong className="oauth-task-account">
            <span>{job.target === "all" ? "全部 CPA" : job.target}</span>
            {accountEmail ? <span className="oauth-task-email">{accountEmail}</span> : null}
          </strong>
        </div>
        <div>
          <span>执行状态</span>
          <Tag color={job.status === "succeeded" ? "success" : job.status === "failed" ? "error" : active ? "processing" : "default"}>
            {runtimeJobStatusLabels[job.status] ?? job.status}
          </Tag>
        </div>
        <div>
          <span>开始时间</span>
          <time>{formatSiteTimestamp(startedAt)}</time>
        </div>
        <div>
          <span>完成时间</span>
          <time>{job.finished_at ? formatSiteTimestamp(job.finished_at) : active ? "执行中" : "—"}</time>
        </div>
        <div>
          <span>任务耗时</span>
          <strong>{formatTaskDuration(startedAt, job.finished_at, active)}</strong>
        </div>
        <div>
          <span>输出记录</span>
          <strong>{outputLineCount} 行</strong>
        </div>
      </div>
      {device.url || device.code ? (
        <section className="oauth-copy-panel" aria-label="OAuth 设备授权信息">
          <div className="oauth-copy-grid">
            <div>
              <span>授权地址</span>
              <code>{device.url || "—"}</code>
              <Button
                disabled={!device.url}
                danger={deviceCopyState.url === "failed"}
                icon={addressCopyButton.icon}
                onClick={() => void copyDeviceValue("url", device.url)}
              >{addressCopyButton.label}</Button>
            </div>
            <div>
              <span>设备码</span>
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
      {pollError ? <MutationError error={pollError} title="任务状态刷新失败，正在重试" /> : null}
      {job.error ? <Alert className="page-alert" type="error" showIcon title="任务执行失败" description={job.error} /> : null}
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
    ? "正在读取…"
    : query.isError
      ? (query.error instanceof Error ? query.error.message : "日志读取失败")
      : query.data?.output || "暂无日志";
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(output);
      setCopyNotice("完整输出已复制");
    } catch {
      setCopyNotice("完整输出复制失败，请手动复制");
    }
  };
  return (
    <Modal
      className="legacy-output-modal"
      title={<LegacyDialogTitle title={`${target} 日志`} kicker="SERVICE LOGS" />}
      open
      width={900}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      footer={[
        <Button className="legacy-output-secondary" key="copy" onClick={() => void copy()}>复制完整输出</Button>,
        <Button className="legacy-output-ghost" key="close" onClick={onClose}>关闭</Button>
      ]}
    >
      <div className="oauth-task-meta"><span>最近 200 行</span><span>{target}</span></div>
      {copyNotice ? <Alert className="page-alert" type="info" showIcon title={copyNotice} /> : null}
      {query.data?.truncated ? <Alert className="page-alert" type="warning" showIcon title="输出已按 2 MiB 上限截断" /> : null}
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
    ? `上游显示可用 ${available ?? "—"} 次，目前提供 ${details.credits.length} 条可选择明细。`
    : `当前可用 ${available ?? details?.credits.length ?? "—"} 次，本次将消耗其中 1 次。`;
  return (
    <Modal
      className="legacy-account-editor-modal account-quota-reset-modal"
      title={<LegacyDialogTitle title={`重置 ${account.id} 周限额`} kicker="WEEKLY QUOTA RESET" />}
      open
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={pending ? "正在重置…" : "确认重置"}
      okType="default"
      cancelText="取消"
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
        <div className="warning-banner">重置会消耗 1 次重置额度，并立即刷新当前已耗尽的周限额。操作不可撤销。</div>
        {query.isPending ? <Skeleton active paragraph={{ rows: 4 }} /> : null}
        {query.isError ? <LegacyFormError error={query.error} /> : null}
        {details ? (
          <>
            <p className="quota-reset-credit-summary">{creditSummary}</p>
            <p className="quota-reset-targets">将刷新：{details.windows.map((window) => window.label || "周限额").join("、") || "当前没有可重置的周限额"}</p>
            <label className="field">
              <span>选择要使用的重置额度</span>
              <LegacyEnhancedSelect
                label="选择要使用的重置额度"
                value={creditID}
                options={details.credits.map((credit, index) => ({
                  value: credit.id,
                  label: `${credit.title || "Full reset"}${details.credits.length > 1 ? ` #${index + 1}` : ""} · ${credit.expires_at ? `${formatSiteTimestamp(credit.expires_at)} 到期` : "长期有效"}`
                }))}
                autoFocus
                required
                onChange={setCreditID}
              />
            </label>
            <p className="field-help">日期表示该次 Full reset 的到期时间。提交前系统会重新读取额度状态；如果所选额度已使用或过期，本次操作会自动停止。</p>
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
  confirm: z.string().trim().min(1, "请输入 CPA 标识以确认"),
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
        title="清除 OAuth 授权？"
        open
        okText="清除授权"
        danger
        confirmLoading={pending}
        onCancel={onCancel}
        onOk={() => onSubmit({ kind: "clear-auth", request: { id: account.id, confirm: account.id } })}
      >
        <Paragraph>
          {account.id} 的本地授权文件会先归档再清除，容器随后重启。用户 Key 不会被删除。
        </Paragraph>
        {error ? <MutationError error={error} title="OAuth 清理未执行" /> : null}
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
      title={<LegacyDialogTitle title="删除业务 CPA" kicker="DESTRUCTIVE ACTION" />}
      open
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText="确认删除"
      okType="default"
      cancelText="取消"
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
          容器、路由、授权目录和用户关联记录将被移除；统一 Key 在其他 CPA 中继续有效。
        </div>
        {deleting && accounts.length <= 1 ? <div className="warning-banner">不能删除最后一个业务账号</div> : null}
        {deleting && accounts.length > 1 ? (
          <Controller
            control={form.control}
            name="fallback_account"
            render={({ field }) => (
              <label className="field">
                <span>用户切换到</span>
                <LegacyEnhancedSelect label="用户切换到" value={field.value} options={fallbackOptions} onChange={field.onChange} />
              </label>
            )}
          />
        ) : null}
        <Controller
          control={form.control}
          name="confirm"
          render={({ field }) => (
            <label className="field">
              <span>输入 CPA 标识以确认</span>
              <input {...field} aria-label="输入 CPA 标识以确认" autoComplete="off" autoFocus placeholder={account.id} required />
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

function errorMessage(error: unknown, fallback = "请求失败，请稍后重试") {
  return error instanceof ApiError || error instanceof Error ? error.message : fallback;
}

function AccountStatus({ account }: { account: Account }) {
  let label: string;
  let tone: string;
  let detail: string;
  if (account.operational_status) {
    const status = account.operational_status;
    label = status.label;
    tone = status.tone;
    detail = accountRuntimeDetail(account);
  } else {
    label = "暂不可迁入";
    tone = "neutral";
    detail = stateLabels[account.account_state.reason] ?? "CPA 当前不满足迁入条件";
    if (!account.enabled) {
      label = "已停用";
      detail = "账号已停用，不再接收新用户";
    } else if (account.runtime_state === "stopped") {
      label = "已停止";
      tone = "danger";
      detail = "CPA 容器未运行";
    } else if (!account.state_available) {
      label = "状态未知";
      detail = "CPA 运行或额度状态暂不可用";
    } else if (account.account_state.exhausted) {
      label = "额度耗尽";
      tone = "danger";
      detail = "官方周额度已经耗尽";
    } else if (runtimeStateReasons.has(account.account_state.reason)) {
      label = stateLabels[account.account_state.reason];
      tone = account.account_state.reason === "credential_unavailable" ? "danger" : "warning";
    } else if (account.account_state.eligible) {
      label = "可用";
      tone = "success";
      detail = "CPA 原生凭据状态正常";
    } else {
      label = stateLabels[account.account_state.reason] ?? "暂不可迁入";
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
  const parts = [account.operational_status?.reason?.trim()].filter((value): value is string => Boolean(value));
  if (!runtime) return parts.join("\n") || "CPA 原生凭据状态正常";
  if (runtime.error_count) {
    parts.push(`近 1h ${runtime.error_count} 次错误${runtime.rate_429_count ? `，其中 429 × ${runtime.rate_429_count}` : ""}`);
  }
  if (runtime.affected_users > 0) parts.push(`影响 ${runtime.affected_users} 位用户`);
  if (runtime.last_error_status > 0) {
    parts.push(`最近 HTTP ${runtime.last_error_status}`);
    parts.push(`最近错误时间：${formatSiteTimestamp(runtime.last_error_at)}`);
  }
  if (runtime.error_log_status === "ok") parts.push(`原生错误文件 ${runtime.error_log_files} 个`);
  return [...new Set(parts)].join("\n") || "CPA 原生凭据状态正常";
}

function RebalanceSummary({ result }: { result: RebalanceResponse }) {
  const destinations = Object.entries(result.rebalance.destinations);
  return (
    <Space orientation="vertical" size={4}>
      <Text>迁移用户：{result.rebalance.moved_users}</Text>
      {destinations.length ? <Text>迁入分布：{destinations.map(([account, count]) => `${account} ${count}`).join("，")}</Text> : null}
      {result.rebalance.snapshot_generation ? (
        <Text type="secondary"><SafetyCertificateOutlined aria-hidden="true" /> 鉴权快照 {result.rebalance.snapshot_generation.slice(0, 12)}</Text>
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
	credential_unavailable: "凭据不可用",
	transient_cooldown: "临时冷却",
	rate_limited: "限流中",
	degraded: "近期异常",
	runtime_unknown: "原生状态未知",
  quota_stale: "额度数据过期",
  quota_unavailable: "额度状态未知",
  reserve_reached: "达到安全余量",
  oauth_missing: "OAuth 未配置",
  container_not_running: "CPA 容器未运行",
  upstream_disallowed: "上游暂不可用",
  account_disabled: "已停用"
};

const runtimeStateReasons = new Set([
  "credential_unavailable",
  "transient_cooldown",
  "rate_limited",
  "degraded",
  "runtime_unknown"
]);

const proxyModeLabel: Record<string, string> = {
  inherit: "继承默认",
  custom: "独立代理",
  direct: "直连"
};

const runtimeActionLabel: Record<AccountRuntimeAction, string> = {
  start: "启动",
  stop: "停止",
  restart: "重启"
};
