import { t } from "../../i18n";
import { useSiteTimezone, siteDateTimeFormat, getSiteTimezone, formatSiteTimestamp } from "../site-time";
import { DatePicker, Modal } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import timezonePlugin from "dayjs/plugin/timezone";
import utc from "dayjs/plugin/utc";
import { useEffect, useMemo, useState } from "react";

dayjs.extend(utc);
dayjs.extend(timezonePlugin);

export type CustomUsageRange = {
  startAt: number;
  endAt: number;
};

type PickerRange = [Dayjs | null, Dayjs | null] | null;

export function CustomUsageRangeModal({
  open,
  title,
  range,
  timezone,
  onCancel,
  onApply
}: {
  open: boolean;
  title: string;
  range: CustomUsageRange | null;
  timezone?: string;
  onCancel: () => void;
  onApply: (range: CustomUsageRange) => void;
}) {
  useSiteTimezone();
  const zone = normalizeTimezone(timezone);
  const [draft, setDraft] = useState<PickerRange>(() => createPickerRange(range, zone));
  const [error, setError] = useState("");
  const nowInZone = dayjs().tz(zone);

  useEffect(() => {
    if (!open) return;
    setDraft(createPickerRange(range, zone));
    setError("");
  }, [open, range?.endAt, range?.startAt, zone]);

  const timestamps = useMemo(() => pickerTimestamps(draft, zone), [draft, zone]);
  const preview = timestamps
    ? formatFullCustomUsageRange(timestamps, zone)
    : t("common.select_a_start_and_end_time");

  const submit = () => {
    if (!timestamps) {
      setError(t("common.select_a_valid_start_and_end_time"));
      return;
    }
    if (timestamps.startAt >= timestamps.endAt) {
      setError(t("common.start_time_must_precede_end_time"));
      return;
    }
    if (timestamps.endAt > Math.floor(Date.now() / 1000)) {
      setError(t("common.end_time_cannot_be_in_the_future"));
      return;
    }
    setError("");
    onApply(timestamps);
  };

  return (
    <Modal
      className="custom-usage-range-modal"
      title={<div className="custom-usage-range-title"><strong>{title}</strong></div>}
      open={open}
      width={720}
      centered
      transitionName=""
      maskTransitionName=""
      okText={t("common.apply_range")}
      cancelText={t("common.cancel")}
      onCancel={() => {
        setError("");
        onCancel();
      }}
      onOk={submit}
      destroyOnHidden
    >
      <div className="custom-usage-range-body">
        <DatePicker.RangePicker
          className="custom-usage-range-picker"
          aria-label={t("common.time_range_2")}
          value={draft}
          format="YYYY/MM/DD HH:mm:ss"
          showTime={{ format: "HH:mm:ss" }}
          allowClear={false}
          inputReadOnly
          disabledDate={(current) => current.tz(zone, true).startOf("day").isAfter(nowInZone.endOf("day"))}
          onCalendarChange={(value) => {
            setDraft(value ? [value[0], value[1]] : null);
            setError("");
          }}
          onChange={(value) => {
            setDraft(value ? [value[0], value[1]] : null);
            setError("");
          }}
          presets={[
            { label: t("common.last_1_hour"), value: [nowInZone.subtract(1, "hour"), nowInZone] },
            { label: t("common.last_24_hours"), value: [nowInZone.subtract(24, "hour"), nowInZone] },
            { label: t("common.last_7_days"), value: [nowInZone.subtract(7, "day"), nowInZone] }
          ]}
        />
        <div className="custom-range-selection" aria-live="polite">
          <span>{t("common.selected_range")}</span>
          <strong>{preview}</strong>
        </div>
        <p className="custom-range-error" role="alert">{error}</p>
      </div>
    </Modal>
  );
}

function createPickerRange(range: CustomUsageRange | null, zone: string): PickerRange {
  const now = Math.floor(Date.now() / 60_000) * 60;
  const startAt = range?.startAt ?? now - 24 * 60 * 60;
  const endAt = range?.endAt ?? now;
  return [dayjs.unix(startAt).tz(zone), dayjs.unix(endAt).tz(zone)];
}

function pickerTimestamps(range: PickerRange, zone: string): CustomUsageRange | null {
  if (!range?.[0]?.isValid() || !range?.[1]?.isValid()) return null;
  return {
    startAt: range[0].tz(zone, true).unix(),
    endAt: range[1].tz(zone, true).unix()
  };
}

function normalizeTimezone(value?: string) {
  const candidate = value?.trim() || getSiteTimezone();
  try {
    siteDateTimeFormat("en-US", { timeZone: candidate }).format();
    return candidate;
  } catch {
    return "UTC";
  }
}

export function formatCustomUsageRange(range: CustomUsageRange | null, timezone?: string) {
  if (!range?.startAt || !range?.endAt) return t("common.select_time_range");
  return formatFullCustomUsageRange(range, timezone);
}

export function formatFullCustomUsageRange(range: CustomUsageRange, timezone?: string) {
  const zone = normalizeTimezone(timezone);
  return `${formatSiteTimestamp(range.startAt, zone)} → ${formatSiteTimestamp(range.endAt, zone)}`;
}
