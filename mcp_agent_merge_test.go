package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexSwitch_preservesExistingMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir Codex dir: %v", err)
	}
	existing := strings.Join([]string{
		`[mcp_servers.existing]`,
		`command = "python"`,
		`args = ["existing.py"]`,
		``,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed Codex config: %v", err)
	}
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
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	assertCodexMCPSections(t, string(configBytes))
}

func TestOpencodeSwitch_preservesExistingMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir OpenCode dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(existingOpenCodeMCPConfig()), 0o644); err != nil {
		t.Fatalf("seed OpenCode config: %v", err)
	}
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
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read OpenCode config: %v", err)
	}
	assertOpenCodeMCPExistingAndAlpha(t, configBytes)
}

func TestCodexRestore_preservesExistingMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir Codex dir: %v", err)
	}
	existing := strings.Join([]string{
		`model = "deepseek-v4-pro"`,
		``,
		`[mcp_servers.existing]`,
		`command = "python"`,
		``,
	}, "\n")
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
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
	assertCodexMCPSections(t, string(configBytes))
}

func TestOpencodeRestore_preservesExistingMCPServers_whenRegistryHasStdioServer(t *testing.T) {
	// Given
	home := t.TempDir()
	opencodeDir := filepath.Join(home, ".config", "opencode")
	configPath := filepath.Join(opencodeDir, "opencode.json")
	if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
		t.Fatalf("mkdir OpenCode dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(existingOpenCodeMCPConfig()), 0o644); err != nil {
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
	assertOpenCodeMCPExistingAndAlpha(t, configBytes)
}

func assertCodexMCPSections(t *testing.T, config string) {
	t.Helper()

	for _, want := range []string{"[mcp_servers.existing]", `command = "python"`, "[mcp_servers.alpha]", `command = "node"`} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex config missing %q:\n%s", want, config)
		}
	}
}

func assertOpenCodeMCPExistingAndAlpha(t *testing.T, configBytes []byte) {
	t.Helper()

	mcpServers := parseOpenCodeMCPServers(t, configBytes)
	if _, ok := mcpServers["existing"]; !ok {
		t.Fatalf("existing server missing: %#v", mcpServers)
	}
	if _, ok := mcpServers["alpha"]; !ok {
		t.Fatalf("alpha server missing: %#v", mcpServers)
	}
}

func existingOpenCodeMCPConfig() string {
	return `{
  "$schema": "https://opencode.ai/config.json",
  "model": "deepseek-v4-pro",
	  "mcp": {
	    "existing": {
	      "type": "local",
	      "command": ["python", "existing.py"],
	      "enabled": true
	    }
	  }
}`
}
