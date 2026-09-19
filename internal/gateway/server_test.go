package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func newCloseNotifyRecorder() *closeNotifyRecorder {
	return &closeNotifyRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closed:           make(chan bool),
	}
}

func (recorder *closeNotifyRecorder) CloseNotify() <-chan bool {
	return recorder.closed
}

func TestHTTPGatewayPublicOrderingAndCompatibleErrors(t *testing.T) {
	now := time.Unix(1000, 0)
	emptyGateway := newTestHTTPGateway(t, NewEngine(), now, nil, nil)
	notReady := performRequest(emptyGateway.InternalHandler(), http.MethodGet, "/__internal/ready", "")
	if notReady.Code != http.StatusServiceUnavailable || notReady.Header().Get("Retry-After") != "1" {
		t.Fatalf("empty Gateway readiness = status %d retry %q", notReady.Code, notReady.Header().Get("Retry-After"))
	}

	unknown := performRequest(emptyGateway.PublicHandler(), http.MethodGet, "/admin/secrets", "")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", unknown.Code)
	}
	unavailable := performRequest(emptyGateway.PublicHandler(), http.MethodPost, "/v1/responses", "")
	assertHTTPError(t, unavailable, http.StatusServiceUnavailable, "authentication_snapshot_unavailable")
	if unavailable.Header().Get("Retry-After") != "1" {
		t.Fatalf("503 Retry-After = %q", unavailable.Header().Get("Retry-After"))
	}

	engine := loadFixtureEngine(t, now)
	gateway := newTestHTTPGateway(t, engine, now, nil, nil)
	invalid := performRequest(gateway.PublicHandler(), http.MethodGet, "/v1/models", "")
	assertHTTPError(t, invalid, http.StatusUnauthorized, "")
	exceeded := performRequest(
		gateway.PublicHandler(),
		http.MethodPost,
		"/v1/responses",
		"Bearer "+fixtureExternalKey,
	)
	assertHTTPError(t, exceeded, http.StatusTooManyRequests, "weekly_user_token_quota_exceeded")
	if exceeded.Header().Get("Retry-After") != "1000" {
		t.Fatalf("429 Retry-After = %q, want 1000", exceeded.Header().Get("Retry-After"))
	}
	publicInternal := performRequest(
		gateway.PublicHandler(),
		http.MethodGet,
		"/__internal/snapshots",
		"Bearer "+fixtureExternalKey,
	)
	if publicInternal.Code != http.StatusNotFound {
		t.Fatalf("public internal probe status = %d, want 404", publicInternal.Code)
	}
	publicReadiness := performRequest(gateway.PublicHandler(), http.MethodGet, "/__internal/ready", "")
	if publicReadiness.Code != http.StatusNotFound {
		t.Fatalf("public readiness status = %d, want 404", publicReadiness.Code)
	}
}

func TestHTTPGatewayReplacesExternalKeyAndNormalizesPath(t *testing.T) {
	now := time.Unix(1000, 0)
	engine := loadFixtureEngine(t, now)
	loadFailOpenHeartbeat(t, engine)
	var accessLog bytes.Buffer
	var captured *http.Request
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request.Clone(request.Context())
		captured.Header = request.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Strict-Transport-Security": []string{"max-age=31536000"},
			},
			Body:    io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request: request,
		}, nil
	})
	gateway := newTestHTTPGateway(t, engine, now, transport, &accessLog)
	request := httptest.NewRequest(http.MethodPost, "/v1//./responses?stream=true", nil)
	request.Host = "api.example.test"
	request.Header.Set("Authorization", "Bearer "+fixtureExternalKey)
	request.Header.Set("Proxy-Authorization", "Bearer "+fixtureExternalKey)
	request.Header.Set("X-Forwarded-Proto", "https")
	response := newCloseNotifyRecorder()
	gateway.PublicHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body=%s", response.Code, response.Body.String())
	}
	if captured == nil {
		t.Fatal("upstream request was not captured")
	}
	if captured.URL.Scheme != "http" || captured.URL.Host != "cliproxy-alpha:8317" {
		t.Fatalf("upstream URL = %s", captured.URL.String())
	}
	if captured.URL.Path != "/v1/responses" || captured.URL.RawQuery != "stream=true" {
		t.Fatalf("upstream path/query = %s?%s", captured.URL.Path, captured.URL.RawQuery)
	}
	if captured.Host != "api.example.test" {
		t.Fatalf("upstream Host = %q", captured.Host)
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer fixture-internal-key" {
		t.Fatalf("upstream Authorization = %q", got)
	}
	if got := captured.Header.Get("Proxy-Authorization"); got != "" {
		t.Fatalf("upstream Proxy-Authorization leaked: %q", got)
	}
	for _, values := range captured.Header {
		for _, value := range values {
			if strings.Contains(value, fixtureExternalKey) {
				t.Fatalf("external API Key leaked in upstream headers")
			}
		}
	}
	if got := response.Header().Get("Strict-Transport-Security"); got != "max-age=0" {
		t.Fatalf("HTTPS HSTS = %q", got)
	}
	if values := response.Header().Values("Strict-Transport-Security"); len(values) != 1 {
		t.Fatalf("HTTPS HSTS values = %#v", values)
	}
	fields := strings.Split(strings.TrimSpace(accessLog.String()), "\t")
	if len(fields) != 5 || fields[1] != "fixture@example.com:alpha" || fields[2] != "alpha" || fields[3] != "200" {
		t.Fatalf("access TSV = %q", accessLog.String())
	}
	if strings.Contains(accessLog.String(), fixtureExternalKey) || strings.Contains(accessLog.String(), "fixture-internal-key") {
		t.Fatal("credential leaked into access TSV")
	}
}

func TestHTTPGatewayInternalProbeBypassesQuotaAndReportsStatus(t *testing.T) {
	now := time.Unix(1000, 0)
	engine := loadFixtureEngine(t, now)
	var capturedPath string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		capturedPath = request.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
			Request:    request,
		}, nil
	})
	gateway := newTestHTTPGateway(t, engine, now, transport, nil)
	ready := performRequest(gateway.InternalHandler(), http.MethodGet, "/__internal/ready", "")
	if ready.Code != http.StatusOK || strings.TrimSpace(ready.Body.String()) != "ready" {
		t.Fatalf("Gateway readiness = status %d body %q", ready.Code, ready.Body.String())
	}
	probe := performRequest(
		gateway.InternalHandler(),
		http.MethodGet,
		"/__internal/probe/models",
		"Bearer "+fixtureExternalKey,
	)
	if probe.Code != http.StatusOK || capturedPath != "/v1/models" {
		t.Fatalf("internal probe = status %d path %q body %s", probe.Code, capturedPath, probe.Body.String())
	}

	statusResponse := performRequest(gateway.InternalHandler(), http.MethodGet, "/__internal/snapshots", "")
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d", statusResponse.Code)
	}
	var status Status
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode snapshot status: %v", err)
	}
	if status.Auth.ActiveGeneration != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		status.Quota.ActiveGeneration != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ||
		status.Quota.LastSuccessAt != 1000 {
		t.Fatalf("snapshot status mismatch: %#v", status)
	}
}

func TestHTTPGatewayProxyErrorKeepsHTTPSHSTSContract(t *testing.T) {
	now := time.Unix(1000, 0)
	engine := loadFixtureEngine(t, now)
	loadFailOpenHeartbeat(t, engine)
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("upstream unavailable")
	})
	gateway := newTestHTTPGateway(t, engine, now, transport, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer "+fixtureExternalKey)
	request.Header.Set("X-Forwarded-Proto", "https")
	response := newCloseNotifyRecorder()
	gateway.PublicHandler().ServeHTTP(response, request)

	assertHTTPError(t, response.ResponseRecorder, http.StatusBadGateway, "upstream_unavailable")
	if values := response.Header().Values("Strict-Transport-Security"); len(values) != 1 || values[0] != "max-age=0" {
		t.Fatalf("proxy error HSTS values = %#v", values)
	}
}

func TestHTTPGatewayStreamsTracksInflightAndPropagatesCancellation(t *testing.T) {
	upstreamStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	releaseFirstChunk := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"effort":"xhigh"`) {
			t.Error("stream request was not capped")
		}
		close(upstreamStarted)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: first\n\n")
		writer.(http.Flusher).Flush()
		<-releaseFirstChunk
		<-request.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()

	address := strings.TrimPrefix(upstream.URL, "http://")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}
	now := time.Unix(1000, 0)
	engine := loadFixtureEngine(t, now)
	setTestEffortLimit(t, engine, "xhigh", now)
	loadFailOpenHeartbeat(t, engine)
	gateway := newTestHTTPGateway(t, engine, now, transport, nil)
	server := httptest.NewServer(gateway.PublicHandler())
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"reasoning":{"effort":"ultra"},"stream":true}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+fixtureExternalKey)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		close(releaseFirstChunk)
		t.Fatalf("start streaming request: %v", err)
	}
	defer response.Body.Close()
	<-upstreamStarted

	firstLine := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(response.Body).ReadString('\n')
		firstLine <- line
	}()
	select {
	case line := <-firstLine:
		if line != "data: first\n" {
			t.Fatalf("first streamed line = %q", line)
		}
	case <-time.After(time.Second):
		close(releaseFirstChunk)
		t.Fatal("first streaming chunk was buffered")
	}

	stats := performRequest(gateway.InternalHandler(), http.MethodGet, "/__stats", "")
	var inflight []InflightStat
	if err := json.Unmarshal(stats.Body.Bytes(), &inflight); err != nil {
		t.Fatalf("decode inflight stats: %v", err)
	}
	if len(inflight) != 1 || inflight[0].Inflight != 1 || inflight[0].Label != "fixture@example.com:alpha" {
		t.Fatalf("inflight stats = %#v", inflight)
	}

	close(releaseFirstChunk)
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not reach upstream")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stats = performRequest(gateway.InternalHandler(), http.MethodGet, "/__stats", "")
		if err := json.Unmarshal(stats.Body.Bytes(), &inflight); err == nil && len(inflight) == 1 && inflight[0].Inflight == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("inflight request did not drain: %s", stats.Body.String())
}

func newTestHTTPGateway(
	t *testing.T,
	engine *Engine,
	now time.Time,
	transport http.RoundTripper,
	accessLog io.Writer,
) *HTTPGateway {
	t.Helper()
	gateway, err := NewHTTPGateway(HTTPGatewayConfig{
		Engine:    engine,
		Logger:    zap.NewNop(),
		Transport: transport,
		AccessLog: accessLog,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHTTPGateway: %v", err)
	}
	return gateway
}

func performRequest(handler http.Handler, method string, path string, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := newCloseNotifyRecorder()
	handler.ServeHTTP(response, request)
	return response.ResponseRecorder
}

func assertHTTPError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q", body.Error.Code, code)
	}
}

func loadFailOpenHeartbeat(t *testing.T, engine *Engine) {
	t.Helper()
	raw := `{
		"version":1,
		"updated_at":1000,
		"ok":false,
		"error":"collector unavailable",
		"stale_after_seconds":15,
		"last_success_at":0,
		"fail_open_after_seconds":300
	}`
	if err := engine.LoadQuotaHeartbeat(strings.NewReader(raw)); err != nil {
		t.Fatalf("load fail-open heartbeat: %v", err)
	}
}
