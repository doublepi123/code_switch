package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type launchInvocation struct {
	Agent AgentName
	Path  string
	Args  []string
	Env   []string
}

var (
	lookPath      = exec.LookPath
	launchCommand = defaultLaunchCommand
)

func defaultLaunchCommand(inv launchInvocation) error {
	cmd := exec.Command(inv.Path, inv.Args...)
	cmd.Env = inv.Env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func launchAgent(agent AgentName, provider, modelOverride, apiKeyFlag string, out io.Writer) error {
	pa, cfg, configPath, err := resolveProviderAndKeyForAgent(agent, provider, apiKeyFlag, modelOverride)
	if err != nil {
		return err
	}
	return launchAgentWithConfig(agent, pa.Provider, modelOverride, pa.APIKey, cfg, configPath, out)
}

func launchAgentWithConfig(agent AgentName, provider, modelOverride, apiKey string, cfg *AppConfig, configPath string, out io.Writer) error {
	provider = canonicalProviderName(provider)
	preset, err := resolveAgentSwitchPreset(agent, provider, cfg, modelOverride)
	if err != nil {
		return err
	}
	plan, err := resolveConnection(agent, provider, preset, "auto")
	if err != nil {
		return err
	}
	plan = adjustLaunchConnectionPlan(agent, plan, preset)
	pairs, state, cleanup, err := launchEnvPairsForPlan(agent, provider, preset, plan, apiKey, cfg, configPath)
	if err != nil {
		return err
	}
	var cleanups []func() error
	runCleanups := func(runErr error) error {
		for i := len(cleanups) - 1; i >= 0; i-- {
			if err := cleanups[i](); err != nil && runErr == nil {
				runErr = err
			}
		}
		return runErr
	}
	if cleanup != nil {
		cleanups = append(cleanups, cleanup)
	}
	if agent == agentCodex {
		codexHome, codexCleanup, err := prepareTemporaryCodexHome(provider, preset, plan, state, cfg)
		if err != nil {
			return runCleanups(err)
		}
		cleanups = append(cleanups, func() error { codexCleanup(); return nil })
		pairs = append(pairs, envPair{Key: "CODEX_HOME", Value: codexHome})
	}
	bin, err := lookPath(string(agent))
	if err != nil {
		return runCleanups(fmt.Errorf("find %s in PATH: %w", agent, err))
	}
	inv := launchInvocation{Agent: agent, Path: bin, Env: mergeEnv(os.Environ(), pairs)}
	if out != nil {
		fmt.Fprintf(out, "launching %s with %s (temporary)\n", agent, provider)
	}
	return runCleanups(launchCommand(inv))
}

func adjustLaunchConnectionPlan(agent AgentName, plan ConnectionPlan, preset ProviderPreset) ConnectionPlan {
	if agent == agentOpencode && plan.Mode == connectionModeDirect {
		if endpoint, ok := preset.presetEndpoint(protocolOpenAIChat); ok {
			plan.UpstreamProtocol = protocolOpenAIChat
			plan.Endpoint = endpoint
		}
	}
	return plan
}

func launchEnvPairs(agent AgentName, preset ProviderPreset, plan ConnectionPlan, apiKey string) ([]envPair, error) {
	switch agent {
	case agentClaude:
		baseURL := strings.TrimSpace(plan.Endpoint.BaseURL)
		if baseURL == "" {
			baseURL = preset.BaseURL
		}
		authEnv := strings.TrimSpace(plan.Endpoint.AuthEnv)
		if authEnv == "" {
			authEnv = strings.TrimSpace(preset.AuthEnv)
		}
		if authEnv == "" {
			authEnv = "ANTHROPIC_API_KEY"
		}
		pairs := []envPair{{Key: "ANTHROPIC_BASE_URL", Value: baseURL}, {Key: authEnv, Value: apiKey}}
		if !preset.NoModel {
			pairs = append(pairs, envPair{Key: "ANTHROPIC_MODEL", Value: preset.Model})
			if preset.Haiku != "" {
				pairs = append(pairs, envPair{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: preset.Haiku})
			}
			if preset.Sonnet != "" {
				pairs = append(pairs, envPair{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: preset.Sonnet})
			}
			if preset.Opus != "" {
				pairs = append(pairs, envPair{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: preset.Opus})
			}
			if preset.Subagent != "" {
				pairs = append(pairs, envPair{Key: "CLAUDE_CODE_SUBAGENT_MODEL", Value: preset.Subagent})
			}
		}
		if preset.ReasoningEffort != "" {
			pairs = append(pairs, envPair{Key: "CLAUDE_CODE_EFFORT_LEVEL", Value: preset.ReasoningEffort})
		}
		for _, key := range sortedExtraEnv(preset.ExtraEnv) {
			pairs = append(pairs, envPair{Key: key, Value: fmt.Sprint(preset.ExtraEnv[key])})
		}
		return pairs, nil
	case agentCodex, agentOpencode:
		baseURL := strings.TrimSpace(plan.Endpoint.BaseURL)
		if baseURL == "" {
			baseURL = preset.BaseURL
		}
		pairs := []envPair{{Key: "OPENAI_BASE_URL", Value: baseURL}, {Key: "OPENAI_API_KEY", Value: apiKey}}
		if !preset.NoModel {
			pairs = append(pairs, envPair{Key: "OPENAI_MODEL", Value: preset.Model})
		}
		return pairs, nil
	default:
		return nil, fmt.Errorf("unsupported agent %q", agent)
	}
}

func launchEnvPairsForPlan(agent AgentName, provider string, preset ProviderPreset, plan ConnectionPlan, apiKey string, cfg *AppConfig, configPath string) ([]envPair, ProxyRuntimeState, func() error, error) {
	if plan.Mode == connectionModeDirect {
		pairs, err := launchEnvPairs(agent, preset, plan, apiKey)
		return pairs, ProxyRuntimeState{}, nil, err
	}
	token, state, cleanup, err := configureTemporaryProxyRoute(agent, provider, preset.Model, plan, apiKey, cfg, configPath)
	if err != nil {
		return nil, ProxyRuntimeState{}, nil, err
	}
	proxyPreset := preset
	proxyPreset.BaseURL = proxyBaseURL(state.Port, agent != agentClaude)
	proxyPreset.AuthEnv = ""
	proxyPlan := plan
	proxyPlan.Endpoint = ProtocolEndpoint{BaseURL: proxyPreset.BaseURL}
	if agent == agentClaude {
		proxyPlan.Endpoint.AuthEnv = "ANTHROPIC_AUTH_TOKEN"
	}
	pairs, err := launchEnvPairs(agent, proxyPreset, proxyPlan, token)
	if err != nil {
		_ = cleanup()
		return nil, ProxyRuntimeState{}, nil, err
	}
	return pairs, state, cleanup, nil
}

func configureTemporaryProxyRoute(agent AgentName, provider, model string, plan ConnectionPlan, apiKey string, cfg *AppConfig, configPath string) (string, ProxyRuntimeState, func() error, error) {
	if cfg == nil {
		return "", ProxyRuntimeState{}, nil, fmt.Errorf("app config is nil")
	}
	ensureAppConfigMaps(cfg)
	if strings.TrimSpace(configPath) == "" {
		path, err := appConfigPath()
		if err != nil {
			return "", ProxyRuntimeState{}, nil, err
		}
		configPath = path
	}
	if cfg.Proxy == nil {
		cfg.Proxy = &ProxyConfig{Host: "127.0.0.1"}
	}
	wasRunning, _, err := proxyDaemonIsRunning(cfg)
	if err != nil {
		return "", ProxyRuntimeState{}, nil, err
	}
	originalBytes, readErr := os.ReadFile(configPath)
	originalExists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", ProxyRuntimeState{}, nil, readErr
	}
	if cfg.Proxy.Routes == nil {
		cfg.Proxy.Routes = map[string]ProxyRouteConfig{}
	}
	if strings.TrimSpace(apiKey) != "" {
		stored := cfg.Providers[provider]
		stored.APIKey = apiKey
		cfg.Providers[provider] = stored
	}
	if err := writeProxyRouteConfig(cfg, plan, strings.TrimSpace(model), ""); err != nil {
		return "", ProxyRuntimeState{}, nil, err
	}
	agentKey := string(agent)
	route := cfg.Proxy.Routes[agentKey]
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		return "", ProxyRuntimeState{}, nil, err
	}
	cleanup := func() error {
		var restored *AppConfig
		if originalExists {
			if err := writeTextAtomic(configPath, string(originalBytes), 0o600); err != nil {
				return err
			}
			loaded, err := loadAppConfigFrom(configPath)
			if err != nil {
				return err
			}
			restored = loaded
		} else {
			if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			restored = &AppConfig{Providers: map[string]StoredProvider{}}
		}
		if wasRunning {
			_, err := ensureProxyDaemon(restored)
			return err
		}
		return stopProxyDaemon()
	}
	if _, err := ensureProxyDaemon(cfg); err != nil {
		_ = cleanup()
		return "", ProxyRuntimeState{}, nil, err
	}
	state, err := readProxyRuntimeState()
	if err != nil {
		_ = cleanup()
		return "", ProxyRuntimeState{}, nil, err
	}
	return route.Token, state, cleanup, nil
}

func prepareTemporaryCodexHome(provider string, preset ProviderPreset, plan ConnectionPlan, state ProxyRuntimeState, cfg *AppConfig) (string, func(), error) {
	dir, err := os.MkdirTemp("", "code-switch-codex-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	configPath := codexConfigPath(dir)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		cleanup()
		return "", nil, err
	}
	var rendered string
	if plan.Mode == connectionModeProxy {
		baseURL := proxyBaseURL(state.Port, true)
		rendered = renderProxyCodexConfigForBaseURLWithCatalogProtocol(preset.Model, baseURL, "", plan.UpstreamProtocol)
	} else {
		rendered = applyCodexPresetTOMLWithProtocol("", preset, provider, plan.UpstreamProtocol)
	}
	if err := writeTextAtomic(configPath, rendered, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

func mergeEnv(base []string, pairs []envPair) []string {
	values := map[string]string{}
	order := make([]string, 0, len(base)+len(pairs))
	managed := map[string]struct{}{
		"OPENAI_API_KEY":  {},
		"OPENAI_BASE_URL": {},
		"OPENAI_MODEL":    {},
		"CODEX_HOME":      {},
	}
	for _, key := range managedEnvKeys {
		managed[key] = struct{}{}
	}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, skip := managed[key]; skip {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, p := range pairs {
		if _, exists := values[p.Key]; !exists {
			order = append(order, p.Key)
		}
		values[p.Key] = p.Value
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}
