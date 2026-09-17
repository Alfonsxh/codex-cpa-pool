package notifications

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"golang.org/x/sync/errgroup"
)

type Worker struct {
	Store    Store
	Activity ActivityProvider
	Sender   ContentSender
	Now      func() time.Time
}

func (worker *Worker) RunOnce(ctx context.Context) (RunResult, error) {
	if worker.Store == nil {
		return RunResult{}, errors.New("notification worker requires a control-plane store")
	}
	settings, err := worker.Store.ReadSettings(ctx)
	if err != nil {
		return RunResult{}, fmt.Errorf("read notification settings: %w", err)
	}
	config, err := ParseConfig(settings)
	if err != nil {
		return RunResult{}, err
	}
	state, _, err := ReadRuntimeState(ctx, worker.Store)
	if err != nil {
		return RunResult{}, err
	}
	now := time.Now
	if worker.Now != nil {
		now = worker.Now
	}
	nowTime := now()
	nowUnix := nowTime.Unix()
	state.HeartbeatAt = int64Pointer(nowUnix)
	state.Scheduled = pruneScheduled(state.Scheduled, nowUnix)
	result := RunResult{Sent: make([]string, 0), Enabled: config.Enabled}
	configured := false
	if worker.Sender != nil {
		configured, err = worker.Sender.Configured(ctx)
		if err != nil {
			return result, worker.recordError(ctx, state, err)
		}
	}
	if !config.Enabled || !configured {
		state.NextScheduleAt = nil
		if err := worker.patchState(ctx, state,
			"heartbeat_at", "scheduled", "next_schedule_at",
		); err != nil {
			return result, err
		}
		result.Enabled = false
		return result, nil
	}
	if worker.Activity == nil || worker.Sender == nil {
		return result, worker.recordError(ctx, state, errors.New("notification worker dependencies are incomplete"))
	}
	state.NextScheduleAt = NextScheduleAt(nowTime.In(config.Timezone), config.DailyTimes)
	dueKeys := dueScheduleKeys(nowTime, config, state.Scheduled)
	lastQuotaCheck := int64Value(state.QuotaCheckedAt)
	quotaCheckDue := lastQuotaCheck == 0 || nowUnix >= lastQuotaCheck+int64(config.QuotaCheckInterval/time.Second)
	evaluateQuota := quotaCheckDue || len(dueKeys) > 0
	if len(dueKeys) == 0 && !evaluateQuota {
		if err := worker.patchState(ctx, state,
			"heartbeat_at", "scheduled", "next_schedule_at",
		); err != nil {
			return result, err
		}
		return result, nil
	}

	snapshot, err := CollectSnapshot(ctx, worker.Store, worker.Activity)
	if err != nil {
		return result, worker.recordError(ctx, state, err)
	}
	threshold := config.ThresholdPercent
	quotaAccounts := make(map[string]struct{}, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		if account.Enabled && !strings.EqualFold(strings.TrimSpace(account.StateReason), "account_disabled") {
			quotaAccounts[account.ID] = struct{}{}
		}
	}
	previousAlerts := make(map[string]AlertRecord)
	for key, value := range state.QuotaAlerts {
		if value.AlertedAt != nil && *value.AlertedAt > 0 {
			previousAlerts[key] = value
		}
	}
	previousWindows := state.QuotaWindows
	currentSignals := make(map[string]Row)
	transitionEvents := make(map[string]string)
	weeklyRefreshDetected := false
	if evaluateQuota {
		for _, row := range QuotaRows(snapshot, threshold, nil) {
			switch row.Level {
			case "disabled", "stopped", "unauthorized", "credential_unavailable", "degraded", "reserved":
				continue
			}
			currentSignals[row.Key] = row
		}
		for key, row := range currentSignals {
			if config.QuotaAlertEnabled {
				previousLevel := previousAlerts[key].Level
				switch row.Level {
				case "warning":
					if previousLevel == "exhausted" {
						transitionEvents[key] = "recovered_warning"
					} else if previousLevel != "warning" && previousLevel != "exhausted" {
						transitionEvents[key] = "warning"
					}
				case "exhausted":
					if previousLevel != "exhausted" {
						transitionEvents[key] = "exhausted"
					}
				case "normal":
					if previousLevel == "warning" || previousLevel == "exhausted" {
						transitionEvents[key] = "recovered"
					}
				}
			}
			previous, found := previousWindows[key]
			// A percentage drop alone is not proof of a new weekly cycle. Old
			// records without ResetAt establish a baseline on their next read.
			if row.Level == "normal" && found && previous.ResetAt != nil && row.ResetAt != nil &&
				*previous.ResetAt > 0 && *previous.ResetAt <= nowUnix &&
				*row.ResetAt > nowUnix && *row.ResetAt > *previous.ResetAt {
				transitionEvents[key] = "refreshed"
				weeklyRefreshDetected = true
			}
		}
		updatedWindows := make(map[string]WindowRecord)
		for key, value := range previousWindows {
			if _, found := quotaAccounts[accountFromSignalKey(key)]; found && regularSignalKey(key) {
				updatedWindows[key] = value
			}
		}
		for key, row := range currentSignals {
			if row.Level != "unavailable" && row.UsedPercent != nil {
				updatedWindows[key] = WindowRecord{UsedPercent: *row.UsedPercent, ObservedAt: nowUnix, ResetAt: row.ResetAt}
			}
		}
		state.QuotaWindows = updatedWindows
		state.QuotaCheckedAt = int64Pointer(nowUnix)
	}

	scheduledSent := false
	if len(dueKeys) > 0 {
		content, buildError := BuildMarkdownV2(
			snapshot, config.ShortName+i18n.Text(config.Language, "admin.account_quota_report"), config.Timezone,
			threshold, nowTime, nil, transitionEvents, UsageCenterURL(config.PublicBaseURL),
			ReportOptions{PreviousWindows: previousWindows, Language: config.Language},
		)
		if buildError != nil {
			return result, worker.recordError(ctx, state, buildError)
		}
		if _, sendError := worker.Sender.Send(ctx, content); sendError != nil {
			return result, worker.recordError(ctx, state, sendError)
		}
		payloadHash := PayloadHash(content)
		for _, key := range dueKeys {
			state.Scheduled[key] = ScheduledRecord{SentAt: nowUnix, PayloadHash: payloadHash}
		}
		result.Sent = append(result.Sent, "scheduled")
		scheduledSent = true
	}
	if len(transitionEvents) > 0 && !scheduledSent {
		title, label := transitionTitle(config.ShortName, transitionEvents, config.Language)
		var onlyKeys map[string]struct{}
		if !weeklyRefreshDetected {
			onlyKeys = make(map[string]struct{}, len(transitionEvents))
			for key := range transitionEvents {
				onlyKeys[key] = struct{}{}
			}
		}
		content, buildError := BuildMarkdownV2(
			snapshot, title, config.Timezone, threshold, nowTime,
			onlyKeys, transitionEvents, UsageCenterURL(config.PublicBaseURL),
			ReportOptions{PreviousWindows: previousWindows, Language: config.Language},
		)
		if buildError != nil {
			return result, worker.recordError(ctx, state, buildError)
		}
		if _, sendError := worker.Sender.Send(ctx, content); sendError != nil {
			return result, worker.recordError(ctx, state, sendError)
		}
		result.Sent = append(result.Sent, label)
	}
	if config.QuotaAlertEnabled && evaluateQuota {
		updatedAlerts := make(map[string]AlertRecord)
		for key, value := range previousAlerts {
			if _, found := quotaAccounts[accountFromSignalKey(key)]; found && regularSignalKey(key) {
				updatedAlerts[key] = value
			}
		}
		for key, row := range currentSignals {
			switch row.Level {
			case "normal":
				delete(updatedAlerts, key)
			case "warning", "exhausted":
				previous, found := previousAlerts[key]
				if found {
					previous.ResetAt = row.ResetKey
					previous.Level = row.Level
					previous.Threshold = threshold
					if _, transitioned := transitionEvents[key]; transitioned {
						previous.TransitionedAt = int64Pointer(nowUnix)
					}
					updatedAlerts[key] = previous
				} else if _, transitioned := transitionEvents[key]; transitioned &&
					(scheduledSent || len(result.Sent) > 0) {
					updatedAlerts[key] = AlertRecord{
						ResetAt: row.ResetKey, Level: row.Level, Threshold: threshold,
						AlertedAt: int64Pointer(nowUnix), TransitionedAt: int64Pointer(nowUnix),
					}
				}
			}
		}
		state.QuotaAlerts = updatedAlerts
	}
	if len(result.Sent) > 0 {
		state.LastSuccessAt = int64Pointer(nowUnix)
	}
	state.LastError = ""
	state.LastErrorMessage = nil
	fields := []string{
		"heartbeat_at", "scheduled", "next_schedule_at", "quota_windows",
		"quota_checked_at", "quota_alerts", "last_error", "last_error_message",
	}
	if len(result.Sent) > 0 {
		fields = append(fields, "last_success_at")
	}
	if err := worker.patchState(ctx, state, fields...); err != nil {
		return result, err
	}
	return result, nil
}

func CollectSnapshot(
	ctx context.Context,
	store SnapshotStore,
	activityProvider ActivityProvider,
) (Snapshot, error) {
	if store == nil || activityProvider == nil {
		return Snapshot{}, errors.New("notification snapshot dependencies are incomplete")
	}
	if reader, ok := activityProvider.(interface{ SetActiveUserWindow(time.Duration) }); ok {
		if settingsStore, settingsOK := store.(interface {
			ReadSettings(context.Context) (map[string]any, error)
		}); settingsOK {
			settings, err := settingsStore.ReadSettings(ctx)
			if err == nil {
				reader.SetActiveUserWindow(usage.ActiveUserWindowFromSettings(settings))
			}
		}
	}
	var (
		accounts         []controlplane.Account
		failoverState    failover.RuntimeState
		quotaState       quota.RuntimeState
		activity         map[string]int
		failoverStateSet bool
		quotaStateSet    bool
	)
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		accounts, err = store.ReadAccounts(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		quotaState, quotaStateSet, err = quota.ReadState(groupContext, store)
		return err
	})
	group.Go(func() error {
		var err error
		failoverState, failoverStateSet, err = failover.ReadRuntimeState(groupContext, store)
		return err
	})
	group.Go(func() error {
		var err error
		if configured, ok := activityProvider.(interface {
			RefreshActiveUsers(context.Context) (map[string]int, error)
		}); ok {
			activity, err = configured.RefreshActiveUsers(groupContext)
		} else {
			activity, err = activityProvider.RefreshActiveUsersLastHour(groupContext)
		}
		return err
	})
	if err := group.Wait(); err != nil {
		return Snapshot{}, fmt.Errorf("collect notification snapshot: %w", err)
	}
	quotaByAccount := make(map[string]quota.AccountQuota)
	if quotaStateSet {
		for _, account := range quotaState.Snapshot.Accounts {
			quotaByAccount[account.Account] = account
		}
	}
	reasonByAccount := make(map[string]string)
	if failoverStateSet && failoverState.Mode == failover.ModeActive {
		for account, state := range failoverState.Accounts {
			reasonByAccount[account] = state.Reason
		}
	}
	windowSeconds := int64(900)
	if reader, ok := activityProvider.(interface{ ActiveUserWindow() time.Duration }); ok {
		windowSeconds = int64(reader.ActiveUserWindow() / time.Second)
	}
	result := Snapshot{Accounts: make([]AccountSnapshot, 0, len(accounts)), ActiveUserWindowSeconds: windowSeconds}
	for _, account := range accounts {
		accountQuota, found := quotaByAccount[account.ID]
		if !found {
			accountQuota = quota.AccountQuota{Account: account.ID, Status: "unavailable", WeeklyWindows: []quota.WeeklyWindow{}}
		}
		result.Accounts = append(result.Accounts, AccountSnapshot{
			ID: account.ID, Enabled: account.GroupEnabled, StateReason: reasonByAccount[account.ID],
			ActiveUsers1H: activity[account.ID], Quota: accountQuota,
		})
	}
	return result, nil
}

func (worker *Worker) recordError(ctx context.Context, state RuntimeState, runError error) error {
	state.LastError, state.LastErrorMessage = FailureRecord(runError)
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if writeError := worker.patchState(
		finalizeContext,
		state,
		"heartbeat_at", "scheduled", "next_schedule_at", "quota_windows",
		"quota_checked_at", "last_error", "last_error_message",
	); writeError != nil {
		return errors.Join(runError, writeError)
	}
	return runError
}

func (worker *Worker) patchState(ctx context.Context, state RuntimeState, fields ...string) error {
	state.Version = RuntimeStateVersion
	if state.Scheduled == nil {
		state.Scheduled = make(map[string]ScheduledRecord)
	}
	if state.QuotaAlerts == nil {
		state.QuotaAlerts = make(map[string]AlertRecord)
	}
	if state.QuotaWindows == nil {
		state.QuotaWindows = make(map[string]WindowRecord)
	}
	values := map[string]any{"version": state.Version}
	for _, field := range fields {
		switch field {
		case "scheduled":
			values[field] = state.Scheduled
		case "quota_alerts":
			values[field] = state.QuotaAlerts
		case "quota_windows":
			values[field] = state.QuotaWindows
		case "heartbeat_at":
			values[field] = state.HeartbeatAt
		case "last_success_at":
			values[field] = state.LastSuccessAt
		case "last_error":
			values[field] = state.LastError
		case "last_error_message":
			values[field] = state.LastErrorMessage
		case "next_schedule_at":
			values[field] = state.NextScheduleAt
		case "quota_checked_at":
			values[field] = state.QuotaCheckedAt
		default:
			return fmt.Errorf("unknown notification runtime state field %q", field)
		}
	}
	if err := worker.Store.PatchRuntimeState(ctx, RuntimeStateName, values); err != nil {
		return fmt.Errorf("patch notification runtime state: %w", err)
	}
	return nil
}

func dueScheduleKeys(now time.Time, config Config, scheduled map[string]ScheduledRecord) []string {
	local := now.In(config.Timezone)
	grace := max(config.ScheduleGrace, time.Minute)
	result := make([]string, 0)
	for _, clock := range config.DailyTimes {
		due := time.Date(local.Year(), local.Month(), local.Day(), clock.Hour, clock.Minute, 0, 0, config.Timezone)
		key := config.TimezoneName + "|" + local.Format("2006-01-02") + "@" + clock.String()
		if !now.Before(due) && now.Before(due.Add(grace)) {
			if _, found := scheduled[key]; !found {
				result = append(result, key)
			}
		}
	}
	sort.Strings(result)
	return result
}

// NextScheduleAt returns the next configured daily time in the supplied timezone.
func NextScheduleAt(local time.Time, clocks []ClockTime) *int64 {
	var next time.Time
	for dayOffset := 0; dayOffset <= 1; dayOffset++ {
		date := local.AddDate(0, 0, dayOffset)
		for _, clock := range clocks {
			candidate := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour, clock.Minute, 0, 0, local.Location())
			if candidate.After(local) && (next.IsZero() || candidate.Before(next)) {
				next = candidate
			}
		}
	}
	if next.IsZero() {
		return nil
	}
	return int64Pointer(next.Unix())
}

func pruneScheduled(values map[string]ScheduledRecord, now int64) map[string]ScheduledRecord {
	cutoff := now - 14*24*60*60
	result := make(map[string]ScheduledRecord)
	for key, value := range values {
		if value.SentAt >= cutoff {
			result[key] = value
		}
	}
	return result
}

func regularSignalKey(key string) bool {
	_, window, found := strings.Cut(key, "|")
	return found && strings.HasPrefix(strings.ToLower(window), "default:")
}

func transitionTitle(shortName string, transitions map[string]string, languages ...i18n.Language) (string, string) {
	types := make(map[string]struct{})
	for _, value := range transitions {
		types[value] = struct{}{}
	}
	switch {
	case reflect.DeepEqual(types, map[string]struct{}{"warning": {}}):
		return "🟠 " + shortName + i18n.Text(i18n.Selected(languages), "notifications.weekly_quota_warning"), "quota_alert"
	case reflect.DeepEqual(types, map[string]struct{}{"exhausted": {}}):
		return "🔴 " + shortName + i18n.Text(i18n.Selected(languages), "notifications.weekly_quota_exhausted"), "quota_exhausted"
	case reflect.DeepEqual(types, map[string]struct{}{"recovered_warning": {}}):
		return "🟠 " + shortName + i18n.Text(i18n.Selected(languages), "notifications.quota_recovered_still_within_the_warning_range_2"), "quota_recovered"
	case subset(types, "recovered", "recovered_warning"):
		return "🟢 " + shortName + i18n.Text(i18n.Selected(languages), "notifications.quota_recovered_2"), "quota_recovered"
	case reflect.DeepEqual(types, map[string]struct{}{"refreshed": {}}):
		return "🔄 " + shortName + i18n.Text(i18n.Selected(languages), "notifications.weekly_quota_reset_2"), "quota_refreshed"
	default:
		return shortName + i18n.Text(i18n.Selected(languages), "notifications.account_quota_changed"), "quota_transition"
	}
}

func subset(values map[string]struct{}, allowed ...string) bool {
	if len(values) == 0 {
		return false
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	for value := range values {
		if _, found := allowedSet[value]; !found {
			return false
		}
	}
	return true
}

func accountFromSignalKey(key string) string {
	account, _, _ := strings.Cut(key, "|")
	return account
}

func int64Pointer(value int64) *int64 {
	return &value
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func boundedError(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}
