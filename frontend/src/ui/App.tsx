import { LanguageSelect } from "./LanguageSelect";
import { t } from "../i18n";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useCallback, useEffect, useRef } from "react";
import { useState } from "react";
import { Link, Navigate, Route, Routes, useLocation } from "react-router-dom";

import { ApiError, subscribeUnauthorized } from "../api/client";
import { onboardingQueryKey, readOnboarding } from "../api/onboarding";
import { defaultPublicSiteConfiguration, publicSiteQueryKey, readPublicSiteConfiguration } from "../api/public-site";
import { logout, readSession, refreshSession, sessionQueryKey } from "../api/session";
import { applicationHref } from "../application-links";
import { AdminToolbarContext, type AdminPageDetail } from "./AdminToolbarContext";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { ReleaseVersionIndicator } from "./components/ReleaseVersionIndicator";
import { LoginPage } from "./LoginPage";
import { ThemeToggle, useTheme } from "./ThemeProvider";

const AccountsPage = lazy(() => import("./AccountsPage").then((module) => ({ default: module.AccountsPage })));
const OverviewPage = lazy(() => import("./OverviewPage").then((module) => ({ default: module.OverviewPage })));
const TeamsPage = lazy(() => import("./TeamsPage").then((module) => ({ default: module.TeamsPage })));
const UsersPage = lazy(() => import("./UsersPage").then((module) => ({ default: module.UsersPage })));
const ConfigurationPage = lazy(() => import("./ConfigurationPage").then((module) => ({ default: module.ConfigurationPage })));
const RuntimePage = lazy(() => import("./RuntimePage").then((module) => ({ default: module.RuntimePage })));
const OnboardingPage = lazy(() => import("./OnboardingPage").then((module) => ({ default: module.OnboardingPage })));

export function App() {
  const queryClient = useQueryClient();
  const [loginNotice, setLoginNotice] = useState("");
  const session = useQuery({
    queryKey: sessionQueryKey,
    queryFn: ({ signal }) => readSession(signal),
    retry: false,
    refetchOnWindowFocus: false
  });

  const logoutMutation = useMutation({
    mutationFn: () => logout(session.data?.csrf_token ?? ""),
    onSettled: () => {
      queryClient.clear();
      void queryClient.invalidateQueries({ queryKey: sessionQueryKey, exact: true });
    }
  });
  const expireSession = useCallback((notice = "") => {
    if (notice) setLoginNotice(notice);
    queryClient.removeQueries({
      predicate: (query) => query.queryKey[0] !== sessionQueryKey[0]
    });
    void queryClient.resetQueries({ queryKey: sessionQueryKey, exact: true });
  }, [queryClient]);
  useEffect(() => subscribeUnauthorized((event) => {
    if (event.scope === "admin" && event.path !== "/admin/api/session") {
      expireSession(adminSessionNotice(event.code, event.message));
    }
  }), [expireSession]);
  useEffect(() => {
    const csrfToken = session.data?.csrf_token;
    if (!csrfToken) return;
    let refreshPending = false;
    let lastRefreshAt = 0;
    const refreshInterval = 5 * 60 * 1_000;
    const onActivity = (event: PointerEvent | KeyboardEvent) => {
      if (!event.isTrusted || (event instanceof KeyboardEvent && event.repeat)) return;
      const now = Date.now();
      if (refreshPending || now - lastRefreshAt < refreshInterval) return;
      refreshPending = true;
      lastRefreshAt = now;
      void refreshSession(csrfToken)
        .then((payload) => queryClient.setQueryData(sessionQueryKey, payload))
        .catch(() => undefined)
        .finally(() => { refreshPending = false; });
    };
    window.addEventListener("pointerdown", onActivity, { passive: true });
    window.addEventListener("keydown", onActivity);
    return () => {
      window.removeEventListener("pointerdown", onActivity);
      window.removeEventListener("keydown", onActivity);
    };
  }, [queryClient, session.data?.csrf_token]);
  if (session.isPending) {
    return <AppLoading />;
  }
  if (session.error instanceof ApiError && session.error.status === 401) {
    return <LoginPage notice={loginNotice} onAuthenticated={() => setLoginNotice("")} />;
  }
  if (session.isError || !session.data?.authenticated) {
    return (
      <CenteredState
        title={t("common.management_service_unavailable")}
        detail={session.error instanceof Error ? session.error.message : t("common.unable_to_verify_the_management_session")}
        actionLabel={t("common.retry")}
        onAction={() => void session.refetch()}
      />
    );
  }

  return (
    <AdminShell
      loggingOut={logoutMutation.isPending}
      onLogout={() => logoutMutation.mutate()}
    >
      <Suspense fallback={<PageLoading />}>
        <AuthenticatedRoutes
          csrfToken={session.data.csrf_token ?? ""}
          onManagementKeyRotated={(message) => expireSession(message)}
        />
      </Suspense>
    </AdminShell>
  );
}

function adminSessionNotice(code: string, fallback: string) {
  if (code === "session_expired") return t("common.your_management_session_expired_enter_the_management_key_again");
  if (code === "session_invalidated") return t("common.the_management_key_changed_enter_it_again");
  if (code === "session_missing") return t("common.your_management_session_ended_enter_the_management_key_again");
  return fallback || t("common.your_management_session_is_invalid_enter_the_management_key_again");
}

function AuthenticatedRoutes({
  csrfToken,
  onManagementKeyRotated
}: {
  csrfToken: string;
  onManagementKeyRotated: (message: string) => void;
}) {
  const location = useLocation();
  const onboarding = useQuery({
    queryKey: onboardingQueryKey,
    queryFn: ({ signal }) => readOnboarding(signal),
    staleTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false
  });
  const autoRedirected = useRef(false);
  useEffect(() => {
    if (location.pathname.startsWith("/setup")) autoRedirected.current = true;
  }, [location.pathname]);
  if (
    onboarding.data
    && !onboarding.data.required_complete
    && !location.pathname.startsWith("/setup")
    && !autoRedirected.current
  ) {
    autoRedirected.current = true;
    return <Navigate to="/setup" replace />;
  }
  return (
    <Routes>
      <Route path="/setup" element={<OnboardingPage csrfToken={csrfToken} />} />
      <Route path="/overview" element={<OverviewPage />} />
      <Route path="/accounts" element={<AccountsPage csrfToken={csrfToken} />} />
      <Route path="/users" element={<UsersPage csrfToken={csrfToken} />} />
      <Route path="/teams" element={<TeamsPage csrfToken={csrfToken} />} />
      <Route path="/notifications" element={<Navigate to="/configuration" replace />} />
      <Route path="/runtime" element={<RuntimePage csrfToken={csrfToken} />} />
      <Route path="/configuration" element={<ConfigurationPage csrfToken={csrfToken} onManagementKeyRotated={onManagementKeyRotated} />} />
      <Route path="/settings" element={<Navigate to="/configuration" replace />} />
      <Route path="*" element={<Navigate to="/overview" replace />} />
    </Routes>
  );
}

type AdminPage = {
  eyebrow: string;
  title: string;
};

const adminNavigation = [
  { to: "/overview", icon: "⌂", label: t("common.overview") },
  { to: "/accounts", icon: "▣", label: t("common.accounts") },
  { to: "/users", icon: "◎", label: t("common.users") },
  { to: "/teams", icon: "◇", label: t("common.teams") },
  { to: "/runtime", icon: "⌘", label: t("common.maintenance") },
  { to: "/configuration", icon: "⚙", label: t("common.configuration") }
] as const;

function currentAdminPage(pathname: string): AdminPage {
  if (pathname.startsWith("/setup")) return { eyebrow: "GETTING STARTED", title: t("common.initial_setup") };
  if (pathname.startsWith("/configuration") || pathname.startsWith("/settings") || pathname.startsWith("/notifications")) {
    return { eyebrow: "CONTROL PLANE SETTINGS", title: t("common.configuration") };
  }
  if (pathname.startsWith("/runtime")) return { eyebrow: "STACK CONTROL", title: t("common.maintenance") };
  if (pathname.startsWith("/teams")) return { eyebrow: "TEAM MANAGEMENT", title: t("common.teams") };
  if (pathname.startsWith("/users")) return { eyebrow: "USER MANAGEMENT", title: t("common.users") };
  if (pathname.startsWith("/accounts")) return { eyebrow: "ACCOUNT MANAGEMENT", title: t("common.accounts") };
  return { eyebrow: "OPERATIONS OVERVIEW", title: t("common.overview") };
}

function currentNavigationPath(pathname: string) {
  if (pathname.startsWith("/setup")) return "";
  if (pathname.startsWith("/configuration") || pathname.startsWith("/settings") || pathname.startsWith("/notifications")) {
    return "/configuration";
  }
  return adminNavigation.find((item) => pathname.startsWith(item.to))?.to ?? "/overview";
}

export function AdminShell({
  children,
  loggingOut,
  onLogout
}: {
  children: React.ReactNode;
  loggingOut: boolean;
  onLogout: () => void;
}) {
  const queryClient = useQueryClient();
  const { toasts, showToast } = useLegacyToasts();
  const { theme } = useTheme();
  const publicSite = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: 1,
    refetchOnWindowFocus: true
  });
  const productName = publicSite.data?.product_name ?? defaultPublicSiteConfiguration.product_name;
  const location = useLocation();
  const isOnboarding = location.pathname.startsWith("/setup");
  const page = currentAdminPage(location.pathname);
  const selectedPath = currentNavigationPath(location.pathname);
  const navigationRef = useRef<HTMLElement>(null);
  const refreshActionRef = useRef<(() => Promise<void>) | null>(null);
  const [pageRefreshing, setPageRefreshing] = useState(false);
  const [manualRefreshing, setManualRefreshing] = useState(false);
  const [refreshLabel, setRefreshLabel] = useState(t("common.waiting_for_refresh"));
  const [pageDetail, setPageDetail] = useState<AdminPageDetail | null>(null);
  const setRefreshAction = useCallback((action: (() => Promise<void>) | null) => {
    refreshActionRef.current = action;
  }, []);
  useEffect(() => {
    const navigation = navigationRef.current;
    const selectedItem = navigation?.querySelector<HTMLElement>('[aria-current="page"]');
    if (!navigation || !selectedItem || navigation.scrollWidth <= navigation.clientWidth) return;
    selectedItem.scrollIntoView({ block: "nearest", inline: "center", behavior: "auto" });
  }, [selectedPath]);
  useEffect(() => setRefreshLabel(t("common.waiting_for_refresh")), [selectedPath]);
  useEffect(() => setPageDetail(null), [selectedPath]);
  const visiblePageDetail = selectedPath === "/configuration" ? pageDetail : null;
  const refreshActivePage = async () => {
    if (manualRefreshing) return;
    setManualRefreshing(true);
    try {
      if (refreshActionRef.current) await refreshActionRef.current();
      else await queryClient.refetchQueries({ type: "active" });
    } catch (error) {
      showToast(error instanceof Error ? error.message : t("common.refresh_failed_please_try_again_later"), "error");
    } finally {
      setManualRefreshing(false);
    }
  };
  const refreshing = pageRefreshing || manualRefreshing;
  const toolbar = {
    setRefreshing: setPageRefreshing,
    setRefreshLabel,
    setRefreshAction,
    setPageDetail
  };
  if (isOnboarding) {
    return (
      <div className="onboarding-app-shell">
        <AdminToolbarContext.Provider value={toolbar}>
          {children}
        </AdminToolbarContext.Provider>
      </div>
    );
  }
  return (
    <div className="app-shell">
      <aside className="side-nav" aria-label={t("common.admin_navigation")}>
        <Link className="brand side-nav-brand" to="/overview" aria-label={t("common.admin", [productName])}>
          <span className="brand-mark">
            <img
              src={`/portal/assets/codex-cpa-pool-mark${theme === "dark" ? "-dark" : ""}.svg`}
              alt=""
            />
          </span>
          <span className="brand-copy">
            <strong title={productName}>{productName}</strong>
            <small>Control Plane</small>
          </span>
        </Link>
        <nav ref={navigationRef} className="admin-nav" aria-label={t("common.main_navigation")}>
          {adminNavigation.map((item) => (
            <Link
              key={item.to}
              className={`admin-nav-item${selectedPath === item.to ? " active" : ""}`}
              to={item.to}
              aria-current={selectedPath === item.to ? "page" : undefined}
            >
              <span className="admin-nav-icon" aria-hidden="true">{item.icon}</span>
              <span>{item.label}</span>
            </Link>
          ))}
        </nav>
        <section className="side-nav-switcher" aria-label={t("common.switch_workspace")}>
          <div className="side-nav-switcher-heading"><span>{t("common.switch_workspace")}</span><small>SWITCH</small></div>
          <div className="side-nav-switcher-links">
            <a href={applicationHref("portal")}>
              <span className="side-nav-switcher-index">01</span>
              <span className="side-nav-switcher-copy"><strong>{t("common.service_portal_2")}</strong><small>{t("common.choose_a_workspace")}</small></span>
              <span className="side-nav-switcher-arrow" aria-hidden="true">›</span>
            </a>
            <a href={applicationHref("usage")}>
              <span className="side-nav-switcher-index">02</span>
              <span className="side-nav-switcher-copy"><strong>{t("common.usage_center")}</strong><small>{t("common.keys_accounts_usage")}</small></span>
              <span className="side-nav-switcher-arrow" aria-hidden="true">›</span>
            </a>
          </div>
        </section>
        <div className="side-nav-footer">
          <div className="side-nav-auth-status">
            <span className="status-dot" aria-hidden="true" />
            <span>{t("common.admin_api_authenticated")}</span>
          </div>
          <span className="side-nav-footer-separator" aria-hidden="true">|</span>
          <ReleaseVersionIndicator className="side-nav-release" />
        </div>
      </aside>
      <main className="main-surface">
        <header className="top-bar">
          <div className="top-bar-heading">
            <h1>
              <span>{page.title}</span>
              {visiblePageDetail ? <span className="page-heading-path"><span className="page-heading-separator" aria-hidden="true">/</span><span>{visiblePageDetail.title}</span></span> : null}
              {visiblePageDetail?.sectionTitle ? <span className="page-heading-path page-heading-section"><span className="page-heading-separator" aria-hidden="true">/</span><span>{visiblePageDetail.sectionTitle}</span></span> : null}
            </h1>
            <span className="eyebrow">
              <span>{page.eyebrow}</span>
              {visiblePageDetail ? <span className="page-heading-path"><span className="page-heading-separator" aria-hidden="true">/</span><span>{visiblePageDetail.eyebrow}</span></span> : null}
            </span>
          </div>
          <div className="top-bar-actions">
            <span className="top-bar-refresh-state">{refreshing ? t("common.refreshing") : refreshLabel}</span>
            <LanguageSelect /><ThemeToggle />
            <button
              className="button button-quiet top-bar-refresh"
              type="button"
              disabled={manualRefreshing}
              onClick={() => void refreshActivePage()}
            >
 {t("common.refresh")} </button>
            <button className="button button-quiet top-bar-logout" type="button" onClick={onLogout} disabled={loggingOut}>
              {loggingOut ? t("common.signing_out") : t("common.sign_out")}
            </button>
          </div>
        </header>
        <AdminToolbarContext.Provider value={toolbar}>
          {children}
        </AdminToolbarContext.Provider>
        <LegacyToastRegion toasts={toasts} />
      </main>
    </div>
  );
}

function PageLoading() {
  return (
    <section className="page-content" aria-label={t("common.loading_page")}>
      <div className="skeleton skeleton-title" />
      <div className="skeleton skeleton-line" />
      <div className="skeleton skeleton-table" />
    </section>
  );
}

function AppLoading() {
  return (
    <div className="loading-shell" aria-label={t("common.loading_admin")}>
      <div className="loading-brand" />
      <div className="loading-panel">
        <div className="skeleton skeleton-title" />
        <div className="skeleton skeleton-line" />
        <div className="skeleton skeleton-table" />
      </div>
    </div>
  );
}

export function CenteredState({
  title,
  detail,
  actionLabel,
  onAction
}: {
  title: string;
  detail: string;
  actionLabel: string;
  onAction: () => void;
}) {
  return (
    <main className="centered-state">
      <div className="state-symbol" aria-hidden="true">!</div>
      <h1>{title}</h1>
      <p>{detail}</p>
      <button className="button button-primary" type="button" onClick={onAction}>
        {actionLabel}
      </button>
    </main>
  );
}
