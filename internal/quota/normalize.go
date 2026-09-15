package quota

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

// An additional window that upstream left unnamed keeps its position in the key,
// which lets presentation rebuild the authored label for the response language.
const (
	additionalWindowKeyPrefix         = "additional:"
	unnamedAdditionalWindowIdentifier = "additional-"
)

type quotaSource struct {
	key              string
	label            string
	meteredFeature   *string
	rateLimit        map[string]any
	reachedTypeAlias map[string]struct{}
}

func Normalize(account string, payload map[string]any) AccountQuota {
	result := unavailableAccount(account, "weekly_unavailable")
	if planType, ok := payload["plan_type"].(string); ok {
		planType = strings.TrimSpace(planType)
		if planType != "" {
			result.PlanType = &planType
		}
	}
	defaultRateLimit := object(payload["rate_limit"])
	result.Allowed = boolean(defaultRateLimit["allowed"])
	result.LimitReached = boolean(defaultRateLimit["limit_reached"])
	resetCredits := object(payload["rate_limit_reset_credits"])
	result.ResetCreditCount = nonnegativeInt(resetCredits["available_count"])
	applicable := nonnegativeInt(resetCredits["applicable_available_count"])
	reachedDetails := strings.ToLower(strings.TrimSpace(stringValue(object(payload["rate_limit_reached_type"])["details"])))

	sources := []quotaSource{newQuotaSource("default", i18n.Text(i18n.English, "notifications.weekly_limit"), nil, defaultRateLimit, "default")}
	for index, raw := range list(payload["additional_rate_limits"]) {
		item := object(raw)
		if item == nil {
			continue
		}
		limitName := strings.TrimSpace(stringValue(item["limit_name"]))
		metered := strings.TrimSpace(stringValue(item["metered_feature"]))
		identifier := metered
		if identifier == "" {
			identifier = limitName
		}
		label := limitName
		if label == "" {
			label = metered
		}
		if identifier == "" {
			identifier = unnamedAdditionalWindowIdentifier + strconv.Itoa(index+1)
			label = i18n.Text(i18n.English, "quota.additional_weekly_limit") + strconv.Itoa(index+1)
		}
		var meteredPointer *string
		if metered != "" {
			value := metered
			meteredPointer = &value
		}
		sources = append(sources, newQuotaSource(
			additionalWindowKeyPrefix+identifier, label, meteredPointer, object(item["rate_limit"]),
			identifier, limitName, metered,
		))
	}

	for _, source := range sources {
		if source.rateLimit == nil {
			continue
		}
		limitReached := boolValue(source.rateLimit["limit_reached"], false)
		for _, slot := range []string{"primary_window", "secondary_window"} {
			window := object(source.rateLimit[slot])
			windowSeconds := nonnegativeInt(window["limit_window_seconds"])
			if windowSeconds == nil || *windowSeconds != WeeklyWindowSeconds {
				continue
			}
			used := numberValue(window["used_percent"], 0)
			used = math.Max(0, math.Min(used, 100))
			reported := round2(used)
			effective := reported
			if limitReached {
				effective = 100
			}
			_, resettableSource := source.reachedTypeAlias[reachedDetails]
			// A depleted weekly window can request a reset even when upstream
			// applicability hints disagree. Keep explicit upstream
			// eligibility for windows whose percentage has not reached 100 yet.
			resettable := reported >= 100 ||
				(applicable != nil && *applicable > 0 && limitReached && resettableSource)
			result.WeeklyWindows = append(result.WeeklyWindows, WeeklyWindow{
				Key: source.key + ":" + slot, Label: source.label,
				MeteredFeature: source.meteredFeature, WindowSlot: slot,
				UsedPercent: effective, RemainingPercent: round2(100 - effective),
				ReportedUsedPercent: reported, ResetAt: nonnegativeInt(window["reset_at"]),
				ResetAfterSeconds: nonnegativeInt(window["reset_after_seconds"]),
				WindowSeconds:     *windowSeconds, LimitReached: limitReached,
				Resettable: resettable,
			})
		}
	}
	if len(result.WeeklyWindows) > 0 {
		result.Status = "ok"
		weekly := result.WeeklyWindows[0]
		result.Weekly = &weekly
	}
	return result
}

func newQuotaSource(
	key string,
	label string,
	meteredFeature *string,
	rateLimit map[string]any,
	aliases ...string,
) quotaSource {
	aliasSet := make(map[string]struct{})
	for _, alias := range aliases {
		if alias = strings.ToLower(strings.TrimSpace(alias)); alias != "" {
			aliasSet[alias] = struct{}{}
		}
	}
	return quotaSource{
		key: key, label: label, meteredFeature: meteredFeature,
		rateLimit: rateLimit, reachedTypeAlias: aliasSet,
	}
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func list(value any) []any {
	result, _ := value.([]any)
	return result
}

func boolean(value any) *bool {
	result, ok := value.(bool)
	if !ok {
		return nil
	}
	return &result
}

func boolValue(value any, fallback bool) bool {
	if result := boolean(value); result != nil {
		return *result
	}
	return fallback
}

func nonnegativeInt(value any) *int64 {
	if value == nil {
		return nil
	}
	var result int64
	switch typed := value.(type) {
	case bool:
		return nil
	case json.Number:
		parsed, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil
		}
		result = int64(parsed)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil
		}
		result = int64(typed)
	case float32:
		result = int64(typed)
	case int:
		result = int64(typed)
	case int64:
		result = typed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil
		}
		result = int64(parsed)
	default:
		return nil
	}
	if result < 0 {
		result = 0
	}
	return &result
}

func numberValue(value any, fallback float64) float64 {
	var result float64
	switch typed := value.(type) {
	case bool:
		return fallback
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return fallback
		}
		result = parsed
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int:
		result = float64(typed)
	case int64:
		result = float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return fallback
		}
		result = parsed
	default:
		return fallback
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return fallback
	}
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func round2(value float64) float64 {
	result := math.Round(value*100) / 100
	if result == 0 {
		return 0
	}
	return result
}
