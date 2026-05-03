# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test

```bash
go build -o cs .       # Build binary named 'cs'
go run .               # Run directly without building
go test ./...           # Run all tests
go test -run TestName   # Run specific test
```

## Quality Requirements

Before submitting any change:

1. **Unit tests must pass** — all existing tests in `main_test.go`
2. **Verify interactive flow end-to-end** — run `go run .` and walk through:
   - Provider selection with arrow keys
   - Entering API key
   - Model selection
   - Switching to a different provider and confirming with `go run . current`
3. **Check UI/UX** — confirm:
   - No text garbling or encoding issues
   - TUI renders correctly in terminal
   - Help text is visible and accurate
4. **Verify logic edge cases**:
   - `--reset-key` re-prompts for API key
   - Re-running configure reuses saved keys
   - Custom provider creation works
   - `--claude-dir` override works
5. **Compile without errors** — `go build -o cs .` succeeds
6. **Integration check**: save a key, switch provider, confirm current output matches expectations

## Architecture

Multi-file Go project (~2700 lines of source + ~5200 lines of tests).

**Source files:**
- `main.go` (420 lines): CLI entry point, subcommand dispatch, shell completions, utility functions
- `config.go` (200 lines): config file I/O, atomic writes with 0o600 permissions, backups, legacy migration
- `presets.go` (490 lines): provider presets, types (ProviderPreset, StoredProvider, AppConfig), model resolution, detection
- `switch.go` (156 lines): `switch` subcommand, `applyPreset()` which writes env vars to settings.json
- `tui.go` (730 lines): interactive TUI via tview, fallback text prompts, custom provider forms
- `test.go` (137 lines): `test` subcommand for API connectivity checks
- `upgrade.go` (583 lines): self-upgrade from GitHub releases with checksum verification

**Provider system:**
- `providerPresets` map in `presets.go`: built-in providers (minimax-cn, minimax-global, openrouter, opencode-go, deepseek, xiaomimimo-cn, ollama)
- `providerAliases` map: backwards-compatible aliases (minimax → minimax-cn, etc.)
- `StoredProvider` struct: persisted per-provider config (name, base URL, model, API key, authEnv)

**Config files:**
- App config: `~/.claude-switch/config.json` — stores per-provider settings including API keys (chmod 0o600)
- Claude settings: `~/.claude/settings.json` — target file this tool modifies
- `managedEnvKeys` in `presets.go`: env vars this tool writes/clears when switching

**TUI implementation:**
- Uses `tview` library for interactive arrow-key navigation
- `runArrowTUI()` in `tui.go`: main TUI entry point with page-based navigation
- Falls back to text prompts when stdin is not a terminal (`shouldUseArrowTUI()`)
- Pages: providers list → provider detail → models list → custom forms

**Key functions:**
- `switchProvider()` in `switch.go`: applies preset to settings.json with backup and atomic write
- `applyPreset()` in `switch.go`: clears managed env keys, sets new provider config
- `backupIfExists()` in `config.go`: creates timestamped backup before modifying settings
- `writeJSONAtomic()` in `config.go`: writes via temp file then rename, chmod 0o600
- `detectProvider()` in `presets.go`: identifies provider from base URL pattern

**CLI subcommands:** `list`, `configure` (default, interactive TUI), `current`, `set-key`, `switch`, `test`, `remove`, `upgrade`, `completion`
