package admin

import (
	"context"
	"errors"
	"math"
	"net/http"

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

type overviewCatalogAccount struct {
	ID                string                   `json:"id"`
	OperationalStatus accountOperationalStatus `json:"operational_status"`
}

type overviewCatalogUser struct {
	Email  string `json:"email"`
	Status string `json:"status"`
}

type overviewCatalogResponse struct {
	GeneratedAt int64                    `json:"generated_at"`
	Accounts    []overviewCatalogAccount `json:"accounts"`
	Users       []overviewCatalogUser    `json:"users"`
}

type overviewStatusResponse struct {
	GeneratedAt        int64                       `json:"generated_at"`
	AuthorizedAccounts int                         `json:"authorized_accounts"`
	RunningServices    int                         `json:"running_services"`
	TotalServices      int                         `json:"total_services"`
	Requests5M         int64                       `json:"requests_5m"`
	AccountQuota       overviewAccountQuotaSummary `json:"account_quota"`
	Warnings           []string                    `json:"warnings"`
}

type overviewAccountQuotaSummary struct {
	Available                   bool     `json:"available"`
	EnabledAccounts             int      `json:"enabled_accounts"`
	KnownAccounts               int      `json:"known_accounts"`
	UnknownAccounts             int      `json:"unknown_accounts"`
	AverageUsedPercent          *float64 `json:"average_used_percent"`
	AverageRemainingPercent     *float64 `json:"average_remaining_percent"`
	EquivalentRemainingAccounts float64  `json:"equivalent_remaining_accounts"`
	ExhaustedAccounts           int      `json:"exhausted_accounts"`
	HighRiskAccounts            int      `json:"high_risk_accounts"`
}

type overviewGatewayUsageReader interface {
	PublicGatewayUsage(context.Context, []string, int64, int64) (map[string]usage.PublicAccountUsage, error)
}

func (server *Server) readOverviewSummary(c *gin.Context) {
	summary, err := server.store.ReadOverviewSummary(c.Request.Context())
	if err != nil {
		server.internalError(c, "read bounded overview summary", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"generated_at": server.now().Unix(),
		"source":       "control-plane",
		"summary":      summary,
	})
}

// readOverviewCatalog reuses the canonical account presentation for trend selectors.
func (server *Server) readOverviewCatalog(c *gin.Context) {
	if server.accountStates == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.overview_filter_directory_service_is_not_ready"), "overview_catalog_not_ready")
		return
	}
	var (
		accounts []controlplane.Account
		users    []controlplane.UserSummary
		states   map[string]failover.AccountState
	)
	group, groupContext := errgroup.WithContext(c.Request.Context())
	group.Go(func() error {
		var err error
		accounts, err = server.store.ReadAccounts(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		users, err = server.store.ListUserSummaries(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		states, err = server.accountStates.AccountStates(groupContext)
		return err
	})
	if err := group.Wait(); err != nil {
		server.internalError(c, "read overview catalog", err)
		return
	}

	accountItems := make([]overviewCatalogAccount, 0, len(accounts))
	for _, account := range accounts {
		state, found := states[account.ID]
		accountItems = append(accountItems, overviewCatalogAccount{ID: account.ID,
			OperationalStatus: accountstatus.Present(account.GroupEnabled, state, found, httpi18n.Locale(c))})
	}
	userItems := make([]overviewCatalogUser, 0, len(users))
	for _, user := range users {
		userItems = append(userItems, overviewCatalogUser{Email: user.Email, Status: user.Status})
	}
	httpi18n.JSON(c, http.StatusOK, overviewCatalogResponse{
		GeneratedAt: server.now().Unix(), Accounts: accountItems, Users: userItems,
	})
}

// readOverviewStatus isolates the runtime-bound counters used by the final
// three legacy metric cards. It performs bounded read-only calls and never
// acquires the control-plane write fence or caches data in the Admin process.
func (server *Server) readOverviewStatus(c *gin.Context) {
	if server.runtime == nil || server.oauth == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.overview_runtime_status_service_is_not_ready"), "overview_status_not_ready")
		return
	}
	var (
		accounts        []controlplane.Account
		services        []runtimeops.Service
		officialQuota   quota.RuntimeState
		quotaFound      bool
		quotaStateError error
	)
	group, groupContext := errgroup.WithContext(c.Request.Context())
	group.Go(func() error {
		var err error
		accounts, err = server.store.ReadAccounts(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		services, err = server.runtime.List(groupContext)
		return err
	})
	group.Go(func() error {
		quotaFound, quotaStateError = server.store.ReadRuntimeState(groupContext, quota.RuntimeStateName, &officialQuota)
		return nil
	})
	if err := group.Wait(); err != nil {
		server.internalError(c, "read overview runtime status", err)
		return
	}

	response := overviewStatusResponse{
		GeneratedAt:   server.now().Unix(),
		TotalServices: len(services),
		Warnings:      make([]string, 0),
	}
	if quotaStateError != nil {
		server.logger.Warn("overview account quota is unavailable", zap.Error(quotaStateError))
		response.Warnings = append(response.Warnings, httpi18n.Text(c, "admin.unable_to_read_account_weekly_quotas"))
	}
	response.AccountQuota = buildOverviewAccountQuotaSummary(
		accounts,
		officialQuota.Snapshot,
		quotaFound && quotaStateError == nil,
	)
	for _, service := range services {
		if service.State == "running" {
			response.RunningServices++
		}
	}
	accountIDs := make([]string, 0, len(accounts))
	for _, account := range accounts {
		accountIDs = append(accountIDs, account.ID)
		if _, err := server.oauth.Load(account.ID); err == nil {
			response.AuthorizedAccounts++
		} else if !errors.Is(err, quota.ErrOAuthMissing) {
			server.internalError(c, "read overview OAuth status", err)
			return
		}
	}
	if reader, ok := server.usage.(overviewGatewayUsageReader); ok {
		usageByAccount, err := reader.PublicGatewayUsage(
			c.Request.Context(), accountIDs, response.GeneratedAt-300, response.GeneratedAt,
		)
		if err != nil {
			server.logger.Warn("five-minute overview usage is unavailable", zap.Error(err))
			response.Warnings = append(response.Warnings, httpi18n.Text(c, "admin.unable_to_read_statistics_for_the_last_5_minutes"))
		} else {
			for _, account := range accountIDs {
				response.Requests5M += usageByAccount[account].RequestCount
			}
		}
	} else {
		response.Warnings = append(response.Warnings, httpi18n.Text(c, "admin.statistics_for_the_last_5_minutes_are_unavailable"))
	}
	httpi18n.JSON(c, http.StatusOK, response)
}

func buildOverviewAccountQuotaSummary(
	accounts []controlplane.Account,
	snapshot quota.Snapshot,
	quotaFound bool,
) overviewAccountQuotaSummary {
	summary := overviewAccountQuotaSummary{}
	quotaByAccount := make(map[string]quota.AccountQuota, len(snapshot.Accounts))
	if quotaFound {
		for _, accountQuota := range snapshot.Accounts {
			quotaByAccount[accountQuota.Account] = accountQuota
		}
	}
	var usedTotal, remainingTotal float64
	for _, account := range accounts {
		if !account.GroupEnabled {
			continue
		}
		summary.EnabledAccounts++
		accountQuota, found := quotaByAccount[account.ID]
		if !found || accountQuota.Status != "ok" || accountQuota.Weekly == nil {
			continue
		}
		used := math.Max(0, math.Min(accountQuota.Weekly.UsedPercent, 100))
		limitReached := accountQuota.Weekly.LimitReached ||
			(accountQuota.LimitReached != nil && *accountQuota.LimitReached) ||
			(accountQuota.Allowed != nil && !*accountQuota.Allowed)
		if limitReached {
			used = 100
		}
		remaining := 100 - used
		summary.KnownAccounts++
		usedTotal += used
		remainingTotal += remaining
		switch {
		case used >= 100:
			summary.ExhaustedAccounts++
		case used >= 90:
			summary.HighRiskAccounts++
		}
	}
	summary.UnknownAccounts = summary.EnabledAccounts - summary.KnownAccounts
	if summary.KnownAccounts == 0 {
		return summary
	}
	summary.Available = true
	averageUsed := roundOverviewQuota(usedTotal / float64(summary.KnownAccounts))
	averageRemaining := roundOverviewQuota(remainingTotal / float64(summary.KnownAccounts))
	summary.AverageUsedPercent = &averageUsed
	summary.AverageRemainingPercent = &averageRemaining
	summary.EquivalentRemainingAccounts = roundOverviewQuota(remainingTotal / 100)
	return summary
}

func roundOverviewQuota(value float64) float64 {
	return math.Round(value*100) / 100
}
