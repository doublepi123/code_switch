# OpenCode Agent Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `opencode` as a first-class `--agent` target so users can configure the OpenCode CLI via `code-switch`, writing `~/.config/opencode/opencode.json` using the Anthropic provider with custom `baseURL` and `{env:...}` API keys.

**Architecture:** Reuse the existing per-agent storage model (`AppConfig.Agents["opencode"]`) and the existing Claude Code provider presets. Introduce a new `opencode.go` file for JSON config I/O, keep all agent-specific dispatch in `agent.go`, and extend CLI/TUI commands to recognize `agentOpencode`.

**Tech Stack:** Go standard library only (`encoding/json`, `strings`, `os`, `path/filepath`).

---

## File structure

| File | Responsibility |
|------|----------------|
| `presets.go` | Add `agentOpencode` constant. |
| `agent.go` | Parse/display/agent-config helpers for OpenCode; resolve presets and provider lists. |
| `opencode.go` (new) | Read/write `opencode.json`, apply/restore managed settings, detect current provider. |
| `switch.go` | Dispatch to `switchOpencodeProvider`. |
| `config.go` | Persist OpenCode provider config in `AppConfig.Agents`. |
| `main.go` | `current`, `list`, usage, completions. |
| `tui.go` | Agent selection, OpenCode provider/model lists, `--opencode-dir`. |
| `restore.go` | Dispatch to `restoreOpencodeConfig`. |
| `env.go` | `env`/`token` output for OpenCode. |
| `test.go` | `test --agent opencode` path. |
| `opencode_test.go` (new) | Unit tests for OpenCode switch/restore/current/env/token/list. |

---

### Task 1: Add `agentOpencode` identity and agent-config helpers

**Files:**
- Modify: `presets.go:184-189`
- Modify: `agent.go:10-21`, `agent.go:23-32`, `agent.go:34-47`, `agent.go:49-78`, `agent.go:127-142`, `agent.go:144-160`, `agent.go:162-174`, `agent.go:176-199`, `agent.go:201-217`, `agent.go:219-227`, `agent.go:248-254`

- [ ] **Step 1: Add constant in `presets.go`**

```go
const (
	agentClaude   AgentName = "claude"
	agentCodex    AgentName = "codex"
	agentOpencode AgentName = "opencode"
)
```

- [ ] **Step 2: Update `parseAgentName` in `agent.go`**

```go
func parseAgentName(value string) (AgentName, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return agentClaude, nil
	}
	switch AgentName(value) {
	case agentClaude, agentCodex, agentOpencode:
		return AgentName(value), nil
	default:
		return "", fmt.Errorf("unsupported agent %q, use claude, codex, or opencode", value)
	}
}
```

- [ ] **Step 3: Update `agentDisplayName` in `agent.go`**

```go
func agentDisplayName(agent AgentName) string {
	switch agent {
	case agentCodex:
		return "Codex"
	case agentClaude:
		return "Claude Code"
	case agentOpencode:
		return "OpenCode"
	default:
		return fmt.Sprintf("Unknown (%s)", string(agent))
	}
}
```

- [ ] **Step 4: Add OpenCode config helpers in `agent.go`**

Add after `codexProviderConfig`:

```go
func opencodeProviderConfig(cfg *AppConfig, provider string) StoredProvider {
	agentCfg := agentConfig(cfg, agentOpencode)
	return agentCfg.Providers[provider]
}
```

Update `storedAPIKeyForAgent`:

```go
func storedAPIKeyForAgent(cfg *AppConfig, agent AgentName, provider string) string {
	if agent == agentCodex {
		key := strings.TrimSpace(codexProviderConfig(cfg, provider).APIKey)
		if key != "" {
			return key
		}
	}
	if agent == agentOpencode {
		key := strings.TrimSpace(opencodeProviderConfig(cfg, provider).APIKey)
		if key != "" {
			return key
		}
	}
	return strings.TrimSpace(cfg.Providers[provider].APIKey)
}
```

- [ ] **Step 5: Update preset resolution for OpenCode in `agent.go`**

In `resolveAgentProviderPreset`, add a branch for `agentOpencode` that reuses `resolveProviderPreset`:

```go
func resolveAgentProviderPreset(agent AgentName, provider string, cfg *AppConfig) (ProviderPreset, error) {
	switch agent {
	case agentCodex:
		provider = canonicalProviderName(provider)
		preset, err := codexPresetForProvider(provider)
		if err != nil {
			return ProviderPreset{}, err
		}
		if stored := codexProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
			preset = withSelectedModel(preset, stored.Model)
		}
		return preset, nil
	case agentOpencode:
		return resolveProviderPreset(provider, cfg)
	default:
		return resolveProviderPreset(provider, cfg)
	}
}
```

Similarly update `resolveAgentSwitchPreset` to use `resolveSwitchPreset` for OpenCode.

- [ ] **Step 6: Update provider list helpers in `agent.go`**

Update `providerNamesForAgent`:

```go
func providerNamesForAgent(agent AgentName, cfg *AppConfig, includeCustomOption bool, includeRestoreOption bool) []string {
	var names []string
	switch agent {
	case agentCodex:
		names = []string{"deepseek", "kimi-coding", "ollama-cloud", "openrouter"}
	case agentOpencode:
		names = sortedProviderNames(cfg, includeCustomOption)
	default:
		names = sortedProviderNames(cfg, includeCustomOption)
	}
	if includeRestoreOption {
		names = append(names, restoreProviderOption)
	}
	return names
}
```

Update `providerModelsForAgentWithAPIKey` to fall through to `providerModels` for OpenCode.

Update `defaultSelectionModelForAgent`:

```go
func defaultSelectionModelForAgent(cfg *AppConfig, agent AgentName, provider, currentProvider, currentModel string) string {
	if agent == agentClaude || agent == agentOpencode {
		return defaultSelectionModel(cfg, provider, currentProvider, currentModel)
	}
	// codex branch unchanged
	...
}
```

Update `sortedAgentNames`:

```go
func sortedAgentNames() []AgentName {
	names := []AgentName{agentClaude, agentCodex, agentOpencode}
	sort.Slice(names, func(i, j int) bool {
		return agentDisplayName(names[i]) < agentDisplayName(names[j])
	})
	return names
}
```

- [ ] **Step 7: Run existing tests**

```bash
go test ./...
```

Expected: PASS (only constants/helpers changed; no behavior yet).

- [ ] **Step 8: Commit**

```bash
git add presets.go agent.go
git commit -m "feat: add opencode agent identity and config helpers

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Implement `opencode.go` core config I/O

**Files:**
- Create: `opencode.go`

- [ ] **Step 1: Create `opencode.go` with path, switch, restore, current helpers**

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func opencodeConfigPath(overrideDir string) string {
	dir := strings.TrimSpace(overrideDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), ".config", "opencode", "opencode.json")
		}
		dir = filepath.Join(home, ".config", "opencode")
	}
	return filepath.Join(dir, "opencode.json")
}

func switchOpencodeProvider(provider string, cfg *AppConfig, apiKey, modelOverride, opencodeDir string, out io.Writer, dryRun bool) error {
	provider = canonicalProviderName(provider)
	preset, err := resolveAgentSwitchPreset(agentOpencode, provider, cfg, modelOverride)
	if err != nil {
		return err
	}
	configPath := opencodeConfigPath(opencodeDir)

	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}

	if dryRun {
		fmt.Fprintf(out, "[dry-run] would switch OpenCode to %s\n", preset.Name)
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		fmt.Fprintf(out, "[dry-run] base_url: %s\n", preset.BaseURL)
		fmt.Fprintf(out, "[dry-run] model: %s\n", preset.Model)
		fmt.Fprintf(out, "[dry-run] api_key_env: %s\n", authEnv)
		return nil
	}

	existingBytes, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing := string(existingBytes)

	if err := backupIfExists(configPath); err != nil {
		return err
	}

	updated := applyOpencodePresetJSON(existing, preset, authEnv)
	if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
		return err
	}

	stored := opencodeProviderConfig(cfg, provider)
	if apiKey != "" {
		stored.APIKey = apiKey
	}
	stored.Model = preset.Model
	setAgentProviderConfig(cfg, agentOpencode, provider, stored)

	fmt.Fprintf(out, "%s\n", successPrefix(fmt.Sprintf("switched OpenCode to %s", preset.Name)))
	fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
	fmt.Fprintf(out, "%s\n", formatLabel("base_url", preset.BaseURL))
	fmt.Fprintf(out, "%s\n", formatLabel("model", preset.Model))
	fmt.Fprintf(out, "%s\n", formatLabel("api_key_env", authEnv))
	return nil
}

func applyOpencodePresetJSON(existing string, preset ProviderPreset, authEnv string) string {
	root := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		_ = json.Unmarshal([]byte(existing), &root)
	}

	root["$schema"] = "https://opencode.ai/config.json"
	root["model"] = preset.Model

	provider := map[string]any{}
	if raw, ok := root["provider"]; ok {
		if m, ok := raw.(map[string]any); ok {
			provider = m
		}
	}

	anthropic := map[string]any{}
	options := map[string]any{
		"baseURL": preset.BaseURL,
		"apiKey":  fmt.Sprintf("{env:%s}", authEnv),
	}
	anthropic["options"] = options
	provider["anthropic"] = anthropic
	root["provider"] = provider

	data, _ := json.MarshalIndent(root, "", "  ")
	return string(data) + "\n"
}

func restoreOpencodeConfig(opencodeDir string, cfg *AppConfig, out io.Writer, dryRun bool) error {
	configPath := opencodeConfigPath(opencodeDir)
	existingBytes, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(out, "%s\n", successPrefix("restored OpenCode official config"))
			fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
			return nil
		}
		return err
	}
	existing := string(existingBytes)
	updated := removeOpencodeManagedJSON(existing)

	if dryRun {
		fmt.Fprintf(out, "[dry-run] would restore OpenCode official config\n")
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		return nil
	}

	if err := backupIfExists(configPath); err != nil {
		return err
	}

	if strings.TrimSpace(updated) == "" {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "%s\n", successPrefix("restored OpenCode official config"))
	fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
	return nil
}

func removeOpencodeManagedJSON(existing string) string {
	root := map[string]any{}
	if strings.TrimSpace(existing) == "" {
		return ""
	}
	if err := json.Unmarshal([]byte(existing), &root); err != nil {
		return existing
	}

	delete(root, "model")
	if raw, ok := root["provider"]; ok {
		if provider, ok := raw.(map[string]any); ok {
			delete(provider, "anthropic")
			if len(provider) == 0 {
				delete(root, "provider")
			} else {
				root["provider"] = provider
			}
		}
	}

	if len(root) == 0 {
		return ""
	}
	data, _ := json.MarshalIndent(root, "", "  ")
	return string(data) + "\n"
}

func currentOpencodeProvider(opencodeDir string) (string, string, string, string, error) {
	configPath := opencodeConfigPath(opencodeDir)
	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return configPath, "", "", "", nil
		}
		return configPath, "", "", "", err
	}
	root := map[string]any{}
	if err := json.Unmarshal(content, &root); err != nil {
		return configPath, "", "", "", err
	}

	model, _ := root["model"].(string)
	baseURL := ""
	authEnv := ""
	if raw, ok := root["provider"].(map[string]any); ok {
		if anthropic, ok := raw["anthropic"].(map[string]any); ok {
			if opts, ok := anthropic["options"].(map[string]any); ok {
				baseURL, _ = opts["baseURL"].(string)
				if apiKey, ok := opts["apiKey"].(string); ok {
					authEnv = opencodeAuthEnvFromAPIKeyRef(apiKey)
				}
			}
		}
	}
	return configPath, model, baseURL, authEnv, nil
}

func opencodeAuthEnvFromAPIKeyRef(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	const prefix = "{env:"
	const suffix = "}"
	if strings.HasPrefix(apiKey, prefix) && strings.HasSuffix(apiKey, suffix) {
		return strings.TrimSuffix(strings.TrimPrefix(apiKey, prefix), suffix)
	}
	return ""
}
```

- [ ] **Step 2: Build to catch compile errors**

```bash
go build -o cs .
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add opencode.go
git commit -m "feat: add opencode config read/write helpers

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Wire `switch --agent opencode`

**Files:**
- Modify: `switch.go:12-24`, `switch.go:82-157`, `switch.go:177-187`

- [ ] **Step 1: Add `OpencodeDir` to `providerArgs`**

```go
type providerArgs struct {
	Agent       AgentName
	Provider    string
	APIKey      string
	Model       string
	ClaudeDir   string
	CodexDir    string
	OpencodeDir string
	DryRun      bool
	Haiku       string
	Sonnet      string
	Opus        string
	Subagent    string
}
```

- [ ] **Step 2: Update `cmdSwitchWithOutput` flag set**

Add flag after `--codex-dir`:

```go
opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
```

Update usage string to mention `opencode-dir`:

```go
return errors.New("usage: code-switch switch <provider> [--agent claude|codex|opencode] [--api-key sk-xxx] [--model model-id] [--haiku model] [--sonnet model] [--opus model] [--subagent model] [--claude-dir DIR] [--codex-dir DIR] [--opencode-dir DIR]")
```

Update `switchFlagNeedsValue`:

```go
case "-opencode-dir", "--opencode-dir":
	return true
```

- [ ] **Step 3: Dispatch to OpenCode in `cmdSwitchWithOutput`**

After the Codex branch:

```go
if agent == agentOpencode {
	if !*dryRun {
		stored := opencodeProviderConfig(cfg, pa.Provider)
		stored.APIKey = pa.APIKey
		stored.Model = pa.Model
		setAgentProviderConfig(cfg, agentOpencode, pa.Provider, stored)
		if err := writeJSONAtomic(configPath, cfg); err != nil {
			return err
		}
	}
	return switchOpencodeProvider(pa.Provider, cfg, pa.APIKey, pa.Model, *opencodeDir, out, *dryRun)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add switch.go
git commit -m "feat: wire switch command for opencode agent

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Wire `configure` / TUI for OpenCode

**Files:**
- Modify: `config.go:230-263`
- Modify: `tui.go:17-142`, `tui.go:144-170`, `tui.go:196-201`, `tui.go:456-508` (approximate, verify exact lines)

- [ ] **Step 1: Update `upsertProviderConfig` in `config.go`**

```go
func upsertProviderConfig(cfg *AppConfig, selection ConfigureSelection, apiKey string) {
	if selection.Agent == string(agentCodex) {
		upsertAgentProviderConfig(cfg, agentCodex, selection, apiKey)
		return
	}
	if selection.Agent == string(agentOpencode) {
		upsertAgentProviderConfig(cfg, agentOpencode, selection, apiKey)
		return
	}
	// claude branch unchanged
	...
}
```

- [ ] **Step 2: Update `cmdConfigure` in `tui.go`**

Add flag:

```go
opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
```

Update current-provider detection:

```go
var currentProvider, currentModel string
switch agent {
case agentCodex:
	_, cp, cm, _, _ := currentCodexProvider(*codexDir)
	currentProvider = codexTOMLProviderKey(cp)
	currentModel = cm
case agentOpencode:
	_, cm, _, _, _ := currentOpencodeProvider(*opencodeDir)
	currentModel = cm
	// derive provider from model + stored config if possible, else empty
	if cm != "" {
		for name, stored := range cfg.Providers {
			if stored.Model == cm {
				currentProvider = name
				break
			}
		}
	}
	for name, stored := range agentConfig(cfg, agentOpencode).Providers {
		if stored.Model == cm {
			currentProvider = name
			break
		}
	}
default:
	currentProvider, currentModel = currentConfiguredProvider(cfg, *claudeDir)
}
```

Pass `opencodeDir` to `runArrowTUI` and update the function signature.

- [ ] **Step 3: Update `tuiState` struct in `tui.go`**

```go
type tuiState struct {
	app             *tview.Application
	pages           *tview.Pages
	cfg             *AppConfig
	agent           AgentName
	selectAgent     bool
	currentProvider string
	currentModel    string
	claudeDir       string
	codexDir        string
	opencodeDir     string
	...
}
```

- [ ] **Step 4: Update provider list in TUI**

`ts.showProviders()` already calls `providerNamesForAgent`, which now returns all providers for OpenCode. No change needed there.

- [ ] **Step 5: Update restore dispatch in `cmdConfigure`**

```go
if provider == restoreProviderOption {
	switch agent {
	case agentCodex:
		return restoreCodexConfig(*codexDir, cfg, out, *dryRun)
	case agentOpencode:
		return restoreOpencodeConfig(*opencodeDir, cfg, out, *dryRun)
	default:
		return restoreClaudeConfig(*claudeDir, out, *dryRun)
	}
}
```

- [ ] **Step 6: Update switch dispatch in `cmdConfigure`**

```go
case agentOpencode:
	if err := switchOpencodeProvider(provider, cfg, apiKey, selection.Model, *opencodeDir, out, false); err != nil {
		return err
	}
default:
	...
```

- [ ] **Step 7: Run tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add config.go tui.go
git commit -m "feat: wire configure and TUI for opencode agent

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Wire `current`, `env`, `token`, `test`, `list`

**Files:**
- Modify: `main.go:137-216`, `main.go:95-135`, `main.go:461-466`
- Modify: `env.go:12-114`
- Modify: `test.go:17-50`

- [ ] **Step 1: Update `cmdCurrent` in `main.go`**

Add flag:

```go
opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
```

Update `showBoth` logic to handle three agents. After Codex block, add OpenCode block:

```go
if showBoth || agent == agentOpencode {
	configPath, model, baseURL, authEnv, err := currentOpencodeProvider(*opencodeDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "OpenCode\n")
	fmt.Fprintf(out, "  %s\n", formatLabel("config", configPath))
	if baseURL == "" {
		fmt.Fprintf(out, "  %s\n", formatLabel("provider", "unknown"))
	} else {
		fmt.Fprintf(out, "  %s\n", formatLabel("provider", detectProvider(baseURL, model)))
		if baseURL != "" {
			fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
		}
		if model != "" {
			fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
		}
		if authEnv != "" {
			fmt.Fprintf(out, "  %s\n", formatLabel("api_key_env", authEnv))
		}
	}
	if showBoth {
		fmt.Fprintln(out)
	}
}
```

- [ ] **Step 2: Update `cmdList` in `main.go`**

`providerNamesForAgent` already handles OpenCode. Just update the default agent flag help text where appropriate (covered in Task 7).

- [ ] **Step 3: Update `cmdEnv` in `env.go`**

Add OpenCode branch before the generic Claude branch:

```go
if agent == agentOpencode {
	preset, err := resolveAgentSwitchPreset(agentOpencode, pa.Provider, cfg, pa.Model)
	if err != nil {
		return err
	}
	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}
	fmt.Fprintf(out, "# OpenCode uses env-based auth; export these variables:\n")
	fmt.Fprintf(out, "export ANTHROPIC_BASE_URL=%s\n", shellSingleQuote(preset.BaseURL))
	fmt.Fprintf(out, "export ANTHROPIC_MODEL=%s\n", shellSingleQuote(preset.Model))
	fmt.Fprintf(out, "export %s=%s\n", authEnv, shellSingleQuote(pa.APIKey))
	return nil
}
```

- [ ] **Step 4: Update `cmdToken` in `env.go`**

No change needed; `resolveProviderAndKeyForAgent` already returns the stored key.

- [ ] **Step 5: Update `cmdTest` in `test.go`**

Add OpenCode branch before the generic test:

```go
if agent == agentOpencode {
	return testProvider(out, preset, pa.APIKey, "/v1/messages")
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add main.go env.go test.go
git commit -m "feat: wire current/env/token/test/list for opencode agent

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Wire `set-key`, `remove`, `restore`

**Files:**
- Modify: `main.go:218-278`, `main.go:281-353`
- Modify: `restore.go:10-38`

- [ ] **Step 1: Update `cmdSetKey` in `main.go`**

Add OpenCode branch before the Claude branch:

```go
if agent == agentOpencode {
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q for agent opencode", remaining[0])
	}
	_ = preset
	agentCfg := agentConfig(cfg, agentOpencode)
	stored := agentCfg.Providers[provider]
	stored.APIKey = remaining[1]
	agentCfg.Providers[provider] = stored
	cfg.Agents[string(agentOpencode)] = agentCfg
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved api key for %s (opencode) in %s\n", provider, path)
	return nil
}
```

- [ ] **Step 2: Update `cmdRemove` in `main.go`**

Add OpenCode branch:

```go
if agent == agentOpencode {
	agentCfg := agentConfig(cfg, agentOpencode)
	_, ok := agentCfg.Providers[provider]
	if !ok {
		return fmt.Errorf("no saved configuration for provider %q for agent opencode", provider)
	}
	if !*force {
		stored := opencodeProviderConfig(cfg, provider)
		showKey := maskAPIKey(stored.APIKey)
		fmt.Fprintf(out, "Remove saved config for %s (opencode, key: %s)? [y/N]: ", provider, showKey)
		reader := bufio.NewReader(in)
		response, err := readLine(reader)
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			fmt.Fprintln(out, "cancelled")
			return nil
		}
	}
	delete(agentCfg.Providers, provider)
	cfg.Agents[string(agentOpencode)] = agentCfg
	fmt.Fprintf(out, "removed %s (opencode) from %s\n", provider, path)
	return writeJSONAtomic(path, cfg)
}
```

- [ ] **Step 3: Update `cmdRestore` in `restore.go`**

Add flag:

```go
opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
```

Update usage and dispatch:

```go
if fs.NArg() != 0 {
	return fmt.Errorf("usage: code-switch restore [--agent claude|codex|opencode] [--dry-run]")
}

switch agent {
case agentCodex:
	return restoreCodexConfig(*codexDir, cfg, out, *dryRun)
case agentOpencode:
	return restoreOpencodeConfig(*opencodeDir, cfg, out, *dryRun)
default:
	return restoreClaudeConfig(*claudeDir, out, *dryRun)
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go restore.go
git commit -m "feat: wire set-key/remove/restore for opencode agent

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Update usage text and shell completions

**Files:**
- Modify: `main.go:460-466`, `main.go:372-396`, `main.go:399-407`, `main.go:409-433`, `main.go:435-449`

- [ ] **Step 1: Update `printUsage`**

```go
fmt.Fprint(out, "code-switch\n\nUsage:\n  cs --version\n  cs version\n  cs list [--agent claude|codex|opencode] [--verbose]\n  cs [--dry-run] [--reset-key]\n  cs configure [--agent claude|codex|opencode] [--dry-run] [--reset-key]\n  cs current [--agent claude|codex|opencode] [--claude-dir DIR] [--codex-dir DIR] [--opencode-dir DIR]\n  cs set-key <provider> <api-key> [--agent claude|codex|opencode]\n  cs switch <provider> [--agent claude|codex|opencode] [--api-key sk-xxx] [--model model-id] [--haiku model] [--sonnet model] [--opus model] [--subagent model] [--claude-dir DIR] [--codex-dir DIR] [--opencode-dir DIR] [--dry-run]\n  cs env <provider> [--agent claude|codex|opencode] [--api-key sk-xxx]\n  cs token <provider> [--agent claude|codex|opencode] [--api-key sk-xxx]\n  cs restore [--agent claude|codex|opencode] [--dry-run]\n  cs test <provider> [--agent claude|codex|opencode] [--api-key sk-xxx] [--model model-id] [--path /custom/api/path]\n  cs remove <provider> [--agent claude|codex|opencode] [--force]\n  cs upgrade [--dry-run] [--tag vX.Y.Z]\n  cs completion bash|zsh|fish\n\nClaude providers:\n")
...
fmt.Fprint(out, "\nCodex providers:\n  deepseek\n  kimi-coding\n  ollama-cloud\n  openrouter\n")
fmt.Fprint(out, "\nOpenCode providers:\n  <all Claude providers>\n")
```

- [ ] **Step 2: Update bash completion help text for `--agent`**

Wherever bash completion mentions `claude|codex`, update to `claude|codex|opencode` if it appears. The current script does not hardcode the agent list, so no functional change is required there.

- [ ] **Step 3: Update zsh and fish completions**

No provider list change needed (OpenCode reuses Claude providers). Optional: add `--opencode-dir` to completion arguments if desired.

- [ ] **Step 4: Run tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "docs: update usage and completions for opencode agent

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Add unit tests for OpenCode

**Files:**
- Create: `opencode_test.go`

- [ ] **Step 1: Create `opencode_test.go` with switch test**

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeSwitchWritesJSONWithEnvBasedAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-ds-test", "--model", "deepseek-v4-pro", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(opencodeDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("parse opencode config: %v", err)
	}

	if got := cfg["model"]; got != "deepseek-v4-pro" {
		t.Fatalf("model = %v, want deepseek-v4-pro", got)
	}
	provider := cfg["provider"].(map[string]any)["anthropic"].(map[string]any)
	options := provider["options"].(map[string]any)
	if got := options["baseURL"]; got != "https://api.deepseek.com/anthropic" {
		t.Fatalf("baseURL = %v, want https://api.deepseek.com/anthropic", got)
	}
	if got := options["apiKey"]; got != "{env:ANTHROPIC_AUTH_TOKEN}" {
		t.Fatalf("apiKey = %v, want {env:ANTHROPIC_AUTH_TOKEN}", got)
	}
	if strings.Contains(string(configBytes), "sk-ds-test") {
		t.Fatalf("opencode config must not contain plaintext api key")
	}

	appBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	var appCfg AppConfig
	if err := json.Unmarshal(appBytes, &appCfg); err != nil {
		t.Fatalf("unmarshal app config: %v", err)
	}
	if got := appCfg.Agents["opencode"].Providers["deepseek"].APIKey; got != "sk-ds-test" {
		t.Fatalf("opencode stored key = %q, want %q", got, "sk-ds-test")
	}
	if got := appCfg.Agents["opencode"].Providers["deepseek"].Model; got != "deepseek-v4-pro" {
		t.Fatalf("opencode stored model = %q, want %q", got, "deepseek-v4-pro")
	}
}
```

- [ ] **Step 2: Add restore test**

```go
func TestOpencodeRestoreRemovesManagedSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode dir: %v", err)
	}
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "deepseek-v4-pro",
  "provider": {
    "anthropic": {
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "{env:ANTHROPIC_AUTH_TOKEN}"
      }
    }
  },
  "tools": {
    "write": true
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"restore", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode restore returned error: %v", err)
	}

	restoredBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	restored := string(restoredBytes)
	for _, unwanted := range []string{`"model"`, `"anthropic"`, `"baseURL"`, `"apiKey"`} {
		if strings.Contains(restored, unwanted) {
			t.Fatalf("restored config still contains %q:\n%s", unwanted, restored)
		}
	}
	if !strings.Contains(restored, `"tools"`) {
		t.Fatalf("restored config lost user tools section:\n%s", restored)
	}
}
```

- [ ] **Step 3: Add current/env/token/list tests**

Write similar tests:

- `TestOpencodeCurrentReadsConfig`
- `TestOpencodeEnvPrintsExports`
- `TestOpencodeTokenPrintsSavedKey`
- `TestOpencodeListIncludesClaudeProviders`

Use the same patterns as the Codex tests in `code_switch_agent_test.go`.

- [ ] **Step 4: Run new tests**

```bash
go test -run TestOpencode ./...
```

Expected: PASS.

- [ ] **Step 5: Run full test suite**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add opencode_test.go
git commit -m "test: add opencode agent tests

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Manual verification

- [ ] **Step 1: Build**

```bash
go build -o cs .
```

Expected: no errors, `cs` binary created.

- [ ] **Step 2: Run all tests**

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 3: Interactive TUI smoke test**

```bash
./cs configure --agent opencode
```

Walk through:
- Confirm provider list includes all Claude providers.
- Select `deepseek`.
- Enter API key.
- Select `deepseek-v4-pro`.
- Run `./cs current --agent opencode` and verify provider/model/baseURL.

- [ ] **Step 4: Switch/restore round trip**

```bash
./cs switch openrouter --agent opencode --api-key sk-or-test
./cs current --agent opencode
./cs restore --agent opencode
./cs current --agent opencode
```

Verify restore removes managed settings.

- [ ] **Step 5: Commit final verification notes (optional)**

If any fixes were needed during manual verification, commit them with descriptive messages.

---

## Self-review checklist

- [ ] **Spec coverage:** Every design section (agent identity, config file, JSON format, provider coverage, switch/restore, command coverage, TUI, tests) maps to one or more tasks.
- [ ] **No placeholders:** All steps contain concrete file paths, code snippets, and commands.
- [ ] **Type consistency:** `agentOpencode` is used consistently; `OpencodeDir` field matches flag name; `opencodeProviderConfig` matches `codexProviderConfig` pattern.
- [ ] **DRY:** OpenCode reuses existing provider resolution, model lists, backup/write atomic helpers, and test patterns.
- [ ] **YAGNI:** No OPENCODE_CONFIG_DIR env support, no jsonc preservation, no per-provider SDK mappings in this iteration.

## Gaps / future work

- `OPENCODE_CONFIG_DIR` env var support.
- Preserve comments when rewriting `opencode.jsonc`.
- Native per-provider OpenCode SDK mappings if Anthropic-compatible mode is insufficient.
