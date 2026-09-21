package admin

import (
	"net/http"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
	"github.com/gin-gonic/gin"
)

func (server *Server) readExtensions(c *gin.Context) {
	if server.extensions == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"code": "extensions_unavailable", "message": "Software management unavailable"}})
		return
	}
	status, err := server.extensions.Status(c.Request.Context())
	if err != nil {
		server.internalError(c, "read software status", err)
		return
	}
	account := c.Query("account")
	found := account == ""
	for index := range status.Accounts {
		row := &status.Accounts[index]
		if server.runtimeJobs != nil {
			for _, job := range server.runtimeJobs.Recent(maxRuntimeJobsResponse) {
				if job.Action == "plugin-update" && job.Target == row.Account {
					row.Job = &runtimeops.PluginJobStatus{ID: job.ID, Status: job.Status, Error: job.Error}
					break
				}
			}
		}
		if account != "" && row.Account == account {
			found = true
			runtime := server.extensions.TicketStatus(c.Request.Context(), *row)
			row.Runtime = &runtime
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "account_not_found", "message": "Account not found"}})
		return
	}
	httpi18n.JSON(c, http.StatusOK, status)
}
