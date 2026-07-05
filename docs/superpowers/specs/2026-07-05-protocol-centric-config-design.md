# 协议中心配置设计 (Protocol-Centric Config Design)

日期：2026-07-05

## 背景

早期 code-switch 只支持把 Claude 的 `~/.claude/settings.json` 指向 Anthropic Messages 协议的上游 provider，本质是“一组 BaseURL + AuthEnv 的覆写”。随着 Codex（OpenAI Responses 协议）和 OpenCode（OpenAI Chat 协议）的接入，单一协议假设不再成立：

- 不同的 agent 说不同的 wire protocol（messages / responses / chat）。
- 同一个 provider（例如 zhipu-cn）可同时通过多条协议路径被消费，每条路径有独立的 base URL 与鉴权环境变量。
- agent 与 provider 协议不一致时，需要一个本地 daemon 做协议翻译，而不是简单改 env。

旧的数据模型 `BaseURL` / `AuthEnv` 只表达一条 anthropic-messages 路径，无法干净地承载多协议、多入口。本设计把“协议”作为配置与运行时的一等概念，统一描述直连、代理、以及多路由 daemon。

## 重构目标

1. 用 `Protocol` 作为端点维度，让一个 provider 声明它支持的所有协议入口，而不是只覆盖 anthropic-messages。
2. 让 `switch` 决策显式建模：根据 agent 与 provider 的协议交集，决定直连或代理，并允许用户通过 `--via` 强制。
3. 用一个协议注册中心描述入站路径、上游路径与 IR 翻译，使多路由 daemon 只做“按入站 (method, path) 分发”。
4. 用单个进程、单端口的多路由 daemon 替代“每 agent 一个 daemon”，降低运行时复杂度。
5. 保持向后兼容：旧的 `BaseURL` / `AuthEnv` / 配置文件 / CLI 用法不破坏。

## 非目标

- 不引入 provider fallback / 自动重试。
- 不引入远程 proxy 管理；本机进程仍然是唯一 runtime。
- 不改变 `~/.claude/settings.json` / `~/.codex/config.toml` / opencode 的写盘语义，只是让协议选择显式化。
- 不引入新的外部依赖。

## 目标架构

```
┌──────────────────────────── Agent (claude/codex/opencode) ────────────────────────────┐
│                       ClientProtocol: messages / responses / chat                     │
└───────────────────────────────────────┬──────────────────────────────────────────────┘
                                        │
                  resolveConnection(agent, provider, preset, via)
                                        │
        ┌───────────────────────────────┴───────────────────────────────┐
        │ direct（直连）                                │ proxy（多路由 daemon）       │
        ▼                                                               ▼
  env/config 重写                                         本地 HTTP daemon（单端口多路由）
  UpstreamProtocol ∈ DirectProtocols                      入站 (method,path) → adapter
  直接打 provider 上游                                    UpstreamProtocol 可与 ClientProtocol 不同
                                                          通过 IR 翻译
```

核心抽象（实现见 `protocol.go`）：

- `ProviderProtocol`：wire 协议标识符（`anthropic-messages` / `openai-chat` / `openai-responses`）。
- `ProtocolEndpoint`：一条协议路径上的 `{BaseURL, AuthEnv}`。
- `ProviderPreset.Endpoints`：`map[ProviderProtocol]ProtocolEndpoint`，provider 的全部协议入口。
- `AgentProfile`：每个 agent 的 `ClientProtocol` + 协议能力集合。
- `ConnectionPlan`：`resolveConnection` 的产物，描述一次具体连接。
- `ProtocolAdapter` + `ProtocolRegistry`：协议翻译的注册表，驱动多路由 daemon 的请求分发。

## ProtocolAdapter / AgentProfile / ConnectionPlan

### ProtocolAdapter

`protocol_registry.go` 定义：

```go
type ProtocolAdapter interface {
    Name() ProviderProtocol
    InboundMethod() string        // 入站 HTTP 方法（POST）
    InboundPath() string          // 入站路径，空表示不直接面向客户端
    UpstreamPath() string         // 上游路径
    ParseInboundRequest([]byte) (IRRequest, error)
    BuildUpstreamRequest(IRRequest) ([]byte, error)
    ParseUpstreamResponse([]byte, string) (IRResponse, error)
    WriteClientResponse(http.ResponseWriter, IRResponse, bool)
    ConfigureUpstreamRequest(*http.Request, string)
    CanProxyFrom(ProtocolAdapter) (bool, string) // 该 adapter 是否可作为入站被翻译到目标协议
}
```

每个 adapter 同时承担两个角色：

1. **入站侧**：`InboundPath()` 非空时，adapter 能解析 agent 发来的原始请求并最终把 IR 渲染回 agent 期望的格式。
2. **上游侧**：`BuildUpstreamRequest` / `ParseUpstreamResponse` 负责与上游 provider 交互。

翻译通过共享的 IR（`IRRequest` / `IRResponse`，见 `protocol_ir.go`）完成，任意两种协议之间不直接写互转代码。

`defaultProtocolRegistry()` 注册三个 adapter：

- `anthropicMessagesAdapter`：入站 `/v1/messages`。
- `openAIChatAdapter`：入站 `/v1/chat/completions`。
- `openAIResponsesAdapter`：入站 `/v1/responses`。

### AgentProfile

`protocol.go` 为每个 agent 声明协议能力：

```go
type AgentProfile struct {
    ClientProtocol          ProviderProtocol   // agent 实际说哪种协议
    DirectProtocols         []ProviderProtocol // 与上游的交集候选（用于直连）
    ProxyUpstreamPreference []ProviderProtocol // daemon 可翻译到的上游协议偏好顺序
}
```

- `DirectProtocols` 是“直连时尝试的协议”，表示 agent 可不经 daemon 直接写入配置的上游协议候选。
- `ProxyUpstreamPreference` 是“代理时优先选择的上游协议”，daemon 会用 `CanProxyFrom` 校验能否把入站协议翻译过去。

当前 agent 配置：

| agent     | ClientProtocol        | DirectProtocols                       | ProxyUpstreamPreference                       |
|-----------|-----------------------|---------------------------------------|-----------------------------------------------|
| claude    | anthropic-messages    | anthropic-messages                    | openai-responses, openai-chat, anthropic-messages |
| codex     | openai-responses      | openai-responses, openai-chat         | anthropic-messages, openai-chat, openai-responses |
| opencode  | openai-chat           | anthropic-messages, openai-chat       | anthropic-messages, openai-chat               |

### ConnectionPlan

`resolveConnection` 返回：

```go
type ConnectionPlan struct {
    Mode             ConnectionMode   // direct | proxy
    Agent            AgentName
    Provider         string
    ClientProtocol   ProviderProtocol // agent 侧
    UpstreamProtocol ProviderProtocol // provider 侧（直连时来自 DirectProtocols）
    Endpoint         ProtocolEndpoint // 实际上游 base URL + auth env
}
```

- **direct**：`UpstreamProtocol` 是 agent 原生支持的直连协议之一（来自 `DirectProtocols`），`Endpoint` 来自对应的 `presetEndpoint(protocol)`。
- **proxy**：`UpstreamProtocol` 可能与 `ClientProtocol` 不同；daemon 负责 IR 翻译。

`resolveConnection(agent, provider, preset, via)` 行为：

| via      | 行为                                                                                          |
|-----------|-----------------------------------------------------------------------------------------------|
| `""` / `auto` | 先尝试直连（`DirectProtocols` ∩ provider `Endpoints`），失败则尝试代理（按 `ProxyUpstreamPreference`）。 |
| `direct`  | 强制直连；若 agent 与 provider 无共同协议，返回带可操作提示的错误（跨协议必须通过代理路由）。            |
| `proxy`   | 强制代理；若 provider 没有任何 proxy-compatible endpoint，返回错误。                             |
| 其他      | 拒绝，提示 `use auto, direct, or proxy`。                                                       |

## 协议注册中心

`ProtocolRegistry` 维护两张索引：

1. `byName[ProviderProtocol]ProtocolAdapter`：按协议名查 adapter。
2. `byInbound["METHOD /path"]ProtocolAdapter`：按入站 `(method, path)` 查 adapter，用于 daemon 分发。

```go
func defaultProtocolRegistry() *ProtocolRegistry {
    return newProtocolRegistry(
        anthropicMessagesAdapter{},
        openAIChatAdapter{},
        openAIResponsesAdapter{},
    )
}
```

注册中心是协议翻译的唯一入口：所有请求/响应转换、上游路径选择、入站分发都从它查 adapter，避免散落在各处的 `switch protocol`。新增一个协议时：

1. 实现 `ProtocolAdapter`。
2. 在 `defaultProtocolRegistry()` 注册。
3. 必要时更新相关 agent 的 `AgentProfile`。
4. 扩展 `protocol_*_test.go` 与 `protocol_registry_test.go`。

## 多路由 daemon

设计原则：**一个进程，一个端口，多路由**。

- 单进程同时服务所有已配置 agent（claude / codex / opencode）。
- 入站请求按 `(method, path)` 在 `ProtocolRegistry` 中查到入站 adapter。
- 通过 bearer token 把入站请求映射到具体 route（见 `proxy_server.go` 的 `findProxyRouteByBearerToken`）。
- 根据 route 选定上游 provider / model / upstream protocol，由入站 adapter + 上游 adapter 通过 IR 完成翻译。

配置结构（见 `proxy_config.go`）：

```go
type ProxyConfig struct {
    Host   string                      // 默认 127.0.0.1
    Port   int                         // 0 表示自动分配
    Routes map[string]ProxyRouteConfig // key = agent 名（codex / claude / opencode）
}

type ProxyRouteConfig struct {
    Agent            string
    Provider         string
    Model            string
    UpstreamProtocol string
    ModelMappings    map[string]string
}
```

CLI（见 `proxy_cmd.go` / `proxy_lifecycle.go`）：

```bash
cs proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port]
cs proxy preview <agent>     # 解析并展示单条 route
cs proxy status              # 报告所有已配置 route 的运行时状态
cs proxy start               # 后台启动一个 multi-route daemon（spawn 自身 binary 执行 proxy serve）
cs proxy stop                # 终止 daemon
cs proxy serve               # 前台运行 multi-route daemon
```

关键不变量：

- `configure` 只写一条 route（`cfg.Proxy.Routes[<agent>]`），不会覆盖其它 agent。
- `preview` 只解析请求的那条 route，不要求 daemon 在运行。
- `status` 报告全部已配置 route；单条 route 的失效不影响其它 route 的状态展示。
- `proxy start` spawn 当前 binary 的 `proxy serve` 子进程，单一进程同时承载所有 route。

## Endpoints 字段与 provider 迁移

`ProviderPreset.Endpoints` 是 `map[ProviderProtocol]ProtocolEndpoint`。`presetEndpoint(protocol)` 查找顺序：

1. `Endpoints[protocol]` 命中且 `BaseURL` 非空 → 返回。
2. 仅当 `protocol == anthropic-messages` 且 legacy `BaseURL` / `AuthEnv` 非空 → 返回（兼容旧 preset）。
3. 否则该协议不可用。

**迁移规则**：新增非 Anthropic 协议的 provider 时，直接在 `Endpoints` 里声明，不依赖 legacy 字段；已有只填 `BaseURL` / `AuthEnv` 的 Anthropic preset 保持可用。这保证：

- 旧 preset 无需改动。
- 任何“只支持 Anthropic 协议”的 provider 仍只填 legacy 字段即可。
- 多协议 provider 通过 `Endpoints` 显式表达。

## --via 与 switch 集成

`cs switch <provider> [--via auto|direct|proxy]` 直接调用 `resolveConnection`：

- `auto`（默认）：优先直连（与旧行为一致），无共同协议时自动落到代理 route；若用户尚未 `proxy configure`，给出可操作提示。
- `direct`：强制走 env/config 重写路径，不经过 daemon；agent 与 provider 无共同协议时报错。
- `proxy`：强制走 daemon route；若未配置或 provider 无 proxy-compatible endpoint，报错。

实现要求：

- `splitSwitchArgs` 必须把 `--via` / `-via` 视为值参数（支持 `--via=auto` 与 `--via auto` 两种形式）。
- 帮助文本（`printUsage`）必须展示 `[--via auto|direct|proxy]`。
- shell completion（bash / zsh / fish）必须对 `switch` 提供 `auto / direct / proxy` 候选值。

## 迁移兼容

1. **配置文件**：`AppConfig` 新增字段（`Proxy`、`ModelMappings`）均使用 `omitempty`，旧配置加载后零值即可正常运行。
2. **Provider preset**：旧 preset 的 `BaseURL` / `AuthEnv` 通过 `presetEndpoint` 的 fallback 继续工作；无需批量改写。
3. **CLI**：`switch` 默认 `via=auto`，与历史“优先直连”语义一致；新增的 `--via` 是可选 flag。
4. **Runtime**：旧的单 route proxy 配置（`cfg.Proxy.Routes["codex"]`）继续工作；多路由是增量能力，不强制要求为每个 agent 配 route。
5. **Shell completion**：保留原有 `proxy` 二级补全词表 `configure start stop status preview serve` 的精确顺序（被测试钉住），仅在顶层描述和 switch 的 `--via` 候选上增量更新。

## 测试计划

### Unit tests

- `resolveConnection` 对 `auto / direct / proxy` 各分支，覆盖命中与无共同协议两种结果。
- `presetEndpoint` 在 `Endpoints` 命中、fallback 命中、两者皆空三种情况下的返回。
- `ProtocolRegistry.FindInbound` 对三种入站 `(POST, path)` 的分发正确性。
- `ProtocolAdapter.CanProxyFrom` 在所有 adapter 对上的可翻译性矩阵。
- `splitSwitchArgs` 对 `--via=auto`、`--via auto`、缺失 `--via` 三种形态的解析。

### Integration tests

- `cs proxy configure <agent>` 写入单条 route，不触碰其它 route。
- `cs proxy preview <agent>` 解析单条 route 的 base URL / token / model / upstream protocol。
- `cs proxy status` 列出全部已配置 route。
- `cs proxy serve` 在单端口同时为 claude + codex 提供服务，入站路径正确分发到对应 adapter。

### Completion / help tests

- `printUsage` 包含 `cs switch ... [--via auto|direct|proxy]`。
- `printUsage` 包含全部六个 proxy 子命令。
- bash / zsh / fish completion 对 `switch` 提供 `auto / direct / proxy`。
- bash / fish 二级 proxy 词表 `configure start stop status preview serve` 顺序不变。

## 自审

- 无占位 TBD/TODO。
- 重构目标是“让协议成为一等概念”，而不是替换既有 switch / proxy 流程；旧调用路径全部保留。
- 多路由 daemon 是增量设计，单 route 用户无感知。
- 所有新增字段使用 `omitempty`，旧配置文件无需迁移。
- `--via` 是显式 escape hatch，默认值 `auto` 等价于历史行为。
