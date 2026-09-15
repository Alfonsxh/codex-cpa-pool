import { LanguageSelect } from "./LanguageSelect";
import { t } from "../i18n";
import { DownOutlined, LockOutlined, LogoutOutlined } from "@ant-design/icons";
import { Button, Dropdown, Result } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";

import { ApiError } from "../api/client";
import { applicationHref } from "../application-links";
import { defaultPublicSiteConfiguration, publicSiteQueryKey, readPublicSiteConfiguration } from "../api/public-site";
import {
  logoutPortal,
  portalSessionQueryKey,
  readPortalSession,
  type PortalSession
} from "../api/portal";
import { UsageLoginPage } from "./UsageLoginPage";
import { ThemeToggle, useTheme } from "./ThemeProvider";
import { NativeTableViewport } from "./components/NativeTableViewport";

const PortalPasswordModal = lazy(() => import("./PortalPasswordModal").then((module) => ({
  default: module.PortalPasswordModal
})));
const UsageDashboard = lazy(() => import("./UsageDashboard").then((module) => ({
  default: module.UsageDashboard
})));

export function UsageApp() {
  const queryClient = useQueryClient();
  const [sessionExpired, setSessionExpired] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const session = useQuery({
    queryKey: portalSessionQueryKey,
    queryFn: ({ signal }) => readPortalSession(signal),
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const logout = useMutation({
    mutationFn: logoutPortal,
    onSettled: () => {
      queryClient.clear();
      void queryClient.invalidateQueries({ queryKey: portalSessionQueryKey, exact: true });
    }
  });
  const expireSession = useCallback(() => {
    // A child query already proved the cookie is invalid. Clear every cached
    // management value immediately and show the authentication boundary
    // without waiting for a second session request to race the stale screen.
    queryClient.clear();
    setSessionExpired(true);
  }, [queryClient]);
  useEffect(() => {
    if (session.data?.authenticated) setSessionExpired(false);
  }, [session.data?.authenticated, session.dataUpdatedAt]);

  if (sessionExpired) {
    return <UsageAuthenticationBoundary />;
  }

  if (session.isPending) {
    return <UsageLoading />;
  }
  if (session.error instanceof ApiError && session.error.status === 401) {
    return <UsageAuthenticationBoundary />;
  }
  if (session.isError || !session.data?.authenticated) {
    return (
      <main className="centered-state">
        <Result
          status="warning"
          title={t("common.usage_center_unavailable")}
          subTitle={session.error instanceof Error ? session.error.message : t("common.unable_to_verify_the_user_session")}
          extra={<Button type="primary" onClick={() => void session.refetch()}>{t("common.retry")}</Button>}
        />
      </main>
    );
  }

  const updateSession = (updates: Partial<PortalSession>) => {
    queryClient.setQueryData<PortalSession>(portalSessionQueryKey, (current) => current ? { ...current, ...updates } : current);
  };

  return (
    <UsageShell
      user={session.data.user}
      loggingOut={logout.isPending}
      onLogout={() => logout.mutate()}
      onChangePassword={() => setPasswordOpen(true)}
    >
      <Suspense fallback={<UsageLoading />}>
        {session.data.password_change_required ? (
          <section className="usage-password-required">
            <div>
              <span className="eyebrow">SECURITY CHECK</span>
              <h1>{t("common.set_your_personal_password_first")}</h1>
              <p>{t("common.the_initial_password_is_for_first_sign_in_only_after")}</p>
            </div>
            <PortalPasswordModal
              open
              mandatory
              onClose={() => undefined}
              onSuccess={() => updateSession({ password_change_required: false })}
            />
          </section>
        ) : <UsageDashboard key={session.data.user} user={session.data.user} onSessionExpired={expireSession} />}
      </Suspense>
      <Suspense fallback={null}>
        <PortalPasswordModal
          open={passwordOpen}
          onClose={() => setPasswordOpen(false)}
          onSuccess={() => setPasswordOpen(false)}
        />
      </Suspense>
    </UsageShell>
  );
}

function UsageAuthenticationBoundary() {
  return (
    <div className="usage-shell usage-authentication-boundary">
      <header className="usage-topbar usage-preview-topbar">
        <div className="usage-preview-brand">
          <UsageBrand />
          <span className="usage-heading">
            <strong>{t("common.usage_center")}</strong>
            <span className="usage-heading-subtitle" lang="en">USAGE CENTER</span>
          </span>
        </div>
        <LanguageSelect /><ThemeToggle />
      </header>
      <main className="usage-main usage-preview" aria-hidden="true">
        <section className="usage-preview-summary">
          <div className="usage-preview-key">
            <span>{t("common.my_api_key")}</span>
            <code>{t("common.loaded_only_when_needed")}</code>
            <div>
              {[t("common.manage_api_key"), t("common.configure_codex"), t("common.configure_claude_code"), t("common.import_to_cc_switch")].map((label) => (
                <button type="button" disabled key={label}>{label}</button>
              ))}
            </div>
          </div>
          <div className="usage-preview-stat-grid">
            <PreviewStat title={t("common.current_account")} value={t("common.not_selected")} detail={t("common.shown_after_selecting_an_available_account")} />
            <PreviewStat title={t("common.my_weekly_usage")} value="—" detail={t("common.loading_weekly_quota")} />
            <PreviewStat title={t("common.today_s_tokens")} value="—" />
          </div>
        </section>
        <section className="usage-preview-accounts">
          <div className="usage-preview-toolbar">
            <h2>{t("common.account_details")}</h2>
            <div>
              {[t("common.1h"), t("common.today"), t("common.24h"), t("common.7d"), t("common.this_week"), t("common.refresh")].map((label) => (
                <button type="button" disabled key={label}>{label}</button>
              ))}
            </div>
          </div>
          <NativeTableViewport className="usage-preview-table-wrap" aria-label={t("common.account_details_loading_preview")}>
            <table>
              <thead>
                <tr>
                  {[t("common.no"), t("common.current_account"), t("common.cpa_account"), t("common.account_weekly_quota"), t("common.active_users"), t("common.account_status"), t("common.my_requests"), t("common.my_tokens"), t("common.last_used"), t("common.usage_details")].map((label) => <th key={label}>{label}</th>)}
                </tr>
              </thead>
              <tbody>
                {[0, 1, 2].map((row) => (
                  <tr key={row}>{Array.from({ length: 10 }, (_, column) => <td key={column}><span /></td>)}</tr>
                ))}
              </tbody>
            </table>
          </NativeTableViewport>
        </section>
      </main>
      <UsageLoginPage overlay />
    </div>
  );
}

function UsageBrand() {
  const { theme } = useTheme();
  const publicSite = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: 1,
    refetchOnWindowFocus: true
  });
  const productName = publicSite.data?.product_name ?? defaultPublicSiteConfiguration.product_name;
  return (
    <a className="usage-product-brand" href={applicationHref("portal")} aria-label={t("common.service_portal", [productName])} title={productName}>
      <img
        className="usage-brand-logo"
        src={`/portal/assets/codex-cpa-pool-mark${theme === "dark" ? "-dark" : ""}.svg`}
        alt=""
      />
      <span className="usage-product-name">{productName}</span>
    </a>
  );
}

function PreviewStat({ title, value, detail }: { title: string; value: string; detail?: string }) {
  return (
    <article>
      <span>{title}</span>
      <strong>{value}</strong>
      <div className="usage-preview-progress"><i /></div>
      {detail ? <small>{detail}</small> : null}
    </article>
  );
}

function UsageShell({
  user,
  loggingOut,
  onLogout,
  onChangePassword,
  children
}: {
  user: string;
  loggingOut: boolean;
  onLogout: () => void;
  onChangePassword: () => void;
  children: React.ReactNode;
}) {
  const [userMenuOpen, setUserMenuOpen] = useState(false);

  return (
    <main className="usage-shell usage-center-shell">
      <header className="usage-center-head">
        <div className="usage-brand-block">
          <UsageBrand />
          <div className="usage-heading">
            <h1>{t("common.usage_center")}</h1>
            <span className="usage-heading-subtitle" lang="en">USAGE CENTER</span>
          </div>
        </div>
        <div className="usage-user-actions">
          <LanguageSelect /><ThemeToggle className="usage-theme-toggle" />
          <Dropdown
            trigger={["click"]}
            placement="bottomRight"
            open={userMenuOpen && !loggingOut}
            onOpenChange={setUserMenuOpen}
            disabled={loggingOut}
            autoFocus
            destroyOnHidden
            classNames={{ root: "usage-user-menu" }}
            menu={{
              "aria-label": t("common.user_actions"),
              items: [
                { key: "password", label: t("common.change_password"), icon: <LockOutlined aria-hidden="true" />, disabled: loggingOut },
                { key: "logout", label: t("common.sign_out"), icon: <LogoutOutlined aria-hidden="true" />, disabled: loggingOut }
              ],
              onClick: ({ key }) => {
                setUserMenuOpen(false);
                if (loggingOut) return;
                if (key === "password") onChangePassword();
                if (key === "logout") onLogout();
              }
            }}
          >
            <button
              className="usage-user-badge"
              type="button"
              title={user}
              aria-label={t("common.user_menu", [user])}
              aria-haspopup="menu"
              aria-expanded={userMenuOpen && !loggingOut}
              disabled={loggingOut}
            >
              <span className="usage-user-name">{loggingOut ? t("common.signing_out_2") : user}</span>
              <DownOutlined className="usage-user-menu-arrow" aria-hidden="true" />
            </button>
          </Dropdown>
        </div>
      </header>
      <div className="usage-center-content">{children}</div>
    </main>
  );
}

function UsageLoading() {
  return (
    <div className="usage-loading" aria-label={t("common.loading_usage_center")}>
      <div className="skeleton skeleton-title" />
      <div className="skeleton skeleton-line" />
      <div className="skeleton skeleton-table" />
    </div>
  );
}
