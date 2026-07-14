package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

// cmdProxy dispatches the `cs proxy <subcommand>` family. Subcommands:
//
//   - configure <agent> --provider <p> [--model m] [--protocol pr] [--host h] [--port n]
//   - preview <agent>
//   - status              (lifecycle: show proxy runtime state)
//   - start / stop / serve (lifecycle: launch / terminate / foreground the proxy)
//
// The dispatcher is intentionally strict about the subcommand name so typos
// surface immediately rather than being silently treated as a configure
// positional argument.
func cmdProxy(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: code-switch proxy <configure|start|stop|status|stats|preview|serve> ...")
	}
	switch args[0] {
	case "-h", "--help", "help":
		printProxyUsage(out)
		return nil
	case "configure":
		return cmdProxyConfigure(args[1:], out)
	case "preview":
		return cmdProxyPreview(args[1:], out)
	case "status":
		return cmdProxyStatus(args[1:], out)
	case "stats":
		return cmdProxyStats(args[1:], out)
	case "start":
		return cmdProxyStart(args[1:], out)
	case "stop":
		return cmdProxyStop(args[1:], out)
	case "serve":
		return cmdProxyServe(args[1:], out)
	default:
		return fmt.Errorf("unknown proxy subcommand %q (supported: configure, start, stop, status, stats, preview, serve)", args[0])
	}
}

func printProxyUsage(out io.Writer) {
	fmt.Fprint(out, `code-switch proxy

Usage:
	  cs proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port] [--fallback-provider provider] [--fallback-url url]
      write one route of the multi-route proxy daemon
  cs proxy preview <agent>
      show the resolved proxy route for one agent
  cs proxy status
      show proxy runtime status for all configured routes
  cs proxy stats [--log path] [--since duration|RFC3339] [--agent name] [--json]
      aggregate request counts, latency, and token usage from the proxy JSONL log
  cs proxy start
      launch the multi-route proxy daemon as a background process
  cs proxy stop
      terminate a running proxy daemon
  cs proxy serve
      run the multi-route proxy HTTP daemon in the foreground
`)
}

// proxyConfigureUsage is the canonical usage string for `cs proxy configure`.
const proxyConfigureUsage = "usage: code-switch proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port] [--fallback-provider provider] [--fallback-url url]"

// cmdProxyConfigure writes a ProxyRouteConfig for the given agent into the
// persisted AppConfig. It is the only writer of the proxy block, which keeps
// the omitempty semantics intact: a config that has never been configured
// stays proxy-free on disk.
//
// Validation:
//   - provider is canonicalized and must resolve to a known preset or a
//     stored custom provider; otherwise an error is returned.
//   - protocol, when explicitly passed via --protocol, must be one of the
//     supported ProviderProtocol values (resolved via resolveProxyProtocol)
//     and must be compatible with the agent (validated via
//     validateProxyAgentProtocol). When --protocol is OMITTED, the
//     agent-specific default (defaultProxyProtocolForAgent) is used so the
//     persisted UpstreamProtocol is never empty and the route is valid by
//     construction; this prevents a stale-route error at preview/serve time.
//   - agent must be a supported proxy agent (codex or claude).
//   - the RAW (pre-TrimSpace/pre-canonicalize) values of
//     agent/provider/model/protocol/host are screened for Unicode control
//     characters via rejectControlChars to prevent header/log/config
//     injection. Validating the raw value is essential: a trailing newline
//     would otherwise be silently trimmed into a valid-looking token.
//
// Host/Port handling:
//   - Port is validated against [0, 65535]; out-of-range ports are rejected
//     and the config is NOT written.
//   - When --host/--port are NOT explicitly passed, the existing global
//     Host/Port are preserved (so configuring a second agent does not reset
//     the listen address to the default). When they ARE passed, they
//     overwrite the global values, which are then normalized.
func cmdProxyConfigure(args []string, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New(proxyConfigureUsage)
	}
	agent := strings.TrimSpace(args[0])
	if agent == "" {
		return errors.New(proxyConfigureUsage)
	}
	fs := flag.NewFlagSet("proxy configure", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	providerFlag := fs.String("provider", "", "provider")
	modelFlag := fs.String("model", "", "model")
	protocolFlag := fs.String("protocol", "", "upstream protocol (defaults to anthropic-messages at preview time)")
	hostFlag := fs.String("host", "127.0.0.1", "proxy listen host")
	portFlag := fs.Int("port", 0, "proxy listen port (0 = auto-allocate at serve time)")
	fallbackProviderFlag := fs.String("fallback-provider", "", "fallback provider")
	fallbackURLFlag := fs.String("fallback-url", "", "fallback upstream base URL")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*providerFlag) == "" {
		return errors.New(proxyConfigureUsage)
	}

	provider := canonicalProviderName(*providerFlag)

	// Validate all free-form RAW string inputs for control-character
	// injection BEFORE TrimSpace/canonicalize. Validating the raw value
	// (not the trimmed/canonicalized one) is essential: a trailing
	// newline like "zhipu-cn\n" would otherwise be silently trimmed into
	// the valid-looking "zhipu-cn" and slip past this guard. The raw
	// agent positional is args[0] (before TrimSpace), the raw provider
	// is the untouched flag value, and so on.
	for label, value := range map[string]string{
		"agent":             args[0], // raw positional, pre-TrimSpace
		"provider":          *providerFlag,
		"model":             *modelFlag,
		"protocol":          *protocolFlag,
		"host":              *hostFlag,
		"fallback provider": *fallbackProviderFlag,
		"fallback url":      *fallbackURLFlag,
	} {
		if err := rejectControlChars(label, value); err != nil {
			return err
		}
	}

	// Port range check. Use the raw flag value (already parsed to int) so a
	// negative or oversized port is rejected before any config write. The
	// normalization layer only clamps negatives to 0; we want explicit
	// out-of-range values to surface as errors, not be silently coerced.
	if *portFlag < 0 || *portFlag > 65535 {
		return fmt.Errorf("--port %d out of range (must be 0..65535)", *portFlag)
	}

	// Host shape validation. A listen host is NOT a URL: it must be a bare
	// hostname / IP (with optional surrounding brackets for IPv6). Reject
	// any value that carries a scheme, a path separator, or whitespace so
	// the persisted route never ends up with a host that would either fail
	// to bind or silently pollute rendered base URLs. Only validate when
	// --host was explicitly provided; the existing-config / default
	// fallback paths are validated separately at serve time.
	hostProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "host" {
			hostProvided = true
		}
	})
	if hostProvided {
		if err := validateProxyHost(*hostFlag); err != nil {
			return err
		}
	}

	// Agent must be one the proxy MVP supports (codex/claude). This catches
	// typos like "codx" before they land in a route that preview/serve would
	// then reject.
	agentSupported := false
	for _, a := range supportedProxyAgentList {
		if a == agent {
			agentSupported = true
			break
		}
	}
	if !agentSupported {
		return fmt.Errorf("unsupported agent %q for proxy (MVP supports: %s)", agent, strings.Join(supportedProxyAgentList, ", "))
	}

	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}

	// Protocol resolution. When the caller explicitly passed --protocol,
	// validate it against the agent/protocol matrix up-front so the
	// stored route is one the proxy can actually serve. When --protocol
	// was omitted, fall back to the agent-specific default
	// (defaultProxyProtocolForAgent) so the persisted UpstreamProtocol is
	// never empty and the route is valid by construction — a later
	// `preview`/`serve` then never surfaces a stale-route error caused
	// purely by the omission of --protocol.
	protocolProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "protocol" {
			protocolProvided = true
		}
	})
	var protocolResolved ProviderProtocol
	if protocolProvided {
		resolved, err := resolveProxyProtocol(*protocolFlag)
		if err != nil {
			return err
		}
		protocolResolved = resolved
		if err := validateProxyAgentProtocol(agent, protocolResolved); err != nil {
			return err
		}
		if !providerCanUseProxyProtocol(preset, protocolResolved) {
			return fmt.Errorf("provider %q has no %s endpoint", provider, protocolResolved)
		}
	} else {
		profile, ok := agentProfiles[AgentName(agent)]
		if !ok {
			return fmt.Errorf("unsupported agent %q", agent)
		}
		// Resolve the agent-specific default first, then walk
		// ProxyUpstreamPreference and pick the first protocol the provider
		// actually supports. This keeps omit-protocol configurations valid
		// for any provider (the persisted route must never end up with a
		// protocol the provider cannot serve).
		defaultProto := defaultProxyProtocolForAgent(agent)
		if validateProxyAgentProtocol(agent, defaultProto) == nil && providerCanUseProxyProtocol(preset, defaultProto) {
			protocolResolved = defaultProto
		} else {
			found := false
			for _, candidate := range profile.ProxyUpstreamPreference {
				if validateProxyAgentProtocol(agent, candidate) != nil {
					continue
				}
				if !providerCanUseProxyProtocol(preset, candidate) {
					continue
				}
				protocolResolved = candidate
				found = true
				break
			}
			if !found {
				return fmt.Errorf("provider %q has no proxy-compatible endpoint for agent %q", provider, agent)
			}
		}
	}
	fallbackConfig, err := resolveProxyFallbackConfig(provider, *fallbackProviderFlag, *fallbackURLFlag, protocolResolved, cfg, AgentName(agent))
	if err != nil {
		return err
	}

	if cfg.Proxy == nil {
		cfg.Proxy = &ProxyConfig{}
	}
	if cfg.Proxy.Routes == nil {
		cfg.Proxy.Routes = map[string]ProxyRouteConfig{}
	}

	// Host/Port: only overwrite the global values when the user explicitly
	// passed --host/--port. fs.Visit only iterates flags that were set,
	// which is exactly the "was it explicitly provided?" signal we need;
	// an absent flag falls back to the existing persisted value rather than
	// the flag default, so re-configuring a second agent does not silently
	// reset the listen address.
	//
	// NOTE: hostProvided was already computed above for the host-shape
	// validation, so we only compute portProvided here.
	portProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "port" {
			portProvided = true
		}
	})
	if hostProvided {
		cfg.Proxy.Host = strings.TrimSpace(*hostFlag)
	}
	if portProvided {
		cfg.Proxy.Port = *portFlag
	}
	// First-time config: ensure a sane default host is written even when the
	// caller omitted --host, so the block is never left with an empty host.
	if cfg.Proxy.Host == "" {
		cfg.Proxy.Host = "127.0.0.1"
	}
	normalized := normalizeProxyConfig(*cfg.Proxy)
	cfg.Proxy.Host = normalized.Host
	cfg.Proxy.Port = normalized.Port

	routeToken := ""
	var preservedMappings map[string]string
	if existing, ok := cfg.Proxy.Routes[agent]; ok {
		routeToken = strings.TrimSpace(existing.Token)
		preservedMappings = existing.ModelMappings
	}
	if routeToken == "" {
		generated, err := randomProxyRouteToken()
		if err != nil {
			return err
		}
		routeToken = generated
	}

	cfg.Proxy.Routes[agent] = ProxyRouteConfig{
		Agent:            agent,
		Provider:         provider,
		Model:            strings.TrimSpace(*modelFlag),
		UpstreamProtocol: string(protocolResolved),
		Token:            routeToken,
		ModelMappings:    preservedMappings,
		Fallback:         fallbackConfig,
	}

	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "configured proxy route %s -> %s\n", agent, provider)
	return nil
}

func resolveProxyFallbackConfig(primaryProvider, providerRaw, urlRaw string, protocol ProviderProtocol, cfg *AppConfig, agent AgentName) (*ProxyRouteConfig, error) {
	provider := canonicalProviderName(providerRaw)
	baseURL := strings.TrimSpace(urlRaw)
	if provider == "" && baseURL == "" {
		return nil, nil
	}
	if provider == "" {
		provider = primaryProvider
	}
	fallback := &ProxyRouteConfig{Provider: provider, UpstreamProtocol: string(protocol), BaseURL: baseURL}
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return nil, fmt.Errorf("unsupported fallback provider %q", provider)
	}
	if !providerCanUseProxyProtocol(preset, protocol) && baseURL == "" {
		return nil, fmt.Errorf("fallback provider %q has no %s endpoint", provider, protocol)
	}
	if !sameCanonicalProvider(provider, primaryProvider) && !preset.NoAPIKey && strings.TrimSpace(storedAPIKeyForAgent(cfg, agent, provider)) == "" {
		return nil, fmt.Errorf("fallback provider %q has no API key configured; run `cs set-key %s <key>` before configuring it", provider, provider)
	}
	fallback.Model = preset.Model
	return fallback, nil
}

// cmdProxyPreview renders the resolved proxy route for an agent without
// launching anything. It mirrors the shape of `cs run --dry-run` so users
// can see exactly what serve would install: agent, provider, resolved model,
// upstream protocol, the proxy base URL template, the configured port, the
// model-mapping count, and the Codex config.toml fragment.
//
// The provider API key is NEVER printed. The route is built with a literal
// "<token>" placeholder local token so preview output is safe to share.
func cmdProxyPreview(args []string, out io.Writer) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return errors.New("usage: code-switch proxy preview <agent>")
	}
	agent := strings.TrimSpace(args[0])
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	route, err := buildProxyRouteFromConfig(agent, cfg, "<token>")
	if err != nil {
		return err
	}

	proxyCfg := normalizeProxyConfig(*cfg.Proxy)
	// IPv6-safe URL construction: use net.JoinHostPort so an IPv6 listen host
	// like "::1" is bracketed. A naive fmt.Sprintf("http://%s:%d/v1", host,
	// port) would produce the malformed "http://::1:8080/v1". The same
	// bracketing is applied to:
	//   - codexBaseURL (the concrete host:port for the copy-pasteable
	//     config.toml fragment)
	//   - proxyBaseURLTemplate (the placeholder form shown to the user)
	// Both must be IPv6-safe; rendering one and not the other would leave a
	// broken URL in the output.
	hostPort := net.JoinHostPort(proxyCfg.Host, strconv.Itoa(proxyCfg.Port))
	codexBaseURL := "http://" + hostPort + "/v1"
	hostPortTemplate := net.JoinHostPort(proxyCfg.Host, "<port>")
	proxyBaseURLTemplate := "http://" + hostPortTemplate + "/v1"
	fmt.Fprintf(out, "agent: %s\n", agent)
	fmt.Fprintf(out, "provider: %s\n", route.Provider)
	fmt.Fprintf(out, "model: %s\n", route.Model)
	fmt.Fprintf(out, "upstream_protocol: %s\n", route.UpstreamProtocol)
	fmt.Fprintf(out, "proxy_base_url: %s\n", proxyBaseURLTemplate)
	fmt.Fprintf(out, "configured_port: %d\n", proxyCfg.Port)
	fmt.Fprintf(out, "model_mappings: %d\n", len(route.ModelMappings))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "codex config.toml:")
	fmt.Fprint(out, renderProxyCodexConfigForBaseURLWithCatalogProtocol(route.Model, codexBaseURL, "", route.UpstreamProtocol))
	return nil
}
