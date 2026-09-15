package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/mod/semver"
)

const (
	releaseStatusCacheTTL        = 15 * time.Minute
	deploymentVersionMarker      = ".deploy-initialized"
	deploymentVersionMarkerLimit = 4 * 1024
)

var strictReleaseSemverPattern = regexp.MustCompile(
	`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`,
)

// The installer also accepts dotted pre-release tags. Normalize that separator
// only for comparisons; retain the published tag in the displayed version.
var dottedReleasePrereleasePattern = regexp.MustCompile(`^(v?[0-9]+\.[0-9]+\.[0-9]+)\.(rc|alpha|beta)(\.[0-9A-Za-z.-]+)?$`)

type ReleaseCatalog interface {
	LatestRelease(context.Context) (string, error)
}

type releaseStatusResponse struct {
	Configured     bool   `json:"configured"`
	CurrentVersion string `json:"current_version"`
	Available      bool   `json:"available"`
	Status         string `json:"status,omitempty"`
	LatestVersion  string `json:"latest_version,omitempty"`
	CheckedAt      int64  `json:"checked_at,omitempty"`
}

type releaseConfigurationState struct {
	currentVersion string
}

type releaseLookupStatus struct {
	Status        string
	LatestVersion string
	CheckedAt     int64
}

func (server *Server) readReleaseStatus(c *gin.Context) {
	payload, err := server.releaseStatus(c.Request.Context(), c.Query("fresh") == "1")
	if err != nil {
		server.internalError(c, "read release status", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, payload)
}

func (server *Server) releaseStatus(ctx context.Context, force bool) (releaseStatusResponse, error) {
	configuration, err := server.releaseConfiguration(ctx)
	if err != nil {
		return releaseStatusResponse{}, err
	}
	base := releaseStatusResponse{
		Configured:     server.release != nil,
		CurrentVersion: configuration.currentVersion,
		Available:      false,
	}
	if server.release == nil {
		base.Status = "disabled"
		return base, nil
	}
	if normalizedSemver(configuration.currentVersion) == "" {
		base.CurrentVersion = ""
		base.Status = "current_version_unavailable"
		base.CheckedAt = server.now().Unix()
		return base, nil
	}

	metadata := server.cachedLatestRelease(ctx, force)
	base.Status = metadata.Status
	base.LatestVersion = metadata.LatestVersion
	base.CheckedAt = metadata.CheckedAt
	if metadata.Status == "ok" {
		base.Available = semver.Compare(
			normalizedSemver(metadata.LatestVersion),
			normalizedSemver(configuration.currentVersion),
		) > 0
	}
	return base, nil
}

func (server *Server) cachedLatestRelease(
	ctx context.Context,
	force bool,
) releaseLookupStatus {
	now := server.now()
	server.releaseStatusMu.Lock()
	defer server.releaseStatusMu.Unlock()
	if !force && server.releaseStatusCache != nil && now.Before(server.releaseStatusCacheUntil) {
		return *server.releaseStatusCache
	}
	payload := releaseLookupStatus{Status: "unavailable", CheckedAt: now.Unix()}
	if server.release != nil {
		latestVersion, releaseErr := server.release.LatestRelease(ctx)
		latestVersion = strings.TrimSpace(latestVersion)
		if releaseErr == nil && normalizedSemver(latestVersion) != "" {
			payload.Status = "ok"
			payload.LatestVersion = latestVersion
		} else if releaseErr == nil {
			releaseErr = fmt.Errorf("latest GitHub Release contains an invalid version")
		}
		if releaseErr != nil {
			server.logger.Warn("GitHub Release refresh unavailable", zap.String("error", runtimeops.Sanitize(releaseErr.Error())))
		}
	}
	server.releaseStatusCache = &payload
	server.releaseStatusCacheUntil = now.Add(releaseStatusCacheTTL)
	return payload
}

func (server *Server) releaseConfiguration(ctx context.Context) (releaseConfigurationState, error) {
	configuration := releaseConfigurationState{}
	currentVersion, markerPresent, markerErr := readDeploymentVersionMarker(server.root)
	if markerErr != nil {
		server.logger.Warn(
			"deployment version marker unavailable",
			zap.String("error", runtimeops.Sanitize(markerErr.Error())),
		)
	} else if markerPresent {
		configuration.currentVersion = currentVersion
	}
	if !markerPresent {
		deployment := make(map[string]any)
		if _, err := server.store.ReadRuntimeState(ctx, "deployment", &deployment); err != nil {
			return releaseConfigurationState{}, fmt.Errorf("read applied deployment: %w", err)
		}
		applied := deployment
		if value, ok := deployment["applied"].(map[string]any); ok {
			applied = value
		} else if _, hasPending := deployment["pending"]; hasPending {
			applied = map[string]any{}
		}
		legacyVersion, _ := applied["version"].(string)
		if normalizedSemver(legacyVersion) != "" {
			configuration.currentVersion = strings.TrimSpace(legacyVersion)
		}
	}
	return configuration, nil
}

func readDeploymentVersionMarker(root string) (string, bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", false, nil
	}
	path := filepath.Join(root, deploymentVersionMarker)
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", true, fmt.Errorf("inspect %s: %w", deploymentVersionMarker, err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return "", true, fmt.Errorf("%s must be a regular file", deploymentVersionMarker)
	}
	if before.Size() > deploymentVersionMarkerLimit {
		return "", true, fmt.Errorf("%s exceeds %d bytes", deploymentVersionMarker, deploymentVersionMarkerLimit)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", true, fmt.Errorf("open %s: %w", deploymentVersionMarker, err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return "", true, fmt.Errorf("stat opened %s: %w", deploymentVersionMarker, err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return "", true, fmt.Errorf("%s changed while it was read", deploymentVersionMarker)
	}
	content, err := io.ReadAll(io.LimitReader(file, deploymentVersionMarkerLimit+1))
	if err != nil {
		return "", true, fmt.Errorf("read %s: %w", deploymentVersionMarker, err)
	}
	if len(content) > deploymentVersionMarkerLimit {
		return "", true, fmt.Errorf("%s exceeds %d bytes", deploymentVersionMarker, deploymentVersionMarkerLimit)
	}
	version := ""
	versionLines := 0
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "version=") {
			return "", true, fmt.Errorf("%s contains an unsupported entry", deploymentVersionMarker)
		}
		versionLines++
		version = strings.TrimSpace(strings.TrimPrefix(line, "version="))
	}
	if versionLines != 1 || normalizedSemver(version) == "" {
		return "", true, fmt.Errorf("%s must contain one valid semantic version", deploymentVersionMarker)
	}
	return version, true, nil
}

func normalizedSemver(value string) string {
	value = strings.TrimSpace(value)
	value = dottedReleasePrereleasePattern.ReplaceAllString(value, "$1-$2$3")
	if !strictReleaseSemverPattern.MatchString(value) {
		return ""
	}
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if !semver.IsValid(value) {
		return ""
	}
	return value
}
