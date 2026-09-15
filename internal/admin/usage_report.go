package admin

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usagereport"
	"github.com/gin-gonic/gin"
)

type weeklyUsageReader interface {
	ReportUsage(context.Context, []usage.ReportWindow) ([]usage.ReportUsageRow, error)
}

func (server *Server) exportWeeklyUsage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	withUnits := false
	switch c.Query("with_units") {
	case "", "false":
	case "true":
		withUnits = true
	default:
		writeError(c, http.StatusBadRequest, i18n.M("admin.with_units_must_be_true_or_false"), "invalid_report_format")
		return
	}
	reader, ok := server.usage.(weeklyUsageReader)
	if !ok {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_export_service_is_not_ready"), "usage_not_ready")
		return
	}
	if !server.usageReportMu.TryLock() {
		c.Header("Retry-After", "5")
		writeError(c, http.StatusTooManyRequests, i18n.M("admin.another_weekly_report_is_being_generated_please_try_again_later"), "report_busy")
		return
	}
	defer server.usageReportMu.Unlock()
	// Leave room for transfer before the Admin server's 30-second write timeout.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	zone, _, err := server.usageTimezone(c)
	if err != nil {
		return
	}
	period, err := usagereport.ResolvePeriod(c.Query("week_start"), server.now(), zone)
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_report_week")
		return
	}
	teamCatalog, err := server.loadTeamUsageCatalog(c)
	if err != nil {
		return
	}
	accounts, err := server.store.ReadAccounts(ctx)
	if err != nil {
		server.internalError(c, "read report accounts", err)
		return
	}
	routes, err := server.store.ReadRoutes(ctx)
	if err != nil {
		server.internalError(c, "read report user bindings", err)
		return
	}
	catalog := usagereport.Catalog{Accounts: map[string]string{}, Teams: map[string]string{}, UserTeams: teamCatalog.currentTeamByUser, UserAccounts: routes}
	for _, account := range accounts {
		catalog.Accounts[account.ID] = account.Email
	}
	for _, team := range teamCatalog.teams {
		catalog.Teams[team.ID] = team.Name
	}
	var rows []usage.ReportUsageRow
	if windows := period.Windows(); len(windows) > 0 {
		queryCtx, queryCancel := context.WithTimeout(ctx, 12*time.Second)
		rows, err = reader.ReportUsage(queryCtx, windows)
		if queryCtx.Err() != nil {
			err = queryCtx.Err()
		}
		queryCancel()
		if err != nil {
			server.reportError(c, err)
			return
		}
	}
	report, err := usagereport.Build(period, catalog, rows, httpi18n.Locale(c))
	if err != nil {
		server.reportError(c, err)
		return
	}
	data, err := usagereport.XLSX(ctx, report, usagereport.XLSXOptions{WithUnits: withUnits, Language: httpi18n.Locale(c)})
	if err != nil {
		server.reportError(c, err)
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": period.Filename(httpi18n.Locale(c))}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, usagereport.ContentType, data)
}

func (server *Server) reportError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, usage.ErrReportTooLarge):
		writeError(c, http.StatusUnprocessableEntity, i18n.M("admin.this_week_s_details_exceed_the_export_limit_a_complete"), "report_too_large")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), c.Request.Context().Err() != nil:
		writeError(c, http.StatusGatewayTimeout, i18n.M("admin.weekly_report_generation_timed_out_or_was_cancelled_please_try"), "report_timeout")
	default:
		server.internalError(c, "export weekly usage", err)
	}
}
