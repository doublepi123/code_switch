package main

import (
	"encoding/json"
	"io"
)

// agentSnapshot captures the resolved current configuration for a single agent
// target in a machine-readable form. It is the JSON analogue of the human
// output produced by cmdCurrent.
type agentSnapshot struct {
	ConfigFile string `json:"config_file"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url,omitempty"`
	Model      string `json:"model,omitempty"`
	Haiku      string `json:"haiku,omitempty"`
	Sonnet     string `json:"sonnet,omitempty"`
	Opus       string `json:"opus,omitempty"`
	Subagent   string `json:"subagent,omitempty"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
}

// currentSnapshot aggregates the per-agent snapshots emitted by `current --json`.
type currentSnapshot struct {
	Claude   *agentSnapshot `json:"claude,omitempty"`
	Codex    *agentSnapshot `json:"codex,omitempty"`
	Opencode *agentSnapshot `json:"opencode,omitempty"`
}

func renderCurrentJSON(out io.Writer, claudeDir, codexDir, opencodeDir string, showBoth bool, agent AgentName) error {
	snap := currentSnapshot{}
	if showBoth || agent == agentClaude {
		s, err := buildClaudeSnapshot(claudeDir)
		if err != nil {
			return err
		}
		snap.Claude = &s
	}
	if showBoth || agent == agentCodex {
		s, err := buildCodexSnapshot(codexDir)
		if err != nil {
			return err
		}
		snap.Codex = &s
	}
	if showBoth || agent == agentOpencode {
		s, err := buildOpencodeSnapshot(opencodeDir)
		if err != nil {
			return err
		}
		snap.Opencode = &s
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = out.Write(data)
	return err
}

func buildClaudeSnapshot(claudeDir string) (agentSnapshot, error) {
	settingsPath := claudeSettingsPath(claudeDir)
	root, err := readJSONMap(settingsPath)
	if err != nil {
		return agentSnapshot{}, err
	}
	snap := agentSnapshot{ConfigFile: settingsPath}
	env := nestedMap(root, "env")
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)
	if baseURL == "" {
		snap.Provider = "unknown"
		return snap, nil
	}
	snap.Provider = detectProvider(baseURL, model)
	snap.BaseURL = baseURL
	snap.Model = model
	snap.Haiku, _ = env["ANTHROPIC_DEFAULT_HAIKU_MODEL"].(string)
	snap.Sonnet, _ = env["ANTHROPIC_DEFAULT_SONNET_MODEL"].(string)
	snap.Opus, _ = env["ANTHROPIC_DEFAULT_OPUS_MODEL"].(string)
	snap.Subagent, _ = env["CLAUDE_CODE_SUBAGENT_MODEL"].(string)
	return snap, nil
}

func buildCodexSnapshot(codexDir string) (agentSnapshot, error) {
	configPath, provider, model, baseURL, err := currentCodexProvider(codexDir)
	if err != nil {
		return agentSnapshot{}, err
	}
	snap := agentSnapshot{ConfigFile: configPath}
	if provider == "" {
		snap.Provider = "unknown"
		return snap, nil
	}
	snap.Provider = codexTOMLProviderKey(provider)
	snap.BaseURL = baseURL
	snap.Model = model
	return snap, nil
}

func buildOpencodeSnapshot(opencodeDir string) (agentSnapshot, error) {
	configPath, model, baseURL, authEnv, providerName, err := currentOpencodeProvider(opencodeDir)
	if err != nil {
		return agentSnapshot{}, err
	}
	snap := agentSnapshot{ConfigFile: configPath}
	if baseURL == "" {
		snap.Provider = "unknown"
		return snap, nil
	}
	displayProvider := providerName
	if displayProvider == "" {
		displayProvider = detectProvider(baseURL, model)
	}
	snap.Provider = displayProvider
	snap.BaseURL = baseURL
	snap.Model = model
	snap.APIKeyEnv = authEnv
	return snap, nil
}

// providerListItem is the JSON analogue of one row emitted by `list`.
type providerListItem struct {
	Name      string   `json:"name"`
	BaseURL   string   `json:"base_url"`
	Model     string   `json:"model"`
	Models    []string `json:"models,omitempty"`
	KeyStatus string   `json:"key_status"`
}

// renderListJSON emits the provider list for an agent as a JSON array.
func renderListJSON(out io.Writer, agent AgentName, cfg *AppConfig) error {
	names := providerNamesForAgent(agent, cfg, false, false)
	items := make([]providerListItem, 0, len(names))
	for _, name := range names {
		preset, err := resolveAgentProviderPreset(agent, name, cfg)
		if err != nil {
			return err
		}
		status := "missing"
		if preset.NoAPIKey {
			status = "none"
		} else if storedAPIKeyForAgent(cfg, agent, name) != "" {
			status = "set"
		}
		modelLabel := preset.Model
		if preset.NoModel {
			modelLabel = "auto"
		}
		models := providerModelsForAgent(cfg, agent, name)
		if len(models) == 0 {
			models = preset.Models
		}
		items = append(items, providerListItem{
			Name:      name,
			BaseURL:   preset.BaseURL,
			Model:     modelLabel,
			Models:    models,
			KeyStatus: status,
		})
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = out.Write(data)
	return err
}
