# Per-Tier Model Configuration

## Problem

When switching providers, all Claude model tiers (Haiku, Sonnet, Opus, Subagent) are derived from the single selected model. Users cannot configure each tier independently — e.g., using a provider's Opus model as the main model while keeping Haiku and Sonnet on their respective lighter models.

## Solution

Allow per-tier model overrides stored in the config. Preset defaults apply when no override is set. Users can configure tiers via TUI (Edit Tiers page) or CLI flags.

## Config Format

Extend `StoredProvider` with optional tier fields:

```go
type StoredProvider struct {
    Name     string `json:"name,omitempty"`
    BaseURL  string `json:"baseUrl,omitempty"`
    Model    string `json:"model,omitempty"`
    APIKey   string `json:"apiKey,omitempty"`
    AuthEnv  string `json:"authEnv,omitempty"`
    Haiku    string `json:"haiku,omitempty"`
    Sonnet   string `json:"sonnet,omitempty"`
    Opus     string `json:"opus,omitempty"`
    Subagent string `json:"subagent,omitempty"`
}
```

When a tier field is non-empty, it overrides the preset default. When empty, the preset default applies. Fully backwards compatible — existing configs without tier fields continue to work.

## Tier Resolution Logic

In `resolveSwitchPreset`, after computing the preset with `withSelectedModel`, apply stored tier overrides:

1. Compute preset via `withSelectedModel(preset, model)`
2. Look up `cfg.Providers[provider]` for stored tier overrides
3. If `stored.Haiku != ""`, set `preset.Haiku = stored.Haiku` (same for Sonnet, Opus, Subagent)
4. This applies to both `switch` subcommand and TUI configure flow

## TUI Changes

### Model List Page

The tier info line (`haiku: X | sonnet: Y | opus: Z | sub: W`) already displays derived tiers. Add a new action:

- **"t" → Edit Tiers**: Opens the tier configuration page

### Tier Configuration Page

A form with 4 dropdown fields, each pre-filled with the current tier model:

- Haiku model (default: preset's Haiku)
- Sonnet model (default: preset's Sonnet)
- Opus model (default: preset's Opus)
- Subagent model (default: preset's Subagent)

Each field allows selecting from the provider's model list or typing a custom model name. Leaving a field at its default value means "use preset default" (stored as empty string).

Add keybindings:
- Enter → Save tiers and return to detail page
- Esc/q → Cancel and return to detail page

### Detail Page

Add "Edit Tiers" action with shortcut 't':

```
actions.AddItem("Edit Tiers", "", 't', func() {
    ts.showTierConfig(provider, "detail")
})
```

## CLI Changes

Add tier flags to `switch` and `configure` subcommands:

```
cs switch openrouter --model anthropic/claude-opus-4.7 --haiku anthropic/claude-haiku-4.5 --sonnet anthropic/claude-sonnet-4.6
cs configure --haiku anthropic/claude-haiku-4.5
```

Flags:
- `--haiku <model>`: Override Haiku tier model
- `--sonnet <model>`: Override Sonnet tier model
- `--opus <model>`: Override Opus tier model
- `--subagent <model>`: Override Subagent tier model

These flags are stored in the config and persist across switches.

## Smart Defaults Behavior

When selecting a model (e.g., `anthropic/claude-opus-4.7` on OpenRouter), the tier mappings are automatically derived from the preset:

- Haiku → `anthropic/claude-haiku-4.5` (preset default)
- Sonnet → `anthropic/claude-sonnet-4.6` (preset default)
- Opus → `anthropic/claude-opus-4.7` (preset default)

Users only need to manually override tiers when they want something different from the preset's defaults. The TUI shows the current tier mapping after model selection so users can see exactly what will be applied.

## `cs current` Output

Add tier model display to the `current` command output:

```
Claude Code
  settings: /home/user/.claude/settings.json
  provider: openrouter
  base_url: https://openrouter.ai/api
  model: anthropic/claude-opus-4.7
  haiku: anthropic/claude-haiku-4.5
  sonnet: anthropic/claude-sonnet-4.6
  opus: anthropic/claude-opus-4.7
```

## Files to Modify

- `presets.go`: Add tier fields to `StoredProvider`, update `resolveSwitchPreset` to apply stored overrides
- `switch.go`: Add `--haiku`, `--sonnet`, `--opus`, `--subagent` flags to switch command
- `tui.go`: Add tier configuration page, Edit Tiers action, tier info updates
- `main.go`: Add tier flags to configure command, update `cmdCurrent` output, update `cmdList` output
- `config.go`: Update `upsertProviderConfig` to persist tier overrides
- `main_test.go`: Add tests for tier override logic