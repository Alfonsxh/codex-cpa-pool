import "../i18n/admin";
import { t, getIntlLocale } from "../i18n";
import { useSiteTimezone, formatSiteTimestamp, getSiteTimezone } from "./site-time";
import { DownOutlined } from "@ant-design/icons";
import {
  Button,
  Checkbox,
  Drawer,
  Dropdown,
  Form,
  Modal,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
  type TableColumnsType
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Link, useSearchParams } from "react-router-dom";

import { ApiError } from "../api/client";
import {
  normalizedEmailDomains,
  publicSiteQueryKey,
  readPublicSiteConfiguration
} from "../api/public-site";
import {
  applyUserQuotaAction,
  assignUserTeam,
  assignUsersTeam,
  clearUserQuota,
  createUser,
  deleteUser,
  listUsers,
  readUserDetail,
  readUserQuota,
  resetUserPassword,
  revokeUser,
  rotateUserKey,
  updateUserQuota,
  userDetailQueryKey,
  userQuotaQueryKey,
  usersQueryKey,
  usersQueryRoot,
  type UserAccountDetail,
  type UserListParams,
  type UserOneTimeKey,
  type UserQuotaActionInput,
  type UserQuotaMode,
  type UserQuotaResult,
  type UserSummary,
  type UserWeeklyQuota
} from "../api/users";
import {
  readTeamUsage,
  readTeamUsageBreakdown,
  teamUsageQueryKey,
  type TeamCombinationUsage,
  type TeamUsageBreakdownResponse,
  type TeamUsageRow,
  type TeamUsageSeries
} from "../api/teams";
import {
  readUsageBreakdown,
  usageBreakdownQueryKey,
  usageBreakdownQueryRoot,
  type UsageBreakdown,
  type UsageCombination,
  type UsageRange,
  type UsageWindow
} from "../api/usage";
import { ManagementUsageTimeFilter } from "./components/ManagementUsageTimeFilter";
import { AdminTable } from "./components/AdminTable";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { SecretRevealModal, type SecretReveal } from "./components/SecretRevealModal";
import {
  CustomUsageRangeModal,
  type CustomUsageRange
} from "./components/CustomUsageRangeModal";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { useAdminToolbar } from "./AdminToolbarContext";
import {
  formatTokenAmount,
  tokenInputPresentation,
  tokenReadableText
} from "./formatters";
import { useDebouncedValue } from "./hooks/useDebouncedValue";
import { teamTagClassName } from "./teamTagStyles";

const { Paragraph, Text } = Typography;

type UserSortField = "email" | "requests" | "tokens" | "quota" | "last_used";
type SortDirection = "asc" | "desc";
type UserAccountSortField =
  | "account"
  | "status"
  | "requests"
  | "input_tokens"
  | "output_tokens"
  | "reasoning_tokens"
  | "cached_tokens"
  | "total_tokens"
  | "weighted_tokens"
  | "last_used_at";
type TeamAssignment = {
  users: string[];
  targetTeamID: string | null;
};
type LifecycleAction = {
  kind: "rotate" | "reset-password" | "revoke" | "delete";
  user: UserSummary;
  keyLabel?: string;
};
type QuotaActionDraft = {
  action: "add_bonus" | "reset_usage";
  scope: "selected" | "all";
  users: string[];
};

const usageWindowOptions: Array<{ value: Exclude<UsageWindow, "custom">; label: string }> = [
  { value: "3600", label: t("common.1h") },
  { value: "today", label: t("common.today") },
  { value: "86400", label: t("common.24h") },
  { value: "604800", label: t("common.7d") },
  { value: "2592000", label: t("common.30d") },
  { value: "current_week", label: t("common.this_week") },
  { value: "all", label: t("admin.all") }
];

export function LegacyUsersPage({ csrfToken }: { csrfToken: string }) {
  const siteTimezone = useSiteTimezone();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const reportedError = useRef<unknown>(null);
  const [searchDraft, setSearchDraft] = useState("");
  const [query, setQuery] = useState("");
  const [teamID, setTeamID] = useState("");
  const [usageWindow, setUsageWindow] = useState<UsageWindow>("today");
  const [customRange, setCustomRange] = useState<CustomUsageRange | null>(null);
  const [customRangeOpen, setCustomRangeOpen] = useState(false);
  const [sortField, setSortField] = useState<UserSortField>("tokens");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);
  const [selectedUsers, setSelectedUsers] = useState<string[]>([]);
  const [expandedUsers, setExpandedUsers] = useState<string[]>([]);
  const [assignment, setAssignment] = useState<TeamAssignment | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [quotaUser, setQuotaUser] = useState<string | null>(null);
  const [lifecycleAction, setLifecycleAction] = useState<LifecycleAction | null>(null);
  const [secretReveal, setSecretReveal] = useState<SecretReveal | null>(null);
  const [quotaAction, setQuotaAction] = useState<QuotaActionDraft | null>(null);
  const [restoreQuotaUsers, setRestoreQuotaUsers] = useState<string[] | null>(null);
  const [teamUsageOpen, setTeamUsageOpen] = useState(false);
  const lifecycleSubmitRef = useRef(false);
  const handledDeepLink = useRef("");
  const debouncedSearch = useDebouncedValue(searchDraft.trim(), 250);

  useEffect(() => {
    if (debouncedSearch !== searchDraft.trim() || debouncedSearch === query) return;
    setQuery(debouncedSearch);
    setPage(1);
    setExpandedUsers([]);
  }, [debouncedSearch, query, searchDraft]);

  const usageRange = useMemo<UsageRange>(() => ({
    window: usageWindow,
    startAt: usageWindow === "custom" ? customRange?.startAt : undefined,
    endAt: usageWindow === "custom" ? customRange?.endAt : undefined
  }), [customRange?.endAt, customRange?.startAt, usageWindow]);
  const listParams = useMemo<UserListParams>(() => ({
    query,
    teamId: teamID,
    usageState: "all",
    window: String(usageWindow),
    startAt: usageRange.startAt,
    endAt: usageRange.endAt,
    sort: sortField,
    direction: sortDirection,
    page,
    pageSize
  }), [
    page,
    pageSize,
    query,
    sortDirection,
    sortField,
    teamID,
    usageRange.endAt,
    usageRange.startAt,
    usageWindow
  ]);
  const users = useQuery({
    queryKey: usersQueryKey(listParams),
    queryFn: ({ signal }) => listUsers(listParams, signal),
    enabled: usageWindow !== "custom" || customRange !== null,
    placeholderData: (previous) => previous,
    retry: false,
    refetchOnWindowFocus: false
  });
  const siteConfiguration = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    enabled: createOpen,
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: false
  });
  const teamUsage = useQuery({
    queryKey: teamUsageQueryKey(usageRange),
    queryFn: ({ signal }) => readTeamUsage(usageRange, signal),
    enabled: usageWindow !== "custom" || customRange !== null,
    placeholderData: (previous) => previous,
    retry: false,
    refetchOnWindowFocus: false
  });

  const refreshUsers = useCallback(async () => {
    try {
      const [catalog, teamCatalog] = await Promise.all([
        listUsers({ ...listParams, fresh: true }),
        readTeamUsage({ ...usageRange, fresh: true })
      ]);
      queryClient.setQueryData(usersQueryKey(listParams), catalog);
      queryClient.setQueryData(teamUsageQueryKey(usageRange), teamCatalog);
      await Promise.all([
        queryClient.refetchQueries({ queryKey: [...usersQueryRoot, "detail"], type: "active" }),
        queryClient.refetchQueries({ queryKey: [...usageBreakdownQueryRoot, "user"], type: "active" })
      ]);
      setRefreshLabel(userRefreshLabel(catalog.summary_generated_at || catalog.generated_at, catalog.summary_cached));
      showToast(t("admin.data_refreshed"));
    } catch (error) {
      setRefreshLabel(t("admin.refresh_failed"));
      throw error;
    }
  }, [listParams, queryClient, setRefreshLabel, showToast, usageRange]);

  useEffect(() => {
    setRefreshAction(refreshUsers);
    return () => setRefreshAction(null);
  }, [refreshUsers, setRefreshAction]);
  useEffect(() => setRefreshing(users.isFetching), [setRefreshing, users.isFetching]);
  useEffect(() => {
    if (!users.data) return;
    reportedError.current = null;
    setRefreshLabel(userRefreshLabel(users.data.summary_generated_at || users.data.generated_at, users.data.summary_cached));
  }, [setRefreshLabel, siteTimezone, users.data]);
  useEffect(() => {
    if (!users.isError || reportedError.current === users.error) return;
    reportedError.current = users.error;
    setRefreshLabel(t("admin.refresh_failed"));
    showToast(errorMessage(users.error), "error");
  }, [setRefreshLabel, showToast, users.error, users.isError]);
  useEffect(() => () => {
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);

  const refreshAfterMutation = useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: usersQueryRoot }),
      queryClient.invalidateQueries({ queryKey: ["teams"] }),
      queryClient.invalidateQueries({ queryKey: ["overview"] })
    ]);
  }, [queryClient]);

  const assignmentMutation = useMutation({
    mutationFn: (input: TeamAssignment) => (
      input.users.length === 1
        ? assignUserTeam(input.users[0], input.targetTeamID, csrfToken)
        : assignUsersTeam(input.users, input.targetTeamID, csrfToken)
    ),
    onSuccess: async (result) => {
      setAssignment(null);
      showToast(result.message);
      await refreshAfterMutation();
    }
  });
  const createMutation = useMutation({
    mutationFn: (input: { email: string; teamID: string | null }) => createUser(input.email, input.teamID, csrfToken),
    gcTime: 0,
    onSuccess: async (result, input) => {
      setCreateOpen(false);
      setSecretReveal({
        kind: "created",
        message: result.message,
        keys: result.keys,
        password: result.initial_password,
        passwordUser: input.email.trim().toLowerCase()
      });
      createMutation.reset();
      await refreshAfterMutation();
    }
  });
  useEffect(() => {
    const signature = searchParams.toString();
    if (!signature || handledDeepLink.current === signature) return;
    if (searchParams.get("create") !== "1") return;
    handledDeepLink.current = signature;
    createMutation.reset();
    setCreateOpen(true);
    setSearchParams({}, { replace: true });
  }, [createMutation, searchParams, setSearchParams]);
  const lifecycleMutation = useMutation({
    mutationFn: async (action: LifecycleAction) => {
      if (action.kind === "rotate") {
        const result = await rotateUserKey(action.keyLabel || "", csrfToken);
        return { kind: action.kind, message: result.message, keys: result.keys };
      }
      if (action.kind === "reset-password") {
        const result = await resetUserPassword(action.user.email, csrfToken);
        return {
          kind: action.kind,
          message: result.message,
          keys: [] as UserOneTimeKey[],
          password: result.initial_password
        };
      }
      if (action.kind === "revoke") {
        const result = await revokeUser(action.user.email, csrfToken);
        return { kind: action.kind, message: result.message, keys: [] as UserOneTimeKey[] };
      }
      const result = await deleteUser(action.user.email, true, csrfToken);
      return { kind: action.kind, message: result.message, keys: [] as UserOneTimeKey[] };
    },
    gcTime: 0,
    onSuccess: async (result, action) => {
      if (result.keys.length || result.password) {
        setSecretReveal({
          kind: action.kind === "rotate" ? "rotated" : "password-reset",
          message: result.message,
          keys: result.keys,
          password: result.password,
          passwordUser: action.user.email
        });
      } else {
        showToast(result.message);
      }
      lifecycleMutation.reset();
      await refreshAfterMutation();
    },
    onError: (error) => showToast(errorMessage(error), "error"),
    onSettled: () => {
      lifecycleSubmitRef.current = false;
    }
  });
  const quotaActionMutation = useMutation({
    mutationFn: (input: UserQuotaActionInput) => applyUserQuotaAction(input, csrfToken),
    onSuccess: async (result) => {
      setQuotaAction(null);
      setQuotaUser(null);
      setRestoreQuotaUsers(null);
      setSelectedUsers([]);
      showToast(result.message);
      await refreshAfterMutation();
    }
  });

  const catalog = users.data;
  const pageUsers = catalog?.users ?? [];
  useEffect(() => {
    const visible = new Set(pageUsers.map((user) => user.email));
    setSelectedUsers((current) => {
      const next = current.filter((email) => visible.has(email));
      return next.length === current.length && next.every((email, index) => email === current[index])
        ? current
        : next;
    });
  }, [pageUsers]);
  const allSelected = pageUsers.length > 0 && pageUsers.every((user) => selectedUsers.includes(user.email));
  const partiallySelected = !allSelected && pageUsers.some((user) => selectedUsers.includes(user.email));
  const toggleSelected = useCallback((email: string, checked: boolean) => {
    setSelectedUsers((current) => checked
      ? Array.from(new Set([...current, email]))
      : current.filter((item) => item !== email));
  }, []);
  const toggleExpanded = useCallback((email: string) => {
    setExpandedUsers((current) => current.includes(email) ? [] : [email]);
  }, []);
  const changeSort = useCallback((field: UserSortField) => {
    setSortDirection((currentDirection) => (
      sortField === field
        ? (currentDirection === "asc" ? "desc" : "asc")
        : (field === "email" ? "asc" : "desc")
    ));
    setSortField(field);
    setPage(1);
    setExpandedUsers([]);
  }, [sortField]);

  const columns = useMemo(() => userColumns({
    page,
    pageSize,
    selectedUsers,
    allSelected,
    partiallySelected,
    sortField,
    sortDirection,
    usageWindow,
    onSort: changeSort,
    onSelectPage: (checked) => setSelectedUsers(checked ? pageUsers.map((user) => user.email) : []),
    onSelect: toggleSelected,
    onTeam: (user) => setAssignment({
      users: [user.email],
      targetTeamID: user.team_id
    }),
    onQuota: (user) => setQuotaUser(user.email)
  }), [
    allSelected,
    changeSort,
    page,
    pageSize,
    pageUsers,
    partiallySelected,
    selectedUsers,
    sortDirection,
    sortField,
    usageWindow,
    toggleSelected
  ]);
  const teamOptions = [
    { value: "unassigned", label: t("admin.ungrouped") },
    ...(catalog?.teams ?? []).map((team) => ({ value: team.id, label: team.name }))
  ];
  const rangeUpdating = users.isFetching && (users.isPlaceholderData || !catalog);
  const rangeBoundary = (timestamp: number | null | undefined, unbounded = false) => {
    if (rangeUpdating) return "…";
    if (!catalog || users.isPlaceholderData) return "—";
    if (timestamp == null || timestamp <= 0) return unbounded ? t("admin.unlimited") : "—";
    return formatLastUsed(timestamp);
  };
  const total = catalog?.pagination.total ?? 0;
  const selectedTeamUsage = teamUsage.data?.teams.find((team) => team.id === teamID) ?? null;
  const totalPages = Math.max(1, catalog?.pagination.total_pages ?? 1);
  const startIndex = total ? (page - 1) * pageSize + 1 : 0;
  const endIndex = Math.min(page * pageSize, total);

  return (
    <section className="page-content legacy-user-page">
      <div className="legacy-user-management-panel">
        <div className="management-toolbar user-management-toolbar user-time-filter-toolbar">
          <ManagementUsageTimeFilter
            value={usageWindow} options={usageWindowOptions} label={t("admin.user_usage")}
            onChange={(value) => { setUsageWindow(value); setPage(1); setExpandedUsers([]); }}
            onCustomSelect={() => setCustomRangeOpen(true)}
            start={rangeBoundary(catalog?.window_start_at, usageWindow === "all")}
            end={rangeBoundary(catalog?.window_end_at)} updating={rangeUpdating}
          />
          <div className="user-toolbar-actions management-toolbar-controls">
            <div className="management-filter-grid user-filter-grid">
              <div className="user-filter-field user-search-filter-field">
                <label htmlFor="user-search-filter">{t("common.user")}</label>
                <label className="search-field user-search-input">
                  <span aria-hidden="true">⌕</span>
                  <input
                    id="user-search-filter"
                    type="search"
                    aria-label={t("admin.search_users")}
                    placeholder={t("admin.search_user_emails")}
                    value={searchDraft}
                    onChange={(event) => setSearchDraft(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key !== "Enter") return;
                      event.preventDefault();
                      setQuery(event.currentTarget.value.trim());
                      setPage(1);
                      setExpandedUsers([]);
                    }}
                  />
                </label>
              </div>
              <label className="window-field filter-field user-filter-field user-team-filter-field">
                <span>{t("admin.team_2")}</span>
                <LegacyEnhancedSelect
                  id="user-team-filter"
                  label={t("admin.team_2")}
                  value={teamID}
                  options={[{ value: "", label: t("admin.all_teams") }, ...teamOptions]}
                  onChange={(value) => {
                    setTeamID(value);
                    setPage(1);
                    setExpandedUsers([]);
                  }}
                />
              </label>
            </div>
            <div className="management-primary-actions user-primary-actions">
              <Button type="primary" onClick={() => {
                createMutation.reset();
                setCreateOpen(true);
              }}>{t("admin.add_user")}</Button>
            </div>
          </div>
        </div>

        {catalog?.collector.status && catalog.collector.status !== "healthy" ? (
          <div className="notice user-usage-notice" role="status">
            {catalog.collector.status === "starting" ? t("admin.the_usage_collector_is_starting") : t("admin.usage_collection_is_unavailable_user_management_is_unaffected")}
          </div>
        ) : null}

        {selectedUsers.length ? (
          <div className="user-selection-bar">
            <div className="user-selection-summary">
              <span className="selection-count">{t("admin.selected")} {selectedUsers.length} {t("admin.users_3")}</span>
              <small>{t("admin.bulk_actions_affect_only_selected_users_and_preserve_raw_usage")}</small>
            </div>
            <div className="user-selection-actions">
              <button className="button ghost" type="button" onClick={() => setSelectedUsers([])}>{t("admin.clear_selection")}</button>
              <button className="button secondary" type="button" onClick={() => setAssignment({ users: selectedUsers, targetTeamID: null })}>{t("admin.assign_team")}</button>
              <button className="button secondary" type="button" onClick={() => {
                quotaActionMutation.reset();
                setRestoreQuotaUsers([...selectedUsers]);
              }}>{t("admin.restore_organization_default")}</button>
              <button className="button danger-outline" type="button" onClick={() => setQuotaAction({
                action: "reset_usage",
                scope: "selected",
                users: selectedUsers
              })}>{t("admin.reset_weekly_usage")}</button>
            </div>
          </div>
        ) : null}

        <div className={"legacy-user-table-state" + (total ? "" : " is-empty")}>
          <AdminTable<UserSummary>
            rowKey="email"
            className="user-legacy-table"
            columns={columns}
            dataSource={pageUsers}
            loading={users.isPending && !catalog}
            minWidth="100%"
            fillAvailable
            tableLayout="fixed"
            size="small"
            pagination={false}
            locale={{ emptyText: <span className="user-empty-placeholder" aria-hidden="true" /> }}
            rowClassName={(user) => expandedUsers.includes(user.email) ? "user-summary-row expanded" : "user-summary-row"}
            onRow={(user) => ({
              tabIndex: 0,
              "data-user-row": user.email,
              "aria-expanded": expandedUsers.includes(user.email),
              onClick: (event) => {
                if (!isInteractiveRowTarget(event.target)) toggleExpanded(user.email);
              },
              onKeyDown: (event) => {
                if ((event.key === "Enter" || event.key === " ") && !isInteractiveRowTarget(event.target)) {
                  event.preventDefault();
                  toggleExpanded(user.email);
                }
              }
            })}
            expandable={{
              expandedRowKeys: expandedUsers,
              showExpandColumn: false,
              expandedRowClassName: () => "user-detail-row",
              expandedRowRender: (user) => expandedUsers.includes(user.email) ? (
                <UserExpandedRow
                  user={user}
                  range={usageRange}
                  csrfToken={csrfToken}
                  onTeam={() => setAssignment({
                    users: [user.email],
                    targetTeamID: user.team_id
                  })}
                  onQuota={() => setQuotaUser(user.email)}
                  onLifecycle={setLifecycleAction}
                />
              ) : null
            }}
          />

          {!users.isPending && !users.isError && !total ? (
            <div className="user-empty-state">
              <div className="empty-icon" aria-hidden="true">◎</div>
              <h3>{query || teamID ? t("admin.no_matching_users") : t("admin.no_users_yet")}</h3>
              <p>{query || teamID ? t("admin.adjust_your_search") : t("admin.adding_an_email_creates_a_unified_api_key_and_links")}</p>
              <Button type="primary" onClick={() => setCreateOpen(true)}>{t("admin.add_first_user")}</Button>
            </div>
          ) : null}

          {total ? (
            <div className="table-pagination user-pagination" aria-label={t("admin.user_pagination")}>
              <span className="pagination-summary">{t("admin.total")} {formatNumber(total)} {t("admin.users_4")} {formatNumber(startIndex)}–{formatNumber(endIndex)}</span>
              <div className="pagination-actions">
                <label className="pagination-size">
                  <span>{t("admin.per_page")}</span>
                  <LegacyEnhancedSelect
                    label={t("admin.rows_per_page")}
                    value={String(pageSize)}
                    options={[25, 50, 100].map((value) => ({ value: String(value), label: String(value) }))}
                    onChange={(nextValue) => {
                      setPageSize(Number(nextValue));
                      setPage(1);
                      setExpandedUsers([]);
                    }}
                  />
                  <span>{t("admin.rows")}</span>
                </label>
                <nav className="pagination-controls" aria-label={t("admin.user_list_pages")}>
                  <Button disabled={page <= 1} onClick={() => {
                    setPage((current) => Math.max(1, current - 1));
                    setExpandedUsers([]);
                  }}>{t("admin.previous")}</Button>
                  <div className="pagination-pages">
                    {paginationItems(page, totalPages).map((item, index) => (
                      item === "…"
                        ? <span key={"ellipsis-" + index} className="pagination-ellipsis" aria-hidden="true">…</span>
                        : <Button
                            key={item}
                            className={item === page ? "pagination-page active" : "pagination-page"}
                            aria-current={item === page ? "page" : undefined}
                            onClick={() => {
                              setPage(item);
                              setExpandedUsers([]);
                            }}
                          >{item}</Button>
                    ))}
                  </div>
                  <Button disabled={page >= totalPages} onClick={() => {
                    setPage((current) => Math.min(totalPages, current + 1));
                    setExpandedUsers([]);
                  }}>{t("admin.next")}</Button>
                </nav>
              </div>
            </div>
          ) : null}
        </div>
      </div>

      <CustomUsageRangeModal
        open={customRangeOpen}
        title={t("common.select_time_range")}
        timezone={getSiteTimezone()}
        range={customRange}
        onCancel={() => setCustomRangeOpen(false)}
        onApply={(range) => {
          setCustomRange(range);
          setUsageWindow("custom");
          setCustomRangeOpen(false);
          setPage(1);
          setExpandedUsers([]);
        }}
      />
      <UserAssignmentModal
        assignment={assignment}
        teams={teamOptions}
        pending={assignmentMutation.isPending}
        error={assignmentMutation.error}
        onCancel={() => setAssignment(null)}
        onChange={(targetTeamID) => setAssignment((current) => current ? { ...current, targetTeamID } : null)}
        onSubmit={() => assignment && assignmentMutation.mutate(assignment)}
      />
      <CreateUserModal
        open={createOpen}
        teams={teamOptions}
        initialTeamID={catalog?.teams.some((team) => team.id === teamID) ? teamID : ""}
        emailDomains={siteConfiguration.data?.allowed_email_domains}
        emailDomainsLoading={siteConfiguration.isPending || siteConfiguration.isFetching}
        emailDomainsFailed={siteConfiguration.isError}
        onRetryEmailDomains={() => { void siteConfiguration.refetch(); }}
        pending={createMutation.isPending}
        error={createMutation.error}
        onCancel={() => setCreateOpen(false)}
        onSubmit={(input) => createMutation.mutate(input)}
      />
      <UserLifecycleConfirm
        action={lifecycleAction}
        pending={lifecycleMutation.isPending}
        onCancel={() => setLifecycleAction(null)}
        onConfirm={() => {
          if (!lifecycleAction || lifecycleSubmitRef.current) return;
          lifecycleSubmitRef.current = true;
          const action = lifecycleAction;
          setLifecycleAction(null);
          lifecycleMutation.mutate(action);
        }}
      />
      <SecretRevealModal value={secretReveal} onClose={() => setSecretReveal(null)} />
      <UserQuotaDrawer
        user={quotaUser}
        summaryQuota={pageUsers.find((candidate) => candidate.email === quotaUser)?.weekly_quota ?? null}
        csrfToken={csrfToken}
        onClose={() => setQuotaUser(null)}
        onSaved={async (message) => {
          showToast(message);
          await refreshAfterMutation();
        }}
        onFailed={(message) => showToast(message, "error")}
        onAction={(action) => setQuotaAction(action)}
      />
      <QuotaActionModal
        draft={quotaAction}
        users={pageUsers}
        pending={quotaActionMutation.isPending}
        error={quotaActionMutation.error}
        onCancel={() => setQuotaAction(null)}
        onSubmit={(input) => quotaActionMutation.mutate(input)}
      />
      <LegacyConfirmModal
        title={t("admin.restore_the_organization_quota_default_for_users", [restoreQuotaUsers?.length ?? 0])}
        open={restoreQuotaUsers !== null}
        okText={t("admin.restore_organization_default")}
        pending={quotaActionMutation.isPending}
        onCancel={() => {
          if (!quotaActionMutation.isPending) {
            setRestoreQuotaUsers(null);
            quotaActionMutation.reset();
          }
        }}
        onConfirm={() => {
          if (!restoreQuotaUsers) return;
          const usersToRestore = restoreQuotaUsers;
          setRestoreQuotaUsers(null);
          quotaActionMutation.mutate({
            action: "restore_default",
            scope: "selected",
            users: usersToRestore,
            confirm: "restore_default"
          }, {
            onError: (error) => showToast(errorMessage(error), "error")
          });
        }}
      >
        <>
          {restoreQuotaUsers && pageUsers.filter((user) => (
            restoreQuotaUsers.includes(user.email) && user.weekly_quota.policy_mode !== "inherit"
          )).length
            ? t("admin.personal_quota_policies_will_be_removed_for_the_selected_users")
            : t("admin.the_selected_users_already_inherit_the_organization_default_this_week")}
        </>
      </LegacyConfirmModal>
      <TeamUsageDrawer
        open={teamUsageOpen}
        team={selectedTeamUsage}
        range={usageRange}
        onClose={() => setTeamUsageOpen(false)}
      />
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function userColumns({
  page,
  pageSize,
  selectedUsers,
  allSelected,
  partiallySelected,
  sortField,
  sortDirection,
  usageWindow,
  onSort,
  onSelectPage,
  onSelect,
  onTeam,
  onQuota
}: {
  page: number;
  pageSize: number;
  selectedUsers: string[];
  allSelected: boolean;
  partiallySelected: boolean;
  sortField: UserSortField;
  sortDirection: SortDirection;
  usageWindow: UsageWindow;
  onSort: (field: UserSortField) => void;
  onSelectPage: (checked: boolean) => void;
  onSelect: (email: string, checked: boolean) => void;
  onTeam: (user: UserSummary) => void;
  onQuota: (user: UserSummary) => void;
}): TableColumnsType<UserSummary> {
  const sortable = (field: UserSortField, label: string) => ({
    title: (
      <button
        className={"legacy-sort-button" + (sortField === field ? " active" : "")}
        type="button"
        aria-label={sortField === field
          ? label + t("admin.currently") + (sortDirection === "asc" ? t("common.ascending") : t("common.descending")) + t("admin.click_to_reverse_the_sort_order")
          : label + t("common.click_to_sort")}
        onClick={(event) => {
          event.stopPropagation();
          onSort(field);
        }}
      >{label}</button>
    ),
    onHeaderCell: () => {
      const ariaSort: "ascending" | "descending" | "none" = sortField === field
        ? (sortDirection === "asc" ? "ascending" : "descending")
        : "none";
      return { "aria-sort": ariaSort };
    }
  });
  return [
    {
      title: t("common.no"),
      key: "index",
      className: "table-index-column",
      width: "4%",
      render: (_, _user, index) => <span className="table-index-cell">{(page - 1) * pageSize + index + 1}</span>
    },
    {
      title: (
        <IndeterminateCheckbox
          ariaLabel={t("admin.select_users_on_this_page")}
          checked={allSelected}
          indeterminate={partiallySelected}
          onChange={onSelectPage}
        />
      ),
      key: "select",
      className: "user-select-column",
      width: "3%",
      render: (_, user) => (
        <input
          type="checkbox"
          aria-label={t("admin.select") + user.email}
          checked={selectedUsers.includes(user.email)}
          onClick={(event) => event.stopPropagation()}
          onChange={(event) => onSelect(user.email, event.target.checked)}
        />
      )
    },
    {
      title: null,
      key: "toggle",
      className: "user-toggle-column",
      width: "3%",
      onHeaderCell: () => ({ "aria-label": t("admin.expand") }),
      render: () => <span className="user-chevron" aria-hidden="true">›</span>
    },
    {
      ...sortable("email", t("common.user")),
      key: "email",
      width: "15%",
      render: (_, user) => (
        <>
          <span className="table-primary">{user.email}</span>
          <span className="table-secondary">{user.total_records} {t("admin.historical_records")}</span>
        </>
      )
    },
    {
      title: t("admin.team_2"),
      key: "team",
      width: "9%",
      render: (_, user) => (
        <button
          className="classification-button"
          type="button"
          aria-label={t("admin.set") + user.email + t("admin.s_team")}
          onClick={(event) => {
            event.stopPropagation();
            onTeam(user);
          }}
        >
          <span className={teamTagClassName(user.team?.tag_style, !user.team)}>
            {user.team?.name ?? t("admin.ungrouped")}
          </span>
        </button>
      )
    },
    {
      title: t("admin.status_2"),
      key: "status",
      width: "6%",
      render: (_, user) => (
        <span className={"status-chip " + (user.status === "active" ? "success" : "neutral")}>
          {statusLabel(user.status)}
        </span>
      )
    },
    {
      title: "CPA",
      key: "accounts",
      width: "6%",
      render: (_, user) => <UserCoverage user={user} />
    },
    {
      ...sortable("requests", t("admin.requests")),
      key: "requests",
      className: "number-cell",
      width: "7%",
      render: (_, user) => (
        <>
          {formatNumber(user.usage.request_count)}
          {user.usage.failed_count ? <span className="usage-failed">{formatNumber(user.usage.failed_count)} {t("common.failed")}</span> : null}
        </>
      )
    },
    {
      ...sortable("tokens", t("admin.token_usage_3")),
      key: "tokens",
      className: "user-token-column user-token-cell",
      width: "14%",
      render: (_, user) => <UserTokenCell user={user} window={usageWindow} />
    },
    {
      ...sortable("quota", t("admin.weekly_quota_status")),
      key: "quota",
      className: "user-quota-column",
      width: "23%",
      render: (_, user) => <UserQuotaCell user={user} onOpen={() => onQuota(user)} />
    },
    {
      ...sortable("last_used", t("common.last_used")),
      key: "last-used",
      width: "10%",
      render: (_, user) => <UserLastUsed timestamp={user.usage.last_used_at} />
    }
  ];
}

function IndeterminateCheckbox({
  ariaLabel,
  checked,
  indeterminate,
  onChange
}: {
  ariaLabel: string;
  checked: boolean;
  indeterminate: boolean;
  onChange: (checked: boolean) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (input.current) input.current.indeterminate = indeterminate;
  }, [indeterminate]);
  return (
    <input
      ref={input}
      type="checkbox"
      aria-label={ariaLabel}
      checked={checked}
      onClick={(event) => event.stopPropagation()}
      onChange={(event) => onChange(event.target.checked)}
    />
  );
}

function UserCoverage({ user }: { user: UserSummary }) {
  const slots = Math.min(12, user.account_count);
  const activeSlots = user.account_count
    ? Math.round(slots * user.active_accounts / user.account_count)
    : 0;
  return (
    <span className="user-coverage">
      <span className="coverage" aria-hidden="true">
        {Array.from({ length: slots }, (_, index) => <i key={index} className={index < activeSlots ? "active" : ""} />)}
      </span>
      <span className="user-coverage-count">{user.active_accounts}/{user.account_count}</span>
    </span>
  );
}

function UserTokenCell({ user, window }: { user: UserSummary; window: UsageWindow }) {
  return (
    <div className="user-token-summary">
      <div className="user-token-stat user-token-weighted">
        <span>{t("common.weighted", [usageWindowLabel(window)])}</span>
        <LegacyTokenValue value={user.usage.weighted_tokens} />
      </div>
      <div className="user-token-stat user-token-current">
        <span>{t("common.unweighted", [usageWindowLabel(window)])}</span>
        <LegacyTokenValue value={user.usage.total_tokens} />
      </div>
    </div>
  );
}

function UserQuotaCell({ user, onOpen }: { user: UserSummary; onOpen: () => void }) {
  const quota = user.weekly_quota;
  if (!quota.period) return <span className="quota-unavailable">{t("admin.unavailable")}</span>;
  const weightedUsed = quota.weighted_used_tokens ?? quota.used_tokens;
  const rawUsed = quota.raw_used_tokens ?? 0;
  const progress = Math.min(100, Math.max(0, Number(quota.used_percent) || 0));
  const adjustments = [
    quota.bonus_tokens > 0 ? t("admin.bonus_this_week") + tokenText(quota.bonus_tokens) : "",
    quota.usage_reset_tokens > 0 ? t("admin.reset_this_week") + tokenText(quota.usage_reset_tokens) : ""
  ].filter(Boolean);
  return (
    <div className="user-quota-cell">
      <div className="user-quota-primary">
        <span className="user-quota-source">{quotaSourceLabel(quota)}</span>
        <strong>{t("admin.limit")} {quota.unlimited ? t("common.unlimited") : tokenText(quota.limit_tokens)}</strong>
        <button
          className="inline-action"
          type="button"
          onClick={(event) => {
            event.stopPropagation();
            onOpen();
          }}
        >{t("admin.settings")}</button>
      </div>
      <div className="user-quota-meter-copy">
        <span>{t("admin.weighted_usage_this_week_2")}</span>
        <LegacyTokenValue value={weightedUsed} />
      </div>
      {quota.unlimited ? null : (
        <progress aria-label={t("admin.weekly_quota_usage")} className="user-quota-progress" max={100} value={progress} />
      )}
      <div className="user-quota-progress-copy">
        <span>{quota.unlimited ? t("admin.no_percentage_limit") : t("admin.used_2") + formatPercent(quota.used_percent)}</span>
        <span>{quota.unlimited ? t("admin.unlimited_remaining") : t("admin.remaining") + tokenText(quota.remaining_tokens)}</span>
      </div>
      <div className="user-quota-raw-copy"><span>{t("admin.raw_tokens_this_week")}</span><LegacyTokenValue value={rawUsed} /></div>
      {adjustments.length ? <span className="user-quota-adjustment-copy">{adjustments.join(" · ")}</span> : null}
    </div>
  );
}

function LegacyTokenValue({ value }: { value: number | null | undefined }) {
  const amount = Number(value) || 0;
  const [formatted, unit = "Token"] = formatTokenAmount(amount).split(" ");
  const compacted = Math.abs(amount) >= 1_000;
  return (
    <span className="token-usage">
      <span className="token-usage-main" aria-hidden="true">
        <span className="token-usage-value">{formatted}</span>
        <small className="token-usage-unit">{unit}</small>
      </span>
      {compacted ? <small className="token-usage-exact" aria-hidden="true">{formatNumber(amount)} Token</small> : null}
      <span className="token-usage-sr-only">{formatNumber(amount)} Token</span>
    </span>
  );
}

function UserExpandedRow({
  user,
  range,
  csrfToken,
  onTeam,
  onQuota,
  onLifecycle
}: {
  user: UserSummary;
  range: UsageRange;
  csrfToken: string;
  onTeam: () => void;
  onQuota: () => void;
  onLifecycle: (action: LifecycleAction) => void;
}) {
  void csrfToken;
  const [moreActionsOpen, setMoreActionsOpen] = useState(false);
  const [analysisAccount, setAnalysisAccount] = useState("");
  const [accountSort, setAccountSort] = useState<{ field: UserAccountSortField; direction: SortDirection }>({
    field: "total_tokens",
    direction: "desc"
  });
  const detailParams = { window: String(range.window), startAt: range.startAt, endAt: range.endAt };
  const detail = useQuery({
    queryKey: userDetailQueryKey(user.email, detailParams),
    queryFn: ({ signal }) => readUserDetail(user.email, detailParams, signal),
    staleTime: 30_000,
    gcTime: 30_000,
    retry: false
  });
  const breakdownRange = { ...range, account: analysisAccount || undefined };
  const breakdown = useQuery({
    queryKey: usageBreakdownQueryKey("user", user.email, breakdownRange),
    queryFn: ({ signal }) => readUsageBreakdown("user", user.email, breakdownRange, signal),
    staleTime: 30_000,
    gcTime: 30_000,
    retry: false
  });
  if (detail.isPending) {
    return (
      <div className="user-detail-panel">
        <div className="account-model-usage-skeleton" aria-label={t("admin.loading_user_details")}>
          <span />
          <span />
        </div>
      </div>
    );
  }
  if (detail.isError || !detail.data) {
    return (
      <div className="user-detail-panel">
        <div className="account-model-usage-message error" role="alert">
          <span>{errorMessage(detail.error)}</span>
          <button className="inline-action" type="button" onClick={() => void detail.refetch()}>{t("common.retry")}</button>
        </div>
      </div>
    );
  }
  const detailedUser = detail.data.user;
  const accounts = [...detailedUser.accounts].sort((left, right) => compareRows(
    accountSortValue(left, accountSort.field),
    accountSortValue(right, accountSort.field),
    accountSort.direction,
    left.account,
    right.account
  ));
  const keyLabel = detailedUser.accounts.find((account) => account.key)?.key?.label;
  return (
    <div className="user-detail-panel">
      <UserUsageAnalysis
        accounts={detailedUser.accounts.map((account) => account.account)}
        accountFilter={analysisAccount}
        onAccountFilter={setAnalysisAccount}
        query={breakdown.data}
        pending={breakdown.isPending}
        error={breakdown.error}
        onRetry={() => void breakdown.refetch()}
      />
      <div className="usage-analysis-title">
        <strong>{t("admin.cpa_account_usage_analysis")}</strong>
      </div>
      <NativeTableViewport className="user-account-table-wrap" aria-label={t("admin.user_account_details_table")}>
        <table className="user-account-table">
          <thead>
            <tr>
              <th className="table-index-column">{t("common.no")}</th>
              <LegacyNativeSortHeader label={t("common.cpa_account")} field="account" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("admin.key_status")} field="status" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("admin.count")} field="requests" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("common.input_tokens")} field="input_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("common.output_tokens")} field="output_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("common.reasoning_tokens")} field="reasoning_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("common.cached_tokens")} field="cached_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("common.raw_tokens")} field="total_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("common.weighted_tokens_2")} field="weighted_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label={t("common.last_used")} field="last_used_at" sort={accountSort} onSort={setAccountSort} />
            </tr>
          </thead>
          <tbody>
            {accounts.map((account, index) => (
              <tr key={account.account}>
                <td className="table-index-cell">{index + 1}</td>
                <td><span className="table-primary">{account.account}</span></td>
                <td><span className={"status-chip " + statusTone(account.status)}>{statusLabel(account.status)}</span></td>
                <td className="number-cell">{formatNumber(account.usage.request_count)}</td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.input_tokens} /></td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.output_tokens} /></td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.reasoning_tokens} /></td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.cached_tokens} /></td>
                <td className="number-cell token-total"><LegacyTokenValue value={account.usage.total_tokens} /></td>
                <td className="number-cell token-total"><LegacyTokenValue value={account.usage.weighted_tokens} /></td>
                <td><UserLastUsed timestamp={account.usage.last_used_at} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </NativeTableViewport>
      <div className="user-detail-actions" role="group" aria-label={t("admin.user_actions")}>
        <Button onClick={onTeam}>{t("admin.set_team")}</Button>
        <Button onClick={onQuota}>{t("admin.configure_weekly_quota")}</Button>
        <Button onClick={() => onLifecycle({ kind: "reset-password", user })}>{t("admin.reset_password")}</Button>
        <Dropdown
          trigger={["click"]}
          placement="topRight"
          open={moreActionsOpen}
          onOpenChange={setMoreActionsOpen}
          autoFocus
          destroyOnHidden
          classNames={{ root: "user-detail-actions-menu" }}
          menu={{
            "aria-label": t("admin.more_user_actions"),
            items: [
              ...(user.active_keys && keyLabel ? [
                { key: "rotate", label: t("admin.rotate_unified_key") },
                { key: "revoke", label: t("admin.disable_unified_key"), danger: true },
                { type: "divider" as const }
              ] : []),
              { key: "delete", label: t("admin.delete_user"), danger: true }
            ],
            onClick: ({ key }) => {
              setMoreActionsOpen(false);
              if (key === "rotate" && user.active_keys && keyLabel) onLifecycle({ kind: "rotate", user, keyLabel });
              if (key === "revoke" && user.active_keys && keyLabel) onLifecycle({ kind: "revoke", user });
              if (key === "delete") onLifecycle({ kind: "delete", user });
            }
          }}
        >
          <Button aria-haspopup="menu" aria-expanded={moreActionsOpen}>{t("admin.more_actions")} <DownOutlined aria-hidden="true" /></Button>
        </Dropdown>
      </div>
    </div>
  );
}

function UserUsageAnalysis({
  accounts,
  accountFilter,
  onAccountFilter,
  query,
  pending,
  error,
  onRetry
}: {
  accounts: string[];
  accountFilter: string;
  onAccountFilter: (account: string) => void;
  query: UsageBreakdown | undefined;
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  const [selectedEffort, setSelectedEffort] = useState<UserModelEffortSelection | null>(null);
  const header = () => (
    <div className="usage-analysis-header">
      <div className="usage-analysis-title">
        <strong>{t("admin.model_reasoning_analysis")}</strong>
      </div>
      <div className="usage-analysis-filter">
        <LegacyEnhancedSelect
          label="CPA"
          value={accountFilter}
          options={[
            { value: "", label: t("common.all_cpas") },
            ...accounts.map((account) => ({ value: account, label: account }))
          ]}
          onChange={(account) => {
            setSelectedEffort(null);
            onAccountFilter(account);
          }}
        />
      </div>
    </div>
  );
  if (pending && !query) {
    return (
      <section className="user-usage-analysis">
        {header()}
        <div className="usage-analysis-skeleton" aria-label={t("admin.loading_model_analysis")}><span /><span /><span /></div>
      </section>
    );
  }
  if (!query) {
    return (
      <section className="user-usage-analysis">
        {header()}
        <div className="usage-analysis-message error" role="alert">
          <strong>{t("admin.unable_to_load_model_analysis")}</strong>
          <span>{errorMessage(error)}</span>
          <button className="inline-action" type="button" onClick={onRetry}>{t("common.retry")}</button>
        </div>
      </section>
    );
  }
  if (!query.collection_started_at) {
    return (
      <section className="user-usage-analysis">
        {header()}
        <div className="usage-analysis-message">
          <strong>{t("admin.waiting_for_statistics")}</strong>
          <span>{t("admin.models_and_reasoning_effort_are_recorded_from_the_moment_the")}</span>
        </div>
      </section>
    );
  }
  const successCount = query.totals.success_count ?? 0;
  const failedCount = query.totals.failed_count ?? 0;
  const totalWeighted = query.totals.weighted_tokens ?? query.totals.total_tokens;
  const models = groupUserModels(query.combinations);
  const summary = (
    <div className="usage-analysis-summary">
      <div className="usage-analysis-call-stat">
        <span>{t("admin.successful_calls")}</span><strong>{formatNumber(successCount)}</strong>
        <span>{t("admin.failed_calls")}</span><strong className="usage-analysis-failed-count">{formatNumber(failedCount)}</strong>
      </div>
      <div><span>{t("admin.effort_coverage")}</span><strong>{formatUsageRatio(query.totals.known_effort_count ?? 0, successCount)}</strong></div>
      <div className="usage-analysis-token-stat"><span>{t("common.raw_tokens")}</span><strong><LegacyTokenValue value={query.totals.total_tokens} /></strong></div>
      <div className="usage-analysis-token-stat"><span>{t("common.weighted_tokens_2")}</span><strong><LegacyTokenValue value={totalWeighted} /></strong></div>
      <div className="usage-analysis-time-stat"><span>{t("admin.tracking_since")}</span><strong>{formatSiteTimestamp(query.collection_started_at)}</strong></div>
    </div>
  );
  if (!successCount) {
    return (
      <section className="user-usage-analysis">
        {header()}
        {summary}
        <div className="usage-analysis-message compact">
          <strong>{t("admin.no_successful_calls_in_this_range")}</strong>
          <span>{failedCount ? t("admin.failed_calls_are_excluded_from_the_shares", [formatNumber(failedCount)]) : t("admin.model_and_reasoning_effort_combinations_will_appear_after_new_calls")}</span>
        </div>
      </section>
    );
  }
  return (
    <section className="user-usage-analysis">
      {header()}
      {summary}
      <NativeTableViewport className="usage-model-table-wrap" aria-label={t("admin.model_usage_table")}>
        <table className="usage-model-table">
          <thead><tr><th className="table-index-column">{t("common.no")}</th><th>{t("common.model")}</th><th>{t("admin.usage_2")}</th><th>{t("admin.reasoning_effort_mix")}</th><th>{t("admin.token_details")}</th><th>{t("common.calls")}</th></tr></thead>
          <tbody>
            {models.map((model, index) => (
              <tr key={model.model}>
                <td className="table-index-cell">{index + 1}</td>
                <td><span className="table-primary model-name">{model.model}</span></td>
                <td className="number-cell"><LegacyTokenValue value={model.totalTokens} /></td>
                <td><UserModelEffortProgress model={model} onSelect={setSelectedEffort} /></td>
                <td>
                  <dl className="usage-model-token-details">
                    <div><dt>{t("admin.input")}</dt><dd><LegacyTokenValue value={model.inputTokens} /></dd></div>
                    <div><dt>{t("admin.output")}</dt><dd><LegacyTokenValue value={model.outputTokens} /></dd></div>
                    <div><dt>{t("admin.reasoning")}</dt><dd><LegacyTokenValue value={model.reasoningTokens} /></dd></div>
                    <div><dt>{t("admin.cached")}</dt><dd><LegacyTokenValue value={model.cachedTokens} /></dd></div>
                  </dl>
                </td>
                <td className="number-cell">{formatNumber(model.successCount)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </NativeTableViewport>
      {error ? <div className="usage-analysis-stale">{t("admin.refresh_failed_showing_the_last_successful_data")}{errorMessage(error)}</div> : null}
      {selectedEffort ? (
        <UserModelAccountDrawer
          key={JSON.stringify(selectedEffort)}
          selection={selectedEffort}
          query={query}
          error={error}
          pending={pending}
          onRetry={onRetry}
          onClose={() => setSelectedEffort(null)}
        />
      ) : null}
    </section>
  );
}

type UserModelEffortSelection = { model: string; effort: string };

function userUsageModelName(model: string) {
  return model && model !== "unknown" ? model : t("admin.model_not_reported");
}

function userUsageEffortName(effort: string) {
  return effort && effort !== "unknown" ? effort : t("admin.effort_not_reported");
}

function UserModelAccountDrawer({
  selection,
  query,
  error,
  pending,
  onRetry,
  onClose
}: {
  selection: UserModelEffortSelection;
  query: UsageBreakdown;
  error: unknown;
  pending: boolean;
  onRetry: () => void;
  onClose: () => void;
}) {
  const grouped = new Map<string, UsageCombination>();
  query.combinations.filter((row) => (
    userUsageModelName(row.model) === selection.model
    && userUsageEffortName(row.reasoning_effort) === selection.effort
  )).forEach((row) => {
    const account = row.account || "";
    const current = grouped.get(account);
    grouped.set(account, current ? {
      ...current,
      request_count: current.request_count + row.request_count,
      success_count: current.success_count + row.success_count,
      failed_count: current.failed_count + row.failed_count,
      input_tokens: current.input_tokens + row.input_tokens,
      output_tokens: current.output_tokens + row.output_tokens,
      reasoning_tokens: current.reasoning_tokens + row.reasoning_tokens,
      cached_tokens: current.cached_tokens + row.cached_tokens,
      total_tokens: current.total_tokens + row.total_tokens,
      weighted_tokens: (current.weighted_tokens ?? current.total_tokens) + (row.weighted_tokens ?? row.total_tokens),
      last_used_at: Math.max(current.last_used_at || 0, row.last_used_at || 0)
    } : { ...row, account });
  });
  const rows = [...grouped.values()].sort((left, right) => right.total_tokens - left.total_tokens || String(left.account).localeCompare(String(right.account)));
  const successCount = rows.reduce((total, row) => total + row.success_count, 0);
  const multiplier = (row: UsageCombination) => row.total_tokens > 0 ? (row.weighted_tokens ?? row.total_tokens) / row.total_tokens : 0;
  const average = (row: UsageCombination) => row.success_count > 0 ? Math.round(row.total_tokens / row.success_count) : 0;
  const columns: TableColumnsType<UsageCombination> = [
    { title: t("common.cpa_account"), dataIndex: "account", key: "account", width: 160, sorter: (left, right) => String(left.account).localeCompare(String(right.account)), render: (account: string) => <span className="table-primary">{account || t("admin.cpa_not_reported")}</span> },
    { title: t("common.calls"), dataIndex: "success_count", key: "calls", className: "user-model-account-number", width: 84, align: "right", sorter: (left, right) => left.success_count - right.success_count, render: (count: number) => formatNumber(count) },
    { title: <Tooltip title={t("admin.share_of_successful_calls_for_this_model_and_reasoning_effort")}>{t("admin.call_share")}</Tooltip>, key: "share", className: "user-model-account-number", width: 100, align: "right", render: (_, row) => formatUsageRatio(row.success_count, successCount) },
    { title: t("common.raw_tokens"), dataIndex: "total_tokens", key: "raw", width: 150, align: "right", defaultSortOrder: "descend", sorter: (left, right) => left.total_tokens - right.total_tokens, render: (tokens: number) => <LegacyTokenValue value={tokens} /> },
    { title: t("admin.effective_multiplier"), key: "multiplier", className: "user-model-account-number", width: 96, align: "right", sorter: (left, right) => multiplier(left) - multiplier(right), render: (_, row) => "×" + multiplier(row).toFixed(2) },
    { title: t("common.weighted_tokens_2"), key: "weighted", width: 150, align: "right", sorter: (left, right) => (left.weighted_tokens ?? left.total_tokens) - (right.weighted_tokens ?? right.total_tokens), render: (_, row) => <LegacyTokenValue value={row.weighted_tokens ?? row.total_tokens} /> },
    { title: <span className="user-model-account-column-title">{t("admin.average_call")}<small>{t("common.raw_tokens")}</small></span>, key: "average", width: 144, align: "right", sorter: (left, right) => average(left) - average(right), render: (_, row) => <LegacyTokenValue value={average(row)} /> },
    { title: t("common.last_used"), key: "last_used_at", width: 150, align: "right", sorter: (left, right) => (left.last_used_at || 0) - (right.last_used_at || 0), render: (_, row) => <UserLastUsed timestamp={row.last_used_at} /> }
  ];
  return (
    <Drawer
      className="user-model-account-drawer"
      title={(
        <span className="user-model-account-title">
          <span>{selection.model}</span>{" · "}
          <Tag className={`user-model-effort-tag account-model-effort-${effortColorKey(selection.effort)}`}>{selection.effort}</Tag>
 {t("admin.cpa_usage_distribution")}
        </span>
      )}
      placement="right"
      size="min(1200px, 100vw)"
      open
      onClose={onClose}
      destroyOnHidden
    >
      <div className="user-model-account-toolbar">
        <div className="user-model-account-context">
          <strong>{query.user}</strong>
          <span>{formatLastUsed(query.window_start_at)} — {formatLastUsed(query.window_end_at)}</span>
        </div>
      </div>
      {error ? <div className="usage-analysis-stale" role="alert">{t("admin.refresh_failed_showing_the_last_successful_data")}{errorMessage(error)} <button type="button" className="inline-action" onClick={onRetry}>{t("common.retry")}</button></div> : null}
      <AdminTable
        className="user-model-account-table"
        rowKey={(row) => JSON.stringify(row.account ?? "")}
        columns={columns.map((column) => ({
          ...column,
          sortIcon: ({ sortOrder }: { sortOrder?: "ascend" | "descend" | null }) => (
            <span className={"user-model-account-sort-arrow" + (sortOrder ? " active" : "")} aria-hidden="true">
              {sortOrder === "ascend" ? "↑" : sortOrder === "descend" ? "↓" : "↕"}
            </span>
          )
        }))}
        dataSource={rows}
        minWidth={1134}
        showSorterTooltip={false}
        sortDirections={["descend", "ascend", "descend"]}
        loading={pending}
        emptyText={t("admin.no_cpa_usage_for_this_model_and_reasoning_effort_in")}
      />
    </Drawer>
  );
}

type UserModelEffort = UsageCombination & { sharePercent: number };
type UserModelRow = {
  model: string;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  successCount: number;
  efforts: UserModelEffort[];
};

function groupUserModels(combinations: UsageCombination[]): UserModelRow[] {
  const grouped = new Map<string, UsageCombination[]>();
  combinations.forEach((item) => {
    if (item.total_tokens <= 0) return;
    const model = userUsageModelName(item.model);
    grouped.set(model, [...(grouped.get(model) ?? []), item]);
  });
  return [...grouped.entries()].map(([model, items]) => {
    const effortsByName = new Map<string, UsageCombination>();
    items.forEach((item) => {
      const name = userUsageEffortName(item.reasoning_effort);
      const current = effortsByName.get(name);
      effortsByName.set(name, current ? {
        ...current,
        request_count: current.request_count + item.request_count,
        success_count: current.success_count + item.success_count,
        failed_count: current.failed_count + item.failed_count,
        input_tokens: current.input_tokens + item.input_tokens,
        output_tokens: current.output_tokens + item.output_tokens,
        reasoning_tokens: current.reasoning_tokens + item.reasoning_tokens,
        cached_tokens: current.cached_tokens + item.cached_tokens,
        total_tokens: current.total_tokens + item.total_tokens,
        weighted_tokens: Number(current.weighted_tokens ?? current.total_tokens) + Number(item.weighted_tokens ?? item.total_tokens),
        last_used_at: Math.max(current.last_used_at, item.last_used_at)
      } : { ...item, reasoning_effort: name });
    });
    const efforts = [...effortsByName.values()];
    const totalTokens = efforts.reduce((total, item) => total + item.total_tokens, 0);
    let allocated = 0;
    const normalized = efforts
      .sort((left, right) => right.total_tokens - left.total_tokens || left.reasoning_effort.localeCompare(right.reasoning_effort, getIntlLocale()))
      .map((item, index, sorted) => {
        const sharePercent = index === sorted.length - 1
          ? Math.max(0, 100 - allocated)
          : Math.round(item.total_tokens * 10_000 / totalTokens) / 100;
        allocated += sharePercent;
        return { ...item, sharePercent };
      });
    return {
      model,
      totalTokens,
      inputTokens: efforts.reduce((total, item) => total + item.input_tokens, 0),
      outputTokens: efforts.reduce((total, item) => total + item.output_tokens, 0),
      reasoningTokens: efforts.reduce((total, item) => total + item.reasoning_tokens, 0),
      cachedTokens: efforts.reduce((total, item) => total + item.cached_tokens, 0),
      successCount: efforts.reduce((total, item) => total + item.success_count, 0),
      efforts: normalized
    };
  }).sort((left, right) => right.totalTokens - left.totalTokens || left.model.localeCompare(right.model, getIntlLocale()));
}

function UserModelEffortProgress({ model, onSelect }: { model: UserModelRow; onSelect: (selection: UserModelEffortSelection) => void }) {
  return (
    <div className="account-model-progress" role="group" aria-label={t("common.token_share_by_reasoning_effort", [model.model])}>
      {model.efforts.map((effort) => {
        const tooltip = modelEffortTooltipDetails(model.model, effort);
        const shareUnits = Math.max(1, Math.min(100, Math.round(effort.sharePercent)));
        return (
          <LegacyUsageTooltip key={effort.reasoning_effort} content={tooltip}>
            {(events) => (
              <button
                {...events}
                className={`account-model-progress-segment account-model-effort-${effortColorKey(effort.reasoning_effort)} account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}${effort.sharePercent < 18 ? " compact" : ""}`}
                type="button"
                aria-label={t("admin.view_cpa_usage_for", [model.model, effort.reasoning_effort])}
                aria-haspopup="dialog"
                onClick={() => {
                  events.onBlur();
                  onSelect({ model: model.model, effort: effort.reasoning_effort });
                }}
              >
                <span>{effort.reasoning_effort}</span>
                <em>{formatUsageRatio(effort.total_tokens, model.totalTokens)}</em>
              </button>
            )}
          </LegacyUsageTooltip>
        );
      })}
    </div>
  );
}

type ModelEffortTooltipContent = { title: string; metrics: Array<{ label: string; value: string }> };

function modelEffortTooltipDetails(model: string, effort: UserModelEffort | TeamModelEffort): ModelEffortTooltipContent {
  return {
    title: `${model} · ${effort.reasoning_effort}`,
    metrics: [
      { label: t("common.calls"), value: formatNumber(effort.request_count) },
      { label: t("admin.input"), value: formatNumber(effort.input_tokens) },
      { label: t("admin.output"), value: formatNumber(effort.output_tokens) },
      { label: t("admin.reasoning"), value: formatNumber(effort.reasoning_tokens) },
      { label: t("admin.cached"), value: formatNumber(effort.cached_tokens) },
      { label: t("admin.total_tokens_2"), value: formatNumber(effort.total_tokens) },
      { label: t("common.weighted_tokens_2"), value: formatNumber(effort.weighted_tokens ?? effort.total_tokens) }
    ]
  };
}

function modelEffortTooltipText(content: ModelEffortTooltipContent) {
  return [content.title, ...content.metrics.map(({ label, value }) => `${label}：${value}`)].join("，");
}

type LegacyUsageTooltipEvents = {
  onPointerEnter: (event: { currentTarget: HTMLElement }) => void;
  onPointerLeave: () => void;
  onFocus: (event: { currentTarget: HTMLElement }) => void;
  onBlur: () => void;
};

function LegacyUsageTooltip({
  content,
  children
}: {
  content: ModelEffortTooltipContent;
  children: (events: LegacyUsageTooltipEvents) => ReactNode;
}) {
  const trigger = useRef<HTMLElement | null>(null);
  const layer = useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<{ left: number; top: number } | null>(null);
  const text = modelEffortTooltipText(content);
  const show = (element: HTMLElement) => {
    trigger.current = element;
    setPosition(null);
    setOpen(true);
  };
  const hide = () => setOpen(false);
  useLayoutEffect(() => {
    if (!open || !trigger.current || !layer.current) return;
    const rect = trigger.current.getBoundingClientRect();
    const layerRect = layer.current.getBoundingClientRect();
    setPosition({
      left: Math.min(window.innerWidth - layerRect.width - 12, Math.max(12, rect.left + rect.width / 2 - layerRect.width / 2)),
      top: Math.max(12, rect.top - layerRect.height - 8)
    });
  }, [open, text]);
  const events: LegacyUsageTooltipEvents = {
    onPointerEnter: (event) => show(event.currentTarget),
    onPointerLeave: hide,
    onFocus: (event) => show(event.currentTarget),
    onBlur: hide
  };
  return (
    <>
      {children(events)}
      {open ? createPortal(
        <div
          ref={layer}
          className="user-usage-tooltip-layer"
          role="tooltip"
          aria-label={content.title}
          style={{
            left: position?.left ?? 0,
            top: position?.top ?? 0,
            visibility: position ? "visible" : "hidden"
          }}
        >
          <strong className="user-usage-tooltip-title">{content.title}</strong>
          <dl className="user-usage-tooltip-metrics">
            {content.metrics.map(({ label, value }) => (
              <div key={label}><dt>{label}</dt><dd>{value}</dd></div>
            ))}
          </dl>
        </div>,
        document.body
      ) : null}
    </>
  );
}

function LegacyNativeSortHeader({
  label,
  field,
  sort,
  onSort
}: {
  label: string;
  field: UserAccountSortField;
  sort: { field: UserAccountSortField; direction: SortDirection };
  onSort: (sort: { field: UserAccountSortField; direction: SortDirection }) => void;
}) {
  const active = sort.field === field;
  return (
    <th aria-sort={active ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}>
      <button
        className={"legacy-sort-button" + (active ? " active" : "")}
        type="button"
        onClick={() => onSort({
          field,
          direction: active
            ? (sort.direction === "asc" ? "desc" : "asc")
            : (field === "account" || field === "status" ? "asc" : "desc")
        })}
      >{label}</button>
    </th>
  );
}

function UserAssignmentModal({
  assignment,
  teams,
  pending,
  error,
  onCancel,
  onChange,
  onSubmit
}: {
  assignment: TeamAssignment | null;
  teams: Array<{ value: string; label: string }>;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onChange: (teamID: string | null) => void;
  onSubmit: () => void;
}) {
  return (
    <Modal
      className="legacy-user-form-modal"
      title={<LegacyDialogTitle title={(assignment?.users.length ?? 0) > 1 ? t("admin.bulk_team_assignment") : t("admin.set_team")} kicker="TEAM ASSIGNMENT" subtitle={assignment?.users.length === 1 ? assignment.users[0] : t("admin.selected_2") + (assignment?.users.length ?? 0) + t("admin.users")} />}
      open={assignment !== null}
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={t("admin.save_team")}
      cancelText={t("common.cancel")}
      confirmLoading={pending}
      onCancel={onCancel}
      onOk={onSubmit}
      destroyOnHidden
      mask={{ closable: false }}
    >
      <div className="legacy-user-form-body">
        <label className="field">
          <span>{t("admin.reporting_team")}</span>
          <LegacyEnhancedSelect
            label={t("admin.reporting_team")}
            value={assignment?.targetTeamID ?? ""}
            options={teams.map((team) => ({ value: team.value === "unassigned" ? "" : team.value, label: team.label }))}
            onChange={(nextValue) => onChange(nextValue || null)}
          />
          <small>{t("admin.each_user_can_belong_to_one_team_for_usage_reporting")}</small>
        </label>
        <div className="inline-notice">{t("admin.team_reports_aggregate_tokens_for_the_selected_range_using_current")}</div>
        <LegacyFormError error={error} />
      </div>
    </Modal>
  );
}

function CreateUserModal({
  open,
  teams,
  initialTeamID,
  emailDomains,
  emailDomainsLoading,
  emailDomainsFailed,
  onRetryEmailDomains,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  open: boolean;
  teams: Array<{ value: string; label: string }>;
  initialTeamID: string;
  emailDomains: readonly string[] | undefined;
  emailDomainsLoading: boolean;
  emailDomainsFailed: boolean;
  onRetryEmailDomains: () => void;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (input: { email: string; teamID: string | null }) => void;
}) {
  const [email, setEmail] = useState("");
  const [emailDomain, setEmailDomain] = useState("");
  const [emailError, setEmailError] = useState("");
  const [teamID, setTeamID] = useState("");
  const emailInputRef = useRef<HTMLInputElement>(null);
  const domains = useMemo(() => normalizedEmailDomains(emailDomains), [emailDomains]);
  const selectedDomain = domains.includes(emailDomain) ? emailDomain : domains[0] ?? "";
  const emailDomainsReady = !emailDomainsLoading && !emailDomainsFailed && Boolean(selectedDomain);
  useEffect(() => {
    if (!open) return;
    setEmail("");
    setEmailDomain("");
    setEmailError("");
    setTeamID(initialTeamID);
  }, [initialTeamID, open]);
  const parseFullEmail = (value: string) => {
    const parts = value.trim().split("@");
    if (parts.length !== 2 || !domains.includes(parts[1].toLowerCase())) return null;
    return { localPart: parts[0], domain: parts[1].toLowerCase() };
  };
  const acceptFullEmail = (value: string) => {
    const parsed = parseFullEmail(value);
    if (!parsed) return false;
    setEmail(parsed.localPart);
    setEmailDomain(parsed.domain);
    setEmailError("");
    return true;
  };
  const submit = () => {
    if (pending || !emailDomainsReady) return;
    if (!emailInputRef.current?.reportValidity()) return;
    const parsed = parseFullEmail(email);
    const localPart = parsed?.localPart ?? email.trim();
    if (!/^[a-z0-9.!#$%&'*+/=?^_`{|}~-]+$/i.test(localPart)) {
      setEmailError(email.includes("@") ? t("common.the_email_domain_does_not_match_enter_only_the_username") : t("admin.enter_a_valid_email_username"));
      emailInputRef.current?.focus();
      return;
    }
    setEmailError("");
    onSubmit({ email: `${localPart}@${parsed?.domain ?? selectedDomain}`, teamID: teamID || null });
  };
  return (
    <Modal
      className="legacy-user-form-modal"
      title={<LegacyDialogTitle title={t("admin.add_user")} kicker="NEW USER" />}
      open={open}
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={t("admin.create_user")}
      cancelText={t("common.cancel")}
      okButtonProps={{ disabled: pending || !emailDomainsReady }}
      onCancel={onCancel}
      onOk={submit}
      afterOpenChange={(opened) => {
        if (opened) emailInputRef.current?.focus();
      }}
      destroyOnHidden
      mask={{ closable: false }}
    >
      <div className="legacy-user-form-body">
        <div className="field">
          <span id="new-user-email-label">{t("common.user_email")}</span>
          <div className="user-email-fields" role="group" aria-labelledby="new-user-email-label">
            <input
              type="text"
              inputMode="email"
              ref={emailInputRef}
              aria-label={t("common.email_username")}
              aria-invalid={Boolean(emailError)}
              aria-describedby={emailError ? "new-user-email-error" : undefined}
              placeholder={t("common.enter_username")}
              value={email}
              autoFocus
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              required
              disabled={pending}
              onChange={(event) => {
                setEmail(event.target.value);
                setEmailError("");
              }}
              onBlur={() => acceptFullEmail(email)}
              onPaste={(event) => {
                if (acceptFullEmail(event.clipboardData.getData("text"))) event.preventDefault();
              }}
              onKeyDown={(event) => {
                if (event.key !== "Enter" || event.nativeEvent.isComposing) return;
                event.preventDefault();
                event.stopPropagation();
                submit();
              }}
            />
            <LegacyEnhancedSelect
              id="new-user-email-domain"
              label={t("common.email_domain")}
              value={selectedDomain}
              options={domains.length
                ? domains.map((domain) => ({ value: domain, label: `@${domain}` }))
                : [{ value: "", label: emailDomainsLoading ? t("common.loading_domains") : t("common.no_available_domains") }]}
              disabled={pending || !emailDomainsReady}
              onChange={(domain) => {
                const parts = email.trim().split("@");
                if (parts.length === 2) setEmail(parts[0]);
                setEmailDomain(domain);
                setEmailError("");
              }}
            />
          </div>
          {emailDomainsLoading ? <small role="status">{t("common.loading_organization_email_domains")}</small>
            : emailDomainsFailed ? <small role="alert">{t("common.unable_to_load_email_domains")}<Button type="link" size="small" onClick={onRetryEmailDomains}>{t("common.retry")}</Button></small>
              : !domains.length ? <small role="alert">{t("admin.no_organization_email_domain_is_configured_open")}<Link to="/configuration?section=identity&key=identity.allowed_email_domains" onClick={onCancel}>{t("admin.system_configuration")}</Link>{t("admin.to_configure_one")}</small>
                : null}
          {emailError ? <small className="user-email-error" id="new-user-email-error" role="alert">{emailError}</small> : null}
        </div>
        <label className="field add-user-team-field">
          <span>{t("admin.team_membership_2")}</span>
          <LegacyEnhancedSelect
            label={t("admin.team_membership_2")}
            value={teamID}
            options={teams.map((team) => ({ value: team.value === "unassigned" ? "" : team.value, label: team.label }))}
            onChange={setTeamID}
          />
          <small>{t("admin.optional_teams_are_used_for_usage_reporting_and_do_not")}</small>
        </label>
        <div className="inline-notice">
 {t("admin.a_unified_api_key_is_created_and_the_system_s")} <br />
 {t("admin.the_api_key_is_shown_only_once")} <br />
 {t("admin.the_user_must_change_the_default_password_on_first_sign")} </div>
        <LegacyFormError error={error} />
      </div>
    </Modal>
  );
}

function UserLifecycleConfirm({
  action,
  pending,
  onCancel,
  onConfirm
}: {
  action: LifecycleAction | null;
  pending: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  if (!action) return null;
  const copy = lifecycleCopy(action);
  return (
    <LegacyConfirmModal
      title={copy.title}
      open
      okText={copy.okText}
      danger={copy.danger}
      pending={pending}
      onCancel={onCancel}
      onConfirm={onConfirm}
    >
      {copy.message}
    </LegacyConfirmModal>
  );
}

function LegacyConfirmModal({
  title,
  open,
  okText,
  danger,
  pending,
  children,
  onCancel,
  onConfirm
}: {
  title: string;
  open: boolean;
  okText: string;
  danger?: boolean;
  pending?: boolean;
  children: ReactNode;
  onCancel: () => void;
  onConfirm: () => void;
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
      mask={{ closable: false }}
      footer={[
        <Button key="cancel" disabled={pending} onClick={onCancel}>{t("common.cancel")}</Button>,
        <Button key="confirm" danger={danger} type={danger ? "default" : "primary"} loading={pending} onClick={onConfirm}>{okText}</Button>
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

function LegacyDialogTitle({
  title,
  kicker,
  subtitle
}: {
  title: string;
  kicker: string;
  subtitle?: string;
}) {
  return (
    <div className="legacy-dialog-title">
      <strong>{title}</strong>
      <span>{kicker}</span>
      {subtitle ? <small>{subtitle}</small> : null}
    </div>
  );
}

function LegacyFormError({ error }: { error: unknown }) {
  return <p className="form-error" role="alert">{error ? errorMessage(error) : ""}</p>;
}

function UserQuotaDrawer({
  user,
  summaryQuota,
  csrfToken,
  onClose,
  onSaved,
  onFailed,
  onAction
}: {
  user: string | null;
  summaryQuota: UserWeeklyQuota | null;
  csrfToken: string;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onFailed: (message: string) => void;
  onAction: (draft: QuotaActionDraft) => void;
}) {
  const queryClient = useQueryClient();
  const [mode, setMode] = useState<UserQuotaMode>("inherit");
  const [tokens, setTokens] = useState("");
  const [validationError, setValidationError] = useState("");
  const [restoreConfirm, setRestoreConfirm] = useState(false);
  const tokenInputRef = useRef<HTMLInputElement>(null);
  const quotaKey = userQuotaQueryKey(user || "");
  const query = useQuery({
    queryKey: quotaKey,
    queryFn: ({ signal }) => readUserQuota(user || "", signal),
    enabled: Boolean(user),
    staleTime: 0,
    gcTime: 0,
    retry: false
  });
  useEffect(() => {
    if (!query.data) return;
    setMode(query.data.weekly_quota.policy_mode);
    setTokens(query.data.weekly_quota.policy_tokens == null ? "" : String(query.data.weekly_quota.policy_tokens));
    setValidationError("");
  }, [query.data]);
  const finish = async (message: string) => {
    queryClient.removeQueries({ queryKey: quotaKey, exact: true });
    onClose();
    await onSaved(message);
  };
  const update = useMutation({
    mutationFn: () => updateUserQuota(user || "", mode, mode === "custom" ? Number(tokens) : null, csrfToken),
    onSuccess: (result) => void finish(result.message || t("admin.user_weekly_quota_policy_saved"))
  });
  const restore = useMutation({
    mutationFn: () => applyUserQuotaAction({
      action: "restore_default",
      scope: "selected",
      users: user ? [user] : [],
      confirm: "restore_default"
    }, csrfToken),
    onSuccess: (result) => {
      void finish(result.message);
    },
    onError: (error) => onFailed(errorMessage(error))
  });
  const quota = query.data?.weekly_quota ?? summaryQuota ?? undefined;
  const adjustments = query.data?.adjustments ?? [];
  const pending = update.isPending || restore.isPending;
  const save = () => {
    setValidationError("");
    if (mode === "custom" && tokenInputRef.current && !tokenInputRef.current.checkValidity()) {
      tokenInputRef.current.reportValidity();
      tokenInputRef.current.focus();
      return;
    }
    if (mode === "custom" && (!/^\d+$/.test(tokens.trim()) || Number(tokens) <= 0)) {
      setValidationError(t("admin.the_custom_weekly_quota_must_be_a_positive_integer"));
      tokenInputRef.current?.focus();
      return;
    }
    update.mutate();
  };
  return (
    <>
      <Drawer
        className="legacy-user-quota-drawer"
        title={<LegacyDialogTitle title={t("admin.configure_user_weekly_quota")} kicker="USER WEEKLY QUOTA" subtitle={user || ""} />}
        placement="right"
        size={500}
        open={Boolean(user)}
        closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
        onClose={() => {
          setValidationError("");
          onClose();
        }}
        destroyOnHidden
        footer={(
          <div className="legacy-drawer-footer">
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button
              type="primary"
              disabled={!quota || pending}
              onClick={save}
            >{update.isPending ? t("admin.saving") : t("admin.save_quota_policy")}</Button>
          </div>
        )}
      >
        {query.isPending && !summaryQuota ? <Skeleton active paragraph={{ rows: 12 }} /> : null}
        {quota ? (
          <div className="user-quota-drawer-content">
            <dl className="user-quota-summary">
              <QuotaFact label={t("admin.weighted_usage_this_week")} value={<LegacyTokenValue value={quota.weighted_used_tokens ?? quota.used_tokens} />} emphasize={false} />
              <QuotaFact label={t("admin.raw_tokens_this_week")} value={<LegacyTokenValue value={quota.raw_used_tokens} />} emphasize={false} />
              <QuotaFact
                label={t("admin.current_weighted_limit")}
                value={quota.unlimited ? t("common.unlimited") : tokenReadableText(quota.limit_tokens)}
                detail={quota.bonus_tokens > 0 ? t("admin.including_bonus", [tokenReadableText(quota.bonus_tokens)]) : undefined}
              />
              <QuotaFact label={t("admin.base_quota")} value={quota.base_limit_tokens == null ? t("common.unlimited") : tokenReadableText(quota.base_limit_tokens)} />
              <QuotaFact label={t("admin.weighted_quota_remaining")} value={quota.unlimited ? t("common.unlimited") : tokenReadableText(quota.remaining_tokens)} />
              <QuotaFact label={t("admin.next_reset")} value={formatSiteTimestamp(quota.week_end_at)} />
            </dl>
            <div className="inline-notice">{t("admin.quota_includes_the_user_s_tokens_across_all_cpas_reaching")}</div>
            <fieldset className="quota-policy-options">
              <legend>{t("admin.quota_policy")}</legend>
              <label><input type="radio" name="user-quota-mode" value="inherit" checked={mode === "inherit"} onChange={() => {
                setMode("inherit");
                setValidationError("");
              }} /><span><strong>{t("admin.inherit_organization_default")}</strong><small>{quota.default_limit_tokens == null ? t("admin.the_organization_default_is_unlimited") : t("admin.organization_default") + tokenReadableText(quota.default_limit_tokens)}</small></span></label>
              <label><input type="radio" name="user-quota-mode" value="unlimited" checked={mode === "unlimited"} onChange={() => {
                setMode("unlimited");
                setValidationError("");
              }} /><span><strong>{t("common.personal_unlimited_quota")}</strong><small>{t("admin.unaffected_by_future_changes_to_the_organization_default")}</small></span></label>
              <label><input type="radio" name="user-quota-mode" value="custom" checked={mode === "custom"} onChange={() => {
                setMode("custom");
                setValidationError("");
              }} /><span><strong>{t("admin.custom_quota")}</strong><small>{t("admin.recalculated_every_monday_at_00_00")}</small></span></label>
            </fieldset>
            <label className={"field" + (mode === "custom" ? "" : " disabled")}>
              <span>{t("admin.weekly_tokens")}</span>
              <div className="token-input-control">
                <input
                  ref={tokenInputRef}
                  aria-label={t("admin.weekly_tokens")}
                  type="number"
                  inputMode="numeric"
                  value={tokens}
                  min={1}
                  max={1_000_000_000_000}
                  step={1}
                  placeholder={t("admin.e_g_100000000")}
                  disabled={mode !== "custom"}
                  onChange={(event) => {
                    setTokens(event.target.value);
                    setValidationError("");
                  }}
                />
                <TokenInputPreview value={tokens} emptyLabel={t("admin.enter_custom_weekly_quota")} />
              </div>
            </label>
            <section className="user-quota-operations">
              <div className="user-quota-operations-head">
                <div><strong>{t("admin.weekly_quota_actions")}</strong><small>{t("admin.affects_only_the_current_calendar_week_raw_usage_records_are")}</small></div>
                <span className={"status-chip " + (adjustments.length ? "success" : "neutral")}>
                  {adjustments.length ? adjustments.length + t("admin.adjustments") : t("admin.no_adjustments")}
                </span>
              </div>
              <div className="user-quota-operation-grid">
                <button
                  className="quota-operation-card"
                  type="button"
                  disabled={quota.unlimited}
                  onClick={() => {
                    onAction({ action: "add_bonus", scope: "selected", users: user ? [user] : [] });
                  }}
                ><span>{t("admin.add_weekly_bonus")}</span><small>{t("admin.temporarily_increase_available_quota_expires_next_week")}</small></button>
                <button
                  className="quota-operation-card"
                  type="button"
                  disabled={quota.policy_mode === "inherit"}
                  onClick={() => setRestoreConfirm(true)}
                ><span>{t("admin.restore_organization_default")}</span><small>{t("admin.remove_the_personal_policy_and_retain_this_week_s_temporary")}</small></button>
                <button
                  className="quota-operation-card danger"
                  type="button"
                  disabled={!(quota.used_tokens > 0)}
                  onClick={() => {
                    onAction({ action: "reset_usage", scope: "selected", users: user ? [user] : [] });
                  }}
                ><span>{t("admin.reset_weekly_usage")}</span><small>{t("admin.retain_historical_events_and_offset_current_usage_in_the_adjustment")}</small></button>
              </div>
              <div className="quota-adjustment-history">
                {adjustments.slice(0, 4).map((adjustment, index) => (
                  <div className="quota-adjustment-history-row" key={adjustment.created_at + ":" + index}>
                    <strong>{adjustment.action === "bonus" ? t("admin.add_weekly_bonus") : t("admin.reset_weekly_usage")} · {tokenText(adjustment.token_amount)}</strong>
                    <time>{formatSiteTimestamp(adjustment.created_at)}</time>
                    <p title={adjustment.reason}>{adjustment.reason}</p>
                  </div>
                ))}
              </div>
            </section>
            <LegacyFormError error={validationError ? new Error(validationError) : query.error || update.error} />
          </div>
        ) : null}
      </Drawer>
      <LegacyConfirmModal
        title={t("admin.restore_the_organization_quota_default_for_1_user")}
        open={restoreConfirm}
        okText={t("admin.restore_organization_default")}
        pending={restore.isPending}
        onCancel={() => setRestoreConfirm(false)}
        onConfirm={() => {
          setRestoreConfirm(false);
          restore.mutate();
        }}
      >
        {summaryQuota?.policy_mode !== "inherit"
          ? t("admin.the_user_s_personal_quota_policy_will_be_removed_this")
          : t("admin.the_selected_users_already_inherit_the_organization_default_this_week")}
      </LegacyConfirmModal>
    </>
  );
}

function QuotaFact({
  label,
  value,
  detail,
  emphasize = true
}: {
  label: string;
  value: ReactNode;
  detail?: string;
  emphasize?: boolean;
}) {
  return <div><dt>{label}</dt><dd>{emphasize ? <strong>{value}</strong> : value}{detail ? <small>{detail}</small> : null}</dd></div>;
}

function TokenInputPreview({
  value,
  emptyLabel
}: {
  value: string | number | null | undefined;
  emptyLabel?: string;
}) {
  const presentation = tokenInputPresentation(value, emptyLabel);
  return (
    <div className="token-input-preview" data-state={presentation.state} aria-live="polite">
      {presentation.state === "empty" ? <small>{presentation.emptyLabel}</small> : null}
      {presentation.state === "invalid" ? <small>{t("admin.enter_a_positive_integer_token_amount")}</small> : null}
      {presentation.state === "ready" ? (
        <>
          <strong>{presentation.compact}</strong>
          {presentation.localized ? <> <span>{presentation.localized}</span></> : null}
          {presentation.compacted ? <> <small>{t("admin.exact_value")} {presentation.exact}</small></> : null}
        </>
      ) : null}
    </div>
  );
}

function QuotaActionModal({
  draft,
  users,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  draft: QuotaActionDraft | null;
  users: UserSummary[];
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (input: UserQuotaActionInput) => void;
}) {
  const [tokenAmount, setTokenAmount] = useState("");
  const [reason, setReason] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [validationError, setValidationError] = useState("");
  const tokenInputRef = useRef<HTMLInputElement>(null);
  const reasonInputRef = useRef<HTMLTextAreaElement>(null);
  const confirmationInputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (!draft) return;
    setTokenAmount("");
    setReason("");
    setConfirmation("");
    setValidationError("");
  }, [draft]);
  const selected = draft
    ? users.filter((user) => draft.users.includes(user.email))
    : [];
  const targetCount = draft?.scope === "all" ? users.length : selected.length;
  const usedCount = selected.filter((user) => user.weekly_quota.used_tokens > 0).length;
  const totalUsed = selected.reduce((total, user) => total + user.weekly_quota.used_tokens, 0);
  const totalRaw = selected.reduce((total, user) => total + user.weekly_quota.raw_used_tokens, 0);
  const confirmPhrase = draft?.action === "reset_usage"
    ? (draft.scope === "all" ? t("admin.confirm_reset_for_all") : t("admin.confirm_usage_reset"))
    : "";
  const submit = () => {
    if (!draft) return;
    setValidationError("");
    if (draft.action === "add_bonus" && tokenInputRef.current && !tokenInputRef.current.checkValidity()) {
      tokenInputRef.current.reportValidity();
      tokenInputRef.current.focus();
      return;
    }
    if (reasonInputRef.current && !reasonInputRef.current.checkValidity()) {
      reasonInputRef.current.reportValidity();
      reasonInputRef.current.focus();
      return;
    }
    if (confirmationInputRef.current && !confirmationInputRef.current.checkValidity()) {
      confirmationInputRef.current.reportValidity();
      confirmationInputRef.current.focus();
      return;
    }
    if (!reason.trim()) {
      setValidationError(t("admin.enter_a_reason_for_the_quota_change"));
      reasonInputRef.current?.focus();
      return;
    }
    if (draft.action === "add_bonus" && (!/^\d+$/.test(tokenAmount.trim()) || Number(tokenAmount) <= 0)) {
      setValidationError(t("admin.the_bonus_must_be_a_positive_integer"));
      tokenInputRef.current?.focus();
      return;
    }
    if (confirmPhrase && confirmation.trim() !== confirmPhrase) {
      setValidationError(t("admin.enter", [confirmPhrase]));
      confirmationInputRef.current?.focus();
      return;
    }
    onSubmit({
      action: draft.action,
      scope: draft.scope,
      users: draft.users,
      tokenAmount: draft.action === "add_bonus" ? Number(tokenAmount) : undefined,
      reason: reason.trim(),
      confirm: draft.action === "add_bonus"
        ? "add_bonus"
        : (draft.scope === "all" ? "reset_all_current_week_usage" : "reset_current_week_usage")
    });
  };
  return (
    <Modal
      className="legacy-user-form-modal quota-action-modal"
      title={<LegacyDialogTitle
        title={draft?.action === "add_bonus"
          ? t("admin.add_weekly_bonus")
          : (draft?.scope === "all" ? t("admin.reset_all_users_weekly_usage") : t("admin.reset_weekly_usage"))}
        kicker="QUOTA ADJUSTMENT"
        subtitle={draft?.scope === "all"
          ? t("admin.all_users", [formatNumber(targetCount)])
          : (targetCount === 1 ? draft?.users[0] : t("admin.selected_2") + formatNumber(targetCount) + t("admin.users"))}
      />}
      open={draft !== null}
      width={520}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={pending ? t("admin.processing") : (draft?.action === "add_bonus" ? t("admin.confirm_bonus") : t("admin.confirm_usage_reset"))}
      okButtonProps={{ danger: draft?.action === "reset_usage", disabled: !draft || pending }}
      onCancel={onCancel}
      onOk={submit}
      afterOpenChange={(opened) => {
        if (!opened) return;
        if (draft?.action === "add_bonus") tokenInputRef.current?.focus();
        else reasonInputRef.current?.focus();
      }}
      destroyOnHidden
      mask={{ closable: false }}
    >
      <div className="legacy-user-form-body">
        <div className="quota-action-impact">
          <strong>{draft?.action === "add_bonus" ? t("admin.increase_this_week_s_available_quota_without_changing_the_base") : t("admin.reset_billable_usage_while_retaining_raw_token_events_and_reporting")}</strong>
          <dl>
            <div><dt>{t("admin.affected_users")}</dt><dd>{formatNumber(targetCount)} {t("admin.users_2")}</dd></div>
            <div><dt>{t("admin.users_with_weekly_usage")}</dt><dd>{formatNumber(usedCount)} {t("admin.users_2")}</dd></div>
            <div><dt>{t("admin.current_weighted_usage")}</dt><dd>{tokenText(totalUsed)}</dd></div>
            <div><dt>{t("admin.cumulative_raw_tokens")}</dt><dd>{tokenText(totalRaw)}</dd></div>
          </dl>
        </div>
        {draft?.action === "add_bonus" ? (
          <label className="field">
            <span>{t("admin.bonus_tokens")}</span>
            <div className="token-input-control">
              <input
                ref={tokenInputRef}
                aria-label={t("admin.bonus_tokens")}
                type="number"
                inputMode="numeric"
                min={1}
                max={1_000_000_000_000}
                step={1}
                required
                placeholder={t("admin.e_g_100000000")}
                value={tokenAmount}
                onChange={(event) => {
                  setTokenAmount(event.target.value);
                  setValidationError("");
                }}
              />
              <TokenInputPreview value={tokenAmount} emptyLabel={t("admin.enter_this_week_s_bonus_quota")} />
            </div>
          </label>
        ) : null}
        <label className="field">
          <span>{t("admin.reason")}</span>
          <textarea
            ref={reasonInputRef}
            aria-label={t("admin.reason")}
            maxLength={200}
            rows={3}
            required
            value={reason}
            placeholder={t("admin.describe_the_business_reason_or_incident_up_to_200_characters")}
            onChange={(event) => setReason(event.target.value)}
          />
        </label>
        {confirmPhrase ? (
          <label className="field confirmation-field">
            <span>{t("admin.enter_2")}{confirmPhrase}{t("admin.to_continue")}</span>
            <input
              ref={confirmationInputRef}
              value={confirmation}
              required
              autoComplete="off"
              onChange={(event) => setConfirmation(event.target.value)}
            />
          </label>
        ) : null}
        <div className="inline-notice">
          {draft?.action === "add_bonus"
            ? t("admin.the_bonus_applies_only_to_the_current_week_the_base")
            : t("admin.this_offset_is_recorded_as_a_baseline_new_tokens_continue")}
        </div>
        <LegacyFormError error={validationError ? new Error(validationError) : error} />
      </div>
    </Modal>
  );
}

function TeamUsageDrawer({
  open,
  team,
  range,
  onClose
}: {
  open: boolean;
  team: TeamUsageRow | null;
  range: UsageRange;
  onClose: () => void;
}) {
  const query = useQuery({
    queryKey: [
      ...teamUsageQueryKey(range),
      "breakdown",
      team?.id ?? ""
    ],
    queryFn: ({ signal }) => readTeamUsageBreakdown(team?.id ?? "", range, signal),
    enabled: open && Boolean(team),
    gcTime: 0,
    retry: false
  });
  return (
    <Drawer
      className="legacy-team-usage-drawer"
      title={<LegacyDialogTitle
        title={team ? team.name + t("admin.token_usage") : t("admin.team_token_usage")}
        kicker="TEAM TOKEN ANALYTICS"
        subtitle={usageWindowLabel(range.window) + t("admin.model_reasoning_effort")}
      />}
      placement="right"
      size={780}
      open={open}
      onClose={onClose}
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      destroyOnHidden
      mask={{ closable: false }}
      footer={<Button onClick={onClose}>{t("common.close")}</Button>}
    >
      {query.isPending ? (
        <div className="team-usage-skeleton" aria-label={t("admin.loading_team_token_usage")}>
          <span /><span /><span /><span />
        </div>
      ) : null}
      {query.isError ? (
        <div className="team-usage-state error">{t("admin.unable_to_load_team_usage_2")}{errorMessage(query.error)}</div>
      ) : null}
      {team && query.data ? <TeamUsageContent team={team} payload={query.data} range={range} /> : null}
    </Drawer>
  );
}

function TeamUsageContent({
  team,
  payload,
  range
}: {
  team: TeamUsageRow;
  payload: TeamUsageBreakdownResponse;
  range: UsageRange;
}) {
  const rawTokens = Number(payload.totals.total_tokens) || 0;
  const weightedTokens = Number(payload.totals.weighted_tokens) || 0;
  const multiplier = rawTokens > 0 ? weightedTokens / rawTokens : 1;
  const models = groupTeamModels(payload.combinations);
  return (
    <div className="team-usage-content">
      <section className="team-detail-summary">
        <div className="team-detail-primary">
          <span>{t("common.weighted_tokens", [usageWindowLabel(range.window)])}</span>
          <strong><LegacyTokenValue value={weightedTokens} /></strong>
          <small>{formatNumber(payload.totals.request_count)} {t("admin.calls_2")} {formatNumber(payload.totals.failed_count)} {t("admin.failed_2")}</small>
        </div>
        <div className="team-detail-facts">
          <div><span>{t("common.raw_tokens")}</span><strong><LegacyTokenValue value={rawTokens} /></strong></div>
          <div><span>{t("admin.average_multiplier")}</span><strong>×{multiplier.toFixed(2)}</strong></div>
          <div><span>{t("admin.current_members")}</span><strong>{formatNumber(team.current_user_count)}</strong></div>
          <div><span>{t("admin.active_members")}</span><strong>{formatNumber(team.usage.active_users)}</strong></div>
        </div>
      </section>
      <TeamUsageTrend series={payload.series} />
      <section className="team-combination-section">
        <div className="team-detail-heading">
          <div><h4>{t("common.model_reasoning_effort")}</h4><p className="section-kicker">MODEL × EFFORT</p></div>
          <span>{t("admin.color_segments_show_token_shares_by_reasoning_effort_for_this")}</span>
        </div>
        <div className="team-combination-list">
          {models.length ? models.map((model) => (
            <div className="team-combination-row" key={model.model}>
              <span className="team-combination-label">
                <strong title={model.model}>{model.model}</strong>
                <small>{formatNumber(model.requestCount)} {t("admin.calls")}</small>
              </span>
              <span className="team-combination-progress">
                <span className="account-model-progress" role="group" aria-label={model.model + t("admin.token_share_by_reasoning_effort")}>
                  {model.efforts.map((effort) => {
                    const tooltip = modelEffortTooltipDetails(model.model, effort);
                    const shareUnits = Math.max(1, Math.min(100, Math.round(effort.sharePercent)));
                    return (
                      <LegacyUsageTooltip key={effort.reasoning_effort} content={tooltip}>
                        {(events) => (
                          <button
                            {...events}
                            className={`account-model-progress-segment account-model-effort-${effortColorKey(effort.reasoning_effort)} account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}${effort.sharePercent < 18 ? " compact" : ""}`}
                            type="button"
                            aria-label={modelEffortTooltipText(tooltip)}
                          >
                            <span>{effort.reasoning_effort}</span>
                            <em>{effort.sharePercent.toFixed(1)}%</em>
                          </button>
                        )}
                      </LegacyUsageTooltip>
                    );
                  })}
                </span>
              </span>
              <span className="team-combination-value">
                <strong><LegacyTokenValue value={model.weightedTokens} /></strong>
                <small>{t("common.weighted_tokens_2")}</small>
              </span>
            </div>
          )) : (
            <div className="team-usage-state">
              <strong>{t("admin.no_model_details")}</strong>
              <span>{t("admin.no_successful_calls_with_recorded_model_and_reasoning_effort_in")}</span>
            </div>
          )}
        </div>
      </section>
      <section className="team-member-section">
        <div className="team-detail-heading">
          <div><h4>{t("admin.active_member_ranking")}</h4><p className="section-kicker">MEMBERS</p></div>
          <span>{t("admin.top_8")}</span>
        </div>
        <div className="team-member-ranking">
          {payload.users.length ? payload.users.slice(0, 8).map((user, index) => (
            <div key={user.user}>
              <span>{String(index + 1).padStart(2, "0")}</span>
              <strong title={user.user}>{user.user}</strong>
              <em>{tokenText(user.weighted_tokens)}</em>
            </div>
          )) : <div className="team-usage-state"><span>{t("admin.no_active_members_in_this_range")}</span></div>}
        </div>
      </section>
    </div>
  );
}

function TeamUsageTrend({ series }: { series: TeamUsageSeries }) {
  const values = Array.isArray(series.values) ? series.values.map((value) => Number(value) || 0) : [];
  const buckets = Array.isArray(series.buckets) ? series.buckets : [];
  if (!values.length || !buckets.length) {
    return <div className="team-trend-empty">{t("admin.no_trend_data_in_this_range")}</div>;
  }
  const width = 640;
  const height = 120;
  const paddingX = 8;
  const paddingY = 10;
  const maximum = Math.max(...values, 0);
  const scaleMaximum = Math.max(maximum, 1);
  const points = values.map((value, index) => ({
    x: values.length === 1 ? width / 2 : paddingX + index * (width - paddingX * 2) / (values.length - 1),
    y: height - paddingY - value * (height - paddingY * 2) / scaleMaximum
  }));
  const lastPoint = points.at(-1) ?? { x: width / 2, y: height - paddingY };
  return (
    <section className="team-trend">
      <div className="team-trend-head"><h4>{t("admin.weighted_token_trend")}</h4><span>{t("admin.every")} {Math.max(1, Math.round(series.bucket_seconds / 60))} {t("admin.minutes")}</span></div>
      <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={t("admin.team_weighted_token_trend_peak_tokens", [formatNumber(maximum)])}>
        <line x1={paddingX} y1={height - paddingY} x2={width - paddingX} y2={height - paddingY} />
        <polyline points={points.map((point) => `${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ")} />
        <circle cx={lastPoint.x} cy={lastPoint.y} r="4" />
      </svg>
      <div className="team-trend-axis">
        <span>{formatSiteTimestamp(series.start_at)}</span>
        <strong>{t("common.peak")} <LegacyTokenValue value={maximum} /></strong>
        <span>{formatSiteTimestamp(series.end_at)}</span>
      </div>
    </section>
  );
}

type TeamModelEffort = TeamCombinationUsage & { sharePercent: number };
type TeamModelRow = {
  model: string;
  requestCount: number;
  weightedTokens: number;
  efforts: TeamModelEffort[];
};

function groupTeamModels(combinations: TeamCombinationUsage[]): TeamModelRow[] {
  const grouped = new Map<string, TeamCombinationUsage[]>();
  combinations.forEach((item) => grouped.set(item.model, [...(grouped.get(item.model) ?? []), item]));
  return [...grouped.entries()].map(([model, efforts]) => {
    const weightedTokens = efforts.reduce((total, effort) => total + Number(effort.weighted_tokens ?? effort.total_tokens), 0);
    return {
      model,
      weightedTokens,
      requestCount: efforts.reduce((total, effort) => total + Number(effort.request_count), 0),
      efforts: efforts.map((effort) => {
        const effortTokens = Number(effort.weighted_tokens ?? effort.total_tokens);
        return {
          ...effort,
          sharePercent: weightedTokens > 0 ? effortTokens * 100 / weightedTokens : 0
        };
      }).sort((left, right) => right.sharePercent - left.sharePercent || left.reasoning_effort.localeCompare(right.reasoning_effort))
    };
  }).sort((left, right) => right.weightedTokens - left.weightedTokens || left.model.localeCompare(right.model));
}

function lifecycleCopy(action: LifecycleAction) {
  if (action.kind === "rotate") {
    return {
      title: t("admin.rotate_key"),
      message: t("admin.the_old_key_expires_immediately_the_new_key_is_shown"),
      okText: t("admin.confirm_rotation"),
      danger: false
    };
  }
  if (action.kind === "reset-password") {
    return {
      title: t("admin.reset_user_password"),
      message: action.user.email + t("admin.s_password_will_reset_to_the_system_s_initial_password"),
      okText: t("admin.confirm_reset"),
      danger: true
    };
  }
  if (action.kind === "revoke") {
    return {
      title: t("admin.disable_the_user_s_api_key"),
      message: action.user.email + t("admin.s_unified_api_key_will_expire_immediately"),
      okText: t("admin.disable_all"),
      danger: true
    };
  }
  return {
    title: t("admin.delete_user_and_api_key"),
    message: action.user.active_keys
      ? t("admin.will_be_removed_from_the_user_list_and_active_keys", [action.user.email, action.user.active_keys])
      : t("admin.will_be_removed_from_the_user_list_historical_usage_and", [action.user.email]),
    okText: t("admin.delete_user"),
    danger: true
  };
}

function accountSortValue(account: UserAccountDetail, field: UserAccountSortField): string | number | null {
  if (field === "account") return account.account;
  if (field === "status") return statusRank(account.status);
  if (field === "requests") return account.usage.request_count;
  if (field === "input_tokens") return account.usage.input_tokens;
  if (field === "output_tokens") return account.usage.output_tokens;
  if (field === "reasoning_tokens") return account.usage.reasoning_tokens;
  if (field === "cached_tokens") return account.usage.cached_tokens;
  if (field === "total_tokens") return account.usage.total_tokens;
  if (field === "weighted_tokens") return account.usage.weighted_tokens;
  return account.usage.last_used_at || null;
}

function compareRows(
  left: string | number | null,
  right: string | number | null,
  direction: SortDirection,
  leftFallback: string,
  rightFallback: string
) {
  if (left == null && right != null) return 1;
  if (left != null && right == null) return -1;
  let comparison = 0;
  if (typeof left === "number" && typeof right === "number") comparison = left - right;
  else comparison = String(left ?? "").localeCompare(String(right ?? ""), getIntlLocale());
  if (comparison === 0) comparison = leftFallback.localeCompare(rightFallback, getIntlLocale());
  return direction === "desc" ? -comparison : comparison;
}

function statusRank(status: string) {
  return { active: 1, inactive: 2, revoked: 3, missing: 4 }[status] ?? 5;
}

function statusTone(status: string) {
  return status === "active" ? "success" : status === "missing" ? "warning" : "neutral";
}

function statusLabel(status: string) {
  return { active: t("admin.enable"), inactive: t("common.disabled"), revoked: t("admin.revoked"), missing: t("admin.not_created_2") }[status] ?? status;
}

function quotaSourceLabel(quota: UserWeeklyQuota) {
  return {
    default: t("common.organization_default"),
    user_unlimited: t("common.personal_unlimited_quota"),
    user_custom: t("common.user_override")
  }[quota.source] || t("common.unknown_quota");
}

function quotaPolicyLifetime(quota: UserWeeklyQuota) {
  return quota.personal_policy_reset_enabled
    ? t("admin.this_week_only_restores_the_organization_default_next_week")
    : t("admin.persists_until_the_organization_default_is_restored_manually");
}

function tokenText(value: number | null | undefined) {
  if (value == null) return t("common.unlimited");
  const formatted = formatTokenAmount(Number(value) || 0);
  return formatted.includes(" ") ? formatted : formatted + " Token";
}

function formatPercent(value: number | null | undefined) {
  return value == null ? "—" : Math.max(0, value).toFixed(1) + "%";
}

function formatUsageRatio(value: number | null | undefined, total: number | null | undefined) {
  const denominator = Number(total) || 0;
  if (denominator <= 0) return "0%";
  return new Intl.NumberFormat(getIntlLocale(), { maximumFractionDigits: 1 }).format((Number(value) || 0) * 100 / denominator) + "%";
}

function usageWindowLabel(window: UsageWindow) {
  return {
    "3600": t("common.1h"),
    "21600": t("common.6h"),
    today: t("common.today"),
    "86400": t("common.24h"),
    "604800": t("common.7d"),
    "2592000": t("common.30d"),
    current_week: t("common.this_week"),
    since_reset: t("admin.quota_cycle"),
    all: t("admin.all_time"),
    custom: t("admin.custom_range")
  }[window] || t("common.current_range");
}

function formatNumber(value: number | null | undefined) {
  return new Intl.NumberFormat(getIntlLocale()).format(Number(value) || 0);
}

function UserLastUsed({ timestamp }: { timestamp: number | null | undefined }) {
  if (!timestamp || !Number.isFinite(timestamp) || timestamp <= 0) return <span className="user-last-used">{t("admin.never_used")}</span>;
  const label = formatLastUsed(timestamp);
  return (
    <time className="user-last-used" dateTime={new Date(timestamp * 1000).toISOString()} title={label}>
      {label.replace(" ", "\n")}
    </time>
  );
}

function formatLastUsed(timestamp: number | null | undefined) {
  return timestamp && Number.isFinite(timestamp) && timestamp > 0 ? formatSiteTimestamp(timestamp) : t("admin.never_used");
}

function effortLabel(value: string) {
  return {
    none: t("common.none"),
    minimal: t("common.minimal"),
    low: t("common.low"),
    medium: t("common.medium"),
    high: t("common.high"),
    xhigh: t("common.ultra"),
    max: t("common.max"),
    ultra: t("common.extra_high"),
    auto: t("common.auto"),
    unknown: t("common.unknown")
  }[value] ?? value;
}

function effortColorKey(effort: string) {
  return ["none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max", "auto"].includes(effort)
    ? effort
    : "unknown";
}

function paginationItems(current: number, total: number): Array<number | "…"> {
  if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1);
  const items: Array<number | "…"> = [1];
  if (current > 4) items.push("…");
  const start = Math.max(2, current - 1);
  const end = Math.min(total - 1, current + 1);
  for (let page = start; page <= end; page += 1) items.push(page);
  if (current < total - 3) items.push("…");
  items.push(total);
  return items;
}

function userRefreshLabel(timestamp: number, cached: boolean) {
  return t("admin.user_data_updated") + formatSiteTimestamp(timestamp) + (cached ? t("admin.cached_3") : "");
}

function errorMessage(error: unknown) {
  return error instanceof ApiError || error instanceof Error ? error.message : t("admin.refresh_and_try_again");
}

function isInteractiveRowTarget(target: EventTarget | null) {
  return target instanceof Element && Boolean(target.closest("button, a, input, select, textarea, label, [role='button']"));
}
