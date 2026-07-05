package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	familyClaude = "claude"
	familyGemini = "gemini"
	familyGPTOSS = "gpt_oss"
	familyOther  = "other"

	reasonQuota      = "quota"
	reasonPermission = "permission"
	reasonTransient  = "transient"
	reasonTimeout    = "timeout"
)

type stateKey struct {
	Provider string `json:"provider"`
	AuthID   string `json:"auth_id"`
	Family   string `json:"family"`
}

type cooldownRecord struct {
	Until        time.Time `json:"until"`
	Reason       string    `json:"reason"`
	StatusCode   int       `json:"status_code,omitempty"`
	FailureCount int       `json:"failure_count"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastModel    string    `json:"last_model,omitempty"`
}

type inflightRecord struct {
	Provider string
	AuthID   string
	Family   string
	Model    string
	At       time.Time
}

type quotaState struct {
	mu       sync.Mutex
	blocks   map[stateKey]cooldownRecord
	rr       map[string]int
	inflight map[string]inflightRecord
}

func newQuotaState() *quotaState {
	return &quotaState{
		blocks:   make(map[stateKey]cooldownRecord),
		rr:       make(map[string]int),
		inflight: make(map[string]inflightRecord),
	}
}

func normalizeProvider(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeAuthID(v string) string {
	return strings.TrimSpace(v)
}

func modelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimSuffix(m, "-antigravity")
	m = strings.ReplaceAll(m, "_", "-")
	switch {
	case strings.HasPrefix(m, "claude-"), m == "claude":
		return familyClaude
	case strings.HasPrefix(m, "gemini-"), m == "gemini":
		return familyGemini
	case strings.HasPrefix(m, "gpt-oss-"), strings.HasPrefix(m, "gpt_oss_"), m == "gpt-oss", m == "gpt_oss":
		return familyGPTOSS
	default:
		return familyOther
	}
}

func (s *quotaState) rememberInflight(runID string, rec inflightRecord) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight[runID] = rec
}

func (s *quotaState) popInflight(runID string) (inflightRecord, bool) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return inflightRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.inflight[runID]
	if ok {
		delete(s.inflight, runID)
	}
	return rec, ok
}

func (s *quotaState) markSuccess(provider, authID, family string) {
	key := stateKey{Provider: normalizeProvider(provider), AuthID: normalizeAuthID(authID), Family: family}
	if key.Provider == "" || key.AuthID == "" || key.Family == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blocks, key)
}

func (s *quotaState) markFailure(provider, authID, family, model string, status int, timeout bool, headers http.Header, cfg pluginConfig, now time.Time) cooldownRecord {
	key := stateKey{Provider: normalizeProvider(provider), AuthID: normalizeAuthID(authID), Family: family}
	if key.Provider == "" || key.AuthID == "" || key.Family == "" {
		return cooldownRecord{}
	}
	reason := failureReason(status, timeout)
	dur := cooldownDuration(reason, headers, cfg)
	if dur <= 0 {
		return cooldownRecord{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.blocks[key]
	rec := cooldownRecord{
		Until:        now.Add(dur),
		Reason:       reason,
		StatusCode:   status,
		FailureCount: prev.FailureCount + 1,
		UpdatedAt:    now,
		LastModel:    strings.TrimSpace(model),
	}
	s.blocks[key] = rec
	return rec
}

func failureReason(status int, timeout bool) string {
	if timeout {
		return reasonTimeout
	}
	switch status {
	case http.StatusTooManyRequests:
		return reasonQuota
	case http.StatusForbidden, http.StatusUnauthorized:
		return reasonPermission
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return reasonTransient
	default:
		if status >= 500 {
			return reasonTransient
		}
		return ""
	}
}

func cooldownDuration(reason string, headers http.Header, cfg pluginConfig) time.Duration {
	switch reason {
	case reasonQuota:
		if d := retryAfterDuration(headers, cfg.MaxRetryAfterSeconds); d > 0 {
			return d
		}
		return time.Duration(cfg.QuotaCooldownSeconds) * time.Second
	case reasonPermission:
		return time.Duration(cfg.PermissionCooldownSeconds) * time.Second
	case reasonTransient, reasonTimeout:
		return time.Duration(cfg.TransientCooldownSeconds) * time.Second
	default:
		return 0
	}
}

func retryAfterDuration(headers http.Header, capSeconds int) time.Duration {
	if headers == nil {
		return 0
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		if capSeconds > 0 && n > capSeconds {
			n = capSeconds
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0
		}
		if capSeconds > 0 && d > time.Duration(capSeconds)*time.Second {
			return time.Duration(capSeconds) * time.Second
		}
		return d
	}
	return 0
}

func (s *quotaState) isBlocked(provider, authID, family string, now time.Time) (cooldownRecord, bool) {
	key := stateKey{Provider: normalizeProvider(provider), AuthID: normalizeAuthID(authID), Family: family}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.blocks[key]
	if !ok {
		return cooldownRecord{}, false
	}
	if !rec.Until.After(now) {
		delete(s.blocks, key)
		return cooldownRecord{}, false
	}
	return rec, true
}

func (s *quotaState) pickCandidate(provider, model string, candidates []pluginapi.SchedulerAuthCandidate, cfg pluginConfig, now time.Time) (pluginapi.SchedulerAuthCandidate, bool) {
	provider = normalizeProvider(provider)
	family := modelFamily(model)
	if !cfg.familyAllowed(family) {
		return pluginapi.SchedulerAuthCandidate{}, false
	}
	var allowed []pluginapi.SchedulerAuthCandidate
	for _, candidate := range candidates {
		if normalizeProvider(candidate.Provider) != provider {
			continue
		}
		if _, blocked := s.isBlocked(provider, candidate.ID, family, now); blocked {
			continue
		}
		allowed = append(allowed, candidate)
	}
	if len(allowed) == 0 {
		return pluginapi.SchedulerAuthCandidate{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := provider + ":" + family
	idx := s.rr[key] % len(allowed)
	s.rr[key] = (s.rr[key] + 1) % len(allowed)
	return allowed[idx], true
}

func (s *quotaState) allProviderFamilyCandidatesBlocked(provider, model string, candidates []pluginapi.SchedulerAuthCandidate, cfg pluginConfig, now time.Time) bool {
	provider = normalizeProvider(provider)
	family := modelFamily(model)
	if !cfg.familyAllowed(family) {
		return false
	}
	seenProviderCandidate := false
	for _, candidate := range candidates {
		if normalizeProvider(candidate.Provider) != provider {
			continue
		}
		seenProviderCandidate = true
		if _, blocked := s.isBlocked(provider, candidate.ID, family, now); !blocked {
			return false
		}
	}
	return seenProviderCandidate
}

func (s *quotaState) snapshot(now time.Time) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]any, 0, len(s.blocks))
	for key, rec := range s.blocks {
		if !rec.Until.After(now) {
			delete(s.blocks, key)
			continue
		}
		out = append(out, map[string]any{
			"provider":      key.Provider,
			"auth_id":       key.AuthID,
			"family":        key.Family,
			"until":         rec.Until.Format(time.RFC3339),
			"reason":        rec.Reason,
			"status_code":   rec.StatusCode,
			"failure_count": rec.FailureCount,
			"last_model":    rec.LastModel,
			"updated_at":    rec.UpdatedAt.Format(time.RFC3339),
		})
	}
	return out
}
