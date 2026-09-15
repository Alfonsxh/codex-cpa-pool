import { LanguageSelect } from "./LanguageSelect";
import { t } from "../i18n";
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { Link, Navigate, Route, Routes } from "react-router-dom";

import { ApiError } from "../api/client";
import { applicationHref } from "../application-links";
import {
  defaultPublicSiteConfiguration,
  listNativeAccounts,
  nativeAccountsQueryKey,
  publicSiteQueryKey,
  readPublicSiteConfiguration
} from "../api/public-site";
import type { NativeAccount, PublicSiteConfiguration } from "../api/public-site";
import { ThemeToggle, useTheme, type ThemeMode } from "./ThemeProvider";

export function PortalApp() {
  return (
    <Routes>
      <Route path="/" element={<PortalLandingApp />} />
      <Route path="/native/*" element={<NativeAccountsPage />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

export function PortalLandingApp() {
  const branding = useBranding();
  const { theme } = useTheme();
  usePageTitle(t("common.service_portal_2"));

  return (
    <main className="portal-shell">
      <header className="portal-masthead">
        <section className="portal-hero" aria-labelledby="portal-title">
          <a className="portal-brand" href="/" aria-label={t("common.service_portal", [branding.configuration.product_name])}>
            <img
              src={brandLogoURL(branding.configuration, theme)}
              alt={branding.configuration.product_name}
            />
          </a>
          <h1 id="portal-title">{t("common.choose_your_workspace")}</h1>
          <p className="portal-subtitle">{t("common.manage_cpa_accounts_or_open_usage_center_to_view_your")}</p>
        </section>
        <div className="portal-header-actions">
          <div className="portal-environment"><span aria-hidden="true" /><b>{branding.configuration.environment_label || "Self-hosted service"}</b></div>
          <LanguageSelect /><ThemeToggle className="portal-theme-toggle" />
        </div>
      </header>

      <section className="portal-entry-grid" aria-label={t("common.available_workspaces")}>
        <EntryCard
          className="portal-entry-primary"
          href={applicationHref("admin")}
          number="01"
          badge={t("common.management_key_required")}
          icon="control"
          eyebrow="CONTROL PLANE"
          title={t("common.admin_console")}
          description={t("common.manage_cpa_accounts_users_keys_oauth_containers_logs_and_diagnostic")}
          action={t("common.open_admin_console")}
        />
        <EntryCard
          href={applicationHref("usage")}
          number="02"
          badge={t("common.sign_in_with_email")}
          icon="usage"
          eyebrow="ACCESS & OBSERVABILITY"
          title={t("common.usage_center")}
          description={t("common.view_your_api_key_switch_cpa_accounts_and_track_requests")}
          action={t("common.open_usage_center")}
        />
      </section>
      <PortalFooter productName={branding.configuration.product_name} />
    </main>
  );
}

export function NativeAccountsPage() {
  const branding = useBranding();
  const accounts = useQuery({
    queryKey: nativeAccountsQueryKey,
    queryFn: ({ signal }) => listNativeAccounts(signal),
    retry: false,
    refetchOnWindowFocus: true
  });
  usePageTitle(t("common.cpa_accounts_2"));
  useNativeLightPresentation();

  const nativeAccounts = accounts.data?.accounts ?? [];
  const loginRequired = accounts.error instanceof ApiError && accounts.error.status === 401;
  const countLabel = accounts.isPending
    ? t("common.loading")
    : loginRequired
      ? t("common.admin_sign_in_required")
      : accounts.isError
        ? t("common.list_unavailable")
        : t("common.cpa_accounts", [nativeAccounts.length]);

  return (
    <main className="portal-shell native-page">
      <div className="native-language"><LanguageSelect /></div>
      <Link className="native-back" to="/">{t("common.back_to_portal")}</Link>
      <section className="native-heading">
        <div>
          <p className="native-eyebrow">{branding.configuration.product_name} · BUSINESS ACCOUNTS</p>
          <h1>{t("common.cpa_accounts_2")}</h1>
          <p className="native-subtitle">{t("common.each_upstream_account_has_its_own_cpa_select_an_account")}</p>
        </div>
        <div className="native-environment"><span aria-hidden="true" /><b>{countLabel}</b></div>
      </section>

      <NativeAccountGrid accounts={nativeAccounts} />
      {accounts.isError ? (
        <div className="native-error" role="alert">
          <strong>{loginRequired ? t("common.sign_in_to_admin_first") : t("common.unable_to_load_cpa_accounts")}</strong>
          <span>
            {loginRequired
              ? t("common.sign_in_and_return_here_to_load_accounts_native_ports")
              : t("common.please_try_again_later_or_check_service_status_in_admin")}
            {!loginRequired ? (
              <button type="button" aria-label={t("common.read_again")} onClick={() => void accounts.refetch()}>{t("common.read_again_2")}</button>
            ) : null}
          </span>
        </div>
      ) : null}
      <section className="native-access-note">
        <div><p className="native-kicker">ACCESS CONTROL</p><h2>{t("common.only_administrators_can_add_accounts")}</h2></div>
        <span>{t("common.account_information_is_stored_in_the_control_plane_database_public")}</span>
      </section>
    </main>
  );
}

function NativeAccountGrid({ accounts }: { accounts: NativeAccount[] }) {
  return (
    <section className="native-grid" aria-label={t("common.cpa_native_management_entries")}>
      {accounts.map((account, index) => <NativeAccountCard account={account} index={index} key={account.id} />)}
      <a href={applicationHref("admin", "?action=add-account")} className="native-card native-add-card" aria-label={t("common.add_cpa_account")}>
        <div className="native-card-top"><span className="native-index">＋</span><span className="native-access native-access-guarded">{t("common.admin_only")}</span></div>
        <div>
          <p className="native-kicker">EXPAND ACCOUNT POOL</p>
          <h2>{t("common.add_cpa_account")}</h2>
          <p>{t("common.verify_the_management_key_then_enter_the_account_id_and")}</p>
        </div>
        <div className="native-meta"><span>{t("common.saved_automatically")}</span><b>{t("common.add")}</b></div>
      </a>
    </section>
  );
}

function NativeAccountCard({ account, index }: { account: NativeAccount; index: number }) {
  const managementURL = safeManagementURL(account.management_url);
  const content = (
    <>
      <div className="native-card-top">
        <span className="native-index">{String(index + 1).padStart(2, "0")}</span>
        <span className="native-access native-access-public">{t("common.cpa_accounts_2")}</span>
      </div>
      <div>
        <p className="native-kicker">{account.id.toUpperCase()}</p>
        <h2>{account.id}</h2>
        <p>{managementURL ? t("common.accessible_only_from_the_deployment_host") : t("common.native_management_ports_are_not_exposed_publicly")}</p>
      </div>
      <div className="native-meta">
        <span>{account.group_enabled ? t("common.account_enabled") : t("common.account_disabled")}</span>
        <b>{managementURL ? t("common.open") : t("common.local_access_only")}</b>
      </div>
    </>
  );
  if (!managementURL) return <article className="native-card">{content}</article>;
  return <a href={managementURL} rel="noreferrer" className="native-card">{content}</a>;
}

function EntryCard({
  className = "", href, number, badge, icon, eyebrow, title, description, action
}: {
  className?: string;
  href: string;
  number: string;
  badge: string;
  icon: "control" | "usage";
  eyebrow: string;
  title: string;
  description: string;
  action: string;
}) {
  return (
    <a href={href} className={`portal-entry-card ${className}`.trim()}>
      <div className="portal-card-top"><span>{number}</span><span className={`portal-access ${className ? "guarded" : "public"}`}>{badge}</span></div>
      <div className="portal-card-content">
        <span className="portal-card-icon" aria-hidden="true">
          {icon === "control" ? (
            <svg viewBox="0 0 24 24" fill="none"><path d="M4 6.5h16M7 3.5v6M17 3.5v6M6 12h5v5H6zM14 12h4M14 16h4M4 20.5h16" /></svg>
          ) : (
            <svg viewBox="0 0 24 24" fill="none"><path d="M4 19.5V14m5 5.5V9m5 10.5V12m5 7.5V5M3 20.5h18" /></svg>
          )}
        </span>
        <p className="portal-eyebrow">{eyebrow}</p>
        <h2>{title}</h2>
        <p>{description}</p>
      </div>
      <span className="portal-enter"><span>{action}</span><b>→</b></span>
    </a>
  );
}

function PortalFooter({ productName }: { productName: string }) {
  return <footer className="portal-footer"><span>{productName}</span><span>{t("common.select_a_workspace_to_continue")}</span></footer>;
}

function useBranding() {
  const query = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: 1,
    refetchOnWindowFocus: true
  });
  return { configuration: query.data ?? defaultPublicSiteConfiguration, degraded: query.isError };
}

function useNativeLightPresentation() {
  useEffect(() => {
    const root = document.documentElement;
    const previousColorScheme = root.style.colorScheme;
    root.classList.add("native-page-active");
    root.style.colorScheme = "light";
    return () => {
      root.classList.remove("native-page-active");
      root.style.colorScheme = previousColorScheme;
    };
  }, []);
}

function usePageTitle(title: string) {
  useEffect(() => {
    document.title = title;
  }, [title]);
}

function brandLogoURL(configuration: PublicSiteConfiguration, theme: ThemeMode): string {
  if (!configuration.logo.custom || !configuration.logo.sha256) {
    if (theme === "dark" && configuration.logo.url.endsWith("codex-cpa-pool-logo.svg")) {
      return configuration.logo.url.replace(/\.svg$/, "-dark.svg");
    }
    return configuration.logo.url;
  }
  const separator = configuration.logo.url.includes("?") ? "&" : "?";
  return `${configuration.logo.url}${separator}v=${encodeURIComponent(configuration.logo.sha256.slice(0, 16))}`;
}

export function safeManagementURL(value?: string): string | undefined {
  if (!value) return undefined;
  try {
    const parsed = new URL(value);
    const hostname = parsed.hostname.replace(/^\[|\]$/g, "").toLowerCase();
    const loopback = hostname === "localhost" || hostname === "::1" || /^127(?:\.\d{1,3}){3}$/.test(hostname);
    if (parsed.protocol !== "http:" || !loopback || !parsed.port || parsed.username || parsed.password ||
      parsed.pathname !== "/management.html" || parsed.search || parsed.hash) {
      return undefined;
    }
    return parsed.href;
  } catch {
    return undefined;
  }
}
