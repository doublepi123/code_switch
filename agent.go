package main

import (
	"fmt"
	"sort"
	"strings"
)

func parseAgentName(value string) (AgentName, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return agentClaude, nil
	}
	switch AgentName(value) {
	case agentClaude, agentCodex:
		return AgentName(value), nil
	default:
		return "", fmt.Errorf("unsupported agent %q, use claude or codex", value)
	}
}

func agentDisplayName(agent AgentName) string {
	switch agent {
	case agentCodex:
		return "Codex"
	case agentClaude:
		return "Claude Code"
	default:
		return fmt.Sprintf("Unknown (%s)", string(agent))
	}
}

func ensureAppConfigMaps(cfg *AppConfig) {
	if cfg.Providers == nil {
		cfg.Providers = map[string]StoredProvider{}
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}
	for name, agentCfg := range cfg.Agents {
		if agentCfg.Providers == nil {
			agentCfg.Providers = map[string]StoredProvider{}
			cfg.Agents[name] = agentCfg
		}
	}
}

func agentConfig(cfg *AppConfig, agent AgentName) AgentConfig {
	ensureAppConfigMaps(cfg)
	agentCfg := cfg.Agents[string(agent)]
	if agentCfg.Providers == nil {
		agentCfg.Providers = map[string]StoredProvider{}
	}
	return agentCfg
}

func setAgentProviderConfig(cfg *AppConfig, agent AgentName, provider string, stored StoredProvider) {
	ensureAppConfigMaps(cfg)
	agentCfg := agentConfig(cfg, agent)
	agentCfg.Providers[provider] = stored
	cfg.Agents[string(agent)] = agentCfg
}

func codexProviderConfig(cfg *AppConfig, provider string) StoredProvider {
	agentCfg := agentConfig(cfg, agentCodex)
	return agentCfg.Providers[provider]
}

func storedAPIKeyForAgent(cfg *AppConfig, agent AgentName, provider string) string {
	if agent == agentCodex {
		key := strings.TrimSpace(codexProviderConfig(cfg, provider).APIKey)
		if key != "" {
			return key
		}
	}
	return strings.TrimSpace(cfg.Providers[provider].APIKey)
}

func codexOllamaCloudPreset() ProviderPreset {
	preset := providerPresets["ollama-cloud"]
	preset.BaseURL = "https://ollama.com/v1"
	preset.AuthEnv = "OLLAMA_API_KEY"
	return preset
}

func codexOpenRouterPreset() ProviderPreset {
	preset := providerPresets["openrouter"]
	preset.BaseURL = "https://openrouter.ai/api/v1"
	preset.AuthEnv = "OPENROUTER_API_KEY"
	preset.ForceModelTiers = true
	return preset
}

func codexDeepSeekPreset() ProviderPreset {
	preset := providerPresets["deepseek"]
	preset.BaseURL = "https://api.deepseek.com/v1"
	preset.AuthEnv = "DEEPSEEK_API_KEY"
	preset.ForceModelTiers = true
	preset.ReasoningEffort = "xhigh"
	return preset
}

func resolveAgentProviderPreset(agent AgentName, provider string, cfg *AppConfig) (ProviderPreset, error) {
	switch agent {
	case agentCodex:
		provider = canonicalProviderName(provider)
		var preset ProviderPreset
		switch provider {
		case "deepseek":
			preset = codexDeepSeekPreset()
		case "ollama-cloud":
			preset = codexOllamaCloudPreset()
		case "openrouter":
			preset = codexOpenRouterPreset()
		default:
			return ProviderPreset{}, fmt.Errorf("unsupported provider %q for agent codex", provider)
		}
		if stored := codexProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
			preset = withSelectedModel(preset, stored.Model)
		}
		return preset, nil
	default:
		return resolveProviderPreset(provider, cfg)
	}
}

func resolveAgentSwitchPreset(agent AgentName, provider string, cfg *AppConfig, modelOverride string) (ProviderPreset, error) {
	switch agent {
	case agentCodex:
		provider = canonicalProviderName(provider)
		var preset ProviderPreset
		switch provider {
		case "deepseek":
			preset = codexDeepSeekPreset()
		case "ollama-cloud":
			preset = codexOllamaCloudPreset()
		case "openrouter":
			preset = codexOpenRouterPreset()
		default:
			return ProviderPreset{}, fmt.Errorf("unsupported provider %q for agent codex", provider)
		}
		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(codexProviderConfig(cfg, provider).Model)
		}
		return withSelectedModel(preset, model), nil
	default:
		return resolveSwitchPreset(provider, cfg, modelOverride)
	}
}

func providerNamesForAgent(agent AgentName, cfg *AppConfig, includeCustomOption bool, includeRestoreOption bool) []string {
	var names []string
	switch agent {
	case agentCodex:
		names = []string{"deepseek", "ollama-cloud", "openrouter"}
	default:
		names = sortedProviderNames(cfg, includeCustomOption)
	}
	if includeRestoreOption {
		names = append(names, restoreProviderOption)
	}
	return names
}

func providerModelsForAgent(cfg *AppConfig, agent AgentName, provider string) []string {
	if agent == agentCodex {
		preset, err := resolveAgentProviderPreset(agent, provider, cfg)
		if err != nil {
			return nil
		}
		if provider == "openrouter" {
			if models := openRouterModels(cfg); len(models) > 0 {
				return models
			}
		}
		if len(preset.Models) == 0 {
			return []string{preset.Model}
		}
		return preset.Models
	}
	return providerModels(cfg, provider)
}

func defaultSelectionModelForAgent(cfg *AppConfig, agent AgentName, provider, currentProvider, currentModel string) string {
	if agent == agentClaude {
		return defaultSelectionModel(cfg, provider, currentProvider, currentModel)
	}
	if provider == currentProvider && currentModel != "" {
		for _, model := range providerModelsForAgent(cfg, agent, provider) {
			if model == currentModel {
				return currentModel
			}
		}
	}
	preset, err := resolveAgentProviderPreset(agent, provider, cfg)
	if err != nil {
		return ""
	}
	return preset.Model
}

func modelIndexForAgent(cfg *AppConfig, agent AgentName, provider, currentProvider, currentModel string) int {
	selected := defaultSelectionModelForAgent(cfg, agent, provider, currentProvider, currentModel)
	for i, model := range providerModelsForAgent(cfg, agent, provider) {
		if model == selected {
			return i
		}
	}
	return 0
}

func buildModelListForAgent(cfg *AppConfig, agent AgentName, provider string, customModels map[string]string) []string {
	models := providerModelsForAgent(cfg, agent, provider)
	customModel := strings.TrimSpace(customModels[provider])
	if customModel == "" {
		return models
	}
	filtered := []string{customModel}
	for _, model := range models {
		if model != customModel {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func sortedAgentNames() []AgentName {
	names := []AgentName{agentClaude, agentCodex}
	sort.Slice(names, func(i, j int) bool {
		return agentDisplayName(names[i]) < agentDisplayName(names[j])
	})
	return names
}
