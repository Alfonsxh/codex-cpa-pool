import { t } from "../i18n";
import { defaultSiteTimezone } from "../ui/site-time";
import { apiRequest } from "./client";
import type { NativeAccountCatalog, PublicSiteConfiguration } from "./generated";

export type { NativeAccount, NativeAccountCatalog, PublicSiteConfiguration } from "./generated";

export const publicSiteQueryKey = ["public-site-configuration"] as const;
export const nativeAccountsQueryKey = ["native-accounts"] as const;

export const defaultPublicSiteConfiguration: PublicSiteConfiguration = {
  version: 1,
  timezone: defaultSiteTimezone,
  product_name: "Codex CPA Pool",
  short_name: "CCPA",
  environment_label: "Self-hosted service",
  public_base_url: "",
  allowed_email_domains: [],
  provider_name: "Codex CPA Pool",
  api_key_env: "CCPA_API_KEY",
  default_model: "gpt-5.6-sol",
  logo: {
    custom: false,
    url: "/portal/assets/codex-cpa-pool-logo.svg",
    content_type: "image/svg+xml",
    sha256: "",
    updated_at: null
  }
};

export async function readPublicSiteConfiguration(signal?: AbortSignal): Promise<PublicSiteConfiguration> {
  const configuration = await apiRequest<PublicSiteConfiguration>("/site-config.json", { signal, cache: "no-store" });
  // Reject an unsupported zone as a query error; keep the last valid display zone.
  new Intl.DateTimeFormat("en", { timeZone: configuration.timezone || defaultSiteTimezone }).format();
  return configuration;
}

export function emailDomainHint(domains: readonly string[] | undefined): string {
  const suffixes = normalizedEmailDomains(domains).map((domain) => `@${domain}`);
  if (!suffixes.length) return "";
  return `${suffixes.length === 1 ? t("common.organization_email_domain") : t("common.allowed_organization_email_domains")}：${suffixes.join("、")}`;
}

export function emailPlaceholder(domains: readonly string[] | undefined): string {
  return `name@${normalizedEmailDomains(domains)[0] ?? "example.com"}`;
}

export function normalizedEmailDomains(domains: readonly string[] | undefined): string[] {
  const result: string[] = [];
  const seen = new Set<string>();
  for (const value of domains ?? []) {
    const domain = value.trim().replace(/^@+/, "").toLowerCase();
    if (!domain || seen.has(domain)) continue;
    seen.add(domain);
    result.push(domain);
  }
  return result;
}

export function listNativeAccounts(signal?: AbortSignal): Promise<NativeAccountCatalog> {
  return apiRequest<NativeAccountCatalog>("/admin/api/native-accounts", { signal, cache: "no-store" });
}
