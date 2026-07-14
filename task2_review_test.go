package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- Issue 1: help/completion must wire up the `proxy` family ----

func TestPrintUsageIncludesProxySubcommands(t *testing.T) {
	out := &bytes.Buffer{}
	printUsage(out)
	s := out.String()
	for _, want := range []string{
		"cs proxy",
		"proxy configure",
		"proxy start",
		"proxy stop",
		"proxy status",
		"proxy preview",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("printUsage missing %q\noutput:\n%s", want, s)
		}
	}
}

func TestBashCompletionIncludesProxy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := bashCompletionString()
	// top-level proxy command must be completable (appears in the -W word list)
	if !strings.Contains(s, " proxy ") {
		t.Fatalf("bash completion top-level missing proxy:\n%s", s)
	}
	// second-level subcommands for proxy
	if !strings.Contains(s, "configure start stop status stats preview serve") {
		t.Fatalf("bash completion missing proxy subcommands:\n%s", s)
	}
}

func TestZshCompletionIncludesProxy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := zshCompletionString()
	// zsh wraps top-level commands in single quotes with a description
	if !strings.Contains(s, "'proxy:") {
		t.Fatalf("zsh completion top-level missing proxy:\n%s", s)
	}
}

func TestFishCompletionIncludesProxy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := fishCompletionString()
	// fish uses -a 'proxy' for the top-level command
	if !strings.Contains(s, "'proxy'") {
		t.Fatalf("fish completion top-level missing proxy:\n%s", s)
	}
	if !strings.Contains(s, "configure start stop status stats preview serve") {
		t.Fatalf("fish completion missing proxy subcommands:\n%s", s)
	}
}

// ---- Issue 2: proxy configure must not clobber existing Host/Port when
// flags are not explicitly passed ----

func TestProxyConfigurePreservesExistingHostPortWhenFlagsOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// First configure: set explicit host/port and a route.
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn", "--model", "glm-5.2",
		"--host", "0.0.0.0", "--port", "18080",
	}, nil, &out); err != nil {
		t.Fatalf("first proxy configure: %v", err)
	}

	// Second configure: configure a *different* agent without --host/--port.
	// The existing global Host/Port must be preserved, NOT reset to defaults.
	out.Reset()
	if err := runWithIO([]string{
		"proxy", "configure", "claude",
		"--provider", "zhipu-cn",
	}, nil, &out); err != nil {
		t.Fatalf("second proxy configure: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Proxy == nil {
		t.Fatalf("proxy block missing")
	}
	if cfg.Proxy.Host != "0.0.0.0" {
		t.Fatalf("Host = %q, want 0.0.0.0 (preserved)", cfg.Proxy.Host)
	}
	if cfg.Proxy.Port != 18080 {
		t.Fatalf("Port = %d, want 18080 (preserved)", cfg.Proxy.Port)
	}
}

func TestProxyConfigureExplicitHostPortOverwritesExisting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn",
		"--host", "0.0.0.0", "--port", "18080",
	}, nil, &out); err != nil {
		t.Fatalf("first configure: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn",
		"--host", "127.0.0.1", "--port", "9090",
	}, nil, &out); err != nil {
		t.Fatalf("second configure: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Proxy.Host != "127.0.0.1" {
		t.Fatalf("Host = %q, want 127.0.0.1", cfg.Proxy.Host)
	}
	if cfg.Proxy.Port != 9090 {
		t.Fatalf("Port = %d, want 9090", cfg.Proxy.Port)
	}
}

// ---- Issue 3: --port range validation ----

func TestProxyConfigureRejectsPortOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		port string
	}{
		{"negative", "-1"},
		{"too_big", "65536"},
		{"way_too_big", "99999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			var out bytes.Buffer
			if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
				t.Fatalf("set-key: %v", err)
			}
			out.Reset()
			err := runWithIO([]string{
				"proxy", "configure", "codex",
				"--provider", "zhipu-cn",
				"--port", tc.port,
			}, nil, &out)
			if err == nil {
				t.Fatalf("expected error for port %s", tc.port)
			}
			// Config must NOT have been written with the bad port.
			data, rerr := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
			if rerr != nil {
				t.Fatalf("read config: %v", rerr)
			}
			var cfg AppConfig
			if jerr := json.Unmarshal(data, &cfg); jerr != nil {
				t.Fatalf("unmarshal: %v", jerr)
			}
			if cfg.Proxy != nil && cfg.Proxy.Port != 0 {
				t.Fatalf("config should not have been written with bad port; got Port=%d", cfg.Proxy.Port)
			}
		})
	}
}

func TestProxyConfigureAcceptsBoundaryPorts(t *testing.T) {
	for _, port := range []string{"0", "65535"} {
		t.Run("port_"+port, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			var out bytes.Buffer
			if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
				t.Fatalf("set-key: %v", err)
			}
			out.Reset()
			if err := runWithIO([]string{
				"proxy", "configure", "codex",
				"--provider", "zhipu-cn",
				"--port", port,
			}, nil, &out); err != nil {
				t.Fatalf("port %s should be accepted, got error: %v", port, err)
			}
		})
	}
}

// ---- Issue 4: agent/protocol compatibility validation ----

func TestValidateProxyAgentProtocol(t *testing.T) {
	cases := []struct {
		name     string
		agent    string
		protocol ProviderProtocol
		wantErr  bool
	}{
		// codex (Responses client)
		{"codex anthropic ok", "codex", protocolAnthropicMessages, false},
		{"codex openai-chat ok", "codex", protocolOpenAIChat, false},
		{"codex openai-responses ok", "codex", protocolOpenAIResponses, false},
		// claude (Anthropic Messages client)
		{"claude openai-responses ok", "claude", protocolOpenAIResponses, false},
		{"claude openai-chat ok", "claude", protocolOpenAIChat, false},
		{"claude anthropic-messages ok", "claude", protocolAnthropicMessages, false},
		// unknown agent
		{"unknown agent rejected", "unknown-agent", protocolOpenAIChat, true},
		{"empty agent rejected", "", protocolOpenAIChat, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProxyAgentProtocol(tc.agent, tc.protocol)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for agent=%q protocol=%q", tc.agent, tc.protocol)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for agent=%q protocol=%q: %v", tc.agent, tc.protocol, err)
			}
		})
	}
}

func TestProxyConfigureAcceptsCodexSameProtocolPassthrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn",
		"--protocol", string(protocolAnthropicMessages),
	}, nil, &out)
	if err != nil {
		t.Fatalf("proxy configure codex + anthropic-messages: %v\nout=%s", err, out.String())
	}
}

func TestProxyConfigureRejectsUnknownAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{
		"proxy", "configure", "unknown-agent",
		"--provider", "zhipu-cn",
	}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for unknown agent")
	}
}

// ---- Issue 5: control-character injection rejection ----

func TestRejectControlChars(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"clean", "127.0.0.1", false},
		{"clean model", "glm-5.2", false},
		{"newline", "evil\nhost", true},
		{"carriage return", "evil\rhost", true},
		{"tab allowed", "tab\there", false}, // tab is explicitly allowed
		{"newline leading", "\nhost", true},
		{"newline trailing", "host\n", true},
		{"crlf", "evil\r\nhost", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectControlChars("test", tc.value)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for value %q", tc.value)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for value %q: %v", tc.value, err)
			}
		})
	}
}

func TestProxyConfigureRejectsControlCharsInAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{
		"proxy", "configure", "co\ndex",
		"--provider", "zhipu-cn",
	}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for agent with newline")
	}
}

func TestProxyConfigureRejectsControlCharsInHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn",
		"--host", "evil\nhost",
	}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for host with newline")
	}
}

func TestProxyConfigureRejectsControlCharsInModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn",
		"--model", "evil\nmodel",
	}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for model with newline")
	}
}

// ---- Issue 6: renderProxyCodexConfigForBaseURL uses configured host/port ----

func TestRenderProxyCodexConfigForBaseURL(t *testing.T) {
	got := renderProxyCodexConfigForBaseURL("glm-5.2", "http://0.0.0.0:18080/v1")
	for _, want := range []string{
		`model = "glm-5.2"`,
		`base_url = "http://0.0.0.0:18080/v1"`,
		`model_provider = "code-switch-proxy"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\noutput:\n%s", want, got)
		}
	}
	// Default placeholder version still works for backwards compat.
	def := renderProxyCodexConfig("glm-5.2")
	if !strings.Contains(def, "http://127.0.0.1:<port>/v1") {
		t.Fatalf("default render should keep placeholder, got:\n%s", def)
	}
}

func TestProxyPreviewUsesConfiguredHostPortInCodexConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn", "--model", "glm-5.2",
		"--host", "0.0.0.0", "--port", "18080",
	}, nil, &out); err != nil {
		t.Fatalf("configure: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"proxy", "preview", "codex"}, nil, &out); err != nil {
		t.Fatalf("preview: %v", err)
	}
	s := out.String()
	// The codex config block in preview should embed the configured host:port,
	// not the bare <port> placeholder.
	if !strings.Contains(s, "0.0.0.0:18080") {
		t.Fatalf("preview codex config should embed configured host:port:\n%s", s)
	}
	if strings.Contains(s, "127.0.0.1:<port>") {
		t.Fatalf("preview codex config should not contain placeholder:\n%s", s)
	}
}

// ---- Issue 7: dispatcher callable tests for status/start/stop/serve ----

func TestProxyDispatcherStatusCallable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	// Via dispatcher: must route to status, must not panic, and with no state
	// should report not running.
	if err := cmdProxy([]string{"status"}, &out); err != nil {
		t.Fatalf("status returned error: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("status output = %q", out.String())
	}
}

func TestProxyDispatcherStartCallable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := cmdProxy([]string{"start"}, &out)
	if err == nil {
		t.Fatalf("start without configured route should return error")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("start error should mention missing config: %v", err)
	}
}

func TestProxyDispatcherStopCallable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdProxy([]string{"stop"}, &out); err != nil {
		t.Fatalf("stop returned error: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("stop output = %q", out.String())
	}
}

func TestProxyDispatcherServeCallable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := cmdProxy([]string{"serve"}, &out)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("serve without token should mention token, got: %v", err)
	}
}
