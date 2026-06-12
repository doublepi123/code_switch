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

	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}

	if dryRun {
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

	updated := applyOpencodePresetJSON(existing, preset, authEnv)
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
	fmt.Fprintf(out, "%s\n", formatLabel("api_key_env", authEnv))
	return nil
}

func applyOpencodePresetJSON(existing string, preset ProviderPreset, authEnv string) string {
	root := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		_ = json.Unmarshal([]byte(existing), &root)
	}

	root["$schema"] = "https://opencode.ai/config.json"
	root["model"] = preset.Model

	provider := map[string]any{}
	if raw, ok := root["provider"]; ok {
		if m, ok := raw.(map[string]any); ok {
			provider = m
		}
	}

	anthropic := map[string]any{}
	options := map[string]any{
		"baseURL": preset.BaseURL,
		"apiKey":  fmt.Sprintf("{env:%s}", authEnv),
	}
	anthropic["options"] = options
	provider["anthropic"] = anthropic
	root["provider"] = provider

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
	if raw, ok := root["provider"]; ok {
		if provider, ok := raw.(map[string]any); ok {
			delete(provider, "anthropic")
			if len(provider) == 0 {
				delete(root, "provider")
			} else {
				root["provider"] = provider
			}
		}
	}

	if len(root) == 0 {
		return ""
	}
	data, _ := json.MarshalIndent(root, "", "  ")
	return string(data) + "\n"
}

func currentOpencodeProvider(opencodeDir string) (string, string, string, string, error) {
	configPath := opencodeConfigPath(opencodeDir)
	content, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return configPath, "", "", "", nil
		}
		return configPath, "", "", "", err
	}
	root := map[string]any{}
	if err := json.Unmarshal(content, &root); err != nil {
		return configPath, "", "", "", err
	}

	model, _ := root["model"].(string)
	baseURL := ""
	authEnv := ""
	if raw, ok := root["provider"].(map[string]any); ok {
		if anthropic, ok := raw["anthropic"].(map[string]any); ok {
			if opts, ok := anthropic["options"].(map[string]any); ok {
				baseURL, _ = opts["baseURL"].(string)
				if apiKey, ok := opts["apiKey"].(string); ok {
					authEnv = opencodeAuthEnvFromAPIKeyRef(apiKey)
				}
			}
		}
	}
	return configPath, model, baseURL, authEnv, nil
}

func opencodeAuthEnvFromAPIKeyRef(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	const prefix = "{env:"
	const suffix = "}"
	if strings.HasPrefix(apiKey, prefix) && strings.HasSuffix(apiKey, suffix) {
		return strings.TrimSuffix(strings.TrimPrefix(apiKey, prefix), suffix)
	}
	return ""
}
