package admin

import (
	"context"
	"errors"
	"fmt"
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

type UserQuotaActionRequest struct {
	Action      string
	Scope       string
	Users       []string
	TokenAmount int64
	Reason      string
}

type UserQuotaOperationSummary struct {
	TotalUsers              int    `json:"total_users"`
	UsersWithUsage          int    `json:"users_with_usage"`
	TotalUsedTokens         int64  `json:"total_used_tokens"`
	TotalRawUsedTokens      int64  `json:"total_raw_used_tokens"`
	UsersWithPersonalPolicy int    `json:"users_with_personal_policy"`
	UsersWithBonus          int    `json:"users_with_bonus"`
	UsersWithUsageReset     int    `json:"users_with_usage_reset"`
	WeekStartAt             *int64 `json:"week_start_at"`
	WeekEndAt               *int64 `json:"week_end_at"`
}

type UserQuotaActionResponse struct {
	usage.QuotaActionResult
	Message         string                    `json:"message"`
	QuotaOperations UserQuotaOperationSummary `json:"quota_operations"`
}

type quotaActionPayload struct {
	Action      string   `json:"action"`
	Scope       string   `json:"scope"`
	Users       []string `json:"users"`
	TokenAmount any      `json:"token_amount"`
	Reason      string   `json:"reason"`
	Confirm     string   `json:"confirm"`
}

func (server *Server) readUserQuotaOperations(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	result, err := server.users.ReadQuotaOperations(c.Request.Context())
	if err != nil {
		server.writeUserLifecycleError(c, "read user quota operation summary", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, result)
}

func (server *Server) applyUserQuotaAction(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body quotaActionPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_quota_action_parameters"), "invalid_request")
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	if scope == "" {
		scope = "selected"
	}
	if !validQuotaActionConfirmation(action, scope, body.Confirm) ||
		(scope == "all" && action != "reset_usage") ||
		(scope != "all" && scope != "selected") {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_the_quota_action"), "invalid_request")
		return
	}
	tokenAmount := int64(0)
	if action == "add_bonus" {
		var err error
		tokenAmount, err = parseQuotaActionTokens(body.TokenAmount)
		if err != nil {
			writeError(c, http.StatusBadRequest, i18n.M("admin.the_bonus_must_be_a_positive_integer"), "invalid_request")
			return
		}
	}
	result, err := server.users.ApplyUserQuotaAction(c.Request.Context(), UserQuotaActionRequest{
		Action: action, Scope: scope, Users: body.Users,
		TokenAmount: tokenAmount, Reason: body.Reason,
	})
	if err != nil {
		server.writeUserLifecycleError(c, "apply user quota action", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, result)
}

func validQuotaActionConfirmation(action string, scope string, confirmation string) bool {
	expected := ""
	switch action {
	case "restore_default":
		expected = "restore_default"
	case "add_bonus":
		expected = "add_bonus"
	case "reset_usage":
		if scope == "all" {
			expected = "reset_all_current_week_usage"
		} else {
			expected = "reset_current_week_usage"
		}
	}
	return expected != "" && confirmation == expected
}

func parseQuotaActionTokens(value any) (int64, error) {
	var tokens int64
	switch typed := value.(type) {
	case float64:
		tokens = int64(typed)
		if float64(tokens) != typed {
			return 0, usage.ErrInvalidQuotaAction
		}
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" {
			return 0, usage.ErrInvalidQuotaAction
		}
		for _, character := range raw {
			if character < '0' || character > '9' {
				return 0, usage.ErrInvalidQuotaAction
			}
		}
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, usage.ErrInvalidQuotaAction
		}
		tokens = parsed
	default:
		return 0, usage.ErrInvalidQuotaAction
	}
	if tokens <= 0 || tokens > 1_000_000_000_000 {
		return 0, usage.ErrInvalidQuotaAction
	}
	return tokens, nil
}

func (manager *UserManager) ApplyUserQuotaAction(
	ctx context.Context,
	request UserQuotaActionRequest,
) (UserQuotaActionResponse, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	settings, err := manager.store.ReadSettings(ctx)
	if err != nil {
		return UserQuotaActionResponse{}, fmt.Errorf("read quota action settings: %w", err)
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.Scope = strings.ToLower(strings.TrimSpace(request.Scope))
	if request.Scope == "" {
		request.Scope = "selected"
	}
	if request.Action != "restore_default" && request.Action != "add_bonus" && request.Action != "reset_usage" {
		return UserQuotaActionResponse{}, usage.ErrInvalidQuotaAction
	}
	if request.Scope != "selected" && request.Scope != "all" {
		return UserQuotaActionResponse{}, usage.ErrInvalidQuotaAction
	}
	if request.Scope == "all" && request.Action != "reset_usage" {
		return UserQuotaActionResponse{}, usage.ErrInvalidQuotaAction
	}
	if request.Action == "add_bonus" && (request.TokenAmount <= 0 || request.TokenAmount > 1_000_000_000_000) {
		return UserQuotaActionResponse{}, usage.ErrInvalidQuotaAction
	}
	if request.Action != "restore_default" {
		request.Reason = strings.Join(strings.Fields(request.Reason), " ")
		if request.Reason == "" || utf8.RuneCountInString(request.Reason) > 200 {
			return UserQuotaActionResponse{}, usage.ErrInvalidQuotaAction
		}
	}
	knownUsers, err := manager.store.KnownUsers(ctx)
	if err != nil {
		return UserQuotaActionResponse{}, fmt.Errorf("list quota action users: %w", err)
	}
	known := make(map[string]struct{}, len(knownUsers))
	for _, user := range knownUsers {
		known[strings.ToLower(strings.TrimSpace(user))] = struct{}{}
	}
	knownUsers = knownUsers[:0]
	for user := range known {
		knownUsers = append(knownUsers, user)
	}
	sort.Strings(knownUsers)
	users := knownUsers
	if request.Scope == "selected" {
		selected := make(map[string]struct{}, len(request.Users))
		for _, rawUser := range request.Users {
			if strings.TrimSpace(rawUser) == "" {
				continue
			}
			user, normalizeError := identity.NormalizeUser(settings, rawUser)
			if normalizeError != nil {
				return UserQuotaActionResponse{}, normalizeError
			}
			selected[user] = struct{}{}
		}
		if len(selected) == 0 || len(selected) > 500 {
			return UserQuotaActionResponse{}, usage.ErrInvalidQuotaAction
		}
		users = make([]string, 0, len(selected))
		for user := range selected {
			if _, found := known[user]; !found {
				return UserQuotaActionResponse{}, controlplane.ErrUserLifecycleNotFound
			}
			users = append(users, user)
		}
		sort.Strings(users)
	}
	if len(users) == 0 {
		return UserQuotaActionResponse{}, usage.ErrInvalidQuotaAction
	}
	defaultLimit, _, err := userQuotaConfiguration(settings)
	if err != nil {
		return UserQuotaActionResponse{}, err
	}
	result, err := manager.credentials.ApplyQuotaAction(ctx, usage.QuotaActionRequest{
		Action: request.Action, Users: users, TokenAmount: request.TokenAmount,
		Reason: request.Reason, CreatedBy: "admin", DefaultLimit: defaultLimit,
	})
	if err != nil {
		return UserQuotaActionResponse{}, err
	}
	quotas, err := manager.credentials.WeeklyQuotas(ctx, knownUsers, defaultLimit)
	if err != nil {
		return UserQuotaActionResponse{}, fmt.Errorf("read quota operation summary: %w", err)
	}
	return UserQuotaActionResponse{
		QuotaActionResult: result,
		Message:           quotaActionMessage(request.Action, len(users), result, i18n.FromContext(ctx)),
		QuotaOperations:   summarizeQuotaOperations(knownUsers, quotas),
	}, nil
}

// ReadQuotaOperations returns the exact current-week impact used by the
// Configuration Center before it enables the destructive all-user reset. It
// is deliberately read-only and does not acquire the lifecycle writer lock.
func (manager *UserManager) ReadQuotaOperations(
	ctx context.Context,
) (UserQuotaOperationSummary, error) {
	settings, err := manager.store.ReadSettings(ctx)
	if err != nil {
		return UserQuotaOperationSummary{}, fmt.Errorf("read quota operation settings: %w", err)
	}
	knownUsers, err := manager.store.KnownUsers(ctx)
	if err != nil {
		return UserQuotaOperationSummary{}, fmt.Errorf("list quota operation users: %w", err)
	}
	known := make(map[string]struct{}, len(knownUsers))
	for _, rawUser := range knownUsers {
		if user := strings.ToLower(strings.TrimSpace(rawUser)); user != "" {
			known[user] = struct{}{}
		}
	}
	knownUsers = knownUsers[:0]
	for user := range known {
		knownUsers = append(knownUsers, user)
	}
	sort.Strings(knownUsers)
	defaultLimit, _, err := userQuotaConfiguration(settings)
	if err != nil {
		return UserQuotaOperationSummary{}, err
	}
	quotas, err := manager.credentials.WeeklyQuotas(ctx, knownUsers, defaultLimit)
	if err != nil {
		return UserQuotaOperationSummary{}, fmt.Errorf("read quota operation summary: %w", err)
	}
	return summarizeQuotaOperations(knownUsers, quotas), nil
}

func quotaActionMessage(action string, selected int, result usage.QuotaActionResult, languages ...i18n.Language) string {
	switch action {
	case "restore_default":
		return i18n.M("admin.restored_the_organization_quota_default_for_users", i18n.Params{"Count": selected}).Render(i18n.Selected(languages))
	case "add_bonus":
		return i18n.M("admin.added_weekly_bonus_quota_for_users_effective_after_the_next", i18n.Params{"Count": selected}).Render(i18n.Selected(languages))
	default:
		message := i18n.M("admin.reset_weekly_usage_for_users_effective_after_the_next_collection", i18n.Params{"Count": len(result.AppliedUsers)}).Render(i18n.Selected(languages))
		if len(result.SkippedUsers) > 0 {
			message += i18n.M("admin.skipped_users_whose_current_usage_is_zero", i18n.Params{"Count": len(result.SkippedUsers)}).Render(i18n.Selected(languages))
		}
		return message
	}
}

func summarizeQuotaOperations(users []string, quotas map[string]usage.WeeklyQuota) UserQuotaOperationSummary {
	summary := UserQuotaOperationSummary{TotalUsers: len(users)}
	for _, user := range users {
		quota := quotas[user]
		if summary.WeekStartAt == nil {
			start, end := quota.WeekStartAt, quota.WeekEndAt
			summary.WeekStartAt, summary.WeekEndAt = &start, &end
		}
		if quota.UsedTokens > 0 {
			summary.UsersWithUsage++
		}
		summary.TotalUsedTokens += quota.UsedTokens
		summary.TotalRawUsedTokens += quota.RawUsedTokens
		if quota.PolicyMode != "inherit" {
			summary.UsersWithPersonalPolicy++
		}
		if quota.BonusTokens > 0 {
			summary.UsersWithBonus++
		}
		if quota.UsageResetTokens > 0 {
			summary.UsersWithUsageReset++
		}
	}
	return summary
}

func isQuotaActionInputError(err error) bool {
	return errors.Is(err, usage.ErrInvalidQuotaAction) ||
		errors.Is(err, usage.ErrQuotaActionUnlimited) ||
		errors.Is(err, usage.ErrQuotaActionLimitExceeded)
}
