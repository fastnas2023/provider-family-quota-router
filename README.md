# provider-family-quota-router

CLIProxyAPI native plugin for passive provider-family quota routing. It solves a practical OAuth routing problem: when one account fails for one model family, CLIProxyAPI should temporarily avoid only that account-family pair instead of disabling the whole account or repeatedly retrying a known-bad route.

Author: Jason Zahng QQ:350400138

## 简介

`provider-family-quota-router` 是一个符合 CLIProxyAPI 官方 native plugin ABI 的插件，用于被动记录上游真实请求结果，并按 `provider + auth_id + model_family` 做精细冷却。

它主要解决的问题：

- 一个账号的 Claude 额度/限流失败，不应该影响同账号的 Gemini 或 GPT-OSS。
- 某个账号某个模型族刚失败后，不应该继续被轮询反复命中。
- 不应该为了探测额度频繁发后台请求，避免还没使用就把额度消耗掉。
- Trae、Claude Compatible、OpenAI Compatible 等客户端只需要正常请求，插件在 CLIProxyAPI 侧被动学习结果。

开发思路和开发路径见：[docs/development-path.md](docs/development-path.md)。

## Purpose

The plugin helps CLIProxyAPI avoid repeatedly selecting an OAuth credential that has just failed for one model family.

Initial scope:

- Provider: `antigravity`
- Families: `claude`, `gemini`, `gpt_oss`, `other`

Core behavior:

- A failure on `provider + auth_id + family` cools down only that account-family pair.
- It does not disable the whole account.
- It does not affect other families on the same account.
- It does not actively probe quota or send background model requests.
- It learns from real request results only.

## Build

Build on the target OS/architecture:

```bash
go test ./...
go vet -buildvcs=false ./...
CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared -o build/provider-family-quota-router.so
```

For a Linux ARM64 VPS, the output must be an `ELF 64-bit ARM aarch64 shared object`.

## Install

Copy the `.so` into the CLIProxyAPI plugin directory:

```bash
mkdir -p plugins/linux/arm64
install -m 0755 build/provider-family-quota-router.so \
  plugins/linux/arm64/provider-family-quota-router-v0.1.0.so
```

Enable it in `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    provider-family-quota-router:
      enabled: true
      priority: 20
      providers:
      - antigravity
      families:
      - claude
      - gemini
      - gpt_oss
      - other
      model_suffixes:
      - "-antigravity"
      max_attempts: 4
      attempt_timeout_seconds: 18
      quota_cooldown_seconds: 18000
      transient_cooldown_seconds: 300
      permission_cooldown_seconds: 3600
      fail_when_all_blocked: true
```

Restart CLIProxyAPI and verify:

```bash
curl http://127.0.0.1:8317/v0/resource/plugins/provider-family-quota-router/state
```

## Notes

This plugin follows CLIProxyAPI native plugin ABI v1 and uses official `sdk/pluginapi` and `sdk/pluginabi` types.

It is not a quota scanner. Do not use it to actively probe accounts.
