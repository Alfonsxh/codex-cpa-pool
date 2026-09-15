import "../i18n/usage";
import { t } from "../i18n";
import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, Button, Form, Input, Modal, Space, Typography } from "antd";
import { useMutation } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { changePortalPassword } from "../api/portal";

const passwordSchema = z.object({
  currentPassword: z.string().min(1, t("usage.enter_your_current_password")).max(128, t("common.invalid_password_format")),
  newPassword: z.string().min(8, t("usage.the_new_password_must_contain_at_least_8_characters")).max(128, t("common.invalid_password_format")),
  confirmation: z.string().min(1, t("usage.enter_the_new_password_again"))
}).refine((values) => values.newPassword === values.confirmation, {
  path: ["confirmation"],
  message: t("usage.the_new_passwords_do_not_match")
}).refine((values) => values.newPassword !== values.currentPassword, {
  path: ["newPassword"],
  message: t("usage.the_new_password_must_differ_from_the_current_password")
});

type PasswordValues = z.infer<typeof passwordSchema>;

export function PortalPasswordModal({
  open,
  mandatory = false,
  onClose,
  onSuccess
}: {
  open: boolean;
  mandatory?: boolean;
  onClose: () => void;
  onSuccess: () => void;
}) {
  const form = useForm<PasswordValues>({
    resolver: zodResolver(passwordSchema),
    defaultValues: { currentPassword: "", newPassword: "", confirmation: "" }
  });
  const change = useMutation({
    gcTime: 0,
    mutationFn: () => changePortalPassword(
      form.getValues("currentPassword"),
      form.getValues("newPassword")
    ),
    onSuccess: () => {
      form.reset();
      change.reset();
      onSuccess();
    }
  });
  const close = () => {
    if (mandatory || change.isPending) return;
    form.reset();
    change.reset();
    onClose();
  };

  return (
    <Modal
      className="portal-password-modal"
      title={mandatory ? t("usage.change_your_password_on_first_sign_in") : t("usage.change_personal_password")}
      open={open}
      width={620}
      closable={!mandatory}
      mask={{ closable: !mandatory }}
      keyboard={!mandatory}
      footer={null}
      onCancel={close}
      destroyOnHidden
    >
      <Space orientation="vertical" size={16} className="portal-form-stack">
        <Typography.Paragraph type="secondary">
 {t("usage.other_browser_sessions_for_this_user_will_be_revoked_the")} </Typography.Paragraph>
        {change.isError ? (
          <Alert
            type="error"
            showIcon
            title={t("usage.unable_to_change_password")}
            description={change.error instanceof ApiError ? change.error.message : t("common.please_try_again_later")}
          />
        ) : null}
        <form
          className="portal-password-form"
          noValidate
          onSubmit={form.handleSubmit(() => change.mutate())}
        >
          <Form.Item
            label={t("usage.current_password")}
            htmlFor="portal-current-password"
            validateStatus={form.formState.errors.currentPassword ? "error" : undefined}
            help={form.formState.errors.currentPassword?.message}
          >
            <Controller
              control={form.control}
              name="currentPassword"
              render={({ field }) => (
                <Input.Password id="portal-current-password" autoComplete="current-password" visibilityToggle={{ tabIndex: -1 }} {...field} />
              )}
            />
          </Form.Item>
          <Form.Item
            label={t("usage.new_password")}
            htmlFor="portal-new-password"
            validateStatus={form.formState.errors.newPassword ? "error" : undefined}
            help={form.formState.errors.newPassword?.message}
          >
            <Controller
              control={form.control}
              name="newPassword"
              render={({ field }) => (
                <Input.Password id="portal-new-password" autoComplete="new-password" visibilityToggle={{ tabIndex: -1 }} {...field} />
              )}
            />
          </Form.Item>
          <Form.Item
            label={t("usage.confirm_new_password")}
            htmlFor="portal-password-confirmation"
            validateStatus={form.formState.errors.confirmation ? "error" : undefined}
            help={form.formState.errors.confirmation?.message}
          >
            <Controller
              control={form.control}
              name="confirmation"
              render={({ field }) => (
                <Input.Password id="portal-password-confirmation" autoComplete="new-password" visibilityToggle={{ tabIndex: -1 }} {...field} />
              )}
            />
          </Form.Item>
          <Space className="portal-form-actions">
            {!mandatory ? <Button tabIndex={-1} onClick={close}>{t("common.cancel")}</Button> : null}
            <Button type="primary" htmlType="submit" loading={change.isPending}>{t("usage.save_new_password")}</Button>
          </Space>
        </form>
      </Space>
    </Modal>
  );
}
