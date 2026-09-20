package runtimeops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountconfig"
	"github.com/Alfonsxh/codex-cpa-pool/internal/accountlifecycle"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/containerd/errdefs"
	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	networktypes "github.com/moby/moby/api/types/network"

	dockerclient "github.com/moby/moby/client"
)

const (
	defaultAccountNetwork      = "cliproxy-backend"
	defaultAccountInstance     = "cliproxy"
	defaultAccountListen       = "127.0.0.1"
	defaultAccountProbeTimeout = 12 * time.Second
	accountContainerPort       = "8317/tcp"
	serviceTimezone            = "UTC"
	maximumOAuthSnapshotBytes  = 2 * 1024 * 1024
)

var runtimeIdentifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type AccountRuntimeStore interface {
	ReadSettings(context.Context) (map[string]any, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
	ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error)
	ReadSecret(context.Context, string) (string, bool, error)
}

type CPAImageProjector interface {
	ProjectCPAImage(context.Context, string, string) error
}

type AccountLifecycleDockerClient interface {
	DockerClient
	ContainerCreate(context.Context, dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error)
	ContainerInspect(context.Context, string, dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error)
	ContainerRemove(context.Context, string, dockerclient.ContainerRemoveOptions) (dockerclient.ContainerRemoveResult, error)
}

type AccountRuntimeConfig struct {
	Root           string
	NetworkName    string
	InstanceName   string
	ProbeTimeout   time.Duration
	HTTPClient     *resty.Client
	ImageProjector CPAImageProjector
	Drainer        accountlifecycle.AccountDrainer
}

// AccountRuntime uses the same Moby client and exact Compose labels as the
// bounded runtime API. It recreates one business CPA at a time and retains a
// deterministic rollback specification until the lifecycle Saga completes.
type AccountRuntime struct {
	client         AccountLifecycleDockerClient
	accounts       AccountCatalog
	project        string
	root           string
	network        string
	instance       string
	store          AccountRuntimeStore
	http           *resty.Client
	probeTimeout   time.Duration
	imageProjector CPAImageProjector
	drainer        accountlifecycle.AccountDrainer
}

func NewAccountRuntime(
	manager *Manager,
	store AccountRuntimeStore,
	config AccountRuntimeConfig,
) (*AccountRuntime, error) {
	if manager == nil || manager.client == nil || store == nil {
		return nil, errors.New("account runtime requires the active Moby manager and control-plane store")
	}
	client, ok := manager.client.(AccountLifecycleDockerClient)
	if !ok {
		return nil, errors.New("Moby client does not implement account lifecycle operations")
	}
	root, err := filepath.Abs(strings.TrimSpace(config.Root))
	if err != nil || strings.TrimSpace(config.Root) == "" {
		return nil, errors.New("account runtime requires an absolute deployment root")
	}
	root = filepath.Clean(root)
	networkName := strings.TrimSpace(config.NetworkName)
	if networkName == "" {
		networkName = defaultAccountNetwork
	}
	instanceName := strings.TrimSpace(config.InstanceName)
	if instanceName == "" {
		instanceName = defaultAccountInstance
	}
	if !runtimeIdentifierPattern.MatchString(networkName) || !runtimeIdentifierPattern.MatchString(instanceName) {
		return nil, errors.New("account runtime network and instance names contain unsupported characters")
	}
	if config.ProbeTimeout <= 0 {
		config.ProbeTimeout = defaultAccountProbeTimeout
	}
	if config.ProbeTimeout < time.Second || config.ProbeTimeout > time.Minute {
		return nil, errors.New("account runtime probe timeout must be between one second and one minute")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = resty.New().SetTimeout(3 * time.Second).SetRedirectPolicy(resty.NoRedirectPolicy())
	}
	return &AccountRuntime{
		client: client, accounts: manager.accounts, project: manager.project, root: root, network: networkName,
		instance: instanceName, store: store, http: httpClient,
		probeTimeout: config.ProbeTimeout, imageProjector: config.ImageProjector, drainer: config.Drainer,
	}, nil
}

type accountOAuthDockerClient interface {
	AccountLifecycleDockerClient
	ContainerAttach(context.Context, string, dockerclient.ContainerAttachOptions) (dockerclient.ContainerAttachResult, error)
	ContainerWait(context.Context, string, dockerclient.ContainerWaitOptions) dockerclient.ContainerWaitResult
}

type oauthFileIdentity struct {
	size   int64
	mtime  int64
	digest [sha256.Size]byte
}

// Login runs CLIProxyAPI's remote-friendly device flow in an isolated,
// one-off Moby container. It reuses the account's exact immutable image,
// config, proxy/network, and OAuth bind mount without stopping or replacing
// the customer-serving CPA container.
func (runtime *AccountRuntime) Login(
	ctx context.Context,
	rawAccountID string,
	output io.Writer,
) (OperationResult, error) {
	accountID, err := controlplane.NormalizeAccountID(rawAccountID)
	if err != nil || accountID != strings.ToLower(strings.TrimSpace(rawAccountID)) {
		return OperationResult{}, fmt.Errorf("%w: invalid OAuth account", ErrRuntimeTarget)
	}
	if output == nil {
		return OperationResult{}, errors.New("OAuth device login requires an output writer")
	}
	client, ok := runtime.client.(accountOAuthDockerClient)
	if !ok {
		return OperationResult{}, errors.New("Moby client does not implement OAuth device login")
	}
	accounts, err := runtime.accounts.ReadAccounts(ctx)
	if err != nil {
		return OperationResult{}, fmt.Errorf("read OAuth account catalog: %w", err)
	}
	var account controlplane.Account
	found := false
	for _, candidate := range accounts {
		if candidate.ID == accountID {
			account = candidate
			found = true
			break
		}
	}
	if !found {
		return OperationResult{}, fmt.Errorf("%w: OAuth account %s does not exist", ErrRuntimeTarget, accountID)
	}
	authRoot := filepath.Join(runtime.root, "auth", accountID)
	before, err := snapshotOAuthFiles(authRoot)
	if err != nil {
		return OperationResult{}, err
	}
	options, err := runtime.createOptions(ctx, account)
	if err != nil {
		return OperationResult{}, err
	}
	operationID := uuid.NewString()
	options.Name = runtime.instance + "-oauth-" + accountID + "-" + operationID[:8]
	options.Config.Cmd = []string{
		"./CLIProxyAPI", "-config", accountconfig.ContainerFile,
		"-codex-device-login", "-no-browser",
	}
	options.Config.AttachStdout = true
	options.Config.AttachStderr = true
	options.Config.ExposedPorts = nil
	options.Config.Labels = map[string]string{
		"io.codex-cpa.account":    accountID,
		"io.codex-cpa.managed-by": "codex-cpa",
		"io.codex-cpa.operation":  "oauth-device-login",
	}
	options.HostConfig.AutoRemove = false
	options.HostConfig.PortBindings = nil
	options.HostConfig.RestartPolicy = containertypes.RestartPolicy{Name: containertypes.RestartPolicyDisabled}
	options.NetworkingConfig = &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{
		runtime.network: {Aliases: []string{"oauth-" + accountID + "-" + operationID[:8]}},
	}}
	created, err := client.ContainerCreate(ctx, options)
	if err != nil {
		return OperationResult{}, fmt.Errorf("create OAuth login container for %s: %w", accountID, err)
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_, _ = client.ContainerRemove(cleanupContext, created.ID, dockerclient.ContainerRemoveOptions{Force: true})
	}()
	attached, err := client.ContainerAttach(ctx, created.ID, dockerclient.ContainerAttachOptions{
		Stream: true, Stdout: true, Stderr: true,
	})
	if err != nil {
		return OperationResult{}, fmt.Errorf("attach OAuth login output for %s: %w", accountID, err)
	}
	defer attached.Close()
	wait := client.ContainerWait(ctx, created.ID, dockerclient.ContainerWaitOptions{
		Condition: containertypes.WaitConditionNextExit,
	})
	copyDone := make(chan error, 1)
	go func() {
		_, copyError := stdcopy.StdCopy(output, output, attached.Reader)
		copyDone <- copyError
	}()
	if _, err := client.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		return OperationResult{}, fmt.Errorf("start OAuth login container for %s: %w", accountID, err)
	}
	var exitCode int64
	select {
	case response := <-wait.Result:
		exitCode = response.StatusCode
		if response.Error != nil && strings.TrimSpace(response.Error.Message) != "" {
			return OperationResult{}, fmt.Errorf("OAuth login container failed: %s", Sanitize(response.Error.Message))
		}
	case waitError := <-wait.Error:
		return OperationResult{}, fmt.Errorf("wait for OAuth login container: %w", waitError)
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	}
	select {
	case copyError := <-copyDone:
		if copyError != nil {
			return OperationResult{}, fmt.Errorf("read OAuth login output: %w", copyError)
		}
	case <-time.After(5 * time.Second):
		return OperationResult{}, errors.New("OAuth login output did not close after container exit")
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	}
	if exitCode != 0 {
		return OperationResult{}, fmt.Errorf("OAuth login container exited with code %d", exitCode)
	}
	after, err := snapshotOAuthFiles(authRoot)
	if err != nil {
		return OperationResult{}, err
	}
	if oauthSnapshotsEqual(before, after) {
		return OperationResult{}, i18n.M("runtimeops.oauth_authorization_did_not_complete_no_new_or_updated_authentication")
	}
	return OperationResult{Action: "login", Target: accountID, Services: []Service{}}, nil
}

func snapshotOAuthFiles(root string) (map[string]oauthFileIdentity, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read OAuth directory: %w", err)
	}
	result := make(map[string]oauthFileIdentity)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect OAuth file: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumOAuthSnapshotBytes {
			return nil, errors.New("OAuth directory contains an unsafe JSON file")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open OAuth file for verification: %w", err)
		}
		hash := sha256.New()
		_, copyError := io.Copy(hash, io.LimitReader(file, maximumOAuthSnapshotBytes+1))
		closeError := file.Close()
		if copyError != nil || closeError != nil {
			return nil, errors.Join(copyError, closeError)
		}
		var digest [sha256.Size]byte
		copy(digest[:], hash.Sum(nil))
		result[entry.Name()] = oauthFileIdentity{
			size: info.Size(), mtime: info.ModTime().UnixNano(), digest: digest,
		}
	}
	return result, nil
}

func oauthSnapshotsEqual(left, right map[string]oauthFileIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for name, identity := range left {
		if right[name] != identity {
			return false
		}
	}
	return true
}

func (runtime *AccountRuntime) ReservedHostPorts(ctx context.Context) (map[int]struct{}, error) {
	containers, err := runtime.client.ContainerList(ctx, dockerclient.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list Docker host ports: %w", err)
	}
	ports := make(map[int]struct{})
	for _, candidate := range containers.Items {
		for _, binding := range candidate.Ports {
			if binding.PublicPort > 0 {
				ports[int(binding.PublicPort)] = struct{}{}
			}
		}
	}
	return ports, nil
}

// MigrateLegacyConfigMounts replaces only account containers that still use
// the old single-file bind mount. A directory bind observes atomic config-file
// replacement by path, whereas Docker pins a single-file bind to the inode that
// existed when the container was created. The legacy file is removed only
// after the replacement container has loaded and passed its authenticated
// probe; a failed replacement is rebuilt with the original image and mount.
func (runtime *AccountRuntime) MigrateLegacyConfigMounts(ctx context.Context) ([]string, error) {
	accounts, err := runtime.accounts.ReadAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("read accounts for config mount migration: %w", err)
	}
	sort.Slice(accounts, func(left, right int) bool { return accounts[left].ID < accounts[right].ID })
	migrated := make([]string, 0)
	for _, account := range accounts {
		container, found, err := runtime.findAccountContainer(ctx, account.ID)
		if err != nil {
			return migrated, err
		}
		if !found {
			continue
		}
		inspection, err := runtime.client.ContainerInspect(ctx, container.dockerID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			return migrated, fmt.Errorf("inspect account config mount for %s: %w", account.ID, err)
		}
		if imageID := strings.TrimSpace(inspection.Container.Image); imageID != "" {
			// Recreate from the exact already-running image content, not from a
			// mutable tag that may have moved since this container was created.
			container.Image = imageID
		} else if inspection.Container.Config != nil && strings.TrimSpace(inspection.Container.Config.Image) != "" {
			container.Image = strings.TrimSpace(inspection.Container.Config.Image)
		}
		layout, err := runtime.detectConfigMountLayout(account.ID, inspection.Container.Mounts)
		if err != nil {
			return migrated, err
		}
		if layout == configMountLegacyFile {
			if container.State == string(containertypes.StateRunning) {
				if runtime.drainer == nil {
					return migrated, fmt.Errorf("migrate account config mount for %s: %w", account.ID, accountlifecycle.ErrRouteEvacuationUnavailable)
				}
				if err := runtime.drainer.WaitAccountDrained(ctx, account.ID); err != nil {
					return migrated, fmt.Errorf("migrate account config mount for %s: %w", account.ID, err)
				}
			}
			if err := runtime.recreateLegacyConfigContainer(ctx, account, container); err != nil {
				return migrated, err
			}
			migrated = append(migrated, account.ID)
		}
		if err := removeLegacyConfigFile(runtime.root, account.ID); err != nil {
			return migrated, err
		}
	}
	return migrated, nil
}

func (runtime *AccountRuntime) detectConfigMountLayout(
	accountID string,
	mounts []containertypes.MountPoint,
) (accountConfigMountLayout, error) {
	expectedDirectory := filepath.Clean(accountconfig.Directory(runtime.root, accountID))
	expectedLegacy := filepath.Clean(accountconfig.LegacyFile(runtime.root, accountID))
	legacyTarget := filepath.Clean("/CLIProxyAPI/configs/" + accountID + ".yaml")
	currentFound := false
	legacyFound := false
	for _, mounted := range mounts {
		target := filepath.Clean(mounted.Destination)
		if target != filepath.Clean(accountconfig.ContainerDirectory) && target != legacyTarget {
			continue
		}
		if mounted.Type != mount.TypeBind || mounted.RW {
			return configMountDirectory, fmt.Errorf("%w: account %s config mount is not a read-only bind", ErrRuntimeConflict, accountID)
		}
		source := filepath.Clean(mounted.Source)
		switch target {
		case filepath.Clean(accountconfig.ContainerDirectory):
			if source != expectedDirectory {
				return configMountDirectory, fmt.Errorf("%w: account %s config directory source is %s", ErrRuntimeConflict, accountID, source)
			}
			currentFound = true
		case legacyTarget:
			if source != expectedLegacy {
				return configMountDirectory, fmt.Errorf("%w: account %s legacy config source is %s", ErrRuntimeConflict, accountID, source)
			}
			legacyFound = true
		}
	}
	if currentFound == legacyFound {
		return configMountDirectory, fmt.Errorf("%w: account %s has an ambiguous or missing config mount", ErrRuntimeConflict, accountID)
	}
	if legacyFound {
		return configMountLegacyFile, nil
	}
	return configMountDirectory, nil
}

func (runtime *AccountRuntime) recreateLegacyConfigContainer(
	ctx context.Context,
	account controlplane.Account,
	container Service,
) error {
	wasRunning := container.State == string(containertypes.StateRunning)
	if err := runtime.removeContainer(ctx, container.dockerID); err != nil {
		return fmt.Errorf("remove legacy config container %s: %w", account.ID, err)
	}
	newID, migrationError := runtime.createAccountContainerWithLayout(
		ctx, account, wasRunning, container.Image, configMountDirectory,
	)
	if migrationError == nil && wasRunning {
		migrationError = runtime.probeAccount(ctx, account.ID)
		if migrationError != nil && newID != "" {
			cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			migrationError = errors.Join(migrationError, runtime.removeContainer(cleanupContext, newID))
			cancel()
		}
	}
	if migrationError == nil {
		return nil
	}
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, rollbackError := runtime.createAccountContainerWithLayout(
		rollbackContext, account, wasRunning, container.Image, configMountLegacyFile,
	)
	return errors.Join(
		fmt.Errorf("migrate account %s config mount: %w", account.ID, migrationError),
		wrapRuntimeError("restore legacy account config mount", rollbackError),
	)
}

func removeLegacyConfigFile(root, accountID string) error {
	path := accountconfig.LegacyFile(root, accountID)
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect migrated legacy config for %s: %w", accountID, err)
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: migrated legacy config is not a regular file: %s", ErrRuntimeConflict, path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove migrated legacy config for %s: %w", accountID, err)
	}
	return nil
}

func (runtime *AccountRuntime) PrepareCreate(
	ctx context.Context,
	account controlplane.Account,
) (accountlifecycle.RuntimeTransition, error) {
	if _, found, err := runtime.findAccountContainer(ctx, account.ID); err != nil {
		return nil, err
	} else if found {
		return nil, fmt.Errorf("%w: account container already exists for %s", ErrRuntimeConflict, account.ID)
	}
	containerID, err := runtime.createAndProbe(ctx, account)
	if err != nil {
		return nil, err
	}
	return &accountRuntimeTransition{
		runtime: runtime, kind: "create", after: account, newContainerID: containerID,
	}, nil
}

func (runtime *AccountRuntime) PrepareUpdate(
	ctx context.Context,
	before controlplane.Account,
	after controlplane.Account,
) (accountlifecycle.RuntimeTransition, error) {
	old, found, err := runtime.findAccountContainer(ctx, before.ID)
	if err != nil {
		return nil, err
	}
	wasRunning := found && old.State == string(containertypes.StateRunning)
	if found {
		if err := runtime.removeContainer(ctx, old.dockerID); err != nil {
			return nil, fmt.Errorf("remove previous account container %s: %w", before.ID, err)
		}
	}
	newContainerID, err := runtime.createAndProbe(ctx, after)
	if err != nil {
		return &accountRuntimeTransition{
			runtime: runtime, kind: "update", before: before, after: after,
			oldExisted: found, oldWasRunning: wasRunning, newContainerID: newContainerID,
		}, err
	}
	return &accountRuntimeTransition{
		runtime: runtime, kind: "update", before: before, after: after,
		oldExisted: found, oldWasRunning: wasRunning, newContainerID: newContainerID,
	}, nil
}

func (runtime *AccountRuntime) PrepareDelete(
	ctx context.Context,
	account controlplane.Account,
) (accountlifecycle.RuntimeTransition, error) {
	old, found, err := runtime.findAccountContainer(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	return &accountRuntimeTransition{
		runtime: runtime, kind: "delete", before: account,
		oldExisted: found, oldWasRunning: found && old.State == string(containertypes.StateRunning),
		oldContainerID: old.dockerID,
	}, nil
}

// RestartRunningAccount never resurrects a stopped or disabled account.
func (runtime *AccountRuntime) RestartRunningAccount(ctx context.Context, accountID string) error {
	accounts, err := runtime.accounts.ReadAccounts(ctx)
	if err != nil {
		return err
	}
	enabled := false
	for _, account := range accounts {
		if account.ID == accountID && account.GroupEnabled {
			enabled = true
		}
	}
	if !enabled {
		return errors.New("account is disabled or missing")
	}
	container, found, err := runtime.findAccountContainer(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || container.State != "running" {
		return errors.New("account is stopped; plugin operation cancelled")
	}
	if runtime.drainer == nil {
		return accountlifecycle.ErrRouteEvacuationUnavailable
	}
	if err := runtime.drainer.WaitAccountDrained(ctx, accountID); err != nil {
		return err
	}
	container, found, err = runtime.findAccountContainer(ctx, accountID)
	if err != nil {
		return err
	}
	if !found || container.State != "running" {
		return errors.New("account stopped while draining")
	}
	return runtime.RestartAccount(ctx, accountID)
}

func (runtime *AccountRuntime) RestartAccount(ctx context.Context, accountID string) error {
	container, found, err := runtime.findAccountContainer(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: account container %s is unavailable", ErrRuntimeTarget, accountID)
	}
	timeout := 30
	if _, err := runtime.client.ContainerRestart(
		ctx, container.dockerID, dockerclient.ContainerRestartOptions{Timeout: &timeout},
	); err != nil {
		return fmt.Errorf("restart account container %s: %w", accountID, err)
	}
	return runtime.probeAccount(ctx, accountID)
}

// ReconcileAccount is the idempotent process-interruption recovery boundary.
// It is called only while the Admin writer lease is held and before the Admin
// listener starts accepting mutations.
func (runtime *AccountRuntime) ReconcileAccount(
	ctx context.Context,
	previousAccountID string,
	desired *controlplane.Account,
) error {
	previousAccountID = strings.TrimSpace(previousAccountID)
	if desired == nil {
		if previousAccountID == "" {
			return nil
		}
		container, found, err := runtime.findAccountContainer(ctx, previousAccountID)
		if err != nil || !found {
			return err
		}
		return runtime.removeContainer(ctx, container.dockerID)
	}
	if normalized, err := controlplane.NormalizeAccountID(desired.ID); err != nil || normalized != desired.ID {
		return fmt.Errorf("%w: invalid desired recovery account", ErrRuntimeTarget)
	}
	// A rename keeps the same Host port. Remove the non-authoritative side
	// before creating the canonical container so Docker cannot reject the bind.
	if previousAccountID != "" && previousAccountID != desired.ID {
		container, found, err := runtime.findAccountContainer(ctx, previousAccountID)
		if err != nil {
			return err
		}
		if found {
			if err := runtime.removeContainer(ctx, container.dockerID); err != nil {
				return err
			}
		}
	}
	container, found, err := runtime.findAccountContainer(ctx, desired.ID)
	if err != nil {
		return err
	}
	if found {
		if err := runtime.removeContainer(ctx, container.dockerID); err != nil {
			return err
		}
	}
	_, err = runtime.createAndProbe(ctx, *desired)
	return err
}

type accountRuntimeTransition struct {
	runtime        *AccountRuntime
	kind           string
	before         controlplane.Account
	after          controlplane.Account
	oldExisted     bool
	oldWasRunning  bool
	oldContainerID string
	newContainerID string
	committed      bool
	rolledBack     bool
}

func (transition *accountRuntimeTransition) Commit(ctx context.Context) error {
	if transition == nil || transition.committed {
		return nil
	}
	if transition.rolledBack {
		return errors.New("account runtime transition was already rolled back")
	}
	if transition.kind == "delete" && transition.oldExisted {
		if err := transition.runtime.removeContainer(ctx, transition.oldContainerID); err != nil {
			return err
		}
		transition.oldContainerID = ""
	}
	transition.committed = true
	return nil
}

func (transition *accountRuntimeTransition) Rollback(ctx context.Context) error {
	if transition == nil || transition.rolledBack {
		return nil
	}
	errorsFound := make([]error, 0, 2)
	switch transition.kind {
	case "create":
		if transition.newContainerID != "" {
			errorsFound = append(errorsFound, transition.runtime.removeContainer(ctx, transition.newContainerID))
		}
	case "update":
		if transition.newContainerID != "" {
			errorsFound = append(errorsFound, transition.runtime.removeContainer(ctx, transition.newContainerID))
		}
		if transition.oldExisted {
			_, err := transition.runtime.createAccountContainer(ctx, transition.before, transition.oldWasRunning)
			errorsFound = append(errorsFound, err)
		}
	case "delete":
		if transition.oldExisted {
			if transition.oldContainerID != "" {
				if transition.oldWasRunning {
					_, err := transition.runtime.client.ContainerStart(
						ctx, transition.oldContainerID, dockerclient.ContainerStartOptions{},
					)
					errorsFound = append(errorsFound, err)
				}
			} else {
				_, err := transition.runtime.createAccountContainer(ctx, transition.before, transition.oldWasRunning)
				errorsFound = append(errorsFound, err)
			}
		}
	default:
		errorsFound = append(errorsFound, errors.New("unknown account runtime transition"))
	}
	transition.rolledBack = true
	return errors.Join(compactErrors(errorsFound)...)
}

func (runtime *AccountRuntime) createAndProbe(ctx context.Context, account controlplane.Account) (string, error) {
	containerID, err := runtime.createAccountContainer(ctx, account, true)
	if err != nil {
		return "", err
	}
	if err := runtime.probeAccount(ctx, account.ID); err != nil {
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtime.probeTimeout)
		defer cancel()
		return "", errors.Join(err, wrapRuntimeError("remove failed account candidate", runtime.removeContainer(rollbackContext, containerID)))
	}
	return containerID, nil
}

func (runtime *AccountRuntime) createAccountContainer(
	ctx context.Context,
	account controlplane.Account,
	start bool,
) (string, error) {
	return runtime.createAccountContainerWithImage(ctx, account, start, "")
}

func (runtime *AccountRuntime) createAccountContainerWithImage(
	ctx context.Context,
	account controlplane.Account,
	start bool,
	imageReference string,
) (string, error) {
	return runtime.createAccountContainerWithLayout(ctx, account, start, imageReference, configMountDirectory)
}

func (runtime *AccountRuntime) createAccountContainerWithLayout(
	ctx context.Context,
	account controlplane.Account,
	start bool,
	imageReference string,
	layout accountConfigMountLayout,
) (string, error) {
	options, err := runtime.createOptionsWithImageAndLayout(ctx, account, imageReference, layout)
	if err != nil {
		return "", err
	}
	created, err := runtime.client.ContainerCreate(ctx, options)
	if err != nil {
		return "", fmt.Errorf("create account container %s: %w", account.ID, err)
	}
	if !start {
		return created.ID, nil
	}
	if _, err := runtime.client.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return "", errors.Join(
			fmt.Errorf("start account container %s: %w", account.ID, err),
			wrapRuntimeError("remove unstarted account container", runtime.removeContainer(rollbackContext, created.ID)),
		)
	}
	return created.ID, nil
}

func (runtime *AccountRuntime) createOptions(
	ctx context.Context,
	account controlplane.Account,
) (dockerclient.ContainerCreateOptions, error) {
	return runtime.createOptionsWithImage(ctx, account, "")
}

func (runtime *AccountRuntime) createOptionsWithImage(
	ctx context.Context,
	account controlplane.Account,
	imageReference string,
) (dockerclient.ContainerCreateOptions, error) {
	return runtime.createOptionsWithImageAndLayout(ctx, account, imageReference, configMountDirectory)
}

type accountConfigMountLayout int

const (
	configMountDirectory accountConfigMountLayout = iota
	configMountLegacyFile
)

func (runtime *AccountRuntime) createOptionsWithImageAndLayout(
	ctx context.Context,
	account controlplane.Account,
	imageReference string,
	layout accountConfigMountLayout,
) (dockerclient.ContainerCreateOptions, error) {
	accountID, err := controlplane.NormalizeAccountID(account.ID)
	if err != nil || accountID != account.ID || account.Port < 1 || account.Port > 65535 {
		return dockerclient.ContainerCreateOptions{}, fmt.Errorf("%w: invalid account container metadata", ErrRuntimeTarget)
	}
	configSource := accountconfig.Directory(runtime.root, account.ID)
	configTarget := accountconfig.ContainerDirectory
	configCommand := accountconfig.ContainerFile
	if layout == configMountLegacyFile {
		configSource = accountconfig.LegacyFile(runtime.root, account.ID)
		configTarget = "/CLIProxyAPI/configs/" + account.ID + ".yaml"
		configCommand = configTarget
	} else if layout != configMountDirectory {
		return dockerclient.ContainerCreateOptions{}, errors.New("unknown account config mount layout")
	}
	if layout == configMountDirectory {
		if err := requireRuntimeDirectory(configSource); err != nil {
			return dockerclient.ContainerCreateOptions{}, err
		}
		if err := requireRuntimeRegularFile(accountconfig.File(runtime.root, account.ID)); err != nil {
			return dockerclient.ContainerCreateOptions{}, err
		}
	} else if err := requireRuntimeRegularFile(configSource); err != nil {
		return dockerclient.ContainerCreateOptions{}, err
	}
	for _, path := range []string{
		filepath.Join(runtime.root, "management", "config", "static"),
		filepath.Join(runtime.root, "auth", account.ID),
		filepath.Join(runtime.root, "logs", account.ID),
	} {
		if err := requireRuntimeDirectory(path); err != nil {
			return dockerclient.ContainerCreateOptions{}, err
		}
	}
	image, listenAddress, err := runtime.imageAndListenAddress(ctx)
	if err != nil {
		return dockerclient.ContainerCreateOptions{}, err
	}
	if strings.TrimSpace(imageReference) != "" {
		image = strings.TrimSpace(imageReference)
	}
	if image == "" || len(image) > 512 || strings.ContainsAny(image, "\r\n\t \x00") {
		return dockerclient.ContainerCreateOptions{}, errors.New("CPA image reference is invalid")
	}
	hostAddress, err := netip.ParseAddr(listenAddress)
	if err != nil || !hostAddress.IsLoopback() {
		return dockerclient.ContainerCreateOptions{}, errors.New("business CPA listen address must be a loopback IP")
	}
	port := networktypes.MustParsePort(accountContainerPort)
	service := "cliproxy-" + account.ID
	labels := map[string]string{
		composeProjectLabel:         runtime.project,
		composeServiceLabel:         service,
		"com.docker.compose.oneoff": "False",
		"io.codex-cpa.account":      account.ID,
		"io.codex-cpa.managed-by":   "codex-cpa",
	}
	return dockerclient.ContainerCreateOptions{
		Name: runtime.instance + "-" + account.ID,
		Config: &containertypes.Config{
			Image:        image,
			Cmd:          []string{"./CLIProxyAPI", "-config", configCommand},
			Env:          []string{"TZ=" + serviceTimezone},
			ExposedPorts: networktypes.PortSet{port: struct{}{}},
			Labels:       labels,
		},
		HostConfig: &containertypes.HostConfig{
			RestartPolicy: containertypes.RestartPolicy{Name: containertypes.RestartPolicyUnlessStopped},
			LogConfig: containertypes.LogConfig{Type: "json-file", Config: map[string]string{
				"max-size": "20m", "max-file": "3",
			}},
			NetworkMode: containertypes.NetworkMode(runtime.network),
			PortBindings: networktypes.PortMap{port: []networktypes.PortBinding{{
				HostIP: hostAddress, HostPort: strconv.Itoa(account.Port),
			}}},
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: configSource, Target: configTarget, ReadOnly: true},
				{Type: mount.TypeBind, Source: filepath.Join(runtime.root, "management", "config", "static"), Target: "/CLIProxyAPI/configs/static", ReadOnly: true},
				{Type: mount.TypeBind, Source: filepath.Join(runtime.root, "auth", account.ID), Target: "/root/.cli-proxy-api"},
				{Type: mount.TypeBind, Source: filepath.Join(runtime.root, "logs", account.ID), Target: "/CLIProxyAPI/logs"},
			},
		},
		NetworkingConfig: &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{
			runtime.network: {Aliases: []string{service}},
		}},
	}, nil
}

func (runtime *AccountRuntime) imageAndListenAddress(ctx context.Context) (string, string, error) {
	settings, err := runtime.store.ReadSettings(ctx)
	if err != nil {
		return "", "", fmt.Errorf("read account runtime settings: %w", err)
	}
	image := ""
	state := struct {
		Applied struct {
			ResolvedReference string `json:"resolved_ref"`
		} `json:"applied"`
	}{}
	if _, err := runtime.store.ReadRuntimeState(ctx, "cliproxy_image", &state); err != nil {
		return "", "", fmt.Errorf("read applied CPA image state: %w", err)
	}
	image = strings.TrimSpace(state.Applied.ResolvedReference)
	if image == "" {
		image, err = runtimeStringSetting(settings, "runtime.cliproxy_image", DefaultCPAImageUpdateChannel)
		if err != nil {
			return "", "", err
		}
	}
	listenAddress, err := accountListenAddress(settings)
	if err != nil {
		return "", "", err
	}
	if image == "" || len(image) > 512 || strings.ContainsAny(image, "\r\n\t ") {
		return "", "", errors.New("applied CPA image reference is invalid")
	}
	return image, strings.TrimSpace(listenAddress), nil
}

func (runtime *AccountRuntime) configuredAccountListenAddress(ctx context.Context) (string, error) {
	settings, err := runtime.store.ReadSettings(ctx)
	if err != nil {
		return "", fmt.Errorf("read account runtime settings: %w", err)
	}
	return accountListenAddress(settings)
}

func accountListenAddress(settings map[string]any) (string, error) {
	return runtimeStringSetting(settings, "accounts.listen_address", defaultAccountListen)
}

func (runtime *AccountRuntime) probeAccount(ctx context.Context, accountID string) error {
	keys, err := runtime.store.ReadInternalKeys(ctx)
	if err != nil {
		return fmt.Errorf("read account probe credentials: %w", err)
	}
	users := make([]string, 0, len(keys))
	for user, key := range keys {
		if key.Status == "active" && strings.TrimSpace(key.Key) != "" {
			users = append(users, user)
		}
	}
	sort.Strings(users)
	expectedStatus := http.StatusOK
	probeKey := ""
	endpoint := "http://cliproxy-" + accountID + ":8317/v1/models"
	if len(users) == 0 {
		// CLIProxyAPI treats api-keys: [] as an unauthenticated model endpoint,
		// so /v1/models cannot prove the zero-user bootstrap configuration.
		// The authenticated management endpoint proves both HTTP readiness and
		// that the generated per-account management credential was loaded.
		managementKey, found, readError := runtime.store.ReadSecret(ctx, "cpa_management_key")
		managementKey = strings.TrimSpace(managementKey)
		if readError != nil {
			return fmt.Errorf("read account probe management credential: %w", readError)
		}
		if !found || managementKey == "" || strings.ContainsAny(managementKey, "\r\n\x00") {
			return errors.New("cannot verify account runtime without a valid management credential")
		}
		probeKey = managementKey
		endpoint = "http://cliproxy-" + accountID + ":8317/v0/management/auth-files"
	} else {
		probeKey = keys[users[0]].Key
	}
	probeContext, cancel := context.WithTimeout(ctx, runtime.probeTimeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastStatus int
	var lastError error
	for {
		request := runtime.http.R().
			SetContext(probeContext).
			SetDoNotParseResponse(true)
		request.SetHeader("Authorization", "Bearer "+probeKey)
		response, requestError := request.Get(endpoint)
		if response != nil {
			lastStatus = response.StatusCode()
			if response.RawBody() != nil {
				_ = response.RawBody().Close()
			}
		}
		if requestError == nil && lastStatus == expectedStatus {
			return nil
		}
		lastError = requestError
		select {
		case <-probeContext.Done():
			return fmt.Errorf(
				"account %s runtime probe failed with status %d, expected %d: %w",
				accountID, lastStatus, expectedStatus, errors.Join(lastError, probeContext.Err()),
			)
		case <-ticker.C:
		}
	}
}

func (runtime *AccountRuntime) findAccountContainer(ctx context.Context, accountID string) (Service, bool, error) {
	accountID, err := controlplane.NormalizeAccountID(accountID)
	if err != nil {
		return Service{}, false, err
	}
	service := "cliproxy-" + accountID
	filter := make(dockerclient.Filters).
		Add("label", composeProjectLabel+"="+runtime.project).
		Add("label", composeServiceLabel+"="+service)
	containers, err := runtime.client.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return Service{}, false, fmt.Errorf("list account container %s: %w", accountID, err)
	}
	matches := make([]Service, 0, len(containers.Items))
	for _, candidate := range containers.Items {
		if candidate.Labels[composeProjectLabel] != runtime.project || candidate.Labels[composeServiceLabel] != service {
			continue
		}
		name := ""
		if len(candidate.Names) > 0 {
			name = strings.TrimPrefix(candidate.Names[0], "/")
		}
		matches = append(matches, Service{
			Service: service, ContainerID: shortID(candidate.ID), Name: name,
			Image: candidate.Image, State: strings.ToLower(strings.TrimSpace(string(candidate.State))),
			Status: candidate.Status, dockerID: candidate.ID, imageID: candidate.ImageID,
		})
	}
	if len(matches) > 1 {
		return Service{}, false, fmt.Errorf("%w: multiple containers claim account service %s", ErrRuntimeConflict, service)
	}
	if len(matches) == 0 {
		return Service{}, false, nil
	}
	return matches[0], true, nil
}

func (runtime *AccountRuntime) removeContainer(ctx context.Context, containerID string) error {
	if strings.TrimSpace(containerID) == "" {
		return nil
	}
	inspection, err := runtime.client.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspect account container before removal: %w", err)
	}
	if inspection.Container.State != nil && inspection.Container.State.Running {
		timeout := 30
		if _, err := runtime.client.ContainerStop(ctx, containerID, dockerclient.ContainerStopOptions{Timeout: &timeout}); err != nil {
			return fmt.Errorf("stop account container: %w", err)
		}
	}
	if _, err := runtime.client.ContainerRemove(ctx, containerID, dockerclient.ContainerRemoveOptions{}); err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("remove account container: %w", err)
	}
	return nil
}

func runtimeStringSetting(settings map[string]any, key, fallback string) (string, error) {
	raw, found := settings[key]
	if !found || raw == nil {
		return fallback, nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("runtime setting %s must be a string", key)
	}
	return value, nil
}

func requireRuntimeDirectory(path string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("required account runtime path %s: %w", path, err)
	}
	if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("account runtime path must be a real directory: %s", path)
	}
	return nil
}

func requireRuntimeRegularFile(path string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("required account runtime path %s: %w", path, err)
	}
	if !information.Mode().IsRegular() || information.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("account runtime path must be a regular file: %s", path)
	}
	return nil
}

func wrapRuntimeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func compactErrors(values []error) []error {
	result := make([]error, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, value)
		}
	}
	return result
}

var _ accountlifecycle.Runtime = (*AccountRuntime)(nil)
