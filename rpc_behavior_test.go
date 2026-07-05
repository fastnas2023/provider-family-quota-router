package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func configureForTest(t *testing.T) {
	t.Helper()
	state = newQuotaState()
	cfg := defaultConfig()
	cfg.Enabled = true
	currentConfig.Store(cfg)
}

func decodeOKResult[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v; raw=%s", err, raw)
	}
	if !env.OK {
		t.Fatalf("envelope ok=false: %s", raw)
	}
	var out T
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("decode result: %v; result=%s", err, env.Result)
	}
	return out
}

func TestRPCModelRouteOnlyHandlesConfiguredAntigravityAliases(t *testing.T) {
	configureForTest(t)

	raw, err := handleMethod(pluginabi.MethodModelRoute, mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:   "claude",
			RequestedModel: "claude-sonnet-4-6-antigravity",
		},
	}))
	if err != nil {
		t.Fatalf("route handled model error = %v", err)
	}
	handled := decodeOKResult[pluginapi.ModelRouteResponse](t, raw)
	if !handled.Handled || handled.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("route response = %+v, want self handled", handled)
	}

	raw, err = handleMethod(pluginabi.MethodModelRoute, mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:   "gemini",
			RequestedModel: "gemini-3-flash-antigravity",
		},
	}))
	if err != nil {
		t.Fatalf("route native gemini error = %v", err)
	}
	unhandled := decodeOKResult[pluginapi.ModelRouteResponse](t, raw)
	if unhandled.Handled {
		t.Fatalf("native gemini route should not be handled: %+v", unhandled)
	}
}

func TestRPCSchedulerSkipsOnlyBlockedFamilyAndRecordsInflight(t *testing.T) {
	configureForTest(t)
	cfg := loadedConfig()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	state.markFailure("antigravity", "auth-a", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusTooManyRequests, false, nil, cfg, now)

	raw, err := handleMethod(pluginabi.MethodSchedulerPick, mustJSON(t, pluginapi.SchedulerPickRequest{
		Provider: "antigravity",
		Model:    "claude-sonnet-4-6-antigravity",
		Options:  pluginapi.SchedulerOptions{Headers: map[string][]string{runIDHeader: []string{"run-1"}}},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "antigravity"},
			{ID: "auth-b", Provider: "antigravity"},
		},
	}))
	if err != nil {
		t.Fatalf("scheduler pick error = %v", err)
	}
	resp := decodeOKResult[pluginapi.SchedulerPickResponse](t, raw)
	if !resp.Handled || resp.AuthID != "auth-b" {
		t.Fatalf("scheduler response = %+v, want auth-b", resp)
	}
	rec, ok := state.popInflight("run-1")
	if !ok || rec.AuthID != "auth-b" || rec.Family != familyClaude {
		t.Fatalf("inflight = %+v ok=%v, want auth-b claude", rec, ok)
	}

	raw, err = handleMethod(pluginabi.MethodSchedulerPick, mustJSON(t, pluginapi.SchedulerPickRequest{
		Provider: "antigravity",
		Model:    "gemini-3-flash-antigravity",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "antigravity"},
			{ID: "auth-b", Provider: "antigravity"},
		},
	}))
	if err != nil {
		t.Fatalf("gemini scheduler pick error = %v", err)
	}
	geminiResp := decodeOKResult[pluginapi.SchedulerPickResponse](t, raw)
	if !geminiResp.Handled || geminiResp.AuthID != "auth-a" {
		t.Fatalf("gemini scheduler response = %+v, want auth-a because claude block must not apply", geminiResp)
	}
}

func TestRPCSchedulerAllBlockedReturnsExplicitError(t *testing.T) {
	configureForTest(t)
	cfg := loadedConfig()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	state.markFailure("antigravity", "auth-a", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusTooManyRequests, false, nil, cfg, now)
	state.markFailure("antigravity", "auth-b", familyClaude, "claude-sonnet-4-6-antigravity", http.StatusTooManyRequests, false, nil, cfg, now)

	raw, err := handleMethod(pluginabi.MethodSchedulerPick, mustJSON(t, pluginapi.SchedulerPickRequest{
		Provider: "antigravity",
		Model:    "claude-sonnet-4-6-antigravity",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-a", Provider: "antigravity"},
			{ID: "auth-b", Provider: "antigravity"},
		},
	}))
	if err != nil {
		t.Fatalf("scheduler pick all-blocked error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "provider_family_cooldown" {
		t.Fatalf("all-blocked envelope = %s, want provider_family_cooldown", raw)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
