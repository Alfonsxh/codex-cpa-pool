import "../i18n/usage";
import { t, getIntlLocale } from "../i18n";
import { useSiteTimezone } from "./site-time";
import * as echarts from "echarts/core";
import { LineChart, type LineSeriesOption } from "echarts/charts";
import {
  AriaComponent,
  GridComponent,
  TooltipComponent,
  type AriaComponentOption,
  type GridComponentOption,
  type TooltipComponentOption
} from "echarts/components";
import { SVGRenderer } from "echarts/renderers";
import type { ComposeOption, EChartsType } from "echarts/core";
import type { DefaultLabelFormatterCallbackParams as CallbackDataParams } from "echarts";
import { Tooltip } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

import { ApiError } from "../api/client";
import {
  portalUsageTrendQueryKey,
  readPortalUsageTrend,
  type PortalUsageTrend,
  type PortalUsageTrendDimension,
  type PortalUsageTrendWindow
} from "../api/portal";
import { formatTokenAmount, formatTokens } from "./formatters";
import { usageChartColors } from "./components/UsageChart";
import { useTheme } from "./ThemeProvider";
import { formatUsageCombinationLabel as combinationLabel } from "./usage-multiplier-labels";

echarts.use([LineChart, GridComponent, TooltipComponent, AriaComponent, SVGRenderer]);

type DailyTrendOption = ComposeOption<
  LineSeriesOption | GridComponentOption | TooltipComponentOption | AriaComponentOption
>;

type PortalTrendSeries = {
  name: string;
  values: Array<number | null>;
};

type PortalTrendMetric = "total" | "weighted";

type PortalTrendSummaryValue = {
  label: string;
  value: string;
  title: string;
  metric: PortalTrendMetric;
};

type PortalTrendSummaryItem = {
  label: string;
  value?: string;
  title?: string;
  values?: PortalTrendSummaryValue[];
};

const directCombinationLimit = 9;

export type PortalTrendUpdateStatus = {
  updatedAt: number;
  refreshing: boolean;
  failed: boolean;
};

export function PortalDailyUsageTrend({
  user,
  expanded,
  window,
  onSessionExpired,
  onUpdateStatusChange
}: {
  user: string;
  expanded: boolean;
  window: PortalUsageTrendWindow;
  onSessionExpired: () => void;
  onUpdateStatusChange?: (status: PortalTrendUpdateStatus) => void;
}) {
  useSiteTimezone();
  const [dimension, setDimension] = useState<PortalUsageTrendDimension>("total");
  const [modelMetric, setModelMetric] = useState<PortalTrendMetric>("weighted");
  const query = useQuery({
    queryKey: [...portalUsageTrendQueryKey(window, dimension), user],
    queryFn: ({ signal }) => readPortalUsageTrend(window, dimension, signal),
    enabled: expanded,
    staleTime: 30_000,
    gcTime: 5 * 60_000,
    retry: false,
    refetchOnWindowFocus: false
  });

  useEffect(() => {
    if (query.error instanceof ApiError && query.error.status === 401) onSessionExpired();
  }, [onSessionExpired, query.error]);

  useEffect(() => {
    onUpdateStatusChange?.({
      updatedAt: query.data?.generated_at ?? 0,
      refreshing: query.isFetching,
      failed: query.isError
    });
  }, [onUpdateStatusChange, query.data?.generated_at, query.isFetching, query.isError]);

  const summary = useMemo(
    () => summarizePortalTrend(query.data, dimension),
    [dimension, query.data]
  );
  const series = useMemo(
    () => buildPortalTrendSeries(query.data, dimension, modelMetric),
    [dimension, modelMetric, query.data]
  );
  const hasData = Boolean(query.data?.days.some((day) => day.request_count > 0 || day.weighted_tokens > 0 || day.total_tokens > 0));
  return (
    <section className={`usage-trend-card${expanded ? "" : " collapsed"}`} aria-label={t("usage.daily_usage")}>
      <header className="usage-trend-header">
        <div className="usage-trend-heading">
          <div className="usage-trend-dimensions" role="group" aria-label={t("usage.trend_dimension")}>
            <button type="button" aria-pressed={dimension === "total"} onClick={() => setDimension("total")}>{t("usage.total_usage")}</button>
            <button type="button" aria-pressed={dimension === "model_reasoning"} onClick={() => setDimension("model_reasoning")}>{t("usage.model_reasoning_effort")}</button>
          </div>
        </div>

        <div className={`usage-trend-summary${dimension === "model_reasoning" ? " model-reasoning" : ""}`} aria-label={t("usage.trend_summary")}>
          {summary.items.map((item) => {
            const combinationCount = item.label === t("usage.combinations");
            return (
              <div className={[
                item.label === t("usage.top_combination") ? "primary-combination" : "",
                item.values ? "has-metrics" : "",
                combinationCount ? "combination-count" : ""
              ].filter(Boolean).join(" ")} key={item.label}>
                <span>{item.label}</span>
                {item.value ? <strong title={item.title}>{query.isPending ? "—" : item.value}</strong> : null}
                {item.values ? (
                  <div className="usage-trend-summary-values">
                    {item.values.map((value) => (
                      <Tooltip
                        key={value.metric}
                        title={query.isPending ? undefined : value.title}
                        placement="top"
                        mouseEnterDelay={0.15}
                        rootClassName="usage-trend-summary-popup"
                      >
                        <div
                          className="usage-trend-summary-value"
                          data-metric={value.metric}
                          aria-label={t("usage.tokens_exact_count", [value.label, query.isPending ? t("common.loading") : value.title])}
                        >
                          <small><i className="usage-trend-summary-value-marker" aria-hidden="true" /><span>{value.label}</span></small>
                          <strong>{query.isPending ? "—" : value.value}</strong>
                        </div>
                      </Tooltip>
                    ))}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>

      </header>

      {expanded ? (
        <div className="usage-trend-body" id="usage-trend-body">
          {query.isPending ? <TrendSkeleton /> : null}
          {query.isError ? (
            <div className="usage-trend-state error" role="alert">
              <span><strong>{t("usage.unable_to_load_daily_usage")}</strong> · {errorMessage(query.error)}</span>
              <button type="button" onClick={() => void query.refetch()}>{t("common.retry")}</button>
            </div>
          ) : null}
          {query.data && !hasData ? (
            <div className="usage-trend-state">{t("usage.no_collected_usage_in_the_selected_range")}</div>
          ) : null}
          {query.data && hasData ? (
            <PortalTrendChart
              trend={query.data}
              series={series}
              dimension={dimension}
              modelMetric={modelMetric}
              onModelMetricChange={setModelMetric}
            />
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

function TrendSkeleton() {
  return (
    <div className="usage-trend-skeleton" aria-label={t("usage.loading_daily_usage")}>
      <span /><span /><span /><span />
    </div>
  );
}

function PortalTrendChart({
  trend,
  series,
  dimension,
  modelMetric,
  onModelMetricChange
}: {
  trend: PortalUsageTrend;
  series: PortalTrendSeries[];
  dimension: PortalUsageTrendDimension;
  modelMetric: PortalTrendMetric;
  onModelMetricChange: (metric: PortalTrendMetric) => void;
}) {
  const { theme } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<EChartsType | null>(null);
  const labels = useMemo(() => trend.days.map((day) => formatTrendDate(day.date)), [trend.days]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container || series.length === 0) return;
    const chart = echarts.init(container, undefined, { renderer: "svg" });
    chartRef.current = chart;
    const dark = theme === "dark";
    const axisColor = dark ? "#7f8ba3" : "#8b95a7";
    const gridColor = dark ? "#273247" : "#e3e8f1";
    const labelIndexes = new Set([0, 0.25, 0.5, 0.75, 1].map((ratio) => (
      Math.round(Math.max(0, labels.length - 1) * ratio)
    )));
    const option: DailyTrendOption = {
      animation: false,
      aria: { enabled: true, description: t("usage.personal_daily_token_trend") },
      grid: { left: 76, right: 18, top: 18, bottom: 44, containLabel: false },
      tooltip: {
        trigger: "axis",
        triggerOn: "mousemove|click|mousewheel",
        confine: true,
        appendTo: "body",
        enterable: false,
        renderMode: "html",
        className: "usage-chart-echarts-tooltip usage-trend-echarts-tooltip",
        backgroundColor: "#171d2b",
        borderColor: "#39455e",
        borderWidth: 1,
        padding: 0,
        extraCssText: "z-index:10000;max-height:none;overflow:hidden;border-radius:9px;box-shadow:0 12px 28px rgb(8 12 22 / 28%);",
        axisPointer: { type: "line", lineStyle: { color: axisColor, type: "dashed", width: 1 } },
        formatter: (parameters) => renderPortalTrendTooltip(parameters, trend, dimension, modelMetric)
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
          fontSize: 12,
          margin: 17,
          interval: (index: number) => labelIndexes.has(index),
          hideOverlap: true
        }
      },
      yAxis: {
        type: "value",
        min: 0,
        splitNumber: 4,
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: axisColor,
          fontFamily: "SFMono-Regular, Consolas, Liberation Mono, monospace",
          fontSize: 12,
          margin: 12,
          formatter: (value: number) => formatTokens(value)
        },
        splitLine: { lineStyle: { color: gridColor, width: 1 } }
      },
      series: series.map((item, index): LineSeriesOption => ({
        name: item.name,
        type: "line",
        data: item.values,
        connectNulls: false,
        showSymbol: true,
        symbol: "circle",
        symbolSize: 5,
        sampling: "lttb",
        smooth: false,
        emphasis: { focus: "series" },
        lineStyle: { width: 2, color: trendSeriesColor(index, dimension) },
        itemStyle: {
          color: dark ? "#151b28" : "#ffffff",
          borderColor: trendSeriesColor(index, dimension),
          borderWidth: 2
        }
      }))
    };
    chart.setOption(option, { notMerge: true });
    const resizeObserver = new ResizeObserver(() => chart.resize());
    resizeObserver.observe(container);
    return () => {
      resizeObserver.disconnect();
      chart.dispose();
      if (chartRef.current === chart) chartRef.current = null;
    };
  }, [dimension, labels, modelMetric, series, theme, trend]);

  const showDay = (index: number) => {
    if (!chartRef.current || trend.days.length === 0) return;
    const bounded = Math.max(0, Math.min(trend.days.length - 1, index));
    chartRef.current.dispatchAction({ type: "showTip", seriesIndex: 0, dataIndex: bounded });
    containerRef.current?.setAttribute("data-active-index", String(bounded));
  };

  return (
    <div className={`usage-trend-chart${dimension === "model_reasoning" ? " model-reasoning" : ""}`}>
      {dimension === "model_reasoning" ? (
        <div className="usage-trend-chart-toolbar">
          <div className="usage-trend-metric-switch" role="group" aria-label={t("usage.model_trend_metric")}>
            <button type="button" data-metric="weighted" aria-pressed={modelMetric === "weighted"} onClick={() => onModelMetricChange("weighted")}><i aria-hidden="true" /><span>{t("common.weighted_2")}</span></button>
            <button type="button" data-metric="total" aria-pressed={modelMetric === "total"} onClick={() => onModelMetricChange("total")}><i aria-hidden="true" /><span>{t("common.unweighted_2")}</span></button>
          </div>
        </div>
      ) : null}
      <div
        className="usage-trend-chart-plot"
        role="img"
        aria-label={t("usage.personal_daily_token_trend_hover_or_use_the_left_and", [dimension === "model_reasoning" ? t("usage.showing_tokens", [modelMetric === "total" ? t("common.unweighted_2") : t("common.weighted_2")]) : ""])}
        tabIndex={0}
        onFocus={() => showDay(trend.days.length - 1)}
        onBlur={() => chartRef.current?.dispatchAction({ type: "hideTip" })}
        onKeyDown={(event) => {
          if (!trend.days.length || !["ArrowLeft", "ArrowRight", "Home", "End", "Escape"].includes(event.key)) return;
          event.preventDefault();
          if (event.key === "Escape") {
            chartRef.current?.dispatchAction({ type: "hideTip" });
            return;
          }
          const current = Number(containerRef.current?.getAttribute("data-active-index") ?? trend.days.length - 1);
          const next = event.key === "Home" ? 0 : event.key === "End"
            ? trend.days.length - 1
            : current + (event.key === "ArrowLeft" ? -1 : 1);
          showDay(next);
        }}
      >
        <div className="usage-trend-chart-canvas" ref={containerRef} aria-hidden="true" />
      </div>
    </div>
  );
}

export function buildPortalTrendSeries(
  trend: PortalUsageTrend | undefined,
  dimension: PortalUsageTrendDimension,
  modelMetric: PortalTrendMetric = "weighted"
): PortalTrendSeries[] {
  if (!trend) return [];
  const visibleValue = (state: string, value: number) => state === "uncollected" ? null : value;
  if (dimension === "total") {
    return [
      { name: t("common.raw_tokens"), values: trend.days.map((day) => visibleValue(day.collection_state, day.total_tokens)) },
      { name: t("common.weighted_tokens_2"), values: trend.days.map((day) => visibleValue(day.collection_state, day.weighted_tokens)) }
    ];
  }

  const totals = new Map<string, { model: string; effort: string; total: number }>();
  for (const day of trend.days) {
    for (const item of day.combinations) {
      const key = combinationKey(item.model, item.reasoning_effort);
      const current = totals.get(key) ?? { model: item.model, effort: item.reasoning_effort, total: 0 };
      current.total += item.weighted_tokens;
      totals.set(key, current);
    }
  }
  const ranked = [...totals.entries()].sort((left, right) => (
    right[1].total - left[1].total || combinationLabel(left[1].model, left[1].effort).localeCompare(
      combinationLabel(right[1].model, right[1].effort), getIntlLocale(), { numeric: true }
    )
  ));
  const direct = ranked.slice(0, directCombinationLimit);
  const overflow = new Set(ranked.slice(directCombinationLimit).map(([key]) => key));
  const metricValue = (item: PortalUsageTrend["days"][number]["combinations"][number]) => (
    modelMetric === "total" ? item.total_tokens : item.weighted_tokens
  );
  const result = direct.map(([key, identity]) => ({
    name: combinationLabel(identity.model, identity.effort, trend.current_multipliers),
    values: trend.days.map((day) => visibleValue(day.collection_state, day.combinations
      .filter((item) => combinationKey(item.model, item.reasoning_effort) === key)
      .reduce((sum, item) => sum + metricValue(item), 0)))
  }));
  if (overflow.size > 0) {
    result.push({
      name: t("usage.other_combinations"),
      values: trend.days.map((day) => visibleValue(day.collection_state, day.combinations
        .filter((item) => overflow.has(combinationKey(item.model, item.reasoning_effort)))
        .reduce((sum, item) => sum + metricValue(item), 0)))
    });
  }
  return result;
}

export function summarizePortalTrend(
  trend: PortalUsageTrend | undefined,
  dimension: PortalUsageTrendDimension
): { items: PortalTrendSummaryItem[] } {
  if (!trend) {
    return { items: defaultSummaryItems(dimension) };
  }
  const weightedTotal = trend.days.reduce((sum, day) => sum + day.weighted_tokens, 0);
  const rawTotal = trend.days.reduce((sum, day) => sum + day.total_tokens, 0);
  if (dimension === "total") {
    const weightedPeak = trend.days.reduce((maximum, day) => Math.max(maximum, day.weighted_tokens), 0);
    const rawPeak = trend.days.reduce((maximum, day) => Math.max(maximum, day.total_tokens), 0);
    return { items: [
      { label: t("usage.day_usage", [trend.window_days]), values: dualSummaryValues(rawTotal, weightedTotal) },
      { label: t("usage.daily_average"), values: dualSummaryValues(
        Math.round(rawTotal / Math.max(1, trend.window_days)),
        Math.round(weightedTotal / Math.max(1, trend.window_days))
      ) },
      { label: t("common.peak"), values: dualSummaryValues(rawPeak, weightedPeak) }
    ] };
  }
  const combinations = new Map<string, { label: string; raw: number; weighted: number }>();
  for (const day of trend.days) {
    for (const item of day.combinations) {
      const key = combinationKey(item.model, item.reasoning_effort);
      const current = combinations.get(key) ?? { label: combinationLabel(item.model, item.reasoning_effort, trend.current_multipliers), raw: 0, weighted: 0 };
      current.raw += item.total_tokens;
      current.weighted += item.weighted_tokens;
      combinations.set(key, current);
    }
  }
  const primary = [...combinations.values()].sort((left, right) => right.weighted - left.weighted || left.label.localeCompare(right.label))[0];
  return { items: [
    { label: t("usage.day_usage", [trend.window_days]), values: dualSummaryValues(rawTotal, weightedTotal) },
    {
      label: t("usage.top_combination"),
      value: primary?.label ?? "—",
      title: primary?.label ?? t("usage.no_combinations"),
      values: primary ? dualSummaryValues(primary.raw, primary.weighted) : dualSummaryValues(0, 0)
    },
    { label: t("usage.combinations"), value: String(combinations.size), title: t("usage.model_and_reasoning_effort_combinations", [combinations.size]) }
  ] };
}

function dualSummaryValues(raw: number, weighted: number): PortalTrendSummaryValue[] {
  return [
    { label: t("common.weighted_2"), value: formatTokens(weighted), title: weighted.toLocaleString("en-US"), metric: "weighted" },
    { label: t("common.unweighted_2"), value: formatTokens(raw), title: raw.toLocaleString("en-US"), metric: "total" }
  ];
}

function defaultSummaryItems(dimension: PortalUsageTrendDimension) {
  return dimension === "total"
    ? [
      { label: t("usage.30_day_usage"), values: dualSummaryValues(0, 0) },
      { label: t("usage.daily_average"), values: dualSummaryValues(0, 0) },
      { label: t("common.peak"), values: dualSummaryValues(0, 0) }
    ]
    : [
      { label: t("usage.30_day_usage"), values: dualSummaryValues(0, 0) },
      { label: t("usage.top_combination"), value: "—", title: t("common.loading"), values: dualSummaryValues(0, 0) },
      { label: t("usage.combinations"), value: "—", title: t("common.loading") }
    ];
}

export function renderPortalTrendTooltip(
  parameters: CallbackDataParams | CallbackDataParams[],
  trend: PortalUsageTrend,
  dimension: PortalUsageTrendDimension,
  modelMetric: PortalTrendMetric = "weighted"
) {
  const values = Array.isArray(parameters) ? parameters : [parameters];
  const index = Number(values[0]?.dataIndex ?? 0);
  const day = trend.days[index];
  if (!day) return "";
  const rows = values
    .map((item) => ({
      name: String(item.seriesName ?? ""),
      value: chartValue(item.value),
      // ECharts exposes itemStyle.color here. Trend points intentionally use
      // the panel background as their fill and the series color as a border,
      // so item.color would render every Tooltip marker almost black. Keep the
      // marker tied to the same deterministic palette as the line instead.
      color: trendSeriesColor(item.seriesIndex, dimension)
    }))
    .filter((item) => item.value !== null)
    .sort((left, right) => dimension === "total" ? 0 : (right.value ?? 0) - (left.value ?? 0) || left.name.localeCompare(right.name))
    .slice(0, 10)
    .map((item) => renderTooltipRow(item, dimension))
    .join("");
  const requestRow = dimension === "total"
    ? t("usage.span_class_usage_trend_tooltip_request_b_requests_b_em", [day.request_count.toLocaleString("en-US")])
    : t("usage.span_class_usage_trend_tooltip_total_b_daily_b_em", [modelMetric === "total" ? t("common.unweighted_2") : t("common.weighted_2"), escapeHTML(`${formatTokenAmount(modelMetric === "total" ? day.total_tokens : day.weighted_tokens)} Token`)]);
  return `<div class="overview-chart-tooltip usage-trend-tooltip" role="tooltip" data-active="true" data-layout="single-column" data-dimension="${escapeAttribute(dimension)}"><strong>${escapeHTML(formatTrendDate(day.date))}</strong>${rows}${requestRow}</div>`;
}

function trendSeriesColor(seriesIndex: CallbackDataParams["seriesIndex"], dimension: PortalUsageTrendDimension) {
  const index = Number(seriesIndex);
  const palette = dimension === "total" ? ["#6374d8", "#d18b41"] : usageChartColors;
  return palette[Number.isInteger(index) && index >= 0 ? index % palette.length : 0];
}

function renderTooltipRow(
  item: { name: string; value: number | null; color: string },
  dimension: PortalUsageTrendDimension
) {
  const value = `${formatTokenAmount(item.value ?? 0)} Token`;
  if (dimension === "total") {
    return `<span class="usage-trend-tooltip-row"><i style="background:${escapeAttribute(item.color)}"></i><b title="${escapeAttribute(item.name)}">${escapeHTML(item.name)}</b><em>${escapeHTML(value)}</em></span>`;
  }
  const separator = item.name.lastIndexOf(" · ");
  const model = separator >= 0 ? item.name.slice(0, separator) : item.name;
  const effort = separator >= 0 ? item.name.slice(separator + 3) : "";
  return `<span class="usage-trend-tooltip-row usage-trend-tooltip-combination" title="${escapeAttribute(item.name)}"><i style="background:${escapeAttribute(item.color)}"></i><b class="usage-trend-tooltip-model">${escapeHTML(model)}</b> <b class="usage-trend-tooltip-effort">${escapeHTML(effort)}</b> <em>${escapeHTML(value)}</em></span>`;
}

function chartValue(value: CallbackDataParams["value"]): number | null {
  if (Array.isArray(value)) {
    const last = value[value.length - 1];
    return last === null || last === undefined ? null : Number(last) || 0;
  }
  return value === null || value === undefined || value === "-" ? null : Number(value) || 0;
}

function combinationKey(model: string, effort: string) {
  return `${model}\u0000${effort}`;
}

function formatTrendDate(date: string) {
  return date.replace(/-/g, "/");
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : t("usage.unknown_error");
}

function escapeHTML(value: string) {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
  })[character] ?? character);
}

function escapeAttribute(value: string) {
  return escapeHTML(value).replace(/`/g, "&#96;");
}
