package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/sitetime"
	"github.com/gin-gonic/gin"
)

const (
	onboardingVersion          = 1
	onboardingSkippedSetting   = "onboarding.skipped_recommended"
	onboardingRequiredKind     = "required"
	onboardingRecommendedKind  = "recommended"
	onboardingCompleteStatus   = "complete"
	onboardingIncompleteStatus = "incomplete"
	onboardingSkippedStatus    = "skipped"
)

var onboardingRecommendedIDs = []string{
	"public_base_url",
	"quota_timezone",
	"weekly_quota",
	"notifications",
	"branding",
	"proxy",
}

type onboardingStep struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Status      string   `json:"status"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	ActionPath  string   `json:"action_path"`
	Blockers    []string `json:"blockers"`
}

type onboardingRequiredProgress struct {
	Complete int `json:"complete"`
	Total    int `json:"total"`
}

type onboardingRecommendedProgress struct {
	Complete int `json:"complete"`
	Skipped  int `json:"skipped"`
	Total    int `json:"total"`
}

type onboardingStatusResponse struct {
	Version            int                           `json:"version"`
	GeneratedAt        int64                         `json:"generated_at"`
	RequiredComplete   bool                          `json:"required_complete"`
	Required           onboardingRequiredProgress    `json:"required"`
	Recommended        onboardingRecommendedProgress `json:"recommended"`
	SkippedRecommended []string                      `json:"skipped_recommended"`
	Steps              []onboardingStep              `json:"steps"`
}

type onboardingPreferencesPayload struct {
	Confirm            string   `json:"confirm"`
	SkippedRecommended []string `json:"skipped_recommended"`
}

func (server *Server) readOnboarding(c *gin.Context) {
	payload, err := server.onboardingStatus(c.Request.Context())
	if err != nil {
		server.internalError(c, "read onboarding status", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, payload)
}

func (server *Server) updateOnboardingPreferences(c *gin.Context) {
	var body onboardingPreferencesPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_saving_setup_preferences"), "invalid_request")
		return
	}
	skipped, err := normalizeSkippedRecommended(body.SkippedRecommended)
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	if err := server.store.UpdateSettings(c.Request.Context(), map[string]any{
		onboardingSkippedSetting: skipped,
	}); err != nil {
		server.internalError(c, "update onboarding preferences", err)
		return
	}
	payload, err := server.onboardingStatus(c.Request.Context())
	if err != nil {
		server.internalError(c, "read updated onboarding status", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, payload)
}

func (server *Server) onboardingStatus(ctx context.Context) (onboardingStatusResponse, error) {
	settings, err := server.store.ReadSettings(ctx)
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding settings: %w", err)
	}
	secretStatuses, err := server.store.SecretStatuses(ctx)
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding secret statuses: %w", err)
	}
	values, err := generalSettingsFromMap(settings)
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding general settings: %w", err)
	}
	skipped, err := skippedRecommendedFromSettings(settings)
	if err != nil {
		return onboardingStatusResponse{}, err
	}
	_, customLogo, err := server.store.ReadBrandingAsset(ctx, "logo")
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding branding status: %w", err)
	}

	required := make([]onboardingStep, 0, 2)
	emailDomainsComplete := len(values.AllowedEmailDomains) > 0
	required = append(required, onboardingRequiredStep(
		"email_domains", emailDomainsComplete, i18n.Text(i18n.FromContext(ctx), "admin.organization_access"), i18n.Text(i18n.FromContext(ctx), "admin.configure_email_domains_allowed_for_user_creation_and_sign_in"),
		"/configuration?section=identity&key=identity.allowed_email_domains", nil,
	))
	initialPasswordComplete := hasSecretStatus(secretStatuses, portalInitialPasswordSecret)
	required = append(required, onboardingRequiredStep(
		"initial_password", initialPasswordComplete, i18n.Text(i18n.FromContext(ctx), "admin.initial_user_password"), i18n.Text(i18n.FromContext(ctx), "admin.used_for_new_users_first_sign_in_must_be_changed"),
		"/configuration?section=access", nil,
	))

	recommendedConfigured := map[string]bool{
		"public_base_url": strings.TrimSpace(values.PublicBaseURL) != "",
		"quota_timezone":  configuredNonEmptyString(settings, sitetime.SettingKey) || configuredNonEmptyString(settings, "user_quota.timezone") || configuredNonEmptyString(settings, "notification.timezone"),
		"weekly_quota":    configuredPositiveNumber(settings, "user_quota.default_weekly_tokens"),
		"notifications":   hasSecretStatus(secretStatuses, "wecom_webhook"),
		"branding":        customLogo || brandingCustomized(values),
		"proxy":           settingBool(settings, "cpa.proxy_enabled") && hasSecretStatus(secretStatuses, defaultProxySecretName),
	}
	recommendedDefinitions := []onboardingStep{
		{ID: "public_base_url", Kind: onboardingRecommendedKind, Title: i18n.Text(i18n.FromContext(ctx), "admin.public_url"), Description: i18n.Text(i18n.FromContext(ctx), "admin.used_for_notifications_and_client_exports_if_blank_the_browser"), ActionPath: "/configuration?section=brand&key=branding.public_base_url"},
		{ID: "quota_timezone", Kind: onboardingRecommendedKind, Title: i18n.Text(i18n.FromContext(ctx), "admin.system_timezone"), Description: i18n.Text(i18n.FromContext(ctx), "admin.shared_timezone_for_page_times_usage_calendar_week_quotas_and"), ActionPath: "/configuration?section=general&key=system.timezone"},
		{ID: "weekly_quota", Kind: onboardingRecommendedKind, Title: i18n.Text(i18n.FromContext(ctx), "admin.default_weekly_quota"), Description: i18n.Text(i18n.FromContext(ctx), "admin.set_an_organization_wide_default_token_limit_for_new_users"), ActionPath: "/configuration?section=quota&key=user_quota.default_weekly_tokens"},
		{ID: "notifications", Kind: onboardingRecommendedKind, Title: i18n.Text(i18n.FromContext(ctx), "admin.wecom_notifications"), Description: i18n.Text(i18n.FromContext(ctx), "admin.configure_a_webhook_for_quota_reports_and_error_alerts"), ActionPath: "/configuration?section=notifications"},
		{ID: "branding", Kind: onboardingRecommendedKind, Title: i18n.Text(i18n.FromContext(ctx), "admin.branding"), Description: i18n.Text(i18n.FromContext(ctx), "admin.customize_the_product_name_environment_label_and_logo_as_needed"), ActionPath: "/configuration?section=brand"},
		{ID: "proxy", Kind: onboardingRecommendedKind, Title: i18n.Text(i18n.FromContext(ctx), "admin.default_upstream_proxy"), Description: i18n.Text(i18n.FromContext(ctx), "admin.configure_only_when_a_proxy_is_required_to_reach_the"), ActionPath: "/configuration?section=requests&key=cpa.proxy_url"},
	}
	skippedSet := make(map[string]struct{}, len(skipped))
	for _, id := range skipped {
		skippedSet[id] = struct{}{}
	}
	recommendedProgress := onboardingRecommendedProgress{Total: len(recommendedDefinitions)}
	for index := range recommendedDefinitions {
		step := &recommendedDefinitions[index]
		step.Blockers = make([]string, 0)
		switch {
		case recommendedConfigured[step.ID]:
			step.Status = onboardingCompleteStatus
			recommendedProgress.Complete++
		case hasString(skippedSet, step.ID):
			step.Status = onboardingSkippedStatus
			recommendedProgress.Skipped++
		default:
			step.Status = onboardingIncompleteStatus
		}
	}

	requiredProgress := onboardingRequiredProgress{Total: len(required)}
	for _, step := range required {
		if step.Status == onboardingCompleteStatus {
			requiredProgress.Complete++
		}
	}
	requiredComplete := requiredProgress.Complete == requiredProgress.Total
	steps := append(required, recommendedDefinitions...)
	return onboardingStatusResponse{
		Version: onboardingVersion, GeneratedAt: server.now().Unix(), RequiredComplete: requiredComplete,
		Required: requiredProgress, Recommended: recommendedProgress,
		SkippedRecommended: skipped, Steps: steps,
	}, nil
}

func onboardingRequiredStep(
	id string,
	complete bool,
	title string,
	description string,
	actionPath string,
	blockers []string,
) onboardingStep {
	status := onboardingIncompleteStatus
	if complete {
		status = onboardingCompleteStatus
	}
	if blockers == nil {
		blockers = make([]string, 0)
	}
	return onboardingStep{
		ID: id, Kind: onboardingRequiredKind, Status: status, Title: title,
		Description: description, ActionPath: actionPath, Blockers: blockers,
	}
}

func normalizeSkippedRecommended(values []string) ([]string, error) {
	allowed := make(map[string]struct{}, len(onboardingRecommendedIDs))
	for _, id := range onboardingRecommendedIDs {
		allowed[id] = struct{}{}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		id := strings.TrimSpace(raw)
		if _, found := allowed[id]; !found {
			return nil, i18n.M("admin.this_setup_step_cannot_be_skipped", i18n.Params{"Value": id})
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func skippedRecommendedFromSettings(settings map[string]any) ([]string, error) {
	values, err := stringListSettingValue(settings, onboardingSkippedSetting, []string{})
	if err != nil {
		return nil, fmt.Errorf("read skipped onboarding recommendations: %w", err)
	}
	allowed := make(map[string]struct{}, len(onboardingRecommendedIDs))
	for _, id := range onboardingRecommendedIDs {
		allowed[id] = struct{}{}
	}
	filtered := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, id := range values {
		if _, found := allowed[id]; !found {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, id)
	}
	sort.Strings(filtered)
	return filtered, nil
}

func configuredNonEmptyString(settings map[string]any, key string) bool {
	value, found := settings[key].(string)
	return found && strings.TrimSpace(value) != ""
}

func configuredPositiveNumber(settings map[string]any, key string) bool {
	value, found := settings[key]
	if !found || value == nil {
		return false
	}
	switch typed := value.(type) {
	case float64:
		return typed > 0
	case int:
		return typed > 0
	case int64:
		return typed > 0
	default:
		return false
	}
}

func settingBool(settings map[string]any, key string) bool {
	value, _ := settings[key].(bool)
	return value
}

func brandingCustomized(values generalSettingsValues) bool {
	defaults := defaultGeneralSettings()
	return values.ProductName != defaults.ProductName || values.ShortName != defaults.ShortName ||
		values.EnvironmentLabel != defaults.EnvironmentLabel
}

func hasString(values map[string]struct{}, value string) bool {
	_, found := values[value]
	return found
}
