# AGENTS.md

## Build and Test

```bash
go build -o cs .          # binary must be named 'cs' (CI packages it as 'cs' inside archives)
go test ./...              # all tests, single package
go test -run TestName .    # run one test
go vet ./...               # static analysis
```

Go 1.22+. Two dependencies: `tview` + `tcell` for TUI. Everything in `package main` — no subpackages.

## After Each Fix

Every bug fix or code change must be verified by running both unit tests and a build verification:

```bash
go vet ./... && go test ./... && go build -o cs .
```

A fix is not complete until all three pass:
- `go vet ./...` — no static analysis warnings
- `go test ./...` — all unit tests pass (no regressions)
- `go build -o cs .` — binary compiles without errors

If the fix touches provider logic, also run `cs test <provider>` (if that provider's API key is configured) as a smoke test.

## Versioning

`main.version` defaults to `"dev"`. CI injects it via `-ldflags="-X main.version=${VERSION}"`. Do not hardcode a version string; always use the `version` variable.

## Config Files This Tool Touches

- **App config** (`~/.claude-switch/config.json`): stores per-provider API keys, models. Handled by `loadAppConfig` / `writeJSONAtomic`.
- **Claude settings** (`~/.claude/settings.json`): the target this tool modifies. Backed up before write (`backupIfExists`), written atomically.

Both paths can be overridden: settings path via `--claude-dir`, app config path is always under `~/.claude-switch/`.

## Architecture Notes

Single-file project. Key sections in `main.go`:

- **Provider definitions**: `providerPresets` map (~line 70). Adding a provider means adding a `ProviderPreset` entry here and to `detectProvider()`.
- **Model tier mapping**: `withSelectedModel()` handles three cases: (1) custom model → override all tiers, (2) preset model with `ModelTierOverrides` → use those tiers, (3) preset model without overrides → keep preset defaults. This means selecting `minimax-m2.5` for opencode-go keeps tier models at `minimax-m2.7` — intentional, not a bug.
- **Auth env priority**: `deepseek` writes to `ANTHROPIC_AUTH_TOKEN` (via `AuthEnv` field); all others write to `ANTHROPIC_API_KEY`. Both keys are in `managedEnvKeys` and get cleared on switch to avoid duplicates.
- **`managedEnvKeys`**: the full set of env keys deleted before applying a new preset. If a provider's `ExtraEnv` doesn't include a key, it's simply not re-set.
- **Custom providers**: persist in `cfg.Providers` map. `sortedProviderNames()` filters out entries with empty `BaseURL`. Created only via TUI or fallback text prompts — `cmdSetKey` cannot create one from scratch.

## Testing Gotchas

- `printUsage()` writes directly to `os.Stdout`, not the `out io.Writer` parameter. Tests that check help output must capture `os.Stdout`.
- `--help` passed to `runWithIO` enters `cmdConfigure`, which runs `flag.Parse` and returns `flag.ErrHelp` — it's an **error** path, not success.
- `runWithIO()` is the testable entrypoint. Tests set `HOME` via `t.Setenv("HOME", t.TempDir())` to isolate config files.
- `writeJSONAtomic` appends a trailing `\n` — tests that unmarshal written files don't need to account for this, but raw string checks should.
- Archive test helpers (`makeTarGzArchive`, `makeZipArchive`) are at the bottom of `main_test.go`.

## Common Pitfalls

- `uniqueCustomProviderKey` has a bounded loop (max 9998 iterations). It falls back to a nanosecond timestamp if exhausted — do not remove the bound.
- `splitSwitchArgs` must handle `--key=value` (no space) vs `--key value` (space). The former's value is embedded, the latter consumes the next arg.
- `canonicalProviderName` normalizes and resolves `providerAliases`. Always run user-entered provider names through it.
- `resolveSwitchPreset` validates models for opencode-go against `unsupportedOpenCodeGoAnthropicModels` — chat/completions-only models are rejected with a descriptive error.
- Release artifacts: `claude-switch-{os}-{arch}.tar.gz` (or `.zip` on Windows), each containing a single `cs` or `cs.exe` file. The `upgrade` command's `upgradeAssetName()` must match this convention.
