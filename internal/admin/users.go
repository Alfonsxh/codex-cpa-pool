package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/identity"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

type createUserPayload struct {
	Email  string  `json:"email" binding:"required,max=254"`
	TeamID *string `json:"team_id"`
}

type userActionPayload struct {
	Email      string `json:"email" binding:"required,max=254"`
	Confirm    string `json:"confirm" binding:"max=254"`
	RevokeKeys bool   `json:"revoke_keys"`
}

type userKeyActionPayload struct {
	Email   string `json:"email" binding:"max=254"`
	Label   string `json:"label" binding:"max=320"`
	Confirm string `json:"confirm" binding:"max=32"`
}

type userQuotaPayload struct {
	Email        string `json:"email" binding:"required,max=254"`
	Mode         string `json:"mode" binding:"required,max=16"`
	WeeklyTokens *int64 `json:"weekly_tokens"`
}

type userManagementSummary struct {
	controlplane.UserSummary
	AccountCount int                   `json:"account_count"`
	Usage        usage.WeightedMetrics `json:"usage"`
	WeeklyQuota  UserWeeklyQuota       `json:"weekly_quota"`
}

type userKeyPreview struct {
	Label        string `json:"label"`
	Account      string `json:"account"`
	AccountEmail string `json:"account_email"`
	User         string `json:"user"`
	Status       string `json:"status"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	Preview      string `json:"preview"`
}

type oneTimeUserKey struct {
	userKeyPreview
	Key string `json:"key"`
}

type userAccountDetail struct {
	Account      string                `json:"account"`
	AccountEmail string                `json:"account_email"`
	Status       string                `json:"status"`
	HistoryCount int                   `json:"history_count"`
	Key          *userKeyPreview       `json:"key"`
	Usage        usage.WeightedMetrics `json:"usage"`
}

type userManagementDetail struct {
	userManagementSummary
	Accounts []userAccountDetail `json:"accounts"`
}

func (server *Server) listUsers(c *gin.Context) {
	view := strings.ToLower(strings.TrimSpace(c.Query("view")))
	if view == "" {
		view = "summary"
	}
	if view != "summary" && view != "members" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_directory_view"), "invalid_request")
		return
	}
	page, err := positiveQueryInteger(c, "page", 1)
	if err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_pagination_parameters"), "invalid_request")
		return
	}
	pageSize, err := positiveQueryInteger(c, "page_size", 50)
	if err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_pagination_parameters"), "invalid_request")
		return
	}
	if pageSize != 25 && pageSize != 50 && pageSize != 100 {
		writeError(c, http.StatusBadRequest, i18n.M("admin.page_size_must_be_25_50_or_100"), "invalid_page_size")
		return
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("q")))
	if utf8.RuneCountInString(search) > 200 {
		writeError(c, http.StatusBadRequest, i18n.M("admin.user_search_text_is_too_long"), "invalid_request")
		return
	}
	teamID := strings.TrimSpace(c.Query("team_id"))
	usageState := strings.ToLower(strings.TrimSpace(c.Query("usage_state")))
	if usageState == "" {
		usageState = "all"
	}
	if usageState != "all" && usageState != "used" && usageState != "unused" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_token_activity_filter"), "invalid_usage_state")
		return
	}
	sortField := strings.ToLower(strings.TrimSpace(c.Query("sort")))
	if sortField == "" {
		sortField = "tokens"
	}
	if sortField != "email" && sortField != "requests" && sortField != "tokens" &&
		sortField != "quota" && sortField != "last_used" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_sort_field"), "invalid_sort_field")
		return
	}
	direction := strings.ToLower(strings.TrimSpace(c.Query("direction")))
	if direction == "" {
		direction = "desc"
	}
	if direction != "asc" && direction != "desc" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_sort_direction"), "invalid_sort_direction")
		return
	}
	window, err := server.parseUsageWindow(c, false)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	usageReader, ok := server.usage.(UserUsageSummaryReader)
	if !ok || server.users == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.user_usage_directory_service_is_not_ready"), "usage_not_ready")
		return
	}
	baseUsers, err := server.store.ListUserSummaries(c.Request.Context())
	if err != nil {
		server.internalError(c, "list user summaries", err)
		return
	}
	filteredUsers := make([]controlplane.UserSummary, 0, len(baseUsers))
	for _, user := range baseUsers {
		if teamID == "unassigned" && user.TeamID != nil {
			continue
		}
		if teamID != "" && teamID != "unassigned" && (user.TeamID == nil || *user.TeamID != teamID) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(user.Email), search) &&
			(user.Team == nil || !strings.Contains(strings.ToLower(user.Team.Name), search)) {
			continue
		}
		filteredUsers = append(filteredUsers, user)
	}
	userEmails := make([]string, 0, len(filteredUsers))
	for _, user := range filteredUsers {
		userEmails = append(userEmails, user.Email)
	}
	var usageSummaries map[string]usage.WeightedMetrics
	if view == "members" {
		usageSummaries, err = usageReader.UserSummariesForUsers(
			c.Request.Context(), userEmails, window.queryStartAt, window.queryEndAt,
		)
	} else {
		usageSummaries, err = usageReader.UserSummaries(
			c.Request.Context(), window.queryStartAt, window.queryEndAt,
		)
	}
	if err != nil {
		server.internalError(c, "read user usage summaries", err)
		return
	}
	quotas := map[string]UserWeeklyQuota{}
	accounts := []controlplane.Account{}
	teams := []controlplane.Team{}
	collector := usage.CollectorStatus{}
	if view == "summary" {
		quotas, err = server.users.ReadUserQuotas(c.Request.Context(), userEmails)
		if err != nil {
			server.writeUserLifecycleError(c, "read user quota catalog", err)
			return
		}
		accounts, err = server.store.ReadAccounts(c.Request.Context())
		if err != nil {
			server.internalError(c, "read user catalog accounts", err)
			return
		}
		teams, err = server.store.ListTeams(c.Request.Context())
		if err != nil {
			server.internalError(c, "read user catalog teams", err)
			return
		}
		collector, err = server.usage.Status(c.Request.Context())
		if err != nil {
			server.internalError(c, "read user collector status", err)
			return
		}
	}
	items := make([]userManagementSummary, 0, len(filteredUsers))
	for _, user := range filteredUsers {
		metrics := usageSummaries[user.Email]
		item := userManagementSummary{
			UserSummary: user, AccountCount: len(accounts), Usage: metrics,
			WeeklyQuota: quotas[user.Email],
		}
		used := item.Usage.TotalTokens > 0
		if usageState == "used" && !used || usageState == "unused" && used {
			continue
		}
		items = append(items, item)
	}
	sortUserManagementSummaries(items, sortField, direction)
	total := len(items)
	totalPages := max(1, (total+pageSize-1)/pageSize)
	page = min(page, totalPages)
	start := (page - 1) * pageSize
	end := min(start+pageSize, total)
	pageItems := items[start:end]
	accountCatalog := make(map[string]gin.H, len(accounts))
	for _, account := range accounts {
		accountCatalog[account.ID] = gin.H{"email": account.Email}
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at":         window.GeneratedAt,
		"window":               window.Window,
		"window_seconds":       window.WindowSeconds,
		"window_start_at":      window.WindowStartAt,
		"window_end_at":        window.WindowEndAt,
		"window_timezone":      window.WindowTimezone,
		"summary_generated_at": window.GeneratedAt,
		"summary_cached":       false,
		"users":                pageItems,
		"accounts":             accountCatalog,
		"teams":                teams,
		"tags":                 []any{},
		"collector":            collector,
		"pagination": gin.H{
			"page":        page,
			"page_size":   pageSize,
			"total":       total,
			"total_pages": totalPages,
		},
	})
}

func sortUserManagementSummaries(items []userManagementSummary, field string, direction string) {
	sort.SliceStable(items, func(leftIndex, rightIndex int) bool {
		left := items[leftIndex]
		right := items[rightIndex]
		if field == "last_used" {
			leftAvailable := left.Usage.LastUsedAt > 0
			rightAvailable := right.Usage.LastUsedAt > 0
			if leftAvailable != rightAvailable {
				return leftAvailable
			}
		}
		comparison := 0
		switch field {
		case "email":
			comparison = strings.Compare(strings.ToLower(left.Email), strings.ToLower(right.Email))
		case "requests":
			comparison = compareInt64(left.Usage.RequestCount, right.Usage.RequestCount)
		case "quota":
			comparison = compareInt64(left.WeeklyQuota.UsedTokens, right.WeeklyQuota.UsedTokens)
		case "last_used":
			comparison = compareInt64(left.Usage.LastUsedAt, right.Usage.LastUsedAt)
		default:
			comparison = compareInt64(left.Usage.WeightedTokens, right.Usage.WeightedTokens)
		}
		if comparison == 0 {
			return strings.ToLower(left.Email) < strings.ToLower(right.Email)
		}
		if direction == "desc" {
			return comparison > 0
		}
		return comparison < 0
	})
}

func compareInt64(left, right int64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func (server *Server) userDetail(c *gin.Context) {
	usageReader, ok := server.usage.(UserUsageSummaryReader)
	if !ok || server.users == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.user_detail_service_is_not_ready"), "usage_not_ready")
		return
	}
	email := strings.ToLower(strings.TrimSpace(c.Query("email")))
	if email == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.specify_the_user_s_email"), "invalid_request")
		return
	}
	window, err := server.parseUsageWindow(c, false)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	users, err := server.store.ListUserSummaries(c.Request.Context())
	if err != nil {
		server.internalError(c, "list user detail summaries", err)
		return
	}
	var summary *controlplane.UserSummary
	for index := range users {
		if users[index].Email == email {
			summary = &users[index]
			break
		}
	}
	if summary == nil {
		writeError(c, http.StatusNotFound, i18n.M("admin.user_does_not_exist"), "user_not_found")
		return
	}
	accountUsage, err := usageReader.UserAccounts(
		c.Request.Context(), email, window.queryStartAt, window.queryEndAt,
	)
	if err != nil {
		server.internalError(c, "read user account usage", err)
		return
	}
	quota, err := server.users.ReadUserQuota(c.Request.Context(), email)
	if err != nil {
		server.writeUserLifecycleError(c, "read user detail quota", err)
		return
	}
	accounts, err := server.store.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read user detail accounts", err)
		return
	}
	records, err := server.store.ReadKeyRecordsForUsers(c.Request.Context(), []string{email})
	if err != nil {
		server.internalError(c, "read user detail key records", err)
		return
	}
	usageByAccount := make(map[string]usage.WeightedMetrics, len(accountUsage.Accounts))
	for _, item := range accountUsage.Accounts {
		usageByAccount[item.Account] = item.WeightedMetrics
	}
	details := make([]userAccountDetail, 0, len(accounts))
	for _, account := range accounts {
		matching := make([]controlplane.KeyRecord, 0)
		for _, record := range records {
			if record.Account == account.ID {
				matching = append(matching, record)
			}
		}
		sort.SliceStable(matching, func(left, right int) bool {
			return matching[left].CreatedAt < matching[right].CreatedAt
		})
		status := "missing"
		var current *controlplane.KeyRecord
		for index := range matching {
			if matching[index].Status == "active" {
				current = &matching[index]
				status = "active"
			}
		}
		if current == nil && len(matching) > 0 {
			current = &matching[len(matching)-1]
			status = current.Status
		}
		var key *userKeyPreview
		if current != nil {
			preview := maskedKeyPreview(current.Key)
			key = &userKeyPreview{
				Label: current.Label, Account: current.Account, AccountEmail: current.AccountEmail,
				User: current.User, Status: current.Status, CreatedAt: current.CreatedAt,
				UpdatedAt: current.UpdatedAt, Preview: preview,
			}
		}
		details = append(details, userAccountDetail{
			Account: account.ID, AccountEmail: account.Email, Status: status,
			HistoryCount: len(matching), Key: key, Usage: usageByAccount[account.ID],
		})
	}
	response := userManagementDetail{
		userManagementSummary: userManagementSummary{
			UserSummary: *summary, AccountCount: len(accounts), Usage: accountUsage.Totals,
			WeeklyQuota: quota.WeeklyQuota,
		},
		Accounts: details,
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at":    window.GeneratedAt,
		"window":          window.Window,
		"window_seconds":  window.WindowSeconds,
		"window_start_at": window.WindowStartAt,
		"window_end_at":   window.WindowEndAt,
		"window_timezone": window.WindowTimezone,
		"user":            response,
	})
}

func maskedKeyPreview(key string) string {
	if key == "" {
		return ""
	}
	prefix := "cpa"
	if separator := strings.LastIndex(key, "_"); separator > 0 {
		prefix = key[:separator]
	}
	suffix := key
	if len(suffix) > 4 {
		suffix = suffix[len(suffix)-4:]
	}
	return prefix + "_••••" + suffix
}

func (server *Server) createUser(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body createUserPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_parameters"), "invalid_request")
		return
	}
	result, err := server.users.CreateUser(c.Request.Context(), body.Email, body.TeamID)
	if err != nil {
		server.writeUserLifecycleError(c, "create user", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpi18n.JSON(c, http.StatusCreated, gin.H{
		"message":          i18n.M("admin.user_created_the_api_key_is_shown_only_once_usage"),
		"keys":             []oneTimeUserKey{server.oneTimeKey(c, result.User, result.APIKey, "")},
		"initial_password": result.InitialPassword,
		"team_id":          result.TeamID,
	})
}

func (server *Server) rotateUserKey(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userKeyActionPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_api_key_rotation_parameters"), "invalid_request")
		return
	}
	if body.Confirm != "" && body.Confirm != "rotate" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_api_key_rotation"), "invalid_request")
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	label := strings.TrimSpace(body.Label)
	if email == "" && label != "" {
		email = strings.ToLower(strings.TrimSpace(strings.SplitN(label, ":", 2)[0]))
	}
	if email == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.specify_the_user_key_to_rotate"), "invalid_request")
		return
	}
	result, err := server.users.RotateUserKey(c.Request.Context(), email)
	if err != nil {
		server.writeUserLifecycleError(c, "rotate user API key", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("admin.unified_user_key_rotated_the_new_key_is_shown_only"),
		"keys":    []oneTimeUserKey{server.oneTimeKey(c, email, result.APIKey, label)},
	})
}

func (server *Server) revokeUser(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userActionPayload
	if err := c.ShouldBindJSON(&body); err != nil || (body.Confirm != "" && body.Confirm != "revoke") {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_disabling_the_user_s_api_key"), "invalid_request")
		return
	}
	result, err := server.users.RevokeUser(c.Request.Context(), body.Email)
	if err != nil {
		server.writeUserLifecycleError(c, "revoke user", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"message": i18n.M("admin.unified_user_key_disabled"), "revoked": result.RevokedKeys})
}

func (server *Server) resetUserPassword(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userActionPayload
	if err := c.ShouldBindJSON(&body); err != nil || (body.Confirm != "" && body.Confirm != "reset") {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_resetting_the_usage_center_password"), "invalid_request")
		return
	}
	result, err := server.users.ResetUserPassword(c.Request.Context(), body.Email)
	if err != nil {
		server.writeUserLifecycleError(c, "reset user password", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":                  i18n.M("admin.user_password_reset_to_the_initial_password_existing_sessions_ended"),
		"user":                     result.User,
		"password_change_required": result.PasswordChangeRequired,
		"initial_password":         result.InitialPassword,
	})
}

func (server *Server) oneTimeKey(c *gin.Context, email string, key string, preferredLabel string) oneTimeUserKey {
	records, err := server.store.ReadKeyRecordsForUsers(c.Request.Context(), []string{email})
	if err == nil {
		for index := len(records) - 1; index >= 0; index-- {
			record := records[index]
			if record.Status != "active" || record.Key != key {
				continue
			}
			return oneTimeUserKey{
				userKeyPreview: userKeyPreview{
					Label: record.Label, Account: record.Account, AccountEmail: record.AccountEmail,
					User: record.User, Status: record.Status, CreatedAt: record.CreatedAt,
					UpdatedAt: record.UpdatedAt, Preview: maskedKeyPreview(record.Key),
				},
				Key: key,
			}
		}
	}
	label := strings.TrimSpace(preferredLabel)
	if label == "" {
		label = email
	}
	account := ""
	if separator := strings.LastIndex(label, ":"); separator >= 0 && separator+1 < len(label) {
		account = label[separator+1:]
	}
	return oneTimeUserKey{
		userKeyPreview: userKeyPreview{
			Label: label, Account: account, User: email, Status: "active", Preview: maskedKeyPreview(key),
		},
		Key: key,
	}
}

func (server *Server) deleteUser(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userActionPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != body.Email {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirmation_must_exactly_match_the_user_s_email"), "invalid_request")
		return
	}
	result, err := server.users.DeleteUser(c.Request.Context(), body.Email, body.RevokeKeys)
	if err != nil {
		server.writeUserLifecycleError(c, "delete user", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("admin.user_routes_login_credentials_and_current_quota_policy_deleted_historical"),
		"user":    result,
	})
}

func (server *Server) readUserQuota(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.specify_the_user_s_email"), "invalid_request")
		return
	}
	result, err := server.users.ReadUserQuota(c.Request.Context(), email)
	if err != nil {
		server.writeUserLifecycleError(c, "read user quota", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, result)
}

func (server *Server) updateUserQuota(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userQuotaPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_weekly_quota_parameters"), "invalid_request")
		return
	}
	body.Mode = strings.ToLower(strings.TrimSpace(body.Mode))
	if !validUserQuotaPolicy(body.Mode, body.WeeklyTokens) {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_quota_mode_or_custom_weekly_quota"), "invalid_request")
		return
	}
	result, err := server.users.UpdateUserQuota(
		c.Request.Context(), body.Email, body.Mode, body.WeeklyTokens,
	)
	if err != nil {
		server.writeUserLifecycleError(c, "update user quota", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":      i18n.M("admin.user_weekly_quota_policy_saved_the_quota_collector_will_publish"),
		"user":         result.User,
		"weekly_quota": result.WeeklyQuota,
		"adjustments":  result.Adjustments,
	})
}

func (server *Server) clearUserQuota(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.specify_the_user_s_email"), "invalid_request")
		return
	}
	result, err := server.users.ClearUserQuota(c.Request.Context(), email)
	if err != nil {
		server.writeUserLifecycleError(c, "clear user quota", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":      i18n.M("admin.organization_weekly_quota_default_restored_the_quota_collector_will_publish"),
		"user":         result.User,
		"weekly_quota": result.WeeklyQuota,
		"adjustments":  result.Adjustments,
	})
}

func validUserQuotaPolicy(mode string, weeklyTokens *int64) bool {
	switch mode {
	case "inherit", "unlimited":
		return weeklyTokens == nil
	case "custom":
		return weeklyTokens != nil && *weeklyTokens > 0 && *weeklyTokens <= 1_000_000_000_000
	default:
		return false
	}
}

func (server *Server) requireUserLifecycle(c *gin.Context) bool {
	if server.users != nil {
		return true
	}
	writeError(c, http.StatusServiceUnavailable, i18n.M("admin.user_lifecycle_service_is_not_ready"), "user_lifecycle_unavailable")
	return false
}

func (server *Server) writeUserLifecycleError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidUser):
		writeError(c, http.StatusBadRequest, i18n.M("admin.the_user_s_email_domain_is_not_allowed"), "invalid_user")
	case errors.Is(err, ErrInitialPasswordMissing):
		writeError(c, http.StatusConflict, i18n.M("admin.configure_the_initial_usage_center_password_first"), "initial_password_unavailable")
	case errors.Is(err, controlplane.ErrUserAlreadyActive):
		writeError(c, http.StatusConflict, i18n.M("admin.the_user_already_has_an_active_api_key"), "user_exists")
	case errors.Is(err, controlplane.ErrUserLifecycleNotFound):
		writeError(c, http.StatusNotFound, i18n.M("admin.user_does_not_exist_or_has_no_active_api_key"), "user_not_found")
	case errors.Is(err, controlplane.ErrTeamNotFound):
		writeError(c, http.StatusNotFound, i18n.M("admin.team_does_not_exist"), "team_not_found")
	case errors.Is(err, controlplane.ErrUserDeleteRequiresRevoke):
		writeError(c, http.StatusConflict, i18n.M("admin.the_user_still_has_active_api_keys_confirm_disabling_them"), "active_keys_present")
	case errors.Is(err, controlplane.ErrUserLifecycleConflict), errors.Is(err, identity.ErrRotationConflict):
		writeError(c, http.StatusConflict, i18n.M("admin.user_state_changed_refresh_and_try_again"), "user_lifecycle_conflict")
	case errors.Is(err, identity.ErrRotationUnsafe), errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, i18n.M("admin.user_or_api_key_state_is_unsafe_the_operation_was"), "invalid_request")
	case isQuotaActionInputError(err):
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_quota_action_parameters_or_target_state"), "invalid_request")
	default:
		server.internalError(c, operation, err)
	}
}

func positiveQueryInteger(c *gin.Context, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errors.New("expected a positive integer")
	}
	return value, nil
}

type optionalNullableString struct {
	Set   bool
	Value *string
}

func (value *optionalNullableString) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		value.Value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("expected a string or null")
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		value.Value = nil
	} else {
		value.Value = &decoded
	}
	return nil
}

type userTeamPayload struct {
	Email          string                 `json:"email"`
	Users          []string               `json:"users"`
	TeamID         *string                `json:"team_id"`
	ExpectedTeamID optionalNullableString `json:"expected_team_id"`
}

func (server *Server) updateUserTeam(c *gin.Context) {
	var body userTeamPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_team_parameters"), "invalid_request")
		return
	}
	server.applyUserTeamUpdate(c, []string{body.Email}, body)
}

func (server *Server) updateUserTeams(c *gin.Context) {
	var body userTeamPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_user_team_parameters"), "invalid_request")
		return
	}
	server.applyUserTeamUpdate(c, body.Users, body)
}

func (server *Server) applyUserTeamUpdate(c *gin.Context, requested []string, body userTeamPayload) {
	users := normalizeUserEmails(requested)
	if len(users) == 0 || len(users) > 500 {
		writeError(c, http.StatusBadRequest, i18n.M("admin.select_between_1_and_500_users"), "invalid_request")
		return
	}
	knownUsers, err := server.store.KnownUsers(c.Request.Context())
	if err != nil {
		server.internalError(c, "list known users for team assignment", err)
		return
	}
	known := make(map[string]struct{}, len(knownUsers))
	for _, user := range knownUsers {
		known[strings.ToLower(strings.TrimSpace(user))] = struct{}{}
	}
	missing := make([]string, 0)
	for _, user := range users {
		if _, found := known[user]; !found {
			missing = append(missing, user)
		}
	}
	if len(missing) > 0 {
		message := httpi18n.Text(c, "admin.user_does_not_exist_2") + strings.Join(missing[:min(len(missing), 3)], "、")
		writeError(c, http.StatusNotFound, message, "user_not_found")
		return
	}
	if server.teamIdentities == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_identity_synchronization_service_is_not_ready"), "usage_not_ready")
		return
	}
	teamID := normalizeOptionalString(body.TeamID)
	expectation := controlplane.TeamExpectation{
		Provided: body.ExpectedTeamID.Set,
		TeamID:   normalizeOptionalString(body.ExpectedTeamID.Value),
	}
	assignments, err := server.store.SetUserTeamsExpected(
		c.Request.Context(),
		users,
		teamID,
		expectation,
	)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	classifications, err := server.store.ReadUserTeams(c.Request.Context(), users)
	if err != nil {
		server.internalError(c, "read updated user team assignments", err)
		return
	}
	identities := make(map[string]usage.TeamIdentity, len(classifications))
	for user, classification := range classifications {
		identity := usage.TeamIdentity{MembershipVersion: classification.TeamMembershipVersion}
		if classification.TeamID != nil {
			identity.TeamID = *classification.TeamID
		}
		identities[user] = identity
	}
	if _, err := server.teamIdentities.SyncUserTeams(c.Request.Context(), identities); err != nil {
		server.internalError(c, "synchronize updated user team identities", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":         i18n.M("admin.updated_team_membership_for_users_team_usage_is_calculated_using", i18n.Params{"Count": len(users)}),
		"assignments":     assignments,
		"classifications": classifications,
	})
}

func normalizeUserEmails(values []string) []string {
	unique := make(map[string]struct{})
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}
