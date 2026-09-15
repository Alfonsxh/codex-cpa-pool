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

func formatActivityWindow(window time.Duration) string {
	minutes := int(window / time.Minute)
	if minutes%60 == 0 {
		return fmt.Sprintf("近 %d 小时", minutes/60)
	}
	return fmt.Sprintf("近 %d 分钟", minutes)
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
		writeError(c, http.StatusServiceUnavailable, "账号目录服务尚未就绪", "accounts_not_ready")
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
			accountQuota := quotaByAccount[account.ID]
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
		warnings = append(warnings, "账号额度状态暂不可用，已按状态未知展示")
	}
	if activityError != nil {
		server.logger.Warn("active-user activity is unavailable", zap.Error(activityError))
		warnings = append(warnings, formatActivityWindow(server.activityWindow())+"活跃用户数暂不可用")
	}
	if usageError != nil {
		server.logger.Warn("account usage summaries are unavailable", zap.Error(usageError))
		warnings = append(warnings, "当前用量范围的账号请求与 Token 统计暂不可用")
	}
	if keyRecordError != nil {
		server.logger.Warn("account key associations are unavailable", zap.Error(keyRecordError))
		warnings = append(warnings, "账号关联用户数暂不可用")
	}
	if quotaStateError != nil {
		server.logger.Warn("official quota reset summary is unavailable", zap.Error(quotaStateError))
		warnings = append(warnings, "周限额重置状态暂不可用")
	}
	if refreshError != nil {
		server.logger.Warn("official quota refresh request state is unavailable", zap.Error(refreshError))
		warnings = append(warnings, "周限额刷新状态暂不可用")
	}
	if collectorError != nil {
		server.logger.Warn("usage collector status is unavailable", zap.Error(collectorError))
		collector = usage.CollectorStatus{Status: "unavailable"}
		warnings = append(warnings, "用量采集器状态暂不可用")
	}
	if runtimeError != nil {
		server.logger.Warn("account container status is unavailable", zap.Error(runtimeError))
		warnings = append(warnings, "CPA 容器状态暂不可用")
	}
	if sinceReset && len(usageStartAtByAccount) < len(accounts) {
		warnings = append(warnings, fmt.Sprintf(
			"%d 个 CPA 未获得额度周期边界，本周期用量显示为不可用",
			len(accounts)-len(usageStartAtByAccount),
		))
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
		accountQuota := quotaByAccount[account.ID]
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
		operationalStatus := accountstatus.Present(account.GroupEnabled, state, stateAvailable && stateError == nil)
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
	c.JSON(http.StatusOK, gin.H{
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
		writeError(c, http.StatusBadRequest, "请选择要重置周限额的 CPA", "invalid_request")
		return
	}
	inspector, ok := server.quotaResetter.(quotaResetInspector)
	if !ok {
		writeError(c, http.StatusServiceUnavailable, "周限额重置详情服务尚未就绪", "quota_reset_not_ready")
		return
	}
	result, err := inspector.Inspect(c.Request.Context(), account)
	if err != nil {
		server.writeQuotaResetReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (server *Server) writeQuotaResetReadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, "周限额重置参数无效", "invalid_request")
	case errors.Is(err, quota.ErrResetAccountNotFound):
		writeError(c, http.StatusNotFound, "CPA 账号不存在", "account_not_found")
	case errors.Is(err, quota.ErrOAuthMissing):
		writeError(c, http.StatusConflict, "该 CPA 尚未完成 OAuth 授权", "quota_auth_missing")
	case errors.Is(err, quota.ErrAuthExpired):
		writeError(c, http.StatusConflict, "上游 OAuth 授权已失效，请重新完成 OAuth 后再重试", "quota_auth_expired")
	default:
		writeError(c, http.StatusBadGateway, "无法读取上游周限额重置详情，请稍后重试", "quota_upstream_unavailable")
	}
}

func (server *Server) createAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body accountlifecycle.CreateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "账号创建参数无效", "invalid_request")
		return
	}
	result, err := server.accountLifecycle.Create(c.Request.Context(), body)
	if err != nil {
		server.writeAccountLifecycleError(c, "create account", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "业务 CPA 已创建并通过运行探针", "account": result})
}

func (server *Server) updateAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
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
		writeError(c, http.StatusBadRequest, "账号更新参数无效", "invalid_request")
		return
	}
	currentID := strings.ToLower(strings.TrimSpace(body.ID))
	newID := strings.ToLower(strings.TrimSpace(body.NewID))
	if newID != "" && newID != currentID && strings.TrimSpace(body.Confirm) != currentID {
		writeError(c, http.StatusBadRequest, "重命名确认内容必须与当前 CPA 标识完全一致", "invalid_confirmation")
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
	message := "CPA 账号已更新并通过运行探针"
	if c.FullPath() == "/admin/api/accounts/policy" {
		message = "CPA 账号选择策略已更新"
	} else if result.RenamedFrom != "" {
		message = "CPA 已重命名、重建并通过运行探针"
	}
	c.JSON(http.StatusOK, gin.H{"message": message, "account": result})
}

func (server *Server) repairUnavailableAccountProxy(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID       string `json:"id" binding:"required"`
		ProxyURL string `json:"proxy_url" binding:"required"`
		Confirm  string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "代理恢复参数无效", "invalid_request")
		return
	}
	accountID, err := controlplane.NormalizeAccountID(body.ID)
	if err != nil || accountID != strings.TrimSpace(body.ID) ||
		strings.TrimSpace(body.Confirm) != "repair-proxy:"+accountID {
		writeError(c, http.StatusBadRequest, "代理恢复确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
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
	c.JSON(http.StatusOK, gin.H{
		"message": "不可用 CPA 的代理投影已恢复；后续账号维护将继续使用安全迁移与请求排空",
		"account": result,
	})
}

func (server *Server) clearAccountAuth(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID      string `json:"id" binding:"required"`
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Confirm) != strings.TrimSpace(body.ID) {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	result, err := server.accountLifecycle.ClearAuth(c.Request.Context(), body.ID)
	if err != nil {
		server.writeAccountLifecycleError(c, "clear account OAuth", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "OAuth 授权已清除，原文件已安全归档", "account": result})
}

func (server *Server) deleteAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID              string `json:"id" binding:"required"`
		Confirm         string `json:"confirm" binding:"required"`
		RevokeKeys      bool   `json:"revoke_keys"`
		FallbackAccount string `json:"fallback_account"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Confirm) != strings.TrimSpace(body.ID) {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	result, err := server.accountLifecycle.Delete(c.Request.Context(), accountlifecycle.DeleteRequest{
		AccountID: body.ID, FallbackAccount: body.FallbackAccount, RevokeExclusive: body.RevokeKeys,
	})
	if err != nil {
		server.writeAccountLifecycleError(c, "delete account", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "业务 CPA 已删除，配置、授权和日志已安全归档", "account": result})
}

func (server *Server) writeAccountLifecycleError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, "账号参数无效，请检查标识、邮箱、代理和确认字段", "invalid_request")
	case errors.Is(err, controlplane.ErrAccountLifecycleNotFound):
		writeError(c, http.StatusNotFound, "CPA 账号不存在", "account_not_found")
	case errors.Is(err, controlplane.ErrAccountAlreadyExists),
		errors.Is(err, controlplane.ErrAccountEmailAlreadyExists),
		errors.Is(err, controlplane.ErrAccountPortAlreadyExists):
		writeError(c, http.StatusConflict, "CPA 标识、邮箱或端口已被占用", "account_exists")
	case errors.Is(err, controlplane.ErrAccountDeleteLast):
		writeError(c, http.StatusConflict, "至少保留一个业务 CPA，不能删除最后一个账号", "account_last")
	case errors.Is(err, controlplane.ErrAccountDeleteRequiresRevoke):
		writeError(c, http.StatusConflict, "该 CPA 仍有独占有效 Key，请确认同时停用后再删除", "account_revoke_required")
	case errors.Is(err, controlplane.ErrAccountDeleteNeedsFallback):
		writeError(c, http.StatusConflict, "请选择其他已启用 CPA 作为安全迁移目标", "account_fallback_required")
	case errors.Is(err, controlplane.ErrAccountLifecycleConflict),
		errors.Is(err, accountlifecycle.ErrNoAccountPort),
		errors.Is(err, runtimeops.ErrRuntimeConflict):
		writeError(c, http.StatusConflict, "账号状态已变化或没有安全运行资源，未执行切换", "account_lifecycle_conflict")
	case errors.Is(err, controlplane.ErrLeaseLost):
		writeError(c, http.StatusServiceUnavailable, "控制面所有权已变化，操作已停止并回滚", "ownership_lost")
	case errors.Is(err, accountlifecycle.ErrRouteEvacuationUnavailable),
		errors.Is(err, accountlifecycle.ErrLifecycleRecoveryRequired):
		writeError(c, http.StatusServiceUnavailable, "账号安全迁移与恢复服务尚未就绪", "account_lifecycle_not_ready")
	case errors.Is(err, accountlifecycle.ErrAccountDrainTimeout):
		writeError(c, http.StatusConflict, "该 CPA 仍有进行中的 Codex 请求，账号未重建或删除，请稍后重试", "account_requests_active")
	case errors.Is(err, accountlifecycle.ErrUnavailableProxyRepairRejected):
		writeError(c, http.StatusConflict, "当前状态不满足受限代理恢复条件，请使用普通账号维护流程", "account_proxy_repair_unavailable")
	default:
		server.internalError(c, operation, err)
	}
}

func (server *Server) rebalanceAllAccounts(c *gin.Context) {
	var body struct {
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "rebalance-all" {
		writeError(c, http.StatusBadRequest, "请确认一键负载均衡全部账号", "invalid_request")
		return
	}
	if server.rebalancer == nil {
		writeError(c, http.StatusServiceUnavailable, "负载均衡服务尚未就绪", "rebalance_not_ready")
		return
	}
	result, err := server.rebalancer.RebalanceAll(c.Request.Context())
	if err != nil {
		switch {
		case errors.Is(err, failover.ErrRebalanceUnsafe),
			errors.Is(err, failover.ErrRebalanceUnavailable),
			errors.Is(err, controlplane.ErrRouteConflict),
			errors.Is(err, controlplane.ErrRouteUserUnsafe):
			writeError(c, http.StatusConflict, "当前用户或账号状态不满足安全迁移条件，未执行任何迁移", "account_rebalance_unavailable")
		default:
			server.internalError(c, "rebalance all accounts", err)
		}
		return
	}
	message := "账号已处于目标分布，无需迁移"
	if result.MovedUsers > 0 && result.ActivityRefreshed {
		message = "账号用户负载均衡已完成，" + formatActivityWindow(server.activityWindow()) + "活跃用户数已刷新"
	} else if result.MovedUsers > 0 {
		message = "账号用户负载均衡已完成，但" + formatActivityWindow(server.activityWindow()) + "活跃用户数刷新失败"
	}
	c.JSON(http.StatusOK, gin.H{
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
		writeError(c, http.StatusBadRequest, "账号迁移参数无效", "invalid_request")
		return
	}
	account := strings.ToLower(strings.TrimSpace(body.ID))
	if account == "" || strings.ToLower(strings.TrimSpace(body.Confirm)) != account {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	if server.accounts == nil {
		writeError(c, http.StatusServiceUnavailable, "账号目录服务尚未就绪", "accounts_not_ready")
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
		writeError(c, http.StatusNotFound, "CPA 账号不存在", "account_not_found")
		return
	}
	if server.rebalancer == nil {
		writeError(c, http.StatusServiceUnavailable, "负载均衡服务尚未就绪", "rebalance_not_ready")
		return
	}
	result, err := server.rebalancer.EvacuateAccount(c.Request.Context(), account)
	if err != nil {
		switch {
		case errors.Is(err, failover.ErrRebalanceUnsafe),
			errors.Is(err, failover.ErrRebalanceUnavailable),
			errors.Is(err, controlplane.ErrRouteConflict),
			errors.Is(err, controlplane.ErrRouteUserUnsafe):
			writeError(c, http.StatusConflict, "当前用户或账号状态不满足安全迁移条件，未执行任何迁移", "account_rebalance_unavailable")
		default:
			server.internalError(c, "rebalance account", err)
		}
		return
	}
	message := "该账号当前没有需要迁移的用户"
	if result.MovedUsers > 0 && result.ActivityRefreshed {
		message = "账号用户已全部安全迁移，" + formatActivityWindow(server.activityWindow()) + "活跃用户数已刷新"
	} else if result.MovedUsers > 0 {
		message = "账号用户已全部安全迁移，但" + formatActivityWindow(server.activityWindow()) + "活跃用户数刷新失败"
	}
	c.JSON(http.StatusOK, gin.H{
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
		writeError(c, http.StatusBadRequest, "周限额重置参数无效", "invalid_request")
		return
	}
	account := strings.ToLower(strings.TrimSpace(body.Account))
	if account == "" || strings.ToLower(strings.TrimSpace(body.Confirm)) != account {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	creditID := strings.TrimSpace(body.CreditID)
	if creditID == "" || len(creditID) > 512 {
		writeError(c, http.StatusBadRequest, "请选择要使用的重置额度", "invalid_request")
		return
	}
	if server.quotaResetter == nil {
		writeError(c, http.StatusServiceUnavailable, "周限额重置服务尚未就绪", "quota_reset_not_ready")
		return
	}
	result, err := server.quotaResetter.Reset(c.Request.Context(), account, creditID)
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrInvalidCatalogInput):
			writeError(c, http.StatusBadRequest, "周限额重置参数无效", "invalid_request")
		case errors.Is(err, quota.ErrResetAccountNotFound):
			writeError(c, http.StatusNotFound, "CPA 账号不存在", "account_not_found")
		case errors.Is(err, quota.ErrOAuthMissing):
			writeError(c, http.StatusConflict, "该 CPA 尚未完成 OAuth 授权", "quota_auth_missing")
		case errors.Is(err, quota.ErrAuthExpired):
			writeError(c, http.StatusConflict, "上游 OAuth 授权已失效，请重新完成 OAuth 后再重试", "quota_auth_expired")
		case errors.Is(err, quota.ErrResetCreditChanged):
			writeError(c, http.StatusConflict, "所选重置额度已使用、过期或不可用，请刷新列表后重新选择", "quota_reset_credit_changed")
		case errors.Is(err, quota.ErrResetUnavailable):
			writeError(c, http.StatusConflict, "当前没有已耗尽且可重置的周限额，请等待额度耗尽或刷新列表", "quota_reset_unavailable")
		case errors.Is(err, quota.ErrResetRejected):
			writeError(c, http.StatusConflict, "上游已拒绝本次重置周限额，请刷新周限额后重试", "quota_reset_rejected")
		case errors.Is(err, controlplane.ErrLeaseLost):
			writeError(c, http.StatusServiceUnavailable, "控制面所有权已变化，操作已停止", "ownership_lost")
		default:
			writeError(c, http.StatusBadGateway, "无法连接上游完成重置周限额，请稍后重试", "quota_upstream_unavailable")
		}
		return
	}
	message := "重置请求已处理，请刷新确认最新周限额"
	if result.WindowsReset > 0 {
		message = fmt.Sprintf("周限额已重置，共刷新 %d 个窗口", result.WindowsReset)
	}
	c.JSON(http.StatusOK, gin.H{
		"message":       message,
		"account":       result.Account,
		"windows":       result.Windows,
		"windows_reset": result.WindowsReset,
		"code":          result.Code,
		"credit":        result.Credit,
	})
}
