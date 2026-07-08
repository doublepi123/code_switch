package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

func parseAgentName(value string) (AgentName, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return agentClaude, nil
	}
	switch AgentName(value) {
	case agentClaude, agentCodex, agentOpencode:
		return AgentName(value), nil
	default:
		return "", fmt.Errorf("unsupported agent %q, use claude, codex, or opencode", value)
	}
}

func agentDisplayName(agent AgentName) string {
	switch agent {
	case agentCodex:
		return "Codex"
	case agentClaude:
		return "Claude Code"
	case agentOpencode:
		return "OpenCode"
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

func opencodeProviderConfig(cfg *AppConfig, provider string) StoredProvider {
	agentCfg := agentConfig(cfg, agentOpencode)
	return agentCfg.Providers[provider]
}

func storedAPIKeyForAgent(cfg *AppConfig, agent AgentName, provider string) string {
	if agent == agentCodex {
		key := strings.TrimSpace(codexProviderConfig(cfg, provider).APIKey)
		if key != "" {
			return key
		}
	}
	if agent == agentOpencode {
		key := strings.TrimSpace(opencodeProviderConfig(cfg, provider).APIKey)
		if key != "" {
			return key
		}
	}
	return strings.TrimSpace(cfg.Providers[provider].APIKey)
}

func resolveAgentProviderPreset(agent AgentName, provider string, cfg *AppConfig) (ProviderPreset, error) {
	switch agent {
	case agentCodex:
		provider = canonicalProviderName(provider)
		preset, err := presetForAgentDirectProtocol(agent, provider)
		if err != nil {
			preset, err = resolveProviderPreset(provider, cfg)
			if err != nil {
				return ProviderPreset{}, err
			}
		}
		if stored := codexProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
			preset = withSelectedModel(preset, stored.Model)
			applyStoredEndpointOverride(&preset, stored)
		} else {
			applyStoredEndpointOverride(&preset, codexProviderConfig(cfg, provider))
		}
		preset.ForceModelTiers = true
		return preset, nil
	case agentOpencode:
		preset, err := resolveProviderPreset(provider, cfg)
		if err != nil {
			return ProviderPreset{}, err
		}
		applyStoredEndpointOverride(&preset, opencodeProviderConfig(cfg, provider))
		return preset, nil
	default:
		return resolveProviderPreset(provider, cfg)
	}
}

func resolveAgentSwitchPreset(agent AgentName, provider string, cfg *AppConfig, modelOverride string) (ProviderPreset, error) {
	switch agent {
	case agentCodex:
		provider = canonicalProviderName(provider)
		preset, err := presetForAgentDirectProtocol(agent, provider)
		if err != nil {
			preset, err = resolveSwitchPreset(provider, cfg, modelOverride)
			if err != nil {
				return ProviderPreset{}, err
			}
		} else if strings.TrimSpace(modelOverride) != "" {
			preset = withSelectedModel(preset, modelOverride)
		}
		if strings.TrimSpace(modelOverride) == "" {
			if model := strings.TrimSpace(codexProviderConfig(cfg, provider).Model); model != "" {
				preset = withSelectedModel(preset, model)
			}
		}
		applyStoredEndpointOverride(&preset, codexProviderConfig(cfg, provider))
		preset.ForceModelTiers = true
		return preset, nil
	case agentOpencode:
		preset, err := resolveSwitchPreset(provider, cfg, modelOverride)
		if err != nil {
			return ProviderPreset{}, err
		}
		applyStoredEndpointOverride(&preset, opencodeProviderConfig(cfg, provider))
		return preset, nil
	default:
		return resolveSwitchPreset(provider, cfg, modelOverride)
	}
}

func providerNamesForAgent(agent AgentName, cfg *AppConfig, includeCustomOption bool, includeRestoreOption bool) []string {
	if cfg == nil {
		cfg = &AppConfig{Providers: map[string]StoredProvider{}}
	}
	names := sortedProviderNames(cfg, includeCustomOption)
	if includeRestoreOption {
		names = append(names, restoreProviderOption)
	}
	return names
}

func presetForAgentDirectProtocol(agent AgentName, provider string) (ProviderPreset, error) {
	provider = canonicalProviderName(provider)
	preset, ok := providerPresets[provider]
	if !ok {
		return ProviderPreset{}, fmt.Errorf("unsupported provider %q for agent %s", provider, agent)
	}
	profile, ok := agentProfiles[agent]
	if !ok {
		return ProviderPreset{}, fmt.Errorf("unsupported agent %q", agent)
	}
	for _, protocol := range profile.DirectProtocols {
		endpoint, ok := preset.presetEndpoint(protocol)
		if !ok {
			continue
		}
		preset.BaseURL = endpoint.BaseURL
		preset.AuthEnv = endpoint.AuthEnv
		if preset.ReasoningEffort == "" {
			if effort, ok := preset.ExtraEnv["CLAUDE_CODE_EFFORT_LEVEL"].(string); ok {
				preset.ReasoningEffort = effort
			}
		}
		if agent == agentCodex {
			preset.ForceModelTiers = true
		}
		return preset, nil
	}
	return ProviderPreset{}, fmt.Errorf("unsupported provider %q for agent %s", provider, agent)
}

func presetNamesForAgentDirectProtocols(agent AgentName) []string {
	profile, ok := agentProfiles[agent]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(providerPresets))
	for name, preset := range providerPresets {
		for _, protocol := range profile.DirectProtocols {
			if _, ok := preset.presetEndpoint(protocol); ok {
				names = append(names, name)
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

func providerModelsForAgent(cfg *AppConfig, agent AgentName, provider string) []string {
	return providerModelsForAgentWithAPIKey(cfg, agent, provider, "")
}

func providerModelsForAgentWithAPIKey(cfg *AppConfig, agent AgentName, provider, apiKey string) []string {
	provider = canonicalProviderName(provider)
	catalog := providerModelCatalog(cfg, agent, provider, apiKey)
	if catalog.Err != "" && len(catalog.Models) == 0 {
		fmt.Fprintf(os.Stderr, "warning: failed to resolve models for provider %q: %s\n", provider, catalog.Err)
		return nil
	}
	return modelIDs(catalog)
}

func defaultSelectionModelForAgent(cfg *AppConfig, agent AgentName, provider, currentProvider, currentModel string) string {
	if agent == agentClaude || agent == agentOpencode {
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
	return buildModelListForAgentWithAPIKey(cfg, agent, provider, customModels, "")
}

func buildModelListForAgentWithAPIKey(cfg *AppConfig, agent AgentName, provider string, customModels map[string]string, apiKey string) []string {
	models := providerModelsForAgentWithAPIKey(cfg, agent, provider, apiKey)
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
	names := []AgentName{agentClaude, agentCodex, agentOpencode}
	sort.Slice(names, func(i, j int) bool {
		return agentDisplayName(names[i]) < agentDisplayName(names[j])
	})
	return names
}
