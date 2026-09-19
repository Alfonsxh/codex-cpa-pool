package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync/atomic"
	"time"
)

const AuthSnapshotMaxAge = 5 * time.Second

type Identity struct {
	UserEmail   string
	Account     string
	Backend     string
	InternalKey string
	Label       string
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

type WeeklyQuota struct {
	UsedTokens         int64  `json:"used_tokens"`
	WeightedUsedTokens int64  `json:"weighted_used_tokens"`
	RawUsedTokens      int64  `json:"raw_used_tokens"`
	LimitTokens        int64  `json:"limit_tokens"`
	WeekEndAt          int64  `json:"week_end_at"`
	QuotaUnit          string `json:"quota_unit"`
}

type ErrorResponse struct {
	Error           APIError     `json:"error"`
	UserWeeklyQuota *WeeklyQuota `json:"user_weekly_quota,omitempty"`
}

type Decision struct {
	Allowed            bool
	Status             int
	Identity           *Identity
	Response           *ErrorResponse
	RetryAfterSeconds  int64
	MaxReasoningEffort string
	Warning            string
}

func (decision Decision) UpstreamAuthorization() string {
	if decision.Identity == nil || decision.Identity.InternalKey == "" {
		return ""
	}
	return "Bearer " + decision.Identity.InternalKey
}

type authState struct {
	maxReasoningEffort string
	generation         string
	previousGeneration string
	generatedAt        float64
	loadedAt           int64
	recordCount        int
	byDigest           map[string]Identity
}

type quotaState struct {
	generation         string
	previousGeneration string
	generatedAt        float64
	loadedAt           int64
	recordCount        int
	byUser             map[string]QuotaRecord
}

type Engine struct {
	auth      atomic.Pointer[authState]
	quota     atomic.Pointer[quotaState]
	heartbeat atomic.Pointer[QuotaHeartbeat]
}

func NewEngine() *Engine {
	return &Engine{}
}

func (engine *Engine) LoadAuthSnapshot(reader io.Reader, loadedAt time.Time) error {
	snapshot, err := ParseAuthSnapshot(reader)
	if err != nil {
		return err
	}
	byDigest := make(map[string]Identity, len(snapshot.Records))
	for _, record := range snapshot.Records {
		byDigest[record.ExternalKeySHA256] = Identity{
			UserEmail:   record.UserEmail,
			Account:     record.Account,
			Backend:     record.Backend,
			InternalKey: record.InternalKey,
			Label:       record.Label,
		}
	}
	previous := engine.auth.Load()
	previousGeneration := ""
	if previous != nil {
		previousGeneration = previous.previousGeneration
		if previous.generation != snapshot.Generation {
			previousGeneration = previous.generation
		}
	}
	engine.auth.Store(&authState{
		generation:         snapshot.Generation,
		maxReasoningEffort: snapshot.MaxReasoningEffort,
		previousGeneration: previousGeneration,
		generatedAt:        snapshot.GeneratedAt,
		loadedAt:           loadedAt.Unix(),
		recordCount:        len(snapshot.Records),
		byDigest:           byDigest,
	})
	return nil
}

func (engine *Engine) LoadQuotaSnapshot(reader io.Reader, loadedAt time.Time) error {
	snapshot, err := ParseQuotaSnapshot(reader)
	if err != nil {
		return err
	}
	byUser := make(map[string]QuotaRecord, len(snapshot.Records))
	for _, record := range snapshot.Records {
		byUser[record.UserEmail] = record
	}
	previous := engine.quota.Load()
	previousGeneration := ""
	if previous != nil {
		previousGeneration = previous.previousGeneration
		if previous.generation != snapshot.Generation {
			previousGeneration = previous.generation
		}
	}
	engine.quota.Store(&quotaState{
		generation:         snapshot.Generation,
		previousGeneration: previousGeneration,
		generatedAt:        snapshot.GeneratedAt,
		loadedAt:           loadedAt.Unix(),
		recordCount:        len(snapshot.Records),
		byUser:             byUser,
	})
	return nil
}

func (engine *Engine) LoadQuotaHeartbeat(reader io.Reader) error {
	heartbeat, err := ParseQuotaHeartbeat(reader)
	if err != nil {
		return err
	}
	engine.heartbeat.Store(heartbeat)
	return nil
}

func (engine *Engine) Authorize(now time.Time, authorization string, enforceQuota bool) Decision {
	nowUnix := now.Unix()
	auth := engine.auth.Load()
	if auth == nil || auth.generation == "" || auth.loadedAt <= 0 ||
		nowUnix-auth.loadedAt > int64(AuthSnapshotMaxAge/time.Second) {
		return unavailableAuthDecision("authentication_snapshot_unavailable")
	}

	externalKey, ok := ExtractBearer(authorization)
	if !ok {
		return invalidKeyDecision()
	}
	digestBytes := sha256.Sum256([]byte(externalKey))
	digest := hex.EncodeToString(digestBytes[:])
	identity, ok := auth.byDigest[digest]
	if !ok {
		return invalidKeyDecision()
	}
	decision := Decision{Allowed: true, Identity: &identity, MaxReasoningEffort: auth.maxReasoningEffort}
	if !enforceQuota {
		return decision
	}

	heartbeat := engine.heartbeat.Load()
	if heartbeat == nil || heartbeat.LastSuccessAt <= 0 ||
		nowUnix-heartbeat.LastSuccessAt > heartbeat.FailOpenAfterSeconds {
		decision.Warning = "collector_last_success"
		return decision
	}
	quota := engine.quota.Load()
	if quota == nil || quota.loadedAt <= 0 ||
		nowUnix-quota.loadedAt > heartbeat.FailOpenAfterSeconds {
		decision.Warning = "snapshot_loader"
		return decision
	}
	if quota.generation == "" {
		decision.Warning = "snapshot_missing"
		return decision
	}
	userQuota, ok := quota.byUser[identity.UserEmail]
	if !ok {
		decision.Warning = "user_record_missing"
		return decision
	}
	if nowUnix >= userQuota.WeekEndAt {
		decision.Warning = "snapshot_period_expired"
		return decision
	}
	if userQuota.LimitTokens < 0 || userQuota.UsedTokens < userQuota.LimitTokens {
		return decision
	}

	retryAfter := userQuota.WeekEndAt - nowUnix
	if retryAfter < 1 {
		retryAfter = 1
	}
	decision.Allowed = false
	decision.Status = 429
	decision.RetryAfterSeconds = retryAfter
	decision.Response = &ErrorResponse{
		Error: APIError{
			Message: "Weekly user token quota exceeded",
			Type:    "tokens",
			Code:    "weekly_user_token_quota_exceeded",
		},
		UserWeeklyQuota: &WeeklyQuota{
			UsedTokens:         userQuota.UsedTokens,
			WeightedUsedTokens: userQuota.UsedTokens,
			RawUsedTokens:      userQuota.RawUsedTokens,
			LimitTokens:        userQuota.LimitTokens,
			WeekEndAt:          userQuota.WeekEndAt,
			QuotaUnit:          "weighted_tokens",
		},
	}
	return decision
}

func unavailableAuthDecision(code string) Decision {
	return Decision{
		Status:            503,
		RetryAfterSeconds: 1,
		Response: &ErrorResponse{Error: APIError{
			Message: "API authentication is temporarily unavailable",
			Type:    "server_error",
			Code:    code,
		}},
	}
}

func invalidKeyDecision() Decision {
	return Decision{
		Status: 401,
		Response: &ErrorResponse{Error: APIError{
			Message: "Invalid API key",
			Type:    "invalid_request_error",
		}},
	}
}

// MaxReasoningEffort is read for each new response on an established WebSocket.
func (engine *Engine) MaxReasoningEffort() string {
	if state := engine.auth.Load(); state != nil {
		return state.maxReasoningEffort
	}
	return ""
}
