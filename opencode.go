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

	updated, err := applyOpencodePresetJSON(existing, preset, provider, apiKey, cfg)
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

func applyOpencodePresetJSON(existing string, preset ProviderPreset, providerKey string, apiKey string, cfg *AppConfig) (string, error) {
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
		for key, value := range existingProviders {
			if !isOpencodeManagedProviderKey(key, cfg) {
				providers[key] = value
			}
		}
	}
	providers[providerKey] = providerEntry
	root["provider"] = providers
	removeManagedMCPFromJSON(root, cfg, agentOpencode)
	mergeMCPConfig(root, generateOpencodeMCPConfig(cfg))

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal OpenCode config: %w", err)
	}
	return string(data) + "\n", nil
}

func restoreOpencodeConfig(opencodeDir string, cfg *AppConfig, out io.Writer, dryRun bool) error {
	configPath := opencodeConfigPath(opencodeDir)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if !dryRun {
			generated := generateOpencodeMCPConfig(cfg)
			if len(generated) > 0 {
				root := map[string]any{}
				mergeMCPConfig(root, generated)
				data, err := json.MarshalIndent(root, "", "  ")
				if err != nil {
					return fmt.Errorf("marshal OpenCode config: %w", err)
				}
				if err := writeTextAtomic(configPath, string(data)+"\n", 0o600); err != nil {
					return err
				}
			}
		}
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
	updated, err := removeOpencodeManagedJSON(existing, cfg)
	if err != nil {
		return err
	}

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

func removeOpencodeManagedJSON(existing string, cfg *AppConfig) (string, error) {
	root := map[string]any{}
	if strings.TrimSpace(existing) == "" {
		return "", nil
	}
	if err := json.Unmarshal([]byte(existing), &root); err != nil {
		return "", fmt.Errorf("parse existing OpenCode config: %w", err)
	}

	delete(root, "model")
	if raw, ok := root["provider"]; ok {
		providers, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("parse existing OpenCode config: provider must be an object")
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
	removeManagedMCPFromJSON(root, cfg, agentOpencode)
	mergeMCPConfig(root, generateOpencodeMCPConfig(cfg))

	// If only $schema remains, return empty to trigger file deletion
	if len(root) == 1 {
		if _, hasSchema := root["$schema"]; hasSchema {
			return "", nil
		}
	}

	if len(root) == 0 {
		return "", nil
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal OpenCode config: %w", err)
	}
	return string(data) + "\n", nil
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
	raw, hasProviders := root["provider"].(map[string]any)
	if !hasProviders {
		return configPath, model, baseURL, authEnv, providerName, nil
	}
	// Sort keys for deterministic iteration
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// applyOpencodePresetJSON never removes old provider entries, so after
	// switching from provider A to provider B the JSON contains both. The
	// active provider is the one whose "models" map contains root["model"];
	// picking the first sorted provider with a baseURL reports the wrong
	// provider/baseURL after switching. Match by model first, and only fall
	// back to the first provider with a baseURL when no model match is found.
	if model != "" {
		for _, key := range keys {
			entry, ok := raw[key].(map[string]any)
			if !ok {
				continue
			}
			models, ok := entry["models"].(map[string]any)
			if !ok {
				continue
			}
			if _, has := models[model]; !has {
				continue
			}
			if opts, ok := entry["options"].(map[string]any); ok {
				if b, ok := opts["baseURL"].(string); ok {
					baseURL = b
				}
			}
			providerName = key
			authEnv = deriveAuthEnvForProvider(key)
			return configPath, model, baseURL, authEnv, providerName, nil
		}
	}
	// Fall back to the first provider entry that has options.baseURL.
	for _, key := range keys {
		entry, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		opts, ok := entry["options"].(map[string]any)
		if !ok {
			continue
		}
		b, ok := opts["baseURL"].(string)
		if !ok {
			continue
		}
		baseURL = b
		providerName = key
		authEnv = deriveAuthEnvForProvider(key)
		break
	}
	return configPath, model, baseURL, authEnv, providerName, nil
}

func deriveAuthEnvForProvider(provider string) string {
	provider = canonicalProviderName(provider)
	if preset, ok := providerPresets[provider]; ok {
		authEnv := strings.TrimSpace(preset.AuthEnv)
		if authEnv == "" {
			return "ANTHROPIC_API_KEY"
		}
		return authEnv
	}
	return "ANTHROPIC_API_KEY"
}
