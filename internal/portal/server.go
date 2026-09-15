package portal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountstatus"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/identity"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/sitetime"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	sessionCookieName = "cpa_user_session"
	defaultSessionTTL = 12 * time.Hour
	maximumBodySize   = int64(1 << 20)
)

type IdentityStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
	ReadKeyRecordsForUsers(context.Context, []string) ([]controlplane.KeyRecord, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadSecret(context.Context, string) (string, bool, error)
}

type SessionStore interface {
	CreateSession(context.Context, string, time.Duration) (string, usage.PortalSession, error)
	ResolveSession(context.Context, string) (usage.PortalSession, error)
	RevokeSession(context.Context, string) error
	Credential(context.Context, string) (usage.PortalCredential, error)
	SetCredential(context.Context, string, string, bool, string) (usage.PortalCredential, error)
}

type UsageReader interface {
	UserAccounts(context.Context, string, int64, *int64) (usage.UserAccountSummary, error)
	UserBreakdown(context.Context, string, string, int64, *int64) (usage.UserBreakdown, error)
	UserDailyTrend(
		context.Context, string, int, string, int64, usage.UserTrendDimension,
	) (usage.UserDailyTrend, error)
}

// QuotaReader deliberately keeps the natural-week quota read separate from
// session/credential ownership. Portal pages can therefore request the
// summary only when it is visible without widening the session store API.
type QuotaReader interface {
	WeeklyQuota(context.Context, string, *int64) (usage.WeeklyQuota, error)
}

type RouteChanger interface {
	MoveUser(context.Context, string, string, string) (failover.RebalanceResult, error)
}

type KeyRotator interface {
	RotateUserKey(context.Context, string, string) (identity.RotationResult, error)
}

type QuotaStateStore interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
}

type PublicUsageReader interface {
	PublicGatewayUsage(context.Context, []string, int64, int64) (map[string]usage.PublicAccountUsage, error)
}

type InflightKeyReader interface {
	InflightKeyCounts(context.Context) (map[string]int64, error)
}

type Config struct {
	Identity      IdentityStore
	Sessions      SessionStore
	Usage         UsageReader
	Quotas        QuotaReader
	States        failover.AccountStateProvider
	ListStates    failover.AccountStateProvider
	Activity      failover.ActivityProvider
	Routes        RouteChanger
	Keys          KeyRotator
	QuotaStore    QuotaStateStore
	PublicUsage   PublicUsageReader
	Inflight      InflightKeyReader
	Logger        *zap.Logger
	Now           func() time.Time
	SessionTTL    time.Duration
	SecureCookies bool
	LoginLimiter  *LoginLimiter
}

type Server struct {
	identity      IdentityStore
	sessions      SessionStore
	usage         UsageReader
	quotas        QuotaReader
	states        failover.AccountStateProvider
	listStates    failover.AccountStateProvider
	activity      failover.ActivityProvider
	routes        RouteChanger
	keys          KeyRotator
	quotaStore    QuotaStateStore
	publicUsage   PublicUsageReader
	inflight      InflightKeyReader
	logger        *zap.Logger
	now           func() time.Time
	sessionTTL    time.Duration
	secureCookies bool
	loginLimiter  *LoginLimiter
}

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code"`
}

type portalAuth struct {
	Session    usage.PortalSession
	Credential usage.PortalCredential
	Token      string
	Records    []controlplane.KeyRecord
	APIKey     string
}

type usageWindow struct {
	Name     any    `json:"window"`
	Seconds  *int64 `json:"window_seconds"`
	StartAt  int64  `json:"window_start_at"`
	EndAt    int64  `json:"window_end_at"`
	Timezone string `json:"window_timezone,omitempty"`
}

func New(config Config) (*Server, error) {
	if config.Identity == nil || config.Sessions == nil {
		return nil, errors.New("portal server requires identity and session stores")
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.SessionTTL > 30*24*time.Hour {
		return nil, errors.New("portal session TTL must not exceed 30 days")
	}
	if config.LoginLimiter == nil {
		config.LoginLimiter = NewLoginLimiter(config.Now)
	}
	if config.ListStates == nil {
		config.ListStates = config.States
	}
	return &Server{
		identity: config.Identity, sessions: config.Sessions, usage: config.Usage, quotas: config.Quotas,
		states: config.States, activity: config.Activity, routes: config.Routes, keys: config.Keys,
		listStates:  config.ListStates,
		quotaStore:  config.QuotaStore,
		publicUsage: config.PublicUsage, inflight: config.Inflight,
		logger: config.Logger, now: config.Now, sessionTTL: config.SessionTTL,
		secureCookies: config.SecureCookies, loginLimiter: config.LoginLimiter,
	}, nil
}

func (server *Server) Register(router gin.IRouter) {
	router = router.Group("", httpi18n.Middleware())
	usageRoutes := router.Group("/usage")
	usageRoutes.Use(server.noStore(), server.recovery())
	usageRoutes.POST("/session", server.limitBody(), server.createSession)
	usageRoutes.GET("/session", server.readSession)
	usageRoutes.DELETE("/session", server.deleteSession)
	usageRoutes.GET("/me/profile", server.readProfile)
	usageRoutes.GET("/me/key", server.readKey)
	usageRoutes.GET("/me/quota", server.readQuota)
	usageRoutes.GET("/me/accounts", server.readAccounts)
	usageRoutes.GET("/me/route", server.readRoute)
	usageRoutes.POST("/me/route/auto-assign", server.autoAssignRoute)
	usageRoutes.GET("/me/usage-breakdown", server.readUsageBreakdown)
	usageRoutes.GET("/me/usage-trend", server.readUsageTrend)
	usageRoutes.PUT("/me/password", server.limitBody(), server.changePassword)
	usageRoutes.PUT("/me/group", server.limitBody(), server.changeRoute)
	usageRoutes.POST("/me/key/rotate", server.limitBody(), server.rotateKey)
	usageRoutes.GET("/limits", server.readUsageLimits)
	usageRoutes.GET("/api", server.readPublicGatewayUsage)
}

type publicGatewayUsageRow struct {
	Account      string `json:"account"`
	InflightKeys int64  `json:"inflight_keys"`
	ActiveKeys   int64  `json:"active_keys"`
	RequestCount int64  `json:"request_count"`
}

func (server *Server) readPublicGatewayUsage(c *gin.Context) {
	if server.publicUsage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("portal.public_usage_service_is_not_ready"), "public_usage_not_ready")
		return
	}
	window := int64(300)
	if raw := strings.TrimSpace(c.Query("window")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_reporting_range"), "invalid_request")
			return
		}
		window = parsed
	}
	if window != 300 && window != 3600 && window != 86400 {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_reporting_range"), "invalid_request")
		return
	}
	accounts, err := server.identity.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read public usage accounts", err)
		return
	}
	accountIDs := make([]string, 0, len(accounts))
	for _, account := range accounts {
		accountIDs = append(accountIDs, account.ID)
	}
	now := server.now().Unix()
	usageByAccount, err := server.publicUsage.PublicGatewayUsage(
		c.Request.Context(), accountIDs, now-window, now+1,
	)
	if err != nil {
		server.internalError(c, "read public gateway usage", err)
		return
	}
	inflight := make(map[string]int64)
	if server.inflight != nil {
		if values, readErr := server.inflight.InflightKeyCounts(c.Request.Context()); readErr == nil {
			inflight = values
		} else {
			server.logger.Warn("public in-flight usage unavailable", zap.Error(readErr))
		}
	}
	rows := make([]publicGatewayUsageRow, 0, len(accountIDs))
	totals := gin.H{"inflight_keys": int64(0), "active_keys": int64(0), "requests": int64(0)}
	for _, accountID := range accountIDs {
		metrics := usageByAccount[accountID]
		row := publicGatewayUsageRow{
			Account: accountID, InflightKeys: inflight[accountID],
			ActiveKeys: metrics.ActiveKeys, RequestCount: metrics.RequestCount,
		}
		rows = append(rows, row)
		totals["inflight_keys"] = totals["inflight_keys"].(int64) + row.InflightKeys
		totals["active_keys"] = totals["active_keys"].(int64) + row.ActiveKeys
		totals["requests"] = totals["requests"].(int64) + row.RequestCount
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": now, "window_seconds": window, "truncated": false,
		"cached": false, "totals": totals, "accounts": rows,
	})
}

type publicWeeklyWindow struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	MeteredFeature      *string `json:"metered_feature"`
	WindowSlot          string  `json:"window_slot"`
	UsedPercent         float64 `json:"used_percent"`
	RemainingPercent    float64 `json:"remaining_percent"`
	ReportedUsedPercent float64 `json:"reported_used_percent"`
	ResetAt             *int64  `json:"reset_at"`
	ResetAfterSeconds   *int64  `json:"reset_after_seconds"`
	WindowSeconds       int64   `json:"window_seconds"`
	LimitReached        bool    `json:"limit_reached"`
}

type publicAccountQuota struct {
	Account       string               `json:"account"`
	Status        string               `json:"status"`
	PlanType      *string              `json:"plan_type"`
	Allowed       *bool                `json:"allowed"`
	LimitReached  *bool                `json:"limit_reached"`
	Weekly        *publicWeeklyWindow  `json:"weekly"`
	WeeklyWindows []publicWeeklyWindow `json:"weekly_windows"`
}

func (server *Server) readUsageLimits(c *gin.Context) {
	if server.quotaStore == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("portal.account_weekly_quota_service_is_not_ready"), "usage_limits_not_ready")
		return
	}
	state, found, err := quota.ReadState(c.Request.Context(), server.quotaStore)
	if err != nil {
		server.internalError(c, "read public usage limits", err)
		return
	}
	snapshot := state.Snapshot
	if !found || state.Version == 0 {
		snapshot = quota.Snapshot{Accounts: []quota.AccountQuota{}}
	}
	accounts := make([]publicAccountQuota, 0, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		windows := make([]publicWeeklyWindow, 0, len(account.WeeklyWindows))
		for _, window := range account.WeeklyWindows {
			windows = append(windows, sanitizeWeeklyWindow(window.WithLanguage(httpi18n.Locale(c))))
		}
		var weekly *publicWeeklyWindow
		if account.Weekly != nil {
			value := sanitizeWeeklyWindow(account.Weekly.WithLanguage(httpi18n.Locale(c)))
			weekly = &value
		}
		accounts = append(accounts, publicAccountQuota{
			Account: account.Account, Status: account.Status, PlanType: account.PlanType,
			Allowed: account.Allowed, LimitReached: account.LimitReached,
			Weekly: weekly, WeeklyWindows: windows,
		})
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": snapshot.GeneratedAt, "cache_ttl_seconds": snapshot.CacheTTLSeconds,
		"cached": snapshot.Cached, "refreshing": snapshot.Refreshing, "accounts": accounts,
	})
}

func sanitizeWeeklyWindow(window quota.WeeklyWindow) publicWeeklyWindow {
	return publicWeeklyWindow{
		Key: window.Key, Label: window.Label, MeteredFeature: window.MeteredFeature,
		WindowSlot: window.WindowSlot, UsedPercent: window.UsedPercent,
		RemainingPercent: window.RemainingPercent, ReportedUsedPercent: window.ReportedUsedPercent,
		ResetAt: window.ResetAt, ResetAfterSeconds: window.ResetAfterSeconds,
		WindowSeconds: window.WindowSeconds, LimitReached: window.LimitReached,
	}
}

func (server *Server) createSession(c *gin.Context) {
	var body struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || ValidateCurrentPassword(body.Password) != nil {
		writeError(c, http.StatusBadRequest, i18n.M("portal.invalid_email_or_password_format"), "invalid_request")
		return
	}
	user := normalizeEmail(body.Email)
	identityDigest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(body.Email))))
	limitKeys := []string{
		"portal-ip:" + c.ClientIP(),
		"portal-account:" + hex.EncodeToString(identityDigest[:8]),
	}
	if allowed, retry := server.loginLimiter.Allow(limitKeys...); !allowed {
		seconds := max(int(math.Ceil(retry.Seconds())), 1)
		c.Header("Retry-After", strconv.Itoa(seconds))
		writeError(c, http.StatusTooManyRequests, i18n.M("portal.too_many_sign_in_attempts_please_try_again_later"), "rate_limited")
		return
	}
	records, recordError := server.activeRecords(c.Request.Context(), user)
	credential, credentialError := server.sessions.Credential(c.Request.Context(), user)
	encoded := DummyPasswordHash()
	if credentialError == nil {
		encoded = credential.PasswordHash
	}
	passwordMatches := VerifyPassword(body.Password, encoded)
	if recordError != nil || credentialError != nil || !passwordMatches {
		if credentialError != nil && !errors.Is(credentialError, usage.ErrPortalCredentialNotFound) {
			server.internalError(c, "read portal credential", credentialError)
			return
		}
		if recordError != nil && !errors.Is(recordError, errPortalUserUnavailable) &&
			!errors.Is(recordError, errPortalKeyMigrating) {
			server.internalError(c, "read portal identity", recordError)
			return
		}
		writeError(c, http.StatusUnauthorized, i18n.M("portal.incorrect_email_or_password"), "invalid_credentials")
		return
	}
	_ = records
	server.loginLimiter.Forget(limitKeys...)
	token, session, err := server.sessions.CreateSession(c.Request.Context(), user, server.sessionTTL)
	if err != nil {
		server.internalError(c, "create portal session", err)
		return
	}
	server.writeSessionCookie(c, token, session.ExpiresAt)
	httpi18n.JSON(c, http.StatusCreated, gin.H{
		"authenticated": true, "user": user, "expires_at": session.ExpiresAt,
		"password_change_required": credential.MustChange,
	})
}

func (server *Server) readSession(c *gin.Context) {
	auth, ok := server.requireAuth(c, true)
	if !ok {
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"authenticated": true, "user": auth.Session.User,
		"expires_at":               auth.Session.ExpiresAt,
		"password_change_required": auth.Credential.MustChange,
	})
}

func (server *Server) deleteSession(c *gin.Context) {
	token := server.sessionToken(c)
	if err := server.sessions.RevokeSession(c.Request.Context(), token); err != nil {
		server.internalError(c, "revoke portal session", err)
		return
	}
	server.writeSessionCookie(c, "", 0)
	httpi18n.JSON(c, http.StatusOK, gin.H{"logged_out": true})
}

func (server *Server) readProfile(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal route", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"user":          auth.Session.User,
		"current_group": routes[auth.Session.User], "generated_at": server.now().Unix(),
	})
}

func (server *Server) readKey(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"api_key": auth.APIKey, "generated_at": server.now().Unix(),
	})
}

type portalWeeklyQuota struct {
	usage.WeeklyQuota
	PersonalPolicyResetEnabled bool `json:"personal_policy_reset_enabled"`
}

func (server *Server) readQuota(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.quotas == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("portal.personal_weekly_quota_service_is_not_ready"), "quota_not_ready")
		return
	}
	settings, err := server.identity.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal quota settings", err)
		return
	}
	defaultLimit, resetOnNewWeek, err := portalQuotaConfiguration(settings)
	if err != nil {
		server.internalError(c, "parse portal quota settings", err)
		return
	}
	weekly, err := server.quotas.WeeklyQuota(c.Request.Context(), auth.Session.User, defaultLimit)
	if err != nil {
		server.internalError(c, "read portal weekly quota", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": server.now().Unix(),
		"weekly_quota": portalWeeklyQuota{
			WeeklyQuota: weekly, PersonalPolicyResetEnabled: resetOnNewWeek,
		},
	})
}

func portalQuotaConfiguration(settings map[string]any) (*int64, bool, error) {
	resetOnNewWeek := true
	if raw, found := settings["user_quota.reset_personal_weekly_on_new_week"]; found {
		value, valid := raw.(bool)
		if !valid {
			return nil, false, errors.New("user quota reset policy must be a boolean")
		}
		resetOnNewWeek = value
	}
	var defaultLimit *int64
	if raw, found := settings["user_quota.default_weekly_tokens"]; found && raw != nil {
		var value int64
		switch typed := raw.(type) {
		case int:
			value = int64(typed)
		case int64:
			value = typed
		case float64:
			value = int64(typed)
			if float64(value) != typed {
				return nil, false, errors.New("default weekly quota must be a positive integer or null")
			}
		default:
			return nil, false, errors.New("default weekly quota must be a positive integer or null")
		}
		if value <= 0 || value > 1_000_000_000_000 {
			return nil, false, errors.New("default weekly quota is outside the supported range")
		}
		defaultLimit = &value
	}
	return defaultLimit, resetOnNewWeek, nil
}

func (server *Server) readRoute(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal route", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"current_group": routes[auth.Session.User], "generated_at": server.now().Unix()})
}

func (server *Server) readAccounts(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	window, err := server.parseUsageWindow(c.Request.Context(), c.Query("window"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	if c.Query("fresh") == "1" {
		refreshStore, ready := server.quotaStore.(quota.RefreshRequestStore)
		if !ready {
			writeError(c, http.StatusServiceUnavailable, i18n.M("portal.quota_refresh_service_is_not_ready"), "quota_refresh_not_ready")
			return
		}
		if _, _, err := quota.RequestRefresh(c.Request.Context(), refreshStore, server.now()); err != nil {
			server.internalError(c, "request official quota refresh", err)
			return
		}
	}

	accounts, err := server.identity.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal accounts", err)
		return
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal routes", err)
		return
	}
	accountUsage, err := server.usage.UserAccounts(
		c.Request.Context(), auth.Session.User, window.StartAt, &window.EndAt,
	)
	if err != nil {
		server.internalError(c, "read portal account usage", err)
		return
	}
	usageByAccount := make(map[string]usage.WeightedMetrics, len(accountUsage.Accounts))
	for _, item := range accountUsage.Accounts {
		usageByAccount[item.Account] = item.WeightedMetrics
	}
	states := make(map[string]failover.AccountState)
	warnings := make([]string, 0, 2)
	if server.listStates != nil {
		if loaded, stateError := server.listStates.AccountStates(c.Request.Context()); stateError == nil {
			states = loaded
		} else {
			warnings = append(warnings, httpi18n.Text(c, "admin.account_quota_status_is_unavailable_and_is_shown_as_unknown"))
			server.logger.Warn("portal account state unavailable", zap.Error(stateError))
		}
	}
	refreshing := false
	if server.quotaStore != nil {
		request, _, err := quota.ReadRefreshRequest(c.Request.Context(), server.quotaStore)
		if err != nil {
			warnings = append(warnings, httpi18n.Text(c, "portal.quota_refresh_status_is_unavailable"))
		} else {
			refreshing = request.Pending()
		}
	}

	activity := make(map[string]int)
	if server.activity != nil {
		var loaded map[string]int
		var activityError error
		if configured, ok := server.activity.(interface {
			RefreshActiveUsers(context.Context) (map[string]int, error)
		}); ok {
			loaded, activityError = configured.RefreshActiveUsers(c.Request.Context())
		} else {
			loaded, activityError = server.activity.RefreshActiveUsersLastHour(c.Request.Context())
		}
		if activityError == nil {
			activity = loaded
		} else {
			warnings = append(warnings, httpi18n.Text(c, "admin.activity_unavailable", i18n.Params{"Window": formatPortalActivityWindow(server.activity, httpi18n.Locale(c))}))
			server.logger.Warn("portal account activity unavailable", zap.Error(activityError))
		}
	}
	items := make([]gin.H, 0, len(auth.Records))
	for _, account := range accounts {
		if !recordHasAccount(auth.Records, account.ID) {
			continue
		}
		state, stateFound := states[account.ID]
		presentation := presentAccountState(account, state, stateFound, httpi18n.Locale(c))
		items = append(items, gin.H{
			"id": account.ID, "email": account.Email, "display_name": account.Email,
			"current": routes[auth.Session.User] == account.ID,
			"enabled": account.GroupEnabled, "selectable": presentation.Selectable,
			"status": presentation, "active_users_1h": activity[account.ID],
			"usage": usageByAccount[account.ID],
		})
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": server.now().Unix(), "window": window,
		"active_user_window_seconds": activeUserWindowSeconds(server.activity),
		"current_group":              routes[auth.Session.User], "accounts": items,
		"totals": accountUsage.Totals, "warnings": warnings, "quota_refreshing": refreshing,
	})
}

func activeUserWindowSeconds(provider failover.ActivityProvider) int64 {
	if reader, ok := provider.(*usage.Store); ok {
		return int64(reader.ActiveUserWindow() / time.Second)
	}
	return int64(usage.DefaultActiveUserWindow / time.Second)
}

func formatPortalActivityWindow(provider failover.ActivityProvider, languages ...i18n.Language) string {
	seconds := activeUserWindowSeconds(provider)
	minutes := (seconds + 59) / 60
	if minutes%60 == 0 {
		return i18n.M("admin.last_hours", i18n.Params{"Count": minutes / 60}).Render(i18n.Selected(languages))
	}
	return i18n.M("admin.last_minutes", i18n.Params{"Count": minutes}).Render(i18n.Selected(languages))
}

func (server *Server) readUsageBreakdown(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	account := strings.TrimSpace(c.Query("account"))
	if account != "" && !recordHasAccount(auth.Records, account) {
		writeError(c, http.StatusNotFound, i18n.M("portal.account_does_not_exist"), "account_not_found")
		return
	}
	window, err := server.parseUsageWindow(c.Request.Context(), c.Query("window"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	breakdown, err := server.usage.UserBreakdown(
		c.Request.Context(), auth.Session.User, account, window.StartAt, &window.EndAt,
	)
	if err != nil {
		server.internalError(c, "read portal usage breakdown", err)
		return
	}
	multipliers, err := server.currentUsageMultipliers(c.Request.Context())
	if err != nil {
		server.internalError(c, "read current usage multipliers", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": server.now().Unix(), "window": window.Name,
		"window_seconds": window.Seconds, "window_start_at": window.StartAt,
		"window_end_at": window.EndAt, "window_timezone": window.Timezone,
		"account": optionalAccount(account), "user": auth.Session.User,
		"definition":            httpi18n.Text(c, "portal.includes_only_business_requests_persisted_by_the_collector_weighted_tokens"),
		"collection_started_at": breakdown.CollectionStartedAt,
		"effective_start_at":    breakdown.EffectiveStartAt,
		"totals":                breakdown.Totals, "models": breakdown.Models,
		"reasoning_efforts": breakdown.ReasoningEfforts, "combinations": breakdown.Combinations,
		"current_multipliers": multipliers,
	})
}

func (server *Server) readUsageTrend(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	if _, supplied := c.Request.URL.Query()["user"]; supplied {
		writeError(c, http.StatusBadRequest, i18n.M("portal.personal_trends_do_not_accept_a_user_parameter"), "invalid_request")
		return
	}
	window := strings.ToLower(strings.TrimSpace(c.Query("window")))
	if window == "" {
		window = "30d"
	}
	windowDays, found := map[string]int{"7d": 7, "30d": 30, "90d": 90}[window]
	if !found {
		writeError(c, http.StatusBadRequest, i18n.M("portal.invalid_trend_reporting_range"), "invalid_request")
		return
	}
	dimension := usage.UserTrendDimension(strings.ToLower(strings.TrimSpace(c.Query("dimension"))))
	if dimension == "" {
		dimension = usage.UserTrendTotal
	}
	if dimension != usage.UserTrendTotal && dimension != usage.UserTrendModelReasoning {
		writeError(c, http.StatusBadRequest, i18n.M("portal.invalid_trend_dimension"), "invalid_request")
		return
	}
	timezone, err := server.usageTimezone(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	trend, err := server.usage.UserDailyTrend(
		c.Request.Context(), auth.Session.User, windowDays, timezone, server.now().Unix(), dimension,
	)
	if err != nil {
		server.internalError(c, "read portal daily usage trend", err)
		return
	}
	multipliers, err := server.currentUsageMultipliers(c.Request.Context())
	if err != nil {
		server.internalError(c, "read current usage multipliers", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": server.now().Unix(), "window": window, "window_days": trend.WindowDays,
		"window_start_at": trend.WindowStartAt, "window_end_at": trend.WindowEndAt,
		"window_timezone": trend.Timezone, "dimension": trend.Dimension,
		"definition":            httpi18n.Text(c, "portal.includes_only_business_requests_persisted_by_the_collector_grouped_by"),
		"collection_started_at": trend.CollectionStartedAt,
		"effective_start_at":    trend.EffectiveStartAt,
		"days":                  trend.Days,
		"current_multipliers":   multipliers,
	})
}

func (server *Server) changePassword(c *gin.Context) {
	auth, ok := server.requireAuth(c, true)
	if !ok {
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_password_format"), "invalid_password")
		return
	}
	if err := ValidateCurrentPassword(body.CurrentPassword); err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_password")
		return
	}
	if err := ValidateNewPassword(body.NewPassword); err != nil {
		writeError(c, http.StatusBadRequest, err, "weak_password")
		return
	}
	if !VerifyPassword(body.CurrentPassword, auth.Credential.PasswordHash) {
		writeError(c, http.StatusUnauthorized, i18n.M("portal.incorrect_current_password"), "invalid_current_password")
		return
	}
	if auth.Credential.MustChange && constantTimeEqual(body.NewPassword, body.CurrentPassword) {
		writeError(c, http.StatusBadRequest, i18n.M("portal.the_new_password_must_differ_from_the_initial_password"), "weak_password")
		return
	}
	initialPassword, found, err := server.identity.ReadSecret(c.Request.Context(), "portal_initial_password")
	if err != nil {
		server.internalError(c, "read initial portal password", err)
		return
	}
	if found && constantTimeEqual(body.NewPassword, initialPassword) {
		writeError(c, http.StatusBadRequest, i18n.M("portal.the_new_password_must_differ_from_the_initial_password"), "weak_password")
		return
	}
	encoded, err := HashPassword(body.NewPassword)
	if err != nil {
		server.internalError(c, "hash portal password", err)
		return
	}
	if _, err := server.sessions.SetCredential(
		c.Request.Context(), auth.Session.User, encoded, false, auth.Token,
	); err != nil {
		server.internalError(c, "update portal password", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"message": i18n.M("portal.password_changed"), "password_change_required": false})
}

func (server *Server) changeRoute(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	var body struct {
		GroupID string `json:"group_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("portal.target_account_is_required"), "invalid_request")
		return
	}
	target := strings.TrimSpace(body.GroupID)
	if !recordHasAccount(auth.Records, target) {
		writeError(c, http.StatusNotFound, i18n.M("portal.target_account_does_not_exist"), "account_not_found")
		return
	}
	accounts, err := server.identity.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal accounts", err)
		return
	}
	account, found := accountByID(accounts, target)
	if !found || !account.GroupEnabled {
		writeError(c, http.StatusConflict, i18n.M("portal.the_target_account_cannot_currently_be_selected"), "account_unavailable")
		return
	}
	if server.states != nil {
		states, stateError := server.states.AccountStates(c.Request.Context())
		if stateError != nil {
			server.internalError(c, "read portal account state", stateError)
			return
		}
		state, stateFound := states[target]
		if !presentAccountState(account, state, stateFound).Selectable {
			writeError(c, http.StatusConflict, i18n.M("portal.the_target_account_cannot_currently_be_selected"), "account_unavailable")
			return
		}
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal route", err)
		return
	}
	current := routes[auth.Session.User]
	if current == target {
		httpi18n.JSON(c, http.StatusOK, gin.H{"message": i18n.M("portal.this_account_is_already_selected"), "current_group": target, "changed": false})
		return
	}
	if server.routes == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("portal.route_switching_service_is_not_ready"), "route_change_not_ready")
		return
	}
	result, err := server.routes.MoveUser(c.Request.Context(), auth.Session.User, target, current)
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrRouteConflict):
			writeError(c, http.StatusConflict, i18n.M("portal.your_current_account_changed_refresh_and_try_again"), "route_conflict")
		case errors.Is(err, controlplane.ErrRouteUserUnsafe), errors.Is(err, controlplane.ErrRouteTargetNotFound):
			writeError(c, http.StatusConflict, i18n.M("portal.the_user_or_target_account_does_not_meet_safe_switching"), "route_unavailable")
		default:
			server.internalError(c, "change portal route", err)
		}
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("portal.current_account_switched"), "current_group": target, "changed": result.MovedUsers > 0,
		"snapshot_generation": result.SnapshotGeneration,
	})
}

func (server *Server) autoAssignRoute(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal route for automatic assignment", err)
		return
	}
	if current := strings.TrimSpace(routes[auth.Session.User]); current != "" {
		httpi18n.JSON(c, http.StatusOK, gin.H{
			"message": i18n.M("portal.the_user_already_has_an_account"), "current_group": current, "changed": false,
		})
		return
	}
	if server.states == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("portal.account_quota_status_service_is_not_ready"), "account_state_not_ready")
		return
	}
	if server.routes == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("portal.route_switching_service_is_not_ready"), "route_change_not_ready")
		return
	}
	accounts, err := server.identity.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal accounts for automatic assignment", err)
		return
	}
	states, err := server.states.AccountStates(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal account state for automatic assignment", err)
		return
	}
	candidates := make([]string, 0, len(auth.Records))
	for _, account := range accounts {
		if account.GroupEnabled && recordHasAccount(auth.Records, account.ID) {
			candidates = append(candidates, account.ID)
		}
	}
	target, found := failover.LeastUsedEligibleAccount(candidates, states)
	if !found {
		writeError(c, http.StatusConflict, i18n.M("portal.no_available_account_has_reliable_quota_status"), "route_unavailable")
		return
	}
	result, err := server.routes.MoveUser(c.Request.Context(), auth.Session.User, target, "")
	if err != nil {
		if errors.Is(err, controlplane.ErrRouteConflict) {
			latest, readError := server.identity.ReadRoutes(c.Request.Context())
			if readError == nil {
				if current := strings.TrimSpace(latest[auth.Session.User]); current != "" {
					httpi18n.JSON(c, http.StatusOK, gin.H{
						"message": i18n.M("portal.the_user_already_has_an_account"), "current_group": current, "changed": false,
					})
					return
				}
			}
			writeError(c, http.StatusConflict, i18n.M("portal.account_assignment_state_changed_please_try_again"), "route_conflict")
			return
		}
		if errors.Is(err, controlplane.ErrRouteUserUnsafe) || errors.Is(err, controlplane.ErrRouteTargetNotFound) {
			writeError(c, http.StatusConflict, i18n.M("portal.the_user_or_target_account_does_not_meet_safe_assignment"), "route_unavailable")
			return
		}
		server.internalError(c, "automatically assign portal route", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("portal.current_account_assigned_automatically"), "current_group": target, "changed": result.MovedUsers > 0,
		"snapshot_generation": result.SnapshotGeneration,
	})
}

func (server *Server) rotateKey(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	var body struct {
		Confirm bool `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !body.Confirm {
		writeError(c, http.StatusBadRequest, i18n.M("portal.confirm_refreshing_the_api_key"), "confirmation_required")
		return
	}
	if server.keys == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("portal.api_key_refresh_service_is_not_ready"), "key_rotation_not_ready")
		return
	}
	result, err := server.keys.RotateUserKey(c.Request.Context(), auth.Session.User, auth.APIKey)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrRotationConflict), errors.Is(err, identity.ErrRotationUnsafe):
			writeError(c, http.StatusConflict, i18n.M("portal.api_key_state_changed_refresh_and_try_again"), "key_rotation_conflict")
		default:
			server.internalError(c, "rotate portal API key", err)
		}
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("portal.api_key_refreshed_the_old_key_expired_update_your_client"),
		"api_key": result.APIKey, "snapshot_generation": result.SnapshotGeneration,
	})
}

var (
	errPortalUserUnavailable = errors.New("portal user unavailable")
	errPortalKeyMigrating    = errors.New("portal key migration required")
)

func (server *Server) requireAuth(c *gin.Context, allowPasswordChange bool) (portalAuth, bool) {
	token := server.sessionToken(c)
	session, err := server.sessions.ResolveSession(c.Request.Context(), token)
	if err != nil {
		if errors.Is(err, usage.ErrPortalSessionNotFound) {
			writeError(c, http.StatusUnauthorized, i18n.M("portal.your_session_expired"), "session_required")
		} else {
			server.internalError(c, "resolve portal session", err)
		}
		return portalAuth{}, false
	}
	records, err := server.activeRecords(c.Request.Context(), session.User)
	if err != nil {
		if errors.Is(err, errPortalUserUnavailable) || errors.Is(err, errPortalKeyMigrating) {
			_ = server.sessions.RevokeSession(c.Request.Context(), token)
			writeError(c, http.StatusUnauthorized, i18n.M("portal.the_user_is_disabled_deleted_or_its_key_is_being"), "session_required")
		} else {
			server.internalError(c, "validate portal identity", err)
		}
		return portalAuth{}, false
	}
	credential, err := server.sessions.Credential(c.Request.Context(), session.User)
	if err != nil {
		if errors.Is(err, usage.ErrPortalCredentialNotFound) {
			_ = server.sessions.RevokeSession(c.Request.Context(), token)
			writeError(c, http.StatusUnauthorized, i18n.M("portal.user_credentials_are_uninitialized_or_invalid"), "session_required")
		} else {
			server.internalError(c, "read portal credential", err)
		}
		return portalAuth{}, false
	}
	if credential.MustChange && !allowPasswordChange {
		writeError(c, http.StatusForbidden, i18n.M("portal.change_the_initial_password_before_continuing"), "password_change_required")
		return portalAuth{}, false
	}
	return portalAuth{
		Session: session, Credential: credential, Token: token,
		Records: records, APIKey: records[0].Key,
	}, true
}

func (server *Server) activeRecords(ctx context.Context, user string) ([]controlplane.KeyRecord, error) {
	if user == "" {
		return nil, errPortalUserUnavailable
	}
	records, err := server.identity.ReadKeyRecordsForUsers(ctx, []string{user})
	if err != nil {
		return nil, err
	}
	active := make([]controlplane.KeyRecord, 0, len(records))
	keys := make(map[string]struct{})
	for _, record := range records {
		if record.Status != "active" || !strings.EqualFold(strings.TrimSpace(record.User), user) {
			continue
		}
		active = append(active, record)
		keys[record.Key] = struct{}{}
	}
	if len(active) == 0 {
		return nil, errPortalUserUnavailable
	}
	if len(keys) != 1 {
		return nil, errPortalKeyMigrating
	}
	sort.Slice(active, func(left, right int) bool { return active[left].Label < active[right].Label })
	return active, nil
}

func (server *Server) parseUsageWindow(ctx context.Context, raw string) (usageWindow, error) {
	now := server.now()
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		value = "today"
	}
	if value == "today" || value == "current_week" {
		timezone, err := server.usageTimezone(ctx)
		if err != nil {
			return usageWindow{}, err
		}
		location, err := time.LoadLocation(timezone)
		if err != nil {
			return usageWindow{}, i18n.M("portal.invalid_usage_timezone_configuration")
		}
		local := now.In(location)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
		if value == "current_week" {
			daysSinceMonday := (int(local.Weekday()) + 6) % 7
			start = start.AddDate(0, 0, -daysSinceMonday)
		}
		return usageWindow{Name: value, StartAt: start.Unix(), EndAt: now.Unix(), Timezone: timezone}, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return usageWindow{}, i18n.M("admin.invalid_reporting_range")
	}
	allowed := map[int64]struct{}{3600: {}, 86400: {}, 604800: {}, 2592000: {}}
	if _, found := allowed[seconds]; !found {
		return usageWindow{}, i18n.M("admin.invalid_reporting_range")
	}
	return usageWindow{Name: seconds, Seconds: &seconds, StartAt: now.Unix() - seconds, EndAt: now.Unix()}, nil
}

func (server *Server) usageTimezone(ctx context.Context) (string, error) {
	settings, err := server.identity.ReadSettings(ctx)
	if err != nil {
		return "", i18n.M("portal.unable_to_read_the_usage_timezone")
	}
	timezone, err := sitetime.Name(settings)
	if err != nil {
		return "", i18n.M("portal.invalid_usage_timezone_configuration")
	}
	return timezone, nil
}

type accountStatus struct {
	accountstatus.Presentation
	UsedPercent      *float64 `json:"used_percent,omitempty"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	ResetAt          int64    `json:"reset_at,omitempty"`
}

func presentAccountState(account controlplane.Account, state failover.AccountState, found bool, languages ...i18n.Language) accountStatus {
	return accountStatus{
		Presentation: accountstatus.Present(account.GroupEnabled, state, found, languages...),
		UsedPercent:  state.UsedPercent, RemainingPercent: state.RemainingPercent, ResetAt: state.ResetAt,
	}
}

func (server *Server) sessionToken(c *gin.Context) string {
	cookie, err := c.Request.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func (server *Server) writeSessionCookie(c *gin.Context, token string, expiresAt int64) {
	forwardedProtocol := strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]))
	cookie := &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/usage", HttpOnly: true,
		Secure: server.secureCookies || forwardedProtocol == "https", SameSite: http.SameSiteLaxMode,
	}
	if expiresAt <= 0 {
		cookie.Expires = time.Unix(1, 0).UTC()
		cookie.MaxAge = -1
	} else {
		cookie.Expires = time.Unix(expiresAt, 0).UTC()
		cookie.MaxAge = max(int(cookie.Expires.Sub(server.now()).Seconds()), 1)
	}
	http.SetCookie(c.Writer, cookie)
}

func (server *Server) limitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maximumBodySize)
		c.Next()
	}
}

func (server *Server) noStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	}
}

func (server *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				server.logger.Error(
					"portal request panic", zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path), zap.String("panic_type", fmt.Sprintf("%T", recovered)),
					zap.Stack("stack"),
				)
				writeError(c, http.StatusInternalServerError, i18n.M("admin.internal_service_error"), "internal_error")
				c.Abort()
			}
		}()
		c.Next()
	}
}

func (server *Server) internalError(c *gin.Context, operation string, err error) {
	server.logger.Error(operation, zap.String("method", c.Request.Method), zap.String("path", c.Request.URL.Path), zap.Error(err))
	writeError(c, http.StatusInternalServerError, i18n.M("admin.internal_service_error"), "internal_error")
}

func writeError(c *gin.Context, status int, message any, code string) {
	httpi18n.Error(c, status, message, code)
}

func normalizeEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.Count(value, "@") != 1 {
		return ""
	}
	return value
}

func constantTimeEqual(left string, right string) bool {
	leftDigest := sha256.Sum256([]byte(left))
	rightDigest := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftDigest[:], rightDigest[:]) == 1
}

func recordHasAccount(records []controlplane.KeyRecord, account string) bool {
	for _, record := range records {
		if record.Account == account {
			return true
		}
	}
	return false
}

func accountByID(accounts []controlplane.Account, target string) (controlplane.Account, bool) {
	for _, account := range accounts {
		if account.ID == target {
			return account, true
		}
	}
	return controlplane.Account{}, false
}

func optionalAccount(account string) any {
	if account == "" {
		return nil
	}
	return account
}

func stringSetting(value any, fallback string) string {
	if typed, ok := value.(string); ok && strings.TrimSpace(typed) != "" {
		return strings.TrimSpace(typed)
	}
	return fallback
}
