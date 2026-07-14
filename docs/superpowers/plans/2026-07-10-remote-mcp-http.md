# Remote HTTP MCP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace legacy SSE MCP registry support with remote HTTP servers that render safely into Claude Code, Codex, and OpenCode configurations.

**Architecture:** Keep `MCPServerConfig` as the common registry model, adding HTTP URL and static-header validation while preserving stdio behavior. Track generated MCP names per agent so a switch or restore can clean only that agent's old entries; migrate the existing global tracking list once on config load. Render validated registry entries through target-specific Claude JSON, Codex TOML, and OpenCode JSON adapters.

**Tech Stack:** Go 1.22+, standard library (`net/url`, `net/http`, `httptest`), existing `package main` layout, Go unit tests.

---

## File Structure

- `presets.go`: Add the per-agent managed-MCP-name persistence field while retaining the old field for one-time migration.
- `agent.go`: Initialize the per-agent map alongside existing AppConfig maps.
- `config.go` and `proxy_config.go`: Normalize legacy global managed names into per-agent state during config loading.
- `mcp.go`: Validate `stdio` and `http`, validate remote URLs, validate the whole registry, and probe HTTP endpoints with static headers.
- `mcp_manager.go`: Resolve managed names per agent and preserve legacy cleanup names until migration has occurred.
- `mcp_cmd.go`: Replace SSE CLI help with HTTP, parse repeatable headers, and allow legacy entries only in list/remove.
- `mcp_agent.go`: Generate Claude/OpenCode remote HTTP entries and remove only the active agent's managed JSON entries.
- `mcp_agent_toml.go`: Render Codex remote URL/header tables and remove only Codex-managed TOML entries.
- `switch.go`, `codex.go`, `opencode.go`, `restore.go`, `tui.go`: Validate registry use, pass the active agent to cleanup, and persist per-agent generated-name state after successful writes.
- `mcp_test.go`, `mcp_cmd_test.go`, `mcp_agent_*_test.go`, and `mcp_manager_test.go`: Lock each behavior with unit and `httptest` coverage.

## Task 1: Add per-agent MCP cleanup state and migrate legacy state

**Files:**
- Modify: `presets.go:120-129`
- Modify: `agent.go:36-52`
- Modify: `proxy_config.go:139-149`
- Modify: `config.go:69-105`
- Modify: `mcp_manager.go:79-89`
- Modify: `mcp_manager_test.go`
- Test: `mcp_manager_test.go`

- [ ] **Step 1: Write failing migration and isolation tests**

```go
func TestNormalizeAppConfig_migratesLegacyManagedMCPNamesPerAgent(t *testing.T) {
	cfg := &AppConfig{ManagedMCPNames: []string{"alpha"}}

	normalizeAppConfig(cfg)

	for _, agent := range []AgentName{agentClaude, agentCodex, agentOpencode} {
		if got := cfg.ManagedMCPNamesByAgent[string(agent)]; !reflect.DeepEqual(got, []string{"alpha"}) {
			t.Fatalf("managed names for %s = %v, want [alpha]", agent, got)
		}
	}
	if len(cfg.ManagedMCPNames) != 0 {
		t.Fatalf("legacy names = %v, want empty", cfg.ManagedMCPNames)
	}
}

func TestManagedMCPNamesForAgent_doesNotReadAnotherAgentsNames(t *testing.T) {
	cfg := &AppConfig{ManagedMCPNamesByAgent: map[string][]string{
		string(agentClaude): []string{"claude-only"},
		string(agentCodex):  []string{"codex-only"},
	}}

	if got := managedMCPNamesForAgent(cfg, agentClaude); !reflect.DeepEqual(got, []string{"claude-only"}) {
		t.Fatalf("Claude names = %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestNormalizeAppConfig_migratesLegacyManagedMCPNamesPerAgent|TestManagedMCPNamesForAgent_doesNotReadAnotherAgentsNames' .`

Expected: FAIL because `ManagedMCPNamesByAgent` and `managedMCPNamesForAgent` do not exist.

- [ ] **Step 3: Add persisted per-agent state and normalization**

Add the following field without removing the legacy field:

```go
ManagedMCPNames        []string            `json:"managedMcpNames,omitempty"`
ManagedMCPNamesByAgent map[string][]string `json:"managedMcpNamesByAgent,omitempty"`
```

Extend `ensureAppConfigMaps` so `ManagedMCPNamesByAgent` is non-nil. Extend
`normalizeAppConfig` to perform the one-time migration only when the legacy
slice is non-empty: copy its names into each of `claude`, `codex`, and
`opencode` if that agent has no existing list, then set `ManagedMCPNames` to
nil. Add helpers:

```go
func managedMCPNamesForAgent(cfg *AppConfig, agent AgentName) []string

func setManagedMCPNamesForAgent(cfg *AppConfig, agent AgentName) {
	cfg.ManagedMCPNamesByAgent[string(agent)] = managedMCPServerNames(cfg)
}
```

Both helpers must copy slices so callers cannot mutate persisted state through
a returned slice.

- [ ] **Step 4: Run focused tests**

Run: `go test -run 'TestNormalizeAppConfig_migratesLegacyManagedMCPNamesPerAgent|TestManagedMCPNamesForAgent_doesNotReadAnotherAgentsNames|TestMCPManager' .`

Expected: PASS.

- [ ] **Step 5: Commit the migration foundation**

```bash
git add presets.go agent.go config.go proxy_config.go mcp_manager.go mcp_manager_test.go
git commit -m "feat: track managed MCP names per agent"
```

## Task 2: Replace SSE validation with strict remote HTTP validation and probes

**Files:**
- Modify: `mcp.go:1-95`
- Modify: `mcp_test.go`

- [ ] **Step 1: Write failing transport, URL, header, and probe tests**

```go
func TestValidateMCPServerConfig_acceptsHTTPURL(t *testing.T) {
	err := validateMCPServerConfig(MCPServerConfig{
		Name: "remote", Transport: "http", URL: "https://mcp.example.test/v1", Headers: map[string]string{"Authorization": "Bearer test"},
	})
	if err != nil {
		t.Fatalf("validateMCPServerConfig() error = %v", err)
	}
}

func TestValidateMCPServerConfig_rejectsSSEAndInvalidHTTPURLs(t *testing.T) {
	for _, server := range []MCPServerConfig{
		{Name: "legacy", Transport: "sse", URL: "https://mcp.example.test/sse"},
		{Name: "relative", Transport: "http", URL: "/mcp"},
		{Name: "ftp", Transport: "http", URL: "ftp://mcp.example.test"},
	} {
		if err := validateMCPServerConfig(server); err == nil {
			t.Fatalf("expected validation error for %+v", server)
		}
	}
}

func TestTestMCPServerHTTP_forwardsHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	if err := testMCPServer(context.Background(), MCPServerConfig{Name: "remote", Transport: "http", URL: server.URL, Headers: map[string]string{"Authorization": "Bearer test"}}); err != nil {
		t.Fatalf("testMCPServer() error = %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestValidateMCPServerConfig_acceptsHTTPURL|TestValidateMCPServerConfig_rejectsSSEAndInvalidHTTPURLs|TestTestMCPServerHTTP_forwardsHeaders' .`

Expected: FAIL because `http` is not a valid transport and probes do not set headers.

- [ ] **Step 3: Implement minimal validation and health checks**

Use `net/url` to parse an HTTP URL and require:

```go
parsed, err := url.ParseRequestURI(strings.TrimSpace(s.URL))
if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
	return fmt.Errorf("http mcp server %q requires an absolute http or https url", s.Name)
}
```

Replace the `sse` branch with `http`. Before `http.DefaultClient.Do(req)`, add
each `s.Headers` entry with `req.Header.Set(key, value)`. Wrap probe errors with
the server name, and report HTTP failures as `mcp http endpoint returned ...`.
Add a `validateMCPRegistry(cfg *AppConfig) error` helper that validates every
server in deterministic name order; it is the strict gate for switch, restore,
doctor, and explicit test paths.

- [ ] **Step 4: Add failure-path tests**

Add `httptest` coverage for a 401 response and a handler that exceeds a parent
context deadline. Assert both errors include the configured server name. Keep
the existing stdio true/false test unchanged except for removing SSE fixtures.

- [ ] **Step 5: Run focused tests**

Run: `go test -run 'TestValidateMCPServerConfig|TestTestMCPServer|TestCheckMCPHealth' .`

Expected: PASS.

- [ ] **Step 6: Commit validation and probes**

```bash
git add mcp.go mcp_test.go
git commit -m "feat: validate and probe HTTP MCP servers"
```

## Task 3: Add HTTP MCP CLI parsing while preserving legacy cleanup access

**Files:**
- Modify: `mcp_cmd.go:31-173`
- Modify: `mcp_cmd_test.go`
- Modify: `main.go:719-724`

- [ ] **Step 1: Write failing command tests**

```go
func TestCmdMCPAddHTTP_persistsHeaders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := &bytes.Buffer{}
	err := runWithIO([]string{
		"mcp", "add", "remote", "--transport", "http", "--url", "https://mcp.example.test/v1",
		"--header", "Authorization: Bearer test", "--header", "X-Trace: a:b",
	}, strings.NewReader(""), out)
	if err != nil {
		t.Fatalf("runWithIO() error = %v", err)
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.MCPServers["remote"].Headers["X-Trace"]; got != "a:b" {
		t.Fatalf("X-Trace = %q", got)
	}
}

func TestCmdMCPListAndRemove_allowLegacySSE(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".code-switch", "config.json")
	cfg := &AppConfig{Providers: map[string]StoredProvider{}, MCPServers: map[string]MCPServerConfig{
		"legacy": {Name: "legacy", Transport: "sse", URL: "https://mcp.example.test/sse"},
	}}
	if err := writeJSONAtomic(path, cfg); err != nil {
		t.Fatal(err)
	}
	listOut := &bytes.Buffer{}
	if err := runWithIO([]string{"mcp", "list"}, strings.NewReader(""), listOut); err != nil {
		t.Fatalf("list error = %v", err)
	}
	if !strings.Contains(listOut.String(), "legacy\tsse\thttps://mcp.example.test/sse") {
		t.Fatalf("list output = %q", listOut.String())
	}
	if err := runWithIO([]string{"mcp", "remove", "legacy"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("remove error = %v", err)
	}
	loaded, err := loadAppConfigFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.MCPServers["legacy"]; ok {
		t.Fatalf("legacy MCP server remains after removal: %#v", loaded.MCPServers)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestCmdMCPAddHTTP_persistsHeaders|TestCmdMCPListAndRemove_allowLegacySSE' .`

Expected: FAIL because `--header` is not defined and add accepts `sse` instead of `http`.

- [ ] **Step 3: Parse and validate repeatable headers**

Introduce a small `headerFlags` type implementing `flag.Value` and use it for
`--header`. Its `Set` method must split only at the first `:`, trim both the
name and value, reject an empty name and duplicate names, and save a
`map[string]string`. Change usage/help text to say `stdio or http` and HTTP
URL. Set `MCPServerConfig.Headers` from parsed flags.

- [ ] **Step 4: Keep legacy management commands permissive**

Do not call `validateMCPRegistry` from `cmdMCPList` or `cmdMCPRemove`.
Continue using per-server `validateMCPServerConfig` in `mcp add`, so a new SSE
entry cannot be added. Let `cmdMCPTest` call `testMCPServer`, which rejects a
legacy SSE entry with the migration error.

- [ ] **Step 5: Update help and run focused tests**

Update `printUsage` and command-local usage errors to show:

```text
cs mcp add <name> --transport http --url <url> [--header "Name: Value" ...]
```

Run: `go test -run 'TestCmdMCP|TestMCP.*Usage|TestHelp' .`

Expected: PASS.

- [ ] **Step 6: Commit CLI support**

```bash
git add mcp_cmd.go mcp_cmd_test.go main.go
git commit -m "feat: add HTTP MCP command support"
```

## Task 4: Render remote HTTP MCP servers for all agents

**Files:**
- Modify: `mcp_agent.go:5-133`
- Modify: `mcp_agent_toml.go:9-46`
- Modify: `mcp_agent_format_test.go`
- Modify: `mcp_agent_filters_test.go`
- Modify: `mcp_agent_test.go`

- [ ] **Step 1: Write failing pure-generator tests**

```go
func TestGenerateAgentMCPConfigs_includeHTTPServer(t *testing.T) {
	cfg := &AppConfig{MCPServers: map[string]MCPServerConfig{
		"remote": {Name: "remote", Transport: "http", URL: "https://mcp.example.test/v1", Headers: map[string]string{"Authorization": "Bearer test"}},
	}}

	claude := generateClaudeMCPConfig(cfg)["mcpServers"].(map[string]any)["remote"].(map[string]any)
	if claude["type"] != "http" || claude["url"] != "https://mcp.example.test/v1" {
		t.Fatalf("Claude remote = %#v", claude)
	}

	opencode := generateOpencodeMCPConfig(cfg)["mcp"].(map[string]any)["remote"].(map[string]any)
	if opencode["type"] != "remote" || opencode["url"] != "https://mcp.example.test/v1" {
		t.Fatalf("OpenCode remote = %#v", opencode)
	}

	codex := generateCodexMCPConfig(cfg)["mcp_servers"].(map[string]any)["remote"].(map[string]any)
	if codex["url"] != "https://mcp.example.test/v1" {
		t.Fatalf("Codex remote = %#v", codex)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestGenerateAgentMCPConfigs_includeHTTPServer .`

Expected: FAIL because all current generators skip non-stdio servers.

- [ ] **Step 3: Implement target-specific entry builders**

Keep `agentMCPServerConfig` for stdio. Add dedicated builders that defensively
copy headers:

```go
func claudeHTTPMCPServerConfig(server MCPServerConfig) map[string]any {
	return map[string]any{"type": "http", "url": server.URL, "headers": copyMCPHeaders(server.Headers)}
}

func opencodeHTTPMCPServerConfig(server MCPServerConfig) map[string]any {
	return map[string]any{"type": "remote", "url": server.URL, "headers": copyMCPHeaders(server.Headers), "enabled": true}
}

func codexHTTPMCPServerConfig(server MCPServerConfig) map[string]any {
	return map[string]any{"url": server.URL, "http_headers": copyMCPHeaders(server.Headers)}
}
```

Only include `headers`/`http_headers` when the source map is non-empty. Extend
each generator's transport switch to include enabled `http` entries. Keep
OpenCode's existing tool filters only on its generated entries; do not add
unconfirmed Codex filtering fields.

- [ ] **Step 4: Extend Codex TOML writer**

For an entry containing `url`, write:

```toml
[mcp_servers.remote]
url = "https://mcp.example.test/v1"

[mcp_servers.remote.http_headers]
Authorization = "Bearer test"
```

Keep the existing stdio `command`, `args`, and `env` output unchanged. Sort
header keys using `sortedStringMapKeys` for deterministic output.

- [ ] **Step 5: Add integration assertions and disabled behavior**

Add switch/restore tests that parse Claude/OpenCode JSON and inspect Codex TOML
for the remote type, URL, headers, and absence of disabled remote entries. Add
one test that mutates a returned config header map and proves the source
`MCPServerConfig.Headers` did not change.

- [ ] **Step 6: Run generator test suite**

Run: `go test -run 'TestGenerate.*MCP|TestClaude.*MCP|TestCodex.*MCP|TestOpencode.*MCP' .`

Expected: PASS.

- [ ] **Step 7: Commit generator support**

```bash
git add mcp_agent.go mcp_agent_toml.go mcp_agent_format_test.go mcp_agent_filters_test.go mcp_agent_test.go
git commit -m "feat: generate remote HTTP MCP configs"
```

## Task 5: Use per-agent cleanup state in switch and restore flows

**Files:**
- Modify: `mcp_agent.go:115-133`
- Modify: `mcp_agent_toml.go:49-108`
- Modify: `switch.go:151-224,265-345`
- Modify: `codex.go:261-298,444-470`
- Modify: `opencode.go:126-139,210-234`
- Modify: `restore.go:10-114`
- Modify: `tui.go:60-95`
- Modify: `mcp_agent_cleanup_test.go`

- [ ] **Step 1: Write the cross-agent cleanup regression**

```go
func TestManagedMCPRemoval_cleansEachAgentAfterAnotherAgentSyncs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	codexDir := filepath.Join(home, ".codex")
	cfg := &AppConfig{Providers: map[string]StoredProvider{}, MCPServers: map[string]MCPServerConfig{
		"alpha": {Name: "alpha", Transport: "stdio", Command: "node"},
	}}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir},
		{"switch", "deepseek", "--agent", "codex", "--api-key", "sk-test", "--codex-dir", codexDir},
		{"mcp", "remove", "alpha"},
		{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir},
		{"switch", "deepseek", "--agent", "codex", "--api-key", "sk-test", "--codex-dir", codexDir},
	} {
		if err := runWithIO(args, strings.NewReader(""), &bytes.Buffer{}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, ok := parseClaudeMCPServersOptional(t, mustReadFile(t, filepath.Join(claudeDir, "settings.json")))["alpha"]; ok {
		t.Fatal("Claude retained removed alpha")
	}
	codexConfig := string(mustReadFile(t, filepath.Join(codexDir, "config.toml")))
	if strings.Contains(codexConfig, "mcp_servers.alpha") {
		t.Fatalf("Codex retained removed alpha:\n%s", codexConfig)
	}
}
```

Also add a test that seeds legacy global `ManagedMCPNames`, loads the config,
then verifies each agent receives the legacy cleanup name before its first
switch.

- [ ] **Step 2: Run cleanup tests to verify they fail**

Run: `go test -run 'TestManagedMCPRemoval_cleansEachAgentAfterAnotherAgentSyncs|TestClaudeSwitch_removesManagedMCPServer|TestOpencodeSwitch_removesManagedMCPServer' .`

Expected: FAIL because cleanup currently reads and overwrites one global list.

- [ ] **Step 3: Make cleanup agent-specific**

Change the JSON cleanup signature to:

```go
func removeManagedMCPFromJSON(root map[string]any, cfg *AppConfig, agent AgentName)
```

Use `managedMCPNamesForAgent(cfg, agent)` rather than the global list. Change
the TOML cleanup helper to use `agentCodex`. Pass `agentClaude` from Claude
switch/restore, `agentCodex` from Codex switch/restore, and `agentOpencode`
from OpenCode switch/restore.

- [ ] **Step 4: Persist current names only after a successful target write**

In the existing `persistAppConfig` closure in `cmdSwitchWithOutput`, replace
the global assignment with:

```go
setManagedMCPNamesForAgent(cfg, agent)
```

In `cmdRestore`, call the same helper only after the selected agent's restore
function returns nil and before `writeJSONAtomic`. Keep `--dry-run` free of
both agent-config and AppConfig changes.

- [ ] **Step 5: Add strict registry gates at consuming boundaries**

Call `validateMCPRegistry(cfg)` before each switch/restore path writes a target
config, including the TUI path. Do not call it from `mcp list` or `mcp remove`.
Add tests proving a legacy SSE entry blocks switch, restore, `cs mcp test`, and
doctor with the migration error, but remains listable and removable.

- [ ] **Step 6: Run cleanup and lifecycle tests**

Run: `go test -run 'TestManagedMCP|TestClaudeSwitch_removesManagedMCP|TestOpencodeSwitch_removesManagedMCP|Test.*Restore.*MCP' .`

Expected: PASS.

- [ ] **Step 7: Commit lifecycle changes**

```bash
git add mcp_agent.go mcp_agent_toml.go switch.go codex.go opencode.go restore.go tui.go mcp_agent_cleanup_test.go
git commit -m "fix: isolate managed MCP cleanup per agent"
```

## Task 6: Finalize doctor behavior and full verification

**Files:**
- Modify: `doctor.go:79-114`
- Modify: `mcp_test.go`

- [ ] **Step 1: Write failing disabled-server doctor test**

```go
func TestCheckMCPHealth_skipsDisabledServers(t *testing.T) {
	cfg := &AppConfig{MCPServers: map[string]MCPServerConfig{
		"disabled": {Name: "disabled", Transport: "http", URL: "http://127.0.0.1:1", Disabled: true},
	}}

	result := checkMCPHealth(cfg)
	if result.Status != "ok" {
		t.Fatalf("disabled server should be skipped: %+v", result)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -run TestCheckMCPHealth_skipsDisabledServers .`

Expected: FAIL because the current doctor probes every configured server.

- [ ] **Step 3: Skip disabled servers and retain explicit testing**

In `checkMCPHealth`, continue before creating a context when `server.Disabled`
is true. Do not add that condition to `cmdMCPTest`; explicit test remains an
opt-in probe. Ensure doctor returns `okResult("mcp servers", "no enabled MCP servers configured")`
when every configured server is disabled.

- [ ] **Step 4: Run full validation**

Run: `go vet ./... && go test -count=1 ./... && go build -o cs .`

Expected: all commands exit 0.

- [ ] **Step 5: Run CLI smoke tests in an isolated home**

Run:

```bash
TMP=$(mktemp -d)
HOME="$TMP" go run . mcp add remote --transport http --url http://127.0.0.1:1 --header "Authorization: Bearer test"
HOME="$TMP" go run . mcp list
HOME="$TMP" go run . mcp remove remote
rm -rf "$TMP"
```

Expected: add/list/remove succeed without touching the developer's real config.

- [ ] **Step 6: Commit doctor behavior and tests**

```bash
git add doctor.go mcp_test.go
git commit -m "feat: skip disabled MCP servers in doctor"
```
