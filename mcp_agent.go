package main

import "strings"

func generateClaudeMCPConfig(cfg *AppConfig) map[string]any {
	return generateStdioMCPConfig(cfg)
}

func generateCodexMCPConfig(cfg *AppConfig) map[string]any {
	return generateStdioMCPConfigWithKey(cfg, "mcp_servers")
}

func generateOpencodeMCPConfig(cfg *AppConfig) map[string]any {
	servers := map[string]any{}
	if cfg != nil {
		for name, server := range cfg.MCPServers {
			if server.Disabled || strings.TrimSpace(server.Transport) != "stdio" {
				continue
			}
			servers[name] = opencodeMCPServerConfig(server)
		}
	}
	if len(servers) == 0 {
		return map[string]any{}
	}
	return map[string]any{"mcp": servers}
}

func generateStdioMCPConfig(cfg *AppConfig) map[string]any {
	return generateStdioMCPConfigWithKey(cfg, "mcpServers")
}

func generateStdioMCPConfigWithKey(cfg *AppConfig, key string) map[string]any {
	servers := map[string]any{}
	if cfg != nil {
		for name, server := range cfg.MCPServers {
			if server.Disabled || strings.TrimSpace(server.Transport) != "stdio" {
				continue
			}
			servers[name] = agentMCPServerConfig(server)
		}
	}
	if len(servers) == 0 {
		return map[string]any{}
	}
	return map[string]any{key: servers}
}

func agentMCPServerConfig(server MCPServerConfig) map[string]any {
	out := map[string]any{"command": server.Command}
	if len(server.Args) > 0 {
		out["args"] = append([]string(nil), server.Args...)
	}
	if len(server.Env) > 0 {
		env := make(map[string]string, len(server.Env))
		for key, value := range server.Env {
			env[key] = value
		}
		out["env"] = env
	}
	return out
}

func mcpToolAllowed(s MCPServerConfig, tool string) bool {
	if containsString(s.BlockedTools, tool) {
		return false
	}
	return len(s.AllowedTools) == 0 || containsString(s.AllowedTools, tool)
}

func opencodeMCPServerConfig(server MCPServerConfig) map[string]any {
	command := make([]string, 0, 1+len(server.Args))
	command = append(command, server.Command)
	command = append(command, server.Args...)
	out := map[string]any{
		"type":    "local",
		"command": command,
		"enabled": true,
	}
	if len(server.Env) > 0 {
		env := make(map[string]string, len(server.Env))
		for key, value := range server.Env {
			env[key] = value
		}
		out["environment"] = env
	}
	if len(server.AllowedTools) > 0 {
		out["allowedTools"] = append([]string(nil), server.AllowedTools...)
	}
	if len(server.BlockedTools) > 0 {
		out["blockedTools"] = append([]string(nil), server.BlockedTools...)
	}
	return out
}

func mergeMCPConfig(root map[string]any, generated map[string]any) {
	for key, raw := range generated {
		generatedServers, ok := raw.(map[string]any)
		if !ok || len(generatedServers) == 0 {
			continue
		}
		servers := map[string]any{}
		if existing, ok := root[key].(map[string]any); ok {
			for name, value := range existing {
				servers[name] = value
			}
		}
		for name, value := range generatedServers {
			servers[name] = value
		}
		root[key] = servers
	}
}

func removeManagedMCPFromJSON(root map[string]any, cfg *AppConfig) {
	if cfg == nil || len(cfg.ManagedMCPNames) == 0 {
		return
	}
	for _, key := range []string{"mcpServers", "mcp"} {
		servers, ok := root[key].(map[string]any)
		if !ok {
			continue
		}
		for _, name := range cfg.ManagedMCPNames {
			delete(servers, name)
		}
		if len(servers) == 0 {
			delete(root, key)
		} else {
			root[key] = servers
		}
	}
}
