import { LanguageSelect } from "./LanguageSelect";
import { t } from "../i18n";
import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { useEffect, useMemo, useState } from "react";
import { z } from "zod";

import { ApiError } from "../api/client";
import { loginPortal, portalSessionQueryKey } from "../api/portal";
import { applicationHref } from "../application-links";
import {
  normalizedEmailDomains,
  publicSiteQueryKey,
  readPublicSiteConfiguration
} from "../api/public-site";
import { ThemeToggle, useTheme } from "./ThemeProvider";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { LegacyPasswordInput } from "./components/LegacyPasswordInput";

const loginSchema = z.object({
  email: z.string().trim().min(1, t("common.enter_your_email_username")),
  password: z.string().min(1, t("common.enter_your_password")).max(128, t("common.invalid_password_format"))
});

type LoginValues = z.infer<typeof loginSchema>;

export function UsageLoginPage({ overlay = false }: { overlay?: boolean }) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const [retrySeconds, setRetrySeconds] = useState(0);
  const [emailDomain, setEmailDomain] = useState("");
  const siteConfiguration = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: false
  });
  const emailDomains = useMemo(() => normalizedEmailDomains(siteConfiguration.data?.allowed_email_domains), [siteConfiguration.data?.allowed_email_domains]);
  const selectedDomain = emailDomains.includes(emailDomain) ? emailDomain : emailDomains[0] ?? "";
  const domainsLoading = siteConfiguration.isPending || siteConfiguration.isFetching;
  const domainsReady = !domainsLoading && !siteConfiguration.isError && Boolean(selectedDomain);
  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { email: "", password: "" }
  });
  const login = useMutation({
    gcTime: 0,
    mutationFn: (credentials: LoginValues) => loginPortal(credentials.email, credentials.password),
    onSuccess: (session) => {
      form.reset();
      setEmailDomain("");
      login.reset();
      queryClient.setQueryData(portalSessionQueryKey, session);
    }
  });
  const emailInput = form.register("email");
  const parseFullEmail = (value: string) => {
    const parts = value.trim().split("@");
    if (parts.length !== 2 || !emailDomains.includes(parts[1].toLowerCase())) return null;
    return { localPart: parts[0], domain: parts[1].toLowerCase() };
  };
  const acceptFullEmail = (value: string) => {
    const parsed = parseFullEmail(value);
    if (!parsed) return false;
    form.setValue("email", parsed.localPart, { shouldDirty: true });
    form.clearErrors("email");
    setEmailDomain(parsed.domain);
    return true;
  };
  const submit = (values: LoginValues) => {
    if (retrySeconds > 0 || login.isPending || !domainsReady) return;
    const parsed = parseFullEmail(values.email);
    if (values.email.includes("@") && !parsed) {
      form.setError("email", { type: "validate", message: t("common.the_email_domain_does_not_match_enter_only_the_username") }, { shouldFocus: true });
      return;
    }
    const address = `${parsed?.localPart ?? values.email}@${parsed?.domain ?? selectedDomain}`;
    if (!z.string().email().safeParse(address).success) {
      form.setError("email", { type: "validate", message: t("common.enter_a_valid_organization_email") }, { shouldFocus: true });
      return;
    }
    login.mutate({ email: address, password: values.password });
  };
  useEffect(() => {
    if (!(login.error instanceof ApiError) || login.error.status !== 429) return;
    setRetrySeconds(Math.max(1, login.error.retryAfterSeconds || 1));
  }, [login.error]);
  useEffect(() => {
    if (retrySeconds <= 0) return;
    const timer = window.setTimeout(() => setRetrySeconds((current) => Math.max(0, current - 1)), 1_000);
    return () => window.clearTimeout(timer);
  }, [retrySeconds]);

  const formCard = (
      <form
        className={`login-card auth-card usage-login-card ${overlay ? "usage-login-card-overlay" : ""}`}
        noValidate
        onSubmit={form.handleSubmit(submit)}
      >
        {!overlay ? <div className="login-card-toolbar">
          <a href={applicationHref("portal")} aria-label={t("common.back_to_codex_cpa_home")}>
            <img
              className="auth-brand-logo"
              src={`/portal/assets/codex-cpa-pool-logo${theme === "dark" ? "-dark" : ""}.svg`}
              alt="Codex CPA Pool"
            />
          </a>
          <ThemeToggle />
        </div> : null}
        <div className="auth-language"><LanguageSelect /></div>
        <div className="login-card-heading">
          <span className="eyebrow">USER</span>
          <h1>{t("common.sign_in_to_usage_center")}</h1>
          <p>{t("common.sign_in_with_your_organization_email_and_personal_password_to")}</p>
        </div>
          <div className="field">
            <span id="usage-login-email-label">{t("common.user_email")}</span>
            <div className="usage-login-email-fields" role="group" aria-labelledby="usage-login-email-label">
              <input
                type="text"
                inputMode="email"
                aria-label={t("common.email_username")}
                autoComplete="username"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                placeholder={t("common.enter_username")}
                disabled={login.isPending}
                aria-invalid={Boolean(form.formState.errors.email)}
                aria-describedby={form.formState.errors.email ? "usage-login-email-error" : undefined}
                {...emailInput}
                onChange={(event) => {
                  void emailInput.onChange(event);
                  form.clearErrors("email");
                }}
                onBlur={(event) => {
                  void emailInput.onBlur(event);
                  acceptFullEmail(event.target.value);
                }}
                onPaste={(event) => {
                  if (acceptFullEmail(event.clipboardData.getData("text"))) event.preventDefault();
                }}
              />
              <LegacyEnhancedSelect
                id="usage-login-email-domain"
                label={t("common.email_domain")}
                value={selectedDomain}
                options={emailDomains.length
                  ? emailDomains.map((domain) => ({ value: domain, label: `@${domain}` }))
                  : [{ value: "", label: domainsLoading ? t("common.loading_domains") : t("common.no_available_domains") }]}
                disabled={login.isPending || !domainsReady}
                onChange={(domain) => {
                  const parts = form.getValues("email").trim().split("@");
                  if (parts.length === 2) form.setValue("email", parts[0], { shouldDirty: true });
                  setEmailDomain(domain);
                  form.clearErrors("email");
                }}
              />
            </div>
            {form.formState.errors.email ? <small className="field-error" id="usage-login-email-error" role="alert">{form.formState.errors.email.message}</small> : null}
            {domainsLoading ? <small className="field-hint" role="status">{t("common.loading_organization_email_domains")}</small>
              : siteConfiguration.isError ? <small className="field-error" role="alert">{t("common.unable_to_load_email_domains")}<button className="usage-email-retry" type="button" onClick={() => { void siteConfiguration.refetch(); }}>{t("common.retry")}</button></small>
                : !emailDomains.length ? <small className="field-error" role="alert">{t("common.no_organization_email_domain_is_configured_contact_your_administrator")}</small>
                  : null}
          </div>
          <div className="field">
            <span>{t("common.password")}</span>
            <Controller
              control={form.control}
              name="password"
              render={({ field }) => (
                <LegacyPasswordInput
                  id="usage-login-password"
                  name={field.name}
                  value={field.value}
                  inputRef={field.ref}
                  onBlur={field.onBlur}
                  onValueChange={field.onChange}
                  ariaLabel={t("common.password")}
                  ariaInvalid={Boolean(form.formState.errors.password)}
                  disabled={login.isPending}
                  autoComplete="current-password"
                />
              )}
            />
            {form.formState.errors.password ? <small className="field-error">{form.formState.errors.password.message}</small> : null}
          </div>
          {login.isError ? (
            <div className="inline-alert" role="alert">
              {retrySeconds > 0
                ? t("common.too_many_sign_in_attempts_try_again_in_seconds", [retrySeconds])
                : login.error.message}
            </div>
          ) : null}
          <button className="button button-primary button-block" type="submit" disabled={login.isPending || retrySeconds > 0 || !domainsReady}>
            {retrySeconds > 0 ? t("common.retry_in_seconds", [retrySeconds]) : login.isPending ? t("common.verifying") : t("common.sign_in")}
          </button>
        <a className="quiet-link" href={applicationHref("portal")}>{t("common.back_to_portal_2")}</a>
      </form>
  );

  if (overlay) {
    return (
      <div className="usage-login-backdrop">
        <section className="usage-login-dialog" role="dialog" aria-modal="true" aria-label={t("common.sign_in_to_usage_center")}>
          {formCard}
        </section>
      </div>
    );
  }
  return (
    <main className="login-layout auth-screen usage-login-layout">
      {formCard}
    </main>
  );
}
