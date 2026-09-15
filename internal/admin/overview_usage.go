package admin

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

var overviewUsageBuckets = map[int64]int64{
	int64(time.Hour / time.Second):           60,
	int64(6 * time.Hour / time.Second):       5 * 60,
	int64(24 * time.Hour / time.Second):      15 * 60,
	int64(7 * 24 * time.Hour / time.Second):  60 * 60,
	int64(30 * 24 * time.Hour / time.Second): 6 * 60 * 60,
}

const maxOverviewUsageBuckets = int64(360)

func (server *Server) readOverviewUsage(c *gin.Context) {
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	accounts, err := server.store.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read overview usage accounts", err)
		return
	}
	allAccountIDs := make([]string, 0, len(accounts))
	accountSet := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		allAccountIDs = append(allAccountIDs, account.ID)
		accountSet[account.ID] = struct{}{}
	}
	selectedAccounts := overviewFilterValues(c.QueryArray("account"), false)
	for _, account := range selectedAccounts {
		if _, found := accountSet[account]; !found {
			writeError(c, http.StatusNotFound, i18n.M("admin.cpa_account_does_not_exist"), "account_not_found")
			return
		}
	}
	trendAccounts := allAccountIDs
	if len(selectedAccounts) > 0 {
		trendAccounts = selectedAccounts
	}
	knownUsers, err := server.store.KnownUsers(c.Request.Context())
	if err != nil {
		server.internalError(c, "read overview usage users", err)
		return
	}
	knownUserSet := make(map[string]struct{}, len(knownUsers))
	for _, user := range knownUsers {
		knownUserSet[strings.ToLower(strings.TrimSpace(user))] = struct{}{}
	}
	selectedUsers := overviewFilterValues(c.QueryArray("user"), true)
	for _, user := range selectedUsers {
		if _, found := knownUserSet[user]; !found {
			writeError(c, http.StatusNotFound, i18n.M("admin.user_does_not_exist"), "user_not_found")
			return
		}
	}
	userLimit := 10
	if raw := strings.TrimSpace(c.Query("user_limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_trend_count"), "invalid_request")
			return
		}
		userLimit = min(max(parsed, 1), 500)
	}
	tokenMode := usage.TokenMode(strings.ToLower(strings.TrimSpace(c.DefaultQuery("token_mode", string(usage.TokenModeUnweighted)))))
	if tokenMode != usage.TokenModeUnweighted && tokenMode != usage.TokenModeWeighted {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_token_accounting_metric"), "invalid_request")
		return
	}
	now := server.now()
	location, windowTimezone, err := server.usageTimezone(c)
	if err != nil {
		return
	}
	window, startAt, endAt, bucketSeconds, windowSeconds, err := server.overviewUsageWindow(c, now, location)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	var startAtByAccount map[string]int64
	unavailableAccounts := []string{}
	if window == sinceResetWindow {
		startAtByAccount, unavailableAccounts, err = server.overviewUsageAccountStarts(
			c.Request.Context(), allAccountIDs, trendAccounts, now.Unix(),
		)
		if err != nil {
			server.internalError(c, "read overview quota periods", err)
			return
		}
		startAt = 0
		foundStart := false
		for _, account := range trendAccounts {
			if accountStart, found := startAtByAccount[account]; found && (!foundStart || accountStart < startAt) {
				startAt = accountStart
				foundStart = true
			}
		}
		if !foundStart {
			startAt = now.Unix() - quota.WeeklyWindowSeconds
		}
	}
	trend, err := server.usage.TokenTimeSeries(
		c.Request.Context(), trendAccounts, knownUsers, selectedUsers,
		startAt, endAt, bucketSeconds, userLimit, tokenMode, startAtByAccount,
	)
	if err != nil {
		server.internalError(c, "query overview token trend", err)
		return
	}
	collector, err := server.usage.Status(c.Request.Context())
	if err != nil {
		server.internalError(c, "read overview collector status", err)
		return
	}
	var selectedAccount any
	if len(selectedAccounts) == 1 {
		selectedAccount = selectedAccounts[0]
	}
	var selectedUser any
	if len(selectedUsers) == 1 {
		selectedUser = selectedUsers[0]
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": trend.GeneratedAt, "window": window,
		"window_seconds": windowSeconds, "window_start_at": startAt, "window_timezone": windowTimezone,
		"window_start_at_by_account": startAtByAccount, "unavailable_accounts": unavailableAccounts,
		"bucket_seconds": bucketSeconds, "buckets": trend.Buckets,
		"accounts": trend.Accounts, "users": trend.Users,
		"selected_account": selectedAccount, "selected_user": selectedUser,
		"selected_accounts": selectedAccounts, "selected_users": selectedUsers,
		"user_limit": userLimit, "collector": collector, "cached": false,
	})
}

func (server *Server) overviewUsageWindow(
	c *gin.Context,
	now time.Time,
	location *time.Location,
) (any, int64, int64, int64, any, error) {
	raw := strings.ToLower(strings.TrimSpace(c.Query("window")))
	if raw == "" {
		raw = strconv.FormatInt(defaultUsageWindow, 10)
	}
	generatedAt := now.Unix()
	if raw == customUsageWindow {
		startAt, startErr := strconv.ParseInt(strings.TrimSpace(c.Query("start_at")), 10, 64)
		endAt, endErr := strconv.ParseInt(strings.TrimSpace(c.Query("end_at")), 10, 64)
		if startErr != nil || endErr != nil || startAt < 0 || endAt <= startAt || endAt > generatedAt+60 {
			return nil, 0, 0, 0, nil, &usageWindowError{
				message: i18n.M("admin.invalid_custom_reporting_range"), code: "invalid_request", status: http.StatusBadRequest,
			}
		}
		duration := endAt - startAt
		return customUsageWindow, startAt, endAt, overviewUsageBucketSeconds(duration), duration, nil
	}
	if raw == todayUsageWindow {
		localNow := now.In(location)
		start := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).Unix()
		return todayUsageWindow, start, generatedAt + 1, 15 * 60, nil, nil
	}
	if raw == sinceResetWindow {
		return sinceResetWindow, generatedAt - quota.WeeklyWindowSeconds, generatedAt + 1,
			60 * 60, quota.WeeklyWindowSeconds, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, 0, 0, 0, nil, invalidOverviewUsageWindow()
	}
	bucketSeconds, found := overviewUsageBuckets[seconds]
	if !found {
		return nil, 0, 0, 0, nil, invalidOverviewUsageWindow()
	}
	return seconds, generatedAt - seconds, generatedAt + 1, bucketSeconds, seconds, nil
}

func (server *Server) overviewUsageAccountStarts(
	ctx context.Context,
	allAccounts []string,
	scopeAccounts []string,
	now int64,
) (map[string]int64, []string, error) {
	state, _, err := quota.ReadState(ctx, server.store)
	if err != nil {
		return nil, nil, err
	}
	known := make(map[string]struct{}, len(allAccounts))
	for _, account := range allAccounts {
		known[account] = struct{}{}
	}
	starts := make(map[string]int64)
	for _, account := range state.Snapshot.Accounts {
		if _, found := known[account.Account]; !found || account.Weekly == nil {
			continue
		}
		if startAt, found := weeklyWindowStart(*account.Weekly, now); found {
			starts[account.Account] = startAt
		}
	}
	unavailable := make([]string, 0)
	for _, account := range scopeAccounts {
		if _, found := starts[account]; !found {
			unavailable = append(unavailable, account)
		}
	}
	return starts, unavailable, nil
}

func weeklyWindowStart(window quota.WeeklyWindow, now int64) (int64, bool) {
	windowSeconds := window.WindowSeconds
	if windowSeconds <= 0 {
		windowSeconds = quota.WeeklyWindowSeconds
	}
	var startAt int64
	switch {
	case window.ResetAt != nil && *window.ResetAt > 0:
		startAt = *window.ResetAt - windowSeconds
	case window.ResetAfterSeconds != nil && *window.ResetAfterSeconds >= 0:
		startAt = now + *window.ResetAfterSeconds - windowSeconds
	default:
		return 0, false
	}
	if startAt < 0 || startAt > now {
		return 0, false
	}
	return startAt, true
}

func overviewUsageBucketSeconds(windowSeconds int64) int64 {
	windowSeconds = max(windowSeconds, 1)
	for _, bucket := range []int64{60, 5 * 60, 15 * 60, 60 * 60, 6 * 60 * 60, 24 * 60 * 60} {
		if int64(math.Ceil(float64(windowSeconds)/float64(bucket)))+1 <= maxOverviewUsageBuckets {
			return bucket
		}
	}
	return max(24*60*60, int64(math.Ceil(float64(windowSeconds)/float64(maxOverviewUsageBuckets-1))))
}

func invalidOverviewUsageWindow() *usageWindowError {
	return &usageWindowError{message: i18n.M("admin.invalid_trend_time_range"), code: "invalid_request", status: http.StatusBadRequest}
}

func overviewFilterValues(rawValues []string, lower bool) []string {
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, raw := range rawValues {
		for _, item := range strings.Split(raw, ",") {
			value := strings.TrimSpace(item)
			if lower {
				value = strings.ToLower(value)
			}
			if value == "" {
				continue
			}
			if _, found := seen[value]; found {
				continue
			}
			seen[value] = struct{}{}
			values = append(values, value)
		}
	}
	return values
}
