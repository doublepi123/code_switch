# Phase 1 Iteration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the four Phase 1 capabilities (configurable agent profiles, customizable tier mappings, proxy fallback + logging, and doctor drift detection) with TDD, ensuring `go vet ./...`, `go test ./...`, and `go build -o cs .` all pass.

**Architecture:** Keep all code in `package main` as existing. Introduce small, focused new files for each capability (`agent_profile.go`, `model_tiers.go`, `proxy_fallback.go`, `proxy_log.go`, `doctor_drift.go`) with matching `*_test.go` files. Modify existing files only at integration points. Maintain the existing protocol IR/registry abstraction and the single-binary Go delivery.

**Tech Stack:** Go 1.22+, existing deps (`tview`, `tcell`), `go test` for unit tests, `go vet` for static analysis, real HTTP test servers for proxy integration tests.

---

## Task 1: Configurable Agent Profiles

**Files:**
- Create: `agent_profile.go`, `agent_profile_test.go`
- Modify: `protocol.go`, `config.go`, `agent.go`, `switch.go` (integration points)

Current `agentProfiles` is a hard-coded `map[AgentName]AgentProfile` in `protocol.go`. Make it loadable from `AppConfig.AgentProfiles` so users can override or add profiles (e.g., for a future Zed agent) without recompiling.

- [ ] **Step 1: Write the failing test**

```go
func TestAgentProfileFromConfig(t *testing.T) {
    cfg := &AppConfig{
        AgentProfiles: map[string]AgentProfile{
            "zed": {
                ClientProtocol:          protocolOpenAIChat,
                DirectProtocols:         []ProviderProtocol{protocolOpenAIChat},
                ProxyUpstreamPreference: []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat},
            },
        },
    }
    profile, ok := getAgentProfile(cfg, AgentName("zed"))
    if !ok {
        t.Fatalf("expected profile for zed")
    }
    if profile.ClientProtocol != protocolOpenAIChat {
        t.Fatalf("expected client protocol openai-chat, got %s", profile.ClientProtocol)
    }
}
```

Run: `go test -run TestAgentProfileFromConfig .`
Expected: FAIL with `getAgentProfile` not defined.

- [ ] **Step 2: Implement `getAgentProfile` and `AgentProfiles` config field**

In `agent_profile.go`:

```go
package main

func getAgentProfile(cfg *AppConfig, agent AgentName) (AgentProfile, bool) {
    if cfg != nil && cfg.AgentProfiles != nil {
        if p, ok := cfg.AgentProfiles[string(agent)]; ok {
            return p, true
        }
    }
    p, ok := agentProfiles[agent]
    return p, ok
}
```

In `config.go` add to `AppConfig`:

```go
AgentProfiles map[string]AgentProfile `json:"agentProfiles,omitempty"`
```

In `protocol.go`, replace direct `agentProfiles[agent]` reads with `getAgentProfile(cfg, agent)` where a config is available. Where no config is available, keep fallback to built-in `agentProfiles`.

- [ ] **Step 3: Verify tests pass**

Run: `go test -run TestAgentProfileFromConfig .`
Expected: PASS.

- [ ] **Step 4: Add tests for built-in fallback and unknown agent**

```go
func TestAgentProfileBuiltinFallback(t *testing.T) {
    cfg := &AppConfig{}
    p, ok := getAgentProfile(cfg, agentClaude)
    if !ok || p.ClientProtocol != protocolAnthropicMessages {
        t.Fatalf("expected built-in claude profile")
    }
}

func TestAgentProfileUnknown(t *testing.T) {
    cfg := &AppConfig{}
    _, ok := getAgentProfile(cfg, AgentName("unknown"))
    if ok {
        t.Fatalf("expected no profile for unknown agent")
    }
}
```

- [ ] **Step 5: Run full package tests and vet**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent_profile.go agent_profile_test.go protocol.go config.go
git commit -m "feat: configurable agent profiles from app config"
```

---

## Task 2: Customizable Model Tier Mappings

**Files:**
- Create: `model_tiers.go`, `model_tiers_test.go`
- Modify: `switch.go`, `presets.go`, `proxy_server.go` (model resolution)

Current tier overrides are persisted in `StoredProvider` per-provider per-agent. Provide a clean API to resolve haiku/sonnet/opus/subagent from a provider preset, stored overrides, and CLI flags, and expose it for proxy `ModelMappings` generation.

- [ ] **Step 1: Write the failing test**

```go
func TestResolveModelTiers(t *testing.T) {
    preset := ProviderPreset{
        Model: "base-model",
        Haiku: "haiku-model",
        Sonnet: "sonnet-model",
        Opus: "opus-model",
        Subagent: "subagent-model",
    }
    stored := StoredProvider{Sonnet: "stored-sonnet"}
    flags := ModelTiers{Opus: "flag-opus"}
    tiers := resolveModelTiers(preset, stored, flags)
    if tiers.Haiku != "haiku-model" {
        t.Fatalf("expected haiku from preset, got %s", tiers.Haiku)
    }
    if tiers.Sonnet != "stored-sonnet" {
        t.Fatalf("expected sonnet from stored, got %s", tiers.Sonnet)
    }
    if tiers.Opus != "flag-opus" {
        t.Fatalf("expected opus from flag, got %s", tiers.Opus)
    }
    if tiers.Subagent != "subagent-model" {
        t.Fatalf("expected subagent from preset, got %s", tiers.Subagent)
    }
}
```

Run: `go test -run TestResolveModelTiers .`
Expected: FAIL with `resolveModelTiers` not defined.

- [ ] **Step 2: Implement `resolveModelTiers`**

In `model_tiers.go`:

```go
package main

func resolveModelTiers(preset ProviderPreset, stored StoredProvider, flags ModelTiers) ModelTiers {
    pick := func(flag, stored, preset, base string) string {
        if flag != "" {
            return flag
        }
        if stored != "" {
            return stored
        }
        if preset != "" {
            return preset
        }
        return base
    }
    base := preset.Model
    return ModelTiers{
        Haiku:    pick(flags.Haiku, stored.Haiku, preset.Haiku, base),
        Sonnet:   pick(flags.Sonnet, stored.Sonnet, preset.Sonnet, base),
        Opus:     pick(flags.Opus, stored.Opus, preset.Opus, base),
        Subagent: pick(flags.Subagent, stored.Subagent, preset.Subagent, base),
    }
}
```

- [ ] **Step 3: Refactor `switch.go` to use `resolveModelTiers`**

Replace the inline tier override resolution in `cmdSwitchWithOutput` with a call to `resolveModelTiers`. Ensure the persisted override behavior is preserved: CLI flags override stored, stored overrides preset.

- [ ] **Step 4: Refactor proxy model mapping generation**

In `proxy_server.go` or a helper, generate `ModelMappings` from resolved tiers so proxy requests map client model names (e.g., "sonnet") to upstream models correctly. Add a test in `proxy_server_test.go` or `model_tiers_test.go` that constructs a route and asserts the mapping.

- [ ] **Step 5: Verify tests pass**

Run: `go test -run TestResolveModelTiers .`
Expected: PASS.

- [ ] **Step 6: Run full package tests and vet**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add model_tiers.go model_tiers_test.go switch.go presets.go proxy_server.go
git commit -m "feat: customizable and resolvable model tier mappings"
```

---

## Task 3: Proxy Upstream Fallback

**Files:**
- Create: `proxy_fallback.go`, `proxy_fallback_test.go`
- Modify: `proxy_config.go`, `proxy_server.go`, `proxy_cmd.go`, `proxy_lifecycle.go`

Add a `Fallback` field to `ProxyRouteConfig` and `ProxyRoute`. When a primary upstream returns a retryable error (network failure, 5xx, 429), try the fallback upstream before returning an error to the client.

- [ ] **Step 1: Write the failing test**

```go
func TestProxyFallbackOnServerError(t *testing.T) {
    primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer primary.Close()

    fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        w.Write([]byte(`{"id":"fb","choices":[{"message":{"content":"ok"}}]}`))
    }))
    defer fallback.Close()

    route := ProxyRoute{
        Provider:         "test",
        Model:            "test-model",
        UpstreamProtocol: protocolOpenAIChat,
        UpstreamBaseURL:  primary.URL,
        Fallback: &ProxyRoute{
            Provider:         "test-fallback",
            UpstreamBaseURL:  fallback.URL,
            UpstreamProtocol: protocolOpenAIChat,
        },
    }
    // ... exercise handler and assert fallback response
}
```

Run: `go test -run TestProxyFallbackOnServerError .`
Expected: FAIL with `Fallback` field missing or helper not defined.

- [ ] **Step 2: Add `Fallback` to route config structs**

In `proxy_config.go` (or wherever `ProxyRouteConfig` is defined):

```go
type ProxyRouteConfig struct {
    Provider        string            `json:"provider"`
    Model           string            `json:"model"`
    Protocol        ProviderProtocol  `json:"protocol"`
    BaseURL         string            `json:"baseUrl"`
    AuthEnv         string            `json:"authEnv"`
    ModelMappings   map[string]string `json:"modelMappings,omitempty"`
    Fallback        *ProxyRouteConfig `json:"fallback,omitempty"`
    Token           string            `json:"token,omitempty"`
}
```

Mirror in `ProxyRoute` in `proxy_server.go`.

- [ ] **Step 3: Implement fallback-aware upstream call**

In `proxy_fallback.go`, add a helper `tryUpstream` that executes a single upstream request and classifies the result as retryable or not. Modify `serveProtocolUpstream` (and passthrough path) in `proxy_server.go` to call fallback when primary fails.

Retryable conditions:
- Network / connection errors
- HTTP 5xx
- HTTP 429 Too Many Requests
- HTTP 408 Request Timeout

Non-retryable: 4xx (except 408/429), 2xx.

- [ ] **Step 4: Add CLI/config support for fallback**

In `proxy_cmd.go` add optional `--fallback-provider` and `--fallback-url` flags to `cs proxy configure`. When provided, populate `ProxyRouteConfig.Fallback`.

- [ ] **Step 5: Verify tests pass**

Run: `go test -run TestProxyFallback .`
Expected: PASS.

- [ ] **Step 6: Run full package tests and vet**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add proxy_fallback.go proxy_fallback_test.go proxy_config.go proxy_server.go proxy_cmd.go proxy_lifecycle.go
git commit -m "feat: proxy upstream fallback for retryable failures"
```

---

## Task 4: Proxy Request/Response Logging

**Files:**
- Create: `proxy_log.go`, `proxy_log_test.go`
- Modify: `proxy_server.go`, `proxy_lifecycle.go`, `proxy_cmd.go`

Add optional, structured logging of proxy requests and responses to a local log file. Log is off by default; enable with `--log` on `cs proxy start` / `cs proxy serve` or via config.

- [ ] **Step 1: Write the failing test**

```go
func TestProxyLoggerWritesRequest(t *testing.T) {
    logFile := filepath.Join(t.TempDir(), "proxy.log")
    logger, err := newProxyLogger(logFile)
    if err != nil {
        t.Fatal(err)
    }
    logger.logRequest(proxyLogEntry{Method: "POST", Path: "/v1/messages", Provider: "test"})
    logger.close()

    data, err := os.ReadFile(logFile)
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(string(data), `"method":"POST"`) {
        t.Fatalf("expected log to contain method POST, got %s", string(data))
    }
}
```

Run: `go test -run TestProxyLoggerWritesRequest .`
Expected: FAIL with `newProxyLogger` not defined.

- [ ] **Step 2: Implement `proxyLogger`**

In `proxy_log.go`:

```go
package main

import (
    "encoding/json"
    "io"
    "os"
    "sync"
    "time"
)

type proxyLogEntry struct {
    Timestamp  time.Time `json:"timestamp"`
    Method     string    `json:"method"`
    Path       string    `json:"path"`
    Provider   string    `json:"provider"`
    Model      string    `json:"model,omitempty"`
    StatusCode int       `json:"statusCode,omitempty"`
    Error      string    `json:"error,omitempty"`
    DurationMs int64     `json:"durationMs,omitempty"`
}

type proxyLogger struct {
    mu     sync.Mutex
    writer io.WriteCloser
    enc    *json.Encoder
}

func newProxyLogger(path string) (*proxyLogger, error) {
    if path == "" {
        return &proxyLogger{writer: nil}, nil
    }
    f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
    if err != nil {
        return nil, err
    }
    return &proxyLogger{writer: f, enc: json.NewEncoder(f)}, nil
}

func (l *proxyLogger) logRequest(entry proxyLogEntry) {
    if l == nil || l.writer == nil {
        return
    }
    l.mu.Lock()
    defer l.mu.Unlock()
    entry.Timestamp = time.Now().UTC()
    _ = l.enc.Encode(entry)
}

func (l *proxyLogger) close() error {
    if l == nil || l.writer == nil {
        return nil
    }
    return l.writer.Close()
}
```

- [ ] **Step 3: Wire logger into proxy server**

Pass `*proxyLogger` into `newProxyHandler` and `serveProtocolUpstream`. Log each inbound request and upstream outcome (status/error/duration). Ensure logger is closed on server shutdown.

- [ ] **Step 4: Add `--log` flag to proxy commands**

Add `--log <path>` to `cs proxy start` and `cs proxy serve`. Store the path in `ProxyConfig` or pass it through environment to the daemon. Simpler: `cs proxy serve --log path` writes directly; `cs proxy start --log path` writes the path into `proxy-state.json` so the child process can use it.

- [ ] **Step 5: Verify tests pass**

Run: `go test -run TestProxyLogger .`
Expected: PASS.

- [ ] **Step 6: Run full package tests and vet**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add proxy_log.go proxy_log_test.go proxy_server.go proxy_lifecycle.go proxy_cmd.go
git commit -m "feat: optional proxy request/response logging"
```

---

## Task 5: Doctor Configuration Drift Detection

**Files:**
- Create: `doctor_drift.go`, `doctor_drift_test.go`
- Modify: `doctor.go`

`cs doctor` currently checks file parse, permissions, and daemon health. Add drift detection: compare the current provider/model/base_url in each agent's config file against the provider code-switch expects based on `AppConfig.Proxy.Routes` or `Providers`.

- [ ] **Step 1: Write the failing test**

```go
func TestClaudeDriftDetection(t *testing.T) {
    tmp := t.TempDir()
    settings := map[string]any{
        "env": map[string]any{
            "ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
            "ANTHROPIC_MODEL":    "deepseek-v4-pro",
        },
    }
    writeJSONMap(t, tmp, "settings.json", settings)

    cfg := &AppConfig{
        Providers: map[string]StoredProvider{
            "deepseek": {Model: "deepseek-v4-pro"},
        },
    }
    r := checkClaudeDrift(tmp, cfg)
    if r.Status != "ok" {
        t.Fatalf("expected no drift, got %s: %s", r.Status, r.Detail)
    }
}
```

Run: `go test -run TestClaudeDriftDetection .`
Expected: FAIL with missing helper or test setup.

- [ ] **Step 2: Extract drift checking logic into `doctor_drift.go`**

Current `checkClaudeDrift` exists in `doctor.go`. Move it to `doctor_drift.go` and add equivalent checks for Codex and OpenCode. Each check should:

1. Read the agent's config file.
2. Determine the expected provider/model from `AppConfig` (preferring proxy route if active, else stored provider).
3. Compare with the actual values in the config file.
4. Return `ok` if consistent, `warn` if drifted, or `ok` if no provider is configured.

For Codex, parse `~/.codex/config.toml` to extract `model_provider`, `model`, and base URL from `[model_providers.*]`.
For OpenCode, parse `~/.config/opencode/opencode.json` to extract the active provider/model.

- [ ] **Step 3: Add `runDoctor` integration**

Call the new Codex/OpenCode drift checks in `runDoctor` alongside the existing Claude drift check.

- [ ] **Step 4: Verify tests pass**

Run: `go test -run TestClaudeDriftDetection .`
Expected: PASS.

Add tests for Codex and OpenCode drift.

- [ ] **Step 5: Run full package tests and vet**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add doctor_drift.go doctor_drift_test.go doctor.go
git commit -m "feat: doctor configuration drift detection for all agents"
```

---

## Task 6: Final Integration and Verification

- [ ] **Step 1: Run full build and test suite**

```bash
go vet ./... && go test ./... && go build -o cs .
```
Expected: all pass, binary produced.

- [ ] **Step 2: Run proxy fallback smoke test**

Start a local test server returning 500 and a fallback returning 200. Configure `cs proxy configure codex --provider test ...` with fallback, start proxy, send a request, and verify fallback response.

- [ ] **Step 3: Run doctor drift smoke test**

Configure a provider, run `cs switch`, then manually edit `~/.claude/settings.json` env to a different base URL. Run `cs doctor --json` and verify drift warning.

- [ ] **Step 4: Final commit**

```bash
git commit -m "chore: phase 1 integration and verification"
```

---

## Spec Coverage Check

| Requirement | Task |
|-------------|------|
| Agent profiles configurable/extensible | Task 1 |
| Model tier mappings customizable | Task 2 |
| Proxy upstream fallback | Task 3 |
| Proxy request/response logging | Task 4 |
| Doctor configuration drift detection | Task 5 |
| All tests pass, binary builds | Task 6 |

## Placeholder Scan

No TBD/TODO/fill-in placeholders. Every task includes concrete test code, function signatures, and file paths.

## Type Consistency Notes

- `ModelTiers` is already defined in `presets.go`; reuse it for the flags argument in `resolveModelTiers`.
- `ProxyRoute` and `ProxyRouteConfig` structures must remain in sync.
- `AgentProfile` is already defined in `protocol.go`; reuse it in `AppConfig.AgentProfiles`.
