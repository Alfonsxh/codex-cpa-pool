import "../../i18n/admin";
import { t } from "../../i18n";
import { DownloadOutlined, FileExcelOutlined } from "@ant-design/icons";
import { Alert, Button, DatePicker, Modal } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import utc from "dayjs/plugin/utc";
import timezone from "dayjs/plugin/timezone";
import { useEffect, useRef, useState } from "react";

import { exportWeeklyUsage } from "../../api/overview";
import { useSiteTimezone } from "../site-time";

dayjs.extend(utc);
dayjs.extend(timezone);

function monday(date: Dayjs) {
  return date.startOf("day").subtract((date.day() + 6) % 7, "day");
}

export function WeeklyUsageExport({ onDownloaded }: { onDownloaded: () => void }) {
  const zone = useSiteTimezone();
  const [open, setOpen] = useState(false);
  const [week, setWeek] = useState<Dayjs | null>(null);
  const [withUnits, setWithUnits] = useState(true);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => { request.current?.abort(); request.current = null; }, []);

  // Calendar dates are intentionally zone-free in the picker. The server alone
  // translates the selected Monday to timezone-aware interval boundaries.
  const today = dayjs(dayjs().tz(zone).format("YYYY-MM-DD"));
  const currentWeek = monday(today);
  const previousWeek = currentWeek.subtract(7, "day");
  const end = week?.add(6, "day").endOf("day");
  const partial = week?.isSame(currentWeek, "day") ?? false;
  const valid = Boolean(week && !week.isAfter(currentWeek, "day"));

  function selectWeek(value: Dayjs | null) {
    setWeek(value ? monday(value) : null);
    setError("");
  }

  function close() {
    request.current?.abort();
    request.current = null;
    setPending(false);
    setOpen(false);
  }

  async function download() {
    if (!week || !valid || request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    setError("");
    let timedOut = false;
    const timer = window.setTimeout(() => { timedOut = true; controller.abort(); }, 30_000);
    try {
      const { blob, filename } = await exportWeeklyUsage(week.format("YYYY-MM-DD"), controller.signal, withUnits);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;
      document.body.appendChild(link);
      try { link.click(); } finally {
        link.remove();
        window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
      }
      setOpen(false);
      onDownloaded();
    } catch (cause) {
      if (request.current !== controller) return;
      if (timedOut) setError(t("common.weekly_report_download_timed_out_please_try_again_later"));
      else if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : t("common.unable_to_export_the_weekly_report_please_try_again_later"));
    } finally {
      window.clearTimeout(timer);
      if (request.current === controller) {
        request.current = null;
        setPending(false);
      }
    }
  }

  return <>
    <Button icon={<DownloadOutlined />} onClick={() => {
      selectWeek(previousWeek);
      setOpen(true);
    }}>{t("common.export")}</Button>
    <Modal className="weekly-usage-export-modal" open={open} centered width={560}
      title={<div className="weekly-usage-export-title">
        <span className="weekly-usage-export-icon" aria-hidden="true"><FileExcelOutlined /></span>
        <strong>{t("common.export_token_weekly_report")}</strong>
      </div>}
      okText={pending ? t("common.generating") : t("common.download_xlsx")} cancelText={pending ? t("common.cancel_generation") : t("common.cancel")}
      confirmLoading={pending} okButtonProps={{ disabled: !valid, icon: <DownloadOutlined /> }}
      footer={(_, { OkBtn, CancelBtn }) => <div className="weekly-usage-export-footer">
        {pending ? <span role="status">{t("common.preparing_report_data")}</span> : null}
        <div className="weekly-usage-export-actions"><CancelBtn /><OkBtn /></div>
      </div>}
      onOk={() => void download()} onCancel={close} destroyOnHidden>
      <div className="weekly-usage-export">
        <section className="weekly-usage-export-period" aria-labelledby="weekly-report-period-title">
          <div className="weekly-usage-export-heading">
            <h3 id="weekly-report-period-title">{t("common.reporting_period")}</h3>
            <div className="weekly-usage-export-shortcuts" role="group" aria-label={t("common.select_reporting_week")}>
              <button type="button" disabled={pending} aria-pressed={week?.isSame(previousWeek, "day") ?? false}
                onClick={() => selectWeek(previousWeek)}>{t("common.last_week")}</button>
              <button type="button" disabled={pending} aria-pressed={partial}
                onClick={() => selectWeek(currentWeek)}>{t("common.this_week")}</button>
            </div>
          </div>
          <div className="weekly-usage-export-dates">
            <div className="weekly-usage-export-date-field">
              <label htmlFor="weekly-report-start">{t("common.started")}</label>
              <DatePicker className="weekly-usage-export-picker" id="weekly-report-start" value={week}
                allowClear={false} inputReadOnly disabled={pending} aria-describedby="weekly-report-period"
                format="YYYY-MM-DD HH:mm:ss" placeholder={t("common.select_start_time")} minDate={dayjs("1970-01-01")}
                disabledDate={(date) => date.isAfter(today, "day")}
                onChange={selectWeek} />
            </div>
            <div className="weekly-usage-export-date-field">
              <label htmlFor="weekly-report-end">{t("common.end_time")}</label>
              <DatePicker className="weekly-usage-export-picker" id="weekly-report-end" value={end ?? null}
                allowClear={false} inputReadOnly disabled={pending} aria-describedby="weekly-report-period"
                format="YYYY-MM-DD HH:mm:ss" placeholder={t("common.select_end_time")} minDate={dayjs("1970-01-01")}
                disabledDate={(date) => monday(date).isAfter(currentWeek, "day")}
                onChange={selectWeek} />
            </div>
          </div>
          <span className="sr-only" id="weekly-report-period" aria-live="polite">
            {week?.format("YYYY-MM-DD HH:mm:ss")} {t("common.to")} {end?.format("YYYY-MM-DD HH:mm:ss")}
          </span>
          {partial ? <span className="weekly-usage-export-partial">{t("common.week_in_progress")}</span> : null}
        </section>
        <section className="weekly-usage-export-content" aria-labelledby="weekly-report-content">
          <div className="weekly-usage-export-heading">
            <h3 id="weekly-report-content">{t("common.report_content")}</h3><span>{t("common.5_worksheets")}</span>
          </div>
          <ul className="weekly-usage-export-sheets">
            {[t("common.usage_overview"), t("common.team_statistics"), t("common.account_details"), t("common.user_usage_details"), t("common.daily_trends")].map((sheet) => <li key={sheet}>{sheet}</li>)}
          </ul>
          <div className="weekly-usage-export-format">
            <div className="weekly-usage-export-heading">
              <h3 id="weekly-report-format">{t("common.token_number_format")}</h3>
              <div className="weekly-usage-export-shortcuts" role="group" aria-labelledby="weekly-report-format">
                <button type="button" disabled={pending} aria-pressed={withUnits}
                  onClick={() => { setWithUnits(true); setError(""); }}>{t("common.with_units")}</button>
                <button type="button" disabled={pending} aria-pressed={!withUnits}
                  onClick={() => { setWithUnits(false); setError(""); }}>{t("common.without_units")}</button>
              </div>
            </div>
            <p aria-live="polite">{t("common.example")}<strong>{withUnits ? "1.25 M" : "1,250,000"}</strong></p>
          </div>
        </section>
        <Alert className="weekly-usage-export-method" type="info" showIcon title={t("common.reporting_rules")}
          description={<>
            <p>{t("common.calendar_weeks_start_and_end_dates_are_linked_automatically")}</p>
            <p>{t("common.ranked_by_raw_tokens_with_weighted_usage_included_teams_use")}</p>
            <p>{partial ? t("common.this_week_s_usage_up_to_generation_time_compared_with") : t("common.compare_the_selected_week_with_the_previous_complete_calendar_week")}</p>
          </>} />
        {error ? <Alert type="error" title={error} showIcon /> : null}
      </div>
    </Modal>
  </>;
}
