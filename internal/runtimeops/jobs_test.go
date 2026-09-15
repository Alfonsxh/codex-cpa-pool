package runtimeops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobManagerBoundsConcurrencyReusesDuplicatesAndSerializesAll(t *testing.T) {
	controller := &blockingController{started: make(chan string, 4), release: make(chan struct{})}
	manager, err := newJobManager(controller, 1, 2, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)

	first, err := manager.Submit("restart", "alpha")
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	awaitRuntimeValue(t, controller.started, "restart:alpha")
	reused, err := manager.Submit("restart", "alpha")
	if err != nil || !reused.Reused || reused.Job.ID != first.Job.ID {
		t.Fatalf("duplicate submission = (%#v, %v)", reused, err)
	}
	if _, err := manager.Submit("stop", "alpha"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("conflicting account job error = %v", err)
	}
	if _, err := manager.Submit("stop", "all"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("all conflict error = %v", err)
	}

	second, err := manager.Submit("up", "beta")
	if err != nil || second.Job.Action != "start" || second.Job.Status != "queued" {
		t.Fatalf("queued second = (%#v, %v)", second, err)
	}
	close(controller.release)
	awaitJobStatus(t, manager, first.Job.ID, "succeeded")
	awaitJobStatus(t, manager, second.Job.ID, "succeeded")
	if controller.maxRunning != 1 {
		t.Fatalf("maximum controller concurrency = %d", controller.maxRunning)
	}
}

func TestJobManagerCancelsQueuedAndRunningJobs(t *testing.T) {
	controller := &blockingController{started: make(chan string, 4), release: make(chan struct{})}
	manager, err := newJobManager(controller, 1, 2, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)

	running, _ := manager.Submit("restart", "alpha")
	awaitRuntimeValue(t, controller.started, "restart:alpha")
	queued, _ := manager.Submit("stop", "beta")
	if cancelled, err := manager.Cancel(queued.Job.ID); err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel queued = (%#v, %v)", cancelled, err)
	}
	awaitJobStatus(t, manager, queued.Job.ID, "cancelled")
	if cancelled, err := manager.Cancel(running.Job.ID); err != nil || cancelled.Status != "cancelling" {
		t.Fatalf("cancel running = (%#v, %v)", cancelled, err)
	}
	awaitJobStatus(t, manager, running.Job.ID, "cancelled")
	if _, err := manager.Cancel(running.Job.ID); !errors.Is(err, ErrJobFinished) {
		t.Fatalf("cancel finished error = %v", err)
	}
}

func TestJobManagerReportsFailuresRedactsErrorsAndRejectsFullQueue(t *testing.T) {
	controller := &blockingController{
		started: make(chan string, 4), release: make(chan struct{}),
		errors: map[string]error{"stop:beta": errors.New("Authorization: Bearer secret-token")},
	}
	manager, err := newJobManager(controller, 1, 1, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)
	running, _ := manager.Submit("restart", "alpha")
	awaitRuntimeValue(t, controller.started, "restart:alpha")
	failed, _ := manager.Submit("stop", "beta")
	if _, err := manager.Submit("start", "gamma"); !errors.Is(err, ErrJobQueueFull) {
		t.Fatalf("full queue error = %v", err)
	}
	close(controller.release)
	awaitJobStatus(t, manager, running.Job.ID, "succeeded")
	result := awaitJobStatus(t, manager, failed.Job.ID, "failed")
	if result.Error != "Authorization: Bearer [REDACTED]" {
		t.Fatalf("redacted job error = %q", result.Error)
	}
	if _, err := manager.Get("missing"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("missing job error = %v", err)
	}
}

func TestJobManagerStreamsSanitizedOAuthOutputReusesAndCancels(t *testing.T) {
	controller := &blockingController{started: make(chan string, 1), release: make(chan struct{})}
	login := &blockingLoginController{started: make(chan string, 1), release: make(chan struct{})}
	manager, err := newJobManagerWithLogin(controller, login, 1, 2, 10)
	if err != nil {
		t.Fatalf("new OAuth job manager: %v", err)
	}
	t.Cleanup(manager.Close)

	submission, err := manager.Submit("login", "alpha")
	if err != nil {
		t.Fatalf("submit OAuth job: %v", err)
	}
	awaitRuntimeValue(t, login.started, "alpha")
	deadline := time.Now().Add(2 * time.Second)
	var running Job
	for time.Now().Before(deadline) {
		running, err = manager.Get(submission.Job.ID)
		if err != nil {
			t.Fatalf("get OAuth job: %v", err)
		}
		if strings.Contains(running.Output, "Codex device URL: https://auth.example.test/device") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(running.Output, "Codex device URL: https://auth.example.test/device") {
		t.Fatalf("OAuth output was not published while running: %q", running.Output)
	}
	if strings.Contains(running.Output, "secret-token") || strings.Contains(running.Output, "secret-json") ||
		!strings.Contains(running.Output, "Bearer [REDACTED]") || !strings.Contains(running.Output, `"access_token":"[REDACTED]"`) {
		t.Fatalf("OAuth output was not sanitized: %q", running.Output)
	}
	reused, err := manager.Submit("login", "alpha")
	if err != nil || !reused.Reused || reused.Job.ID != submission.Job.ID {
		t.Fatalf("duplicate OAuth submission = (%#v, %v)", reused, err)
	}
	cancelled, err := manager.Cancel(submission.Job.ID)
	if err != nil || cancelled.Status != "cancelling" {
		t.Fatalf("cancel OAuth job = (%#v, %v)", cancelled, err)
	}
	awaitJobStatus(t, manager, submission.Job.ID, "cancelled")
	close(login.release)
}

func TestJobManagerRejectsOAuthWhenLoginControllerIsUnavailable(t *testing.T) {
	controller := &blockingController{started: make(chan string, 1), release: make(chan struct{})}
	manager, err := newJobManager(controller, 1, 1, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)
	if _, err := manager.Submit("login", "alpha"); !errors.Is(err, ErrRuntimeTarget) {
		t.Fatalf("OAuth unavailable error = %v", err)
	}
}

func TestJobManagerRunsImageActionsUnderSharedOperationLock(t *testing.T) {
	controller := &blockingController{started: make(chan string, 1), release: make(chan struct{})}
	login := &blockingLoginController{started: make(chan string, 1), release: make(chan struct{})}
	locker := &recordingJobLocker{}
	images := &blockingImageController{
		started: make(chan string, 2), release: make(chan struct{}), locker: locker,
	}
	manager, err := newJobManagerWithControllers(controller, login, images, nil, locker, 1, 2, 10)
	if err != nil {
		t.Fatalf("new image job manager: %v", err)
	}
	t.Cleanup(manager.Close)

	pull, err := manager.Submit("image-pull", "all")
	if err != nil || pull.Job.Name != "Pull CPA image" {
		t.Fatalf("submit image pull = (%#v, %v)", pull, err)
	}
	awaitRuntimeValue(t, images.started, "image-pull:all")
	if !images.observedLock.Load() {
		t.Fatal("image pull ran outside the identity operation lock")
	}
	reused, err := manager.Submit("image-pull", "all")
	if err != nil || !reused.Reused || reused.Job.ID != pull.Job.ID {
		t.Fatalf("duplicate image pull = (%#v, %v)", reused, err)
	}
	close(images.release)
	completed := awaitJobStatus(t, manager, pull.Job.ID, "succeeded")
	if !strings.Contains(completed.Output, "image pull output") {
		t.Fatalf("image pull output = %q", completed.Output)
	}

	images.release = make(chan struct{})
	update, err := manager.Submit("image-update", "alpha")
	if err != nil || update.Job.Name != "Update CPA image" {
		t.Fatalf("submit image update = (%#v, %v)", update, err)
	}
	awaitRuntimeValue(t, images.started, "image-update:alpha")
	close(images.release)
	awaitJobStatus(t, manager, update.Job.ID, "succeeded")
	if _, err := manager.Submit("image-pull", "alpha"); !errors.Is(err, ErrRuntimeTarget) {
		t.Fatalf("non-all image pull error = %v", err)
	}
}

func TestJobManagerRejectsImageActionsWithoutController(t *testing.T) {
	controller := &blockingController{started: make(chan string, 1), release: make(chan struct{})}
	manager, err := newJobManager(controller, 1, 1, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)
	if _, err := manager.Submit("image-update", "all"); !errors.Is(err, ErrRuntimeTarget) {
		t.Fatalf("image controller unavailable error = %v", err)
	}
}

func TestJobManagerRunsFrozenLegacyDiagnosticsAndLocksRender(t *testing.T) {
	controller := &blockingController{started: make(chan string, 1), release: make(chan struct{})}
	locker := &recordingJobLocker{}
	diagnostics := &recordingDiagnosticController{locker: locker}
	manager, err := newJobManagerWithControllers(controller, nil, nil, diagnostics, locker, 1, 3, 10)
	if err != nil {
		t.Fatalf("new diagnostic job manager: %v", err)
	}
	t.Cleanup(manager.Close)

	for _, test := range []struct {
		action string
		name   string
	}{
		{action: "health", name: "Health check"},
		{action: "verify-routing", name: "Verify routes"},
		{action: "render", name: "Render and validate configuration"},
	} {
		submission, submitError := manager.Submit(test.action, "ignored-target")
		if submitError != nil || submission.Job.Name != test.name || submission.Job.Target != "all" {
			t.Fatalf("submit %s = (%#v, %v)", test.action, submission, submitError)
		}
		completed := awaitJobStatus(t, manager, submission.Job.ID, "succeeded")
		if !strings.Contains(completed.Output, test.action+" output") {
			t.Fatalf("%s output = %q", test.action, completed.Output)
		}
	}
	if diagnostics.calls != 3 || !diagnostics.renderObservedLock {
		t.Fatalf("diagnostic calls=%d render lock=%v", diagnostics.calls, diagnostics.renderObservedLock)
	}
}

func TestJobManagerRejectsDiagnosticsWithoutController(t *testing.T) {
	controller := &blockingController{started: make(chan string, 1), release: make(chan struct{})}
	manager, err := newJobManager(controller, 1, 1, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)
	for _, action := range []string{"health", "verify-routing", "render"} {
		if _, err := manager.Submit(action, "all"); !errors.Is(err, ErrRuntimeTarget) {
			t.Fatalf("%s unavailable error = %v", action, err)
		}
	}
}

type blockingController struct {
	started chan string
	release chan struct{}
	errors  map[string]error

	mu         sync.Mutex
	running    int
	maxRunning int
}

type blockingLoginController struct {
	started chan string
	release chan struct{}
}

type blockingImageController struct {
	started      chan string
	release      chan struct{}
	locker       *recordingJobLocker
	observedLock atomic.Bool
}

type recordingDiagnosticController struct {
	locker             *recordingJobLocker
	calls              int
	renderObservedLock bool
}

func (controller *recordingDiagnosticController) Health(
	_ context.Context,
	output io.Writer,
) (OperationResult, error) {
	controller.calls++
	_, _ = fmt.Fprintln(output, "health output")
	return OperationResult{Action: "health", Target: "all"}, nil
}

func (controller *recordingDiagnosticController) VerifyRouting(
	_ context.Context,
	output io.Writer,
) (OperationResult, error) {
	controller.calls++
	_, _ = fmt.Fprintln(output, "verify-routing output")
	return OperationResult{Action: "verify-routing", Target: "all"}, nil
}

func (controller *recordingDiagnosticController) Render(
	_ context.Context,
	output io.Writer,
) (OperationResult, error) {
	controller.calls++
	controller.renderObservedLock = controller.locker.locked.Load()
	_, _ = fmt.Fprintln(output, "render output")
	return OperationResult{Action: "render", Target: "all"}, nil
}

func (controller *blockingImageController) PullImage(
	ctx context.Context,
	output io.Writer,
) (OperationResult, error) {
	return controller.run(ctx, "image-pull", "all", output)
}

func (controller *blockingImageController) UpdateImage(
	ctx context.Context,
	target string,
	output io.Writer,
) (OperationResult, error) {
	return controller.run(ctx, "image-update", target, output)
}

func (controller *blockingImageController) run(
	ctx context.Context,
	action string,
	target string,
	output io.Writer,
) (OperationResult, error) {
	controller.observedLock.Store(controller.locker.locked.Load())
	_, _ = fmt.Fprintln(output, "image pull output")
	controller.started <- action + ":" + target
	select {
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	case <-controller.release:
		return OperationResult{Action: action, Target: target}, nil
	}
}

type recordingJobLocker struct {
	mutex  sync.Mutex
	locked atomic.Bool
}

func (locker *recordingJobLocker) Lock() {
	locker.mutex.Lock()
	locker.locked.Store(true)
}

func (locker *recordingJobLocker) Unlock() {
	locker.locked.Store(false)
	locker.mutex.Unlock()
}

func (controller *blockingLoginController) Login(
	ctx context.Context,
	target string,
	output io.Writer,
) (OperationResult, error) {
	_, _ = fmt.Fprintln(output, "Codex device URL: https://auth.example.test/device")
	_, _ = fmt.Fprintln(output, `{"access_token":"secret-json"}`)
	_, _ = fmt.Fprintln(output, "Authorization: Bearer secret-token")
	controller.started <- target
	select {
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	case <-controller.release:
		return OperationResult{Action: "login", Target: target}, nil
	}
}

func (controller *blockingController) Start(ctx context.Context, target string) (OperationResult, error) {
	return controller.run(ctx, "start", target)
}

func (controller *blockingController) Stop(ctx context.Context, target string) (OperationResult, error) {
	return controller.run(ctx, "stop", target)
}

func (controller *blockingController) Restart(ctx context.Context, target string) (OperationResult, error) {
	return controller.run(ctx, "restart", target)
}

func (controller *blockingController) run(ctx context.Context, action string, target string) (OperationResult, error) {
	controller.mu.Lock()
	controller.running++
	if controller.running > controller.maxRunning {
		controller.maxRunning = controller.running
	}
	controller.mu.Unlock()
	defer func() {
		controller.mu.Lock()
		controller.running--
		controller.mu.Unlock()
	}()
	key := action + ":" + target
	controller.started <- key
	select {
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	case <-controller.release:
	}
	return OperationResult{Action: action, Target: target}, controller.errors[key]
}

func awaitRuntimeValue(t *testing.T, values <-chan string, expected string) {
	t.Helper()
	select {
	case value := <-values:
		if value != expected {
			t.Fatalf("runtime value = %q, want %q", value, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q", expected)
	}
}

func awaitJobStatus(t *testing.T, manager *JobManager, id string, expected string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(id)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.Status == expected {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	job, _ := manager.Get(id)
	t.Fatalf("job status = %q, want %q", job.Status, expected)
	return Job{}
}
