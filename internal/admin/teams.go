package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/gin-gonic/gin"
)

type teamPayload struct {
	ID          string `json:"id"`
	Name        string `json:"name" binding:"required,max=64"`
	Description string `json:"description" binding:"max=200"`
}

func (server *Server) listTeams(c *gin.Context) {
	teams, err := server.store.ListTeams(c.Request.Context())
	if err != nil {
		server.internalError(c, "list teams", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"teams": teams})
}

func (server *Server) createTeam(c *gin.Context) {
	var body teamPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_team_parameters"), "invalid_request")
		return
	}
	team, err := server.store.CreateTeam(c.Request.Context(), body.Name, body.Description)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	httpi18n.JSON(c, http.StatusCreated, gin.H{"message": i18n.M("admin.team_created"), "team": team})
}

func (server *Server) updateTeam(c *gin.Context) {
	var body teamPayload
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.ID) == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_team_parameters"), "invalid_request")
		return
	}
	team, err := server.store.UpdateTeam(
		c.Request.Context(),
		body.ID,
		body.Name,
		body.Description,
	)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"message": i18n.M("admin.team_updated"), "team": team})
}

func (server *Server) deleteTeam(c *gin.Context) {
	teamID := strings.TrimSpace(c.Query("id"))
	if teamID == "" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_team_parameters"), "invalid_request")
		return
	}
	team, err := server.store.DeleteTeam(c.Request.Context(), teamID)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{"message": i18n.M("admin.team_deleted"), "team": team})
}

func (server *Server) writeControlPlaneError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_request_parameters"), "invalid_request")
	case errors.Is(err, controlplane.ErrTeamNameExists):
		writeError(c, http.StatusConflict, i18n.M("admin.team_name_already_exists"), "team_name_conflict")
	case errors.Is(err, controlplane.ErrTeamNotFound):
		writeError(c, http.StatusNotFound, i18n.M("admin.team_does_not_exist"), "team_not_found")
	case errors.Is(err, controlplane.ErrTeamNotEmpty):
		writeError(c, http.StatusBadRequest, i18n.M("admin.the_team_still_has_users_and_cannot_be_deleted"), "team_not_empty")
	case errors.Is(err, controlplane.ErrTeamMembershipConflict):
		writeError(c, http.StatusConflict, i18n.M("admin.user_team_membership_changed_refresh_and_try_again"), "team_membership_conflict")
	default:
		server.internalError(c, "mutate control-plane catalog", err)
	}
}
