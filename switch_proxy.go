package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	proxyDaemonIsRunning = defaultProxyDaemonIsRunning
	startProxyDaemon     = defaultStartProxyDaemon
	stopProxyDaemon      = defaultStopProxyDaemon
)

func resetProxyDaemonHooks() {
	proxyDaemonIsRunning = defaultProxyDaemonIsRunning
	startProxyDaemon = defaultStartProxyDaemon
	stopProxyDaemon = defaultStopProxyDaemon
}

func writeClaudeProxyConfig(port int, token string) error {
	return writeClaudeProxyConfigInDir("", port, token)
}

func writeClaudeProxyConfigInDir(claudeDir string, port int, token string) error {
	settingsPath := claudeSettingsPath(claudeDir)
	cf := newConfigFile(settingsPath)
	unlock, err := cf.lock()
	if err != nil {
		return err
	}
	defer unlock()

	root, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}
	if err := backupIfExists(settingsPath); err != nil {
		return err
	}
	env := ensureNestedMap(root, "env")
	for _, key := range managedEnvKeys {
		delete(env, key)
	}
	env["ANTHROPIC_BASE_URL"] = proxyBaseURL(port, false)
	env["ANTHROPIC_AUTH_TOKEN"] = strings.TrimSpace(token)
	return writeJSONAtomic(settingsPath, root)
}

func writeCodexProxyConfig(port int, token string, upstreamProtocol ProviderProtocol) error {
	return writeCodexProxyConfigInDir("", port, token, upstreamProtocol, "code-switch-proxy")
}

func writeCodexProxyConfigInDir(codexDir string, port int, token string, upstreamProtocol ProviderProtocol, model string) error {
	configPath := codexConfigPath(codexDir)
	cf := newConfigFile(configPath)
	unlock, err := cf.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := backupIfExists(configPath); err != nil {
		return err
	}
	if strings.TrimSpace(model) == "" {
		model = "code-switch-proxy"
	}
	catalogPath := codexModelCatalogPath(codexDir)
	if err := writeCodexModelCatalog(catalogPath, model); err != nil {
		return err
	}
	return writeTextAtomic(configPath, renderProxyCodexConfigForBaseURLWithCatalogProtocol(model, proxyBaseURL(port, true), catalogPath, upstreamProtocol), 0o600)
}

func writeOpencodeProxyConfig(port int, token string) error {
	return writeOpencodeProxyConfigInDir("", port, token, "code-switch-proxy")
}

func writeOpencodeProxyConfigInDir(opencodeDir string, port int, token string, model string) error {
	configPath := opencodeConfigPath(opencodeDir)
	cf := newConfigFile(configPath)
	unlock, err := cf.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := backupIfExists(configPath); err != nil {
		return err
	}
	if strings.TrimSpace(model) == "" {
		model = "code-switch-proxy"
	}
	root := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"model":   model,
		"provider": map[string]any{
			"code-switch-proxy": map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "code-switch proxy",
				"options": map[string]any{
					"baseURL": proxyBaseURL(port, true),
					"apiKey":  strings.TrimSpace(token),
				},
				"models": map[string]any{model: map[string]any{"name": model}},
			},
		},
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeTextAtomic(configPath, string(data)+"\n", 0o600)
}

func refreshProxyClientConfigs(state ProxyRuntimeState, cfg *AppConfig) error {
	if cfg == nil || cfg.Proxy == nil || len(cfg.Proxy.Routes) == 0 {
		return nil
	}
	for _, agent := range sortedProxyRouteAgents(cfg.Proxy.Routes) {
		persisted := cfg.Proxy.Routes[agent]
		token := strings.TrimSpace(persisted.Token)
		if token == "" {
			return fmt.Errorf("proxy route for agent %q has no token", agent)
		}
		route, err := buildProxyRouteFromConfig(agent, cfg, token)
		if err != nil {
			return err
		}
		switch AgentName(agent) {
		case agentClaude:
			if err := writeClaudeProxyConfigInDir("", state.Port, token); err != nil {
				return err
			}
		case agentCodex:
			if err := writeCodexProxyConfigInDir("", state.Port, token, route.UpstreamProtocol, route.Model); err != nil {
				return err
			}
		case agentOpencode:
			if err := writeOpencodeProxyConfigInDir("", state.Port, token, route.Model); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported agent %q", agent)
		}
	}
	return nil
}

func ensureProxyDaemon(cfg *AppConfig) error {
	running, routeChanged, err := proxyDaemonIsRunning(cfg)
	if err != nil {
		return err
	}
	if !running {
		return startProxyDaemon(cfg)
	}
	if routeChanged {
		if err := stopProxyDaemon(); err != nil {
			return err
		}
		return startProxyDaemon(cfg)
	}
	return nil
}

func defaultProxyDaemonIsRunning(cfg *AppConfig) (bool, bool, error) {
	state, err := readProxyRuntimeState()
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	report, healthErr := checkProxyHealth(ctx, state)
	cancel()
	if healthErr != nil || report.InstanceID == "" || report.InstanceID != state.InstanceID || report.PID == 0 || report.PID != state.PID {
		return false, false, nil
	}
	if cfg == nil || cfg.Proxy == nil {
		return true, false, nil
	}
	proxyCfg := normalizeProxyConfig(*cfg.Proxy)
	if proxyCfg.Port != 0 && proxyCfg.Port != state.Port {
		return true, true, nil
	}
	if strings.TrimSpace(proxyCfg.Host) != "" && strings.TrimSpace(proxyCfg.Host) != state.Host {
		return true, true, nil
	}
	// Existing daemon processes read the route table at startup. A switch that
	// rewrites routes must restart the daemon so the in-memory route registry is
	// refreshed; this may briefly interrupt other proxied agents.
	return true, true, nil
}

func defaultStartProxyDaemon(cfg *AppConfig) error {
	agent := "codex"
	if cfg != nil && cfg.Proxy != nil && len(cfg.Proxy.Routes) == 1 {
		for key := range cfg.Proxy.Routes {
			agent = key
		}
	}
	return cmdProxyStart([]string{"--agent", agent}, io.Discard)
}

func defaultStopProxyDaemon() error {
	return cmdProxyStop(nil, io.Discard)
}

func writeProxyRouteConfig(cfg *AppConfig, plan ConnectionPlan, model, token string) error {
	if cfg.Proxy == nil {
		cfg.Proxy = &ProxyConfig{Host: "127.0.0.1"}
	}
	if cfg.Proxy.Routes == nil {
		cfg.Proxy.Routes = map[string]ProxyRouteConfig{}
	}
	if strings.TrimSpace(cfg.Proxy.Host) == "" {
		cfg.Proxy.Host = "127.0.0.1"
	}
	existing := cfg.Proxy.Routes[string(plan.Agent)]
	if strings.TrimSpace(token) == "" {
		token = strings.TrimSpace(existing.Token)
	}
	if strings.TrimSpace(token) == "" {
		generated, err := randomProxyRouteToken()
		if err != nil {
			return err
		}
		token = generated
	}
	cfg.Proxy.Routes[string(plan.Agent)] = ProxyRouteConfig{
		Agent:            string(plan.Agent),
		Provider:         plan.Provider,
		Model:            strings.TrimSpace(model),
		UpstreamProtocol: string(plan.UpstreamProtocol),
		Token:            token,
	}
	return nil
}

func switchProxyProvider(pa *providerArgs, cfg *AppConfig, persistAppConfig func() error, plan ConnectionPlan, claudeDir, codexDir, opencodeDir string, out io.Writer, dryRun bool) error {
	provider := canonicalProviderName(pa.Provider)
	preset, err := resolveAgentSwitchPreset(pa.Agent, provider, cfg, pa.Model)
	if err != nil {
		return err
	}
	model := strings.TrimSpace(preset.Model)
	existingToken := ""
	if cfg.Proxy != nil && cfg.Proxy.Routes != nil {
		existingToken = cfg.Proxy.Routes[string(pa.Agent)].Token
	}
	if err := writeProxyRouteConfig(cfg, plan, model, existingToken); err != nil {
		return err
	}
	route := cfg.Proxy.Routes[string(pa.Agent)]
	stored := cfg.Providers[provider]
	if strings.TrimSpace(pa.APIKey) != "" {
		stored.APIKey = pa.APIKey
	}
	stored.Model = model
	cfg.Providers[provider] = stored

	if dryRun {
		fmt.Fprintf(out, "[dry-run] would switch %s to %s via proxy\n", pa.Agent, preset.Name)
		fmt.Fprintf(out, "[dry-run] mode: proxy\n")
		fmt.Fprintf(out, "[dry-run] upstream_protocol: %s\n", plan.UpstreamProtocol)
		fmt.Fprintf(out, "[dry-run] local_proxy: %s\n", proxyBaseURL(normalizeProxyConfig(*cfg.Proxy).Port, pa.Agent != agentClaude))
		return nil
	}

	if err := persistAppConfig(); err != nil {
		return err
	}
	fmt.Fprintln(out, "proxy route changed; restarting the proxy daemon may briefly interrupt other proxied agents")
	if err := ensureProxyDaemon(cfg); err != nil {
		return err
	}
	state, err := readProxyRuntimeState()
	if err != nil {
		return err
	}
	switch pa.Agent {
	case agentClaude:
		if err := writeClaudeProxyConfigInDir(claudeDir, state.Port, route.Token); err != nil {
			return err
		}
	case agentCodex:
		if err := writeCodexProxyConfigInDir(codexDir, state.Port, route.Token, plan.UpstreamProtocol, model); err != nil {
			return err
		}
	case agentOpencode:
		if err := writeOpencodeProxyConfigInDir(opencodeDir, state.Port, route.Token, model); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported agent %q", pa.Agent)
	}
	fmt.Fprintf(out, "%s\n", successPrefix(fmt.Sprintf("switched %s to %s via proxy", pa.Agent, preset.Name)))
	fmt.Fprintf(out, "%s\n", formatLabel("mode", string(connectionModeProxy)))
	fmt.Fprintf(out, "%s\n", formatLabel("client_protocol", string(plan.ClientProtocol)))
	fmt.Fprintf(out, "%s\n", formatLabel("upstream_protocol", string(plan.UpstreamProtocol)))
	fmt.Fprintf(out, "%s\n", formatLabel("local_proxy", proxyBaseURL(state.Port, pa.Agent != agentClaude)))
	fmt.Fprintf(out, "%s\n", formatLabel("provider", provider))
	fmt.Fprintf(out, "%s\n", formatLabel("model", model))
	return nil
}

func proxyBaseURL(port int, v1 bool) string {
	base := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if v1 {
		return base + "/v1"
	}
	return base
}

func proxyDaemonStatusText() string {
	state, err := readProxyRuntimeState()
	if os.IsNotExist(err) {
		return "not running"
	}
	if err != nil {
		return "unknown: " + err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	report, healthErr := checkProxyHealth(ctx, state)
	cancel()
	if healthErr != nil || report.InstanceID != state.InstanceID || report.PID != state.PID {
		return "stale"
	}
	return fmt.Sprintf("running (%s)", state.BaseURL)
}

func proxyRouteForLocalConfig(agent AgentName, baseURL, token string) (ProxyRouteConfig, bool) {
	cfg, _, err := loadAppConfig()
	if err != nil || cfg.Proxy == nil || cfg.Proxy.Routes == nil {
		return ProxyRouteConfig{}, false
	}
	statePort := 0
	if state, err := readProxyRuntimeState(); err == nil {
		statePort = state.Port
	}
	for key, route := range cfg.Proxy.Routes {
		if key != string(agent) {
			continue
		}
		if strings.TrimSpace(route.Token) != "" && strings.TrimSpace(token) != "" && strings.TrimSpace(route.Token) != strings.TrimSpace(token) {
			continue
		}
		proxyCfg := normalizeProxyConfig(*cfg.Proxy)
		if statePort != 0 {
			proxyCfg.Port = statePort
		}
		if baseURL == proxyBaseURL(proxyCfg.Port, false) || baseURL == proxyBaseURL(proxyCfg.Port, true) {
			return route, true
		}
	}
	return ProxyRouteConfig{}, false
}
