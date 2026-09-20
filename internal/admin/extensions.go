package admin

import (
	"net/http"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
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
	httpi18n.JSON(c, http.StatusOK, status)
}
