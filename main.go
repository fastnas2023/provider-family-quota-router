package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var state = newQuotaState()

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	Scheduler             bool                         `json:"scheduler"`
	ModelRouter           bool                         `json:"model_router"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
	ManagementAPI         bool                         `json:"management_api"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
	StreamID       string `json:"stream_id,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodSchedulerPick:
		return pickAuth(request)
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginName})
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return countTokens(request)
	case pluginabi.MethodExecutorHTTPRequest:
		return httpRequest(request)
	case pluginabi.MethodManagementRegister:
		return managementRegister(request)
	case pluginabi.MethodManagementHandle:
		return managementHandle(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg, err := decodeConfig(req.ConfigYAML)
	if err != nil {
		return err
	}
	currentConfig.Store(cfg)
	return nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          "0.1.0",
			Author:           "Jason Zahng QQ:350400138",
			GitHubRepository: "https://github.com/fastnas2023/provider-family-quota-router",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable passive provider-family quota routing."},
				{Name: "providers", Type: pluginapi.ConfigFieldTypeArray, Description: "Provider keys to handle. Default: antigravity."},
				{Name: "families", Type: pluginapi.ConfigFieldTypeArray, Description: "Model families to track: claude, gemini, gpt_oss, other."},
				{Name: "model_suffixes", Type: pluginapi.ConfigFieldTypeArray, Description: "Model alias suffixes handled by the router. Default: -antigravity."},
				{Name: "max_attempts", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum host model attempts before returning failure."},
				{Name: "attempt_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Per-attempt host callback timeout."},
				{Name: "stream_read_timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Per-read timeout while forwarding host model streams."},
				{Name: "quota_cooldown_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Default 429 cooldown when Retry-After is absent."},
				{Name: "permission_cooldown_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Cooldown for auth or permission-like failures."},
				{Name: "transient_cooldown_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Cooldown for timeout and 5xx-like failures."},
				{Name: "max_retry_after_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum Retry-After duration accepted from upstream."},
				{Name: "delegate_when_no_block", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"", pluginapi.SchedulerBuiltinRoundRobin, pluginapi.SchedulerBuiltinFillFirst}, Description: "Reserved scheduler fallback strategy when the plugin declines a pick."},
				{Name: "fail_when_all_blocked", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Return a scheduler error when all provider-family candidates are cooling down."},
			},
		},
		Capabilities: registrationCapabilities{
			Scheduler:             true,
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats:  []string{"openai", "claude"},
			ExecutorOutputFormats: []string{"openai", "claude"},
			ManagementAPI:         true,
		},
	}
}

func routeModel(raw []byte) ([]byte, error) {
	var req rpcModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !sourceFormatSupported(req.SourceFormat) || !cfg.modelMatches(req.RequestedModel) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "provider_family_quota_router",
	})
}

func pickAuth(raw []byte) ([]byte, error) {
	var req pluginapi.SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	cfg := loadedConfig()
	if !cfg.Enabled {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	provider := normalizeProvider(req.Provider)
	if provider == "" && len(req.Providers) == 1 {
		provider = normalizeProvider(req.Providers[0])
	}
	if !cfg.providerAllowed(provider) {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	family := modelFamily(req.Model)
	if !cfg.familyAllowed(family) {
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	selected, ok := state.pickCandidate(provider, req.Model, req.Candidates, cfg, time.Now())
	if !ok {
		if cfg.FailWhenAllBlocked && state.allProviderFamilyCandidatesBlocked(provider, req.Model, req.Candidates, cfg, time.Now()) {
			return errorEnvelope("provider_family_cooldown", "all candidates are cooling down for provider="+provider+" family="+family), nil
		}
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: false})
	}
	if runID := headerValue(req.Options.Headers, runIDHeader); runID != "" {
		state.rememberInflight(runID, inflightRecord{
			Provider: provider,
			AuthID:   selected.ID,
			Family:   family,
			Model:    req.Model,
			At:       time.Now(),
		})
	}
	return okEnvelope(pluginapi.SchedulerPickResponse{Handled: true, AuthID: selected.ID})
}

func execute(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	resp, err := runWithFailover(req, false)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: resp.Body, Headers: resp.Headers, Metadata: map[string]any{"status_code": resp.StatusCode}})
}

func executeStream(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		return errorEnvelope("executor_error", "stream_id is required"), nil
	}
	go func() {
		err := runStreamWithFailover(req, streamID)
		if err != nil {
			closePluginStream(streamID, err.Error())
			return
		}
		closePluginStream(streamID, "")
	}()
	return okEnvelope(map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func countTokens(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return errorEnvelope("count_tokens_unsupported", "provider-family-quota-router does not call upstream for token counting"), nil
}

func httpRequest(raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorHTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	return errorEnvelope("http_request_unsupported", "provider-family-quota-router only handles model execution and scheduling"), nil
}

func headerValue(headers map[string][]string, key string) string {
	for k, values := range headers {
		if strings.EqualFold(k, key) && len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, err)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}
	var env envelope
	if err := json.Unmarshal(rawResponse, &env); err != nil {
		return nil, fmt.Errorf("decode host envelope %s: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}
