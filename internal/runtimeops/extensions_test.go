package runtimeops

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/cpaplugin"
)

type extensionTestStore struct {
	settings map[string]any
	state    map[string][]byte
	accounts []controlplane.Account
	writes   int
}

func (s *extensionTestStore) ReadSettings(context.Context) (map[string]any, error) {
	return s.settings, nil
}
func (s *extensionTestStore) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return s.accounts, nil
}
func (s *extensionTestStore) ReadSecret(context.Context, string) (string, bool, error) {
	return "secret-fixture", true, nil
}
func (s *extensionTestStore) ReadRuntimeState(_ context.Context, key string, out any) (bool, error) {
	data, ok := s.state[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(data, out)
}
func (s *extensionTestStore) WriteRuntimeState(_ context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	s.state[key] = data
	s.writes++
	return err
}

type extensionTestRuntime struct {
	running               bool
	restarts, validations int
	failRestart           bool
	failValidation        bool
}

func (r *extensionTestRuntime) List(context.Context) ([]Service, error) {
	state := "stopped"
	if r.running {
		state = "running"
	}
	return []Service{{Service: "cliproxy-alpha", State: state}}, nil
}
func (r *extensionTestRuntime) RestartRunningAccount(context.Context, string) error {
	r.restarts++
	if r.failRestart && r.restarts == 1 {
		return errors.New("restart failed")
	}
	return nil
}
func (r *extensionTestRuntime) ValidatePlugin(context.Context, string, string) error {
	r.validations++
	if r.failValidation {
		return errors.New("ABI mismatch")
	}
	return nil
}

type extensionTestProjection struct {
	store  *extensionTestStore
	stages []bool
}

func (p *extensionTestProjection) RefreshAccounts(ctx context.Context) error {
	var i cpaplugin.Installation
	p.store.ReadRuntimeState(ctx, cpaplugin.StatePrefix+"alpha", &i)
	p.stages = append(p.stages, i.Staging)
	return nil
}

type extensionRoundTrip func(*http.Request) (*http.Response, error)

func (f extensionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func extensionResponse(body []byte) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}
}
func pluginFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	lib := make([]byte, 64)
	copy(lib, []byte{0x7f, 'E', 'L', 'F', 2, 1})
	lib[18] = 0x3e
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, err := z.Create("codex-ticket.so")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(lib)
	z.Close()
	return buf.Bytes(), lib
}
func newExtensionsFixture(t *testing.T) (*Extensions, *extensionTestStore, *extensionTestRuntime, *extensionTestProjection) {
	t.Helper()
	archive, _ := pluginFixture(t)
	hash := sha256.Sum256(archive)
	s := &extensionTestStore{settings: map[string]any{cpaplugin.Prefix + "version": "v0.2.0", cpaplugin.Prefix + "enabled": true, cpaplugin.Prefix + "accounts": "alpha"}, state: map[string][]byte{}, accounts: []controlplane.Account{{ID: "alpha", GroupEnabled: true}}}
	r := &extensionTestRuntime{running: true}
	p := &extensionTestProjection{store: s}
	e := NewExtensions(t.TempDir(), s, r, p)
	os.MkdirAll(filepath.Join(e.Root, "configs", "alpha"), 0o700)
	e.Client = &http.Client{Transport: extensionRoundTrip(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, ".zip") {
			return extensionResponse(archive), nil
		}
		return extensionResponse([]byte(`{"tag_name":"v0.2.0","assets":[{"name":"codex-ticket_0.2.0_linux_amd64.zip","browser_download_url":"https://github.com/` + cpaplugin.Repository + `/releases/download/v0.2.0/codex-ticket_0.2.0_linux_amd64.zip","digest":"sha256:` + hex.EncodeToString(hash[:]) + `"}]}`)), nil
	})}
	e.managementHTTP = &http.Client{Transport: extensionRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer secret-fixture" {
			t.Error("management credential absent")
		}
		return extensionResponse([]byte(`{"plugins_enabled":true,"plugins":[{"id":"codex-ticket","registered":true,"metadata":{"version":"0.2.0"}}]}`)), nil
	})}
	return e, s, r, p
}
func TestPluginUpgradeStagesBeforeEnabling(t *testing.T) {
	e, s, r, p := newExtensionsFixture(t)
	if _, err := e.UpdatePlugin(context.Background(), "alpha", io.Discard); err != nil {
		t.Fatal(err)
	}
	var got cpaplugin.Installation
	s.ReadRuntimeState(context.Background(), cpaplugin.StatePrefix+"alpha", &got)
	if got.Version != "v0.2.0" || got.Staging || r.restarts != 1 || r.validations != 1 || len(p.stages) != 2 || !p.stages[0] || p.stages[1] {
		t.Fatalf("installation=%+v runtime=%+v stages=%v", got, r, p.stages)
	}
}

func TestAccountInstallEnrollsWithoutChangingLegacyScope(t *testing.T) {
	e, s, r, _ := newExtensionsFixture(t)
	s.settings[cpaplugin.Prefix+"accounts"] = "another-account"
	if _, err := e.UpdatePlugin(context.Background(), "alpha", io.Discard); err != nil {
		t.Fatal(err)
	}
	status, err := e.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := status.Accounts[0]
	if !row.Selected || !row.Installation.Managed || s.settings[cpaplugin.Prefix+"accounts"] != "another-account" || r.restarts != 1 {
		t.Fatalf("account enrollment failed: %+v", row)
	}
}

func TestTicketStatusIsReadOnlySanitizedAndSkipsStoppedAccount(t *testing.T) {
	e, s, r, _ := newExtensionsFixture(t)
	calls := 0
	e.managementHTTP = &http.Client{Transport: extensionRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet {
			t.Fatal("status mutated upstream")
		}
		if strings.HasSuffix(req.URL.Path, "/plugins") {
			return extensionResponse([]byte(`{"plugins":[{"id":"codex-ticket","registered":true,"metadata":{"version":"0.2.0"}}]}`)), nil
		}
		return extensionResponse([]byte(`{"harvest_active":true,"inject_active":true,"cached_count":0,"harvest_reason":"ready","inject_reason":"private-secret","turn_state":"opaque-secret","entries":[{"model":"gpt-6-astra","last_http":200,"last_length":312,"reason":"length_mismatch","backoff_seconds":600,"injected_count":0,"ticket":"opaque-secret","auth_index":"private-auth"}]}`)), nil
	})}
	row := PluginAccountStatus{Account: "alpha", Running: true, Enabled: true}
	status := e.TicketStatus(context.Background(), row)
	encoded, _ := json.Marshal(status)
	if status.State != "ready" || status.CachedCount != 0 || len(status.Entries) != 1 || status.Entries[0].LastLength != 312 || status.InjectReason != "other" || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private-auth") {
		t.Fatalf("unexpected status: %s", encoded)
	}
	row.Running = false
	if e.TicketStatus(context.Background(), row).State != "stopped" || calls != 2 || s.writes != 0 || r.restarts != 0 {
		t.Fatal("status had side effects")
	}
}
func TestPluginFailureRestoresPreviousVersion(t *testing.T) {
	e, s, r, _ := newExtensionsFixture(t)
	old := cpaplugin.Installation{Version: "v0.1.0", SHA256: "old"}
	s.WriteRuntimeState(context.Background(), cpaplugin.StatePrefix+"alpha", old)
	r.failRestart = true
	if _, err := e.UpdatePlugin(context.Background(), "alpha", io.Discard); err == nil {
		t.Fatal("expected failure")
	}
	var got cpaplugin.Installation
	s.ReadRuntimeState(context.Background(), cpaplugin.StatePrefix+"alpha", &got)
	if got != old || r.restarts != 2 {
		t.Fatalf("rollback=%+v restarts=%d", got, r.restarts)
	}
}
func TestPluginStoppedAndDisabledAccountsUntouched(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		e, s, r, _ := newExtensionsFixture(t)
		if disabled {
			s.accounts[0].GroupEnabled = false
		} else {
			r.running = false
		}
		if _, err := e.UpdatePlugin(context.Background(), "alpha", io.Discard); err == nil {
			t.Fatal("expected rejection")
		}
		if s.writes != 0 || r.restarts != 0 || r.validations != 0 {
			t.Fatal("inactive account mutated")
		}
	}
}
func TestPluginCompatibilityFailureDoesNotChangeLiveConfig(t *testing.T) {
	e, s, r, p := newExtensionsFixture(t)
	r.failValidation = true
	if _, err := e.UpdatePlugin(context.Background(), "alpha", io.Discard); err == nil {
		t.Fatal("expected failure")
	}
	if s.writes != 0 || r.restarts != 0 || len(p.stages) != 0 {
		t.Fatal("live configuration changed before compatibility passed")
	}
}
func TestPluginChecksumMismatchRejected(t *testing.T) {
	e, _, _, _ := newExtensionsFixture(t)
	original := e.Client.Transport
	e.Client.Transport = extensionRoundTrip(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, ".zip") {
			return extensionResponse([]byte("corrupt")), nil
		}
		return original.RoundTrip(req)
	})
	if _, _, err := e.pluginArtifact(context.Background(), "v0.2.0"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error=%v", err)
	}
}
func TestVersionChecksAreMetadataOnlyAndPreserveLastSuccess(t *testing.T) {
	e, s, r, _ := newExtensionsFixture(t)
	calls := 0
	e.Client.Transport = extensionRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if !strings.HasSuffix(req.URL.Path, "/releases/latest") {
			t.Fatalf("not metadata: %s", req.URL.Path)
		}
		return extensionResponse([]byte(`{"tag_name":"v7.3.9"}`)), nil
	})
	if err := e.checkVersions(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || r.restarts != 0 || r.validations != 0 {
		t.Fatal("unexpected effects")
	}
	e.checkVersions(context.Background(), false)
	if calls != 2 {
		t.Fatal("interval ignored")
	}
	e.Client.Transport = extensionRoundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	if e.checkVersions(context.Background(), true) == nil {
		t.Fatal("failure hidden")
	}
	var checks map[string]VersionCheck
	s.ReadRuntimeState(context.Background(), extensionChecksKey, &checks)
	if checks["cpa"].Version != "v7.3.9" || checks["cpa"].CheckedAt == 0 || checks["cpa"].Error == "" {
		t.Fatalf("previous check lost: %#v", checks)
	}
}
