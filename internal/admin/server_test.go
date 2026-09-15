package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountlifecycle"
	"github.com/Alfonsxh/codex-cpa-pool/internal/accountstatus"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/identity"
	"github.com/Alfonsxh/codex-cpa-pool/internal/notifications"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

func TestAdminSharedGinRouterRegistersPortalRoutesBeforeNoRoute(t *testing.T) {
	baseServer, store := newTestAdmin(t)
	baseServer.Close()
	registrar := &fakeRouteRegistrar{}
	server, err := New(Config{Store: store, Portal: registrar})
	if err != nil {
		t.Fatalf("New shared router: %v", err)
	}
	t.Cleanup(server.Close)

	response := performAdminRequest(server, http.MethodGet, "/usage/test", nil, nil, nil)
	if response.Code != http.StatusOK || !registrar.registered || !strings.Contains(response.Body.String(), "portal") {
		t.Fatalf("shared portal route = %d %s, registrar=%#v", response.Code, response.Body.String(), registrar)
	}
}

func TestAdminSessionUsesRevocableServerSideStateAndCSRF(t *testing.T) {
	server, _ := newTestAdmin(t)

	response := performAdminRequest(server, http.MethodGet, "/admin/api/session", nil, nil, nil)
	assertAdminError(t, response, http.StatusUnauthorized, "session_missing")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/session", nil, map[string]string{
		"X-Management-Key": "wrong-management-key",
	}, nil)
	assertAdminError(t, response, http.StatusUnauthorized, "management_key_invalid")

	response = performAdminRequest(server, http.MethodPost, "/admin/api/session", nil, map[string]string{
		"X-Management-Key":  "test-management-key",
		"X-Forwarded-Proto": "https",
	}, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		Authenticated     bool   `json:"authenticated"`
		CSRFToken         string `json:"csrf_token"`
		IdleExpiresAt     int64  `json:"idle_expires_at"`
		AbsoluteExpiresAt int64  `json:"absolute_expires_at"`
	}
	var createdFields map[string]json.RawMessage
	decodeAdminResponse(t, response, &createdFields)
	if _, found := createdFields["accounts"]; found {
		t.Fatalf("session response eagerly includes account catalog: %#v", createdFields)
	}
	decodeAdminResponse(t, response, &created)
	if !created.Authenticated || created.CSRFToken == "" || created.IdleExpiresAt <= time.Now().Unix() ||
		created.AbsoluteExpiresAt <= created.IdleExpiresAt {
		t.Fatalf("created session = %#v", created)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %#v", cookies)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/admin" {
		t.Fatalf("session cookie = %#v", cookie)
	}
	if response.Header().Get("Strict-Transport-Security") != "max-age=0" {
		t.Fatalf("HSTS = %q", response.Header().Get("Strict-Transport-Security"))
	}

	response = performAdminRequest(server, http.MethodGet, "/admin/api/session", nil, nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("read session status = %d, body = %s", response.Code, response.Body.String())
	}
	var loaded struct {
		CSRFToken string `json:"csrf_token"`
	}
	decodeAdminResponse(t, response, &loaded)
	if loaded.CSRFToken != created.CSRFToken {
		t.Fatalf("loaded CSRF token = %q, want %q", loaded.CSRFToken, created.CSRFToken)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/session/refresh", nil, map[string]string{
		"X-CSRF-Token": created.CSRFToken,
	}, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"absolute_expires_at":`) {
		t.Fatalf("refresh session status = %d, body = %s", response.Code, response.Body.String())
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/teams", map[string]any{
		"name": "Platform",
	}, nil, cookie)
	assertAdminError(t, response, http.StatusForbidden, "csrf_required")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/teams", map[string]any{
		"name": "Platform",
	}, map[string]string{"X-CSRF-Token": created.CSRFToken}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("session team create status = %d, body = %s", response.Code, response.Body.String())
	}

	response = performAdminRequest(server, http.MethodDelete, "/admin/api/session", nil, map[string]string{
		"X-CSRF-Token": created.CSRFToken,
	}, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("delete session status = %d, body = %s", response.Code, response.Body.String())
	}
	deletedCookies := response.Result().Cookies()
	if len(deletedCookies) != 1 || deletedCookies[0].MaxAge >= 0 {
		t.Fatalf("deleted session cookie = %#v", deletedCookies)
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/session", nil, nil, cookie)
	assertAdminError(t, response, http.StatusUnauthorized, "session_expired")
}

func TestAdminSessionRefreshExtendsIdleDeadlineWithoutMovingAbsoluteDeadline(t *testing.T) {
	server, _ := newTestAdmin(t)
	now := time.Now().Truncate(time.Second)
	server.now = func() time.Time { return now }

	createdResponse := performAdminRequest(server, http.MethodPost, "/admin/api/session", nil, map[string]string{
		"X-Management-Key": "test-management-key",
	}, nil)
	var created struct {
		CSRFToken         string `json:"csrf_token"`
		IdleExpiresAt     int64  `json:"idle_expires_at"`
		AbsoluteExpiresAt int64  `json:"absolute_expires_at"`
	}
	decodeAdminResponse(t, createdResponse, &created)
	cookie := createdResponse.Result().Cookies()[0]

	now = now.Add(5 * time.Minute)
	refreshedResponse := performAdminRequest(server, http.MethodPost, "/admin/api/session/refresh", nil, map[string]string{
		"X-CSRF-Token": created.CSRFToken,
	}, cookie)
	var refreshed struct {
		IdleExpiresAt     int64 `json:"idle_expires_at"`
		AbsoluteExpiresAt int64 `json:"absolute_expires_at"`
	}
	decodeAdminResponse(t, refreshedResponse, &refreshed)
	if refreshed.IdleExpiresAt != created.IdleExpiresAt+int64((5*time.Minute)/time.Second) ||
		refreshed.AbsoluteExpiresAt != created.AbsoluteExpiresAt {
		t.Fatalf("refreshed session = %#v, created = %#v", refreshed, created)
	}

	now = time.Unix(refreshed.IdleExpiresAt+1, 0)
	expired := performAdminRequest(server, http.MethodGet, "/admin/api/session", nil, nil, cookie)
	assertAdminError(t, expired, http.StatusUnauthorized, "session_expired")
}

func TestAdminManagementKeyRotationInvalidatesOldKeysAndSessionsWithoutEchoingSecret(t *testing.T) {
	server, store := newTestAdmin(t)
	created := performAdminRequest(server, http.MethodPost, "/admin/api/session", nil, map[string]string{
		"X-Management-Key": "test-management-key",
	}, nil)
	if created.Code != http.StatusCreated || len(created.Result().Cookies()) != 1 {
		t.Fatalf("create pre-rotation session = %d %s", created.Code, created.Body.String())
	}
	cookie := created.Result().Cookies()[0]

	response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/management-key", map[string]any{
		"new_key": "new-management-key!", "confirmation": "different-key!",
	}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	assertAdminError(t, response, http.StatusBadRequest, "management_key_mismatch")

	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/management-key", map[string]any{
		"new_key": "new-management-key!", "confirmation": "new-management-key!",
	}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "new-management-key!") ||
		!strings.Contains(response.Body.String(), `"rotated":true`) {
		t.Fatalf("rotate management key = %d %s", response.Code, response.Body.String())
	}
	stored, found, err := store.ReadSecret(context.Background(), "cpa_management_key")
	if err != nil || !found || stored != "new-management-key!" {
		t.Fatalf("stored rotated management key = (%q, %v, %v)", stored, found, err)
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/teams", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil)
	assertAdminError(t, response, http.StatusUnauthorized, "management_key_invalid")
	response = performAdminRequest(server, http.MethodGet, "/admin/api/session", nil, nil, cookie)
	assertAdminError(t, response, http.StatusUnauthorized, "session_invalidated")
	response = performAdminRequest(server, http.MethodGet, "/admin/api/teams", nil,
		map[string]string{"X-Management-Key": "new-management-key!"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("new management key = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminOperationImpactUsesCurrentRoutesAndRejectsUnsafeTargets(t *testing.T) {
	server, store := newTestAdmin(t)
	ctx := context.Background()
	if err := store.WriteRoutes(ctx, map[string]string{
		"alice@example.com": "alpha", "bob@example.com": "alpha",
	}); err != nil {
		t.Fatalf("write impact routes: %v", err)
	}
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodGet,
		"/admin/api/operations/impact?action=stop&target=alpha", nil, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"target_type":"account"`) ||
		!strings.Contains(response.Body.String(), `"routed_users":2`) {
		t.Fatalf("account operation impact = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodGet,
		"/admin/api/operations/impact?action=stop&target=usage-collector", nil, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"target_type":"service"`) ||
		!strings.Contains(response.Body.String(), `"routed_users":null`) {
		t.Fatalf("service operation impact = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodGet,
		"/admin/api/operations/impact?action=restart&target=alpha", nil, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_runtime_target")
	response = performAdminRequest(server, http.MethodGet,
		"/admin/api/operations/impact?action=stop&target=gateway-blue", nil, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_runtime_target")
}

func TestAdminOverviewUsageUsesBoundedFineGrainedTrendQuery(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	reader := &fakeUsageReader{
		trendResult: usage.TokenTrend{
			GeneratedAt: 1_000, WindowStartAt: -2_600, WindowSeconds: 3_600,
			BucketSeconds: 60, Buckets: []int64{-2_640, -2_580},
			Accounts: []usage.TokenSeries{{Name: "alpha", Values: []int64{321, 0}, Total: 321, WeightedValues: []int64{400, 0}, WeightedTotal: 400}},
			Users:    []usage.TokenSeries{{Name: "alice@example.com", Values: []int64{321, 0}, Total: 321, WeightedValues: []int64{400, 0}, WeightedTotal: 400}},
		},
		collectorResult: usage.CollectorStatus{Status: "healthy"},
	}
	server, err := New(Config{Store: store, Usage: reader, Now: func() time.Time { return time.Unix(1_000, 0) }})
	if err != nil {
		t.Fatalf("New overview usage Admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodGet,
		"/admin/api/overview/usage?window=3600&account=alpha&user=alice%40example.com&user_limit=999&token_mode=weighted", nil, headers, nil)
	if response.Code != http.StatusOK || reader.trendCalls != 1 ||
		!strings.Contains(response.Body.String(), `"selected_account":"alpha"`) ||
		!strings.Contains(response.Body.String(), `"selected_user":"alice@example.com"`) ||
		!strings.Contains(response.Body.String(), `"total":321`) ||
		!strings.Contains(response.Body.String(), `"weighted_total":400`) ||
		!strings.Contains(response.Body.String(), `"window_timezone":"Asia/Shanghai"`) ||
		!strings.Contains(response.Body.String(), `"status":"healthy"`) {
		t.Fatalf("overview usage = %d %s, reader=%#v", response.Code, response.Body.String(), reader)
	}
	if reader.trendStartAt != -2_600 || reader.trendEndAt != 1_001 || reader.trendBucketSeconds != 60 {
		t.Fatalf("overview trend bounds = %#v", reader)
	}
	if reader.trendUserLimit != 500 || reader.trendTokenMode != usage.TokenModeWeighted {
		t.Fatalf("overview trend ranking = %#v", reader)
	}
	response = performAdminRequest(server, http.MethodGet,
		"/admin/api/overview/usage?window=300", nil, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	response = performAdminRequest(server, http.MethodGet,
		"/admin/api/overview/usage?window=3600&account=missing", nil, headers, nil)
	assertAdminError(t, response, http.StatusNotFound, "account_not_found")
	response = performAdminRequest(server, http.MethodGet,
		"/admin/api/overview/usage?window=3600&user=missing%40example.com", nil, headers, nil)
	assertAdminError(t, response, http.StatusNotFound, "user_not_found")
}

func TestAdminOverviewUsageSinceResetUsesOfficialQuotaPeriods(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	accounts, err := store.ReadAccounts(context.Background())
	if err != nil {
		t.Fatalf("read overview accounts: %v", err)
	}
	accounts = append(accounts, controlplane.Account{
		ID: "beta", Email: "beta@accounts.example.com", Port: 18319,
		ProxyMode: "inherit", CreatedAt: 100, GroupEnabled: true,
	})
	if err := store.WriteAccounts(context.Background(), accounts); err != nil {
		t.Fatalf("write overview accounts: %v", err)
	}
	now := int64(2_000_000)
	periodStart := now - 1_800
	resetAt := periodStart + quota.WeeklyWindowSeconds
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: "alpha", Status: "ok", Weekly: &quota.WeeklyWindow{
				WindowSeconds: quota.WeeklyWindowSeconds, ResetAt: &resetAt,
			},
		}}},
	}); err != nil {
		t.Fatalf("write overview quota state: %v", err)
	}
	reader := &fakeUsageReader{trendResult: usage.TokenTrend{
		GeneratedAt: now, WindowStartAt: periodStart, WindowSeconds: quota.WeeklyWindowSeconds,
		BucketSeconds: 3600, Buckets: []int64{periodStart},
		Accounts: []usage.TokenSeries{{Name: "alpha", Total: 100}},
	}, collectorResult: usage.CollectorStatus{Status: "healthy"}}
	server, err := New(Config{Store: store, Usage: reader, Now: func() time.Time { return time.Unix(now, 0) }})
	if err != nil {
		t.Fatalf("New since-reset overview Admin: %v", err)
	}
	t.Cleanup(server.Close)
	response := performAdminRequest(server, http.MethodGet,
		"/admin/api/overview/usage?window=since_reset", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("since-reset overview = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Window                 string           `json:"window"`
		WindowSeconds          int64            `json:"window_seconds"`
		WindowStartAt          int64            `json:"window_start_at"`
		WindowStartAtByAccount map[string]int64 `json:"window_start_at_by_account"`
		UnavailableAccounts    []string         `json:"unavailable_accounts"`
		BucketSeconds          int64            `json:"bucket_seconds"`
	}
	decodeAdminResponse(t, response, &payload)
	if payload.Window != sinceResetWindow || payload.WindowSeconds != quota.WeeklyWindowSeconds ||
		payload.WindowStartAt != periodStart || payload.BucketSeconds != 3600 ||
		payload.WindowStartAtByAccount["alpha"] != periodStart ||
		len(payload.UnavailableAccounts) != 1 || payload.UnavailableAccounts[0] != "beta" {
		t.Fatalf("since-reset overview payload = %#v", payload)
	}
	if reader.trendStartAt != periodStart || reader.trendEndAt != now+1 ||
		reader.trendStartAtByAccount["alpha"] != periodStart {
		t.Fatalf("since-reset overview reader = %#v", reader)
	}
}

func TestAdminReleaseStatusUsesDeploymentMarkerAndFreshBypassesCache(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	root := t.TempDir()
	marker := filepath.Join(root, deploymentVersionMarker)
	if err := os.WriteFile(marker, []byte("version=v1.1.0\n"), 0o600); err != nil {
		t.Fatalf("write deployment marker: %v", err)
	}
	if err := store.UpdateSettings(context.Background(), map[string]any{
		"delivery.release_metadata_image": "",
	}); err != nil {
		t.Fatalf("write retired release image setting: %v", err)
	}
	release := &fakeReleaseCatalog{version: "v1.2.0"}
	server, err := New(Config{
		Root: root, Store: store, Release: release, Now: func() time.Time { return time.Unix(2_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("New release Admin: %v", err)
	}
	t.Cleanup(server.Close)
	first, err := server.releaseStatus(context.Background(), false)
	if err != nil || !first.Configured || first.CurrentVersion != "v1.1.0" || !first.Available ||
		first.LatestVersion != "v1.2.0" || first.Status != "ok" {
		t.Fatalf("first release status = %#v, err=%v", first, err)
	}
	if _, err := server.releaseStatus(context.Background(), false); err != nil {
		t.Fatalf("read cached release status: %v", err)
	}
	if err := os.WriteFile(marker, []byte("version=v1.3.0\n"), 0o600); err != nil {
		t.Fatalf("update deployment marker: %v", err)
	}
	newerCurrent, err := server.releaseStatus(context.Background(), false)
	if err != nil || newerCurrent.CurrentVersion != "v1.3.0" || newerCurrent.Available {
		t.Fatalf("release status after marker update = %#v, err=%v", newerCurrent, err)
	}
	if release.calls != 1 {
		t.Fatalf("marker refresh should reuse remote metadata cache, calls = %d", release.calls)
	}
	if _, err := server.releaseStatus(context.Background(), true); err != nil {
		t.Fatalf("force release refresh: %v", err)
	}
	if release.calls != 2 {
		t.Fatalf("GitHub Release calls = %d", release.calls)
	}
	if normalizedSemver("v1.2.0-rc.2") == "" || normalizedSemver("v1.2.0-rc.01") != "" {
		t.Fatal("release semver validation does not match strict prerelease ordering")
	}
}

func TestAdminReleaseStatusSupportsLegacyFallbackDisabledAndFailureStates(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	if err := store.WriteRuntimeState(context.Background(), "deployment", map[string]any{
		"applied": map[string]any{"version": "v1.1.0"},
	}); err != nil {
		t.Fatalf("write deployment state: %v", err)
	}
	disabledServer, err := New(Config{Root: t.TempDir(), Store: store})
	if err != nil {
		t.Fatalf("New disabled release Admin: %v", err)
	}
	t.Cleanup(disabledServer.Close)
	disabled, err := disabledServer.releaseStatus(context.Background(), true)
	if err != nil || disabled.Configured || disabled.CurrentVersion != "v1.1.0" ||
		disabled.Status != "disabled" || disabled.CheckedAt != 0 {
		t.Fatalf("disabled release status = %#v, err=%v", disabled, err)
	}

	release := &fakeReleaseCatalog{err: errors.New("GitHub unavailable")}
	server, err := New(Config{Root: t.TempDir(), Store: store, Release: release})
	if err != nil {
		t.Fatalf("New release Admin: %v", err)
	}
	t.Cleanup(server.Close)
	unavailable, err := server.releaseStatus(context.Background(), true)
	if err != nil || !unavailable.Configured || unavailable.CurrentVersion != "v1.1.0" ||
		unavailable.Status != "unavailable" || unavailable.CheckedAt == 0 || release.calls != 1 {
		t.Fatalf("unavailable release status = %#v, calls=%d, err=%v", unavailable, release.calls, err)
	}
}

func TestAdminReleaseStatusRejectsInvalidMarkerWithoutReportingStaleFallback(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	if err := store.WriteRuntimeState(context.Background(), "deployment", map[string]any{
		"applied": map[string]any{"version": "v9.9.9"},
	}); err != nil {
		t.Fatalf("write deployment state: %v", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, deploymentVersionMarker), []byte("version=not-semver\n"), 0o600); err != nil {
		t.Fatalf("write invalid marker: %v", err)
	}
	release := &fakeReleaseCatalog{version: "v10.0.0"}
	server, err := New(Config{Root: root, Store: store, Release: release})
	if err != nil {
		t.Fatalf("New release Admin: %v", err)
	}
	t.Cleanup(server.Close)
	status, err := server.releaseStatus(context.Background(), false)
	if err != nil || status.CurrentVersion != "" || status.Status != "current_version_unavailable" ||
		status.Available || release.calls != 0 {
		t.Fatalf("invalid marker release status = %#v, calls=%d, err=%v", status, release.calls, err)
	}
}

func TestReadDeploymentVersionMarkerValidatesFileBoundaryAndContent(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		version, present, err := readDeploymentVersionMarker(t.TempDir())
		if err != nil || present || version != "" {
			t.Fatalf("missing marker = (%q, %v, %v)", version, present, err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, deploymentVersionMarker), []byte("version=v2.0.0-rc.29\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		version, present, err := readDeploymentVersionMarker(root)
		if err != nil || !present || version != "v2.0.0-rc.29" {
			t.Fatalf("valid marker = (%q, %v, %v)", version, present, err)
		}
	})
	for name, content := range map[string]string{
		"duplicate":        "version=v1.0.0\nversion=v1.0.1\n",
		"invalid semver":   "version=v1.0\n",
		"unexpected entry": "version=v1.0.0\ndomain=example.com\n",
		"oversized":        strings.Repeat("x", deploymentVersionMarkerLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, deploymentVersionMarker), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, present, err := readDeploymentVersionMarker(root); !present || err == nil {
				t.Fatalf("unsafe marker present=%v err=%v", present, err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.WriteFile(target, []byte("version=v1.0.0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, deploymentVersionMarker)); err != nil {
			t.Fatal(err)
		}
		if _, present, err := readDeploymentVersionMarker(root); !present || err == nil {
			t.Fatalf("symlink marker present=%v err=%v", present, err)
		}
	})
}

func TestAdminTeamAPIUsesFineGrainedContractWithoutTags(t *testing.T) {
	server, _ := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/teams", nil, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list teams status = %d, body = %s", response.Code, response.Body.String())
	}
	var catalog map[string]json.RawMessage
	decodeAdminResponse(t, response, &catalog)
	if _, found := catalog["teams"]; !found {
		t.Fatalf("team catalog = %#v", catalog)
	}
	if _, found := catalog["tags"]; found {
		t.Fatalf("new team API exposes retired tags: %#v", catalog)
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/tags", nil, headers, nil)
	assertAdminError(t, response, http.StatusNotFound, "not_found")

	response = performAdminRequest(server, http.MethodPost, "/admin/api/teams", map[string]any{
		"name":        "  Platform   Team ",
		"description": " Core owners ",
	}, headers, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create team status = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		Team controlplane.Team `json:"team"`
	}
	decodeAdminResponse(t, response, &created)
	if created.Team.Name != "Platform Team" || created.Team.Description != "Core owners" ||
		created.Team.TagStyle != "indigo" {
		t.Fatalf("created team = %#v", created.Team)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/teams", map[string]any{
		"name": "platform team",
	}, headers, nil)
	assertAdminError(t, response, http.StatusConflict, "team_name_conflict")
	response = performAdminRequest(server, http.MethodPut, "/admin/api/teams", map[string]any{
		"id":          created.Team.ID,
		"name":        "Platform",
		"description": "Updated",
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("update team status = %d, body = %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(
		server,
		http.MethodDelete,
		"/admin/api/teams?id="+created.Team.ID,
		nil,
		headers,
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("delete team status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Admin API Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
}

func TestAdminTeamUsageUsesCurrentMembershipAndLazyBreakdownContract(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	ctx := context.Background()
	team, err := store.CreateTeam(ctx, "Platform", "Platform owners")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	teamID := team.ID
	if _, err := store.SetUserTeams(ctx, []string{"alice@example.com"}, &teamID); err != nil {
		t.Fatalf("assign team: %v", err)
	}
	reader := &fakeUsageReader{
		teamResult: map[string]usage.TeamUsageMetrics{
			team.ID:      {WeightedMetrics: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{TotalTokens: 100}, WeightedTokens: 150}, ActiveUsers: 1},
			"unassigned": {},
		},
		teamBreakdownResult: usage.TeamBreakdown{
			TeamID: team.ID, Attribution: "current_membership",
			Totals: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{TotalTokens: 100}, WeightedTokens: 150},
			Users:  []usage.TeamUserUsage{{User: "alice@example.com"}},
			Series: usage.TeamUsageSeries{StartAt: 0, EndAt: 1_000, BucketSeconds: 300, Buckets: []int64{0}, Values: []int64{150}},
		},
	}
	server, err := New(Config{Store: store, Usage: reader, Now: func() time.Time { return time.Unix(1_000, 0) }})
	if err != nil {
		t.Fatalf("New team usage Admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/teams/usage?window=all", nil, headers, nil)
	if response.Code != http.StatusOK || reader.teamCalls != 1 ||
		!strings.Contains(response.Body.String(), `"attribution":"current_membership"`) ||
		!strings.Contains(response.Body.String(), `"weighted_tokens":150`) ||
		strings.Contains(response.Body.String(), `"tags"`) {
		t.Fatalf("team usage response = %d %s, reader=%#v", response.Code, response.Body.String(), reader)
	}
	response = performAdminRequest(
		server, http.MethodGet,
		"/admin/api/teams/usage-breakdown?window=all&team_id="+team.ID,
		nil, headers, nil,
	)
	if response.Code != http.StatusOK || reader.teamBreakdownCalls != 1 ||
		!strings.Contains(response.Body.String(), `"definition":"team_model_reasoning_effort_tokens"`) ||
		!strings.Contains(response.Body.String(), `"user":"alice@example.com"`) {
		t.Fatalf("team breakdown response = %d %s, reader=%#v", response.Code, response.Body.String(), reader)
	}
	response = performAdminRequest(
		server, http.MethodGet,
		"/admin/api/teams/usage-breakdown?window=all&team_id=missing",
		nil, headers, nil,
	)
	assertAdminError(t, response, http.StatusNotFound, "team_not_found")
	if reader.teamBreakdownCalls != 1 {
		t.Fatal("unknown team reached usage reader")
	}
}

func TestUserTeamAPIValidatesUsersAndRejectsStaleMembership(t *testing.T) {
	server, _ := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodPost, "/admin/api/teams", map[string]any{
		"name": "Platform",
	}, headers, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create team status = %d, body = %s", response.Code, response.Body.String())
	}
	var created struct {
		Team controlplane.Team `json:"team"`
	}
	decodeAdminResponse(t, response, &created)

	response = performAdminRequest(server, http.MethodPut, "/admin/api/users/team", map[string]any{
		"email":            "Alice@Example.com",
		"team_id":          created.Team.ID,
		"expected_team_id": nil,
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("assign user team status = %d, body = %s", response.Code, response.Body.String())
	}
	var assigned map[string]json.RawMessage
	decodeAdminResponse(t, response, &assigned)
	if bytes.Contains(response.Body.Bytes(), []byte(`"tags"`)) {
		t.Fatalf("new user team response exposes retired tags: %s", response.Body.String())
	}
	synchronizer := server.teamIdentities.(*fakeTeamIdentitySynchronizer)
	identity, found := synchronizer.identities["alice@example.com"]
	if synchronizer.calls != 1 || !found || identity.TeamID != created.Team.ID || identity.MembershipVersion != 1 {
		t.Fatalf("synchronized team identity = %#v, calls=%d", synchronizer.identities, synchronizer.calls)
	}

	response = performAdminRequest(server, http.MethodPut, "/admin/api/users/team", map[string]any{
		"email":            "alice@example.com",
		"team_id":          nil,
		"expected_team_id": nil,
	}, headers, nil)
	assertAdminError(t, response, http.StatusConflict, "team_membership_conflict")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/team/batch", map[string]any{
		"users":            []string{"alice@example.com"},
		"team_id":          nil,
		"expected_team_id": nil,
	}, headers, nil)
	assertAdminError(t, response, http.StatusConflict, "team_membership_conflict")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/team/batch", map[string]any{
		"users":            []string{"alice@example.com"},
		"team_id":          nil,
		"expected_team_id": created.Team.ID,
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("expected batch reassignment status = %d, body = %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/team/batch", map[string]any{
		"users":   []string{"missing@example.com"},
		"team_id": created.Team.ID,
	}, headers, nil)
	assertAdminError(t, response, http.StatusNotFound, "user_not_found")
}

func TestUserTeamAPIRejectsMutationWhenUsageIdentitySyncIsUnavailable(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	team, err := store.CreateTeam(context.Background(), "Platform", "")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	server, err := New(Config{Store: store})
	if err != nil {
		t.Fatalf("New Admin without usage identity synchronizer: %v", err)
	}
	t.Cleanup(server.Close)
	response := performAdminRequest(server, http.MethodPut, "/admin/api/users/team", map[string]any{
		"email": "alice@example.com", "team_id": team.ID,
	}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	assertAdminError(t, response, http.StatusServiceUnavailable, "usage_not_ready")
	classifications, err := store.ReadUserTeams(context.Background(), []string{"alice@example.com"})
	if err != nil || classifications["alice@example.com"].TeamID != nil ||
		classifications["alice@example.com"].TeamMembershipVersion != 0 {
		t.Fatalf("team assignment changed without usage sync: %#v, %v", classifications, err)
	}
}

func TestUserListAPIIsFineGrainedPaginatedAndSecretFree(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	reader := &fakeUsageReader{
		collectorResult: usage.CollectorStatus{Status: "healthy"},
		userSummaries: map[string]usage.WeightedMetrics{
			"alice@example.com": {
				RawMetrics: usage.RawMetrics{
					RequestCount: 3, SuccessCount: 2, FailedCount: 1,
					TotalTokens: 900, LastUsedAt: 950,
				},
				WeightedTokens: 1_100,
			},
		},
	}
	fixedNow := time.Unix(1_787_500_800, 0)
	server, err := New(Config{
		Store: store, Usage: reader, Users: &fakeUserLifecycle{},
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("New user catalog Admin: %v", err)
	}
	t.Cleanup(server.Close)
	if err := store.WriteRoutes(context.Background(), map[string]string{
		"alice@example.com": "alpha",
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	response := performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/users?view=summary&window=today&page=1&page_size=25&q=alice&sort=tokens&direction=desc",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("list users status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Users      []controlplane.UserSummary `json:"users"`
		Pagination struct {
			Page     int `json:"page"`
			PageSize int `json:"page_size"`
			Total    int `json:"total"`
		} `json:"pagination"`
	}
	decodeAdminResponse(t, response, &payload)
	if len(payload.Users) != 1 || payload.Users[0].Email != "alice@example.com" ||
		payload.Users[0].RouteAccountID == nil || *payload.Users[0].RouteAccountID != "alpha" ||
		payload.Pagination.Page != 1 || payload.Pagination.PageSize != 25 || payload.Pagination.Total != 1 {
		t.Fatalf("user list payload = %#v", payload)
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load Shanghai timezone: %v", err)
	}
	localNow := fixedNow.In(shanghai)
	expectedStart := time.Date(
		localNow.Year(), localNow.Month(), localNow.Day(),
		0, 0, 0, 0, shanghai,
	).Unix()
	if reader.userSummaryCalls != 1 || reader.userSummaryStartAt != expectedStart {
		t.Fatalf("user summary calls = %d, start=%d", reader.userSummaryCalls, reader.userSummaryStartAt)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("test_external_alice")) ||
		bytes.Contains(response.Body.Bytes(), []byte(`"key":"`)) {
		t.Fatalf("fine-grained user list leaked unrelated data: %s", response.Body.String())
	}

	response = performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/users?view=members&window=today&page=1&page_size=50&q=alice&sort=tokens&direction=desc",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	if response.Code != http.StatusOK || reader.selectedUserSummaryCalls != 1 ||
		strings.Join(reader.selectedUserSummaryUsers, ",") != "alice@example.com" ||
		!strings.Contains(response.Body.String(), `"accounts":{}`) ||
		!strings.Contains(response.Body.String(), `"teams":[]`) {
		t.Fatalf("member catalog = %d %s reader=%#v", response.Code, response.Body.String(), reader)
	}

	response = performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/users?page_size=10",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_page_size")
}

func TestUserDetailAPIIsLazyWindowedAndNeverReturnsRawKey(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	reader := &fakeUsageReader{
		collectorResult: usage.CollectorStatus{Status: "healthy"},
		userSummaries:   map[string]usage.WeightedMetrics{},
		userAccountResult: usage.UserAccountSummary{
			Totals: usage.WeightedMetrics{
				RawMetrics:     usage.RawMetrics{RequestCount: 2, TotalTokens: 700, LastUsedAt: 900},
				WeightedTokens: 850,
			},
			Accounts: []usage.UserAccountUsage{{
				Account: "alpha",
				WeightedMetrics: usage.WeightedMetrics{
					RawMetrics:     usage.RawMetrics{RequestCount: 2, TotalTokens: 700, LastUsedAt: 900},
					WeightedTokens: 850,
				},
			}},
		},
	}
	fixedNow := time.Unix(1_787_500_800, 0)
	server, err := New(Config{
		Store: store, Usage: reader, Users: &fakeUserLifecycle{},
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("New user detail Admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(
		server, http.MethodGet,
		"/admin/api/users?view=summary&window=today&page=1&page_size=50&sort=tokens&direction=desc",
		nil, headers, nil,
	)
	if response.Code != http.StatusOK || reader.userAccountCalls != 0 {
		t.Fatalf("catalog eagerly loaded detail = %d calls=%d body=%s", response.Code, reader.userAccountCalls, response.Body.String())
	}

	response = performAdminRequest(
		server, http.MethodGet,
		"/admin/api/users/detail?email=alice%40example.com&window=today",
		nil, headers, nil,
	)
	if response.Code != http.StatusOK || reader.userAccountCalls != 1 ||
		reader.userAccountEmail != "alice@example.com" ||
		!strings.Contains(response.Body.String(), `"account_count":1`) ||
		!strings.Contains(response.Body.String(), `"weighted_tokens":850`) ||
		!strings.Contains(response.Body.String(), `"preview":"test_external_••••lice"`) ||
		strings.Contains(response.Body.String(), "test_external_alice") {
		t.Fatalf("user detail response = %d %s, reader=%#v", response.Code, response.Body.String(), reader)
	}

	response = performAdminRequest(
		server, http.MethodGet,
		"/admin/api/users/detail?email=missing%40example.com&window=today",
		nil, headers, nil,
	)
	assertAdminError(t, response, http.StatusNotFound, "user_not_found")
	if reader.userAccountCalls != 1 {
		t.Fatal("unknown user reached usage detail reader")
	}

	response = performAdminRequest(server, http.MethodGet, "/admin/api/users/detail", nil, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
}

func TestUserCatalogAndDetailFailClosedWithoutRequiredReaders(t *testing.T) {
	server, _ := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	for _, path := range []string{
		"/admin/api/users?view=summary&window=today&page=1&page_size=50&sort=tokens&direction=desc",
		"/admin/api/users/detail?email=alice%40example.com&window=today",
	} {
		response := performAdminRequest(server, http.MethodGet, path, nil, headers, nil)
		assertAdminError(t, response, http.StatusServiceUnavailable, "usage_not_ready")
	}
}

func TestUserLifecycleGinContractsMatchLegacyRequestsAndReturnSecretsOnce(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	lifecycle := &fakeUserLifecycle{}
	server, err := New(Config{Store: store, Users: lifecycle})
	if err != nil {
		t.Fatalf("New lifecycle Admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodPost, "/admin/api/users", map[string]any{
		"email": "new@example.com",
	}, headers, nil)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "one-time-api-key") ||
		!strings.Contains(response.Body.String(), "one-time-password") ||
		!strings.Contains(response.Body.String(), `"keys"`) ||
		!strings.Contains(response.Body.String(), `"initial_password":"one-time-password"`) ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create user response = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/keys/rotate", map[string]any{
		"email": "new@example.com", "confirm": "wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	if lifecycle.rotateCalls != 0 {
		t.Fatal("rotation ran without exact confirmation")
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/keys/rotate", map[string]any{
		"label": "new@example.com:alpha",
	}, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "rotated-one-time-key") ||
		!strings.Contains(response.Body.String(), `"keys"`) ||
		response.Header().Get("Cache-Control") != "no-store" || lifecycle.rotateCalls != 1 {
		t.Fatalf("rotate user response = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/reset-password", map[string]any{
		"email": "new@example.com",
	}, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"initial_password":"one-time-password"`) ||
		strings.Contains(response.Body.String(), `"password":{`) ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reset password response = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/revoke", map[string]any{
		"email": "new@example.com",
	}, headers, nil)
	if response.Code != http.StatusOK || lifecycle.revokeCalls != 1 ||
		!strings.Contains(response.Body.String(), `"revoked":1`) {
		t.Fatalf("revoke user response = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/delete", map[string]any{
		"email": "new@example.com", "confirm": "NEW@example.com", "revoke_keys": true,
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/delete", map[string]any{
		"email": "new@example.com", "confirm": "new@example.com", "revoke_keys": true,
	}, headers, nil)
	if response.Code != http.StatusOK || lifecycle.deleteCalls != 1 || !lifecycle.deleteRevoke {
		t.Fatalf("delete user response = %d %s, lifecycle=%#v", response.Code, response.Body.String(), lifecycle)
	}
}

func TestUserQuotaGinContractsAreFineGrainedAndValidatePolicy(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	lifecycle := &fakeUserLifecycle{}
	server, err := New(Config{Store: store, Users: lifecycle})
	if err != nil {
		t.Fatalf("New quota Admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(
		server, http.MethodGet, "/admin/api/users/quota?email=new%40example.com", nil, headers, nil,
	)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"policy_mode":"inherit"`) ||
		!strings.Contains(response.Body.String(), `"personal_policy_reset_enabled":true`) ||
		!strings.Contains(response.Body.String(), `"adjustments":[]`) ||
		strings.Contains(response.Body.String(), `"users"`) {
		t.Fatalf("read user quota response = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPut, "/admin/api/users/quota", map[string]any{
		"email": "new@example.com", "mode": "custom", "weekly_tokens": 0,
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	if lifecycle.quotaMode != "" {
		t.Fatal("invalid quota policy reached service")
	}
	response = performAdminRequest(server, http.MethodPut, "/admin/api/users/quota", map[string]any{
		"email": "new@example.com", "mode": "custom", "weekly_tokens": 500,
	}, headers, nil)
	if response.Code != http.StatusOK || lifecycle.quotaMode != "custom" || lifecycle.quotaTokens == nil ||
		*lifecycle.quotaTokens != 500 || !strings.Contains(response.Body.String(), `"limit_tokens":500`) {
		t.Fatalf("update user quota response = %d %s, lifecycle=%#v", response.Code, response.Body.String(), lifecycle)
	}
	response = performAdminRequest(
		server, http.MethodDelete, "/admin/api/users/quota?email=new%40example.com", nil, headers, nil,
	)
	if response.Code != http.StatusOK || lifecycle.quotaMode != "inherit" ||
		!strings.Contains(response.Body.String(), `"policy_mode":"inherit"`) {
		t.Fatalf("clear user quota response = %d %s, lifecycle=%#v", response.Code, response.Body.String(), lifecycle)
	}
}

func TestUserQuotaActionGinContractRequiresExactConfirmationAndBoundsScope(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	lifecycle := &fakeUserLifecycle{}
	server, err := New(Config{Store: store, Users: lifecycle})
	if err != nil {
		t.Fatalf("New quota action Admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/users/quota-actions", nil, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"total_users":2`) ||
		!strings.Contains(response.Body.String(), `"users_with_usage":1`) {
		t.Fatalf("quota operation summary response = %d %s", response.Code, response.Body.String())
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/quota-actions", map[string]any{
		"action": "add_bonus", "scope": "selected", "users": []string{"alice@example.com"},
		"token_amount": "200", "reason": "temporary capacity", "confirm": "wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	if lifecycle.quotaActionCalls != 0 {
		t.Fatal("quota action ran without exact confirmation")
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/quota-actions", map[string]any{
		"action": "add_bonus", "scope": "selected",
		"users":        []string{"Alice@Example.com", "alice@example.com"},
		"token_amount": "200", "reason": "temporary capacity", "confirm": "add_bonus",
	}, headers, nil)
	if response.Code != http.StatusOK || lifecycle.quotaActionCalls != 1 ||
		lifecycle.quotaAction.TokenAmount != 200 || lifecycle.quotaAction.Scope != "selected" ||
		!strings.Contains(response.Body.String(), `"quota_operations"`) {
		t.Fatalf("quota action response = %d %s, lifecycle=%#v", response.Code, response.Body.String(), lifecycle)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/quota-actions", map[string]any{
		"action": "restore_default", "scope": "all", "confirm": "restore_default",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	if lifecycle.quotaActionCalls != 1 {
		t.Fatal("unsupported all-user restore reached quota action service")
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/users/quota-actions", map[string]any{
		"action": "reset_usage", "scope": "all", "reason": "incident correction",
		"confirm": "reset_all_current_week_usage",
	}, headers, nil)
	if response.Code != http.StatusOK || lifecycle.quotaActionCalls != 2 ||
		lifecycle.quotaAction.Scope != "all" {
		t.Fatalf("all-user reset response = %d %s, lifecycle=%#v", response.Code, response.Body.String(), lifecycle)
	}
}

func TestBrandingLogoIsPublicAndDoesNotExposeMissingAsset(t *testing.T) {
	server, store := newTestAdmin(t)
	response := performAdminRequest(server, http.MethodGet, "/branding/logo", nil, nil, nil)
	assertAdminError(t, response, http.StatusNotFound, "not_found")

	asset, err := store.WriteBrandingAsset(
		context.Background(),
		"logo",
		"logo.png",
		"image/png",
		[]byte("test-logo"),
	)
	if err != nil {
		t.Fatalf("WriteBrandingAsset: %v", err)
	}
	response = performAdminRequest(server, http.MethodGet, "/branding/logo", nil, nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "test-logo" {
		t.Fatalf("branding response = (%d, %q)", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("branding Content-Type = %q", response.Header().Get("Content-Type"))
	}
	if response.Header().Get("ETag") != `"`+asset.SHA256+`"` {
		t.Fatalf("branding ETag = %q", response.Header().Get("ETag"))
	}
}

func TestBrandingMutationValidatesContentAndSupportsReset(t *testing.T) {
	server, _ := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	unsafeSVG := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/logo", map[string]any{
		"filename":     "unsafe.svg",
		"content_type": "image/svg+xml",
		"data_base64":  base64.StdEncoding.EncodeToString(unsafeSVG),
		"confirm":      "save",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_logo")

	safeSVG := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle cx="5" cy="5" r="5"/></svg>`)
	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/logo", map[string]any{
		"filename":     "Company Logo.svg",
		"content_type": "image/svg+xml",
		"data_base64":  base64.StdEncoding.EncodeToString(safeSVG),
		"confirm":      "save",
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("save branding status = %d, body = %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodGet, "/branding/logo", nil, nil, nil)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), safeSVG) {
		t.Fatalf("saved branding response = (%d, %q)", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodDelete, "/admin/api/settings/logo", map[string]any{
		"confirm": "reset",
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("reset branding status = %d, body = %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodGet, "/branding/logo", nil, nil, nil)
	assertAdminError(t, response, http.StatusNotFound, "not_found")
}

func TestAdminRejectsOversizedJSONBody(t *testing.T) {
	server, _ := newTestAdmin(t)
	body := []byte(`{"name":"` + strings.Repeat("x", int(defaultBodyLimit)) + `"}`)
	response := performRawAdminRequest(server, http.MethodPost, "/admin/api/teams", body, map[string]string{
		"Content-Type":     "application/json",
		"X-Management-Key": "test-management-key",
	}, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
}

func TestGlobalAccountRebalanceRequiresConfirmationAndReturnsRefreshedActivity(t *testing.T) {
	rebalancer := &fakeAdminRebalancer{result: failover.RebalanceResult{
		MovedUsers:         3,
		Destinations:       map[string]int{"beta": 2, "gamma": 1},
		TargetCounts:       map[string]int{"alpha": 2, "beta": 2, "gamma": 1},
		SnapshotGeneration: "generation-test",
		ActiveUsers1H:      map[string]int{"alpha": 2, "beta": 2, "gamma": 1},
		ActivityRefreshed:  true,
	}}
	server, _ := newTestAdminWithRebalancer(t, rebalancer)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/rebalance-all", map[string]any{
		"confirm": "wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	if rebalancer.calls != 0 {
		t.Fatal("rebalancer was called without exact confirmation")
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/rebalance-all", map[string]any{
		"confirm": "rebalance-all",
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("global rebalance status = %d, body = %s", response.Code, response.Body.String())
	}
	if rebalancer.calls != 1 || !bytes.Contains(response.Body.Bytes(), []byte(`"activity_refreshed":true`)) {
		t.Fatalf("global rebalance response = %s, calls = %d", response.Body.String(), rebalancer.calls)
	}
}

func TestSingleAccountRebalanceRequiresExactConfirmationAndReturnsRefreshedActivity(t *testing.T) {
	rebalancer := &fakeAdminRebalancer{evacuationResult: failover.EvacuationResult{
		RebalanceResult: failover.RebalanceResult{
			MovedUsers: 1, Destinations: map[string]int{"beta": 1},
			SnapshotGeneration: "generation-test", ActiveUsers1H: map[string]int{"alpha": 0, "beta": 1},
			ActivityRefreshed: true,
		},
		Plan: failover.PlanSummary{
			Sources: map[string]int{"alpha": 1}, Destinations: map[string]int{"beta": 1},
			AffectedUsers: 1, PlannedUsers: 1,
		},
	}}
	server, _ := newTestAdminWithRebalancer(t, rebalancer)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/rebalance", map[string]any{
		"id": "alpha", "confirm": "wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_confirmation")
	if rebalancer.evacuateCalls != 0 {
		t.Fatal("single-account rebalancer was called without exact confirmation")
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/rebalance", map[string]any{
		"id": "missing", "confirm": "missing",
	}, headers, nil)
	assertAdminError(t, response, http.StatusNotFound, "account_not_found")
	if rebalancer.evacuateCalls != 0 {
		t.Fatal("single-account rebalancer was called for an unknown account")
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/rebalance", map[string]any{
		"id": "alpha", "confirm": "alpha",
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("single-account rebalance status = %d, body = %s", response.Code, response.Body.String())
	}
	if rebalancer.evacuateCalls != 1 || rebalancer.evacuatedAccount != "alpha" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"activity_refreshed":true`)) {
		t.Fatalf("single-account rebalance response = %s, rebalancer = %#v", response.Body.String(), rebalancer)
	}
}

func TestAccountQuotaResetRequiresExactConfirmationAndMapsSafeResult(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	expiresAt := int64(1_786_579_200)
	resetter := &fakeQuotaResetter{
		inspection: quota.ResetInspection{
			Account: "alpha", AvailableCount: func() *int64 { value := int64(2); return &value }(),
			Windows: []quota.ResetWindow{{Key: "default:primary_window", Label: "常规周限额"}},
			Credits: []quota.ResetCreditOption{{ID: "credit-selected", Title: "Full reset", ExpiresAt: &expiresAt}},
		},
		result: quota.ResetResult{
			Account: "alpha", WindowsReset: 1, Code: "rate_limit_reset_credit_consumed",
			Windows: []quota.ResetWindow{{Key: "default:primary_window", Label: "常规周限额"}},
			Credit:  quota.ResetCreditResult{Title: "Full reset", ExpiresAt: &expiresAt, Status: "redeemed"},
		}}
	server, err := New(Config{Store: store, QuotaResetter: resetter})
	if err != nil {
		t.Fatalf("New quota reset admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodGet, "/admin/api/accounts/quota-reset?account=alpha", nil, headers, nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"available_count":2`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"credit-selected"`)) || resetter.inspectCalls != 1 {
		t.Fatalf("quota reset inspection = %d, %s, resetter = %#v", response.Code, response.Body.String(), resetter)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/reset-quota", map[string]any{
		"account": "alpha", "credit_id": "credit-selected", "confirm": "wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_confirmation")
	if resetter.calls != 0 {
		t.Fatal("quota resetter was called without exact confirmation")
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/reset-quota", map[string]any{
		"account": "alpha", "credit_id": "credit-selected", "confirm": "alpha",
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("quota reset status = %d, body = %s", response.Code, response.Body.String())
	}
	if resetter.calls != 1 || resetter.account != "alpha" || resetter.creditID != "credit-selected" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"windows_reset":1`)) ||
		bytes.Contains(response.Body.Bytes(), []byte("credit-selected")) {
		t.Fatalf("quota reset response = %s, resetter = %#v", response.Body.String(), resetter)
	}
}

func TestAccountQuotaResetMapsChangedCreditAndOAuthFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "changed credit", err: quota.ErrResetCreditChanged, code: "quota_reset_credit_changed"},
		{name: "unavailable", err: quota.ErrResetUnavailable, code: "quota_reset_unavailable"},
		{name: "missing auth", err: quota.ErrOAuthMissing, code: "quota_auth_missing"},
		{name: "expired auth", err: quota.ErrAuthExpired, code: "quota_auth_expired"},
		{name: "rejected", err: quota.ErrResetRejected, code: "quota_reset_rejected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, store := newTestAdmin(t)
			base.Close()
			server, err := New(Config{Store: store, QuotaResetter: &fakeQuotaResetter{err: test.err}})
			if err != nil {
				t.Fatalf("New quota reset admin: %v", err)
			}
			t.Cleanup(server.Close)
			response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/reset-quota", map[string]any{
				"account": "alpha", "credit_id": "credit-selected", "confirm": "alpha",
			}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
			assertAdminError(t, response, http.StatusConflict, test.code)
		})
	}
}

func TestAccountLifecycleCompatibilityRoutesEnforceConfirmationsAndReturnSecretFreeResults(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	service := &fakeAccountLifecycle{}
	server, err := New(Config{Store: store, AccountLifecycle: service})
	if err != nil {
		t.Fatalf("New account lifecycle server: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts", map[string]any{
		"id": "gamma", "email": "gamma@accounts.example.com", "proxy_mode": "custom",
		"proxy_url": "socks5://user:password@127.0.0.1:1080",
	}, headers, nil)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "password") ||
		service.createRequest.ProxyURL == "" {
		t.Fatalf("create account response = %d %s request=%#v", response.Code, response.Body.String(), service.createRequest)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/update", map[string]any{
		"id": "alpha", "new_id": "gamma", "email": "gamma@accounts.example.com",
		"confirm": "wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_confirmation")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/update", map[string]any{
		"id": "alpha", "new_id": "gamma", "email": "gamma@accounts.example.com",
		"confirm": "alpha", "proxy_mode": "direct",
	}, headers, nil)
	if response.Code != http.StatusOK || service.updateRequest.NewAccountID != "gamma" {
		t.Fatalf("update account response = %d %s request=%#v", response.Code, response.Body.String(), service.updateRequest)
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/update", map[string]any{
		"id": "alpha", "new_id": "alpha", "email": "alpha@accounts.example.com",
		"confirm": "", "proxy_mode": "custom", "proxy_url": "",
	}, headers, nil)
	if response.Code != http.StatusOK || service.updateRequest.ProxyURL != nil {
		t.Fatalf("legacy empty proxy must retain encrypted value: %d %s request=%#v", response.Code, response.Body.String(), service.updateRequest)
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/policy", map[string]any{
		"id": "alpha", "group_enabled": false, "default_group": false, "fallback_account": "beta",
	}, headers, nil)
	if response.Code != http.StatusOK || service.updateRequest.Enabled == nil || *service.updateRequest.Enabled ||
		service.updateRequest.FallbackAccount != "beta" || !service.updateRequest.PolicyOnly {
		t.Fatalf("update account policy response = %d %s request=%#v", response.Code, response.Body.String(), service.updateRequest)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/repair-proxy", map[string]any{
		"id": "alpha", "proxy_url": "http://proxy-user:proxy-password@proxy.example.com:16169",
		"confirm": "repair-proxy:wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_confirmation")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/repair-proxy", map[string]any{
		"id": "alpha", "proxy_url": "http://proxy-user:proxy-password@proxy.example.com:16169",
		"confirm": "repair-proxy:alpha",
	}, headers, nil)
	if response.Code != http.StatusOK || !service.updateRequest.AllowUnavailableProxyRepair ||
		service.updateRequest.ProxyMode != "custom" || service.updateRequest.ProxyURL == nil ||
		strings.Contains(response.Body.String(), "proxy-password") {
		t.Fatalf("repair proxy response = %d %s request=%#v", response.Code, response.Body.String(), service.updateRequest)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/clear-auth", map[string]any{
		"id": "alpha", "confirm": "beta",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_confirmation")
	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/clear-auth", map[string]any{
		"id": "alpha", "confirm": "alpha",
	}, headers, nil)
	if response.Code != http.StatusOK || service.clearAccount != "alpha" {
		t.Fatalf("clear auth response = %d %s", response.Code, response.Body.String())
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/accounts/delete", map[string]any{
		"id": "alpha", "confirm": "alpha", "revoke_keys": true, "fallback_account": "beta",
	}, headers, nil)
	if response.Code != http.StatusOK || !service.deleteRequest.RevokeExclusive ||
		service.deleteRequest.FallbackAccount != "beta" {
		t.Fatalf("delete account response = %d %s request=%#v", response.Code, response.Body.String(), service.deleteRequest)
	}
}

func TestAccountLifecycleMapsSafetyFailuresWithoutLeakingInternalError(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	service := &fakeAccountLifecycle{err: controlplane.ErrAccountDeleteRequiresRevoke}
	server, err := New(Config{Store: store, AccountLifecycle: service})
	if err != nil {
		t.Fatalf("New account lifecycle server: %v", err)
	}
	t.Cleanup(server.Close)
	response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/delete", map[string]any{
		"id": "alpha", "confirm": "alpha", "revoke_keys": false,
	}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	assertAdminError(t, response, http.StatusConflict, "account_revoke_required")
}

func TestAccountLifecycleMapsDrainAndRecoveryFailuresToStableCodes(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "active requests", err: accountlifecycle.ErrAccountDrainTimeout, status: http.StatusConflict, code: "account_requests_active"},
		{name: "recovery required", err: accountlifecycle.ErrLifecycleRecoveryRequired, status: http.StatusServiceUnavailable, code: "account_lifecycle_not_ready"},
		{name: "proxy repair rejected", err: accountlifecycle.ErrUnavailableProxyRepairRejected, status: http.StatusConflict, code: "account_proxy_repair_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, store := newTestAdmin(t)
			base.Close()
			service := &fakeAccountLifecycle{err: test.err}
			server, err := New(Config{Store: store, AccountLifecycle: service})
			if err != nil {
				t.Fatalf("New account lifecycle server: %v", err)
			}
			t.Cleanup(server.Close)
			response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/delete", map[string]any{
				"id": "alpha", "confirm": "alpha", "revoke_keys": true, "fallback_account": "beta",
			}, map[string]string{"X-Management-Key": "test-management-key"}, nil)
			assertAdminError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), test.err.Error()) {
				t.Fatalf("safe error response leaked internal cause: %s", response.Body.String())
			}
		})
	}
}

func TestOverviewSummaryIsBoundedAndNeverReturnsIdentitiesOrSecrets(t *testing.T) {
	server, store := newTestAdmin(t)
	if err := store.WriteAccounts(context.Background(), []controlplane.Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, GroupEnabled: true, DefaultGroup: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("write overview accounts: %v", err)
	}
	if err := store.WriteRoutes(context.Background(), map[string]string{"alice@example.com": "alpha"}); err != nil {
		t.Fatalf("write overview routes: %v", err)
	}

	response := performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/overview/summary",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("overview summary status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Source  string                       `json:"source"`
		Summary controlplane.OverviewSummary `json:"summary"`
	}
	decodeAdminResponse(t, response, &payload)
	if payload.Source != "control-plane" || payload.Summary.Accounts != 2 ||
		payload.Summary.Users != 1 || payload.Summary.ActiveKeys != 1 ||
		payload.Summary.RoutedUsers != 1 || payload.Summary.IncompleteMatrices != 1 {
		t.Fatalf("overview summary = %#v", payload)
	}
	for _, forbidden := range []string{
		"alice@example.com", "alpha@accounts.example.com", "test_external_alice", "test-management-key",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("overview summary leaks %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestOverviewCatalogUsesTheAccountPageOperationalStatusContract(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, GroupEnabled: true, DefaultGroup: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, GroupEnabled: false},
	}); err != nil {
		t.Fatalf("write overview catalog accounts: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []controlplane.KeyRecord{
		{Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "alice@example.com", Status: "active", Key: "test_external_alice", CreatedAt: 100, UpdatedAt: 100},
		{Label: "bob@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "bob@example.com", Status: "revoked", Key: "test_external_bob", CreatedAt: 100, UpdatedAt: 100},
	}); err != nil {
		t.Fatalf("write overview catalog users: %v", err)
	}
	remaining := 80.0
	if err := store.WriteRuntimeState(ctx, quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{
			{Account: "alpha", Status: "ok", Weekly: &quota.WeeklyWindow{RemainingPercent: remaining}},
			{Account: "beta", Status: "ok", Weekly: &quota.WeeklyWindow{RemainingPercent: remaining}},
		}},
	}); err != nil {
		t.Fatalf("write overview catalog quota state: %v", err)
	}
	server, err := New(Config{
		Store: store,
		AccountStates: staticAdminAccountStates{states: map[string]failover.AccountState{
			"alpha": {Account: "alpha", Eligible: true, Reason: "available", RemainingPercent: &remaining},
			"beta":  {Account: "beta", Eligible: true, RemainingPercent: &remaining},
		}},
		AccountRuntime: staticAdminAccountRuntime{states: map[string]accountstatus.State{
			"alpha": {AuthFiles: 1, Runtime: accountstatus.Runtime{State: "available"}},
			"beta":  {AuthFiles: 1, Runtime: accountstatus.Runtime{State: "available"}},
		}},
		OAuth: mappedAdminOAuth{configured: map[string]bool{"alpha": true, "beta": true}},
		Runtime: &fakeRuntimeCatalog{services: []runtimeops.Service{
			{Service: "cliproxy-alpha", State: "running"},
			{Service: "cliproxy-beta", State: "running"},
		}},
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("New overview catalog Admin: %v", err)
	}
	t.Cleanup(server.Close)
	response := performAdminRequest(server, http.MethodGet, "/admin/api/overview/catalog", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("overview catalog = %d %s", response.Code, response.Body.String())
	}
	var payload overviewCatalogResponse
	decodeAdminResponse(t, response, &payload)
	if payload.GeneratedAt != 1_800_000_000 || len(payload.Accounts) != 2 || len(payload.Users) != 2 {
		t.Fatalf("overview catalog shape = %#v", payload)
	}
	if payload.Accounts[0].ID != "alpha" || payload.Accounts[0].OperationalStatus.Code != "available" ||
		payload.Accounts[1].ID != "beta" || payload.Accounts[1].OperationalStatus.Code != "disabled" {
		t.Fatalf("overview operational statuses = %#v", payload.Accounts)
	}
	if payload.Users[0] != (overviewCatalogUser{Email: "alice@example.com", Status: "active"}) ||
		payload.Users[1] != (overviewCatalogUser{Email: "bob@example.com", Status: "inactive"}) {
		t.Fatalf("overview user statuses = %#v", payload.Users)
	}
	for _, forbidden := range []string{"alpha@accounts.example.com", "test_external_alice", "test-management-key"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("overview catalog leaks %q: %s", forbidden, response.Body.String())
		}
	}

	unauthorized := performAdminRequest(server, http.MethodGet, "/admin/api/overview/catalog", nil, nil, nil)
	assertAdminError(t, unauthorized, http.StatusUnauthorized, "session_missing")
}

func TestOverviewStatusUsesLiveOAuthRuntimeAndFiveMinuteUsage(t *testing.T) {
	base, store := newTestAdmin(t)
	base.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, GroupEnabled: true, DefaultGroup: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("write overview status accounts: %v", err)
	}
	if err := store.WriteRuntimeState(ctx, quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{
			{Account: "alpha", Status: "ok", Weekly: &quota.WeeklyWindow{UsedPercent: 20}},
			{Account: "beta", Status: "ok", Weekly: &quota.WeeklyWindow{UsedPercent: 70}},
		}},
	}); err != nil {
		t.Fatalf("write overview status quota state: %v", err)
	}
	usageReader := &fakeUsageReader{publicResult: map[string]usage.PublicAccountUsage{
		"alpha": {Account: "alpha", RequestCount: 41},
		"beta":  {Account: "beta", RequestCount: 6},
	}}
	server, err := New(Config{
		Store: store,
		OAuth: mappedAdminOAuth{configured: map[string]bool{"alpha": true}},
		Runtime: &fakeRuntimeCatalog{services: []runtimeops.Service{
			{Service: "gateway", State: "running"},
			{Service: "admin", State: "running"},
			{Service: "usage-collector", State: "exited"},
			{Service: "cliproxy-alpha", State: "running"},
		}},
		Usage: usageReader,
		Now:   func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("New overview status Admin: %v", err)
	}
	t.Cleanup(server.Close)
	response := performAdminRequest(server, http.MethodGet, "/admin/api/overview/status", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("overview status = %d %s", response.Code, response.Body.String())
	}
	var payload overviewStatusResponse
	decodeAdminResponse(t, response, &payload)
	if payload.GeneratedAt != 1_800_000_000 || payload.AuthorizedAccounts != 1 ||
		payload.RunningServices != 3 || payload.TotalServices != 4 || payload.Requests5M != 47 ||
		len(payload.Warnings) != 0 {
		t.Fatalf("overview status payload = %#v", payload)
	}
	assertOverviewAccountQuotaSummary(t, payload.AccountQuota, overviewAccountQuotaSummary{
		Available: true, EnabledAccounts: 2, KnownAccounts: 2,
		AverageUsedPercent: float64Pointer(45), AverageRemainingPercent: float64Pointer(55),
		EquivalentRemainingAccounts: 1.1,
	})
	if usageReader.publicCalls != 1 || usageReader.publicStartAt != 1_799_999_700 ||
		usageReader.publicEndAt != 1_800_000_000 ||
		!slices.Equal(usageReader.publicAccounts, []string{"alpha", "beta"}) {
		t.Fatalf("overview public usage request = %#v", usageReader)
	}
}

func TestOverviewCatalogAndStatusFailClosedWhenLiveDependenciesAreMissing(t *testing.T) {
	server, _ := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	for _, path := range []string{"/admin/api/overview/catalog", "/admin/api/overview/status"} {
		response := performAdminRequest(server, http.MethodGet, path, nil, headers, nil)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s missing dependencies = %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestGeneralSettingsAPIUsesLiveAllowlistAndPreservesUnrelatedSettings(t *testing.T) {
	server, store := newTestAdmin(t)
	ctx := context.Background()
	if err := store.WriteSettings(ctx, map[string]any{
		"branding.product_name": "Existing CPA",
		"notification.enabled":  true,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := store.WriteSecret(ctx, "portal_initial_password", "must-not-leak"); err != nil {
		t.Fatalf("write initial password: %v", err)
	}
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/general", nil, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("general settings status = %d, body = %s", response.Code, response.Body.String())
	}
	var current generalSettingsResponse
	decodeAdminResponse(t, response, &current)
	if current.Values.ProductName != "Existing CPA" || current.Values.KeyPrefix != "ccpa_" ||
		!current.Security.ManagementKeyConfigured || !current.Security.InitialPasswordConfigured ||
		current.ApplyMode != "live" {
		t.Fatalf("general settings = %#v", current)
	}
	if strings.Contains(response.Body.String(), "must-not-leak") || strings.Contains(response.Body.String(), "test-management-key") {
		t.Fatalf("general settings leak secret material: %s", response.Body.String())
	}

	response = performAdminRequest(server, http.MethodPut, "/admin/api/settings/general", map[string]any{
		"confirm": "save",
		"values": map[string]any{
			"product_name":          "CPA Control",
			"short_name":            "CPA",
			"environment_label":     "Internal",
			"public_base_url":       "https://cpa.example.com/path",
			"allowed_email_domains": []string{"Example.com"},
			"key_prefix":            "cpa_",
			"provider_name":         "CPA Provider",
			"api_key_env":           "CCPA_API_KEY",
			"default_model":         "gpt-test",
		},
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_settings")

	response = performAdminRequest(server, http.MethodPut, "/admin/api/settings/general", map[string]any{
		"confirm": "save",
		"values": map[string]any{
			"product_name":          "CPA Control",
			"short_name":            "CPA",
			"environment_label":     "Internal",
			"public_base_url":       "https://cpa.example.com/",
			"allowed_email_domains": []string{"Example.com", "@example.com", "example.org"},
			"key_prefix":            "custom_",
			"provider_name":         "CPA Provider",
			"api_key_env":           "CUSTOM_API_KEY",
			"default_model":         "gpt-test",
		},
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("save general settings = %d, body = %s", response.Code, response.Body.String())
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil {
		t.Fatalf("read saved settings: %v", err)
	}
	domains, ok := settings["identity.allowed_email_domains"].([]any)
	if !ok || len(domains) != 2 || settings["branding.public_base_url"] != "https://cpa.example.com" ||
		settings["identity.key_prefix"] != "custom_" || settings["notification.enabled"] != true {
		t.Fatalf("saved settings = %#v", settings)
	}
}

func TestPublicSiteConfigurationUsesExplicitSafeAllowlist(t *testing.T) {
	server, store := newTestAdmin(t)
	ctx := context.Background()
	if err := store.WriteSettings(ctx, map[string]any{
		"branding.product_name":          "Public CPA",
		"branding.short_name":            "P-CPA",
		"branding.environment_label":     "Test only",
		"branding.public_base_url":       "https://cpa.example.com",
		"identity.allowed_email_domains": []string{"Example.com", "staff.example.com"},
		"identity.key_prefix":            "private_",
		"portal.provider_name":           "Public Provider",
		"portal.api_key_env":             "PUBLIC_API_KEY",
		"portal.default_model":           "gpt-public",
		"unrelated.private_setting":      "do-not-leak",
	}); err != nil {
		t.Fatalf("write public settings: %v", err)
	}

	response := performAdminRequest(server, http.MethodGet, "/site-config.json", nil, nil, nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public site configuration = %d %s %#v", response.Code, response.Body.String(), response.Header())
	}
	var configuration publicSiteConfiguration
	decodeAdminResponse(t, response, &configuration)
	if configuration.ProductName != "Public CPA" || configuration.PublicBaseURL != "https://cpa.example.com" ||
		configuration.ProviderName != "Public Provider" || configuration.Logo.Custom ||
		configuration.Logo.URL != defaultPortalLogoURL || configuration.Logo.UpdatedAt != nil ||
		!slices.Equal(configuration.AllowedEmailDomains, []string{"example.com", "staff.example.com"}) {
		t.Fatalf("public site configuration = %#v", configuration)
	}
	for _, forbidden := range []string{"private_", "key_prefix", "do-not-leak", "private_setting"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("public site configuration leaks %q: %s", forbidden, response.Body.String())
		}
	}

	asset, err := store.WriteBrandingAsset(ctx, "logo", "custom.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	if err != nil {
		t.Fatalf("write public logo: %v", err)
	}
	response = performAdminRequest(server, http.MethodGet, "/site-config.json", nil, nil, nil)
	decodeAdminResponse(t, response, &configuration)
	if !configuration.Logo.Custom || configuration.Logo.URL != "/branding/logo" ||
		configuration.Logo.SHA256 != asset.SHA256 || configuration.Logo.UpdatedAt == nil ||
		*configuration.Logo.UpdatedAt != asset.UpdatedAt {
		t.Fatalf("custom public site logo = %#v", configuration.Logo)
	}
}

func TestNativeAccountsRequireAdminAndExposeLocalURLOnlyForLoopbackHost(t *testing.T) {
	server, store := newTestAdmin(t)
	if err := store.UpdateSettings(context.Background(), map[string]any{
		"accounts.listen_address": "::1",
	}); err != nil {
		t.Fatalf("write native listen address: %v", err)
	}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/native-accounts", nil, nil, nil)
	assertAdminError(t, response, http.StatusUnauthorized, "session_missing")

	headers := map[string]string{"X-Management-Key": "test-management-key", "Host": "cpa.example.com"}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/native-accounts", nil, headers, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "management_url") ||
		strings.Contains(response.Body.String(), "18318") || strings.Contains(response.Body.String(), "accounts.example.com") {
		t.Fatalf("public-host native accounts = %d %s", response.Code, response.Body.String())
	}

	headers["Host"] = "[::1]:8317"
	response = performAdminRequest(server, http.MethodGet, "/admin/api/native-accounts", nil, headers, nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("loopback native accounts = %d %s %#v", response.Code, response.Body.String(), response.Header())
	}
	var catalog nativeAccountCatalog
	decodeAdminResponse(t, response, &catalog)
	if len(catalog.Accounts) != 1 || catalog.Accounts[0].ID != "alpha" || !catalog.Accounts[0].GroupEnabled ||
		catalog.Accounts[0].ManagementURL != "http://[::1]:18318/management.html" {
		t.Fatalf("loopback native accounts = %#v", catalog)
	}
	if strings.Contains(response.Body.String(), "port") || strings.Contains(response.Body.String(), "accounts.example.com") {
		t.Fatalf("native accounts leak internal fields: %s", response.Body.String())
	}

	for _, malformedHost := range []string{"127.0.0.1:invalid", "127.0.0.1:0", "::1"} {
		headers["Host"] = malformedHost
		response = performAdminRequest(server, http.MethodGet, "/admin/api/native-accounts", nil, headers, nil)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "management_url") {
			t.Fatalf("malformed loopback Host %q exposed a management URL: %d %s", malformedHost, response.Code, response.Body.String())
		}
	}
}

func TestAccountCatalogReturnsOnlyAccountScopedLiveData(t *testing.T) {
	server, store := newTestAdmin(t)
	resetCount := int64(1)
	resetAt := int64(200)
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: "alpha", ResetCreditCount: &resetCount,
			WeeklyWindows: []quota.WeeklyWindow{{Label: "常规周限额", ResetAt: &resetAt, Resettable: true}},
		}}},
	}); err != nil {
		t.Fatalf("write quota state: %v", err)
	}
	if err := store.WriteRoutes(context.Background(), map[string]string{
		"alice@example.com": "alpha",
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}
	remaining := 84.0
	server.accountStates = staticAdminAccountStates{states: map[string]failover.AccountState{
		"alpha": {
			Account: "alpha", Eligible: true, Reason: "available",
			Headroom: 79, RemainingPercent: &remaining, ObservedAt: 100,
		},
	}}
	server.activity = staticAdminActivity{
		counts: map[string]int{"alpha": 1},
		emails: map[string][]string{"alpha": {"alice@example.com"}},
	}
	server.usage = &fakeUsageReader{accountSummaries: map[string]usage.AccountUsageSummary{
		"alpha": {
			Account: "alpha", ActiveUsers: 1,
			RawMetrics: usage.RawMetrics{RequestCount: 3, FailedCount: 1, TotalTokens: 120, LastUsedAt: 90},
		},
	}}
	response := performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/accounts",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("account catalog status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Accounts                []accountListItem `json:"accounts"`
		ActiveUserWindowSeconds int64             `json:"active_user_window_seconds"`
		Warnings                []string          `json:"warnings"`
	}
	decodeAdminResponse(t, response, &payload)
	if len(payload.Accounts) != 1 || payload.Accounts[0].RoutedUsers != 1 ||
		payload.ActiveUserWindowSeconds != int64(15*time.Minute/time.Second) ||
		payload.Accounts[0].ActiveUsers1H == nil || *payload.Accounts[0].ActiveUsers1H != 1 ||
		len(payload.Accounts[0].ActiveEmails1H) != 1 || payload.Accounts[0].ActiveEmails1H[0] != "alice@example.com" ||
		payload.Accounts[0].ResetCreditCount == nil || *payload.Accounts[0].ResetCreditCount != 1 ||
		!payload.Accounts[0].Resettable || len(payload.Accounts[0].ResetWindowLabels) != 1 ||
		!payload.Accounts[0].StateAvailable || !payload.Accounts[0].UsageAvailable ||
		payload.Accounts[0].Usage.TotalTokens != 120 || payload.Accounts[0].AssociatedUsers != 1 ||
		len(payload.Warnings) != 0 {
		t.Fatalf("account catalog = %#v", payload)
	}
}

func TestAccountCatalogFreshRequestsQuotaWorkerAndReturnsLegacyRefreshMetadata(t *testing.T) {
	server, store := newTestAdmin(t)
	server.now = func() time.Time { return time.Unix(100, 0) }
	server.usage = &fakeUsageReader{collectorResult: usage.CollectorStatus{
		Status: "healthy", HeartbeatAt: 99, EventCount: 3, CollectionStartedAt: 10,
	}}
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{
			GeneratedAt: 50, CacheTTLSeconds: 60,
			Accounts: []quota.AccountQuota{{Account: "alpha", Status: "ok", WeeklyWindows: []quota.WeeklyWindow{}}},
		},
	}); err != nil {
		t.Fatalf("write quota state: %v", err)
	}
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(
		server, http.MethodGet, "/admin/api/accounts?window=today&fresh=1", nil, headers, nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("fresh account catalog = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		QuotaGeneratedAt     *int64                `json:"quota_generated_at"`
		QuotaCached          bool                  `json:"quota_cached"`
		QuotaRefreshing      bool                  `json:"quota_refreshing"`
		QuotaCacheTTLSeconds int64                 `json:"quota_cache_ttl_seconds"`
		Collector            usage.CollectorStatus `json:"collector"`
	}
	decodeAdminResponse(t, response, &payload)
	if payload.QuotaGeneratedAt == nil || *payload.QuotaGeneratedAt != 50 || !payload.QuotaCached ||
		!payload.QuotaRefreshing || payload.QuotaCacheTTLSeconds != 60 || payload.Collector.Status != "healthy" {
		t.Fatalf("fresh account metadata = %#v", payload)
	}
	request, found, err := quota.ReadRefreshRequest(context.Background(), store)
	if err != nil || !found || !request.Pending() {
		t.Fatalf("quota refresh request = (%#v, %v, %v)", request, found, err)
	}

	response = performAdminRequest(
		server, http.MethodGet, "/admin/api/accounts?window=today&fresh=1", nil, headers, nil,
	)
	var duplicate quota.RefreshRequestState
	if _, err := store.ReadRuntimeState(context.Background(), quota.RefreshRequestStateName, &duplicate); err != nil ||
		duplicate.RequestID != request.RequestID {
		t.Fatalf("duplicate fresh request = (%#v, %v)", duplicate, err)
	}

	if err := quota.MarkRefreshCompleted(context.Background(), store, request.RequestID, time.Unix(101, 0), nil); err != nil {
		t.Fatalf("complete quota refresh request: %v", err)
	}
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{
			GeneratedAt: 100, CacheTTLSeconds: 60,
			Accounts: []quota.AccountQuota{{Account: "alpha", Status: "ok", WeeklyWindows: []quota.WeeklyWindow{}}},
		},
	}); err != nil {
		t.Fatalf("write refreshed quota state: %v", err)
	}
	server.now = func() time.Time { return time.Unix(110, 0) }
	response = performAdminRequest(
		server, http.MethodGet, "/admin/api/accounts?window=today&fresh=1", nil, headers, nil,
	)
	decodeAdminResponse(t, response, &payload)
	if payload.QuotaGeneratedAt == nil || *payload.QuotaGeneratedAt != 100 || payload.QuotaRefreshing {
		t.Fatalf("throttled fresh account metadata = %#v", payload)
	}
	var throttled quota.RefreshRequestState
	if _, err := store.ReadRuntimeState(context.Background(), quota.RefreshRequestStateName, &throttled); err != nil ||
		throttled.RequestID != request.RequestID {
		t.Fatalf("throttled fresh request changed state = (%#v, %v)", throttled, err)
	}
}

func TestAccountCatalogPreservesLegacyContainerRuntimeAndOperationalStatusFacts(t *testing.T) {
	server, store := newTestAdmin(t)
	allowed := true
	resetAt := int64(200)
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: "alpha", Status: "ok", Allowed: &allowed,
			Weekly: &quota.WeeklyWindow{RemainingPercent: 84, ResetAt: &resetAt},
		}}},
	}); err != nil {
		t.Fatalf("write quota state: %v", err)
	}
	server.runtime = &fakeRuntimeCatalog{services: []runtimeops.Service{{
		Service: "cliproxy-alpha", State: "running", Status: "Up 8 days (healthy)", Health: "healthy",
	}}}
	server.oauth = staticAdminOAuth{}
	server.accountRuntime = staticAdminAccountRuntime{states: map[string]accountstatus.State{
		"alpha": {
			Reason: accountstatus.ReasonDegraded, AuthFiles: 1,
			Runtime: accountstatus.Runtime{
				State: "degraded", QueryStatus: "ok", CredentialStatus: "active", CredentialCount: 1,
				ErrorCount: 1, ErrorLogStatus: "ok", ErrorLogFiles: 2,
			},
		},
	}}
	remaining := 84.0
	server.accountStates = staticAdminAccountStates{states: map[string]failover.AccountState{
		"alpha": {
			Account: "alpha", Eligible: true, Reason: accountstatus.ReasonDegraded,
			Headroom: 79, RemainingPercent: &remaining, ObservedAt: 100,
		},
	}}
	response := performAdminRequest(
		server, http.MethodGet, "/admin/api/accounts", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("account catalog status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Accounts []accountListItem `json:"accounts"`
	}
	decodeAdminResponse(t, response, &payload)
	if len(payload.Accounts) != 1 {
		t.Fatalf("account catalog = %#v", payload)
	}
	account := payload.Accounts[0]
	if account.Service != "cliproxy-alpha" || account.ContainerState != "running" ||
		account.ContainerStatus != "Up 8 days (healthy)" || account.ContainerHealth != "healthy" ||
		account.ProxySource != "direct" || account.ProxyDisplay != "direct" ||
		account.AuthFiles != 1 || account.AuthState != "configured" ||
		account.Runtime.ErrorCount != 1 || account.Runtime.ErrorLogFiles != 2 ||
		account.OperationalStatus.Code != "degraded" || account.OperationalStatus.Label != "Recent errors" ||
		account.OperationalStatus.Reason != "This account has recent request errors" || !account.OperationalStatus.Selectable {
		t.Fatalf("legacy account facts = %#v", account)
	}
}

func TestAccountCatalogReturnsLegacyProxySourceAndRedactedDisplay(t *testing.T) {
	server, store := newTestAdmin(t)
	accounts, err := store.ReadAccounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("read account = (%#v, %v)", accounts, err)
	}
	accounts[0].ProxyMode = "custom"
	if err := store.WriteAccounts(context.Background(), accounts); err != nil {
		t.Fatalf("write custom proxy account: %v", err)
	}
	if err := store.WriteSecret(
		context.Background(), "cpa_account_proxy_url:alpha", "socks5://proxy-user:plain-secret@127.0.0.1:1080",
	); err != nil {
		t.Fatalf("write account proxy: %v", err)
	}
	response := performAdminRequest(
		server, http.MethodGet, "/admin/api/accounts", nil,
		map[string]string{"X-Management-Key": "test-management-key"}, nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("account catalog = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Accounts []accountListItem `json:"accounts"`
	}
	decodeAdminResponse(t, response, &payload)
	if len(payload.Accounts) != 1 || payload.Accounts[0].ProxySource != "account" ||
		payload.Accounts[0].ProxyDisplay != "socks5://proxy-user:***@127.0.0.1:1080" ||
		!payload.Accounts[0].ProxyConfigured {
		t.Fatalf("account proxy presentation = %#v", payload.Accounts)
	}
	if strings.Contains(response.Body.String(), "plain-secret") {
		t.Fatalf("account catalog leaked proxy password: %s", response.Body.String())
	}
}

func TestAccountCatalogPreservesCustomAndPerAccountQuotaWindows(t *testing.T) {
	server, store := newTestAdmin(t)
	server.now = func() time.Time { return time.Unix(10_000, 0) }
	resetAt := int64(10_600)
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: "alpha", Status: "ok", Weekly: &quota.WeeklyWindow{
				WindowSeconds: 3_600, ResetAt: &resetAt,
			},
		}}},
	}); err != nil {
		t.Fatalf("write quota state: %v", err)
	}
	reader := &fakeUsageReader{accountSummaries: map[string]usage.AccountUsageSummary{
		"alpha": {Account: "alpha", RawMetrics: usage.RawMetrics{RequestCount: 2, TotalTokens: 120}},
	}}
	server.usage = reader
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(
		server, http.MethodGet,
		"/admin/api/accounts?window=custom&start_at=6&end_at=9",
		nil, headers, nil,
	)
	if response.Code != http.StatusOK || reader.accountSummaryStartAt != 6 ||
		reader.accountSummaryEndAt == nil || *reader.accountSummaryEndAt != 9 ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window":"custom"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_start_at":6`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_end_at":9`)) {
		t.Fatalf("custom account catalog = %d %s reader=%#v", response.Code, response.Body.String(), reader)
	}

	response = performAdminRequest(
		server, http.MethodGet, "/admin/api/accounts?window=since_reset", nil, headers, nil,
	)
	if response.Code != http.StatusOK || reader.accountSummaryByStartCalls != 1 ||
		reader.accountSummaryStartAtByAccount["alpha"] != 7_000 ||
		reader.accountSummaryEndAt == nil || *reader.accountSummaryEndAt != 10_000 ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window":"since_reset"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_start_at":null`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_start_at_by_account":{"alpha":7000}`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"usage_window_start_at":7000`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"usage_window_available":true`)) {
		t.Fatalf("since-reset account catalog = %d %s reader=%#v", response.Code, response.Body.String(), reader)
	}
	if reader.accountSummaryCalls != 1 {
		t.Fatalf("since-reset account catalog used ordinary summary query: %#v", reader)
	}
}

func TestUsageBreakdownAPIsAreLazyLiveReadsWithBoundedContracts(t *testing.T) {
	server, store := newTestAdmin(t)
	server.now = func() time.Time { return time.Unix(10_000, 0) }
	reader := &fakeUsageReader{
		userResult: usage.UserBreakdown{
			CollectionStartedAt: 100,
			EffectiveStartAt:    6400,
			Totals: usage.UserTotals{WeightedMetrics: usage.WeightedMetrics{
				RawMetrics:     usage.RawMetrics{RequestCount: 2, TotalTokens: 120},
				WeightedTokens: 150,
			}},
			Models: []usage.UserModelUsage{}, ReasoningEfforts: []usage.UserEffortUsage{},
			Combinations: []usage.UserCombinationUsage{},
		},
		accountResult: usage.AccountBreakdown{
			CollectionStartedAt: 100,
			EffectiveStartAt:    6000,
			Totals:              usage.RawMetrics{RequestCount: 3, TotalTokens: 180},
			Models:              []usage.AccountModelUsage{},
			Combinations:        []usage.AccountCombinationUsage{},
		},
	}
	server.usage = reader
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/users/usage-breakdown?email=alice@example.com&window=3600",
		nil,
		headers,
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("user usage status = %d, body = %s", response.Code, response.Body.String())
	}
	if reader.userCalls != 1 || reader.userStartAt != 6400 || reader.userEndAt != nil {
		t.Fatalf("user usage query = %#v", reader)
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"weighted_tokens":150`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_end_at":10000`)) ||
		bytes.Contains(response.Body.Bytes(), []byte("test_external_alice")) {
		t.Fatalf("user usage response = %s", response.Body.String())
	}

	response = performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/accounts/usage-breakdown?account=alpha&window=custom&start_at=6000&end_at=7000",
		nil,
		headers,
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("account usage status = %d, body = %s", response.Code, response.Body.String())
	}
	if reader.accountCalls != 1 || reader.accountStartAt != 6000 ||
		reader.accountEndAt == nil || *reader.accountEndAt != 7000 {
		t.Fatalf("account usage query = %#v", reader)
	}
	if bytes.Contains(response.Body.Bytes(), []byte("weighted")) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_end_at":7000`)) ||
		bytes.Contains(response.Body.Bytes(), []byte("alice@example.com")) {
		t.Fatalf("account usage response leaks user or weighted data: %s", response.Body.String())
	}

	resetAt := int64(10_600)
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: "alpha", Status: "ok", Weekly: &quota.WeeklyWindow{
				WindowSeconds: 3_600, ResetAt: &resetAt,
			},
		}}},
	}); err != nil {
		t.Fatalf("write account quota period: %v", err)
	}
	response = performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/accounts/usage-breakdown?account=alpha&window=since_reset",
		nil,
		headers,
		nil,
	)
	if response.Code != http.StatusOK || reader.accountCalls != 2 || reader.accountStartAt != 7_000 ||
		reader.accountEndAt != nil || !bytes.Contains(response.Body.Bytes(), []byte(`"window":"since_reset"`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_seconds":null`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_start_at":7000`)) ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"window_end_at":10000`)) {
		t.Fatalf("since-reset account usage = %d %s, reader=%#v", response.Code, response.Body.String(), reader)
	}

	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: "alpha", Status: "unavailable",
		}}},
	}); err != nil {
		t.Fatalf("write unavailable account quota period: %v", err)
	}
	response = performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/accounts/usage-breakdown?account=alpha&window=since_reset",
		nil,
		headers,
		nil,
	)
	assertAdminError(t, response, http.StatusConflict, "usage_window_unavailable")
	if reader.accountCalls != 2 {
		t.Fatal("unavailable quota-period window reached the usage database")
	}
}

func TestNotificationSettingsWebhookAndManualSendContract(t *testing.T) {
	baseServer, store := newTestAdmin(t)
	baseServer.Close()
	sender := &fakeAdminNotificationSender{configured: true}
	server, err := New(Config{
		Store: store, Activity: staticAdminActivity{counts: map[string]int{"alpha": 2}},
		NotificationSender: sender, Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("New notification admin: %v", err)
	}
	t.Cleanup(server.Close)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/settings/notifications", nil, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("read notification settings = %d, %s", response.Code, response.Body.String())
	}
	var initial notificationSettingsResponse
	decodeAdminResponse(t, response, &initial)
	if initial.Notifications.WebhookConfigured || initial.Values.Enabled || initial.Values.Timezone != "Asia/Shanghai" {
		t.Fatalf("initial notification settings = %#v", initial)
	}
	response = performAdminRequest(server, http.MethodPut, "/admin/api/settings/notifications", map[string]any{
		"confirm": "save", "values": map[string]any{},
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")

	response = performAdminRequest(server, http.MethodPut, "/admin/api/settings/notifications", map[string]any{
		"confirm": "save", "values": map[string]any{"enabled": true},
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("injected configured sender should allow enable = %d, %s", response.Code, response.Body.String())
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/notification-webhook", map[string]any{
		"confirm": "save", "webhook_url": "https://example.com/send?key=should-not-leak",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	if strings.Contains(response.Body.String(), "should-not-leak") {
		t.Fatalf("invalid webhook leaked in response: %s", response.Body.String())
	}

	const webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-send-placeholder"
	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/notification-webhook", map[string]any{
		"confirm": "save", "webhook_url": webhook,
	}, headers, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), webhook) {
		t.Fatalf("save webhook = %d, %s", response.Code, response.Body.String())
	}
	var savedWebhook struct {
		Notifications notificationStatus `json:"notifications"`
	}
	decodeAdminResponse(t, response, &savedWebhook)
	if !savedWebhook.Notifications.WebhookConfigured || savedWebhook.Notifications.WebhookURL != "" {
		t.Fatalf("saved webhook status leaked credential = %#v", savedWebhook.Notifications)
	}
	if savedWebhook.Notifications.WebhookDisplayURL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=••••••lder" {
		t.Fatalf("saved webhook display = %q", savedWebhook.Notifications.WebhookDisplayURL)
	}
	stored, found, err := store.ReadSecret(context.Background(), "wecom_webhook")
	if err != nil || !found || stored != webhook {
		t.Fatalf("stored webhook = (%q, %v, %v)", stored, found, err)
	}

	response = performAdminRequest(server, http.MethodPut, "/admin/api/settings/notifications", map[string]any{
		"confirm": "save",
		"values": map[string]any{
			"enabled": true, "timezone": "Asia/Shanghai", "daily_times": "18:00,09:00,09:00",
			"schedule_grace_minutes": 0, "quota_alert_enabled": true,
			"weekly_threshold_percent": 92.5,
		},
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("update notification settings = %d, %s", response.Code, response.Body.String())
	}
	settings, err := store.ReadSettings(context.Background())
	if err != nil || settings["notification.daily_times"] != "09:00,18:00" ||
		settings["notification.timezone"] != nil {
		t.Fatalf("stored notification settings = (%#v, %v)", settings, err)
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/settings/notifications", nil, headers, nil)
	var redacted notificationSettingsResponse
	decodeAdminResponse(t, response, &redacted)
	if !redacted.Notifications.WebhookConfigured || redacted.Notifications.WebhookURL != "" ||
		strings.Contains(response.Body.String(), webhook) {
		t.Fatalf("read notification settings leaked webhook = %s", response.Body.String())
	}
	if redacted.Notifications.WebhookDisplayURL != savedWebhook.Notifications.WebhookDisplayURL {
		t.Fatal("saved Webhook display was lost on reload")
	}

	resetAt := int64(1_900_000_000)
	resetCount := int64(2)
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{
		Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{
			Account: "alpha", Status: "ok", ResetCreditCount: &resetCount,
			WeeklyWindows: []quota.WeeklyWindow{{
				Key: "default:primary_window", Label: "常规周限额", UsedPercent: 55, ResetAt: &resetAt,
			}},
		}}},
	}); err != nil {
		t.Fatalf("write quota fixture: %v", err)
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/notifications/send", map[string]any{}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("manual notification = %d, %s", response.Code, response.Body.String())
	}
	if len(sender.contents) != 1 || !strings.Contains(sender.contents[0], "55% | 2 | 2") ||
		!strings.Contains(sender.contents[0], "# CCPA · Account quota report") {
		t.Fatalf("manual notification content = %#v", sender.contents)
	}
	state, found, err := notifications.ReadRuntimeState(context.Background(), store)
	if err != nil || !found || state.LastSuccessAt == nil || *state.LastSuccessAt != 1_800_000_000 || state.LastError != "" {
		t.Fatalf("manual notification state = (%#v, %v, %v)", state, found, err)
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/notifications/test", map[string]any{}, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Test message sent") {
		t.Fatalf("test notification = %d, %s", response.Code, response.Body.String())
	}
	if len(sender.contents) != 2 || !strings.Contains(sender.contents[1], "# ✅ CCPA · Notification test") ||
		!strings.Contains(sender.contents[1], "WeCom notification channel connected successfully") ||
		strings.Contains(sender.contents[1], "账号额度报告") || strings.Contains(sender.contents[1], "55%") {
		t.Fatalf("test notification content = %#v", sender.contents)
	}

	sender.sendError = errors.New("failed " + webhook)
	response = performAdminRequest(server, http.MethodPost, "/admin/api/notifications/test", map[string]any{}, headers, nil)
	assertAdminError(t, response, http.StatusBadGateway, "notification_send_failed")
	if strings.Contains(response.Body.String(), "test-send-placeholder") || !strings.Contains(response.Body.String(), "[REDACTED]") {
		t.Fatalf("manual notification error was not redacted: %s", response.Body.String())
	}

	sender.sendError = i18n.M("notifications.wecom_message_delivery_failed_invalid_response")
	localizedHeaders := map[string]string{"X-Management-Key": "test-management-key", "Accept-Language": "zh-CN"}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/notifications/test", map[string]any{}, localizedHeaders, nil)
	assertAdminError(t, response, http.StatusBadGateway, "notification_send_failed")
	if !strings.Contains(response.Body.String(), "企业微信消息发送失败：响应无效") {
		t.Fatalf("authored delivery failure did not follow the request language: %s", response.Body.String())
	}

	response = performAdminRequest(server, http.MethodGet, "/admin/api/settings/notifications", nil, localizedHeaders, nil)
	var localizedStatus notificationSettingsResponse
	decodeAdminResponse(t, response, &localizedStatus)
	if localizedStatus.Notifications.LastError != "企业微信消息发送失败：响应无效" {
		t.Fatalf("persisted failure did not follow the request language: %q", localizedStatus.Notifications.LastError)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/notification-webhook/clear", map[string]any{
		"confirm": "clear",
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("clear webhook = %d, %s", response.Code, response.Body.String())
	}
	if _, found, err := store.ReadSecret(context.Background(), "wecom_webhook"); err != nil || found {
		t.Fatalf("webhook after clear = (%v, %v)", found, err)
	}
	settings, err = store.ReadSettings(context.Background())
	if err != nil || settings["notification.enabled"] != false {
		t.Fatalf("notification enabled after clear = (%#v, %v)", settings["notification.enabled"], err)
	}
}

func TestDefaultNotificationSenderRejectsEnableWithoutWebhook(t *testing.T) {
	server, _ := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodPut, "/admin/api/settings/notifications", map[string]any{
		"confirm": "save", "values": map[string]any{"enabled": true},
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "invalid_request")
	if !strings.Contains(response.Body.String(), "webhook") {
		t.Fatalf("missing webhook response = %s", response.Body.String())
	}
}

func TestAdminInitialPasswordUsesEncryptedSecretWithoutReturningIt(t *testing.T) {
	server, store := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodPost, "/admin/api/settings/initial-password", map[string]any{
		"initial_password": "first-password!", "confirmation": "different-password!",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "password_mismatch")

	password := "future-user-password!"
	response = performAdminRequest(server, http.MethodPost, "/admin/api/settings/initial-password", map[string]any{
		"initial_password": password, "confirmation": password,
	}, headers, nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), password) {
		t.Fatalf("initial password response = %d %s", response.Code, response.Body.String())
	}
	stored, found, err := store.ReadSecret(context.Background(), portalInitialPasswordSecret)
	if err != nil || !found || stored != password {
		t.Fatalf("stored initial password = (%q, %v, %v)", stored, found, err)
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/settings/general", nil, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"initial_password_configured":true`) {
		t.Fatalf("general settings after initial password = %d %s", response.Code, response.Body.String())
	}
}

func newTestAdmin(t *testing.T) (*Server, *controlplane.Store) {
	return newTestAdminWithRebalancer(t, nil)
}

func newTestAdminWithRebalancer(t *testing.T, rebalancer AccountRebalancer) (*Server, *controlplane.Store) {
	t.Helper()
	store, err := controlplane.Open(context.Background(), t.TempDir(), controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	if err := store.WriteSecret(context.Background(), "cpa_management_key", "test-management-key"); err != nil {
		store.Close()
		t.Fatalf("write management key: %v", err)
	}
	if err := store.WriteAccounts(context.Background(), []controlplane.Account{{
		ID:           "alpha",
		Email:        "alpha@accounts.example.com",
		Port:         18318,
		ProxyMode:    "inherit",
		CreatedAt:    100,
		GroupEnabled: true,
		DefaultGroup: true,
	}}); err != nil {
		store.Close()
		t.Fatalf("write accounts: %v", err)
	}
	if err := store.WriteKeyRecords(context.Background(), []controlplane.KeyRecord{{
		Label:        "alice@example.com:alpha",
		Account:      "alpha",
		AccountEmail: "alpha@accounts.example.com",
		User:         "alice@example.com",
		Status:       "active",
		Key:          "test_external_alice",
		CreatedAt:    100,
		UpdatedAt:    100,
	}}); err != nil {
		store.Close()
		t.Fatalf("write users: %v", err)
	}
	server, err := New(Config{
		Store:          store,
		SessionTTL:     15 * time.Minute,
		Rebalancer:     rebalancer,
		TeamIdentities: &fakeTeamIdentitySynchronizer{},
	})
	if err != nil {
		store.Close()
		t.Fatalf("New admin server: %v", err)
	}
	t.Cleanup(func() {
		server.Close()
		_ = store.Close()
	})
	return server, store
}

type fakeTeamIdentitySynchronizer struct {
	identities map[string]usage.TeamIdentity
	calls      int
	err        error
}

func (synchronizer *fakeTeamIdentitySynchronizer) SyncUserTeams(
	_ context.Context,
	identities map[string]usage.TeamIdentity,
) (int, error) {
	synchronizer.calls++
	synchronizer.identities = make(map[string]usage.TeamIdentity, len(identities))
	for user, identity := range identities {
		synchronizer.identities[user] = identity
	}
	return len(identities), synchronizer.err
}

type fakeAdminRebalancer struct {
	result           failover.RebalanceResult
	err              error
	calls            int
	evacuationResult failover.EvacuationResult
	evacuationErr    error
	evacuateCalls    int
	evacuatedAccount string
}

type fakeQuotaResetter struct {
	inspection   quota.ResetInspection
	inspectErr   error
	inspectCalls int
	result       quota.ResetResult
	err          error
	calls        int
	account      string
	creditID     string
}

type fakeAccountLifecycle struct {
	createRequest accountlifecycle.CreateRequest
	updateRequest accountlifecycle.UpdateRequest
	deleteRequest accountlifecycle.DeleteRequest
	clearAccount  string
	err           error
}

func (service *fakeAccountLifecycle) Create(_ context.Context, request accountlifecycle.CreateRequest) (accountlifecycle.CreateResult, error) {
	service.createRequest = request
	return accountlifecycle.CreateResult{
		Account:        controlplane.Account{ID: request.ID, Email: request.Email, Port: 18320},
		CreatedKeyRows: 2, SnapshotGeneration: "generation-test",
	}, service.err
}

func (service *fakeAccountLifecycle) Update(_ context.Context, request accountlifecycle.UpdateRequest) (accountlifecycle.UpdateResult, error) {
	service.updateRequest = request
	return accountlifecycle.UpdateResult{
		Account:     controlplane.Account{ID: request.NewAccountID, Email: request.Email, Port: 18318},
		RenamedFrom: request.AccountID, SnapshotGeneration: "generation-test",
	}, service.err
}

func (service *fakeAccountLifecycle) Delete(_ context.Context, request accountlifecycle.DeleteRequest) (accountlifecycle.DeleteResult, error) {
	service.deleteRequest = request
	return accountlifecycle.DeleteResult{
		AccountID: request.AccountID, ReplacementAccount: request.FallbackAccount,
		Backup: "backups/accounts/fixture", SnapshotGeneration: "generation-test",
	}, service.err
}

func (service *fakeAccountLifecycle) ClearAuth(_ context.Context, accountID string) (accountlifecycle.AuthClearResult, error) {
	service.clearAccount = accountID
	return accountlifecycle.AuthClearResult{AccountID: accountID, Backup: "backups/accounts/fixture"}, service.err
}

type fakeUserLifecycle struct {
	rotateCalls      int
	revokeCalls      int
	deleteCalls      int
	deleteRevoke     bool
	quotaMode        string
	quotaTokens      *int64
	quotaActionCalls int
	quotaAction      UserQuotaActionRequest
}

func (service *fakeUserLifecycle) CreateUser(context.Context, string, *string) (UserCreateResult, error) {
	return UserCreateResult{
		User: "new@example.com", APIKey: "one-time-api-key", InitialPassword: "one-time-password",
		Accounts: 1, SnapshotGeneration: "generation-test",
	}, nil
}

func (service *fakeUserLifecycle) RotateUserKey(context.Context, string) (identity.RotationResult, error) {
	service.rotateCalls++
	return identity.RotationResult{APIKey: "rotated-one-time-key", SnapshotGeneration: "generation-test"}, nil
}

func (service *fakeUserLifecycle) RevokeUser(context.Context, string) (UserRevokeResult, error) {
	service.revokeCalls++
	return UserRevokeResult{User: "new@example.com", RevokedKeys: 1, SnapshotGeneration: "generation-test"}, nil
}

func (service *fakeUserLifecycle) ResetUserPassword(context.Context, string) (PasswordResetResult, error) {
	return PasswordResetResult{
		User: "new@example.com", InitialPassword: "one-time-password", PasswordChangeRequired: true,
	}, nil
}

func (service *fakeUserLifecycle) DeleteUser(_ context.Context, _ string, revoke bool) (UserDeleteResult, error) {
	service.deleteCalls++
	service.deleteRevoke = revoke
	return UserDeleteResult{User: "new@example.com", RemovedRecords: 1, RevokedActiveKeys: 1}, nil
}

func (service *fakeUserLifecycle) ReadUserQuota(context.Context, string) (UserQuotaResult, error) {
	limit := int64(1_000)
	return UserQuotaResult{
		User: "new@example.com",
		WeeklyQuota: UserWeeklyQuota{
			WeeklyQuota: usage.WeeklyQuota{
				PolicyMode: "inherit", LimitTokens: &limit, DefaultLimitTokens: &limit,
				QuotaUnit: "weighted_tokens",
			},
			PersonalPolicyResetEnabled: true,
		},
		Adjustments: []usage.QuotaAdjustment{},
	}, nil
}

func (service *fakeUserLifecycle) ReadUserQuotas(_ context.Context, users []string) (map[string]UserWeeklyQuota, error) {
	result := make(map[string]UserWeeklyQuota, len(users))
	for _, user := range users {
		quota, err := service.ReadUserQuota(context.Background(), user)
		if err != nil {
			return nil, err
		}
		result[user] = quota.WeeklyQuota
	}
	return result, nil
}

func (service *fakeUserLifecycle) UpdateUserQuota(
	_ context.Context,
	_ string,
	mode string,
	weeklyTokens *int64,
) (UserQuotaResult, error) {
	service.quotaMode = mode
	service.quotaTokens = weeklyTokens
	return UserQuotaResult{
		User: "new@example.com",
		WeeklyQuota: UserWeeklyQuota{
			WeeklyQuota: usage.WeeklyQuota{
				PolicyMode: mode, PolicyTokens: weeklyTokens, LimitTokens: weeklyTokens,
				Unlimited: mode == "unlimited", QuotaUnit: "weighted_tokens",
			},
			PersonalPolicyResetEnabled: true,
		},
		Adjustments: []usage.QuotaAdjustment{},
	}, nil
}

func (service *fakeUserLifecycle) ClearUserQuota(context.Context, string) (UserQuotaResult, error) {
	service.quotaMode = "inherit"
	return service.ReadUserQuota(context.Background(), "new@example.com")
}

func (service *fakeUserLifecycle) ReadQuotaOperations(context.Context) (UserQuotaOperationSummary, error) {
	return UserQuotaOperationSummary{TotalUsers: 2, UsersWithUsage: 1, TotalUsedTokens: 300, TotalRawUsedTokens: 150}, nil
}

func (service *fakeUserLifecycle) ApplyUserQuotaAction(
	_ context.Context,
	request UserQuotaActionRequest,
) (UserQuotaActionResponse, error) {
	service.quotaActionCalls++
	service.quotaAction = request
	return UserQuotaActionResponse{
		QuotaActionResult: usage.QuotaActionResult{
			Action: request.Action, AppliedUsers: append([]string(nil), request.Users...), SkippedUsers: []string{},
			TokenAmount: func() *int64 { value := request.TokenAmount; return &value }(),
		},
		Message:         "quota action applied",
		QuotaOperations: UserQuotaOperationSummary{TotalUsers: len(request.Users)},
	}, nil
}

type fakeRouteRegistrar struct {
	registered bool
}

func (registrar *fakeRouteRegistrar) Register(router gin.IRouter) {
	registrar.registered = true
	router.GET("/usage/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"surface": "portal"})
	})
}

type staticAdminAccountStates struct {
	states map[string]failover.AccountState
}

type staticAdminOAuth struct{}

func (staticAdminOAuth) Load(string) (quota.OAuthRecord, error) {
	return quota.OAuthRecord{AccountID: "alpha"}, nil
}

type mappedAdminOAuth struct {
	configured map[string]bool
}

func (loader mappedAdminOAuth) Load(account string) (quota.OAuthRecord, error) {
	if !loader.configured[account] {
		return quota.OAuthRecord{}, quota.ErrOAuthMissing
	}
	return quota.OAuthRecord{AccessToken: "test-token", AccountID: account}, nil
}

type staticAdminAccountRuntime struct {
	states map[string]accountstatus.State
}

func (runtime staticAdminAccountRuntime) Observe(
	_ context.Context,
	_ map[string]string,
) map[string]accountstatus.State {
	return runtime.states
}

func (provider staticAdminAccountStates) AccountStates(context.Context) (map[string]failover.AccountState, error) {
	return provider.states, nil
}

type staticAdminActivity struct {
	counts map[string]int
	emails map[string][]string
}

type fakeAdminNotificationSender struct {
	configured bool
	contents   []string
	sendError  error
}

func (sender *fakeAdminNotificationSender) Configured(context.Context) (bool, error) {
	return sender.configured, nil
}

func (sender *fakeAdminNotificationSender) Send(_ context.Context, content string) (notifications.SendResult, error) {
	if sender.sendError != nil {
		return notifications.SendResult{}, sender.sendError
	}
	sender.contents = append(sender.contents, content)
	return notifications.SendResult{ErrorCode: 0, Message: "ok"}, nil
}

type fakeUsageReader struct {
	userResult                     usage.UserBreakdown
	accountResult                  usage.AccountBreakdown
	userCalls                      int
	accountCalls                   int
	userStartAt                    int64
	accountStartAt                 int64
	userEndAt                      *int64
	accountEndAt                   *int64
	teamResult                     map[string]usage.TeamUsageMetrics
	teamBreakdownResult            usage.TeamBreakdown
	teamCalls                      int
	teamBreakdownCalls             int
	trendResult                    usage.TokenTrend
	collectorResult                usage.CollectorStatus
	trendCalls                     int
	trendStartAt                   int64
	trendEndAt                     int64
	trendBucketSeconds             int64
	trendUserLimit                 int
	trendTokenMode                 usage.TokenMode
	trendStartAtByAccount          map[string]int64
	accountSummaries               map[string]usage.AccountUsageSummary
	accountSummaryCalls            int
	accountSummaryByStartCalls     int
	accountSummaryStartAt          int64
	accountSummaryEndAt            *int64
	accountSummaryStartAtByAccount map[string]int64
	userSummaries                  map[string]usage.WeightedMetrics
	userSummaryCalls               int
	selectedUserSummaryCalls       int
	selectedUserSummaryUsers       []string
	userSummaryStartAt             int64
	userSummaryEndAt               *int64
	userAccountResult              usage.UserAccountSummary
	userAccountCalls               int
	userAccountEmail               string
	userAccountStartAt             int64
	userAccountEndAt               *int64
	publicResult                   map[string]usage.PublicAccountUsage
	publicError                    error
	publicCalls                    int
	publicAccounts                 []string
	publicStartAt                  int64
	publicEndAt                    int64
}

type fakeReleaseCatalog struct {
	version string
	err     error
	calls   int
}

func (catalog *fakeReleaseCatalog) LatestRelease(_ context.Context) (string, error) {
	catalog.calls++
	return catalog.version, catalog.err
}

func (reader *fakeUsageReader) TokenTimeSeries(
	_ context.Context,
	_ []string,
	_ []string,
	_ []string,
	startAt int64,
	endAt int64,
	bucketSeconds int64,
	userLimit int,
	tokenMode usage.TokenMode,
	startAtByAccount map[string]int64,
) (usage.TokenTrend, error) {
	reader.trendCalls++
	reader.trendStartAt, reader.trendEndAt, reader.trendBucketSeconds = startAt, endAt, bucketSeconds
	reader.trendUserLimit = userLimit
	reader.trendTokenMode = tokenMode
	reader.trendStartAtByAccount = startAtByAccount
	return reader.trendResult, nil
}

func (reader *fakeUsageReader) Status(context.Context) (usage.CollectorStatus, error) {
	return reader.collectorResult, nil
}

func (reader *fakeUsageReader) PublicGatewayUsage(
	_ context.Context,
	accounts []string,
	startAt int64,
	endAt int64,
) (map[string]usage.PublicAccountUsage, error) {
	reader.publicCalls++
	reader.publicAccounts = append([]string(nil), accounts...)
	reader.publicStartAt, reader.publicEndAt = startAt, endAt
	result := make(map[string]usage.PublicAccountUsage, len(reader.publicResult))
	for account, item := range reader.publicResult {
		result[account] = item
	}
	return result, reader.publicError
}

func (reader *fakeUsageReader) UserSummaries(
	_ context.Context,
	startAt int64,
	endAt *int64,
) (map[string]usage.WeightedMetrics, error) {
	reader.userSummaryCalls++
	reader.userSummaryStartAt = startAt
	reader.userSummaryEndAt = cloneTestInt64(endAt)
	result := make(map[string]usage.WeightedMetrics, len(reader.userSummaries))
	for user, summary := range reader.userSummaries {
		result[user] = summary
	}
	return result, nil
}

func (reader *fakeUsageReader) UserSummariesForUsers(
	_ context.Context,
	users []string,
	startAt int64,
	endAt *int64,
) (map[string]usage.WeightedMetrics, error) {
	reader.selectedUserSummaryCalls++
	reader.selectedUserSummaryUsers = append([]string(nil), users...)
	reader.userSummaryStartAt = startAt
	reader.userSummaryEndAt = cloneTestInt64(endAt)
	result := make(map[string]usage.WeightedMetrics, len(users))
	for _, user := range users {
		result[user] = reader.userSummaries[user]
	}
	return result, nil
}

func (reader *fakeUsageReader) UserAccounts(
	_ context.Context,
	email string,
	startAt int64,
	endAt *int64,
) (usage.UserAccountSummary, error) {
	reader.userAccountCalls++
	reader.userAccountEmail = email
	reader.userAccountStartAt = startAt
	reader.userAccountEndAt = cloneTestInt64(endAt)
	return reader.userAccountResult, nil
}

func (reader *fakeUsageReader) AccountSummaries(
	_ context.Context,
	accounts []string,
	startAt int64,
	endAt *int64,
) (map[string]usage.AccountUsageSummary, error) {
	reader.accountSummaryCalls++
	reader.accountSummaryStartAt = startAt
	reader.accountSummaryEndAt = cloneTestInt64(endAt)
	result := make(map[string]usage.AccountUsageSummary, len(accounts))
	for _, account := range accounts {
		result[account] = reader.accountSummaries[account]
	}
	return result, nil
}

func (reader *fakeUsageReader) AccountSummariesByStart(
	_ context.Context,
	startAtByAccount map[string]int64,
	endAt *int64,
) (map[string]usage.AccountUsageSummary, error) {
	reader.accountSummaryByStartCalls++
	reader.accountSummaryStartAtByAccount = make(map[string]int64, len(startAtByAccount))
	for account, startAt := range startAtByAccount {
		reader.accountSummaryStartAtByAccount[account] = startAt
	}
	reader.accountSummaryEndAt = cloneTestInt64(endAt)
	result := make(map[string]usage.AccountUsageSummary, len(startAtByAccount))
	for account := range startAtByAccount {
		result[account] = reader.accountSummaries[account]
	}
	return result, nil
}

func (reader *fakeUsageReader) UserBreakdown(
	_ context.Context,
	_ string,
	_ string,
	startAt int64,
	endAt *int64,
) (usage.UserBreakdown, error) {
	reader.userCalls++
	reader.userStartAt = startAt
	reader.userEndAt = cloneTestInt64(endAt)
	return reader.userResult, nil
}

func (reader *fakeUsageReader) AccountBreakdown(
	_ context.Context,
	_ string,
	startAt int64,
	endAt *int64,
) (usage.AccountBreakdown, error) {
	reader.accountCalls++
	reader.accountStartAt = startAt
	reader.accountEndAt = cloneTestInt64(endAt)
	return reader.accountResult, nil
}

func (reader *fakeUsageReader) TeamUsage(
	_ context.Context,
	_ []string,
	_ map[string]string,
	_ *int64,
	_ *int64,
) (map[string]usage.TeamUsageMetrics, error) {
	reader.teamCalls++
	return reader.teamResult, nil
}

func (reader *fakeUsageReader) TeamBreakdown(
	_ context.Context,
	_ string,
	_ []string,
	_ *int64,
	_ *int64,
) (usage.TeamBreakdown, error) {
	reader.teamBreakdownCalls++
	return reader.teamBreakdownResult, nil
}

func cloneTestInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (activity staticAdminActivity) RefreshActiveUsersLastHour(context.Context) (map[string]int, error) {
	return activity.counts, nil
}

func (activity staticAdminActivity) ActiveUserEmailsLastHour(context.Context) (map[string][]string, error) {
	return activity.emails, nil
}

func (rebalancer *fakeAdminRebalancer) RebalanceAll(context.Context) (failover.RebalanceResult, error) {
	rebalancer.calls++
	return rebalancer.result, rebalancer.err
}

func (rebalancer *fakeAdminRebalancer) EvacuateAccount(
	_ context.Context,
	account string,
) (failover.EvacuationResult, error) {
	rebalancer.evacuateCalls++
	rebalancer.evacuatedAccount = account
	return rebalancer.evacuationResult, rebalancer.evacuationErr
}

func (resetter *fakeQuotaResetter) Reset(
	_ context.Context,
	account string,
	creditID string,
) (quota.ResetResult, error) {
	resetter.calls++
	resetter.account = account
	resetter.creditID = creditID
	return resetter.result, resetter.err
}

func (resetter *fakeQuotaResetter) Inspect(_ context.Context, account string) (quota.ResetInspection, error) {
	resetter.inspectCalls++
	resetter.account = account
	return resetter.inspection, resetter.inspectErr
}

func performAdminRequest(
	server *Server,
	method string,
	path string,
	body any,
	headers map[string]string,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	return performRawAdminRequest(server, method, path, raw, headers, cookie)
}

func performRawAdminRequest(
	server *Server,
	method string,
	path string,
	body []byte,
	headers map[string]string,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		if strings.EqualFold(name, "Host") {
			request.Host = value
		} else {
			request.Header.Set(name, value)
		}
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func assertAdminError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("error status = %d, want %d, body = %s", response.Code, status, response.Body.String())
	}
	var envelope ErrorEnvelope
	decodeAdminResponse(t, response, &envelope)
	if envelope.Error.Code != code {
		t.Fatalf("error code = %q, want %q, body = %s", envelope.Error.Code, code, response.Body.String())
	}
}

func decodeAdminResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}
