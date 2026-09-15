package quota

import (
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

// authoredWindowLabel rebuilds labels this service owns. Upstream limit names
// are never rewritten, so only unnamed windows are reported as authored.
func authoredWindowLabel(key string, lang i18n.Language) (string, bool) {
	if strings.HasPrefix(key, "default:") {
		return i18n.Text(lang, "notifications.weekly_limit"), true
	}
	if position, found := strings.CutPrefix(key, additionalWindowKeyPrefix+unnamedAdditionalWindowIdentifier); found {
		index, _, _ := strings.Cut(position, ":")
		return i18n.Text(lang, "quota.additional_weekly_limit") + index, true
	}
	return "", false
}

// WithLanguage presents a copy. Official quota snapshots and upstream labels
// stay unchanged, including when concurrent clients select different languages.
func (window WeeklyWindow) WithLanguage(lang i18n.Language) WeeklyWindow {
	if label, authored := authoredWindowLabel(window.Key, lang); authored {
		window.Label = label
	}
	return window
}
func (account AccountQuota) WithLanguage(lang i18n.Language) AccountQuota {
	if account.Weekly != nil {
		window := account.Weekly.WithLanguage(lang)
		account.Weekly = &window
	}
	if account.WeeklyWindows != nil {
		windows := make([]WeeklyWindow, len(account.WeeklyWindows))
		for i, window := range account.WeeklyWindows {
			windows[i] = window.WithLanguage(lang)
		}
		account.WeeklyWindows = windows
	}
	return account
}
func (snapshot Snapshot) Localized(lang i18n.Language) any {
	accounts := make([]AccountQuota, len(snapshot.Accounts))
	for i, account := range snapshot.Accounts {
		accounts[i] = account.WithLanguage(lang)
	}
	snapshot.Accounts = accounts
	return snapshot
}

func localizeResetWindows(windows []ResetWindow, lang i18n.Language) []ResetWindow {
	if windows == nil {
		return nil
	}
	out := append([]ResetWindow(nil), windows...)
	for i := range out {
		if label, authored := authoredWindowLabel(out[i].Key, lang); authored {
			out[i].Label = label
		}
	}
	return out
}
func (inspection ResetInspection) Localized(lang i18n.Language) any {
	inspection.Windows = localizeResetWindows(inspection.Windows, lang)
	return inspection
}
func (result ResetResult) WithLanguage(lang i18n.Language) ResetResult {
	result.Windows = localizeResetWindows(result.Windows, lang)
	return result
}
