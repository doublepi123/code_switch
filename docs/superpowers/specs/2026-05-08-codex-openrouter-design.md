# Codex OpenRouter Support + Model Search

## Summary

Extend Codex agent to support OpenRouter as a provider (currently only `ollama-cloud` is supported), with dynamic model fetching from OpenRouter API and a search/filter UI on the model selection page.

## Architecture

### 1. Codex OpenRouter Preset (`agent.go`)

Follow the existing `codexOllamaCloudPreset()` pattern:

```go
func codexOpenRouterPreset() ProviderPreset {
    preset := providerPresets["openrouter"]
    preset.BaseURL = "https://openrouter.ai/api/v1"
    preset.AuthEnv = "OPENROUTER_API_KEY"
    preset.ForceModelTiers = true
    return preset
}
```

Differences from the Claude-facing `openrouter` preset:
- **BaseURL**: `https://openrouter.ai/api/v1` (with `/v1` suffix for Codex)
- **AuthEnv**: `OPENROUTER_API_KEY` (command-based auth, not inline key)
- **ForceModelTiers**: `true` (Codex uses a single model, no tier separation)

**Whitelist expansion** in `resolveAgentProviderPreset()`:
- From: only `"ollama-cloud"` allowed
- To: `"ollama-cloud"` and `"openrouter"` allowed
- Same change in `resolveAgentSwitchPreset()` and `providerNamesForAgent()`

### 2. Provider Name Mapping

Add a helper to map preset keys to TOML provider names:

```go
func codexTOMLProviderName(provider string) string {
    switch provider {
    case "openrouter":
        return "OpenRouter"
    default:
        return provider // "ollama-cloud" → "ollama-cloud"
    }
}
```

Used by `applyCodexPresetTOML()`, `removeCodexManagedTOML()`, and `isManagedCodexModel()` for consistency.

### 3. TOML Generation (`codex.go`)

Current `applyCodexPresetTOML()` hardcodes `model_provider = "ollama-cloud"` and the TOML section name. It must be parameterized to generate provider-specific output.

**For ollama-cloud** (unchanged behavior):
```toml
model = "qwen3-coder:480b"
model_provider = "ollama-cloud"

[model_providers.ollama-cloud]
name = "Ollama Cloud"
base_url = "https://ollama.com/v1"
wire_api = "responses"
...
```

**For openrouter** (new):
```toml
model = "anthropic/claude-sonnet-4.6"
model_provider = "OpenRouter"

[model_providers.OpenRouter]
name = "OpenRouter"
base_url = "https://openrouter.ai/api/v1"
wire_api = "responses"

[model_providers.OpenRouter.auth]
command = "cs"
args = ["token", "openrouter", "--agent", "codex"]
```

Key parameterizations:
- `model_provider` top-level key: `"OpenRouter"` for openrouter, `"ollama-cloud"` for ollama-cloud
- `[model_providers.<name>]` section header: uses actual provider identity
- `base_url`: from `preset.BaseURL`
- `name`: from `preset.Name`
- `auth` args: `["token", "<provider>", "--agent", "codex"]`
- `reasoning_effort`: only output when preset has it configured

**Cleanup functions** must also be updated:
- `removeCodexManagedTOML()`: recognize both `[model_providers.ollama-cloud]` and `[model_providers.OpenRouter]` sections
- `isManagedCodexModel()`: recognize models from both presets

### 4. Dynamic Model Fetching (`presets.go`)

New function `discoverOpenRouterModels()`:

```go
type openRouterModelsResponse struct {
    Data []openRouterModelData `json:"data"`
}

type openRouterModelData struct {
    ID string `json:"id"`
}

func discoverOpenRouterModels(cfg *AppConfig) []string
```

- `GET https://openrouter.ai/api/v1/models` with 3s timeout
- Requires API key in `Authorization` header (read from config)
- Fails silently, returns `nil` on any error
- Returns sorted list of model IDs

Wrapped by `openRouterModels(cfg *AppConfig) []string` — follows `ollamaModels()` pattern: if discovery succeeds use it, otherwise fall back to static `preset.Models`.

**Call site**: `providerModelsForAgent()` (agent.go:133) — for Codex, currently returns `preset.Models` directly. Must add dynamic model discovery here:
```go
func providerModelsForAgent(cfg *AppConfig, agent AgentName, provider string) []string {
    if agent == agentCodex {
        preset, err := resolveAgentProviderPreset(agent, provider, cfg)
        if err != nil {
            return nil
        }
        if provider == "openrouter" {
            if models := openRouterModels(cfg); len(models) > 0 {
                return models
            }
        }
        // ... fallback to preset.Models
    }
    return providerModels(cfg, provider)
}
```

This also affects `defaultSelectionModelForAgent()` and `modelIndexForAgent()` which call `providerModelsForAgent()` — they automatically benefit from dynamic models.

### 5. Model Search/Filter (`tui.go`)

`showModels()` gets a search input field above the model list:

```
┌──────────────────────────────┐
│ Search:                      │  ← InputField
├──────────────────────────────┤
│ anthropic/claude-sonnet-4.6  │
│ anthropic/claude-opus-4.5    │  ← Filtered model list
│ anthropic/claude-3.5-sonnet  │
│ Custom model...              │
└──────────────────────────────┘
```

Behavior:
- **Filter mode**: typing filters model list in real-time (case-insensitive substring match)
- **`/` key**: focuses the search input
- **`Esc`** while in search input: clears search, returns focus to model list
- **Empty search**: shows all models
- **`r` key**: triggers model refresh (calls `discoverOpenRouterModels()` and rebuilds the list)
- **Custom model** option always visible at bottom
- Initial focus: on search input (not model list)

Implementation:
- `tview.InputField` with `SetChangedFunc()` to trigger re-filtering
- Reuse the existing `flex` layout, insert InputField above the list

### 6. Help Text (`main.go`)

`printUsage()` updated:
```
Codex providers:
  ollama-cloud
  openrouter
```

### 7. Shell Completions (`main.go`)

`providerCompletionWordList()` already includes all preset names including `openrouter` — no change needed, but verify it works with the `--agent codex` context.

## Data Flow

```
User: cs switch openrouter --agent codex
    │
    ├─ parseAgentName("codex") → agent = agentCodex
    ├─ resolveAgentSwitchPreset(agentCodex, "openrouter", cfg)
    │   └─ codexOpenRouterPreset() → preset with BaseURL + AuthEnv overrides
    ├─ switchCodexProvider("openrouter", cfg, apiKey, model, codexDir, out)
    │   ├─ resolveAgentSwitchPreset() again
    │   ├─ applyCodexPresetTOML(preset) → generates TOML with:
    │   │   model_provider = "OpenRouter"
    │   │   [model_providers.OpenRouter]
    │   │   base_url = "https://openrouter.ai/api/v1"
    │   │   wire_api = "responses"
    │   │   [model_providers.OpenRouter.auth]
    │   │   command = "cs" args = ["token", "openrouter", "--agent", "codex"]
    │   ├─ writeTextAtomic() → ~/.codex/config.toml
    │   └─ setAgentProviderConfig() → ~/.code-switch/config.json
    │       under Agents["codex"].Providers["openrouter"]
    └─ Done
```

## Testing

- New test: `cs switch openrouter --agent codex` writes correct TOML
- New test: `cs switch ollama-cloud --agent codex` still works (no regression)
- New test: `discoverOpenRouterModels()` parsing of API response
- New test: model filter in TUI (unit test the filter function)
- Existing codex tests must continue passing

## Non-Goals

- No changes to Claude-side openrouter behavior
- No custom provider support for Codex (still only presets)
- No pagination in model list
- No OpenRouter model refresh for Claude agent
