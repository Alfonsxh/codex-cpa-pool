import { t } from "../../i18n";
export const recentUsageWindows = [
  { value: "3600", label: t("common.1h") },
  { value: "21600", label: t("common.6h") },
  { value: "today", label: t("common.today") },
  { value: "86400", label: t("common.24h") },
  { value: "604800", label: t("common.7d") },
  { value: "2592000", label: t("common.30d") }
] as const;

export function UsageTimeRangeControl<T extends string>({ value, options, onChange, label, className = "", onCustomSelect }: {
  value: T;
  options: ReadonlyArray<{ value: T; label: string; title?: string }>;
  onChange: (value: T) => void;
  label: string;
  className?: string;
  onCustomSelect?: () => void;
}) {
  return (
    <fieldset className={`overview-legacy-window-control usage-time-control ${className}`.trim()}>
      <legend>{t("common.time_range_2")}</legend>
      <div className="overview-legacy-window-segments usage-time-segments" role="group" aria-label={label}>
        {options.map((option) => (
          <button key={option.value} type="button" title={option.title} aria-pressed={value === option.value} onClick={() => onChange(option.value)}>
            {option.label}
          </button>
        ))}
        {onCustomSelect ? <button type="button" aria-pressed={value === "custom"} title={t("common.select_time_range")} onClick={onCustomSelect}>{t("common.custom")}</button> : null}
      </div>
    </fieldset>
  );
}
