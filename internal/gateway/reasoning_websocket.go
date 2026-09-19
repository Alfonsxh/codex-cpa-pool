package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

func responseWebSocket(request *http.Request, path string) bool {
	return request.Method == http.MethodGet && strings.EqualFold(request.Header.Get("Upgrade"), "websocket") &&
		(path == "/v1/responses" || path == "/backend-api/codex/responses" ||
			(strings.HasPrefix(path, "/api/provider/") && strings.HasSuffix(path, "/v1/responses")))
}

// HTTP upgrade tunnelling cannot enforce a policy on subsequent response.create
// messages. Terminate these two WebSockets, retaining the authenticated account,
// handshake metadata, subprotocol, message type, response bytes and cancellation.
func (gateway *HTTPGateway) proxyResponsesWebSocket(c *gin.Context, identity Identity, path string) {
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	url := *c.Request.URL
	url.Scheme, url.Host, url.Path, url.RawPath = "ws", identity.Backend, path, ""
	headers := c.Request.Header.Clone()
	stripWebSocketHeaders(headers)
	headers.Set("Authorization", "Bearer "+identity.InternalKey)
	headers.Del("Proxy-Authorization")
	protocols := strings.Split(c.Request.Header.Get("Sec-WebSocket-Protocol"), ",")
	for index := range protocols {
		protocols[index] = strings.TrimSpace(protocols[index])
	}
	if len(protocols) == 1 && protocols[0] == "" {
		protocols = nil
	}
	// Dial retains only 1 KiB of failed handshake bodies. Keep the original
	// non-upgrade response so upstream errors remain complete and streaming.
	var rejection *http.Response
	transport := websocketTransport(func(request *http.Request) (*http.Response, error) {
		response, err := gateway.proxy.Transport.RoundTrip(request)
		if err == nil && response.StatusCode != http.StatusSwitchingProtocols {
			rejection = new(http.Response)
			*rejection = *response
			response.Body = http.NoBody
		}
		return response, err
	})
	upstream, response, err := websocket.Dial(ctx, url.String(), &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		HTTPHeader: headers, Host: c.Request.Host, Subprotocols: protocols,
	})
	if err != nil {
		if rejection != nil {
			response = rejection
		}
		if response != nil && response.StatusCode != http.StatusSwitchingProtocols {
			defer response.Body.Close()
			copyWebSocketResponseHeaders(c.Writer.Header(), response.Header)
			applyHSTS(c.Writer.Header(), c.GetHeader("X-Forwarded-Proto"))
			c.Status(response.StatusCode)
			_, _ = io.Copy(c.Writer, response.Body)
		} else {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			// Dial errors may contain URL query values; do not log client data.
			gateway.handleProxyError(c.Writer, c.Request, errors.New("upstream WebSocket handshake failed"))
		}
		return
	}
	defer upstream.CloseNow()
	copyWebSocketResponseHeaders(c.Writer.Header(), response.Header)
	applyHSTS(c.Writer.Header(), c.GetHeader("X-Forwarded-Proto"))
	selected := []string(nil)
	if protocol := upstream.Subprotocol(); protocol != "" {
		selected = []string{protocol}
	}
	downstream, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		Subprotocols: selected,
		// Public authorization already required an explicit Bearer Key, never a
		// cookie. Keep the existing transparent proxy's Origin behavior.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer downstream.CloseNow()
	upstream.SetReadLimit(maxPolicyRequestBytes)
	downstream.SetReadLimit(maxPolicyRequestBytes)
	type result struct {
		fromClient bool
		err        error
	}
	finished := make(chan result, 2)
	relay := func(source, destination *websocket.Conn, fromClient bool) {
		for {
			kind, data, err := source.Read(ctx)
			if err == nil && fromClient {
				data, err = rewriteWebSocketRequest(data, gateway.engine.MaxReasoningEffort())
			}
			if err == nil {
				err = destination.Write(ctx, kind, data)
			}
			if err != nil {
				finished <- result{fromClient: fromClient, err: err}
				return
			}
		}
	}
	go relay(downstream, upstream, true)
	go relay(upstream, downstream, false)
	ended := <-finished
	status := websocket.CloseStatus(ended.err)
	if ended.err == errInvalidWebSocketRequest {
		status = websocket.StatusPolicyViolation
	} else if status < 1000 || status == websocket.StatusAbnormalClosure || status == websocket.StatusNoStatusRcvd || status == websocket.StatusTLSHandshake {
		status = websocket.StatusInternalError
	}
	if ended.err == errInvalidWebSocketRequest {
		_ = downstream.Close(status, "Invalid response.create request")
	} else if ended.fromClient {
		_ = upstream.Close(status, "")
	} else {
		_ = downstream.Close(status, "")
	}
	_ = upstream.CloseNow()
	_ = downstream.CloseNow()
	cancel()
	<-finished
}

type websocketTransport func(*http.Request) (*http.Response, error)

func (transport websocketTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func rewriteWebSocketRequest(data []byte, limit string) ([]byte, error) {
	if limit == "" {
		return data, nil
	}
	kind, err := reasoningpolicy.MessageType(data)
	if err != nil {
		return nil, errInvalidWebSocketRequest
	}
	if kind != "response.create" {
		return data, nil
	}
	mapped, err := reasoningpolicy.Rewrite(data, limit)
	if err != nil {
		return nil, errInvalidWebSocketRequest
	}
	return mapped, nil
}

func stripWebSocketHeaders(headers http.Header) {
	for _, connection := range headers.Values("Connection") {
		for _, key := range strings.Split(connection, ",") {
			headers.Del(strings.TrimSpace(key))
		}
	}
	for _, key := range []string{"Connection", "Upgrade", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization", "Transfer-Encoding", "Te", "Trailer", "Content-Length", "Sec-WebSocket-Key", "Sec-WebSocket-Accept", "Sec-WebSocket-Version", "Sec-WebSocket-Protocol", "Sec-WebSocket-Extensions"} {
		headers.Del(key)
	}
}

func copyWebSocketResponseHeaders(target, source http.Header) {
	headers := source.Clone()
	stripWebSocketHeaders(headers)
	for key, values := range headers {
		target[key] = append([]string(nil), values...)
	}
}
