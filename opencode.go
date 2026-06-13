package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	existingBytes, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing := string(existingBytes)

	if err := backupIfExists(configPath); err != nil {
		return err
	}

	updated := applyOpencodePresetJSON(existing, preset, provider, apiKey)
	if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
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

func applyOpencodePresetJSON(existing string, preset ProviderPreset, providerKey string, apiKey string) string {
	root := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		_ = json.Unmarshal([]byte(existing), &root)
	}

	root["$schema"] = "https://opencode.ai/config.json"
	root["model"] = preset.Model

	npmPkg := opencodeNPMForProvider(providerKey)

	models := map[string]any{}
	models[preset.Model] = map[string]any{"name": preset.Model}

	providerEntry := map[string]any{
		"npm":     npmPkg,
		"name":    preset.Name,
		"options": map[string]any{
			"baseURL": preset.BaseURL,
			"apiKey":  apiKey,
		},
		"models": models,
	}

	// Replace entire provider object with single entry for the selected provider
	root["provider"] = map[string]any{
		providerKey: providerEntry,
	}

	data, _ := json.MarshalIndent(root, "", "  ")
	return string(data) + "\n"
}

func restoreOpencodeConfig(opencodeDir string, cfg *AppConfig, out io.Writer, dryRun bool) error {
	configPath := opencodeConfigPath(opencodeDir)
	existingBytes, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(out, "%s\n", successPrefix("restored OpenCode official config"))
			fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
			return nil
		}
		return err
	}
	existing := string(existingBytes)
	updated := removeOpencodeManagedJSON(existing)

	if dryRun {
		fmt.Fprintf(out, "[dry-run] would restore OpenCode official config\n")
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		return nil
	}

	if err := backupIfExists(configPath); err != nil {
		return err
	}

	if strings.TrimSpace(updated) == "" {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "%s\n", successPrefix("restored OpenCode official config"))
	fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
	return nil
}

func removeOpencodeManagedJSON(existing string) string {
	root := map[string]any{}
	if strings.TrimSpace(existing) == "" {
		return ""
	}
	if err := json.Unmarshal([]byte(existing), &root); err != nil {
		return existing
	}

	delete(root, "model")
	delete(root, "provider")

	// If only $schema remains, return empty to trigger file deletion
	if len(root) == 1 {
		if _, hasSchema := root["$schema"]; hasSchema {
			return ""
		}
	}

	if len(root) == 0 {
		return ""
	}
	data, _ := json.MarshalIndent(root, "", "  ")
	return string(data) + "\n"
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
		// Find the first provider entry that has options.baseURL
		for key, val := range raw {
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
