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

- **App config** (`~/.code-switch/config.json`): stores per-provider API keys, models. Handled by `loadAppConfig` / `writeJSONAtomic`. `loadAppConfig` migrates old `~/.claude-switch/config.json` once without deleting it.
- **Claude settings** (`~/.claude/settings.json`): the target this tool modifies. Backed up before write (`backupIfExists`), written atomically.
- **Codex config** (`~/.codex/config.toml`): Codex target config, managed for first-phase `ollama-cloud` support.

Agent config paths can be overridden with `--claude-dir`, `--codex-dir`, and `--opencode-dir`; app config path is always under `~/.code-switch/`.

## Architecture Notes

Multi-file project (~2700 loc source, ~5200 loc tests). Key sections:

- **Provider definitions**: `providerPresets` map in `presets.go`. Built-in providers: `deepseek`, `minimax-cn`, `minimax-global`, `openrouter`, `opencode-go`, `xiaomimimo-cn`, `ollama`, `ollama-cloud`, `zai`, `zhipu-cn`, `volcengine`, `kimi-coding`. Adding a provider means adding a `ProviderPreset` entry, updating `detectProvider()`, and updating the three shell completion string constants in `main.go`.
- **`Endpoints` field**: `ProviderPreset.Endpoints` is `map[ProviderProtocol]ProtocolEndpoint`. Each entry declares the upstream base URL + auth-env for one wire protocol (e.g. `anthropic-messages`, `openai-chat`, `openai-responses`). `presetEndpoint()` looks up `Endpoints[protocol]` first, then falls back to the legacy `BaseURL`/`AuthEnv` pair for `anthropic-messages` only. When adding a provider that speaks a non-Anthropic protocol, populate `Endpoints` directly rather than relying on the fallback.
- **Protocol registry**: `protocol_registry.go` defines `ProviderProtocol`, the `ProtocolAdapter` interface, and `ProtocolRegistry`. `defaultProtocolRegistry()` registers three adapters: `anthropicMessagesAdapter`, `openAIChatAdapter`, `openAIResponsesAdapter`. The registry is keyed by protocol name and by inbound `(method, path)` so the multi-route daemon can dispatch an incoming request to the correct adapter. Each adapter implements `ParseInboundRequest` / `BuildUpstreamRequest` / `ParseUpstreamResponse` / `WriteClientResponse` / `CanProxyFrom`, translating through the shared `IRRequest` / `IRResponse` IR in `protocol_ir.go`.
- **Decision engine**: `protocol.go` owns `AgentProfile`, `ConnectionPlan`, and `resolveConnection(agent, provider, preset, via)`. `agentProfiles` declares each agent's `ClientProtocol`, `DirectProtocols` (intersection-eligible for direct switch), and `ProxyUpstreamPreference` (ordered protocols the proxy may translate to). `resolveConnection` honors `via` = `auto` (try direct then proxy) / `direct` / `proxy`; an unknown value is rejected with a descriptive error. Direct connection requires a protocol the agent speaks natively; proxy connection requires an upstream protocol the daemon can translate to the agent's client protocol (`CanProxyFrom`).
- **`--via` flag**: `cs switch <provider> [--via auto|direct|proxy]` selects the connection mode. `auto` (default) prefers a direct switch when agent and provider share a protocol; otherwise it falls back to a proxy route. `direct` forces env-var/config rewrite and errors if no shared protocol exists. `proxy` forces a proxy route and errors if the provider has no proxy-compatible endpoint. `splitSwitchArgs` must keep `--via`/`-via` in its value-bearing flag list.
- **Multi-route daemon**: `proxy_cmd.go` + `proxy_server.go` + `proxy_lifecycle.go` implement `cs proxy configure|preview|status|start|stop|serve`. The daemon is **multi-route**: one process serves all configured agents (claude/codex/opencode) on a single port, dispatching by inbound `(method, path)` to the right adapter and route. `cs proxy configure <agent>` writes one route into `cfg.Proxy.Routes[<agent>]`; `cs proxy preview <agent>` resolves a single route; `cs proxy status` reports all configured routes. `proxy start` spawns `proxy serve` as a background child of the same binary.
- **Ollama dynamic models**: `ollamaModels()` probes `http://localhost:11434/api/tags` (3s timeout) and returns discovered model names, falling back to the static `Models` list. Do not assume the static list is the full set available.
- **Alias resolution**: `canonicalProviderName` resolves `providerAliases` (e.g. `minimax` → `minimax-cn`, `xiaomimimo` → `xiaomimimo-cn`).
- **Legacy migration**: `loadAppConfig` auto-migrates an old `minimax` config key to `minimax-cn` via `migrateLegacyProviders`.
- **Model tier mapping**: `withSelectedModel()` handles three cases: (1) custom model → override all tiers, (2) preset model with `ModelTierOverrides` → use those tiers, (3) preset model without overrides → keep preset defaults. This means selecting `minimax-m2.5` for opencode-go keeps tier models at `minimax-m2.7` — intentional, not a bug.
- **Auth env priority**: providers with `AuthEnv: ANTHROPIC_AUTH_TOKEN` (currently `minimax-cn`, `minimax-global`, `deepseek`, `xiaomimimo-cn`, `ollama`, `ollama-cloud`, and `kimi-coding`) write bearer tokens; providers without `AuthEnv` write to `ANTHROPIC_API_KEY`. Both keys are in `managedEnvKeys` and get cleared on switch to avoid duplicates.
- **`managedEnvKeys`**: the full set of env keys deleted before applying a new preset. If a provider's `ExtraEnv` doesn't include a key, it's simply not re-set.
- **Custom providers**: persist in `cfg.Providers` map. `sortedProviderNames()` filters out entries with empty `BaseURL`. Created only via TUI or fallback text prompts — `cmdSetKey` cannot create one from scratch.
- **CLI subcommands**: `list`, `configure` (default TUI), `current`, `set-key`, `switch` (with `--via`), `restore`, `test`, `remove`, `upgrade`, `proxy` (configure/preview/status/start/stop/serve), `run`, `completion` (bash/zsh/fish).

## Testing Gotchas

- `--help` passed to `runWithIO` enters `cmdConfigure`, which runs `flag.Parse` and returns `flag.ErrHelp` — it's an **error** path, not success.
- `runWithIO()` is the testable entrypoint. Tests set `HOME` via `t.Setenv("HOME", t.TempDir())` to isolate config files.
- `writeJSONAtomic` appends a trailing `\n` — tests that unmarshal written files don't need to account for this, but raw string checks should.
- Archive test helpers (`makeTarGzArchive`, `makeZipArchive`) are at the bottom of `main_test.go`.
- Shell completion tests (see `task6_help_completion_test.go`) assert that the second-level proxy word list appears verbatim as `configure start stop status preview serve` in both bash and fish output. Keep that exact ordering intact when editing the completion generators. zsh asserts each `'subcommand:` prefix is present, so the description text after the colon is free-form but the prefix is pinned.
- Protocol/proxy tests live in `protocol_test.go`, `protocol_registry_test.go`, `protocol_phase2_test.go`, `proxy_*_test.go`. They construct `AgentProfile` / `ConnectionPlan` directly and exercise `resolveConnection`, the registry's inbound dispatch, and the multi-route daemon's adapter selection. When adding a new agent or protocol, update both `agentProfiles` and the registry, then extend these tests.

## Common Pitfalls

- `uniqueCustomProviderKey` has a bounded loop (max 9998 iterations). It falls back to a nanosecond timestamp if exhausted — do not remove the bound.
- `splitSwitchArgs` must handle `--key=value` (no space) vs `--key value` (space). The former's value is embedded, the latter consumes the next arg.
- `canonicalProviderName` normalizes and resolves `providerAliases`. Always run user-entered provider names through it.
- `resolveSwitchPreset` validates models for opencode-go against `unsupportedOpenCodeGoAnthropicModels` — chat/completions-only models are rejected with a descriptive error.
- Release artifacts: `code-switch-{os}-{arch}.tar.gz` (or `.zip` on Windows), each containing a single `cs` or `cs.exe` file. The `upgrade` command's `upgradeAssetName()` must match this convention.
