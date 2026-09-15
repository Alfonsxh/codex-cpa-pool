import "../i18n/admin";
import { t } from "../i18n";
import { zodResolver } from "@hookform/resolvers/zod";
import { KeyOutlined, ReloadOutlined, SaveOutlined, UndoOutlined, UploadOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Avatar, Button, Card, Col, Form, Input, Modal, Popconfirm, Row, Skeleton, Space, Tag, Typography } from "antd";
import { Controller, useForm } from "react-hook-form";
import { useEffect, useState } from "react";
import { z } from "zod";

import {
  generalSettingsQueryKey,
  readGeneralSettings,
  resetBrandingLogo,
  rotateManagementKey,
  saveBrandingLogo,
  saveGeneralSettings,
  type GeneralSettings,
  type GeneralSettingsValues
} from "../api/general-settings";
import { PageState } from "./components/PageState";
import { PageToolbar } from "./components/PageToolbar";
import { ConfigurationSectionNav } from "./ConfigurationSectionNav";
import { InitialPasswordModal } from "./InitialPasswordModal";
import { useTheme, type ThemeMode } from "./ThemeProvider";

const { Paragraph, Text } = Typography;

const settingsSchema = z.object({
  product_name: z.string().trim().min(2, t("admin.enter_at_least_2_characters")).max(64, t("admin.enter_no_more_than_64_characters")),
  short_name: z.string().trim().min(2, t("admin.enter_at_least_2_characters")).max(32, t("admin.enter_no_more_than_32_characters")),
  environment_label: z.string().trim().max(64, t("admin.enter_no_more_than_64_characters")),
  public_base_url: z.string().trim().refine(validPublicBaseURL, t("admin.enter_an_http_s_root_url_without_a_path_credentials")),
  allowed_email_domains: z.string(),
  key_prefix: z.string().regex(/^[a-z][a-z0-9_]{1,30}_$/, t("admin.enter_a_3_32_character_lowercase_prefix_ending_with_an")),
  provider_name: z.string().trim().min(2, t("admin.enter_at_least_2_characters")).max(48, t("admin.enter_no_more_than_48_characters")),
  api_key_env: z.string().regex(/^[A-Z][A-Z0-9_]{1,63}$/, t("admin.enter_a_valid_uppercase_environment_variable_name")),
  default_model: z.string().trim().min(1, t("admin.enter_a_default_model")).max(128, t("admin.enter_no_more_than_128_characters"))
});

type SettingsFormValues = z.infer<typeof settingsSchema>;

const managementKeySchema = z.object({
  new_key: z.string().min(12, t("admin.enter_at_least_12_characters")).max(128, t("admin.enter_no_more_than_128_characters")).regex(/^\S+$/, t("admin.whitespace_is_not_allowed")),
  confirmation: z.string().min(1, t("admin.enter_the_new_management_key_again"))
}).refine((values) => values.new_key === values.confirmation, {
  path: ["confirmation"],
  message: t("admin.the_management_keys_do_not_match")
});

type ManagementKeyFormValues = z.infer<typeof managementKeySchema>;
const maxLogoBytes = 2 * 1024 * 1024;
const supportedLogoTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"]);

export function GeneralSettingsPage({
  csrfToken,
  onManagementKeyRotated = () => undefined
}: {
  csrfToken: string;
  onManagementKeyRotated?: (message: string) => void;
}) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState("");
  const [initialPasswordOpen, setInitialPasswordOpen] = useState(false);
  const [logoOpen, setLogoOpen] = useState(false);
  const [logoFile, setLogoFile] = useState<File | null>(null);
  const [logoValidationError, setLogoValidationError] = useState("");
  const [managementKeyOpen, setManagementKeyOpen] = useState(false);
  const settings = useQuery({
    queryKey: generalSettingsQueryKey,
    queryFn: ({ signal }) => readGeneralSettings(signal)
  });
  const form = useForm<SettingsFormValues>({
    resolver: zodResolver(settingsSchema),
    defaultValues: emptyFormValues()
  });
  const managementKeyForm = useForm<ManagementKeyFormValues>({
    resolver: zodResolver(managementKeySchema),
    defaultValues: { new_key: "", confirmation: "" }
  });
  useEffect(() => {
    if (settings.data) form.reset(toFormValues(settings.data.values));
  }, [form, settings.data]);
  const mutation = useMutation({
    mutationFn: (values: SettingsFormValues) => saveGeneralSettings(toAPIValues(values), csrfToken),
    onSuccess: (result) => {
      queryClient.setQueryData(generalSettingsQueryKey, result.settings);
      form.reset(toFormValues(result.settings.values));
      setNotice(result.message);
    }
  });
  const logoMutation = useMutation({
    mutationFn: (file: File) => saveBrandingLogo(file, csrfToken),
    onSuccess: (result) => {
      queryClient.setQueryData<GeneralSettings>(generalSettingsQueryKey, (current) => current ? ({
        ...current,
        branding: {
          custom_logo: result.logo.custom,
          logo_sha256: result.logo.sha256
        }
      }) : current);
      setLogoFile(null);
      setLogoValidationError("");
      setLogoOpen(false);
      setNotice(result.message);
    }
  });
  const logoResetMutation = useMutation({
    mutationFn: () => resetBrandingLogo(csrfToken),
    onSuccess: (result) => {
      queryClient.setQueryData<GeneralSettings>(generalSettingsQueryKey, (current) => current ? ({
        ...current,
        branding: { custom_logo: false }
      }) : current);
      setNotice(result.message);
    }
  });
  const managementKeyMutation = useMutation({
    gcTime: 0,
    mutationFn: () => rotateManagementKey(
      managementKeyForm.getValues("new_key"),
      managementKeyForm.getValues("confirmation"),
      csrfToken
    ),
    onSuccess: (result) => {
      managementKeyForm.reset();
      managementKeyMutation.reset();
      setManagementKeyOpen(false);
      onManagementKeyRotated(result.message);
    }
  });

  if (settings.isPending) {
    return <section className="page-content"><Skeleton active paragraph={{ rows: 10 }} /></section>;
  }
  if (settings.isError) {
    return (
      <section className="page-content">
        <PageState
          kind="error"
          title={t("admin.unable_to_load_general_settings")}
          detail={settings.error instanceof Error ? settings.error.message : t("common.please_try_again_later")}
          onAction={() => void settings.refetch()}
        />
      </section>
    );
  }

  return (
    <section className="page-content settings-page">
      <ConfigurationSectionNav />
      <PageToolbar
        description={t("admin.manage_brand_login_domains_and_client_export_fields_that_take")}
        actions={(
          <Button icon={<ReloadOutlined aria-hidden="true" />} loading={settings.isFetching} onClick={() => void settings.refetch()}>
 {t("admin.refresh_page")} </Button>
        )}
      />
      {notice ? <Alert className="page-alert" type="success" showIcon closable title={notice} onClose={() => setNotice("")} /> : null}
      {mutation.isError ? (
        <Alert className="page-alert" type="error" showIcon title={t("admin.settings_were_not_saved")} description={mutation.error instanceof Error ? mutation.error.message : t("admin.request_failed")} />
      ) : null}

      <Row gutter={[16, 16]} className="settings-status-grid">
        <Col xs={24} md={8}>
          <Card title={t("common.management_key")}>
            <Space orientation="vertical" size={12}>
              <Tag color={settings.data.security.management_key_configured ? "success" : "error"}>{settings.data.security.management_key_configured ? t("admin.configured") : t("admin.not_configured")}</Tag>
              <Button
                size="small"
                icon={<KeyOutlined aria-hidden="true" />}
                disabled={!settings.data.security.management_key_configured}
                onClick={() => setManagementKeyOpen(true)}
              >
 {t("admin.rotate_key_2")} </Button>
            </Space>
          </Card>
        </Col>
        <Col xs={24} md={8}>
          <Card title={t("admin.initial_user_password")}>
            <Space orientation="vertical" size={12}>
              <Tag color={settings.data.security.initial_password_configured ? "success" : "warning"}>{settings.data.security.initial_password_configured ? t("admin.configured") : t("admin.not_configured")}</Tag>
              <Button size="small" onClick={() => setInitialPasswordOpen(true)}>{settings.data.security.initial_password_configured ? t("admin.update_password") : t("admin.set_up_now")}</Button>
            </Space>
          </Card>
        </Col>
        <Col xs={24} md={8}>
          <Card title={t("admin.brand_logo")}>
            <Space size={12} align="center">
              <Avatar
                className="settings-logo-preview"
                shape="square"
                size={44}
                src={logoPreviewURL(settings.data, theme)}
              >
                C
              </Avatar>
              <Space orientation="vertical" size={8}>
                <Tag color={settings.data.branding.custom_logo ? "blue" : "default"}>{settings.data.branding.custom_logo ? t("admin.custom") : t("admin.default")}</Tag>
                <Space size={6} wrap>
                  <Button size="small" icon={<UploadOutlined aria-hidden="true" />} onClick={() => setLogoOpen(true)}>
                    {settings.data.branding.custom_logo ? t("admin.replace") : t("admin.upload")}
                  </Button>
                  {settings.data.branding.custom_logo ? (
                    <Popconfirm
                      title={t("admin.restore_the_default_logo")}
                      description={t("admin.the_custom_logo_will_be_removed_from_the_control_plane")}
                      okText={t("admin.confirm_restore")}
                      cancelText={t("common.cancel")}
                      onConfirm={() => logoResetMutation.mutate()}
                    >
                      <Button size="small" icon={<UndoOutlined aria-hidden="true" />} loading={logoResetMutation.isPending}>{t("admin.restore_default")}</Button>
                    </Popconfirm>
                  ) : null}
                </Space>
              </Space>
            </Space>
          </Card>
        </Col>
      </Row>

      <form onSubmit={form.handleSubmit((values) => mutation.mutate(values))}>
        <Card
          title={t("admin.brand_identity")}
          className="settings-form-card"
          extra={<Text type="secondary">{t("admin.saved_as_individual_sqlite_updates_takes_effect_immediately")}</Text>}
        >
          <Row gutter={[18, 0]}>
            <Col xs={24} lg={12}><FormField control={form.control} name="product_name" label={t("admin.product_name")} /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="short_name" label={t("admin.short_name")} /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="environment_label" label={t("admin.environment_label")} /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="public_base_url" label={t("admin.public_url")} placeholder="https://cpa.example.com" /></Col>
            <Col xs={24}>
              <Controller
                control={form.control}
                name="allowed_email_domains"
                render={({ field, fieldState }) => (
                  <Form.Item label={t("admin.allowed_email_domains")} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message ?? t("admin.separate_with_commas_spaces_or_newlines_leaving_this_blank_prevents")}>
                    <Input.TextArea {...field} aria-label={t("admin.allowed_email_domains")} autoSize={{ minRows: 2, maxRows: 4 }} placeholder="example.com, example.org" />
                  </Form.Item>
                )}
              />
            </Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="key_prefix" label={t("admin.new_key_prefix")} /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="provider_name" label={t("admin.client_provider_name")} /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="api_key_env" label={t("admin.client_key_environment_variable")} /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="default_model" label={t("admin.default_client_model")} /></Col>
          </Row>
          <Space>
            <Button type="primary" htmlType="submit" icon={<SaveOutlined aria-hidden="true" />} loading={mutation.isPending}>{t("admin.save_general_settings")}</Button>
            <Button type="default" icon={<UndoOutlined aria-hidden="true" />} disabled={mutation.isPending || !form.formState.isDirty} onClick={() => form.reset(toFormValues(settings.data.values))}>{t("admin.discard_changes")}</Button>
          </Space>
        </Card>
      </form>
      <InitialPasswordModal
        open={initialPasswordOpen}
        csrfToken={csrfToken}
        onClose={() => setInitialPasswordOpen(false)}
        onSuccess={(message) => {
          queryClient.setQueryData(generalSettingsQueryKey, {
            ...settings.data,
            security: { ...settings.data.security, initial_password_configured: true }
          });
          setInitialPasswordOpen(false);
          setNotice(message);
        }}
      />
      <Modal
        title={settings.data.branding.custom_logo ? t("admin.replace_brand_logo") : t("admin.upload_brand_logo")}
        open={logoOpen}
        okText={t("admin.save_logo")}
        cancelText={t("common.cancel")}
        confirmLoading={logoMutation.isPending}
        okButtonProps={{ disabled: !logoFile || Boolean(logoValidationError) }}
        onCancel={() => {
          if (logoMutation.isPending) return;
          setLogoOpen(false);
          setLogoFile(null);
          setLogoValidationError("");
          logoMutation.reset();
        }}
        onOk={() => logoFile && logoMutation.mutate(logoFile)}
        destroyOnHidden
      >
        <Paragraph type="secondary">{t("admin.png_jpeg_gif_webp_or_safe_svg_up_to_2")}</Paragraph>
        <label className="logo-file-picker">
          <UploadOutlined aria-hidden="true" />
          <span>{logoFile ? logoFile.name : t("admin.choose_logo_file")}</span>
          <input
            aria-label={t("admin.logo_file")}
            type="file"
            accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml"
            onChange={(event) => {
              const selected = event.target.files?.[0] ?? null;
              const validationError = selected ? validateLogoFile(selected) : t("admin.choose_a_logo_file");
              setLogoFile(validationError ? null : selected);
              setLogoValidationError(validationError);
              logoMutation.reset();
            }}
          />
        </label>
        {logoValidationError ? <Alert className="page-alert" type="error" showIcon message={logoValidationError} /> : null}
        {logoMutation.isError ? <Alert className="page-alert" type="error" showIcon message={t("admin.logo_was_not_saved")} description={logoMutation.error instanceof Error ? logoMutation.error.message : t("common.please_try_again_later")} /> : null}
      </Modal>
      <Modal
        title={t("admin.rotate_management_key")}
        open={managementKeyOpen}
        okText={t("admin.rotate_sign_in_again")}
        cancelText={t("common.cancel")}
        confirmLoading={managementKeyMutation.isPending}
        cancelButtonProps={{ tabIndex: -1 }}
        okButtonProps={{ htmlType: "submit", form: "general-settings-management-key-form" }}
        onCancel={() => {
          if (managementKeyMutation.isPending) return;
          setManagementKeyOpen(false);
          managementKeyForm.reset();
          managementKeyMutation.reset();
        }}
        destroyOnHidden
      >
        <Alert
          className="page-alert"
          type="warning"
          showIcon
          message={t("admin.all_management_sessions_end_immediately_after_submission")}
          description={t("admin.api_keys_user_sessions_and_data_plane_traffic_are_unchanged")}
        />
        {managementKeyMutation.isError ? <Alert className="page-alert" type="error" showIcon message={t("admin.management_key_was_not_updated")} description={managementKeyMutation.error instanceof Error ? managementKeyMutation.error.message : t("common.please_try_again_later")} /> : null}
        <form id="general-settings-management-key-form" onSubmit={managementKeyForm.handleSubmit(() => managementKeyMutation.mutate())}>
        <Form component={false} layout="vertical" requiredMark={false}>
          <Controller
            control={managementKeyForm.control}
            name="new_key"
            render={({ field, fieldState }) => (
              <Form.Item label={t("admin.new_management_key")} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
                <Input.Password {...field} aria-label={t("admin.new_management_key")} autoComplete="new-password" visibilityToggle={{ tabIndex: -1 }} />
              </Form.Item>
            )}
          />
          <Controller
            control={managementKeyForm.control}
            name="confirmation"
            render={({ field, fieldState }) => (
              <Form.Item label={t("admin.confirm_new_management_key")} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
                <Input.Password {...field} aria-label={t("admin.confirm_new_management_key")} autoComplete="new-password" visibilityToggle={{ tabIndex: -1 }} />
              </Form.Item>
            )}
          />
        </Form>
        </form>
      </Modal>
    </section>
  );
}

function FormField({ control, name, label, placeholder }: {
  control: ReturnType<typeof useForm<SettingsFormValues>>["control"];
  name: Exclude<keyof SettingsFormValues, "allowed_email_domains">;
  label: string;
  placeholder?: string;
}) {
  return (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <Form.Item label={label} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
          <Input {...field} aria-label={label} placeholder={placeholder} />
        </Form.Item>
      )}
    />
  );
}

function emptyFormValues(): SettingsFormValues {
  return {
    product_name: "", short_name: "", environment_label: "", public_base_url: "",
    allowed_email_domains: "", key_prefix: "ccpa_", provider_name: "", api_key_env: "CCPA_API_KEY", default_model: ""
  };
}

function toFormValues(values: GeneralSettingsValues): SettingsFormValues {
  return { ...values, allowed_email_domains: values.allowed_email_domains.join(", ") };
}

function toAPIValues(values: SettingsFormValues): GeneralSettingsValues {
  return {
    ...values,
    allowed_email_domains: values.allowed_email_domains
      .split(/[,，\s]+/)
      .map((domain) => domain.trim())
      .filter(Boolean)
  };
}

function validPublicBaseURL(value: string) {
  if (!value) return true;
  try {
    const parsed = new URL(value);
    return (parsed.protocol === "http:" || parsed.protocol === "https:") &&
      !parsed.username && !parsed.password &&
      (parsed.pathname === "" || parsed.pathname === "/") &&
      !parsed.search && !parsed.hash;
  } catch {
    return false;
  }
}

function validateLogoFile(file: File) {
  if (!supportedLogoTypes.has(file.type)) return t("admin.only_png_jpeg_gif_webp_or_svg_files_are_supported");
  if (file.size < 1) return t("admin.the_logo_file_cannot_be_empty");
  if (file.size > maxLogoBytes) return t("admin.the_logo_file_must_not_exceed_2_mib");
  if (Array.from(file.name).length > 128) return t("admin.the_logo_filename_must_not_exceed_128_characters");
  return "";
}

function logoPreviewURL(settings: GeneralSettings, theme: ThemeMode) {
  if (!settings.branding.custom_logo) return `/portal/assets/codex-cpa-pool-mark${theme === "dark" ? "-dark" : ""}.svg`;
  const digest = settings.branding.logo_sha256 ?? "";
  return digest ? `/branding/logo?v=${encodeURIComponent(digest.slice(0, 16))}` : "/branding/logo";
}
