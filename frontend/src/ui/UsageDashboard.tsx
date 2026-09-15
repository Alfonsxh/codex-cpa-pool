import "../i18n/usage";
import { t, getIntlLocale } from "../i18n";
import { useSiteTimezone, formatSiteTimestamp } from "./site-time";
import { Alert, App as AntApp, Button, Form, Input, Modal, Skeleton, Space, Tabs, Tooltip } from "antd";
import { CopyOutlined, QuestionCircleOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { flushSync } from "react-dom";

import { accountListRefreshOptions, refreshAccountList } from "../api/account-refresh";
import { ApiError } from "../api/client";
import {
  autoAssignPortalAccount,
  portalAccountsQueryKey,
  portalAccountsQueryRoot,
  portalBreakdownQueryKey,
  portalBreakdownQueryRoot,
  portalProfileQueryKey,
  portalQuotaQueryKey,
  portalRouteQueryKey,
  readPortalAccounts,
  readPortalBreakdown,
  readPortalKey,
  readPortalProfile,
  readPortalQuota,
  readPortalRoute,
  rotatePortalKey,
  switchPortalAccount,
  type PortalAccount,
  type PortalQuota,
  type PortalUsageTrendWindow,
  type PortalUsageWindow
} from "../api/portal";
import type { UsageBreakdown, UsageCombination, UsageMetrics } from "../api/generated";
import { PortalClientConfigModal, type PortalClientConfigMode } from "./PortalClientConfigModal";
import { PortalDailyUsageTrend, type PortalTrendUpdateStatus } from "./PortalDailyUsageTrend";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { formatTokenAmount, formatTokens } from "./formatters";
import { formatUsageCombinationLabel, formatUsageModelLabel, formatUsageReasoningLabel } from "./usage-multiplier-labels";

type SortField = "current" | "account" | "quota" | "active_users" | "status" | "requests" | "tokens" | "last_used";
type SortState = { field: SortField; direction: "asc" | "desc"; pinCurrent: boolean };
type PrimarySection = "trend" | "accounts";

const defaultSort: SortState = { field: "quota", direction: "asc", pinCurrent: true };
const portalTrendWindowOptions: Array<{ value: PortalUsageTrendWindow; label: string }> = [
  { value: "7d", label: t("usage.7d") },
  { value: "30d", label: t("usage.30d") },
  { value: "90d", label: t("usage.90d") }
];

export function UsageDashboard({ user, onSessionExpired }: { user: string; onSessionExpired: () => void }) {
  useSiteTimezone();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [window, setWindow] = useState<PortalUsageWindow>("today");
  const [sort, setSort] = useState<SortState>(defaultSort);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [primarySection, setPrimarySection] = useState<PrimarySection>("accounts");
  const [trendWindow, setTrendWindow] = useState<PortalUsageTrendWindow>("30d");
  const [trendUpdateStatus, setTrendUpdateStatus] = useState<PortalTrendUpdateStatus>({ updatedAt: 0, refreshing: false, failed: false });
  const compactTabs = useMediaQuery("(max-width: 1120px)");
  const [showKey, setShowKey] = useState(false);
  const [keyOpen, setKeyOpen] = useState(false);
  const [keyValue, setKeyValue] = useState("");
  const [keyLoading, setKeyLoading] = useState(false);
  const [keyError, setKeyError] = useState("");
  const keyRequest = useRef<AbortController | null>(null);
  const autoAssignAttempted = useRef(false);
  const [rotationOpen, setRotationOpen] = useState(false);
  const [clientConfigMode, setClientConfigMode] = useState<PortalClientConfigMode | null>(null);
  const [switchTarget, setSwitchTarget] = useState<PortalAccount | null>(null);

  // Personal query caches are isolated by the authenticated user.
  const profileQueryKey = [...portalProfileQueryKey, user];
  const quotaQueryKey = [...portalQuotaQueryKey, user];
  const routeQueryKey = [...portalRouteQueryKey, user];

  const profile = useQuery({
    queryKey: profileQueryKey,
    queryFn: ({ signal }) => readPortalProfile(signal),
    staleTime: 15_000,
    gcTime: 5 * 60_000,
    refetchOnMount: true,
    refetchOnWindowFocus: true
  });
  const accounts = useQuery({
    queryKey: [...portalAccountsQueryKey(window), user],
    queryFn: ({ signal }) => readPortalAccounts(window, signal),
    ...accountListRefreshOptions
  });
  const quota = useQuery({
    queryKey: quotaQueryKey,
    queryFn: ({ signal }) => readPortalQuota(signal),
    staleTime: 15_000,
    gcTime: 5 * 60_000,
    refetchOnMount: true,
    refetchOnWindowFocus: true
  });
  const route = useQuery({
    queryKey: routeQueryKey,
    queryFn: ({ signal }) => readPortalRoute(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: true,
    refetchInterval: 10_000
  });

  useEffect(() => {
    if ([profile.error, quota.error, accounts.error, route.error].some(isUnauthorized)) onSessionExpired();
  }, [accounts.error, onSessionExpired, profile.error, quota.error, route.error]);
  useEffect(() => () => keyRequest.current?.abort(), []);
  useEffect(() => setExpanded(new Set()), [window]);

  const currentGroup = route.data?.current_group ?? accounts.data?.current_group ?? profile.data?.current_group ?? "";
  const currentAccount = accounts.data?.accounts.find((item) => item.id === currentGroup);
  const sortedAccounts = useMemo(
    () => sortAccounts(accounts.data?.accounts ?? [], currentGroup, sort),
    [accounts.data?.accounts, currentGroup, sort]
  );

  const accountSwitch = useMutation({
    mutationFn: (account: PortalAccount) => switchPortalAccount(account.id),
    onSuccess: async (result) => {
      queryClient.setQueryData(routeQueryKey, {
        current_group: result.current_group,
        generated_at: Math.floor(Date.now() / 1000)
      });
      setSwitchTarget(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: profileQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: quotaQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: portalAccountsQueryRoot })
      ]);
      void message.success(result.changed ? t("usage.account_switched_and_gateway_activation_confirmed") : t("usage.this_account_is_already_selected"));
    }
  });
  const autoAssignment = useMutation({
    mutationFn: autoAssignPortalAccount,
    onSuccess: async (result) => {
      queryClient.setQueryData(routeQueryKey, {
        current_group: result.current_group,
        generated_at: Math.floor(Date.now() / 1000)
      });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: profileQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: quotaQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: portalAccountsQueryRoot })
      ]);
      void message.success(result.changed ? t("usage.automatically_assigned", [accountLabelByID(accounts.data?.accounts ?? [], result.current_group)]) : t("usage.current_cpa_account_restored"));
    }
  });
  const rotation = useMutation({
    gcTime: 0,
    mutationFn: rotatePortalKey,
    onSuccess: (result) => {
      setKeyValue(result.api_key);
      setKeyError("");
      setShowKey(true);
      setKeyOpen(true);
      setRotationOpen(false);
      rotation.reset();
      void message.success(t("usage.api_key_refreshed_the_old_key_expired_immediately"));
    }
  });

  useEffect(() => {
    if (!route.isSuccess || !accounts.isSuccess || currentGroup || accounts.data.accounts.length === 0 || autoAssignAttempted.current) return;
    autoAssignAttempted.current = true;
    autoAssignment.mutate();
  }, [accounts.data, accounts.isSuccess, currentGroup, route.isSuccess]);

  const copyKey = async () => {
    if (!keyValue) return;
    try {
      await navigator.clipboard.writeText(keyValue);
      void message.success(t("usage.api_key_copied"));
    } catch {
      void message.error(t("usage.the_browser_blocked_copying_reveal_the_key_and_copy_it"));
    }
  };
  const revealKey = async () => {
    keyRequest.current?.abort();
    const request = new AbortController();
    keyRequest.current = request;
    setKeyValue("");
    setKeyError("");
    setShowKey(false);
    setKeyLoading(true);
    setKeyOpen(true);
    try {
      const result = await readPortalKey(request.signal);
      if (keyRequest.current === request) setKeyValue(result.api_key);
    } catch (error) {
      if (!request.signal.aborted && keyRequest.current === request) setKeyError(errorMessage(error));
    } finally {
      if (keyRequest.current === request) {
        keyRequest.current = null;
        setKeyLoading(false);
      }
    }
  };
  const closeKey = () => {
    keyRequest.current?.abort();
    keyRequest.current = null;
    flushSync(() => {
      setKeyValue("");
      setKeyError("");
      setKeyLoading(false);
      setShowKey(false);
    });
    setKeyOpen(false);
  };
  const accountRefresh = useMutation({
    mutationFn: () => refreshAccountList(
      queryClient, portalAccountsQueryRoot, [...portalAccountsQueryKey(window), user],
      (signal) => readPortalAccounts(window, signal, true)
    ),
    onError: (error) => { if (isUnauthorized(error)) onSessionExpired(); }
  });
  const refresh = () => {
    accountRefresh.mutate();
    void Promise.all([
      profile.refetch(), quota.refetch(), route.refetch(),
      queryClient.invalidateQueries({ queryKey: portalBreakdownQueryRoot })
    ]);
  };
  const toggleExpanded = (accountID: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(accountID)) next.delete(accountID);
      else next.add(accountID);
      return next;
    });
  };
  const changeSort = (field: SortField) => {
    setSort((current) => current.field === field
      ? { field, direction: current.direction === "asc" ? "desc" : "asc", pinCurrent: field === "quota" }
      : { field, direction: field === "account" || field === "status" || field === "quota" ? "asc" : "desc", pinCurrent: field === "quota" });
  };

  return (
    <section className="usage-dashboard">
      <section className="usage-key-card" aria-label={t("usage.personal_credentials_usage_summary")}>
        <div className="usage-key-panel">
          <div className="usage-key-value">
            <span>{t("common.my_api_key")}</span>
            <code aria-label={t("usage.api_key_security_status")}>{t("common.loaded_only_when_needed")}</code>
          </div>
          <div className="usage-key-actions">
            <button className="usage-secondary-button usage-credential-entry" type="button" onClick={() => void revealKey()}>{t("common.manage_api_key")}</button>
            <button className="usage-secondary-button" type="button" onClick={() => setClientConfigMode("codex")}>{t("common.configure_codex")}</button>
            <button className="usage-secondary-button" type="button" onClick={() => setClientConfigMode("claude")}>{t("common.configure_claude_code")}</button>
            <button className="usage-primary-button" type="button" onClick={() => setClientConfigMode("ccswitch")}>{t("common.import_to_cc_switch")}</button>
          </div>
        </div>

        <div className="usage-token-overview">
          <CurrentAccountSummary account={currentAccount} loading={route.isPending || accounts.isPending} />
          <PersonalQuotaSummary quota={quota.data} loading={quota.isPending} error={quota.error} onRetry={() => void quota.refetch()} />
          <RangeSummary window={window} metrics={accounts.data?.totals} loading={accounts.isPending} />
        </div>
      </section>

      {accounts.data?.warnings.map((warning) => (
        <section className="usage-route-notice" role="status" key={warning}><strong>{t("usage.account_notice")}</strong><span>{warning}</span></section>
      ))}
      {!currentGroup && !accounts.isPending ? (
        <Alert
          className="usage-route-alert"
          type={autoAssignment.isError ? "error" : "info"}
          showIcon
          role={autoAssignment.isError ? "alert" : "status"}
          title={autoAssignment.isPending ? t("usage.assigning_a_cpa_automatically") : autoAssignment.isError ? t("usage.automatic_cpa_assignment_failed") : t("usage.automatic_cpa_assignment_unavailable")}
          description={autoAssignment.isPending
            ? t("usage.selecting_the_available_account_with_the_lowest_weekly_quota_usage")
            : autoAssignment.isError
              ? t("usage.retry_automatic_assignment_or_select_an_account_manually_below", [errorMessage(autoAssignment.error)])
              : accounts.data?.accounts.length
                ? t("usage.automatic_assignment_is_incomplete_retry_or_select_an_account_manually")
                : t("usage.no_accounts_are_available_refresh_after_an_account_is_ready")}
          action={autoAssignment.isError ? <Button size="small" onClick={() => { autoAssignment.reset(); autoAssignment.mutate(); }}>{t("usage.retry_automatic_assignment")}</Button> : undefined}
        />
      ) : null}

      <div className={`usage-detail-sections ${primarySection === "trend" ? "trend-primary" : "accounts-primary"}`}>
        <Tabs
          className="usage-primary-tabs"
          activeKey={primarySection}
          destroyOnHidden={false}
          onChange={(key) => setPrimarySection(key as PrimarySection)}
          tabBarExtraContent={compactTabs ? undefined : primarySection === "trend" ? (
            <TrendWindowControl window={trendWindow} onChange={setTrendWindow} updateStatus={trendUpdateStatus} />
          ) : (
            <AccountWindowControl
              window={window}
              onChange={setWindow}
              refreshing={accountRefresh.isPending || profile.isFetching || quota.isFetching || accounts.isFetching || route.isFetching}
              loading={accountRefresh.isPending || accounts.data?.quota_refreshing === true || accounts.isFetching || quota.isFetching}
              failed={accounts.isError || quota.isError}
              updatedAt={accounts.data?.generated_at ?? quota.data?.generated_at ?? 0}
              onRefresh={refresh}
            />
          )}
          items={[
            {
              key: "accounts",
              label: (
                <span className="usage-primary-tab-label">
                  <span className="usage-primary-tab-dot" aria-hidden="true" />
                  <span className="usage-primary-tab-text">{t("common.account_details")}</span>
                  <span className="usage-primary-tab-count" aria-hidden="true"><span>{accounts.isPending ? "…" : sortedAccounts.length}</span></span>
                </span>
              ),
              children: (
                <section className="usage-account-section" aria-label={t("common.account_details")}>
                  {compactTabs ? <AccountWindowControl
                    className="usage-mobile-panel-actions"
                    window={window}
                    onChange={setWindow}
                    refreshing={accountRefresh.isPending || profile.isFetching || quota.isFetching || accounts.isFetching || route.isFetching}
                    loading={accountRefresh.isPending || accounts.data?.quota_refreshing === true || accounts.isFetching || quota.isFetching}
                    failed={accounts.isError || quota.isError}
                    updatedAt={accounts.data?.generated_at ?? quota.data?.generated_at ?? 0}
                    onRefresh={refresh}
                  /> : null}
                  {primarySection === "accounts" && accounts.isError ? (
                    <div className="usage-error" role="alert">
                      <span><strong>{t("usage.unable_to_load_accounts_and_usage")}</strong> · {errorMessage(accounts.error)}</span>
                      <button className="usage-secondary-button" type="button" onClick={() => void accounts.refetch()}>{t("common.reload")}</button>
                    </div>
                  ) : primarySection === "accounts" ? (
                    <NativeTableViewport className="usage-table-wrap" aria-label={t("usage.account_details_table")}>
                      <table className="usage-account-table">
                        <thead>
                          <tr>
                            <th className="table-index-column" scope="col">{t("common.no")}</th>
                            <SortableHeader field="current" label={t("common.current_account")} sort={sort} onSort={changeSort} />
                            <SortableHeader field="account" label={t("common.cpa_account")} sort={sort} onSort={changeSort} />
                            <SortableHeader field="quota" label={t("common.account_weekly_quota")} detail={t("usage.shared_by_all_users_lowest_usage_first")} sort={sort} onSort={changeSort} />
                            <SortableHeader field="active_users" label={t("common.active_users")} detail={formatActiveUserWindow(accounts.data?.active_user_window_seconds ?? 900)} sort={sort} onSort={changeSort} />
                            <SortableHeader field="status" label={t("common.account_status")} sort={sort} onSort={changeSort} />
                            <SortableHeader field="requests" label={t("common.my_requests")} detail={windowLabel(window)} sort={sort} onSort={changeSort} />
                            <SortableHeader className="usage-token-header" field="tokens" label={t("common.my_tokens")} detail={windowLabel(window)} sort={sort} onSort={changeSort} />
                            <SortableHeader field="last_used" label={t("common.last_used")} detail={t("usage.my_history")} sort={sort} onSort={changeSort} />
                            <th scope="col"><span className="sr-only">{t("common.usage_details")}</span></th>
                          </tr>
                        </thead>
                        <tbody>
                          {accounts.isPending ? <UsageTableSkeleton /> : null}
                          {!accounts.isPending && sortedAccounts.map((account, index) => (
                            <AccountRows
                              key={account.id}
                              account={account}
                              user={user}
                              index={index}
                              currentGroup={currentGroup}
                              window={window}
                              activeUserWindowSeconds={accounts.data?.active_user_window_seconds ?? 900}
                              expanded={expanded.has(account.id)}
                              onToggle={() => toggleExpanded(account.id)}
                              onSwitch={() => { accountSwitch.reset(); setSwitchTarget(account); }}
                            />
                          ))}
                        </tbody>
                      </table>
                      {!accounts.isPending && sortedAccounts.length === 0 ? <div className="usage-empty">{t("usage.no_available_accounts")}</div> : null}
                    </NativeTableViewport>
                  ) : null}
                </section>
              )
            },
            {
              key: "trend",
              label: <span className="usage-primary-tab-label"><span className="usage-primary-tab-dot" aria-hidden="true" /><span className="usage-primary-tab-text">{t("usage.daily_usage")}</span></span>,
              children: (
                <>
                  {compactTabs ? <TrendWindowControl className="usage-mobile-panel-actions" window={trendWindow} onChange={setTrendWindow} updateStatus={trendUpdateStatus} /> : null}
                  <PortalDailyUsageTrend
                    expanded={primarySection === "trend"}
                    user={user}
                    window={trendWindow}
                    onSessionExpired={onSessionExpired}
                    onUpdateStatusChange={setTrendUpdateStatus}
                  />
                </>
              )
            }
          ]}
        />
      </div>

      <Modal title={t("usage.switch_to", [switchTarget ? accountLabel(switchTarget) : t("usage.target_account")])} open={Boolean(switchTarget)} okText={t("usage.confirm_switch")} cancelText={t("common.cancel")} confirmLoading={accountSwitch.isPending} onCancel={() => !accountSwitch.isPending && setSwitchTarget(null)} onOk={() => switchTarget && accountSwitch.mutate(switchTarget)} destroyOnHidden>
        {accountSwitch.isError ? <Alert type="error" showIcon title={t("usage.unable_to_switch_account")} description={errorMessage(accountSwitch.error)} /> : null}
      </Modal>

      <Modal title={t("common.manage_api_key")} open={keyOpen} footer={<Button onClick={closeKey}>{t("common.close")}</Button>} onCancel={closeKey} destroyOnHidden>
        <Space orientation="vertical" size={16} className="portal-form-stack">
          {keyLoading ? <Skeleton.Input active block /> : null}
          {keyError ? <Alert type="error" showIcon title={t("usage.unable_to_read_api_key")} description={keyError} /> : null}
          {keyValue ? (
            <Form.Item label="API Key">
              <Space.Compact block>
                <Input.Password value={keyValue} readOnly visibilityToggle={{ visible: showKey, onVisibleChange: setShowKey }} aria-label="API Key" autoComplete="off" />
                <Button type="primary" icon={<CopyOutlined aria-hidden="true" />} onClick={() => void copyKey()}>{t("common.copy")}</Button>
              </Space.Compact>
            </Form.Item>
          ) : null}
          <section className="usage-key-danger-zone" aria-label={t("usage.api_key_danger_zone")}>
            <div><strong>{t("usage.refresh_api_key")}</strong><p>{t("usage.the_old_key_expires_as_soon_as_the_gateway_activates")}</p></div>
            <Button danger onClick={() => { rotation.reset(); setRotationOpen(true); }}>{t("usage.refresh_api_key")}</Button>
          </section>
        </Space>
      </Modal>

      <Modal title={t("usage.refresh_personal_api_key")} open={rotationOpen} okText={t("usage.refresh_invalidate_old_key")} cancelText={t("common.cancel")} okButtonProps={{ danger: true }} confirmLoading={rotation.isPending} onCancel={() => !rotation.isPending && setRotationOpen(false)} onOk={() => rotation.mutate()} destroyOnHidden>
        <Space orientation="vertical" size={16} className="portal-form-stack">
          <Alert type="warning" showIcon title={t("usage.the_old_api_key_will_expire_immediately")} description={t("usage.after_refreshing_update_your_codex_clients_immediately_success_is_returned")} />
          {rotation.isError ? <Alert type="error" showIcon title={t("usage.unable_to_refresh_api_key")} description={errorMessage(rotation.error)} /> : null}
        </Space>
      </Modal>

      <PortalClientConfigModal open={clientConfigMode !== null} mode={clientConfigMode ?? "codex"} user={profile.data?.user ?? "user"} currentGroup={currentGroup} onClose={() => setClientConfigMode(null)} onSessionExpired={onSessionExpired} />
    </section>
  );
}

function TrendWindowControl({
  className = "",
  window,
  onChange,
  updateStatus
}: {
  className?: string;
  window: PortalUsageTrendWindow;
  onChange: (window: PortalUsageTrendWindow) => void;
  updateStatus: PortalTrendUpdateStatus;
}) {
  return (
    <div className={`usage-trend-toolbar-actions ${className}`.trim()}>
      <UsageUpdateBadge scope={t("usage.daily_usage")} {...updateStatus} />
      <div className="usage-trend-windows" role="group" aria-label={t("usage.daily_trend_time_range")}>
        {portalTrendWindowOptions.map((option) => (
          <button type="button" key={option.value} aria-pressed={window === option.value} onClick={() => onChange(option.value)}>{option.label}</button>
        ))}
      </div>
    </div>
  );
}

function AccountWindowControl({
  className = "",
  window,
  onChange,
  refreshing,
  loading,
  failed,
  updatedAt,
  onRefresh
}: {
  className?: string;
  window: PortalUsageWindow;
  onChange: (window: PortalUsageWindow) => void;
  refreshing: boolean;
  loading: boolean;
  failed: boolean;
  updatedAt: number;
  onRefresh: () => void;
}) {
  return (
    <div className={`usage-toolbar-actions usage-tab-toolbar-actions ${className}`.trim()}>
      <UsageUpdateBadge scope={t("common.account_details")} updatedAt={updatedAt} refreshing={loading} failed={failed} />
      <div className="usage-window-switcher" role="group" aria-label={t("usage.reporting_time_range")}>
        {portalWindowOptions.map((option) => (
          <button type="button" key={option.value} aria-pressed={window === option.value} onClick={() => onChange(option.value)}>{option.label}</button>
        ))}
      </div>
      <button className="usage-refresh-button" type="button" disabled={refreshing} onClick={onRefresh}>
        {loading ? t("usage.refreshing") : t("common.refresh")}
      </button>
    </div>
  );
}

function UsageUpdateBadge({ scope, updatedAt, refreshing, failed }: PortalTrendUpdateStatus & { scope: string }) {
  const hasTimestamp = Number.isFinite(updatedAt) && updatedAt > 0;
  const state = refreshing ? "loading" : failed ? "error" : hasTimestamp ? "ready" : "empty";
  return <div className="usage-updated" role="status" aria-label={t("usage.data_update_time", [scope])} data-state={state}>
    <span className="usage-update-dot" aria-hidden="true" />
    <span className="usage-update-label">{refreshing ? t("common.updating") : failed ? t("common.update_failed") : t("usage.data_update")}</span>
    <time key={updatedAt} className="usage-update-time" dateTime={hasTimestamp ? new Date(updatedAt * 1000).toISOString() : undefined}>
      {hasTimestamp ? formatServerTimestamp(updatedAt) : refreshing ? t("common.loading_2") : t("common.no_data")}
    </time>
  </div>;
}

function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(() => window.matchMedia?.(query).matches ?? false);
  useEffect(() => {
    const media = window.matchMedia?.(query);
    if (!media) return;
    const update = () => setMatches(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [query]);
  return matches;
}

function CurrentAccountSummary({ account, loading }: { account?: PortalAccount; loading: boolean }) {
  const used = account ? accountUsedPercent(account) : 0;
  const remaining = account?.status.remaining_percent ?? (account ? Math.max(0, 100 - used) : 0);
  return (
    <div className="usage-current-account" aria-labelledby="current-account-label">
      <div className="usage-current-account-head">
        <span className="usage-current-account-label" id="current-account-label">{t("common.current_account")}</span>
        {loading && !account ? <span className="usage-status usage-summary-tag degraded">{t("common.loading_3")}</span> : account ? <StatusTag account={account} summary /> : <span className="usage-status usage-summary-tag degraded">{t("usage.not_selected")}</span>}
      </div>
      <strong className="usage-current-account-name" title={account ? accountLabel(account) : undefined}>{account ? accountLabel(account) : loading ? t("common.loading") : t("common.not_selected")}</strong>
      <div className={`usage-current-quota ${used >= 100 ? "exhausted" : used >= 80 ? "warning" : ""}`.trim()}>
        <div><span>{account ? t("usage.weekly_quota", [formatPercent(used)]) : t("common.shown_after_selecting_an_available_account")}</span><strong>{account ? t("usage.remaining", [formatPercent(remaining)]) : "—"}</strong></div>
        <progress className="usage-quota-track" max="100" value={used} aria-label={account ? t("usage.current_account_weekly_quota_used", [formatPercent(used)]) : t("usage.no_account_selected")} />
      </div>
    </div>
  );
}

function PersonalQuotaSummary({ quota, loading, error, onRetry }: { quota?: PortalQuota; loading: boolean; error: unknown; onRetry: () => void }) {
  const weekly = quota?.weekly_quota;
  const percent = weekly?.unlimited ? 0 : clampPercent(weekly?.used_percent ?? 0);
  const remaining = weekly?.unlimited ? null : Math.max(0, 100 - percent);
  const quotaTooltip = weekly ? (
    <div className="usage-quota-tooltip">
      <strong>{t("usage.my_weekly_quota")}</strong>
      <span><b>{t("usage.weighted_usage")}</b><em>{formatNumber(weekly.weighted_used_tokens)}</em></span>
      <span><b>{t("usage.raw_usage")}</b><em>{formatNumber(weekly.raw_used_tokens)}</em></span>
      <span><b>{t("usage.total_quota")}</b><em>{weekly.unlimited ? t("common.unlimited") : formatNumber(weekly.limit_tokens ?? 0)}</em></span>
      <span><b>{t("usage.remaining_quota")}</b><em>{weekly.unlimited ? t("common.unlimited") : formatNumber(weekly.remaining_tokens ?? 0)}</em></span>
    </div>
  ) : null;
  return (
    <div className="usage-personal-overview" aria-labelledby="personal-usage-label">
      <div className="usage-personal-overview-head"><span id="personal-usage-label">{t("common.my_weekly_usage")}</span><small className="usage-summary-tag">{weekly ? quotaSourceLabel(weekly.source) : t("common.organization_default")}</small></div>
      {error ? (
        <div className="usage-current-quota degraded">
          <div><span>{t("usage.unable_to_load_personal_weekly_quota")}</span><button className="usage-inline-retry" type="button" onClick={onRetry}>{t("common.retry")}</button></div>
          <progress className="usage-quota-track" max="100" value="0" aria-label={t("usage.personal_weekly_quota_unavailable")} />
        </div>
      ) : (
        <div className={`usage-current-quota ${weekly?.limit_reached ? "exhausted" : percent >= 90 ? "warning" : ""}`.trim()}>
          <div><span>{loading ? t("common.loading_weekly_quota") : weekly?.unlimited ? t("usage.unlimited_weekly_quota") : t("usage.weekly_quota_2", [formatPercent(percent)])}</span><strong>{loading ? "—" : weekly?.unlimited ? t("usage.unlimited_remaining") : t("usage.remaining", [formatPercent(remaining ?? 0)])}</strong></div>
          <progress className="usage-quota-track" max="100" value={percent} aria-label={weekly?.unlimited ? t("usage.personal_weekly_quota_is_unlimited") : t("usage.personal_weekly_quota_used", [formatPercent(percent)])} />
          <div className="usage-personal-quota-detail">
            <span>{weekly ? weekly.unlimited ? t("usage.weighted_usage_2", [formatTokens(weekly.weighted_used_tokens)]) : t("usage.weighted_usage_3", [formatTokens(weekly.weighted_used_tokens), formatTokens(weekly.limit_tokens ?? 0)]) : t("usage.loading_usage")}{weekly ? <UsageHelp
              label={t("usage.view_personal_weekly_quota_token_details")}
              title={quotaTooltip}
            /> : null}</span>
            <time>{weekly ? t("usage.reset", [formatServerTimestamp(weekly.week_end_at)]) : "—"}</time>
          </div>
        </div>
      )}
    </div>
  );
}

function RangeSummary({ window, metrics, loading }: { window: PortalUsageWindow; metrics?: UsageMetrics; loading: boolean }) {
  return (
    <div className="usage-range-overview" aria-labelledby="usage-summary-label">
      <div className="usage-range-overview-head"><span id="usage-summary-label">{windowLabel(window)} Token</span><small>{t("common.all_cpas")}</small></div>
      <TokenPair metrics={metrics} loading={loading} />
    </div>
  );
}

function SortableHeader({ field, label, detail, className = "", sort, onSort }: { field: SortField; label: string; detail?: string; className?: string; sort: SortState; onSort: (field: SortField) => void }) {
  const active = sort.field === field;
  return (
    <th className={className} scope="col" aria-sort={active ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}>
      <button className={`usage-sort-button ${active ? "active" : ""}`.trim()} data-direction={active ? sort.direction : undefined} type="button" aria-label={`${label}${active ? t("usage.currently", [sort.direction === "asc" ? t("common.ascending") : t("common.descending")]) : t("common.click_to_sort")}`} onClick={() => onSort(field)}>
        <span className="usage-sort-copy"><span>{label}</span>{detail ? <small>{detail}</small> : null}</span>
      </button>
    </th>
  );
}

function AccountRows({ user, account, index, currentGroup, window, activeUserWindowSeconds, expanded, onToggle, onSwitch }: { user: string; account: PortalAccount; index: number; currentGroup: string; window: PortalUsageWindow; activeUserWindowSeconds: number; expanded: boolean; onToggle: () => void; onSwitch: () => void }) {
  const current = account.id === currentGroup;
  const used = accountUsedPercent(account);
  const remaining = account.status.remaining_percent ?? Math.max(0, 100 - used);
  const rowKeyDown = (event: React.KeyboardEvent<HTMLTableRowElement>) => {
    if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    onToggle();
  };
  return (
    <>
      <tr className={`usage-summary-row ${current ? "current" : ""}`.trim()} aria-expanded={expanded} tabIndex={0} onKeyDown={rowKeyDown} onClick={(event) => { if (!(event.target as HTMLElement).closest("button, a")) onToggle(); }}>
        <td className="table-index-cell" data-label={t("common.no")}>{index + 1}</td>
        <td data-label={t("common.current_account")}>
          {current ? <span className="usage-current-mark" title={t("common.current_account")}><svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m5 13 4 4L19 7" /></svg><span className="sr-only">{t("common.current_account")}</span></span> : <button className="usage-select-button" type="button" disabled={!account.selectable || !account.status.selectable} title={(account.status.reason)} onClick={onSwitch}>{currentGroup ? t("usage.switch") : t("usage.select")}</button>}
        </td>
        <td data-label={t("common.cpa_account")}><strong className="usage-account-id" title={accountLabel(account)}>{accountLabel(account)}</strong></td>
        <td data-label={t("common.account_weekly_quota")}>
          <div className={`usage-quota ${used >= 100 ? "exhausted" : used >= 80 ? "warning" : ""}`.trim()}>
            <div><strong>{formatPercent(used)}</strong><span>{t("common.remaining")} {formatPercent(remaining)}</span></div>
            <progress className="usage-quota-track" max="100" value={used} aria-label={t("common.used", [formatPercent(used)])} />
            <small>{account.status.reset_at ? t("usage.resets", [formatServerTimestamp(account.status.reset_at)]) : t("common.unknown_reset_time")}</small>
          </div>
        </td>
        <td data-label={t("usage.active_users", [formatActiveUserWindow(activeUserWindowSeconds)])}><strong className="usage-cell-number">{formatNumber(account.active_users_1h)}</strong></td>
        <td data-label={t("common.account_status")}><StatusTag account={account} /></td>
        <td data-label={t("usage.my_requests", [windowLabel(window)])}><strong className="usage-cell-number" title={formatNumber(account.usage.request_count)}>{formatCompact(account.usage.request_count)}</strong></td>
        <td className="usage-token-cell" data-label={t("usage.my_tokens", [windowLabel(window)])}><div className="usage-token-content"><TokenPair metrics={account.usage} /></div></td>
        <td data-label={t("usage.my_last_use")}><time className="usage-last-used">{formatServerTimestamp(account.usage.last_used_at)}</time></td>
        <td><button className="usage-expand-button" type="button" aria-label={expanded ? t("usage.collapse_usage_details") : t("common.usage_details")} aria-expanded={expanded} onClick={onToggle}>{expanded ? "−" : "+"}</button></td>
      </tr>
      {expanded ? <UsageBreakdownRow user={user} account={account} window={window} /> : null}
    </>
  );
}

function UsageBreakdownRow({ user, account, window }: { user: string; account: PortalAccount; window: PortalUsageWindow }) {
  const query = useQuery({
    queryKey: [...portalBreakdownQueryKey(account.id, window), user],
    queryFn: ({ signal }) => readPortalBreakdown(account.id, window, signal),
    staleTime: 30_000,
    gcTime: 5 * 60_000,
    retry: false,
    refetchOnWindowFocus: false
  });
  return (
    <tr className="usage-detail-row" data-detail-for={account.id}>
      <td colSpan={10}>
        <div className="usage-account-detail">
          <div className="usage-detail-panel">
            <div className="usage-detail-heading"><strong>{t("usage.my_usage_details")}</strong><span>{windowLabel(window)}</span></div>
            {query.data ? <UsageTokenGrid metrics={query.data.totals} /> : <div className="usage-token-grid usage-token-grid-placeholder" aria-label={t("usage.loading_my_model_token_details")}>{Array.from({ length: 8 }, (_, index) => <Skeleton.Input active key={index} />)}</div>}
          </div>
          <section className="account-model-usage" aria-label={t("usage.my_tokens_by_model_and_reasoning_effort")}>
            <div className="account-model-usage-title"><span>{t("usage.my_model_reasoning_effort_tokens")}</span><small>{windowLabel(window)}</small></div>
            {query.isError ? <div className="account-model-usage-message error" role="alert"><span>{errorMessage(query.error)}</span><button className="usage-breakdown-retry" type="button" onClick={() => void query.refetch()}>{t("common.retry")}</button></div> : query.data ? <ModelBreakdown data={query.data} window={window} /> : <div className="account-model-usage-skeleton" aria-label={t("usage.loading_my_model_token_details")}><span /><span /></div>}
          </section>
        </div>
      </td>
    </tr>
  );
}

function UsageTokenGrid({ metrics }: { metrics: UsageMetrics }) {
  const cacheRate = metrics.input_tokens > 0 ? formatPercent((metrics.cached_tokens / metrics.input_tokens) * 100) : "0%";
  return (
    <div className="usage-token-grid">
      <Metric label={t("common.successful_requests")} value={formatNumber(metrics.success_count)} />
      <Metric label={t("common.failed_requests")} value={formatNumber(metrics.failed_count)} />
      <TokenMetric label={t("common.input_tokens")} value={metrics.input_tokens} />
      <TokenMetric label={t("common.output_tokens")} value={metrics.output_tokens} />
      <TokenMetric label={t("common.reasoning_tokens")} value={metrics.reasoning_tokens} />
      <div className="usage-cache-metric">
        <span>{t("common.cache_rate")}</span>
        <div className="usage-metric-value"><strong className="usage-cache-rate" title={t("common.cached_tokens_input_tokens")}>{cacheRate}</strong></div>
      </div>
      <TokenMetric label={t("common.raw_tokens")} value={metrics.total_tokens} />
      <TokenMetric label={t("common.weighted_tokens_2")} value={metrics.weighted_tokens ?? metrics.total_tokens} />
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><div className="usage-metric-value"><strong>{value}</strong></div></div>;
}

function TokenMetric({ label, value }: { label: string; value: number }) {
  const safeValue = Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;
  return (
    <div>
      <span>{label}</span>
      <div className="usage-metric-value">
        <strong>{formatTokens(safeValue)}</strong>
        {safeValue >= 1_000 ? <small className="usage-token-raw">{formatNumber(safeValue)} Token</small> : null}
      </div>
    </div>
  );
}

function UsageHelp({ label, title }: { label: string; title: ReactNode }) {
  return (
    <Tooltip title={title} trigger={["hover", "focus"]} placement="top">
      <button className="usage-help-button" type="button" aria-label={label}>
        <QuestionCircleOutlined aria-hidden="true" />
      </button>
    </Tooltip>
  );
}

function ModelBreakdown({ data, window }: { data: UsageBreakdown; window: PortalUsageWindow }) {
  const models = groupModelCombinations(data.combinations);
  if (models.length === 0) return <div className="account-model-usage-message">{t("usage.no_personal_model_and_reasoning_effort_token_data_in_this")}</div>;
  return (
    <div className="account-model-usage-list">
      {models.map((model) => {
        const modelLabel = formatUsageModelLabel(model.name, data.current_multipliers);
        return (
          <div className="account-model-usage-row" key={model.name}>
            <div className="account-model-usage-head">
              <strong className="account-model-name" title={modelLabel}>{modelLabel}</strong>
              <span className="account-model-token">{formatTokens(model.total)}</span>
            </div>
            <div className="account-model-progress" role="group" aria-label={t("common.token_share_by_reasoning_effort", [modelLabel])}>
              {model.efforts.map((effort) => (
                <Tooltip
                  key={effort.reasoning_effort}
                  title={<ModelEffortTooltip model={model.name} effort={effort} window={window} multipliers={data.current_multipliers} />}
                  trigger={["hover", "focus"]}
                  placement="top"
                  rootClassName="usage-model-effort-popup"
                >
                  <button className={`account-model-progress-segment account-model-effort-${effortColorKey(effort.reasoning_effort)} ${effort.share < 18 ? "compact" : ""}`.trim()} style={{ flexGrow: Math.max(1, Math.round(effort.share)) }} type="button" aria-label={t("usage.view_reasoning_effort_token_details", [modelLabel, formatUsageReasoningLabel(effort.reasoning_effort, data.current_multipliers)])}>
                    <span>{formatUsageReasoningLabel(effort.reasoning_effort, data.current_multipliers)}</span><em>{formatPercent(effort.share)}</em>
                  </button>
                </Tooltip>
              ))}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function groupModelCombinations(combinations: UsageCombination[]) {
  const grouped = new Map<string, UsageCombination[]>();
  for (const item of combinations) grouped.set(item.model, [...(grouped.get(item.model) ?? []), item]);
  return [...grouped.entries()].map(([name, efforts]) => {
    const total = efforts.reduce((sum, effort) => sum + (effort.weighted_tokens ?? effort.total_tokens), 0);
    return { name, total, efforts: efforts.map((effort) => ({ ...effort, share: total > 0 ? ((effort.weighted_tokens ?? effort.total_tokens) / total) * 100 : 0 })) };
  }).sort((left, right) => right.total - left.total || left.name.localeCompare(right.name));
}

function ModelEffortTooltip({ model, effort, window, multipliers }: { model: string; effort: UsageCombination & { share: number }; window: PortalUsageWindow; multipliers: UsageBreakdown["current_multipliers"] }) {
  return (
    <div className="usage-model-effort-tooltip">
      <strong>{formatUsageCombinationLabel(model, effort.reasoning_effort, multipliers)}</strong>
      <span><b>{t("common.reporting_range")}</b><em>{windowLabel(window)}</em></span>
      <span><b>{t("usage.weighted_share_within_this_model")}</b><em>{formatPercent(effort.share)}</em></span>
      <span><b>{t("common.calls")}</b><em>{formatNumber(effort.request_count)}</em></span>
      <span><b>{t("common.input_tokens")}</b><em>{formatNumber(effort.input_tokens)}</em></span>
      <span><b>{t("common.output_tokens")}</b><em>{formatNumber(effort.output_tokens)}</em></span>
      <span><b>{t("common.reasoning_tokens")}</b><em>{formatNumber(effort.reasoning_tokens)}</em></span>
      <span><b>{t("common.cached_tokens")}</b><em>{formatNumber(effort.cached_tokens)}</em></span>
      <span><b>{t("common.raw_tokens")}</b><em>{formatNumber(effort.total_tokens)}</em></span>
      <span><b>{t("common.weighted_tokens_2")}</b><em>{formatNumber(effort.weighted_tokens ?? effort.total_tokens)}</em></span>
    </div>
  );
}

function TokenPair({ metrics, loading = false }: { metrics?: UsageMetrics; loading?: boolean }) {
  return (
    <div className="usage-user-token-pair">
      <div><small>{t("common.weighted_2")}</small>{loading ? <strong>—</strong> : <TokenValue value={metrics?.weighted_tokens ?? metrics?.total_tokens ?? 0} />}</div>
      <div><small>{t("common.unweighted_2")}</small>{loading ? <strong>—</strong> : <TokenValue value={metrics?.total_tokens ?? 0} />}</div>
    </div>
  );
}

function TokenValue({ value }: { value: number }) {
  const safe = Math.max(0, Math.floor(Number.isFinite(value) ? value : 0));
  const compact = formatTokenAmount(safe);
  const [amount, unit = "Token"] = compact.split(" ");
  const exact = `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 }).format(safe)} Token`;
  return (
    <span className="token-usage">
      <span className="token-usage-main" aria-hidden="true"><span className="token-usage-value">{amount}</span><small className="token-usage-unit">{unit}</small></span>
      {safe >= 1_000 ? <small className="token-usage-exact" aria-hidden="true">{exact}</small> : null}
      <span className="token-usage-sr-only">{exact}</span>
    </span>
  );
}

function StatusTag({ account, summary = false }: { account: PortalAccount; summary?: boolean }) {
  const className = account.status.tone === "success" ? "available" : account.status.tone === "warning" ? "warning" : account.status.tone === "danger" ? "unavailable" : "degraded";
  return <span className={`usage-status ${summary ? "usage-summary-tag " : ""}${className}`} title={(account.status.reason)}>{(account.status.label)}</span>;
}

function UsageTableSkeleton() {
  return <>{Array.from({ length: 3 }, (_, row) => <tr className="usage-summary-row usage-skeleton-row" key={row} aria-label={t("usage.loading_accounts_and_usage")}>{Array.from({ length: 10 }, (_item, column) => <td key={column}><span /></td>)}</tr>)}</>;
}

function accountLabel(account: PortalAccount) {
  return account.email.trim() || account.display_name;
}

function accountLabelByID(accounts: PortalAccount[], accountID: string) {
  const account = accounts.find((item) => item.id === accountID);
  return account ? accountLabel(account) : t("usage.available_cpa_accounts");
}

function sortAccounts(accounts: PortalAccount[], currentGroup: string, sort: SortState) {
  const direction = sort.direction === "asc" ? 1 : -1;
  return [...accounts].sort((left, right) => {
    if (sort.pinCurrent && (left.id === currentGroup) !== (right.id === currentGroup)) return left.id === currentGroup ? -1 : 1;
    const compared = compareSortValue(sortValue(left, currentGroup, sort.field), sortValue(right, currentGroup, sort.field));
    return compared * direction || accountLabel(left).localeCompare(accountLabel(right), getIntlLocale(), { numeric: true });
  });
}

function sortValue(account: PortalAccount, currentGroup: string, field: SortField): number | string | null {
  if (field === "current") return account.id === currentGroup ? 0 : 1;
  if (field === "account") return accountLabel(account);
  if (field === "quota") return Number.isFinite(accountUsedPercent(account)) ? accountUsedPercent(account) : null;
  if (field === "active_users") return account.active_users_1h;
  if (field === "status") return statusRank(account);
  if (field === "requests") return account.usage.request_count;
  if (field === "tokens") return account.usage.weighted_tokens ?? account.usage.total_tokens;
  return account.usage.last_used_at || null;
}

function compareSortValue(left: number | string | null, right: number | string | null) {
  if (left === null && right === null) return 0;
  if (left === null) return 1;
  if (right === null) return -1;
  if (typeof left === "string" || typeof right === "string") return String(left).localeCompare(String(right), getIntlLocale(), { numeric: true });
  return left - right;
}

function accountUsedPercent(account: PortalAccount) {
  if (account.status.used_percent !== undefined) return clampPercent(account.status.used_percent);
  if (account.status.remaining_percent !== undefined) return 100 - clampPercent(account.status.remaining_percent);
  return Number.POSITIVE_INFINITY;
}

function statusRank(account: PortalAccount) {
  return ({ available: 0, quota_warning: 1, transient_cooldown: 2, rate_limited: 3, degraded: 4, quota_unknown: 5, unknown: 6, quota_exhausted: 7, credential_unavailable: 8, auth_missing: 9, stopped: 10, disabled: 11 } as Record<string, number>)[account.status.code] ?? 12;
}

function quotaSourceLabel(source: string) {
  return ({ default: t("common.organization_default"), user_unlimited: t("common.personal_unlimited_quota"), user_custom: t("common.user_override") } as Record<string, string>)[source] ?? t("common.unknown_status");
}

function effortColorKey(value: string) {
  return ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "auto"].includes(value) ? value : "unknown";
}

function isUnauthorized(error: unknown) {
  return error instanceof ApiError && error.status === 401;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : t("common.please_try_again_later");
}

function windowLabel(window: PortalUsageWindow) {
  return portalWindowOptions.find((item) => item.value === window)?.label ?? t("common.current_range");
}

function clampPercent(value: number) {
  return Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0));
}

function formatPercent(value: number) {
  return `${new Intl.NumberFormat(getIntlLocale(), { maximumFractionDigits: 1 }).format(value)}%`;
}

function formatNumber(value: number) {
  return new Intl.NumberFormat(getIntlLocale()).format(Number(value) || 0);
}

function formatActiveUserWindow(seconds: number): string {
  const minutes = Math.max(1, Math.round(seconds / 60));
  return minutes % 60 === 0 ? t("common.last_hours", [minutes / 60]) : t("common.last_minutes", [minutes]);
}

function formatCompact(value: number) {
  return new Intl.NumberFormat(getIntlLocale(), { notation: "compact", maximumFractionDigits: 1 }).format(Number(value) || 0);
}

export function formatServerTimestamp(timestamp: number) {
  return formatSiteTimestamp(timestamp);
}

const portalWindowOptions: Array<{ value: PortalUsageWindow; label: string }> = [
  { value: "3600", label: t("common.1h") },
  { value: "today", label: t("common.today") },
  { value: "86400", label: t("common.24h") },
  { value: "604800", label: t("common.7d") },
  { value: "current_week", label: t("common.this_week") }
];
