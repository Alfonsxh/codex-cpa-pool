import { t } from "../../i18n";
import { Select } from "antd";
import { useMemo } from "react";

const commonZones: Record<string, string> = {
  "Asia/Shanghai": t("common.beijing"),
  UTC: t("common.coordinated_universal_time"),
  "Asia/Hong_Kong": t("common.hong_kong"),
  "Asia/Tokyo": t("common.tokyo"),
  "Asia/Singapore": t("common.singapore"),
  "Europe/London": t("common.london"),
  "America/New_York": t("common.new_york"),
  "America/Los_Angeles": t("common.los_angeles")
};

export function timezoneOptions(value: string) {
  const supported = (Intl as typeof Intl & { supportedValuesOf?: (key: "timeZone") => string[] })
    .supportedValuesOf?.("timeZone") ?? [];
  const zones = [...new Set([...Object.keys(commonZones), ...supported, value].filter(Boolean))];
  const now = new Date();
  return zones.map((zone) => {
    const offset = new Intl.DateTimeFormat("en", { timeZone: zone, timeZoneName: "longOffset" })
      .formatToParts(now).find((part) => part.type === "timeZoneName")?.value.replace("GMT", "UTC") ?? "";
    return { value: zone, label: `${commonZones[zone] ? `${commonZones[zone]} · ` : ""}${zone} (${offset})` };
  });
}

export function TimezoneSelect({ id, value, onChange, disabled = false, ariaInvalid, ariaDescribedBy }: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  ariaInvalid?: boolean;
  ariaDescribedBy?: string;
}) {
  const options = useMemo(() => timezoneOptions(value), [value]);
  return <Select className="timezone-select" id={id} aria-label={t("common.system_timezone")} aria-invalid={ariaInvalid} aria-describedby={ariaDescribedBy} value={value} options={options} onChange={onChange}
    disabled={disabled} showSearch={{ optionFilterProp: "label" }} placeholder={t("common.search_city_or_timezone")}
    style={{ width: "100%", minWidth: 0 }} popupMatchSelectWidth />;
}
