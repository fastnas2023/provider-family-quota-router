package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestModelFamily(t *testing.T) {
	tests := map[string]string{
		"claude-sonnet-4-6-antigravity": familyClaude,
		"claude-opus-4-6-thinking":      familyClaude,
		"gemini-3-flash-antigravity":    familyGemini,
		"gemini-2.5-pro":                familyGemini,
		"gpt-oss-120b-medium":           familyGPTOSS,
		"unknown-model-antigravity":     familyOther,
	}
	for model, want := range tests {
		if got := modelFamily(model); got != want {
			t.Fatalf("modelFamily(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestFamilyCooldownDoesNotDisableOtherFamilies(t *testing.T) {
	s := newQuotaState()
	cfg := defaultConfig()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	s.markFailure("antigravity", "auth-a", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusTooManyRequests, false, nil, cfg, now)

	if _, blocked := s.isBlocked("antigravity", "auth-a", familyClaude, now.Add(time.Minute)); !blocked {
		t.Fatal("claude family should be blocked")
	}
	if _, blocked := s.isBlocked("antigravity", "auth-a", familyGemini, now.Add(time.Minute)); blocked {
		t.Fatal("gemini family should not be blocked by claude failure")
	}
}

func TestSchedulerSkipsBlockedFamilyCandidate(t *testing.T) {
	s := newQuotaState()
	cfg := defaultConfig()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: "auth-a", Provider: "antigravity"},
		{ID: "auth-b", Provider: "antigravity"},
	}
	s.markFailure("antigravity", "auth-a", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusGatewayTimeout, true, nil, cfg, now)

	got, ok := s.pickCandidate("antigravity", "claude-sonnet-4-6-antigravity", candidates, cfg, now.Add(time.Minute))
	if !ok {
		t.Fatal("expected a candidate")
	}
	if got.ID != "auth-b" {
		t.Fatalf("picked %q, want auth-b", got.ID)
	}

	got, ok = s.pickCandidate("antigravity", "gemini-3-flash-antigravity", candidates, cfg, now.Add(time.Minute))
	if !ok {
		t.Fatal("expected a gemini candidate")
	}
	if got.ID != "auth-a" {
		t.Fatalf("picked %q for gemini, want auth-a because claude block must not apply", got.ID)
	}
}

func TestRetryAfterCooldown(t *testing.T) {
	s := newQuotaState()
	cfg := defaultConfig()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	headers := http.Header{"Retry-After": []string{"120"}}

	rec := s.markFailure("antigravity", "auth-a", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusTooManyRequests, false, headers, cfg, now)
	if got := rec.Until.Sub(now); got != 120*time.Second {
		t.Fatalf("cooldown = %s, want 120s", got)
	}
}

func TestSuccessClearsOnlyMatchingFamilyCooldown(t *testing.T) {
	s := newQuotaState()
	cfg := defaultConfig()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)

	s.markFailure("antigravity", "auth-a", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusTooManyRequests, false, nil, cfg, now)
	s.markFailure("antigravity", "auth-a", familyGemini, "gemini-3-flash-antigravity", http.StatusTooManyRequests, false, nil, cfg, now)
	s.markSuccess("antigravity", "auth-a", familyClaude)

	if _, blocked := s.isBlocked("antigravity", "auth-a", familyClaude, now.Add(time.Minute)); blocked {
		t.Fatal("claude cooldown should be cleared")
	}
	if _, blocked := s.isBlocked("antigravity", "auth-a", familyGemini, now.Add(time.Minute)); !blocked {
		t.Fatal("gemini cooldown should remain")
	}
}

func TestAllProviderFamilyCandidatesBlocked(t *testing.T) {
	s := newQuotaState()
	cfg := defaultConfig()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	candidates := []pluginapi.SchedulerAuthCandidate{
		{ID: "auth-a", Provider: "antigravity"},
		{ID: "auth-b", Provider: "antigravity"},
	}

	s.markFailure("antigravity", "auth-a", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusGatewayTimeout, true, nil, cfg, now)
	if s.allProviderFamilyCandidatesBlocked("antigravity", "claude-sonnet-4-6-antigravity", candidates, cfg, now.Add(time.Minute)) {
		t.Fatal("not all candidates are blocked yet")
	}

	s.markFailure("antigravity", "auth-b", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusTooManyRequests, false, nil, cfg, now)
	if !s.allProviderFamilyCandidatesBlocked("antigravity", "claude-sonnet-4-6-antigravity", candidates, cfg, now.Add(time.Minute)) {
		t.Fatal("all claude candidates should be blocked")
	}
	if s.allProviderFamilyCandidatesBlocked("antigravity", "gemini-3-flash-antigravity", candidates, cfg, now.Add(time.Minute)) {
		t.Fatal("gemini candidates must not be considered blocked by claude cooldowns")
	}
}

func TestSourceFormatSupported(t *testing.T) {
	for _, format := range []string{"", "openai", "claude", "OpenAI"} {
		if !sourceFormatSupported(format) {
			t.Fatalf("source format %q should be supported", format)
		}
	}
	if sourceFormatSupported("gemini") {
		t.Fatal("native gemini source format should not be routed by this plugin")
	}
}
