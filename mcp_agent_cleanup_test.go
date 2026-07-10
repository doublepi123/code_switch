package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeSwitch_removesManagedMCPServer_whenDisabled(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("initial switch returned error: %v", err)
	}
	appCfg.MCPServers["alpha"] = MCPServerConfig{Name: "alpha", Transport: "stdio", Command: "node", Disabled: true}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("update app config: %v", err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	settings := map[string]any{}
	if err := json.Unmarshal(mustReadFile(t, settingsPath), &settings); err != nil {
		t.Fatalf("parse Claude settings: %v", err)
	}
	settings["mcpServers"].(map[string]any)["user-server"] = map[string]any{"command": "uvx"}
	if err := writeJSONAtomic(settingsPath, settings); err != nil {
		t.Fatalf("seed user MCP server: %v", err)
	}

	// When
	output.Reset()
	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("second switch returned error: %v", err)
	}

	// Then
	mcpServers := parseClaudeMCPServers(t, mustReadFile(t, settingsPath))
	if _, ok := mcpServers["alpha"]; ok {
		t.Fatalf("disabled managed server should be removed: %#v", mcpServers)
	}
	if _, ok := mcpServers["user-server"]; !ok {
		t.Fatalf("user MCP server should be preserved: %#v", mcpServers)
	}
}

func TestClaudeSwitch_removesManagedMCPServer_whenChangedToSSE(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("initial switch returned error: %v", err)
	}
	appCfg.MCPServers["alpha"] = MCPServerConfig{Name: "alpha", Transport: "sse", URL: "https://example.com/alpha/sse"}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("update app config: %v", err)
	}

	// When
	output.Reset()
	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("second switch returned error: %v", err)
	}

	// Then
	mcpServers := parseClaudeMCPServersOptional(t, mustReadFile(t, filepath.Join(claudeDir, "settings.json")))
	if _, ok := mcpServers["alpha"]; ok {
		t.Fatalf("SSE managed server should be removed from Claude stdio config: %#v", mcpServers)
	}
}

func TestOpencodeSwitch_removesManagedMCPServer_whenDisabled(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("initial switch returned error: %v", err)
	}
	appCfg.MCPServers["alpha"] = MCPServerConfig{Name: "alpha", Transport: "stdio", Command: "node", Disabled: true}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("update app config: %v", err)
	}
	configPath := filepath.Join(opencodeDir, "opencode.json")
	root := map[string]any{}
	if err := json.Unmarshal(mustReadFile(t, configPath), &root); err != nil {
		t.Fatalf("parse OpenCode config: %v", err)
	}
	root["mcp"].(map[string]any)["user-server"] = map[string]any{"type": "local", "command": []any{"uvx"}, "enabled": true}
	if err := writeJSONAtomic(configPath, root); err != nil {
		t.Fatalf("seed user MCP server: %v", err)
	}

	// When
	output.Reset()
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("second switch returned error: %v", err)
	}

	// Then
	mcpServers := parseOpenCodeMCPServers(t, mustReadFile(t, configPath))
	if _, ok := mcpServers["alpha"]; ok {
		t.Fatalf("disabled managed server should be removed: %#v", mcpServers)
	}
	if _, ok := mcpServers["user-server"]; !ok {
		t.Fatalf("user MCP server should be preserved: %#v", mcpServers)
	}
}

func TestOpencodeSwitch_removesManagedMCPServer_whenChangedToSSE(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("initial switch returned error: %v", err)
	}
	appCfg.MCPServers["alpha"] = MCPServerConfig{Name: "alpha", Transport: "sse", URL: "https://example.com/alpha/sse"}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("update app config: %v", err)
	}

	// When
	output.Reset()
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("second switch returned error: %v", err)
	}

	// Then
	mcpServers := parseOpenCodeMCPServersOptional(t, mustReadFile(t, filepath.Join(opencodeDir, "opencode.json")))
	if _, ok := mcpServers["alpha"]; ok {
		t.Fatalf("SSE managed server should be removed from OpenCode local config: %#v", mcpServers)
	}
}

func TestOpencodeRestore_doesNotCreateConfig_whenMissingAndNoMCPServers(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	appCfg := &AppConfig{Providers: map[string]StoredProvider{}}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}

	// When
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"restore", "--agent", "opencode", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("restore returned error: %v", err)
	}

	// Then
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("OpenCode config should not be created, stat error = %v", err)
	}
	if !strings.Contains(output.String(), "restored OpenCode official config") {
		t.Fatalf("restore output missing success message: %q", output.String())
	}
}

func TestClaudeSwitch_removesManagedMCPServer_whenRemovedFromConfig(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("initial switch returned error: %v", err)
	}

	// Server removed via CLI: no longer in MCPServers, but ManagedMCPNames retains the name until next switch cleans it up.
	appCfg = &AppConfig{
		Providers:       map[string]StoredProvider{},
		MCPServers:      map[string]MCPServerConfig{},
		ManagedMCPNames: []string{"alpha"},
	}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("update app config: %v", err)
	}

	// When
	output.Reset()
	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("second switch returned error: %v", err)
	}

	// Then
	settingsPath := filepath.Join(claudeDir, "settings.json")
	mcpServers := parseClaudeMCPServersOptional(t, mustReadFile(t, settingsPath))
	if _, ok := mcpServers["alpha"]; ok {
		t.Fatalf("removed managed server should be cleaned: %#v", mcpServers)
	}
}

func TestOpencodeSwitch_removesManagedMCPServer_whenRemovedFromConfig(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	appCfg := testMCPAgentConfig()
	appCfg.Providers = map[string]StoredProvider{}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("seed app config: %v", err)
	}
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("initial switch returned error: %v", err)
	}

	// Simulate removing the server via cs mcp remove.
	appCfg = &AppConfig{
		Providers:       map[string]StoredProvider{},
		MCPServers:      map[string]MCPServerConfig{},
		ManagedMCPNames: []string{"alpha"},
	}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), appCfg); err != nil {
		t.Fatalf("update app config: %v", err)
	}

	// When
	output.Reset()
	if err := runWithIO([]string{"switch", "deepseek", "--agent", "opencode", "--api-key", "sk-test", "--opencode-dir", opencodeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("second switch returned error: %v", err)
	}

	// Then
	configPath := filepath.Join(opencodeDir, "opencode.json")
	mcpServers := parseOpenCodeMCPServersOptional(t, mustReadFile(t, configPath))
	if _, ok := mcpServers["alpha"]; ok {
		t.Fatalf("removed managed server should be cleaned: %#v", mcpServers)
	}
}

func parseClaudeMCPServersOptional(t *testing.T, settingsBytes []byte) map[string]any {
	t.Helper()

	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("parse Claude settings: %v", err)
	}
	mcpServers, ok := settings["mcpServers"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return mcpServers
}

func parseOpenCodeMCPServersOptional(t *testing.T, configBytes []byte) map[string]any {
	t.Helper()

	var root map[string]any
	if err := json.Unmarshal(configBytes, &root); err != nil {
		t.Fatalf("parse OpenCode config: %v", err)
	}
	mcpServers, ok := root["mcp"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return mcpServers
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
