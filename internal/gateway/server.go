package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type HTTPGatewayConfig struct {
	Engine    *Engine
	Logger    *zap.Logger
	AccessLog io.Writer
	Transport http.RoundTripper
	Now       func() time.Time
}

type HTTPGateway struct {
	engine    *Engine
	logger    *zap.Logger
	now       func() time.Time
	proxy     *httputil.ReverseProxy
	accessLog *tsvAccessLogger
	inflight  *inflightRegistry

	warningMu sync.Mutex
	warnings  map[string]int64

	public   *gin.Engine
	internal *gin.Engine
}

type proxyContextKey struct{}

type proxyContext struct {
	identity Identity
	host     string
}

const (
	contextLabelKey   = "gateway.access.label"
	contextAccountKey = "gateway.access.account"
)

var configureGinMode sync.Once

func NewHTTPGateway(config HTTPGatewayConfig) (*HTTPGateway, error) {
	if config.Engine == nil {
		return nil, fmt.Errorf("gateway HTTP server requires an authorization engine")
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Transport == nil {
		config.Transport = http.DefaultTransport
	}
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})

	gateway := &HTTPGateway{
		engine:    config.Engine,
		logger:    config.Logger,
		now:       config.Now,
		accessLog: &tsvAccessLogger{writer: config.AccessLog},
		inflight:  newInflightRegistry(),
		warnings:  make(map[string]int64),
	}
	gateway.proxy = &httputil.ReverseProxy{
		Director:      gateway.directProxyRequest,
		Transport:     config.Transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler:  gateway.handleProxyError,
		ModifyResponse: func(response *http.Response) error {
			forwardedProtocol := ""
			if response.Request != nil {
				forwardedProtocol = response.Request.Header.Get("X-Forwarded-Proto")
			}
			applyHSTS(response.Header, forwardedProtocol)
			return nil
		},
	}

	public, err := gateway.newRouter()
	if err != nil {
		return nil, err
	}
	public.Use(gateway.recovery(), gateway.hsts(), gateway.publicAccessLog())
	public.Any("/__health", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte("ok\n"))
	})
	public.NoRoute(gateway.handlePublicRequest)
	gateway.public = public

	internal, err := gateway.newRouter()
	if err != nil {
		return nil, err
	}
	internal.Use(gateway.recovery(), gateway.hsts())
	internal.GET("/__internal/ready", func(c *gin.Context) {
		if !gateway.engine.AuthenticationReady(gateway.now()) {
			c.Header("Retry-After", "1")
			c.Data(http.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("authentication snapshot unavailable\n"))
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte("ready\n"))
	})
	internal.GET("/__internal/probe/models", gateway.handleInternalModelProbe)
	internal.GET("/__internal/snapshots", func(c *gin.Context) {
		c.JSON(http.StatusOK, gateway.engine.Status())
	})
	internal.GET("/__stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, gateway.inflight.Snapshot())
	})
	internal.NoRoute(func(c *gin.Context) {
		c.Status(http.StatusNotFound)
	})
	gateway.internal = internal

	return gateway, nil
}

func (gateway *HTTPGateway) newRouter() (*gin.Engine, error) {
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = false
	router.RemoveExtraSlash = false
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("disable trusted proxies: %w", err)
	}
	return router, nil
}

func (gateway *HTTPGateway) PublicHandler() http.Handler {
	return gateway.public
}

func (gateway *HTTPGateway) InternalHandler() http.Handler {
	return gateway.internal
}

func (gateway *HTTPGateway) handlePublicRequest(c *gin.Context) {
	normalizedPath, allowed := NormalizePublicPath(c.Request.URL.Path)
	if !allowed {
		c.Status(http.StatusNotFound)
		return
	}
	decision := gateway.engine.Authorize(gateway.now(), c.GetHeader("Authorization"), true)
	if !decision.Allowed {
		gateway.writeDecision(c, decision)
		return
	}
	gateway.warnFailOpen(decision.Warning)
	identity := *decision.Identity
	c.Set(contextLabelKey, identity.Label)
	c.Set(contextAccountKey, identity.Account)
	done := gateway.inflight.Start(identity.Label, identity.Account)
	defer done()
	if responseWebSocket(c.Request, normalizedPath) {
		gateway.proxyResponsesWebSocket(c, identity, normalizedPath)
		return
	}
	if !gateway.applyReasoningPolicy(c, decision.MaxReasoningEffort, normalizedPath) {
		return
	}
	gateway.proxyRequest(c, identity, normalizedPath)
}

func (gateway *HTTPGateway) handleInternalModelProbe(c *gin.Context) {
	decision := gateway.engine.Authorize(gateway.now(), c.GetHeader("Authorization"), false)
	if !decision.Allowed {
		gateway.writeDecision(c, decision)
		return
	}
	gateway.proxyRequest(c, *decision.Identity, "/v1/models")
}

func (gateway *HTTPGateway) writeDecision(c *gin.Context, decision Decision) {
	if decision.RetryAfterSeconds > 0 {
		c.Header("Retry-After", fmt.Sprintf("%d", decision.RetryAfterSeconds))
	}
	status := decision.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	c.JSON(status, decision.Response)
}

func (gateway *HTTPGateway) proxyRequest(c *gin.Context, identity Identity, targetPath string) {
	// ReverseProxy copies response headers with Add. Remove the middleware's
	// provisional HSTS value first; ModifyResponse writes the authoritative
	// single value after seeing the upstream response.
	c.Writer.Header().Del("Strict-Transport-Security")
	request := c.Request.Clone(context.WithValue(c.Request.Context(), proxyContextKey{}, proxyContext{
		identity: identity,
		host:     c.Request.Host,
	}))
	clonedURL := *request.URL
	clonedURL.Path = targetPath
	clonedURL.RawPath = ""
	request.URL = &clonedURL
	gateway.proxy.ServeHTTP(c.Writer, request)
}

func (gateway *HTTPGateway) directProxyRequest(request *http.Request) {
	proxy, ok := request.Context().Value(proxyContextKey{}).(proxyContext)
	if !ok {
		return
	}
	request.URL.Scheme = "http"
	request.URL.Host = proxy.identity.Backend
	request.Host = proxy.host
	request.Header.Set("Authorization", "Bearer "+proxy.identity.InternalKey)
	request.Header.Del("Proxy-Authorization")
}

func (gateway *HTTPGateway) handleProxyError(writer http.ResponseWriter, request *http.Request, err error) {
	fields := []zap.Field{
		zap.String("method", request.Method),
		zap.String("path", request.URL.Path),
		zap.Error(err),
	}
	if proxy, ok := request.Context().Value(proxyContextKey{}).(proxyContext); ok {
		fields = append(fields, zap.String("account", proxy.identity.Account))
	}
	gateway.logger.Warn("gateway upstream request failed", fields...)
	applyHSTS(writer.Header(), request.Header.Get("X-Forwarded-Proto"))
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(writer).Encode(ErrorResponse{Error: APIError{
		Message: "Upstream service is temporarily unavailable",
		Type:    "server_error",
		Code:    "upstream_unavailable",
	}})
}

func (gateway *HTTPGateway) warnFailOpen(reason string) {
	if reason == "" {
		return
	}
	now := gateway.now().Unix()
	gateway.warningMu.Lock()
	last := gateway.warnings[reason]
	if last == now {
		gateway.warningMu.Unlock()
		return
	}
	gateway.warnings[reason] = now
	gateway.warningMu.Unlock()
	gateway.logger.Warn("user quota protection unavailable; request allowed", zap.String("reason", reason))
}

func (gateway *HTTPGateway) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				applyHSTS(c.Writer.Header(), c.GetHeader("X-Forwarded-Proto"))
				gateway.logger.Error(
					"gateway request panic",
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
					zap.String("panic_type", fmt.Sprintf("%T", recovered)),
					zap.Stack("stack"),
				)
				c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorResponse{Error: APIError{
					Message: "Internal server error",
					Type:    "server_error",
					Code:    "internal_error",
				}})
			}
		}()
		c.Next()
	}
}

func (gateway *HTTPGateway) hsts() gin.HandlerFunc {
	return func(c *gin.Context) {
		applyHSTS(c.Writer.Header(), c.GetHeader("X-Forwarded-Proto"))
		c.Next()
	}
}

func applyHSTS(header http.Header, forwardedProtocol string) {
	if forwardedProtocol == "https" {
		header.Set("Strict-Transport-Security", "max-age=0")
		return
	}
	header.Del("Strict-Transport-Security")
}

func (gateway *HTTPGateway) publicAccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		if skipPublicAccessLog(c.Request.URL.Path) {
			c.Next()
			return
		}
		startedAt := gateway.now()
		defer func() {
			label, _ := c.Get(contextLabelKey)
			account, _ := c.Get(contextAccountKey)
			if err := gateway.accessLog.Write(accessLogRow{
				Timestamp: startedAt,
				Label:     stringValue(label),
				Account:   stringValue(account),
				Status:    c.Writer.Status(),
				Duration:  gateway.now().Sub(startedAt),
			}); err != nil {
				gateway.logger.Warn("gateway access log write failed", zap.Error(err))
			}
		}()
		c.Next()
	}
}

func skipPublicAccessLog(path string) bool {
	return path == "/__health" || path == "/__stats" || strings.HasPrefix(path, "/__internal/")
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

type accessLogRow struct {
	Timestamp time.Time
	Label     string
	Account   string
	Status    int
	Duration  time.Duration
}

type tsvAccessLogger struct {
	mu     sync.Mutex
	writer io.Writer
}

func (logger *tsvAccessLogger) Write(row accessLogRow) error {
	if logger.writer == nil {
		return nil
	}
	line := fmt.Sprintf(
		"%.3f\t%s\t%s\t%d\t%.3f\n",
		float64(row.Timestamp.UnixNano())/float64(time.Second),
		sanitizeTSV(row.Label),
		sanitizeTSV(row.Account),
		row.Status,
		max(0, row.Duration.Seconds()),
	)
	logger.mu.Lock()
	defer logger.mu.Unlock()
	_, err := io.WriteString(logger.writer, line)
	return err
}

func sanitizeTSV(value string) string {
	return strings.Map(func(character rune) rune {
		switch character {
		case '\t', '\r', '\n':
			return '_'
		default:
			return character
		}
	}, value)
}

type InflightStat struct {
	Label    string `json:"label"`
	Account  string `json:"account"`
	Inflight int64  `json:"inflight"`
}

type inflightRegistry struct {
	mu      sync.Mutex
	entries map[string]InflightStat
}

func newInflightRegistry() *inflightRegistry {
	return &inflightRegistry{entries: make(map[string]InflightStat)}
}

func (registry *inflightRegistry) Start(label string, account string) func() {
	registry.mu.Lock()
	entry := registry.entries[label]
	entry.Label = label
	entry.Account = account
	entry.Inflight++
	registry.entries[label] = entry
	registry.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			entry := registry.entries[label]
			if entry.Inflight > 0 {
				entry.Inflight--
			}
			registry.entries[label] = entry
			registry.mu.Unlock()
		})
	}
}

func (registry *inflightRegistry) Snapshot() []InflightStat {
	registry.mu.Lock()
	result := make([]InflightStat, 0, len(registry.entries))
	for _, entry := range registry.entries {
		result = append(result, entry)
	}
	registry.mu.Unlock()
	sort.Slice(result, func(left, right int) bool {
		return result[left].Label < result[right].Label
	})
	return result
}
