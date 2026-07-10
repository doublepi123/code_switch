package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSwitch_writesMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}

	// When
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("switch returned error: %v", err)
	}

	// Then
	settingsBytes, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("parse Claude settings: %v", err)
	}
	mcpServers, ok := settings["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or not an object: %#v", settings["mcpServers"])
	}
	alpha, ok := mcpServers["alpha"].(map[string]any)
	if !ok {
		t.Fatalf("alpha server missing or not an object: %#v", mcpServers["alpha"])
	}
	if got := alpha["command"]; got != "node" {
		t.Fatalf("command = %v, want node", got)
	}
	if _, ok := mcpServers["beta"]; ok {
		t.Fatalf("SSE server should not be written: %#v", mcpServers)
	}
}

func TestCodexPresetTOML_writesMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	cfg := testMCPAgentConfig()

	// When
	got := applyCodexPresetTOML("", providerPresets["deepseek"], "deepseek", cfg)

	// Then
	for _, want := range []string{
		"[mcp_servers.alpha]",
		`command = "node"`,
		`args = ["server.js", "--debug"]`,
		"[mcp_servers.alpha.env]",
		`TOKEN = "secret"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Codex TOML missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "mcp_servers.beta") || strings.Contains(got, "mcp_servers.gamma") {
		t.Fatalf("Codex TOML should skip SSE and disabled servers:\n%s", got)
	}
}

func TestCodexSwitch_writesMCPServersWithRunWithIO_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}

	// When
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "codex", "--api-key", "sk-test", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("switch returned error: %v", err)
	}

	// Then
	configBytes, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{"[mcp_servers.alpha]", `command = "node"`, `TOKEN = "secret"`} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, "mcp_servers.beta") || strings.Contains(config, "mcp_servers.gamma") {
		t.Fatalf("Codex config should skip SSE and disabled servers:\n%s", config)
	}
}

func TestOpencodePresetJSON_writesMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	cfg := testMCPAgentConfig()

	// When
	got, err := applyOpencodePresetJSON("", providerPresets["deepseek"], "deepseek", "sk-test", cfg)
	if err != nil {
		t.Fatalf("applyOpencodePresetJSON returned error: %v", err)
	}

	// Then
	var root map[string]any
	if err := json.Unmarshal([]byte(got), &root); err != nil {
		t.Fatalf("parse OpenCode JSON: %v", err)
	}
	mcpServers, ok := root["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp missing or not an object: %#v", root["mcp"])
	}
	alpha, ok := mcpServers["alpha"].(map[string]any)
	if !ok {
		t.Fatalf("alpha server missing or not an object: %#v", mcpServers["alpha"])
	}
	if got := alpha["type"]; got != "local" {
		t.Fatalf("type = %v, want local", got)
	}
	if got := alpha["enabled"]; got != true {
		t.Fatalf("enabled = %v, want true", got)
	}
	command, ok := alpha["command"].([]any)
	if !ok {
		t.Fatalf("command missing or not an array: %#v", alpha["command"])
	}
	if got := command[0]; got != "node" {
		t.Fatalf("command[0] = %v, want node", got)
	}
	if _, ok := alpha["environment"].(map[string]any); !ok {
		t.Fatalf("environment missing or not an object: %#v", alpha["environment"])
	}
	if _, ok := mcpServers["beta"]; ok {
		t.Fatalf("SSE server should not be written: %#v", mcpServers)
	}
}

func TestOpencodeSwitch_writesMCPServersWithRunWithIO_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}

	// When
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("switch returned error: %v", err)
	}

	// Then
	configBytes, err := os.ReadFile(filepath.Join(opencodeDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read OpenCode config: %v", err)
	}
	assertOpenCodeMCPAlpha(t, configBytes)
}

func TestClaudeRestore_writesMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	root := map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic"}}
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		t.Fatalf("seed Claude settings: %v", err)
	}

	// When
	if err := restoreClaudeConfig(claudeDir, testMCPAgentConfig(), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("restoreClaudeConfig returned error: %v", err)
	}

	// Then
	settingsBytes, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read Claude settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("parse Claude settings: %v", err)
	}
	if _, ok := settings["mcpServers"].(map[string]any)["alpha"]; !ok {
		t.Fatalf("restore did not write alpha MCP server: %#v", settings["mcpServers"])
	}
}

func TestCodexRestore_writesMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir Codex dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("model = \"deepseek-v4-pro\"\n"), 0o644); err != nil {
		t.Fatalf("seed Codex config: %v", err)
	}

	// When
	if err := restoreCodexConfig(codexDir, testMCPAgentConfig(), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("restoreCodexConfig returned error: %v", err)
	}

	// Then
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if got := string(configBytes); !strings.Contains(got, "[mcp_servers.alpha]") {
		t.Fatalf("restore did not write alpha MCP server:\n%s", got)
	}
}

func TestOpencodeRestore_writesMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir OpenCode dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"$schema":"https://opencode.ai/config.json","model":"deepseek-v4-pro"}`), 0o644); err != nil {
		t.Fatalf("seed OpenCode config: %v", err)
	}

	// When
	if err := restoreOpencodeConfig(opencodeDir, testMCPAgentConfig(), &bytes.Buffer{}, false); err != nil {
		t.Fatalf("restoreOpencodeConfig returned error: %v", err)
	}

	// Then
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read OpenCode config: %v", err)
	}
	assertOpenCodeMCPAlpha(t, configBytes)
}

func TestOpencodeRestore_createsMCPServersWithRunWithIO_whenConfigMissing(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}

	// When
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"restore", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("restore returned error: %v", err)
	}

	// Then
	configBytes, err := os.ReadFile(filepath.Join(opencodeDir, "opencode.json"))
	if err != nil {
		t.Fatalf("read OpenCode config: %v", err)
	}
	assertOpenCodeMCPAlpha(t, configBytes)
}

func parseClaudeMCPServers(t *testing.T, settingsBytes []byte) map[string]any {
	t.Helper()

	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("parse Claude settings: %v", err)
	}
	mcpServers, ok := settings["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or not an object: %#v", settings["mcpServers"])
	}
	return mcpServers
}
