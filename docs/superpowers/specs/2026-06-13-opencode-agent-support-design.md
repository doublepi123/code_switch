# OpenCode Agent Support Design

**Date:** 2026-06-13  
**Status:** Approved  
**Topic:** Add `opencode` as a first-class agent target alongside Claude Code and Codex.

## Goal

Allow users to configure the [OpenCode CLI](https://opencode.ai/docs) using the same `code-switch` interface they already use for Claude Code and Codex. After this change, running `cs switch <provider> --agent opencode` will write the correct configuration to OpenCode's config file, and all existing commands (`current`, `env`, `token`, `restore`, `list`, `configure`, `test`, `set-key`, `remove`) will support `--agent opencode`.

## Background

OpenCode CLI stores its configuration in `~/.config/opencode/opencode.json` (JSON/JSONC). It natively supports the Anthropic provider and allows overriding `baseURL` and `apiKey` via the `provider.anthropic.options` object. API keys can be referenced with the `{env:VAR_NAME}` syntax, which avoids storing plaintext secrets in the config file.

Because OpenCode's Anthropic provider accepts a custom `baseURL`, every provider already supported by Claude Code (all presets plus custom providers) can be reused directly for OpenCode without converting to an OpenAI-compatible endpoint.

## Design

### 1. Agent identity

- Add a new `AgentName` constant: `agentOpencode = "opencode"`.
- `agentDisplayName(agentOpencode)` returns `"OpenCode"`.
- `parseAgentName` accepts `"opencode"`.

### 2. Configuration storage

OpenCode provider settings are stored in the existing `AppConfig.Agents` map:

```json
{
  "agents": {
    "opencode": {
      "providers": {
        "deepseek": {
          "apiKey": "sk-test",
          "model": "deepseek-v4-pro"
        }
      }
    }
  }
}
```

This mirrors the Codex agent storage model and keeps per-agent API keys isolated.

### 3. Target config file

- Default path: `~/.config/opencode/opencode.json`
- Override flag: `--opencode-dir DIR` resolves to `DIR/opencode.json`
- Future: respect `OPENCODE_CONFIG_DIR`, but not required for the first iteration.

### 4. Written JSON format

When switching provider `deepseek`, the tool writes:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "deepseek-v4-pro",
  "provider": {
    "anthropic": {
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "{env:ANTHROPIC_AUTH_TOKEN}"
      }
    }
  }
}
```

#### Field mapping

| Claude env / preset field | OpenCode JSON path                                  |
|---------------------------|-----------------------------------------------------|
| `ANTHROPIC_MODEL`         | `model`                                             |
| `ANTHROPIC_BASE_URL`      | `provider.anthropic.options.baseURL`                |
| `ANTHROPIC_API_KEY`       | `provider.anthropic.options.apiKey` → `{env:ANTHROPIC_API_KEY}` |
| `ANTHROPIC_AUTH_TOKEN`    | `provider.anthropic.options.apiKey` → `{env:ANTHROPIC_AUTH_TOKEN}` |

The API key is **never** written in plaintext. The config references the same auth env variable that Claude Code uses, so `cs env --agent opencode` and `cs token --agent opencode` remain useful.

### 5. Provider coverage

All Claude Code providers are available to OpenCode:

- Every entry in `providerPresets`.
- Every custom provider stored in `cfg.Providers`.
- Provider aliases resolve exactly as they do for Claude Code.

There is no separate "opencode provider list" to maintain.

### 6. Switch flow

1. Resolve the provider preset using the existing `resolveProviderPreset` (same as Claude Code).
2. Build an OpenCode-specific preset by keeping `Model` and `BaseURL` unchanged.
3. Determine the auth env from `preset.AuthEnv`, defaulting to `ANTHROPIC_API_KEY`.
4. Read existing `opencode.json`, remove managed keys, then inject:
   - `model`
   - `provider.anthropic.options.baseURL`
   - `provider.anthropic.options.apiKey` as `{env:<authEnv>}`
5. Write atomically with `writeTextAtomic`.
6. Persist the selected provider/API key/model in `AppConfig.Agents["opencode"]`.

### 7. Restore flow

`cs restore --agent opencode`:

1. Read `opencode.json`.
2. Remove managed keys: `model` and `provider.anthropic`.
3. If `provider` becomes empty, remove it.
4. If the file becomes empty (or only `$schema` remains), delete `opencode.json`.
5. Otherwise, write the cleaned content back.
6. Create a backup before modifying, as with Claude/Codex.

### 8. Command coverage

All subcommands gain `--agent opencode` support:

| Command     | Notes                                                       |
|-------------|-------------------------------------------------------------|
| `configure` | TUI and fallback prompts include OpenCode agent option.     |
| `switch`    | Writes `opencode.json` via new `switchOpencodeProvider`.    |
| `current`   | Parses `model` and `provider.anthropic.options.baseURL`.    |
| `set-key`   | Stores key under `Agents.opencode.Providers`.               |
| `env`       | Prints exports for `ANTHROPIC_BASE_URL`, `ANTHROPIC_MODEL`, and auth env. |
| `token`     | Prints raw stored API key.                                  |
| `restore`   | Removes managed OpenCode settings.                          |
| `test`      | Reuses existing Anthropic `/v1/messages` test path.         |
| `remove`    | Removes stored provider config.                             |
| `list`      | Lists all Claude Code providers because all are reusable.   |

### 9. TUI changes

- Agent selection page includes "OpenCode".
- `tuiState` gains an `opencodeDir` field.
- Provider list for OpenCode uses the same list as Claude Code (all presets + custom + restore).
- Model list uses existing `providerModels()`.
- No tier overrides (haiku/sonnet/opus/subagent) UI is shown for OpenCode, because OpenCode only consumes a single `model` field.

### 10. New file: `opencode.go`

Responsibilities:

- `opencodeConfigPath`
- `switchOpencodeProvider`
- `applyOpencodePresetJSON`
- `restoreOpencodeConfig`
- `removeOpencodeManagedJSON`
- `currentOpencodeProvider`

Uses standard library only (`encoding/json`, `strings`).

### 11. Existing file modifications

- `presets.go`: add `agentOpencode` constant.
- `agent.go`: extend agent parsing, display, config helpers, and preset resolution to include `agentOpencode`.
- `switch.go`: dispatch to `switchOpencodeProvider` when `agent == agentOpencode`.
- `config.go`: `upsertProviderConfig` handles `agentOpencode`.
- `main.go`: `cmdCurrent`, `cmdList`, usage text, shell completions.
- `tui.go`: agent selection, provider/model lists, directory handling.
- `restore.go`: dispatch to `restoreOpencodeConfig`.
- `env.go` / `test.go`: agent-aware output and test path selection.

### 12. Testing

New file `opencode_test.go` covers:

1. `switch --agent opencode` writes correct `opencode.json` with env-based API key.
2. API key is not stored plaintext in `opencode.json`.
3. `AppConfig.Agents["opencode"]` stores the selected provider and key.
4. Custom provider baseURL is preserved unchanged.
5. `restore --agent opencode` removes managed fields and deletes empty config.
6. `current --agent opencode` displays correct provider/model/baseURL.
7. `env --agent opencode` and `token --agent opencode` produce expected output.
8. `list --agent opencode` includes Claude Code providers.

Manual verification per `CLAUDE.md`:

- `go build -o cs .`
- `go test ./...`
- Interactive `go run .` flow for OpenCode, then `go run . current --agent opencode`.

## Trade-offs

- **Always use `provider.anthropic`**: OpenCode supports multiple provider backends, but mapping every Claude provider into the Anthropic provider with a custom `baseURL` is the smallest, most consistent change. If OpenCode later requires provider-specific SDK options, we can introduce per-provider mappings later.
- **No plaintext API keys**: Using `{env:...}` keeps the config file safe, but users must export the correct env var or rely on `cs env`/`cs token`. This matches the existing Codex command-auth philosophy.
- **No separate opencode provider list**: Reusing all Claude providers avoids a second list to maintain, but the TUI/help text must make it clear that OpenCode supports the same set.

## Future work

- Respect `OPENCODE_CONFIG_DIR` env var.
- Support `opencode.jsonc` (comments) without stripping user comments.
- Add per-provider OpenCode SDK mappings if needed (e.g., native OpenRouter provider instead of Anthropic-compatible).
- Add OpenCode-specific tool/permission defaults.

## Sources

- [OpenCode Configuration Docs](https://opencode.ai/docs/config)
- [OpenCode Providers Docs](https://opencode.ai/docs/providers)
- [OpenCode Models Docs (zh-CN)](https://opencode.ai/docs/zh-cn/models/)
- [OpenCode GitHub Repository](https://github.com/anomalyco/opencode)
