package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type hostExecResult struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
	Timeout    bool
	Err        error
}

func runWithFailover(req rpcExecutorRequest, stream bool) (hostExecResult, error) {
	cfg := loadedConfig()
	if !cfg.Enabled {
		return hostExecResult{}, fmt.Errorf("plugin disabled")
	}
	var last hostExecResult
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		runID := newRunID(req.Model, attempt)
		result := hostModelExecuteWithTimeout(req, runID, stream, time.Duration(cfg.AttemptTimeoutSeconds)*time.Second)
		last = result
		rec, ok := state.popInflight(runID)
		if ok && result.StatusCode >= 200 && result.StatusCode < 300 && !result.Timeout && result.Err == nil {
			state.markSuccess(rec.Provider, rec.AuthID, rec.Family)
			return result, nil
		}
		if ok {
			state.markFailure(rec.Provider, rec.AuthID, rec.Family, rec.Model, result.StatusCode, result.Timeout, result.Headers, cfg, time.Now())
		}
		if !retryableResult(result) {
			if result.Err != nil {
				return result, result.Err
			}
			return result, nil
		}
	}
	if last.Err != nil {
		return last, last.Err
	}
	return last, fmt.Errorf("all provider-family attempts failed, last status=%d", last.StatusCode)
}

func retryableResult(result hostExecResult) bool {
	if result.Timeout {
		return true
	}
	switch result.StatusCode {
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return result.StatusCode >= 500
	}
}

func hostModelExecuteWithTimeout(req rpcExecutorRequest, runID string, stream bool, timeout time.Duration) hostExecResult {
	resultCh := make(chan hostExecResult, 1)
	go func() {
		resultCh <- hostModelExecute(req, runID, stream)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result
	case <-timer.C:
		return hostExecResult{StatusCode: http.StatusGatewayTimeout, Timeout: true, Err: fmt.Errorf("host model callback timeout after %s", timeout)}
	}
}

func hostModelExecute(req rpcExecutorRequest, runID string, stream bool) hostExecResult {
	headers := cloneHeader(req.Headers)
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set(runIDHeader, runID)
	body := req.Payload
	if len(body) == 0 {
		body = req.OriginalRequest
	}
	entryProtocol := strings.TrimSpace(req.SourceFormat)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(req.Format)
	}
	if entryProtocol == "" {
		entryProtocol = "openai"
	}
	exitProtocol := strings.TrimSpace(req.Format)
	if exitProtocol == "" {
		exitProtocol = entryProtocol
	}
	raw, err := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entryProtocol,
			ExitProtocol:  exitProtocol,
			Model:         req.Model,
			Stream:        false,
			Body:          body,
			Headers:       headers,
			Query:         cloneValues(req.Query),
			Alt:           req.Alt,
		},
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return hostExecResult{StatusCode: statusFromError(err), Err: err}
	}
	var resp pluginapi.HostModelExecutionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return hostExecResult{Err: err}
	}
	return hostExecResult{StatusCode: resp.StatusCode, Headers: resp.Headers, Body: resp.Body}
}

func runStreamWithFailover(req rpcExecutorRequest, pluginStreamID string) error {
	cfg := loadedConfig()
	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		runID := newRunID(req.Model, attempt)
		start := hostModelExecuteStreamWithTimeout(req, runID, time.Duration(cfg.AttemptTimeoutSeconds)*time.Second)
		rec, ok := state.popInflight(runID)
		if start.Timeout || start.Err != nil || retryableResult(start.hostExecResult) {
			if ok {
				state.markFailure(rec.Provider, rec.AuthID, rec.Family, rec.Model, start.StatusCode, start.Timeout, start.Headers, cfg, time.Now())
			}
			if start.Err != nil {
				lastErr = start.Err
			} else {
				lastErr = fmt.Errorf("host stream status %d", start.StatusCode)
			}
			continue
		}
		if ok {
			state.markSuccess(rec.Provider, rec.AuthID, rec.Family)
		}
		return forwardHostStream(start.StreamID, pluginStreamID, time.Duration(cfg.StreamReadTimeoutSeconds)*time.Second)
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("all provider-family stream attempts failed")
}

type hostStreamStart struct {
	hostExecResult
	StreamID string
}

func hostModelExecuteStreamWithTimeout(req rpcExecutorRequest, runID string, timeout time.Duration) hostStreamStart {
	resultCh := make(chan hostStreamStart, 1)
	go func() {
		resultCh <- hostModelExecuteStream(req, runID)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result
	case <-timer.C:
		return hostStreamStart{hostExecResult: hostExecResult{StatusCode: http.StatusGatewayTimeout, Timeout: true, Err: fmt.Errorf("host model stream callback timeout after %s", timeout)}}
	}
}

func hostModelExecuteStream(req rpcExecutorRequest, runID string) hostStreamStart {
	headers := cloneHeader(req.Headers)
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set(runIDHeader, runID)
	body := req.Payload
	if len(body) == 0 {
		body = req.OriginalRequest
	}
	entryProtocol := strings.TrimSpace(req.SourceFormat)
	if entryProtocol == "" {
		entryProtocol = strings.TrimSpace(req.Format)
	}
	if entryProtocol == "" {
		entryProtocol = "openai"
	}
	exitProtocol := strings.TrimSpace(req.Format)
	if exitProtocol == "" {
		exitProtocol = entryProtocol
	}
	raw, err := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: entryProtocol,
			ExitProtocol:  exitProtocol,
			Model:         req.Model,
			Stream:        true,
			Body:          body,
			Headers:       headers,
			Query:         cloneValues(req.Query),
			Alt:           req.Alt,
		},
		HostCallbackID: req.HostCallbackID,
	})
	if err != nil {
		return hostStreamStart{hostExecResult: hostExecResult{StatusCode: statusFromError(err), Err: err}}
	}
	var resp pluginapi.HostModelStreamResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return hostStreamStart{hostExecResult: hostExecResult{Err: err}}
	}
	return hostStreamStart{hostExecResult: hostExecResult{StatusCode: resp.StatusCode, Headers: resp.Headers}, StreamID: resp.StreamID}
}

func forwardHostStream(hostStreamID, pluginStreamID string, readTimeout time.Duration) error {
	if strings.TrimSpace(hostStreamID) == "" {
		return fmt.Errorf("empty host stream id")
	}
	defer func() {
		_, _ = callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: hostStreamID})
	}()
	firstPayload := true
	for {
		chunk, err := readHostStreamWithTimeout(hostStreamID, readTimeout)
		if err != nil {
			return err
		}
		if chunk.Error != "" {
			return fmt.Errorf("%s", chunk.Error)
		}
		if len(chunk.Payload) > 0 {
			if firstPayload && looksLikeWrongSSE(chunk.Payload) {
				return fmt.Errorf("host stream payload format does not match downstream protocol")
			}
			firstPayload = false
			if err := emitPluginStreamChunk(pluginStreamID, chunk.Payload); err != nil {
				return err
			}
		}
		if chunk.Done {
			return nil
		}
	}
}

func readHostStreamWithTimeout(streamID string, timeout time.Duration) (pluginapi.HostModelStreamReadResponse, error) {
	type result struct {
		chunk pluginapi.HostModelStreamReadResponse
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		raw, err := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if err := json.Unmarshal(raw, &chunk); err != nil {
			resultCh <- result{err: err}
			return
		}
		resultCh <- result{chunk: chunk}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-resultCh:
		return res.chunk, res.err
	case <-timer.C:
		return pluginapi.HostModelStreamReadResponse{}, fmt.Errorf("host stream read timeout after %s", timeout)
	}
}

func emitPluginStreamChunk(streamID string, payload []byte) error {
	_, err := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: streamID, Payload: bytes.Clone(payload)})
	return err
}

func closePluginStream(streamID, errMsg string) {
	_, _ = callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{StreamID: streamID, Error: strings.TrimSpace(errMsg)})
}

func looksLikeWrongSSE(payload []byte) bool {
	s := string(payload)
	return strings.Contains(s, "event: response.") && !strings.Contains(s, "event: message_")
}

func newRunID(model string, attempt int) string {
	return fmt.Sprintf("%d-%d-%s", time.Now().UnixNano(), attempt, strings.ReplaceAll(strings.TrimSpace(model), " ", "_"))
}

func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	for _, code := range []int{401, 403, 408, 429, 500, 502, 503, 504} {
		if strings.Contains(msg, fmt.Sprintf("%d", code)) {
			return code
		}
	}
	return 0
}

func cloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for k, values := range src {
		dst[k] = append([]string(nil), values...)
	}
	return dst
}

func cloneValues(src map[string][]string) map[string][]string {
	if src == nil {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for k, values := range src {
		dst[k] = append([]string(nil), values...)
	}
	return dst
}
