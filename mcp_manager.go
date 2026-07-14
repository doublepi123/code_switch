package main

import (
	"fmt"
	"sort"
	"strings"
)

type mcpManager struct{ cfg *AppConfig }

func newMCPManager(cfg *AppConfig) *mcpManager { return &mcpManager{cfg: cfg} }

func (m *mcpManager) add(s MCPServerConfig) error {
	if m.cfg == nil {
		return fmt.Errorf("mcp manager requires config")
	}
	s.Name = canonicalMCPServerName(s.Name)
	if err := validateMCPServerConfig(s); err != nil {
		return err
	}
	ensureAppConfigMaps(m.cfg)
	if _, ok := m.cfg.MCPServers[s.Name]; ok {
		return fmt.Errorf("mcp server %q already exists", s.Name)
	}
	m.cfg.MCPServers[s.Name] = s
	m.cfg.ManagedMCPNames = appendUniqueManagedMCPName(m.cfg.ManagedMCPNames, s.Name)
	return nil
}

func (m *mcpManager) remove(name string) error {
	if m.cfg == nil {
		return fmt.Errorf("mcp manager requires config")
	}
	ensureAppConfigMaps(m.cfg)
	key := canonicalMCPServerName(name)
	if _, ok := m.cfg.MCPServers[key]; !ok {
		return fmt.Errorf("mcp server %q not found", key)
	}
	delete(m.cfg.MCPServers, key)
	return nil
}

func appendUniqueManagedMCPName(names []string, name string) []string {
	for _, n := range names {
		if n == name {
			return names
		}
	}
	return append(names, name)
}

func (m *mcpManager) list() []MCPServerConfig {
	if m.cfg == nil || len(m.cfg.MCPServers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m.cfg.MCPServers))
	for name := range m.cfg.MCPServers {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	servers := make([]MCPServerConfig, 0, len(keys))
	for _, name := range keys {
		servers = append(servers, m.cfg.MCPServers[name])
	}
	return servers
}

func (m *mcpManager) get(name string) (MCPServerConfig, bool) {
	if m.cfg == nil {
		return MCPServerConfig{}, false
	}
	ensureAppConfigMaps(m.cfg)
	server, ok := m.cfg.MCPServers[canonicalMCPServerName(name)]
	return server, ok
}

func canonicalMCPServerName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func managedMCPServerNames(cfg *AppConfig) []string {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func managedMCPNamesForAgent(cfg *AppConfig, agent AgentName) []string {
	if cfg == nil {
		return nil
	}
	return append([]string(nil), cfg.ManagedMCPNamesByAgent[string(agent)]...)
}

func setManagedMCPNamesForAgent(cfg *AppConfig, agent AgentName) {
	if cfg == nil {
		return
	}
	ensureAppConfigMaps(cfg)
	cfg.ManagedMCPNamesByAgent[string(agent)] = append([]string(nil), managedMCPServerNames(cfg)...)
}
