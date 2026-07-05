# TUI Proxy Manager 设计

日期：2026-07-04

## 背景

项目已具备：

- `cs model get/set/list`
- `cs model-map set/get/list/remove`
- `cs use-model`
- 协议 proxy handler：Responses / Anthropic Messages / OpenAI Chat 转换
- `cs run codex --provider <provider> --dry-run`

但 TUI 目前只覆盖 provider 选择、API key、model、tier 配置。用户希望 TUI 也支持模型映射、快速切换和完整 proxy 管理能力，包括启动与停止本地 proxy 服务。

## 目标

1. 在 TUI 中管理 provider 默认模型和 proxy model mappings。
2. 在 TUI 中配置 proxy route。
3. 在 TUI 中启动、停止、查看本地 proxy 服务。
4. 底层 proxy 生命周期能力同时暴露给 CLI，避免 TUI 私有逻辑。
5. 保持现有 `switch` / `configure` 流程低耦合，不改变既有 provider 切换语义。

## 非目标

- 不实现多 provider fallback。
- 不实现自动测速或健康检查选模型。
- 不实现复杂多 route 调度；MVP 只需要按 agent 管理 route。
- 不修改 Claude/Codex/OpenCode 的真实配置文件，MVP 先显示配置预览和环境变量。
- 不实现远程 proxy 管理；仅管理本机进程。

## 用户界面

### Provider 详情页

在现有 `showDetail()` Actions 中新增：

- `Use Model`
- `Manage Model Mappings`
- `Proxy Manager`

### Use Model

表单字段：

- `Model`

保存行为等价于：

```bash
cs use-model <provider> <model>
```

写入：

- `cfg.Providers[provider].Model = model`
- `cfg.ModelMappings[provider]["default"] = model`

校验：

- model 不能为空。
- 内建 NoModel provider 拒绝。
- 复用 `validateProviderModel(provider, model)`。
- custom provider 允许任意非空 model。

### Manage Model Mappings

页面内容：

- 显示当前 provider 的 mappings。
- `Add / Update Mapping`：输入 `client model` 和 `upstream model`。
- `Remove Mapping`：输入 `client model` 删除。
- `Back` 返回详情页。

保存行为：

- 写入 `cfg.ModelMappings[provider][clientModel] = upstreamModel`。
- 删除最后一条 mapping 后移除 provider entry，保持 config 简洁。

校验：

- client/upstream model 不能为空。
- provider 来自详情页，仍在 helper 层做存在性校验。

### Proxy Manager

页面动作：

- `Configure Route`
- `Start Proxy`
- `Stop Proxy`
- `Status`
- `Agent Config Preview`
- `Back`

`Configure Route` 字段：

- `Agent`: MVP 支持 `codex` 和 `claude`。
- `Provider`: 默认当前详情页 provider。
- `Model`: 默认 provider 当前模型，可为空时 fallback provider 默认。
- `Upstream Protocol`: 默认按 provider 能力推导；允许用户选择 `anthropic_messages`、`openai_chat`、`openai_responses`。

`Agent Config Preview` 显示：

- base URL，例如 `http://127.0.0.1:<port>/v1`
- token 是否已生成，不显示完整 token 时可提供复制提示；测试输出不得泄露真实 provider key。
- model。
- Codex/OpenAI-compatible 配置片段。
- Claude/Anthropic-compatible 环境变量片段。

## 配置结构

`AppConfig` 新增：

```go
Proxy ProxyConfig `json:"proxy,omitempty"`

type ProxyConfig struct {
    Host   string                      `json:"host,omitempty"`
    Port   int                         `json:"port,omitempty"`
    Routes map[string]ProxyRouteConfig `json:"routes,omitempty"`
}

type ProxyRouteConfig struct {
    Agent            string            `json:"agent"`
    Provider         string            `json:"provider"`
    Model            string            `json:"model,omitempty"`
    UpstreamProtocol string            `json:"upstreamProtocol,omitempty"`
    ModelMappings    map[string]string `json:"modelMappings,omitempty"`
}
```

默认值：

- `Host`: `127.0.0.1`
- `Port`: `0` 表示自动分配。
- `Routes`: key 为 agent 名称，MVP 使用 `codex` / `claude`。

配置中的 `ModelMappings` 是 route 快照。构造 runtime route 时，优先使用 route 自身 mappings；如果为空，则 fallback 到 `cfg.ModelMappings[provider]`。

## Runtime 状态

新增状态文件：

```text
~/.code-switch/proxy-state.json
```

结构：

```go
type ProxyRuntimeState struct {
    PID       int       `json:"pid"`
    Host      string    `json:"host"`
    Port      int       `json:"port"`
    BaseURL   string    `json:"baseURL"`
    Token     string    `json:"token"`
    StartedAt time.Time `json:"startedAt"`
}
```

状态校验：

- PID 存在但 health check 失败时视为 stale。
- stale state 可由 `proxy status` 报告，也可由 `proxy start` 覆盖。
- `proxy stop` 删除 stale state 并返回明确消息。

## CLI 支撑

新增：

```bash
cs proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port]
cs proxy start
cs proxy stop
cs proxy status
cs proxy preview <agent>
```

TUI 调用同一组内部 helper，而不是复制逻辑。

MVP 行为：

- `proxy start` 后台启动当前二进制的 `proxy serve` 子进程。
- `proxy serve` 负责监听端口、注册 `/healthz`、处理 `/v1/responses` 和 `/v1/messages`。
- `proxy stop` 读取 state PID，校验进程后发送终止信号。
- 不使用 force kill，除非后续单独设计。

## Runtime route 构造

新增 helper：

```go
buildProxyRouteFromConfig(agent string, cfg *AppConfig) (ProxyRoute, error)
```

步骤：

1. 读取 `cfg.Proxy.Routes[agent]`。
2. canonicalize provider。
3. resolve provider preset 和 API key。
4. resolve model：route model > provider stored model > preset default。
5. resolve mappings：route mappings > `cfg.ModelMappings[provider]`。
6. resolve upstream protocol。
7. 返回 `ProxyRoute`。

## 错误处理

- provider 未知：显示可操作错误。
- model 不合法：显示校验错误，不保存。
- provider API key 缺失：允许保存配置，但启动时提示缺 key。
- 端口占用：启动失败，保留旧 state 不覆盖，错误中包含 host/port。
- state stale：status 明确显示 stale；start 可覆盖。
- stop 非 code-switch proxy PID：拒绝停止，避免误杀。

## 测试计划

### Unit tests

- Use Model helper 同时写 provider model 和 `default` mapping。
- NoModel provider 拒绝 Use Model。
- unsupported opencode-go model 拒绝。
- Add/Update/Remove mapping helper。
- `buildProxyRouteFromConfig` 注入 model mappings。
- route mappings 优先于 provider global mappings。
- stale runtime state 检测。

### Integration tests

- `cs proxy configure` 写入 config。
- `cs proxy start` 启动服务，`proxy status` 显示 running。
- 请求 `/healthz` 成功。
- 请求 `/v1/responses` 时 model mapping 生效。
- `cs proxy stop` 后 state 清理，端口关闭。

### TUI tests

- 表单 helper 覆盖配置写入，不依赖真实终端交互。
- Actions 中包含新增入口。
- 错误 label 可显示校验失败。

## 实施顺序

1. 提取 TUI/CLI 可共享的 model 和 mapping helper。
2. 增加 proxy config/state 数据结构和 route builder。
3. 实现 `cs proxy configure/status/preview`。
4. 实现 `cs proxy serve/start/stop`。
5. 接入 TUI：Use Model、Manage Model Mappings、Proxy Manager。
6. 完整验证：`go vet ./... && go test ./... && go build -o cs .`。

## 自审

- 无占位 TBD/TODO。
- TUI 与 CLI 共享 helper，避免重复逻辑。
- MVP 范围明确包含启动/停止，但不包含复杂多 route/fallback。
- proxy state 明确了 stale、误杀、端口占用等错误路径。
- 测试覆盖配置、runtime、TUI helper 三层。
