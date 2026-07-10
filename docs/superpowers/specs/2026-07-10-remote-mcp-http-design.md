# Remote HTTP MCP Design

## Goal

Extend the unified MCP registry so one remote HTTP MCP server definition can
be rendered into valid native configuration for Claude Code, Codex, and
OpenCode. Remote HTTP replaces the registry's legacy `sse` transport; this
iteration does not retain SSE compatibility.

## Scope

- Support two registry transports: `stdio` and `http`.
- Add remote HTTP servers with `cs mcp add <name> --transport http --url <url>`
  and repeatable `--header "Name: Value"` flags.
- Preserve `Headers`, `Disabled`, `AllowedTools`, and `BlockedTools` in the
  common server configuration.
- Generate native remote MCP configuration for all three target agents.
- Probe configured HTTP MCP endpoints from `cs mcp test` and `cs doctor`.
- Reject legacy `sse` configuration when a command would consume or generate
  agent MCP configuration, while keeping `cs mcp list` and `cs mcp remove`
  available so users can remove legacy entries.

## Non-goals

- Supporting legacy Server-Sent Events transport.
- Implementing OAuth flows, dynamic header helpers, or agent-specific
  credential stores.
- Adding MCP management to the TUI.
- Changing existing stdio MCP behavior.

## Configuration Model

`MCPServerConfig.Transport` accepts only `stdio` and `http`.

- A `stdio` server requires `Command`; `Args` and `Env` remain optional.
- An `http` server requires an absolute `http` or `https` URL. `Headers` are
  optional static request headers.
- `AllowedTools` and `BlockedTools` retain their existing semantics. A blocked
  tool always overrides an allowed tool.
- `Disabled` suppresses the server from all generated agent configurations.

New and updated server definitions accept only `stdio` and `http`. A command
that consumes the registry for generation or probing validates every configured
server; an `sse` entry returns an actionable error explaining that it must be
recreated with `--transport http`. The application does not silently
reinterpret an SSE URL as an HTTP MCP endpoint. `cs mcp list` and `cs mcp
remove` intentionally inspect the raw registry without this strict validation,
so users can discover and remove a legacy entry.

## CLI Behavior

The MCP add command accepts exactly one of these forms:

```text
cs mcp add <name> --transport stdio --command <cmd> [-- arg ...]
cs mcp add <name> --transport http --url <url> [--header "Name: Value" ...]
```

The `--header` flag is repeatable, requires a non-empty header name, and splits
only on the first colon so values may contain additional colons. Duplicate
header names are rejected. The help output and validation messages remove SSE
examples. `cs mcp list` continues to display the configured transport and
command or URL. `cs mcp test` uses the same transport-aware health probe as
`cs doctor`.

## Agent Configuration Mapping

| Registry transport | Claude Code | Codex | OpenCode |
| --- | --- | --- | --- |
| `stdio` | `mcpServers.<name>.command/args/env` | `[mcp_servers.<name>] command/args/env` | `mcp.<name> = { type = "local", command = [...], environment = ... }` |
| `http` | `mcpServers.<name> = { type = "http", url, headers }` | `[mcp_servers.<name>] url`, plus `http_headers` | `mcp.<name> = { type = "remote", url, headers }` |

Agent generators must never emit an SSE-specific field. Claude and OpenCode
receive copied JSON header maps. Codex receives an equivalent TOML
`http_headers` table. Header values are copied rather than sharing mutable map
references with the app configuration.

OpenCode continues to emit `allowedTools` and `blockedTools`. Claude and Codex
continue to omit tool filtering in this iteration: the existing Codex
generator contract does not emit a confirmed native mapping, and this work
does not broaden that contract.

## Configuration Lifecycle and Cleanup

Managed MCP names are tracked per target agent, rather than in one global list.
Before a switch or restore, the target config removes only the names last
written for that target agent, preserves unrelated user entries, and merges
the current registry output. On success it synchronizes that target agent's
managed-name list to the current registry names.

Existing configurations with the legacy global `ManagedMCPNames` field migrate
once during config normalization: its names seed each of Claude, Codex, and
OpenCode's per-agent managed-name list, then the legacy field is cleared. This
ensures an old entry is cleaned from each agent the next time that agent is
switched or restored.

This lifecycle applies identically to remote HTTP and stdio servers. Removing
an HTTP server followed by a switch or restore removes its previous generated
entry without deleting a user-owned entry with any other name.

## Health Checks and Errors

HTTP health checks make a bounded `GET` request to the configured URL with the
configured headers and the existing three-second deadline.

- A response below HTTP 400 is healthy.
- Network failures, context expiry, malformed URLs, and HTTP 4xx/5xx results
  are reported with the server name and actionable error detail.
- A failed remote check produces a doctor warning, not a process failure for
  unrelated doctor checks. Its detail includes the server name.
- Doctor skips disabled servers; explicit `cs mcp test <name>` still probes a
  disabled server so it can be tested before re-enabling.
- Stdio health-check behavior remains unchanged.

## Testing and Acceptance Criteria

Unit tests must cover:

1. validation accepts `http` and rejects `sse`, invalid URLs, invalid headers,
   and an HTTP server without a URL;
2. CLI parsing, repeated headers, legacy SSE list/remove behavior, and help
   for remote HTTP addition;
3. native Claude, Codex, and OpenCode remote configuration, including headers;
4. disabled remote servers and removal cleanup while preserving user entries;
5. HTTP health checks for success, failure status, timeout, header forwarding,
   and disabled-server skipping in doctor;
6. per-agent managed-name migration and cleanup after another agent has been
   switched or restored;
7. existing stdio regression coverage.

The iteration is complete only when `go vet ./...`, `go test ./...`, and
`go build -o cs .` succeed.

## Risks and Decisions

- Remote MCP standards and agent support evolve independently. The registry
  deliberately uses the stable product term `http`, while each generator owns
  the target-specific native representation.
- Dropping SSE is a deliberate compatibility break requested for this
  iteration. Existing users must remove and recreate those entries as HTTP
  servers before generation, testing, or doctor can consume the registry.
- OAuth and environment-derived header support are deferred because they need
  secure, agent-specific credential semantics beyond static header mapping.
