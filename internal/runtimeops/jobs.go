package runtimeops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/alitto/pond/v2"
	"github.com/google/uuid"
)

const (
	defaultJobConcurrency = 1
	defaultJobQueueSize   = 16
	defaultJobHistory     = 60
)

var (
	ErrJobQueueFull = errors.New("runtime job queue is full")
	ErrJobConflict  = errors.New("a conflicting runtime job is already active")
	ErrJobNotFound  = errors.New("runtime job was not found")
	ErrJobFinished  = errors.New("runtime job is already finished")
)

type Controller interface {
	Start(context.Context, string) (OperationResult, error)
	Stop(context.Context, string) (OperationResult, error)
	Restart(context.Context, string) (OperationResult, error)
}

// LoginController runs one isolated CLIProxyAPI device-login container for an
// exact CPA account. Output is streamed into the in-memory job so the Admin UI
// can expose the HTTPS device URL and one-time code while the task is running.
// Implementations must verify that an OAuth JSON file was actually added or
// changed; a zero process exit code alone is not success.
type LoginController interface {
	Login(context.Context, string, io.Writer) (OperationResult, error)
}

type ImageController interface {
	PullImage(context.Context, io.Writer) (OperationResult, error)
	UpdateImage(context.Context, string, io.Writer) (OperationResult, error)
}

type Job struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Action     string           `json:"action"`
	Target     string           `json:"target"`
	Status     string           `json:"status"`
	CreatedAt  int64            `json:"created_at"`
	StartedAt  *int64           `json:"started_at,omitempty"`
	FinishedAt *int64           `json:"finished_at,omitempty"`
	Result     *OperationResult `json:"result,omitempty"`
	Output     string           `json:"output,omitempty"`
	Error      string           `json:"error,omitempty"`
	cancel     context.CancelFunc
}

type JobSubmission struct {
	Job    Job  `json:"job"`
	Reused bool `json:"reused"`
}

type JobManager struct {
	controller    Controller
	login         LoginController
	images        ImageController
	diagnostics   DiagnosticController
	operationLock sync.Locker
	pool          pond.Pool
	ctx           context.Context
	cancel        context.CancelFunc
	now           func() time.Time
	newID         func() string
	history       int

	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string
	active map[string]string
}

func NewJobManager(controller Controller) (*JobManager, error) {
	return newJobManagerWithControllers(controller, nil, nil, nil, nil, defaultJobConcurrency, defaultJobQueueSize, defaultJobHistory)
}

func newJobManager(controller Controller, concurrency int, queueSize int, history int) (*JobManager, error) {
	return newJobManagerWithControllers(controller, nil, nil, nil, nil, concurrency, queueSize, history)
}

func NewJobManagerWithLogin(controller Controller, login LoginController) (*JobManager, error) {
	return newJobManagerWithControllers(controller, login, nil, nil, nil, defaultJobConcurrency, defaultJobQueueSize, defaultJobHistory)
}

func NewJobManagerWithControllers(
	controller Controller,
	login LoginController,
	images ImageController,
	operationLock sync.Locker,
) (*JobManager, error) {
	return newJobManagerWithControllers(
		controller, login, images, nil, operationLock,
		defaultJobConcurrency, defaultJobQueueSize, defaultJobHistory,
	)
}

func NewJobManagerWithDiagnostics(
	controller Controller,
	login LoginController,
	images ImageController,
	diagnostics DiagnosticController,
	operationLock sync.Locker,
) (*JobManager, error) {
	return newJobManagerWithControllers(
		controller, login, images, diagnostics, operationLock,
		defaultJobConcurrency, defaultJobQueueSize, defaultJobHistory,
	)
}

func newJobManagerWithLogin(
	controller Controller,
	login LoginController,
	concurrency int,
	queueSize int,
	history int,
) (*JobManager, error) {
	return newJobManagerWithControllers(controller, login, nil, nil, nil, concurrency, queueSize, history)
}

func newJobManagerWithControllers(
	controller Controller,
	login LoginController,
	images ImageController,
	diagnostics DiagnosticController,
	operationLock sync.Locker,
	concurrency int,
	queueSize int,
	history int,
) (*JobManager, error) {
	if controller == nil {
		return nil, errors.New("runtime jobs require a controller")
	}
	if concurrency <= 0 || queueSize <= 0 || history <= 0 {
		return nil, errors.New("runtime job limits must be positive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &JobManager{
		controller: controller, login: login, images: images, diagnostics: diagnostics, operationLock: operationLock,
		pool: pond.NewPool(
			concurrency,
			pond.WithContext(ctx),
			pond.WithQueueSize(queueSize),
			pond.WithNonBlocking(true),
		),
		ctx: ctx, cancel: cancel, now: time.Now,
		newID: uuid.NewString, history: history,
		jobs: make(map[string]*Job), active: make(map[string]string),
	}, nil
}

func (manager *JobManager) Submit(action string, target string) (JobSubmission, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	target = normalizeTarget(target)
	if action != "start" && action != "up" && action != "stop" && action != "restart" && action != "login" &&
		action != "image-pull" && action != "image-update" && action != "health" &&
		action != "verify-routing" && action != "render" {
		return JobSubmission{}, fmt.Errorf("%w: unsupported action %s", ErrRuntimeTarget, action)
	}
	if action == "login" && manager.login == nil {
		return JobSubmission{}, fmt.Errorf("%w: OAuth device login is unavailable", ErrRuntimeTarget)
	}
	if (action == "image-pull" || action == "image-update") && manager.images == nil {
		return JobSubmission{}, fmt.Errorf("%w: CPA image operations are unavailable", ErrRuntimeTarget)
	}
	if (action == "health" || action == "verify-routing" || action == "render") && manager.diagnostics == nil {
		return JobSubmission{}, fmt.Errorf("%w: runtime diagnostics are unavailable", ErrRuntimeTarget)
	}
	if action == "image-pull" && target != "all" {
		return JobSubmission{}, fmt.Errorf("%w: CPA image pull target must be all", ErrRuntimeTarget)
	}
	if action == "health" || action == "verify-routing" || action == "render" {
		target = "all"
	}
	if action == "up" {
		action = "start"
	}

	manager.mu.Lock()
	if manager.pool.Stopped() {
		manager.mu.Unlock()
		return JobSubmission{}, pond.ErrPoolStopped
	}
	if existingID, conflict := manager.findConflictLocked(action, target); conflict {
		existing := cloneJob(manager.jobs[existingID])
		manager.mu.Unlock()
		if existing.Action == action && existing.Target == target {
			return JobSubmission{Job: existing, Reused: true}, nil
		}
		return JobSubmission{}, fmt.Errorf("%w: job %s is %s %s", ErrJobConflict, existing.ID, existing.Action, existing.Target)
	}

	jobContext, cancel := context.WithCancel(manager.ctx)
	job := &Job{
		ID: manager.newID(), Name: jobName(action, target), Action: action, Target: target,
		Status: "queued", CreatedAt: manager.now().Unix(), cancel: cancel,
	}
	manager.jobs[job.ID] = job
	manager.order = append(manager.order, job.ID)
	manager.active[target] = job.ID
	task, accepted := manager.pool.TrySubmitErr(func() error {
		return manager.execute(jobContext, job.ID)
	})
	if !accepted {
		delete(manager.jobs, job.ID)
		manager.order = manager.order[:len(manager.order)-1]
		delete(manager.active, target)
		cancel()
		manager.mu.Unlock()
		return JobSubmission{}, ErrJobQueueFull
	}
	created := cloneJob(job)
	manager.mu.Unlock()

	go func() {
		manager.finish(job.ID, task.Wait())
	}()
	return JobSubmission{Job: created}, nil
}

func (manager *JobManager) Get(id string) (Job, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, found := manager.jobs[strings.TrimSpace(id)]
	if !found {
		return Job{}, ErrJobNotFound
	}
	return cloneJob(job), nil
}

func (manager *JobManager) Recent(limit int) []Job {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if limit <= 0 || limit > manager.history {
		limit = manager.history
	}
	result := make([]Job, 0, min(limit, len(manager.order)))
	for index := len(manager.order) - 1; index >= 0 && len(result) < limit; index-- {
		if job := manager.jobs[manager.order[index]]; job != nil {
			result = append(result, cloneJob(job))
		}
	}
	return result
}

func (manager *JobManager) Cancel(id string) (Job, error) {
	manager.mu.Lock()
	job, found := manager.jobs[strings.TrimSpace(id)]
	if !found {
		manager.mu.Unlock()
		return Job{}, ErrJobNotFound
	}
	if isTerminalJobStatus(job.Status) {
		finished := cloneJob(job)
		manager.mu.Unlock()
		return finished, ErrJobFinished
	}
	if job.Status == "queued" {
		finishedAt := manager.now().Unix()
		job.Status = "cancelled"
		job.FinishedAt = &finishedAt
		delete(manager.active, job.Target)
		job.cancel()
		manager.trimLocked()
	} else if job.Status != "cancelling" {
		job.Status = "cancelling"
		job.cancel()
	}
	result := cloneJob(job)
	manager.mu.Unlock()
	return result, nil
}

func (manager *JobManager) Close() {
	if manager == nil {
		return
	}
	manager.cancel()
	manager.pool.StopAndWait()
}

func (manager *JobManager) execute(ctx context.Context, id string) error {
	manager.mu.Lock()
	job := manager.jobs[id]
	if job == nil {
		manager.mu.Unlock()
		return ErrJobNotFound
	}
	if err := ctx.Err(); err != nil {
		manager.mu.Unlock()
		return err
	}
	startedAt := manager.now().Unix()
	job.StartedAt = &startedAt
	job.Status = "running"
	action, target := job.Action, job.Target
	manager.mu.Unlock()

	var (
		result OperationResult
		err    error
	)
	switch action {
	case "start":
		result, err = manager.controller.Start(ctx, target)
	case "stop":
		result, err = manager.controller.Stop(ctx, target)
	case "restart":
		result, err = manager.controller.Restart(ctx, target)
	case "login":
		output := &jobOutputWriter{manager: manager, id: id}
		result, err = manager.login.Login(ctx, target, output)
		output.Flush()
	case "image-pull", "image-update":
		output := &jobOutputWriter{manager: manager, id: id}
		if manager.operationLock != nil {
			manager.operationLock.Lock()
			defer manager.operationLock.Unlock()
		}
		if action == "image-pull" {
			result, err = manager.images.PullImage(ctx, output)
		} else {
			result, err = manager.images.UpdateImage(ctx, target, output)
		}
		output.Flush()
	case "health", "verify-routing", "render":
		output := &jobOutputWriter{manager: manager, id: id}
		if action == "render" && manager.operationLock != nil {
			manager.operationLock.Lock()
			defer manager.operationLock.Unlock()
		}
		switch action {
		case "health":
			result, err = manager.diagnostics.Health(ctx, output)
		case "verify-routing":
			result, err = manager.diagnostics.VerifyRouting(ctx, output)
		case "render":
			result, err = manager.diagnostics.Render(ctx, output)
		}
		output.Flush()
	default:
		err = fmt.Errorf("%w: unsupported action %s", ErrRuntimeTarget, action)
	}
	if err == nil {
		manager.mu.Lock()
		if job = manager.jobs[id]; job != nil {
			cloned := result
			job.Result = &cloned
		}
		manager.mu.Unlock()
	}
	return err
}

// jobOutputWriter publishes complete lines only. Holding the final partial
// line prevents a credential-shaped value split across Docker stream chunks
// from becoming observable before the aggregate sanitizer can inspect it.
type jobOutputWriter struct {
	manager *JobManager
	id      string
	pending bytes.Buffer
}

func (writer *jobOutputWriter) Write(payload []byte) (int, error) {
	original := len(payload)
	_, _ = writer.pending.Write(payload)
	for {
		index := bytes.IndexByte(writer.pending.Bytes(), '\n')
		if index < 0 {
			break
		}
		writer.manager.appendOutput(writer.id, string(writer.pending.Next(index+1)))
	}
	return original, nil
}

func (writer *jobOutputWriter) Flush() {
	if writer.pending.Len() > 0 {
		writer.manager.appendOutput(writer.id, writer.pending.String())
		writer.pending.Reset()
	}
}

func (manager *JobManager) appendOutput(id string, payload string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job := manager.jobs[id]
	if job == nil || payload == "" {
		return
	}
	combined := job.Output + payload
	const limit = 2 * 1024 * 1024
	if len(combined) > limit {
		combined = combined[:limit] + i18n.Text(i18n.English, "runtimeops.output_truncated")
	}
	job.Output = Sanitize(strings.ToValidUTF8(combined, "�"))
}

func (manager *JobManager) finish(id string, err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job := manager.jobs[id]
	if job == nil || isTerminalJobStatus(job.Status) {
		return
	}
	finishedAt := manager.now().Unix()
	job.FinishedAt = &finishedAt
	if errors.Is(err, context.Canceled) {
		job.Status = "cancelled"
	} else if err != nil {
		job.Status = "failed"
		job.Error = Sanitize(err.Error())
	} else {
		job.Status = "succeeded"
	}
	delete(manager.active, job.Target)
	manager.trimLocked()
}

func (manager *JobManager) findConflictLocked(action string, target string) (string, bool) {
	if existing, found := manager.active[target]; found {
		return existing, true
	}
	if target == "all" {
		ids := make([]string, 0, len(manager.active))
		for _, id := range manager.active {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			return ids[0], true
		}
		return "", false
	}
	if existing, found := manager.active["all"]; found {
		return existing, true
	}
	return "", false
}

func (manager *JobManager) trimLocked() {
	for len(manager.order) > manager.history {
		oldest := manager.order[0]
		manager.order = manager.order[1:]
		job := manager.jobs[oldest]
		if job != nil && !isTerminalJobStatus(job.Status) {
			manager.order = append(manager.order, oldest)
			continue
		}
		delete(manager.jobs, oldest)
	}
}

func jobName(action, target string, languages ...i18n.Language) string {
	switch action {
	case "start":
		return i18n.Text(i18n.Selected(languages), "runtimeops.start_service")
	case "stop":
		return i18n.Text(i18n.Selected(languages), "runtimeops.stop_service")
	case "restart":
		return i18n.Text(i18n.Selected(languages), "runtimeops.restart_service")
	case "login":
		return i18n.Text(i18n.Selected(languages), "runtimeops.oauth_authorization")
	case "image-pull":
		return i18n.Text(i18n.Selected(languages), "runtimeops.pull_cpa_image")
	case "image-update":
		if target == "all" {
			return i18n.Text(i18n.Selected(languages), "runtimeops.update_all_cpa_images")
		}
		return i18n.Text(i18n.Selected(languages), "runtimeops.update_cpa_image")
	case "health":
		return i18n.Text(i18n.Selected(languages), "runtimeops.health_check")
	case "verify-routing":
		return i18n.Text(i18n.Selected(languages), "runtimeops.verify_routes")
	case "render":
		return i18n.Text(i18n.Selected(languages), "runtimeops.render_and_validate_configuration")
	default:
		return i18n.Text(i18n.Selected(languages), "runtimeops.maintenance")
	}
}

func cloneJob(job *Job) Job {
	if job == nil {
		return Job{}
	}
	cloned := *job
	cloned.cancel = nil
	if job.StartedAt != nil {
		value := *job.StartedAt
		cloned.StartedAt = &value
	}
	if job.FinishedAt != nil {
		value := *job.FinishedAt
		cloned.FinishedAt = &value
	}
	if job.Result != nil {
		result := *job.Result
		result.Services = append([]Service(nil), job.Result.Services...)
		cloned.Result = &result
	}
	return cloned
}

func isTerminalJobStatus(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func (job Job) WithLanguage(lang i18n.Language) Job {
	job.Name = jobName(job.Action, job.Target, lang)
	return job
}
