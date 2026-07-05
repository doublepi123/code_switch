package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Task 2 final-review blockers — TDD RED tests.
//
// These tests pin down three regressions in the proxy config validation path
// that allowed hand-edited configs to slip control characters past the
// guard, or silently downgraded a claude route's empty protocol to the
// global anthropic default instead of the agent-specific default.
//
// They are written BEFORE the fix and are expected to FAIL (RED) against the
// current code; the implementation in proxy_config.go is then changed to
// make them pass.
// =============================================================================

// ---------------------------------------------------------------------------
// Issue 1: normalizeAppConfig must not TrimSpace-clean control characters in
// proxy.host. After load, preview/build route must ERROR on hosts like
// "\n127.0.0.1" / "127.0.0.1\n" rather than silently trimming them into a
// valid-looking token.
//
// Strategy: normalizeAppConfig must only default a host when it is empty or
// trim-empty; it must NOT trim control characters out of a non-empty host.
// The raw host value must be preserved so buildProxyRouteFromConfig's
// rejectControlChars screen can reject it.
// ---------------------------------------------------------------------------

// TestNormalizeAppConfigDoesNotTrimControlCharsFromHost verifies that a host
// carrying leading/trailing control characters is NOT silently trimmed to a
// clean token. The raw value must be preserved for the downstream control-
// char guard to reject.
func TestNormalizeAppConfigDoesNotTrimControlCharsFromHost(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"leading newline", "\n127.0.0.1"},
		{"trailing newline", "127.0.0.1\n"},
		{"leading crlf", "\r\n127.0.0.1"},
		{"trailing crlf", "127.0.0.1\r\n"},
		{"embedded newline", "evil\nhost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &AppConfig{
				Providers: map[string]StoredProvider{},
				Proxy:     &ProxyConfig{Host: tc.host},
			}
			normalizeAppConfig(cfg)
			if cfg.Proxy.Host != tc.host {
				t.Fatalf("normalizeAppConfig silently trimmed host: got %q, want raw %q (control chars must be preserved for the rejectControlChars guard)", cfg.Proxy.Host, tc.host)
			}
		})
	}
}

// TestNormalizeAppConfigStillDefaultsEmptyHost verifies the fix does NOT
// regress the "fill empty host with 127.0.0.1" behavior: a host that is
// truly empty (or whitespace-only) is still replaced with the default.
func TestNormalizeAppConfigStillDefaultsEmptyHost(t *testing.T) {
	for _, host := range []string{"", "   ", "\t"} {
		cfg := &AppConfig{
			Providers: map[string]StoredProvider{},
			Proxy:     &ProxyConfig{Host: host},
		}
		normalizeAppConfig(cfg)
		if cfg.Proxy.Host != "127.0.0.1" {
			t.Fatalf("host %q should default to 127.0.0.1, got %q", host, cfg.Proxy.Host)
		}
	}
}

// TestLoadThenPreviewRejectsLeadingNewlineHost seeds a hand-edited config on
// disk whose proxy.host carries a LEADING newline ("\n127.0.0.1"). The old
// behavior trimmed that to "127.0.0.1" and the route built silently. The
// new behavior must surface a control-character error at preview time.
//
// This is the end-to-end version of the unit test above: it exercises the
// full load -> normalize -> preview pipeline.
func TestLoadThenPreviewRejectsLeadingNewlineHost(t *testing.T) {
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
	}, nil, &out); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// Hand-edit the host to carry a LEADING newline. TrimSpace would clean
	// this silently; the guard must surface it as an error instead.
	cfgPath := filepath.Join(home, ".code-switch", "config.json")
	if err := seedProxyHost(cfgPath, "\n127.0.0.1"); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	out.Reset()
	err := runWithIO([]string{"proxy", "preview", "codex"}, nil, &out)
	if err == nil {
		t.Fatalf("expected preview to reject host with leading newline, got success:\n%s", out.String())
	}
}

// TestLoadThenPreviewRejectsTrailingNewlineHost mirrors the test above but
// with a TRAILING newline ("127.0.0.1\n"), which is the other TrimSpace-
// cleaned shape called out in the spec.
func TestLoadThenPreviewRejectsTrailingNewlineHost(t *testing.T) {
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
	}, nil, &out); err != nil {
		t.Fatalf("configure: %v", err)
	}

	cfgPath := filepath.Join(home, ".code-switch", "config.json")
	if err := seedProxyHost(cfgPath, "127.0.0.1\n"); err != nil {
		t.Fatalf("seed host: %v", err)
	}

	out.Reset()
	err := runWithIO([]string{"proxy", "preview", "codex"}, nil, &out)
	if err == nil {
		t.Fatalf("expected preview to reject host with trailing newline, got success:\n%s", out.String())
	}
}

// seedProxyHost reads the config at cfgPath, sets proxy.host to host, and
// writes it back. Used to simulate a hand-edited config carrying control
// characters that TrimSpace would otherwise silently clean.
func seedProxyHost(cfgPath, host string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	if cfg.Proxy == nil {
		cfg.Proxy = &ProxyConfig{}
	}
	cfg.Proxy.Host = host
	return writeJSONAtomic(cfgPath, cfg)
}

// ---------------------------------------------------------------------------
// Issue 2: ProxyRouteConfig.Agent must also be screened for control
// characters. A hand-edited route whose stored Agent field carries a control
// char must be rejected, even if the route key itself is clean (the key and
// the field are allowed to differ, but control chars in the field are always
// illegal).
// ---------------------------------------------------------------------------

// TestBuildProxyRouteFromConfigRejectsControlCharInAgentField verifies that
// when the stored ProxyRouteConfig.Agent carries a control character, the
// route build rejects it (even though the route key "codex" is clean).
func TestBuildProxyRouteFromConfigRejectsControlCharInAgentField(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				// Route key is clean ("codex"), but the stored Agent field
				// carries a trailing newline — a hand-edit shape that must
				// be rejected.
				"codex": {
					Agent:            "codex\n",
					Provider:         "zhipu-cn",
					UpstreamProtocol: string(protocolAnthropicMessages),
				},
			},
		},
	}
	_, err := buildProxyRouteFromConfig("codex", cfg, "tok")
	if err == nil {
		t.Fatal("expected error for Agent field carrying a control character, got nil")
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Fatalf("error should mention the agent field, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Issue 3: buildProxyRouteFromConfig must resolve an empty persisted protocol
// for a claude route via defaultProxyProtocolForAgent(agent) (which yields
// openai-responses for claude), NOT the global anthropic-messages default.
// This keeps configure and hand-edited-config behavior identical.
// ---------------------------------------------------------------------------

// TestBuildProxyRouteFromConfigEmptyClaudeProtocolUsesAgentDefault verifies
// that a claude route with an empty UpstreamProtocol resolves to
// protocolOpenAIResponses (defaultProxyProtocolForAgent("claude")), not to
// the global protocolAnthropicMessages default.
//
// This is critical because:
//   - configure persists claude routes with protocolOpenAIResponses by default.
//   - a hand-edited config that omits the field must produce the SAME route.
//   - if it defaulted to anthropic-messages instead, the route would be
//     REJECTED by validateProxyAgentProtocol (claude + anthropic-messages is
//     an illegal combination), surfacing a confusing stale-route error.
func TestBuildProxyRouteFromConfigEmptyClaudeProtocolUsesAgentDefault(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"claude": {
					Agent:    "claude",
					Provider: "zhipu-cn",
					// UpstreamProtocol intentionally empty: simulates a
					// hand-edited config that omitted the field.
				},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("claude", cfg, "tok")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig(claude, empty protocol): %v", err)
	}
	if route.UpstreamProtocol != protocolOpenAIResponses {
		t.Fatalf("claude empty protocol resolved to %q, want %q (agent-specific default via defaultProxyProtocolForAgent)",
			route.UpstreamProtocol, protocolOpenAIResponses)
	}
}

// TestBuildProxyRouteFromConfigEmptyCodexProtocolUsesAgentDefault mirrors
// the above for codex: an empty protocol must resolve to
// protocolAnthropicMessages (the codex-specific default), which happens to
// match the old global default, but is asserted here so the agent-specific
// resolution is pinned for both supported agents.
func TestBuildProxyRouteFromConfigEmptyCodexProtocolUsesAgentDefault(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:    "codex",
					Provider: "zhipu-cn",
				},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "tok")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig(codex, empty protocol): %v", err)
	}
	if route.UpstreamProtocol != protocolAnthropicMessages {
		t.Fatalf("codex empty protocol resolved to %q, want %q", route.UpstreamProtocol, protocolAnthropicMessages)
	}
}
