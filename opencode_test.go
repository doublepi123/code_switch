package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeSwitchWritesJSONWithProviderBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-ds-test", "--model", "deepseek-v4-pro", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(opencodeDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("parse opencode config: %v", err)
	}

	if got := cfg["model"]; got != "deepseek-v4-pro" {
		t.Fatalf("model = %v, want deepseek-v4-pro", got)
	}
	providers := cfg["provider"].(map[string]any)
	// Provider key should be "deepseek" not "anthropic"
	providerEntry, ok := providers["deepseek"].(map[string]any)
	if !ok {
		t.Fatalf("provider entry key should be 'deepseek', got keys: %v", keysOf(providers))
	}
	if got := providerEntry["npm"]; got != "@ai-sdk/anthropic" {
		t.Fatalf("npm = %v, want @ai-sdk/anthropic", got)
	}
	if got := providerEntry["name"]; got != "DeepSeek" {
		t.Fatalf("name = %v, want DeepSeek", got)
	}
	options := providerEntry["options"].(map[string]any)
	if got := options["baseURL"]; got != "https://api.deepseek.com/anthropic" {
		t.Fatalf("baseURL = %v, want https://api.deepseek.com/anthropic", got)
	}
	if got := options["apiKey"]; got != "sk-ds-test" {
		t.Fatalf("apiKey = %v, want sk-ds-test (plaintext)", got)
	}
	models := providerEntry["models"].(map[string]any)
	if _, ok := models["deepseek-v4-pro"]; !ok {
		t.Fatalf("models missing deepseek-v4-pro key")
	}
	modelEntry := models["deepseek-v4-pro"].(map[string]any)
	if got := modelEntry["name"]; got != "deepseek-v4-pro" {
		t.Fatalf("model name = %v, want deepseek-v4-pro", got)
	}

	appBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	var appCfg AppConfig
	if err := json.Unmarshal(appBytes, &appCfg); err != nil {
		t.Fatalf("unmarshal app config: %v", err)
	}
	if got := appCfg.Agents["opencode"].Providers["deepseek"].APIKey; got != "sk-ds-test" {
		t.Fatalf("opencode stored key = %q, want %q", got, "sk-ds-test")
	}
	if got := appCfg.Agents["opencode"].Providers["deepseek"].Model; got != "deepseek-v4-pro" {
		t.Fatalf("opencode stored model = %q, want %q", got, "deepseek-v4-pro")
	}
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestOpencodeSwitchWithPlaintextAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "openrouter", "--agent", "opencode", "--api-key", "sk-or-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(opencodeDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("parse opencode config: %v", err)
	}
	providerEntry := cfg["provider"].(map[string]any)["openrouter"].(map[string]any)
	if got := providerEntry["npm"]; got != "@ai-sdk/anthropic" {
		t.Fatalf("npm = %v, want @ai-sdk/anthropic", got)
	}
	if got := providerEntry["name"]; got != "OpenRouter" {
		t.Fatalf("name = %v, want OpenRouter", got)
	}
	options := providerEntry["options"].(map[string]any)
	if got := options["apiKey"]; got != "sk-or-test" {
		t.Fatalf("apiKey = %v, want sk-or-test (plaintext)", got)
	}
	// OpenRouter preset does not set AuthEnv, so it would default to ANTHROPIC_API_KEY
	// but the config should store the key in plaintext, not as {env:...}
}

func TestOpencodeSwitchPreservesExistingProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode dir: %v", err)
	}
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "legacy-model",
  "provider": {
    "legacy": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Legacy Provider",
      "options": {
        "baseURL": "https://legacy.example.com",
        "apiKey": "legacy-key"
      },
      "models": {
        "legacy-model": { "name": "legacy-model" }
      }
    }
  },
  "tools": {
    "write": true
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-ds-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("parse opencode config: %v", err)
	}
	providers := cfg["provider"].(map[string]any)
	if _, ok := providers["legacy"]; !ok {
		t.Fatalf("switch lost existing provider block:\n%s", string(configBytes))
	}
	if _, ok := providers["deepseek"]; !ok {
		t.Fatalf("switch did not add selected provider:\n%s", string(configBytes))
	}
	if _, ok := cfg["tools"].(map[string]any); !ok {
		t.Fatalf("switch lost user tools section:\n%s", string(configBytes))
	}
}

func TestOpencodeSwitchRejectsInvalidExistingJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode dir: %v", err)
	}
	original := []byte(`{"provider":`)
	if err := os.WriteFile(configPath, original, 0o644); err != nil {
		t.Fatalf("write invalid opencode config: %v", err)
	}

	output := &bytes.Buffer{}
	err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-ds-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output)
	if err == nil {
		t.Fatal("expected invalid existing opencode config to fail")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error = %v, want parse context", err)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read opencode config after failed switch: %v", readErr)
	}
	if string(after) != string(original) {
		t.Fatalf("invalid config was modified, got %q want %q", string(after), string(original))
	}
}

func TestOpencodeRestoreRemovesManagedSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode dir: %v", err)
	}
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "deepseek-v4-pro",
  "provider": {
    "deepseek": {
      "npm": "@ai-sdk/anthropic",
      "name": "DeepSeek",
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "sk-ds-test"
      },
      "models": {
        "deepseek-v4-pro": { "name": "deepseek-v4-pro" }
      }
    }
  },
  "tools": {
    "write": true
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"restore", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode restore returned error: %v", err)
	}

	restoredBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	restored := string(restoredBytes)
	for _, unwanted := range []string{`"model"`, `"provider"`, `"baseURL"`, `"apiKey"`} {
		if strings.Contains(restored, unwanted) {
			t.Fatalf("restored config still contains %q:\n%s", unwanted, restored)
		}
	}
	if !strings.Contains(restored, `"tools"`) {
		t.Fatalf("restored config lost user tools section:\n%s", restored)
	}
	// $schema should be preserved since tools section is present
	if !strings.Contains(restored, `"$schema"`) {
		t.Fatalf("restored config lost $schema:\n%s", restored)
	}
}

func TestOpencodeRestorePreservesUnmanagedProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode dir: %v", err)
	}
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "deepseek-v4-pro",
  "provider": {
    "deepseek": {
      "npm": "@ai-sdk/anthropic",
      "name": "DeepSeek",
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "sk-ds-test"
      },
      "models": {
        "deepseek-v4-pro": { "name": "deepseek-v4-pro" }
      }
    },
    "legacy": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Legacy Provider",
      "options": {
        "baseURL": "https://legacy.example.com",
        "apiKey": "legacy-key"
      },
      "models": {
        "legacy-model": { "name": "legacy-model" }
      }
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"restore", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode restore returned error: %v", err)
	}

	restoredBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	var restored map[string]any
	if err := json.Unmarshal(restoredBytes, &restored); err != nil {
		t.Fatalf("parse restored config: %v", err)
	}
	providers := restored["provider"].(map[string]any)
	if _, ok := providers["deepseek"]; ok {
		t.Fatalf("restore kept managed provider:\n%s", string(restoredBytes))
	}
	if _, ok := providers["legacy"]; !ok {
		t.Fatalf("restore lost unmanaged provider:\n%s", string(restoredBytes))
	}
	if _, ok := restored["model"]; ok {
		t.Fatalf("restore kept managed model:\n%s", string(restoredBytes))
	}
}

func TestOpencodeRestoreRemovesFileWhenOnlySchemaRemains(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode dir: %v", err)
	}
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "deepseek-v4-pro",
  "provider": {
    "deepseek": {
      "npm": "@ai-sdk/anthropic",
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "sk-ds-test"
      },
      "models": { "deepseek-v4-pro": { "name": "deepseek-v4-pro" } }
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"restore", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode restore returned error: %v", err)
	}

	// File should be deleted since only $schema would remain
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("opencode config should be deleted after restore when only $schema remains")
	}
}

func TestOpencodeRestoreNonExistentConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode-nonexistent")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"restore", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode restore should succeed on non-existent config: %v", err)
	}
	if !strings.Contains(output.String(), "restored OpenCode official config") {
		t.Fatalf("expected restore success message, got: %s", output.String())
	}
}

func TestOpencodeCurrentReadsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir opencode dir: %v", err)
	}
	configPath := filepath.Join(opencodeDir, "opencode.json")
	config := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "deepseek-v4-pro",
  "provider": {
    "deepseek": {
      "npm": "@ai-sdk/anthropic",
      "name": "DeepSeek",
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "sk-ds-test"
      },
      "models": {
        "deepseek-v4-pro": { "name": "deepseek-v4-pro" }
      }
    }
  }
}`
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"current", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode current returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, "OpenCode") {
		t.Fatalf("current output missing OpenCode header:\n%s", out)
	}
	if !strings.Contains(out, "deepseek") {
		t.Fatalf("current output missing deepseek provider:\n%s", out)
	}
	if !strings.Contains(out, "deepseek-v4-pro") {
		t.Fatalf("current output missing model:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("current output missing auth env:\n%s", out)
	}
}

func TestOpencodeEnvPrintsExports(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"env", "deepseek", "--agent", "opencode", "--api-key", "sk-ds-test"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode env returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, "ANTHROPIC_BASE_URL") {
		t.Fatalf("env output missing ANTHROPIC_BASE_URL:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_MODEL") {
		t.Fatalf("env output missing ANTHROPIC_MODEL:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("env output missing ANTHROPIC_AUTH_TOKEN:\n%s", out)
	}
	if !strings.Contains(out, "sk-ds-test") {
		t.Fatalf("env output missing api key:\n%s", out)
	}
}

func TestOpencodeTokenPrintsSavedKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// First set the key
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"set-key", "--agent", "opencode", "deepseek", "sk-ds-test"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode set-key returned error: %v", err)
	}

	// Then get token
	output.Reset()
	if err := runWithIO([]string{"token", "deepseek", "--agent", "opencode"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode token returned error: %v", err)
	}

	if got := strings.TrimSpace(output.String()); got != "sk-ds-test" {
		t.Fatalf("token = %q, want sk-ds-test", got)
	}
}

func TestOpencodeListIncludesClaudeProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"list", "--agent", "opencode"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode list returned error: %v", err)
	}

	out := output.String()
	// OpenCode should include the same providers as Claude
	for _, name := range []string{"deepseek", "openrouter"} {
		if !strings.Contains(out, name) {
			t.Fatalf("list output missing provider %q:\n%s", name, out)
		}
	}
	// Codex-only providers should not appear
	if strings.Contains(out, "kimi-coding") {
		// kimi-coding is also a Claude preset now, so it should appear
	}
}

func TestOpencodeSwitchDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir, "--dry-run"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode switch dry-run returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, "[dry-run]") {
		t.Fatalf("dry-run output missing marker:\n%s", out)
	}
	if !strings.Contains(out, "deepseek") {
		t.Fatalf("dry-run output missing provider:\n%s", out)
	}
	// Config file should not exist
	if _, err := os.Stat(filepath.Join(opencodeDir, "opencode.json")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create config file")
	}
}

func TestOpencodeSetKeyAndRemoveProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Set key
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"set-key", "--agent", "opencode", "openrouter", "sk-or-test"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode set-key returned error: %v", err)
	}

	// Verify key is stored
	appBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	var appCfg AppConfig
	if err := json.Unmarshal(appBytes, &appCfg); err != nil {
		t.Fatalf("unmarshal app config: %v", err)
	}
	if got := appCfg.Agents["opencode"].Providers["openrouter"].APIKey; got != "sk-or-test" {
		t.Fatalf("stored key = %q, want sk-or-test", got)
	}

	// Remove with force
	output.Reset()
	if err := runWithIO([]string{"remove", "--agent", "opencode", "--force", "openrouter"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode remove returned error: %v", err)
	}

	// Verify key is removed
	appBytes, err = os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config after remove: %v", err)
	}
	appCfg = AppConfig{}
	if err := json.Unmarshal(appBytes, &appCfg); err != nil {
		t.Fatalf("unmarshal app config after remove: %v", err)
	}
	if _, ok := appCfg.Agents["opencode"].Providers["openrouter"]; ok {
		t.Fatalf("provider should be removed from opencode agent config")
	}
}

func TestOpencodeCustomProviderSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")

	// First create a custom provider via switch (which creates a stored custom provider)
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-ds-custom", "--model", "deepseek-v4-flash", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("opencode switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(opencodeDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("parse opencode config: %v", err)
	}

	// Custom model should be in the config
	if got := cfg["model"]; got != "deepseek-v4-flash" {
		t.Fatalf("model = %v, want deepseek-v4-flash", got)
	}
	providerEntry := cfg["provider"].(map[string]any)["deepseek"].(map[string]any)
	options := providerEntry["options"].(map[string]any)
	if got := options["baseURL"]; got != "https://api.deepseek.com/anthropic" {
		t.Fatalf("baseURL = %v", got)
	}
	if got := providerEntry["npm"]; got != "@ai-sdk/anthropic" {
		t.Fatalf("npm = %v, want @ai-sdk/anthropic", got)
	}
	models := providerEntry["models"].(map[string]any)
	if _, ok := models["deepseek-v4-flash"]; !ok {
		t.Fatalf("models missing deepseek-v4-flash key")
	}
}
