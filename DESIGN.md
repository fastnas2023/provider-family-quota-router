# provider-family-quota-router

## Target

This plugin adds passive quota-aware auth selection for CLIProxyAPI OAuth-style providers.

Initial provider scope:

- `antigravity`

Initial model-family scope:

- `claude`
- `gemini`
- `gpt_oss`
- `other`

Core rule:

> A failure on one account for one model family only cools down that account-family pair. It must not disable the entire account, and it must not affect other model families on the same account.

Example:

- `auth-a` fails on `claude-sonnet-4-6-antigravity`.
- The plugin cools down `antigravity + auth-a + claude`.
- The same auth can still be selected for `gemini-*` or `gpt-oss-*` models if those families are not cooled down.

## Non-goals

- Not a Codex 429 clone.
- Not a Claude single-model plugin.
- Not an active quota probe.
- Not a full replacement for CLIProxyAPI core retry logic.
- Not an online installer in this development step.

## Request flow

1. `ModelRouter` handles matching provider aliases, defaulting to models ending in `-antigravity`.
2. Requests are routed to the plugin executor so the plugin can observe real request results.
3. The executor calls the host model path with the same model and protocol.
4. `Scheduler` chooses an auth candidate for the same provider/model family, avoiding unexpired family cooldowns.
5. If a real request returns quota/rate-limit/transient status, or if the host call does not return within the configured timeout, only the selected account-family pair is cooled down.
6. On success, the selected account-family cooldown is cleared.

## Passive quota policy

The plugin does not refresh or probe quota on its own.

Quota state is learned only from real traffic:

- HTTP 429: quota/rate cooldown.
- HTTP 403: permission/quota-like family cooldown.
- HTTP 408/500/502/503/504: transient family cooldown.
- host callback timeout: transient family cooldown.
- HTTP 2xx: clear cooldown for the selected account-family.

If upstream returns `Retry-After`, the plugin uses it within configured bounds. Otherwise it uses conservative defaults.

## Boundaries and risks

- The plugin can return early to the client when the host callback hangs, but it cannot forcibly kill the already-started core executor HTTP request. That requires a core executor timeout fix.
- The plugin uses an internal request header to correlate scheduler picks with executor results. The header contains no secret, but it may pass through if the host forwards all headers.
- Streaming retry is safe only before the first upstream payload. After bytes have been emitted to the client, later failures are forwarded as stream errors rather than replayed.

## Online install policy

This development step does not install anything online.

Before any online install:

1. Build the Linux plugin artifact in an isolated directory.
2. Back up current plugin and config files.
3. Copy the plugin into the CLIProxyAPI plugin directory.
4. Add plugin config with `enabled: true`.
5. Restart CLIProxyAPI.
6. Verify only plugin load logs and the management `/state` route.
7. Do not send real model requests until separately confirmed.
