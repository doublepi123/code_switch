package main

import (
	"reflect"
	"testing"
)

func TestMCPManagerAddListGetRemove(t *testing.T) {
	t.Parallel()

	cfg := &AppConfig{MCPServers: map[string]MCPServerConfig{}}
	mgr := newMCPManager(cfg)

	server := MCPServerConfig{
		Name:      "  beta  ",
		Transport: "stdio",
		Command:   "node",
		Args:      []string{"server.js"},
	}

	if err := mgr.add(server); err != nil {
		t.Fatalf("add returned error: %v", err)
	}

	if got, ok := mgr.get("beta"); !ok {
		t.Fatalf("get returned ok=false")
	} else if got.Name != "beta" || got.Command != "node" {
		t.Fatalf("get returned %+v", got)
	}

	if len(mgr.list()) != 1 {
		t.Fatalf("list len = %d, want 1", len(mgr.list()))
	}

	if err := mgr.add(MCPServerConfig{Name: "alpha", Transport: "sse", URL: "https://example.com/sse"}); err != nil {
		t.Fatalf("add alpha returned error: %v", err)
	}

	gotNames := []string{mgr.list()[0].Name, mgr.list()[1].Name}
	wantNames := []string{"alpha", "beta"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("list names = %v, want %v", gotNames, wantNames)
	}

	if err := mgr.remove(" beta "); err != nil {
		t.Fatalf("remove returned error: %v", err)
	}
	if _, ok := mgr.get("beta"); ok {
		t.Fatalf("get after remove returned ok=true")
	}
}

func TestMCPManagerAddDuplicateAndRemoveUnknown(t *testing.T) {
	t.Parallel()

	mgr := newMCPManager(&AppConfig{MCPServers: map[string]MCPServerConfig{}})

	if err := mgr.add(MCPServerConfig{Name: "alpha", Transport: "stdio", Command: "node"}); err != nil {
		t.Fatalf("initial add returned error: %v", err)
	}
	if err := mgr.add(MCPServerConfig{Name: " alpha ", Transport: "stdio", Command: "node"}); err == nil {
		t.Fatalf("duplicate add returned nil error")
	}

	if err := mgr.remove("missing"); err == nil {
		t.Fatalf("remove missing returned nil error")
	}
}

func TestMCPManagerTracksManagedMCPNames(t *testing.T) {
	t.Parallel()

	cfg := &AppConfig{MCPServers: map[string]MCPServerConfig{}}
	mgr := newMCPManager(cfg)

	if err := mgr.add(MCPServerConfig{Name: "alpha", Transport: "stdio", Command: "node"}); err != nil {
		t.Fatalf("add alpha returned error: %v", err)
	}
	if err := mgr.add(MCPServerConfig{Name: "beta", Transport: "stdio", Command: "node"}); err != nil {
		t.Fatalf("add beta returned error: %v", err)
	}

	want := []string{"alpha", "beta"}
	if !reflect.DeepEqual(cfg.ManagedMCPNames, want) {
		t.Fatalf("ManagedMCPNames after add = %v, want %v", cfg.ManagedMCPNames, want)
	}

	if err := mgr.add(MCPServerConfig{Name: "alpha", Transport: "stdio", Command: "node"}); err == nil {
		t.Fatalf("duplicate add returned nil error")
	}
	if !reflect.DeepEqual(cfg.ManagedMCPNames, want) {
		t.Fatalf("ManagedMCPNames after duplicate add = %v, want %v", cfg.ManagedMCPNames, want)
	}

	// Removal only deletes from MCPServers; ManagedMCPNames is reconciled later by switch/restore.
	if err := mgr.remove("alpha"); err != nil {
		t.Fatalf("remove alpha returned error: %v", err)
	}
	if !reflect.DeepEqual(cfg.ManagedMCPNames, want) {
		t.Fatalf("ManagedMCPNames after remove = %v, want %v", cfg.ManagedMCPNames, want)
	}
}

func TestNormalizeAppConfigMigratesLegacyManagedMCPNames(t *testing.T) {
	t.Parallel()

	legacyNames := []string{"beta", "alpha"}
	cfg := &AppConfig{
		ManagedMCPNames: legacyNames,
		ManagedMCPNamesByAgent: map[string][]string{
			string(agentCodex): {},
		},
	}

	normalizeAppConfig(cfg)

	if len(cfg.ManagedMCPNames) != 0 {
		t.Fatalf("ManagedMCPNames after migration = %v, want empty", cfg.ManagedMCPNames)
	}
	if got := cfg.ManagedMCPNamesByAgent[string(agentClaude)]; !reflect.DeepEqual(got, legacyNames) {
		t.Fatalf("claude managed names = %v, want %v", got, legacyNames)
	}
	if got := cfg.ManagedMCPNamesByAgent[string(agentCodex)]; len(got) != 0 {
		t.Fatalf("codex managed names = %v, want existing empty list preserved", got)
	}
	if got := cfg.ManagedMCPNamesByAgent[string(agentOpencode)]; !reflect.DeepEqual(got, legacyNames) {
		t.Fatalf("opencode managed names = %v, want %v", got, legacyNames)
	}

	cfg.ManagedMCPNamesByAgent[string(agentClaude)][0] = "changed"
	if got := cfg.ManagedMCPNamesByAgent[string(agentOpencode)]; !reflect.DeepEqual(got, legacyNames) {
		t.Fatalf("opencode managed names after claude mutation = %v, want %v", got, legacyNames)
	}
}

func TestManagedMCPNamesForAgentIsolation(t *testing.T) {
	t.Parallel()

	cfg := &AppConfig{MCPServers: map[string]MCPServerConfig{
		"beta":  {Name: "beta"},
		"alpha": {Name: "alpha"},
	}}
	setManagedMCPNamesForAgent(cfg, agentClaude)

	cfg.MCPServers = map[string]MCPServerConfig{"codex-only": {Name: "codex-only"}}
	setManagedMCPNamesForAgent(cfg, agentCodex)

	wantClaude := []string{"alpha", "beta"}
	if got := managedMCPNamesForAgent(cfg, agentClaude); !reflect.DeepEqual(got, wantClaude) {
		t.Fatalf("claude managed names = %v, want %v", got, wantClaude)
	}
	wantCodex := []string{"codex-only"}
	if got := managedMCPNamesForAgent(cfg, agentCodex); !reflect.DeepEqual(got, wantCodex) {
		t.Fatalf("codex managed names = %v, want %v", got, wantCodex)
	}

	claudeNames := managedMCPNamesForAgent(cfg, agentClaude)
	claudeNames[0] = "changed"
	if got := managedMCPNamesForAgent(cfg, agentClaude); !reflect.DeepEqual(got, wantClaude) {
		t.Fatalf("claude managed names after returned slice mutation = %v, want %v", got, wantClaude)
	}
	if got := managedMCPNamesForAgent(cfg, agentCodex); !reflect.DeepEqual(got, wantCodex) {
		t.Fatalf("codex managed names after claude mutation = %v, want %v", got, wantCodex)
	}
}
