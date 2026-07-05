package main

import (
	"encoding/json"
	"testing"
)

type testEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
}

type testManagementRegistrationResponse struct {
	Resources []struct {
		Path        string
		Menu        string
		Description string
	} `json:"resources,omitempty"`
}

func TestManagementRegisterJSONSchema(t *testing.T) {
	raw, err := managementRegister(nil)
	if err != nil {
		t.Fatalf("managementRegister() error = %v", err)
	}
	var env testEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("envelope ok = false: %s", raw)
	}
	var resp testManagementRegistrationResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatalf("decode registration: %v; result=%s", err, env.Result)
	}
	if len(resp.Resources) != 1 {
		t.Fatalf("resources = %d, want 1; result=%s", len(resp.Resources), env.Result)
	}
	if resp.Resources[0].Path != "/state" {
		t.Fatalf("resource path = %q, want /state", resp.Resources[0].Path)
	}
}

func TestManagementStatePath(t *testing.T) {
	for _, path := range []string{"", "/state", "/state/", "/v0/resource/plugins/provider-family-quota-router/state"} {
		if !managementStatePath(path) {
			t.Fatalf("managementStatePath(%q) = false, want true", path)
		}
	}
	if managementStatePath("/v0/resource/plugins/other/state") {
		t.Fatal("other plugin path should not match")
	}
}
