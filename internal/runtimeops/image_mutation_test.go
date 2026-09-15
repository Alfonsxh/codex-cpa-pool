package runtimeops

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/containerd/errdefs"
	"github.com/go-resty/resty/v2"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	dockerclient "github.com/moby/moby/client"
)

const (
	fixtureMutableImage   = "registry.example.test/cpa:latest"
	fixtureImmutableImage = "registry.example.test/cpa@sha256:newdigest"
	fixtureNewImageID     = "sha256:new-image"
)

func TestAccountRuntimePullImageWritesCandidateOnly(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
	runtime, store, client, projector := newCPAImageRuntime(t, accounts)
	beforeApplied := cloneStringMap(mapValue(store.state["applied"]))
	var output strings.Builder

	result, err := runtime.PullImage(context.Background(), &output)
	if err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	if result.Action != "image-pull" || result.Target != "all" || client.pullReferences[0] != fixtureMutableImage {
		t.Fatalf("pull result=%#v references=%#v", result, client.pullReferences)
	}
	candidate := mapValue(store.state["candidate"])
	if stringValue(candidate["image_id"]) != fixtureNewImageID ||
		stringValue(candidate["resolved_ref"]) != fixtureImmutableImage ||
		!mapsEqual(beforeApplied, mapValue(store.state["applied"])) {
		t.Fatalf("image state after pull = %#v", store.state)
	}
	if projector.calls != 0 || len(client.creates) != 0 || !strings.Contains(output.String(), "Image ready") {
		t.Fatalf("pull side effects: projector=%d creates=%d output=%q", projector.calls, len(client.creates), output.String())
	}
}

func TestAccountRuntimePullImageUsesLatestUpdateChannelByDefault(t *testing.T) {
	runtime, store, client, _ := newCPAImageRuntime(t, nil)
	store.settings = map[string]any{"accounts.listen_address": "127.0.0.1"}
	client.imageIDs[DefaultCPAImageUpdateChannel] = fixtureNewImageID
	var output strings.Builder

	if _, err := runtime.PullImage(context.Background(), &output); err != nil {
		t.Fatalf("PullImage with default update channel: %v", err)
	}
	if len(client.pullReferences) != 1 || client.pullReferences[0] != DefaultCPAImageUpdateChannel {
		t.Fatalf("default pull references = %#v", client.pullReferences)
	}
}

func TestAccountRuntimePullImageUsesSandboxedBannerProbeForMetadataPoorImage(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
	runtime, store, client, projector := newCPAImageRuntime(t, accounts)
	client.inspectResult = &dockerclient.ImageInspectResult{}
	client.inspectResult.ID = fixtureNewImageID
	client.inspectResult.RepoDigests = []string{fixtureImmutableImage}
	client.inspectResult.RepoTags = []string{fixtureMutableImage}
	client.inspectResult.Config = &dockerspec.DockerOCIImageConfig{}
	client.inspectResult.Config.Cmd = []string{"./CLIProxyAPI"}
	client.bannerOutput = "CLIProxyAPI Version: v7.2.141, Commit: banner123, BuiltAt: 2026-08-28T01:02:03Z\n"
	var output strings.Builder

	if _, err := runtime.PullImage(context.Background(), &output); err != nil {
		t.Fatalf("PullImage with banner: %v", err)
	}
	candidate := mapValue(store.state["candidate"])
	if stringValue(candidate["version"]) != "v7.2.141" ||
		stringValue(candidate["commit"]) != "banner123" ||
		stringValue(candidate["built_at"]) != "2026-08-28T01:02:03Z" {
		t.Fatalf("banner candidate = %#v", candidate)
	}
	if projector.calls != 0 || len(client.creates) != 1 || len(client.removed) != 1 {
		t.Fatalf("banner lifecycle: projector=%d creates=%d removed=%#v", projector.calls, len(client.creates), client.removed)
	}
	created := client.creates[0]
	if created.Config.Image != fixtureNewImageID || strings.Join(created.Config.Entrypoint, " ") != "./CLIProxyAPI" ||
		strings.Join(created.Config.Cmd, " ") != "-h" ||
		!created.Config.NetworkDisabled || created.HostConfig.NetworkMode != container.NetworkMode("none") ||
		!created.HostConfig.ReadonlyRootfs || strings.Join(created.HostConfig.CapDrop, ",") != "ALL" ||
		strings.Join(created.HostConfig.SecurityOpt, ",") != "no-new-privileges" ||
		created.HostConfig.PidsLimit == nil || *created.HostConfig.PidsLimit != 64 ||
		len(created.HostConfig.Mounts) != 0 || len(created.HostConfig.Binds) != 0 || len(created.HostConfig.PortBindings) != 0 {
		t.Fatalf("unsafe banner probe options = %#v", created)
	}
	if !strings.Contains(output.String(), "v7.2.141") {
		t.Fatalf("banner output = %q", output.String())
	}
}

func TestCPAImageIdentityPrefersMetadataAndUsesHighestSameRepositoryTag(t *testing.T) {
	inspected := dockerclient.ImageInspectResult{}
	inspected.ID = fixtureNewImageID
	inspected.RepoTags = []string{
		"registry.example.test/cpa:v7.2.9",
		"registry.example.test/other:v99.0.0",
		"registry.example.test/cpa:v7.2.10",
		fixtureMutableImage,
	}
	inspected.Config = &dockerspec.DockerOCIImageConfig{}
	identity, err := resolveCPAImageIdentity(fixtureMutableImage, inspected)
	if err != nil || stringValue(identity["version"]) != "v7.2.10" {
		t.Fatalf("tag identity = (%#v, %v)", identity, err)
	}
	inspected.Config.Labels = map[string]string{"org.opencontainers.image.version": "v8.0.0"}
	identity, err = resolveCPAImageIdentity(fixtureMutableImage, inspected)
	if err != nil || stringValue(identity["version"]) != "v8.0.0" {
		t.Fatalf("label identity = (%#v, %v)", identity, err)
	}
}

func TestAccountRuntimePullImageKeepsInvalidBannerNonFatalAndBoundsOutput(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
	runtime, store, client, _ := newCPAImageRuntime(t, accounts)
	client.inspectResult = &dockerclient.ImageInspectResult{}
	client.inspectResult.ID = fixtureNewImageID
	client.inspectResult.RepoTags = []string{fixtureMutableImage}
	client.inspectResult.Config = &dockerspec.DockerOCIImageConfig{}
	client.inspectResult.Config.Entrypoint = []string{"/app/CLIProxyAPI"}
	client.bannerOutput = "CLIProxyAPI Version: development, Commit: invalid, BuiltAt: now\n"
	var output strings.Builder

	if _, err := runtime.PullImage(context.Background(), &output); err != nil {
		t.Fatalf("PullImage invalid banner: %v", err)
	}
	if stringValue(mapValue(store.state["candidate"])["version"]) != "" ||
		!strings.Contains(output.String(), "Image has no recognizable version") {
		t.Fatalf("invalid banner state=%#v output=%q", store.state, output.String())
	}
	if created := client.creates[0].Config; strings.Join(created.Entrypoint, " ") != "/app/CLIProxyAPI" ||
		strings.Join(created.Cmd, " ") != "-h" {
		t.Fatalf("entrypoint probe command = entrypoint=%q cmd=%q", created.Entrypoint, created.Cmd)
	}
	bounded := &limitedBannerOutput{limit: cpaImageBannerMaximumOutput}
	_, _ = bounded.Write([]byte(strings.Repeat("x", cpaImageBannerMaximumOutput+1024)))
	if len(bounded.String()) != cpaImageBannerMaximumOutput {
		t.Fatalf("bounded output length = %d", len(bounded.String()))
	}
}

func TestAccountRuntimePullImageRefusesWrappedOrUnexpectedBannerEntrypoints(t *testing.T) {
	shellEntrypoint := &dockerspec.DockerOCIImageConfig{}
	shellEntrypoint.Entrypoint = []string{"/bin/sh", "-c"}
	shellEntrypoint.Cmd = []string{"./CLIProxyAPI -h"}
	unexpectedCommand := &dockerspec.DockerOCIImageConfig{}
	unexpectedCommand.Cmd = []string{"/usr/bin/env", "./CLIProxyAPI", "-h"}
	for name, config := range map[string]*dockerspec.DockerOCIImageConfig{
		"shell entrypoint":   shellEntrypoint,
		"unexpected command": unexpectedCommand,
	} {
		t.Run(name, func(t *testing.T) {
			accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
			runtime, store, client, projector := newCPAImageRuntime(t, accounts)
			client.inspectResult = &dockerclient.ImageInspectResult{}
			client.inspectResult.ID = fixtureNewImageID
			client.inspectResult.RepoTags = []string{fixtureMutableImage}
			client.inspectResult.Config = config
			client.bannerOutput = "CLIProxyAPI Version: v99.0.0, Commit: unsafe, BuiltAt: now\n"
			var output strings.Builder

			if _, err := runtime.PullImage(context.Background(), &output); err != nil {
				t.Fatalf("PullImage refused probe: %v", err)
			}
			if len(client.creates) != 0 || projector.calls != 0 ||
				stringValue(mapValue(store.state["candidate"])["version"]) != "" ||
				!strings.Contains(output.String(), "Image has no recognizable version") {
				t.Fatalf("unsafe probe side effects: creates=%d projector=%d state=%#v output=%q", len(client.creates), projector.calls, store.state, output.String())
			}
		})
	}
}

func TestAccountRuntimeUpdatesOneImageWithImmutableReference(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
	runtime, store, client, projector := newCPAImageRuntime(t, accounts)
	var output strings.Builder

	result, err := runtime.UpdateImage(context.Background(), "alpha", &output)
	if err != nil {
		t.Fatalf("UpdateImage: %v", err)
	}
	if result.Action != "image-update" || result.Target != "alpha" || len(result.Services) != 1 {
		t.Fatalf("update result = %#v", result)
	}
	if len(client.tags) != 1 || client.tags[0].Source != oldImageID("alpha") ||
		client.tags[0].Target != "fixture-cpa-rollback:alpha" {
		t.Fatalf("rollback tags = %#v", client.tags)
	}
	if len(client.createImages) != 1 || client.createImages[0] != fixtureImmutableImage {
		t.Fatalf("created images = %#v", client.createImages)
	}
	if projector.calls != 1 || projector.references[0] != fixtureImmutableImage ||
		projector.listenAddresses[0] != "127.0.0.1" ||
		stringValue(mapValue(store.state["applied"])["image_id"]) != fixtureNewImageID {
		t.Fatalf("commit projector=%#v listeners=%#v state=%#v", projector.references, projector.listenAddresses, store.state)
	}
	assertRunningImage(t, client, "alpha", fixtureNewImageID)
}

func TestAccountRuntimeUpdatesImageWithoutKeysAfterManagementProbe(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
	runtime, store, client, projector := newCPAImageRuntime(t, accounts)
	store.keys = map[string]controlplane.InternalKey{}
	client.zeroKeyProbeStatus = http.StatusOK
	var output strings.Builder

	if _, err := runtime.UpdateImage(context.Background(), "alpha", &output); err != nil {
		t.Fatalf("UpdateImage without Keys: %v", err)
	}
	if projector.calls != 1 ||
		!strings.Contains(output.String(), "Runtime probe") {
		t.Fatalf("zero-Key image verification: projector=%d output=%q", projector.calls, output.String())
	}
	assertRunningImage(t, client, "alpha", fixtureNewImageID)
}

func TestAccountRuntimeRejectsUnauthorizedImageManagementProbeWithoutKeys(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
	runtime, store, client, projector := newCPAImageRuntime(t, accounts)
	store.keys = map[string]controlplane.InternalKey{}
	client.zeroKeyProbeStatus = http.StatusUnauthorized
	before := cloneStringMap(store.state)

	_, err := runtime.UpdateImage(context.Background(), "alpha", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "expected 200") {
		t.Fatalf("UpdateImage with rejected management probe = %v", err)
	}
	if projector.calls != 0 || !mapsEqual(before, store.state) {
		t.Fatalf("open zero-Key update committed state: projector=%d state=%#v", projector.calls, store.state)
	}
	assertRunningImage(t, client, "alpha", oldImageID("alpha"))
}

func TestAccountRuntimeUpdateAllSkipsDisabledAndStoppedAccounts(t *testing.T) {
	accounts := []controlplane.Account{
		{ID: "alpha", Port: 18318, GroupEnabled: true},
		{ID: "beta", Port: 18319, GroupEnabled: true},
		{ID: "gamma", Port: 18320, GroupEnabled: false},
	}
	runtime, _, client, projector := newCPAImageRuntime(t, accounts)
	client.setState("old-beta", container.StateExited)
	var output strings.Builder

	result, err := runtime.UpdateImage(context.Background(), "all", &output)
	if err != nil {
		t.Fatalf("UpdateImage all: %v", err)
	}
	if len(result.Services) != 1 || result.Services[0].Service != "cliproxy-alpha" || len(client.createImages) != 1 {
		t.Fatalf("updated services=%#v creates=%#v", result.Services, client.createImages)
	}
	if !strings.Contains(output.String(), "Skipping beta: CPA is stopped") || !strings.Contains(output.String(), "Skipping gamma: CPA is disabled") {
		t.Fatalf("skip output = %q", output.String())
	}
	if projector.calls != 1 {
		t.Fatalf("projector calls = %d", projector.calls)
	}
}

func TestAccountRuntimeUpdateFailureRollsBackAllAttemptedAccountsInReverse(t *testing.T) {
	accounts := []controlplane.Account{
		{ID: "alpha", Port: 18318, GroupEnabled: true},
		{ID: "beta", Port: 18319, GroupEnabled: true},
	}
	runtime, store, client, projector := newCPAImageRuntime(t, accounts)
	client.failTargetProbeAccount = "beta"
	before := cloneStringMap(store.state)
	var output strings.Builder

	_, err := runtime.UpdateImage(context.Background(), "all", &output)
	if err == nil || !strings.Contains(err.Error(), "runtime probe failed") {
		t.Fatalf("UpdateImage failure = %v", err)
	}
	if projector.calls != 0 || !mapsEqual(before, store.state) {
		t.Fatalf("failed update committed state: projector=%d state=%#v", projector.calls, store.state)
	}
	wantCreates := []string{
		fixtureImmutableImage, fixtureImmutableImage,
		"fixture-cpa-rollback:beta", "fixture-cpa-rollback:alpha",
	}
	if strings.Join(client.createImages, ",") != strings.Join(wantCreates, ",") {
		t.Fatalf("create/rollback order = %#v, want %#v", client.createImages, wantCreates)
	}
	assertRunningImage(t, client, "alpha", oldImageID("alpha"))
	assertRunningImage(t, client, "beta", oldImageID("beta"))
	if !strings.Contains(output.String(), "Restored beta") || !strings.Contains(output.String(), "Restored alpha") {
		t.Fatalf("rollback output = %q", output.String())
	}
}

func TestAccountRuntimeProjectionFailureRestoresStateAndContainer(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", Port: 18318, GroupEnabled: true}}
	runtime, store, client, projector := newCPAImageRuntime(t, accounts)
	projector.err = errors.New("fixture projection failure")
	before := cloneStringMap(store.state)

	_, err := runtime.UpdateImage(context.Background(), "alpha", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "fixture projection failure") {
		t.Fatalf("projection failure = %v", err)
	}
	if !mapsEqual(before, store.state) {
		t.Fatalf("state not restored: got %#v want %#v", store.state, before)
	}
	assertRunningImage(t, client, "alpha", oldImageID("alpha"))
}

func newCPAImageRuntime(
	t *testing.T,
	accounts []controlplane.Account,
) (*AccountRuntime, *imageMutationStore, *fakeCPAImageClient, *recordingCPAImageProjector) {
	t.Helper()
	ids := make(map[string]string)
	containers := make([]container.Summary, 0, len(accounts))
	accountIDs := make([]string, 0, len(accounts))
	for _, account := range accounts {
		accountIDs = append(accountIDs, account.ID)
		ids["fixture-cpa-rollback:"+account.ID] = oldImageID(account.ID)
		summary := accountContainerSummary("old-"+account.ID, account.ID, container.StateRunning, uint16(account.Port))
		summary.ImageID = oldImageID(account.ID)
		summary.Image = "registry.example.test/cpa:v1"
		containers = append(containers, summary)
	}
	root := newRuntimeRoot(t, accountIDs...)
	base := &fakeAccountLifecycleClient{containers: containers}
	client := &fakeCPAImageClient{
		fakeAccountLifecycleClient: base,
		imageIDs: map[string]string{
			fixtureMutableImage: fixtureNewImageID, fixtureImmutableImage: fixtureNewImageID,
		},
		taggedIDs: ids,
	}
	manager, err := New(client, "fixture-project", staticAccountCatalog{accounts: accounts})
	if err != nil {
		t.Fatalf("New runtime manager: %v", err)
	}
	store := &imageMutationStore{
		settings: map[string]any{
			"accounts.listen_address": "127.0.0.1",
			"runtime.cliproxy_image":  fixtureMutableImage,
		},
		state: map[string]any{
			"candidate": fixtureImageIdentity(),
			"applied": map[string]any{
				"image_id": "sha256:old-applied", "resolved_ref": "registry.example.test/cpa@sha256:old",
				"version": "v1.0.0", "applied_at": float64(10),
			},
		},
		keys: map[string]controlplane.InternalKey{
			"alice@example.com": {Key: "cpa_internal_fixture", Status: "active"},
		},
		secrets: map[string]string{"cpa_management_key": "management-key-for-tests"},
	}
	projector := &recordingCPAImageProjector{}
	client.probeStore = store
	runtime, err := NewAccountRuntime(manager, store, AccountRuntimeConfig{
		Root: root, NetworkName: "fixture-backend", InstanceName: "fixture",
		ProbeTimeout: timeSecond, ImageProjector: projector,
		HTTPClient: resty.NewWithClient(&http.Client{
			Transport: client, Timeout: timeSecond,
		}),
	})
	if err != nil {
		t.Fatalf("NewAccountRuntime: %v", err)
	}
	client.runtime = runtime
	return runtime, store, client, projector
}

const timeSecond = 1_000_000_000

type imageMutationStore struct {
	settings map[string]any
	state    map[string]any
	keys     map[string]controlplane.InternalKey
	secrets  map[string]string
}

func (store *imageMutationStore) ReadSettings(context.Context) (map[string]any, error) {
	return cloneStringMap(store.settings), nil
}

func (store *imageMutationStore) ReadRuntimeState(_ context.Context, _ string, target any) (bool, error) {
	raw, _ := json.Marshal(store.state)
	return len(store.state) > 0, json.Unmarshal(raw, target)
}

func (store *imageMutationStore) WriteRuntimeState(_ context.Context, _ string, payload any) error {
	raw, _ := json.Marshal(payload)
	store.state = make(map[string]any)
	return json.Unmarshal(raw, &store.state)
}

func (store *imageMutationStore) DeleteRuntimeState(context.Context, string) error {
	store.state = map[string]any{}
	return nil
}

func (store *imageMutationStore) ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error) {
	return store.keys, nil
}

func (store *imageMutationStore) ReadSecret(_ context.Context, name string) (string, bool, error) {
	value, found := store.secrets[name]
	return value, found, nil
}

type recordingCPAImageProjector struct {
	calls           int
	references      []string
	listenAddresses []string
	err             error
}

func (projector *recordingCPAImageProjector) ProjectCPAImage(_ context.Context, reference string, listenAddress string) error {
	projector.calls++
	projector.references = append(projector.references, reference)
	projector.listenAddresses = append(projector.listenAddresses, listenAddress)
	return projector.err
}

type fakeCPAImageClient struct {
	*fakeAccountLifecycleClient
	imageIDs               map[string]string
	taggedIDs              map[string]string
	tags                   []dockerclient.ImageTagOptions
	createImages           []string
	pullReferences         []string
	failTargetProbeAccount string
	zeroKeyProbeStatus     int
	runtime                *AccountRuntime
	probeStore             *imageMutationStore
	inspectResult          *dockerclient.ImageInspectResult
	bannerOutput           string
}

func (client *fakeCPAImageClient) ImagePull(
	_ context.Context,
	reference string,
	_ dockerclient.ImagePullOptions,
) (dockerclient.ImagePullResponse, error) {
	client.pullReferences = append(client.pullReferences, reference)
	return &fakeImagePullResponse{}, nil
}

func (client *fakeCPAImageClient) ImageInspect(
	_ context.Context,
	reference string,
	_ ...dockerclient.ImageInspectOption,
) (dockerclient.ImageInspectResult, error) {
	if client.inspectResult != nil {
		return *client.inspectResult, nil
	}
	imageID, found := client.imageIDs[reference]
	if !found {
		return dockerclient.ImageInspectResult{}, fmtErrorNotFound("image")
	}
	result := dockerclient.ImageInspectResult{}
	result.ID = imageID
	result.RepoDigests = []string{fixtureImmutableImage}
	result.RepoTags = []string{"registry.example.test/cpa:v7.2.140"}
	result.Config = &dockerspec.DockerOCIImageConfig{}
	result.Config.Labels = map[string]string{
		"org.opencontainers.image.version":  "v7.2.140",
		"org.opencontainers.image.revision": "87c0bc86d4a8",
	}
	return result, nil
}

func (client *fakeCPAImageClient) ContainerLogs(
	_ context.Context,
	_ string,
	_ dockerclient.ContainerLogsOptions,
) (dockerclient.ContainerLogsResult, error) {
	var output bytes.Buffer
	header := make([]byte, 8)
	header[0] = byte(stdcopy.Stdout)
	binary.BigEndian.PutUint32(header[4:], uint32(len(client.bannerOutput)))
	_, _ = output.Write(header)
	_, _ = output.WriteString(client.bannerOutput)
	return io.NopCloser(bytes.NewReader(output.Bytes())), nil
}

func (client *fakeCPAImageClient) ImageList(context.Context, dockerclient.ImageListOptions) (dockerclient.ImageListResult, error) {
	return dockerclient.ImageListResult{Items: []image.Summary{}}, nil
}

func (client *fakeCPAImageClient) ImageTag(
	_ context.Context,
	options dockerclient.ImageTagOptions,
) (dockerclient.ImageTagResult, error) {
	client.tags = append(client.tags, options)
	client.taggedIDs[options.Target] = options.Source
	return dockerclient.ImageTagResult{}, nil
}

func (client *fakeCPAImageClient) ContainerCreate(
	ctx context.Context,
	options dockerclient.ContainerCreateOptions,
) (dockerclient.ContainerCreateResult, error) {
	created, err := client.fakeAccountLifecycleClient.ContainerCreate(ctx, options)
	if err != nil {
		return created, err
	}
	client.createImages = append(client.createImages, options.Config.Image)
	imageID := client.imageIDs[options.Config.Image]
	if imageID == "" {
		imageID = client.taggedIDs[options.Config.Image]
	}
	for index := range client.containers {
		if client.containers[index].ID == created.ID {
			client.containers[index].ImageID = imageID
		}
	}
	return created, nil
}

func (client *fakeCPAImageClient) RoundTrip(request *http.Request) (*http.Response, error) {
	account := strings.TrimSuffix(strings.TrimPrefix(request.URL.Host, "cliproxy-"), ":8317")
	status := http.StatusOK
	if client.probeStore != nil && len(client.probeStore.keys) == 0 && client.zeroKeyProbeStatus != 0 {
		status = client.zeroKeyProbeStatus
	}
	if account == client.failTargetProbeAccount {
		for _, candidate := range client.containers {
			if candidate.Labels[composeServiceLabel] == "cliproxy-"+account && candidate.ImageID == fixtureNewImageID {
				status = http.StatusServiceUnavailable
				break
			}
		}
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"fixture"}]}`)), Request: request,
	}, nil
}

type fakeImagePullResponse struct{ bytes.Reader }

func (response *fakeImagePullResponse) Close() error               { return nil }
func (response *fakeImagePullResponse) Wait(context.Context) error { return nil }
func (response *fakeImagePullResponse) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

func fixtureImageIdentity() map[string]any {
	return map[string]any{
		"source_ref": fixtureMutableImage, "version": "v7.2.140", "commit": "87c0bc86d4a8",
		"image_id": fixtureNewImageID, "image_short_id": shortID(fixtureNewImageID),
		"repo_digest": fixtureImmutableImage, "repo_digests": []any{fixtureImmutableImage},
		"resolved_ref": fixtureImmutableImage, "pulled_at": float64(20),
	}
}

func oldImageID(account string) string { return "sha256:old-" + account }

func assertRunningImage(t *testing.T, client *fakeCPAImageClient, account, imageID string) {
	t.Helper()
	for _, candidate := range client.containers {
		if candidate.Labels[composeServiceLabel] == "cliproxy-"+account {
			if candidate.State != container.StateRunning || candidate.ImageID != imageID {
				t.Fatalf("account %s container = %#v", account, candidate)
			}
			return
		}
	}
	t.Fatalf("account %s container missing: %#v", account, client.containers)
}

func mapsEqual(left, right map[string]any) bool {
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return bytes.Equal(leftRaw, rightRaw)
}

func fmtErrorNotFound(kind string) error {
	return errors.Join(errdefs.ErrNotFound, errors.New("fixture "+kind+" not found"))
}
