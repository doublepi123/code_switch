package main

import (
	"encoding/json"
	"testing"
)

func assertOpenCodeMCPAlpha(t *testing.T, configBytes []byte) {
	t.Helper()

	mcpServers := parseOpenCodeMCPServers(t, configBytes)
	alpha, ok := mcpServers["alpha"].(map[string]any)
	if !ok {
		t.Fatalf("alpha server missing or not an object: %#v", mcpServers["alpha"])
	}
	command, ok := alpha["command"].([]any)
	if !ok {
		t.Fatalf("command missing or not an array: %#v", alpha["command"])
	}
	if got := command[0]; got != "node" {
		t.Fatalf("command[0] = %v, want node", got)
	}
	if got := alpha["type"]; got != "local" {
		t.Fatalf("type = %v, want local", got)
	}
	if got := alpha["enabled"]; got != true {
		t.Fatalf("enabled = %v, want true", got)
	}
	if _, ok := alpha["environment"].(map[string]any); !ok {
		t.Fatalf("environment missing or not an object: %#v", alpha["environment"])
	}
	if _, ok := mcpServers["beta"]; ok {
		t.Fatalf("SSE server should not be written: %#v", mcpServers)
	}
}

func parseOpenCodeMCPServers(t *testing.T, configBytes []byte) map[string]any {
	t.Helper()

	var root map[string]any
	if err := json.Unmarshal(configBytes, &root); err != nil {
		t.Fatalf("parse OpenCode config: %v", err)
	}
	mcpServers, ok := root["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("mcp missing or not an object: %#v", root["mcp"])
	}
	return mcpServers
}
