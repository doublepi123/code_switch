package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdMCPAddListRemoveAndTest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), &AppConfig{Providers: map[string]StoredProvider{}, MCPServers: map[string]MCPServerConfig{}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	buf := &bytes.Buffer{}
	if err := cmdMCP([]string{"add", "alpha", "--transport", "stdio", "--command", "true"}, buf); err != nil {
		t.Fatalf("cmdMCP add returned error: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "added mcp server alpha") {
		t.Fatalf("add output = %q", got)
	}

	buf.Reset()
	if err := cmdMCP([]string{"add", "beta", "--transport", "sse", "--url", "https://example.com/sse"}, buf); err != nil {
		t.Fatalf("cmdMCP sse add returned error: %v", err)
	}

	buf.Reset()
	if err := cmdMCP([]string{"list"}, buf); err != nil {
		t.Fatalf("cmdMCP list returned error: %v", err)
	}
	gotList := buf.String()
	if !strings.Contains(gotList, "alpha") || !strings.Contains(gotList, "beta") {
		t.Fatalf("list output = %q", gotList)
	}
	if idxAlpha := strings.Index(gotList, "alpha"); idxAlpha == -1 {
		t.Fatalf("alpha missing from list output: %q", gotList)
	} else if idxBeta := strings.Index(gotList, "beta"); idxBeta == -1 || idxAlpha > idxBeta {
		t.Fatalf("list output not sorted: %q", gotList)
	}

	buf.Reset()
	if err := cmdMCP([]string{"test", "alpha"}, buf); err != nil {
		t.Fatalf("cmdMCP test returned error: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "mcp server alpha is ok") {
		t.Fatalf("test output = %q", got)
	}

	buf.Reset()
	if err := cmdMCP([]string{"remove", "alpha"}, buf); err != nil {
		t.Fatalf("cmdMCP remove returned error: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "removed mcp server alpha") {
		t.Fatalf("remove output = %q", got)
	}
}

func TestCmdMCPListOutputFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), &AppConfig{Providers: map[string]StoredProvider{}, MCPServers: map[string]MCPServerConfig{}}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := cmdMCP([]string{"add", "alpha", "--transport", "stdio", "--command", "true"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}

	buf := &bytes.Buffer{}
	if err := cmdMCP([]string{"list"}, buf); err != nil {
		t.Fatalf("cmdMCP list returned error: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"NAME", "TRANSPORT", "COMMAND/URL", "DISABLED", "alpha", "stdio", "true", "false"} {
		if !strings.Contains(got, want) {
			t.Fatalf("list output %q missing %q", got, want)
		}
	}
}

func TestPrintUsageIncludesMCP(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	printUsage(buf)
	got := buf.String()
	for _, want := range []string{"cs mcp list", "cs mcp add <name>", "cs mcp remove <name>", "cs mcp test <name>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage output missing %q: %q", want, got)
		}
	}
}

func TestCmdMCPAddUsageMentionsSSE(t *testing.T) {
	t.Parallel()

	err := cmdMCP([]string{"add", "alpha"}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected usage error")
	}
	if got := err.Error(); !strings.Contains(got, "--transport sse --url <url>") || !strings.Contains(got, "--transport stdio --command <cmd>") {
		t.Fatalf("usage error missing transports: %q", got)
	}
}
