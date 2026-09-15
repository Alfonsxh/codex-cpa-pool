import { t, getIntlLocale } from "../../i18n";
import { useSiteTimezone, formatSiteTimestamp } from "../site-time";
import * as echarts from "echarts/core";
import { LineChart, type LineSeriesOption } from "echarts/charts";
import {
  AriaComponent,
  GridComponent,
  MarkLineComponent,
  MarkPointComponent,
  TooltipComponent,
  type AriaComponentOption,
  type GridComponentOption,
  type TooltipComponentOption
} from "echarts/components";
import { SVGRenderer } from "echarts/renderers";
import type { ComposeOption, EChartsType } from "echarts/core";
import type { DefaultLabelFormatterCallbackParams as CallbackDataParams } from "echarts";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import type { TokenSeries } from "../../api/overview";
import { formatTokens } from "../formatters";
import { useTheme } from "../ThemeProvider";
import { OverviewTokenValue } from "./OverviewTokenValue";

echarts.use([LineChart, GridComponent, MarkLineComponent, MarkPointComponent, TooltipComponent, AriaComponent, SVGRenderer]);

type UsageChartOption = ComposeOption<
  LineSeriesOption | GridComponentOption | TooltipComponentOption | AriaComponentOption
>;

export const usageChartColors = [
  "#6374d8", "#4b8ccf", "#c58a34", "#9070c5", "#5263aa",
  "#c45757", "#d16f4f", "#b96894", "#447a9d", "#8b6d48"
] as const;

export type UsageChartProps = {
  buckets: number[];
  series: TokenSeries[];
  summary?: boolean;
  valueLabel: string;
  timezone?: string;
  ariaLabel: string;
  footer?: ReactNode;
};

export function UsageChart({ buckets, series, summary = false, valueLabel, timezone, ariaLabel, footer }: UsageChartProps) {
  const siteTimezone = useSiteTimezone();
  timezone = timezone || siteTimezone;
  const { theme } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<EChartsType | null>(null);
  const [selectedPoint, setSelectedPoint] = useState<{ buckets: number[]; index: number } | null>(null);
  const selectedIndex = selectedPoint?.buckets === buckets ? selectedPoint.index : null;
  const pointIndex = selectedIndex ?? Math.max(0, buckets.length - 1);
  const height = 500;
  const summaryMetrics = useMemo(() => summary ? summarizeUsageChart(buckets, series) : null, [buckets, series, summary]);
  const summaryColumns = summaryMetrics ? [
    ["point", selectedIndex === null ? t("common.latest_interval") : t("common.selected_interval"), summaryMetrics.values[pointIndex] ?? 0],
    ["current", t("common.current"), summaryMetrics.current],
    ["total", t("common.range_total"), summaryMetrics.total],
    ["average", t("common.average"), summaryMetrics.average],
    ["maximum", t("common.maximum"), summaryMetrics.maximum]
  ] as const : [];
  const labels = useMemo(
    () => buckets.map((timestamp) => formatSiteTimestamp(timestamp, timezone)),
    [buckets, timezone]
  );

  useEffect(() => {
    const container = containerRef.current;
    if (!container || !buckets.length || !series.length) return;

    const chart = echarts.init(container, undefined, { renderer: "svg" });
    chartRef.current = chart;
    const dark = theme === "dark";
    const axisColor = dark ? "#7f8ba3" : "#8b95a7";
    const gridColor = dark ? "#273247" : "#e3e8f1";
    const chartWidth = Math.round(container.getBoundingClientRect().width) || 1_000;
    const recordedMaximum = Math.max(1, ...series.flatMap((item) => item.values
      .slice(0, buckets.length).map((value) => Number.isFinite(value) ? value : 0)));
    const maximum = recordedMaximum * 1.1;
    const yAxisSplitNumber = 10;
    const roughInterval = maximum / yAxisSplitNumber;
    const intervalMagnitude = 10 ** Math.floor(Math.log10(roughInterval));
    const yAxisInterval = Math.max(1, intervalMagnitude * (
      [1, 2, 5, 10].find((step) => step * intervalMagnitude >= roughInterval) ?? 10
    ));
    const peak = summary ? usageChartPeak(series[0]?.values ?? []) : null;
    const peakColor = valueLabel === t("common.weighted_2") ? "#d18b41" : "#6374d8";
    const xLabelIndexes = new Set(Array.from({ length: 11 }, (_, index) => (
      Math.round(Math.max(0, labels.length - 1) * index / 10)
    )));
    const option: UsageChartOption = {
      animation: false,
      color: [...usageChartColors],
      aria: { enabled: true, description: ariaLabel },
      grid: {
        // Frozen v1 renders non-summary SVGs in a fixed 1000px viewBox and
        // stretches them to the panel width. Scale its plot margins too.
        left: summary ? chartWidth <= 520 ? 66 : 72 : chartWidth * 82 / 1_000,
        right: summary ? 16 : chartWidth * 20 / 1_000,
        top: 24,
        bottom: 62,
        containLabel: false
      },
      tooltip: {
        trigger: "axis",
        showContent: !summary,
        triggerOn: "mousemove|click|mousewheel",
        confine: true,
        enterable: false,
        renderMode: "html",
        className: "usage-chart-echarts-tooltip",
        backgroundColor: "#171d2b",
        borderColor: "#39455e",
        borderWidth: 1,
        padding: 0,
        extraCssText: "max-height:none;overflow:hidden;border-radius:9px;box-shadow:0 12px 28px rgb(8 12 22 / 28%);",
        axisPointer: {
          type: "line",
          lineStyle: { color: axisColor, type: "dashed", width: 1 }
        },
        formatter: (parameters) => renderUsageTooltip(parameters, buckets, valueLabel, timezone)
      },
      xAxis: {
        type: "category",
        boundaryGap: false,
        data: labels,
        axisLine: { lineStyle: { color: gridColor } },
        axisTick: { show: false },
        axisLabel: {
          color: axisColor,
          fontFamily: "SFMono-Regular, Consolas, Liberation Mono, monospace",
          fontSize: 11,
          lineHeight: 15,
          margin: 22,
          formatter: (label: string) => label.replace(" ", "\n"),
          interval: (index: number) => xLabelIndexes.has(index),
          hideOverlap: true
        }
      },
      yAxis: {
        type: "value",
        min: 0,
        max: maximum,
        interval: yAxisInterval,
        minInterval: 1,
        splitNumber: yAxisSplitNumber,
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: axisColor,
          fontFamily: "SFMono-Regular, Consolas, Liberation Mono, monospace",
          fontSize: 11,
          margin: 12,
          showMaxLabel: Number.isInteger(maximum / yAxisInterval),
          formatter: formatAxisTokens
        },
        splitLine: { lineStyle: { color: gridColor, width: 1 } }
      },
      series: series.map((item, index): LineSeriesOption => ({
        name: item.name,
        type: "line",
        data: item.values,
        showSymbol: summary,
        symbol: "circle",
        symbolSize: summary ? 6 : 4,
        sampling: "lttb",
        smooth: false,
        connectNulls: false,
        emphasis: { focus: "series" },
        lineStyle: { width: summary ? 2.5 : 2, color: usageChartColors[index % usageChartColors.length] },
        itemStyle: {
          color: dark ? "#151b28" : "#ffffff",
          borderColor: usageChartColors[index % usageChartColors.length],
          borderWidth: 2
        },
        areaStyle: summary
          ? { color: dark ? "#262d51" : "#eef0ff", opacity: 0.62 }
          : undefined,
        markLine: peak && index === 0 ? {
          silent: true,
          symbol: "none",
          lineStyle: { color: peakColor, type: "dashed", width: 1.5, opacity: 0.8 },
          label: { show: false },
          data: [{ yAxis: peak.value }]
        } : undefined,
        markPoint: peak && index === 0 ? {
          silent: true,
          symbol: "circle",
          symbolSize: 12,
          itemStyle: { color: peakColor, borderColor: dark ? "#151b28" : "#ffffff", borderWidth: 2 },
          label: {
            show: true,
            position: "top",
            distance: 12,
            align: peak.index <= (buckets.length - 1) * 0.15 ? "left"
              : peak.index >= (buckets.length - 1) * 0.85 ? "right" : "center",
            formatter: t("common.peak_2", [formatTokens(peak.value)]),
            color: dark ? "#edf1fb" : "#293348",
            fontSize: 12,
            fontWeight: 650,
            backgroundColor: dark ? "#151b28" : "#ffffff",
            borderColor: peakColor,
            borderWidth: 1,
            borderRadius: 4,
            padding: [4, 8]
          },
          data: [{ name: t("common.peak"), coord: [peak.index, peak.value], value: peak.value }]
        } : undefined
      }))
    };
    chart.setOption(option, { notMerge: true });

    if (summary) {
      chart.on("updateAxisPointer", (event) => {
        const pointer = event as { axesInfo?: Array<{ value?: number | string }> };
        const index = Number(pointer.axesInfo?.[0]?.value);
        if (Number.isInteger(index) && index >= 0 && index < buckets.length) {
          setSelectedPoint((previous) => previous?.buckets === buckets && previous.index === index
            ? previous : { buckets, index });
          container.setAttribute("data-active-index", String(index));
        }
      });
      chart.getZr().on("globalout", () => setSelectedPoint(null));
    }

    const resizeObserver = new ResizeObserver(() => chart.resize());
    resizeObserver.observe(container);
    return () => {
      resizeObserver.disconnect();
      chart.dispose();
      if (chartRef.current === chart) chartRef.current = null;
    };
  }, [ariaLabel, buckets, labels, series, summary, theme, timezone, valueLabel]);

  const showBucket = (index: number) => {
    const chart = chartRef.current;
    if (!chart || !buckets.length) return;
    const bounded = Math.max(0, Math.min(buckets.length - 1, index));
    if (summary) setSelectedPoint({ buckets, index: bounded });
    chart.dispatchAction({ type: "showTip", seriesIndex: 0, dataIndex: bounded });
    containerRef.current?.setAttribute("data-active-index", String(bounded));
  };

  return (
    <>
      <div
        className={`overview-legacy-chart${summary ? " summary" : ""}`}
        role="img"
        aria-label={t("common.hover_or_use_the_left_and_right_arrow_keys_to", [ariaLabel])}
        tabIndex={0}
        onFocus={() => showBucket(buckets.length - 1)}
        onBlur={() => {
          chartRef.current?.dispatchAction({ type: "hideTip" });
          setSelectedPoint(null);
        }}
        onKeyDown={(event) => {
          if (!buckets.length || !["ArrowLeft", "ArrowRight", "Home", "End", "Escape"].includes(event.key)) return;
          event.preventDefault();
          if (event.key === "Escape") {
            chartRef.current?.dispatchAction({ type: "hideTip" });
            setSelectedPoint(null);
            return;
          }
          const current = summary ? pointIndex : Number(containerRef.current?.getAttribute("data-active-index") ?? buckets.length - 1);
          const next = event.key === "Home"
            ? 0
            : event.key === "End"
              ? buckets.length - 1
              : current + (event.key === "ArrowLeft" ? -1 : 1);
          showBucket(next);
        }}
      >
        <div ref={containerRef} className="usage-chart-canvas" style={{ height }} aria-hidden="true" />
      </div>
      {footer}
      {summary && summaryMetrics ? (
        <section className="overview-chart-summary" aria-label={t("common.all_accounts_summary")}>
          <table className="overview-chart-summary-table" aria-label={t("common.all_accounts_token_statistics")}>
            <colgroup>
              <col className="overview-chart-summary-time-column" />
              <col className="overview-chart-summary-mode-column" />
              <col span={5} />
            </colgroup>
            <thead>
              <tr>
                <th scope="col" className="overview-chart-summary-time">{t("common.time")}</th>
                <th scope="col" className="overview-chart-summary-mode">{t("common.token_metric")}</th>
                {summaryColumns.map(([key, label]) => (
                  <th scope="col" className="overview-token-number-cell" data-metric-header={key} key={key}>{label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              <tr>
                <td className="overview-chart-summary-time">
                  <time dateTime={buckets[pointIndex] ? new Date(buckets[pointIndex] * 1000).toISOString() : undefined}>
                    {formatSiteTimestamp(buckets[pointIndex] ?? 0, timezone)}
                  </time>
                </td>
                <td className="overview-chart-summary-mode">
                  <span className={`overview-chart-mode-tag ${valueLabel === t("common.weighted_2") ? "weighted" : "unweighted"}`}><i aria-hidden="true" />{valueLabel}</span>
                </td>
                {summaryColumns.map(([key, , value]) => (
                  <td className="overview-chart-summary-token overview-token-number-cell" data-metric={key} key={key}>
                    <OverviewTokenValue value={value} />
                  </td>
                ))}
              </tr>
            </tbody>
          </table>
        </section>
      ) : null}
    </>
  );
}

// Read only the values already selected for the plotted weighted/unweighted series.
// Separate API summary fields must not introduce a different Token basis here.
export function summarizeUsageChart(buckets: number[], series: Pick<TokenSeries, "values">[]) {
  const values = buckets.map((_, index) => series.reduce((sum, item) => {
    const value = item.values[index];
    return sum + (Number.isFinite(value) ? value : 0);
  }, 0));
  const total = values.reduce((sum, value) => sum + value, 0);
  return {
    values,
    current: values.at(-1) ?? 0,
    total,
    average: values.length ? Math.round(total / values.length) : 0,
    maximum: Math.max(0, ...values)
  };
}

export function usageChartPeak(values: number[]) {
  let peak: { index: number; value: number } | null = null;
  for (let index = 0; index < values.length; index += 1) {
    const value = values[index];
    if (Number.isFinite(value) && (!peak || value > peak.value)) peak = { index, value };
  }
  return peak;
}

export function tooltipRows(parameters: CallbackDataParams | CallbackDataParams[]) {
  const values = Array.isArray(parameters) ? parameters : [parameters];
  return values
    .map((item) => ({
      name: String(item.seriesName ?? ""),
      value: chartValue(item.value),
      color: usageChartColors[Number(item.seriesIndex ?? 0) % usageChartColors.length],
      seriesIndex: Number(item.seriesIndex ?? 0)
    }))
    .sort((left, right) => right.value - left.value || left.name.localeCompare(right.name, getIntlLocale(), { numeric: true }))
    .slice(0, 10);
}

export function renderUsageTooltip(
  parameters: CallbackDataParams | CallbackDataParams[],
  buckets: number[],
  valueLabel = "",
  timezone?: string
) {
  const values = Array.isArray(parameters) ? parameters : [parameters];
  const dataIndex = Number(values[0]?.dataIndex ?? 0);
  const timestamp = formatSiteTimestamp(buckets[dataIndex] ?? 0, timezone);
  const rows = tooltipRows(values).map((item) => (
    `<span><i style="background:${escapeAttribute(item.color)}"></i>`
    + `<b title="${escapeAttribute(item.name)}">${escapeHtml(item.name)}</b>`
    + `<em>${escapeHtml(formatTokens(item.value))}</em></span>`
  )).join("");
  const escapedValueLabel = escapeHtml(valueLabel);
  const modeTone = valueLabel === t("common.weighted_2") ? "weighted" : "unweighted";
  const heading = escapedValueLabel
    ? `<strong><span>${escapeHtml(timestamp)}</span><small class="overview-chart-mode-tag ${modeTone}"><i></i>${escapedValueLabel}</small></strong>`
    : `<strong>${escapeHtml(timestamp)}</strong>`;
  return `<div class="overview-chart-tooltip" role="tooltip" data-active="true" data-layout="single-column" data-list="${values.length > 1}">${heading}${rows}</div>`;
}

function chartValue(value: CallbackDataParams["value"]) {
  if (Array.isArray(value)) {
    for (let index = value.length - 1; index >= 0; index -= 1) {
      if (typeof value[index] === "number") return Number(value[index]);
    }
    return 0;
  }
  return Number(value ?? 0);
}

function formatAxisTokens(value: number) {
  const tokens = Math.max(0, Math.round(Number.isFinite(value) ? value : 0));
  for (const [divisor, unit] of [[1_000_000_000, "B"], [1_000_000, "M"], [1_000, "K"]] as const) {
    if (tokens >= divisor && tokens % divisor === 0) {
      return `${(tokens / divisor).toLocaleString("en-US")} ${unit}`;
    }
  }
  return `${tokens.toLocaleString("en-US")} Token`;
}

function escapeHtml(value: string) {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"
  })[character] ?? character);
}

function escapeAttribute(value: string) {
  return escapeHtml(value).replace(/[^#(),.%\w\s-]/g, "");
}
