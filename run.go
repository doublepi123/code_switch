package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// cmdRun implements `cs run <agent> --provider <provider> [--model model-id] [--dry-run]`.
//
// Argument shape is strict: the agent MUST be the first positional argument
// and every flag MUST come after it. This matches the documented usage and
// keeps the command predictable for shell completion. `cs run --provider X codex`
// is rejected as a usage error rather than silently re-interpreted.
//
// MVP scope:
//   - Only agent=codex is supported; other agents return an error.
//   - --provider is required.
//   - Only --dry-run is implemented; a real launch returns errNotImplemented.
//
// The provider/model/key are resolved through the Claude resolver so every
// Claude preset is selectable; the upstream provider key is NEVER printed.
// Dry-run emits the proxy plan (agent/provider/model/protocol/urls/env) and
// the rendered codex config.toml.
func cmdRun(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: code-switch run <agent> --provider <provider> [--model model-id] [--dry-run]")
	}
	// The agent is the mandatory first positional argument. Any leading flag
	// (e.g. `cs run --provider X codex`) is a usage error: flags belong after
	// the agent so completion and parsing stay unambiguous.
	if strings.HasPrefix(args[0], "-") {
		return errors.New("usage: code-switch run <agent> --provider <provider> [--model model-id] [--dry-run]")
	}
	agent, err := parseAgentName(args[0])
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	providerFlag := fs.String("provider", "", "provider to route through the proxy (required)")
	modelFlag := fs.String("model", "", "override model id")
	dryRun := fs.Bool("dry-run", false, "preview the proxy plan without launching the agent")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	// Reject any trailing positional args or leftover flags: the only
	// positional argument is the agent, which we already consumed.
	if fs.NArg() != 0 {
		return errors.New("usage: code-switch run <agent> --provider <provider> [--model model-id] [--dry-run]")
	}
	if agent != agentCodex {
		return fmt.Errorf("unsupported agent %q (MVP supports only %q)", agent, agentCodex)
	}
	if strings.TrimSpace(*providerFlag) == "" {
		return errors.New("--provider is required (usage: code-switch run <agent> --provider <provider> [--dry-run])")
	}
	if !*dryRun {
		return errors.New("only --dry-run is implemented in the MVP; real agent launch is not yet supported")
	}

	// Resolve provider/model/key via the Claude resolver so every Claude preset
	// is selectable. The resolved key is forwarded to the proxy internally and
	// must never appear in the dry-run output.
	pa, cfg, _, err := resolveProviderAndKeyForAgent(agentClaude, *providerFlag, "", *modelFlag)
	if err != nil {
		return err
	}

	// pa.Model is only the raw --model input (empty when omitted); derive the
	// final model the same way `switch` does so dry-run reflects what would
	// actually be used: preset default, stored model, or --model override.
	preset, err := resolveSwitchPreset(pa.Provider, cfg, *modelFlag)
	if err != nil {
		return err
	}

	token, err := randomProxyToken()
	if err != nil {
		return fmt.Errorf("generate proxy token: %w", err)
	}

	codexHome := filepath.Join(os.TempDir(), fmt.Sprintf("code-switch-codex-%d", os.Getpid()))

	// MVP only supports the Anthropic Messages upstream protocol (see
	// proxy_server.go); surface it explicitly so users know which adapter
	// their provider will be routed through.
	const upstreamProtocol = string(protocolAnthropicMessages)
	const proxyBaseURL = "http://127.0.0.1:<port>/v1"

	// Build the proxy route via the shared helper so the dry-run preview
	// reflects exactly what the (future) real launch path would install —
	// including any persisted cfg.ModelMappings for this provider. The
	// route's Model and ModelMappings are derived here rather than read
	// ad-hoc from the preset/config, keeping a single source of truth.
	route := buildProxyRoute(pa.Provider, preset, cfg, protocolAnthropicMessages, token)
	model := route.Model

	fmt.Fprintf(out, "agent: %s\n", agent)
	fmt.Fprintf(out, "provider: %s\n", pa.Provider)
	fmt.Fprintf(out, "model: %s\n", model)
	fmt.Fprintf(out, "upstream_protocol: %s\n", upstreamProtocol)
	fmt.Fprintf(out, "proxy_base_url: %s\n", proxyBaseURL)
	if len(route.ModelMappings) > 0 {
		fmt.Fprintf(out, "model_mappings: %d\n", len(route.ModelMappings))
	}
	fmt.Fprintf(out, "CODEX_HOME=%s\n", codexHome)
	fmt.Fprintf(out, "CODE_SWITCH_PROXY_API_KEY=%s\n", token)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "codex config.toml:")
	fmt.Fprint(out, renderProxyCodexConfig(model))
	return nil
}

// randomProxyToken returns a random opaque token of the form
// "csproxy-<32 hex chars>". It uses crypto/rand so the token is
// unpredictable; the hex encoding keeps it safe to embed in TOML and
// shell environments without quoting.
func randomProxyToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "csproxy-" + hex.EncodeToString(buf[:]), nil
}

// renderProxyCodexConfig returns the TOML fragment that configures Codex to
// route through the local code-switch proxy. The proxy port is allocated at
// launch time, so the template emits the literal "<port>" placeholder; the
// real launch path (not yet implemented) will substitute the bound port.
func renderProxyCodexConfig(model string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model = %s\n", tomlQuoteBasicString(model))
	b.WriteString("model_provider = \"code-switch-proxy\"\n")
	b.WriteString("\n[model_providers.code-switch-proxy]\n")
	b.WriteString("name = \"code-switch proxy\"\n")
	b.WriteString("base_url = \"http://127.0.0.1:<port>/v1\"\n")
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString("env_key = \"CODE_SWITCH_PROXY_API_KEY\"\n")
	return b.String()
}

// buildProxyRoute constructs a ProxyRoute for the given provider, injecting
// any persisted per-provider model mappings (cfg.ModelMappings[provider])
// into the route so the proxy's model-resolution layer can rewrite client
// model names to upstream models. It is the single wiring point between
// persisted config and the proxy's runtime route table.
//
// The function deliberately does NOT launch a daemon or bind a port: it is a
// pure value-builder so it can be unit-tested in isolation and reused by
// both the (future) real launch path and the `run --dry-run` preview. The
// caller supplies the chosen upstream protocol and the local token the
// proxy will enforce.
//
// ModelMappings is defensive-copied: mutating the returned route's map does
// not mutate cfg.ModelMappings, so a caller cannot accidentally corrupt the
// persisted config via the route.
func buildProxyRoute(provider string, preset ProviderPreset, cfg *AppConfig, upstreamProtocol ProviderProtocol, localToken string) ProxyRoute {
	route := ProxyRoute{
		Provider:         provider,
		Model:            preset.Model,
		UpstreamProtocol: upstreamProtocol,
		UpstreamBaseURL:  preset.BaseURL,
		LocalToken:       localToken,
	}
	if cfg != nil && len(cfg.ModelMappings[provider]) > 0 {
		route.ModelMappings = make(map[string]string, len(cfg.ModelMappings[provider]))
		for k, v := range cfg.ModelMappings[provider] {
			route.ModelMappings[k] = v
		}
	}
	return route
}
