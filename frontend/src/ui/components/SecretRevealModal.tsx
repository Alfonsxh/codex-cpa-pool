import { t } from "../../i18n";
import { Button, Input, Modal, Tooltip, type InputRef } from "antd";
import { useEffect, useId, useRef, useState } from "react";

import type { UserOneTimeKey } from "../../api/users";

export type SecretReveal = {
  kind?: "created" | "rotated" | "password-reset";
  message: string;
  keys: UserOneTimeKey[];
  password?: string;
  passwordUser?: string;
};

export function SecretRevealModal({ value, onClose }: {
  value: SecretReveal | null;
  onClose: () => void;
}) {
  if (!value) return null;

  const user = value.passwordUser || value.keys[0]?.user || "";
  const secrets = [
    ...(value.password ? [{ id: "password", label: t("common.initial_password"), value: value.password }] : []),
    ...value.keys.map((key, index) => ({
      id: `key-${index}`,
      label: value.keys.length > 1 ? `API Key ${index + 1}` : "API Key",
      value: key.key
    }))
  ];
  const kind = value.kind ?? (value.password ? (value.keys.length ? "created" : "password-reset") : "rotated");
  const resultText = {
    created: t("common.user_created"),
    rotated: t("common.api_key_updated"),
    "password-reset": t("common.password_reset")
  }[kind] ?? t("common.credentials_generated");
  const hint = value.password
    ? (value.keys.length ? t("common.save_the_api_key_the_password_must_be_changed_on") : t("common.the_password_must_be_changed_on_the_next_sign_in"))
    : t("common.save_the_generated_api_key");
  const allSecrets = [
    ...(user ? [t("common.user_2", [user])] : []),
    ...secrets.map((secret) => `${secret.label}：${secret.value}`)
  ].join("\n");

  return (
    <Modal
      className="legacy-secret-modal"
      title={(
        <div className="secret-dialog-title">
          <strong>{t("common.user_credentials")}</strong>
          <span aria-hidden="true">ONE-TIME SECRET</span>
        </div>
      )}
      open
      width={600}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      mask={{ closable: false }}
      footer={[
        <SecretCopyButton key="copy-all" text={allSecrets} label={t("common.copy_all")} />,
        <Button key="saved" type="primary" onClick={onClose}>{t("common.i_have_saved_it")}</Button>
      ]}
    >
      <div className="secret-user-summary">
        <span className="secret-user-result" role="status">{resultText}</span>
        {user ? <strong className="secret-user-email" title={user}>{user}</strong> : null}
      </div>
      <div className="secret-fields">
        {secrets.map((secret) => <SecretField key={secret.id} label={secret.label} value={secret.value} />)}
      </div>
      <p className="secret-save-hint" role="note">{hint}</p>
    </Modal>
  );
}

function SecretField({ label, value }: { label: string; value: string }) {
  const id = useId();
  const input = useRef<InputRef>(null);
  const hovered = useRef(false);
  const [shownValue, setShownValue] = useState<string | null>(null);
  const visible = shownValue === value;

  useEffect(() => {
    // Scope pointer-directed select-all to this mounted credential field. Hover alone
    // must not move focus, and keyboard-only selection keeps the native input path.
    const selectHovered = (event: KeyboardEvent) => {
      if (!hovered.current || event.defaultPrevented || event.isComposing || event.altKey
        || !(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "a") return;
      event.preventDefault();
      event.stopPropagation();
      input.current?.focus({ preventScroll: true });
      input.current?.select();
    };
    document.addEventListener("keydown", selectHovered, true);
    return () => document.removeEventListener("keydown", selectHovered, true);
  }, []);

  return (
    <div className="secret-field" role="group" aria-label={label}>
      <label className="secret-field-label" htmlFor={id}>{label}</label>
      <div
        className="secret-field-control"
        onMouseEnter={() => { hovered.current = true; }}
        onMouseLeave={() => { hovered.current = false; }}
      >
        <Input.Password
          ref={input}
          id={id}
          className="secret-field-input"
          value={value}
          readOnly
          autoComplete="off"
          spellCheck={false}
          visibilityToggle={{ visible, onVisibleChange: (next) => setShownValue(next ? value : null) }}
          onKeyDown={(event) => {
            if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "a") {
              event.preventDefault();
              event.stopPropagation();
              event.currentTarget.select();
            }
          }}
        />
      </div>
      <SecretCopyButton text={value} label={t("common.copy")} accessibleLabel={t("common.copy_2", [label])} />
    </div>
  );
}

function SecretCopyButton({ text, label, accessibleLabel = label }: {
  text: string;
  label: string;
  accessibleLabel?: string;
}) {
  const [feedback, setFeedback] = useState<{ text: string; status: "copying" | "copied" | "failed" } | null>(null);
  const request = useRef(0);
  const timer = useRef<number | undefined>(undefined);
  const status = feedback?.text === text ? feedback.status : undefined;

  useEffect(() => () => {
    request.current += 1;
    window.clearTimeout(timer.current);
  }, []);

  const copy = async () => {
    const currentRequest = ++request.current;
    window.clearTimeout(timer.current);
    setFeedback({ text, status: "copying" });
    let nextStatus: "copied" | "failed" = "copied";
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(text);
    } catch {
      nextStatus = "failed";
    }
    if (currentRequest !== request.current) return;
    setFeedback({ text, status: nextStatus });
    timer.current = window.setTimeout(() => {
      if (currentRequest === request.current) setFeedback(null);
    }, 2_000);
  };
  const feedbackLabel = status === "copied" ? t("common.copied_2") : status === "failed" ? t("common.copy_failed") : "";

  return (
    <Tooltip title={status === "failed" ? t("common.reveal_the_credentials_and_copy_them_manually") : t("common.copy_full_content")}>
      <Button
        className="secret-copy-action"
        size="small"
        aria-label={feedbackLabel ? `${accessibleLabel}，${feedbackLabel}` : accessibleLabel}
        loading={status === "copying"}
        danger={status === "failed"}
        onClick={() => void copy()}
      ><span aria-live="polite">{feedbackLabel || label}</span></Button>
    </Tooltip>
  );
}
