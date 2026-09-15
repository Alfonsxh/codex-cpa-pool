package notifications

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"

	_ "time/tzdata"

	"github.com/Alfonsxh/codex-cpa-pool/internal/sitetime"
)

func ParseConfig(settings map[string]any) (Config, error) {
	timezoneName, err := sitetime.Name(settings)
	if err != nil {
		return Config{}, err
	}
	location, err := sitetime.Validate(timezoneName)
	if err != nil {
		return Config{}, i18n.M("notifications.invalid_notification_timezone", i18n.Params{"Detail": err}).WithCause(err)
	}
	dailyTimes, err := ParseClockTimes(stringSetting(
		settings["notification.daily_times"], "09:00,14:00,18:00",
	))
	if err != nil {
		return Config{}, err
	}
	graceMinutes := numberSetting(settings["notification.schedule_grace_minutes"], 15)
	if graceMinutes < 0 || graceMinutes > 120 {
		graceMinutes = 15
	}
	threshold := numberSetting(settings["notification.weekly_threshold_percent"], 90)
	if threshold < 1 || threshold > 100 {
		threshold = 90
	}
	quotaSeconds := numberSetting(settings["usage.quota_cache_seconds"], 60)
	if quotaSeconds < 30 || quotaSeconds > 3600 {
		quotaSeconds = 60
	}
	return Config{
		Language: i18n.FromSettings(settings),
		Enabled:  boolSetting(settings["notification.enabled"], false),
		Timezone: location, TimezoneName: timezoneName, DailyTimes: dailyTimes,
		ScheduleGrace:      time.Duration(graceMinutes * float64(time.Minute)),
		QuotaAlertEnabled:  boolSetting(settings["notification.quota_alert_enabled"], true),
		ThresholdPercent:   threshold,
		QuotaCheckInterval: time.Duration(quotaSeconds * float64(time.Second)),
		ShortName:          stringSetting(settings["branding.short_name"], "CCPA"),
		PublicBaseURL:      stringSetting(settings["branding.public_base_url"], ""),
	}, nil
}

func ParseClockTimes(value string) ([]ClockTime, error) {
	seen := make(map[string]ClockTime)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.Split(item, ":")
		if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
			return nil, i18n.M("notifications.notification_times_must_use_hh_mm")
		}
		hour, hourError := strconv.Atoi(parts[0])
		minute, minuteError := strconv.Atoi(parts[1])
		if hourError != nil || minuteError != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
			return nil, i18n.M("notifications.notification_times_must_use_hh_mm")
		}
		clock := ClockTime{Hour: hour, Minute: minute}
		seen[clock.String()] = clock
	}
	if len(seen) == 0 {
		return nil, i18n.M("notifications.at_least_one_notification_time_is_required")
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]ClockTime, 0, len(keys))
	for _, key := range keys {
		result = append(result, seen[key])
	}
	return result, nil
}

func stringSetting(value any, fallback string) string {
	result, found := value.(string)
	if !found || strings.TrimSpace(result) == "" {
		return fallback
	}
	return strings.TrimSpace(result)
}

func boolSetting(value any, fallback bool) bool {
	result, found := value.(bool)
	if !found {
		return fallback
	}
	return result
}

func numberSetting(value any, fallback float64) float64 {
	var result float64
	switch typed := value.(type) {
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int:
		result = float64(typed)
	case int64:
		result = float64(typed)
	default:
		return fallback
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return fallback
	}
	return result
}
