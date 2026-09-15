package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/httpi18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/notifications"
	"github.com/Alfonsxh/codex-cpa-pool/internal/sitetime"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type notificationStatus struct {
	WebhookConfigured bool   `json:"webhook_configured"`
	WebhookURL        string `json:"webhook_url"`
	WebhookDisplayURL string `json:"webhook_display_url,omitempty"`
	WorkerStatus      string `json:"worker_status"`
	HeartbeatAt       *int64 `json:"heartbeat_at"`
	LastSuccessAt     *int64 `json:"last_success_at"`
	LastError         string `json:"last_error"`
	NextScheduleAt    *int64 `json:"next_schedule_at"`
}

type notificationValues struct {
	Enabled           bool    `json:"enabled"`
	Timezone          string  `json:"timezone"`
	DailyTimes        string  `json:"daily_times"`
	ScheduleGrace     int     `json:"schedule_grace_minutes"`
	QuotaAlertEnabled bool    `json:"quota_alert_enabled"`
	ThresholdPercent  float64 `json:"weekly_threshold_percent"`
}

type notificationSettingsResponse struct {
	Notifications notificationStatus `json:"notifications"`
	Values        notificationValues `json:"values"`
}

func (server *Server) readNotificationSettings(c *gin.Context) {
	payload, err := server.notificationSettings(c.Request.Context(), httpi18n.Locale(c))
	if err != nil {
		server.internalError(c, "read notification settings", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, payload)
}

func (server *Server) notificationSettings(ctx context.Context, lang i18n.Language) (notificationSettingsResponse, error) {
	settings, err := server.store.ReadSettings(ctx)
	if err != nil {
		return notificationSettingsResponse{}, err
	}
	config, err := notifications.ParseConfig(settings)
	if err != nil {
		return notificationSettingsResponse{}, err
	}
	status, err := server.notificationStatusWithConfig(ctx, config, lang)
	if err != nil {
		return notificationSettingsResponse{}, err
	}
	clocks := make([]string, 0, len(config.DailyTimes))
	for _, clock := range config.DailyTimes {
		clocks = append(clocks, clock.String())
	}
	return notificationSettingsResponse{
		Notifications: status,
		Values: notificationValues{
			Enabled: config.Enabled, Timezone: config.TimezoneName,
			DailyTimes:        strings.Join(clocks, ","),
			ScheduleGrace:     int(config.ScheduleGrace / time.Minute),
			QuotaAlertEnabled: config.QuotaAlertEnabled,
			ThresholdPercent:  config.ThresholdPercent,
		},
	}, nil
}

func (server *Server) notificationStatus(ctx context.Context, lang i18n.Language) (notificationStatus, error) {
	settings, err := server.store.ReadSettings(ctx)
	if err != nil {
		return notificationStatus{}, err
	}
	config, err := notifications.ParseConfig(settings)
	if err != nil {
		return notificationStatus{}, err
	}
	return server.notificationStatusWithConfig(ctx, config, lang)
}

func (server *Server) notificationStatusWithConfig(ctx context.Context, config notifications.Config, lang i18n.Language) (notificationStatus, error) {
	state, _, err := notifications.ReadRuntimeState(ctx, server.store)
	if err != nil {
		return notificationStatus{}, err
	}
	now := server.now()
	status := notificationStatus{
		HeartbeatAt: state.HeartbeatAt, LastSuccessAt: state.LastSuccessAt,
		LastError:    state.ErrorText(lang),
		WorkerStatus: notifications.WorkerStatus(state, now, notifications.DefaultMaxHeartbeatAge),
	}
	webhook, found, err := server.store.ReadSecret(ctx, "wecom_webhook")
	if err != nil {
		return notificationStatus{}, err
	}
	if found {
		status.WebhookDisplayURL = notifications.MaskedWebhookURL(webhook)
		status.WebhookConfigured = status.WebhookDisplayURL != ""
	}
	if status.WorkerStatus == "running" && config.Enabled && status.WebhookConfigured {
		status.NextScheduleAt = notifications.NextScheduleAt(now.In(config.Timezone), config.DailyTimes)
	}
	return status, nil
}

type notificationSettingsPayload struct {
	Confirm string `json:"confirm"`
	Values  struct {
		Enabled           *bool    `json:"enabled"`
		Timezone          *string  `json:"timezone"`
		DailyTimes        *string  `json:"daily_times"`
		ScheduleGrace     *int     `json:"schedule_grace_minutes"`
		QuotaAlertEnabled *bool    `json:"quota_alert_enabled"`
		ThresholdPercent  *float64 `json:"weekly_threshold_percent"`
	} `json:"values"`
}

func (server *Server) updateNotificationSettings(c *gin.Context) {
	var body notificationSettingsPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_saving_notification_settings"), "invalid_request")
		return
	}
	server.configurationLock.Lock()
	defer server.configurationLock.Unlock()
	changes, err := server.validatedNotificationChanges(c.Request.Context(), body)
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	if len(changes) == 0 {
		writeError(c, http.StatusBadRequest, i18n.M("admin.provide_at_least_one_notification_setting"), "invalid_request")
		return
	}
	if err := server.store.UpdateSettings(c.Request.Context(), changes); err != nil {
		server.internalError(c, "update notification settings", err)
		return
	}
	payload, err := server.notificationSettings(c.Request.Context(), httpi18n.Locale(c))
	if err != nil {
		server.internalError(c, "read updated notification settings", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("admin.wecom_notification_settings_saved"), "notifications": payload.Notifications,
		"values": payload.Values,
	})
}

func (server *Server) validatedNotificationChanges(
	ctx context.Context,
	body notificationSettingsPayload,
) (map[string]any, error) {
	changes := make(map[string]any)
	if body.Values.Enabled != nil {
		if *body.Values.Enabled {
			configured, err := server.notificationSender.Configured(ctx)
			if err != nil {
				return nil, i18n.M("admin.unable_to_verify_the_wecom_webhook_configuration")
			}
			if !configured {
				return nil, i18n.M("admin.configure_a_webhook_before_enabling_wecom_notifications")
			}
		}
		changes["notification.enabled"] = *body.Values.Enabled
	}
	if body.Values.Timezone != nil {
		value := strings.TrimSpace(*body.Values.Timezone)
		if value == "" {
			return nil, i18n.M("admin.notification_timezone_is_required")
		}
		if _, err := time.LoadLocation(value); err != nil {
			return nil, i18n.M("admin.invalid_notification_timezone")
		}
		settings, err := server.store.ReadSettings(ctx)
		if err != nil {
			return nil, err
		}
		configured, err := sitetime.Name(settings)
		if err != nil {
			return nil, err
		}
		if value != configured {
			return nil, i18n.M("admin.notifications_use_the_system_timezone_change_it_in_configuration_center")
		}
	}
	if body.Values.DailyTimes != nil {
		clocks, err := notifications.ParseClockTimes(*body.Values.DailyTimes)
		if err != nil {
			return nil, err
		}
		values := make([]string, 0, len(clocks))
		for _, clock := range clocks {
			values = append(values, clock.String())
		}
		changes["notification.daily_times"] = strings.Join(values, ",")
	}
	if body.Values.ScheduleGrace != nil {
		if *body.Values.ScheduleGrace < 0 || *body.Values.ScheduleGrace > 120 {
			return nil, i18n.M("admin.the_missed_delivery_window_must_be_between_0_and_120")
		}
		changes["notification.schedule_grace_minutes"] = *body.Values.ScheduleGrace
	}
	if body.Values.QuotaAlertEnabled != nil {
		changes["notification.quota_alert_enabled"] = *body.Values.QuotaAlertEnabled
	}
	if body.Values.ThresholdPercent != nil {
		if *body.Values.ThresholdPercent < 1 || *body.Values.ThresholdPercent > 100 {
			return nil, i18n.M("admin.the_weekly_quota_alert_threshold_must_be_between_1_and")
		}
		changes["notification.weekly_threshold_percent"] = *body.Values.ThresholdPercent
	}
	return changes, nil
}

type notificationWebhookPayload struct {
	WebhookURL string `json:"webhook_url"`
	Confirm    string `json:"confirm"`
}

func (server *Server) updateNotificationWebhook(c *gin.Context) {
	var body notificationWebhookPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_saving_the_wecom_webhook"), "invalid_request")
		return
	}
	webhook, err := notifications.ValidateWebhookURL(body.WebhookURL)
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	if err := server.store.WriteSecret(c.Request.Context(), "wecom_webhook", webhook); err != nil {
		server.internalError(c, "save notification webhook", err)
		return
	}
	status, err := server.notificationStatus(c.Request.Context(), httpi18n.Locale(c))
	if err != nil {
		server.internalError(c, "read saved notification webhook", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("admin.wecom_webhook_saved"), "notifications": status,
	})
}

type notificationConfirmPayload struct {
	Confirm string `json:"confirm"`
}

func (server *Server) clearNotificationWebhook(c *gin.Context) {
	var body notificationConfirmPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "clear" {
		writeError(c, http.StatusBadRequest, i18n.M("admin.confirm_clearing_the_wecom_webhook"), "invalid_request")
		return
	}
	// Delete first: if the following settings update fails, sending remains
	// fail-closed instead of continuing with a credential the operator cleared.
	if err := server.store.DeleteSecret(c.Request.Context(), "wecom_webhook"); err != nil {
		server.internalError(c, "clear notification webhook", err)
		return
	}
	if err := server.store.UpdateSettings(
		c.Request.Context(), map[string]any{"notification.enabled": false},
	); err != nil {
		server.internalError(c, "disable notifications after webhook clear", err)
		return
	}
	status, err := server.notificationStatus(c.Request.Context(), httpi18n.Locale(c))
	if err != nil {
		server.internalError(c, "read cleared notification webhook", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": i18n.M("admin.wecom_webhook_cleared_notifications_are_disabled"), "notifications": status,
	})
}

func (server *Server) sendNotification(c *gin.Context) {
	if server.activity == nil {
		writeError(c, http.StatusServiceUnavailable, i18n.M("admin.usage_query_service_is_not_ready"), "usage_not_ready")
		return
	}
	configured, err := server.notificationSender.Configured(c.Request.Context())
	if err != nil {
		server.internalError(c, "check notification webhook", err)
		return
	}
	if !configured {
		writeError(c, http.StatusBadRequest, i18n.M("admin.no_wecom_webhook_configured"), "invalid_request")
		return
	}
	settings, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read notification configuration", err)
		return
	}
	config, err := notifications.ParseConfig(settings)
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	snapshot, err := notifications.CollectSnapshot(c.Request.Context(), server.store, server.activity)
	if err != nil {
		server.internalError(c, "collect manual notification snapshot", err)
		return
	}
	content, err := notifications.BuildMarkdownV2(
		snapshot, config.ShortName+i18n.Text(config.Language, "admin.account_quota_report"), config.Timezone,
		config.ThresholdPercent, server.now(), nil, nil,
		notifications.UsageCenterURL(config.PublicBaseURL), notifications.ReportOptions{Language: config.Language},
	)
	if err != nil {
		writeError(c, http.StatusBadGateway, err, "notification_send_failed")
		return
	}
	server.deliverNotification(c, content, httpi18n.Text(c, "admin.account_report_sent_to_the_wecom_group"))
}

func (server *Server) testNotification(c *gin.Context) {
	configured, err := server.notificationSender.Configured(c.Request.Context())
	if err != nil {
		server.internalError(c, "check notification webhook", err)
		return
	}
	if !configured {
		writeError(c, http.StatusBadRequest, i18n.M("admin.no_wecom_webhook_configured"), "invalid_request")
		return
	}
	settings, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read notification configuration", err)
		return
	}
	config, err := notifications.ParseConfig(settings)
	if err != nil {
		writeError(c, http.StatusBadRequest, err, "invalid_request")
		return
	}
	content, err := notifications.BuildTestMarkdownV2(config, server.now())
	if err != nil {
		writeError(c, http.StatusBadGateway, err, "notification_send_failed")
		return
	}
	server.deliverNotification(c, content, httpi18n.Text(c, "admin.test_message_sent_to_the_wecom_group"))
}

func (server *Server) deliverNotification(c *gin.Context, content string, successMessage string) {
	result, sendError := server.notificationSender.Send(c.Request.Context(), content)
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
	defer cancel()
	if sendError != nil {
		text, record := notifications.FailureRecord(sendError)
		if stateError := server.store.PatchRuntimeState(
			finalizeContext,
			notifications.RuntimeStateName,
			map[string]any{
				"version":    notifications.RuntimeStateVersion,
				"last_error": text, "last_error_message": record,
			},
		); stateError != nil {
			server.logger.Error("record manual notification failure", zap.Error(stateError))
		}
		writeError(c, http.StatusBadGateway, notifications.ResponseError(sendError), "notification_send_failed")
		return
	}
	now := server.now().Unix()
	if err := server.store.PatchRuntimeState(
		finalizeContext,
		notifications.RuntimeStateName,
		map[string]any{
			"version": notifications.RuntimeStateVersion, "last_success_at": now,
			"last_error": "", "last_error_message": nil,
		},
	); err != nil {
		server.internalError(c, "record manual notification success", err)
		return
	}
	httpi18n.JSON(c, http.StatusOK, gin.H{
		"message": successMessage, "format": "markdown_v2", "result": result,
	})
}
