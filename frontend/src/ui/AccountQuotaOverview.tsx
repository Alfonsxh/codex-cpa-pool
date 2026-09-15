import "../i18n/admin";
import { t, getIntlLocale } from "../i18n";
import { Link } from "react-router-dom";

import type { OverviewAccountQuotaSummary } from "../api/overview";

export function AccountQuotaOverview({ quota }: { quota: OverviewAccountQuotaSummary }) {
  const used = clampPercent(quota.average_used_percent ?? 0);
  const remaining = clampPercent(quota.average_remaining_percent ?? 0);
  return (
    <section className="overview-account-quota overview-legacy-panel" aria-labelledby="overview-account-quota-title">
      <header className="overview-account-quota-header">
        <div>
          <h3 id="overview-account-quota-title">{t("common.account_weekly_quota")}</h3>
          <p className="section-kicker">ACCOUNT WEEKLY QUOTA</p>
        </div>
        <Link className="button ghost overview-account-quota-link" to="/accounts">{t("admin.view_account_details")}</Link>
      </header>

      {quota.enabled_accounts === 0 ? (
        <QuotaState title={t("admin.no_enabled_accounts")} detail={t("admin.create_and_enable_a_cpa_account_to_see_weekly_quota")} />
      ) : !quota.available || quota.known_accounts === 0 ? (
        <QuotaState
          title={t("admin.quota_data_unavailable")}
          detail={t("admin.none_of_the_enabled_accounts_currently_has_weekly_quota_data", [quota.enabled_accounts])}
        />
      ) : (
        <div className="overview-account-quota-body">
            <div className="overview-account-quota-primary">
              <span>{t("admin.average_quota_used")}</span>
              <strong>{formatPercent(used)}</strong>
              <div
                className="overview-account-quota-progress"
                role="progressbar"
                aria-label={t("admin.average_weekly_quota_used")}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={used}
              >
                <i style={{ width: `${used}%` }} />
              </div>
              <div className="overview-account-quota-progress-labels">
                <span>{t("admin.used")} {formatPercent(used)}</span>
                <span>{t("common.remaining")} {formatPercent(remaining)}</span>
              </div>
            </div>

            <dl className="overview-account-quota-metrics" aria-label={t("admin.account_weekly_quota_summary")}>
              <QuotaMetric label={t("admin.remaining_accounts")} value={t("admin.accounts", [formatEquivalent(quota.equivalent_remaining_accounts)])} />
              <QuotaMetric label={t("admin.data_coverage")} value={`${quota.known_accounts} / ${quota.enabled_accounts}`} />
              <QuotaMetric label={t("admin.exhausted")} value={quota.exhausted_accounts} tone={quota.exhausted_accounts > 0 ? "danger" : undefined} />
              <QuotaMetric label={t("admin.high_risk")} value={quota.high_risk_accounts} tone={quota.high_risk_accounts > 0 ? "warning" : undefined} />
              <QuotaMetric label={t("common.unknown_quota")} value={quota.unknown_accounts} tone={quota.unknown_accounts > 0 ? "neutral" : undefined} />
            </dl>
        </div>
      )}
    </section>
  );
}

function QuotaMetric({
  label,
  value,
  tone
}: {
  label: string;
  value: string | number;
  tone?: "danger" | "warning" | "neutral";
}) {
  return (
    <div className={tone ? `tone-${tone}` : undefined}>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function QuotaState({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="overview-account-quota-state" role="status">
      <strong>{title}</strong>
      <span>{detail}</span>
    </div>
  );
}

function clampPercent(value: number) {
  return Math.min(100, Math.max(0, Number.isFinite(value) ? value : 0));
}

function formatPercent(value: number) {
  return `${value.toLocaleString(getIntlLocale(), { minimumFractionDigits: 1, maximumFractionDigits: 1 })}%`;
}

function formatEquivalent(value: number) {
  const safe = Number.isFinite(value) ? Math.max(0, value) : 0;
  return safe.toLocaleString(getIntlLocale(), { minimumFractionDigits: 1, maximumFractionDigits: 1 });
}
