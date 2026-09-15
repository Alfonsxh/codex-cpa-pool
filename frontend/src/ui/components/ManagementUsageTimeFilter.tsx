import { t } from "../../i18n";
import { UsageTimeRangeControl } from "./UsageTimeRangeControl";

export function ManagementUsageTimeFilter<T extends string>({
  value, options, onChange, onCustomSelect, label, start, end, updating
}: {
  value: T;
  options: ReadonlyArray<{ value: T; label: string }>;
  onChange: (value: T) => void;
  onCustomSelect: () => void;
  label: string;
  start: string;
  end: string;
  updating: boolean;
}) {
  return <div className="overview-token-window-row user-time-filter">
    <UsageTimeRangeControl value={value} options={options} onChange={onChange} onCustomSelect={onCustomSelect} label={t("common.time_range", [label])} />
    <div className="overview-token-window-boundaries" aria-label={t("common.time_boundaries", [label])} aria-live="polite" aria-busy={updating}>
      <div className="overview-token-window-value"><small>{t("common.start_time")}</small><strong>{start}</strong></div>
      <div className="overview-token-window-value"><small>{t("common.end_time")}</small><strong>{end}</strong></div>
    </div>
  </div>;
}
