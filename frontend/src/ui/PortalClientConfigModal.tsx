import "../i18n/usage";
import { t } from "../i18n";
import { Alert, Button, Modal, Skeleton, Space, Typography } from "antd";
import { useEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";

import { ApiError } from "../api/client";
import { readPortalKey } from "../api/portal";
import {
  defaultPublicSiteConfiguration,
  readPublicSiteConfiguration,
  type PublicSiteConfiguration
} from "../api/public-site";

export type PortalClientConfigMode = "codex" | "claude" | "ccswitch";

type ClientConfig = {
  title: string;
  file?: string;
  steps?: string[];
  notice?: string;
  value: string;
  sections?: ClientConfigSection[];
  copyLabel?: string;
  externalLink?: string;
};

type ClientConfigSection = {
  title: string;
  file: string;
  description: string;
  value: string;
  hint?: string;
  copyLabel?: string;
};

const historyPrompt = t("usage.my_sign_in_method_has_changed_from_oauth_to_an");

export function PortalClientConfigModal({
  open,
  mode,
  user,
  currentGroup,
  onClose,
  onSessionExpired
}: {
  open: boolean;
  mode: PortalClientConfigMode;
  user: string;
  currentGroup: string;
  onClose: () => void;
  onSessionExpired: () => void;
}) {
  const [apiKey, setAPIKey] = useState("");
  const [siteConfig, setSiteConfig] = useState<PublicSiteConfiguration>(defaultPublicSiteConfiguration);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const request = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!open) return;
    setCopied("");
    setError("");
  }, [mode, open]);

  useEffect(() => {
    if (!open) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setAPIKey("");
    void Promise.all([
      readPortalKey(controller.signal),
      readPublicSiteConfiguration(controller.signal).catch(() => defaultPublicSiteConfiguration)
    ]).then(([key, configuration]) => {
      if (request.current !== controller) return;
      setAPIKey(key.api_key);
      setSiteConfig(configuration);
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || request.current !== controller) return;
      if (reason instanceof ApiError && reason.status === 401) {
        onSessionExpired();
        return;
      }
      setError(reason instanceof Error ? reason.message : t("usage.unable_to_read_client_configuration"));
    }).finally(() => {
      if (request.current !== controller) return;
      request.current = null;
      setLoading(false);
    });
    return () => controller.abort();
  }, [mode, onSessionExpired, open]);

  const config = useMemo(() => buildClientConfig({
    mode,
    apiKey,
    user,
    currentGroup,
    siteConfig,
    browserOrigin: typeof window === "undefined" ? "" : window.location.origin
  }), [apiKey, currentGroup, mode, siteConfig, user]);

  const close = () => {
    request.current?.abort();
    request.current = null;
    // The generated snippets contain the full Key. Clear them synchronously
    // before Ant Design runs its closing animation and keeps the Modal mounted.
    flushSync(() => {
      setAPIKey("");
      setError("");
      setCopied("");
      setLoading(false);
    });
    onClose();
  };

  const copy = async (value: string, label = t("common.copied_2")) => {
    try {
      await writeClipboardText(value);
      setCopied(label);
    } catch {
      setCopied(t("usage.copy_failed_select_the_configuration_and_copy_it_manually"));
    }
  };

  const copyAndImport = async () => {
    if (!config.externalLink) return;
    const result = await copyAndImportConfig({
      value: config.value,
      externalLink: config.externalLink,
      writeText: writeClipboardText,
      openLink: (link) => window.location.assign(link)
    });
    flushSync(() => setCopied(result.message));
  };

  return (
    <Modal
      className="portal-client-config-modal"
      title={config.title}
      open={open}
      width={mode === "claude" || mode === "ccswitch" ? 960 : 760}
      footer={null}
      onCancel={close}
      destroyOnHidden
    >
      {loading ? (
        <Skeleton active paragraph={{ rows: 8 }} />
      ) : error ? (
        <Space orientation="vertical" size={14} className="portal-config-stack">
          <Alert type="error" showIcon title={t("usage.unable_to_read_client_configuration")} description={error} />
          <Button onClick={close}>{t("common.close")}</Button>
        </Space>
      ) : apiKey ? (
        <Space orientation="vertical" size={16} className="portal-config-stack">
          {mode === "claude" && config.notice ? <Alert type="warning" showIcon title={t("usage.configuration_contains_the_full_api_key")} description={config.notice} /> : null}
          {mode === "claude" ? (
            <div className="portal-config-guide">
              <div><Typography.Text type="secondary">{t("usage.file_to_edit")}</Typography.Text><code>{config.file}</code></div>
              <div><Typography.Text type="secondary">{t("usage.steps")}</Typography.Text><ol>{config.steps?.map((step) => <li key={step}>{step}</li>)}</ol></div>
            </div>
          ) : null}
          {config.sections?.length ? (
            <div className="portal-config-workflow">
              {config.sections.map((section, index) => (
                <article className="portal-config-step" key={section.title}>
                  <span className="portal-config-step-number">{String(index + 1).padStart(2, "0")}</span>
                  <div>
                    <header>
                      <span><strong>{section.title}</strong><code>{section.file}</code></span>
                      {section.copyLabel ? <Button size="small" onClick={() => void copy(section.value, t("common.copied", [section.title]))}>{section.copyLabel}</Button> : null}
                    </header>
                    <p>{section.description}</p>
                    <pre className="portal-config-preview"><code>{section.value}</code></pre>
                    {section.hint ? <small>{section.hint}</small> : null}
                  </div>
                </article>
              ))}
            </div>
          ) : <pre className="portal-config-preview"><code>{config.value}</code></pre>}
          <ConfigActions
            copied={copied}
            onClose={close}
            onCopy={mode === "codex" ? undefined : mode === "ccswitch" ? () => void copyAndImport() : () => void copy(config.value, t("usage.configuration_copied"))}
            copyLabel={mode === "ccswitch" ? t("usage.copy_import") : config.copyLabel ?? t("usage.copy_configuration")}
          />
        </Space>
      ) : null}
    </Modal>
  );
}

function ConfigActions({
  copied,
  onClose,
  onCopy,
  copyLabel,
  closeLabel = t("common.close")
}: {
  copied: string;
  onClose: () => void;
  onCopy?: () => void;
  copyLabel: string;
  closeLabel?: string;
}) {
  return (
    <div className="portal-config-actions">
      <Typography.Text type={copied.startsWith(t("common.copy_failed")) ? "danger" : "secondary"} role="status">{copied}</Typography.Text>
      <Space wrap>
        <Button onClick={onClose}>{closeLabel}</Button>
        {onCopy ? <Button type="primary" onClick={onCopy}>{copyLabel}</Button> : null}
      </Space>
    </div>
  );
}

export function buildClientConfig({
  mode,
  apiKey,
  user,
  currentGroup,
  siteConfig,
  browserOrigin
}: {
  mode: PortalClientConfigMode;
  apiKey: string;
  user: string;
  currentGroup: string;
  siteConfig: PublicSiteConfiguration;
  browserOrigin: string;
}): ClientConfig {
  const origin = publicBaseURL(siteConfig.public_base_url, browserOrigin);
  const baseURL = `${origin}/v1`;
  const model = siteConfig.default_model || "gpt-5.6-sol";
  const provider = `${siteConfig.provider_name || "Codex CPA Pool"} · ${user.split("@", 1)[0] || "user"}`;
  const environment = siteConfig.api_key_env || "CCPA_API_KEY";
  const codex = buildCodexConfig(provider, baseURL, model, apiKey);
  if (mode === "codex") {
    return {
      title: t("common.configure_codex"),
      value: codex,
      sections: [
        {
          title: t("usage.codex_configuration"),
          file: "~/.codex/config.toml",
          description: t("usage.merge_the_following_into_your_codex_configuration_file_save_it"),
          value: codex,
          hint: t("usage.this_configuration_contains_your_current_api_key_save_it_only"),
          copyLabel: t("usage.copy_configuration")
        },
        {
          title: t("usage.migrate_previous_sessions"),
          file: "Codex Agent",
          description: t("usage.give_the_following_instruction_to_codex_agent_to_migrate_sessions"),
          value: historyPrompt,
          copyLabel: t("usage.copy_migration_instruction")
        }
      ]
    };
  }
  const launcher = buildClaudeLauncher(environment, origin, model);
  if (mode === "claude") {
    return {
      title: t("usage.claude_code_terminal_setup"),
      file: "~/.config/claude-cpa/",
      steps: [t("usage.prepare_directory"), t("usage.save_key"), t("usage.create_launch_script"), t("usage.load_verify")],
      value: launcher,
      notice: t("usage.this_content_includes_your_full_api_key_save_it_only"),
      copyLabel: t("usage.copy_launch_script"),
      sections: [
        {
          title: t("usage.prepare_configuration_directory"), file: t("usage.terminal"), description: t("usage.ensure_claude_code_is_installed_then_create_a_configuration_directory"),
          value: 'claude --version\nmkdir -p "$HOME/.config/claude-cpa"\nchmod 700 "$HOME/.config/claude-cpa"', copyLabel: t("usage.copy_command")
        },
        {
          title: t("usage.save_current_api_key"), file: "~/.config/claude-cpa/env", description: t("usage.create_this_file_and_paste_the_content_below_it_contains"),
          value: `${environment}=${shellQuote(apiKey)}\n`, hint: t("usage.after_saving_run_chmod_600_home_config_claude_cpa_env"), copyLabel: t("usage.copy_file_content")
        },
        {
          title: t("usage.create_the_claude_cpa_launch_script"), file: "~/.config/claude-cpa/claude-cpa.zsh", description: t("usage.this_function_affects_only_claude_cpa_it_uses_through_the", [model]),
          value: launcher, hint: t("usage.after_saving_run_chmod_600_home_config_claude_cpa_claude"), copyLabel: t("usage.copy_launch_script")
        },
        {
          title: t("usage.load_terminal_command"), file: "~/.zshrc", description: t("usage.append_this_line_to_the_file_so_every_new_terminal"),
          value: 'source "$HOME/.config/claude-cpa/claude-cpa.zsh"\n', copyLabel: t("usage.copy_loading_configuration")
        },
        {
          title: t("usage.load_verify"), file: t("usage.terminal"), description: t("usage.reload_the_configuration_check_that_the_function_exists_then_send"),
          value: 'source "$HOME/.zshrc"\ntype claude_cpa\nclaude_cpa -p \'Reply only: OK\' --output-format text\nclaude_cpa', copyLabel: t("usage.copy_verification_command")
        }
      ]
    };
  }
  const params = new URLSearchParams({
    resource: "provider",
    app: "codex",
    name: provider,
    endpoint: baseURL,
    apiKey: "PASTE_API_KEY_AFTER_IMPORT",
    homepage: `${origin}/usage/`,
    model,
    notes: t("usage.the_import_link_contains_no_api_key_paste_the_full", [siteConfig.product_name, currentGroup])
  });
  return {
    title: t("usage.finish_cc_switch_setup"),
    value: codex,
    copyLabel: t("usage.copy_import"),
    sections: [
      {
        title: t("usage.codex_configuration"),
        file: "~/.codex/config.toml",
        description: t("usage.copy_import_copies_the_full_configuration_below_before_opening_cc"),
        value: codex
      },
      {
        title: t("usage.migrate_previous_sessions"),
        file: "Codex Agent",
        description: t("usage.give_the_following_instruction_to_codex_agent_to_migrate_sessions"),
        value: historyPrompt,
        copyLabel: t("usage.copy_migration_instruction")
      }
    ],
    externalLink: `ccswitch://v1/import?${params.toString()}`,
  };
}

export async function copyAndImportConfig({
  value,
  externalLink,
  writeText,
  openLink
}: {
  value: string;
  externalLink: string;
  writeText: (value: string) => Promise<void>;
  openLink: (link: string) => void;
}) {
  try {
    await writeText(value);
  } catch (error) {
    const permissionDenied = error instanceof DOMException && error.name === "NotAllowedError";
    return {
      status: "copy_failed" as const,
      message: permissionDenied
        ? t("usage.copy_failed_clipboard_permission_denied_cc_switch_was_not_opened")
        : t("usage.copy_failed_cc_switch_was_not_opened_allow_clipboard_access")
    };
  }
  // Give the browser/OS one paint boundary to commit a legacy copy event
  // before handing focus to the external protocol handler.
  await settleClipboardWrite();
  try {
    openLink(externalLink);
    return { status: "opened" as const, message: t("usage.full_configuration_copied_opening_cc_switch") };
  } catch {
    return { status: "open_failed" as const, message: t("usage.configuration_copied_but_cc_switch_could_not_open_check_that") };
  }
}

export async function writeClipboardText(
  value: string,
  writers: {
    asyncWriter?: ((text: string) => Promise<void>) | null;
    legacyWriter?: (text: string) => boolean;
  } = {}
) {
  const asyncWriter = writers.asyncWriter === undefined
    ? (typeof navigator !== "undefined" && navigator.clipboard?.writeText
        ? navigator.clipboard.writeText.bind(navigator.clipboard)
        : null)
    : writers.asyncWriter;
  if (asyncWriter) {
    // A rejected Clipboard API call represents an explicit browser decision.
    // Do not bypass it with execCommand; the fallback is only for HTTP origins
    // where navigator.clipboard is unavailable altogether.
    await asyncWriter(value);
    return;
  }
  const legacyWriter = writers.legacyWriter ?? legacyClipboardWrite;
  if (legacyWriter(value)) return;
  throw new Error("clipboard unavailable");
}

function legacyClipboardWrite(value: string) {
  if (typeof document === "undefined" || !document.body || typeof document.execCommand !== "function") {
    return false;
  }
  const previouslyFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.readOnly = true;
  textarea.setAttribute("aria-hidden", "true");
  textarea.style.position = "fixed";
  textarea.style.inset = "0 auto auto 0";
  textarea.style.width = "2px";
  textarea.style.height = "2px";
  textarea.style.padding = "0";
  textarea.style.border = "0";
  textarea.style.opacity = "0.01";
  textarea.style.pointerEvents = "none";
  document.body.appendChild(textarea);
  textarea.focus({ preventScroll: true });
  textarea.select();
  textarea.setSelectionRange(0, value.length);
  let wroteCopyEvent = false;
  const onCopy = (event: ClipboardEvent) => {
    if (!event.clipboardData) return;
    event.preventDefault();
    event.clipboardData.clearData();
    event.clipboardData.setData("text/plain", value);
    wroteCopyEvent = event.clipboardData.getData("text/plain") === value;
  };
  document.addEventListener("copy", onCopy, { capture: true, once: true });
  try {
    // execCommand may return true without changing the system clipboard. Only
    // report success when the copy event accepted the exact payload as well.
    return document.execCommand("copy") && wroteCopyEvent;
  } finally {
    document.removeEventListener("copy", onCopy, true);
    textarea.remove();
    previouslyFocused?.focus({ preventScroll: true });
  }
}

function settleClipboardWrite() {
  return new Promise<void>((resolve) => {
    if (typeof requestAnimationFrame === "function") {
      requestAnimationFrame(() => resolve());
      return;
    }
    setTimeout(resolve, 0);
  });
}

function buildCodexConfig(provider: string, baseURL: string, model: string, apiKey: string) {
  return [
    'model_provider = "custom"',
    `model = ${tomlString(model)}`,
    'model_reasoning_effort = "xhigh"',
    'plan_mode_reasoning_effort = "max"',
    "",
    "[model_providers.custom]",
    `name = ${tomlString(provider)}`,
    `base_url = ${tomlString(baseURL)}`,
    'wire_api = "responses"',
    "requires_openai_auth = false",
    'http_headers = { "X-OpenAI-Actor-Authorization" = "local-proxy" }',
    `experimental_bearer_token = ${tomlString(apiKey)}`,
    ""
  ].join("\n");
}

function buildClaudeLauncher(environment: string, origin: string, model: string) {
  return [
    "claude_cpa() (",
    '  local env_file="${HOME}/.config/claude-cpa/env"',
    '  if [[ ! -r "$env_file" ]]; then print -u2 "claude_cpa: missing $env_file"; return 1; fi',
    '  source "$env_file"',
    `  if [[ -z "\${${environment}:-}" ]]; then print -u2 "claude_cpa: ${environment} is empty"; return 1; fi`,
    "  unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY",
    "  unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_FOUNDRY_API_KEY",
    `  export ANTHROPIC_AUTH_TOKEN="$${environment}"`,
    `  unset ${environment}`,
    `  export ANTHROPIC_BASE_URL=${shellQuote(origin)}`,
    `  export ANTHROPIC_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_SMALL_FAST_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_OPUS_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_SONNET_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_HAIKU_MODEL=${shellQuote(model)}`,
    `  export CLAUDE_CODE_SUBAGENT_MODEL=${shellQuote(model)}`,
    '  export CLAUDE_CODE_EFFORT_LEVEL="xhigh"',
    "  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
    "  export CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1",
    "  export CLAUDE_CODE_DISABLE_1M_CONTEXT=1",
    "  export ENABLE_CLAUDEAI_MCP_SERVERS=0",
    "  export API_TIMEOUT_MS=600000",
    "  export CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR=1",
    '  command claude --dangerously-skip-permissions --verbose --effort xhigh "$@"',
    ")",
    ""
  ].join("\n");
}

function publicBaseURL(configured: string, fallback: string) {
  try {
    const parsed = new URL(configured.trim().replace(/\/+$/, "") || fallback);
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) throw new Error("invalid URL");
    return parsed.href.replace(/\/+$/, "");
  } catch {
    return fallback.replace(/\/+$/, "");
  }
}

function tomlString(value: string) {
  return JSON.stringify(value);
}

function shellQuote(value: string) {
  return `'${String(value).replaceAll("'", `'"'"'`)}'`;
}
