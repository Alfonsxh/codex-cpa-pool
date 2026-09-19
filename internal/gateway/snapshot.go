package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-pool/internal/reasoningpolicy"
)

const MaxSnapshotBytes = 16 * 1024 * 1024

var accountNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)
var backendPattern = regexp.MustCompile(`^cliproxy-[a-z][a-z0-9-]+:8317$`)

type AuthRecord struct {
	ExternalKeySHA256 string `json:"external_key_sha256"`
	UserEmail         string `json:"user_email"`
	Account           string `json:"account"`
	Backend           string `json:"backend"`
	InternalKey       string `json:"internal_key"`
	Label             string `json:"label"`
}

type AuthSnapshot struct {
	Version            int          `json:"version"`
	Generation         string       `json:"generation"`
	GeneratedAt        float64      `json:"generated_at"`
	Records            []AuthRecord `json:"records"`
	MaxReasoningEffort string       `json:"max_reasoning_effort,omitempty"`
}

type QuotaRecord struct {
	UserEmail             string `json:"user_email"`
	WeekStartAt           int64  `json:"week_start_at"`
	WeekEndAt             int64  `json:"week_end_at"`
	LimitTokens           int64  `json:"limit_tokens"`
	UsedTokens            int64  `json:"used_tokens"`
	RawUsedTokens         int64  `json:"raw_used_tokens"`
	WeightedRawUsedTokens int64  `json:"weighted_raw_used_tokens"`
	QuotaUnit             string `json:"quota_unit"`
}

type QuotaSnapshot struct {
	Version       int           `json:"version"`
	Generation    string        `json:"generation"`
	GeneratedAt   float64       `json:"generated_at"`
	ContentSHA256 string        `json:"content_sha256,omitempty"`
	Records       []QuotaRecord `json:"records"`
}

type QuotaHeartbeat struct {
	Version              int
	UpdatedAt            int64
	OK                   bool
	Error                string
	StaleAfterSeconds    int64
	LastSuccessAt        int64
	FailOpenAfterSeconds int64
}

type rawQuotaRecord struct {
	UserEmail             string   `json:"user_email"`
	WeekStartAt           *float64 `json:"week_start_at"`
	WeekEndAt             *float64 `json:"week_end_at"`
	LimitTokens           *float64 `json:"limit_tokens"`
	UsedTokens            *float64 `json:"used_tokens"`
	RawUsedTokens         any      `json:"raw_used_tokens"`
	WeightedRawUsedTokens any      `json:"weighted_raw_used_tokens"`
}

type rawAuthSnapshot struct {
	Version            int          `json:"version"`
	Generation         string       `json:"generation"`
	GeneratedAt        any          `json:"generated_at"`
	Records            []AuthRecord `json:"records"`
	MaxReasoningEffort string       `json:"max_reasoning_effort,omitempty"`
}

type rawQuotaSnapshot struct {
	Version       int              `json:"version"`
	Generation    string           `json:"generation"`
	GeneratedAt   any              `json:"generated_at"`
	ContentSHA256 string           `json:"content_sha256"`
	Records       []rawQuotaRecord `json:"records"`
}

type rawQuotaHeartbeat struct {
	Version              int      `json:"version"`
	UpdatedAt            *float64 `json:"updated_at"`
	OK                   *bool    `json:"ok"`
	Error                string   `json:"error"`
	StaleAfterSeconds    any      `json:"stale_after_seconds"`
	LastSuccessAt        *float64 `json:"last_success_at"`
	FailOpenAfterSeconds *float64 `json:"fail_open_after_seconds"`
}

func ParseAuthSnapshot(reader io.Reader) (*AuthSnapshot, error) {
	raw, err := readSnapshot(reader)
	if err != nil {
		return nil, err
	}
	var input rawAuthSnapshot
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode auth snapshot: %w", err)
	}
	if input.Version != 1 || !validLowerHex(input.Generation, 32) || input.Records == nil || !reasoningpolicy.ValidLimit(input.MaxReasoningEffort) {
		return nil, errors.New("invalid auth snapshot envelope")
	}
	snapshot := &AuthSnapshot{
		Version:            input.Version,
		Generation:         input.Generation,
		GeneratedAt:        snapshotNumberOrFallback(input.GeneratedAt, 0),
		Records:            input.Records,
		MaxReasoningEffort: input.MaxReasoningEffort,
	}
	seen := make(map[string]struct{}, len(snapshot.Records))
	for index, record := range snapshot.Records {
		_, duplicate := seen[record.ExternalKeySHA256]
		valid := validLowerHex(record.ExternalKeySHA256, 64) &&
			record.UserEmail != "" &&
			accountNamePattern.MatchString(record.Account) &&
			backendPattern.MatchString(record.Backend) &&
			record.InternalKey != "" &&
			record.Label != "" &&
			!duplicate
		if !valid {
			return nil, fmt.Errorf("invalid auth snapshot record at index %d", index)
		}
		seen[record.ExternalKeySHA256] = struct{}{}
	}
	return snapshot, nil
}

func ParseQuotaSnapshot(reader io.Reader) (*QuotaSnapshot, error) {
	raw, err := readSnapshot(reader)
	if err != nil {
		return nil, err
	}
	var input rawQuotaSnapshot
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode quota snapshot: %w", err)
	}
	if input.Version != 1 || !validLowerHex(input.Generation, 32) || input.Records == nil {
		return nil, errors.New("invalid quota snapshot envelope")
	}
	snapshot := &QuotaSnapshot{
		Version:       input.Version,
		Generation:    input.Generation,
		GeneratedAt:   snapshotNumberOrFallback(input.GeneratedAt, 0),
		ContentSHA256: input.ContentSHA256,
		Records:       make([]QuotaRecord, 0, len(input.Records)),
	}
	seen := make(map[string]struct{}, len(input.Records))
	for index, rawRecord := range input.Records {
		if rawRecord.UserEmail == "" || rawRecord.WeekStartAt == nil ||
			rawRecord.WeekEndAt == nil || rawRecord.LimitTokens == nil ||
			rawRecord.UsedTokens == nil {
			return nil, fmt.Errorf("invalid quota snapshot record at index %d", index)
		}
		_, duplicate := seen[rawRecord.UserEmail]
		if duplicate || !finite(*rawRecord.WeekStartAt) || !finite(*rawRecord.WeekEndAt) ||
			!finite(*rawRecord.LimitTokens) || !finite(*rawRecord.UsedTokens) ||
			*rawRecord.WeekEndAt <= *rawRecord.WeekStartAt ||
			*rawRecord.LimitTokens < -1 || *rawRecord.UsedTokens < 0 {
			return nil, fmt.Errorf("invalid quota snapshot record at index %d", index)
		}
		weekStart, err := floorInt64(*rawRecord.WeekStartAt)
		if err != nil {
			return nil, fmt.Errorf("invalid quota week_start_at at index %d: %w", index, err)
		}
		weekEnd, err := floorInt64(*rawRecord.WeekEndAt)
		if err != nil {
			return nil, fmt.Errorf("invalid quota week_end_at at index %d: %w", index, err)
		}
		limit, err := floorInt64(*rawRecord.LimitTokens)
		if err != nil {
			return nil, fmt.Errorf("invalid quota limit_tokens at index %d: %w", index, err)
		}
		used, err := floorInt64(*rawRecord.UsedTokens)
		if err != nil {
			return nil, fmt.Errorf("invalid quota used_tokens at index %d: %w", index, err)
		}
		rawUsed, err := optionalSnapshotFloor(rawRecord.RawUsedTokens, used)
		if err != nil {
			return nil, fmt.Errorf("invalid quota raw_used_tokens at index %d: %w", index, err)
		}
		weightedRawUsed, err := optionalSnapshotFloor(rawRecord.WeightedRawUsedTokens, used)
		if err != nil {
			return nil, fmt.Errorf("invalid quota weighted_raw_used_tokens at index %d: %w", index, err)
		}
		snapshot.Records = append(snapshot.Records, QuotaRecord{
			UserEmail:             rawRecord.UserEmail,
			WeekStartAt:           weekStart,
			WeekEndAt:             weekEnd,
			LimitTokens:           limit,
			UsedTokens:            used,
			RawUsedTokens:         rawUsed,
			WeightedRawUsedTokens: weightedRawUsed,
			QuotaUnit:             "weighted_tokens",
		})
		seen[rawRecord.UserEmail] = struct{}{}
	}
	return snapshot, nil
}

func ParseQuotaHeartbeat(reader io.Reader) (*QuotaHeartbeat, error) {
	raw, err := readSnapshot(reader)
	if err != nil {
		return nil, err
	}
	var input rawQuotaHeartbeat
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode quota heartbeat: %w", err)
	}
	if input.Version != 1 || input.UpdatedAt == nil || input.OK == nil ||
		input.LastSuccessAt == nil || input.FailOpenAfterSeconds == nil {
		return nil, errors.New("invalid quota heartbeat")
	}
	updatedAt, err := floorInt64(*input.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("invalid quota heartbeat updated_at: %w", err)
	}
	lastSuccessAt, err := floorInt64(*input.LastSuccessAt)
	if err != nil {
		return nil, fmt.Errorf("invalid quota heartbeat last_success_at: %w", err)
	}
	failOpenAfter, err := floorInt64(*input.FailOpenAfterSeconds)
	if err != nil {
		return nil, fmt.Errorf("invalid quota heartbeat fail_open_after_seconds: %w", err)
	}
	staleAfter := int64(15)
	if parsed, ok := snapshotNumber(input.StaleAfterSeconds); ok {
		staleAfter, err = floorInt64(parsed)
		if err != nil {
			return nil, fmt.Errorf("invalid quota heartbeat stale_after_seconds: %w", err)
		}
	}
	return &QuotaHeartbeat{
		Version:              input.Version,
		UpdatedAt:            updatedAt,
		OK:                   *input.OK,
		Error:                input.Error,
		StaleAfterSeconds:    max(5, staleAfter),
		LastSuccessAt:        max(0, lastSuccessAt),
		FailOpenAfterSeconds: max(30, failOpenAfter),
	}, nil
}

func readSnapshot(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("snapshot reader is nil")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, MaxSnapshotBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	if len(raw) == 0 || len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty snapshot")
	}
	if len(raw) > MaxSnapshotBytes {
		return nil, errors.New("snapshot too large")
	}
	return raw, nil
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func floorInt64(value float64) (int64, error) {
	const minInt64Value = -float64(uint64(1) << 63)
	const maxInt64Exclusive = float64(uint64(1) << 63)
	if !finite(value) || value < minInt64Value || value >= maxInt64Exclusive {
		return 0, errors.New("number is outside int64 range")
	}
	return int64(math.Floor(value)), nil
}

func optionalSnapshotFloor(value any, fallback int64) (int64, error) {
	parsed, ok := snapshotNumber(value)
	if !ok {
		return fallback, nil
	}
	return floorInt64(parsed)
}

func snapshotNumberOrFallback(value any, fallback float64) float64 {
	parsed, ok := snapshotNumber(value)
	if !ok {
		return fallback
	}
	return parsed
}

func snapshotNumber(value any) (float64, bool) {
	var parsed float64
	switch candidate := value.(type) {
	case float64:
		parsed = candidate
	case string:
		value, err := strconv.ParseFloat(strings.TrimSpace(candidate), 64)
		if err != nil {
			return 0, false
		}
		parsed = value
	default:
		return 0, false
	}
	if !finite(parsed) {
		return 0, false
	}
	return parsed, true
}
