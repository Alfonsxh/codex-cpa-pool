package runtimeops

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/cpaplugin"
	"github.com/google/renameio/v2"
)

const extensionChecksKey = "cpa_extension_version_checks"

type ExtensionStore interface {
	ReadSettings(context.Context) (map[string]any, error)
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSecret(context.Context, string) (string, bool, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
}
type ExtensionProjection interface{ RefreshAccounts(context.Context) error }
type ExtensionRuntime interface {
	List(context.Context) ([]Service, error)
	RestartRunningAccount(context.Context, string) error
	ValidatePlugin(context.Context, string, string) error
}
type VersionCheck struct {
	Version     string `json:"version"`
	URL         string `json:"url"`
	AttemptedAt int64  `json:"attempted_at"`
	CheckedAt   int64  `json:"checked_at"`
	Error       string `json:"error,omitempty"`
}
type PluginAccountStatus struct {
	Account      string                 `json:"account"`
	Running      bool                   `json:"running"`
	Selected     bool                   `json:"selected"`
	Enabled      bool                   `json:"enabled"`
	Installation cpaplugin.Installation `json:"installation"`
	Runtime      *TicketRuntimeStatus   `json:"runtime,omitempty"`
	Job          *PluginJobStatus       `json:"job,omitempty"`
}
type ExtensionStatus struct {
	BundledVersion string                  `json:"bundled_version"`
	Checks         map[string]VersionCheck `json:"checks"`
	Accounts       []PluginAccountStatus   `json:"accounts"`
	DesiredVersion string                  `json:"desired_version"`
}
type Extensions struct {
	managementHTTP *http.Client
	Lifetime       context.Context
	Root           string
	Store          ExtensionStore
	Runtime        ExtensionRuntime
	Projection     ExtensionProjection
	Client         *http.Client
	APIBase        string // tests only; production uses GitHub's fixed API.
	mu             sync.Mutex
}

func NewExtensions(root string, store ExtensionStore, runtime ExtensionRuntime, projection ExtensionProjection) *Extensions {
	return &Extensions{Root: root, Store: store, Runtime: runtime, Projection: projection, Client: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || !releaseHost(req.URL.Hostname()) {
			return errors.New("untrusted release redirect")
		}
		return nil
	}}}
}
func releaseHost(host string) bool {
	return host == "api.github.com" || host == "github.com" || host == "release-assets.githubusercontent.com" || host == "objects.githubusercontent.com"
}

// Run checks metadata only. It neither pulls images nor installs plugins nor
// calls the model provider. Store writes retain Admin's ownership fence.
func (e *Extensions) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = e.checkVersions(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (e *Extensions) CheckVersions(ctx context.Context, output io.Writer) (OperationResult, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	err := e.checkVersions(ctx, true)
	if err == nil {
		fmt.Fprintln(output, "Version metadata checked; no software was installed.")
	}
	return OperationResult{Action: "version-check", Target: "all", Services: []Service{}}, err
}
func (e *Extensions) checkVersions(ctx context.Context, force bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	settings, err := e.Store.ReadSettings(ctx)
	if err != nil {
		return err
	}
	checks := map[string]VersionCheck{}
	if _, err = e.Store.ReadRuntimeState(ctx, extensionChecksKey, &checks); err != nil {
		return err
	}
	hours := 6
	if v, ok := settings["software.check_interval_hours"]; ok {
		b, _ := json.Marshal(v)
		if json.Unmarshal(b, &hours) != nil || hours < 1 || hours > 168 {
			return errors.New("invalid version check interval")
		}
	}
	var failures []error
	changed := false
	for name, repo := range map[string]string{"cpa": "router-for-me/CLIProxyAPI", "codex-ticket": cpaplugin.Repository} {
		key := "software.cpa_auto_check"
		if name == "codex-ticket" {
			key = "software.plugin_auto_check"
		}
		if enabled, ok := settings[key].(bool); ok && !enabled && !force {
			continue
		}
		previous := checks[name]
		now := time.Now().Unix()
		if !force && previous.AttemptedAt > 0 && now-previous.AttemptedAt < int64(hours*3600) {
			continue
		}
		previous.AttemptedAt = now
		previous.Error = ""
		release, lookupErr := e.release(ctx, repo, "latest")
		if lookupErr != nil {
			previous.Error = "release_check_failed"
			failures = append(failures, fmt.Errorf("%s release check failed", name))
		} else {
			previous.Version = release.Tag
			previous.URL = "https://github.com/" + repo + "/releases/tag/" + release.Tag
			previous.CheckedAt = now
		}
		checks[name] = previous
		changed = true
	}
	if changed {
		if err := e.Store.WriteRuntimeState(ctx, extensionChecksKey, checks); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
func (e *Extensions) Status(ctx context.Context) (ExtensionStatus, error) {
	s := ExtensionStatus{Checks: map[string]VersionCheck{}, Accounts: []PluginAccountStatus{}}
	settings, err := e.Store.ReadSettings(ctx)
	if err != nil {
		return s, err
	}
	c, err := cpaplugin.Parse(settings)
	if err != nil {
		return s, err
	}
	s.DesiredVersion = c.Version
	if _, err = e.Store.ReadRuntimeState(ctx, extensionChecksKey, &s.Checks); err != nil {
		return s, err
	}
	accounts, err := e.Store.ReadAccounts(ctx)
	if err != nil {
		return s, err
	}
	running := map[string]bool{}
	if e.Runtime != nil {
		services, err := e.Runtime.List(ctx)
		if err != nil {
			return s, err
		}
		for _, service := range services {
			if service.State == "running" {
				running[strings.TrimPrefix(service.Service, "cliproxy-")] = true
			}
		}
	}
	for _, account := range accounts {
		row := PluginAccountStatus{Account: account.ID, Running: running[account.ID], Selected: c.Selected(account.ID), Enabled: account.GroupEnabled}
		if _, err = e.Store.ReadRuntimeState(ctx, cpaplugin.StatePrefix+account.ID, &row.Installation); err != nil {
			return s, err
		}
		row.Selected = row.Selected || row.Installation.Managed
		s.Accounts = append(s.Accounts, row)
	}
	return s, nil
}

type releaseMetadata struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

func (e *Extensions) release(ctx context.Context, repo, version string) (releaseMetadata, error) {
	var result releaseMetadata
	base := e.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	path := "latest"
	if version != "latest" {
		if !cpaplugin.VersionPattern.MatchString(version) {
			return result, errors.New("invalid release version")
		}
		path = "tags/" + version
	}
	body, err := e.fetch(ctx, base+"/repos/"+repo+"/releases/"+path, 2<<20)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(body, &result) != nil || !cpaplugin.VersionPattern.MatchString(result.Tag) || result.Draft || result.Prerelease {
		return result, errors.New("invalid stable release metadata")
	}
	if version != "latest" && result.Tag != version {
		return result, errors.New("release version mismatch")
	}
	return result, nil
}
func (e *Extensions) fetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("invalid release URL")
	}
	if e.APIBase == "" && (req.URL.Scheme != "https" || !releaseHost(req.URL.Hostname())) {
		return nil, errors.New("untrusted release URL")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "CCPA-software-check")
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, errors.New("release fetch failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("release response exceeded limit")
	}
	return body, nil
}

func (e *Extensions) pluginArtifact(ctx context.Context, version string) ([]byte, string, error) {
	release, err := e.release(ctx, cpaplugin.Repository, version)
	if err != nil {
		return nil, "", err
	}
	// Current reviewed publisher ships only linux/amd64. Never substitute a
	// different architecture or an unreviewed repository when absent.
	name := "codex-ticket_" + strings.TrimPrefix(version, "v") + "_linux_amd64.zip"
	var address, digest string
	for _, asset := range release.Assets {
		if asset.Name == name {
			address = asset.URL
			digest = asset.Digest
		}
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.Path != "/"+cpaplugin.Repository+"/releases/download/"+version+"/"+name {
		return nil, "", errors.New("plugin release asset unavailable")
	}
	expected, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || !strings.HasPrefix(digest, "sha256:") || len(expected) != 32 {
		return nil, "", errors.New("plugin release has no SHA256 digest")
	}
	archive, err := e.fetch(ctx, address, 32<<20)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(archive)
	if !bytes.Equal(sum[:], expected) {
		return nil, "", errors.New("plugin archive checksum mismatch")
	}
	library, err := extractPlugin(archive)
	if err != nil {
		return nil, "", err
	}
	sum = sha256.Sum256(library)
	return library, hex.EncodeToString(sum[:]), nil
}
func extractPlugin(archive []byte) ([]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	var result []byte
	for _, file := range z.File {
		if file.Name != "codex-ticket.so" {
			continue
		}
		if result != nil || !file.Mode().IsRegular() || file.UncompressedSize64 > 64<<20 {
			return nil, errors.New("invalid plugin library")
		}
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		result, err = io.ReadAll(io.LimitReader(r, 64<<20+1))
		r.Close()
		if err != nil || len(result) > 64<<20 {
			return nil, errors.New("plugin library too large")
		}
	}
	if len(result) < 20 || !bytes.Equal(result[:4], []byte{0x7f, 'E', 'L', 'F'}) || result[4] != 2 || result[5] != 1 || result[18] != 0x3e || result[19] != 0 {
		return nil, errors.New("plugin is not a linux amd64 ELF library")
	}
	return result, nil
}

// UpdatePlugin is called by the bounded runtime job pool under the common
// operation lock. It touches exactly one enabled, already-running account.
func (e *Extensions) UpdatePlugin(ctx context.Context, target string, output io.Writer) (OperationResult, error) {
	ctx, cancel := e.operationContext(ctx)
	defer cancel()
	result := OperationResult{Action: "plugin-update", Target: target, Services: []Service{}}
	if e.Runtime == nil || e.Projection == nil {
		return result, errors.New("plugin runtime unavailable")
	}
	settings, err := e.Store.ReadSettings(ctx)
	if err != nil {
		return result, err
	}
	config, err := cpaplugin.Parse(settings)
	if err != nil {
		return result, err
	}
	status, err := e.Status(ctx)
	if err != nil {
		return result, err
	}
	eligible := false
	for _, a := range status.Accounts {
		if a.Account == target && a.Enabled && a.Running {
			eligible = true
		}
	}
	if !eligible {
		return result, errors.New("select an enabled, running account")
	}
	// Ensure native plugin API exists before downloading or touching a config.
	var existing struct {
		PluginsEnabled bool `json:"plugins_enabled"`
	}
	if err = e.management(ctx, target, "/plugins", &existing); err != nil {
		return result, fmt.Errorf("CPA plugin API is unavailable; upgrade to a plugin-capable glibc build first: %w", err)
	}
	fmt.Fprintln(output, "Downloading the selected plugin release and verifying SHA256.")
	library, checksum, err := e.pluginArtifact(ctx, config.Version)
	if err != nil {
		return result, err
	}
	base := filepath.Join(e.Root, "configs", target)
	// Reject symlink parents; only files beneath the existing account config mount.
	for _, dir := range []string{base, filepath.Join(base, "plugins"), filepath.Join(base, "plugins", config.Version)} {
		if st, err := os.Lstat(dir); err == nil {
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return result, errors.New("unsafe plugin directory")
			}
		} else if os.IsNotExist(err) {
			if err = os.Mkdir(dir, 0o700); err != nil {
				return result, err
			}
		} else {
			return result, err
		}
	}
	path := filepath.Join(base, "plugins", config.Version, "codex-ticket.so")
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		return result, errors.New("unsafe plugin file")
	}
	if old, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(old, library) {
			return result, errors.New("same plugin version has different content; refusing replacement")
		}
	} else if !os.IsNotExist(err) {
		return result, err
	} else if err = renameio.WriteFile(path, library, 0o600); err != nil {
		return result, err
	}
	fmt.Fprintln(output, "Checking the plugin in an isolated container without credentials or network.")
	if err = e.Runtime.ValidatePlugin(ctx, target, config.Version); err != nil {
		return result, err
	}
	var previous cpaplugin.Installation
	if _, err = e.Store.ReadRuntimeState(ctx, cpaplugin.StatePrefix+target, &previous); err != nil {
		return result, err
	}
	next := cpaplugin.Installation{Version: config.Version, SHA256: checksum, InstalledAt: time.Now().Unix(), Staging: true, Managed: true}
	if err = e.Store.WriteRuntimeState(ctx, cpaplugin.StatePrefix+target, next); err != nil {
		return result, err
	}
	applyErr := e.activate(ctx, target, next)
	if applyErr == nil {
		fmt.Fprintln(output, "Plugin compatibility verified. Installed version is pinned for this account.")
		return result, nil
	}
	fmt.Fprintln(output, "Activation failed; restoring the previous plugin configuration.")
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	rollbackErr := e.Store.WriteRuntimeState(rollbackCtx, cpaplugin.StatePrefix+target, previous)
	if rollbackErr == nil {
		rollbackErr = e.Projection.RefreshAccounts(rollbackCtx)
	}
	if rollbackErr == nil {
		rollbackErr = e.Runtime.RestartRunningAccount(rollbackCtx, target)
	}
	return result, errors.Join(applyErr, rollbackErr)
}
func (e *Extensions) activate(ctx context.Context, target string, next cpaplugin.Installation) error {
	if err := e.Projection.RefreshAccounts(ctx); err != nil {
		return err
	}
	if err := e.Runtime.RestartRunningAccount(ctx, target); err != nil {
		return err
	}
	settings, err := e.Store.ReadSettings(ctx)
	if err != nil {
		return err
	}
	c, err := cpaplugin.Parse(settings)
	if err != nil {
		return err
	}
	if !c.Enabled {
		return e.commitInstallation(ctx, target, next)
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var response struct {
			Plugins []struct {
				ID         string `json:"id"`
				Registered bool   `json:"registered"`
				Metadata   struct {
					Version string `json:"version"`
				} `json:"metadata"`
			} `json:"plugins"`
		}
		if e.management(ctx, target, "/plugins", &response) == nil {
			for _, p := range response.Plugins {
				if p.ID == "codex-ticket" && p.Registered && strings.TrimPrefix(p.Metadata.Version, "v") == strings.TrimPrefix(next.Version, "v") {
					return e.commitInstallation(ctx, target, next)
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("plugin registration/version verification timed out")
		case <-ticker.C:
		}
	}
}
func (e *Extensions) management(ctx context.Context, target, path string, out any) error {
	key, found, err := e.Store.ReadSecret(ctx, "cpa_management_key")
	if err != nil || !found {
		return errors.New("CPA management credential unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://cliproxy-"+target+":8317/v0/management"+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if e.managementHTTP != nil {
		client = e.managementHTTP
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("CPA management connection failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("CPA management HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("invalid CPA management response")
	}
	return json.Unmarshal(data, out)
}

func (e *Extensions) commitInstallation(ctx context.Context, target string, next cpaplugin.Installation) error {
	next.Staging = false
	if err := e.Store.WriteRuntimeState(ctx, cpaplugin.StatePrefix+target, next); err != nil {
		return err
	}
	return e.Projection.RefreshAccounts(ctx)
}

func (e *Extensions) operationContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	if e.Lifetime == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(e.Lifetime, cancel)
	if e.Lifetime.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
