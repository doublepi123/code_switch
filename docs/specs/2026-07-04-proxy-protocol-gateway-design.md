# code-switch 独立代理层与三向协议网关设计

日期：2026-07-04

## 1. 背景与目标

`code-switch` 当前是一个轻量配置切换工具：它把 Claude Code、Codex、OpenCode 的配置写到各自配置文件中，让 agent 直接连接目标 provider。

现有模式的限制是：agent 和 provider 的 API 协议必须天然匹配，或 provider 自己同时提供多种兼容协议。例如：

- Claude Code 主要说 Anthropic Messages 协议：`POST /v1/messages`
- Codex 主要说 OpenAI Responses 协议：`POST /v1/responses`
- OpenCode 可通过 AI SDK 使用 Anthropic 或 OpenAI-compatible provider
- 许多国内 provider 只提供 Anthropic-compatible endpoint，导致 Codex 无法直接使用
- 许多 OpenAI-compatible provider 可被 Codex 使用，但 Claude Code 无法直接使用

本设计新增一个**低耦合、独立运行的本地代理层**，类似 LiteLLM / claude-code-router，但作为 `code-switch` 的可选能力存在。

核心目标：

1. 代理监听本地端口，agent 配置指向本地代理。
2. 代理根据当前 route 把请求转发到目标 provider。
3. 当 agent 协议与 provider 协议不一致时，代理进行协议转换。
4. 长期支持三种协议互转：
   - Anthropic Messages
   - OpenAI Chat Completions
   - OpenAI Responses
5. 长期支持两种生命周期：
   - 临时前台：随 agent 会话启动和退出
   - daemon：常驻后台代理

MVP 目标需要收窄：第一阶段只验证一个最高价值链路，即 **Codex / OpenAI Responses client → Anthropic Messages upstream**。这能直接解决“Codex 不能使用大量 Anthropic-compatible provider”的问题。三向互转、daemon、工具调用流式转换作为后续阶段推进。

非目标：

- 不在第一阶段实现 GUI。
- 不在第一阶段实现团队级鉴权、配额、数据库 Dashboard。
- 不把现有 `switch` 模式替换掉；原有直连 provider 的配置切换能力保持不变。
- 不要求代理层覆盖所有 provider 的高级私有能力；优先保证文本、工具调用、流式输出的通用路径。

## 2. 用户体验

### 2.1 临时前台模式

临时模式用于一次性启动 agent。代理和 agent 生命周期绑定。

示例：

```bash
cs run codex --provider minimax-cn
cs run claude --provider openrouter --model anthropic/claude-sonnet-4.6
cs run opencode --provider zhipu-cn
```

行为：

1. `cs` 分配本地端口。
2. 启动本地代理。
3. 为 agent 生成临时配置或环境变量，使 agent 指向本地代理。
4. 启动 agent 子进程。
5. agent 退出后，代理退出。

### 2.2 Daemon 模式

Daemon 模式用于长时间运行的本地网关。

示例：

```bash
cs proxy start --port 45123
cs proxy route set --agent codex --provider minimax-cn --upstream-protocol anthropic-messages
cs proxy route get --agent codex
cs proxy route list
cs proxy status
cs proxy stop
```

推荐配置方式：

```bash
cs proxy configure codex
cs proxy configure claude
cs proxy configure opencode
```

行为：

1. `proxy configure` 将 agent 的配置指向本地代理，而不是直接指向 provider。
2. `proxy route` 改变本地代理的上游 provider 和协议。
3. agent 不需要重写主 provider 配置即可复用本地代理。

### 2.3 现有命令关系

保留现有命令：

- `cs switch`：写 agent 配置，直连 provider。
- `cs env`：输出 provider 环境变量。
- `cs test`：直接测试 provider。

新增命令建议：

- `cs run <agent> --provider <provider>`：临时启动代理和 agent。
- `cs proxy start|stop|status|route|configure|test`：管理独立代理。

`run` 是高层便捷命令；`proxy` 是底层代理管理命令。

## 3. 架构原则

### 3.1 低耦合

代理层独立于现有配置切换逻辑：

- 现有 provider preset 继续复用，但需要增加协议元数据。
- 现有 `switch`、`restore`、`doctor` 不依赖代理。
- 代理相关代码集中在独立文件组中，例如：
  - `proxy.go`
  - `proxy_server.go`
  - `proxy_routes.go`
  - `protocol_ir.go`
  - `protocol_anthropic.go`
  - `protocol_openai_chat.go`
  - `protocol_openai_responses.go`
  - `sse.go`

### 3.2 显式协议元数据

当前 `ProviderPreset` 没有协议字段，协议依赖 BaseURL 路径隐式推断。代理层需要显式协议类型。

新增类型：

```go
type ProviderProtocol string

const (
    protocolAnthropicMessages ProviderProtocol = "anthropic-messages"
    protocolOpenAIChat        ProviderProtocol = "openai-chat"
    protocolOpenAIResponses   ProviderProtocol = "openai-responses"
)
```

`ProviderPreset` 新增可选字段：

```go
Protocol ProviderProtocol
```

对于同一 provider 暴露多协议 endpoint 的情况，新增 endpoint map：

```go
ProtocolEndpoints map[ProviderProtocol]string
```

示例：

```go
deepseek: {
    BaseURL: "https://api.deepseek.com/anthropic",
    Protocol: protocolAnthropicMessages,
    ProtocolEndpoints: map[ProviderProtocol]string{
        protocolAnthropicMessages: "https://api.deepseek.com/anthropic",
        protocolOpenAIResponses:   "https://api.deepseek.com/v1",
        protocolOpenAIChat:        "https://api.deepseek.com/v1",
    },
}
```

### 3.3 自定义 typed IR 作为内部语义中心

协议转换采用中间表示（IR）模式，而不是六个方向两两互转。

不直接把 OpenAI Chat Completions 的 JSON 结构作为内部 IR。虽然 LiteLLM 和 claude-code-router 都使用 OpenAI 风格作为统一格式，但 Chat Completions 对 Anthropic content block、Responses output item、reasoning、多模态、tool result 的表达力不足。如果直接把 Chat JSON 当语义中心，后续会不断塞入私有扩展，形成“伪 OpenAI Chat”。

因此代理层使用自定义 typed IR：

```go
type IRRequest struct {
    Model       string
    Messages    []IRMessage
    Tools       []IRTool
    Stream      bool
    MaxTokens   int
    Temperature *float64
}

type IRMessage struct {
    Role  string
    Parts []IRPart
}

type IRPart struct {
    Type     string // text, tool_use, tool_result, image, reasoning
    Text     string
    ToolCall *IRToolCall
}
```

选择 typed IR 的原因：

1. Anthropic content block、OpenAI tool calls、Responses output item 都能映射为明确类型。
2. 不把任何外部协议的历史包袱泄漏到内部接口。
3. 后续新增协议时只需实现 `协议 ↔ IR`，不需要新增所有组合映射。
4. 转换前可以基于 IR capability 做统一校验，避免转换到一半失败。

转换链：

```text
client protocol -> IR -> upstream protocol
upstream protocol -> IR -> client protocol
```

三种协议需要实现：

```text
Anthropic Messages   ↔ IR
OpenAI Chat          ↔ IR
OpenAI Responses     ↔ IR
```

## 4. HTTP 代理流程

### 4.1 请求识别

代理根据请求 path 判断 client protocol：

| Path | Client Protocol |
| --- | --- |
| `/v1/messages` | Anthropic Messages |
| `/v1/chat/completions` | OpenAI Chat Completions |
| `/v1/responses` | OpenAI Responses |

未知 path 返回 404，并提示支持的 endpoint。

### 4.2 上游选择

上游由 route 决定：

```go
type ProxyRoute struct {
    Agent            AgentName
    Provider         string
    Model            string
    UpstreamProtocol ProviderProtocol
    UpstreamBaseURL  string
    TokenHash        string
}
```

临时模式中 route 来自命令行参数。

Daemon 模式中 route 存在代理状态文件中，例如：

```text
~/.code-switch/proxy/state.json
```

Daemon 模式不能只靠 path 判断 route，因为多个 agent 可能使用同一协议 endpoint。路由键必须包含一个随机本地 token：

```text
Authorization: Bearer <random-local-token>
```

`cs proxy configure <agent>` 为每个 agent 写入不同的随机 token。代理状态中保存 token hash 到 route 的映射，文件权限必须为 `0600`。固定字符串（例如 `cs-proxy`）只能用于临时 dry-run 示例，不能用于 daemon 鉴权或路由。

### 4.3 请求转换

流程：

1. 解析 client 请求体。
2. 转为 IR。
3. 应用 provider model、system、tool、reasoning 参数修正。
4. 从 IR 转为 upstream protocol 请求。
5. 注入认证 header。
6. 发往上游。

认证信息复用现有 provider key 存储和 `resolveProviderAndKeyForAgent()`，但代理层不写 agent 主配置。

### 4.4 响应转换

非流式响应：

1. 读上游响应 JSON。
2. 转为 IR response。
3. 转为 client protocol response。
4. 返回客户端。

流式响应：

1. 保持 `text/event-stream`。
2. 逐 SSE 事件读取上游响应。
3. 每个事件转为一个或多个 IR stream event。
4. IR stream event 转为 client protocol SSE event。
5. 每写一个事件立即 `Flush()`。

不使用 `httputil.ReverseProxy` 作为主要实现，因为它不适合逐事件修改 SSE 流。

## 5. 协议转换范围

协议转换按阶段交付。长期目标是三向互转；MVP 只做一个端到端价值链路：

```text
client: OpenAI Responses  -> IR -> upstream: Anthropic Messages / OpenAI Chat Completions
```

MVP 支持 Codex CLI 简单文本请求所需的 Responses 字段子集：`instructions` 映射为系统提示，Responses input 中的 `developer` 消息映射为 IR system，`user`/`assistant` 文本消息转为上游消息。Responses 入站上游可使用 Anthropic Messages 或 OpenAI Chat Completions；Anthropic `/v1/messages` 入站也可代理到真正实现 OpenAI Responses API 的上游。对于要求 `stream:true` 的 Responses 上游，代理会请求上游 SSE 并聚合为非流式 Anthropic response 返回给 Claude 客户端。`prompt_cache_key`、`client_metadata`、`store`、`include`、`reasoning:null` 作为非语义字段显式允许并忽略。`tools`、`tool_choice`、`parallel_tool_calls` 在 MVP 中允许出现但不转换工具定义或工具调用，仅用于让不会实际调用工具的简单文本请求跑通。MVP 对 image、非文本 content part、非 null reasoning 和未知顶层字段返回明确 400 错误，不做静默降级。

### 5.1 第一阶段必须支持

#### 文本消息

MVP（第一阶段）的输入能力范围被严格收窄，避免在实现未完整时假装支持全部 Responses 协议：

- **支持** Responses `input` 为字符串：转换为单条 `user` text message。
- **支持** Responses `input` 为 text-only message array：允许 `developer`、`user`、`assistant`；`developer` 映射为 IR system，最终进入上游系统提示。
- **支持** 顶层 `instructions` 字符串：映射为 IR system；Anthropic 上游进入顶层 `system`，OpenAI Chat 上游进入 `system` message。
- **支持** `stream:true` 的最小 SSE 包装：上游仍使用非流式 request，代理拿到完整响应后合成 Responses SSE 事件序列返回。
- **允许并忽略** `prompt_cache_key`、`client_metadata`、`store`、`include`、`reasoning:null`。
- **允许但不转换** `tools`、`tool_choice`、`parallel_tool_calls`：MVP 仅保证未实际触发工具调用的简单文本请求可跑通。
- **不支持** image、非文本 content part、非 null reasoning：出现时返回 400。
- 未知 Responses 顶层字段（例如 `temperature`、`top_p`、`previous_response_id`、`metadata`、`user`、`text`）返回 400，不能静默忽略。

#### 工具调用（第二阶段）

- Anthropic `tool_use`
- OpenAI Chat `tool_calls`
- OpenAI Responses `function_call`
- 工具调用参数 JSON 增量流

#### 基础 usage

- input tokens
- output tokens
- total tokens

如果上游不返回 usage，响应中可省略或置零，但不能伪造精确值。

#### stop reason 映射

| Anthropic | OpenAI Chat | Responses |
| --- | --- | --- |
| `end_turn` | `stop` | `completed` |
| `max_tokens` | `length` | `incomplete` |
| `tool_use` | `tool_calls` | `function_call` |
| `stop_sequence` | `stop` | `completed` |

### 5.2 第一阶段降级处理

以下能力第一阶段以 best-effort 处理：

- Anthropic thinking / signature_delta
- Responses reasoning summary
- 图片输入
- web_search / code_interpreter / container
- logprobs
- annotations
- provider 私有字段

降级规则：

1. 可安全映射的字段保留。
2. 无法映射但不影响文本输出的字段忽略，并在 debug 日志中记录。
3. 无法支持且会改变语义的请求返回 400，错误信息说明不支持的字段和建议。
4. MVP 阶段，tool、image、reasoning 请求默认视为会改变语义，返回 400；后续阶段逐项放开。

### 5.3 不允许静默错误

代理不能悄悄吞掉关键能力。例如：

- 请求包含 tool definitions，但目标协议转换器不支持 tool，应返回 400。
- 请求包含图片，但当前转换器不支持图片，应返回 400。
- 上游返回未知 SSE 事件，若无法安全忽略，应向客户端返回协议对应的 error event。

## 6. 流式转换设计

### 6.1 SSE 解析与写入

使用 Go 标准库手写 SSE 解析：

- 首选 `bufio.Reader` 按行读取，显式限制单个 SSE event 最大大小。
- 支持 `event:`、多行 `data:`、空行分隔。
- 支持 CRLF、注释行（`: ping`）、无 `event` 仅 `data` 的事件。
- 支持 OpenAI `data: [DONE]`、Anthropic `ping`、Responses 带 event name 的事件。
- 避免无界 channel 或无界 buffer；采用“读一个事件 → 转换 → 写一个事件”的同步流程，利用写阻塞形成自然 backpressure。

写入：

```go
w.Header().Set("Content-Type", "text/event-stream")
w.Header().Set("Cache-Control", "no-cache")
w.Header().Set("Connection", "keep-alive")
flusher, ok := w.(http.Flusher)
```

每个事件写入后立即 flush。

写失败时必须立即 cancel upstream context 并关闭 upstream body。

### 6.2 Stream IR

定义协议无关的流式事件：

```go
type StreamEventType string

const (
    streamStart       StreamEventType = "start"
    streamTextDelta   StreamEventType = "text_delta"
    streamToolStart   StreamEventType = "tool_start"
    streamToolDelta   StreamEventType = "tool_delta"
    streamToolStop    StreamEventType = "tool_stop"
    streamUsage       StreamEventType = "usage"
    streamStop        StreamEventType = "stop"
    streamError       StreamEventType = "error"
)
```

示例结构：

```go
type StreamEvent struct {
    Type       StreamEventType
    ID         string
    Model      string
    Text       string
    ToolID     string
    ToolName   string
    ToolIndex  int
    ToolDelta  string
    StopReason string
    Usage      *Usage
    Err        *ProxyError
}
```

每个协议实现两个接口：

```go
type StreamDecoder interface {
    Decode(event SSEEvent) ([]StreamEvent, error)
}

type StreamEncoder interface {
    Encode(event StreamEvent) ([]SSEEvent, error)
}
```

一个上游 SSE 事件可能产生 0、1 或多个 IR event；一个 IR event 也可能产生 0、1 或多个客户端 SSE event。

### 6.3 状态机

每个协议转换器维护自己的状态：

```go
type StreamState struct {
    Started           bool
    Completed         bool
    ContentBlockIndex int
    OutputIndex       int
    ContentIndex      int
    CurrentBlockType  string
    ToolBuffers       map[string]*strings.Builder
}
```

工具调用流式转换必须按 tool call id 或 index 缓冲参数碎片：

- OpenAI Chat：`delta.tool_calls[].function.arguments`
- Anthropic：`input_json_delta.partial_json`
- Responses：`function_call_arguments.delta`

MVP 不启用工具调用转换，但状态机接口应预留该能力。

关键规则：

- Anthropic 输出必须合成 `message_start`、`content_block_start`、`content_block_delta`、`content_block_stop`、`message_delta`、`message_stop`。
- OpenAI Chat 输出必须合成 `chat.completion.chunk`，最后输出 `data: [DONE]`。
- Responses 输出必须合成 `response.created`、`response.output_item.added`、`response.output_text.delta`、`response.completed`。

### 6.4 取消与错误

上游请求必须绑定客户端 request context：

```go
upstreamReq = upstreamReq.WithContext(r.Context())
```

当客户端断开：

- 取消上游请求。
- 停止读取 SSE。
- 释放连接。

当上游错误：

- 如果还没写响应 header，返回普通 HTTP error。
- 如果已经进入 SSE，写入目标协议对应的 error event，然后结束流。

## 7. Agent 配置到代理

### 7.1 Claude Code

Claude Code 指向本地代理：

```text
ANTHROPIC_BASE_URL=http://127.0.0.1:<port>
ANTHROPIC_API_KEY=<random-local-token>
ANTHROPIC_MODEL=<model>
```

Claude 会请求 `/v1/messages`，代理识别为 Anthropic client protocol。

### 7.2 Codex

Codex 不能纯 env 注入 provider 配置。临时模式使用 `CODEX_HOME` 指向 OS tempdir 下的临时目录，并写入最小 `config.toml`：

```toml
model = "<model>"
model_provider = "code-switch-proxy"

[model_providers.code-switch-proxy]
name = "code-switch proxy"
base_url = "http://127.0.0.1:<port>/v1"
wire_api = "responses"
env_key = "CODE_SWITCH_PROXY_API_KEY"
```

同时注入：

```text
CODEX_HOME=<os temp dir>/code-switch-codex-<pid>
CODE_SWITCH_PROXY_API_KEY=<random-local-token>
```

临时目录放在 `os.TempDir()`，目录权限必须为 `0700`。默认依赖 OS temp 清理；正常退出时可 best-effort 删除。

Daemon 模式下，`cs proxy configure codex` 可以写入用户的 Codex config，使 Codex 长期指向本地代理。

### 7.3 OpenCode

OpenCode 支持 `OPENCODE_CONFIG_CONTENT`。临时模式优先使用内联配置，避免写主配置文件：

```json
{
  "provider": {
    "code-switch-proxy": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "code-switch proxy",
      "options": {
        "baseURL": "http://127.0.0.1:<port>/v1",
        "apiKey": "{env:CODE_SWITCH_PROXY_API_KEY}"
      },
      "models": {
        "<model>": { "name": "<model>" }
      }
    }
  },
  "model": "code-switch-proxy/<model>"
}
```

## 8. 配置与状态文件

代理状态目录：

```text
~/.code-switch/proxy/
```

文件：

- `state.json`：daemon pid、port、当前 route。
- `proxy.log`：daemon 日志。
- `routes.json`：可选，保存 per-agent 默认 route。

状态写入必须使用现有 `writeJSONAtomic` 和文件锁模式，避免并发命令破坏状态。

权限要求：

- `~/.code-switch/proxy/`：`0700`
- `state.json` / route token 文件：`0600`
- 日志默认不记录 request / response body。
- 如需 body debug dump，必须显式 opt-in，且文档提示可能包含 prompt、API key 或隐私数据。
- PID 文件必须处理 stale PID 和 PID reuse，不能盲目 kill 任意同 PID 进程。

## 9. Provider 扩展

代理层引入协议字段后，可接入只提供 OpenAI-compatible 的 provider，例如：

- Qwen / DashScope
- Doubao / Volcengine OpenAI-compatible endpoint
- Moonshot / Kimi OpenAI endpoint
- Baichuan
- 任意自定义 OpenAI-compatible API

自定义 provider 需要可声明：

```json
{
  "base_url": "https://example.com/v1",
  "protocol": "openai-chat",
  "models": ["model-a"]
}
```

## 10. 测试策略

必须新增单元测试和集成式 HTTP 测试。

### 10.1 协议转换单元测试

覆盖：

- Anthropic request -> IR
- IR -> Anthropic request
- OpenAI Chat request -> IR
- IR -> OpenAI Chat request
- Responses request -> IR
- IR -> Responses request
- tool definitions 映射
- tool calls 映射
- stop reason 映射
- usage 映射
- 不支持字段返回错误

### 10.2 SSE 流式测试

用固定 SSE fixture 测试：

- Anthropic stream -> Chat stream
- Chat stream -> Anthropic stream
- Responses stream -> Anthropic stream
- Anthropic stream -> Responses stream
- Chat stream -> Responses stream
- Responses stream -> Chat stream

每个测试验证：

- 事件顺序正确。
- 必须合成的 start/stop 事件存在。
- 文本增量不丢失。
- tool delta 不丢失。
- 最终 done/stop 事件正确。

### 10.3 HTTP 代理测试

使用 `httptest.Server` 模拟上游 provider：

- 非流式请求转发。
- 流式请求转发。
- 上游 401 / 429 / 500。
- 客户端取消 context。
- provider key header 注入。

### 10.4 命令测试

覆盖：

- `cs proxy start --port 0`
- `cs proxy status`
- `cs proxy route set --agent codex --provider minimax-cn`
- `cs proxy route get --agent codex`
- `cs proxy route list`
- `cs proxy stop`
- `cs run codex --provider minimax-cn --dry-run`
- `cs run claude --provider openrouter --dry-run`
- `cs run opencode --provider zhipu-cn --dry-run`

考虑到真实 agent 不适合在单元测试里启动，`run` 命令需要支持 `--dry-run` 输出将要执行的命令、环境变量和临时配置路径。

### 10.5 验证命令

所有实现完成后必须通过：

```bash
go vet ./... && go test ./... && go build -o cs .
```

## 11. 分阶段实现

### 阶段 1：临时前台代理骨架与 Responses → Anthropic 文本桥

- 新增 `cs run codex --provider <anthropic-provider> --dry-run`。
- 支持临时前台代理。
- 支持 Codex 临时 `CODEX_HOME` 配置。
- 支持 Responses request -> IR -> Anthropic request。
- 支持 Anthropic non-stream text response -> IR -> Responses response。
- 对 tool/image/reasoning 请求返回 400。

价值：优先验证最高价值链路：Codex 使用原本不支持的 Anthropic-compatible provider。

### 阶段 2：Responses → Anthropic 流式文本

- 实现 SSE parser/writer。
- 支持 Anthropic stream text -> IR -> Responses stream text。
- 支持 context cancellation 和写失败中止上游。
- 支持 `cs proxy test`。

价值：覆盖 Codex 交互式体验。

### 阶段 3：代理 daemon 与 route 管理

- 新增 `cs proxy start|status|stop`。
- 新增 `cs proxy route set|get|list`。
- 支持随机 local token 到 route 映射。
- 支持 `cs proxy configure codex`。

价值：实现常驻代理，但不扩大协议转换范围。

### 阶段 4：三协议非流式文本转换

- 实现 Anthropic ↔ IR。
- 实现 OpenAI Chat ↔ IR。
- 实现 Responses ↔ IR。
- 支持三种协议的非流式文本请求/响应。

价值：扩大协议覆盖面，同时避免先碰所有流式状态机。

### 阶段 5：三协议流式文本转换

- 实现三种协议的文本流式互转。
- 支持 passthrough 快路径：同协议转发不进入 IR。

价值：覆盖多数 agent 的普通对话体验。

### 阶段 6：工具调用转换

- 实现 tool definitions 映射。
- 实现 tool call streaming delta 映射。
- 对无法支持的工具能力返回明确错误。

价值：让 coding agent 的工具调用场景可用。

### 阶段 7：`cs run` 扩展到三 agent

- 启动 Claude/Codex/OpenCode 子进程。
- Codex 使用 `CODEX_HOME` tempdir。
- OpenCode 使用 `OPENCODE_CONFIG_CONTENT`。
- 支持 `--dry-run`。

价值：实现类似 `ollama run` 的一键体验。

### 阶段 8：Provider 扩展与稳定性

- 增加 OpenAI-compatible 国内 provider。
- 增加 route 持久化。
- 增加 debug log。
- 增加基本 usage 记录。

## 12. 风险与权衡

### 12.1 复杂度风险

三向流式翻译是显著复杂功能，预计新增源码和测试约数千行。必须分阶段实现，不能一次性堆完。

### 12.2 协议语义损失

三种协议不是完全等价。thinking、reasoning、annotations、tool delta、图片等高级字段存在不可逆映射。设计必须明确错误或降级，不能假装完全兼容。

### 12.3 定位风险

代理层可能让项目从轻量配置切换器变重。因此代理层必须保持独立：

- 默认不启动 daemon。
- `switch` 不依赖 proxy。
- 文档清楚区分直连模式和代理模式。

### 12.4 安全风险

本地代理会持有 provider API key。必须：

- 默认只监听 `127.0.0.1`。
- 禁止默认监听 `0.0.0.0`。
- 日志不打印 API key。
- 状态文件不保存明文 API key，只保存 provider 名和 model。
- 上游 key 每次从 app config 读取。
- daemon token 使用随机值，不使用固定 `cs-proxy`。
- 如果未来支持监听 `0.0.0.0`，必须显式危险提示并要求鉴权。

## 13. 成功标准

MVP 成功标准：

1. `cs run codex --provider minimax-cn --dry-run` 能展示临时 `CODEX_HOME`、本地代理地址、随机 token 和 route。
2. 临时前台代理能接收 Codex 的 `/v1/responses` 请求。
3. 代理能把 Responses 非流式文本请求转换为 Anthropic Messages 请求，发送到至少一个 Anthropic-compatible provider 的 mock server。
4. 代理能把 Anthropic Messages 非流式文本响应转换为 Responses 响应。
5. MVP 阶段不启用流式文本；流式路径在阶段 2 完成。
6. tool、image、reasoning 请求返回明确 400 错误，不静默忽略。
7. 所有新增逻辑有单元测试和 HTTP 测试。
8. `go vet ./... && go test ./... && go build -o cs .` 通过。
