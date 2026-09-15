package runtimeops

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/moby/moby/api/pkg/stdcopy"
	containertypes "github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

func TestManagerUsesComposeLabelsAndRestrictsMutatingTargets(t *testing.T) {
	client := &fakeDockerClient{containers: runtimeFixtureContainers()}
	manager := newRuntimeTestManager(t, client)
	services, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(services) != 5 || services[0].Service != "admin" || services[1].Service != "cliproxy-alpha" ||
		services[1].ContainerID != "aaaaaaaaaaaa" || services[1].Name != "cpa-alpha" {
		t.Fatalf("services = %#v", services)
	}
	if len(client.listLabels) != 1 || client.listLabels[0] != composeProjectLabel+"=fixture-project" {
		t.Fatalf("Docker label filter = %#v", client.listLabels)
	}

	stopped, err := manager.Stop(context.Background(), "alpha")
	if err != nil || len(stopped.Services) != 1 || stopped.Services[0].Service != "cliproxy-alpha" ||
		len(client.stopped) != 1 || client.stopped[0] != strings.Repeat("a", 64) {
		t.Fatalf("Stop alpha = (%#v, %v), calls=%#v", stopped, err, client.stopped)
	}
	client.stopped = nil
	all, err := manager.Stop(context.Background(), "all")
	if err != nil {
		t.Fatalf("Stop all: %v", err)
	}
	if len(all.Services) != 3 || strings.Join(client.stopped, ",") != strings.Join([]string{
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("w", 64),
	}, ",") {
		t.Fatalf("Stop all services = %#v, calls=%#v", all.Services, client.stopped)
	}
	if _, err := manager.Stop(context.Background(), "edge"); !errors.Is(err, ErrRuntimeTarget) {
		t.Fatalf("mutating Edge error = %v", err)
	}
	if _, err := manager.Restart(context.Background(), "admin"); !errors.Is(err, ErrRuntimeTarget) {
		t.Fatalf("mutating Admin error = %v", err)
	}
}

func TestContainerHealthPreservesComposeStatusSemantics(t *testing.T) {
	for status, expected := range map[string]string{
		"Up 8 days (healthy)":             "healthy",
		"Up 3 seconds (health: starting)": "starting",
		"Up 1 minute (unhealthy)":         "unhealthy",
		"Exited (1) 2 hours ago":          "",
	} {
		if actual := containerHealth(status); actual != expected {
			t.Fatalf("containerHealth(%q) = %q, want %q", status, actual, expected)
		}
	}
}

func TestManagerStartsStoppedServicesAndRejectsDuplicateComposeService(t *testing.T) {
	containers := runtimeFixtureContainers()
	containers[1].State = "exited"
	client := &fakeDockerClient{containers: containers}
	manager := newRuntimeTestManager(t, client)
	if _, err := manager.Start(context.Background(), "beta"); err != nil {
		t.Fatalf("Start beta: %v", err)
	}
	if len(client.started) != 1 || client.started[0] != strings.Repeat("b", 64) {
		t.Fatalf("started = %#v", client.started)
	}
	client.containers = append(client.containers, containertypes.Summary{
		ID: strings.Repeat("d", 64), Labels: map[string]string{
			composeProjectLabel: "fixture-project", composeServiceLabel: "web",
		},
	})
	if _, err := manager.List(context.Background()); !errors.Is(err, ErrRuntimeConflict) {
		t.Fatalf("duplicate service error = %v", err)
	}
}

func TestManagerReadsBoundedRedactedMultiplexedLogs(t *testing.T) {
	var multiplexed bytes.Buffer
	writeMultiplexedLog(&multiplexed, stdcopy.Stdout, []byte("Authorization: Bearer secret-token\napi=cpa_user_0123456789abcdef\n"))
	writeMultiplexedLog(&multiplexed, stdcopy.Stderr, []byte(`{"access_token":"oauth-secret"}`+"\n"+strings.Repeat("x", 128)))
	client := &fakeDockerClient{
		containers: runtimeFixtureContainers(),
		logs:       map[string][]byte{strings.Repeat("a", 64): multiplexed.Bytes()},
	}
	manager := newRuntimeTestManager(t, client)
	manager.logLimit = 96
	result, err := manager.Logs(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !result.Truncated || !strings.Contains(result.Output, "Bearer [REDACTED]") ||
		!strings.Contains(result.Output, "key_[REDACTED]") || strings.Contains(result.Output, "secret-token") ||
		strings.Contains(result.Output, "0123456789abcdef") || !strings.Contains(result.Output, "Output truncated") {
		t.Fatalf("logs = %#v", result)
	}
	if client.logOptions.Tail != "200" || !client.logOptions.ShowStdout || !client.logOptions.ShowStderr {
		t.Fatalf("log options = %#v", client.logOptions)
	}
}

func TestManagerSanitizesCredentialThatCrossesTheOutputBoundary(t *testing.T) {
	var multiplexed bytes.Buffer
	writeMultiplexedLog(
		&multiplexed,
		stdcopy.Stdout,
		[]byte(strings.Repeat("x", 24)+" cpa_user_0123456789abcdef\n"),
	)
	client := &fakeDockerClient{
		containers: runtimeFixtureContainers(),
		logs:       map[string][]byte{strings.Repeat("a", 64): multiplexed.Bytes()},
	}
	manager := newRuntimeTestManager(t, client)
	manager.logLimit = 32
	result, err := manager.Logs(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !result.Truncated || strings.Contains(result.Output, "cpa_user_") || strings.Contains(result.Output, "0123456789abcdef") {
		t.Fatalf("boundary logs = %#v", result)
	}
}

func TestReadOnlyDockerClientCannotExposeMutationInterfaces(t *testing.T) {
	client := &readOnlyDockerClient{}
	if _, err := client.ContainerStart(context.Background(), "fixture", dockerclient.ContainerStartOptions{}); !errors.Is(err, ErrRuntimeReadOnly) {
		t.Fatalf("read-only start error = %v", err)
	}
	if _, err := client.ContainerStop(context.Background(), "fixture", dockerclient.ContainerStopOptions{}); !errors.Is(err, ErrRuntimeReadOnly) {
		t.Fatalf("read-only stop error = %v", err)
	}
	if _, err := client.ContainerRestart(context.Background(), "fixture", dockerclient.ContainerRestartOptions{}); !errors.Is(err, ErrRuntimeReadOnly) {
		t.Fatalf("read-only restart error = %v", err)
	}
	var candidate any = client
	if _, ok := candidate.(AccountLifecycleDockerClient); ok {
		t.Fatal("read-only Docker client unexpectedly implements account lifecycle operations")
	}
	if _, ok := candidate.(imageDockerClient); !ok {
		t.Fatal("read-only Docker client must retain image inspection")
	}
}

func runtimeFixtureContainers() []containertypes.Summary {
	container := func(id string, service string, state string) containertypes.Summary {
		return containertypes.Summary{
			ID: strings.Repeat(id, 64), Names: []string{"/cpa-" + strings.TrimPrefix(service, "cliproxy-")},
			Image: "fixture/" + service + ":v1", State: containertypes.ContainerState(state), Status: state,
			Labels: map[string]string{composeProjectLabel: "fixture-project", composeServiceLabel: service},
		}
	}
	return []containertypes.Summary{
		container("a", "cliproxy-alpha", "running"),
		container("b", "cliproxy-beta", "running"),
		container("w", "web", "running"),
		container("e", "edge", "running"),
		container("m", "admin", "running"),
		{ID: strings.Repeat("x", 64), Labels: map[string]string{
			composeProjectLabel: "another-project", composeServiceLabel: "web",
		}},
	}
}

func writeMultiplexedLog(target *bytes.Buffer, stream stdcopy.StdType, payload []byte) {
	header := make([]byte, 8)
	header[0] = byte(stream)
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	_, _ = target.Write(header)
	_, _ = target.Write(payload)
}

func newRuntimeTestManager(t *testing.T, client DockerClient) *Manager {
	t.Helper()
	manager, err := New(client, "fixture-project", staticAccountCatalog{accounts: []controlplane.Account{
		{ID: "alpha"}, {ID: "beta"},
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return manager
}

type staticAccountCatalog struct{ accounts []controlplane.Account }

func (catalog staticAccountCatalog) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return append([]controlplane.Account(nil), catalog.accounts...), nil
}

type fakeDockerClient struct {
	containers []containertypes.Summary
	logs       map[string][]byte
	listLabels []string
	started    []string
	stopped    []string
	restarted  []string
	logOptions dockerclient.ContainerLogsOptions
}

func (client *fakeDockerClient) ContainerList(_ context.Context, options dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error) {
	for value := range options.Filters["label"] {
		client.listLabels = append(client.listLabels, value)
	}
	sort.Strings(client.listLabels)
	return dockerclient.ContainerListResult{Items: append([]containertypes.Summary(nil), client.containers...)}, nil
}

func (client *fakeDockerClient) ContainerStart(_ context.Context, id string, _ dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error) {
	client.started = append(client.started, id)
	return dockerclient.ContainerStartResult{}, nil
}

func (client *fakeDockerClient) ContainerStop(_ context.Context, id string, _ dockerclient.ContainerStopOptions) (dockerclient.ContainerStopResult, error) {
	client.stopped = append(client.stopped, id)
	return dockerclient.ContainerStopResult{}, nil
}

func (client *fakeDockerClient) ContainerRestart(_ context.Context, id string, _ dockerclient.ContainerRestartOptions) (dockerclient.ContainerRestartResult, error) {
	client.restarted = append(client.restarted, id)
	return dockerclient.ContainerRestartResult{}, nil
}

func (client *fakeDockerClient) ContainerLogs(_ context.Context, id string, options dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error) {
	client.logOptions = options
	return io.NopCloser(bytes.NewReader(client.logs[id])), nil
}
