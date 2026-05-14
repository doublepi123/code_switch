package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func appConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".code-switch", "config.json"), nil
}

func legacyAppConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude-switch", "config.json"), nil
}

func claudeSettingsPath(overrideDir string) string {
	dir := strings.TrimSpace(overrideDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".claude", "settings.json")
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

func loadAppConfig() (*AppConfig, string, error) {
	path, err := appConfigPath()
	if err != nil {
		return nil, "", err
	}
	cfg, err := loadAppConfigFrom(path)
	if err != nil {
		return nil, "", err
	}
	return cfg, path, nil
}

func loadAppConfigLocked() (*AppConfig, string, func(), error) {
	path, err := appConfigPath()
	if err != nil {
		return nil, "", nil, err
	}
	cf := newConfigFile(path)
	unlock, err := cf.lock()
	if err != nil {
		return nil, "", nil, err
	}
	cfg, err := loadAppConfigFrom(path)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	return cfg, path, unlock, nil
}

func loadAppConfigFrom(path string) (*AppConfig, error) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		legacyPath, legacyErr := legacyAppConfigPath()
		if legacyErr != nil {
			return nil, legacyErr
		}
		legacyData, legacyReadErr := os.ReadFile(legacyPath)
		if legacyReadErr != nil {
			if os.IsNotExist(legacyReadErr) {
				ensureAppConfigMaps(cfg)
				return cfg, nil
			}
			return nil, legacyReadErr
		}
		if err := json.Unmarshal(legacyData, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", legacyPath, err)
		}
		migrateLegacyProviders(cfg)
		ensureAppConfigMaps(cfg)
		if err := writeJSONAtomic(path, cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	migrateLegacyProviders(cfg)
	ensureAppConfigMaps(cfg)
	return cfg, nil
}

func migrateLegacyProviders(cfg *AppConfig) {
	legacy, ok := cfg.Providers["minimax"]
	if ok {
		if _, exists := cfg.Providers["minimax-cn"]; !exists && strings.TrimSpace(legacy.APIKey) != "" {
			cfg.Providers["minimax-cn"] = legacy
		}
		delete(cfg.Providers, "minimax")
	}
}

func readJSONMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func nestedMap(root map[string]any, key string) map[string]any {
	raw, ok := root[key]
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

func ensureNestedMap(root map[string]any, key string) map[string]any {
	if obj := nestedMap(root, key); obj != nil {
		return obj
	}
	obj := map[string]any{}
	root[key] = obj
	return obj
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	os.Chmod(filepath.Dir(path), 0o755)
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	// Ensure config files containing API keys are never world-readable.
	return os.Chmod(path, 0o600)
}

func backupIfExists(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backupDir := filepath.Dir(path)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	os.Chmod(backupDir, 0o755)
	f, err := os.CreateTemp(backupDir, filepath.Base(path)+".bak-*")
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

func upsertProviderConfig(cfg *AppConfig, selection ConfigureSelection, apiKey string) {
	if selection.Agent == string(agentCodex) {
		upsertAgentProviderConfig(cfg, agentCodex, selection, apiKey)
		return
	}
	stored := cfg.Providers[selection.Provider]
	stored.APIKey = apiKey
	stored.Model = strings.TrimSpace(selection.Model)
	stored.AuthEnv = strings.TrimSpace(selection.AuthEnv)
	if selection.Name != "" {
		stored.Name = strings.TrimSpace(selection.Name)
	}
	if selection.BaseURL != "" {
		stored.BaseURL = strings.TrimSpace(selection.BaseURL)
	}
	cfg.Providers[selection.Provider] = stored
}

func upsertAgentProviderConfig(cfg *AppConfig, agent AgentName, selection ConfigureSelection, apiKey string) {
	stored := codexProviderConfig(cfg, selection.Provider)
	stored.APIKey = apiKey
	stored.Model = strings.TrimSpace(selection.Model)
	if selection.Name != "" {
		stored.Name = strings.TrimSpace(selection.Name)
	}
	if selection.BaseURL != "" {
		stored.BaseURL = strings.TrimSpace(selection.BaseURL)
	}
	setAgentProviderConfig(cfg, agent, selection.Provider, stored)
}

func currentConfiguredProvider(cfg *AppConfig, claudeDir string) (string, string) {
	root, err := readJSONMap(claudeSettingsPath(claudeDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: read settings:", err)
		return "", ""
	}
	env := nestedMap(root, "env")
	if env == nil {
		return "", ""
	}
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)
	if provider := detectProvider(baseURL, model); provider != customDetectedProvider {
		return provider, model
	}
	for name, stored := range cfg.Providers {
		if strings.TrimSpace(stored.BaseURL) == strings.TrimSpace(baseURL) {
			return name, model
		}
	}
	return customDetectedProvider, model
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "not saved"
	}
	if len(key) <= 6 {
		return strings.Repeat("*", len(key))
	}
	return key[:3] + strings.Repeat("*", len(key)-6) + key[len(key)-3:]
}
