package gateway

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
	"github.com/gin-gonic/gin"
)

const maxPolicyRequestBytes = 64 << 20

func reasoningRequestPath(path string) bool {
	return path == "/v1/responses" || path == "/v1/responses/compact" ||
		path == "/v1/chat/completions" || path == "/backend-api/codex/responses" ||
		path == "/backend-api/codex/responses/compact" ||
		(strings.HasPrefix(path, "/api/provider/") && (strings.HasSuffix(path, "/v1/responses") || strings.HasSuffix(path, "/v1/chat/completions")))
}

func (gateway *HTTPGateway) applyReasoningPolicy(c *gin.Context, limit, path string) bool {
	request := c.Request
	if limit == "" || request.Method != http.MethodPost || !reasoningRequestPath(path) {
		return true
	}
	reader := request.Body
	if reader == nil {
		policyError(c, http.StatusBadRequest, "invalid_request", "Request must be a JSON object")
		return false
	}
	defer request.Body.Close()
	switch strings.ToLower(strings.TrimSpace(request.Header.Get("Content-Encoding"))) {
	case "", "identity":
		if request.ContentLength > maxPolicyRequestBytes {
			policyError(c, http.StatusRequestEntityTooLarge, "request_too_large", "Request exceeds the 64 MiB reasoning-policy limit")
			return false
		}
	case "gzip":
		decoded, err := gzip.NewReader(reader)
		if err != nil {
			policyError(c, http.StatusBadRequest, "invalid_request", "Invalid compressed request")
			return false
		}
		defer decoded.Close()
		reader = decoded
	default:
		policyError(c, http.StatusUnsupportedMediaType, "unsupported_content_encoding", "Supported request encodings are identity and gzip")
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxPolicyRequestBytes+1))
	if len(raw) > maxPolicyRequestBytes {
		policyError(c, http.StatusRequestEntityTooLarge, "request_too_large", "Request exceeds the 64 MiB reasoning-policy limit")
		return false
	}
	if err != nil {
		policyError(c, http.StatusBadRequest, "invalid_request", "Could not read request body")
		return false
	}
	mapped, err := reasoningpolicy.Rewrite(raw, limit)
	if err != nil {
		policyError(c, http.StatusBadRequest, "invalid_request", "Request must be a valid JSON object without duplicate fields")
		return false
	}
	request.Body = io.NopCloser(bytes.NewReader(mapped))
	request.GetBody = nil // A policy rewrite must not make a generation replayable.
	request.ContentLength = int64(len(mapped))
	request.TransferEncoding = nil
	request.Header.Del("Content-Length")
	request.Header.Del("Content-Encoding")
	return true
}

func policyError(c *gin.Context, status int, code, message string) {
	c.JSON(status, ErrorResponse{Error: APIError{Message: message, Type: "invalid_request_error", Code: code}})
}

var errInvalidWebSocketRequest = errors.New("invalid response.create request")
