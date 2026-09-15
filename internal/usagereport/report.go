// Package usagereport builds administrative weekly usage reports from a bounded
// usage snapshot and a current, credential-free identity catalog.
package usagereport

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
)

type Period struct {
	Start, End, WeekEnd, PreviousStart, PreviousEnd, GeneratedAt time.Time
}

// ResolvePeriod accepts a Monday date in the configured business timezone.
// For an incomplete week the comparison ends at the previous week's same local
// weekday and clock time, including weeks crossing daylight-saving changes.
func ResolvePeriod(weekStart string, now time.Time, zone *time.Location) (Period, error) {
	now = now.In(zone).Truncate(time.Second)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
	monday := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	start := monday.AddDate(0, 0, -7)
	if weekStart != "" {
		var err error
		start, err = time.ParseInLocation(time.DateOnly, weekStart, zone)
		if err != nil || start.Format(time.DateOnly) != weekStart || start.Weekday() != time.Monday ||
			start.Year() < 1970 || start.After(monday) {
			return Period{}, i18n.M("usagereport.select_a_valid_monday_no_later_than_the_current_week")
		}
	}
	end := start.AddDate(0, 0, 7)
	period := Period{Start: start, End: end, WeekEnd: end, PreviousStart: start.AddDate(0, 0, -7), PreviousEnd: start, GeneratedAt: now}
	if end.After(now) {
		period.End = now
		period.PreviousEnd = now.AddDate(0, 0, -7)
	}
	return period, nil
}

func (period Period) Partial() bool { return period.End.Before(period.WeekEnd) }

func (period Period) Filename(languages ...i18n.Language) string {
	return i18n.M("usagereport.ccpa_token_report_xlsx", i18n.Params{"Start": period.Start.Format(time.DateOnly), "End": period.WeekEnd.AddDate(0, 0, -1).Format(time.DateOnly)}).Render(i18n.Selected(languages))
}

type reportDay struct {
	previous bool
	index    int
}

func (period Period) days() ([]usage.ReportWindow, []reportDay) {
	windows := []usage.ReportWindow{}
	days := []reportDay{}
	for _, previous := range []bool{true, false} {
		start, end := period.Start, period.End
		if previous {
			start, end = period.PreviousStart, period.PreviousEnd
		}
		for index, day := 0, start; day.Before(end) && index < 7; index, day = index+1, day.AddDate(0, 0, 1) {
			next := day.AddDate(0, 0, 1)
			if next.After(end) {
				next = end
			}
			windows = append(windows, usage.ReportWindow{StartAt: day.Unix(), EndAt: next.Unix()})
			days = append(days, reportDay{previous: previous, index: index})
		}
	}
	return windows, days
}

func (period Period) Windows() []usage.ReportWindow {
	windows, _ := period.days()
	return windows
}

type Catalog struct {
	Accounts     map[string]string // CPA ID -> email, never credentials or runtime configuration
	Teams        map[string]string // team ID -> display name
	UserTeams    map[string]string // normalized email -> current team ID
	UserAccounts map[string]string // normalized email -> current route; never historical attribution
}

type Metrics struct {
	usage.WeightedMetrics
	Users, Accounts, Teams, Days int
}

type Entry struct {
	ID, Name, Team    string
	CurrentAccount    string
	BoundUsers        int
	Current, Previous Metrics
}

type Daily struct {
	Date, PreviousDate time.Time
	Included           bool
	Current, Previous  Metrics
}

type Report struct {
	Period                 Period
	Current, Previous      Metrics
	Teams, Accounts, Users []Entry
	Daily                  []Daily
}

type metricAccumulator struct {
	usage.WeightedMetrics
	users, accounts, teams map[string]struct{}
	days                   map[int]struct{}
}

func newAccumulator() *metricAccumulator {
	return &metricAccumulator{users: map[string]struct{}{}, accounts: map[string]struct{}{}, teams: map[string]struct{}{}, days: map[int]struct{}{}}
}

func (acc *metricAccumulator) add(row usage.ReportUsageRow, day int, team string) {
	m := row.Usage
	acc.RequestCount += m.RequestCount
	acc.SuccessCount += m.SuccessCount
	acc.FailedCount += m.FailedCount
	acc.InputTokens += m.InputTokens
	acc.OutputTokens += m.OutputTokens
	acc.ReasoningTokens += m.ReasoningTokens
	acc.CachedTokens += m.CachedTokens
	acc.TotalTokens += m.TotalTokens
	acc.WeightedTokens += m.WeightedTokens
	acc.LastUsedAt = max(acc.LastUsedAt, m.LastUsedAt)
	if m.RequestCount > 0 {
		if row.User != "" {
			acc.users[row.User] = struct{}{}
		}
		if row.Account != "" {
			acc.accounts[row.Account] = struct{}{}
		}
		if team != "" {
			acc.teams[team] = struct{}{}
		}
		acc.days[day] = struct{}{}
	}
}

func (acc *metricAccumulator) metrics() Metrics {
	return Metrics{WeightedMetrics: acc.WeightedMetrics, Users: len(acc.users), Accounts: len(acc.accounts), Teams: len(acc.teams), Days: len(acc.days)}
}

type entryAccumulator struct {
	entry             Entry
	current, previous *metricAccumulator
}

func ensureEntry(entries map[string]*entryAccumulator, id, name, team string) *entryAccumulator {
	if entry, ok := entries[id]; ok {
		return entry
	}
	entry := &entryAccumulator{entry: Entry{ID: id, Name: name, Team: team}, current: newAccumulator(), previous: newAccumulator()}
	entries[id] = entry
	return entry
}

func sortedEntries(entries map[string]*entryAccumulator) []Entry {
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		entry.entry.Current, entry.entry.Previous = entry.current.metrics(), entry.previous.metrics()
		result = append(result, entry.entry)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Current.TotalTokens != result[j].Current.TotalTokens {
			return result[i].Current.TotalTokens > result[j].Current.TotalTokens
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// Build reconciles every row into all three dimensions, including unknown or
// removed users/accounts. Both comparison periods use the same current catalog.
func Build(period Period, catalog Catalog, rows []usage.ReportUsageRow, languages ...i18n.Language) (Report, error) {
	if len(catalog.Teams) > 20_000 || len(catalog.Accounts) > 20_000 || len(catalog.UserTeams) > 20_000 {
		return Report{}, usage.ErrReportTooLarge
	}
	report := Report{Period: period}
	_, days := period.days()
	current, previous := newAccumulator(), newAccumulator()
	currentDays, previousDays := [7]*metricAccumulator{}, [7]*metricAccumulator{}
	for i := range 7 {
		currentDays[i], previousDays[i] = newAccumulator(), newAccumulator()
	}
	teams, accounts, users := map[string]*entryAccumulator{}, map[string]*entryAccumulator{}, map[string]*entryAccumulator{}
	teamFor := func(user string) (string, string) {
		id := catalog.UserTeams[user]
		if name, found := catalog.Teams[id]; found && id != "" {
			return id, name
		}
		return "", i18n.Text(i18n.Selected(languages), "usagereport.unassigned")
	}
	for id, name := range catalog.Teams {
		ensureEntry(teams, id, name, "")
	}
	for id, email := range catalog.Accounts {
		ensureEntry(accounts, id, email, "")
	}
	for user := range catalog.UserTeams {
		_, name := teamFor(user)
		ensureEntry(users, user, user, name)
	}
	for _, row := range rows {
		if row.Window < 0 || row.Window >= len(days) {
			return Report{}, errors.New("report row has invalid day")
		}
		row.User, row.Account = strings.ToLower(strings.TrimSpace(row.User)), strings.TrimSpace(row.Account)
		day := days[row.Window]
		teamID, teamName := teamFor(row.User)
		accountName := catalog.Accounts[row.Account]
		userName := row.User
		if userName == "" {
			userName = i18n.Text(i18n.Selected(languages), "usagereport.unidentified_user")
		}
		entries := []*entryAccumulator{
			ensureEntry(teams, teamID, teamName, ""),
			ensureEntry(accounts, row.Account, accountName, ""),
			ensureEntry(users, row.User, userName, teamName),
		}
		if len(teams) > 20_000 || len(accounts) > 20_000 || len(users) > 20_000 {
			return Report{}, usage.ErrReportTooLarge
		}
		if day.previous {
			previous.add(row, day.index, teamID)
			previousDays[day.index].add(row, day.index, teamID)
			for _, entry := range entries {
				entry.previous.add(row, day.index, teamID)
			}
		} else {
			current.add(row, day.index, teamID)
			currentDays[day.index].add(row, day.index, teamID)
			for _, entry := range entries {
				entry.current.add(row, day.index, teamID)
			}
		}
	}
	if len(teams) > 20_000 || len(accounts) > 20_000 || len(users) > 20_000 {
		return Report{}, usage.ErrReportTooLarge
	}
	// Current bindings are informational and never reattribute historical usage.
	for user := range catalog.UserTeams {
		accountID := catalog.UserAccounts[user]
		users[user].entry.CurrentAccount = accountID
		if accountID != "" {
			if account, ok := accounts[accountID]; ok {
				account.entry.BoundUsers++
			}
		}
	}
	report.Current, report.Previous = current.metrics(), previous.metrics()
	report.Teams, report.Accounts, report.Users = sortedEntries(teams), sortedEntries(accounts), sortedEntries(users)
	for i := range 7 {
		date := period.Start.AddDate(0, 0, i)
		report.Daily = append(report.Daily, Daily{Date: date, PreviousDate: period.PreviousStart.AddDate(0, 0, i), Included: date.Before(period.End), Current: currentDays[i].metrics(), Previous: previousDays[i].metrics()})
	}
	return report, nil
}
