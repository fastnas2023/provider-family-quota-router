package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testRegistrationEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
}

type testRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  struct {
		Scheduler             bool                         `json:"scheduler"`
		ModelRouter           bool                         `json:"model_router"`
		Executor              bool                         `json:"executor"`
		ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
		ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
		ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
		ManagementAPI         bool                         `json:"management_api"`
	} `json:"capabilities"`
}

func TestPluginRegistrationMatchesHostSchema(t *testing.T) {
	raw, err := okEnvelope(pluginRegistration())
	if err != nil {
		t.Fatalf("okEnvelope(pluginRegistration()) error = %v", err)
	}
	var env testRegistrationEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var reg testRegistration
	if err := json.Unmarshal(env.Result, &reg); err != nil {
		t.Fatalf("decode registration: %v; result=%s", err, env.Result)
	}
	if reg.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema version = %d, want %d", reg.SchemaVersion, pluginabi.SchemaVersion)
	}
	if !reg.Capabilities.Scheduler || !reg.Capabilities.ModelRouter || !reg.Capabilities.Executor || !reg.Capabilities.ManagementAPI {
		t.Fatalf("missing expected capabilities: %+v", reg.Capabilities)
	}
	if reg.Capabilities.ExecutorModelScope != pluginapi.ExecutorModelScopeStatic {
		t.Fatalf("executor scope = %q, want static", reg.Capabilities.ExecutorModelScope)
	}
}

func TestExecutorHTTPRequestReturnsExplicitUnsupported(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodExecutorHTTPRequest, []byte(`{}`))
	if err != nil {
		t.Fatalf("handleMethod executor.http_request error = %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "http_request_unsupported" {
		t.Fatalf("unexpected envelope: %s", raw)
	}
}
