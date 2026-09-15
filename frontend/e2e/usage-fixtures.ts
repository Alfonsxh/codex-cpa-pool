import type { Page, Route } from "@playwright/test";

export async function fulfillJSON(route: Route, payload: unknown, headers: Record<string, string> = {}) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    headers: { "Content-Language": route.request().headers()["accept-language"] ?? "en", ...headers },
    body: JSON.stringify(payload)
  });
}

export function usageAccountsFixture(language = "zh-CN") {
  const usage = {
    request_count: 2,
    success_count: 2,
    failed_count: 0,
    input_tokens: 100,
    output_tokens: 20,
    reasoning_tokens: 10,
    cached_tokens: 0,
    total_tokens: 120,
    weighted_tokens: 180,
    last_used_at: 1_787_500_700
  };
  return {
    generated_at: 1_787_500_800,
    window: {
      window: "today",
      window_seconds: null,
      window_start_at: 1_787_472_000,
      window_end_at: 1_787_500_800,
      window_timezone: "Asia/Shanghai"
    },
    current_group: "alpha",
    accounts: [{
      id: "alpha",
      email: "cpa.alpha@example.com",
      display_name: "cpa.alpha@example.com",
      current: true,
      enabled: true,
      selectable: true,
      status: {
        code: "available",
        label: language === "en" ? "Available" : "可用",
        tone: "success",
        reason: language === "en" ? "This account is available" : "账号当前可用",
        selectable: true,
        used_percent: 20,
        remaining_percent: 80,
        reset_at: 1_787_846_400
      },
      active_users_1h: 2,
      active_user_emails_1h: ["alice@example.com", "bob@example.com"],
      reset_credit_count: 1,
      resettable: true,
      reset_window_labels: ["常规周限额"],
      usage
    }],
    totals: usage,
    warnings: []
  };
}

export function usageQuotaFixture() {
  return {
    generated_at: 1_787_500_800,
    weekly_quota: {
      period: "natural_week",
      timezone: "Asia/Shanghai",
      week_start_at: 1_787_241_600,
      week_end_at: 1_787_846_400,
      limit_tokens: 20_000_000,
      base_limit_tokens: 20_000_000,
      bonus_tokens: 0,
      used_tokens: 3_000_000,
      weighted_used_tokens: 3_000_000,
      raw_used_tokens: 2_400_000,
      unweighted_used_tokens: 2_400_000,
      weighted_raw_used_tokens: 3_000_000,
      usage_reset_tokens: 0,
      remaining_tokens: 17_000_000,
      used_percent: 15,
      limit_reached: false,
      source: "default",
      policy_mode: "inherit",
      policy_tokens: null,
      policy_updated_at: null,
      policy_updated_by: null,
      policy_reset_at: null,
      default_limit_tokens: 20_000_000,
      unlimited: false,
      soft_limit: false,
      quota_unit: "weighted_tokens",
      adjustment_count: 0,
      personal_policy_reset_enabled: true
    }
  };
}

export function usageBreakdownFixture(window: string, account: string) {
  const totals = {
    request_count: 916,
    success_count: 915,
    failed_count: 1,
    input_tokens: 129_700_000,
    output_tokens: 485_000,
    reasoning_tokens: 238_000,
    cached_tokens: 121_000_000,
    total_tokens: 130_153_327,
    weighted_tokens: 130_500_000,
    last_used_at: 1_787_500_700
  };
  const xhigh = {
    request_count: 911,
    success_count: 910,
    failed_count: 1,
    input_tokens: 129_080_000,
    output_tokens: 465_000,
    reasoning_tokens: 225_500,
    cached_tokens: 120_500_000,
    total_tokens: 129_513_327,
    weighted_tokens: 129_847_500,
    last_used_at: 1_787_500_700
  };
  const maximum = {
    request_count: 5,
    success_count: 5,
    failed_count: 0,
    input_tokens: 620_000,
    output_tokens: 20_000,
    reasoning_tokens: 12_500,
    cached_tokens: 500_000,
    total_tokens: 640_000,
    weighted_tokens: 652_500,
    last_used_at: 1_787_500_600
  };
  return {
    generated_at: 1_787_500_800,
    window,
    window_seconds: 86_400,
    window_start_at: 1_787_472_000,
    window_end_at: 1_787_500_800,
    collection_started_at: 1_787_472_000,
    effective_start_at: 1_787_472_000,
    definition: "视觉回归账号明细",
    account,
    user: "alice@example.com",
    totals,
    models: [{ model: "gpt-5.6-sol", ...totals }],
    reasoning_efforts: [
      { reasoning_effort: "xhigh", ...xhigh },
      { reasoning_effort: "max", ...maximum }
    ],
    combinations: [
      { account, model: "gpt-5.6-sol", reasoning_effort: "xhigh", ...xhigh },
      { account, model: "gpt-5.6-sol", reasoning_effort: "max", ...maximum }
    ]
  };
}

export function usageTrendFixture(window: string, dimension: string) {
  const windowDays = window === "7d" ? 7 : window === "90d" ? 90 : 30;
  const end = Date.UTC(2026, 7, 29) / 1000;
  const start = end - (windowDays - 1) * 86_400;
  const combinationDefinitions = [
    ["gpt-5.6-sol", "xhigh", 0.52],
    ["gpt-5.6-terra", "medium", 0.26],
    ["gpt-5.6-luna", "high", 0.16],
    ["gpt-5.6-luna", "low", 0.06]
  ] as const;
  const days = Array.from({ length: windowDays }, (_, index) => {
    const totalTokens = 380_000 + index * 22_000 + (index % 5) * 35_000;
    const weightedTokens = Math.round(totalTokens * 1.28);
    return {
      date: new Date((start + index * 86_400) * 1000).toISOString().slice(0, 10),
      start_at: start + index * 86_400,
      end_at: start + (index + 1) * 86_400,
      collection_state: index === 0 && windowDays === 90 ? "partial" : "complete",
      request_count: 80 + index * 2,
      total_tokens: totalTokens,
      weighted_tokens: weightedTokens,
      combinations: dimension === "model_reasoning" ? combinationDefinitions.map(([model, reasoningEffort, share]) => ({
        model,
        reasoning_effort: reasoningEffort,
        request_count: Math.max(1, Math.round((80 + index * 2) * share)),
        total_tokens: Math.round(totalTokens * share),
        weighted_tokens: Math.round(weightedTokens * share)
      })) : []
    };
  });
  return {
    generated_at: end,
    window,
    window_days: windowDays,
    window_start_at: days[0]?.start_at ?? start,
    window_end_at: days.at(-1)?.end_at ?? end,
    window_timezone: "Asia/Shanghai",
    dimension,
    definition: "视觉回归自然日聚合",
    collection_started_at: days[0]?.start_at ?? start,
    effective_start_at: days[0]?.start_at ?? start,
    days
  };
}

export async function installUsageVisualBackend(page: Page, state: "normal" | "loading" | "empty" | "error" = "normal") {
  await page.route("**/site-config.json", (route) => fulfillJSON(route, {
    version: 1,
    product_name: "Codex CPA Pool",
    short_name: "CCPA",
    environment_label: "本地模拟预览",
    public_base_url: "http://127.0.0.1:8317",
    provider_name: "Codex CPA Pool",
    api_key_env: "CCPA_API_KEY",
    default_model: "gpt-5.6-sol",
    logo: { custom: false, url: "/portal/assets/codex-cpa-pool-logo.svg", content_type: "image/svg+xml", sha256: "", updated_at: null }
  }));
  await page.route(/\/usage\/(?:session|me)(?:\/|\?|$)/, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    if (path === "/usage/session") {
      await fulfillJSON(route, { authenticated: true, user: "alice@example.com", expires_at: 1_787_544_000, password_change_required: false });
      return;
    }
    if (path === "/usage/me/profile") {
      await fulfillJSON(route, { user: "alice@example.com", current_group: "alpha", generated_at: 1_787_500_800 });
      return;
    }
    if (path === "/usage/me/route") {
      await fulfillJSON(route, { current_group: "alpha", generated_at: 1_787_500_800 });
      return;
    }
    if (path === "/usage/me/quota") {
      await fulfillJSON(route, usageQuotaFixture());
      return;
    }
    if (path === "/usage/me/accounts") {
      if (state === "loading") await new Promise((resolve) => setTimeout(resolve, 30_000));
      if (state === "error") {
        await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "visual_error", message: "视觉回归模拟错误" } }) });
        return;
      }
      const payload = usageAccountsFixture(route.request().headers()["accept-language"]);
      if (state === "empty") payload.accounts = [];
      await fulfillJSON(route, payload);
      return;
    }
    if (path === "/usage/me/key") {
      await fulfillJSON(route, { api_key: "visual-api-key", generated_at: 1_787_500_800 }, { "Cache-Control": "no-store" });
      return;
    }
    if (path === "/usage/me/usage-trend") {
      await fulfillJSON(route, usageTrendFixture(
        url.searchParams.get("window") ?? "30d",
        url.searchParams.get("dimension") ?? "total"
      ));
      return;
    }
    if (path === "/usage/me/usage-breakdown") {
      await fulfillJSON(route, usageBreakdownFixture(
        url.searchParams.get("window") ?? "today",
        url.searchParams.get("account") ?? "alpha"
      ));
      return;
    }
    await route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { code: "visual_not_found", message: path } }) });
  });
}
