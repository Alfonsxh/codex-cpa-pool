import "../i18n/admin";
import { t, getIntlLocale } from "../i18n";
import { useSiteTimezone, formatSiteTimestamp, getSiteTimezone } from "./site-time";
import { Alert, Button, Empty, Result, Skeleton, Spin, Typography } from "antd";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  lazy,
  Suspense,
  useEffect,
  useMemo,
  useState,
  type ReactNode
} from "react";

import {
  overviewCatalogQueryKey,
  overviewStatusQueryKey,
  overviewSummaryQueryKey,
  readOverviewCatalog,
  readOverviewStatus,
  readOverviewSummary,
  readOverviewUsage,
  type OverviewUsageOptions,
  type OverviewUsageWindow,
  type TokenSeries
} from "../api/overview";
import { listRuntimeJobs, runtimeJobsQueryKey, type RuntimeJob } from "../api/runtime";
import { onboardingQueryKey, readOnboarding } from "../api/onboarding";
import { useAdminToolbar } from "./AdminToolbarContext";
import { AccountQuotaOverview } from "./AccountQuotaOverview";
import {
  CustomUsageRangeModal,
  type CustomUsageRange
} from "./components/CustomUsageRangeModal";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { LegacyUsageMultiSelect } from "./components/LegacyUsageMultiSelect";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { OverviewTokenValue } from "./components/OverviewTokenValue";
import { formatTokens } from "./formatters";
import { recentUsageWindows, UsageTimeRangeControl } from "./components/UsageTimeRangeControl";
import { OnboardingCard } from "./OnboardingCard";
import { WeeklyUsageExport } from "./components/WeeklyUsageExport";

const { Text } = Typography;

type SeriesSortKey = "name" | "status" | "current" | "average" | "maximum" | "total";

type SortState = {
  key: SeriesSortKey;
  direction: "asc" | "desc";
};

type TokenMode = "unweighted" | "weighted";
type UsageSeriesView = "aggregate" | "account" | "user";

const standardWindows: Array<{ value: Exclude<OverviewUsageWindow, "custom">; label: string }> = [
  ...recentUsageWindows,
  { value: "since_reset", label: t("admin.quota_cycle") }
];

const chartColors = [
  "#6374d8", "#4b8ccf", "#c58a34", "#9070c5", "#5263aa",
  "#c45757", "#d16f4f", "#b96894", "#447a9d", "#8b6d48"
];

const EChartsUsageChart = lazy(() => import("./components/UsageChart").then((module) => ({
  default: module.UsageChart
})));

export function OverviewPage() {
  const siteTimezone = useSiteTimezone();
  const queryClient = useQueryClient();
  const [usageWindow, setUsageWindow] = useState<OverviewUsageWindow>("today");
  const [selectedAccounts, setSelectedAccounts] = useState<string[]>([]);
  const [selectedUsers, setSelectedUsers] = useState<string[]>([]);
  const [userLimit, setUserLimit] = useState(10);
  const [refreshSeconds, setRefreshSeconds] = useState(30);
  const [tokenMode, setTokenMode] = useState<TokenMode>("unweighted");
  const [usageView, setUsageView] = useState<UsageSeriesView>("aggregate");
  const [accountOptions, setAccountOptions] = useState<string[]>([]);
  const [userOptions, setUserOptions] = useState<string[]>([]);
  const [customRange, setCustomRange] = useState<CustomUsageRange | null>(null);
  const [customOpen, setCustomOpen] = useState(false);
  const { setRefreshing, setRefreshLabel, setRefreshAction } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();

  const overview = useQuery({
    queryKey: overviewSummaryQueryKey,
    queryFn: ({ signal }) => readOverviewSummary(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const catalog = useQuery({
    queryKey: overviewCatalogQueryKey,
    queryFn: ({ signal }) => readOverviewCatalog(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const status = useQuery({
    queryKey: overviewStatusQueryKey,
    queryFn: ({ signal }) => readOverviewStatus(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const usageOptions = useMemo<OverviewUsageOptions>(() => ({
    window: usageWindow,
    accounts: selectedAccounts,
    users: selectedUsers,
    userLimit,
    tokenMode,
    startAt: usageWindow === "custom" ? customRange?.startAt : undefined,
    endAt: usageWindow === "custom" ? customRange?.endAt : undefined
  }), [customRange, selectedAccounts, selectedUsers, tokenMode, usageWindow, userLimit]);
  const usageQueryKey = useMemo(() => [
      "overview-usage",
      usageWindow,
      selectedAccounts,
      selectedUsers,
      userLimit,
      tokenMode,
      customRange?.startAt,
      customRange?.endAt
    ] as const, [customRange?.endAt, customRange?.startAt, selectedAccounts, selectedUsers, tokenMode, usageWindow, userLimit]);
  const usage = useQuery({
    queryKey: usageQueryKey,
    queryFn: ({ signal }) => readOverviewUsage(usageOptions, signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    placeholderData: keepPreviousData,
    refetchInterval: refreshSeconds > 0 ? refreshSeconds * 1000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false
  });
  const usageRefresh = useMutation({
    mutationFn: () => readOverviewUsage({ ...usageOptions, fresh: true }),
    onSuccess: (payload) => queryClient.setQueryData(usageQueryKey, payload)
  });
  const jobs = useQuery({
    queryKey: runtimeJobsQueryKey,
    queryFn: ({ signal }) => listRuntimeJobs(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const onboarding = useQuery({
    queryKey: onboardingQueryKey,
    queryFn: ({ signal }) => readOnboarding(signal),
    staleTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false
  });

  useEffect(() => {
    setRefreshAction(async () => {
      const [nextOverview, nextStatus, nextCatalog, nextUsage, nextJobs] = await Promise.all([
        readOverviewSummary(),
        readOverviewStatus(),
        readOverviewCatalog(),
        readOverviewUsage({ ...usageOptions, fresh: true }),
        listRuntimeJobs()
      ]);
      queryClient.setQueryData(overviewSummaryQueryKey, nextOverview);
      queryClient.setQueryData(overviewStatusQueryKey, nextStatus);
      queryClient.setQueryData(overviewCatalogQueryKey, nextCatalog);
      queryClient.setQueryData(usageQueryKey, nextUsage);
      queryClient.setQueryData(runtimeJobsQueryKey, nextJobs);
      showToast(t("admin.overview_and_token_trends_refreshed"));
    });
    return () => setRefreshAction(null);
  }, [queryClient, setRefreshAction, showToast, usageOptions, usageQueryKey]);

  useEffect(() => {
    if (catalog.data) {
      const accounts = catalog.data.accounts.map((account) => account.id);
      const users = catalog.data.users.map((user) => user.email);
      setAccountOptions(accounts);
      setUserOptions(users);
      const availableAccounts = new Set(accounts);
      const availableUsers = new Set(users);
      setSelectedAccounts((current) => current.filter((value) => availableAccounts.has(value)));
      setSelectedUsers((current) => current.filter((value) => availableUsers.has(value)));
      return;
    }
    if (!usage.data) return;
    setAccountOptions((current) => mergeOptions(current, usage.data.accounts.map((series) => series.name)));
    setUserOptions((current) => mergeOptions(current, usage.data.users.map((series) => series.name)));
  }, [catalog.data, usage.data]);
  const refreshing = overview.isFetching || status.isFetching || catalog.isFetching || usage.isFetching || usageRefresh.isPending || jobs.isFetching;
  useEffect(() => setRefreshing(refreshing), [refreshing, setRefreshing]);
  useEffect(() => {
    const generatedAt = Math.max(overview.data?.generated_at ?? 0, status.data?.generated_at ?? 0, usage.data?.generated_at ?? 0);
    if (generatedAt > 0) setRefreshLabel(t("admin.updated", [formatSiteTimestamp(generatedAt)]));
  }, [overview.data?.generated_at, setRefreshLabel, siteTimezone, status.data?.generated_at, usage.data?.generated_at]);
  useEffect(() => () => {
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);

  if (overview.isPending || status.isPending) {
    return (
      <section className="page-content overview-legacy-page" aria-label={t("admin.loading_overview")}>
        <div className="overview-legacy-metrics overview-legacy-metrics-loading">
          {Array.from({ length: 6 }, (_, index) => <Skeleton.Node key={index} active />)}
        </div>
        <Skeleton active paragraph={{ rows: 10 }} />
      </section>
    );
  }
  if (overview.isError || status.isError) {
    const loadError = overview.error ?? status.error;
    return (
      <section className="page-content">
        <Result
          status="warning"
          title={t("admin.unable_to_load_overview_data")}
          subTitle={loadError instanceof Error ? loadError.message : t("common.please_try_again_later")}
          extra={<Button type="primary" onClick={() => void Promise.all([overview.refetch(), status.refetch()])}>{t("common.reload")}</Button>}
        />
      </section>
    );
  }

  const summary = overview.data.summary;
  const usageBoundaryUpdating = usage.isFetching && (usage.isPlaceholderData || !usage.data);
  return (
    <section className="page-content overview-legacy-page">
      {onboarding.data ? <OnboardingCard status={onboarding.data} /> : null}
      {summary.incomplete_key_matrices > 0 ? (
        <Alert
          className="page-alert"
          type="warning"
          showIcon
          title={t("admin.users_have_incomplete_unified_key_account_coverage", [summary.incomplete_key_matrices])}
          description={t("admin.these_users_cannot_move_across_accounts_load_balancing_rejects_the")}
        />
      ) : null}
      {status.data.warnings.length ? (
        <Alert
          className="page-alert"
          type="warning"
          showIcon
          title={t("admin.some_runtime_statuses_are_unavailable")}
          description={status.data.warnings.join("；")}
        />
      ) : null}

      <div className="overview-legacy-metrics" aria-label={t("admin.key_metrics")}>
        <Metric label={t("common.cpa_account")} value={summary.accounts} detail={t("admin.enabled_authorized", [summary.enabled_accounts, status.data.authorized_accounts])} />
        <Metric label={t("admin.user_status")} value={`${summary.active_users}/${summary.users}`} detail={t("admin.routed", [summary.routed_users])} />
        <Metric label={t("admin.key_health")} value={summary.active_keys} detail={t("admin.coverage_issues", [summary.incomplete_key_matrices])} />
        <Metric label={t("admin.team_coverage")} value={summary.teams} detail={t("admin.unassigned", [summary.unassigned_users])} />
        <Metric label={t("admin.service_status")} value={`${status.data.running_services}/${status.data.total_services}`} detail={t("admin.compose_services")} />
        <Metric label={t("admin.requests_5_min")} value={status.data.requests_5m} detail={t("admin.gateway_access_log")} />
      </div>

      <AccountQuotaOverview quota={status.data.account_quota} />

      <section className="overview-legacy-monitor overview-token-monitor-card" aria-labelledby="overview-token-monitor-title">
        <div className="overview-legacy-toolbar overview-token-monitor-toolbar">
          <div className="overview-token-heading-row">
            <div className="overview-legacy-toolbar-title usage-monitor-title">
              <h3 id="overview-token-monitor-title">{t("admin.token_usage_2")}</h3>
              <p className="section-kicker">TOKEN MONITOR</p>
            </div>
            <div className="overview-token-heading-actions">
              <div className="overview-collector-meta" aria-live="polite">
                <span className={`overview-collector-state ${collectorState(usage.data?.collector.status).tone}`}>
                  {usage.isPending ? t("admin.loading") : collectorState(usage.data?.collector.status).label}
                </span>
                <time aria-label={t("admin.last_collection")}>
                  {usage.data?.collector.heartbeat_at
                    ? formatSiteTimestamp(usage.data.collector.heartbeat_at, getSiteTimezone())
                    : "—"}
                </time>
              </div>
              <WeeklyUsageExport onDownloaded={() => showToast(t("admin.weekly_report_generated_download_started"))} />
            </div>
          </div>
          <div className="overview-legacy-filters usage-monitor-filters" aria-label={t("admin.token_dashboard_filters")}>
            <div className="overview-token-window-row">
              <UsageTimeRangeControl
                label={t("admin.token_usage_time_range")}
                value={usageWindow}
                options={standardWindows}
                onChange={setUsageWindow}
                onCustomSelect={() => setCustomOpen(true)}
              />
              <div className="overview-token-window-boundaries" aria-label={t("admin.token_usage_time_boundaries")} aria-live="polite" aria-busy={usageBoundaryUpdating}>
                <UsageTimeBoundary
                  label={t("common.start_time")}
                  value={usage.data ? formatOverviewUsageBoundary(usage.data.window_start_at, getSiteTimezone()) : "—"}
                  updating={usageBoundaryUpdating}
                />
                <UsageTimeBoundary
                  label={t("common.end_time")}
                  value={usage.data
                    ? formatOverviewUsageBoundary(
                        usageWindow === "custom" && customRange ? customRange.endAt : usage.data.generated_at,
                        getSiteTimezone()
                      )
                    : "—"}
                  updating={usageBoundaryUpdating}
                />
              </div>
            </div>
            <div className="overview-token-scope-filters">
              <fieldset className="overview-token-mode-control">
                <legend>{t("common.token_metric")}</legend>
                <div className="overview-token-mode-segments" role="group" aria-label={t("admin.token_accounting_metric")}>
                  <button
                    type="button"
                    className="unweighted"
                    aria-pressed={tokenMode === "unweighted"}
                    onClick={() => setTokenMode("unweighted")}
                  ><i aria-hidden="true" />{t("common.unweighted_2")}</button>
                  <button
                    type="button"
                    className="weighted"
                    aria-pressed={tokenMode === "weighted"}
                    onClick={() => setTokenMode("weighted")}
                  ><i aria-hidden="true" />{t("common.weighted_2")}</button>
                </div>
              </fieldset>
              <LegacyUsageMultiSelect
                id="overview-usage-account-react"
                label="CPA"
                allLabel={t("common.all_cpas")}
                searchPlaceholder={t("admin.search_cpas")}
                value={selectedAccounts}
                options={accountOptions.map((account) => ({ value: account, label: account }))}
                loading={catalog.isPending}
                error={catalog.isError}
                onChange={setSelectedAccounts}
              />
              <LegacyUsageMultiSelect
                id="overview-usage-user-react"
                label={t("common.user")}
                allLabel={t("admin.all_users_2")}
                searchPlaceholder={t("admin.search_user_emails")}
                value={selectedUsers}
                options={userOptions.map((user) => ({ value: user, label: user }))}
                loading={catalog.isPending}
                error={catalog.isError}
                onChange={setSelectedUsers}
              />
              <div className="overview-legacy-refresh-controls usage-refresh-controls">
                <span className="overview-refresh-label">{t("admin.auto_refresh")}</span>
                <div className="overview-refresh-actions">
                  <div className="overview-legacy-filter usage-variable-select usage-refresh-control">
                    <span className="sr-only">{t("admin.refresh_interval")}</span>
                    <LegacyEnhancedSelect
                      label={t("admin.auto_refresh")}
                      value={String(refreshSeconds)}
                      options={[
                        { value: "0", label: t("common.close") },
                        { value: "10", label: t("admin.10_seconds") },
                        { value: "30", label: t("admin.30_seconds") },
                        { value: "60", label: t("admin.1_minute") },
                        { value: "300", label: t("admin.5_minutes") }
                      ]}
                      onChange={(nextValue) => setRefreshSeconds(Number(nextValue))}
                    />
                  </div>
                  <button
                    type="button"
                    className="button ghost usage-monitor-refresh overview-legacy-refresh-button"
                    disabled={usage.isFetching || usageRefresh.isPending}
                    aria-label={t("admin.refresh_token_dashboard")}
                    onClick={() => usageRefresh.mutate()}
                  >
                    <span aria-hidden="true">↻</span><span>{t("common.refresh")}</span>
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        {usage.isError || usageRefresh.isError ? (
          <Alert
            type="error"
            showIcon
            title={t("admin.unable_to_load_token_dashboard")}
            description={(usageRefresh.error ?? usage.error) instanceof Error
              ? (usageRefresh.error ?? usage.error as Error).message
              : t("common.please_try_again_later")}
            action={<Button size="small" onClick={() => usageRefresh.mutate()}>{t("common.retry")}</Button>}
          />
        ) : null}
        {usage.data?.unavailable_accounts.length ? (
          <Alert
            type="warning"
            showIcon
            title={t("admin.accounts_have_no_quota_period_start", [usage.data.unavailable_accounts.length])}
            description={t("admin.quota_period_trends_include_only_accounts_with_a_valid_period")}
          />
        ) : null}

        {usage.isPending ? (
          <div className="overview-legacy-loading"><Spin /><span>{t("admin.loading_token_buckets_for_the_selected_range")}</span></div>
        ) : usage.data ? (
          <UsageDashboard
            payload={usage.data}
            accountStatuses={new Map(catalog.data?.accounts.map((account) => [account.id, {
              label: account.operational_status.label,
              tone: account.operational_status.tone
            }]))}
            userStatuses={new Map(catalog.data?.users.map((user) => [user.email, user.status === "active"
              ? { label: t("admin.active"), tone: "success" }
              : { label: t("admin.disable"), tone: "neutral" }]))}
            tokenMode={tokenMode}
            view={usageView}
            onViewChange={setUsageView}
            canLoadMoreUsers={selectedUsers.length === 0
              && userLimit < Math.min(500, userOptions.length)
              && usage.data.users.length >= usage.data.user_limit}
            onLoadMoreUsers={() => setUserLimit((current) => Math.min(500, userOptions.length, current + 10))}
          />
        ) : null}
      </section>

      <RecentJobs jobs={jobs.data?.jobs ?? []} pending={jobs.isPending} error={jobs.error} />

      <CustomUsageRangeModal
        open={customOpen}
        title={t("common.custom")}
        range={customRange}
        timezone={getSiteTimezone()}
        onCancel={() => setCustomOpen(false)}
        onApply={(range) => {
          setCustomRange(range);
          setUsageWindow("custom");
          setCustomOpen(false);
        }}
      />
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function UsageTimeBoundary({ label, value, updating }: {
  label: string;
  value: string;
  updating: boolean;
}) {
  return (
    <span className="overview-token-window-value">
      <small>{label}</small>
      <strong>{updating ? "…" : value}</strong>
    </span>
  );
}

function Metric({ label, value, detail }: { label: string; value: string | number; detail: string }) {
  return (
    <article className="overview-legacy-metric">
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </article>
  );
}

function UsageDashboard({
  payload,
  accountStatuses,
  userStatuses,
  tokenMode,
  view,
  onViewChange,
  canLoadMoreUsers,
  onLoadMoreUsers
}: {
  payload: Awaited<ReturnType<typeof readOverviewUsage>>;
  accountStatuses: Map<string, SeriesStatus>;
  userStatuses: Map<string, SeriesStatus>;
  tokenMode: TokenMode;
  view: UsageSeriesView;
  onViewChange: (view: UsageSeriesView) => void;
  canLoadMoreUsers: boolean;
  onLoadMoreUsers: () => void;
}) {
  const aggregate = aggregateTokenSeries(payload.buckets, payload.accounts);
  const interval = formatBucketInterval(payload.bucket_seconds);
  const baseSeries = view === "aggregate"
    ? payload.accounts.length ? [aggregate] : []
    : view === "account" ? payload.accounts : payload.users;
  const selectedSeries = tokenMode === "weighted" ? baseSeries.map(asWeightedSeries) : baseSeries;
  const chartSeries = view === "aggregate" ? selectedSeries : topTokenSeries(selectedSeries, 10);
  const metrics = tokenMode === "weighted" ? asWeightedSeries(aggregate) : aggregate;
  const modeLabel = tokenMode === "weighted" ? t("common.weighted_2") : t("common.unweighted_2");
  const viewLabel = view === "aggregate" ? t("common.all_accounts") : view === "account" ? t("common.cpa_account") : t("common.user");
  const chartAriaDetails = view === "aggregate"
    ? t("admin.current_range_total_average_maximum", [formatTokens(metrics.current), formatTokens(metrics.total), formatTokens(metrics.average), formatTokens(metrics.maximum)])
    : chartSeries.map((item) => `${item.name} ${formatTokens(item.total)}`).join("，");
  const activeViewTabID = `overview-token-tab-${view}`;
  const emptyText = view === "user" ? t("admin.no_user_token_data_in_the_selected_range") : t("admin.no_account_token_data_in_the_selected_range");
  const chartFooter = (
    <footer className="overview-legacy-summary-footer overview-token-workspace-footer">
      <span>{t("admin.unit_2")}{modeLabel} Token / {interval}</span>
    </footer>
  );
  return (
    <article className="overview-legacy-usage-panel overview-token-workspace">
      <header className="overview-token-workspace-header">
        <div className="overview-token-view-region">
          <div className="overview-token-view-switch" role="tablist" aria-label={t("admin.token_usage_view")}>
            <button id="overview-token-tab-aggregate" type="button" role="tab" aria-selected={view === "aggregate"} aria-controls="overview-token-series" onClick={() => onViewChange("aggregate")}>{t("common.all_accounts")}</button>
            <button id="overview-token-tab-account" type="button" role="tab" aria-selected={view === "account"} aria-controls="overview-token-series" onClick={() => onViewChange("account")}>{t("admin.cpa_account_token_statistics")}</button>
            <button id="overview-token-tab-user" type="button" role="tab" aria-selected={view === "user"} aria-controls="overview-token-series" onClick={() => onViewChange("user")}>{t("admin.user_token_statistics")}</button>
          </div>
        </div>
      </header>

      <section
        id="overview-token-series"
        className="overview-token-data-scroll"
        role="tabpanel"
        aria-labelledby={activeViewTabID}
        aria-label={t("admin.token_trend_details", [viewLabel])}
        tabIndex={0}
      >
        {chartSeries.length ? (
          <UsageChartLoader
            buckets={payload.buckets}
            series={chartSeries}
            summary={view === "aggregate"}
            valueLabel={modeLabel}
            timezone={payload.window_timezone}
            ariaLabel={t("admin.token_usage_trend", [viewLabel, modeLabel, chartAriaDetails])}
            footer={chartFooter}
          />
        ) : (
          <>
            <div className="overview-legacy-chart-empty"><strong>{t("admin.no_trend_data")}</strong><span>{emptyText}</span></div>
            {chartFooter}
          </>
        )}
        {view !== "aggregate" ? <SeriesTable
          subjectLabel={view === "user" ? t("common.user") : "CPA"}
          series={view === "user" ? payload.users : payload.accounts}
          emptyText={emptyText}
          statuses={view === "user" ? userStatuses : accountStatuses}
          tokenMode={tokenMode}
          canLoadMore={view === "user" && canLoadMoreUsers}
          onLoadMore={view === "user" ? onLoadMoreUsers : undefined}
          resetKey={`${view}:${tokenMode}:${payload.window_start_at}:${payload.selected_accounts.join(",")}:${payload.selected_users.join(",")}`}
        /> : null}
      </section>
    </article>
  );
}

function UsageChartLoader({ buckets, series, summary = false, valueLabel, timezone, ariaLabel, footer }: {
  buckets: number[];
  series: TokenSeries[];
  summary?: boolean;
  valueLabel: string;
  timezone: string;
  ariaLabel: string;
  footer: ReactNode;
}) {
  return (
    <Suspense fallback={(
      <>
        <div className={`overview-legacy-chart overview-legacy-chart-loading${summary ? " summary" : ""}`} role="status">
          <Spin size="small" /><span>{t("admin.loading_trend_chart")}</span>
        </div>
        {footer}
      </>
    )}>
      <EChartsUsageChart
        buckets={buckets}
        series={series}
        summary={summary}
        valueLabel={valueLabel}
        timezone={timezone}
        ariaLabel={ariaLabel}
        footer={footer}
      />
    </Suspense>
  );
}

function SeriesTable({ subjectLabel, series, emptyText, statuses, tokenMode, canLoadMore, onLoadMore, resetKey }: {
  subjectLabel: string;
  series: TokenSeries[];
  emptyText: string;
  statuses: Map<string, SeriesStatus>;
  tokenMode: TokenMode;
  canLoadMore: boolean;
  onLoadMore?: () => void;
  resetKey: string;
}) {
  const [sort, setSort] = useState<SortState>({ key: "total", direction: "desc" });
  const [visibleRows, setVisibleRows] = useState(10);
  useEffect(() => setVisibleRows(10), [resetKey]);
  const sorted = useMemo(() => [...series].sort((left, right) => {
    const leftValue = seriesSortValue(left, sort.key, statuses, tokenMode);
    const rightValue = seriesSortValue(right, sort.key, statuses, tokenMode);
    const comparison = typeof leftValue === "number" && typeof rightValue === "number"
      ? leftValue - rightValue
      : String(leftValue).localeCompare(String(rightValue), getIntlLocale());
    const directed = sort.direction === "asc" ? comparison : -comparison;
    return directed || left.name.localeCompare(right.name, getIntlLocale());
  }), [series, sort, statuses, tokenMode]);
  const colorByName = useMemo(() => new Map(
    topTokenSeries(tokenMode === "weighted" ? series.map(asWeightedSeries) : series, series.length)
      .map((item, index) => [item.name, chartColors[index % chartColors.length]])
  ), [series, tokenMode]);
  const updateSort = (key: SeriesSortKey) => setSort((current) => ({
    key,
    direction: current.key === key
      ? current.direction === "desc" ? "asc" : "desc"
      : key === "name" || key === "status" ? "asc" : "desc"
  }));
  const loadNextPage = () => {
    const nextVisibleRows = visibleRows + 10;
    setVisibleRows(nextVisibleRows);
    if (nextVisibleRows > sorted.length && canLoadMore) onLoadMore?.();
  };
  return (
    <NativeTableViewport
      className="overview-legacy-table-wrap overview-token-detail-table"
      aria-label={t("admin.usage_details_table", [subjectLabel])}
      onScroll={(event) => {
        const viewport = event.currentTarget;
        if (viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - 48
          && (visibleRows < sorted.length || canLoadMore)) loadNextPage();
      }}
    >
      <table className="overview-legacy-table">
        <thead>
          <tr>
            <SeriesTableHeader label={subjectLabel} sortKey="name" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label={t("admin.status_2")} sortKey="status" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label={t("common.current")} sortKey="current" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label={t("common.average")} sortKey="average" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label={t("common.maximum")} sortKey="maximum" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label={t("common.range_total")} sortKey="total" sort={sort} onSort={updateSort} />
          </tr>
        </thead>
        <tbody>
          {sorted.length ? sorted.slice(0, visibleRows).map((item) => {
            const status = seriesStatus(item, statuses);
            return (
              <tr key={item.name}>
                <td><span className="overview-series-name"><i style={{ background: colorByName.get(item.name) ?? chartColors[0] }} /><strong>{item.name}</strong></span></td>
                <td><span className={`overview-status-chip ${status.tone}`}>{status.label}</span></td>
                <td className="overview-token-number-cell"><SeriesTokenValue series={item} metric="current" tokenMode={tokenMode} /></td>
                <td className="overview-token-number-cell"><SeriesTokenValue series={item} metric="average" tokenMode={tokenMode} /></td>
                <td className="overview-token-number-cell"><SeriesTokenValue series={item} metric="maximum" tokenMode={tokenMode} /></td>
                <td className="overview-token-number-cell"><SeriesTokenValue series={item} metric="total" tokenMode={tokenMode} /></td>
              </tr>
            );
          }) : (
            <tr><td className="overview-legacy-table-empty" colSpan={6}>{emptyText}</td></tr>
          )}
        </tbody>
      </table>
      {(visibleRows < sorted.length || canLoadMore) ? <button className="overview-token-load-more" type="button" onClick={loadNextPage} aria-label={t("admin.load_more_usage_details", [subjectLabel])}>{t("admin.load_more")}</button> : null}
    </NativeTableViewport>
  );
}

function SeriesTableHeader({ label, sortKey, sort, onSort }: {
  label: string;
  sortKey: SeriesSortKey;
  sort: SortState;
  onSort: (key: SeriesSortKey) => void;
}) {
  const active = sort.key === sortKey;
  const ariaSort = active ? (sort.direction === "asc" ? "ascending" : "descending") : "none";
  return (
    <th scope="col" aria-sort={ariaSort} className={sortKey !== "name" && sortKey !== "status" ? "overview-token-number-cell" : undefined}>
      <SortButton label={label} sortKey={sortKey} sort={sort} onSort={onSort} />
    </th>
  );
}

function SortButton({ label, sortKey, sort, onSort }: {
  label: string;
  sortKey: SeriesSortKey;
  sort: SortState;
  onSort: (key: SeriesSortKey) => void;
}) {
  const active = sort.key === sortKey;
  const ariaLabel = active
    ? t("admin.currently_click_to_reverse_the_sort_order", [label, sort.direction === "asc" ? t("common.ascending") : t("common.descending")])
    : t("admin.click_to_sort", [label]);
  return (
    <button
      type="button"
      className={`sort-button${active ? " active" : ""}`}
      data-monitor-sort={sortKey}
      data-direction={active ? sort.direction : undefined}
      aria-label={ariaLabel}
      onClick={() => onSort(sortKey)}
    >{label}</button>
  );
}

function SeriesTokenValue({ series, metric, tokenMode }: {
  series: TokenSeries;
  metric: Exclude<SeriesSortKey, "name" | "status">;
  tokenMode: TokenMode;
}) {
  const value = tokenMode === "weighted" ? weightedMetric(series, metric) : series[metric];
  return <OverviewTokenValue value={value} />;
}

function RecentJobs({ jobs, pending, error }: { jobs: RuntimeJob[]; pending: boolean; error: unknown }) {
  return (
    <section className="overview-legacy-jobs" aria-labelledby="overview-recent-jobs-title">
      <header>
        <div><h2 id="overview-recent-jobs-title">{t("admin.recent_tasks")}</h2><span>ACTIVITY</span></div>
        <a href="/admin/runtime">{t("admin.view_all_tasks")}</a>
      </header>
      <div className="overview-legacy-panel overview-legacy-job-list">
        {pending ? <div className="overview-legacy-job-state"><Spin size="small" /> {t("admin.loading_tasks")}</div> : null}
        {error ? <Alert type="error" showIcon message={t("admin.unable_to_load_recent_tasks")} /> : null}
        {!pending && !error && jobs.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("admin.no_runtime_tasks")} /> : null}
        {jobs.slice(0, 8).map((job) => {
          const status = jobStatus(job.status);
          return (
            <div className="overview-legacy-job-row" key={job.id}>
              <strong>{job.name || actionLabel(job.action)}</strong>
              <span>{job.target}</span>
              <time>{formatSiteTimestamp(job.created_at)}</time>
              <span className={`overview-status-chip ${status.tone}`}>{status.label}</span>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function aggregateTokenSeries(buckets: number[], series: TokenSeries[]): TokenSeries {
  const values = buckets.map((_, index) => series.reduce((sum, item) => sum + (item.values[index] ?? 0), 0));
  const weightedValues = buckets.map((_, index) => series.reduce((sum, item) => (
    sum + (item.weighted_values?.[index] ?? item.values[index] ?? 0)
  ), 0));
  const total = values.reduce((sum, value) => sum + value, 0);
  const weightedTotal = weightedValues.reduce((sum, value) => sum + value, 0);
  return {
    name: t("admin.all_accounts_combined"),
    values,
    current: values.at(-1) ?? 0,
    average: values.length ? Math.round(total / values.length) : 0,
    maximum: Math.max(...values, 0),
    total,
    weighted_values: weightedValues,
    weighted_current: weightedValues.at(-1) ?? 0,
    weighted_average: weightedValues.length ? Math.round(weightedTotal / weightedValues.length) : 0,
    weighted_maximum: Math.max(...weightedValues, 0),
    weighted_total: weightedTotal
  };
}

function asWeightedSeries(series: TokenSeries): TokenSeries {
  return {
    ...series,
    values: series.weighted_values ?? series.values,
    current: series.weighted_current ?? series.current,
    average: series.weighted_average ?? series.average,
    maximum: series.weighted_maximum ?? series.maximum,
    total: series.weighted_total ?? series.total
  };
}

function topTokenSeries(series: TokenSeries[], limit: number) {
  return [...series]
    .sort((left, right) => right.total - left.total || left.name.localeCompare(right.name, getIntlLocale()))
    .slice(0, limit);
}

function seriesSortValue(
  series: TokenSeries,
  key: SeriesSortKey,
  statuses: Map<string, SeriesStatus>,
  tokenMode: TokenMode
) {
  if (key === "status") return seriesStatusRank(seriesStatus(series, statuses));
  if (key === "name") return series.name;
  return tokenMode === "weighted" ? weightedMetric(series, key) : series[key];
}

function weightedMetric(series: TokenSeries, metric: Exclude<SeriesSortKey, "name" | "status">) {
  if (metric === "current") return series.weighted_current ?? series.current;
  if (metric === "average") return series.weighted_average ?? series.average;
  if (metric === "maximum") return series.weighted_maximum ?? series.maximum;
  return series.weighted_total ?? series.total;
}

function mergeOptions(current: string[], incoming: string[]) {
  const next = Array.from(new Set([...current, ...incoming]));
  return next.length === current.length && next.every((value, index) => value === current[index]) ? current : next;
}

type SeriesStatus = { label: string; tone: string };

function seriesStatus(series: TokenSeries, statuses: Map<string, SeriesStatus>): SeriesStatus {
  return statuses.get(series.name) ?? { label: t("common.unknown_status"), tone: "neutral" };
}

function seriesStatusRank(status: SeriesStatus) {
  return ({ success: 0, warning: 1, danger: 2, neutral: 3 } as Record<string, number>)[status.tone] ?? 9;
}

function collectorState(status?: string) {
  if (["ok", "healthy"].includes(status ?? "")) return { label: t("admin.collection_healthy"), tone: "success" };
  if (["starting", "degraded"].includes(status ?? "")) return { label: status === "starting" ? t("admin.collector_starting") : t("admin.collection_degraded"), tone: "warning" };
  if (!status) return { label: t("admin.waiting_for_collection"), tone: "neutral" };
  return { label: t("admin.collection_error"), tone: "danger" };
}

function jobStatus(status: RuntimeJob["status"]) {
  const labels: Record<RuntimeJob["status"], { label: string; tone: string }> = {
    queued: { label: t("admin.queued"), tone: "neutral" },
    running: { label: t("admin.running_2"), tone: "warning" },
    cancelling: { label: t("admin.cancelling"), tone: "warning" },
    succeeded: { label: t("common.succeeded"), tone: "success" },
    failed: { label: t("common.failed"), tone: "danger" },
    cancelled: { label: t("common.cancelled"), tone: "neutral" }
  };
  return labels[status];
}

function actionLabel(action: RuntimeJob["action"]) {
  const labels: Record<RuntimeJob["action"], string> = {
    start: t("admin.start_service"),
    stop: t("admin.stop_service"),
    restart: t("admin.restart_service"),
    login: t("admin.oauth_authorization"),
    "image-pull": t("admin.pull_image"),
    "image-update": t("admin.update_cpa_image")
  };
  return labels[action];
}

export function formatOverviewUsageRange(startAt: number, endAt: number, timezone = getSiteTimezone()) {
  if (!Number.isFinite(startAt) || !Number.isFinite(endAt) || startAt <= 0 || endAt < startAt) return t("admin.usage_boundaries_unavailable");
  return `${formatOverviewUsageBoundary(startAt, timezone)} — ${formatOverviewUsageBoundary(endAt, timezone)}`;
}

export function formatOverviewUsageBoundary(timestamp: number, timezone = getSiteTimezone()) {
  return formatSiteTimestamp(timestamp, timezone);
}

function formatBucketInterval(seconds: number) {
  if (seconds < 60) return t("admin.sec", [seconds]);
  if (seconds < 3600) return t("admin.min", [Math.round(seconds / 60)]);
  if (seconds < 86400) return t("admin.hours", [Math.round(seconds / 3600)]);
  return t("admin.days", [Math.round(seconds / 86400)]);
}
