# Code Switch Agent Support Design

## Goal

Create a successor project for `claude-switch` named `code-switch` at `git@github.com:doublepi123/code_switch.git`, while keeping the installed binary name `cs`. The new tool must preserve all current Claude Code switching behavior and add first-phase Codex support for Ollama Cloud.

## Scope

The first phase supports two agents:

- `claude`: existing Claude Code behavior and all existing providers.
- `codex`: Ollama Cloud only, using Codex's Responses API configuration.

The default agent is `claude` when no agent is specified, so existing commands such as `cs switch openrouter` and `cs current` remain compatible.

The TUI starts with an agent selection page. After selecting `Claude Code` or `Codex`, the user sees the provider page for that agent. That provider page includes a `Restore official config...` action for the selected agent.

## Configuration Files

The app config moves from:

```text
~/.claude-switch/config.json
```

to:

```text
~/.code-switch/config.json
```

On startup, loading follows these rules:

1. If `~/.code-switch/config.json` exists, read and write only that file.
2. If the new config does not exist but `~/.claude-switch/config.json` exists, migrate once by reading the old config, applying existing legacy provider migration, and writing the result to `~/.code-switch/config.json`.
3. If neither file exists, start with an empty config and write the new path on save.
4. Do not delete or modify the old `~/.claude-switch/config.json`.

The migrated top-level `providers` map remains the canonical Claude provider store, so existing Claude API keys and selected models work immediately after migration.

Codex-specific provider state is stored under an optional `agents.codex.providers` map. Codex may reuse a migrated top-level `providers.ollama-cloud.apiKey` as an initial saved key if no Codex-specific key exists. Once the user saves a Codex provider config, it is written to `agents.codex.providers.ollama-cloud` so Codex model selection does not overwrite Claude's selected model.

## CLI Behavior

Commands keep the existing shape and add `--agent claude|codex` where relevant:

- `cs list [--agent claude|codex] [--verbose]`
- `cs configure [--agent claude|codex] [--dry-run] [--reset-key]`
- `cs current [--agent claude|codex]`
- `cs switch <provider> [--agent claude|codex] [--api-key ...] [--model ...] [--dry-run]`
- `cs restore [--agent claude|codex] [--dry-run]`
- `cs test <provider> [--agent claude|codex] [--api-key ...] [--model ...]`

Default behavior without `--agent` remains Claude. Bare `cs` opens the TUI agent selection page.

## Claude Writer

Claude switching keeps the existing `~/.claude/settings.json` writer:

- Resolve provider and model with existing preset logic.
- Back up the settings file before writes.
- Clear `managedEnvKeys`.
- Write the selected provider's `ANTHROPIC_*` env values and extra env values.
- Preserve unrelated settings.

`restore --agent claude` backs up `~/.claude/settings.json`, removes only `managedEnvKeys` from `env`, removes `env` if it becomes empty, and preserves all other settings.

## Codex Writer

Codex writes `~/.codex/config.toml` by default, with a `--codex-dir DIR` override where needed for tests and scripted use.

Codex Ollama Cloud writes a custom provider. Codex provider IDs `openai`, `ollama`, and `lmstudio` are reserved by Codex, so this tool uses `ollama-cloud`.

The written TOML fields are:

```toml
model = "<selected-model>"
model_provider = "ollama-cloud"

[model_providers.ollama-cloud]
name = "Ollama Cloud"
base_url = "https://ollama.com/v1"
env_key = "OLLAMA_API_KEY"
env_key_instructions = "Set OLLAMA_API_KEY to your Ollama API key"
wire_api = "responses"
```

Codex must use the Responses API. The tool does not support or generate `wire_api = "chat"`.

The API key is saved only in `~/.code-switch/config.json`; it is not written directly to Codex TOML. Codex's config points to `OLLAMA_API_KEY` via `env_key`. Users can load the saved key into the current shell with:

```bash
eval "$(cs env ollama-cloud --agent codex)"
```

`restore --agent codex` backs up `~/.codex/config.toml`, removes only this tool's managed Codex settings, and preserves unrelated configuration:

- Remove top-level `model_provider` only when it is `ollama-cloud`.
- Remove top-level `model` only when `model_provider` is managed by this tool and the model matches the tool's selected or known Codex model.
- Remove `[model_providers.ollama-cloud]`.
- Preserve profiles, projects, MCP servers, plugins, approval policy, sandbox settings, and other user keys.

## TUI Flow

The TUI starts with an agent selection page:

```text
Claude Code
Codex
```

Selecting an agent opens that agent's provider page:

- Claude lists all current Claude providers plus `Restore official config...`.
- Codex lists only `ollama-cloud` plus `Restore official config...`.

Provider detail, API key editing, model selection, custom model entry, and apply flow follow the existing TUI patterns.

Selecting `Restore official config...` opens a confirmation page. Confirming runs restore for the selected agent and exits the TUI with a restored message. Cancelling returns to that agent's provider page.

## Provider Support

Claude keeps current providers:

- `deepseek`
- `minimax-cn`
- `minimax-global`
- `openrouter`
- `opencode-go`
- `xiaomimimo-cn`
- `ollama`
- `ollama-cloud`
- custom providers

Codex first phase supports only:

- `ollama-cloud`

Codex Ollama Cloud model defaults come from the existing Ollama Cloud model list where appropriate, with default `qwen3-coder:480b`. Codex test and switch logic must target `/v1/responses`, not chat completions.

## Project Rename

Public naming changes:

- README title becomes `code-switch`.
- Repository references become `git@github.com:doublepi123/code_switch.git`.
- Upgrade default repo becomes `doublepi123/code_switch`.
- Version output becomes `code-switch <version>`.
- Release archive names become `code-switch-{os}-{arch}.tar.gz` or `.zip`.
- Archive contents remain `cs` or `cs.exe`.

The Go module may remain simple and single-package. The binary must continue to be built as `cs`.

## Tests

Implementation must use test-first changes for new behavior. Required coverage:

- App config migration from `~/.claude-switch/config.json` to `~/.code-switch/config.json`.
- New config wins when both old and new configs exist.
- Saves after migration write only `~/.code-switch/config.json`.
- Default agent is Claude.
- `--agent codex` works for `list`, `current`, `switch`, `configure`, and `restore`.
- Existing Claude switch/current behavior remains compatible.
- Claude restore removes only managed env keys and preserves unrelated settings.
- Codex switch writes `model_provider`, `model`, `[model_providers.ollama-cloud]`, `base_url`, `env_key`, and `wire_api = "responses"`.
- Codex switch does not write API key plaintext to `~/.codex/config.toml`.
- Codex restore removes only managed Codex provider settings and preserves unrelated TOML content.
- Codex never writes `wire_api = "chat"`.
- TUI agent/provider state includes restore entries after agent selection.
- README, usage, completion, upgrade repo, and archive names reflect `code-switch`.

Final verification must pass:

```bash
go vet ./... && go test ./... && go build -o cs .
```

If provider logic changes and the needed API key is configured, run the matching `cs test <provider>` smoke test.

## External References

- OpenAI Codex configuration reference: https://developers.openai.com/codex/config-reference
- OpenAI Codex advanced configuration: https://developers.openai.com/codex/config-advanced
- Ollama Codex integration: https://docs.ollama.com/integrations/codex
- Ollama OpenAI compatibility: https://docs.ollama.com/openai
