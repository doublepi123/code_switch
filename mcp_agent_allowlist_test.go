package main

import "testing"

func TestMCPToolAllowed(t *testing.T) {
	tests := []struct {
		name   string
		server MCPServerConfig
		tool   string
		want   bool
	}{
		{
			name: "empty allowlist and blocklist allow any tool",
			server: MCPServerConfig{Name: "alpha"},
			tool:   "tool-a",
			want:   true,
		},
		{
			name: "blocklist blocks listed tool",
			server: MCPServerConfig{Name: "alpha", BlockedTools: []string{"tool-a"}},
			tool:   "tool-a",
			want:   false,
		},
		{
			name: "allowlist only allows listed tools",
			server: MCPServerConfig{Name: "alpha", AllowedTools: []string{"tool-a"}},
			tool:   "tool-b",
			want:   false,
		},
		{
			name: "blocklist overrides allowlist",
			server: MCPServerConfig{Name: "alpha", AllowedTools: []string{"tool-a"}, BlockedTools: []string{"tool-a"}},
			tool:   "tool-a",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpToolAllowed(tc.server, tc.tool); got != tc.want {
				t.Fatalf("mcpToolAllowed() = %v, want %v", got, tc.want)
			}
		})
	}
}
