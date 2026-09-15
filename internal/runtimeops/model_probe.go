package runtimeops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

const modelProbeBodyLimit = 1 << 20

var modelIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)

type ModelProbeStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error)
}

type ModelProbeRuntime interface {
	List(context.Context) ([]Service, error)
}

type ModelProbeError struct {
	Code    string
	Message *i18n.Message
}

func (err *ModelProbeError) Error() string { return err.Message.Error() }
func (err *ModelProbeError) Unwrap() error { return err.Message }

type AccountModels struct {
	Account string   `json:"account"`
	Models  []string `json:"models"`
}

type ModelProbeResult struct {
	Account        string `json:"account"`
	Model          string `json:"model"`
	Success        bool   `json:"success"`
	ElapsedMS      int64  `json:"elapsed_ms"`
	CheckedAt      int64  `json:"checked_at"`
	UpstreamStatus int    `json:"upstream_status"`
	Code           string `json:"code"`
	Message        string `json:"message"`
}

// ModelProbe sends a fixed, small generation request to one existing CPA. It
// never changes user routes or returns internal credentials/upstream bodies.
type ModelProbe struct {
	store   ModelProbeStore
	runtime ModelProbeRuntime
	client  *http.Client
	mu      sync.Mutex
	active  map[string]bool
}

func NewModelProbe(store ModelProbeStore, runtime ModelProbeRuntime, client *http.Client) *ModelProbe {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// This hop stays on the private CPA network. The account itself owns
		// its configured outbound proxy; never forward internal Keys to an
		// ambient HTTP_PROXY on the Admin process.
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	}
	boundedClient := *client
	boundedClient.Timeout = 25 * time.Second
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ModelProbe{store: store, runtime: runtime, client: &boundedClient, active: make(map[string]bool)}
}

func (probe *ModelProbe) acquire(account string) (func(), error) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if probe.active[account] || len(probe.active) >= 3 {
		return nil, &ModelProbeError{"model_test_busy", i18n.M("runtimeops.a_model_test_is_already_running_please_try_again_later")}
	}
	probe.active[account] = true
	return func() { probe.mu.Lock(); delete(probe.active, account); probe.mu.Unlock() }, nil
}

func (probe *ModelProbe) target(ctx context.Context, account string) (string, string, error) {
	normalized, err := controlplane.NormalizeAccountID(account)
	if err != nil || normalized != account {
		return "", "", &ModelProbeError{"invalid_account", i18n.M("runtimeops.invalid_account_id")}
	}
	accounts, err := probe.store.ReadAccounts(ctx)
	if err != nil {
		return "", "", &ModelProbeError{"model_test_unavailable", i18n.M("runtimeops.cannot_read_the_account_catalog_right_now")}
	}
	found := false
	for _, candidate := range accounts {
		found = found || candidate.ID == account
	}
	if !found {
		return "", "", &ModelProbeError{"account_not_found", i18n.M("portal.account_does_not_exist")}
	}
	services, err := probe.runtime.List(ctx)
	if err != nil {
		return "", "", &ModelProbeError{"model_test_unavailable", i18n.M("runtimeops.cannot_read_the_account_runtime_status_right_now")}
	}
	running := false
	for _, service := range services {
		if service.Service == "cliproxy-"+account {
			running = service.State == "running"
		}
	}
	if !running {
		return "", "", &ModelProbeError{"account_not_running", i18n.M("runtimeops.the_account_container_is_stopped_start_it_first")}
	}
	keys, err := probe.store.ReadInternalKeys(ctx)
	if err != nil {
		return "", "", &ModelProbeError{"model_test_unavailable", i18n.M("runtimeops.cannot_read_test_credentials_right_now")}
	}
	users := make([]string, 0, len(keys))
	for user, key := range keys {
		if key.Status == "active" && strings.TrimSpace(key.Key) != "" {
			users = append(users, user)
		}
	}
	sort.Strings(users)
	if len(users) == 0 {
		return "", "", &ModelProbeError{"model_test_no_credentials", i18n.M("runtimeops.no_internal_credentials_are_available_create_a_user_first_binding")}
	}
	// The account projection installs all active internal Keys on every CPA,
	// including accounts with no routed users, just as runtime readiness probes do.
	return "http://cliproxy-" + account + ":8317", keys[users[0]].Key, nil
}

func (probe *ModelProbe) request(ctx context.Context, base, key, path string, body []byte) (int, []byte, error) {
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := probe.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, modelProbeBodyLimit+1))
	if err == nil && len(payload) > modelProbeBodyLimit {
		err = errors.New("model response too large")
	}
	return response.StatusCode, payload, err
}

func (probe *ModelProbe) Models(ctx context.Context, account string) (AccountModels, error) {
	release, err := probe.acquire(account)
	if err != nil {
		return AccountModels{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	base, key, err := probe.target(ctx, account)
	if err != nil {
		return AccountModels{}, err
	}
	status, payload, err := probe.request(ctx, base, key, "/v1/models", nil)
	if err != nil || status != http.StatusOK {
		return AccountModels{}, &ModelProbeError{"model_list_unavailable", i18n.M("runtimeops.cannot_read_this_account_s_model_list_check_the_container")}
	}
	var decoded struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &decoded) != nil {
		return AccountModels{}, &ModelProbeError{"invalid_model_list", i18n.M("runtimeops.the_account_returned_an_invalid_model_list")}
	}
	result := AccountModels{Account: account, Models: []string{}}
	seen := make(map[string]bool)
	for _, model := range decoded.Data {
		if modelIDPattern.MatchString(model.ID) && !seen[model.ID] && len(result.Models) < 200 {
			result.Models = append(result.Models, model.ID)
			seen[model.ID] = true
		}
	}
	sort.Strings(result.Models)
	return result, nil
}

func (probe *ModelProbe) Test(ctx context.Context, account, model string) (ModelProbeResult, error) {
	if !modelIDPattern.MatchString(model) {
		return ModelProbeResult{}, &ModelProbeError{"invalid_model", i18n.M("runtimeops.select_a_valid_model")}
	}
	release, err := probe.acquire(account)
	if err != nil {
		return ModelProbeResult{}, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	base, key, err := probe.target(ctx, account)
	if err != nil {
		return ModelProbeResult{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "input": "Reply with exactly OK.", "max_output_tokens": 64,
		"reasoning": map[string]string{"effort": "low"}, "stream": false,
	})
	started := time.Now()
	status, payload, requestError := probe.request(ctx, base, key, "/v1/responses", body)
	result := ModelProbeResult{Account: account, Model: model, ElapsedMS: time.Since(started).Milliseconds(),
		CheckedAt: time.Now().Unix(), UpstreamStatus: status, Code: "model_test_failed", Message: i18n.Text(i18n.FromContext(ctx), "runtimeops.the_model_did_not_complete_generation_check_the_account_logs")}
	if requestError != nil {
		result.Code, result.Message = "model_connection_failed", i18n.Text(i18n.FromContext(ctx), "runtimeops.model_connection_failed_or_the_response_was_incomplete_check_the")
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(requestError, context.DeadlineExceeded) {
			result.Code, result.Message = "model_test_timeout", i18n.Text(i18n.FromContext(ctx), "runtimeops.the_model_test_timed_out_after_25_seconds_please_try")
		}
		return result, nil
	}
	if status != http.StatusOK {
		switch status {
		case 401, 403:
			result.Code, result.Message = "model_auth_failed", i18n.Text(i18n.FromContext(ctx), "runtimeops.account_authorization_failed_check_oauth_status")
		case 400, 404:
			result.Code, result.Message = "model_not_supported", i18n.Text(i18n.FromContext(ctx), "runtimeops.this_account_cannot_use_the_selected_model_or_test_parameters")
		case 429:
			result.Code, result.Message = "model_rate_limited", i18n.Text(i18n.FromContext(ctx), "runtimeops.account_quota_is_insufficient_or_requests_are_limited_check_the")
		default:
			result.Code, result.Message = "model_upstream_error", i18n.Text(i18n.FromContext(ctx), "runtimeops.the_model_service_is_unavailable_retry_later_or_check_the")
		}
		return result, nil
	}
	var decoded struct {
		Status string `json:"status"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(payload, &decoded) != nil || decoded.Status != "completed" {
		return result, nil
	}
	for _, item := range decoded.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				result.Success, result.Code, result.Message = true, "model_test_passed", i18n.Text(i18n.FromContext(ctx), "runtimeops.the_model_completed_generation_and_returned_text")
				return result, nil
			}
		}
	}
	return result, nil
}
