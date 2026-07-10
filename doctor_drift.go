package main

import (
	"fmt"
	"strings"
)

type doctorDriftActual struct {
	Provider string
	Model    string
	BaseURL  string
}

type doctorDriftExpected struct {
	Provider string
	Model    string
	BaseURL  string
	Source   string
}

func checkClaudeDrift(claudeDir string, cfg *AppConfig) checkResult {
	root, err := readJSONMap(claudeSettingsPath(claudeDir))
	if err != nil {
		return warnResult("claude config drift", "read settings: "+err.Error())
	}
	env := nestedMap(root, "env")
	actual := doctorDriftActual{}
	if env != nil {
		actual.BaseURL, _ = env["ANTHROPIC_BASE_URL"].(string)
		actual.Model, _ = env["ANTHROPIC_MODEL"].(string)
	}
	actual.Provider = detectProvider(actual.BaseURL, actual.Model)
	if actual.Provider == customDetectedProvider {
		actual.Provider = ""
	}
	return checkAgentDrift(agentClaude, cfg, actual)
}

func checkCodexDrift(codexDir string, cfg *AppConfig) checkResult {
	_, provider, model, baseURL, err := currentCodexProvider(codexDir)
	if err != nil {
		return warnResult("codex config drift", "read config: "+err.Error())
	}
	actual := doctorDriftActual{Provider: provider, Model: model, BaseURL: baseURL}
	return checkAgentDrift(agentCodex, cfg, actual)
}

func checkOpencodeDrift(opencodeDir string, cfg *AppConfig) checkResult {
	_, model, baseURL, _, provider, err := currentOpencodeProvider(opencodeDir)
	if err != nil {
		return warnResult("opencode config drift", "read config: "+err.Error())
	}
	actual := doctorDriftActual{Provider: provider, Model: model, BaseURL: baseURL}
	return checkAgentDrift(agentOpencode, cfg, actual)
}

func checkAgentDrift(agent AgentName, cfg *AppConfig, actual doctorDriftActual) checkResult {
	name := doctorDriftCheckName(agent)
	expected, ok := expectedDoctorDrift(agent, cfg, actual)
	if !ok {
		return okResult(name, "no tracked provider active")
	}
	if doctorDriftMatches(expected, actual) {
		return okResult(name, fmt.Sprintf("%s matches %s", formatDoctorDriftActual(actual), expected.Source))
	}
	return warnResult(name, fmt.Sprintf("%s differs from %s %s (run `cs switch %s --agent %s` to resync)", formatDoctorDriftActual(actual), expected.Source, formatDoctorDriftExpected(expected), expected.Provider, agent))
}

func doctorDriftCheckName(agent AgentName) string {
	return string(agent) + " config drift"
}

func expectedDoctorDrift(agent AgentName, cfg *AppConfig, actual doctorDriftActual) (doctorDriftExpected, bool) {
	if cfg == nil {
		return doctorDriftExpected{}, false
	}
	if cfg.Proxy != nil {
		if route, ok := cfg.Proxy.Routes[string(agent)]; ok {
			return doctorDriftExpected{Provider: route.Provider, Model: route.Model, BaseURL: route.BaseURL, Source: "proxy route for " + string(agent)}, true
		}
	}
	provider, stored, ok := expectedStoredProviderForDrift(agent, cfg, actual)
	if !ok {
		return doctorDriftExpected{}, false
	}
	expected := expectedFromStoredProvider(provider, stored)
	if strings.TrimSpace(expected.BaseURL) == "" {
		return doctorDriftExpected{}, false
	}
	expected.Source = "stored provider " + provider
	return expected, true
}

func expectedFromStoredProvider(provider string, stored StoredProvider) doctorDriftExpected {
	expected := doctorDriftExpected{Provider: provider, Model: stored.Model, BaseURL: stored.BaseURL}
	if preset, ok := providerPresets[canonicalProviderName(provider)]; ok {
		if strings.TrimSpace(expected.Model) == "" {
			expected.Model = preset.Model
		}
		if strings.TrimSpace(expected.BaseURL) == "" {
			expected.BaseURL = preset.BaseURL
		}
	}
	return expected
}

func expectedStoredProviderForDrift(agent AgentName, cfg *AppConfig, actual doctorDriftActual) (string, StoredProvider, bool) {
	provider := canonicalProviderName(actual.Provider)
	if provider != "" {
		if stored, ok := storedProviderForAgent(cfg, agent, provider); ok {
			return provider, stored, true
		}
	}
	if provider, stored, ok := storedProviderMatchingBaseURL(agent, cfg, actual.BaseURL); ok {
		return provider, stored, true
	}
	return singleStoredProviderForAgent(agent, cfg)
}

func storedProviderMatchingBaseURL(agent AgentName, cfg *AppConfig, baseURL string) (string, StoredProvider, bool) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", StoredProvider{}, false
	}
	for provider, stored := range storedProvidersForAgent(agent, cfg) {
		if strings.TrimSpace(stored.BaseURL) == baseURL {
			return provider, stored, true
		}
	}
	return "", StoredProvider{}, false
}

func singleStoredProviderForAgent(agent AgentName, cfg *AppConfig) (string, StoredProvider, bool) {
	providers := storedProvidersForAgent(agent, cfg)
	if len(providers) != 1 {
		return "", StoredProvider{}, false
	}
	for provider, stored := range providers {
		return provider, stored, true
	}
	return "", StoredProvider{}, false
}

func storedProvidersForAgent(agent AgentName, cfg *AppConfig) map[string]StoredProvider {
	if cfg == nil {
		return nil
	}
	if agent == agentClaude {
		return cfg.Providers
	}
	agentCfg := agentConfig(cfg, agent)
	return agentCfg.Providers
}

func doctorDriftMatches(expected doctorDriftExpected, actual doctorDriftActual) bool {
	return doctorDriftProviderMatches(expected.Provider, actual.Provider) &&
		doctorDriftFieldMatches(expected.Model, actual.Model) &&
		doctorDriftFieldMatches(expected.BaseURL, actual.BaseURL)
}

func doctorDriftProviderMatches(expected, actual string) bool {
	if strings.TrimSpace(actual) == "" {
		return true
	}
	return doctorDriftFieldMatches(expected, actual)
}

func doctorDriftFieldMatches(expected, actual string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return true
	}
	return expected == strings.TrimSpace(actual)
}

func formatDoctorDriftActual(actual doctorDriftActual) string {
	return fmt.Sprintf("config provider=%q model=%q base_url=%q", actual.Provider, actual.Model, actual.BaseURL)
}

func formatDoctorDriftExpected(expected doctorDriftExpected) string {
	return fmt.Sprintf("provider=%q model=%q base_url=%q", expected.Provider, expected.Model, expected.BaseURL)
}
