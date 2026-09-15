package runtimeops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"

	containertypes "github.com/moby/moby/api/types/container"

	dockerclient "github.com/moby/moby/client"
	"golang.org/x/mod/semver"
)

const (
	DefaultCPAImageUpdateChannel = "docker.m.daocloud.io/eceasy/cli-proxy-api:latest"
	cpaImageBannerProbeTimeout   = 15 * time.Second
	cpaImageBannerMaximumOutput  = 64 * 1024
)

type imageRuntimeStore interface {
	ReadSettings(context.Context) (map[string]any, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
}

type imageDockerClient interface {
	ImageInspect(context.Context, string, ...dockerclient.ImageInspectOption) (dockerclient.ImageInspectResult, error)
	ImageList(context.Context, dockerclient.ImageListOptions) (dockerclient.ImageListResult, error)
}

type cpaImageDockerClient interface {
	imageDockerClient
	ImagePull(context.Context, string, dockerclient.ImagePullOptions) (dockerclient.ImagePullResponse, error)
	ImageTag(context.Context, dockerclient.ImageTagOptions) (dockerclient.ImageTagResult, error)
}

type cpaImageBannerDockerClient interface {
	ContainerCreate(context.Context, dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error)
	ContainerStart(context.Context, string, dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error)
	ContainerWait(context.Context, string, dockerclient.ContainerWaitOptions) dockerclient.ContainerWaitResult
	ContainerLogs(context.Context, string, dockerclient.ContainerLogsOptions) (dockerclient.ContainerLogsResult, error)
	ContainerRemove(context.Context, string, dockerclient.ContainerRemoveOptions) (dockerclient.ContainerRemoveResult, error)
}

type cpaImageStateStore interface {
	imageRuntimeStore
	WriteRuntimeState(context.Context, string, any) error
	DeleteRuntimeState(context.Context, string) error
}

var (
	semanticImageVersionPattern  = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	cpaImageVersionBannerPattern = regexp.MustCompile(`CLIProxyAPI Version:\s*([^,\s]+),\s*Commit:\s*([^,\s]+),\s*BuiltAt:\s*([^\r\n]+)`)
)

type CPAImageLocal struct {
	Available   bool     `json:"available"`
	ID          string   `json:"id"`
	ShortID     string   `json:"short_id"`
	Created     string   `json:"created"`
	RepoDigests []string `json:"repo_digests"`
	Version     string   `json:"version"`
	Commit      string   `json:"commit"`
	BuiltAt     string   `json:"built_at"`
	ResolvedRef string   `json:"resolved_ref"`
}

type CPAAccountImage struct {
	Account           string `json:"account"`
	Service           string `json:"service"`
	Enabled           bool   `json:"enabled"`
	ContainerExists   bool   `json:"container_exists"`
	Running           bool   `json:"running"`
	State             string `json:"state"`
	ImageRef          string `json:"image_ref"`
	ImageID           string `json:"image_id"`
	ImageShortID      string `json:"image_short_id"`
	Version           string `json:"version"`
	UsingTarget       bool   `json:"using_target"`
	RollbackAvailable bool   `json:"rollback_available"`
}

type CPAImageStatus struct {
	TargetImage   string            `json:"target_image"`
	UpdateChannel string            `json:"update_channel"`
	Candidate     map[string]any    `json:"candidate"`
	Applied       map[string]any    `json:"applied"`
	LocalImage    CPAImageLocal     `json:"local_image"`
	Accounts      []CPAAccountImage `json:"accounts"`
	RunningCount  int               `json:"running_count"`
	CurrentCount  int               `json:"current_count"`
	OutdatedCount int               `json:"outdated_count"`
}

func (manager *Manager) CPAImageStatus(ctx context.Context) (CPAImageStatus, error) {
	store, ok := manager.accounts.(imageRuntimeStore)
	if !ok {
		return CPAImageStatus{}, fmt.Errorf("runtime account catalog does not expose image state")
	}
	docker, ok := manager.client.(imageDockerClient)
	if !ok {
		return CPAImageStatus{}, fmt.Errorf("Docker client does not expose image inspection")
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("read CPA image settings: %w", err)
	}
	target := strings.TrimSpace(stringValue(settings["runtime.cliproxy_image"]))
	if target == "" {
		target = DefaultCPAImageUpdateChannel
	}
	state := make(map[string]any)
	if _, err := store.ReadRuntimeState(ctx, "cliproxy_image", &state); err != nil {
		return CPAImageStatus{}, fmt.Errorf("read CPA image runtime state: %w", err)
	}
	result := CPAImageStatus{
		TargetImage: target, UpdateChannel: target,
		Candidate: mapValue(state["candidate"]), Applied: mapValue(state["applied"]),
		Accounts: make([]CPAAccountImage, 0),
	}
	inspected, inspectErr := docker.ImageInspect(ctx, target)
	if inspectErr == nil {
		result.LocalImage = CPAImageLocal{
			Available: true, ID: inspected.ID, ShortID: shortID(inspected.ID),
			Created: inspected.Created, RepoDigests: append([]string{}, inspected.RepoDigests...),
		}
		if stringValue(result.Candidate["image_id"]) != inspected.ID {
			result.Candidate = map[string]any{}
		}
		result.LocalImage.Version = stringValue(result.Candidate["version"])
		result.LocalImage.Commit = stringValue(result.Candidate["commit"])
		result.LocalImage.BuiltAt = stringValue(result.Candidate["built_at"])
		result.LocalImage.ResolvedRef = stringValue(result.Candidate["resolved_ref"])
	} else if !errdefs.IsNotFound(inspectErr) {
		return CPAImageStatus{}, fmt.Errorf("inspect target CPA image: %w", inspectErr)
	} else {
		result.Candidate = map[string]any{}
		result.LocalImage.RepoDigests = []string{}
	}
	images, err := docker.ImageList(ctx, dockerclient.ImageListOptions{})
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("list local CPA images: %w", err)
	}
	availableRefs := make(map[string]struct{})
	for _, image := range images.Items {
		for _, reference := range image.RepoTags {
			availableRefs[reference] = struct{}{}
		}
	}
	accounts, err := manager.accounts.ReadAccounts(ctx)
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("read CPA image accounts: %w", err)
	}
	filter := make(dockerclient.Filters).Add("label", composeProjectLabel+"="+manager.project)
	containers, err := manager.client.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return CPAImageStatus{}, fmt.Errorf("list CPA image containers: %w", err)
	}
	byService := make(map[string]containertypes.Summary)
	for _, container := range containers.Items {
		if container.Labels[composeProjectLabel] != manager.project {
			continue
		}
		service := strings.TrimSpace(container.Labels[composeServiceLabel])
		if strings.HasPrefix(service, "cliproxy-") {
			byService[service] = container
		}
	}
	for _, account := range accounts {
		service := "cliproxy-" + account.ID
		container, exists := byService[service]
		stateName := "missing"
		if exists {
			stateName = strings.ToLower(strings.TrimSpace(string(container.State)))
		}
		running := exists && stateName == "running"
		identity := knownImageIdentity(state, container.ImageID)
		rollbackReference := rollbackReference(container.Names, account.ID)
		_, rollbackAvailable := availableRefs[rollbackReference]
		item := CPAAccountImage{
			Account: account.ID, Service: service, Enabled: account.GroupEnabled,
			ContainerExists: exists, Running: running, State: stateName,
			ImageRef: container.Image, ImageID: container.ImageID, ImageShortID: shortID(container.ImageID),
			Version:           stringValue(identity["version"]),
			UsingTarget:       result.LocalImage.Available && container.ImageID == result.LocalImage.ID,
			RollbackAvailable: rollbackAvailable,
		}
		result.Accounts = append(result.Accounts, item)
		if account.GroupEnabled && running {
			result.RunningCount++
			if item.UsingTarget {
				result.CurrentCount++
			} else {
				result.OutdatedCount++
			}
		}
	}
	sort.Slice(result.Accounts, func(left, right int) bool {
		return result.Accounts[left].Account < result.Accounts[right].Account
	})
	return result, nil
}

// PullImage pulls the configured mutable channel, resolves the resulting
// local image to an immutable identity, and records it only as candidate. It
// never changes the applied Compose projection or recreates a CPA.
func (runtime *AccountRuntime) PullImage(ctx context.Context, output io.Writer) (OperationResult, error) {
	if output == nil {
		return OperationResult{}, errors.New("CPA image pull requires an output writer")
	}
	store, docker, err := runtime.imageMutationDependencies(false)
	if err != nil {
		return OperationResult{}, err
	}
	target, err := runtime.configuredCPAImage(ctx)
	if err != nil {
		return OperationResult{}, err
	}
	_, _ = fmt.Fprint(output, i18n.M("runtimeops.pulling_cpa_image", i18n.Params{"Value": target}).Render(i18n.English))
	stream, err := docker.ImagePull(ctx, target, dockerclient.ImagePullOptions{})
	if err != nil {
		return OperationResult{}, fmt.Errorf("pull CPA image: %w", err)
	}
	defer stream.Close()
	if err := stream.Wait(ctx); err != nil {
		return OperationResult{}, fmt.Errorf("wait for CPA image pull: %w", err)
	}
	inspected, err := docker.ImageInspect(ctx, target)
	if err != nil {
		return OperationResult{}, fmt.Errorf("inspect pulled CPA image: %w", err)
	}
	identity, err := resolveCPAImageIdentityWithBanner(ctx, docker, target, inspected)
	if err != nil {
		return OperationResult{}, err
	}
	candidate, err := writeCPAImageCandidate(ctx, store, identity, time.Now().Unix())
	if err != nil {
		return OperationResult{}, err
	}
	_, _ = fmt.Fprint(output, i18n.M("runtimeops.image_ready", i18n.Params{"Value1": target, "Value2": firstImageIdentityValue(candidate, "version", i18n.Text(i18n.English, "runtimeops.image_has_no_recognizable_version")), "Value3": firstImageIdentityValue(candidate, "image_short_id", i18n.Text(i18n.English, "runtimeops.unknown_digest"))}).Render(i18n.English))
	return OperationResult{Action: "image-pull", Target: "all", Services: []Service{}}, nil
}

type cpaImageRollback struct {
	account     controlplane.Account
	oldImageID  string
	rollbackRef string
}

// UpdateImage applies the already-pulled immutable candidate to one account or
// all running enabled accounts. Every old image is tagged before the first
// recreate. A probe, state-commit, or Compose-projection failure rolls all
// attempted accounts back in reverse order.
func (runtime *AccountRuntime) UpdateImage(ctx context.Context, rawTarget string, output io.Writer) (OperationResult, error) {
	if output == nil {
		return OperationResult{}, errors.New("CPA image update requires an output writer")
	}
	store, docker, err := runtime.imageMutationDependencies(true)
	if err != nil {
		return OperationResult{}, err
	}
	target := normalizeTarget(rawTarget)
	accounts, err := runtime.accounts.ReadAccounts(ctx)
	if err != nil {
		return OperationResult{}, fmt.Errorf("read CPA image accounts: %w", err)
	}
	sort.Slice(accounts, func(left, right int) bool { return accounts[left].ID < accounts[right].ID })
	selected := make([]controlplane.Account, 0, len(accounts))
	for _, account := range accounts {
		if target != "all" && account.ID != target {
			continue
		}
		if target != "all" && !account.GroupEnabled {
			return OperationResult{}, i18n.M("runtimeops.cpa_account_is_disabled_and_cannot_be_updated", i18n.Params{"Detail": ErrRuntimeTarget, "Value2": target}).WithCause(ErrRuntimeTarget)
		}
		selected = append(selected, account)
	}
	if target != "all" && len(selected) != 1 {
		return OperationResult{}, i18n.M("runtimeops.select_all_or_a_valid_cpa_account_for_the_image", i18n.Params{"Detail": ErrRuntimeTarget}).WithCause(ErrRuntimeTarget)
	}
	configured, err := runtime.configuredCPAImage(ctx)
	if err != nil {
		return OperationResult{}, err
	}
	inspected, err := docker.ImageInspect(ctx, configured)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return OperationResult{}, i18n.M("runtimeops.the_target_image_has_not_been_pulled_run_pull_image")
		}
		return OperationResult{}, fmt.Errorf("inspect target CPA image: %w", err)
	}
	identity, err := runtime.matchOrRecordCPAImageCandidate(ctx, store, configured, inspected)
	if err != nil {
		return OperationResult{}, err
	}
	resolvedReference := strings.TrimSpace(stringValue(identity["resolved_ref"]))
	if resolvedReference == "" {
		return OperationResult{}, i18n.M("runtimeops.the_target_image_has_no_applicable_immutable_identifier")
	}

	rollbacks := make([]cpaImageRollback, 0, len(selected))
	alreadyCurrent := make([]controlplane.Account, 0, len(selected))
	for _, account := range selected {
		if !account.GroupEnabled {
			_, _ = fmt.Fprint(output, i18n.M("runtimeops.skipping_cpa_is_disabled", i18n.Params{"Value": account.ID}).Render(i18n.English))
			continue
		}
		container, found, err := runtime.findAccountContainer(ctx, account.ID)
		if err != nil {
			return OperationResult{}, err
		}
		if !found || container.State != "running" {
			_, _ = fmt.Fprint(output, i18n.M("runtimeops.skipping_cpa_is_stopped_the_next_start_will_use_the", i18n.Params{"Value": account.ID}).Render(i18n.English))
			continue
		}
		if containerImageID(container) == inspected.ID {
			_, _ = fmt.Fprint(output, i18n.M("runtimeops.skipping_already_running_the_target_image", i18n.Params{"Value": account.ID}).Render(i18n.English))
			alreadyCurrent = append(alreadyCurrent, account)
			continue
		}
		oldImageID := containerImageID(container)
		if oldImageID == "" {
			return OperationResult{}, fmt.Errorf("account %s running container has no image identity", account.ID)
		}
		rollbackRef := runtime.instance + "-cpa-rollback:" + account.ID
		if _, err := docker.ImageTag(ctx, dockerclient.ImageTagOptions{Source: oldImageID, Target: rollbackRef}); err != nil {
			return OperationResult{}, fmt.Errorf("tag rollback image for %s: %w", account.ID, err)
		}
		rollbacks = append(rollbacks, cpaImageRollback{account: account, oldImageID: oldImageID, rollbackRef: rollbackRef})
	}

	if len(rollbacks) == 0 {
		if len(alreadyCurrent) == 0 {
			_, _ = fmt.Fprintln(output, i18n.Text(i18n.English, "runtimeops.no_running_cpas_the_applied_version_was_not_changed"))
			return OperationResult{Action: "image-update", Target: target, Services: []Service{}}, nil
		}
		for _, account := range alreadyCurrent {
			if err := runtime.probeImageAccount(ctx, account.ID, output); err != nil {
				return OperationResult{}, err
			}
		}
		if err := runtime.commitCPAImageApplied(ctx, store, identity); err != nil {
			return OperationResult{}, err
		}
		_, _ = fmt.Fprintln(output, i18n.Text(i18n.English, "runtimeops.running_cpas_verified_the_target_version_is_pinned"))
		return OperationResult{Action: "image-update", Target: target, Services: []Service{}}, nil
	}

	attempted := make([]cpaImageRollback, 0, len(rollbacks))
	updated := make([]Service, 0, len(rollbacks))
	var operationError error
	for _, snapshot := range rollbacks {
		attempted = append(attempted, snapshot)
		_, _ = fmt.Fprint(output, i18n.M("runtimeops.updating", i18n.Params{"Value1": snapshot.account.ID, "Value2": shortID(snapshot.oldImageID), "Value3": shortID(inspected.ID)}).Render(i18n.English))
		if err := runtime.replaceAccountImage(ctx, snapshot.account, resolvedReference, output); err != nil {
			operationError = err
			break
		}
		service, _, err := runtime.findAccountContainer(ctx, snapshot.account.ID)
		if err != nil {
			operationError = err
			break
		}
		updated = append(updated, service)
	}
	if operationError == nil {
		operationError = runtime.commitCPAImageApplied(ctx, store, identity)
	}
	if operationError != nil {
		_, _ = fmt.Fprint(output, i18n.M("runtimeops.image_update_failed_restoring_processed_cpas", i18n.Params{"Value": Sanitize(operationError.Error())}).Render(i18n.English))
		rollbackError := runtime.rollbackCPAImages(ctx, attempted, output)
		if rollbackError != nil {
			return OperationResult{}, errors.Join(operationError, i18n.M("runtimeops.some_cpa_rollbacks_failed", i18n.Params{"Detail": rollbackError}).WithCause(rollbackError))
		}
		return OperationResult{}, operationError
	}
	_, _ = fmt.Fprint(output, i18n.M("runtimeops.cpa_image_update_completed", i18n.Params{"Count": len(updated)}).Render(i18n.English))
	return OperationResult{Action: "image-update", Target: target, Services: updated}, nil
}

func (runtime *AccountRuntime) imageMutationDependencies(requireProjector bool) (cpaImageStateStore, cpaImageDockerClient, error) {
	store, ok := runtime.store.(cpaImageStateStore)
	if !ok {
		return nil, nil, errors.New("control-plane store does not expose CPA image state mutations")
	}
	docker, ok := runtime.client.(cpaImageDockerClient)
	if !ok {
		return nil, nil, errors.New("Docker client does not expose CPA image mutations")
	}
	if requireProjector && runtime.imageProjector == nil {
		return nil, nil, errors.New("CPA image Compose projector is unavailable")
	}
	return store, docker, nil
}

func (runtime *AccountRuntime) configuredCPAImage(ctx context.Context) (string, error) {
	settings, err := runtime.store.ReadSettings(ctx)
	if err != nil {
		return "", fmt.Errorf("read CPA image settings: %w", err)
	}
	target := strings.TrimSpace(stringValue(settings["runtime.cliproxy_image"]))
	if target == "" {
		target = DefaultCPAImageUpdateChannel
	}
	if target == "" || len(target) > 512 || strings.ContainsAny(target, "\r\n\t \x00") {
		return "", errors.New("configured CPA image reference is invalid")
	}
	return target, nil
}

func (runtime *AccountRuntime) matchOrRecordCPAImageCandidate(
	ctx context.Context,
	store cpaImageStateStore,
	configured string,
	inspected dockerclient.ImageInspectResult,
) (map[string]any, error) {
	state := make(map[string]any)
	if _, err := store.ReadRuntimeState(ctx, "cliproxy_image", &state); err != nil {
		return nil, fmt.Errorf("read CPA image runtime state: %w", err)
	}
	candidate := mapValue(state["candidate"])
	if stringValue(candidate["image_id"]) == inspected.ID {
		return cloneStringMap(candidate), nil
	}
	docker, ok := runtime.client.(cpaImageDockerClient)
	if !ok {
		return nil, errors.New("Docker client does not expose CPA image mutations")
	}
	identity, err := resolveCPAImageIdentityWithBanner(ctx, docker, configured, inspected)
	if err != nil {
		return nil, err
	}
	return writeCPAImageCandidate(ctx, store, identity, time.Now().Unix())
}

func (runtime *AccountRuntime) replaceAccountImage(
	ctx context.Context,
	account controlplane.Account,
	imageReference string,
	output io.Writer,
) error {
	container, found, err := runtime.findAccountContainer(ctx, account.ID)
	if err != nil {
		return err
	}
	if !found || container.State != "running" {
		return fmt.Errorf("account %s stopped before image replacement", account.ID)
	}
	if err := runtime.removeContainer(ctx, container.dockerID); err != nil {
		return err
	}
	containerID, err := runtime.createAccountContainerWithImage(ctx, account, true, imageReference)
	if err != nil {
		return err
	}
	if err := runtime.probeImageAccount(ctx, account.ID, output); err != nil {
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtime.probeTimeout)
		defer cancel()
		return errors.Join(err, wrapRuntimeError("remove failed image candidate", runtime.removeContainer(rollbackContext, containerID)))
	}
	return nil
}

func (runtime *AccountRuntime) probeImageAccount(ctx context.Context, accountID string, output io.Writer) error {
	if err := runtime.probeAccount(ctx, accountID); err != nil {
		return err
	}
	_, _ = fmt.Fprint(output, i18n.M("runtimeops.verified_runtime_probe", i18n.Params{"Field": accountID}).Render(i18n.English))
	return nil
}

func (runtime *AccountRuntime) rollbackCPAImages(ctx context.Context, attempted []cpaImageRollback, output io.Writer) error {
	timeout := 30*time.Second + time.Duration(len(attempted))*runtime.probeTimeout
	rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	errorsFound := make([]error, 0)
	for index := len(attempted) - 1; index >= 0; index-- {
		snapshot := attempted[index]
		container, found, err := runtime.findAccountContainer(rollbackContext, snapshot.account.ID)
		if err == nil && found {
			err = runtime.removeContainer(rollbackContext, container.dockerID)
		}
		if err == nil {
			_, err = runtime.createAccountContainerWithImage(rollbackContext, snapshot.account, true, snapshot.rollbackRef)
		}
		if err == nil {
			err = runtime.probeImageAccount(rollbackContext, snapshot.account.ID, output)
		}
		if err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("%s: %w", snapshot.account.ID, err))
			continue
		}
		_, _ = fmt.Fprint(output, i18n.M("runtimeops.restored", i18n.Params{"Value": snapshot.account.ID}).Render(i18n.English))
	}
	return errors.Join(errorsFound...)
}

func (runtime *AccountRuntime) commitCPAImageApplied(
	ctx context.Context,
	store cpaImageStateStore,
	identity map[string]any,
) error {
	previous := make(map[string]any)
	found, err := store.ReadRuntimeState(ctx, "cliproxy_image", &previous)
	if err != nil {
		return fmt.Errorf("read CPA image state before commit: %w", err)
	}
	listenAddress, err := runtime.configuredAccountListenAddress(ctx)
	if err != nil {
		return fmt.Errorf("read account listen address before commit: %w", err)
	}
	before := cloneStringMap(previous)
	next := cloneStringMap(previous)
	history := cloneStringMap(mapValue(next["history"]))
	for _, record := range []map[string]any{mapValue(next["applied"]), identity} {
		if imageID := stringValue(record["image_id"]); imageID != "" {
			history[imageID] = cloneStringMap(record)
		}
	}
	trimCPAImageHistory(history, 32)
	now := time.Now().Unix()
	candidate := cloneStringMap(identity)
	if existing := mapValue(next["candidate"]); stringValue(existing["image_id"]) == stringValue(identity["image_id"]) {
		candidate["pulled_at"] = imageIdentityTimestamp(existing, "pulled_at", now)
	} else {
		candidate["pulled_at"] = now
	}
	applied := cloneStringMap(identity)
	applied["applied_at"] = now
	next["history"], next["candidate"], next["applied"] = history, candidate, applied
	if err := store.WriteRuntimeState(ctx, "cliproxy_image", next); err != nil {
		return fmt.Errorf("write applied CPA image state: %w", err)
	}
	resolvedReference := stringValue(identity["resolved_ref"])
	if err := runtime.imageProjector.ProjectCPAImage(ctx, resolvedReference, listenAddress); err != nil {
		var restoreError error
		if found {
			restoreError = store.WriteRuntimeState(context.WithoutCancel(ctx), "cliproxy_image", before)
		} else {
			restoreError = store.DeleteRuntimeState(context.WithoutCancel(ctx), "cliproxy_image")
		}
		return errors.Join(fmt.Errorf("project applied CPA image: %w", err), wrapRuntimeError("restore CPA image state", restoreError))
	}
	return nil
}

func resolveCPAImageIdentity(reference string, inspected dockerclient.ImageInspectResult) (map[string]any, error) {
	imageID := strings.TrimSpace(inspected.ID)
	if imageID == "" {
		return nil, errors.New("pulled CPA image has no immutable image ID")
	}
	repoDigests := make([]string, 0, len(inspected.RepoDigests))
	for _, value := range inspected.RepoDigests {
		if value = strings.TrimSpace(value); value != "" {
			repoDigests = append(repoDigests, value)
		}
	}
	sourceRepository := imageRepository(reference)
	repoDigest := ""
	for _, value := range repoDigests {
		if strings.SplitN(value, "@", 2)[0] == sourceRepository {
			repoDigest = value
			break
		}
	}
	if repoDigest == "" && len(repoDigests) > 0 {
		repoDigest = repoDigests[0]
	}
	version, commit, builtAt := "", "", ""
	if inspected.Config != nil {
		labels := inspected.Config.Labels
		if labelVersion := strings.TrimSpace(labels["org.opencontainers.image.version"]); semanticImageVersionPattern.MatchString(labelVersion) {
			version = labelVersion
		}
		commit = strings.TrimSpace(labels["org.opencontainers.image.revision"])
		builtAt = strings.TrimSpace(labels["org.opencontainers.image.created"])
	}
	if version == "" {
		versions := make([]string, 0, len(inspected.RepoTags))
		for _, repoTag := range inspected.RepoTags {
			repository := imageRepository(repoTag)
			tag := strings.TrimPrefix(strings.TrimPrefix(repoTag, repository), ":")
			if repository == sourceRepository && semanticImageVersionPattern.MatchString(tag) {
				versions = append(versions, tag)
			}
		}
		sort.SliceStable(versions, func(left, right int) bool {
			return semver.Compare(normalizedImageSemver(versions[left]), normalizedImageSemver(versions[right])) > 0
		})
		if len(versions) > 0 {
			version = versions[0]
		}
	}
	resolvedReference := repoDigest
	if resolvedReference == "" {
		resolvedReference = imageID
	}
	return map[string]any{
		"source_ref": reference, "version": version, "commit": commit, "built_at": builtAt,
		"image_id": imageID, "image_short_id": shortID(imageID), "repo_digest": repoDigest,
		"repo_digests": repoDigests, "resolved_ref": resolvedReference,
	}, nil
}

func resolveCPAImageIdentityWithBanner(
	ctx context.Context,
	docker cpaImageDockerClient,
	reference string,
	inspected dockerclient.ImageInspectResult,
) (map[string]any, error) {
	identity, err := resolveCPAImageIdentity(reference, inspected)
	if err != nil || stringValue(identity["version"]) != "" {
		return identity, err
	}
	client, ok := any(docker).(cpaImageBannerDockerClient)
	if !ok {
		return identity, nil
	}
	version, commit, builtAt, probeError := probeCPAImageVersionBanner(ctx, client, inspected)
	if probeError != nil || !semanticImageVersionPattern.MatchString(version) {
		return identity, nil
	}
	identity["version"] = version
	if stringValue(identity["commit"]) == "" {
		identity["commit"] = commit
	}
	if stringValue(identity["built_at"]) == "" {
		identity["built_at"] = builtAt
	}
	return identity, nil
}

func probeCPAImageVersionBanner(
	ctx context.Context,
	client cpaImageBannerDockerClient,
	inspected dockerclient.ImageInspectResult,
) (string, string, string, error) {
	imageID := strings.TrimSpace(inspected.ID)
	if imageID == "" {
		return "", "", "", errors.New("CPA image version probe requires an immutable image ID")
	}
	entrypoint, command, err := cpaImageBannerProbeCommand(inspected)
	if err != nil {
		return "", "", "", err
	}
	pidsLimit := int64(64)
	probeContext, cancel := context.WithTimeout(ctx, cpaImageBannerProbeTimeout)
	defer cancel()
	created, err := client.ContainerCreate(probeContext, dockerclient.ContainerCreateOptions{
		Name: "cpa-image-version-" + uuid.NewString()[:8],
		Config: &containertypes.Config{
			Image: imageID, Entrypoint: entrypoint, Cmd: command, NetworkDisabled: true,
			Labels: map[string]string{
				"io.codex-cpa.managed-by": "codex-cpa",
				"io.codex-cpa.operation":  "image-version-probe",
			},
		},
		HostConfig: &containertypes.HostConfig{
			NetworkMode: containertypes.NetworkMode("none"), ReadonlyRootfs: true,
			CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
			Resources:     containertypes.Resources{PidsLimit: &pidsLimit},
			RestartPolicy: containertypes.RestartPolicy{Name: containertypes.RestartPolicyDisabled},
		},
	})
	if err != nil {
		return "", "", "", fmt.Errorf("create CPA image version probe: %w", err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		_, _ = client.ContainerRemove(cleanupContext, created.ID, dockerclient.ContainerRemoveOptions{Force: true})
	}()
	wait := client.ContainerWait(probeContext, created.ID, dockerclient.ContainerWaitOptions{
		Condition: containertypes.WaitConditionNextExit,
	})
	if _, err := client.ContainerStart(probeContext, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		return "", "", "", fmt.Errorf("start CPA image version probe: %w", err)
	}
	select {
	case response := <-wait.Result:
		if response.Error != nil && strings.TrimSpace(response.Error.Message) != "" {
			return "", "", "", fmt.Errorf("CPA image version probe failed: %s", Sanitize(response.Error.Message))
		}
	case waitError := <-wait.Error:
		return "", "", "", fmt.Errorf("wait for CPA image version probe: %w", waitError)
	case <-probeContext.Done():
		return "", "", "", probeContext.Err()
	}
	logs, err := client.ContainerLogs(probeContext, created.ID, dockerclient.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: "all",
	})
	if err != nil {
		return "", "", "", fmt.Errorf("read CPA image version probe output: %w", err)
	}
	defer logs.Close()
	output := &limitedBannerOutput{limit: cpaImageBannerMaximumOutput}
	if _, err := stdcopy.StdCopy(output, output, logs); err != nil {
		return "", "", "", fmt.Errorf("decode CPA image version probe output: %w", err)
	}
	match := cpaImageVersionBannerPattern.FindStringSubmatch(output.String())
	if len(match) != 4 {
		return "", "", "", errors.New("CPA image version probe returned no recognized banner")
	}
	return strings.TrimSpace(match[1]), strings.TrimSpace(match[2]), strings.TrimSpace(match[3]), nil
}

// cpaImageBannerProbeCommand never delegates version discovery to a shell,
// init wrapper, or arbitrary image-defined entrypoint. Only the expected
// CLIProxyAPI executable is allowed, and all inherited default arguments are
// replaced by the single bounded help flag.
func cpaImageBannerProbeCommand(inspected dockerclient.ImageInspectResult) ([]string, []string, error) {
	if inspected.Config == nil {
		return nil, nil, errors.New("CPA image version probe requires image process metadata")
	}
	executable := ""
	if len(inspected.Config.Entrypoint) > 0 {
		if len(inspected.Config.Entrypoint) != 1 {
			return nil, nil, errors.New("CPA image version probe refused a wrapped entrypoint")
		}
		executable = inspected.Config.Entrypoint[0]
	} else if len(inspected.Config.Cmd) > 0 {
		executable = inspected.Config.Cmd[0]
	} else {
		executable = "./CLIProxyAPI"
	}
	executable = strings.TrimSpace(executable)
	if !strings.EqualFold(path.Base(executable), "CLIProxyAPI") {
		return nil, nil, errors.New("CPA image version probe refused an unexpected executable")
	}
	return []string{executable}, []string{"-h"}, nil
}

type limitedBannerOutput struct {
	buffer strings.Builder
	limit  int
}

func (output *limitedBannerOutput) Write(payload []byte) (int, error) {
	available := max(0, output.limit-output.buffer.Len())
	if available > 0 {
		_, _ = output.buffer.Write(payload[:min(len(payload), available)])
	}
	return len(payload), nil
}

func (output *limitedBannerOutput) String() string { return output.buffer.String() }

func normalizedImageSemver(value string) string {
	if strings.HasPrefix(value, "v") {
		return value
	}
	return "v" + value
}

func writeCPAImageCandidate(
	ctx context.Context,
	store cpaImageStateStore,
	identity map[string]any,
	pulledAt int64,
) (map[string]any, error) {
	state := make(map[string]any)
	if _, err := store.ReadRuntimeState(ctx, "cliproxy_image", &state); err != nil {
		return nil, fmt.Errorf("read CPA image candidate state: %w", err)
	}
	candidate := cloneStringMap(identity)
	candidate["pulled_at"] = pulledAt
	state["candidate"] = candidate
	if err := store.WriteRuntimeState(ctx, "cliproxy_image", state); err != nil {
		return nil, fmt.Errorf("write CPA image candidate state: %w", err)
	}
	return candidate, nil
}

func imageRepository(reference string) string {
	reference = strings.TrimSpace(strings.SplitN(reference, "@", 2)[0])
	slash := strings.LastIndex(reference, "/")
	colon := strings.LastIndex(reference, ":")
	if colon > slash {
		return reference[:colon]
	}
	return reference
}

func cloneStringMap(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	output := make(map[string]any)
	if json.Unmarshal(raw, &output) != nil {
		return map[string]any{}
	}
	return output
}

func trimCPAImageHistory(history map[string]any, maximum int) {
	if len(history) <= maximum {
		return
	}
	ids := make([]string, 0, len(history))
	for id := range history {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool {
		leftRecord, rightRecord := mapValue(history[ids[left]]), mapValue(history[ids[right]])
		leftTime := max(imageIdentityTimestamp(leftRecord, "applied_at", 0), imageIdentityTimestamp(leftRecord, "pulled_at", 0))
		rightTime := max(imageIdentityTimestamp(rightRecord, "applied_at", 0), imageIdentityTimestamp(rightRecord, "pulled_at", 0))
		if leftTime != rightTime {
			return leftTime > rightTime
		}
		return ids[left] < ids[right]
	})
	for _, id := range ids[maximum:] {
		delete(history, id)
	}
}

func imageIdentityTimestamp(record map[string]any, key string, fallback int64) int64 {
	switch value := record[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return fallback
	}
}

func firstImageIdentityValue(identity map[string]any, key, fallback string) string {
	if value := stringValue(identity[key]); value != "" {
		return value
	}
	return fallback
}

func containerImageID(container Service) string {
	// The private dockerID field identifies the container, whereas Image holds
	// the mutable display reference. CPAImageStatus obtains the immutable ID
	// from the ContainerList summary, so image replacement needs the same value.
	return strings.TrimSpace(container.imageID)
}

func knownImageIdentity(state map[string]any, imageID string) map[string]any {
	if imageID == "" {
		return map[string]any{}
	}
	for _, name := range []string{"applied", "candidate"} {
		record := mapValue(state[name])
		if stringValue(record["image_id"]) == imageID {
			return record
		}
	}
	return mapValue(mapValue(state["history"])[imageID])
}

func rollbackReference(names []string, account string) string {
	for _, raw := range names {
		name := strings.TrimPrefix(raw, "/")
		suffix := "-" + account
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix) + "-cpa-rollback:" + account
		}
	}
	return ""
}

func mapValue(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok && typed != nil {
		return typed
	}
	return map[string]any{}
}

func stringValue(value any) string {
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return ""
}
