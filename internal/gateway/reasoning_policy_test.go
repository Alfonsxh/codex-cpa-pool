package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestResponsesWebSocketRejectsUnauthorizedAndPreservesUpstreamErrors(t *testing.T) {
	for _, tc := range []struct {
		name, key                   string
		upstreamStatus, want, calls int
	}{
		{"unauthorized", "invalid-key", 200, 401, 0},
		{"upstream rejection", fixtureExternalKey, 429, 429, 1},
		{"invalid handshake", fixtureExternalKey, 101, 502, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			engine := NewEngine()
			setTestEffortLimit(t, engine, "xhigh", now)
			calls := 0
			rejectionBody := `{"error":"` + strings.Repeat("rate-limit-details ", 200) + `"}`
			gateway := newTestHTTPGateway(t, engine, now, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.upstreamStatus, Header: http.Header{"Retry-After": {"30"}}, Body: io.NopCloser(strings.NewReader(rejectionBody)), Request: request}, nil
			}), nil)
			request := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			request.Header.Set("Authorization", "Bearer "+tc.key)
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Connection", "Upgrade")
			response := httptest.NewRecorder()
			gateway.PublicHandler().ServeHTTP(response, request)
			if response.Code != tc.want || calls != tc.calls {
				t.Fatalf("status=%d calls=%d", response.Code, calls)
			}
			if tc.want == 429 && (response.Header().Get("Retry-After") != "30" || response.Body.String() != rejectionBody) {
				t.Fatal("upstream retry metadata or complete error body lost")
			}
		})
	}
}

func TestWebSocketPolicyRejectsAmbiguousCreateEvents(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.cancel","type":"response.create","reasoning":{"effort":"ultra"}}`,
		`{"type":"response.create","reasoning":{"effort":"low","effort":"ultra"}}`,
		`{"type":"response.create",`,
	} {
		if _, err := rewriteWebSocketRequest([]byte(body), "xhigh"); err != errInvalidWebSocketRequest {
			t.Fatalf("ambiguous message accepted: %v", err)
		}
	}
}

func setTestEffortLimit(t *testing.T, engine *Engine, limit string, now time.Time) {
	t.Helper()
	var snapshot AuthSnapshot
	if err := json.Unmarshal(loadContractFixture(t).AuthSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.MaxReasoningEffort = limit
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.LoadAuthSnapshot(bytes.NewReader(raw), now); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPReasoningCeilingBeforeUpstreamPreservesStreamsAndIdentity(t *testing.T) {
	for _, tc := range []struct{ name, path, body, want, limit, encoding string }{
		{"responses", "/v1/responses?stream=1", `{"model":"m","reasoning":{"effort":"ultra","context":"all_turns"},"input":"do-not-log-opaque"}`, `"effort":"xhigh"`, "xhigh", ""},
		{"codex", "/backend-api/codex/responses", `{"reasoning":{"effort":"max"}}`, `"effort":"xhigh"`, "xhigh", ""},
		{"provider", "/api/provider/openai/v1/responses", `{"reasoning":{"effort":"max"}}`, `"effort":"xhigh"`, "xhigh", ""},
		{"chat", "/v1/chat/completions", `{"reasoning_effort":"max","stream":true}`, `"reasoning_effort":"xhigh"`, "xhigh", ""},
		{"gzip", "/v1/responses", `{"reasoning":{"effort":"max"}}`, `"effort":"xhigh"`, "xhigh", "gzip"},
		{"lower", "/v1/responses", `{ "reasoning": { "effort": "high" } }`, `{ "reasoning": { "effort": "high" } }`, "xhigh", ""},
		{"unset", "/v1/responses", `{ "model":"m", "input":[] }`, `{ "model":"m", "input":[] }`, "xhigh", ""},
		{"disabled", "/v1/responses", `{ "reasoning":{"effort":"ultra"} }`, `{ "reasoning":{"effort":"ultra"} }`, "", ""},
		{"other API", "/v1/images/generations", `{ "reasoning_effort":"ultra" }`, `{ "reasoning_effort":"ultra" }`, "xhigh", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			engine := NewEngine()
			setTestEffortLimit(t, engine, tc.limit, now)
			var logs bytes.Buffer
			calls := 0
			const stream = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"unchanged\"}\n\ndata: [DONE]\n\n"
			gateway := newTestHTTPGateway(t, engine, now, roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), tc.want) {
					t.Errorf("forwarded body = %s", body)
				}
				if request.Header.Get("Authorization") != "Bearer fixture-internal-key" || request.Header.Get("Session-Id") != "stable-session" || request.URL.Host != "cliproxy-alpha:8317" {
					t.Error("identity, routing or session header changed")
				}
				if tc.limit != "" && reasoningRequestPath(request.URL.Path) && (request.ContentLength != int64(len(body)) || request.Header.Get("Content-Encoding") != "" || request.GetBody != nil) {
					t.Error("stale length, compression or replay body")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Codex-Turn-State": {"opaque-state"}}, Body: io.NopCloser(strings.NewReader(stream)), Request: request}, nil
			}), &logs)
			body := []byte(tc.body)
			if tc.encoding == "gzip" {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				_, _ = writer.Write(body)
				_ = writer.Close()
				body = compressed.Bytes()
			}
			request := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+fixtureExternalKey)
			request.Header.Set("Session-Id", "stable-session")
			request.Header.Set("Content-Encoding", tc.encoding)
			response := newCloseNotifyRecorder()
			gateway.PublicHandler().ServeHTTP(response, request)
			if calls != 1 || response.Code != 200 || response.Body.String() != stream || response.Header().Get("X-Codex-Turn-State") != "opaque-state" {
				t.Fatalf("stream/headers changed: %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(logs.String(), "do-not-log-opaque") || strings.Contains(logs.String(), fixtureExternalKey) {
				t.Fatal("payload or credentials leaked into access log")
			}
		})
	}
}

func TestReasoningPolicyRejectsAmbiguousAndUnsupportedRequestsBeforeForwarding(t *testing.T) {
	now := time.Unix(1000, 0)
	engine := NewEngine()
	setTestEffortLimit(t, engine, "xhigh", now)
	calls := 0
	gateway := newTestHTTPGateway(t, engine, now, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		t.Error("invalid request reached upstream")
		return nil, io.EOF
	}), nil)
	for _, tc := range []struct {
		body, encoding, key string
		size                int64
		status              int
	}{
		{`{"reasoning":{"effort":"ultra","effort":"low"}}`, "", fixtureExternalKey, 0, 400},
		{`{"reasoning":`, "", fixtureExternalKey, 0, 400},
		{`{}`, "br", fixtureExternalKey, 0, 415},
		{`bad gzip`, "gzip", fixtureExternalKey, 0, 400},
		{`{}`, "", fixtureExternalKey, maxPolicyRequestBytes + 1, 413},
		{`bad json`, "", "invalid-key", 0, 401},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body))
		r.Header.Set("Content-Encoding", tc.encoding)
		r.Header.Set("Authorization", "Bearer "+tc.key)
		if tc.size != 0 {
			r.ContentLength = tc.size
		}
		w := newCloseNotifyRecorder()
		gateway.PublicHandler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("invalid request status=%d want=%d", w.Code, tc.status)
		}
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestSnapshotLimitValidationRetainsLastGoodPolicy(t *testing.T) {
	now := time.Unix(1000, 0)
	engine := NewEngine()
	setTestEffortLimit(t, engine, "xhigh", now)
	var envelope map[string]any
	_ = json.Unmarshal(loadContractFixture(t).AuthSnapshot, &envelope)
	envelope["max_reasoning_effort"] = "unknown-limit"
	raw, _ := json.Marshal(envelope)
	if err := engine.LoadAuthSnapshot(bytes.NewReader(raw), now); err == nil {
		t.Fatal("invalid snapshot limit accepted")
	}
	if engine.MaxReasoningEffort() != "xhigh" || engine.Authorize(now, "Bearer "+fixtureExternalKey, false).MaxReasoningEffort != "xhigh" {
		t.Fatal("last valid policy lost")
	}
	setTestEffortLimit(t, engine, "", now)
	if engine.MaxReasoningEffort() != "" {
		t.Fatal("disabling limit did not apply")
	}
}

func TestWebSocketPolicyMapsEachNewTurnAndPreservesResponseBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	upstreamClosed := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamClosed)
		if r.Header.Get("Authorization") != "Bearer fixture-internal-key" || r.Header.Get("Session-Id") != "stable-session" {
			t.Error("WebSocket identity/session not preserved")
		}
		w.Header().Set("X-Codex-Turn-State", "handshake-state")
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"responses"}})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := conn.Write(ctx, kind, data); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	now := time.Unix(1000, 0)
	engine := NewEngine()
	setTestEffortLimit(t, engine, "xhigh", now)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = strings.TrimPrefix(upstream.URL, "http://")
		return http.DefaultTransport.RoundTrip(r)
	})
	gateway := newTestHTTPGateway(t, engine, now, transport, nil)
	server := httptest.NewServer(gateway.PublicHandler())
	defer server.Close()
	conn, response, err := websocket.Dial(ctx, server.URL+"/v1/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + fixtureExternalKey}, "Session-Id": {"stable-session"}}, Subprotocols: []string{"responses"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "responses" || response.Header.Get("X-Codex-Turn-State") != "handshake-state" {
		t.Fatal("handshake metadata changed")
	}
	for _, tc := range []struct{ limit, body, want string }{
		{"xhigh", `{"type":"response.create","stream_id":"main","reasoning":{"effort":"ultra"}}`, `"effort":"xhigh"`},
		{"xhigh", `{ "type":"response.create", "reasoning":{"effort":"low"} }`, `{ "type":"response.create", "reasoning":{"effort":"low"} }`},
		{"high", `{"type":"response.create","reasoning":{"effort":"xhigh"}}`, `"effort":"high"`},
		{"high", `{ "type":"response.cancel", "reasoning":{"effort":"ultra"} }`, `{ "type":"response.cancel", "reasoning":{"effort":"ultra"} }`},
		{"", `{ "type":"response.create", "reasoning":{"effort":"ultra"} }`, `{ "type":"response.create", "reasoning":{"effort":"ultra"} }`},
	} {
		setTestEffortLimit(t, engine, tc.limit, now)
		if err := conn.Write(ctx, websocket.MessageText, []byte(tc.body)); err != nil {
			t.Fatal(err)
		}
		kind, data, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageText || !strings.Contains(string(data), tc.want) {
			t.Fatalf("WebSocket reply=%s kind=%v error=%v", data, kind, err)
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	select {
	case <-upstreamClosed:
	case <-time.After(time.Second):
		t.Fatal("client cancellation did not close upstream")
	}
}
