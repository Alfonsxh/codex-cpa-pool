import "../i18n/admin";
import { t, getIntlLocale } from "../i18n";
import { useSiteTimezone, formatSiteTimestamp } from "./site-time";
import { Alert, Button, Form, Input, Modal, Select } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { Controller, useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useSearchParams } from "react-router-dom";

import { ApiError } from "../api/client";
import {
  configurationQueryKey,
  readConfiguration,
  saveConfiguration,
  type ConfigurationCatalog,
  type ConfigurationField,
  type ConfigurationValue
} from "../api/configuration";
import {
  generalSettingsQueryKey,
  readGeneralSettings,
  resetBrandingLogo,
  rotateManagementKey,
  saveBrandingLogo
} from "../api/general-settings";
import {
  clearNotificationWebhook,
  notificationSettingsQueryKey,
  readNotificationSettings,
  saveNotificationWebhook,
  sendNotification,
  testNotification,
  type NotificationSettings,
  type NotificationStatus
} from "../api/notifications";
import {
  readSettingsWorkspace,
  settingsWorkspaceQueryKey
} from "../api/settings-workspace";
import {
  applyUserQuotaAction,
  readUserQuotaOperations,
  userQuotaOperationsQueryKey
} from "../api/users";
import { useAdminToolbar } from "./AdminToolbarContext";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { PageState } from "./components/PageState";
import { NotificationRuntimeStatus } from "./components/NotificationRuntimeStatus";
import { formatTokenAmount, tokenInputPresentation, tokenReadableParts, tokenReadableText } from "./formatters";
import { InitialPasswordModal } from "./InitialPasswordModal";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { LegacyPasswordInput } from "./components/LegacyPasswordInput";

import { TimezoneSelect } from "./components/TimezoneSelect";
import { publicSiteQueryKey } from "../api/public-site";
import { useTheme } from "./ThemeProvider";

import { configurationCategories, configurationControlWidth, configurationSections, configurationSectionFor, legacyConfigurationSection, type ConfigurationCategory } from "./configuration-layout";
import "./configuration-page.css";
import { CPAReleaseStatus, TicketPluginSettings, softwareVersionsQueryKey, ticketAccountIDs } from "./components/SoftwareVersions";

type DraftValue = string | number | boolean | null;
type Draft = Record<string, DraftValue>;
type EditorField = ConfigurationField & { group: ConfigurationCategory; section: string };
const ConfigurationSavingContext = createContext(false);

const managementKeySchema = z.object({
  newKey: z.string().min(12, t("admin.enter_at_least_12_characters")).max(128, t("admin.enter_no_more_than_128_characters")).regex(/^\S+$/, t("admin.whitespace_is_not_allowed")),
  confirmation: z.string().min(1, t("admin.enter_the_new_management_key_again"))
}).refine((values) => values.newKey === values.confirmation, {
  path: ["confirmation"],
  message: t("admin.the_management_keys_do_not_match")
});
const quotaResetSchema = z.object({
  reason: z.string().trim().min(4, t("admin.enter_a_reset_reason_of_at_least_4_characters")).max(240, t("admin.the_reason_must_not_exceed_240_characters")),
  confirmation: z.literal("RESET ALL USERS", { message: t("admin.enter_reset_all_users") })
});
type ManagementKeyValues = z.infer<typeof managementKeySchema>;
type QuotaResetValues = z.infer<typeof quotaResetSchema>;

const maxLogoBytes = 2 * 1024 * 1024;
const supportedLogoTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"]);
const modelMultiplierPrefix = "user_quota.model_multiplier.";
const reasoningMultiplierPrefix = "user_quota.reasoning_multiplier.";
const reasoningColorPrefix = "admin.account_usage.reasoning_effort_color.";
const reasoningEfforts = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "auto", "unknown"] as const;

export function ConfigurationPage({
  csrfToken,
  onManagementKeyRotated = () => undefined
}: {
  csrfToken: string;
  onManagementKeyRotated?: (message: string) => void;
}) {
  useSiteTimezone();
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel, setPageDetail } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const [category, setCategory] = useState<ConfigurationCategory>("admin.brand_identity");
  const [selectedSectionId, setSelectedSectionId] = useState("brand");
  const [expandedCategories, setExpandedCategories] = useState<Record<string, boolean>>(() => Object.fromEntries(configurationCategories.map(({ name }) => [name, true])));
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [draft, setDraft] = useState<Draft>({});
  const [focusKey, setFocusKey] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [initialPasswordOpen, setInitialPasswordOpen] = useState(false);
  const [managementKeyOpen, setManagementKeyOpen] = useState(false);
  const [logoError, setLogoError] = useState("");
  const [logoResetOpen, setLogoResetOpen] = useState(false);
  const [webhookDraft, setWebhookDraft] = useState("");
  const [webhookEditing, setWebhookEditing] = useState(false);
  const [webhookError, setWebhookError] = useState("");
  const [webhookClearOpen, setWebhookClearOpen] = useState(false);
  const [quotaResetOpen, setQuotaResetOpen] = useState(false);
  const workspaceContentRef = useRef<HTMLDivElement>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const handledDeepLink = useRef("");

  const catalog = useQuery({
    queryKey: configurationQueryKey,
    queryFn: ({ signal }) => readConfiguration(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const general = useQuery({
    queryKey: generalSettingsQueryKey,
    queryFn: ({ signal }) => readGeneralSettings(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const notification = useQuery({
    queryKey: notificationSettingsQueryKey,
    queryFn: ({ signal }) => readNotificationSettings(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: true,
    refetchInterval: 30_000
  });
  const workspace = useQuery({
    queryKey: settingsWorkspaceQueryKey,
    queryFn: ({ signal }) => readSettingsWorkspace(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const fields = useMemo(() => flattenConfiguration(catalog.data), [catalog.data]);
  const availableSections = useMemo(() => configurationSections.filter((section) =>
    fields.some((field) => field.section === section.id)
    || ["access", "backups", "storage", "audit", "quota", "notifications"].includes(section.id)
    || (section.id === "brand" && fields.length > 0)
  ), [fields]);
  const activeSection = availableSections.find((section) => section.category === category && section.id === selectedSectionId)
    ?? availableSections.find((section) => section.category === category);
  const quotaOperations = useQuery({
    queryKey: userQuotaOperationsQueryKey,
    queryFn: ({ signal }) => readUserQuotaOperations(signal),
    enabled: activeSection?.id === "quota",
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });

  const dirtyFields = useMemo(
    () => fields.filter((field) => !sameConfigurationValue(normalizeDraftValue(field, draft[field.key]), field.value)),
    [draft, fields]
  );
  const errors = useMemo(() => Object.fromEntries(
    fields.map((field) => [field.key, validateDraftValue(field, draft[field.key])]).filter(([, error]) => Boolean(error))
  ) as Record<string, string>, [draft, fields]);

  useEffect(() => {
    if (!catalog.data) return;
    setDraft((current) => Object.keys(current).length ? current : configurationDraft(catalog.data));
  }, [catalog.data]);

  useEffect(() => {
    if (!catalog.data) return;
    const signature = searchParams.toString();
    if (handledDeepLink.current === signature) return;
    handledDeepLink.current = signature;
    const key = searchParams.get("key") ?? "";
    const field = fields.find((item) => item.key === key);
    const requestedSection = searchParams.get("section") ?? "";
    const section = availableSections.find((item) => item.id === requestedSection)
      ?? legacyConfigurationSection(requestedSection);
    const group = searchParams.get("group") ?? "";
    const current = configurationCategories.find((item) => item.name === group);
    const legacy = current ? undefined : legacyConfigurationSection(group);
    const requestedCategory = field?.group ?? section?.category ?? current?.name ?? legacy?.category ?? "admin.brand_identity";
    const destination = availableSections.some((item) => item.category === requestedCategory) ? requestedCategory : "admin.brand_identity";
    setCategory(destination);
    const targetSection = field?.section ?? section?.id ?? legacy?.id
      ?? availableSections.find((item) => item.category === destination)?.id ?? "";
    setSelectedSectionId(targetSection);
    setExpandedCategories((previous) => ({ ...previous, [destination]: true }));
    setFocusKey(field?.key ?? (requestedSection === "quota-reset" || group === "用量维护" ? "quota-reset" : ""));
    setSearch("");
    setMobileNavigationOpen(false);
  }, [availableSections, catalog.data, fields, searchParams]);

  useEffect(() => {
    const current = configurationCategories.find((item) => item.name === category)!;
    setPageDetail({ title: t(current.name), sectionTitle: activeSection?.title, eyebrow: current.eyebrow });
    return () => setPageDetail(null);
  }, [activeSection, category, setPageDetail]);

  const refreshWorkspace = useCallback(async (notify = false) => {
    setRefreshing(true);
    try {
      const results = await Promise.all([catalog.refetch(), general.refetch(), notification.refetch(), workspace.refetch()]);
      const resultError = results.find((result) => result.error)?.error;
      if (resultError) throw resultError;
      setRefreshLabel(t("admin.configuration_refreshed"));
      if (notify) showToast(t("admin.configuration_center_refreshed"));
    } catch (error) {
      if (notify) showToast(error instanceof Error ? error.message : t("admin.unable_to_refresh_configuration_center"), "error");
      throw error;
    } finally {
      setRefreshing(false);
    }
  }, [catalog, general, notification, workspace, setRefreshLabel, setRefreshing, showToast]);

  useEffect(() => {
    setRefreshAction(() => refreshWorkspace(true));
    return () => setRefreshAction(null);
  }, [refreshWorkspace, setRefreshAction]);

  useEffect(() => {
    if (!catalog.data || !general.data || !notification.data || !workspace.data) return;
    setRefreshLabel(t("admin.configuration_refreshed"));
  }, [catalog.data, general.data, notification.data, setRefreshLabel, workspace.data]);

  const notificationLoaded = Boolean(notification.data);
  useEffect(() => {
    if (!focusKey) return;
    const target = [...document.querySelectorAll<HTMLElement>("[data-configuration-field]")]
      .find((item) => item.dataset.configurationField === focusKey);
    if (!target) return;
    const details = target.closest("details");
    if (details) details.open = true;
    target.classList.add("configuration-field-highlight");
    target.scrollIntoView?.({ block: "center", behavior: "smooth" });
    target.querySelector<HTMLElement>('input:not([type="hidden"]):not([type="file"]):not(:disabled), select:not(.enhanced-select-native):not(:disabled), textarea:not(:disabled), button:not(:disabled)')?.focus({ preventScroll: true });
    const timer = window.setTimeout(() => target.classList.remove("configuration-field-highlight"), 1_600);
    return () => { window.clearTimeout(timer); target.classList.remove("configuration-field-highlight"); };
  }, [focusKey, category, selectedSectionId, catalog.data, general.data, notificationLoaded, workspace.data]);

  const saveMutation = useMutation({
    onMutate: () => setSaveError(""),
    mutationFn: () => saveConfiguration(
      Object.fromEntries(dirtyFields.map((field) => [field.key, normalizeDraftValue(field, draft[field.key])])),
      csrfToken
    ),
    onSuccess: async (result) => {
      setConfirmOpen(false);
      setSaveError("");
      showToast(result.message);
      await queryClient.invalidateQueries({ queryKey: publicSiteQueryKey });
      const refreshed = await catalog.refetch();
      if (refreshed.data) setDraft(configurationDraft(refreshed.data));
      if (dirtyFields.some((field) => field.key.startsWith("plugins.") || field.key.startsWith("software."))) {
        await queryClient.invalidateQueries({ queryKey: softwareVersionsQueryKey });
      }
      if (dirtyFields.some((field) => field.key.startsWith(reasoningColorPrefix))) {
        const stylesheet = document.querySelector<HTMLLinkElement>('link[href*="reasoning-effort-colors.css"]');
        if (stylesheet) stylesheet.href = `/admin/reasoning-effort-colors.css?v=${Date.now()}`;
      }
    },
    onError: () => undefined
  });
  const logoMutation = useMutation({
    gcTime: 0,
    mutationFn: (file: File) => saveBrandingLogo(file, csrfToken),
    onSuccess: async (result) => {
      setLogoError("");
      showToast(result.message);
      await general.refetch();
    },
    onError: (error) => setLogoError(error instanceof Error ? error.message : t("admin.logo_was_not_saved"))
  });
  const logoResetMutation = useMutation({
    mutationFn: () => resetBrandingLogo(csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      await general.refetch();
    },
    onError: (error) => setLogoError(error instanceof Error ? error.message : t("admin.logo_was_not_restored"))
  });
  const webhookMutation = useMutation({
    gcTime: 0,
    mutationFn: () => saveNotificationWebhook(webhookDraft, csrfToken),
    onSuccess: async (result) => {
      setWebhookDraft("");
      setWebhookEditing(false);
      setWebhookError("");
      queryClient.setQueryData<NotificationSettings>(notificationSettingsQueryKey, (current) =>
        current && result.notifications ? { ...current, notifications: result.notifications } : current);
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => setWebhookError(error instanceof Error ? error.message : t("admin.webhook_was_not_saved"))
  });
  const webhookClearMutation = useMutation({
    mutationFn: () => clearNotificationWebhook(csrfToken),
    onSuccess: async (result) => {
      setWebhookClearOpen(false);
      setWebhookDraft("");
      setWebhookEditing(false);
      setWebhookError("");
      queryClient.setQueryData<NotificationSettings>(notificationSettingsQueryKey, (current) =>
        current && result.notifications ? { ...current, notifications: result.notifications } : current);
      // Clearing the Webhook also disables notifications on the server. Rebase
      // this one field so a later global save cannot restore the old switch.
      queryClient.setQueryData<ConfigurationCatalog>(configurationQueryKey, (current) => current && ({
        ...current,
        groups: current.groups.map((group) => ({ ...group, fields: group.fields.map((field) =>
          field.key === "notification.enabled" ? { ...field, value: false } : field) }))
      }));
      setDraft((current) => ({ ...current, "notification.enabled": false }));
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => setWebhookError(error instanceof Error ? error.message : t("admin.webhook_was_not_cleared"))
  });
  const notificationSendMutation = useMutation({
    mutationFn: () => sendNotification(csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => showToast(error instanceof Error ? error.message : t("admin.unable_to_send_account_information"), "error")
  });
  const notificationTestMutation = useMutation({
    mutationFn: () => testNotification(csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => showToast(error instanceof Error ? error.message : t("admin.unable_to_send_test_message"), "error")
  });

  const managementKeyForm = useForm<ManagementKeyValues>({
    resolver: zodResolver(managementKeySchema),
    defaultValues: { newKey: "", confirmation: "" }
  });
  const managementKeyMutation = useMutation({
    gcTime: 0,
    mutationFn: () => rotateManagementKey(managementKeyForm.getValues("newKey"), managementKeyForm.getValues("confirmation"), csrfToken),
    onSuccess: (result) => {
      managementKeyForm.reset();
      setManagementKeyOpen(false);
      onManagementKeyRotated(result.message);
    }
  });
  const quotaResetForm = useForm<QuotaResetValues>({
    resolver: zodResolver(quotaResetSchema),
    defaultValues: { reason: "", confirmation: "" as QuotaResetValues["confirmation"] }
  });
  const quotaResetMutation = useMutation({
    mutationFn: () => applyUserQuotaAction({
      action: "reset_usage",
      scope: "all",
      users: [],
      reason: quotaResetForm.getValues("reason"),
      confirm: "reset_all_current_week_usage"
    }, csrfToken),
    onSuccess: (result) => {
      quotaResetForm.reset();
      setQuotaResetOpen(false);
      showToast(result.message);
      void quotaOperations.refetch();
    }
  });

  const pending = catalog.isPending || general.isPending || notification.isPending || workspace.isPending;
  const loadError = catalog.error ?? general.error ?? (!notification.data ? notification.error : null) ?? workspace.error;
  if (pending) return <ConfigurationSkeleton />;
  if (loadError || !catalog.data || !general.data || !notification.data || !workspace.data) {
    return (
      <section className="page-content legacy-settings-page">
        <PageState kind="error" title={t("admin.unable_to_load_configuration_center")} detail={loadError instanceof Error ? loadError.message : t("admin.configuration_center_data_is_incomplete")} onAction={() => void refreshWorkspace(false)} />
      </section>
    );
  }

  const selectedSections = activeSection ? [activeSection] : [];
  const dirtyCategories = new Set(dirtyFields.map((field) => field.group));
  const dirtyModes = new Map<string, number>();
  dirtyFields.forEach((field) => {
    const label = applyModeLabel(field.apply_mode, field.key);
    dirtyModes.set(label, (dirtyModes.get(label) ?? 0) + 1);
  });
  const riskyEffects = configurationEffects(dirtyFields);
  const searchItems = [
    ...fields.map((field) => ({ key: field.key, label: field.label, group: field.group, section: field.section, description: field.description })),
    ...configurationSections.filter((section) => ["access", "backups", "storage", "audit", "notifications"].includes(section.id))
      .map((section) => ({ key: section.id, label: section.title, group: section.category, section: section.id, description: section.description })),
    { key: "quota-reset", label: t("admin.usage_maintenance"), group: "admin.usage_quotas" as const, section: "quota", description: t("admin.reset_all_users_weekly_usage_for_incident_recovery") }
  ];
  const searchMatches = search.trim() ? searchItems.filter((item) => [t(item.group), item.label, item.key, item.description,
    configurationSections.find((section) => section.id === item.section)?.title].join(" ").toLocaleLowerCase(getIntlLocale()).includes(search.trim().toLocaleLowerCase(getIntlLocale()))) : [];
  const managementKeyError = managementKeyForm.formState.errors.newKey?.message
    ?? managementKeyForm.formState.errors.confirmation?.message
    ?? (managementKeyMutation.isError
      ? managementKeyMutation.error instanceof Error ? managementKeyMutation.error.message : t("admin.management_key_was_not_updated")
      : "");
  const selectConfigurationGroup = (group: ConfigurationCategory, key = "", focus = true) => {
    const field = fields.find((item) => item.key === key);
    const section = availableSections.find((item) => item.id === (field?.section ?? legacyConfigurationSection(key)?.id ?? key))
      ?? availableSections.find((item) => item.category === group);
    const destination = section?.category ?? group;
    setSearch("");
    setCategory(destination);
    setSelectedSectionId(section?.id ?? "");
    setFocusKey(focus ? key : "");
    setExpandedCategories((previous) => ({ ...previous, [destination]: true }));
    const params = new URLSearchParams({ group: destination });
    if (section) params.set("section", section.id);
    if (field) params.set("key", field.key);
    handledDeepLink.current = params.toString();
    setSearchParams(params, { replace: true });
    setMobileNavigationOpen(false);
    if (!focus || !key) {
      workspaceContentRef.current?.scrollTo?.({ top: 0, left: 0 });
      if (mobileNavigationOpen) requestAnimationFrame(() => workspaceContentRef.current?.focus({ preventScroll: true }));
    }
  };
  const updateField = (field: ConfigurationField, value: DraftValue) => setDraft((current) => ({ ...current, [field.key]: value }));
  const requestSave = () => {
    if (!dirtyFields.length) return;
    const invalid = Object.keys(errors)[0];
    if (invalid) {
      const field = fields.find((item) => item.key === invalid);
      if (field) selectConfigurationGroup(field.group, field.key);
      setSaveError(t("admin.fix_these_fields_first", [errors[invalid]]));
      return;
    }
    if (riskyEffects.length) setConfirmOpen(true);
    else saveMutation.mutate();
  };

  return (
    <section className="page-content legacy-settings-page">
      <LegacyToastRegion toasts={toasts} />
      <div className="settings-workspace">
        <aside className="settings-navigation" aria-label={t("admin.configuration_navigation")} data-mobile-open={mobileNavigationOpen}>
          <div className="settings-navigation-fixed">
            <label className="configuration-search">
              <span aria-hidden="true">⌕</span>
              <input aria-label={t("admin.search_settings")} type="search" placeholder={t("admin.search_all_settings")} autoComplete="off" value={search} onChange={(event) => setSearch(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && searchMatches[0]) { event.preventDefault(); selectConfigurationGroup(searchMatches[0].group, searchMatches[0].key); } }} />
            </label>
            <div className="configuration-search-results" hidden={!search.trim()}>
              {searchMatches.length ? searchMatches.map((field) => (
                <button key={field.key} type="button" onClick={() => selectConfigurationGroup(field.group, field.key)}><span>{field.label}</span><small>{t(field.group)} · {field.key}</small></button>
              )) : <p className="configuration-search-empty">{t("admin.no_matches")}</p>}
            </div>
            <button className="settings-mobile-navigation-toggle" type="button" aria-label={t("admin.select_a_setting_section")} aria-expanded={mobileNavigationOpen} aria-controls="configuration-navigation-tree" onClick={() => setMobileNavigationOpen((open) => !open)}>
              <span>{t(category)}{activeSection ? ` / ${activeSection.title}` : ""}</span>
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="m5 7 5 5 5-5" /></svg>
            </button>
            <p className="settings-navigation-label settings-category-label">{t("admin.setting_categories")}</p>
          </div>
          <div className="settings-navigation-scroll">
              <nav id="configuration-navigation-tree" className="configuration-navigation" aria-label={t("admin.setting_categories")}>
                {configurationCategories.map((item) => {
                  const dirtyCount = dirtyFields.filter((field) => field.group === item.name).length;
                  const sections = availableSections.filter((section) => section.category === item.name);
                  const expanded = expandedCategories[item.name] ?? false;
                  const groupId = `configuration-navigation-${item.eyebrow.toLowerCase().replaceAll(" ", "-").replaceAll("&", "and")}`;
                  return <div className="configuration-navigation-group" key={item.name}>
                    <button className="configuration-category-toggle" type="button" aria-label={t(item.name)} aria-expanded={expanded} aria-controls={groupId} aria-describedby={dirtyCount ? `${groupId}-dirty` : undefined} data-current={category === item.name} onClick={() => setExpandedCategories((previous) => ({ ...previous, [item.name]: !expanded }))}>
                      <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="m7 4 6 6-6 6" /></svg>
                      <span>{t(item.name)}</span>
                      {dirtyCount ? <small id={`${groupId}-dirty`} className="dirty">{dirtyCount} {t("admin.changes")}</small> : null}
                    </button>
                    <ul id={groupId} className="configuration-subnavigation" hidden={!expanded}>
                      {sections.map((section) => {
                        const sectionDirtyCount = dirtyFields.filter((field) => field.section === section.id).length;
                        const active = activeSection?.id === section.id;
                        return <li key={section.id}>
                          <button className={active ? "active" : ""} type="button" aria-label={section.title} aria-current={active ? "page" : undefined} aria-controls="configuration-detail" aria-describedby={sectionDirtyCount ? `configuration-dirty-${section.id}` : undefined} onClick={() => selectConfigurationGroup(item.name, section.id, false)}>
                            <span>{section.title}</span>
                            {sectionDirtyCount ? <small id={`configuration-dirty-${section.id}`} className="dirty">{sectionDirtyCount} {t("admin.changes")}</small> : null}
                          </button>
                        </li>;
                      })}
                      {!sections.length ? <li className="configuration-navigation-empty">{t("admin.no_settings")}</li> : null}
                    </ul>
                  </div>;
                })}
              </nav>
          </div>
        </aside>

        <form className="configuration-panel" noValidate aria-busy={saveMutation.isPending} onSubmit={(event) => { event.preventDefault(); requestSave(); }}>
          <div id="configuration-detail" className="settings-workspace-content" ref={workspaceContentRef} role="region" tabIndex={0} aria-label={activeSection?.title ?? t("admin.settings_2")}>
            <ConfigurationSavingContext.Provider value={saveMutation.isPending}>
            <fieldset className="configuration-section-list" disabled={saveMutation.isPending}>
              {selectedSections.map((section) => {
                const sectionFields = fields.filter((field) => field.section === section.id);
                const specialized = (field: EditorField) => section.id === "codex-ticket" || (section.id === "model-multipliers"
                  ? field.key.startsWith(modelMultiplierPrefix)
                  : section.id === "reasoning-multipliers" ? field.key.startsWith(reasoningMultiplierPrefix)
                  : section.id === "appearance" && field.key.startsWith(reasoningColorPrefix));
                const ordinaryFields = sectionFields.filter((field) => !specialized(field));
                const renderField = (key: string) => {
                  const field = sectionFields.find(item => item.key === key);
                  if (!field) return null;
                  const displayed = key === "plugins.codex_ticket.models" ? { ...field, description: t("admin.ticket_models_help") }
                    : key === "software.check_interval_hours" ? { ...field, description: t("admin.ticket_shared_interval") } : field;
                  return <ConfigurationEditor key={key} field={displayed} value={draft[key]} error={errors[key]} dirty={dirtyFields.some(item => item.key === key)} onChange={value => updateField(field, value)} />;
                };
                return <section className="configuration-section" key={section.id} aria-label={section.title} data-configuration-field={section.id}>
                  <div id={`configuration-section-${section.id}`}>
                    {section.id === "codex-ticket" ? <TicketPluginSettings csrfToken={csrfToken} renderField={renderField} tableHeader={<ConfigurationTableHeader />} accounts={String(draft["plugins.codex_ticket.accounts"] ?? "")} accountsError={errors["plugins.codex_ticket.accounts"]} onAccountsChange={value => {
                      const field = sectionFields.find(item => item.key === "plugins.codex_ticket.accounts");
                      if (field) updateField(field, value);
                    }} hasUnsavedChanges={dirtyFields.length > 0} saving={saveMutation.isPending} focusKey={focusKey} /> : null}
                    {section.id === "provisioning" ? <CPAReleaseStatus csrfToken={csrfToken} /> : null}
                    {section.id === "brand" ? <BrandingLogoEditor custom={general.data.branding.custom_logo} sha256={general.data.branding.logo_sha256} pending={logoMutation.isPending || logoResetMutation.isPending} error={logoError} onFile={(file) => { const error = validateLogoFile(file); setLogoError(error); if (!error) logoMutation.mutate(file); }} onReset={() => setLogoResetOpen(true)} /> : null}
                    {ordinaryFields.length ? <div className="configuration-fields">
                      <ConfigurationTableHeader />
                      {ordinaryFields.map((field) => <ConfigurationEditor key={field.key} field={field} value={draft[field.key]} error={errors[field.key]} dirty={dirtyFields.some((item) => item.key === field.key)} onChange={(value) => updateField(field, value)} />)}
                    </div> : null}
                    {section.id === "model-multipliers" ? <ModelMultiplierEditor fields={sectionFields.filter(specialized)} draft={draft} onChange={updateField} /> : null}
                    {section.id === "reasoning-multipliers" || section.id === "appearance" ? <ReasoningStrategyEditor fields={sectionFields.filter(specialized)} draft={draft} onChange={updateField} /> : null}
                    {section.id === "notifications" ? <><p className="configuration-independent-note">{t("admin.the_webhook_is_saved_separately_and_takes_effect_immediately")}</p><NotificationIntegration status={notification.data.notifications} enabled={notification.data.values.enabled} unavailable={notification.isError} value={webhookDraft} error={webhookError} saving={webhookMutation.isPending} clearing={webhookClearMutation.isPending} sending={notificationSendMutation.isPending} testing={notificationTestMutation.isPending} editing={webhookEditing} onEdit={() => { setWebhookError(""); setWebhookEditing(true); }} onCancel={() => { setWebhookDraft(""); setWebhookError(""); setWebhookEditing(false); }} onTest={() => notificationTestMutation.mutate()} onChange={(value) => { setWebhookDraft(value); setWebhookError(""); }} onSave={() => webhookMutation.mutate()} onClear={() => setWebhookClearOpen(true)} onSend={() => notificationSendMutation.mutate()} /></> : null}
                    {section.id === "access" ? <AccessPanel managementKeyConfigured={general.data.security.management_key_configured} initialPasswordConfigured={general.data.security.initial_password_configured} onInitialPassword={() => setInitialPasswordOpen(true)} onManagementKey={() => setManagementKeyOpen(true)} /> : null}
                    {section.id === "backups" ? <BackupsPanel count={workspace.data.backups.count} latest={workspace.data.backups.latest} /> : null}
                    {section.id === "storage" ? <StoragePanel rows={workspace.data.storage} onRefresh={() => refreshWorkspace(true)} /> : null}
                    {section.id === "audit" ? <AuditPanel rows={workspace.data.recent_audit} onRefresh={() => refreshWorkspace(true)} /> : null}
                    {section.id === "quota" ? <QuotaSystemDanger summary={quotaOperations.data} pending={quotaOperations.isPending || quotaOperations.isFetching} failed={quotaOperations.isError} onReset={() => setQuotaResetOpen(true)} /> : null}
                  </div>
                </section>;
              })}
            </fieldset>
            </ConfigurationSavingContext.Provider>
            {!fields.length && category === "admin.brand_identity" ? <div className="configuration-empty-state" role="status"><h3>{t("admin.no_configurable_settings")}</h3><p>{t("admin.manage_access_credentials_in_system_settings")}</p><button className="button button-primary" type="button" onClick={() => selectConfigurationGroup("admin.system_settings", "access")}>{t("admin.open_access_credentials")}</button></div> : null}
          </div>
          <div className="configuration-save-region" hidden={activeSection?.id === "codex-ticket" && !dirtyFields.length && !saveError && !saveMutation.isError}>
            {saveMutation.isError && !confirmOpen ? <p className="form-error" role="alert">{saveMutation.error instanceof Error ? saveMutation.error.message : t("admin.configuration_was_not_saved")}</p> : null}
            {saveError ? <p className="form-error" role="alert">{saveError}</p> : null}
            <div className="configuration-actions"><div className="configuration-change-summary"><span className={`status-chip ${dirtyFields.length ? "warning" : "neutral"}`} role="status">{dirtyFields.length ? t("admin.unsaved_changes", [dirtyFields.length]) : t("admin.no_changes")}</span>{dirtyCategories.size > 1 ? <small>{t("admin.across")} {dirtyCategories.size} {t("admin.categories")}</small> : null}<div className="configuration-impact-summary">{dirtyModes.size ? [...dirtyModes.entries()].map(([label, count]) => <span key={label}><strong>{count}</strong>{label}</span>) : <span>{t("admin.save_changes_together")}</span>}</div></div><div className="configuration-action-buttons"><button className="button button-quiet" type="button" disabled={!dirtyFields.length || saveMutation.isPending} onClick={() => { setDraft(configurationDraft(catalog.data)); setSaveError(""); saveMutation.reset(); }}>{t("admin.discard_unsaved_changes")}</button><button className="button button-primary" type="submit" disabled={!dirtyFields.length || saveMutation.isPending}>{saveMutation.isPending ? t("admin.saving") : t("admin.save_configuration")}</button></div></div>
          </div>
        </form>
      </div>

      <InitialPasswordModal open={initialPasswordOpen} csrfToken={csrfToken} onClose={() => setInitialPasswordOpen(false)} onSuccess={(message) => { setInitialPasswordOpen(false); showToast(message); void general.refetch(); }} />
      <Modal
        className="legacy-account-editor-modal legacy-settings-form-modal"
        open={managementKeyOpen}
        width={560}
        centered
        title={<div className="legacy-dialog-title"><strong>{t("admin.change_management_key")}</strong><span>ACCESS CONTROL</span></div>}
        closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
        transitionName=""
        maskTransitionName=""
        afterOpenChange={(visible) => { if (visible) managementKeyForm.setFocus("newKey"); }}
        onCancel={() => { if (managementKeyMutation.isPending) return; setManagementKeyOpen(false); managementKeyForm.reset(); managementKeyMutation.reset(); }}
        destroyOnHidden
        footer={[
          <Button key="cancel" className="legacy-modal-ghost" tabIndex={-1} disabled={managementKeyMutation.isPending} onClick={() => { setManagementKeyOpen(false); managementKeyForm.reset(); managementKeyMutation.reset(); }}>{t("common.cancel")}</Button>,
          <Button key="submit" type="primary" htmlType="submit" form="settings-management-key-form" disabled={managementKeyMutation.isPending}>{managementKeyMutation.isPending ? t("admin.updating") : t("admin.update_sign_in_again")}</Button>
        ]}
      >
        <p className="warning-banner">{t("admin.all_management_sessions_will_end_immediately_api_keys_user_sessions")}</p>
        <form id="settings-management-key-form" noValidate onSubmit={managementKeyForm.handleSubmit(() => managementKeyMutation.mutate())}>
          <div className="field"><label htmlFor="settings-management-key">{t("admin.new_management_key")}</label><Controller control={managementKeyForm.control} name="newKey" render={({ field }) => <LegacyPasswordInput id="settings-management-key" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={12} maxLength={128} onValueChange={field.onChange} />} /></div>
          <div className="field account-email-field"><label htmlFor="settings-management-key-confirmation">{t("admin.confirm_key")}</label><Controller control={managementKeyForm.control} name="confirmation" render={({ field }) => <LegacyPasswordInput id="settings-management-key-confirmation" ariaLabel={t("admin.enter_the_management_key_again")} value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={12} maxLength={128} onValueChange={field.onChange} />} /></div>
          <p className="form-error" role="alert">{managementKeyError}</p>
        </form>
      </Modal>
      <LegacyConfirmModal title={t("admin.save_settings", [dirtyFields.length])} open={confirmOpen} okText={t("admin.save_apply")} confirmLoading={saveMutation.isPending} onCancel={() => setConfirmOpen(false)} onOk={() => saveMutation.mutate()}><p>{riskyEffects.join("；")}{t("admin.if_applying_fails_the_original_configuration_will_be_restored_where")}</p>{saveMutation.isError ? <Alert type="error" showIcon title={saveMutation.error instanceof Error ? saveMutation.error.message : t("admin.configuration_was_not_saved")} /> : null}</LegacyConfirmModal>
      <LegacyConfirmModal title={t("admin.restore_the_default_logo")} open={logoResetOpen} okText={t("admin.restore_default")} confirmLoading={logoResetMutation.isPending} onCancel={() => !logoResetMutation.isPending && setLogoResetOpen(false)} onOk={() => { setLogoResetOpen(false); logoResetMutation.mutate(); }}><p>{t("admin.remove_the_custom_logo_and_restore_the_default_immediately")}</p></LegacyConfirmModal>
      <LegacyConfirmModal title={t("admin.clear_the_wecom_webhook")} open={webhookClearOpen} okText={t("admin.confirm_clear")} danger confirmLoading={webhookClearMutation.isPending} onCancel={() => !webhookClearMutation.isPending && setWebhookClearOpen(false)} onOk={() => { setWebhookClearOpen(false); webhookClearMutation.mutate(); }}><p>{t("admin.delete_the_webhook_and_disable_wecom_notifications")}</p></LegacyConfirmModal>
      <Modal className="legacy-settings-modal" open={quotaResetOpen} title={t("admin.reset_all_users_weekly_usage")} okText={t("admin.confirm_usage_reset")} cancelText={t("common.cancel")} okButtonProps={{ danger: true }} confirmLoading={quotaResetMutation.isPending} onCancel={() => { if (quotaResetMutation.isPending) return; setQuotaResetOpen(false); quotaResetForm.reset(); quotaResetMutation.reset(); }} onOk={() => void quotaResetForm.handleSubmit(() => quotaResetMutation.mutate())()} destroyOnHidden>
        <p className="warning-banner">{t("admin.every_user_s_remaining_weekly_quota_will_change_immediately_raw")}</p>
        {quotaResetMutation.isError ? <Alert type="error" showIcon message={quotaResetMutation.error instanceof Error ? quotaResetMutation.error.message : t("admin.weekly_usage_was_not_reset")} /> : null}
        <Form layout="vertical" requiredMark={false}><Controller control={quotaResetForm.control} name="reason" render={({ field, fieldState }) => <Form.Item label={t("admin.reason")} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}><Input.TextArea {...field} aria-label={t("admin.reason")} autoSize={{ minRows: 3, maxRows: 5 }} /></Form.Item>} /><Controller control={quotaResetForm.control} name="confirmation" render={({ field, fieldState }) => <Form.Item label={t("admin.enter_reset_all_users_to_confirm")} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}><Input {...field} aria-label={t("admin.usage_reset_confirmation")} autoComplete="off" /></Form.Item>} /></Form>
      </Modal>
    </section>
  );
}

function ConfigurationSkeleton() {
  return <section className="page-content legacy-settings-page" aria-label={t("admin.loading_configuration_center")} aria-busy="true">
    <div className="settings-workspace settings-workspace-skeleton" aria-hidden="true">
      <aside className="settings-navigation"><div className="skeleton skeleton-line" /><div className="skeleton skeleton-table" /></aside>
      <div className="configuration-panel">
        <div className="settings-workspace-content"><div className="configuration-fields">
          <ConfigurationTableHeader />
          {Array.from({ length: 5 }, (_, index) => <div className="configuration-field" key={index}>
            <div className="configuration-field-copy"><div className="skeleton skeleton-line" /><div className="skeleton skeleton-line" /></div>
            <div className="configuration-field-meta"><div className="skeleton skeleton-line" /></div>
            <div className="configuration-field-value"><div className="skeleton skeleton-line" /></div>
          </div>)}
        </div></div>
        <div className="configuration-save-region"><div className="configuration-actions"><div className="skeleton skeleton-line" /><div className="skeleton skeleton-line" /></div></div>
      </div>
    </div>
  </section>;
}

function BrandingLogoEditor({ custom, sha256, pending, error, onFile, onReset }: { custom: boolean; sha256?: string; pending: boolean; error: string; onFile: (file: File) => void; onReset: () => void }) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const { theme } = useTheme();
  const source = custom ? `/branding/logo${sha256 ? `?v=${encodeURIComponent(sha256.slice(0, 16))}` : ""}` : `/portal/assets/codex-cpa-pool-logo${theme === "dark" ? "-dark" : ""}.svg`;
  return <article className="branding-logo-editor"><div className="branding-logo-preview"><img src={source} alt={t("admin.current_logo")} /></div><div className="branding-logo-copy"><strong>{t("admin.brand_logo")}</strong><small>{t("admin.uploads_and_restores_take_effect_immediately")}</small><span className={`status-chip ${custom ? "success" : "neutral"}`}>{custom ? t("admin.custom_logo") : t("admin.default_logo")}</span></div><div className="branding-logo-actions"><button className="button button-secondary" type="button" disabled={pending} onClick={() => fileInputRef.current?.click()}>{pending ? t("admin.uploading") : t("admin.choose_upload")}</button><input ref={fileInputRef} type="file" accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml" disabled={pending} hidden onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) onFile(file); }} /><button className="button danger-outline" type="button" disabled={!custom || pending} onClick={onReset}>{t("admin.restore_default")}</button><small className="form-error" role="alert">{error}</small></div></article>;
}

function NotificationIntegration({ status, enabled, unavailable, value, error, saving, clearing, sending, testing, editing, onEdit, onCancel, onChange, onSave, onClear, onSend, onTest }: {
  status: NotificationStatus;
  enabled: boolean;
  unavailable: boolean;
  value: string;
  error: string;
  saving: boolean;
  clearing: boolean;
  sending: boolean;
  testing: boolean;
  editing: boolean;
  onEdit: () => void;
  onCancel: () => void;
  onChange: (value: string) => void;
  onSave: () => void;
  onClear: () => void;
  onSend: () => void;
  onTest: () => void;
}) {
  const showEditor = editing || !status.webhook_configured;
  const busy = saving || clearing || sending || testing;
  const canSave = !busy && Boolean(value.trim());
  const canSend = status.webhook_configured && !showEditor && !busy;
  return <article className="notification-integration" aria-busy={busy}>
    <div className="notification-integration-head">
      <div className="notification-integration-copy"><strong>{t("admin.wecom_group_webhook")}</strong></div>
      <span className={`status-chip ${status.webhook_configured ? "success" : "neutral"}`}>{status.webhook_configured ? t("admin.webhook_configured") : t("admin.webhook_not_configured")}</span>
    </div>
    <div className="notification-webhook-editor">
      <span className="notification-webhook-label" id="notification-webhook-label">{t("admin.webhook_url")}</span>
      <div className="notification-webhook-control">
        {showEditor ? <input id="notification-webhook-url-react" aria-labelledby="notification-webhook-label" aria-invalid={Boolean(error)} aria-describedby={error ? "notification-webhook-error" : undefined}
          type="url" maxLength={2048} autoComplete="off" spellCheck={false} autoFocus={editing} disabled={busy}
          value={value} placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
          onChange={(event) => onChange(event.target.value)} onKeyDown={(event) => {
            if (event.nativeEvent.isComposing) return;
            if (event.key === "Enter") {
              event.preventDefault();
              event.stopPropagation();
              if (canSave) onSave();
            } else if (event.key === "Escape" && !busy) {
              event.preventDefault();
              event.stopPropagation();
              onCancel();
            }
          }} /> : <code className="notification-webhook-saved" aria-labelledby="notification-webhook-label">
            {status.webhook_display_url || "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=••••••"}
          </code>}
        <div className="notification-webhook-actions">
          {showEditor ? <>
            <button className="button button-primary" type="button" disabled={!canSave} onClick={onSave}>{saving ? t("admin.saving") : t("admin.save_webhook")}</button>
            {status.webhook_configured ? <button className="button button-quiet" type="button" disabled={busy} onClick={onCancel}>{t("common.cancel")}</button> : null}
          </> : <button className="button button-secondary" type="button" disabled={busy} onClick={onEdit}>{t("admin.change_url")}</button>}
          <button className="button danger-outline" type="button" disabled={!canSend} onClick={onClear}>{clearing ? t("admin.clearing") : t("admin.clear_webhook")}</button>
          <button className="button button-quiet" type="button" disabled={!canSend} onClick={onTest}>{testing ? t("admin.sending_test") : t("admin.send_test_message")}</button>
          <button className="button button-quiet" type="button" disabled={!canSend} onClick={onSend}>{sending ? t("admin.sending") : t("admin.send_account_report")}</button>
        </div>
      </div>
      {error ? <p className="form-error" id="notification-webhook-error" role="alert">{error}</p> : null}
    </div>
    <NotificationRuntimeStatus status={status} enabled={enabled} unavailable={unavailable} />
  </article>;
}

function ConfigurationTableHeader({ label = t("admin.setting"), valueLabel = t("admin.value") }: { label?: ReactNode; valueLabel?: string }) {
  return <div className="configuration-table-head">
    <div>{label}</div>
    <span className="configuration-status-heading">{t("admin.takes_effect")}</span>
    <span className="configuration-value-heading">{valueLabel}</span>
  </div>;
}

function ConfigurationApplyMode({ field }: { field: ConfigurationField }) {
  return <div className="configuration-field-meta"><span className="configuration-apply" title={t("admin.how_changes_take_effect")}>{applyModeLabel(field.apply_mode, field.key)}</span></div>;
}

function ConfigurationDirtyMark({ dirty }: { dirty: boolean }) {
  return dirty ? <small className="configuration-dirty-mark">{t("admin.unsaved")}</small> : null;
}

function ConfigurationEditor({ field, value, error, dirty, onChange }: { field: EditorField; value: DraftValue; error?: string; dirty: boolean; onChange: (value: DraftValue) => void }) {
  return <article className={`configuration-field${dirty ? " configuration-field-dirty" : ""}`} data-configuration-field={field.key}>
    <div className="configuration-field-copy">
      <div className="configuration-field-name"><label htmlFor={`configuration-${field.key}`}>{field.label}</label><ConfigurationDirtyMark dirty={dirty} /></div>
      {field.description ? <p>{field.description}</p> : null}
    </div>
    <ConfigurationApplyMode field={field} />
    <ConfigurationValueEditor field={field} value={value} error={error} onChange={onChange} />
  </article>;
}

function ConfigurationValueEditor({ field, value, error, onChange }: { field: ConfigurationField; value: DraftValue; error?: string; onChange: (value: DraftValue) => void }) {
  const valueBeforeFocus = useRef(value);
  const [editRevision, setEditRevision] = useState(0);
  const saving = useContext(ConfigurationSavingContext);
  return <div className="configuration-field-value"
    onFocusCapture={(event) => {
      if (!(event.relatedTarget instanceof Node) || !event.currentTarget.contains(event.relatedTarget)) valueBeforeFocus.current = value;
    }}
    onKeyDown={(event) => {
      const input = event.target;
      // Composite selects own Enter/Escape. Text and numeric edits never submit the whole form.
      if (saving || event.defaultPrevented || event.nativeEvent.isComposing || !(input instanceof HTMLInputElement)
        || input.getAttribute("role") === "combobox" || ["checkbox", "radio", "color", "file"].includes(input.type)) return;
      if (event.key === "Enter") {
        event.preventDefault();
        input.blur();
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onChange(valueBeforeFocus.current);
        // Also discard a duration's intermediate amount when its stored seconds have not changed.
        setEditRevision((revision) => revision + 1);
        input.blur();
      }
    }}>
    <div className="configuration-control-slot" style={{ width: configurationControlWidth(field) }}>
      <ConfigurationControl key={editRevision} field={field} value={value} onChange={onChange} />
    </div>
    {error ? <small className="configuration-control-error" id={`configuration-${field.key}-error`}>{error}</small> : null}
  </div>;
}

function ConfigurationControl({ field, value, onChange }: { field: ConfigurationField; value: DraftValue; onChange: (value: DraftValue) => void }) {
  const disabled = useContext(ConfigurationSavingContext);
  const id = `configuration-${field.key}`;
  const error = validateDraftValue(field, value);
  const errorId = error ? `${id}-error` : undefined;
  if (field.key === "plugins.codex_ticket.models") return <Select id={id} mode="tags" aria-label={field.label} aria-invalid={Boolean(error)} aria-describedby={errorId} value={ticketAccountIDs(String(value ?? ""))} disabled={disabled} tokenSeparators={[","]} open={false} style={{ width: "100%" }} onChange={(models: string[]) => onChange(models.join(","))} />;
  if (field.key === "plugins.codex_ticket.proxy_source") return <div id={id} role="radiogroup" aria-label={field.label} className="ticket-proxy-options">{field.choices?.map(choice => <label key={choice.value}><input type="radio" name={id} value={choice.value} checked={value === choice.value} disabled={disabled} onChange={() => onChange(choice.value)} /><span>{choice.label}</span></label>)}</div>;
  if (field.type === "timezone") return <TimezoneSelect id={id} value={String(value ?? "")} disabled={disabled} ariaInvalid={Boolean(error)} ariaDescribedBy={errorId} onChange={onChange} />;
  if (field.type === "duration" || field.key === "portal.session_ttl_seconds") return <ConfigurationDurationControl field={field} value={value} onChange={onChange} />;
  if (field.type === "boolean") return <div className="configuration-field-control boolean-control"><label><input id={id} type="checkbox" checked={Boolean(value)} disabled={disabled} onChange={(event) => onChange(event.target.checked)} /><span>{value ? t("common.enabled") : t("common.off")}</span></label></div>;
  if (field.type === "choice") return <div className="configuration-choice-control">
    <LegacyEnhancedSelect id={id} label={field.label} value={String(value ?? "")} disabled={disabled} ariaInvalid={Boolean(error)} ariaDescribedBy={errorId} options={(field.choices ?? []).map((choice) => ({ value: choice.value, label: `${choice.label} · ${choice.value}` }))} onChange={onChange} />
    {field.choices?.some((choice) => /^https?:\/\//.test(choice.value)) ? <div className="configuration-choice-address"><span>{sameConfigurationValue(normalizeDraftValue(field, value), field.value) ? t("admin.current_url") : t("admin.pending_url")}</span><code>{String(value ?? "")}</code></div> : null}
  </div>;
  if (field.type === "color") {
    const color = /^#[0-9a-f]{6}$/i.test(String(value ?? "")) ? String(value) : "#687287";
    return <div className="reasoning-color-inputs">
      <label className="reasoning-color-swatch"><input type="color" value={color} disabled={disabled} aria-label={t("admin.choose_color", [field.label])} onChange={(event) => onChange(event.target.value)} /></label>
      <input id={id} aria-label={field.label} aria-invalid={Boolean(error)} aria-describedby={errorId} className="reasoning-color-hex" type="text" value={String(value ?? "")} disabled={disabled} maxLength={7} pattern="#[0-9A-Fa-f]{6}" onChange={(event) => onChange(event.target.value)} />
    </div>;
  }
  if (field.type === "proxy_url_secret") return <LegacyPasswordInput id={id} value={value == null ? "" : String(value)} disabled={disabled} ariaLabel={field.label} ariaInvalid={Boolean(error)} ariaDescribedBy={errorId} placeholder={field.configured ? t("admin.configured_leave_blank_to_keep_the_current_value") : t("admin.e_g_socks5_user_pass_host_1080")} onValueChange={onChange} />;
  const numeric = ["integer", "number", "nullable_integer"].includes(field.type);
  const tokenInput = numeric && field.unit === "Token";
  const elevatedMultiplier = !error && (field.unit_code === "multiplier" || field.key.startsWith("user_quota.model_multiplier.") || field.key.startsWith("user_quota.reasoning_multiplier."))
    && (field.key.startsWith(modelMultiplierPrefix) || field.key.startsWith(reasoningMultiplierPrefix))
    && Number.isFinite(Number(value)) && Number(value) > 1;
  const inputProps: InputHTMLAttributes<HTMLInputElement> & { value: string } = {
    id, type: numeric ? "number" : "text", value: value == null ? "" : String(value), disabled,
    min: field.min, max: field.max, maxLength: field.max_length,
    step: field.type === "number" ? "any" : numeric ? 1 : undefined,
    placeholder: field.type === "nullable_integer" ? t("common.unlimited") : undefined,
    autoComplete: "off", title: value == null || value === "" ? t("admin.click_to_edit") : String(value),
    "aria-label": field.label, "aria-invalid": Boolean(error),
    "aria-describedby": [errorId, field.unit && !tokenInput ? `${id}-unit-label` : undefined, elevatedMultiplier ? `${id}-multiplier-mark` : undefined].filter(Boolean).join(" ") || undefined,
    onChange: (event) => onChange(event.target.value)
  };
  const input = (numeric && !tokenInput) || field.type === "time_list" ? <ConfigurationInlineInput {...inputProps} /> : <input {...inputProps} />;
  if (tokenInput) {
    const presentation = tokenInputPresentation(typeof value === "boolean" ? null : value, field.type === "nullable_integer" ? t("admin.leave_blank_for_unlimited") : t("common.enter_token_amount"));
    return <div className="configuration-token-control token-input-control">{input}<div className="token-input-preview" data-state={presentation.state}>
      {presentation.state === "ready" ? <><strong>{presentation.compact}</strong>{presentation.localized ? <span>{presentation.localized}</span> : null}<small>{t("admin.exact_value")} {presentation.exact}</small></> : <small>{presentation.state === "empty" ? presentation.emptyLabel : t("admin.enter_a_valid_positive_integer_token_amount")}</small>}
    </div></div>;
  }
  const control = field.unit ? <div className={`configuration-unit-control${elevatedMultiplier ? " configuration-multiplier-elevated" : ""}`}>
    {input}<span className="configuration-unit-label" id={`${id}-unit-label`}>{(field.unit)}</span>
    {elevatedMultiplier ? <span className="configuration-multiplier-mark" id={`${id}-multiplier-mark`} title={t("admin.multiplier_above_1")}>{t("admin.high_multiplier")}</span> : null}
  </div> : input;
  return field.type === "nullable_integer" ? <div className="configuration-nullable-control">{control}<small>{t("admin.leave_blank_for_unlimited")}</small></div> : control;
}

function ConfigurationInlineInput({ value, placeholder, ...props }: Omit<InputHTMLAttributes<HTMLInputElement>, "value"> & { value: string }) {
  return <span className="configuration-inline-editor">
    <span className="configuration-inline-size" aria-hidden="true">{value || placeholder || "0"}</span>
    <input {...props} value={value} placeholder={placeholder} autoComplete="off" title={props.title ?? t("admin.click_to_edit")} />
  </span>;
}

const durationUnits = [
  { value: "seconds", label: t("common.sec"), suffix: "s", seconds: 1 },
  { value: "minutes", label: t("admin.minutes"), suffix: "m", seconds: 60 },
  { value: "hours", label: t("admin.hours_2"), suffix: "h", seconds: 3600 },
  { value: "days", label: t("admin.days_2"), suffix: "d", seconds: 86400 }
] as const;
type DurationUnit = (typeof durationUnits)[number]["value"];
const durationBounds = { min: 30, max: 30 * 86400 };

function configurationDurationSeconds(field: ConfigurationField, value: unknown): number | null {
  if (field.type === "duration") {
    const match = /^(\d+)([smhd])$/i.exec(String(value ?? "").trim());
    if (!match) return null;
    const unit = durationUnits.find((item) => item.suffix === match[2].toLowerCase())!;
    const seconds = Number(match[1]) * unit.seconds;
    return Number.isSafeInteger(seconds) ? seconds : null;
  }
  if ((typeof value !== "number" && typeof value !== "string") || String(value).trim() === "") return null;
  const seconds = Number(value);
  return Number.isFinite(seconds) ? seconds : null;
}

function configurationDurationValue(field: ConfigurationField, seconds: number, current: DraftValue): DraftValue {
  if (field.type !== "duration") return seconds;
  // Preserve the existing spelling when the duration has not changed, including saved values such as 60m.
  for (const original of [current, field.value]) {
    if (typeof original === "string" && configurationDurationSeconds(field, original) === seconds) return original;
  }
  const unit = [...durationUnits].reverse().find((item) => seconds > 0 && seconds % item.seconds === 0)
    ?? durationUnits[0];
  return `${seconds / unit.seconds}${unit.suffix}`;
}

function durationAmount(value: DraftValue, secondsPerUnit: number): string {
  if (value == null || value === "" || !Number.isFinite(Number(value))) return "";
  // Eight decimal places keep the display readable without losing whole-second precision.
  return String(Number((Number(value) / secondsPerUnit).toFixed(8)));
}

function durationLimit(seconds: number): string {
  if (seconds % 86400 === 0) return t("admin.days", [seconds / 86400]);
  if (seconds % 3600 === 0) return t("admin.hours", [seconds / 3600]);
  if (seconds % 60 === 0) return t("admin.min", [seconds / 60]);
  return t("admin.sec", [seconds]);
}

function ConfigurationDurationControl({ field, value, onChange }: { field: ConfigurationField; value: DraftValue; onChange: (value: DraftValue) => void }) {
  const disabled = useContext(ConfigurationSavingContext);
  const id = `configuration-${field.key}`;
  const [unit, setUnit] = useState<DurationUnit>(() => field.type === "duration"
    ? durationUnits.find((item) => item.suffix === String(value ?? field.value ?? "").trim().slice(-1).toLowerCase())?.value ?? "hours"
    : "hours");
  const [inputDraft, setInputDraft] = useState<{ value: DraftValue; amount: string } | null>(null);
  const selectedUnit = durationUnits.find((item) => item.value === unit)!;
  const seconds = configurationDurationSeconds(field, value);
  const amount = inputDraft && inputDraft.value === value ? inputDraft.amount : durationAmount(seconds, selectedUnit.seconds);
  const minimum = field.min ?? (field.type === "duration" ? durationBounds.min : undefined);
  const maximum = field.max ?? (field.type === "duration" ? durationBounds.max : undefined);
  const error = validateDraftValue(field, value);

  return <div className="configuration-duration-control">
      <ConfigurationInlineInput
        id={id}
        type="number"
        value={amount}
        min={minimum === undefined ? undefined : minimum / selectedUnit.seconds}
        max={maximum === undefined ? undefined : maximum / selectedUnit.seconds}
        step="any"
        aria-label={field.label}
        aria-invalid={Boolean(error)}
        aria-describedby={error ? `${id}-error` : undefined}
        disabled={disabled}
        autoComplete="off"
        onChange={(event) => {
          const nextAmount = event.target.value;
          const seconds = Number(nextAmount) * selectedUnit.seconds;
          const nextValue = nextAmount === "" || !Number.isFinite(seconds) ? "" : configurationDurationValue(field, Math.round(seconds), value);
          setInputDraft({ value: nextValue, amount: nextAmount });
          onChange(nextValue);
        }}
      />
      <LegacyEnhancedSelect<DurationUnit>
        id={`${id}-unit`}
        label={t("admin.unit", [field.label])}
        value={unit}
        disabled={disabled}
        options={durationUnits.map(({ value: unitValue, label }) => ({ value: unitValue, label }))}
        onChange={(nextUnit) => {
          // Unit selection only changes the display; keep the API's original value and format.
          setUnit(nextUnit);
          setInputDraft(null);
        }}
      />
  </div>;
}

function LegacyConfirmModal({ title, open, children, okText, danger = false, confirmLoading = false, onCancel, onOk }: { title: string; open: boolean; children: ReactNode; okText: string; danger?: boolean; confirmLoading?: boolean; onCancel: () => void; onOk: () => void }) {
  return <Modal className="legacy-confirm-modal" title={<span className="sr-only">{title}</span>} open={open} width={430} centered closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>} transitionName="" maskTransitionName="" onCancel={onCancel} destroyOnHidden footer={[<Button key="cancel" disabled={confirmLoading} onClick={onCancel}>{t("common.cancel")}</Button>, <Button key="confirm" type={danger ? "default" : "primary"} danger={danger} loading={confirmLoading} onClick={onOk}>{okText}</Button>]}><div className="legacy-confirm-body"><div className="legacy-confirm-icon" aria-hidden="true">!</div><h3>{title}</h3><div className="legacy-confirm-message">{children}</div></div></Modal>;
}

function ModelMultiplierEditor({ fields, draft, onChange }: { fields: ConfigurationField[]; draft: Draft; onChange: (field: ConfigurationField, value: DraftValue) => void }) {
  const modelFields = fields.filter((field) => field.key.startsWith(modelMultiplierPrefix));
  return <section className="model-multiplier-editor" aria-label={t("admin.model_multipliers")}>
    <div className="configuration-matrix-viewport" role="region" aria-label={t("admin.model_multiplier_settings")}>
      <div className="configuration-matrix model-multiplier-table">
        <ConfigurationTableHeader label={t("common.model")} valueLabel={t("admin.user_quota_multiplier_2")} />
        {modelFields.map((field) => {
          const model = field.key.slice(modelMultiplierPrefix.length);
          const name = model === "unknown" ? t("admin.other_unmatched") : model;
          const value = draft[field.key] === undefined ? draftValueFromConfiguration(field.value, field.type) : draft[field.key];
          const dirty = !sameConfigurationValue(normalizeDraftValue(field, value), field.value);
          return <div className={`configuration-field model-multiplier-row${dirty ? " configuration-field-dirty" : ""}`} key={field.key} data-configuration-field={field.key}>
            <div className="configuration-field-copy">
              <div className="model-multiplier-name"><label htmlFor={`configuration-${field.key}`} title={name}>{name}</label>{model === "unknown" ? <code>fallback</code> : null}</div>
              <ConfigurationDirtyMark dirty={dirty} />
            </div>
            <ConfigurationApplyMode field={field} />
            <ConfigurationValueEditor field={{ ...field, label: t("admin.user_quota_multiplier", [model === "unknown" ? t("admin.other_unmatched_models") : model]), unit: t("admin.multiplier_unit"), unit_code: "multiplier" }} value={value} error={validateDraftValue(field, value)} onChange={(next) => onChange(field, next)} />
          </div>;
        })}
      </div>
    </div>
    <footer><button className="button button-quiet" type="button" onClick={() => modelFields.forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>{t("admin.restore_default_model_multipliers")}</button></footer>
  </section>;
}

function ReasoningStrategyEditor({ fields, draft, onChange }: { fields: ConfigurationField[]; draft: Draft; onChange: (field: ConfigurationField, value: DraftValue) => void }) {
  const fieldFor = (prefix: string, effort: string) => fields.find((field) => field.key === `${prefix}${effort}`);
  const hasColors = fields.some((field) => field.key.startsWith(reasoningColorPrefix));
  const hasMultipliers = fields.some((field) => field.key.startsWith(reasoningMultiplierPrefix));
  const canvasRef = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const draw = () => {
      const width = Math.max(320, Math.round(canvas.getBoundingClientRect().width || 800));
      const height = 58;
      const ratio = Math.max(1, window.devicePixelRatio || 1);
      canvas.width = Math.round(width * ratio);
      canvas.height = Math.round(height * ratio);
      const context = canvas.getContext("2d");
      if (!context) return;
      context.scale(ratio, ratio);
      const segmentWidth = width / reasoningEfforts.length;
      reasoningEfforts.forEach((effort, index) => {
        const field = fieldFor(reasoningColorPrefix, effort);
        const presentation = reasoningColorPresentation(field ? String(draft[field.key] ?? field.value) : "", String(field?.default ?? "#687287"));
        context.fillStyle = presentation.color;
        context.fillRect(index * segmentWidth, 0, Math.ceil(segmentWidth), height);
        context.fillStyle = presentation.text;
        context.font = "600 9px ui-monospace, SFMono-Regular, Menlo, monospace";
        context.textAlign = "center";
        context.textBaseline = "middle";
        context.fillText(effort, (index + 0.5) * segmentWidth, height / 2, Math.max(24, segmentWidth - 8));
      });
    };
    draw();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(draw);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, [draft, fields]);
  return <section className="reasoning-strategy-editor" aria-label={hasColors ? t("admin.reasoning_effort_colors") : t("admin.reasoning_multipliers")}>
    {hasColors ? <div className="reasoning-color-preview"><strong>{t("admin.color_preview")}</strong><canvas ref={canvasRef} height="58" role="img" aria-label={t("admin.reasoning_effort_color_preview")} /></div> : null}
    <div className="configuration-matrix-viewport" role="region" aria-label={hasColors ? t("admin.reasoning_effort_color_settings") : t("admin.reasoning_effort_multiplier_settings")}>
      <div className="configuration-matrix reasoning-strategy-table">
        <ConfigurationTableHeader label={<div className="reasoning-strategy-names"><span>{t("admin.reasoning_effort_localized")}</span><span>{t("admin.reasoning_effort_id")}</span></div>} valueLabel={hasColors ? t("admin.account_detail_color") : t("admin.user_quota_multiplier_2")} />
        {fields.map((field) => {
          const prefix = hasColors ? reasoningColorPrefix : reasoningMultiplierPrefix;
          if (!field.key.startsWith(prefix)) return null;
          const effort = field.key.slice(prefix.length);
          const value = draft[field.key] === undefined ? draftValueFromConfiguration(field.value, field.type) : draft[field.key];
          const dirty = !sameConfigurationValue(normalizeDraftValue(field, value), field.value);
          const editorField = hasColors ? field : { ...field, label: t("admin.user_quota_multiplier", [reasoningEffortLabel(effort)]), unit: t("admin.multiplier_unit"), unit_code: "multiplier" };
          return <div className={`configuration-field reasoning-strategy-row${dirty ? " configuration-field-dirty" : ""}`} key={field.key} data-configuration-field={field.key}>
            <div className="configuration-field-copy">
              <div className="reasoning-strategy-names">
                <label className="reasoning-strategy-label" htmlFor={`configuration-${field.key}`}>{reasoningEffortLabel(effort)}</label>
                <code className="reasoning-strategy-code">{effort}</code>
              </div>
              <ConfigurationDirtyMark dirty={dirty} />
            </div>
            <ConfigurationApplyMode field={field} />
            <ConfigurationValueEditor field={editorField} value={value} error={validateDraftValue(field, value)} onChange={(next) => onChange(field, next)} />
          </div>;
        })}
      </div>
    </div>
    <div className="reasoning-strategy-defaults">
      {hasMultipliers ? <button className="button button-quiet" type="button" onClick={() => fields.filter((field) => field.key.startsWith(reasoningMultiplierPrefix)).forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>{t("admin.restore_default_reasoning_multipliers")}</button> : null}
      {hasColors ? <button className="button button-quiet" type="button" onClick={() => fields.filter((field) => field.key.startsWith(reasoningColorPrefix)).forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>{t("admin.restore_default_colors")}</button> : null}
    </div>
  </section>;
}

function QuotaSystemDanger({ summary, pending, failed, onReset }: { summary?: { total_users: number; users_with_usage: number; total_used_tokens: number; total_raw_used_tokens: number; week_end_at: number | null }; pending: boolean; failed: boolean; onReset: () => void }) {
  const available = Boolean(summary) && !failed;
  const canReset = available && Number(summary?.users_with_usage ?? 0) > 0;
  return (
    <section className="quota-system-danger" aria-label={t("admin.global_quota_danger_zone")} aria-busy={pending} data-configuration-field="quota-reset">
      <div className="quota-system-danger-copy">
        <strong>{t("admin.reset_everyone_s_weekly_usage")}</strong>
        <p>{t("admin.for_incident_recovery_only_raw_events_quota_policies_and_bonus")}</p>
      </div>
      {available && summary ? (
        <dl className="quota-system-danger-metrics" aria-label={t("admin.weekly_usage_reset_impact")}>
          <div><dt>{t("admin.total_users")}</dt><dd><strong>{summary.total_users.toLocaleString(getIntlLocale())}</strong><span className="quota-system-danger-unit">{t("admin.users_2")}</span></dd></div>
          <div><dt>{t("admin.users_with_usage")}</dt><dd><strong>{summary.users_with_usage.toLocaleString(getIntlLocale())}</strong><span className="quota-system-danger-unit">{t("admin.users_2")}</span></dd></div>
          <QuotaResetTokenMetric label={t("admin.weighted_usage_this_week")} value={summary.total_used_tokens} primary />
          <QuotaResetTokenMetric label={t("admin.raw_tokens_this_week")} value={summary.total_raw_used_tokens} />
        </dl>
      ) : (
        <p className="quota-system-danger-status" role="status">{pending ? t("admin.checking_impact_2") : t("admin.unable_to_determine_the_impact_refresh_configuration_and_try_again")}</p>
      )}
      <div className="quota-system-danger-footer">
        <div className="quota-system-danger-reset-time">
          <span>{t("admin.next_weekly_rollover")}</span>
          {available && summary?.week_end_at
            ? <time dateTime={new Date(summary.week_end_at * 1_000).toISOString()}>{formatSiteTimestamp(summary.week_end_at)}</time>
            : <span>—</span>}
        </div>
        <button className="button danger-outline" type="button" disabled={!canReset || pending} onClick={onReset}>
          {pending ? t("admin.checking_impact") : !available ? t("admin.impact_unavailable") : canReset ? t("admin.reset_all_users_weekly_usage") : t("admin.no_usage_to_reset")}
        </button>
      </div>
    </section>
  );
}

function QuotaResetTokenMetric({ label, value, primary = false }: { label: string; value: number; primary?: boolean }) {
  const details = tokenReadableParts(value, { allowZero: true });
  return <div data-primary={primary}>
    <dt>{label}</dt>
    <dd title={details.state === "ready" ? details.exact : undefined}>
      <strong>{formatTokenAmount(value)}</strong><span className="quota-system-danger-unit">Token</span>
      {details.state === "ready" && details.localized ? <small>{details.localized}</small> : null}
    </dd>
  </div>;
}

function AccessPanel({ managementKeyConfigured, initialPasswordConfigured, onInitialPassword, onManagementKey }: { managementKeyConfigured: boolean; initialPasswordConfigured: boolean; onInitialPassword: () => void; onManagementKey: () => void }) {
  return <div className="configuration-access">
    <div><span><strong>{t("common.management_key")}</strong><small>{managementKeyConfigured ? t("admin.configured_changing_it_ends_management_sessions") : t("admin.not_configured")}</small></span><button className="button button-secondary" type="button" disabled={!managementKeyConfigured} onClick={onManagementKey}>{t("admin.change_management_key")}</button></div>
    <div><span><strong>{t("admin.initial_user_password")}</strong><small>{initialPasswordConfigured ? t("admin.configured_used_for_new_users") : t("admin.not_configured")}</small></span><button className="button button-secondary" type="button" onClick={onInitialPassword}>{t("admin.set_initial_user_password")}</button></div>
    <span className="sr-only">{managementKeyConfigured ? t("admin.management_key_configured") : t("admin.management_key_not_configured")}</span>
  </div>;
}
function BackupsPanel({ count, latest }: { count: number; latest: string }) { return <section className="settings-secondary-panel"><div className="settings-panel-meta"><strong>{count} {t("admin.archives")}</strong></div><div className="settings-panel-body"><div className="settings-panel-callout"><strong>{t("admin.latest_archive")}</strong><span className="settings-path">{latest || t("admin.no_archives")}</span></div></div></section>; }
function StoragePanel({ rows, onRefresh }: { rows: Array<{ label: string; path: string; exists: boolean; mode: string }>; onRefresh: () => Promise<void> }) {
  const createdCount = rows.filter((item) => item.exists).length;
  return (
    <section className="settings-secondary-panel settings-data-panel" aria-label={t("admin.persistent_data")}>
      <SettingsDataPanelSummary
        title={t("admin.persistent_data")}
        summary={`${createdCount}/${rows.length}`}
        summaryLabel={t("admin.created")}
      />
      {rows.length ? (
        <div className="settings-table-panel">
          <div className="settings-table-viewport" role="region" aria-label={t("admin.storage_status_table")}>
            <table className="storage-table">
              <thead><tr><th className="table-index-column">{t("common.no")}</th><th>{t("admin.data")}</th><th>{t("admin.local_path")}</th><th>{t("admin.status_2")}</th><th>{t("admin.permissions")}</th></tr></thead>
              <tbody>{rows.map((item, index) => (
                <tr key={item.path}>
                  <td className="table-index-cell">{index + 1}</td>
                  <td><span className="table-primary">{item.label}</span></td>
                  <td><span className="settings-path">{item.path}</span></td>
                  <td><span className={`status-chip ${item.exists ? "success" : "neutral"}`}>{item.exists ? t("admin.created") : t("admin.not_created")}</span></td>
                  <td className="settings-mode-cell"><span className="settings-path">{item.mode}</span></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </div>
      ) : (
        <SettingsPanelEmptyState icon="▦" title={t("admin.no_local_data_entries_found")} description={t("admin.refresh_this_page_if_it_remains_empty_check_version_support")} actionLabel={t("admin.refresh_local_data")} onAction={onRefresh} />
      )}
    </section>
  );
}

function AuditPanel({ rows, onRefresh }: { rows: Array<{ timestamp: number; action: string; target: string; outcome: string }>; onRefresh: () => Promise<void> }) {
  return (
    <section className="settings-secondary-panel settings-data-panel" aria-label={t("admin.recent_admin_activity")}>
      <SettingsDataPanelSummary
        title={t("admin.recent_admin_activity")}
        summary={String(rows.length)}
        summaryLabel={t("admin.records")}
      />
      {rows.length ? (
        <div className="settings-table-panel">
          <div className="settings-table-viewport" role="region" aria-label={t("admin.admin_audit_table")}>
            <table className="audit-table">
              <thead><tr><th className="table-index-column">{t("common.no")}</th><th>{t("common.time")}</th><th>{t("admin.action")}</th><th>{t("admin.target")}</th><th>{t("admin.result")}</th></tr></thead>
              <tbody>{rows.map((item, index) => (
                <tr key={`${item.timestamp}-${index}`}>
                  <td className="table-index-cell">{index + 1}</td>
                  <td className="settings-time-cell">{formatSiteTimestamp(item.timestamp)}</td>
                  <td><span className="settings-path">{item.action}</span></td>
                  <td>{item.target}</td>
                  <td><span className={`status-chip ${item.outcome === "accepted" ? "success" : "neutral"}`}>{item.outcome || "unknown"}</span></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </div>
      ) : (
        <SettingsPanelEmptyState
          icon="◎"
          title={t("admin.no_admin_activity")}
          description={t("admin.configuration_and_maintenance_operations_will_appear_here")}
          actionLabel={t("admin.refresh_audit_log")}
          onAction={onRefresh}
        />
      )}
    </section>
  );
}

function SettingsDataPanelSummary({ title, summary, summaryLabel }: { title: string; summary: string; summaryLabel: string }) {
  return <div className="settings-panel-meta" aria-label={`${title}：${summary} ${summaryLabel}`}><strong>{summary} {summaryLabel}</strong></div>;
}

function SettingsPanelEmptyState({ icon, title, description, actionLabel, onAction }: { icon: string; title: string; description: string; actionLabel?: string; onAction?: () => Promise<void> }) {
  return <div className="settings-panel-empty"><div className="settings-panel-empty-icon" aria-hidden="true">{icon}</div><h3>{title}</h3><p>{description}</p>{actionLabel && onAction ? <button className="button button-secondary" type="button" onClick={() => { void onAction().catch(() => undefined); }}>{actionLabel}</button> : null}</div>;
}

function flattenConfiguration(catalog?: ConfigurationCatalog): EditorField[] {
  return catalog?.groups.flatMap((group) => group.fields.map((field) => {
    const section = configurationSectionFor(field, group.id ?? group.name);
    return { ...field, group: section.category, section: section.id };
  })) ?? [];
}
function configurationDraft(catalog: ConfigurationCatalog): Draft { return Object.fromEntries(flattenConfiguration(catalog).map((field) => [field.key, draftValueFromConfiguration(field.value, field.type)])); }
function draftValueFromConfiguration(value: ConfigurationValue, type: ConfigurationField["type"]): DraftValue { if (type === "domain_list") return Array.isArray(value) ? value.join(", ") : ""; if (type === "proxy_url_secret") return ""; if (Array.isArray(value)) return value.join(", "); return value; }
function normalizeDraftValue(field: ConfigurationField, value: DraftValue): ConfigurationValue { if (field.type === "proxy_url_secret" && String(value ?? "").trim() === "") return field.value; if (field.type === "domain_list") return [...new Set(String(value ?? "").split(/[,，\s]+/).map((item) => item.trim().toLocaleLowerCase(getIntlLocale())).filter(Boolean))]; if (field.type === "boolean") return Boolean(value); if (["integer", "number", "nullable_integer"].includes(field.type)) { if (field.type === "nullable_integer" && String(value ?? "").trim() === "") return null; if (String(value ?? "").trim() === "") return ""; const number = Number(value); return Number.isFinite(number) ? number : String(value ?? "").trim(); } if (typeof value === "string") return value.trim(); return value; }
function sameConfigurationValue(left: ConfigurationValue, right: ConfigurationValue): boolean { return JSON.stringify(left) === JSON.stringify(right); }
function validateDraftValue(field: ConfigurationField, raw: DraftValue): string { if (field.type === "nullable_integer" && String(raw ?? "").trim() === "") return ""; if (["integer", "number", "nullable_integer"].includes(field.type)) { if (String(raw ?? "").trim() === "") return t("admin.enter_a_valid_number"); const value = Number(raw); if (!Number.isFinite(value)) return t("admin.enter_a_valid_number"); if ((field.type === "integer" || field.type === "nullable_integer") && !Number.isInteger(value)) return t("admin.enter_an_integer"); if (field.min !== undefined && value < field.min) return t("admin.must_be_at_least", [field.key === "portal.session_ttl_seconds" ? durationLimit(field.min) : field.min]); if (field.max !== undefined && value > field.max) return t("admin.must_be_no_more_than", [field.key === "portal.session_ttl_seconds" ? durationLimit(field.max) : field.max]); return ""; } if (field.type === "boolean" || field.type === "choice" || field.type === "domain_list") return ""; const value = String(raw ?? "").trim(); if (field.type === "proxy_url_secret" && value === "") return ""; if (["optional_text", "optional_image", "base_url"].includes(field.type) && value === "") return ""; if (!value) return t("admin.required"); if (field.min_length !== undefined && [...value].length < field.min_length) return t("admin.enter_at_least_characters", [field.min_length]); if (field.max_length !== undefined && [...value].length > field.max_length) return t("admin.enter_no_more_than_characters", [field.max_length]); if (field.type === "key_prefix" && !/^[a-z][a-z0-9_]{1,30}_$/.test(value)) return t("admin.enter_a_3_32_character_lowercase_prefix_ending_with_an"); if (field.type === "env_name" && !/^[A-Z][A-Z0-9_]{1,63}$/.test(value)) return t("admin.enter_a_valid_uppercase_environment_variable_name"); if (field.type === "color" && !/^#[0-9a-fA-F]{6}$/.test(value)) return t("admin.enter_a_rrggbb_color"); if (field.type === "duration") { const seconds = configurationDurationSeconds(field, value); if (seconds === null) return t("admin.enter_a_valid_duration"); if (seconds < durationBounds.min) return t("admin.must_be_at_least", [durationLimit(durationBounds.min)]); if (seconds > durationBounds.max) return t("admin.must_be_no_more_than", [durationLimit(durationBounds.max)]); return ""; } if (field.type === "time_list" && !/^([01]?\d|2[0-3]):[0-5]\d(?:\s*[,，]\s*([01]?\d|2[0-3]):[0-5]\d)*$/.test(value)) return t("admin.enter_hh_mm_separate_multiple_times_with_commas"); if ((field.type === "base_url" || field.type === "proxy_url_secret") && !validConfigurationURL(value, field.type === "proxy_url_secret", field.key === "plugins.codex_ticket.proxy_url")) return t("admin.enter_a_valid_http_s_or_socks5_root_url"); if (field.type === "ip" && !validIPv4(value)) return t("admin.enter_a_valid_ipv4_address"); if ((field.type === "image" || field.type === "optional_image") && !/^[A-Za-z0-9._:/@-]+$/.test(value)) return t("admin.invalid_image_name"); if (field.digest_required && !/^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$/.test(value)) return t("admin.pin_the_image_using_name_tag_sha256_digest"); return ""; }
function validConfigurationURL(value: string, proxy: boolean, ticket = false): boolean { try { const parsed = new URL(value); if (!["http:", "https:", ...(proxy ? ["socks5:", ...(ticket ? ["socks5h:"] : [])] : [])].includes(parsed.protocol)) return false; return Boolean(parsed.hostname) && (parsed.pathname === "/" || parsed.pathname === "") && !parsed.search && !parsed.hash && (proxy || (!parsed.username && !parsed.password)); } catch { return false; } }
function validIPv4(value: string): boolean { const parts = value.split("."); return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) >= 0 && Number(part) <= 255); }
function applyModeLabel(mode: ConfigurationField["apply_mode"], key = ""): string { if (key === "runtime.cliproxy_image") return t("admin.image_management"); return ({ live: t("admin.immediately"), accounts: t("admin.recreates_cpa_accounts"), collector: t("admin.restarts_collector"), future: t("admin.new_accounts_only"), deployment: t("admin.after_account_recreation"), quota: t("admin.on_next_collection") })[mode]; }
function configurationEffects(fields: EditorField[]): string[] { const modes = new Set(fields.map((field) => field.apply_mode)); return [modes.has("accounts") ? t("admin.cpa_accounts_will_be_recreated_one_at_a_time") : "", modes.has("collector") ? t("admin.the_usage_collector_will_restart") : "", modes.has("quota") ? t("admin.user_quota_changes_take_effect_after_the_next_collection") : "", modes.has("deployment") ? t("admin.cpa_parameters_take_effect_after_account_recreation") : ""].filter(Boolean); }
function validateLogoFile(file: File): string { if (!supportedLogoTypes.has(file.type)) return t("admin.only_png_jpeg_gif_webp_or_svg_files_are_supported"); if (file.size < 1) return t("admin.the_logo_file_cannot_be_empty"); if (file.size > maxLogoBytes) return t("admin.the_logo_file_must_not_exceed_2_mib"); if ([...file.name].length > 128) return t("admin.the_logo_filename_must_not_exceed_128_characters"); return ""; }
function reasoningEffortLabel(effort: string): string { return ({ none: t("common.none"), minimal: t("common.minimal"), low: t("common.low"), medium: t("common.medium"), high: t("common.high"), xhigh: t("common.extra_high"), max: t("common.max"), ultra: t("common.ultra"), auto: t("common.auto"), unknown: t("common.unknown") } as Record<string, string>)[effort] ?? effort; }
function reasoningColorPresentation(value: string, fallback = "#687287") { const color = /^#[0-9a-f]{6}$/i.test(value) ? value.toLowerCase() : fallback; const channels = [1, 3, 5].map((index) => Number.parseInt(color.slice(index, index + 2), 16) / 255).map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4); const luminance = 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]; return { color, text: luminance > 0.179 ? "#171d2b" : "#ffffff" }; }
