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

Single-file Go project (~1450 lines). Code organization:

**Provider system:**
- `providerPresets` map (line 56): hardcoded built-in providers (minimax-cn, minimax-global, openrouter, opencode-go)
- `ProviderPreset` struct: name, base URL, default model, model list, per-tier models, extra env vars
- `StoredProvider` struct: persisted per-provider config (name, base URL, model, API key)
- `providerAliases` map (line 111): backwards-compatible aliases (minimax → minimax-cn)

**Config files:**
- App config: `~/.claude-switch/config.json` — stores per-provider settings including API keys
- Claude settings: `~/.claude/settings.json` — target file this tool modifies
- `managedEnvKeys` (line 119): env vars this tool writes/clears when switching

**TUI implementation:**
- Uses `tview` library for interactive arrow-key navigation
- `runArrowTUI()` (line 527): main TUI entry point with page-based navigation
- Falls back to text prompts when stdin is not a terminal (`shouldUseArrowTUI()` line 1297)
- Pages: providers list → provider detail → models list → custom forms

**Key functions:**
- `switchProvider()` (line 325): applies preset to settings.json with backup and atomic write
- `applyPreset()` (line 1311): clears managed env keys, sets new provider config
- `backupIfExists()` (line 1426): creates timestamped backup before modifying settings
- `writeJSONAtomic()` (line 1442): writes via temp file then rename for atomicity
- `detectProvider()` (line 446): identifies provider from base URL pattern

**CLI subcommands:** `list`, `configure` (default, interactive TUI), `current`, `set-key`, `switch`
