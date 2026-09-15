package admin

import (
	"net/http"
	"sort"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

type teamUsageRow struct {
	controlplane.Team
	CurrentUserCount int                    `json:"current_user_count"`
	Usage            usage.TeamUsageMetrics `json:"usage"`
}

type teamUsageResponse struct {
	usageWindowContext
	Attribution string         `json:"attribution"`
	Teams       []teamUsageRow `json:"teams"`
}

type teamUsageBreakdownResponse struct {
	usageWindowContext
	Definition string `json:"definition"`
	usage.TeamBreakdown
}

type teamUsageCatalog struct {
	teams              []controlplane.Team
	teamByID           map[string]controlplane.Team
	currentTeamByUser  map[string]string
	currentUsersByTeam map[string][]string
	unassignedCount    int
}

func (server *Server) readTeamUsage(c *gin.Context) {
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	catalog, err := server.loadTeamUsageCatalog(c)
	if err != nil {
		return
	}
	window, err := server.parseUsageWindow(c, false)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	teamIDs := make([]string, 0, len(catalog.teams))
	for _, team := range catalog.teams {
		teamIDs = append(teamIDs, team.ID)
	}
	metrics, err := server.usage.TeamUsage(
		c.Request.Context(),
		teamIDs,
		catalog.currentTeamByUser,
		window.WindowStartAt,
		window.queryEndAt,
	)
	if err != nil {
		server.internalError(c, "query team usage", err)
		return
	}
	rows := make([]teamUsageRow, 0, len(catalog.teams)+1)
	for _, team := range catalog.teams {
		rows = append(rows, teamUsageRow{
			Team:             team,
			CurrentUserCount: team.UserCount,
			Usage:            metrics[team.ID],
		})
	}
	rows = append(rows, teamUsageRow{
		Team: controlplane.Team{
			ID:          "unassigned",
			Name:        httpi18n.Text(c, "admin.ungrouped"),
			Description: httpi18n.Text(c, "admin.current_users_with_no_assigned_team"),
			TagStyle:    "slate",
			UserCount:   catalog.unassignedCount,
		},
		CurrentUserCount: catalog.unassignedCount,
		Usage:            metrics["unassigned"],
	})
	sort.Slice(rows, func(left, right int) bool {
		if rows[left].Usage.WeightedTokens != rows[right].Usage.WeightedTokens {
			return rows[left].Usage.WeightedTokens > rows[right].Usage.WeightedTokens
		}
		leftName := strings.ToLower(rows[left].Name)
		rightName := strings.ToLower(rows[right].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return rows[left].ID < rows[right].ID
	})
	httpi18n.JSON(c, http.StatusOK, teamUsageResponse{
		usageWindowContext: window,
		Attribution:        "current_membership",
		Teams:              rows,
	})
}

func (server *Server) readTeamUsageBreakdown(c *gin.Context) {
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	catalog, err := server.loadTeamUsageCatalog(c)
	if err != nil {
		return
	}
	teamID := strings.TrimSpace(c.Query("team_id"))
	if teamID != "unassigned" {
		if _, found := catalog.teamByID[teamID]; !found {
			writeError(c, http.StatusNotFound, i18n.M("admin.team_does_not_exist"), "team_not_found")
			return
		}
	}
	window, err := server.parseUsageWindow(c, false)
	if err != nil {
		writeUsageWindowError(c, err)
		return
	}
	breakdown, err := server.usage.TeamBreakdown(
		c.Request.Context(),
		teamID,
		catalog.currentUsersByTeam[teamID],
		window.WindowStartAt,
		window.queryEndAt,
	)
	if err != nil {
		server.internalError(c, "query team usage breakdown", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, teamUsageBreakdownResponse{
		usageWindowContext: window,
		Definition:         "team_model_reasoning_effort_tokens",
		TeamBreakdown:      breakdown,
	})
}

func (server *Server) loadTeamUsageCatalog(c *gin.Context) (teamUsageCatalog, error) {
	ctx := c.Request.Context()
	teams, err := server.store.ListTeams(ctx)
	if err != nil {
		server.internalError(c, "list teams for usage", err)
		return teamUsageCatalog{}, err
	}
	knownUsers, err := server.store.KnownUsers(ctx)
	if err != nil {
		server.internalError(c, "list users for team usage", err)
		return teamUsageCatalog{}, err
	}
	classifications, err := server.store.ReadUserTeams(ctx, knownUsers)
	if err != nil {
		server.internalError(c, "read team memberships for usage", err)
		return teamUsageCatalog{}, err
	}
	catalog := teamUsageCatalog{
		teams:              teams,
		teamByID:           make(map[string]controlplane.Team, len(teams)),
		currentTeamByUser:  make(map[string]string, len(classifications)),
		currentUsersByTeam: make(map[string][]string, len(teams)+1),
	}
	for _, team := range teams {
		catalog.teamByID[team.ID] = team
		catalog.currentUsersByTeam[team.ID] = make([]string, 0, team.UserCount)
	}
	catalog.currentUsersByTeam["unassigned"] = make([]string, 0)
	for rawUser, classification := range classifications {
		user := strings.ToLower(strings.TrimSpace(rawUser))
		if user == "" {
			continue
		}
		teamID := ""
		breakdownID := "unassigned"
		if classification.TeamID != nil {
			teamID = strings.TrimSpace(*classification.TeamID)
			if _, found := catalog.teamByID[teamID]; found {
				breakdownID = teamID
			}
		}
		catalog.currentTeamByUser[user] = teamID
		catalog.currentUsersByTeam[breakdownID] = append(catalog.currentUsersByTeam[breakdownID], user)
		if breakdownID == "unassigned" {
			catalog.unassignedCount++
		}
	}
	for teamID := range catalog.currentUsersByTeam {
		sort.Strings(catalog.currentUsersByTeam[teamID])
	}
	return catalog, nil
}
