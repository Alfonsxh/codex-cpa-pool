package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/go-resty/resty/v2"
)

const (
	webhookHost              = "qyapi.weixin.qq.com"
	webhookPath              = "/cgi-bin/webhook/send"
	maximumWebhookURLLength  = 2048
	maximumWebhookBodyLength = 64 << 10
)

var (
	webhookKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,256}$`)
	webhookRedactor   = regexp.MustCompile(`(?i)https://qyapi\.weixin\.qq\.com/cgi-bin/webhook/send\?key=[^\s&"']+`)
)

type SecretStore interface {
	ReadSecret(context.Context, string) (string, bool, error)
}

type WebhookSender struct {
	Store   SecretStore
	Client  *resty.Client
	Timeout time.Duration
}

func ValidateWebhookURL(value string) (string, error) {
	raw := strings.TrimSpace(value)
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > maximumWebhookURLLength || parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), webhookHost) || parsed.Port() != "" ||
		parsed.Path != webhookPath || parsed.User != nil || parsed.Fragment != "" {
		return "", i18n.M("notifications.webhook_url_must_be_a_wecom_message_delivery_https_url")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 || len(query["key"]) != 1 || !webhookKeyPattern.MatchString(query["key"][0]) {
		return "", i18n.M("notifications.webhook_url_must_be_a_wecom_message_delivery_https_url")
	}
	return raw, nil
}

func RedactWebhook(value any) string {
	return webhookRedactor.ReplaceAllString(fmt.Sprint(value), "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=[REDACTED]")
}

// ResponseError keeps authored messages renderable in the request language while
// arbitrary errors, which may embed the webhook credential, are redacted. Every
// message parameter carrying external text is redacted where the message is
// built, so an authored message is safe to render as-is.
func ResponseError(err error) any {
	var message *i18n.Message
	if errors.As(err, &message) {
		return message
	}
	return RedactWebhook(err)
}

// MaskedWebhookURL is display-only. Never return the complete credential to a
// settings reader or accept this masked representation as a replacement URL.
func MaskedWebhookURL(value string) string {
	validated, err := ValidateWebhookURL(value)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(validated)
	if err != nil {
		return ""
	}
	key := parsed.Query().Get("key")
	return "https://" + webhookHost + webhookPath + "?key=••••••" + key[len(key)-4:]
}

func (sender *WebhookSender) Configured(ctx context.Context) (bool, error) {
	_, err := sender.webhookURL(ctx)
	if errors.Is(err, ErrWebhookNotConfigured) || errors.Is(err, ErrWebhookInvalid) {
		return false, nil
	}
	return err == nil, err
}

var (
	ErrWebhookNotConfigured = i18n.M("admin.no_wecom_webhook_configured")
	ErrWebhookInvalid       = i18n.M("notifications.invalid_wecom_webhook_configuration")
)

func (sender *WebhookSender) webhookURL(ctx context.Context) (string, error) {
	if sender == nil || sender.Store == nil {
		return "", i18n.M("notifications.wecom_sender_requires_a_secret_store")
	}
	value, found, err := sender.Store.ReadSecret(ctx, "wecom_webhook")
	if err != nil {
		return "", i18n.M("notifications.read_wecom_webhook", i18n.Params{"Detail": RedactWebhook(err)}).WithCause(err)
	}
	if !found || strings.TrimSpace(value) == "" {
		return "", ErrWebhookNotConfigured
	}
	validated, err := ValidateWebhookURL(value)
	if err != nil {
		return "", ErrWebhookInvalid
	}
	return validated, nil
}

func (sender *WebhookSender) Send(ctx context.Context, content string) (SendResult, error) {
	if len([]byte(content)) > MarkdownV2MaximumSize {
		return SendResult{}, i18n.M("notifications.wecom_markdown_v2_content_exceeds_4096_bytes")
	}
	webhook, err := sender.webhookURL(ctx)
	if err != nil {
		return SendResult{}, err
	}
	timeout := sender.Timeout
	if timeout <= 0 || timeout > time.Minute {
		timeout = 10 * time.Second
	}
	client := sender.Client
	if client == nil {
		client = resty.New()
	}
	client = client.Clone().
		SetTimeout(timeout).
		SetRetryCount(0).
		SetResponseBodyLimit(maximumWebhookBodyLength).
		SetRedirectPolicy(resty.NoRedirectPolicy())
	response, err := client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json; charset=utf-8").
		SetBody(map[string]any{
			"msgtype":     "markdown_v2",
			"markdown_v2": map[string]string{"content": content},
		}).
		Post(webhook)
	if err != nil {
		return SendResult{}, i18n.M("notifications.wecom_message_delivery_failed", i18n.Params{"Value": RedactWebhook(err)})
	}
	if response.StatusCode() < 200 || response.StatusCode() >= 300 {
		return SendResult{}, i18n.M("notifications.wecom_message_delivery_failed_http", i18n.Params{"Status": response.StatusCode()})
	}
	var payload struct {
		ErrorCode *int   `json:"errcode"`
		Message   string `json:"errmsg"`
	}
	if err := json.Unmarshal(response.Body(), &payload); err != nil || payload.ErrorCode == nil {
		return SendResult{}, i18n.M("notifications.wecom_message_delivery_failed_invalid_response")
	}
	if *payload.ErrorCode != 0 {
		message := strings.TrimSpace(RedactWebhook(payload.Message))
		if message == "" {
			return SendResult{}, i18n.M("notifications.wecom_message_delivery_failed_invalid_response")
		}
		return SendResult{}, i18n.M("notifications.wecom_message_delivery_failed", i18n.Params{"Value": message})
	}
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = "ok"
	}
	return SendResult{ErrorCode: 0, Message: message}, nil
}
