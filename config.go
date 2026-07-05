package main

import (
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

const (
	pluginName     = "provider-family-quota-router"
	runIDHeader    = "X-CLIProxy-PFQR-Run-ID"
	defaultTimeout = 18
)

var currentConfig atomic.Value

type pluginConfig struct {
	Enabled                   bool     `yaml:"enabled"`
	Providers                 []string `yaml:"providers"`
	Families                  []string `yaml:"families"`
	ModelSuffixes             []string `yaml:"model_suffixes"`
	MaxAttempts               int      `yaml:"max_attempts"`
	AttemptTimeoutSeconds     int      `yaml:"attempt_timeout_seconds"`
	StreamReadTimeoutSeconds  int      `yaml:"stream_read_timeout_seconds"`
	QuotaCooldownSeconds      int      `yaml:"quota_cooldown_seconds"`
	PermissionCooldownSeconds int      `yaml:"permission_cooldown_seconds"`
	TransientCooldownSeconds  int      `yaml:"transient_cooldown_seconds"`
	MaxRetryAfterSeconds      int      `yaml:"max_retry_after_seconds"`
	DelegateWhenNoBlock       string   `yaml:"delegate_when_no_block"`
	FailWhenAllBlocked        bool     `yaml:"fail_when_all_blocked"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		Enabled:                   true,
		Providers:                 []string{"antigravity"},
		Families:                  []string{familyClaude, familyGemini, familyGPTOSS, familyOther},
		ModelSuffixes:             []string{"-antigravity"},
		MaxAttempts:               4,
		AttemptTimeoutSeconds:     defaultTimeout,
		StreamReadTimeoutSeconds:  60,
		QuotaCooldownSeconds:      5 * 60 * 60,
		PermissionCooldownSeconds: 60 * 60,
		TransientCooldownSeconds:  5 * 60,
		MaxRetryAfterSeconds:      6 * 60 * 60,
		DelegateWhenNoBlock:       "round-robin",
		FailWhenAllBlocked:        true,
	}
}

func decodeConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultConfig()
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return pluginConfig{}, err
		}
	}
	cfg.normalize()
	return cfg, nil
}

func (c *pluginConfig) normalize() {
	if c.Providers == nil || len(c.Providers) == 0 {
		c.Providers = []string{"antigravity"}
	}
	if c.Families == nil || len(c.Families) == 0 {
		c.Families = []string{familyClaude, familyGemini, familyGPTOSS, familyOther}
	}
	if c.ModelSuffixes == nil || len(c.ModelSuffixes) == 0 {
		c.ModelSuffixes = []string{"-antigravity"}
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 1
	}
	if c.MaxAttempts > 12 {
		c.MaxAttempts = 12
	}
	if c.AttemptTimeoutSeconds <= 0 {
		c.AttemptTimeoutSeconds = defaultTimeout
	}
	if c.StreamReadTimeoutSeconds <= 0 {
		c.StreamReadTimeoutSeconds = 60
	}
	if c.QuotaCooldownSeconds <= 0 {
		c.QuotaCooldownSeconds = 5 * 60 * 60
	}
	if c.PermissionCooldownSeconds <= 0 {
		c.PermissionCooldownSeconds = 60 * 60
	}
	if c.TransientCooldownSeconds <= 0 {
		c.TransientCooldownSeconds = 5 * 60
	}
	if c.MaxRetryAfterSeconds <= 0 {
		c.MaxRetryAfterSeconds = 6 * 60 * 60
	}
	for i := range c.Providers {
		c.Providers[i] = normalizeProvider(c.Providers[i])
	}
	for i := range c.Families {
		c.Families[i] = strings.ToLower(strings.TrimSpace(c.Families[i]))
	}
	for i := range c.ModelSuffixes {
		c.ModelSuffixes[i] = strings.ToLower(strings.TrimSpace(c.ModelSuffixes[i]))
	}
	c.DelegateWhenNoBlock = strings.ToLower(strings.TrimSpace(c.DelegateWhenNoBlock))
}

func loadedConfig() pluginConfig {
	if raw := currentConfig.Load(); raw != nil {
		if cfg, ok := raw.(pluginConfig); ok {
			return cfg
		}
	}
	return defaultConfig()
}

func (c pluginConfig) providerAllowed(provider string) bool {
	provider = normalizeProvider(provider)
	for _, item := range c.Providers {
		if item == provider {
			return true
		}
	}
	return false
}

func (c pluginConfig) familyAllowed(family string) bool {
	family = strings.ToLower(strings.TrimSpace(family))
	for _, item := range c.Families {
		if item == family {
			return true
		}
	}
	return false
}

func (c pluginConfig) modelMatches(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	if !c.familyAllowed(modelFamily(model)) {
		return false
	}
	for _, suffix := range c.ModelSuffixes {
		if suffix != "" && strings.HasSuffix(model, suffix) {
			return true
		}
	}
	return false
}

func sourceFormatSupported(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "openai", "claude":
		return true
	default:
		return false
	}
}
