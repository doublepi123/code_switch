# Proxy Protocol Gateway MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the first usable proxy bridge so `cs run codex --provider minimax-cn --dry-run` can prepare a temporary Codex session and the proxy can translate non-streaming OpenAI Responses requests to Anthropic Messages upstream requests and back.

**Architecture:** This plan implements only the MVP from `docs/specs/2026-07-04-proxy-protocol-gateway-design.md`: Responses client → typed IR → Anthropic upstream, non-streaming text only. The proxy code is isolated in new `proxy_*` and `protocol_*` files; existing `switch` behavior remains unchanged.

**Tech Stack:** Go 1.22, standard library `net/http`, `httptest`, `flag`, `encoding/json`, existing config resolver and test helpers.

---

## Scope

This plan intentionally does **not** implement daemon mode, SSE streaming, tool calls, images, reasoning, OpenCode launch, Claude launch, or provider expansion. Those are later phases.

## File Structure

- Create: `protocol_ir.go` — typed internal request/response representation and validation errors.
- Create: `protocol_responses.go` — OpenAI Responses non-streaming text request/response adapter.
- Create: `protocol_anthropic.go` — Anthropic Messages non-streaming text request/response adapter.
- Create: `proxy_server.go` — HTTP handler that accepts `/v1/responses`, resolves route, calls upstream, and converts responses.
- Create: `run.go` — `cs run` command, Codex dry-run, temporary Codex config generation helpers.
- Modify: `main.go` — command dispatch, version request list, usage and completions for `run`.
- Test: `proxy_protocol_test.go` — protocol conversion unit tests.
- Test: `proxy_server_test.go` — HTTP proxy tests with `httptest.Server` upstream.
- Test: `run_test.go` — CLI dry-run tests.

## Shared Implementation Notes

- Keep all new code in `package main`.
- Do not save provider API keys in proxy state or dry-run output.
- For MVP, reject unsupported fields with actionable errors instead of silently ignoring them.
- Do not commit changes unless the user explicitly asks. The plan says “Checkpoint” instead of “Commit” to respect repository rules.

---

### Task 1: Add typed IR and validation helpers

**Files:**
- Create: `protocol_ir.go`
- Test: `proxy_protocol_test.go`

- [ ] **Step 1: Write failing IR validation tests**

Add `proxy_protocol_test.go` with:

```go
package main

import (
    "strings"
    "testing"
)

func TestIRRequestValidateTextOnlyAcceptsPlainText(t *testing.T) {
    req := IRRequest{
        Model: "MiniMax-M3",
        Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "Say hi"}}}},
        MaxTokens: 32,
    }
    if err := req.ValidateTextOnly(); err != nil {
        t.Fatalf("ValidateTextOnly returned error: %v", err)
    }
}

func TestIRRequestValidateTextOnlyRejectsToolUse(t *testing.T) {
    req := IRRequest{
        Model: "MiniMax-M3",
        Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartToolUse}}}},
        MaxTokens: 32,
    }
    err := req.ValidateTextOnly()
    if err == nil {
        t.Fatal("ValidateTextOnly returned nil, want error")
    }
    if !strings.Contains(err.Error(), "tool_use") {
        t.Fatalf("error = %q, want mention tool_use", err.Error())
    }
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test -run 'TestIRRequestValidateTextOnly' .
```

Expected: FAIL with undefined `IRRequest`, `IRMessage`, `IRPart`, or `irPartText`.

- [ ] **Step 3: Implement minimal IR types**

Create `protocol_ir.go`:

```go
package main

import "fmt"

const (
    irPartText      = "text"
    irPartToolUse   = "tool_use"
    irPartToolResult = "tool_result"
    irPartImage     = "image"
    irPartReasoning = "reasoning"
)

type IRRequest struct {
    Model     string
    Messages  []IRMessage
    Stream    bool
    MaxTokens int
}

type IRMessage struct {
    Role  string
    Parts []IRPart
}

type IRPart struct {
    Type string
    Text string
}

type IRResponse struct {
    ID         string
    Model      string
    Text       string
    StopReason string
    Usage      *IRUsage
}

type IRUsage struct {
    InputTokens  int
    OutputTokens int
    TotalTokens  int
}

func (req IRRequest) ValidateTextOnly() error {
    if req.Model == "" {
        return fmt.Errorf("model is required")
    }
    if len(req.Messages) == 0 {
        return fmt.Errorf("at least one message is required")
    }
    for i, msg := range req.Messages {
        switch msg.Role {
        case "system", "user", "assistant":
        default:
            return fmt.Errorf("message %d has unsupported role %q", i, msg.Role)
        }
        if len(msg.Parts) == 0 {
            return fmt.Errorf("message %d must contain at least one content part", i)
        }
        for j, part := range msg.Parts {
            if part.Type != irPartText {
                return fmt.Errorf("message %d part %d uses unsupported content type %q in MVP text-only proxy", i, j, part.Type)
            }
        }
    }
    return nil
}
```

- [ ] **Step 4: Run tests and verify pass**

Run:

```bash
go test -run 'TestIRRequestValidateTextOnly' .
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Run:

```bash
git diff -- protocol_ir.go proxy_protocol_test.go
```

Expected: diff only contains IR types and IR validation tests.

---

### Task 2: Add OpenAI Responses adapter

**Files:**
- Create: `protocol_responses.go`
- Modify: `proxy_protocol_test.go`

- [ ] **Step 1: Write failing Responses adapter tests**

Append to `proxy_protocol_test.go`:

```go
func TestResponsesRequestToIRText(t *testing.T) {
    body := []byte(`{"model":"codex-model","input":"Say hi","max_output_tokens":12}`)
    req, err := responsesRequestToIR(body)
    if err != nil {
        t.Fatalf("responsesRequestToIR returned error: %v", err)
    }
    if req.Model != "codex-model" {
        t.Fatalf("Model = %q", req.Model)
    }
    if req.MaxTokens != 12 {
        t.Fatalf("MaxTokens = %d", req.MaxTokens)
    }
    if got := req.Messages[0].Parts[0].Text; got != "Say hi" {
        t.Fatalf("text = %q", got)
    }
}

func TestResponsesRequestToIRRejectsStreamingMVP(t *testing.T) {
    body := []byte(`{"model":"codex-model","input":"Say hi","stream":true}`)
    _, err := responsesRequestToIR(body)
    if err == nil {
        t.Fatal("responsesRequestToIR returned nil, want error")
    }
    if !strings.Contains(err.Error(), "streaming") {
        t.Fatalf("error = %q, want mention streaming", err.Error())
    }
}

func TestIRToResponsesResponse(t *testing.T) {
    data, err := irToResponsesResponse(IRResponse{ID: "msg_1", Model: "MiniMax-M3", Text: "Hi", StopReason: "end_turn", Usage: &IRUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}})
    if err != nil {
        t.Fatalf("irToResponsesResponse returned error: %v", err)
    }
    text := string(data)
    for _, want := range []string{`"object":"response"`, `"status":"completed"`, `"output_text":"Hi"`, `"input_tokens":2`, `"output_tokens":3`} {
        if !strings.Contains(text, want) {
            t.Fatalf("response JSON missing %s: %s", want, text)
        }
    }
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test -run 'TestResponses' .
```

Expected: FAIL with undefined `responsesRequestToIR` and `irToResponsesResponse`.

- [ ] **Step 3: Implement Responses adapter**

Create `protocol_responses.go`:

```go
package main

import (
    "encoding/json"
    "fmt"
)

type responsesRequest struct {
    Model           string `json:"model"`
    Input           any    `json:"input"`
    Stream          bool   `json:"stream,omitempty"`
    MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

func responsesRequestToIR(body []byte) (IRRequest, error) {
    var in responsesRequest
    if err := json.Unmarshal(body, &in); err != nil {
        return IRRequest{}, fmt.Errorf("parse responses request: %w", err)
    }
    if in.Stream {
        return IRRequest{}, fmt.Errorf("streaming Responses requests are not supported by proxy MVP")
    }
    if in.Model == "" {
        return IRRequest{}, fmt.Errorf("responses request model is required")
    }
    text, err := responsesInputText(in.Input)
    if err != nil {
        return IRRequest{}, err
    }
    req := IRRequest{
        Model:     in.Model,
        MaxTokens: in.MaxOutputTokens,
        Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: text}}}},
    }
    return req, req.ValidateTextOnly()
}

func responsesInputText(input any) (string, error) {
    switch v := input.(type) {
    case string:
        if v == "" {
            return "", fmt.Errorf("responses input text is required")
        }
        return v, nil
    case []any:
        var text string
        for _, item := range v {
            m, ok := item.(map[string]any)
            if !ok {
                return "", fmt.Errorf("responses input item must be an object")
            }
            role, _ := m["role"].(string)
            if role != "user" {
                return "", fmt.Errorf("responses input role %q is not supported by proxy MVP", role)
            }
            content, ok := m["content"].([]any)
            if !ok {
                return "", fmt.Errorf("responses input content must be an array")
            }
            for _, part := range content {
                pm, ok := part.(map[string]any)
                if !ok {
                    return "", fmt.Errorf("responses content part must be an object")
                }
                typ, _ := pm["type"].(string)
                if typ != "input_text" {
                    return "", fmt.Errorf("responses content type %q is not supported by proxy MVP", typ)
                }
                s, _ := pm["text"].(string)
                text += s
            }
        }
        if text == "" {
            return "", fmt.Errorf("responses input text is required")
        }
        return text, nil
    default:
        return "", fmt.Errorf("responses input must be a string or text-only input array")
    }
}

func irToResponsesResponse(resp IRResponse) ([]byte, error) {
    id := resp.ID
    if id == "" {
        id = "resp_code_switch"
    }
    out := map[string]any{
        "id":          id,
        "object":      "response",
        "status":      "completed",
        "model":       resp.Model,
        "output_text": resp.Text,
        "output": []any{map[string]any{
            "type": "message",
            "role": "assistant",
            "content": []any{map[string]any{"type": "output_text", "text": resp.Text}},
        }},
    }
    if resp.Usage != nil {
        out["usage"] = map[string]any{
            "input_tokens":  resp.Usage.InputTokens,
            "output_tokens": resp.Usage.OutputTokens,
            "total_tokens":  resp.Usage.TotalTokens,
        }
    }
    return json.Marshal(out)
}
```

- [ ] **Step 4: Run tests and verify pass**

Run:

```bash
go test -run 'TestResponses' .
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Run:

```bash
git diff -- protocol_responses.go proxy_protocol_test.go
```

Expected: diff only contains Responses adapter and tests.

---

### Task 3: Add Anthropic Messages adapter

**Files:**
- Create: `protocol_anthropic.go`
- Modify: `proxy_protocol_test.go`

- [ ] **Step 1: Write failing Anthropic adapter tests**

Append to `proxy_protocol_test.go`:

```go
func TestIRToAnthropicRequest(t *testing.T) {
    req := IRRequest{Model: "MiniMax-M3", MaxTokens: 24, Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "Say hi"}}}}}
    data, err := irToAnthropicRequest(req)
    if err != nil {
        t.Fatalf("irToAnthropicRequest returned error: %v", err)
    }
    text := string(data)
    for _, want := range []string{`"model":"MiniMax-M3"`, `"max_tokens":24`, `"role":"user"`, `"text":"Say hi"`} {
        if !strings.Contains(text, want) {
            t.Fatalf("request JSON missing %s: %s", want, text)
        }
    }
}

func TestAnthropicResponseToIR(t *testing.T) {
    body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
    resp, err := anthropicResponseToIR(body)
    if err != nil {
        t.Fatalf("anthropicResponseToIR returned error: %v", err)
    }
    if resp.ID != "msg_1" || resp.Model != "MiniMax-M3" || resp.Text != "Hi" || resp.StopReason != "end_turn" {
        t.Fatalf("unexpected IR response: %+v", resp)
    }
    if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
        t.Fatalf("usage = %+v, want total 5", resp.Usage)
    }
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
go test -run 'TestIRToAnthropicRequest|TestAnthropicResponseToIR' .
```

Expected: FAIL with undefined `irToAnthropicRequest` and `anthropicResponseToIR`.

- [ ] **Step 3: Implement Anthropic adapter**

Create `protocol_anthropic.go`:

```go
package main

import (
    "encoding/json"
    "fmt"
)

func irToAnthropicRequest(req IRRequest) ([]byte, error) {
    if err := req.ValidateTextOnly(); err != nil {
        return nil, err
    }
    maxTokens := req.MaxTokens
    if maxTokens <= 0 {
        maxTokens = 1024
    }
    messages := make([]map[string]any, 0, len(req.Messages))
    for _, msg := range req.Messages {
        if msg.Role == "system" {
            continue
        }
        content := make([]map[string]any, 0, len(msg.Parts))
        for _, part := range msg.Parts {
            content = append(content, map[string]any{"type": "text", "text": part.Text})
        }
        messages = append(messages, map[string]any{"role": msg.Role, "content": content})
    }
    out := map[string]any{"model": req.Model, "max_tokens": maxTokens, "messages": messages}
    return json.Marshal(out)
}

type anthropicMessageResponse struct {
    ID         string `json:"id"`
    Model      string `json:"model"`
    Content    []struct {
        Type string `json:"type"`
        Text string `json:"text"`
    } `json:"content"`
    StopReason string `json:"stop_reason"`
    Usage      struct {
        InputTokens  int `json:"input_tokens"`
        OutputTokens int `json:"output_tokens"`
    } `json:"usage"`
}

func anthropicResponseToIR(body []byte) (IRResponse, error) {
    var in anthropicMessageResponse
    if err := json.Unmarshal(body, &in); err != nil {
        return IRResponse{}, fmt.Errorf("parse anthropic response: %w", err)
    }
    text := ""
    for _, part := range in.Content {
        if part.Type != "text" {
            return IRResponse{}, fmt.Errorf("anthropic response content type %q is not supported by proxy MVP", part.Type)
        }
        text += part.Text
    }
    usage := &IRUsage{InputTokens: in.Usage.InputTokens, OutputTokens: in.Usage.OutputTokens, TotalTokens: in.Usage.InputTokens + in.Usage.OutputTokens}
    return IRResponse{ID: in.ID, Model: in.Model, Text: text, StopReason: in.StopReason, Usage: usage}, nil
}
```

- [ ] **Step 4: Run tests and verify pass**

Run:

```bash
go test -run 'TestIRToAnthropicRequest|TestAnthropicResponseToIR' .
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Run:

```bash
git diff -- protocol_anthropic.go proxy_protocol_test.go
```

Expected: diff only contains Anthropic adapter and tests.

---

### Task 4: Add proxy HTTP server for `/v1/responses`

**Files:**
- Create: `proxy_server.go`
- Test: `proxy_server_test.go`

- [ ] **Step 1: Write failing proxy server test**

Create `proxy_server_test.go`:

```go
package main

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func TestProxyResponsesToAnthropicNonStreaming(t *testing.T) {
    var upstreamPath string
    var upstreamAuth string
    var upstreamBody map[string]any
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        upstreamPath = r.URL.Path
        upstreamAuth = r.Header.Get("Authorization")
        if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
            t.Fatalf("decode upstream body: %v", err)
        }
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`))
    }))
    defer upstream.Close()

    handler := newProxyHandler(ProxyRoute{Provider: "minimax-cn", Model: "MiniMax-M3", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")
    req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"codex-model","input":"Say hi"}`))
    req.Header.Set("Authorization", "Bearer local-token")
    rec := httptest.NewRecorder()

    handler.ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
    }
    if upstreamPath != "/v1/messages" {
        t.Fatalf("upstream path = %q", upstreamPath)
    }
    if upstreamAuth != "Bearer provider-key" {
        t.Fatalf("upstream auth = %q", upstreamAuth)
    }
    if upstreamBody["model"] != "MiniMax-M3" {
        t.Fatalf("upstream model = %v", upstreamBody["model"])
    }
    if !strings.Contains(rec.Body.String(), `"output_text":"Hi"`) {
        t.Fatalf("response body = %s", rec.Body.String())
    }
}
```

- [ ] **Step 2: Run test and verify failure**

Run:

```bash
go test -run TestProxyResponsesToAnthropicNonStreaming .
```

Expected: FAIL with undefined `newProxyHandler`, `ProxyRoute`, or `protocolAnthropicMessages`.

- [ ] **Step 3: Implement proxy handler**

Create `proxy_server.go`:

```go
package main

import (
    "bytes"
    "fmt"
    "io"
    "net/http"
    "strings"
)

type ProviderProtocol string

const (
    protocolAnthropicMessages ProviderProtocol = "anthropic-messages"
    protocolOpenAIChat        ProviderProtocol = "openai-chat"
    protocolOpenAIResponses   ProviderProtocol = "openai-responses"
)

type ProxyRoute struct {
    Provider         string
    Model            string
    UpstreamProtocol ProviderProtocol
    UpstreamBaseURL  string
    LocalToken       string
}

func newProxyHandler(route ProxyRoute, providerKey string) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if route.LocalToken != "" {
            if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); got != route.LocalToken {
                http.Error(w, "invalid code-switch proxy token", http.StatusUnauthorized)
                return
            }
        }
        if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
            http.Error(w, "code-switch proxy MVP supports POST /v1/responses only", http.StatusNotFound)
            return
        }
        body, err := io.ReadAll(r.Body)
        if err != nil {
            http.Error(w, fmt.Sprintf("read request body: %v", err), http.StatusBadRequest)
            return
        }
        irReq, err := responsesRequestToIR(body)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        if route.Model != "" {
            irReq.Model = route.Model
        }
        upstreamBody, err := irToAnthropicRequest(irReq)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        upstreamURL := strings.TrimRight(route.UpstreamBaseURL, "/") + "/v1/messages"
        upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
        if err != nil {
            http.Error(w, fmt.Sprintf("create upstream request: %v", err), http.StatusInternalServerError)
            return
        }
        upstreamReq.Header.Set("Content-Type", "application/json")
        if providerKey != "" {
            upstreamReq.Header.Set("Authorization", "Bearer "+providerKey)
        }
        upstreamResp, err := http.DefaultClient.Do(upstreamReq)
        if err != nil {
            http.Error(w, fmt.Sprintf("upstream request failed: %v", err), http.StatusBadGateway)
            return
        }
        defer upstreamResp.Body.Close()
        upstreamRespBody, err := io.ReadAll(upstreamResp.Body)
        if err != nil {
            http.Error(w, fmt.Sprintf("read upstream response: %v", err), http.StatusBadGateway)
            return
        }
        if upstreamResp.StatusCode < 200 || upstreamResp.StatusCode >= 300 {
            http.Error(w, string(upstreamRespBody), upstreamResp.StatusCode)
            return
        }
        irResp, err := anthropicResponseToIR(upstreamRespBody)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadGateway)
            return
        }
        responseBody, err := irToResponsesResponse(irResp)
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(responseBody)
    })
}
```

- [ ] **Step 4: Run proxy test and verify pass**

Run:

```bash
go test -run TestProxyResponsesToAnthropicNonStreaming .
```

Expected: PASS.

- [ ] **Step 5: Checkpoint**

Run:

```bash
git diff -- proxy_server.go proxy_server_test.go
```

Expected: diff only contains proxy handler and HTTP test.

---

### Task 5: Add `cs run codex --dry-run`

**Files:**
- Create: `run.go`
- Modify: `main.go`
- Test: `run_test.go`

- [ ] **Step 1: Write failing CLI dry-run test**

Create `run_test.go`:

```go
package main

import (
    "bytes"
    "path/filepath"
    "strings"
    "testing"
)

func TestRunCodexDryRun(t *testing.T) {
    home := t.TempDir()
    t.Setenv("HOME", home)
    cfg := AppConfig{Providers: map[string]StoredProvider{"minimax-cn": {Name: "MiniMax CN Token Plan", BaseURL: "https://api.minimaxi.com/anthropic", Model: "MiniMax-M3", APIKey: "sk-secret", AuthEnv: "ANTHROPIC_AUTH_TOKEN"}}}
    if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
        t.Fatalf("write config: %v", err)
    }
    out := &bytes.Buffer{}
    err := runWithIO([]string{"run", "codex", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out)
    if err != nil {
        t.Fatalf("run dry-run returned error: %v", err)
    }
    got := out.String()
    for _, want := range []string{"agent: codex", "provider: minimax-cn", "upstream_protocol: anthropic-messages", "CODEX_HOME=", "CODE_SWITCH_PROXY_API_KEY=", "codex config.toml"} {
        if !strings.Contains(got, want) {
            t.Fatalf("dry-run output missing %q:\n%s", want, got)
        }
    }
    if strings.Contains(got, "sk-secret") {
        t.Fatalf("dry-run leaked API key:\n%s", got)
    }
}
```

- [ ] **Step 2: Run test and verify failure**

Run:

```bash
go test -run TestRunCodexDryRun .
```

Expected: FAIL with unknown command `run`.

- [ ] **Step 3: Implement `cmdRun` dry-run**

Create `run.go`:

```go
package main

import (
    "crypto/rand"
    "encoding/hex"
    "flag"
    "fmt"
    "io"
    "os"
    "path/filepath"
)

func cmdRun(args []string, out io.Writer) error {
    if len(args) == 0 {
        return fmt.Errorf("usage: code-switch run <agent> --provider <provider> [--model model-id] [--dry-run]")
    }
    agent, err := parseAgentName(args[0])
    if err != nil {
        return err
    }
    if agent != agentCodex {
        return fmt.Errorf("run MVP supports codex only")
    }
    fs := flag.NewFlagSet("run", flag.ContinueOnError)
    fs.SetOutput(os.Stderr)
    provider := fs.String("provider", "", "provider to route through the local proxy")
    model := fs.String("model", "", "override model id")
    dryRun := fs.Bool("dry-run", false, "print launch configuration without starting codex")
    if err := fs.Parse(args[1:]); err != nil {
        return err
    }
    if *provider == "" {
        return fmt.Errorf("run requires --provider")
    }
    if !*dryRun {
        return fmt.Errorf("run without --dry-run is not implemented in proxy MVP")
    }
    pa, _, _, err := resolveProviderAndKeyForAgent(agentClaude, *provider, "", *model)
    if err != nil {
        return err
    }
    localToken, err := randomProxyToken()
    if err != nil {
        return err
    }
    tempHome := filepath.Join(os.TempDir(), "code-switch-codex-<pid>")
    codexConfig := renderProxyCodexConfig(pa.Model)
    fmt.Fprintf(out, "agent: codex\n")
    fmt.Fprintf(out, "provider: %s\n", pa.Provider)
    fmt.Fprintf(out, "model: %s\n", pa.Model)
    fmt.Fprintf(out, "upstream_protocol: %s\n", protocolAnthropicMessages)
    fmt.Fprintf(out, "proxy_base_url: http://127.0.0.1:<port>/v1\n")
    fmt.Fprintf(out, "CODEX_HOME=%s\n", tempHome)
    fmt.Fprintf(out, "CODE_SWITCH_PROXY_API_KEY=%s\n", localToken)
    fmt.Fprintf(out, "codex config.toml:\n%s", codexConfig)
    return nil
}

func randomProxyToken() (string, error) {
    var b [16]byte
    if _, err := rand.Read(b[:]); err != nil {
        return "", fmt.Errorf("generate proxy token: %w", err)
    }
    return "csproxy-" + hex.EncodeToString(b[:]), nil
}

func renderProxyCodexConfig(model string) string {
    if model == "" {
        model = "default"
    }
    return fmt.Sprintf("model = %q\nmodel_provider = %q\n\n[model_providers.code-switch-proxy]\nname = %q\nbase_url = %q\nwire_api = %q\nenv_key = %q\n", model, "code-switch-proxy", "code-switch proxy", "http://127.0.0.1:<port>/v1", "responses", "CODE_SWITCH_PROXY_API_KEY")
}
```

- [ ] **Step 4: Register command in `main.go`**

Modify `main.go`:

```go
case "run":
    return cmdRun(args[1:], out)
```

Add `run` to `isVersionRequest` subcommand list. Add `run` to usage and shell completion constants near existing command names.

- [ ] **Step 5: Run CLI test and verify pass**

Run:

```bash
go test -run TestRunCodexDryRun .
```

Expected: PASS.

- [ ] **Step 6: Checkpoint**

Run:

```bash
git diff -- run.go run_test.go main.go
```

Expected: diff only contains `run` command wiring, dry-run helper, and tests.

---

### Task 6: Final MVP verification

**Files:**
- Verify all files changed in Tasks 1-5.

- [ ] **Step 1: Run focused tests**

Run:

```bash
go test -run 'TestIRRequestValidateTextOnly|TestResponses|TestIRToAnthropicRequest|TestAnthropicResponseToIR|TestProxyResponsesToAnthropicNonStreaming|TestRunCodexDryRun' .
```

Expected: PASS.

- [ ] **Step 2: Run full required verification**

Run:

```bash
go vet ./... && go test ./... && go build -o cs .
```

Expected: all commands exit 0.

- [ ] **Step 3: Inspect diff for secrets and scope**

Run:

```bash
git diff -- protocol_ir.go protocol_responses.go protocol_anthropic.go proxy_server.go run.go proxy_protocol_test.go proxy_server_test.go run_test.go main.go
```

Expected: no API keys, no unrelated refactors, no daemon/streaming/tool-call implementation.

- [ ] **Step 4: Update user-facing documentation if implementation proceeds beyond dry-run**

If the implementation adds non-dry-run launch behavior in a later plan, update README with a short “Proxy mode” section. This MVP dry-run plan does not require README changes.

---

## Self-Review

- Spec coverage: This plan covers the spec MVP only: Codex `/v1/responses` non-streaming text to Anthropic Messages upstream, dry-run Codex launch config, token redaction, and HTTP mock tests.
- Intentional gaps: daemon mode, streaming, tool calls, Claude/OpenCode launch, and full three-way conversion are explicitly out of MVP scope and must get separate plans.
- Red-flag scan: No task contains open-ended implementation gaps; each code-changing step includes concrete code.
- Type consistency: `IRRequest`, `IRResponse`, `ProxyRoute`, `ProviderProtocol`, `responsesRequestToIR`, `irToAnthropicRequest`, and `newProxyHandler` are introduced before use in later tasks.
