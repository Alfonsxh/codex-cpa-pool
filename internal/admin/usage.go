package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/sitetime"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

const (
	defaultUsageWindow = int64(24 * time.Hour / time.Second)
	customUsageWindow  = "custom"
	todayUsageWindow   = "today"
	weekUsageWindow    = "current_week"
	allUsageWindow     = "all"
	sinceResetWindow   = "since_reset"
)

var allowedUsageWindows = map[int64]struct{}{
	int64(time.Hour / time.Second):           {},
	int64(6 * time.Hour / time.Second):       {},
	int64(24 * time.Hour / time.Second):      {},
	int64(7 * 24 * time.Hour / time.Second):  {},
	int64(30 * 24 * time.Hour / time.Second): {},
}

type usageWindowContext struct {
	GeneratedAt    int64  `json:"generated_at"`
	Window         any    `json:"window"`
	WindowSeconds  *int64 `json:"window_seconds"`
	WindowStartAt  *int64 `json:"window_start_at"`
	WindowEndAt    *int64 `json:"window_end_at"`
	WindowTimezone string `json:"window_timezone,omitempty"`
	queryStartAt   int64
	queryEndAt     *int64
}

type userUsageResponse struct {
	usageWindowContext
	User       string  `json:"user"`
	Account    *string `json:"account"`
	Definition string  `json:"definition"`
	usage.UserBreakdown
}

type accountUsageResponse struct {
	usageWindowContext
	Account    string `json:"account"`
	Definition string `json:"definition"`
	usage.AccountBreakdown
}

func (server *Server) userUsageBreakdown(c *gin.Context) {
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	user := strings.ToLower(strings.TrimSpace(c.Query("email")))
	if user == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.user_is_required"), "invalid_request")
		return
	}
	found, err := server.store.UserExists(c.Request.Context(), user)
	if err != nil {
		server.internalError(c, "check user for usage", err)
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, i18n.M("admin.user_does_not_exist"), "user_not_found")
		return
	}
	account := strings.ToLower(strings.TrimSpace(c.Query("account")))
	if account != "" {
		found, err := server.accountExists(c, account)
		if err != nil {
			return
		}
		if !found {
			writeError(c, http.StatusNotFound, i18n.M("admin.cpa_account_does_not_exist"), "account_not_found")
			return
		}
	}
	window, err := server.parseUsageWindow(c, false)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	breakdown, err := server.usage.UserBreakdown(
		c.Request.Context(), user, account, window.queryStartAt, window.queryEndAt,
	)
	if err != nil {
		server.internalError(c, "query user usage breakdown", err)
		return
	}
	var selectedAccount *string
	if account != "" {
		selectedAccount = &account
	}
	httpi18n.JSON(c, http.StatusOK, userUsageResponse{
		usageWindowContext: window,
		User:               user,
		Account:            selectedAccount,
		Definition:         "successful_model_requests",
		UserBreakdown:      breakdown,
	})
}

func (server *Server) accountUsageBreakdown(c *gin.Context) {
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	account := strings.ToLower(strings.TrimSpace(c.Query("account")))
	if account == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.cpa_account_is_required"), "invalid_request")
		return
	}
	found, err := server.accountExists(c, account)
	if err != nil {
		return
	}
	if !found {
		writeError(c, http.StatusNotFound, i18n.M("admin.cpa_account_does_not_exist"), "account_not_found")
		return
	}
	window, err := server.parseUsageWindow(c, true)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	breakdown, err := server.usage.AccountBreakdown(
		c.Request.Context(), account, window.queryStartAt, window.queryEndAt,
	)
	if err != nil {
		server.internalError(c, "query account usage breakdown", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, accountUsageResponse{
		usageWindowContext: window,
		Account:            account,
		Definition:         "account_model_reasoning_effort_tokens",
		AccountBreakdown:   breakdown,
	})
}

func (server *Server) accountExists(c *gin.Context, accountID string) (bool, error) {
	if server.accounts == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.account_directory_service_is_not_ready"), "accounts_not_ready")
		return false, errors.New("account catalog is unavailable")
	}
	accounts, err := server.accounts.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read account catalog for usage", err)
		return false, err
	}
	for _, account := range accounts {
		if account.ID == accountID {
			return true, nil
		}
	}
	return false, nil
}

type usageWindowError struct {
	message *i18n.Message
	code    string
	status  int
}

func (value *usageWindowError) Error() string { return value.message.Error() }
func (value *usageWindowError) Unwrap() error { return value.message }

func (server *Server) parseUsageWindow(c *gin.Context, allowSinceReset bool) (usageWindowContext, error) {
	now := server.now()
	generatedAt := now.Unix()
	raw := strings.ToLower(strings.TrimSpace(c.Query("window")))
	if raw == "" {
		raw = strconv.FormatInt(defaultUsageWindow, 10)
	}
	if raw == sinceResetWindow {
		if !allowSinceReset {
			return usageWindowContext{}, invalidUsageWindow()
		}
		account := strings.ToLower(strings.TrimSpace(c.Query("account")))
		startAt, found, err := server.accountQuotaWindowStart(c, account, generatedAt)
		if err != nil {
			return usageWindowContext{}, err
		}
		if !found {
			return usageWindowContext{}, &usageWindowError{
				message: i18n.M("admin.this_cpa_has_no_quota_period_boundaries_refresh_its_quota"),
				code:    "usage_window_unavailable", status: http.StatusConflict,
			}
		}
		endAt := generatedAt
		return usageWindowContext{
			GeneratedAt: generatedAt, Window: sinceResetWindow,
			WindowStartAt: &startAt, WindowEndAt: &endAt,
			queryStartAt: startAt,
		}, nil
	}
	if raw == customUsageWindow {
		startAt, startError := strconv.ParseInt(strings.TrimSpace(c.Query("start_at")), 10, 64)
		endAt, endError := strconv.ParseInt(strings.TrimSpace(c.Query("end_at")), 10, 64)
		if startError != nil || endError != nil || startAt < 0 || endAt <= startAt || endAt > generatedAt+60 {
			return usageWindowContext{}, &usageWindowError{
				message: i18n.M("admin.invalid_custom_reporting_range"), code: "invalid_request", status: http.StatusBadRequest,
			}
		}
		seconds := endAt - startAt
		return usageWindowContext{
			GeneratedAt: generatedAt, Window: customUsageWindow, WindowSeconds: &seconds,
			WindowStartAt: &startAt, WindowEndAt: &endAt,
			queryStartAt: startAt, queryEndAt: &endAt,
		}, nil
	}
	if raw == allUsageWindow {
		startAt := int64(0)
		endAt := generatedAt
		return usageWindowContext{
			GeneratedAt: generatedAt, Window: allUsageWindow, WindowStartAt: nil,
			WindowEndAt: &endAt, queryStartAt: startAt,
		}, nil
	}
	if raw == todayUsageWindow || raw == weekUsageWindow {
		location, name, err := server.usageTimezone(c)
		if err != nil {
			return usageWindowContext{}, err
		}
		localNow := now.In(location)
		start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location)
		end := now
		if raw == weekUsageWindow {
			daysSinceMonday := (int(localNow.Weekday()) + 6) % 7
			start = start.AddDate(0, 0, -daysSinceMonday)
			end = start.AddDate(0, 0, 7)
		}
		startAt, endAt := start.Unix(), end.Unix()
		return usageWindowContext{
			GeneratedAt: generatedAt, Window: raw, WindowStartAt: &startAt,
			WindowEndAt: &endAt, WindowTimezone: name, queryStartAt: startAt,
		}, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return usageWindowContext{}, invalidUsageWindow()
	}
	if _, found := allowedUsageWindows[seconds]; !found {
		return usageWindowContext{}, invalidUsageWindow()
	}
	startAt := generatedAt - seconds
	endAt := generatedAt
	return usageWindowContext{
		GeneratedAt: generatedAt, Window: seconds, WindowSeconds: &seconds,
		WindowStartAt: &startAt, WindowEndAt: &endAt, queryStartAt: startAt,
	}, nil
}

func (server *Server) accountQuotaWindowStart(
	c *gin.Context,
	account string,
	now int64,
) (int64, bool, error) {
	state, _, err := quota.ReadState(c.Request.Context(), server.store)
	if err != nil {
		server.internalError(c, "read account quota period", err)
		return 0, false, err
	}
	for _, candidate := range state.Snapshot.Accounts {
		if candidate.Account != account || candidate.Weekly == nil {
			continue
		}
		startAt, found := weeklyWindowStart(*candidate.Weekly, now)
		return startAt, found, nil
	}
	return 0, false, nil
}

func (server *Server) usageTimezone(c *gin.Context) (*time.Location, string, error) {
	settings, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read usage timezone", err)
		return nil, "", err
	}
	name, err := sitetime.Name(settings)
	if err != nil {
		server.internalError(c, "read usage timezone", err)
		return nil, "", err
	}
	location, err := sitetime.Validate(name)
	if err != nil {
		server.internalError(c, "load usage timezone", err)
		return nil, "", err
	}
	return location, name, nil
}

func invalidUsageWindow() *usageWindowError {
	return &usageWindowError{
		message: i18n.M("admin.invalid_reporting_range"), code: "invalid_request", status: http.StatusBadRequest,
	}
}

func writeUsageWindowError(c *gin.Context, err error) {
	var windowError *usageWindowError
	if errors.As(err, &windowError) {
		writeError(c, windowError.status, windowError.message, windowError.code)
	}
}
