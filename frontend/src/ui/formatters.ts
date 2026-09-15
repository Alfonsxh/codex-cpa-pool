import { t, getLanguage } from "../i18n";
export function formatTokenAmount(value: number) {
  const safeValue = Number.isFinite(value) ? value : 0;
  const absolute = Math.abs(safeValue);
  const compact = (scaled: number, suffix: string) => {
    const rounded = Math.round(scaled * 10) / 10;
    return `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(rounded)} ${suffix}`;
  };
  if (absolute >= 1_000_000_000) return compact(safeValue / 1_000_000_000, "B");
  if (absolute >= 1_000_000) return compact(safeValue / 1_000_000, "M");
  if (absolute >= 1_000) {
    const roundedThousands = Math.round((safeValue / 1_000) * 10) / 10;
    if (Math.abs(roundedThousands) >= 1_000) return compact(safeValue / 1_000_000, "M");
    return compact(safeValue / 1_000, "K");
  }
  return String(Math.round(safeValue));
}

export function formatTokens(value: number) {
  const amount = formatTokenAmount(value);
  return Math.abs(Number.isFinite(value) ? value : 0) >= 1_000 ? amount : `${amount} Token`;
}

type TokenReadableParts =
  | { state: "empty" }
  | { state: "invalid" }
  | {
    state: "ready";
    compact: string;
    localized: string;
    exact: string;
    compacted: boolean;
  };

export type TokenInputPresentation =
  | { state: "empty"; emptyLabel: string }
  | { state: "invalid" }
  | {
    state: "ready";
    compact: string;
    localized: string;
    exact: string;
    compacted: boolean;
  };

const tokenAmountFormatter = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });
const tokenExactFormatter = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });
const tokenMagnitudeFormatter = new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 });

/**
 * Mirrors the frozen Admin token formatter used by quota summaries and inputs.
 * Keep this separate from the shorter chart/table formatter: the legacy quota
 * contract includes both a compact value and the Chinese magnitude.
 */
export function tokenReadableParts(
  value: string | number | null | undefined,
  { allowZero = false }: { allowZero?: boolean } = {}
): TokenReadableParts {
  const raw = String(value ?? "").trim();
  if (!raw) return { state: "empty" };
  if (!/^\d+$/.test(raw)) return { state: "invalid" };
  const tokens = Number(raw);
  if (!Number.isSafeInteger(tokens) || tokens < 0 || (!allowZero && tokens === 0)) {
    return { state: "invalid" };
  }

  let divisor = 1;
  let unit = "Token";
  if (tokens >= 1_000_000_000) [divisor, unit] = [1_000_000_000, "B"];
  else if (tokens >= 1_000_000) [divisor, unit] = [1_000_000, "M"];
  else if (tokens >= 1_000) [divisor, unit] = [1_000, "K"];

  let rounded = Math.round((tokens / divisor) * 10) / 10;
  if (unit === "K" && rounded >= 1_000) {
    [divisor, unit] = [1_000_000, "M"];
    rounded = Math.round((tokens / divisor) * 10) / 10;
  }
  if (unit === "M" && rounded >= 1_000) {
    [divisor, unit] = [1_000_000_000, "B"];
    rounded = Math.round((tokens / divisor) * 10) / 10;
  }

  const amount = tokenAmountFormatter.format(rounded);
  const exact = `${tokenExactFormatter.format(tokens)} Token`;
  const compact = unit === "Token" ? exact : `${amount} ${unit} Token`;
  let localized = "";
  if (getLanguage() === "en") {
    return { state: "ready", compact, localized, exact, compacted: divisor > 1 };
  }
  if (tokens >= 1_000_000_000_000) {
    localized = t("common.trillion_tokens", [tokenMagnitudeFormatter.format(tokens / 1_000_000_000_000)]);
  } else if (tokens >= 100_000_000) {
    localized = t("common.100_million_tokens", [tokenMagnitudeFormatter.format(tokens / 100_000_000)]);
  } else if (tokens >= 10_000) {
    localized = t("common.10_thousand_tokens", [tokenMagnitudeFormatter.format(tokens / 10_000)]);
  }
  return { state: "ready", compact, localized, exact, compacted: divisor > 1 };
}

export function tokenReadableText(value: string | number | null | undefined) {
  const details = tokenReadableParts(value, { allowZero: true });
  if (details.state !== "ready") return "—";
  return details.localized ? `${details.compact}（${details.localized}）` : details.compact;
}

export function tokenInputPresentation(
  value: string | number | null | undefined,
  emptyLabel = t("common.enter_token_amount")
): TokenInputPresentation {
  const details = tokenReadableParts(value);
  if (details.state === "empty") return { state: "empty", emptyLabel };
  if (details.state === "invalid") return { state: "invalid" };
  return details;
}
