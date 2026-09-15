package admin

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/identity"
	"github.com/gin-gonic/gin"
)

const (
	generalSettingsVersion = 1
	defaultProductName     = "Codex CPA Pool"
	// Legacy built-in values remain readable after upgrading an existing settings database.
	legacyProductName = "Codex CPA Cluster"
)

var (
	domainPattern    = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	keyPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}_$`)
	envNamePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

type generalSettingsValues struct {
	ProductName         string   `json:"product_name"`
	ShortName           string   `json:"short_name"`
	EnvironmentLabel    string   `json:"environment_label"`
	PublicBaseURL       string   `json:"public_base_url"`
	AllowedEmailDomains []string `json:"allowed_email_domains"`
	KeyPrefix           string   `json:"key_prefix"`
	ProviderName        string   `json:"provider_name"`
	APIKeyEnv           string   `json:"api_key_env"`
	DefaultModel        string   `json:"default_model"`
}

type generalSettingsSecurity struct {
	ManagementKeyConfigured   bool `json:"management_key_configured"`
	InitialPasswordConfigured bool `json:"initial_password_configured"`
}

type generalSettingsBranding struct {
	CustomLogo bool   `json:"custom_logo"`
	LogoSHA256 string `json:"logo_sha256,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
}

type generalSettingsResponse struct {
	Version     int                     `json:"version"`
	ApplyMode   string                  `json:"apply_mode"`
	GeneratedAt int64                   `json:"generated_at"`
	Values      generalSettingsValues   `json:"values"`
	Security    generalSettingsSecurity `json:"security"`
	Branding    generalSettingsBranding `json:"branding"`
}

func (server *Server) readGeneralSettings(c *gin.Context) {
	payload, err := server.generalSettings(c)
	if err != nil {
		server.internalError(c, "read general settings", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, payload)
}

func (server *Server) updateGeneralSettings(c *gin.Context) {
	var body struct {
		Confirm string                `json:"confirm" binding:"required"`
		Values  generalSettingsValues `json:"values" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_saving_general_settings"), "invalid_request")
		return
	}
	values, err := normalizeGeneralSettings(body.Values)
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_settings")
		return
	}
	if err := server.store.UpdateSettings(c.Request.Context(), values.settingsMap()); err != nil {
		server.internalError(c, "update general settings", err)
		return
	}
	payload, err := server.generalSettings(c)
	if err != nil {
		server.internalError(c, "read updated general settings", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":  i18n.M("admin.general_settings_saved_and_effective_immediately"),
		"settings": payload,
	})
}

func (server *Server) generalSettings(c *gin.Context) (generalSettingsResponse, error) {
	stored, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		return generalSettingsResponse{}, err
	}
	values, err := generalSettingsFromMap(stored)
	if err != nil {
		return generalSettingsResponse{}, err
	}
	statuses, err := server.store.SecretStatuses(c.Request.Context())
	if err != nil {
		return generalSettingsResponse{}, err
	}
	logo, customLogo, err := server.store.ReadBrandingAsset(c.Request.Context(), "logo")
	if err != nil {
		return generalSettingsResponse{}, err
	}
	return generalSettingsResponse{
		Version: generalSettingsVersion, ApplyMode: "live", GeneratedAt: server.now().Unix(), Values: values,
		Security: generalSettingsSecurity{
			ManagementKeyConfigured:   hasSecretStatus(statuses, "cpa_management_key"),
			InitialPasswordConfigured: hasSecretStatus(statuses, "portal_initial_password"),
		},
		Branding: generalSettingsBranding{
			CustomLogo: customLogo, LogoSHA256: logo.SHA256, UpdatedAt: logo.UpdatedAt,
		},
	}, nil
}

func hasSecretStatus(statuses map[string]controlplane.SecretStatus, name string) bool {
	status, found := statuses[name]
	return found && status.SHA256 != ""
}

func generalSettingsFromMap(settings map[string]any) (generalSettingsValues, error) {
	values := defaultGeneralSettings()
	var err error
	if values.ProductName, err = stringSettingValue(settings, "branding.product_name", values.ProductName); err != nil {
		return values, err
	}
	if values.ShortName, err = stringSettingValue(settings, "branding.short_name", values.ShortName); err != nil {
		return values, err
	}
	if values.EnvironmentLabel, err = stringSettingValue(settings, "branding.environment_label", values.EnvironmentLabel); err != nil {
		return values, err
	}
	if values.PublicBaseURL, err = stringSettingValue(settings, "branding.public_base_url", values.PublicBaseURL); err != nil {
		return values, err
	}
	if values.AllowedEmailDomains, err = stringListSettingValue(settings, "identity.allowed_email_domains", values.AllowedEmailDomains); err != nil {
		return values, err
	}
	if values.KeyPrefix, err = stringSettingValue(settings, "identity.key_prefix", values.KeyPrefix); err != nil {
		return values, err
	}
	if values.ProviderName, err = stringSettingValue(settings, "portal.provider_name", values.ProviderName); err != nil {
		return values, err
	}
	if values.APIKeyEnv, err = stringSettingValue(settings, "portal.api_key_env", values.APIKeyEnv); err != nil {
		return values, err
	}
	if values.DefaultModel, err = stringSettingValue(settings, "portal.default_model", values.DefaultModel); err != nil {
		return values, err
	}
	return normalizeGeneralSettings(values)
}

func defaultGeneralSettings() generalSettingsValues {
	return generalSettingsValues{
		ProductName: defaultProductName, ShortName: "CCPA",
		EnvironmentLabel: "Self-hosted service", PublicBaseURL: "",
		AllowedEmailDomains: []string{}, KeyPrefix: identity.DefaultUserKeyPrefix, ProviderName: "Codex CPA Pool",
		APIKeyEnv: "CCPA_API_KEY", DefaultModel: "gpt-5.6-sol",
	}
}

func normalizeGeneralSettings(values generalSettingsValues) (generalSettingsValues, error) {
	var err error
	if values.ProductName, err = normalizeText(values.ProductName, i18n.Ref("admin.product_name"), 2, 64, true); err != nil {
		return values, err
	}
	values.ProductName = normalizeProductName(values.ProductName)
	if values.ShortName, err = normalizeText(values.ShortName, i18n.Ref("admin.short_name"), 2, 32, true); err != nil {
		return values, err
	}
	if values.EnvironmentLabel, err = normalizeText(values.EnvironmentLabel, i18n.Ref("admin.environment_label"), 0, 64, false); err != nil {
		return values, err
	}
	if values.PublicBaseURL, err = normalizeBaseURL(values.PublicBaseURL); err != nil {
		return values, err
	}
	values.AllowedEmailDomains, err = normalizeDomains(values.AllowedEmailDomains)
	if err != nil {
		return values, err
	}
	values.KeyPrefix = strings.ToLower(strings.TrimSpace(values.KeyPrefix))
	if !keyPrefixPattern.MatchString(values.KeyPrefix) {
		return values, i18n.M("admin.the_new_key_prefix_must_contain_3_32_lowercase_letters")
	}
	if values.ProviderName, err = normalizeText(values.ProviderName, i18n.Ref("admin.client_provider_name"), 2, 48, true); err != nil {
		return values, err
	}
	values.APIKeyEnv = strings.TrimSpace(values.APIKeyEnv)
	if !envNamePattern.MatchString(values.APIKeyEnv) {
		return values, i18n.M("admin.the_client_key_environment_variable_must_be_a_valid_uppercase")
	}
	if values.DefaultModel, err = normalizeText(values.DefaultModel, i18n.Ref("admin.default_client_model"), 1, 128, true); err != nil {
		return values, err
	}
	return values, nil
}

// normalizeProductName recognizes only the exact former built-in name. Custom
// brands, including names containing the old name, remain operator-owned.
func normalizeProductName(value string) string {
	if value == legacyProductName {
		return defaultProductName
	}
	return value
}

func normalizeText(value string, label any, minimum int, maximum int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", i18n.M("admin.is_required", i18n.Params{"Field": label})
	}
	length := utf8.RuneCountInString(value)
	if length < minimum {
		return "", i18n.M("admin.must_contain_at_least_characters", i18n.Params{"Field": label, "Minimum": minimum})
	}
	if length > maximum {
		return "", i18n.M("admin.must_not_exceed_characters", i18n.Params{"Field": label, "Maximum": maximum})
	}
	for _, character := range value {
		if character < 32 {
			return "", i18n.M("admin.must_not_contain_control_characters", i18n.Params{"Field": label})
		}
	}
	return value, nil
}

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	for _, character := range value {
		if character < 32 || character == ' ' || character == '\t' || character == '\n' || character == '\r' {
			return "", i18n.M("admin.the_public_url_must_not_contain_whitespace_or_control_characters")
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return "", i18n.M("admin.the_public_url_must_be_a_valid_http_s_url")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" {
		return "", i18n.M("admin.the_public_url_must_be_a_valid_http_s_url")
	}
	if port := parsed.Port(); port != "" {
		value, portError := strconv.Atoi(port)
		if portError != nil || value < 1 || value > 65535 {
			return "", i18n.M("admin.the_public_url_contains_an_invalid_port")
		}
	}
	if parsed.User != nil {
		return "", i18n.M("admin.the_public_url_must_not_contain_a_username_or_password")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", i18n.M("admin.the_public_url_must_use_the_root_path_without_a")
	}
	return strings.TrimRight(value, "/"), nil
}

func normalizeDomains(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		domain := strings.ToLower(strings.TrimLeft(strings.TrimSpace(value), "@"))
		if domain == "" {
			continue
		}
		if len(domain) > 253 || strings.Contains(domain, "..") || !domainPattern.MatchString(domain) {
			return nil, i18n.M("admin.invalid_allowed_email_domain", i18n.Params{"Value": domain})
		}
		if _, found := seen[domain]; found {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result, nil
}

func stringSettingValue(settings map[string]any, key string, fallback string) (string, error) {
	value, found := settings[key]
	if !found || value == nil {
		return fallback, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", i18n.M("admin.invalid_data_type_for_setting", i18n.Params{"Value": key})
	}
	return text, nil
}

func stringListSettingValue(settings map[string]any, key string, fallback []string) ([]string, error) {
	value, found := settings[key]
	if !found || value == nil {
		return append([]string(nil), fallback...), nil
	}
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), nil
	case []any:
		result := make([]string, 0, len(values))
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, i18n.M("admin.invalid_data_type_for_setting", i18n.Params{"Value": key})
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, i18n.M("admin.invalid_data_type_for_setting", i18n.Params{"Value": key})
	}
}

func (values generalSettingsValues) settingsMap() map[string]any {
	return map[string]any{
		"branding.product_name":          values.ProductName,
		"branding.short_name":            values.ShortName,
		"branding.environment_label":     values.EnvironmentLabel,
		"branding.public_base_url":       values.PublicBaseURL,
		"identity.allowed_email_domains": values.AllowedEmailDomains,
		"identity.key_prefix":            values.KeyPrefix,
		"portal.provider_name":           values.ProviderName,
		"portal.api_key_env":             values.APIKeyEnv,
		"portal.default_model":           values.DefaultModel,
	}
}
