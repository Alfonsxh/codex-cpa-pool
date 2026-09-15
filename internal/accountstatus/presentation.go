package accountstatus

import (
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

// Presentation is the shared Admin/Usage account status. Selectable governs
// manual selection; automatic failover continues to require AccountState.Eligible.
type Presentation struct {
	Code       string `json:"code"`
	Label      string `json:"label"`
	Tone       string `json:"tone"`
	Reason     string `json:"reason"`
	Selectable bool   `json:"selectable"`
}

// Present consumes only the canonical live account state, never a separately
// timed quota or native-runtime read from an individual surface.
func Present(enabled bool, state failover.AccountState, found bool, languages ...i18n.Language) Presentation {
	lang := i18n.Selected(languages)
	if !enabled {
		return Presentation{Code: "disabled", Label: i18n.Text(lang, "accountstatus.disabled"), Tone: "neutral", Reason: i18n.Text(lang, "accountstatus.this_account_was_disabled_by_an_administrator")}
	}
	if !found {
		return Presentation{Code: "unknown", Label: i18n.Text(lang, "accountstatus.unknown_status"), Tone: "neutral", Reason: i18n.Text(lang, "accountstatus.account_runtime_status_cannot_be_confirmed")}
	}
	status := Presentation{Code: "unknown", Label: i18n.Text(lang, "accountstatus.unknown_status"), Tone: "neutral", Reason: i18n.Text(lang, "accountstatus.account_runtime_status_cannot_be_confirmed"), Selectable: true}
	switch state.Reason {
	case "available":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"available", i18n.Text(lang, "accountstatus.available"), "success", i18n.Text(lang, "accountstatus.this_account_is_available"), true
	case "quota_exhausted", "upstream_disallowed":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_exhausted", i18n.Text(lang, "accountstatus.quota_exhausted"), "danger", i18n.Text(lang, "accountstatus.account_weekly_quota_is_exhausted"), false
	case "account_disabled":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"disabled", i18n.Text(lang, "accountstatus.disabled"), "neutral", i18n.Text(lang, "accountstatus.this_account_was_disabled_by_an_administrator"), false
	case "container_not_running":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"stopped", i18n.Text(lang, "accountstatus.stopped"), "danger", i18n.Text(lang, "accountstatus.cpa_service_is_not_running"), false
	case "oauth_missing":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"auth_missing", i18n.Text(lang, "accountstatus.unauthorized"), "danger", i18n.Text(lang, "accountstatus.oauth_authorization_is_incomplete"), false
	case "credential_unavailable":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"credential_unavailable", i18n.Text(lang, "accountstatus.credentials_unavailable"), "danger", i18n.Text(lang, "accountstatus.oauth_credentials_expired_authorize_again"), false
	case "transient_cooldown":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"transient_cooldown", i18n.Text(lang, "accountstatus.temporary_cooldown"), "warning", i18n.Text(lang, "accountstatus.an_upstream_request_failed_temporarily_the_cpa_is_waiting_for"), true
	case "rate_limited":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"rate_limited", i18n.Text(lang, "accountstatus.rate_limited"), "warning", i18n.Text(lang, "accountstatus.this_account_recently_returned_429_it_can_still_be_selected"), true
	case "degraded":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"degraded", i18n.Text(lang, "accountstatus.recent_errors"), "warning", i18n.Text(lang, "accountstatus.this_account_has_recent_request_errors"), true
	case "runtime_unknown":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"unknown", i18n.Text(lang, "accountstatus.unknown_status"), "neutral", i18n.Text(lang, "accountstatus.cpa_native_status_cannot_be_queried"), true
	case "reserve_reached":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_warning", i18n.Text(lang, "accountstatus.quota_reserve"), "warning", i18n.Text(lang, "accountstatus.the_account_has_reached_its_quota_reserve"), true
	case "quota_stale":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"unknown", i18n.Text(lang, "accountstatus.unknown_status"), "neutral", i18n.Text(lang, "accountstatus.live_account_status_cannot_be_confirmed"), true
	case "quota_unavailable":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_unknown", i18n.Text(lang, "accountstatus.unknown_quota"), "neutral", i18n.Text(lang, "accountstatus.quota_status_cannot_be_confirmed"), true
	}
	if state.Exhausted {
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_exhausted", i18n.Text(lang, "accountstatus.quota_exhausted"), "danger", i18n.Text(lang, "accountstatus.account_weekly_quota_is_exhausted"), false
	}
	if status.Code == "available" && state.RemainingPercent != nil && *state.RemainingPercent <= 10 {
		status.Code, status.Label, status.Tone, status.Reason = "quota_warning", i18n.Text(lang, "accountstatus.low_quota"), "warning", i18n.Text(lang, "accountstatus.weekly_quota_remaining_is_10_or_less")
	}
	return status
}
