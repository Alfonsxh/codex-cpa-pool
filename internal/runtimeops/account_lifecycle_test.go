package runtimeops

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/bootstrap"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/containerd/errdefs"
	"github.com/go-resty/resty/v2"
	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	networktypes "github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

func TestAccountRuntimeAcceptsBootstrapManagementLayout(t *testing.T) {
	root := t.TempDir()
	if _, err := bootstrap.Initialize(context.Background(), bootstrap.Config{
		Root: root,
		ManagementKeyProvider: func() (string, error) {
			return "bootstrap-management-key", nil
		},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	writeRuntimeTestFile(t, filepath.Join(root, "configs", "alpha", "config.yaml"), "host: \"\"\nport: 8317\n")
	for _, directory := range []string{filepath.Join(root, "auth", "alpha"), filepath.Join(root, "logs", "alpha")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create account runtime directory: %v", err)
		}
	}
	runtime := newAccountRuntimeForTest(
		t,
		newRuntimeTestManager(t, &fakeAccountLifecycleClient{}),
		root,
		&recordingRoundTripper{status: http.StatusOK},
	)
	options, err := runtime.createOptions(context.Background(), controlplane.Account{ID: "alpha", Port: 18320})
	if err != nil {
		t.Fatalf("createOptions with bootstrapped layout: %v", err)
	}
	expected := filepath.Join(root, "management", "config", "static")
	found := false
	for _, mounted := range options.HostConfig.Mounts {
		if mounted.Source == expected && mounted.Target == "/CLIProxyAPI/configs/static" && mounted.ReadOnly {
			found = true
		}
	}
	if !found {
		t.Fatalf("bootstrapped management static mount missing: %#v", options.HostConfig.Mounts)
	}
}

func TestAccountRuntimeCreatesExactMobySpecProbesAndRollsBackCandidate(t *testing.T) {
	root := newRuntimeRoot(t, "gamma")
	client := &fakeAccountLifecycleClient{containers: runtimeFixtureContainers()}
	client.containers[0].Ports = []containertypes.PortSummary{{PrivatePort: 8317, PublicPort: 18318, Type: "tcp"}}
	manager := newRuntimeTestManager(t, client)
	probe := &recordingRoundTripper{status: http.StatusOK}
	runtime := newAccountRuntimeForTest(t, manager, root, probe)
	ports, err := runtime.ReservedHostPorts(context.Background())
	if err != nil {
		t.Fatalf("ReservedHostPorts: %v", err)
	}
	if _, found := ports[18318]; !found {
		t.Fatalf("reserved ports = %#v", ports)
	}
	transition, err := runtime.PrepareCreate(context.Background(), controlplane.Account{
		ID: "gamma", Port: 18320, GroupEnabled: true,
	})
	if err != nil {
		t.Fatalf("PrepareCreate: %v", err)
	}
	if len(client.creates) != 1 {
		t.Fatalf("create calls = %d", len(client.creates))
	}
	created := client.creates[0]
	if created.Name != "fixture-gamma" || created.Config.Image != "registry.example.com/cpa@sha256:immutable" ||
		len(created.Config.Env) != 1 || created.Config.Env[0] != "TZ=UTC" ||
		created.Config.Labels[composeProjectLabel] != "fixture-project" ||
		created.Config.Labels[composeServiceLabel] != "cliproxy-gamma" ||
		created.HostConfig.NetworkMode != "fixture-backend" || len(created.HostConfig.Mounts) != 4 {
		t.Fatalf("created options = %#v", created)
	}
	if mounted := created.HostConfig.Mounts[0]; mounted.Source != filepath.Join(root, "configs", "gamma") ||
		mounted.Target != "/CLIProxyAPI/account-config" || !mounted.ReadOnly {
		t.Fatalf("directory config mount = %#v", mounted)
	}
	port := created.HostConfig.PortBindings[networkPort(t, accountContainerPort)]
	if len(port) != 1 || port[0].HostPort != "18320" || port[0].HostIP != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("created port binding = %#v", port)
	}
	if probe.authorization != "Bearer cpa_internal_fixture" ||
		probe.url != "http://cliproxy-gamma:8317/v1/models" {
		t.Fatalf("probe = auth %q url %q", probe.authorization, probe.url)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "created-1" {
		t.Fatalf("removed candidates = %#v", client.removed)
	}
}

func TestAccountRuntimeCreatesFirstAccountAfterAuthenticatedManagementProbe(t *testing.T) {
	root := newRuntimeRoot(t, "gamma")
	client := &fakeAccountLifecycleClient{}
	manager := newRuntimeTestManager(t, client)
	probe := &recordingRoundTripper{status: http.StatusOK}
	runtime := newAccountRuntimeForTest(t, manager, root, probe)
	runtime.store.(*fakeAccountRuntimeStore).keys = map[string]controlplane.InternalKey{}

	transition, err := runtime.PrepareCreate(context.Background(), controlplane.Account{
		ID: "gamma", Port: 18320, GroupEnabled: true,
	})
	if err != nil {
		t.Fatalf("PrepareCreate without users: %v", err)
	}
	if probe.authorization != "Bearer management-key-for-tests" ||
		probe.url != "http://cliproxy-gamma:8317/v0/management/auth-files" {
		t.Fatalf("bootstrap probe = auth %q url %q", probe.authorization, probe.url)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestAccountRuntimeRejectsUnauthorizedFirstAccountManagementProbe(t *testing.T) {
	root := newRuntimeRoot(t, "gamma")
	client := &fakeAccountLifecycleClient{}
	manager := newRuntimeTestManager(t, client)
	runtime := newAccountRuntimeForTest(
		t, manager, root, &recordingRoundTripper{status: http.StatusUnauthorized},
	)
	runtime.store.(*fakeAccountRuntimeStore).keys = map[string]controlplane.InternalKey{}

	if _, err := runtime.PrepareCreate(context.Background(), controlplane.Account{
		ID: "gamma", Port: 18320, GroupEnabled: true,
	}); err == nil || !strings.Contains(err.Error(), "expected 200") {
		t.Fatalf("PrepareCreate with rejected management probe = %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "created-1" {
		t.Fatalf("rejected management candidate removals = %#v", client.removed)
	}
}

func TestAccountRuntimeRejectsFirstAccountWithoutManagementCredentialAndRemovesCandidate(t *testing.T) {
	root := newRuntimeRoot(t, "gamma")
	client := &fakeAccountLifecycleClient{}
	manager := newRuntimeTestManager(t, client)
	probe := &recordingRoundTripper{status: http.StatusOK}
	runtime := newAccountRuntimeForTest(t, manager, root, probe)
	store := runtime.store.(*fakeAccountRuntimeStore)
	store.keys = map[string]controlplane.InternalKey{}
	delete(store.secrets, "cpa_management_key")

	if _, err := runtime.PrepareCreate(context.Background(), controlplane.Account{
		ID: "gamma", Port: 18320, GroupEnabled: true,
	}); err == nil || !strings.Contains(err.Error(), "valid management credential") {
		t.Fatalf("PrepareCreate without management credential = %v", err)
	}
	if probe.url != "" {
		t.Fatalf("unexpected probe without management credential = %q", probe.url)
	}
	if len(client.removed) != 1 || client.removed[0] != "created-1" {
		t.Fatalf("missing-credential candidate removals = %#v", client.removed)
	}
}

func TestAccountRuntimeUsesLatestUpdateChannelBeforeAnImageIsApplied(t *testing.T) {
	runtime := newAccountRuntimeForTest(
		t,
		newRuntimeTestManager(t, &fakeAccountLifecycleClient{}),
		newRuntimeRoot(t),
		&recordingRoundTripper{status: http.StatusOK},
	)
	store := runtime.store.(*fakeAccountRuntimeStore)
	store.imageState = map[string]any{}

	image, listenAddress, err := runtime.imageAndListenAddress(context.Background())
	if err != nil {
		t.Fatalf("imageAndListenAddress: %v", err)
	}
	if image != DefaultCPAImageUpdateChannel || listenAddress != "127.0.0.1" {
		t.Fatalf("image/listen = %q/%q", image, listenAddress)
	}
}

func TestAccountRuntimeUpdateRollbackRecreatesPreviousContainer(t *testing.T) {
	root := newRuntimeRoot(t, "alpha", "gamma")
	client := &fakeAccountLifecycleClient{containers: []containertypes.Summary{
		accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
	}}
	manager := newRuntimeTestManager(t, client)
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
	transition, err := runtime.PrepareUpdate(context.Background(),
		controlplane.Account{ID: "alpha", Port: 18318},
		controlplane.Account{ID: "gamma", Port: 18318},
	)
	if err != nil {
		t.Fatalf("PrepareUpdate: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "old-alpha" ||
		len(client.creates) != 1 || client.creates[0].Name != "fixture-gamma" {
		t.Fatalf("prepared update: removed=%#v creates=%#v", client.removed, client.creates)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(client.creates) != 2 || client.creates[1].Name != "fixture-alpha" ||
		client.creates[1].Config.Labels[composeServiceLabel] != "cliproxy-alpha" {
		t.Fatalf("restored create options = %#v", client.creates)
	}
}

func TestAccountRuntimeDeleteDefersRemovalUntilCommitAndCanRollbackAfterCommit(t *testing.T) {
	root := newRuntimeRoot(t, "alpha")
	client := &fakeAccountLifecycleClient{containers: []containertypes.Summary{
		accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
	}}
	manager := newRuntimeTestManager(t, client)
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
	transition, err := runtime.PrepareDelete(context.Background(), controlplane.Account{ID: "alpha", Port: 18318})
	if err != nil {
		t.Fatalf("PrepareDelete: %v", err)
	}
	if len(client.removed) != 0 {
		t.Fatalf("delete removed container before snapshot commit: %#v", client.removed)
	}
	if err := transition.Commit(context.Background()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "old-alpha" {
		t.Fatalf("delete commit removed = %#v", client.removed)
	}
	if err := transition.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback after commit: %v", err)
	}
	if len(client.creates) != 1 || client.creates[0].Name != "fixture-alpha" {
		t.Fatalf("delete rollback did not recreate previous account: %#v", client.creates)
	}
}

func TestAccountRuntimeProbeFailureRemovesFailedCandidate(t *testing.T) {
	root := newRuntimeRoot(t, "gamma")
	client := &fakeAccountLifecycleClient{}
	manager := newRuntimeTestManager(t, client)
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusServiceUnavailable})
	runtime.probeTimeout = time.Second
	if _, err := runtime.PrepareCreate(context.Background(), controlplane.Account{ID: "gamma", Port: 18320}); err == nil {
		t.Fatal("PrepareCreate unexpectedly accepted a failed model probe")
	}
	if len(client.removed) != 1 || client.removed[0] != "created-1" {
		t.Fatalf("failed candidate removals = %#v", client.removed)
	}
}

func TestAccountRuntimeReconcileReplacesInterruptedRenameCandidate(t *testing.T) {
	root := newRuntimeRoot(t, "alpha", "gamma")
	client := &fakeAccountLifecycleClient{containers: []containertypes.Summary{
		accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
	}}
	runtime := newAccountRuntimeForTest(
		t,
		newRuntimeTestManager(t, client),
		root,
		&recordingRoundTripper{status: http.StatusOK},
	)
	if err := runtime.ReconcileAccount(
		context.Background(),
		"alpha",
		&controlplane.Account{ID: "gamma", Port: 18318},
	); err != nil {
		t.Fatalf("ReconcileAccount: %v", err)
	}
	if len(client.removed) != 1 || client.removed[0] != "old-alpha" ||
		len(client.creates) != 1 || client.creates[0].Name != "fixture-gamma" ||
		len(client.started) != 1 {
		t.Fatalf("reconcile calls: removed=%#v creates=%#v started=%#v", client.removed, client.creates, client.started)
	}
}

func TestAccountRuntimeMigratesLegacySingleFileMountAfterDrain(t *testing.T) {
	root := newRuntimeRoot(t, "alpha")
	legacy := filepath.Join(root, "configs", "alpha.yaml")
	writeRuntimeTestFile(t, legacy, "legacy: true\n")
	client := &fakeAccountLifecycleClient{
		containers: []containertypes.Summary{
			accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
		},
		mounts: map[string][]containertypes.MountPoint{
			"old-alpha": {{Type: mount.TypeBind, Source: legacy, Destination: "/CLIProxyAPI/configs/alpha.yaml", RW: false}},
		},
	}
	manager := newRuntimeTestManager(t, client)
	manager.accounts = staticAccountCatalog{accounts: []controlplane.Account{{ID: "alpha", Port: 18318}}}
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
	drainer := &recordingAccountDrainer{}
	runtime.drainer = drainer

	migrated, err := runtime.MigrateLegacyConfigMounts(context.Background())
	if err != nil {
		t.Fatalf("MigrateLegacyConfigMounts: %v", err)
	}
	if len(migrated) != 1 || migrated[0] != "alpha" || len(drainer.accounts) != 1 || drainer.accounts[0] != "alpha" {
		t.Fatalf("migration result=%#v drains=%#v", migrated, drainer.accounts)
	}
	if len(client.removed) != 1 || client.removed[0] != "old-alpha" || len(client.creates) != 1 {
		t.Fatalf("migration runtime calls: removed=%#v creates=%#v", client.removed, client.creates)
	}
	mounted := client.creates[0].HostConfig.Mounts[0]
	if mounted.Source != filepath.Join(root, "configs", "alpha") ||
		mounted.Target != "/CLIProxyAPI/account-config" || !mounted.ReadOnly {
		t.Fatalf("migrated config mount = %#v", mounted)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy config remains after verified migration: %v", err)
	}
}

func TestAccountRuntimeMigrationFailureRestoresLegacyContainerAndFile(t *testing.T) {
	root := newRuntimeRoot(t, "alpha")
	legacy := filepath.Join(root, "configs", "alpha.yaml")
	writeRuntimeTestFile(t, legacy, "legacy: true\n")
	client := &fakeAccountLifecycleClient{
		containers: []containertypes.Summary{
			accountContainerSummary("old-alpha", "alpha", containertypes.StateRunning, 18318),
		},
		mounts: map[string][]containertypes.MountPoint{
			"old-alpha": {{Type: mount.TypeBind, Source: legacy, Destination: "/CLIProxyAPI/configs/alpha.yaml", RW: false}},
		},
		createErrors: map[int]error{1: errors.New("fixture directory mount create failure")},
	}
	manager := newRuntimeTestManager(t, client)
	manager.accounts = staticAccountCatalog{accounts: []controlplane.Account{{ID: "alpha", Port: 18318}}}
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
	runtime.drainer = &recordingAccountDrainer{}

	if _, err := runtime.MigrateLegacyConfigMounts(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "fixture directory mount create failure") {
		t.Fatalf("migration failure = %v", err)
	}
	if len(client.creates) != 1 || client.creates[0].HostConfig.Mounts[0].Source != legacy ||
		client.creates[0].HostConfig.Mounts[0].Target != "/CLIProxyAPI/configs/alpha.yaml" {
		t.Fatalf("legacy rollback create = %#v", client.creates)
	}
	if got, err := os.ReadFile(legacy); err != nil || string(got) != "legacy: true\n" {
		t.Fatalf("legacy config after rollback = %q, %v", got, err)
	}
}

func TestAccountRuntimeLoginUsesIsolatedOneOffContainerAndRequiresOAuthChange(t *testing.T) {
	root := newRuntimeRoot(t, "alpha")
	client := &fakeAccountLifecycleClient{
		containers:  runtimeFixtureContainers(),
		oauthOutput: "Codex device URL: https://auth.example.test/device\nCodex device code: ABCD-EFGH\n",
	}
	manager := newRuntimeTestManager(t, client)
	manager.accounts = staticAccountCatalog{accounts: []controlplane.Account{{ID: "alpha", Port: 18318}}}
	runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
	client.oauthStartHook = func() {
		writeRuntimeTestFile(t, filepath.Join(root, "auth", "alpha", "codex.json"), `{"access_token":"new"}`)
	}
	var output strings.Builder
	result, err := runtime.Login(context.Background(), "alpha", &output)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Action != "login" || result.Target != "alpha" ||
		!strings.Contains(output.String(), "Codex device code: ABCD-EFGH") {
		t.Fatalf("OAuth result=%#v output=%q", result, output.String())
	}
	if len(client.creates) != 1 {
		t.Fatalf("OAuth create calls = %d", len(client.creates))
	}
	created := client.creates[0]
	if !strings.HasPrefix(created.Name, "fixture-oauth-alpha-") ||
		created.Config.Image != "registry.example.com/cpa@sha256:immutable" ||
		len(created.Config.Env) != 1 || created.Config.Env[0] != "TZ=UTC" ||
		strings.Join(created.Config.Cmd, " ") != "./CLIProxyAPI -config /CLIProxyAPI/account-config/config.yaml -codex-device-login -no-browser" {
		t.Fatalf("OAuth create identity = %#v", created)
	}
	if created.Config.ExposedPorts != nil || len(created.HostConfig.PortBindings) != 0 ||
		created.HostConfig.RestartPolicy.Name != containertypes.RestartPolicyDisabled ||
		created.Config.Labels[composeProjectLabel] != "" || created.Config.Labels[composeServiceLabel] != "" ||
		created.Config.Labels["com.docker.compose.oneoff"] != "" ||
		created.Config.Labels["io.codex-cpa.operation"] != "oauth-device-login" {
		t.Fatalf("OAuth container was not isolated from the service directory: %#v", created)
	}
	if len(created.HostConfig.Mounts) != 4 || len(client.stopped) != 0 || len(client.restarted) != 0 ||
		len(client.removed) != 1 || client.removed[0] != "created-1" {
		t.Fatalf("OAuth lifecycle calls: mounts=%d stopped=%#v restarted=%#v removed=%#v", len(created.HostConfig.Mounts), client.stopped, client.restarted, client.removed)
	}
}

func TestAccountRuntimeLoginRejectsExitFailureAndUnchangedOAuth(t *testing.T) {
	for _, test := range []struct {
		name       string
		exitCode   int64
		mutateAuth bool
		want       string
	}{
		{name: "non-zero exit", exitCode: 7, mutateAuth: true, want: "exited with code 7"},
		{name: "unchanged auth", exitCode: 0, mutateAuth: false, want: "No new or updated authentication file was detected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newRuntimeRoot(t, "alpha")
			client := &fakeAccountLifecycleClient{oauthOutput: "device flow finished\n", oauthExitCode: test.exitCode}
			manager := newRuntimeTestManager(t, client)
			manager.accounts = staticAccountCatalog{accounts: []controlplane.Account{{ID: "alpha", Port: 18318}}}
			runtime := newAccountRuntimeForTest(t, manager, root, &recordingRoundTripper{status: http.StatusOK})
			if test.mutateAuth {
				client.oauthStartHook = func() {
					writeRuntimeTestFile(t, filepath.Join(root, "auth", "alpha", "codex.json"), `{"access_token":"new"}`)
				}
			}
			_, err := runtime.Login(context.Background(), "alpha", io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Login error = %v, want %q", err, test.want)
			}
			if len(client.removed) != 1 || client.removed[0] != "created-1" {
				t.Fatalf("failed OAuth container was not removed: %#v", client.removed)
			}
		})
	}
}

func newAccountRuntimeForTest(
	t *testing.T,
	manager *Manager,
	root string,
	transport http.RoundTripper,
) *AccountRuntime {
	t.Helper()
	store := &fakeAccountRuntimeStore{
		settings: map[string]any{"accounts.listen_address": "127.0.0.1"},
		imageState: map[string]any{"applied": map[string]any{
			"resolved_ref": "registry.example.com/cpa@sha256:immutable",
		}},
		keys: map[string]controlplane.InternalKey{
			"alice@example.com": {Key: "cpa_internal_fixture", Status: "active"},
		},
		secrets: map[string]string{"cpa_management_key": "management-key-for-tests"},
	}
	runtime, err := NewAccountRuntime(manager, store, AccountRuntimeConfig{
		Root: root, NetworkName: "fixture-backend", InstanceName: "fixture",
		ProbeTimeout: time.Second,
		HTTPClient:   resty.NewWithClient(&http.Client{Transport: transport, Timeout: time.Second}),
	})
	if err != nil {
		t.Fatalf("NewAccountRuntime: %v", err)
	}
	return runtime
}

func newRuntimeRoot(t *testing.T, accounts ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, account := range accounts {
		for path, directory := range map[string]bool{
			filepath.Join(root, "configs", account, "config.yaml"): false,
			filepath.Join(root, "auth", account):                   true,
			filepath.Join(root, "logs", account):                   true,
		} {
			if directory {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatalf("create runtime directory: %v", err)
				}
			} else {
				writeRuntimeTestFile(t, path, "host: \"\"\nport: 8317\n")
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "management", "config", "static"), 0o700); err != nil {
		t.Fatalf("create management static directory: %v", err)
	}
	return root
}

func accountContainerSummary(id, account string, state containertypes.ContainerState, publicPort uint16) containertypes.Summary {
	return containertypes.Summary{
		ID: id, Names: []string{"/fixture-" + account}, Image: "fixture/cpa:v1",
		State: state, Status: string(state), Ports: []containertypes.PortSummary{{
			PrivatePort: 8317, PublicPort: publicPort, Type: "tcp",
		}},
		Labels: map[string]string{
			composeProjectLabel: "fixture-project", composeServiceLabel: "cliproxy-" + account,
		},
	}
}

type fakeAccountLifecycleClient struct {
	containers     []containertypes.Summary
	mounts         map[string][]containertypes.MountPoint
	creates        []dockerclient.ContainerCreateOptions
	createErrors   map[int]error
	started        []string
	stopped        []string
	restarted      []string
	removed        []string
	createCount    int
	oauthOutput    string
	oauthExitCode  int64
	oauthWaitError error
	oauthStartHook func()
	oauthStream    net.Conn
}

func (client *fakeAccountLifecycleClient) ContainerList(
	_ context.Context,
	_ dockerclient.ContainerListOptions,
) (dockerclient.ContainerListResult, error) {
	return dockerclient.ContainerListResult{Items: append([]containertypes.Summary(nil), client.containers...)}, nil
}

func (client *fakeAccountLifecycleClient) ContainerCreate(
	_ context.Context,
	options dockerclient.ContainerCreateOptions,
) (dockerclient.ContainerCreateResult, error) {
	client.createCount++
	if err := client.createErrors[client.createCount]; err != nil {
		return dockerclient.ContainerCreateResult{}, err
	}
	id := "created-" + strconv.Itoa(client.createCount)
	client.creates = append(client.creates, options)
	client.containers = append(client.containers, containertypes.Summary{
		ID: id, Names: []string{"/" + options.Name}, Image: options.Config.Image,
		State: containertypes.StateCreated, Status: "created", Labels: options.Config.Labels,
	})
	if client.mounts == nil {
		client.mounts = make(map[string][]containertypes.MountPoint)
	}
	for _, mounted := range options.HostConfig.Mounts {
		client.mounts[id] = append(client.mounts[id], containertypes.MountPoint{
			Type: mounted.Type, Source: mounted.Source, Destination: mounted.Target, RW: !mounted.ReadOnly,
		})
	}
	return dockerclient.ContainerCreateResult{ID: id}, nil
}

func (client *fakeAccountLifecycleClient) ContainerInspect(
	_ context.Context,
	id string,
	_ dockerclient.ContainerInspectOptions,
) (dockerclient.ContainerInspectResult, error) {
	for _, candidate := range client.containers {
		if candidate.ID == id {
			return dockerclient.ContainerInspectResult{Container: containertypes.InspectResponse{
				ID: id, Image: candidate.Image, Config: &containertypes.Config{Image: candidate.Image},
				State:  &containertypes.State{Running: candidate.State == containertypes.StateRunning},
				Mounts: append([]containertypes.MountPoint(nil), client.mounts[id]...),
			}}, nil
		}
	}
	return dockerclient.ContainerInspectResult{}, fmt.Errorf("%w: fixture container", errdefs.ErrNotFound)
}

func (client *fakeAccountLifecycleClient) ContainerStart(
	_ context.Context,
	id string,
	_ dockerclient.ContainerStartOptions,
) (dockerclient.ContainerStartResult, error) {
	client.started = append(client.started, id)
	client.setState(id, containertypes.StateRunning)
	if client.oauthStream != nil {
		stream := client.oauthStream
		client.oauthStream = nil
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(client.oauthOutput)))
		_, _ = stream.Write(header)
		_, _ = io.WriteString(stream, client.oauthOutput)
		_ = stream.Close()
		if client.oauthStartHook != nil {
			client.oauthStartHook()
		}
	}
	return dockerclient.ContainerStartResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerAttach(
	_ context.Context,
	_ string,
	_ dockerclient.ContainerAttachOptions,
) (dockerclient.ContainerAttachResult, error) {
	reader, writer := net.Pipe()
	client.oauthStream = writer
	return dockerclient.ContainerAttachResult{HijackedResponse: dockerclient.NewHijackedResponse(reader, "application/vnd.docker.raw-stream")}, nil
}

func (client *fakeAccountLifecycleClient) ContainerWait(
	_ context.Context,
	_ string,
	_ dockerclient.ContainerWaitOptions,
) dockerclient.ContainerWaitResult {
	results := make(chan containertypes.WaitResponse, 1)
	errors := make(chan error, 1)
	if client.oauthWaitError != nil {
		errors <- client.oauthWaitError
	} else {
		results <- containertypes.WaitResponse{StatusCode: client.oauthExitCode}
	}
	return dockerclient.ContainerWaitResult{Result: results, Error: errors}
}

func (client *fakeAccountLifecycleClient) ContainerStop(
	_ context.Context,
	id string,
	_ dockerclient.ContainerStopOptions,
) (dockerclient.ContainerStopResult, error) {
	client.stopped = append(client.stopped, id)
	client.setState(id, containertypes.StateExited)
	return dockerclient.ContainerStopResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerRestart(
	_ context.Context,
	id string,
	_ dockerclient.ContainerRestartOptions,
) (dockerclient.ContainerRestartResult, error) {
	client.restarted = append(client.restarted, id)
	client.setState(id, containertypes.StateRunning)
	return dockerclient.ContainerRestartResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerRemove(
	_ context.Context,
	id string,
	_ dockerclient.ContainerRemoveOptions,
) (dockerclient.ContainerRemoveResult, error) {
	client.removed = append(client.removed, id)
	filtered := client.containers[:0]
	for _, candidate := range client.containers {
		if candidate.ID != id {
			filtered = append(filtered, candidate)
		}
	}
	client.containers = filtered
	delete(client.mounts, id)
	return dockerclient.ContainerRemoveResult{}, nil
}

func (client *fakeAccountLifecycleClient) ContainerLogs(
	context.Context,
	string,
	dockerclient.ContainerLogsOptions,
) (dockerclient.ContainerLogsResult, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (client *fakeAccountLifecycleClient) setState(id string, state containertypes.ContainerState) {
	for index := range client.containers {
		if client.containers[index].ID == id {
			client.containers[index].State = state
			client.containers[index].Status = string(state)
		}
	}
}

type fakeAccountRuntimeStore struct {
	settings   map[string]any
	imageState map[string]any
	keys       map[string]controlplane.InternalKey
	secrets    map[string]string
}

func (store *fakeAccountRuntimeStore) ReadSettings(context.Context) (map[string]any, error) {
	return store.settings, nil
}

func (store *fakeAccountRuntimeStore) ReadRuntimeState(_ context.Context, _ string, target any) (bool, error) {
	raw, _ := json.Marshal(store.imageState)
	return true, json.Unmarshal(raw, target)
}

func (store *fakeAccountRuntimeStore) ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error) {
	return store.keys, nil
}

func (store *fakeAccountRuntimeStore) ReadSecret(_ context.Context, name string) (string, bool, error) {
	value, found := store.secrets[name]
	return value, found, nil
}

type recordingRoundTripper struct {
	status        int
	authorization string
	url           string
}

type recordingAccountDrainer struct {
	accounts []string
	err      error
}

func (drainer *recordingAccountDrainer) WaitAccountDrained(_ context.Context, accountID string) error {
	drainer.accounts = append(drainer.accounts, accountID)
	return drainer.err
}

func (transport *recordingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.authorization = request.Header.Get("Authorization")
	transport.url = request.URL.String()
	return &http.Response{
		StatusCode: transport.status,
		Status:     strconv.Itoa(transport.status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    request,
	}, nil
}

func networkPort(t *testing.T, value string) networktypes.Port {
	t.Helper()
	port, err := networktypes.ParsePort(value)
	if err != nil {
		t.Fatalf("parse network port: %v", err)
	}
	return port
}

func writeRuntimeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create runtime test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write runtime test file: %v", err)
	}
}
