package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountlifecycle"
	"github.com/Alfonsxh/codex-cpa-pool/internal/accountstatus"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type AccountCatalog interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
}

type AccountLifecycleService interface {
	Create(context.Context, accountlifecycle.CreateRequest) (accountlifecycle.CreateResult, error)
	Update(context.Context, accountlifecycle.UpdateRequest) (accountlifecycle.UpdateResult, error)
	Delete(context.Context, accountlifecycle.DeleteRequest) (accountlifecycle.DeleteResult, error)
	ClearAuth(context.Context, string) (accountlifecycle.AuthClearResult, error)
}

type accountActivityEmailReader interface {
	ActiveUserEmailsLastHour(context.Context) (map[string][]string, error)
}
type configuredActivityReader interface {
	RefreshActiveUsers(context.Context) (map[string]int, error)
	ActiveUserEmails(context.Context) (map[string][]string, error)
}

func (server *Server) activityWindow() time.Duration {
	if reader, ok := server.activity.(*usage.Store); ok {
		return reader.ActiveUserWindow()
	}
	return usage.DefaultActiveUserWindow
}

func formatActivityWindow(window time.Duration, languages ...i18n.Language) string {
	minutes := int(window / time.Minute)
	if minutes%60 == 0 {
		return i18n.M("admin.last_hours", i18n.Params{"Count": minutes / 60}).Render(i18n.Selected(languages))
	}
	return i18n.M("admin.last_minutes", i18n.Params{"Count": minutes}).Render(i18n.Selected(languages))
}

type AccountRuntimeReader interface {
	Observe(context.Context, map[string]string) map[string]accountstatus.State
}

type accountOperationalStatus = accountstatus.Presentation

type accountListItem struct {
	ID                   string                   `json:"id"`
	Email                string                   `json:"email"`
	Port                 int                      `json:"port"`
	ProxyMode            string                   `json:"proxy_mode"`
	ProxySource          string                   `json:"proxy_source"`
	ProxyDisplay         string                   `json:"proxy_display"`
	Enabled              bool                     `json:"enabled"`
	Default              bool                     `json:"default"`
	Service              string                   `json:"service"`
	ContainerState       string                   `json:"container_state"`
	ContainerStatus      string                   `json:"container_status"`
	ContainerHealth      string                   `json:"container_health"`
	RuntimeState         string                   `json:"runtime_state"`
	OAuthConfigured      *bool                    `json:"oauth_configured"`
	AuthFiles            int                      `json:"auth_files"`
	AuthState            string                   `json:"auth_state"`
	AssociatedUsers      int                      `json:"associated_users"`
	RoutedUsers          int                      `json:"routed_users"`
	ActiveUsers1H        *int                     `json:"active_users_1h"`
	ActiveEmails1H       []string                 `json:"active_user_emails_1h"`
	ResetCreditCount     *int64                   `json:"reset_credit_count"`
	Resettable           bool                     `json:"resettable"`
	ResetWindowLabels    []string                 `json:"reset_window_labels"`
	Quota                quota.AccountQuota       `json:"quota"`
	Usage                usage.RawMetrics         `json:"usage"`
	UsageAvailable       bool                     `json:"usage_available"`
	UsageWindowStartAt   *int64                   `json:"usage_window_start_at"`
	UsageWindowAvailable bool                     `json:"usage_window_available"`
	AccountState         failover.AccountState    `json:"account_state"`
	Runtime              accountstatus.Runtime    `json:"runtime"`
	OperationalStatus    accountOperationalStatus `json:"operational_status"`
	StateAvailable       bool                     `json:"state_available"`
	ProxyConfigured      bool                     `json:"proxy_configured"`
}

func (server *Server) listAccounts(c *gin.Context) {
	if server.accounts == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_directory_service_is_not_ready"), "accounts_not_ready")
		return
	}
	window, err := server.parseAccountListUsageWindow(c)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	if c.Query("fresh") == "1" {
		if _, _, err := quota.RequestRefresh(c.Request.Context(), server.store, server.now()); err != nil {
			server.internalError(c, "request official quota refresh", err)
			return
		}
	}
	sinceReset := window.Window == sinceResetWindow
	accounts, err := server.accounts.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read account catalog", err)
		return
	}
	accountIDs := make([]string, 0, len(accounts))
	for _, account := range accounts {
		accountIDs = append(accountIDs, account.ID)
	}
	var (
		routes          map[string]string
		states          map[string]failover.AccountState
		activity        map[string]int
		activityEmails  map[string][]string
		usageSummaries  map[string]usage.AccountUsageSummary
		keyRecords      []controlplane.KeyRecord
		stateError      error
		activityError   error
		usageError      error
		keyRecordError  error
		secretStatuses  map[string]controlplane.SecretStatus
		officialQuota   quota.RuntimeState
		quotaStateError error
		runtimeServices []runtimeops.Service
		runtimeError    error
		refreshRequest  quota.RefreshRequestState
		refreshError    error
		collector       usage.CollectorStatus
		collectorError  error
		settings        map[string]any
	)
	settings, err = server.store.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read account settings", err)
		return
	}
	if reader, ok := server.activity.(*usage.Store); ok {
		reader.SetActiveUserWindow(usage.ActiveUserWindowFromSettings(settings))
	}
	group, groupContext := errgroup.WithContext(c.Request.Context())
	group.Go(func() error {
		var err error
		secretStatuses, err = server.store.SecretStatuses(groupContext)
		return err
	})
	group.Go(func() error {
		_, quotaStateError = server.store.ReadRuntimeState(groupContext, quota.RuntimeStateName, &officialQuota)
		return nil
	})
	group.Go(func() error {
		refreshRequest, _, refreshError = quota.ReadRefreshRequest(groupContext, server.store)
		return nil
	})
	group.Go(func() error {
		var err error
		routes, err = server.accounts.ReadRoutes(groupContext)
		return err
	})
	if server.accountStates != nil {
		group.Go(func() error {
			states, stateError = server.accountStates.AccountStates(groupContext)
			return nil
		})
	}
	if server.activity != nil {
		group.Go(func() error {
			if reader, ok := server.activity.(configuredActivityReader); ok {
				activityEmails, activityError = reader.ActiveUserEmails(groupContext)
				if activityError == nil {
					activity = make(map[string]int, len(activityEmails))
					for account, emails := range activityEmails {
						activity[account] = len(emails)
					}
				}
				return nil
			}
			if reader, ok := server.activity.(accountActivityEmailReader); ok {
				activityEmails, activityError = reader.ActiveUserEmailsLastHour(groupContext)
				if activityError == nil {
					activity = make(map[string]int, len(activityEmails))
					for account, emails := range activityEmails {
						activity[account] = len(emails)
					}
				}
				return nil
			}
			activity, activityError = server.activity.RefreshActiveUsersLastHour(groupContext)
			return nil
		})
	}
	if !sinceReset {
		if reader, ok := server.usage.(AccountUsageSummaryReader); ok {
			group.Go(func() error {
				usageSummaries, usageError = reader.AccountSummaries(
					groupContext, accountIDs, window.queryStartAt, window.queryEndAt,
				)
				return nil
			})
		} else {
			usageError = errors.New("account usage summary reader is unavailable")
		}
	}
	if server.usage != nil {
		group.Go(func() error {
			collector, collectorError = server.usage.Status(groupContext)
			return nil
		})
	} else {
		collector = usage.CollectorStatus{Status: "unavailable"}
	}
	if reader, ok := server.store.(interface {
		ReadKeyRecords(context.Context) ([]controlplane.KeyRecord, error)
	}); ok {
		group.Go(func() error {
			keyRecords, keyRecordError = reader.ReadKeyRecords(groupContext)
			return nil
		})
	}
	if server.runtime != nil {
		group.Go(func() error {
			runtimeServices, runtimeError = server.runtime.List(groupContext)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		server.internalError(c, "read account catalog", err)
		return
	}
	if reader, ok := server.activity.(*usage.Store); ok {
		reader.SetActiveUserWindow(usage.ActiveUserWindowFromSettings(settings))
	}
	quotaByAccount := make(map[string]quota.AccountQuota, len(officialQuota.Snapshot.Accounts))
	for _, accountQuota := range officialQuota.Snapshot.Accounts {
		quotaByAccount[accountQuota.Account] = accountQuota
	}
	var usageStartAtByAccount map[string]int64
	if sinceReset {
		usageStartAtByAccount = make(map[string]int64, len(accounts))
		for _, account := range accounts {
			accountQuota := quotaByAccount[account.ID].WithLanguage(httpi18n.Locale(c))
			if accountQuota.Weekly == nil {
				continue
			}
			if startAt, found := weeklyWindowStart(*accountQuota.Weekly, window.GeneratedAt); found {
				usageStartAtByAccount[account.ID] = startAt
			}
		}
		if reader, ok := server.usage.(AccountUsageSummaryByAccountReader); ok {
			usageSummaries, usageError = reader.AccountSummariesByStart(
				c.Request.Context(), usageStartAtByAccount, window.WindowEndAt,
			)
		} else {
			usageError = errors.New("per-account usage summary reader is unavailable")
		}
	}
	warnings := make([]string, 0, 6)
	if stateError != nil {
		server.logger.Warn("account operational state is unavailable", zap.Error(stateError))
		warnings = append(warnings, httpi18n.Text(c, "admin.account_quota_status_is_unavailable_and_is_shown_as_unknown"))
	}
	if activityError != nil {
		server.logger.Warn("active-user activity is unavailable", zap.Error(activityError))
		warnings = append(warnings, httpi18n.Text(c, "admin.activity_unavailable", i18n.Params{"Window": formatActivityWindow(server.activityWindow(), httpi18n.Locale(c))}))
	}
	if usageError != nil {
		server.logger.Warn("account usage summaries are unavailable", zap.Error(usageError))
		warnings = append(warnings, httpi18n.Text(c, "admin.account_requests_and_tokens_for_this_range_are_unavailable"))
	}
	if keyRecordError != nil {
		server.logger.Warn("account key associations are unavailable", zap.Error(keyRecordError))
		warnings = append(warnings, httpi18n.Text(c, "admin.associated_user_counts_are_unavailable"))
	}
	if quotaStateError != nil {
		server.logger.Warn("official quota reset summary is unavailable", zap.Error(quotaStateError))
		warnings = append(warnings, httpi18n.Text(c, "admin.weekly_limit_reset_status_is_unavailable"))
	}
	if refreshError != nil {
		server.logger.Warn("official quota refresh request state is unavailable", zap.Error(refreshError))
		warnings = append(warnings, httpi18n.Text(c, "admin.weekly_limit_refresh_status_is_unavailable"))
	}
	if collectorError != nil {
		server.logger.Warn("usage collector status is unavailable", zap.Error(collectorError))
		collector = usage.CollectorStatus{Status: "unavailable"}
		warnings = append(warnings, httpi18n.Text(c, "admin.usage_collector_status_is_unavailable"))
	}
	if runtimeError != nil {
		server.logger.Warn("account container status is unavailable", zap.Error(runtimeError))
		warnings = append(warnings, httpi18n.Text(c, "admin.cpa_container_status_is_unavailable"))
	}
	if sinceReset && len(usageStartAtByAccount) < len(accounts) {
		warnings = append(warnings, i18n.M("admin.cpas_have_no_quota_period_boundaries_usage_for_this_period", i18n.Params{"Count": len(accounts) - len(usageStartAtByAccount)}).Render(httpi18n.Locale(c)))
	}
	routedCounts := make(map[string]int)
	for _, account := range routes {
		routedCounts[account]++
	}
	associatedUsers := make(map[string]map[string]struct{})
	for _, record := range keyRecords {
		if record.Status != "active" || record.Account == "" || record.User == "" {
			continue
		}
		if associatedUsers[record.Account] == nil {
			associatedUsers[record.Account] = make(map[string]struct{})
		}
		associatedUsers[record.Account][record.User] = struct{}{}
	}
	servicesByName := make(map[string]runtimeops.Service, len(runtimeServices))
	runningAccountServices := make(map[string]string)
	for _, service := range runtimeServices {
		servicesByName[service.Service] = service
		if service.State == "running" && strings.HasPrefix(service.Service, "cliproxy-") {
			runningAccountServices[strings.TrimPrefix(service.Service, "cliproxy-")] = service.Service
		}
	}
	runtimeStatuses := make(map[string]accountstatus.State)
	if server.accountRuntime != nil && len(runningAccountServices) > 0 {
		runtimeStatuses = server.accountRuntime.Observe(c.Request.Context(), runningAccountServices)
	}
	items := make([]accountListItem, 0, len(accounts))
	for _, account := range accounts {
		proxyConfigured := secretStatuses["cpa_account_proxy_url:"+account.ID].SHA256 != ""
		proxySource, proxyDisplay, err := accountProxyPresentation(
			c.Request.Context(), server.store, settings, account, proxyConfigured,
		)
		if err != nil {
			server.internalError(c, "read account proxy presentation", err)
			return
		}
		state, stateAvailable := states[account.ID]
		if !stateAvailable {
			state = failover.AccountState{Account: account.ID, Reason: "quota_unavailable"}
		}
		var activeUsers *int
		if activityError == nil && server.activity != nil {
			count := activity[account.ID]
			activeUsers = &count
		}
		var oauthConfigured *bool
		if server.oauth != nil {
			configured := false
			if _, loadError := server.oauth.Load(account.ID); loadError == nil {
				configured = true
				oauthConfigured = &configured
			} else if errors.Is(loadError, quota.ErrOAuthMissing) {
				oauthConfigured = &configured
			} else {
				server.logger.Warn(
					"account OAuth status is unavailable",
					zap.String("account", account.ID), zap.Error(loadError),
				)
			}
		}
		runtimeState := "unknown"
		switch {
		case !account.GroupEnabled:
			runtimeState = "disabled"
		case stateAvailable && state.Reason == "container_not_running":
			runtimeState = "stopped"
		case stateAvailable:
			runtimeState = "running"
		}
		accountUsage := usageSummaries[account.ID]
		usageWindowStartAt := window.WindowStartAt
		usageWindowAvailable := usageError == nil
		if sinceReset {
			startAt, found := usageStartAtByAccount[account.ID]
			usageWindowAvailable = usageError == nil && found
			if found {
				startAtCopy := startAt
				usageWindowStartAt = &startAtCopy
			} else {
				usageWindowStartAt = nil
			}
		}
		accountQuota := quotaByAccount[account.ID].WithLanguage(httpi18n.Locale(c))
		if accountQuota.WeeklyWindows == nil {
			accountQuota.WeeklyWindows = make([]quota.WeeklyWindow, 0)
		}
		resetWindowLabels := make([]string, 0)
		for _, window := range accountQuota.WeeklyWindows {
			if window.Resettable {
				resetWindowLabels = append(resetWindowLabels, window.Label)
			}
		}
		resettable := accountQuota.ResetCreditCount != nil && *accountQuota.ResetCreditCount > 0 && len(resetWindowLabels) > 0
		serviceName := "cliproxy-" + account.ID
		service, serviceFound := servicesByName[serviceName]
		containerState := "missing"
		containerStatus := ""
		containerHealth := ""
		if serviceFound {
			containerState, containerStatus, containerHealth = service.State, service.Status, service.Health
		} else if server.runtime == nil {
			containerState = runtimeState
		}
		runtimeStatus := runtimeStatuses[account.ID]
		authFiles := runtimeStatus.AuthFiles
		authState := "unknown"
		if oauthConfigured != nil {
			if *oauthConfigured {
				authState = "configured"
				if authFiles == 0 {
					authFiles = 1
				}
			} else {
				authState = "pending"
			}
		}
		operationalStatus := accountstatus.Present(account.GroupEnabled, state, stateAvailable && stateError == nil, httpi18n.Locale(c))
		items = append(items, accountListItem{
			ID: account.ID, Email: account.Email, Port: account.Port,
			ProxyMode: account.ProxyMode, ProxySource: proxySource, ProxyDisplay: proxyDisplay,
			Enabled: account.GroupEnabled,
			Default: account.DefaultGroup, Service: serviceName,
			ContainerState: containerState, ContainerStatus: containerStatus, ContainerHealth: containerHealth,
			RuntimeState:    runtimeState,
			OAuthConfigured: oauthConfigured, AssociatedUsers: len(associatedUsers[account.ID]),
			AuthFiles: authFiles, AuthState: authState,
			RoutedUsers: routedCounts[account.ID], ActiveUsers1H: activeUsers,
			ActiveEmails1H:   append([]string{}, activityEmails[account.ID]...),
			ResetCreditCount: accountQuota.ResetCreditCount, Resettable: resettable,
			ResetWindowLabels: resetWindowLabels, Quota: accountQuota,
			Usage: accountUsage.RawMetrics, UsageAvailable: usageWindowAvailable,
			UsageWindowStartAt: usageWindowStartAt, UsageWindowAvailable: usageWindowAvailable,
			AccountState: state, Runtime: runtimeStatus.Runtime,
			OperationalStatus: operationalStatus,
			StateAvailable:    stateAvailable && stateError == nil,
			ProxyConfigured:   proxyConfigured,
		})
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"accounts":                   items,
		"generated_at":               window.GeneratedAt,
		"window":                     window.Window,
		"window_seconds":             window.WindowSeconds,
		"window_start_at":            window.WindowStartAt,
		"window_start_at_by_account": usageStartAtByAccount,
		"window_end_at":              window.WindowEndAt,
		"active_user_window_seconds": int64(server.activityWindow() / time.Second),
		"window_timezone":            window.WindowTimezone,
		"quota_generated_at":         nullablePositiveTimestamp(officialQuota.Snapshot.GeneratedAt),
		"quota_cached":               quotaStateError == nil && officialQuota.Snapshot.GeneratedAt > 0,
		"quota_refreshing":           refreshError == nil && refreshRequest.Pending(),
		"quota_cache_ttl_seconds":    officialQuota.Snapshot.CacheTTLSeconds,
		"collector":                  collector,
		"warnings":                   warnings,
	})
}

func accountProxyPresentation(
	ctx context.Context,
	store interface {
		ReadSecret(context.Context, string) (string, bool, error)
	},
	settings map[string]any,
	account controlplane.Account,
	customConfigured bool,
) (string, string, error) {
	mode := strings.TrimSpace(account.ProxyMode)
	if mode == "" {
		mode = "inherit"
	}
	secretName := ""
	source := "direct"
	switch mode {
	case "direct":
		return source, "direct", nil
	case "custom":
		source = "account"
		if !customConfigured {
			return source, "", nil
		}
		secretName = "cpa_account_proxy_url:" + account.ID
	case "inherit":
		proxyEnabled, _ := settings["cpa.proxy_enabled"].(bool)
		if !proxyEnabled {
			return source, "direct", nil
		}
		source = "default"
		secretName = "cpa_default_proxy_url"
	default:
		return "", "", fmt.Errorf("account %s has invalid proxy mode %q", account.ID, mode)
	}
	value, found, err := store.ReadSecret(ctx, secretName)
	if err != nil {
		return "", "", fmt.Errorf("read effective proxy for account %s: %w", account.ID, err)
	}
	if !found || strings.TrimSpace(value) == "" {
		return source, "", nil
	}
	return source, redactAccountProxyURL(value), nil
}

func redactAccountProxyURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "direct" {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	authority := parsed.Host
	if parsed.User != nil {
		authority = url.PathEscape(parsed.User.Username()) + ":***@" + authority
	}
	return strings.ToLower(parsed.Scheme) + "://" + authority
}

func nullablePositiveTimestamp(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func (server *Server) parseAccountListUsageWindow(c *gin.Context) (usageWindowContext, error) {
	raw := strings.ToLower(strings.TrimSpace(c.Query("window")))
	if raw != sinceResetWindow {
		return server.parseUsageWindow(c, false)
	}
	generatedAt := server.now().Unix()
	endAt := generatedAt
	return usageWindowContext{
		GeneratedAt:   generatedAt,
		Window:        sinceResetWindow,
		WindowSeconds: nil,
		WindowStartAt: nil,
		WindowEndAt:   &endAt,
		queryEndAt:    &endAt,
	}, nil
}

func (server *Server) inspectAccountQuotaReset(c *gin.Context) {
	account := strings.ToLower(strings.TrimSpace(c.Query("account")))
	if account == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.select_the_cpa_whose_weekly_limit_should_be_reset"), "invalid_request")
		return
	}
	inspector, ok := server.quotaResetter.(quotaResetInspector)
	if !ok {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.weekly_limit_reset_detail_service_is_not_ready"), "quota_reset_not_ready")
		return
	}
	result, err := inspector.Inspect(c.Request.Context(), account)
	if err != nil {
		server.writeQuotaResetReadError(c, err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, result)
}

func (server *Server) writeQuotaResetReadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_weekly_limit_reset_parameters"), "invalid_request")
	case errors.Is(err, quota.ErrResetAccountNotFound):
		writeError(c, http.StatusNotFound, i18n.M("admin.cpa_account_does_not_exist"), "account_not_found")
	case errors.Is(err, quota.ErrOAuthMissing):
		writeError(c, http.StatusConflict, i18n.M("admin.this_cpa_has_not_completed_oauth_authorization"), "quota_auth_missing")
	case errors.Is(err, quota.ErrAuthExpired):
		writeError(c, http.StatusConflict, i18n.M("admin.upstream_oauth_authorization_expired_authorize_again_and_retry"), "quota_auth_expired")
	default:
		writeError(c, http.StatusBadGateway, i18n.M("admin.unable_to_read_upstream_weekly_reset_details_please_try_again"), "quota_upstream_unavailable")
	}
}

func (server *Server) createAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_lifecycle_service_is_not_ready"), "account_lifecycle_not_ready")
		return
	}
	var body accountlifecycle.CreateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_account_creation_parameters"), "invalid_request")
		return
	}
	result, err := server.accountLifecycle.Create(c.Request.Context(), body)
	if err != nil {
		server.writeAccountLifecycleError(c, "create account", err)
		return
	}
	// Account creation and OAuth must not be rolled back because a plugin
	// download or compatibility check fails. Use the same serialized runtime
	// job as a manual install and return its separate, visible outcome.
	ticket := runtimeops.PluginJobStatus{Status: "unavailable"}
	if server.runtimeJobs != nil && server.extensions != nil {
		submission, submitErr := server.runtimeJobs.Submit("plugin-update", result.Account.ID)
		if submitErr == nil {
			ticket = runtimeops.PluginJobStatus{ID: submission.Job.ID, Status: submission.Job.Status}
		} else {
			ticket.Status = "submission_failed"
		}
	}
	httpi18n.JSON(c, http.StatusCreated, gin.H{"message": i18n.M("admin.cpa_account_created_and_runtime_probes_passed"), "account": result, "ticket": ticket})
}

func (server *Server) updateAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_lifecycle_service_is_not_ready"), "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID              string  `json:"id" binding:"required"`
		NewID           string  `json:"new_id"`
		Email           string  `json:"email"`
		ProxyMode       string  `json:"proxy_mode"`
		ProxyURL        *string `json:"proxy_url"`
		GroupEnabled    *bool   `json:"group_enabled"`
		DefaultGroup    *bool   `json:"default_group"`
		FallbackAccount string  `json:"fallback_account"`
		Confirm         string  `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_account_update_parameters"), "invalid_request")
		return
	}
	currentID := strings.ToLower(strings.TrimSpace(body.ID))
	newID := strings.ToLower(strings.TrimSpace(body.NewID))
	if newID != "" && newID != currentID && strings.TrimSpace(body.Confirm) != currentID {
		writeError(c, http.StatusBadRequest, i18n.M("admin.rename_confirmation_must_exactly_match_the_current_cpa_id"), "invalid_confirmation")
		return
	}
	proxyURL := body.ProxyURL
	if proxyURL != nil && strings.TrimSpace(*proxyURL) == "" {
		// The legacy account editor always submits an empty write-only field
		// when the operator leaves the encrypted proxy unchanged.
		proxyURL = nil
	}
	result, err := server.accountLifecycle.Update(c.Request.Context(), accountlifecycle.UpdateRequest{
		AccountID: body.ID, NewAccountID: body.NewID, Email: body.Email,
		ProxyMode: body.ProxyMode, ProxyURL: proxyURL, Enabled: body.GroupEnabled,
		Default: body.DefaultGroup, FallbackAccount: body.FallbackAccount,
		PolicyOnly: c.FullPath() == "/admin/api/accounts/policy",
	})
	if err != nil {
		server.writeAccountLifecycleError(c, "update account", err)
		return
	}
	message := httpi18n.Text(c, "admin.cpa_account_updated_and_runtime_probes_passed")
	if c.FullPath() == "/admin/api/accounts/policy" {
		message = httpi18n.Text(c, "admin.account_availability_updated")
	} else if result.RenamedFrom != "" {
		message = httpi18n.Text(c, "admin.cpa_renamed_and_recreated_runtime_probes_passed")
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"message": message, "account": result})
}

func (server *Server) repairUnavailableAccountProxy(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_lifecycle_service_is_not_ready"), "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID       string `json:"id" binding:"required"`
		ProxyURL string `json:"proxy_url" binding:"required"`
		Confirm  string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_proxy_recovery_parameters"), "invalid_request")
		return
	}
	accountID, err := controlplane.NormalizeAccountID(body.ID)
	if err != nil || accountID != strings.TrimSpace(body.ID) ||
		strings.TrimSpace(body.Confirm) != "repair-proxy:"+accountID {
		writeError(c, http.StatusBadRequest, i18n.M("admin.proxy_recovery_confirmation_must_exactly_match_the_cpa_id"), "invalid_confirmation")
		return
	}
	proxyURL := strings.TrimSpace(body.ProxyURL)
	result, err := server.accountLifecycle.Update(c.Request.Context(), accountlifecycle.UpdateRequest{
		AccountID: accountID, ProxyMode: "custom", ProxyURL: &proxyURL,
		AllowUnavailableProxyRepair: true,
	})
	if err != nil {
		server.writeAccountLifecycleError(c, "repair unavailable account proxy", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("admin.the_unavailable_cpa_s_proxy_configuration_was_restored_future_maintenance"),
		"account": result,
	})
}

func (server *Server) clearAccountAuth(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_lifecycle_service_is_not_ready"), "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID      string `json:"id" binding:"required"`
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Confirm) != strings.TrimSpace(body.ID) {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirmation_must_exactly_match_the_cpa_id"), "invalid_confirmation")
		return
	}
	result, err := server.accountLifecycle.ClearAuth(c.Request.Context(), body.ID)
	if err != nil {
		server.writeAccountLifecycleError(c, "clear account OAuth", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"message": i18n.M("admin.oauth_authorization_cleared_original_files_were_safely_archived"), "account": result})
}

func (server *Server) deleteAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_lifecycle_service_is_not_ready"), "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID              string `json:"id" binding:"required"`
		Confirm         string `json:"confirm" binding:"required"`
		RevokeKeys      bool   `json:"revoke_keys"`
		FallbackAccount string `json:"fallback_account"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Confirm) != strings.TrimSpace(body.ID) {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirmation_must_exactly_match_the_cpa_id"), "invalid_confirmation")
		return
	}
	result, err := server.accountLifecycle.Delete(c.Request.Context(), accountlifecycle.DeleteRequest{
		AccountID: body.ID, FallbackAccount: body.FallbackAccount, RevokeExclusive: body.RevokeKeys,
	})
	if err != nil {
		server.writeAccountLifecycleError(c, "delete account", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"message": i18n.M("admin.cpa_account_deleted_configuration_authorization_and_logs_were_safely_archived"), "account": result})
}

func (server *Server) writeAccountLifecycleError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_account_parameters_check_the_id_email_proxy_and_confirmation"), "invalid_request")
	case errors.Is(err, controlplane.ErrAccountLifecycleNotFound):
		writeError(c, http.StatusNotFound, i18n.M("admin.cpa_account_does_not_exist"), "account_not_found")
	case errors.Is(err, controlplane.ErrAccountAlreadyExists),
		errors.Is(err, controlplane.ErrAccountEmailAlreadyExists),
		errors.Is(err, controlplane.ErrAccountPortAlreadyExists):
		writeError(c, http.StatusConflict, i18n.M("admin.the_cpa_id_email_or_port_is_already_in_use"), "account_exists")
	case errors.Is(err, controlplane.ErrAccountDeleteLast):
		writeError(c, http.StatusConflict, i18n.M("admin.keep_at_least_one_cpa_the_last_account_cannot_be"), "account_last")
	case errors.Is(err, controlplane.ErrAccountDeleteRequiresRevoke):
		writeError(c, http.StatusConflict, i18n.M("admin.this_cpa_still_has_exclusive_active_keys_confirm_their_deactivation"), "account_revoke_required")
	case errors.Is(err, controlplane.ErrAccountDeleteNeedsFallback):
		writeError(c, http.StatusConflict, i18n.M("admin.select_another_enabled_cpa_as_a_safe_migration_target"), "account_fallback_required")
	case errors.Is(err, controlplane.ErrAccountLifecycleConflict),
		errors.Is(err, accountlifecycle.ErrNoAccountPort),
		errors.Is(err, runtimeops.ErrRuntimeConflict):
		writeError(c, http.StatusConflict, i18n.M("admin.account_state_changed_or_safe_runtime_resources_are_unavailable_no"), "account_lifecycle_conflict")
	case errors.Is(err, controlplane.ErrLeaseLost):
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.control_plane_ownership_changed_the_operation_stopped_and_was_rolled"), "ownership_lost")
	case errors.Is(err, accountlifecycle.ErrRouteEvacuationUnavailable),
		errors.Is(err, accountlifecycle.ErrLifecycleRecoveryRequired):
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.safe_account_migration_and_recovery_service_is_not_ready"), "account_lifecycle_not_ready")
	case errors.Is(err, accountlifecycle.ErrAccountDrainTimeout):
		writeError(c, http.StatusConflict, i18n.M("admin.this_cpa_still_has_active_codex_requests_it_was_not"), "account_requests_active")
	case errors.Is(err, accountlifecycle.ErrUnavailableProxyRepairRejected):
		writeError(c, http.StatusConflict, i18n.M("admin.the_current_state_does_not_meet_restricted_proxy_recovery_conditions"), "account_proxy_repair_unavailable")
	default:
		server.internalError(c, operation, err)
	}
}

func (server *Server) rebalanceAllAccounts(c *gin.Context) {
	var body struct {
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "rebalance-all" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_balancing_users_across_all_accounts"), "invalid_request")
		return
	}
	if server.rebalancer == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.load_balancing_service_is_not_ready"), "rebalance_not_ready")
		return
	}
	result, err := server.rebalancer.RebalanceAll(c.Request.Context())
	if err != nil {
		switch {
		case errors.Is(err, failover.ErrRebalanceUnsafe),
			errors.Is(err, failover.ErrRebalanceUnavailable),
			errors.Is(err, controlplane.ErrRouteConflict),
			errors.Is(err, controlplane.ErrRouteUserUnsafe):
			writeError(c, http.StatusConflict, i18n.M("admin.user_or_account_state_does_not_meet_safe_migration_conditions"), "account_rebalance_unavailable")
		default:
			server.internalError(c, "rebalance all accounts", err)
		}
		return
	}
	message := httpi18n.Text(c, "admin.accounts_already_match_the_target_distribution_no_migration_is_needed")
	if result.MovedUsers > 0 && result.ActivityRefreshed {
		message = httpi18n.Text(c, "admin.rebalance_activity_refreshed", i18n.Params{"Window": formatActivityWindow(server.activityWindow(), httpi18n.Locale(c))})
	} else if result.MovedUsers > 0 {
		message = httpi18n.Text(c, "admin.rebalance_activity_unavailable", i18n.Params{"Window": formatActivityWindow(server.activityWindow(), httpi18n.Locale(c))})
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":   message,
		"rebalance": result,
	})
}

func (server *Server) rebalanceAccount(c *gin.Context) {
	var body struct {
		ID      string `json:"id" binding:"required"`
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_account_migration_parameters"), "invalid_request")
		return
	}
	account := strings.ToLower(strings.TrimSpace(body.ID))
	if account == "" || strings.ToLower(strings.TrimSpace(body.Confirm)) != account {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirmation_must_exactly_match_the_cpa_id"), "invalid_confirmation")
		return
	}
	if server.accounts == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_directory_service_is_not_ready"), "accounts_not_ready")
		return
	}
	accounts, err := server.accounts.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read account before rebalance", err)
		return
	}
	found := false
	for _, item := range accounts {
		if item.ID == account {
			found = true
			break
		}
	}
	if !found {
		writeError(c, http.StatusNotFound, i18n.M("admin.cpa_account_does_not_exist"), "account_not_found")
		return
	}
	if server.rebalancer == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.load_balancing_service_is_not_ready"), "rebalance_not_ready")
		return
	}
	result, err := server.rebalancer.EvacuateAccount(c.Request.Context(), account)
	if err != nil {
		switch {
		case errors.Is(err, failover.ErrRebalanceUnsafe),
			errors.Is(err, failover.ErrRebalanceUnavailable),
			errors.Is(err, controlplane.ErrRouteConflict),
			errors.Is(err, controlplane.ErrRouteUserUnsafe):
			writeError(c, http.StatusConflict, i18n.M("admin.user_or_account_state_does_not_meet_safe_migration_conditions"), "account_rebalance_unavailable")
		default:
			server.internalError(c, "rebalance account", err)
		}
		return
	}
	message := httpi18n.Text(c, "admin.this_account_has_no_users_to_migrate")
	if result.MovedUsers > 0 && result.ActivityRefreshed {
		message = httpi18n.Text(c, "admin.migration_activity_refreshed", i18n.Params{"Window": formatActivityWindow(server.activityWindow(), httpi18n.Locale(c))})
	} else if result.MovedUsers > 0 {
		message = httpi18n.Text(c, "admin.migration_activity_unavailable", i18n.Params{"Window": formatActivityWindow(server.activityWindow(), httpi18n.Locale(c))})
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":   message,
		"rebalance": result,
	})
}

func (server *Server) resetAccountQuota(c *gin.Context) {
	var body struct {
		Account  string `json:"account" binding:"required"`
		CreditID string `json:"credit_id" binding:"required"`
		Confirm  string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_weekly_limit_reset_parameters"), "invalid_request")
		return
	}
	account := strings.ToLower(strings.TrimSpace(body.Account))
	if account == "" || strings.ToLower(strings.TrimSpace(body.Confirm)) != account {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirmation_must_exactly_match_the_cpa_id"), "invalid_confirmation")
		return
	}
	creditID := strings.TrimSpace(body.CreditID)
	if creditID == "" || len(creditID) > 512 {
		writeError(c, http.StatusBadRequest, i18n.M("admin.select_a_reset_credit"), "invalid_request")
		return
	}
	if server.quotaResetter == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.weekly_limit_reset_service_is_not_ready"), "quota_reset_not_ready")
		return
	}
	result, err := server.quotaResetter.Reset(c.Request.Context(), account, creditID)
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrInvalidCatalogInput):
			writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_weekly_limit_reset_parameters"), "invalid_request")
		case errors.Is(err, quota.ErrResetAccountNotFound):
			writeError(c, http.StatusNotFound, i18n.M("admin.cpa_account_does_not_exist"), "account_not_found")
		case errors.Is(err, quota.ErrOAuthMissing):
			writeError(c, http.StatusConflict, i18n.M("admin.this_cpa_has_not_completed_oauth_authorization"), "quota_auth_missing")
		case errors.Is(err, quota.ErrAuthExpired):
			writeError(c, http.StatusConflict, i18n.M("admin.upstream_oauth_authorization_expired_authorize_again_and_retry"), "quota_auth_expired")
		case errors.Is(err, quota.ErrResetCreditChanged):
			writeError(c, http.StatusConflict, i18n.M("admin.the_selected_reset_credit_was_used_expired_or_is_unavailable"), "quota_reset_credit_changed")
		case errors.Is(err, quota.ErrResetUnavailable):
			writeError(c, http.StatusConflict, i18n.M("admin.no_exhausted_weekly_limit_is_eligible_for_reset_wait_for"), "quota_reset_unavailable")
		case errors.Is(err, quota.ErrResetRejected):
			writeError(c, http.StatusConflict, i18n.M("admin.the_upstream_rejected_this_weekly_reset_refresh_the_quota_and"), "quota_reset_rejected")
		case errors.Is(err, controlplane.ErrLeaseLost):
			writeError(c, http.StatusServiceUnavailable, i18n.M("admin.control_plane_ownership_changed_the_operation_stopped"), "ownership_lost")
		default:
			writeError(c, http.StatusBadGateway, i18n.M("admin.unable_to_reach_the_upstream_to_reset_the_weekly_limit"), "quota_upstream_unavailable")
		}
		return
	}
	message := httpi18n.Text(c, "admin.the_reset_request_was_processed_refresh_to_confirm_the_latest")
	result = result.WithLanguage(httpi18n.Locale(c))
	if result.WindowsReset > 0 {
		message = i18n.M("admin.weekly_limit_reset_windows_refreshed", i18n.Params{"Count": result.WindowsReset}).Render(httpi18n.Locale(c))
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":       message,
		"account":       result.Account,
		"windows":       result.Windows,
		"windows_reset": result.WindowsReset,
		"code":          result.Code,
		"credit":        result.Credit,
	})
}
