import { LanguageSelect } from "./LanguageSelect";
import { t } from "../i18n";
import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { login, sessionQueryKey } from "../api/session";
import { applicationHref } from "../application-links";
import { useTheme } from "./ThemeProvider";

const loginSchema = z.object({
  managementKey: z.string().trim().min(1, t("common.enter_the_management_key"))
});

type LoginValues = z.infer<typeof loginSchema>;

export function LoginPage({ notice = "", onAuthenticated }: { notice?: string; onAuthenticated?: () => void }) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [noticeDismissed, setNoticeDismissed] = useState(false);
  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { managementKey: "" }
  });
  useEffect(() => setNoticeDismissed(false), [notice]);
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: () => login(form.getValues("managementKey")),
    onSuccess: (session) => {
      form.reset({ managementKey: "" });
      setPasswordVisible(false);
      mutation.reset();
      queryClient.setQueryData(sessionQueryKey, session);
      onAuthenticated?.();
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 401) {
        form.reset({ managementKey: "" });
        setPasswordVisible(false);
      }
    }
  });
  const validationError = form.formState.errors.managementKey?.message;
  const requestError = mutation.isError
    ? mutation.error instanceof ApiError && mutation.error.status === 401
      ? t("common.invalid_management_key")
      : mutation.error.message
    : "";
  const errorMessage = requestError || validationError || (!noticeDismissed ? notice : "");

  return (
    <main className="login-layout auth-screen admin-login-layout">
      <section className="login-card auth-card">
        <div className="auth-language"><LanguageSelect /></div>
        <img
          className="auth-brand-logo"
          src={`/portal/assets/codex-cpa-pool-logo${theme === "dark" ? "-dark" : ""}.svg`}
          alt="Codex CPA Pool"
        />
        <h1>{t("common.sign_in_to_admin")}</h1>
        <p className="eyebrow">CONTROL PLANE</p>
        <form
          className="auth-form"
          onSubmit={form.handleSubmit(() => {
            setNoticeDismissed(true);
            if (!mutation.isPending) mutation.mutate();
          })}
        >
          <label htmlFor="management-key">{t("common.management_key")}</label>
          <div className="password-row">
            <span className="password-input">
              <input
                id="management-key"
                type={passwordVisible ? "text" : "password"}
                autoComplete="current-password"
                autoFocus
                required
                aria-invalid={Boolean(validationError)}
                {...form.register("managementKey")}
              />
              <button
                className="password-visibility-toggle"
                type="button"
                tabIndex={-1}
                aria-controls="management-key"
                aria-label={passwordVisible ? t("common.hide_password") : t("common.show_password")}
                aria-pressed={passwordVisible}
                title={passwordVisible ? t("common.hide_password") : t("common.show_password")}
                onClick={() => setPasswordVisible((visible) => !visible)}
              >
                <svg className="password-eye-show" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z" />
                  <circle cx="12" cy="12" r="2.75" />
                </svg>
                <svg className="password-eye-hide" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <path d="M3 3l18 18M10.6 6.2A10.7 10.7 0 0 1 12 6c6 0 9.5 6 9.5 6a17.6 17.6 0 0 1-2.5 3.2M6.2 6.2C3.8 8 2.5 12 2.5 12s3.5 6 9.5 6a9.9 9.9 0 0 0 3.2-.5M9.9 9.9a3 3 0 0 0 4.2 4.2" />
                </svg>
              </button>
            </span>
            <button className="button button-primary primary" type="submit" disabled={mutation.isPending}>
 {t("common.verify_sign_in")} </button>
          </div>
          <p className="form-error" role="alert">{errorMessage}</p>
        </form>
        <a className="quiet-link" href={applicationHref("usage")}>{t("common.open_usage_center_2")}</a>
      </section>
    </main>
  );
}
