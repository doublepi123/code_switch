package main

import "testing"

func TestRemoveManagedMCPFromJSON_removesOnlyRequestedAgentNames(t *testing.T) {
	t.Parallel()

	root := map[string]any{
		"mcpServers": map[string]any{
			"claude-managed":   map[string]any{"command": "node"},
			"opencode-managed": map[string]any{"command": "python"},
			"user-server":      map[string]any{"command": "uvx"},
		},
	}
	cfg := &AppConfig{ManagedMCPNamesByAgent: map[string][]string{
		string(agentClaude):   {"claude-managed"},
		string(agentOpencode): {"opencode-managed"},
	}}

	removeManagedMCPFromJSON(root, cfg, agentClaude)

	servers := root["mcpServers"].(map[string]any)
	if _, exists := servers["claude-managed"]; exists {
		t.Fatalf("claude-managed server was not removed: %#v", servers)
	}
	if _, exists := servers["opencode-managed"]; !exists {
		t.Fatalf("opencode-managed server was removed by Claude cleanup: %#v", servers)
	}
	if _, exists := servers["user-server"]; !exists {
		t.Fatalf("user-owned server was removed: %#v", servers)
	}
}
