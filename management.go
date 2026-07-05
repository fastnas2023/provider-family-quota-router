package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type managementRegistration struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

type managementRequest struct {
	Method         string
	Path           string
	Headers        http.Header
	Query          url.Values
	Body           []byte
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func managementRegister(_ []byte) ([]byte, error) {
	return okEnvelope(managementRegistration{
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        "/state",
				Menu:        "Provider Family Quota Router",
				Description: "Shows passive account-family cooldown state without probing upstream quota.",
			},
		},
	})
}

func managementHandle(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
	}
	status := http.StatusOK
	body, _ := json.MarshalIndent(map[string]any{
		"plugin":    pluginName,
		"config":    loadedConfig(),
		"cooldowns": state.snapshot(time.Now()),
	}, "", "  ")
	if !managementStatePath(req.Path) {
		status = http.StatusNotFound
		body = []byte(`{"error":"not found"}`)
	}
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	})
}

func managementStatePath(path string) bool {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	return path == "" || path == "/state" || strings.HasSuffix(path, "/provider-family-quota-router/state")
}
