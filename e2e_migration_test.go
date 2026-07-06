package main

// e2e_migration_test.go — 阶段6c 端到端 + 旧配置迁移测试。
//
// 本文件专注于以下四条链路，按 TDD 先写测试；如果因生产代码缺口而
// 失败，请返回失败详情，不要改生产代码：
//
//  1. claude -> chat-only provider 的完整流式 + 工具调用链路（mock
//     OpenAI Chat 上游）：验证上游路径 / Authorization / 请求模型 /
//     工具定义 / stream=true，客户端收到 Anthropic SSE，包含
//     text/tool_use，usage token 保真。
//  2. codex -> anthropic-only provider 完整链路（mock Anthropic 上游）：
//     验证转发到 /v1/messages，Anthropic-Version/header/token/工具
//     定义，响应转换为 Responses，usage 保真。
//  3. opencode -> openai-responses provider 完整链路：OpenAI Chat 入站
//     -> Responses 上游，验证路径/模型/工具调用/usage。
//  4. 旧配置迁移测试：无 Protocol 的 StoredProvider 加载默认
//     anthropic-messages；无 Token 的 ProxyRouteConfig 加载自动补全并
//     写回；旧 codex preset 行为等价；旧 proxy.routes 配置兼容。
//
// 约束：package main；不新增依赖；尽量复用现有 helpers（e2eHome /
// e2eWriteAppConfig / startAnthropicUpstream / startOpenAIChatUpstream /
// decodeStreamFixture / encodeStreamFixture / assertStreamText）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 通用辅助
// ---------------------------------------------------------------------------

// e2eMigrationCapture 是一个可记录上游请求的捕获器，覆盖三种协议的字段
// 需求。它与 proxy_server_test.go 的 upstreamCapture 字段兼容，但这里
// 单独定义以便捕获 Responses 上游路径（现有 upstreamCapture 没有针对
// Responses 的专用工厂）。
type e2eMigrationCapture struct {
	path             string
	method           string
	auth             string
	contentType      string
	anthropicVersion string
	body             []byte
}

// startResponsesUpstream 返回一个模拟 OpenAI Responses API 的上游，记录
// 最后一次请求。respBody 为空时返回一个简单的 completed 文本响应。
func startResponsesUpstream(t *testing.T, respBody string) (*httptest.Server, *e2eMigrationCapture) {
	t.Helper()
	if respBody == "" {
		respBody = `{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output_text":"Hi","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`
	}
	cap := &e2eMigrationCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.method = r.Method
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// startAnthropicStreamingUpstream 返回一个模拟 Anthropic 流式上游，
// 按顺序写入 fixture 后关闭。fixture 复用 protocol_stream_test.go 中的
// anthropicStreamFixture / chatStreamFixture。
func startStreamingUpstream(t *testing.T, contentType, fixture string) (*httptest.Server, *e2eMigrationCapture) {
	t.Helper()
	cap := &e2eMigrationCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.method = r.Method
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.anthropicVersion = r.Header.Get(proxyAnthropicVersionHeader)
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, fixture)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// e2eMigrationHome 隔离 HOME 到临时目录，返回 home 路径。
func e2eMigrationHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// e2eMigrationSeedCustomProvider 写入一个指向 mock 上游的自定义 provider
// 到 ~/.code-switch/config.json，并返回写入的 cfg 便于后续断言/修改。
func e2eMigrationSeedCustomProvider(t *testing.T, home, name, upstreamURL, authEnv string) AppConfig {
	t.Helper()
	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			name: {
				Name:    name,
				BaseURL: upstreamURL,
				Model:   "mock-model",
				APIKey:  "mock-key",
				AuthEnv: authEnv,
			},
		},
	}
	e2eWriteAppConfig(t, home, cfg)
	return cfg
}

// ---------------------------------------------------------------------------
// 1. claude -> chat-only provider：完整流式 + 工具调用链路
// ---------------------------------------------------------------------------

// TestE2EMigration_ClaudeToChatOnlyProviderStreamingWithTools 验证 claude
// 客户端通过 Anthropic Messages 入站 (/v1/messages, stream=true) -> 代理
// 翻译为 OpenAI Chat 上游 (/v1/chat/completions, stream=true) -> 客户端
// 收到 Anthropic SSE。覆盖：上游路径、Authorization、请求模型被改写、
// 工具定义转发、stream=true，客户端 SSE 包含 text + tool_use，usage
// token 保真。
func TestE2EMigration_ClaudeToChatOnlyProviderStreamingWithTools(t *testing.T) {
	home := e2eMigrationHome(t)

	// OpenAI Chat 流式上游：返回一个 text 增量 + 一个 tool_call。
	const chatStreamWithTool = "data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"hi\"}"}}]},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}` + "\n\n" +
		"data: [DONE]\n\n"

	upstream, cap := startStreamingUpstream(t, "text/event-stream", chatStreamWithTool)
	e2eMigrationSeedCustomProvider(t, home, "e2e-chat-stream", upstream.URL, "ANTHROPIC_AUTH_TOKEN")

	// 配置 claude 路由 -> openai-chat 上游协议。
	e2eMustRun(t, []string{
		"proxy", "configure", "claude",
		"--provider", "e2e-chat-stream",
		"--protocol", "openai-chat",
		"--host", "127.0.0.1",
		"--port", "0",
	})

	const token = "csproxy-e2e-claude-chat"
	inst, err := prepareProxyServe("claude", "", 0, token)
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	baseURL := inst.state.BaseURL
	srvDone := make(chan struct{})
	go func() {
		_ = inst.server.Serve(inst.ln)
		close(srvDone)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
		select {
		case <-srvDone:
		case <-time.After(2 * time.Second):
		}
		_ = removeProxyRuntimeState()
	})

	// 发送一个带工具定义的 Anthropic Messages 流式请求。
	anthReq := `{"model":"client-model","max_tokens":64,"stream":true,"tools":[{"name":"lookup","description":"look up","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],"messages":[{"role":"user","content":[{"type":"text","text":"Say hi and call lookup"}]}]}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/messages", strings.NewReader(anthReq))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forward status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}

	// 上游必须被调用在 /v1/chat/completions。
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", cap.path)
	}
	// Authorization 必须是 provider key（mock-key）。
	if cap.auth != "Bearer mock-key" {
		t.Fatalf("upstream auth = %q, want Bearer mock-key", cap.auth)
	}
	// 上游请求体必须是 OpenAI Chat 形状，模型被改写为路由模型，stream=true。
	var upReq map[string]any
	if err := json.Unmarshal(cap.body, &upReq); err != nil {
		t.Fatalf("unmarshal upstream body: %v\n%s", err, cap.body)
	}
	if m, _ := upReq["model"].(string); m != "mock-model" {
		t.Fatalf("upstream model = %q, want mock-model\n%s", m, cap.body)
	}
	if stream, _ := upReq["stream"].(bool); !stream {
		t.Fatalf("upstream stream = false, want true\n%s", cap.body)
	}
	// 工具定义必须被转发到上游（OpenAI Chat tools[]）。
	tools, ok := upReq["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("upstream tools missing or empty\n%s", cap.body)
	}
	first, _ := tools[0].(map[string]any)
	fn, _ := first["function"].(map[string]any)
	if fn == nil || fn["name"] != "lookup" {
		t.Fatalf("upstream tool[0].function.name = %v, want lookup\n%s", fn, cap.body)
	}

	// 客户端收到的必须是 Anthropic SSE 流。
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("client content-type = %q, want text/event-stream", ct)
	}
	events := decodeStreamFixture(t, protocolAnthropicMessages, string(body))

	// 必须包含 text 增量 "Hi"。
	assertStreamText(t, events, "Hi")

	// 必须包含 tool_use 块（tool_start + tool_delta + tool_stop）。
	var sawToolStart, sawToolStop bool
	for _, e := range events {
		if e.Type == streamEventToolStart && e.ToolID == "call_1" && e.ToolName == "lookup" {
			sawToolStart = true
		}
		if e.Type == streamEventToolStop && e.ToolID == "call_1" {
			sawToolStop = true
		}
	}
	if !sawToolStart {
		t.Fatalf("client SSE missing tool_start for call_1/lookup; events=%#v", events)
	}
	if !sawToolStop {
		t.Fatalf("client SSE missing tool_stop for call_1; events=%#v", events)
	}

	// usage token 保真：上游 OpenAI Chat 流在最后一个 chunk 携带
	// usage (prompt_tokens=4, completion_tokens=6, total_tokens=10)。
	// 经 IR -> Anthropic SSE 编码后，客户端解码出的 Anthropic SSE
	// 事件中应能体现这些 usage token。
	//
	// Anthropic SSE 的 message_delta.usage 应承载从上游流式 usage
	// 事件转换来的 token 计数。streamEventUsage 携带完整 IRUsage，
	// 编码器必须在最终 message_delta 中写出，解码回 IR 时 token
	// 计数仍应保持一致。
	var gotInput, gotOutput int
	for _, e := range events {
		if e.Type == streamEventUsage && e.Usage != nil {
			gotInput = e.Usage.InputTokens
			gotOutput = e.Usage.OutputTokens
		}
	}
	if gotInput != 4 {
		t.Fatalf("client SSE usage input_tokens = %d, want 4; events=%#v", gotInput, events)
	}
	if gotOutput != 6 {
		t.Fatalf("client SSE usage output_tokens = %d, want 6; events=%#v", gotOutput, events)
	}
}

// TestE2EMigration_ClaudeToChatOnlyProviderNonStreamingWithTools 验证
// 非流式场景下 claude -> chat-only 的工具调用翻译。上游返回一个带
// tool_calls 的非流式 Chat 响应，客户端应收到 Anthropic Messages 响应，
// content 包含 tool_use 块，usage 保真。
func TestE2EMigration_ClaudeToChatOnlyProviderNonStreamingWithTools(t *testing.T) {
	home := e2eMigrationHome(t)

	// 非流式 Chat 响应，带 tool_calls。
	chatResp := `{"id":"chatcmpl_1","object":"chat.completion","model":"chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"hi\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`
	upstream, cap := startOpenAIChatUpstream(t, 0, chatResp)
	e2eMigrationSeedCustomProvider(t, home, "e2e-chat-nonstream", upstream.URL, "ANTHROPIC_AUTH_TOKEN")

	e2eMustRun(t, []string{
		"proxy", "configure", "claude",
		"--provider", "e2e-chat-nonstream",
		"--protocol", "openai-chat",
		"--host", "127.0.0.1",
		"--port", "0",
	})

	const token = "csproxy-e2e-claude-chat-nonstream"
	inst, err := prepareProxyServe("claude", "", 0, token)
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	baseURL := inst.state.BaseURL
	srvDone := make(chan struct{})
	go func() {
		_ = inst.server.Serve(inst.ln)
		close(srvDone)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
		select {
		case <-srvDone:
		case <-time.After(2 * time.Second):
		}
		_ = removeProxyRuntimeState()
	})

	anthReq := `{"model":"client-model","max_tokens":64,"tools":[{"name":"lookup","description":"look up","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],"messages":[{"role":"user","content":[{"type":"text","text":"Call lookup"}]}]}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/messages", strings.NewReader(anthReq))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forward status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}

	// 上游路径与 auth。
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", cap.path)
	}
	if cap.auth != "Bearer mock-key" {
		t.Fatalf("upstream auth = %q, want Bearer mock-key", cap.auth)
	}
	// 上游请求模型被改写。
	var upReq map[string]any
	if err := json.Unmarshal(cap.body, &upReq); err != nil {
		t.Fatalf("unmarshal upstream body: %v\n%s", err, cap.body)
	}
	if m, _ := upReq["model"].(string); m != "mock-model" {
		t.Fatalf("upstream model = %q, want mock-model", m)
	}

	// 客户端收到 Anthropic Messages 响应，stop_reason=tool_use，content
	// 包含 tool_use 块，usage 保真。
	var anthResp map[string]any
	if err := json.Unmarshal(body, &anthResp); err != nil {
		t.Fatalf("unmarshal client response: %v\n%s", err, body)
	}
	if anthResp["type"] != "message" {
		t.Fatalf("client response type = %v, want message\n%s", anthResp["type"], body)
	}
	if anthResp["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use\n%s", anthResp["stop_reason"], body)
	}
	content, _ := anthResp["content"].([]any)
	var sawToolUse bool
	for _, c := range content {
		block, _ := c.(map[string]any)
		if block == nil {
			continue
		}
		if block["type"] == irPartToolUse {
			sawToolUse = true
			if block["id"] != "call_1" {
				t.Fatalf("tool_use id = %v, want call_1", block["id"])
			}
			if block["name"] != "lookup" {
				t.Fatalf("tool_use name = %v, want lookup", block["name"])
			}
		}
	}
	if !sawToolUse {
		t.Fatalf("client response missing tool_use content block\n%s", body)
	}
	// usage 保真。
	usage, _ := anthResp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("client response missing usage\n%s", body)
	}
	if usage["input_tokens"] != float64(3) {
		t.Fatalf("usage input_tokens = %v, want 3", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(5) {
		t.Fatalf("usage output_tokens = %v, want 5", usage["output_tokens"])
	}
}

// ---------------------------------------------------------------------------
// 2. codex -> anthropic-only provider：完整链路
// ---------------------------------------------------------------------------

// TestE2EMigration_CodexToAnthropicOnlyProviderFullChain 验证 codex 客户端
// 通过 OpenAI Responses 入站 (/v1/responses) -> 代理翻译为 Anthropic
// Messages 上游 (/v1/messages)。覆盖：转发到 /v1/messages，
// Anthropic-Version header，Authorization Bearer，工具定义转发，响应
// 转换为 Responses，usage 保真。
func TestE2EMigration_CodexToAnthropicOnlyProviderFullChain(t *testing.T) {
	home := e2eMigrationHome(t)

	// Anthropic 上游响应，带 tool_use 块。
	anthResp := `{"id":"msg_1","type":"message","role":"assistant","model":"anthropic-model","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"hi"}}],"stop_reason":"tool_use","usage":{"input_tokens":4,"output_tokens":7}}`
	upstream, cap := startAnthropicUpstream(t, 0, anthResp)
	e2eMigrationSeedCustomProvider(t, home, "e2e-anthropic-codex", upstream.URL, "ANTHROPIC_AUTH_TOKEN")

	e2eMustRun(t, []string{
		"proxy", "configure", "codex",
		"--provider", "e2e-anthropic-codex",
		"--protocol", "anthropic-messages",
		"--host", "127.0.0.1",
		"--port", "0",
	})

	const token = "csproxy-e2e-codex-anthropic"
	inst, err := prepareProxyServe("codex", "", 0, token)
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	baseURL := inst.state.BaseURL
	srvDone := make(chan struct{})
	go func() {
		_ = inst.server.Serve(inst.ln)
		close(srvDone)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
		select {
		case <-srvDone:
		case <-time.After(2 * time.Second):
		}
		_ = removeProxyRuntimeState()
	})

	// Codex Responses 入站请求，带工具定义。
	respReq := `{"model":"codex-model","input":"Call lookup","tools":[{"type":"function","name":"lookup","description":"look up","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/responses", strings.NewReader(respReq))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forward status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}

	// 上游必须是 /v1/messages。
	if cap.path != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", cap.path)
	}
	// Anthropic-Version header 必须被设置。
	if cap.anthropicVersion != proxyAnthropicVersionValue {
		t.Fatalf("upstream anthropic-version = %q, want %q", cap.anthropicVersion, proxyAnthropicVersionValue)
	}
	// Authorization 必须是 Bearer mock-key。
	if cap.auth != "Bearer mock-key" {
		t.Fatalf("upstream auth = %q, want Bearer mock-key", cap.auth)
	}
	// Content-Type 必须是 application/json。
	if cap.contentType != "application/json" {
		t.Fatalf("upstream content-type = %q, want application/json", cap.contentType)
	}
	// 上游请求体必须是 Anthropic Messages 形状，模型被改写为 mock-model。
	var upReq anthropicRequestBody
	if err := json.Unmarshal(cap.body, &upReq); err != nil {
		t.Fatalf("unmarshal upstream body: %v\n%s", err, cap.body)
	}
	if upReq.Model != "mock-model" {
		t.Fatalf("upstream model = %q, want mock-model\n%s", upReq.Model, cap.body)
	}
	// 工具定义必须被转发到 Anthropic tools[]。
	if len(upReq.Tools) == 0 {
		t.Fatalf("upstream tools missing\n%s", cap.body)
	}
	if upReq.Tools[0].Name != "lookup" {
		t.Fatalf("upstream tool[0].name = %q, want lookup", upReq.Tools[0].Name)
	}

	// 客户端收到 Responses 形状响应。
	var clientResp map[string]any
	if err := json.Unmarshal(body, &clientResp); err != nil {
		t.Fatalf("unmarshal client response: %v\n%s", err, body)
	}
	if clientResp["object"] != "response" {
		t.Fatalf("client response object = %v, want response\n%s", clientResp["object"], body)
	}
	// 工具调用必须出现在 output 数组中（function_call）。
	output, _ := clientResp["output"].([]any)
	var sawFunctionCall bool
	for _, item := range output {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		if obj["type"] == "function_call" {
			sawFunctionCall = true
			if obj["call_id"] != "call_1" {
				t.Fatalf("function_call call_id = %v, want call_1", obj["call_id"])
			}
			if obj["name"] != "lookup" {
				t.Fatalf("function_call name = %v, want lookup", obj["name"])
			}
		}
	}
	if !sawFunctionCall {
		t.Fatalf("client response missing function_call in output\n%s", body)
	}
	// usage 保真。
	usage, _ := clientResp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("client response missing usage\n%s", body)
	}
	if usage["input_tokens"] != float64(4) {
		t.Fatalf("usage input_tokens = %v, want 4", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(7) {
		t.Fatalf("usage output_tokens = %v, want 7", usage["output_tokens"])
	}
}

// ---------------------------------------------------------------------------
// 3. opencode -> openai-responses provider：完整链路
// ---------------------------------------------------------------------------

// TestE2EMigration_OpencodeToResponsesProviderFullChain 验证 opencode 客户端
// 通过 OpenAI Chat 入站 (/v1/chat/completions) -> 代理翻译为 OpenAI
// Responses 上游 (/v1/responses)。覆盖：路径、模型改写、工具调用
// 翻译、usage 保真。
func TestE2EMigration_OpencodeToResponsesProviderFullChain(t *testing.T) {
	home := e2eMigrationHome(t)

	// Responses 上游响应，带 function_call。
	responsesResp := `{"id":"resp_1","object":"response","status":"completed","model":"resp-model","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hi\"}"}],"output_text":"","usage":{"input_tokens":5,"output_tokens":8,"total_tokens":13}}`
	upstream, cap := startResponsesUpstream(t, responsesResp)
	e2eMigrationSeedCustomProvider(t, home, "e2e-responses-opencode", upstream.URL, "OPENAI_API_KEY")

	e2eMustRun(t, []string{
		"proxy", "configure", "opencode",
		"--provider", "e2e-responses-opencode",
		"--protocol", "openai-responses",
		"--host", "127.0.0.1",
		"--port", "0",
	})

	const token = "csproxy-e2e-opencode-responses"
	inst, err := prepareProxyServe("opencode", "", 0, token)
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	baseURL := inst.state.BaseURL
	srvDone := make(chan struct{})
	go func() {
		_ = inst.server.Serve(inst.ln)
		close(srvDone)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
		select {
		case <-srvDone:
		case <-time.After(2 * time.Second):
		}
		_ = removeProxyRuntimeState()
	})

	// OpenCode Chat 入站请求，带工具定义。
	chatReq := `{"model":"client-model","max_tokens":32,"messages":[{"role":"user","content":"Call lookup"}],"tools":[{"type":"function","function":{"name":"lookup","description":"look up","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}]}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", strings.NewReader(chatReq))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forward status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}

	// 上游必须是 /v1/responses。
	if cap.path != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", cap.path)
	}
	// Authorization 必须是 Bearer mock-key。
	if cap.auth != "Bearer mock-key" {
		t.Fatalf("upstream auth = %q, want Bearer mock-key", cap.auth)
	}
	// 上游请求体必须是 Responses 形状，模型被改写为 mock-model。
	var upReq map[string]any
	if err := json.Unmarshal(cap.body, &upReq); err != nil {
		t.Fatalf("unmarshal upstream body: %v\n%s", err, cap.body)
	}
	if m, _ := upReq["model"].(string); m != "mock-model" {
		t.Fatalf("upstream model = %q, want mock-model\n%s", m, cap.body)
	}

	// 客户端收到 OpenAI Chat 响应。
	var chatResp map[string]any
	if err := json.Unmarshal(body, &chatResp); err != nil {
		t.Fatalf("unmarshal client response: %v\n%s", err, body)
	}
	if chatResp["object"] != "chat.completion" {
		t.Fatalf("client response object = %v, want chat.completion\n%s", chatResp["object"], body)
	}
	// 工具调用必须出现在 choices[0].message.tool_calls。
	choices, _ := chatResp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("client response missing choices\n%s", body)
	}
	choice0, _ := choices[0].(map[string]any)
	message, _ := choice0["message"].(map[string]any)
	if message == nil {
		t.Fatalf("client response missing message\n%s", body)
	}
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) == 0 {
		t.Fatalf("client response missing tool_calls\n%s", body)
	}
	tc0, _ := toolCalls[0].(map[string]any)
	fn, _ := tc0["function"].(map[string]any)
	if fn == nil || fn["name"] != "lookup" {
		t.Fatalf("tool_calls[0].function.name = %v, want lookup\n%s", fn, body)
	}
	if tc0["id"] != "call_1" {
		t.Fatalf("tool_calls[0].id = %v, want call_1", tc0["id"])
	}
	// finish_reason 必须是 tool_calls。
	if choice0["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v, want tool_calls", choice0["finish_reason"])
	}
	// usage 保真。
	usage, _ := chatResp["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("client response missing usage\n%s", body)
	}
	if usage["prompt_tokens"] != float64(5) {
		t.Fatalf("usage prompt_tokens = %v, want 5", usage["prompt_tokens"])
	}
	if usage["completion_tokens"] != float64(8) {
		t.Fatalf("usage completion_tokens = %v, want 8", usage["completion_tokens"])
	}
	if usage["total_tokens"] != float64(13) {
		t.Fatalf("usage total_tokens = %v, want 13", usage["total_tokens"])
	}
}

// ---------------------------------------------------------------------------
// 4. 旧配置迁移测试
// ---------------------------------------------------------------------------

// TestLegacy_StoredProviderWithoutProtocolDefaultsAnthropicMessages 验证：
// 一个无 Protocol 的 StoredProvider，通过 providerProtocol() 解析时
// 应默认为 anthropic-messages。
func TestLegacy_StoredProviderWithoutProtocolDefaultsAnthropicMessages(t *testing.T) {
	stored := StoredProvider{
		Name:    "legacy-provider",
		BaseURL: "https://legacy.example.com/anthropic",
		APIKey:  "sk-legacy",
	}
	// 没有设置 Protocol。
	if stored.Protocol != "" {
		t.Fatalf("setup invariant: Protocol = %q, want empty", stored.Protocol)
	}
	got := stored.providerProtocol()
	if got != protocolAnthropicMessages {
		t.Fatalf("providerProtocol() = %q, want anthropic-messages", got)
	}

	// 显式设置为 openai-chat 也应被尊重。
	stored.Protocol = protocolOpenAIChat
	if got := stored.providerProtocol(); got != protocolOpenAIChat {
		t.Fatalf("providerProtocol() = %q, want openai-chat", got)
	}
}

// TestLegacy_StoredProviderWithoutProtocolLoadedFromConfig 验证：写一个
// 无 Protocol 字段的旧 config.json，loadAppConfigFrom 加载后该 provider
// 仍可用（map 中存在），且通过 providerProtocol() 解析得到
// anthropic-messages 默认值。
func TestLegacy_StoredProviderWithoutProtocolLoadedFromConfig(t *testing.T) {
	home := e2eMigrationHome(t)
	dir := filepath.Join(home, ".code-switch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// 手写一个旧式 config（无 protocol 字段）。
	legacyJSON := `{
  "providers": {
    "legacy-anthropic": {
      "name": "Legacy Anthropic",
      "baseUrl": "https://legacy.example.com/anthropic",
      "apiKey": "sk-legacy",
      "model": "legacy-model"
    }
  }
}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg, err := loadAppConfigFrom(path)
	if err != nil {
		t.Fatalf("loadAppConfigFrom: %v", err)
	}
	stored, ok := cfg.Providers["legacy-anthropic"]
	if !ok {
		t.Fatal("legacy-anthropic provider missing after load")
	}
	// 加载后 Protocol 字段可能仍为空，但 providerProtocol() 必须默认到
	// anthropic-messages。
	if got := stored.providerProtocol(); got != protocolAnthropicMessages {
		t.Fatalf("providerProtocol() = %q, want anthropic-messages", got)
	}
}

// TestLegacy_ProxyRouteConfigWithoutTokenAutoCompletesAndWritesBack 验证：
// 一个无 Token 的 ProxyRouteConfig，经过 prepareProxyServe 后，路由
// token 被自动补全并写回 config.json。
func TestLegacy_ProxyRouteConfigWithoutTokenAutoCompletesAndWritesBack(t *testing.T) {
	e2eMigrationHome(t)

	// 先 set-key 让 provider 存在。
	e2eMustRun(t, []string{"set-key", "zhipu-cn", "sk-legacy-route"})

	cfg, path, err := loadAppConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// 手写一个旧式 proxy.routes，token 字段为空。
	cfg.Proxy = &ProxyConfig{
		Host: "127.0.0.1",
		Routes: map[string]ProxyRouteConfig{
			"codex": {
				Agent:            "codex",
				Provider:         "zhipu-cn",
				Model:            "glm-5.2",
				UpstreamProtocol: string(protocolAnthropicMessages),
				// Token 故意留空
			},
		},
	}
	if err := writeJSONAtomic(path, cfg); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	// prepareProxyServe 应触发 ensureProxyRouteTokens 写回。
	inst, err := prepareProxyServe("", "127.0.0.1", 0, "csproxy-legacy-instance-token")
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()

	// 重新加载并验证 token 已被写回。
	reloaded, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	tok := reloaded.Proxy.Routes["codex"].Token
	if tok == "" {
		t.Fatal("route token was not auto-completed after prepareProxyServe")
	}
	if !strings.HasPrefix(tok, "csproxy-route-") {
		t.Fatalf("auto-completed token prefix = %q, want csproxy-route-", tok)
	}
}

// TestLegacy_ProxyRouteConfigWithoutUpstreamProtocolDefaultsPerAgent 验证：
// 一个无 UpstreamProtocol 的 ProxyRouteConfig，通过 ResolveProtocol() 解析
// 时应使用 defaultProxyProtocolForAgent(agent)。codex 默认为
// anthropic-messages，claude 默认为 openai-responses。
func TestLegacy_ProxyRouteConfigWithoutUpstreamProtocolDefaultsPerAgent(t *testing.T) {
	reg := defaultProtocolRegistry()

	// codex: 无 UpstreamProtocol -> 默认 anthropic-messages。
	codexRoute := ProxyRouteConfig{Agent: "codex", Provider: "zhipu-cn", Model: "glm-5.2"}
	got, err := codexRoute.ResolveProtocol(reg)
	if err != nil {
		t.Fatalf("codex ResolveProtocol: %v", err)
	}
	if got != protocolAnthropicMessages {
		t.Fatalf("codex default protocol = %q, want anthropic-messages", got)
	}

	// claude: 无 UpstreamProtocol -> 默认 openai-responses。
	claudeRoute := ProxyRouteConfig{Agent: "claude", Provider: "zhipu-cn", Model: "glm-5.2"}
	got, err = claudeRoute.ResolveProtocol(reg)
	if err != nil {
		t.Fatalf("claude ResolveProtocol: %v", err)
	}
	if got != protocolOpenAIResponses {
		t.Fatalf("claude default protocol = %q, want openai-responses", got)
	}

	// 显式协议必须被尊重。
	codexRoute.UpstreamProtocol = string(protocolOpenAIChat)
	got, err = codexRoute.ResolveProtocol(reg)
	if err != nil {
		t.Fatalf("codex explicit ResolveProtocol: %v", err)
	}
	if got != protocolOpenAIChat {
		t.Fatalf("codex explicit protocol = %q, want openai-chat", got)
	}
}

// TestLegacy_OldCodexPresetBehaviorEquivalent 验证：使用旧 codex preset
// 配置路径（无 UpstreamProtocol 字段的 ProxyRouteConfig，走
// defaultProxyProtocolForAgent(codex) = anthropic-messages 默认）与显式
// 声明 anthropic-messages 的路由行为等价——两者都能通过
// buildProxyRouteFromConfig 解析并生成等价的 ProxyRoute（除 token 外）。
//
// 注意：buildProxyRouteFromConfig 的第一个参数既是路由 map 的 key，也
// 被 validateProxyAgentProtocol 当作 agent 名校验，所以路由 key 必须
// 是受支持的 agent 名 (codex/claude/opencode)。本测试分别用两个独立
// config 各装一个 "codex" 路由来对比。
func TestLegacy_OldCodexPresetBehaviorEquivalent(t *testing.T) {
	// resolveSwitchPreset 要求 provider 在 providerPresets 或 cfg.Providers
	// 里；zhipu-cn 是内置 preset，所以无需 set-key。
	t.Run("legacy_vs_explicit_codex_route", func(t *testing.T) {
		e2eMigrationHome(t)

		base := &AppConfig{Providers: map[string]StoredProvider{}}

		// 旧式路由：无 UpstreamProtocol。
		legacyCfg := *base
		legacyCfg.Proxy = &ProxyConfig{
			Host: "127.0.0.1",
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:    "codex",
					Provider: "zhipu-cn",
					Model:    "glm-5.2",
					// UpstreamProtocol 故意留空
				},
			},
		}
		legacyRoute, err := buildProxyRouteFromConfig("codex", &legacyCfg, "local-token")
		if err != nil {
			t.Fatalf("buildProxyRouteFromConfig (legacy): %v", err)
		}

		// 新式路由：显式 anthropic-messages。
		explicitCfg := *base
		explicitCfg.Proxy = &ProxyConfig{
			Host: "127.0.0.1",
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:            "codex",
					Provider:         "zhipu-cn",
					Model:            "glm-5.2",
					UpstreamProtocol: string(protocolAnthropicMessages),
				},
			},
		}
		explicitRoute, err := buildProxyRouteFromConfig("codex", &explicitCfg, "local-token")
		if err != nil {
			t.Fatalf("buildProxyRouteFromConfig (explicit): %v", err)
		}

		// 两者上游协议必须一致。
		if legacyRoute.UpstreamProtocol != explicitRoute.UpstreamProtocol {
			t.Fatalf("upstream protocol mismatch: legacy=%q explicit=%q",
				legacyRoute.UpstreamProtocol, explicitRoute.UpstreamProtocol)
		}
		if legacyRoute.UpstreamProtocol != protocolAnthropicMessages {
			t.Fatalf("legacy upstream protocol = %q, want anthropic-messages",
				legacyRoute.UpstreamProtocol)
		}
		// 两者上游 base URL、模型必须一致。
		if legacyRoute.UpstreamBaseURL != explicitRoute.UpstreamBaseURL {
			t.Fatalf("upstream base URL mismatch: legacy=%q explicit=%q",
				legacyRoute.UpstreamBaseURL, explicitRoute.UpstreamBaseURL)
		}
		if legacyRoute.Model != explicitRoute.Model {
			t.Fatalf("model mismatch: legacy=%q explicit=%q",
				legacyRoute.Model, explicitRoute.Model)
		}
		if legacyRoute.Provider != explicitRoute.Provider {
			t.Fatalf("provider mismatch: legacy=%q explicit=%q",
				legacyRoute.Provider, explicitRoute.Provider)
		}
	})
}

func TestE2EMigration_CodexDirectOpenAIChatWritesChatWireAPI(t *testing.T) {
	home := e2eMigrationHome(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	e2eWriteAppConfig(t, home, AppConfig{Providers: map[string]StoredProvider{
		"deepseek": {APIKey: "sk-deepseek"},
	}})

	out := e2eMustRun(t, []string{"switch", "deepseek", "--agent", "codex", "--via", "direct", "--codex-dir", codexDir})
	if !strings.Contains(out, "direct") {
		t.Fatalf("switch output missing direct mode:\n%s", out)
	}
	config, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	if !strings.Contains(string(config), `base_url = "https://api.deepseek.com/v1"`) {
		t.Fatalf("codex config missing chat endpoint base_url:\n%s", config)
	}
	if !strings.Contains(string(config), `wire_api = "responses"`) {
		t.Fatalf("codex config wire_api should be responses (codex deprecated chat wire_api):\n%s", config)
	}
}

// TestLegacy_OldProxyRoutesConfigCompatible 验证：一个手写的旧式
// proxy.routes 配置（缺少 token、缺少 UpstreamProtocol、host 为空）
// 经过 loadAppConfigFrom -> normalizeAppConfig -> prepareProxyServe
// 后能被规范化并成功提供代理服务。
func TestLegacy_OldProxyRoutesConfigCompatible(t *testing.T) {
	home := e2eMigrationHome(t)
	dir := filepath.Join(home, ".code-switch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// 手写旧式 config：proxy.host 为空，route 无 token、无 protocol。
	legacyJSON := `{
  "providers": {
    "zhipu-cn": {
      "name": "Zhipu",
      "baseUrl": "https://open.bigmodel.cn/api/anthropic",
      "apiKey": "sk-legacy-routes",
      "model": "glm-5.2"
    }
  },
  "proxy": {
    "routes": {
      "codex": {
        "agent": "codex",
        "provider": "zhipu-cn",
        "model": "glm-5.2"
      }
    }
  }
}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	// 加载 -> normalizeAppConfig 应把 host 填成 127.0.0.1。
	cfg, err := loadAppConfigFrom(path)
	if err != nil {
		t.Fatalf("loadAppConfigFrom: %v", err)
	}
	if cfg.Proxy == nil {
		t.Fatal("proxy block missing after load")
	}
	if cfg.Proxy.Host != "127.0.0.1" {
		t.Fatalf("normalized host = %q, want 127.0.0.1", cfg.Proxy.Host)
	}
	route, ok := cfg.Proxy.Routes["codex"]
	if !ok {
		t.Fatal("codex route missing after load")
	}
	if route.Token != "" {
		t.Fatalf("pre-serve token = %q, want empty (auto-completion happens at serve)", route.Token)
	}

	// prepareProxyServe 应成功（token 自动补全 + 路由解析）。
	inst, err := prepareProxyServe("", "", 0, "csproxy-legacy-routes-instance")
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()

	// 验证写回后的 config：token 已生成、host 已规范化。
	reloaded, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Proxy == nil {
		t.Fatal("proxy block missing after reload")
	}
	if reloaded.Proxy.Host != "127.0.0.1" {
		t.Fatalf("reloaded host = %q, want 127.0.0.1", reloaded.Proxy.Host)
	}
	if tok := reloaded.Proxy.Routes["codex"].Token; tok == "" {
		t.Fatal("codex route token not persisted after serve")
	}
}

// TestLegacy_LegacyMinimaxProviderMigratesToMinimaxCN 验证：旧版 "minimax"
// provider key 在 loadAppConfigFrom 时被迁移为 "minimax-cn"（当后者不
// 存在且旧 key 有非空 APIKey 时）。
func TestLegacy_LegacyMinimaxProviderMigratesToMinimaxCN(t *testing.T) {
	home := e2eMigrationHome(t)
	dir := filepath.Join(home, ".code-switch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacyJSON := `{
  "providers": {
    "minimax": {
      "name": "MiniMax",
      "baseUrl": "https://api.minimaxi.com/anthropic",
      "apiKey": "sk-legacy-minimax",
      "model": "MiniMax-M2"
    }
  }
}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg, err := loadAppConfigFrom(path)
	if err != nil {
		t.Fatalf("loadAppConfigFrom: %v", err)
	}
	if _, ok := cfg.Providers["minimax"]; ok {
		t.Fatal("legacy minimax key should be deleted after migration")
	}
	migrated, ok := cfg.Providers["minimax-cn"]
	if !ok {
		t.Fatal("minimax-cn key should be created after migration")
	}
	if migrated.APIKey != "sk-legacy-minimax" {
		t.Fatalf("migrated apiKey = %q, want sk-legacy-minimax", migrated.APIKey)
	}
	if migrated.Model != "MiniMax-M2" {
		t.Fatalf("migrated model = %q, want MiniMax-M2", migrated.Model)
	}
}

// TestLegacy_LegacyClaudeSwitchConfigMigrates 验证：旧版 ~/.claude-switch/
// config.json 在没有 ~/.code-switch/config.json 时被迁移并写回新路径，
// 且 legacy minimax key 也被迁移。
func TestLegacy_LegacyClaudeSwitchConfigMigrates(t *testing.T) {
	home := e2eMigrationHome(t)
	// 写一个旧版 ~/.claude-switch/config.json。
	legacyDir := filepath.Join(home, ".claude-switch")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacyJSON := `{
  "providers": {
    "minimax": {
      "name": "MiniMax",
      "baseUrl": "https://api.minimaxi.com/anthropic",
      "apiKey": "sk-claude-switch-legacy",
      "model": "MiniMax-M2"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(legacyDir, "config.json"), []byte(legacyJSON), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("loadAppConfig: %v", err)
	}
	// 旧 minimax key 应被迁移为 minimax-cn。
	if _, ok := cfg.Providers["minimax"]; ok {
		t.Fatal("legacy minimax key should be deleted")
	}
	migrated, ok := cfg.Providers["minimax-cn"]
	if !ok {
		t.Fatal("minimax-cn should be migrated from legacy .claude-switch")
	}
	if migrated.APIKey != "sk-claude-switch-legacy" {
		t.Fatalf("migrated apiKey = %q, want sk-claude-switch-legacy", migrated.APIKey)
	}

	// 新路径应已被写入。
	newPath := filepath.Join(home, ".code-switch", "config.json")
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new config path not written: %v", err)
	}
	// 重新加载应能读到迁移后的内容。
	cfg2, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := cfg2.Providers["minimax-cn"]; !ok {
		t.Fatal("minimax-cn missing after reload")
	}
}
