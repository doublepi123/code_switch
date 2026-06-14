package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func opencodeConfigPath(overrideDir string) string {
	dir := strings.TrimSpace(overrideDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), ".config", "opencode", "opencode.json")
		}
		dir = filepath.Join(home, ".config", "opencode")
	}
	return filepath.Join(dir, "opencode.json")
}

func switchOpencodeProvider(provider string, cfg *AppConfig, apiKey, modelOverride, opencodeDir string, out io.Writer, dryRun bool) error {
	provider = canonicalProviderName(provider)
	preset, err := resolveAgentSwitchPreset(agentOpencode, provider, cfg, modelOverride)
	if err != nil {
		return err
	}
	configPath := opencodeConfigPath(opencodeDir)

	if dryRun {
		authEnv := deriveAuthEnvForProvider(provider)
		fmt.Fprintf(out, "[dry-run] would switch OpenCode to %s\n", preset.Name)
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		fmt.Fprintf(out, "[dry-run] base_url: %s\n", preset.BaseURL)
		fmt.Fprintf(out, "[dry-run] model: %s\n", preset.Model)
		fmt.Fprintf(out, "[dry-run] api_key_env: %s\n", authEnv)
		return nil
	}

	cf := newConfigFile(configPath)
	unlock, err := cf.lock()
	if err != nil {
		return err
	}
	defer unlock()

	existingBytes, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing := string(existingBytes)

	if err := backupIfExists(configPath); err != nil {
		return err
	}

	updated, err := applyOpencodePresetJSON(existing, preset, provider, apiKey)
	if err != nil {
		return err
	}
	if err := writeTextAtomic(configPath, updated, 0o600); err != nil {
		return err
	}

	stored := opencodeProviderConfig(cfg, provider)
	if apiKey != "" {
		stored.APIKey = apiKey
	}
	stored.Model = preset.Model
	setAgentProviderConfig(cfg, agentOpencode, provider, stored)

	fmt.Fprintf(out, "%s\n", successPrefix(fmt.Sprintf("switched OpenCode to %s", preset.Name)))
	fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
	fmt.Fprintf(out, "%s\n", formatLabel("base_url", preset.BaseURL))
	fmt.Fprintf(out, "%s\n", formatLabel("model", preset.Model))
	return nil
}

func opencodeNPMForProvider(providerKey string) string {
	if providerKey == "ollama" || providerKey == "ollama-cloud" {
		return "@ai-sdk/openai-compatible"
	}
	return "@ai-sdk/anthropic"
}

func applyOpencodePresetJSON(existing string, preset ProviderPreset, providerKey string, apiKey string) (string, error) {
	root := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &root); err != nil {
			return "", fmt.Errorf("parse existing OpenCode config: %w", err)
		}
	}

	root["$schema"] = "https://opencode.ai/config.json"
	root["model"] = preset.Model

	npmPkg := opencodeNPMForProvider(providerKey)

	models := map[string]any{}
	models[preset.Model] = map[string]any{"name": preset.Model}

	providerEntry := map[string]any{
		"npm":  npmPkg,
		"name": preset.Name,
		"options": map[string]any{
			"baseURL": preset.BaseURL,
			"apiKey":  apiKey,
		},
		"models": models,
	}

	providers := map[string]any{}
	if raw, ok := root["provider"]; ok {
		existingProviders, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("parse existing OpenCode config: provider must be an object")
		}
		providers = existingProviders
	}
	providers[providerKey] = providerEntry
	root["provider"] = providers

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal OpenCode config: %w", err)
	}
	return string(data) + "\n", nil
}

func restoreOpencodeConfig(opencodeDir string, cfg *AppConfig, out io.Writer, dryRun bool) error {
	configPath := opencodeConfigPath(opencodeDir)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Fprintf(out, "%s\n", successPrefix("restored OpenCode official config"))
		fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
		return nil
	}

	if dryRun {
		fmt.Fprintf(out, "[dry-run] would restore OpenCode official config\n")
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		return nil
	}

	cf := newConfigFile(configPath)
	unlock, err := cf.lock()
	if err != nil {
		return err
	}
	defer unlock()

	existingBytes, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	existing := string(existingBytes)
	updated := removeOpencodeManagedJSON(existing, cfg)

	if err := backupIfExists(configPath); err != nil {
		return err
	}

	if strings.TrimSpace(updated) == "" {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := writeTextAtomic(configPath, updated, 0o600); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "%s\n", successPrefix("restored OpenCode official config"))
	fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
	return nil
}

func removeOpencodeManagedJSON(existing string, cfg *AppConfig) string {
	root := map[string]any{}
	if strings.TrimSpace(existing) == "" {
		return ""
	}
	if err := json.Unmarshal([]byte(existing), &root); err != nil {
		return existing
	}

	delete(root, "model")
	if raw, ok := root["provider"]; ok {
		providers, ok := raw.(map[string]any)
		if !ok {
			return existing
		}
		for key := range providers {
			if isOpencodeManagedProviderKey(key, cfg) {
				delete(providers, key)
			}
		}
		if len(providers) == 0 {
			delete(root, "provider")
		} else {
			root["provider"] = providers
		}
	}

	// If only $schema remains, return empty to trigger file deletion
	if len(root) == 1 {
		if _, hasSchema := root["$schema"]; hasSchema {
			return ""
		}
	}

	if len(root) == 0 {
		return ""
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return ""
	}
	return string(data) + "\n"
}

func isOpencodeManagedProviderKey(provider string, cfg *AppConfig) bool {
	provider = canonicalProviderName(provider)
	if _, ok := providerPresets[provider]; ok {
		return true
	}
	if cfg != nil {
		agentCfg := agentConfig(cfg, agentOpencode)
		_, exists := agentCfg.Providers[provider]
		return exists
	}
	return false
}

func currentOpencodeProvider(opencodeDir string) (string, string, string, string, string, error) {
	configPath := opencodeConfigPath(opencodeDir)
	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return configPath, "", "", "", "", nil
		}
		return configPath, "", "", "", "", err
	}
	root := map[string]any{}
	if err := json.Unmarshal(content, &root); err != nil {
		return configPath, "", "", "", "", err
	}

	model, _ := root["model"].(string)
	baseURL := ""
	authEnv := ""
	providerName := ""
	if raw, ok := root["provider"].(map[string]any); ok {
		// Sort keys for deterministic iteration
		keys := make([]string, 0, len(raw))
		for key := range raw {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		// Find the first provider entry that has options.baseURL
		for _, key := range keys {
			val := raw[key]
			if entry, ok := val.(map[string]any); ok {
				if opts, ok := entry["options"].(map[string]any); ok {
					if b, ok := opts["baseURL"].(string); ok {
						baseURL = b
						providerName = key
						authEnv = deriveAuthEnvForProvider(key)
						break
					}
				}
			}
		}
	}
	return configPath, model, baseURL, authEnv, providerName, nil
}

func deriveAuthEnvForProvider(provider string) string {
	if preset, ok := providerPresets[provider]; ok {
		authEnv := strings.TrimSpace(preset.AuthEnv)
		if authEnv == "" {
			return "ANTHROPIC_API_KEY"
		}
		return authEnv
	}
	return "ANTHROPIC_API_KEY"
}
