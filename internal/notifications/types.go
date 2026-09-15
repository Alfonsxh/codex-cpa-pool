package notifications

import (
	"context"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
)

const (
	RuntimeStateName      = "notification"
	RuntimeStateVersion   = 1
	MarkdownV2MaximumSize = 4096
)

type ScheduledRecord struct {
	SentAt      int64  `json:"sent_at"`
	PayloadHash string `json:"payload_hash"`
}

type AlertRecord struct {
	ResetAt        *int64  `json:"reset_at"`
	Level          string  `json:"level"`
	Threshold      float64 `json:"threshold"`
	AlertedAt      *int64  `json:"alerted_at"`
	TransitionedAt *int64  `json:"transitioned_at"`
}

type WindowRecord struct {
	UsedPercent float64 `json:"used_percent"`
	ObservedAt  int64   `json:"observed_at"`
	ResetAt     *int64  `json:"reset_at,omitempty"`
}

type RuntimeState struct {
	Version          int                        `json:"version"`
	Scheduled        map[string]ScheduledRecord `json:"scheduled"`
	QuotaAlerts      map[string]AlertRecord     `json:"quota_alerts"`
	QuotaWindows     map[string]WindowRecord    `json:"quota_windows"`
	HeartbeatAt      *int64                     `json:"heartbeat_at"`
	LastSuccessAt    *int64                     `json:"last_success_at"`
	LastError        string                     `json:"last_error"`
	LastErrorMessage *MessageRecord             `json:"last_error_message,omitempty"`
	NextScheduleAt   *int64                     `json:"next_schedule_at"`
	QuotaCheckedAt   *int64                     `json:"quota_checked_at"`
}

// MessageRecord keeps an authored failure renderable after it is persisted, so
// the status text can follow the reader's language. LastError stays alongside it
// as the redacted diagnostic text.
type MessageRecord struct {
	ID     string      `json:"id"`
	Params i18n.Params `json:"params,omitempty"`
}

type AccountSnapshot struct {
	ID            string
	ActiveUsers1H int
	Quota         quota.AccountQuota
}

type Snapshot struct {
	Accounts                []AccountSnapshot
	ActiveUserWindowSeconds int64
}

type Row struct {
	Key         string
	Account     string
	Label       string
	UsedPercent *float64
	ActiveUsers int
	ResetCount  *int64
	ResetAt     *int64
	ResetKey    *int64
	Level       string
}

type Store interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
	PatchRuntimeState(context.Context, string, map[string]any) error
}

type SnapshotStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
}

type ActivityProvider interface {
	RefreshActiveUsersLastHour(context.Context) (map[string]int, error)
}

type ContentSender interface {
	Configured(context.Context) (bool, error)
	Send(context.Context, string) (SendResult, error)
}

type SendResult struct {
	ErrorCode int    `json:"errcode"`
	Message   string `json:"errmsg"`
}

type Config struct {
	Language           i18n.Language
	Enabled            bool
	Timezone           *time.Location
	TimezoneName       string
	DailyTimes         []ClockTime
	ScheduleGrace      time.Duration
	QuotaAlertEnabled  bool
	ThresholdPercent   float64
	QuotaCheckInterval time.Duration
	ShortName          string
	PublicBaseURL      string
}

type ClockTime struct {
	Hour   int
	Minute int
}

func (clock ClockTime) String() string {
	return time.Date(2000, 1, 1, clock.Hour, clock.Minute, 0, 0, time.UTC).Format("15:04")
}

type RunResult struct {
	Sent    []string `json:"sent"`
	Enabled bool     `json:"enabled"`
}
