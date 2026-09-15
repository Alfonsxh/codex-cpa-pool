package notifications

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
)

func UsageCenterURL(publicBaseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(publicBaseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(parsed.Path, "/usage") {
		parsed.Path += "/usage"
	}
	parsed.Path += "/"
	parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", ""
	parsed.ForceQuery = false
	return parsed.String()
}

// Reports and alerts share one regular weekly window per account. Additional
// model-specific windows never become extra accounts or independent alerts.
func QuotaRows(snapshot Snapshot, thresholdPercent float64, onlyKeys map[string]struct{}) []Row {
	rows := make([]Row, 0, len(snapshot.Accounts))
	seen := make(map[string]struct{}, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		id := defaultString(strings.TrimSpace(account.ID), "unknown")
		if _, found := seen[id]; found {
			continue
		}
		seen[id] = struct{}{}
		row := Row{
			Key: id + "|unavailable", Account: id, Label: i18n.Text(i18n.English, "notifications.weekly_limit"),
			ActiveUsers: max(0, account.ActiveUsers1H), Level: "unavailable",
		}
		if account.Quota.Status == "ok" {
			row.ResetCount = account.Quota.ResetCreditCount
		}
		if window, found := regularWeeklyWindow(account.Quota); found {
			row.Key = id + "|" + window.Key
			if account.Quota.Status == "ok" && !math.IsNaN(window.UsedPercent) && !math.IsInf(window.UsedPercent, 0) {
				used := math.Max(0, math.Min(window.UsedPercent, 100))
				row.Level = "normal"
				switch {
				case window.LimitReached || used >= 100:
					used, row.Level = 100, "exhausted"
				case used >= thresholdPercent:
					row.Level = "warning"
				}
				row.UsedPercent, row.ResetAt, row.ResetKey = &used, window.ResetAt, window.ResetAt
			}
		}
		if len(onlyKeys) > 0 {
			if _, found := onlyKeys[row.Key]; !found {
				continue
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(left int, right int) bool {
		// Keep the report focused on weekly consumption: accounts with a
		// lower used percentage always appear first, regardless of status.
		// Accounts without a usable quota value are placed after measured rows.
		leftUsed, rightUsed := rows[left].UsedPercent, rows[right].UsedPercent
		if leftUsed == nil || rightUsed == nil {
			if leftUsed != nil {
				return true
			}
			if rightUsed != nil {
				return false
			}
			return naturalCompare(rows[left].Account, rows[right].Account) < 0
		}
		if *leftUsed != *rightUsed {
			return *leftUsed < *rightUsed
		}
		return naturalCompare(rows[left].Account, rows[right].Account) < 0
	})
	return rows
}

func regularWeeklyWindow(accountQuota quota.AccountQuota) (quota.WeeklyWindow, bool) {
	windows := accountQuota.WeeklyWindows
	if len(windows) == 0 && accountQuota.Weekly != nil {
		windows = []quota.WeeklyWindow{*accountQuota.Weekly}
	}
	var selected quota.WeeklyWindow
	found := false
	for _, window := range windows {
		key := strings.ToLower(strings.TrimSpace(window.Key))
		if key == "" {
			key = "default:primary_window"
		}
		if !strings.HasPrefix(key, "default:") || strings.Contains(strings.ToLower(window.Label), "gpt-5.3") ||
			(window.WindowSeconds != 0 && window.WindowSeconds != quota.WeeklyWindowSeconds) {
			continue
		}
		window.Key = key
		if !found || key == "default:primary_window" {
			selected, found = window, true
		}
		if key == "default:primary_window" {
			break
		}
	}
	return selected, found
}

type ReportOptions struct {
	Language        i18n.Language
	PreviousWindows map[string]WindowRecord
}

var transitionLabels = map[string]string{
	"warning": "notifications.warning_threshold_reached", "exhausted": "notifications.quota_exhausted",
	"recovered": "notifications.quota_recovered", "recovered_warning": "notifications.quota_recovered_still_within_the_warning_range",
	"refreshed": "notifications.weekly_quota_reset",
}

func BuildMarkdownV2(
	snapshot Snapshot,
	title string,
	location *time.Location,
	thresholdPercent float64,
	now time.Time,
	onlyKeys map[string]struct{},
	transitionEvents map[string]string,
	usageCenterURL string,
	options ...ReportOptions,
) (string, error) {
	lang := i18n.English
	if len(options) > 0 {
		lang = i18n.Normalize(string(options[0].Language))
	}
	if location == nil {
		location = time.UTC
	}
	allRows := QuotaRows(snapshot, thresholdPercent, nil)
	rows := allRows
	if len(onlyKeys) > 0 {
		rows = make([]Row, 0, len(onlyKeys))
		for _, row := range allRows {
			if _, found := onlyKeys[row.Key]; found {
				rows = append(rows, row)
			}
		}
	}
	sections := messageHeader(title, location, now, usageCenterURL, lang, thresholdPercent)
	if len(onlyKeys) > 0 {
		sections = append(sections, i18n.M("notifications.affected_accounts", i18n.Params{"Count": len(rows)}).Render(lang))
	} else {
		sections = append(sections, accountSummary(allRows, snapshot.ActiveUserWindowSeconds, lang))
	}
	var previous map[string]WindowRecord
	if len(options) > 0 {
		previous = options[0].PreviousWindows
	}
	sections = append(sections, accountTable(rows, transitionEvents, previous, location, now, len(onlyKeys) > 0, snapshot.ActiveUserWindowSeconds, lang))
	return boundedMessage(sections)
}

func messageHeader(title string, location *time.Location, now time.Time, usageCenterURL string, lang i18n.Language, threshold ...float64) []string {
	sections := []string{"# " + safeCell(title, 64), i18n.Text(lang, "notifications.report_time") + notificationTime(now, location)}
	if len(threshold) > 0 {
		sections = append(sections, i18n.M("notifications.warning_threshold", i18n.Params{"Value": formatPercentValue(threshold[0])}).Render(lang))
	}
	if usageCenterURL = strings.TrimSpace(usageCenterURL); usageCenterURL != "" {
		link := strings.NewReplacer("(", "%28", ")", "%29", "[", "%5B", "]", "%5D").Replace(usageCenterURL)
		sections = append(sections, i18n.M("notifications.application_url", i18n.Params{"Value1": link, "Value2": link}).Render(lang))
	} else {
		sections = append(sections, i18n.Text(lang, "notifications.application_url_not_configured"))
	}
	return sections
}

func notificationTime(now time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	return now.In(location).Format("2006-01-02 15:04:05")
}

func accountSummary(rows []Row, windowSeconds int64, languages ...i18n.Language) string {
	lang := i18n.Selected(languages)
	counts := make(map[string]int)
	active := 0
	for _, row := range rows {
		counts[row.Level]++
		if row.ActiveUsers > 0 {
			active++
		}
	}
	return i18n.M("notifications.total_accounts_active_accounts_healthy_quota_warning_exhausted_unavailable", i18n.Params{"Value1": len(rows), "Value2": activeWindowLabel(windowSeconds, lang), "Value3": active, "Value4": counts["normal"], "Value5": counts["warning"], "Value6": counts["exhausted"], "Value7": counts["unavailable"]}).Render(lang)
}

func activeWindowLabel(seconds int64, languages ...i18n.Language) string {
	lang := i18n.Selected(languages)
	if seconds <= 0 {
		seconds = int64(15 * time.Minute / time.Second)
	}
	minutes := (seconds + 59) / 60
	if minutes%60 == 0 {
		return i18n.M("admin.last_hours", i18n.Params{"Count": minutes / 60}).Render(lang)
	}
	return i18n.M("admin.last_minutes", i18n.Params{"Count": minutes}).Render(lang)
}

func accountTable(rows []Row, transitions map[string]string, previous map[string]WindowRecord, location *time.Location, now time.Time, eventsOnly bool, windowSeconds int64, languages ...i18n.Language) string {
	lang := i18n.Selected(languages)
	icons := map[string]string{"normal": "🟢", "warning": "🟠", "exhausted": "🔴", "unavailable": "⚪"}
	withChanges := eventsOnly || len(transitions) > 0
	table := []string{
		i18n.Text(lang, "notifications.account_weekly_quota_used") + activeWindowLabel(windowSeconds, lang) + i18n.Text(lang, "notifications.users_resets_remaining_next_period_reset"),
		"| :--- | ---: | ---: | ---: | :--- |",
	}
	if withChanges {
		table = []string{
			i18n.Text(lang, "notifications.account_change_weekly_quota_used") + activeWindowLabel(windowSeconds, lang) + i18n.Text(lang, "notifications.users_resets_remaining_next_period_reset"),
			"| :--- | :--- | ---: | ---: | ---: | :--- |",
		}
	}
	for _, row := range rows {
		account := safeCell(row.Account, 32)
		if !eventsOnly {
			account = icons[row.Level] + " " + account
		}
		cells := []string{account}
		if withChanges {
			cells = append(cells, accountChange(row, transitions[row.Key], previous, lang))
		}
		cells = append(cells, formatPercent(row.UsedPercent), strconv.Itoa(row.ActiveUsers),
			formatOptionalInt(row.ResetCount), formatReset(row.ResetAt, location, now, lang))
		table = append(table, "| "+strings.Join(cells, " | ")+" |")
	}
	if len(rows) == 0 {
		if withChanges {
			table = append(table, i18n.Text(lang, "notifications.no_matching_accounts"))
		} else {
			table = append(table, i18n.Text(lang, "notifications.no_matching_accounts_2"))
		}
	}
	return strings.Join(table, "\n")
}

func accountChange(row Row, event string, previous map[string]WindowRecord, languages ...i18n.Language) string {
	lang := i18n.Selected(languages)
	label := "—"
	if key := transitionLabels[event]; key != "" {
		label = i18n.Text(lang, key)
	}
	if event == "" {
		return label
	}
	if before, found := previous[row.Key]; found && row.UsedPercent != nil &&
		before.UsedPercent >= 0 && before.UsedPercent <= 100 && before.UsedPercent != *row.UsedPercent {
		separator := ": "
		if lang == i18n.Chinese {
			separator = "："
		}
		label += separator + formatPercentValue(before.UsedPercent) + " → " + formatPercent(row.UsedPercent)
	}
	return label
}

func BuildTestMarkdownV2(config Config, now time.Time) (string, error) {
	lang := config.Language
	sections := messageHeader("✅ "+config.ShortName+i18n.Text(lang, "notifications.notification_test"), config.Timezone, now, UsageCenterURL(config.PublicBaseURL), lang)
	sections = append(sections, i18n.Text(lang, "notifications.notification_type_status_channel_test_wecom_notification_channel_connected_successfully"))
	return boundedMessage(sections)
}

func boundedMessage(sections []string) (string, error) {
	content := strings.Join(sections, "\n\n")
	if len([]byte(content)) > MarkdownV2MaximumSize {
		return "", i18n.M("notifications.wecom_markdown_v2_content_exceeds_4096_bytes")
	}
	return content, nil
}

func PayloadHash(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func safeCell(value string, limit int) string {
	text := strings.TrimSpace(strings.NewReplacer("|", `\|`, "\r", " ", "\n", " ").Replace(value))
	if text == "" {
		return "—"
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:max(1, limit-1)]) + "…"
}

func formatPercent(value *float64) string {
	if value == nil {
		return "—"
	}
	return formatPercentValue(*value)
}

func formatPercentValue(value float64) string {
	rendered := strings.TrimRight(strings.TrimRight(strconv.FormatFloat(value, 'f', 2, 64), "0"), ".")
	return rendered + "%"
}

func formatOptionalInt(value *int64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatInt(*value, 10)
}

func formatReset(timestamp *int64, location *time.Location, now time.Time, languages ...i18n.Language) string {
	lang := i18n.Selected(languages)
	if timestamp == nil || *timestamp <= 0 {
		return "—"
	}
	if *timestamp <= now.Unix() {
		return i18n.Text(lang, "notifications.waiting_for_quota_update")
	}
	return time.Unix(*timestamp, 0).In(location).Format("01-02 15:04")
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func naturalCompare(left string, right string) int {
	leftParts := naturalParts(left)
	rightParts := naturalParts(right)
	for index := 0; index < min(len(leftParts), len(rightParts)); index++ {
		leftPart, rightPart := leftParts[index], rightParts[index]
		if leftPart.number && rightPart.number {
			leftNumber, _ := strconv.ParseUint(leftPart.value, 10, 64)
			rightNumber, _ := strconv.ParseUint(rightPart.value, 10, 64)
			if leftNumber < rightNumber {
				return -1
			}
			if leftNumber > rightNumber {
				return 1
			}
		} else {
			leftValue, rightValue := strings.ToLower(leftPart.value), strings.ToLower(rightPart.value)
			if leftValue < rightValue {
				return -1
			}
			if leftValue > rightValue {
				return 1
			}
		}
	}
	if len(leftParts) < len(rightParts) {
		return -1
	}
	if len(leftParts) > len(rightParts) {
		return 1
	}
	return 0
}

type naturalPart struct {
	value  string
	number bool
}

func naturalParts(value string) []naturalPart {
	runes := []rune(value)
	parts := make([]naturalPart, 0)
	for start := 0; start < len(runes); {
		digit := runes[start] >= '0' && runes[start] <= '9'
		end := start + 1
		for end < len(runes) && (runes[end] >= '0' && runes[end] <= '9') == digit {
			end++
		}
		parts = append(parts, naturalPart{value: string(runes[start:end]), number: digit})
		start = end
	}
	return parts
}
