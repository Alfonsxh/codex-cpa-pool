package admin

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/gin-gonic/gin"
)

const (
	portalInitialPasswordSecret = "portal_initial_password"
	minimumPortalPasswordLength = 8
	maximumPortalPasswordLength = 128
	legacyPortalPassword        = "123456"
)

func (server *Server) updateInitialPassword(c *gin.Context) {
	var body struct {
		InitialPassword string `json:"initial_password" binding:"required"`
		Confirmation    string `json:"confirmation" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_password_format"), "invalid_password")
		return
	}
	if !constantTimeEqual(body.InitialPassword, body.Confirmation) {
		writeError(c, http.StatusBadRequest, i18n.M("admin.the_initial_passwords_do_not_match"), "password_mismatch")
		return
	}
	length := utf8.RuneCountInString(body.InitialPassword)
	if length < minimumPortalPasswordLength || length > maximumPortalPasswordLength {
		writeError(c, http.StatusBadRequest, i18n.M("admin.the_initial_password_must_contain_8_128_characters"), "invalid_password")
		return
	}
	if constantTimeEqual(body.InitialPassword, legacyPortalPassword) {
		writeError(c, http.StatusBadRequest, i18n.M("admin.this_retired_default_password_cannot_be_used"), "weak_password")
		return
	}
	if err := server.store.WriteSecret(c.Request.Context(), portalInitialPasswordSecret, body.InitialPassword); err != nil {
		server.internalError(c, "update initial portal password", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message":    i18n.M("admin.initial_user_password_saved_securely_existing_passwords_are_unchanged"),
		"configured": true,
	})
}

func (server *Server) rotateManagementKey(c *gin.Context) {
	var body struct {
		NewKey       string `json:"new_key" binding:"required"`
		Confirmation string `json:"confirmation" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, i18n.M("admin.invalid_management_key_format"), "invalid_management_key")
		return
	}
	if !constantTimeEqual(body.NewKey, body.Confirmation) {
		writeError(c, http.StatusBadRequest, i18n.M("admin.the_management_keys_do_not_match"), "management_key_mismatch")
		return
	}
	if err := validateManagementKey(body.NewKey); err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_management_key")
		return
	}
	current, found, err := server.store.ReadSecret(c.Request.Context(), "cpa_management_key")
	if err != nil {
		server.internalError(c, "read current management key", err)
		return
	}
	if !found {
		writeError(c, http.StatusConflict, i18n.M("admin.no_management_key_configured"), "management_key_not_configured")
		return
	}
	if constantTimeEqual(current, body.NewKey) {
		writeError(c, http.StatusBadRequest, i18n.M("admin.the_new_management_key_must_differ_from_the_current_key"), "management_key_unchanged")
		return
	}
	if err := server.store.WriteSecret(c.Request.Context(), "cpa_management_key", body.NewKey); err != nil {
		server.internalError(c, "rotate management key", err)
		return
	}
	server.sessionGeneration.Add(1)
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("admin.management_key_updated_sign_in_using_the_new_key"),
		"result":  gin.H{"rotated": true, "services": 0},
	})
}

func validateManagementKey(value string) error {
	length := utf8.RuneCountInString(value)
	if length < 12 || length > 128 {
		return i18n.M("admin.the_management_key_must_contain_12_128_characters")
	}
	if strings.TrimSpace(value) != value {
		return i18n.M("admin.the_management_key_must_not_contain_whitespace_or_control_characters")
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return i18n.M("admin.the_management_key_must_not_contain_whitespace_or_control_characters")
		}
	}
	return nil
}

func constantTimeEqual(left string, right string) bool {
	leftBytes := []byte(left)
	rightBytes := []byte(right)
	return len(leftBytes) == len(rightBytes) && subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}
