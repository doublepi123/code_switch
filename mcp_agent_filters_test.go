package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpencodePresetJSON_includesAllowedAndBlockedTools_whenConfigured(t *testing.T) {
	// Given
	cfg := testMCPAgentConfig()
	cfg.MCPServers["alpha"] = MCPServerConfig{
		Name:         "alpha",
		Transport:    "stdio",
		Command:      "node",
		AllowedTools: []string{"tool-a", "tool-b"},
		BlockedTools: []string{"tool-x"},
	}

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
	alpha := root["mcp"].(map[string]any)["alpha"].(map[string]any)
	if got := alpha["allowedTools"]; got == nil {
		t.Fatal("allowedTools missing from OpenCode config")
	}
	if got := alpha["blockedTools"]; got == nil {
		t.Fatal("blockedTools missing from OpenCode config")
	}
	if _, ok := alpha["allowedTools"].([]any); !ok {
		t.Fatalf("allowedTools = %#v, want array", alpha["allowedTools"])
	}
	if _, ok := alpha["blockedTools"].([]any); !ok {
		t.Fatalf("blockedTools = %#v, want array", alpha["blockedTools"])
	}
}

func TestClaudePresetJSON_excludesToolFilters_whenConfigured(t *testing.T) {
	// Given
	cfg := testMCPAgentConfig()
	cfg.MCPServers["alpha"] = MCPServerConfig{
		Name:         "alpha",
		Transport:    "stdio",
		Command:      "node",
		AllowedTools: []string{"tool-a"},
		BlockedTools: []string{"tool-x"},
	}

	// When
	got := generateClaudeMCPConfig(cfg)

	// Then
	alpha := got["mcpServers"].(map[string]any)["alpha"].(map[string]any)
	if _, ok := alpha["allowedTools"]; ok {
		t.Fatalf("Claude config should not include allowedTools: %#v", alpha)
	}
	if _, ok := alpha["blockedTools"]; ok {
		t.Fatalf("Claude config should not include blockedTools: %#v", alpha)
	}
}

func TestCodexPresetTOML_excludesToolFilters_whenConfigured(t *testing.T) {
	// Given
	cfg := testMCPAgentConfig()
	cfg.MCPServers["alpha"] = MCPServerConfig{
		Name:         "alpha",
		Transport:    "stdio",
		Command:      "node",
		AllowedTools: []string{"tool-a"},
		BlockedTools: []string{"tool-x"},
	}

	// When
	got := applyCodexPresetTOML("", providerPresets["deepseek"], "deepseek", cfg)

	// Then
	if strings.Contains(got, "allowedTools") || strings.Contains(got, "blockedTools") {
		t.Fatalf("Codex TOML should not include tool filters:\n%s", got)
	}
}
