# Development Path

Author: Jason Zahng QQ:350400138

## 背景

CLIProxyAPI 可以通过 OAuth 账号接入多个上游 provider。同一个 provider 下，一个账号通常会暴露多个模型族，例如：

- `claude`
- `gemini`
- `gpt_oss`
- `other`

实际使用中，一个账号可能只是在某个模型族上失败。例如 Claude 额度耗尽，但 Gemini 仍然可用。如果直接禁用整个账号，会浪费可用额度；如果不记录失败，又会让后续请求反复打到同一个失败账号，客户端表现为长时间等待、超时或反复报错。

## 要解决的问题

这个插件解决的是精细化路由和冷却问题：

1. 按模型族区分失败范围。
   失败粒度是 `provider + auth_id + family`，不是整个账号。

2. 被动学习真实请求结果。
   插件不主动探测额度，不定时发送模型请求，不做后台 quota 扫描。

3. 避免重复命中刚失败的账号家族。
   当某个账号的某个 family 返回 quota/rate-limit/transient/timeout 类错误时，只把这个账号的这个 family 放入冷却。

4. 保持其他模型族可用。
   例如 `auth-a + claude` 冷却后，`auth-a + gemini` 仍然可以被选择。

5. 所有候选都冷却时快速失败。
   如果某 provider 的某 family 下所有候选账号都在冷却，插件返回明确的 `provider_family_cooldown`，避免请求继续撞已知失败路径。

## 非目标

这个插件不是：

- Codex 429 专用插件。
- Claude 单模型插件。
- 额度扫描器。
- CLIProxyAPI core executor 的替代品。
- 用来主动刷接口判断账号是否恢复的工具。

底层 HTTP 请求如果已经卡在 core executor 中，插件可以让客户端尽快得到超时结果并记录冷却，但不能强杀已经发出的底层连接。彻底取消底层 HTTP 连接需要 CLIProxyAPI core executor 级别的 timeout 支持。

## 官方插件规范

插件遵循 CLIProxyAPI native plugin ABI v1：

- C ABI 入口：`cliproxy_plugin_init`
- RPC schema：`pluginabi.SchemaVersion`
- 注册方法：`plugin.register`
- 重载方法：`plugin.reconfigure`
- 调度方法：`scheduler.pick`
- 路由方法：`model.route`
- 执行方法：`executor.execute`
- 流式执行方法：`executor.execute_stream`
- 管理接口：`management.register` / `management.handle`

代码使用官方 SDK 类型：

- `sdk/pluginabi`
- `sdk/pluginapi`

构建方式必须使用目标系统和架构：

```bash
CGO_ENABLED=1 go build -buildvcs=false -buildmode=c-shared -o build/provider-family-quota-router.so
```

Linux ARM64 VPS 上的产物应为：

```text
ELF 64-bit ARM aarch64 shared object
```

## 设计路径

### 1. ModelRouter 接管目标模型

`model.route` 只接管配置匹配的模型别名，默认是 `-antigravity` 后缀。

当前支持的入口协议：

- `openai`
- `claude`

为了减少误伤，当前不接管 native `gemini` source format。

### 2. Executor 观察真实请求结果

插件 executor 通过 `host.model.execute` 或 `host.model.execute_stream` 调用 CLIProxyAPI 的原生执行链路。

它不直接保存账号凭证，不自己实现 Antigravity 登录，也不绕过 CLIProxyAPI 的官方 auth 管理。

### 3. Scheduler 选择未冷却账号

`scheduler.pick` 根据当前请求的 provider 和 model family，从候选 auth 中跳过仍在冷却的账号家族。

冷却 key：

```text
provider + auth_id + family
```

### 4. 请求结果驱动冷却

失败分类：

- `429`: quota/rate cooldown
- `401/403`: auth/permission cooldown
- `408/500/502/503/504`: transient cooldown
- host callback timeout: timeout cooldown
- `2xx`: 清除对应账号家族冷却

如果上游返回 `Retry-After`，插件优先使用该值，但会受 `max_retry_after_seconds` 限制。

### 5. 管理接口观测状态

插件提供只读状态页：

```text
/v0/resource/plugins/provider-family-quota-router/state
```

当前输出：

- 插件配置
- 当前 cooldown 列表
- 插件名称

## 测试路径

### 离线测试

离线测试不发真实模型请求，不消耗额度。

覆盖内容：

- `model.route` 只接管配置匹配的模型。
- `scheduler.pick` 跳过已冷却账号家族。
- Claude 冷却不影响同账号 Gemini。
- 所有候选都冷却时返回 `provider_family_cooldown`。
- `management.register` 输出符合 CLIProxyAPI resource schema。
- `executor.http_request` 显式返回 unsupported。

运行：

```bash
go test ./...
go vet -buildvcs=false ./...
```

### 线上最小验证

上线后先验证加载和管理接口：

```bash
curl http://127.0.0.1:8317/v0/resource/plugins/provider-family-quota-router/state
```

确认插件加载后，再做最小真实请求，例如 `max_tokens: 1`。不要用大量请求主动压测额度。

## 后续可细化方向

1. 状态持久化。
   将 cooldown 状态写入本地 JSON，重启后继续生效。

2. `/state` 信息增强。
   增加最近选择、最近失败、最近成功、下一次可重试时间。

3. 更细失败分类。
   区分 quota exhausted、rate limit、auth expired、project/API disabled、transient 5xx。

4. 并发保护。
   限制 host callback 并发，避免超时请求在后台堆积。

5. model family 映射可配置。
   支持自定义模型名到 family 的映射，减少硬编码。
