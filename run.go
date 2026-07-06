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
// The upstream provider key is NEVER printed. Dry-run emits the temporary
// environment that would be injected into the selected agent process.
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
	if strings.TrimSpace(*providerFlag) == "" {
		return errors.New("--provider is required (usage: code-switch run <agent> --provider <provider> [--dry-run])")
	}
	if !*dryRun {
		return launchAgent(agent, *providerFlag, *modelFlag, "", out)
	}

	pa, cfg, _, err := resolveProviderAndKeyForAgent(agent, *providerFlag, "", *modelFlag)
	if err != nil {
		return err
	}
	preset, err := resolveAgentSwitchPreset(agent, pa.Provider, cfg, *modelFlag)
	if err != nil {
		return err
	}
	plan, err := resolveConnection(agent, pa.Provider, preset, "auto")
	if err != nil {
		return err
	}
	plan = adjustLaunchConnectionPlan(agent, plan, preset)
	pairs, err := launchEnvPairs(agent, preset, plan, "<redacted>")
	if err != nil {
		return err
	}
	if plan.Mode == connectionModeProxy {
		proxyPreset := preset
		proxyPreset.BaseURL = proxyBaseURLPlaceholder(agent != agentClaude)
		proxyPreset.AuthEnv = ""
		proxyPlan := plan
		proxyPlan.Endpoint = ProtocolEndpoint{BaseURL: proxyPreset.BaseURL}
		if agent == agentClaude {
			proxyPlan.Endpoint.AuthEnv = "ANTHROPIC_AUTH_TOKEN"
		}
		pairs, err = launchEnvPairs(agent, proxyPreset, proxyPlan, "<proxy-token>")
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "agent: %s\n", agent)
	fmt.Fprintf(out, "provider: %s\n", pa.Provider)
	fmt.Fprintf(out, "model: %s\n", preset.Model)
	fmt.Fprintf(out, "mode: %s\n", plan.Mode)
	fmt.Fprintf(out, "upstream_protocol: %s\n", plan.UpstreamProtocol)
	if plan.Mode == connectionModeProxy {
		fmt.Fprintf(out, "proxy_base_url: %s\n", proxyBaseURLPlaceholder(agent != agentClaude))
		if len(cfg.ModelMappings[pa.Provider]) > 0 {
			fmt.Fprintf(out, "model_mappings: %d\n", len(cfg.ModelMappings[pa.Provider]))
		}
	}
	for _, pair := range pairs {
		if strings.Contains(pair.Key, "KEY") || strings.Contains(pair.Key, "TOKEN") {
			fmt.Fprintf(out, "%s=<redacted>\n", pair.Key)
			continue
		}
		fmt.Fprintf(out, "%s=%s\n", pair.Key, pair.Value)
	}
	if agent == agentCodex {
		codexHome := filepath.Join(os.TempDir(), fmt.Sprintf("code-switch-codex-%d", os.Getpid()))
		fmt.Fprintf(out, "CODEX_HOME=%s\n", codexHome)
		if plan.Mode == connectionModeProxy {
			fmt.Fprintln(out, "auth: command-backed (cs token code-switch-proxy --agent codex)")
			fmt.Fprintln(out)
			fmt.Fprintln(out, "codex config.toml:")
			fmt.Fprint(out, renderProxyCodexConfig(preset.Model))
		}
	}
	return nil
}

func proxyBaseURLPlaceholder(v1 bool) string {
	if v1 {
		return "http://127.0.0.1:<port>/v1"
	}
	return "http://127.0.0.1:<port>"
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
	return renderProxyCodexConfigForBaseURL(model, "http://127.0.0.1:<port>/v1")
}

// renderProxyCodexConfigForBaseURL is the baseURL-parameterized form of
// renderProxyCodexConfig. The `cs proxy preview` path knows the configured
// host/port and renders the codex config with a concrete, usable base_url
// (e.g. "http://0.0.0.0:18080/v1") rather than the bare "<port>" placeholder,
// so a user can copy the previewed fragment directly into a config.toml.
//
// The original renderProxyCodexConfig(model) is retained for backwards
// compatibility with the `cs run --dry-run` path, which genuinely does not
// know the port until launch time and therefore keeps the placeholder.
func renderProxyCodexConfigForBaseURL(model, baseURL string) string {
	return renderProxyCodexConfigForBaseURLWithCatalog(model, baseURL, "")
}

func renderProxyCodexConfigForBaseURLWithCatalog(model, baseURL, catalogPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model = %s\n", tomlQuoteBasicString(model))
	b.WriteString("model_provider = \"code-switch-proxy\"\n")
	if strings.TrimSpace(catalogPath) != "" {
		fmt.Fprintf(&b, "model_catalog_json = %s\n", tomlQuoteBasicString(catalogPath))
	}
	b.WriteString("\n[model_providers.code-switch-proxy]\n")
	b.WriteString("name = \"code-switch proxy\"\n")
	fmt.Fprintf(&b, "base_url = %s\n", tomlQuoteBasicString(baseURL))
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString("\n[model_providers.code-switch-proxy.auth]\n")
	b.WriteString("command = \"cs\"\n")
	b.WriteString("args = [\"token\", \"code-switch-proxy\", \"--agent\", \"codex\"]\n")
	return b.String()
}

// renderProxyCodexConfigForBaseURLWithCatalogProtocol is the protocol-aware
// version of renderProxyCodexConfigForBaseURLWithCatalog. It uses the upstream
// protocol to select the correct wire_api value ("responses" or "chat")
// instead of hardcoding "responses". This is needed when the proxy route
// targets an OpenAI Chat upstream (e.g. deepseek, openrouter) rather than
// an OpenAI Responses upstream.
func renderProxyCodexConfigForBaseURLWithCatalogProtocol(model, baseURL, catalogPath string, protocol ProviderProtocol) string {
	var b strings.Builder
	fmt.Fprintf(&b, "model = %s\n", tomlQuoteBasicString(model))
	b.WriteString("model_provider = \"code-switch-proxy\"\n")
	if strings.TrimSpace(catalogPath) != "" {
		fmt.Fprintf(&b, "model_catalog_json = %s\n", tomlQuoteBasicString(catalogPath))
	}
	b.WriteString("\n[model_providers.code-switch-proxy]\n")
	b.WriteString("name = \"code-switch proxy\"\n")
	fmt.Fprintf(&b, "base_url = %s\n", tomlQuoteBasicString(baseURL))
	fmt.Fprintf(&b, "wire_api = %s\n", tomlQuoteBasicString(codexWireAPIForProtocol(protocol)))
	b.WriteString("\n[model_providers.code-switch-proxy.auth]\n")
	b.WriteString("command = \"cs\"\n")
	b.WriteString("args = [\"token\", \"code-switch-proxy\", \"--agent\", \"codex\"]\n")
	return b.String()
}

// buildProxyRoute constructs a ProxyRoute for the given provider, injecting
// the supplied model mappings into the route so the proxy's model-resolution
// layer can rewrite client model names to upstream models. It is the single
// wiring point between preset/config values and the proxy's runtime route
// table.
//
// The function deliberately does NOT launch a daemon or bind a port: it is a
// pure value-builder so it can be unit-tested in isolation and reused by
// both the (future) real launch path and the `run --dry-run` preview. The
// caller supplies the chosen upstream protocol, the local token the proxy
// will enforce, and the model mappings to inject.
//
// ModelMappings is defensive-copied: mutating the returned route's map does
// not mutate the caller's map, so a caller cannot accidentally corrupt its
// own source (e.g. cfg.ModelMappings or a ProxyRouteConfig snapshot) via the
// route.
//
// The signature intentionally takes only the values it uses (provider, preset,
// protocol, token, mappings) and NOT the surrounding *AppConfig. This keeps
// the helper low-coupling and prevents callers from assuming it reads other
// fields from cfg.
func buildProxyRoute(provider string, preset ProviderPreset, upstreamProtocol ProviderProtocol, localToken string, mappings map[string]string) ProxyRoute {
	upstreamBaseURL := preset.BaseURL
	upstreamAuthEnv := preset.AuthEnv
	if endpoint, ok := preset.presetEndpoint(upstreamProtocol); ok {
		upstreamBaseURL = endpoint.BaseURL
		upstreamAuthEnv = endpoint.AuthEnv
	}
	route := ProxyRoute{
		Provider:         provider,
		Model:            preset.Model,
		UpstreamProtocol: upstreamProtocol,
		UpstreamBaseURL:  upstreamBaseURL,
		UpstreamAuthEnv:  upstreamAuthEnv,
		LocalToken:       localToken,
		ModelMappings:    copyStringMap(mappings),
	}
	return route
}
