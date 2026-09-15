package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

const (
	previewCookie = "cpa_admin_preview_session"
	previewCSRF   = "local-mock-csrf"
)

type previewServer struct {
	fixtureDirectory string
	fixtures         map[string][]byte
	portalAssets     map[string][]byte
}

func main() {
	address := flag.String("address", "127.0.0.1:8896", "preview listen address")
	root := flag.String("root", ".", "repository root containing testdata/preview")
	flag.Parse()
	if err := run(*address, *root); err != nil {
		log.Fatal(err)
	}
}

func run(address, root string) error {
	fixtureDirectory := filepath.Join(root, "testdata", "preview")
	portalAssetDirectory := filepath.Join(root, "frontend", "portal", "public", "assets")
	server, err := newPreviewServer(fixtureDirectory, portalAssetDirectory)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              address,
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() {
		result <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownContext)
	}
}

func newPreviewServer(fixtureDirectory string, portalAssetDirectory string) (*previewServer, error) {
	fixtureNames := []string{
		"account-breakdown.json",
		"accounts.json",
		"configuration.json",
		"general-settings.json",
		"images.json",
		"jobs.json",
		"notification-settings.json",
		"onboarding.json",
		"overview-catalog.json",
		"overview-status.json",
		"overview-summary.json",
		"overview-usage-30d.json",
		"overview-usage.json",
		"quota-actions.json",
		"release.json",
		"runtime-services.json",
		"site-config.json",
		"settings-workspace.json",
		"teams-usage.json",
		"teams.json",
		"user-breakdown.json",
		"user-detail.json",
		"users.json",
	}
	server := &previewServer{
		fixtureDirectory: fixtureDirectory,
		fixtures:         make(map[string][]byte, len(fixtureNames)),
		portalAssets:     make(map[string][]byte, 6),
	}
	for _, name := range fixtureNames {
		content, err := os.ReadFile(filepath.Join(fixtureDirectory, name))
		if err != nil {
			return nil, fmt.Errorf("read preview fixture %s: %w", name, err)
		}
		if !json.Valid(content) {
			return nil, fmt.Errorf("preview fixture is not valid JSON: %s", name)
		}
		server.fixtures[name] = content
		english, readError := os.ReadFile(filepath.Join(fixtureDirectory, "en", name))
		if readError == nil {
			if !json.Valid(english) {
				return nil, fmt.Errorf("invalid English preview fixture %s", name)
			}
			server.fixtures["en/"+name] = english
		} else if !os.IsNotExist(readError) {
			return nil, readError
		}
	}
	for _, name := range []string{
		"codex-cpa-pool-favicon-dark.svg",
		"codex-cpa-pool-favicon.svg",
		"codex-cpa-pool-logo-dark.svg",
		"codex-cpa-pool-logo.svg",
		"codex-cpa-pool-mark-dark.svg",
		"codex-cpa-pool-mark.svg",
	} {
		content, err := os.ReadFile(filepath.Join(portalAssetDirectory, name))
		if err != nil {
			return nil, fmt.Errorf("read preview portal asset %s: %w", name, err)
		}
		if len(content) == 0 {
			return nil, fmt.Errorf("preview portal asset is empty: %s", name)
		}
		server.portalAssets[name] = content
	}
	return server, nil
}

func (server *previewServer) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Language", string(i18n.Negotiate(request.Header.Get("Accept-Language"))))
	response.Header().Add("Vary", "Accept-Language")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-CPA-Preview-Mode", "go-mock")
	if strings.HasPrefix(request.URL.Path, "/portal/assets/") {
		server.writePortalAsset(response, request)
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")

	if request.URL.Path == "/healthz" && request.Method == http.MethodGet {
		server.writeJSON(response, http.StatusOK, []byte(`{"status":"ok","mode":"go-mock"}`))
		return
	}
	if request.URL.Path == "/site-config.json" && request.Method == http.MethodGet {
		server.writeFixture(response, http.StatusOK, "site-config.json")
		return
	}
	if request.URL.Path == "/admin/api/session" {
		server.handleSession(response, request)
		return
	}
	if !strings.HasPrefix(request.URL.Path, "/admin/api/") {
		server.writeError(response, http.StatusNotFound, "not_found", "预览接口不存在")
		return
	}
	if !hasPreviewSession(request) {
		server.writeError(response, http.StatusUnauthorized, "unauthorized", "请先输入本地预览密钥")
		return
	}
	if request.Method != http.MethodGet {
		server.writeError(response, http.StatusMethodNotAllowed, "read_only_preview", "本地预览仅允许读取")
		return
	}

	fixture := fixtureForRequest(request)
	if fixture == "" {
		server.writeError(response, http.StatusNotFound, "not_found", "预览接口不存在")
		return
	}
	server.writeFixture(response, http.StatusOK, fixture)
}

func (server *previewServer) writePortalAsset(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		server.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	name := strings.TrimPrefix(request.URL.Path, "/portal/assets/")
	content, ok := server.portalAssets[name]
	if !ok || name == "" || strings.Contains(name, "/") {
		server.writeError(response, http.StatusNotFound, "not_found", "预览资源不存在")
		return
	}
	response.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = response.Write(content)
	}
}

func (server *previewServer) handleSession(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost:
		if strings.TrimSpace(request.Header.Get("X-Management-Key")) == "" {
			server.writeError(response, http.StatusUnauthorized, "unauthorized", "请输入任意非空本地预览密钥")
			return
		}
		http.SetCookie(response, &http.Cookie{
			Name: previewCookie, Value: "mock", Path: "/admin", MaxAge: 3600,
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		server.writeJSON(response, http.StatusCreated, []byte(`{"authenticated":true,"accounts":{"cpa-main":{"email":"main@example.com"},"cpa-lab":{"email":"lab@example.com"},"cpa-edge":{"email":"edge@example.com"}},"csrf_token":"`+previewCSRF+`"}`))
	case http.MethodGet:
		if !hasPreviewSession(request) {
			server.writeError(response, http.StatusUnauthorized, "unauthorized", "请先输入本地预览密钥")
			return
		}
		server.writeJSON(response, http.StatusOK, []byte(`{"authenticated":true,"accounts":{"cpa-main":{"email":"main@example.com"},"cpa-lab":{"email":"lab@example.com"},"cpa-edge":{"email":"edge@example.com"}}}`))
	case http.MethodDelete:
		http.SetCookie(response, &http.Cookie{
			Name: previewCookie, Value: "", Path: "/admin", MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteStrictMode,
		})
		server.writeJSON(response, http.StatusOK, []byte(`{"logged_out":true}`))
	default:
		server.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
	}
}

func hasPreviewSession(request *http.Request) bool {
	cookie, err := request.Cookie(previewCookie)
	return err == nil && cookie.Value == "mock"
}

func fixtureForRequest(request *http.Request) string {
	switch request.URL.Path {
	case "/admin/api/overview/summary":
		return "overview-summary.json"
	case "/admin/api/overview/catalog":
		return "overview-catalog.json"
	case "/admin/api/overview/status":
		return "overview-status.json"
	case "/admin/api/overview/usage":
		if request.URL.Query().Get("window") == "2592000" {
			return "overview-usage-30d.json"
		}
		return "overview-usage.json"
	case "/admin/api/onboarding":
		return "onboarding.json"
	case "/admin/api/accounts":
		return "accounts.json"
	case "/admin/api/accounts/usage-breakdown":
		return "account-breakdown.json"
	case "/admin/api/images/cliproxy":
		return "images.json"
	case "/admin/api/users":
		return "users.json"
	case "/admin/api/users/detail":
		return "user-detail.json"
	case "/admin/api/users/usage-breakdown":
		return "user-breakdown.json"
	case "/admin/api/users/quota-actions":
		return "quota-actions.json"
	case "/admin/api/teams":
		return "teams.json"
	case "/admin/api/teams/usage":
		return "teams-usage.json"
	case "/admin/api/settings/configuration":
		return "configuration.json"
	case "/admin/api/settings/general":
		return "general-settings.json"
	case "/admin/api/settings/notifications":
		return "notification-settings.json"
	case "/admin/api/settings/workspace":
		return "settings-workspace.json"
	case "/admin/api/runtime/services":
		return "runtime-services.json"
	case "/admin/api/jobs", "/admin/api/runtime/jobs":
		return "jobs.json"
	case "/admin/api/release":
		return "release.json"
	default:
		return ""
	}
}

func (server *previewServer) writeFixture(response http.ResponseWriter, status int, name string) {
	if response.Header().Get("Content-Language") == "en" {
		if _, found := server.fixtures["en/"+name]; found {
			name = "en/" + name
		}
	}
	server.writeJSON(response, status, server.fixtures[name])
}

func (server *previewServer) writeJSON(response http.ResponseWriter, status int, payload []byte) {
	response.WriteHeader(status)
	_, _ = response.Write(payload)
}

func (server *previewServer) writeError(response http.ResponseWriter, status int, code, message string) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
	server.writeJSON(response, status, payload)
}
