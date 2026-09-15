// Package i18n owns application-authored messages, independently of request and
// business state. Catalogs are immutable after startup; each render selects its
// own language so concurrent requests cannot change one another's locale.
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"errors"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/nicksnyder/go-i18n/v2/i18n/template"
	"golang.org/x/text/language"
)

type Language string

const English Language = "en"
const Chinese Language = "zh-CN"
const SettingKey = "system.language"

type Params map[string]any

type Message struct {
	ID     string
	Params Params
	Cause  error
}

// M binds data to a stable message ID. Parameters are never parsed as templates.
func M(id string, params ...Params) *Message {
	m := &Message{ID: id}
	if len(params) > 0 {
		m.Params = params[0]
	}
	return m
}
func (m *Message) Error() string                { return m.Render(English) }
func (m *Message) Unwrap() error                { return m.Cause }
func (m *Message) WithCause(err error) *Message { copy := *m; copy.Cause = err; return &copy }

// Reference preserves the identity of a nested application label, such as the
// name of a configuration field, until the response language is known.
type Reference string

func Ref(id string) Reference { return Reference(id) }

//go:embed catalog/*.json
var catalogFS embed.FS
var bundle = loadBundle()

func loadBundle() *goi18n.Bundle {
	b := goi18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("json", json.Unmarshal)
	for _, lang := range []Language{English, Chinese} {
		if _, err := b.LoadMessageFileFS(catalogFS, "catalog/"+string(lang)+".json"); err != nil {
			panic(err)
		}
	}
	return b
}

func Normalize(value string) Language {
	tag, err := language.Parse(value)
	if err == nil {
		base, _ := tag.Base()
		if base.String() == "zh" {
			return Chinese
		}
	}
	return English
}

// Negotiate respects HTTP quality values, including q=0. Unsupported or missing
// preferences fall back to the product default, English.
func Negotiate(accept string) Language {
	if len(accept) > 4096 {
		return English
	}
	tags, _, err := language.ParseAcceptLanguage(accept)
	if err != nil {
		return English
	}
	for _, tag := range tags {
		base, _ := tag.Base()
		switch base.String() {
		case "zh":
			return Chinese
		case "en":
			return English
		}
	}
	return English
}
func FromSettings(settings map[string]any) Language {
	value, _ := settings[SettingKey].(string)
	return Normalize(value)
}

func Text(lang Language, id string, params ...Params) string { return M(id, params...).Render(lang) }
func (m *Message) Render(lang Language) string {
	if m == nil {
		return ""
	}
	data := make(map[string]any, len(m.Params))
	for key, value := range m.Params {
		switch v := value.(type) {
		case Reference:
			data[key] = Text(lang, string(v))
		case *Message:
			data[key] = v.Render(lang)
		case error:
			if nested, ok := ErrorMessage(v); ok {
				data[key] = nested.Render(lang)
			} else {
				data[key] = v.Error()
			}
		default:
			data[key] = value
		}
	}
	config := &goi18n.LocalizeConfig{MessageID: m.ID, TemplateData: data, TemplateParser: &template.TextParser{Option: "missingkey=error"}}
	if count, ok := data["Count"]; ok {
		config.PluralCount = count
	}
	result, err := goi18n.NewLocalizer(bundle, string(Normalize(string(lang)))).Localize(config)
	var missing *goi18n.MessageNotFoundErr
	if err == nil || (result != "" && errors.As(err, &missing)) {
		return result
	}
	// Broken templates must never leak Go's <no value> or an internal message ID.
	// Catalog and binding tests make this fallback exceptional.
	fallback := "The operation could not be completed."
	if lang == Chinese {
		fallback = "操作未能完成。"
	}
	return fallback
}

func ErrorMessage(err error) (*Message, bool) {
	var message *Message
	if errors.As(err, &message) {
		return message, true
	}
	return nil, false
}

// ParamsForResponse exposes only the parameters already used by this authored
// message. Errors are rendered as text; translation references retain their ID.
func (m *Message) ParamsForResponse() Params {
	if len(m.Params) == 0 {
		return nil
	}
	out := make(Params, len(m.Params))
	for key, value := range m.Params {
		switch v := value.(type) {
		case Reference:
			out[key] = map[string]string{"message_key": string(v)}
		case *Message:
			out[key] = map[string]any{"message_key": v.ID, "message_params": v.ParamsForResponse()}
		case error:
			if nested, ok := ErrorMessage(v); ok {
				out[key] = map[string]any{"message_key": nested.ID, "message_params": nested.ParamsForResponse()}
			} else {
				out[key] = v.Error()
			}
		default:
			out[key] = value
		}
	}
	return out
}

// Context carries presentation language across HTTP service calls, never into stored state.
type localeKey struct{}

func WithLanguage(ctx context.Context, lang Language) context.Context {
	return context.WithValue(ctx, localeKey{}, Normalize(string(lang)))
}
func FromContext(ctx context.Context) Language {
	if lang, ok := ctx.Value(localeKey{}).(Language); ok {
		return lang
	}
	return English
}
func Selected(values []Language) Language {
	if len(values) > 0 {
		return Normalize(string(values[0]))
	}
	return English
}
