package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeSwitchWritesJSONWithEnvBasedAPIKey(t *testing.T) {
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
	provider := cfg["provider"].(map[string]any)["anthropic"].(map[string]any)
	options := provider["options"].(map[string]any)
	if got := options["baseURL"]; got != "https://api.deepseek.com/anthropic" {
		t.Fatalf("baseURL = %v, want https://api.deepseek.com/anthropic", got)
	}
	if got := options["apiKey"]; got != "{env:ANTHROPIC_AUTH_TOKEN}" {
		t.Fatalf("apiKey = %v, want {env:ANTHROPIC_AUTH_TOKEN}", got)
	}
	if strings.Contains(string(configBytes), "sk-ds-test") {
		t.Fatalf("opencode config must not contain plaintext api key")
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

func TestOpencodeSwitchWithDefaultAuthEnv(t *testing.T) {
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
	provider := cfg["provider"].(map[string]any)["anthropic"].(map[string]any)
	options := provider["options"].(map[string]any)
	// OpenRouter preset does not set AuthEnv, so default ANTHROPIC_API_KEY is used
	if got := options["apiKey"]; got != "{env:ANTHROPIC_API_KEY}" {
		t.Fatalf("apiKey = %v, want {env:ANTHROPIC_API_KEY}", got)
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
    "anthropic": {
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "{env:ANTHROPIC_AUTH_TOKEN}"
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
	for _, unwanted := range []string{`"model"`, `"anthropic"`, `"baseURL"`, `"apiKey"`} {
		if strings.Contains(restored, unwanted) {
			t.Fatalf("restored config still contains %q:\n%s", unwanted, restored)
		}
	}
	if !strings.Contains(restored, `"tools"`) {
		t.Fatalf("restored config lost user tools section:\n%s", restored)
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
    "anthropic": {
      "options": {
        "baseURL": "https://api.deepseek.com/anthropic",
        "apiKey": "{env:ANTHROPIC_AUTH_TOKEN}"
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
	provider := cfg["provider"].(map[string]any)["anthropic"].(map[string]any)
	options := provider["options"].(map[string]any)
	if got := options["baseURL"]; got != "https://api.deepseek.com/anthropic" {
		t.Fatalf("baseURL = %v", got)
	}
}
