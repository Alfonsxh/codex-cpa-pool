import "../i18n/admin";
import { t } from "../i18n";
import { zodResolver } from "@hookform/resolvers/zod";
import { Button, Modal } from "antd";
import { useMutation } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { saveInitialPassword } from "../api/general-settings";
import { LegacyPasswordInput } from "./components/LegacyPasswordInput";

const initialPasswordSchema = z.object({
  initialPassword: z.string().min(8, t("admin.the_initial_password_must_contain_at_least_8_characters")).max(128, t("admin.the_initial_password_must_not_exceed_128_characters")),
  confirmation: z.string().min(1, t("admin.enter_the_initial_password_again"))
}).refine((values) => values.initialPassword === values.confirmation, {
  path: ["confirmation"],
  message: t("admin.the_initial_passwords_do_not_match")
}).refine((values) => values.initialPassword !== "123456", {
  path: ["initialPassword"],
  message: t("admin.this_retired_default_password_cannot_be_used")
});

type InitialPasswordValues = z.infer<typeof initialPasswordSchema>;

export function InitialPasswordModal({
  open,
  csrfToken,
  onClose,
  onSuccess
}: {
  open: boolean;
  csrfToken: string;
  onClose: () => void;
  onSuccess: (message: string) => void;
}) {
  const form = useForm<InitialPasswordValues>({
    resolver: zodResolver(initialPasswordSchema),
    defaultValues: { initialPassword: "", confirmation: "" }
  });
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: () => saveInitialPassword(
      form.getValues("initialPassword"),
      form.getValues("confirmation"),
      csrfToken
    ),
    onSuccess: (result) => {
      form.reset();
      mutation.reset();
      onSuccess(result.message);
    }
  });
  const close = () => {
    if (mutation.isPending) return;
    form.reset();
    mutation.reset();
    onClose();
  };
  const error = form.formState.errors.initialPassword?.message
    ?? form.formState.errors.confirmation?.message
    ?? (mutation.isError
      ? mutation.error instanceof ApiError ? mutation.error.message : t("common.please_try_again_later")
      : "");

  return (
    <Modal
      className="legacy-account-editor-modal legacy-settings-form-modal"
      title={<div className="legacy-dialog-title"><strong>{t("admin.set_initial_user_password")}</strong><span>ENCRYPTED USER SECRET</span></div>}
      open={open}
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      afterOpenChange={(visible) => { if (visible) form.setFocus("initialPassword"); }}
      onCancel={close}
      destroyOnHidden
      footer={[
        <Button key="cancel" className="legacy-modal-ghost" tabIndex={-1} disabled={mutation.isPending} onClick={close}>{t("common.cancel")}</Button>,
        <Button
          key="submit"
          type="primary"
          htmlType="submit"
          form="settings-initial-password-form"
          disabled={mutation.isPending}
        >{mutation.isPending ? t("admin.saving") : t("admin.save_securely")}</Button>
      ]}
    >
      <div className="warning-banner">{t("admin.used_for_new_users_and_password_resets_stored_encrypted_and")}</div>
      <form id="settings-initial-password-form" noValidate onSubmit={form.handleSubmit(() => mutation.mutate())}>
        <div className="field">
          <label htmlFor="settings-initial-password">{t("admin.new_initial_password")}</label>
          <Controller control={form.control} name="initialPassword" render={({ field }) => (
            <LegacyPasswordInput id="settings-initial-password" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={8} maxLength={128} onValueChange={field.onChange} />
          )} />
        </div>
        <div className="field account-email-field">
          <label htmlFor="settings-initial-password-confirmation">{t("admin.confirm_key")}</label>
          <Controller control={form.control} name="confirmation" render={({ field }) => (
            <LegacyPasswordInput id="settings-initial-password-confirmation" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={8} maxLength={128} onValueChange={field.onChange} />
          )} />
        </div>
        <p className="form-error" role="alert">{error}</p>
      </form>
    </Modal>
  );
}
