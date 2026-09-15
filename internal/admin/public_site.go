package admin

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/sitetime"
	"github.com/gin-gonic/gin"
)

const (
	defaultPortalLogoURL         = "/portal/assets/codex-cpa-pool-logo.svg"
	defaultPortalLogoContentType = "image/svg+xml"
	defaultAccountListenAddress  = "127.0.0.1"
)

type publicSiteLogo struct {
	Custom      bool   `json:"custom"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	UpdatedAt   *int64 `json:"updated_at"`
}

type publicSiteConfiguration struct {
	Version             int            `json:"version"`
	Timezone            string         `json:"timezone"`
	ProductName         string         `json:"product_name"`
	ShortName           string         `json:"short_name"`
	EnvironmentLabel    string         `json:"environment_label"`
	PublicBaseURL       string         `json:"public_base_url"`
	AllowedEmailDomains []string       `json:"allowed_email_domains"`
	ProviderName        string         `json:"provider_name"`
	APIKeyEnv           string         `json:"api_key_env"`
	DefaultModel        string         `json:"default_model"`
	Logo                publicSiteLogo `json:"logo"`
}

type nativeAccount struct {
	ID            string `json:"id"`
	GroupEnabled  bool   `json:"group_enabled"`
	ManagementURL string `json:"management_url,omitempty"`
}

type nativeAccountCatalog struct {
	Accounts []nativeAccount `json:"accounts"`
}

func (server *Server) publicSiteConfiguration(c *gin.Context) {
	settings, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read public site settings", err)
		return
	}
	timezone, err := sitetime.Name(settings)
	if err != nil {
		server.internalError(c, "read site timezone", err)
		return
	}
	values, err := publicGeneralSettingsFromMap(settings)
	if err != nil {
		server.internalError(c, "validate public site settings", err)
		return
	}
	asset, customLogo, err := server.store.ReadBrandingAsset(c.Request.Context(), "logo")
	if err != nil {
		server.internalError(c, "read public site logo", err)
		return
	}
	logo := publicSiteLogo{
		Custom: false, URL: defaultPortalLogoURL,
		ContentType: defaultPortalLogoContentType, SHA256: "", UpdatedAt: nil,
	}
	if customLogo {
		updatedAt := asset.UpdatedAt
		logo = publicSiteLogo{
			Custom: true, URL: "/branding/logo", ContentType: asset.ContentType,
			SHA256: asset.SHA256, UpdatedAt: &updatedAt,
		}
	}
	c.Header("Cache-Control", "no-store")
	httpi18n.JSON(c, http.StatusOK, publicSiteConfiguration{
		Version: generalSettingsVersion, Timezone: timezone, ProductName: values.ProductName,
		ShortName: values.ShortName, EnvironmentLabel: values.EnvironmentLabel,
		PublicBaseURL: values.PublicBaseURL, AllowedEmailDomains: values.AllowedEmailDomains,
		ProviderName: values.ProviderName, APIKeyEnv: values.APIKeyEnv,
		DefaultModel: values.DefaultModel, Logo: logo,
	})
}

// publicGeneralSettingsFromMap intentionally copies only browser-safe keys.
// Identity domains are explicitly public so unauthenticated login forms can
// explain the accepted email suffixes. API Key prefixes and every other
// setting remain excluded, so adding a private setting to the general form
// cannot accidentally widen /site-config.json.
func publicGeneralSettingsFromMap(settings map[string]any) (generalSettingsValues, error) {
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

func (server *Server) nativeAccounts(c *gin.Context) {
	accounts, err := server.store.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read native account catalog", err)
		return
	}
	includeManagementURLs := requestHostIsLoopback(c.Request.Host)
	listenAddress := ""
	if includeManagementURLs {
		settings, settingsError := server.store.ReadSettings(c.Request.Context())
		if settingsError != nil {
			server.internalError(c, "read native account settings", settingsError)
			return
		}
		listenAddress, err = accountListenAddress(settings)
		if err != nil {
			server.internalError(c, "validate native account listen address", err)
			return
		}
	}
	result := make([]nativeAccount, 0, len(accounts))
	for _, account := range accounts {
		item := nativeAccount{ID: account.ID, GroupEnabled: account.GroupEnabled}
		if includeManagementURLs {
			if account.Port < 1 || account.Port > 65535 {
				server.internalError(c, "validate native account port", fmt.Errorf("account %s has invalid port", account.ID))
				return
			}
			item.ManagementURL = "http://" + net.JoinHostPort(listenAddress, strconv.Itoa(account.Port)) + "/management.html"
		}
		result = append(result, item)
	}
	c.Header("Cache-Control", "no-store")
	httpi18n.JSON(c, http.StatusOK, nativeAccountCatalog{Accounts: result})
}

func accountListenAddress(settings map[string]any) (string, error) {
	address, err := stringSettingValue(settings, "accounts.listen_address", defaultAccountListenAddress)
	if err != nil {
		return "", err
	}
	address = strings.TrimSpace(address)
	parsed := net.ParseIP(address)
	if parsed == nil || !parsed.IsLoopback() {
		return "", fmt.Errorf("accounts.listen_address must be a loopback IP address")
	}
	return address, nil
}

func requestHostIsLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	hostname := ""
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		hostname = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	} else if strings.Contains(host, ":") {
		parsedHost, port, err := net.SplitHostPort(host)
		if err != nil {
			return false
		}
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return false
		}
		hostname = parsedHost
	} else {
		hostname = host
	}
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	parsed := net.ParseIP(hostname)
	return parsed != nil && parsed.IsLoopback()
}
