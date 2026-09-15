import "../i18n/admin";
import { t } from "../i18n";
import { useSiteTimezone, getSiteTimezone } from "./site-time";
import { zodResolver } from "@hookform/resolvers/zod";
import {
  BellOutlined,
  DeleteOutlined,
  ReloadOutlined,
  SaveOutlined,
  SendOutlined
} from "@ant-design/icons";
import {
  Alert,
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Space,
  Switch,
  Tag,
  Typography
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import {
  clearNotificationWebhook,
  notificationSettingsQueryKey,
  readNotificationSettings,
  saveNotificationSettings,
  saveNotificationWebhook,
  sendNotification,
  testNotification,
  type NotificationValues
} from "../api/notifications";
import { PageState } from "./components/PageState";
import { NotificationRuntimeStatus } from "./components/NotificationRuntimeStatus";
import { PageToolbar } from "./components/PageToolbar";
import { ConfigurationSectionNav } from "./ConfigurationSectionNav";

const { Paragraph, Text } = Typography;

const notificationSchema = z.object({
  enabled: z.boolean(),
  timezone: z.string().trim().min(1, t("admin.enter_an_iana_timezone")),
  daily_times: z.string().trim().regex(
    /^([01]\d|2[0-3]):[0-5]\d(?:,\s*([01]\d|2[0-3]):[0-5]\d)*$/,
    t("admin.enter_hh_mm_separate_multiple_times_with_commas")
  ),
  schedule_grace_minutes: z.number().int().min(0).max(120),
  quota_alert_enabled: z.boolean(),
  weekly_threshold_percent: z.number().min(1).max(100)
});

const webhookSchema = z.object({
  webhook_url: z.string().trim().url(t("admin.enter_the_full_webhook_url")).startsWith(
    "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=",
    t("admin.only_official_wecom_message_webhook_urls_are_supported")
  )
});

type WebhookValues = z.infer<typeof webhookSchema>;

export function NotificationSettingsPage({ csrfToken }: { csrfToken: string }) {
  useSiteTimezone();
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState("");
  const [clearOpen, setClearOpen] = useState(false);
  const settings = useQuery({
    queryKey: notificationSettingsQueryKey,
    queryFn: ({ signal }) => readNotificationSettings(signal),
    staleTime: 0,
    gcTime: 0,
    refetchInterval: 10_000,
    refetchOnWindowFocus: true
  });
  const form = useForm<NotificationValues>({
    resolver: zodResolver(notificationSchema),
    values: settings.data?.values,
    resetOptions: { keepDirtyValues: true },
    defaultValues: {
      enabled: false,
      timezone: getSiteTimezone(),
      daily_times: "09:00,14:00,18:00",
      schedule_grace_minutes: 15,
      quota_alert_enabled: true,
      weekly_threshold_percent: 90
    }
  });
  const webhookForm = useForm<WebhookValues>({
    resolver: zodResolver(webhookSchema),
    defaultValues: { webhook_url: "" }
  });

  const refresh = async (message: string) => {
    setNotice(message);
    await queryClient.invalidateQueries({ queryKey: notificationSettingsQueryKey, exact: true });
  };
  const settingsMutation = useMutation({
    mutationFn: (values: NotificationValues) => saveNotificationSettings(values, csrfToken),
    onSuccess: async (result) => {
      form.reset(result.values);
      await refresh(result.message);
    }
  });
  const webhookMutation = useMutation({
    gcTime: 0,
    mutationFn: () => saveNotificationWebhook(webhookForm.getValues("webhook_url"), csrfToken),
    onSuccess: async (result) => {
      webhookForm.reset({ webhook_url: "" });
      await refresh(result.message);
    }
  });
  const clearMutation = useMutation({
    mutationFn: () => clearNotificationWebhook(csrfToken),
    onSuccess: async (result) => {
      setClearOpen(false);
      webhookForm.reset({ webhook_url: "" });
      form.setValue("enabled", false);
      await refresh(result.message);
    }
  });
  const sendMutation = useMutation({
    mutationFn: () => sendNotification(csrfToken),
    onSuccess: async (result) => refresh(result.message)
  });
  const testMutation = useMutation({
    mutationFn: () => testNotification(csrfToken),
    onSuccess: async (result) => refresh(result.message)
  });

  if (settings.isPending) {
    return <NotificationPageSkeleton />;
  }
  if (!settings.data) {
    return (
      <section className="page-content">
        <PageState
          kind="error"
          title={t("admin.unable_to_load_notification_settings")}
          detail={settings.error instanceof Error ? settings.error.message : t("common.please_try_again_later")}
          onAction={() => void settings.refetch()}
        />
      </section>
    );
  }

  const status = settings.data.notifications;
  const mutationError = settingsMutation.error ?? webhookMutation.error ?? clearMutation.error ?? sendMutation.error ?? testMutation.error;

  return (
    <section className="page-content notification-page">
      <ConfigurationSectionNav />
      <PageToolbar
        className="account-page-intro"
        description={t("admin.this_page_reads_notification_settings_and_runtime_status_every_10")}
        actions={(
          <Button icon={<ReloadOutlined aria-hidden="true" />} loading={settings.isFetching} onClick={() => void settings.refetch()}>
 {t("admin.refresh_page")} </Button>
        )}
      />

      {notice ? <Alert className="page-alert" type="success" showIcon closable message={notice} onClose={() => setNotice("")} /> : null}
      {mutationError ? (
        <Alert
          className="page-alert"
          type="error"
          showIcon
          message={t("admin.notification_action_failed")}
          description={mutationError instanceof ApiError ? mutationError.message : t("common.please_try_again_later")}
        />
      ) : null}

      <Row gutter={[16, 16]} className="notification-grid">
        <Col xs={24} xl={10}>
          <Card title={t("admin.wecom_webhook")} extra={<WebhookTag configured={status.webhook_configured} />}>
            <Form layout="vertical" requiredMark={false} onFinish={() => webhookForm.handleSubmit(() => webhookMutation.mutate())()}>
              <Form.Item
                label={t("admin.webhook_url")}
                htmlFor="notification-webhook"
                validateStatus={webhookForm.formState.errors.webhook_url ? "error" : undefined}
                help={webhookForm.formState.errors.webhook_url?.message ?? t("admin.the_secret_is_encrypted_in_the_control_plane_sqlite_database")}
              >
                <Controller
                  control={webhookForm.control}
                  name="webhook_url"
                  render={({ field }) => (
                    <Input.Password
                      {...field}
                      id="notification-webhook"
                      autoComplete="off"
                      visibilityToggle={{ tabIndex: -1 }}
                      placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
                    />
                  )}
                />
              </Form.Item>
              <Space wrap>
                <Button
                  type="primary"
                  htmlType="submit"
                  icon={<SaveOutlined aria-hidden="true" />}
                  loading={webhookMutation.isPending}
                >
 {t("admin.save_webhook")} </Button>
                <Button
                  danger
                  icon={<DeleteOutlined aria-hidden="true" />}
                  disabled={!status.webhook_configured}
                  onClick={() => setClearOpen(true)}
                >
 {t("admin.clear")} </Button>
                <Button
                  icon={<SendOutlined aria-hidden="true" />}
                  loading={sendMutation.isPending}
                  disabled={!status.webhook_configured}
                  onClick={() => sendMutation.mutate()}
                >
 {t("admin.send_account_information")} </Button>
                <Button
                  icon={<BellOutlined aria-hidden="true" />}
                  loading={testMutation.isPending}
                  disabled={!status.webhook_configured}
                  onClick={() => testMutation.mutate()}
                >
 {t("admin.send_test_message")} </Button>
              </Space>
            </Form>
          </Card>

          <Card className="notification-status-card" title={t("admin.runtime_status")}>
            <NotificationRuntimeStatus status={status} enabled={settings.data.values.enabled} unavailable={settings.isError} />
          </Card>
        </Col>

        <Col xs={24} xl={14}>
          <Card title={t("admin.delivery_alert_rules")} extra={<BellOutlined aria-hidden="true" />}>
            <Form layout="vertical" requiredMark={false}>
              <Controller
                control={form.control}
                name="enabled"
                render={({ field }) => (
                  <Form.Item label={t("admin.scheduled_notifications")} extra={t("admin.save_a_valid_webhook_before_enabling_notifications")}>
                    <Switch
                      aria-label={t("admin.enable_notification_scheduling")}
                      checked={field.value}
                      onChange={field.onChange}
                      checkedChildren={t("admin.enable")}
                      unCheckedChildren={t("common.close")}
                    />
                  </Form.Item>
                )}
              />
              <Row gutter={16}>
                <Col xs={24} md={12}>
                  <Form.Item label={t("common.system_timezone")}>
                    <span>{settings.data?.values.timezone || getSiteTimezone()}{t("admin.configured_in_configuration_center")}</span>
                  </Form.Item>
                </Col>
                <Col xs={24} md={12}>
                  <Form.Item
                    label={t("admin.daily_delivery_times")}
                    htmlFor="notification-times"
                    validateStatus={form.formState.errors.daily_times ? "error" : undefined}
                    help={form.formState.errors.daily_times?.message}
                  >
                    <Controller
                      control={form.control}
                      name="daily_times"
                      render={({ field }) => <Input {...field} id="notification-times" placeholder="09:00,14:00,18:00" />}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} md={12}>
                  <Controller
                    control={form.control}
                    name="schedule_grace_minutes"
                    render={({ field }) => (
                      <Form.Item label={t("admin.missed_delivery_window")} validateStatus={form.formState.errors.schedule_grace_minutes ? "error" : undefined}>
                        <InputNumber {...field} min={0} max={120} precision={0} suffix={t("admin.minutes")} onChange={(value) => field.onChange(value ?? 0)} />
                      </Form.Item>
                    )}
                  />
                </Col>
                <Col xs={24} md={12}>
                  <Controller
                    control={form.control}
                    name="weekly_threshold_percent"
                    render={({ field }) => (
                      <Form.Item label={t("admin.weekly_quota_alert_threshold")} validateStatus={form.formState.errors.weekly_threshold_percent ? "error" : undefined}>
                        <InputNumber {...field} min={1} max={100} step={0.5} suffix="%" onChange={(value) => field.onChange(value ?? 90)} />
                      </Form.Item>
                    )}
                  />
                </Col>
              </Row>
              <Controller
                control={form.control}
                name="quota_alert_enabled"
                render={({ field }) => (
                  <Form.Item label={t("admin.quota_status_alerts")} extra={t("admin.warning_exhaustion_recovery_and_reset_notifications_are_deduplicated_per_window")}>
                    <Switch
                      aria-label={t("admin.enable_quota_status_alerts")}
                      checked={field.value}
                      onChange={field.onChange}
                      checkedChildren={t("admin.enable")}
                      unCheckedChildren={t("common.close")}
                    />
                  </Form.Item>
                )}
              />
              <Button
                type="primary"
                icon={<SaveOutlined aria-hidden="true" />}
                loading={settingsMutation.isPending}
                onClick={() => void form.handleSubmit((values) => settingsMutation.mutate(values))()}
              >
 {t("admin.save_notification_rules")} </Button>
            </Form>
          </Card>
        </Col>
      </Row>

      <Modal
        title={t("admin.clear_the_wecom_webhook")}
        open={clearOpen}
        okText={t("admin.confirm_clear")}
        cancelText={t("common.cancel")}
        okButtonProps={{ danger: true }}
        confirmLoading={clearMutation.isPending}
        onCancel={() => !clearMutation.isPending && setClearOpen(false)}
        onOk={() => clearMutation.mutate()}
      >
        <Paragraph>{t("admin.clearing_also_disables_notification_scheduling_delivery_history_is_retained_the")}</Paragraph>
      </Modal>
    </section>
  );
}

function WebhookTag({ configured }: { configured: boolean }) {
  return <Tag color={configured ? "success" : "default"}>{configured ? t("admin.configured") : t("admin.not_configured")}</Tag>;
}

function NotificationPageSkeleton() {
  return (
    <section className="page-content" aria-label={t("admin.loading_notification_settings")}>
      <div className="skeleton skeleton-title" />
      <div className="skeleton skeleton-line" />
      <div className="skeleton skeleton-table" />
    </section>
  );
}
