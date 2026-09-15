package runtimeops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/moby/moby/api/pkg/stdcopy"

	dockerclient "github.com/moby/moby/client"
)

const (
	composeProjectLabel  = "com.docker.compose.project"
	composeServiceLabel  = "com.docker.compose.service"
	defaultLogLimit      = 2 * 1024 * 1024
	logSanitizeLookahead = 64 * 1024
)

var (
	ErrUnsafeDockerHost = errors.New("Docker daemon must use a local Unix socket")
	ErrRuntimeTarget    = errors.New("unknown or unavailable runtime target")
	ErrRuntimeConflict  = errors.New("runtime service container conflict")
	ErrRuntimeReadOnly  = errors.New("Docker runtime is read-only")
)

var (
	bearerPattern     = regexp.MustCompile(`(?i)\bBearer\s+\S+`)
	keyPattern        = regexp.MustCompile(`(?i)\b[a-z][a-z0-9_]{1,31}_(?:[a-z0-9]+_)*(?:[0-9a-f]{16}|[0-9a-f]{64}|[0-9a-f]{8}-[0-9a-f-]{27,})\b`)
	jsonSecretPattern = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|api_key)"\s*:\s*")[^"]+`)
)

type DockerClient interface {
	ContainerList(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error)
	ContainerStart(context.Context, string, dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error)
	ContainerStop(context.Context, string, dockerclient.ContainerStopOptions) (dockerclient.ContainerStopResult, error)
	ContainerRestart(context.Context, string, dockerclient.ContainerRestartOptions) (dockerclient.ContainerRestartResult, error)
	ContainerLogs(context.Context, string, dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error)
}

type AccountCatalog interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
}

type Service struct {
	Service     string `json:"service"`
	ContainerID string `json:"container_id"`
	Name        string `json:"name"`
	Image       string `json:"image"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Health      string `json:"health"`
	dockerID    string
	imageID     string
}

type OperationResult struct {
	Action   string    `json:"action"`
	Target   string    `json:"target"`
	Services []Service `json:"services"`
}

type LogsResult struct {
	Target    string `json:"target"`
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

type Manager struct {
	client   DockerClient
	close    func() error
	project  string
	accounts AccountCatalog
	logLimit int
}

func Open(project string, accounts AccountCatalog) (*Manager, error) {
	dockerClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("open Docker Engine client: %w", err)
	}
	if !strings.HasPrefix(strings.ToLower(dockerClient.DaemonHost()), "unix://") {
		_ = dockerClient.Close()
		return nil, ErrUnsafeDockerHost
	}
	manager, err := New(dockerClient, project, accounts)
	if err != nil {
		_ = dockerClient.Close()
		return nil, err
	}
	manager.close = dockerClient.Close
	return manager, nil
}

// OpenReadOnly keeps the Docker Engine SDK available for bounded status and
// log reads while making every mutating Manager method fail closed. The
// wrapper intentionally does not implement account-lifecycle or image-pull
// interfaces, so downstream type assertions cannot recover write access.
func OpenReadOnly(project string, accounts AccountCatalog) (*Manager, error) {
	dockerClient, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("open Docker Engine client: %w", err)
	}
	if !strings.HasPrefix(strings.ToLower(dockerClient.DaemonHost()), "unix://") {
		_ = dockerClient.Close()
		return nil, ErrUnsafeDockerHost
	}
	manager, err := New(&readOnlyDockerClient{client: dockerClient}, project, accounts)
	if err != nil {
		_ = dockerClient.Close()
		return nil, err
	}
	manager.close = dockerClient.Close
	return manager, nil
}

type readOnlyDockerClient struct {
	client *dockerclient.Client
}

func (client *readOnlyDockerClient) ContainerList(
	ctx context.Context,
	options dockerclient.ContainerListOptions,
) (dockerclient.ContainerListResult, error) {
	return client.client.ContainerList(ctx, options)
}

func (client *readOnlyDockerClient) ContainerLogs(
	ctx context.Context,
	id string,
	options dockerclient.ContainerLogsOptions,
) (dockerclient.ContainerLogsResult, error) {
	return client.client.ContainerLogs(ctx, id, options)
}

func (client *readOnlyDockerClient) ContainerStart(
	context.Context,
	string,
	dockerclient.ContainerStartOptions,
) (dockerclient.ContainerStartResult, error) {
	return dockerclient.ContainerStartResult{}, ErrRuntimeReadOnly
}

func (client *readOnlyDockerClient) ContainerStop(
	context.Context,
	string,
	dockerclient.ContainerStopOptions,
) (dockerclient.ContainerStopResult, error) {
	return dockerclient.ContainerStopResult{}, ErrRuntimeReadOnly
}

func (client *readOnlyDockerClient) ContainerRestart(
	context.Context,
	string,
	dockerclient.ContainerRestartOptions,
) (dockerclient.ContainerRestartResult, error) {
	return dockerclient.ContainerRestartResult{}, ErrRuntimeReadOnly
}

func (client *readOnlyDockerClient) ImageInspect(
	ctx context.Context,
	reference string,
	options ...dockerclient.ImageInspectOption,
) (dockerclient.ImageInspectResult, error) {
	return client.client.ImageInspect(ctx, reference, options...)
}

func (client *readOnlyDockerClient) ImageList(
	ctx context.Context,
	options dockerclient.ImageListOptions,
) (dockerclient.ImageListResult, error) {
	return client.client.ImageList(ctx, options)
}

func New(dockerClient DockerClient, project string, accounts AccountCatalog) (*Manager, error) {
	if dockerClient == nil {
		return nil, errors.New("runtime operations require a Docker Engine client")
	}
	project = strings.TrimSpace(project)
	if project == "" || len(project) > 128 || strings.ContainsAny(project, "\r\n\x00") {
		return nil, errors.New("runtime operations require a valid Compose project")
	}
	if accounts == nil {
		return nil, errors.New("runtime operations require an account catalog")
	}
	return &Manager{client: dockerClient, project: project, accounts: accounts, logLimit: defaultLogLimit}, nil
}

func (manager *Manager) Close() error {
	if manager == nil || manager.close == nil {
		return nil
	}
	return manager.close()
}

func (manager *Manager) List(ctx context.Context) ([]Service, error) {
	filter := make(dockerclient.Filters).Add("label", composeProjectLabel+"="+manager.project)
	containers, err := manager.client.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("list Compose project containers: %w", err)
	}
	services := make([]Service, 0, len(containers.Items))
	seen := make(map[string]struct{}, len(containers.Items))
	for _, container := range containers.Items {
		if container.Labels[composeProjectLabel] != manager.project {
			continue
		}
		serviceName := strings.TrimSpace(container.Labels[composeServiceLabel])
		if serviceName == "" {
			continue
		}
		if _, duplicate := seen[serviceName]; duplicate {
			return nil, fmt.Errorf("%w: multiple containers claim service %s", ErrRuntimeConflict, serviceName)
		}
		seen[serviceName] = struct{}{}
		name := ""
		if len(container.Names) > 0 {
			name = strings.TrimPrefix(container.Names[0], "/")
		}
		services = append(services, Service{
			Service: serviceName, ContainerID: shortID(container.ID), Name: name,
			Image: container.Image, State: strings.ToLower(strings.TrimSpace(string(container.State))),
			Status: strings.TrimSpace(container.Status), Health: containerHealth(container.Status),
			dockerID: container.ID, imageID: container.ImageID,
		})
	}
	sort.Slice(services, func(left, right int) bool { return services[left].Service < services[right].Service })
	return services, nil
}

func containerHealth(status string) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	for _, health := range []string{"healthy", "unhealthy", "starting"} {
		if strings.Contains(normalized, "("+health+")") || strings.Contains(normalized, "(health: "+health+")") {
			return health
		}
	}
	return ""
}

func (manager *Manager) Start(ctx context.Context, target string) (OperationResult, error) {
	return manager.operate(ctx, "start", target)
}

func (manager *Manager) Stop(ctx context.Context, target string) (OperationResult, error) {
	return manager.operate(ctx, "stop", target)
}

func (manager *Manager) Restart(ctx context.Context, target string) (OperationResult, error) {
	return manager.operate(ctx, "restart", target)
}

func (manager *Manager) operate(ctx context.Context, action string, target string) (OperationResult, error) {
	resolved, err := manager.resolve(ctx, target, false)
	if err != nil {
		return OperationResult{}, err
	}
	timeout := 30
	for _, service := range resolved {
		var operationError error
		switch action {
		case "start":
			if service.State != "running" {
				_, operationError = manager.client.ContainerStart(ctx, service.dockerID, dockerclient.ContainerStartOptions{})
			}
		case "stop":
			if service.State == "running" {
				_, operationError = manager.client.ContainerStop(ctx, service.dockerID, dockerclient.ContainerStopOptions{Timeout: &timeout})
			}
		case "restart":
			_, operationError = manager.client.ContainerRestart(ctx, service.dockerID, dockerclient.ContainerRestartOptions{Timeout: &timeout})
		default:
			return OperationResult{}, fmt.Errorf("%w: unsupported runtime action", ErrRuntimeTarget)
		}
		if operationError != nil {
			return OperationResult{}, fmt.Errorf("%s runtime service %s: %w", action, service.Service, operationError)
		}
	}
	return OperationResult{Action: action, Target: normalizeTarget(target), Services: resolved}, nil
}

func (manager *Manager) Logs(ctx context.Context, target string) (LogsResult, error) {
	resolved, err := manager.resolve(ctx, target, true)
	if err != nil {
		return LogsResult{}, err
	}
	buffer := &boundedBuffer{limit: manager.logLimit + logSanitizeLookahead}
	for _, service := range resolved {
		stream, err := manager.client.ContainerLogs(ctx, service.dockerID, dockerclient.ContainerLogsOptions{
			ShowStdout: true, ShowStderr: true, Tail: "200",
		})
		if err != nil {
			return LogsResult{}, fmt.Errorf("read runtime service %s logs: %w", service.Service, err)
		}
		_, copyError := stdcopy.StdCopy(buffer, buffer, stream)
		closeError := stream.Close()
		if copyError != nil {
			return LogsResult{}, fmt.Errorf("decode runtime service %s logs: %w", service.Service, copyError)
		}
		if closeError != nil {
			return LogsResult{}, fmt.Errorf("close runtime service %s logs: %w", service.Service, closeError)
		}
	}
	output := Sanitize(buffer.String())
	truncated := buffer.truncated || len(output) > manager.logLimit
	if len(output) > manager.logLimit {
		output = strings.ToValidUTF8(output[:manager.logLimit], "�")
	}
	if truncated {
		output += i18n.Text(i18n.English, "runtimeops.output_truncated")
	}
	return LogsResult{Target: normalizeTarget(target), Output: output, ExitCode: 0, Truncated: truncated}, nil
}

func (manager *Manager) resolve(ctx context.Context, target string, logs bool) ([]Service, error) {
	services, err := manager.List(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]Service, len(services))
	for _, service := range services {
		byName[service.Service] = service
	}
	allowed, err := manager.allowedServices(ctx, logs)
	if err != nil {
		return nil, err
	}
	target = normalizeTarget(target)
	wanted := make([]string, 0)
	if target == "all" {
		wanted = append(wanted, allowed...)
	} else {
		serviceName := target
		for _, accountService := range allowed {
			if accountService == "cliproxy-"+target {
				serviceName = accountService
				break
			}
		}
		if !contains(allowed, serviceName) {
			return nil, fmt.Errorf("%w: %s", ErrRuntimeTarget, target)
		}
		wanted = append(wanted, serviceName)
	}
	result := make([]Service, 0, len(wanted))
	for _, serviceName := range wanted {
		service, found := byName[serviceName]
		if !found {
			if target == "all" {
				continue
			}
			return nil, fmt.Errorf("%w: service %s has no Compose container", ErrRuntimeTarget, serviceName)
		}
		result = append(result, service)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: no matching Compose containers", ErrRuntimeTarget)
	}
	return result, nil
}

func (manager *Manager) allowedServices(ctx context.Context, logs bool) ([]string, error) {
	accounts, err := manager.accounts.ReadAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("read runtime account catalog: %w", err)
	}
	allowed := make([]string, 0, len(accounts)+8)
	for _, account := range accounts {
		allowed = append(allowed, "cliproxy-"+account.ID)
	}
	if logs {
		allowed = append(allowed, "edge", "web", "gateway-blue", "gateway-green", "management", "usage-collector", "log-maintenance", "admin")
	} else {
		allowed = append(allowed, "web", "management", "usage-collector", "log-maintenance")
	}
	return allowed, nil
}

func normalizeTarget(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return "all"
	}
	return target
}

func shortID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Sanitize removes supported credential shapes before runtime text is returned
// by an API, persisted in an in-memory job, or written to service logs.
func Sanitize(value string) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = keyPattern.ReplaceAllString(value, "key_[REDACTED]")
	return jsonSecretPattern.ReplaceAllString(value, `$1[REDACTED]`)
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(payload []byte) (int, error) {
	original := len(payload)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(payload) > remaining {
			payload = payload[:remaining]
			buffer.truncated = true
		}
		_, _ = buffer.buffer.Write(payload)
	} else if original > 0 {
		buffer.truncated = true
	}
	return original, nil
}

func (buffer *boundedBuffer) String() string { return buffer.buffer.String() }
