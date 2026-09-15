// Package httpi18n selects request language and presents authored messages.
// It never searches or rewrites arbitrary JSON string values.
package httpi18n

import (
	"net/http"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/gin-gonic/gin"
)

const contextKey = "cpa.response-language"

func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		selected := i18n.Negotiate(c.GetHeader("Accept-Language"))
		c.Set(contextKey, selected)
		c.Request = c.Request.WithContext(i18n.WithLanguage(c.Request.Context(), selected))
		c.Header("Content-Language", string(selected))
		appendVary(c.Writer.Header(), "Accept-Language")
		c.Next()
	}
}
func appendVary(header http.Header, value string) {
	for _, line := range header.Values("Vary") {
		for _, item := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) || strings.TrimSpace(item) == "*" {
				return
			}
		}
	}
	header.Add("Vary", value)
}
func Locale(c *gin.Context) i18n.Language {
	if selected, ok := c.Get(contextKey); ok {
		if lang, ok := selected.(i18n.Language); ok {
			return lang
		}
	}
	return i18n.Negotiate(c.GetHeader("Accept-Language"))
}
func Text(c *gin.Context, id string, params ...i18n.Params) string {
	return i18n.Text(Locale(c), id, params...)
}

type Notice struct {
	Message       string      `json:"message"`
	MessageKey    string      `json:"message_key,omitempty"`
	MessageParams i18n.Params `json:"message_params,omitempty"`
}

func NewNotice(lang i18n.Language, message *i18n.Message) Notice {
	return Notice{Message: message.Render(lang), MessageKey: message.ID, MessageParams: message.ParamsForResponse()}
}

// Presenter is implemented by DTOs with authored display fields. Implementations
// return copies and must preserve identifiers, user content and numeric facts.
type Presenter interface{ Localized(i18n.Language) any }

func JSON(c *gin.Context, status int, payload any) { c.JSON(status, present(Locale(c), payload)) }
func present(lang i18n.Language, value any) any {
	switch v := value.(type) {
	case *i18n.Message:
		return v.Render(lang)
	case Presenter:
		return v.Localized(lang)
	case gin.H:
		return presentMap(lang, map[string]any(v))
	case map[string]any:
		return presentMap(lang, v)
	case []any:
		out := make([]any, len(v))
		for n, item := range v {
			out[n] = present(lang, item)
		}
		return out
	default:
		return value
	}
}
func presentMap(lang i18n.Language, values map[string]any) map[string]any {
	out := make(map[string]any, len(values)+2)
	for key, value := range values {
		out[key] = present(lang, value)
		if key == "message" {
			if m, ok := value.(*i18n.Message); ok {
				out["message_key"] = m.ID
				if len(m.Params) > 0 {
					out["message_params"] = m.ParamsForResponse()
				}
			}
		}
	}
	return out
}

func Error(c *gin.Context, status int, value any, code string, errorType ...string) {
	kind := code
	if len(errorType) > 0 {
		kind = errorType[0]
	}
	notice := Notice{}
	switch v := value.(type) {
	case *i18n.Message:
		notice = NewNotice(Locale(c), v)
	case error:
		if message, ok := i18n.ErrorMessage(v); ok {
			notice = NewNotice(Locale(c), message)
		} else {
			notice.Message = v.Error()
		}
	case string:
		notice.Message = v
	}
	c.AbortWithStatusJSON(status, gin.H{"error": struct {
		Notice
		Type string `json:"type"`
		Code string `json:"code"`
	}{notice, kind, code}})
}
