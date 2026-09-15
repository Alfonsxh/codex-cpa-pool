import { t } from "../../i18n";
import { useState, type FocusEventHandler, type Ref } from "react";

export function LegacyPasswordInput({
  id,
  value,
  placeholder,
  minLength,
  maxLength,
  autoComplete = "new-password",
  disabled = false,
  name,
  ariaLabel,
  ariaInvalid,
  ariaDescribedBy,
  inputRef,
  onBlur,
  onValueChange
}: {
  id: string;
  value: string;
  placeholder?: string;
  minLength?: number;
  maxLength?: number;
  autoComplete?: string;
  disabled?: boolean;
  name?: string;
  ariaLabel?: string;
  ariaInvalid?: boolean;
  ariaDescribedBy?: string;
  inputRef?: Ref<HTMLInputElement>;
  onBlur?: FocusEventHandler<HTMLInputElement>;
  onValueChange: (value: string) => void;
}) {
  const [visible, setVisible] = useState(false);
  return (
    <span className="password-input">
      <input
        ref={inputRef}
        id={id}
        name={name}
        aria-label={ariaLabel}
        aria-invalid={ariaInvalid}
        aria-describedby={ariaDescribedBy}
        type={visible ? "text" : "password"}
        value={value}
        placeholder={placeholder}
        minLength={minLength}
        maxLength={maxLength}
        autoComplete={autoComplete}
        disabled={disabled}
        onBlur={onBlur}
        onChange={(event) => onValueChange(event.target.value)}
      />
      <button
        className="password-visibility-toggle"
        type="button"
        tabIndex={-1}
        disabled={disabled}
        aria-controls={id}
        aria-label={visible ? t("common.hide_password") : t("common.show_password")}
        aria-pressed={visible}
        title={visible ? t("common.hide_password") : t("common.show_password")}
        onClick={() => setVisible((current) => !current)}
      >
        <svg className="password-eye-show" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z" /><circle cx="12" cy="12" r="2.75" /></svg>
        <svg className="password-eye-hide" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M3 3l18 18M10.6 6.2A10.7 10.7 0 0 1 12 6c6 0 9.5 6 9.5 6a17.6 17.6 0 0 1-2.5 3.2M6.2 6.2C3.8 8 2.5 12 2.5 12s3.5 6 9.5 6a9.9 9.9 0 0 0 3.2-.5M9.9 9.9a3 3 0 0 0 4.2 4.2" /></svg>
      </button>
    </span>
  );
}
