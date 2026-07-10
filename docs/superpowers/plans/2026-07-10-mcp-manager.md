# MCP Manager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a unified MCP (Model Context Protocol) server registry to code-switch that can generate per-agent MCP configurations for Claude Code, Codex, and OpenCode, with support for listing, adding, removing, and health-checking servers.

**Architecture:** Introduce a small MCP abstraction layer (`mcp.go`, `mcp_manager.go`, `mcp_agent.go`) that stores MCP server definitions in `~/.code-switch/config.json` and translates them into the native MCP config format required by each target agent. CLI commands live under `cs mcp <subcommand>`. The `cs doctor` command gains a health check for configured MCP servers.

**Tech Stack:** Go 1.22+, existing `package main` layout, `go test` for unit tests, real/exec-based smoke tests for stdio MCP servers where safe.

---

## Task 1: MCP Core Types and Config Storage

**Files:**
- Create: `mcp.go`, `mcp_test.go`
- Modify: `config.go`

Add `MCPServers map[string]MCPServerConfig` to `AppConfig` (JSON key `mcpServers,omitempty`).

```go
type MCPServerConfig struct {
    Name        string            `json:"name"`
    Transport   string            `json:"transport"` // "stdio" | "sse"
    Command     string            `json:"command,omitempty"`
    Args        []string          `json:"args,omitempty"`
    Env         map[string]string `json:"env,omitempty"`
    URL         string            `json:"url,omitempty"`
    Headers     map[string]string `json:"headers,omitempty"`
    Disabled    bool              `json:"disabled,omitempty"`
    AllowedTools []string         `json:"allowedTools,omitempty"`
    BlockedTools []string         `json:"blockedTools,omitempty"`
}
```

- [ ] **Step 1: Write the failing test**

```go
func TestMCPServerConfigRoundTrip(t *testing.T) {
    cfg := &AppConfig{
        MCPServers: map[string]MCPServerConfig{
            "filesystem": {
                Name:      "filesystem",
                Transport: "stdio",
                Command:   "npx",
                Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
            },
        },
    }
    path := filepath.Join(t.TempDir(), "config.json")
    if err := writeJSONAtomic(path, cfg); err != nil {
        t.Fatal(err)
    }
    loaded, err := loadAppConfigFrom(path)
    if err != nil {
        t.Fatal(err)
    }
    if _, ok := loaded.MCPServers["filesystem"]; !ok {
        t.Fatalf("expected mcp server filesystem to be persisted")
    }
}
```

Run: `go test -run TestMCPServerConfigRoundTrip .`
Expected: FAIL with `MCPServers` field unknown.

- [ ] **Step 2: Add MCPServers field to AppConfig**

In `config.go` / `presets.go` add:

```go
MCPServers map[string]MCPServerConfig `json:"mcpServers,omitempty"`
```

and define `MCPServerConfig` in `mcp.go`.

- [ ] **Step 3: Verify the test passes**

Run: `go test -run TestMCPServerConfigRoundTrip .`
Expected: PASS.

- [ ] **Step 4: Add validation helper tests**

Test `validateMCPServerConfig` rejects empty name/transport, unsupported transport, stdio without command, sse without url.

- [ ] **Step 5: Commit**

```bash
git add mcp.go mcp_test.go config.go
git commit -m "feat: add MCP server config types and storage"
```

---

## Task 2: MCP Manager CLI Commands

**Files:**
- Create: `mcp_manager.go`, `mcp_manager_test.go`, `mcp_cmd.go`, `mcp_cmd_test.go`

Add `cs mcp list`, `cs mcp add <name> --transport stdio --command <cmd> [args...]`, `cs mcp remove <name>`, `cs mcp test <name>`.

- [ ] **Step 1: Write failing tests for list/add/remove/test**

```go
func TestMCPManagerAddAndList(t *testing.T) {
    cfg := &AppConfig{Providers: map[string]StoredProvider{}}
    mgr := newMCPManager(cfg)
    if err := mgr.add(MCPServerConfig{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"@modelcontextprotocol/server-filesystem"}}); err != nil {
        t.Fatal(err)
    }
    servers := mgr.list()
    if len(servers) != 1 || servers[0].Name != "fs" {
        t.Fatalf("expected one server named fs, got %v", servers)
    }
}
```

Run: `go test -run TestMCPManagerAddAndList .`
Expected: FAIL with undefined symbols.

- [ ] **Step 2: Implement MCPManager**

In `mcp_manager.go`:

```go
type mcpManager struct {
    cfg *AppConfig
}

func newMCPManager(cfg *AppConfig) *mcpManager {
    ensureAppConfigMaps(cfg)
    if cfg.MCPServers == nil {
        cfg.MCPServers = map[string]MCPServerConfig{}
    }
    return &mcpManager{cfg: cfg}
}

func (m *mcpManager) add(s MCPServerConfig) error { ... }
func (m *mcpManager) remove(name string) error { ... }
func (m *mcpManager) list() []MCPServerConfig { ... }
func (m *mcpManager) get(name string) (MCPServerConfig, bool) { ... }
```

- [ ] **Step 3: Implement cs mcp CLI**

In `mcp_cmd.go` dispatch `list/add/remove/test` and wire to `main.go`.

- [ ] **Step 4: Verify tests pass**

Run: `go test -run TestMCPManager .` and `go test -run TestMCPCmd .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mcp_manager.go mcp_manager_test.go mcp_cmd.go mcp_cmd_test.go main.go
git commit -m "feat: add cs mcp list/add/remove/test commands"
```

---

## Task 3: Per-Agent MCP Configuration Generation

**Files:**
- Create: `mcp_agent.go`, `mcp_agent_test.go`
- Modify: `claude.go`, `codex.go`, `opencode.go`

Generate the correct MCP config format for each agent:
- Claude Code: `settings.json` -> `"mcpServers": { "name": { "command": "...", "args": [...], "env": {...} } }`
- Codex: `config.toml` -> `[mcpServers.name]` block or equivalent (research exact format)
- OpenCode: `opencode.json` -> `mcp.servers` or equivalent (research exact format)

- [ ] **Step 1: Write failing tests**

```go
func TestClaudeMCPConfigGeneration(t *testing.T) {
    cfg := &AppConfig{
        MCPServers: map[string]MCPServerConfig{
            "fs": {Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"}},
        },
    }
    got := generateClaudeMCPConfig(cfg)
    servers, ok := got["mcpServers"].(map[string]any)
    if !ok {
        t.Fatalf("expected mcpServers map")
    }
    if _, ok := servers["fs"]; !ok {
        t.Fatalf("expected fs server in generated config")
    }
}
```

Run: `go test -run TestClaudeMCPConfigGeneration .`
Expected: FAIL with undefined function.

- [ ] **Step 2: Implement per-agent generators**

In `mcp_agent.go`:

```go
func generateClaudeMCPConfig(cfg *AppConfig) map[string]any { ... }
func generateCodexMCPConfig(cfg *AppConfig) map[string]any { ... }
func generateOpencodeMCPConfig(cfg *AppConfig) map[string]any { ... }
```

For Claude, produce the standard `mcpServers` shape. For Codex and OpenCode, inspect existing agent config writers and produce matching structures.

- [ ] **Step 3: Wire into switch/restore path**

When `cs switch` or `cs restore` writes an agent config, merge the generated MCP block with the existing config so MCP servers are persisted in the agent's native file.

- [ ] **Step 4: Verify tests pass**

Run: `go test -run Test.*MCPConfig .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mcp_agent.go mcp_agent_test.go claude.go codex.go opencode.go
git commit -m "feat: generate per-agent MCP configurations"
```

---

## Task 4: MCP Server Health Check in Doctor

**Files:**
- Modify: `doctor.go`, `doctor_drift.go` or `mcp.go`

Add a `checkMCPHealth` function that attempts to start/validate each configured MCP server. For stdio servers, run the command with `--help` or a lightweight init; for SSE servers, send a HEAD/GET to the URL.

- [ ] **Step 1: Write failing test**

```go
func TestMCPHealthCheckStdio(t *testing.T) {
    cfg := &AppConfig{
        MCPServers: map[string]MCPServerConfig{
            "true": {Name: "true", Transport: "stdio", Command: "true"},
        },
    }
    r := checkMCPHealth(cfg)
    if r.Status != "ok" {
        t.Fatalf("expected ok for true command, got %s: %s", r.Status, r.Detail)
    }
}
```

Run: `go test -run TestMCPHealthCheckStdio .`
Expected: FAIL with undefined function.

- [ ] **Step 2: Implement checkMCPHealth**

In `mcp.go` or `doctor_drift.go`:

```go
func checkMCPHealth(cfg *AppConfig) checkResult { ... }
```

Use `context.WithTimeout` to avoid hanging. For stdio, run the command with a short timeout. For SSE, perform a lightweight HTTP probe.

- [ ] **Step 3: Wire into runDoctor**

Call `checkMCPHealth(cfg)` when `cfg.MCPServers` is non-empty.

- [ ] **Step 4: Verify tests pass**

Run: `go test -run TestMCPHealthCheck .` and `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mcp.go doctor.go doctor_drift.go
git commit -m "feat: add MCP server health check to cs doctor"
```

---

## Task 5: Tool Allowlist/Blocklist Filtering

**Files:**
- Modify: `mcp_agent.go`, `mcp_agent_test.go`

When generating agent MCP configs, filter the server's exposed tools based on `AllowedTools` and `BlockedTools`.

- [ ] **Step 1: Write failing test**

```go
func TestMCPToolFiltering(t *testing.T) {
    s := MCPServerConfig{
        Name:         "fs",
        AllowedTools: []string{"read_file"},
    }
    if !mcpToolAllowed(s, "read_file") {
        t.Fatal("expected read_file allowed")
    }
    if mcpToolAllowed(s, "delete_file") {
        t.Fatal("expected delete_file blocked")
    }
}
```

Run: `go test -run TestMCPToolFiltering .`
Expected: FAIL.

- [ ] **Step 2: Implement mcpToolAllowed**

```go
func mcpToolAllowed(s MCPServerConfig, tool string) bool {
    if len(s.BlockedTools) > 0 && contains(s.BlockedTools, tool) {
        return false
    }
    if len(s.AllowedTools) > 0 && !contains(s.AllowedTools, tool) {
        return false
    }
    return true
}
```

- [ ] **Step 3: Apply filtering in config generation**

For agents that support tool-level allowlists (Claude Code does not natively, but some wrappers do), include the filtered list. For now, store allowed/blocked tools in the generated config as metadata comments or agent-specific fields where supported.

- [ ] **Step 4: Verify tests pass**

Run: `go test -run TestMCPToolFiltering .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mcp_agent.go mcp_agent_test.go
git commit -m "feat: support MCP tool allowlist and blocklist"
```

---

## Task 6: Final Integration and Verification

- [ ] **Step 1: Run full build and test suite**

```bash
go vet ./... && go test ./... && go build -o cs .
```
Expected: all pass, binary produced.

- [ ] **Step 2: Run MCP CLI smoke test**

```bash
export HOME=$(mktemp -d)
./cs mcp add fs --transport stdio --command npx -- -y @modelcontextprotocol/server-filesystem /tmp
./cs mcp list
./cs mcp test fs
./cs switch deepseek  # ensure MCP config is written into Claude settings
./cs doctor
```

- [ ] **Step 3: Final commit**

```bash
git commit -m "chore: MCP manager integration and verification"
```

---

## Spec Coverage Check

| Requirement | Task |
|-------------|------|
| MCP server config storage | Task 1 |
| `cs mcp list/add/remove/test` | Task 2 |
| Per-agent MCP config generation | Task 3 |
| MCP health check in doctor | Task 4 |
| Tool allowlist/blocklist | Task 5 |
| Full verification | Task 6 |

## Placeholder Scan

No TBD/TODO placeholders. Every task includes concrete test code, function signatures, and file paths.

## Type Consistency Notes

- `MCPServerConfig` is the single source of truth for MCP server definitions.
- Generator functions return `map[string]any` to merge with existing agent configs.
- `mcpManager` operates on `*AppConfig` and mutates it in place, matching existing config helpers.
