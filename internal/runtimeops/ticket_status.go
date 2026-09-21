package runtimeops

import (
	"context"
	"strings"
	"time"
)

// Public status deliberately excludes credentials, opaque tickets and upstream
// error text. Reading it never triggers harvesting or a model request.
type TicketRuntimeStatus struct {
	State         string        `json:"state"`
	CheckedAt     int64         `json:"checked_at"`
	Registered    bool          `json:"registered"`
	Version       string        `json:"version"`
	HarvestActive bool          `json:"harvest_active"`
	InjectActive  bool          `json:"inject_active"`
	HarvestReason string        `json:"harvest_reason"`
	InjectReason  string        `json:"inject_reason"`
	CachedCount   int           `json:"cached_count"`
	InflightCount int           `json:"inflight_count"`
	Entries       []TicketEntry `json:"entries"`
}

type TicketEntry struct {
	Model          string `json:"model"`
	LastHTTP       int    `json:"last_http"`
	LastLength     int    `json:"last_length"`
	Reason         string `json:"reason"`
	BackoffSeconds int    `json:"backoff_seconds"`
	InjectedCount  int64  `json:"injected_count"`
}

type PluginJobStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func ticketReason(value string) string {
	switch value {
	case "", "ready", "disabled", "length_mismatch", "no_auth", "no_proxy", "proxy_missing", "proxy_invalid", "timeout", "backoff", "cached", "refreshing":
		return value
	default:
		return "other"
	}
}

// TicketStatus requires a row from the current catalog. Only the account detail
// queries native endpoints, so the main account list does not fan out across CPAs.
func (e *Extensions) TicketStatus(ctx context.Context, row PluginAccountStatus) TicketRuntimeStatus {
	s := TicketRuntimeStatus{State: "unavailable", CheckedAt: time.Now().Unix(), Entries: []TicketEntry{}}
	if !row.Running {
		s.State = "stopped"
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var plugins struct {
		Plugins []struct {
			ID         string `json:"id"`
			Registered bool   `json:"registered"`
			Metadata   struct {
				Version string `json:"version"`
			} `json:"metadata"`
		} `json:"plugins"`
	}
	if e.management(ctx, row.Account, "/plugins", &plugins) != nil {
		return s
	}
	for _, p := range plugins.Plugins {
		if p.ID == "codex-ticket" && p.Registered {
			s.Registered = true
			if len(p.Metadata.Version) < 64 {
				s.Version = strings.TrimSpace(p.Metadata.Version)
			}
		}
	}
	if !s.Registered {
		s.State = "not_loaded"
		return s
	}
	var native TicketRuntimeStatus
	if e.management(ctx, row.Account, "/codex-ticket/status", &native) != nil {
		return s
	}
	s.State = "ready"
	s.HarvestActive, s.InjectActive = native.HarvestActive, native.InjectActive
	s.HarvestReason, s.InjectReason = ticketReason(native.HarvestReason), ticketReason(native.InjectReason)
	s.CachedCount, s.InflightCount = native.CachedCount, native.InflightCount
	for _, entry := range native.Entries {
		if len(s.Entries) >= 64 {
			break
		}
		if len(entry.Model) > 128 {
			continue
		}
		entry.Reason = ticketReason(entry.Reason)
		s.Entries = append(s.Entries, entry)
	}
	return s
}
