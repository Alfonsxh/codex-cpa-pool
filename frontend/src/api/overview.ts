import { t } from "../i18n";
import { apiRequest, apiResponse } from "./client";
import type {
  OverviewCatalog,
  OverviewPayload,
  OverviewStatusPayload,
  OverviewUsageResponse,
  ReleaseStatus
} from "./generated";

export type {
  OverviewAccountQuotaSummary,
  OverviewCatalog,
  OverviewPayload,
  OverviewStatusPayload,
  OverviewSummary,
  OverviewUsageResponse,
  ReleaseStatus,
  TokenSeries
} from "./generated";

export const overviewSummaryQueryKey = ["overview-summary"] as const;
export const overviewCatalogQueryKey = ["overview-catalog"] as const;
export const overviewStatusQueryKey = ["overview-status"] as const;

export function readOverviewSummary(signal?: AbortSignal): Promise<OverviewPayload> {
  return apiRequest<OverviewPayload>("/admin/api/overview/summary", { signal });
}

export function readOverviewCatalog(signal?: AbortSignal): Promise<OverviewCatalog> {
  return apiRequest<OverviewCatalog>("/admin/api/overview/catalog", { signal });
}

export function readOverviewStatus(signal?: AbortSignal): Promise<OverviewStatusPayload> {
  return apiRequest<OverviewStatusPayload>("/admin/api/overview/status", { signal });
}

export type OverviewUsageWindow = "3600" | "21600" | "86400" | "604800" | "2592000" | "today" | "since_reset" | "custom";

export type OverviewUsageOptions = {
  window: OverviewUsageWindow;
  accounts?: string[];
  users?: string[];
  userLimit?: number;
  tokenMode?: "unweighted" | "weighted";
  startAt?: number;
  endAt?: number;
  fresh?: boolean;
};

export function readOverviewUsage(options: OverviewUsageOptions, signal?: AbortSignal): Promise<OverviewUsageResponse> {
  const query = new URLSearchParams({
    window: options.window,
    user_limit: String(options.userLimit ?? 10),
    token_mode: options.tokenMode ?? "unweighted"
  });
  options.accounts?.forEach((account) => query.append("account", account));
  options.users?.forEach((user) => query.append("user", user));
  if (options.window === "custom" && options.startAt !== undefined && options.endAt !== undefined) {
    query.set("start_at", String(options.startAt));
    query.set("end_at", String(options.endAt));
  }
  if (options.fresh) query.set("fresh", "1");
  return apiRequest<OverviewUsageResponse>(`/admin/api/overview/usage?${query}`, { signal });
}

export function readReleaseStatus(fresh = false, signal?: AbortSignal): Promise<ReleaseStatus> {
  return apiRequest<ReleaseStatus>(`/admin/api/release${fresh ? "?fresh=1" : ""}`, { signal });
}

export async function exportWeeklyUsage(weekStart: string, signal?: AbortSignal, withUnits = false) {
  const query = new URLSearchParams({ week_start: weekStart, with_units: String(withUnits) });
  const response = await apiResponse(`/admin/api/overview/usage-report.xlsx?${query}`, {
    signal,
    headers: { Accept: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" }
  });
  if (!response.headers.get("Content-Type")?.includes("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")) {
    throw new Error(t("common.no_valid_weekly_report_was_received_please_try_again_later"));
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const encoded = /filename\*=utf-8''([^;]+)/i.exec(disposition)?.[1];
  let filename = t("common.ccpa_token_report_xlsx", [weekStart]);
  if (encoded) {
    try { filename = decodeURIComponent(encoded); } catch { /* Keep the safe fallback. */ }
  }
  filename = filename.replace(/[\\/\u0000-\u001f\u007f]/g, "_");
  return { blob: await response.blob(), filename };
}
