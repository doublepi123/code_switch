package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdProxyConfigureWritesRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--protocol", string(protocolAnthropicMessages), "--host", "127.0.0.1", "--port", "0"}, nil, &out)
	if err != nil {
		t.Fatalf("proxy configure error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if cfg.Proxy == nil {
		t.Fatalf("proxy block missing: %s", data)
	}
	route := cfg.Proxy.Routes["codex"]
	if route.Provider != "zhipu-cn" || route.Model != "glm-5.2" || route.Agent != "codex" {
		t.Fatalf("route = %+v", route)
	}
}

func TestCmdProxyPreviewDoesNotLeakProviderKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-secret"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, &out); err != nil {
		t.Fatalf("proxy configure error: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"proxy", "preview", "codex"}, nil, &out); err != nil {
		t.Fatalf("proxy preview error: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "agent: codex") || !strings.Contains(s, "provider: zhipu-cn") || !strings.Contains(s, "model: glm-5.2") {
		t.Fatalf("preview missing expected fields:\n%s", s)
	}
	if strings.Contains(s, "sk-secret") {
		t.Fatalf("preview leaked provider key:\n%s", s)
	}
}

func TestCmdProxyConfigureRejectsUnknownProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "no-such-provider"}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "no-such-provider") && !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("error does not mention provider: %v", err)
	}
}

func TestCmdProxyConfigureRejectsUnknownProtocol(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--protocol", "bogus-protocol"}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for unknown protocol, got nil")
	}
}

func TestCmdProxyConfigureDefaultProtocolUsesProviderEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "kimi-coding", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "kimi-coding"}, nil, &out); err != nil {
		t.Fatalf("proxy configure error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := cfg.Proxy.Routes["codex"].UpstreamProtocol; got != string(protocolOpenAIChat) {
		t.Fatalf("upstreamProtocol = %q, want %q", got, protocolOpenAIChat)
	}
}

func TestCmdProxyConfigureRejectsProtocolWithoutProviderEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "kimi-coding", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "kimi-coding", "--protocol", string(protocolAnthropicMessages)}, nil, &out)
	if err == nil {
		t.Fatal("expected error for provider/protocol mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "endpoint") && !strings.Contains(err.Error(), "compatible") {
		t.Fatalf("error should mention endpoint/compatibility: %v", err)
	}
}

func TestCmdProxyPreviewMissingRouteErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := runWithIO([]string{"proxy", "preview", "codex"}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for missing route, got nil")
	}
}

func TestCmdProxyStatusCallable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	// status must at least be callable and produce a deterministic
	// (non-panic) result. Either nil or a descriptive not-running output
	// is acceptable here.
	_ = cmdProxyStatus(nil, &out)
}

// TestCmdProxyPreviewIPv6SafeBaseURL verifies that for an IPv6 listen host
// (e.g. "::1"), preview renders the proxy_base_url and codex config.toml
// base_url with a BRACKETED host so the URLs are well-formed. A naive
// fmt.Sprintf("http://%s:%d/v1", host, port) would produce the malformed
// "http://::1:8080/v1". This must use net.JoinHostPort so the host is wrapped
// in square brackets: "http://[::1]:8080/v1".
func TestCmdProxyPreviewIPv6SafeBaseURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	if err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn", "--model", "glm-5.2",
		"--host", "::1", "--port", "18080",
	}, nil, &out); err != nil {
		t.Fatalf("configure error: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"proxy", "preview", "codex"}, nil, &out); err != nil {
		t.Fatalf("preview error: %v", err)
	}
	s := out.String()
	// proxy_base_url template must be IPv6-safe.
	if !strings.Contains(s, "proxy_base_url: http://[::1]:<port>/v1") {
		t.Fatalf("preview proxy_base_url not IPv6-safe:\n%s", s)
	}
	// codex config.toml base_url must embed the bracketed host:port.
	if !strings.Contains(s, "http://[::1]:18080/v1") {
		t.Fatalf("preview codex config base_url not IPv6-safe:\n%s", s)
	}
	// Must NOT contain the malformed unbracketed form.
	if strings.Contains(s, "http://::1:") {
		t.Fatalf("preview produced unbracketed IPv6 URL:\n%s", s)
	}
}
