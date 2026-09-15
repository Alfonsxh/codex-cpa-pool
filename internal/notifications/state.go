package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/i18n"
)

func DefaultRuntimeState() RuntimeState {
	return RuntimeState{
		Version:      RuntimeStateVersion,
		Scheduled:    make(map[string]ScheduledRecord),
		QuotaAlerts:  make(map[string]AlertRecord),
		QuotaWindows: make(map[string]WindowRecord),
	}
}

func ReadRuntimeState(ctx context.Context, store interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
}) (RuntimeState, bool, error) {
	state := DefaultRuntimeState()
	var raw json.RawMessage
	found, err := store.ReadRuntimeState(ctx, RuntimeStateName, &raw)
	if err != nil {
		return state, false, fmt.Errorf("read notification runtime state: %w", err)
	}
	if !found {
		return state, false, nil
	}
	var fields map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &fields) != nil {
		return state, true, nil
	}
	if !decodeField(fields["version"], &state.Version) || state.Version != RuntimeStateVersion {
		return DefaultRuntimeState(), true, nil
	}
	decodeRecordMap(fields["scheduled"], &state.Scheduled)
	decodeRecordMap(fields["quota_alerts"], &state.QuotaAlerts)
	decodeRecordMap(fields["quota_windows"], &state.QuotaWindows)
	decodeField(fields["heartbeat_at"], &state.HeartbeatAt)
	decodeField(fields["last_success_at"], &state.LastSuccessAt)
	decodeField(fields["last_error"], &state.LastError)
	decodeField(fields["last_error_message"], &state.LastErrorMessage)
	decodeField(fields["next_schedule_at"], &state.NextScheduleAt)
	decodeField(fields["quota_checked_at"], &state.QuotaCheckedAt)
	return state, true, nil
}

// FailureRecord pairs the redacted diagnostic text of a failure with the
// authored message that produced it. Parameters are bounded like the
// diagnostic, because upstream text and provider error bodies are untrusted in
// size.
func FailureRecord(err error) (string, *MessageRecord) {
	text := boundedError(RedactWebhook(err), maximumRecordedError)
	var message *i18n.Message
	if errors.As(err, &message) {
		return text, &MessageRecord{ID: message.ID, Params: boundedParams(message.Params)}
	}
	return text, nil
}

// ErrorText renders the recorded failure in the reader's language. Failures
// without an authored message keep their diagnostic text.
func (state RuntimeState) ErrorText(lang i18n.Language) string {
	if state.LastErrorMessage != nil && state.LastErrorMessage.ID != "" {
		return boundedError(i18n.M(state.LastErrorMessage.ID, state.LastErrorMessage.Params).Render(lang), maximumRecordedError)
	}
	return boundedError(state.LastError, maximumRecordedError)
}

func boundedParams(params i18n.Params) i18n.Params {
	if len(params) == 0 {
		return nil
	}
	bounded := make(i18n.Params, len(params))
	for key, value := range params {
		switch typed := value.(type) {
		case string:
			bounded[key] = boundedError(typed, maximumRecordedError)
		case int, int64, float64, bool, nil:
			bounded[key] = value
		default:
			// Nested messages and errors cannot be rendered again after the JSON
			// round trip, so their redacted text is recorded instead.
			bounded[key] = boundedError(RedactWebhook(typed), maximumRecordedError)
		}
	}
	return bounded
}

const maximumRecordedError = 500

func decodeField(raw json.RawMessage, destination any) bool {
	return len(raw) > 0 && json.Unmarshal(raw, destination) == nil
}

func decodeRecordMap[T any](raw json.RawMessage, destination *map[string]T) {
	var records map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &records) != nil {
		*destination = make(map[string]T)
		return
	}
	result := make(map[string]T, len(records))
	for key, value := range records {
		var record T
		if json.Unmarshal(value, &record) == nil {
			result[key] = record
		}
	}
	*destination = result
}

const DefaultMaxHeartbeatAge = 3 * time.Minute

// WorkerStatus describes the scheduler heartbeat, independently of manual sends
// and delivery failures. It does not inspect the Docker process itself.
func WorkerStatus(
	state RuntimeState,
	now time.Time,
	maxAge time.Duration,
) string {
	if state.Version != RuntimeStateVersion || state.HeartbeatAt == nil || *state.HeartbeatAt <= 0 {
		return "not_started"
	}
	age := now.Unix() - *state.HeartbeatAt
	if maxAge <= 0 || age < 0 || age > int64(maxAge/time.Second) {
		return "heartbeat_lost"
	}
	return "running"
}
