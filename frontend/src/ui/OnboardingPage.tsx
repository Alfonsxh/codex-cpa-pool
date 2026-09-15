import "../i18n/admin";
import { LanguageSelect } from "./LanguageSelect";
import { t } from "../i18n";
import {
  ArrowLeftOutlined,
  ArrowRightOutlined,
  CheckOutlined,
  SettingOutlined
} from "@ant-design/icons";
import { Alert, Button, Input, InputNumber, Progress, Result, Skeleton, Tag } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import {
  configurationQueryKey,
  readConfiguration,
  saveConfiguration,
  type ConfigurationCatalog
} from "../api/configuration";
import { saveNotificationWebhook } from "../api/notifications";
import { publicSiteQueryKey } from "../api/public-site";
import {
  onboardingQueryKey,
  readOnboarding,
  saveOnboardingPreferences,
  type OnboardingStatus,
  type OnboardingStep
} from "../api/onboarding";
import { useAdminToolbar } from "./AdminToolbarContext";
import { InitialPasswordModal } from "./InitialPasswordModal";
import { TimezoneSelect } from "./components/TimezoneSelect";
import { defaultSiteTimezone, formatSiteTimestamp, useSiteTimezone } from "./site-time";

const requiredLabels: Record<string, string> = {
  email_domains: t("admin.access_scope"),
  initial_password: t("common.initial_password")
};

const recommendationLabels: Record<string, string> = {
  public_base_url: t("admin.public_address"),
  quota_timezone: t("common.system_timezone"),
  weekly_quota: t("admin.default_quota"),
  notifications: t("admin.notifications"),
  branding: t("admin.brand"),
  proxy: t("admin.upstream_proxy")
};

type OnboardingDrafts = {
  publicURL: string;
  quotaTimezone: string;
  weeklyQuota: number | null;
  webhookURL: string;
  productName: string;
  shortName: string;
  environmentLabel: string;
  proxyURL: string;
};

type OnboardingPreferenceUpdate = {
  skippedRecommended: string[];
  advanceAfterSave: boolean;
};

export function OnboardingPage({ csrfToken }: { csrfToken: string }) {
  const siteTimezone = useSiteTimezone();
  const location = useLocation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const { setRefreshAction, setRefreshLabel, setRefreshing } = useAdminToolbar();
  const configurationHydrated = useRef(false);
  const [domains, setDomains] = useState("");
  const [drafts, setDrafts] = useState<OnboardingDrafts>({
    publicURL: window.location.origin,
    quotaTimezone: defaultSiteTimezone,
    weeklyQuota: null,
    webhookURL: "",
    productName: "",
    shortName: "",
    environmentLabel: "",
    proxyURL: ""
  });
  const [notice, setNotice] = useState("");
  const [initialPasswordOpen, setInitialPasswordOpen] = useState(false);

  const onboarding = useQuery({
    queryKey: onboardingQueryKey,
    queryFn: ({ signal }) => readOnboarding(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const catalog = useQuery({
    queryKey: configurationQueryKey,
    queryFn: ({ signal }) => readConfiguration(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const selectedID = searchParams.get("step") ?? "";
  const selected = useMemo(() => selectOnboardingStep(onboarding.data, selectedID), [onboarding.data, selectedID]);
  const configurationValues = useMemo(() => configurationValueMap(catalog.data), [catalog.data]);
  const proxyConfigured = configurationField(catalog.data, "cpa.proxy_url")?.configured === true;

  useEffect(() => setRefreshing(onboarding.isFetching || catalog.isFetching), [catalog.isFetching, onboarding.isFetching, setRefreshing]);
  useEffect(() => {
    if (onboarding.data) setRefreshLabel(t("admin.setup_status_updated", [formatSiteTimestamp(onboarding.data.generated_at)]));
    return () => setRefreshLabel("");
  }, [onboarding.data, setRefreshLabel, siteTimezone]);
  useEffect(() => {
    setRefreshAction(async () => {
      const results = await Promise.all([onboarding.refetch(), catalog.refetch()]);
      const error = results.find((result) => result.error)?.error;
      if (error) throw error;
    });
    return () => setRefreshAction(null);
  }, [catalog, onboarding, setRefreshAction]);
  useEffect(() => {
    if (!location.pathname.startsWith("/setup") || !onboarding.data || !selected || selectedID === selected.id) return;
    setSearchParams({ step: selected.id }, { replace: true });
  }, [location.pathname, onboarding.data, selected, selectedID, setSearchParams]);
  useEffect(() => {
    if (!catalog.data || configurationHydrated.current) return;
    configurationHydrated.current = true;
    setDrafts((current) => ({
      ...current,
      publicURL: configurationStringValue(catalog.data, "branding.public_base_url") || window.location.origin,
      quotaTimezone: configurationStringValue(catalog.data, "system.timezone").trim() || defaultSiteTimezone,
      weeklyQuota: configurationNumberValue(catalog.data, "user_quota.default_weekly_tokens"),
      productName: configurationStringValue(catalog.data, "branding.product_name"),
      shortName: configurationStringValue(catalog.data, "branding.short_name"),
      environmentLabel: configurationStringValue(catalog.data, "branding.environment_label")
    }));
  }, [catalog.data]);

  const advanceAfterSave = () => {
    const steps = onboarding.data?.steps ?? [];
    const currentIndex = steps.findIndex((step) => step.id === selected?.id);
    const next = currentIndex >= 0 ? steps[currentIndex + 1] : undefined;
    if (next) setSearchParams({ step: next.id });
  };

  const preferences = useMutation({
    mutationFn: ({ skippedRecommended }: OnboardingPreferenceUpdate) => (
      saveOnboardingPreferences(skippedRecommended, csrfToken)
    ),
    onSuccess: (result, update) => {
      queryClient.setQueryData(onboardingQueryKey, result);
      setNotice(t("admin.setup_preferences_saved"));
      if (update.advanceAfterSave) advanceAfterSave();
    }
  });
  const configuration = useMutation({
    mutationFn: (values: Record<string, unknown>) => saveConfiguration(values, csrfToken),
    onSuccess: async (result) => {
      setNotice(t("admin.completion_status_has_been_checked_again", [result.message]));
      await Promise.all([
        catalog.refetch(),
        queryClient.invalidateQueries({ queryKey: onboardingQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: publicSiteQueryKey })
      ]);
      advanceAfterSave();
    }
  });
  const notificationWebhook = useMutation({
    mutationFn: () => saveNotificationWebhook(drafts.webhookURL.trim(), csrfToken),
    onSuccess: async (result) => {
      setDrafts((current) => ({ ...current, webhookURL: "" }));
      setNotice(t("admin.completion_status_has_been_checked_again", [result.message]));
      await queryClient.invalidateQueries({ queryKey: onboardingQueryKey, exact: true });
      advanceAfterSave();
    }
  });
  if (onboarding.isPending) {
    return (
      <section className="page-content onboarding-page" aria-label={t("admin.loading_initial_setup")}>
        <div className="onboarding-shell"><Skeleton active paragraph={{ rows: 12 }} /></div>
      </section>
    );
  }
  if (onboarding.isError || !onboarding.data || !selected) {
    return (
      <section className="page-content onboarding-page">
        <Result
          status="warning"
          title={t("admin.initial_setup_status_unavailable")}
          subTitle={onboarding.error instanceof Error ? onboarding.error.message : t("admin.unable_to_read_setup_status_please_try_again_later")}
          extra={[
            <Button key="retry" type="primary" onClick={() => void onboarding.refetch()}>{t("common.reload")}</Button>
          ]}
        />
      </section>
    );
  }

  const status = onboarding.data;
  const steps = status.steps;
  const selectedIndex = steps.findIndex((step) => step.id === selected.id);
  const completedCount = status.required.complete + status.recommended.complete + status.recommended.skipped;
  const totalCount = status.required.total + status.recommended.total;
  const completionPercent = Math.round(completedCount / Math.max(1, totalCount) * 100);
  const updateSkipped = (stepID: string, skipped: boolean) => {
    const next = skipped
      ? Array.from(new Set([...status.skipped_recommended, stepID]))
      : status.skipped_recommended.filter((id) => id !== stepID);
    preferences.mutate({ skippedRecommended: next, advanceAfterSave: skipped });
  };
  const jump = (index: number) => {
    const target = steps[index];
    if (target) setSearchParams({ step: target.id });
  };
  const saveDomains = () => configuration.mutate({ "identity.allowed_email_domains": domains.trim() });
  const updateDraft = (key: keyof OnboardingDrafts, value: OnboardingDrafts[keyof OnboardingDrafts]) => {
    setDrafts((current) => ({ ...current, [key]: value }));
  };

  return (
    <section className="page-content onboarding-page">
      <div className="onboarding-shell">
        <header className="onboarding-hero">
          <div>
            <h2>{t("admin.complete_basic_setup")}</h2>
            <p>{t("admin.configure_access_initial_passwords_and_runtime_settings_here_other_operations")}</p>
          </div>
          <div className="onboarding-hero-actions">
            <LanguageSelect />
            <div className="onboarding-progress-card">
              <strong>{completedCount}<span>/{totalCount}</span></strong>
              <div><span>{t("admin.setup_progress")}</span><Progress percent={completionPercent} showInfo={false} size="small" /></div>
            </div>
          </div>
        </header>

        {notice ? <Alert className="page-alert" type="success" showIcon closable title={notice} onClose={() => setNotice("")} /> : null}
        {preferences.isError || configuration.isError || notificationWebhook.isError ? (
          <Alert
            className="page-alert"
            type="error"
            showIcon
            title={t("admin.settings_were_not_saved")}
            description={(preferences.error ?? configuration.error ?? notificationWebhook.error) instanceof Error
              ? (preferences.error ?? configuration.error ?? notificationWebhook.error as Error).message
              : t("common.please_try_again_later")}
          />
        ) : null}

        <div className="onboarding-workspace">
          <aside className="onboarding-steps" aria-label={t("admin.setup")}>
            <OnboardingStepList
              steps={steps}
              selectedID={selected.id}
              onSelect={(id) => setSearchParams({ step: id })}
            />
          </aside>

          <main className="onboarding-step-panel">
            <div className="onboarding-step-heading">
              <div>
                <h3>{(selected.title)}</h3>
                <p>{(selected.description)}</p>
              </div>
              <StepStatusTag status={selected.status} />
            </div>

            <OnboardingStepAction
              step={selected}
              domains={domains}
              drafts={drafts}
              configurationValues={configurationValues}
              configurationPending={catalog.isPending}
              configurationError={catalog.error}
              proxyConfigured={proxyConfigured}
              pending={configuration.isPending || notificationWebhook.isPending}
              onDomainsChange={setDomains}
              onDraftChange={updateDraft}
              onSaveDomains={saveDomains}
              onSaveConfiguration={(values) => configuration.mutate(values)}
              onSaveNotification={() => notificationWebhook.mutate()}
              onRetryConfiguration={() => void catalog.refetch()}
              onOpenInitialPassword={() => setInitialPasswordOpen(true)}
              onNavigate={() => navigate(selected.action_path)}
            />

            <footer className="onboarding-step-footer">
              <Button icon={<ArrowLeftOutlined />} disabled={selectedIndex <= 0} onClick={() => jump(selectedIndex - 1)}>{t("admin.previous_step")}</Button>
              <span>{t("admin.step")} {selectedIndex + 1} {t("admin.of")} {steps.length} {t("admin.steps")}</span>
              <div className="onboarding-step-footer-actions">
                {selected.kind === "recommended" && selected.status !== "complete" ? (
                  selected.status === "skipped" ? (
                    <Button disabled={preferences.isPending} onClick={() => updateSkipped(selected.id, false)}>{t("admin.configure_again")}</Button>
                  ) : (
                    <Button disabled={preferences.isPending} onClick={() => updateSkipped(selected.id, true)}>{t("admin.skip_for_now")}</Button>
                  )
                ) : null}
                {selectedIndex < steps.length - 1 ? (
                  <Button type="primary" onClick={() => jump(selectedIndex + 1)}>{t("admin.next_step")}<ArrowRightOutlined /></Button>
                ) : (
                  <Button type="primary" onClick={() => navigate("/overview")}>{t("admin.open_overview")}<ArrowRightOutlined /></Button>
                )}
              </div>
            </footer>
          </main>
        </div>
      </div>

      <InitialPasswordModal
        open={initialPasswordOpen}
        csrfToken={csrfToken}
        onClose={() => setInitialPasswordOpen(false)}
        onSuccess={(message) => {
          setInitialPasswordOpen(false);
          setNotice(message);
          void queryClient.invalidateQueries({ queryKey: onboardingQueryKey, exact: true });
          advanceAfterSave();
        }}
      />
    </section>
  );
}

function OnboardingStepList({
  steps,
  selectedID,
  onSelect
}: {
  steps: OnboardingStep[];
  selectedID: string;
  onSelect: (id: string) => void;
}) {
  return (
    <section className="onboarding-step-group">
      <nav aria-label={t("admin.setup")}>
        {steps.map((step, index) => (
          <button
            key={step.id}
            className={selectedID === step.id ? "active" : ""}
            type="button"
            aria-current={selectedID === step.id ? "step" : undefined}
            onClick={() => onSelect(step.id)}
          >
            <span className={`onboarding-step-index status-${step.status}`} aria-hidden="true">
              {step.status === "complete" ? <CheckOutlined /> : index + 1}
            </span>
            <span><strong>{requiredLabels[step.id] ?? recommendationLabels[step.id] ?? step.title}</strong><small>{(step.title)}</small></span>
            <i className={`onboarding-step-dot status-${step.status}`} aria-label={stepStatusLabel(step.status)} />
          </button>
        ))}
      </nav>
    </section>
  );
}

function OnboardingStepAction({
  step,
  domains,
  drafts,
  configurationValues,
  configurationPending,
  configurationError,
  proxyConfigured,
  pending,
  onDomainsChange,
  onDraftChange,
  onSaveDomains,
  onSaveConfiguration,
  onSaveNotification,
  onRetryConfiguration,
  onOpenInitialPassword,
  onNavigate
}: {
  step: OnboardingStep;
  domains: string;
  drafts: OnboardingDrafts;
  configurationValues: Record<string, unknown>;
  configurationPending: boolean;
  configurationError: unknown;
  proxyConfigured: boolean;
  pending: boolean;
  onDomainsChange: (value: string) => void;
  onDraftChange: (key: keyof OnboardingDrafts, value: OnboardingDrafts[keyof OnboardingDrafts]) => void;
  onSaveDomains: () => void;
  onSaveConfiguration: (values: Record<string, unknown>) => void;
  onSaveNotification: () => void;
  onRetryConfiguration: () => void;
  onOpenInitialPassword: () => void;
  onNavigate: () => void;
}) {
  if (step.status === "complete") {
    return <div className="onboarding-complete-state"><CheckOutlined /><div><strong>{t("admin.this_step_is_complete")}</strong><p>{t("admin.status_is_checked_live_by_the_control_plane_no_repeat")}</p></div></div>;
  }
  if (step.id === "email_domains") {
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-email-domains">{t("admin.allowed_email_domains")}</label>
        <Input.TextArea id="onboarding-email-domains" value={domains} onChange={(event) => onDomainsChange(event.target.value)} autoSize={{ minRows: 2, maxRows: 4 }} placeholder="example.com, example.org" />
        <small>{t("admin.separate_with_commas_spaces_or_newlines_emails_outside_these_domains")}</small>
        <Button type="primary" loading={pending} disabled={!domains.trim()} onClick={onSaveDomains}>{t("admin.save_check")}</Button>
      </div>
    );
  }
  if (step.id === "initial_password") {
    return (
      <div className="onboarding-action-card">
        <SettingOutlined aria-hidden="true" />
        <div><strong>{t("admin.the_password_is_write_only_and_never_displayed_again")}</strong><p>{t("admin.once_set_the_system_uses_this_initial_password_when_creating")}</p></div>
        <Button type="primary" onClick={onOpenInitialPassword}>{t("admin.set_initial_password")}</Button>
      </div>
    );
  }
  if (step.kind === "required") {
    return (
      <div className="onboarding-action-card">
        <SettingOutlined aria-hidden="true" />
        <div><strong>{t("admin.open_the_management_page")}</strong><p>{t("admin.return_here_after_finishing_the_system_will_read_the_current")}</p></div>
        <Button type="primary" onClick={onNavigate}>{t("admin.open_settings")}<ArrowRightOutlined /></Button>
      </div>
    );
  }
  if (configurationPending) {
    return <div className="onboarding-inline-form onboarding-form-state" aria-label={t("admin.loading_configuration")}><Skeleton active title={false} paragraph={{ rows: 3 }} /></div>;
  }
  if (configurationError) {
    return (
      <div className="onboarding-action-card">
        <SettingOutlined aria-hidden="true" />
        <div><strong>{t("admin.configuration_unavailable")}</strong><p>{configurationError instanceof Error ? configurationError.message : t("admin.unable_to_read_current_configuration_please_try_again_later")}</p></div>
        <Button type="primary" onClick={onRetryConfiguration}>{t("common.read_again")}</Button>
      </div>
    );
  }
  if (step.id === "public_base_url") {
    const valid = /^https?:\/\/[^\s]+$/i.test(drafts.publicURL.trim());
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-public-url">{t("admin.public_url")}</label>
        <Input id="onboarding-public-url" type="url" value={drafts.publicURL} onChange={(event) => onDraftChange("publicURL", event.target.value)} placeholder="https://cpa.example.com" />
        <small>{t("admin.defaults_to_the_current_browser_address_notifications_and_exported_client")}</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={() => onSaveConfiguration({ "branding.public_base_url": drafts.publicURL.trim() })}>{t("admin.use_this_address")}</Button>
      </div>
    );
  }
  if (step.id === "quota_timezone") {
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-system-timezone">{t("common.system_timezone")}</label>
        <TimezoneSelect id="onboarding-system-timezone" value={drafts.quotaTimezone} onChange={(value) => onDraftChange("quotaTimezone", value)} disabled={pending} />
        <small>{t("admin.page_times_usage_calendar_week_quotas_and_notifications_use_this")}</small>
        <Button type="primary" loading={pending} disabled={!drafts.quotaTimezone.trim()} onClick={() => onSaveConfiguration({ "system.timezone": drafts.quotaTimezone.trim() })}>{t("admin.save_timezone")}</Button>
      </div>
    );
  }
  if (step.id === "weekly_quota") {
    const valid = drafts.weeklyQuota !== null && Number.isInteger(drafts.weeklyQuota) && drafts.weeklyQuota > 0 && drafts.weeklyQuota <= 1_000_000_000_000;
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-weekly-quota">{t("admin.default_weekly_quota_for_new_users")}</label>
        <InputNumber id="onboarding-weekly-quota" aria-label={t("admin.default_weekly_quota_for_new_users")} min={1} max={1_000_000_000_000} precision={0} suffix="Token" value={drafts.weeklyQuota} onChange={(value) => onDraftChange("weeklyQuota", typeof value === "number" ? value : null)} placeholder="20000000" />
        <small>{t("admin.weighted_tokens_per_calendar_week_for_example_15_000_000")}</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={() => onSaveConfiguration({ "user_quota.default_weekly_tokens": drafts.weeklyQuota })}>{t("admin.save_default_quota")}</Button>
      </div>
    );
  }
  if (step.id === "notifications") {
    const valid = drafts.webhookURL.trim().startsWith("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=");
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-notification-webhook">{t("admin.wecom_group_webhook")}</label>
        <Input.Password id="onboarding-notification-webhook" value={drafts.webhookURL} onChange={(event) => onDraftChange("webhookURL", event.target.value)} autoComplete="new-password" visibilityToggle={{ tabIndex: -1 }} placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..." />
        <small>{t("admin.the_url_is_written_to_encrypted_storage_setup_status_responses")}</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={onSaveNotification}>{t("admin.save_webhook")}</Button>
      </div>
    );
  }
  if (step.id === "branding") {
    const productName = drafts.productName.trim();
    const shortName = drafts.shortName.trim();
    const environmentLabel = drafts.environmentLabel.trim();
    const valid = productName.length >= 2 && productName.length <= 64 && shortName.length >= 2 && shortName.length <= 32 && environmentLabel.length <= 64;
    const changed = productName !== configurationValues["branding.product_name"] || shortName !== configurationValues["branding.short_name"] || environmentLabel !== configurationValues["branding.environment_label"];
    return (
      <div className="onboarding-inline-form onboarding-inline-form-multi">
        <div className="onboarding-form-fields onboarding-branding-fields">
          <label htmlFor="onboarding-product-name"><span>{t("admin.product_name")}</span><Input id="onboarding-product-name" maxLength={64} value={drafts.productName} onChange={(event) => onDraftChange("productName", event.target.value)} /></label>
          <label htmlFor="onboarding-short-name"><span>{t("admin.short_name")}</span><Input id="onboarding-short-name" maxLength={32} value={drafts.shortName} onChange={(event) => onDraftChange("shortName", event.target.value)} /></label>
          <label htmlFor="onboarding-environment-label"><span>{t("admin.environment_label")}</span><Input id="onboarding-environment-label" maxLength={64} value={drafts.environmentLabel} onChange={(event) => onDraftChange("environmentLabel", event.target.value)} placeholder={t("admin.e_g_engineering_team")} /></label>
        </div>
        <small>{t("admin.change_at_least_one_field_to_mark_this_step_complete")}</small>
        <Button type="primary" loading={pending} disabled={!valid || !changed} onClick={() => onSaveConfiguration({ "branding.product_name": productName, "branding.short_name": shortName, "branding.environment_label": environmentLabel })}>{t("admin.save_branding")}</Button>
      </div>
    );
  }
  if (step.id === "proxy") {
    const proxyURL = drafts.proxyURL.trim();
    const valid = proxyConfigured ? !proxyURL || /^(?:https?|socks5):\/\/[^\s]+$/i.test(proxyURL) : /^(?:https?|socks5):\/\/[^\s]+$/i.test(proxyURL);
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-proxy-url">{t("admin.default_upstream_proxy_url")}</label>
        <Input.Password id="onboarding-proxy-url" value={drafts.proxyURL} onChange={(event) => onDraftChange("proxyURL", event.target.value)} autoComplete="new-password" visibilityToggle={{ tabIndex: -1 }} placeholder={proxyConfigured ? t("admin.saved_encrypted_leave_blank_to_enable_the_existing_proxy") : "socks5://user:password@proxy.example.com:1080"} />
        <small>{t("admin.saving_enables_the_default_proxy_for_all_cpas_set_to")}</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={() => onSaveConfiguration({ "cpa.proxy_enabled": true, ...(proxyURL ? { "cpa.proxy_url": proxyURL } : {}) })}>{t("admin.save_enable_proxy")}</Button>
      </div>
    );
  }
  return null;
}

function StepStatusTag({ status }: { status: OnboardingStep["status"] }) {
  const colors: Record<OnboardingStep["status"], string> = {
    complete: "success",
    incomplete: "processing",
    blocked: "warning",
    skipped: "default",
    unavailable: "error"
  };
  return <Tag color={colors[status]}>{stepStatusLabel(status)}</Tag>;
}

function stepStatusLabel(status: OnboardingStep["status"]) {
  return ({
    complete: t("admin.complete"),
    incomplete: t("admin.needs_setup"),
    blocked: t("admin.waiting_for_prerequisites"),
    skipped: t("common.skipped"),
    unavailable: t("admin.status_unavailable")
  } as const)[status];
}

function selectOnboardingStep(status: OnboardingStatus | undefined, selectedID: string) {
  if (!status?.steps.length) return undefined;
  const explicit = status.steps.find((step) => step.id === selectedID);
  if (explicit) return explicit;
  return status.steps.find((step) => step.kind === "required" && step.status !== "complete")
    ?? status.steps.find((step) => step.kind === "recommended" && !["complete", "skipped"].includes(step.status))
    ?? status.steps[0];
}

function configurationField(catalog: ConfigurationCatalog | undefined, key: string) {
  return catalog?.groups.flatMap((group) => group.fields).find((field) => field.key === key);
}

function configurationValueMap(catalog: ConfigurationCatalog | undefined): Record<string, unknown> {
  return Object.fromEntries(catalog?.groups.flatMap((group) => group.fields).map((field) => [field.key, field.value]) ?? []);
}

function configurationStringValue(catalog: ConfigurationCatalog, key: string): string {
  const value = configurationField(catalog, key)?.value;
  return typeof value === "string" ? value : "";
}

function configurationNumberValue(catalog: ConfigurationCatalog, key: string): number | null {
  const value = configurationField(catalog, key)?.value;
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}
