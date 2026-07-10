package main

import (
	"reflect"
	"testing"
)

func TestGenerateClaudeMCPConfig_includesStdioServers_whenEnabled(t *testing.T) {
	t.Parallel()

	// Given
	cfg := testMCPAgentConfig()

	// When
	got := generateClaudeMCPConfig(cfg)

	// Then
	want := map[string]any{
		"mcpServers": map[string]any{
			"alpha": map[string]any{
				"command": "node",
				"args":    []string{"server.js", "--debug"},
				"env":     map[string]string{"TOKEN": "secret"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generateClaudeMCPConfig() = %#v, want %#v", got, want)
	}
}

func TestGenerateCodexMCPConfig_includesStdioServers_whenEnabled(t *testing.T) {
	t.Parallel()

	// Given
	cfg := testMCPAgentConfig()

	// When
	got := generateCodexMCPConfig(cfg)

	// Then
	want := map[string]any{
		"mcp_servers": map[string]any{
			"alpha": map[string]any{
				"command": "node",
				"args":    []string{"server.js", "--debug"},
				"env":     map[string]string{"TOKEN": "secret"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generateCodexMCPConfig() = %#v, want %#v", got, want)
	}
}

func TestGenerateOpencodeMCPConfig_includesStdioServers_whenEnabled(t *testing.T) {
	t.Parallel()

	// Given
	cfg := testMCPAgentConfig()

	// When
	got := generateOpencodeMCPConfig(cfg)

	// Then
	want := map[string]any{
		"mcp": map[string]any{
			"alpha": map[string]any{
				"type":        "local",
				"command":     []string{"node", "server.js", "--debug"},
				"environment": map[string]string{"TOKEN": "secret"},
				"enabled":     true,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("generateOpencodeMCPConfig() = %#v, want %#v", got, want)
	}
}

func testMCPAgentConfig() *AppConfig {
	return &AppConfig{
		MCPServers: map[string]MCPServerConfig{
			"alpha": {
				Name:      "alpha",
				Transport: "stdio",
				Command:   "node",
				Args:      []string{"server.js", "--debug"},
				Env:       map[string]string{"TOKEN": "secret"},
			},
			"beta": {
				Name:      "beta",
				Transport: "sse",
				URL:       "https://example.com/sse",
			},
			"gamma": {
				Name:      "gamma",
				Transport: "stdio",
				Command:   "python",
				Args:      []string{"server.py"},
				Disabled:  true,
			},
		},
		ManagedMCPNames: []string{"alpha", "beta", "gamma"},
	}
}
