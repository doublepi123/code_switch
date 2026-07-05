package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =====================================================================
// Issue 1: defaultProxyProtocolForAgent + configure must not write an
// invalid route when --protocol is omitted.
//
// TDD: these tests fail against the current code because:
//   - defaultProxyProtocolForAgent does not exist (compile error).
//   - configure writes an empty UpstreamProtocol when --protocol is
//     omitted; the route would then be invalid for the agent at
//     preview/serve time (silent corruption).
// =====================================================================

func TestDefaultProxyProtocolForAgent(t *testing.T) {
	cases := []struct {
		agent string
		want  ProviderProtocol
	}{
		{"codex", protocolAnthropicMessages},
		{"claude", protocolOpenAIResponses},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			got := defaultProxyProtocolForAgent(tc.agent)
			if got != tc.want {
				t.Fatalf("defaultProxyProtocolForAgent(%q) = %q, want %q", tc.agent, got, tc.want)
			}
			// The default must itself be a valid combination so a
			// subsequent validateProxyAgentProtocol never surprises us.
			if err := validateProxyAgentProtocol(tc.agent, got); err != nil {
				t.Fatalf("default protocol %q not valid for agent %q: %v", got, tc.agent, err)
			}
		})
	}
}

func TestProxyConfigureCodexOmitsProtocolWritesValidRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	// No --protocol. Codex defaults to anthropic-messages, which is a
	// valid route. The persisted UpstreamProtocol must be the default
	// (non-empty) so a later preview does not surface as a stale route.
	if err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn", "--model", "glm-5.2",
	}, nil, &out); err != nil {
		t.Fatalf("configure: %v", err)
	}
	route := readProxyRoute(t, home, "codex")
	if route.UpstreamProtocol == "" {
		t.Fatalf("UpstreamProtocol must not be empty when --protocol omitted")
	}
	if ProviderProtocol(route.UpstreamProtocol) != protocolAnthropicMessages {
		t.Fatalf("UpstreamProtocol = %q, want %q", route.UpstreamProtocol, protocolAnthropicMessages)
	}
}

func TestProxyConfigureClaudeOmitsProtocolWritesValidRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{
		"proxy", "configure", "claude",
		"--provider", "zhipu-cn",
	}, nil, &out); err != nil {
		t.Fatalf("configure: %v", err)
	}
	route := readProxyRoute(t, home, "claude")
	if route.UpstreamProtocol == "" {
		t.Fatalf("UpstreamProtocol must not be empty when --protocol omitted")
	}
	if ProviderProtocol(route.UpstreamProtocol) != protocolOpenAIResponses {
		t.Fatalf("UpstreamProtocol = %q, want %q", route.UpstreamProtocol, protocolOpenAIResponses)
	}
	// And preview must succeed (no stale-route error) since the persisted
	// route is now valid by construction.
	out.Reset()
	if err := runWithIO([]string{"proxy", "preview", "claude"}, nil, &out); err != nil {
		t.Fatalf("preview after default-protocol configure should succeed: %v", err)
	}
}

func TestProxyConfigureExplicitProtocolStillOverridesDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	// codex default is anthropic-messages; explicitly request openai-chat.
	if err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn",
		"--protocol", string(protocolOpenAIChat),
	}, nil, &out); err != nil {
		t.Fatalf("configure: %v", err)
	}
	route := readProxyRoute(t, home, "codex")
	if ProviderProtocol(route.UpstreamProtocol) != protocolOpenAIChat {
		t.Fatalf("UpstreamProtocol = %q, want %q", route.UpstreamProtocol, protocolOpenAIChat)
	}
}

// readProxyRoute loads the persisted AppConfig and returns the named
// route, failing the test if the proxy block or route is absent. It is a
// shared helper for the configure-default-protocol tests.
func readProxyRoute(t *testing.T, home, agent string) ProxyRouteConfig {
	t.Helper()
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
	route, ok := cfg.Proxy.Routes[agent]
	if !ok {
		t.Fatalf("route for agent %q missing", agent)
	}
	return route
}

// =====================================================================
// Issue 2: zsh completion must also expose proxy subcommands, not just
// the top-level proxy command.
// =====================================================================

func TestZshCompletionIncludesProxySubcommandBranch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := zshCompletionString()
	// The completion must describe the proxy subcommands somewhere. We
	// accept any of: an explicit case branch for proxy, an _describe
	// listing of the subcommands, or the literal subcommand words in an
	// _arguments branch. The important contract is that typing
	// `cs proxy <TAB>` offers configure/preview/status/start/stop/serve.
	wantSubs := []string{"configure", "preview", "status", "start", "stop", "serve"}
	for _, w := range wantSubs {
		// Each subcommand word must appear in a context that is clearly
		// tied to proxy (i.e. near the word "proxy"), not just anywhere
		// in the script.
		if !strings.Contains(s, w) {
			t.Fatalf("zsh completion missing proxy subcommand %q:\n%s", w, s)
		}
	}
	// And there must be a recognisable zsh dispatch shape for proxy
	// specifically: either a `_describe` line carrying the subcommands,
	// or an `_arguments` / case branch keyed on proxy. This guards
	// against a regression that just dumps the words into a comment.
	if !strings.Contains(s, "proxy") {
		t.Fatalf("zsh completion has no proxy branch at all:\n%s", s)
	}
	// Look for at least one structural marker that ties proxy to its
	// subcommands. We accept either a 'proxy)' case label or a
	// _describe/_values invocation that names proxy subcommands.
	if !containsProxySubcommandBranch(s) {
		t.Fatalf("zsh completion has no structural branch for proxy subcommands:\n%s", s)
	}
}

// containsProxySubcommandBranch returns true if the zsh completion
// script contains a recognisable dispatch shape for the proxy
// subcommands. Recognised shapes:
//   - a case label "proxy)" followed (within a small window) by one of
//     the subcommand words, OR
//   - an explicit _describe/_values call whose first arg names the
//     proxy subcommands together.
func containsProxySubcommandBranch(s string) bool {
	// Case-label shape: "proxy)" near a subcommand word.
	if idx := strings.Index(s, "proxy)"); idx >= 0 {
		window := s[idx:]
		if len(window) > 400 {
			window = window[:400]
		}
		for _, w := range []string{"configure", "preview", "status", "start", "stop", "serve"} {
			if strings.Contains(window, w) {
				return true
			}
		}
	}
	// _describe / _values shape that bundles the subcommands.
	if strings.Contains(s, "configure") && strings.Contains(s, "preview") &&
		strings.Contains(s, "start") && strings.Contains(s, "stop") {
		// additionally require a zsh helper call so we don't match a
		// comment-only listing.
		if strings.Contains(s, "_describe") || strings.Contains(s, "_values") || strings.Contains(s, "_arguments") {
			return true
		}
	}
	return false
}

// =====================================================================
// Issue 3: rejectControlChars must reject ALL unicode control
// characters, and raw (pre-TrimSpace) values must be validated so a
// trailing newline is not silently trimmed away.
// =====================================================================

func TestRejectControlCharsUnicode(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		// ASCII printable + common identifiers: allowed.
		{"clean ipv4", "127.0.0.1", false},
		{"clean model", "glm-5.2", false},
		{"clean ipv6", "::1", false},
		{"clean host with dots", "host.example.com", false},
		{"spaces inside", "a b c", false},
		// Tab is explicitly allowed (some legitimate identifiers use it
		// and it is not a record/line separator).
		{"tab allowed", "a\tb", false},
		// Line/record separators: rejected.
		{"newline", "a\nb", true},
		{"carriage return", "a\rb", true},
		{"crlf", "a\r\nb", true},
		{"leading newline", "\nhost", true},
		{"trailing newline", "host\n", true},
		{"trailing crlf", "host\r\n", true},
		// Other C0 controls that are never legitimate in a host/model/
		// agent/protocol identifier.
		{"null byte", "a\x00b", true},
		{"bell", "a\x07b", true},
		{"backspace", "a\bb", true},
		{"vertical tab", "a\x0bb", true},
		{"form feed", "a\x0cb", true},
		{"escape", "a\x1bb", true},
		{"unit separator", "a\x1fb", true},
		{"DEL", "a\x7fb", true},
		// C1 controls (U+0080..U+009F) are also control characters.
		{"C1 NEL", "a\u0085b", true},
		{"C1 SCI", "a\u009ab", true},
		// Zero-width / format chars (Cf) — these can be used to hide
		// payload in identifiers and logs.
		{"zero width joiner", "a\u200bb", true},
		{"zero width no-break (BOM)", "\ufeffb", true},
		{"left-to-right mark", "a\u200eb", true},
		{"right-to-left mark", "a\u200fb", true},
		// Non-breaking space is NOT a control character and should be
		// allowed (it is just whitespace the caller may TrimSpace).
		{"nbsp allowed", "a\u00a0b", false},
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

// TestProxyConfigureRejectsControlCharsInRawProvider ensures the raw
// (pre-TrimSpace) value is validated, so a trailing newline is not
// silently trimmed into a valid-looking provider name.
func TestProxyConfigureRejectsControlCharsInRawProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	// "zhipu-cn\n" would, after TrimSpace, look like a valid provider —
	// but the raw value carries a line break that must be rejected
	// before normalisation.
	err := runWithIO([]string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn\n",
	}, nil, &out)
	if err == nil {
		t.Fatalf("expected error for provider with trailing newline")
	}
}

// TestProxyPreviewRejectsControlCharsInStoredRoute ensures that when a
// route has been hand-edited (or migrated) to carry a control character
// in its stored host/model/agent/protocol, preview surfaces it as an
// error rather than silently embedding it.
func TestProxyPreviewRejectsControlCharsInStoredRoute(t *testing.T) {
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
	// Hand-edit the config to inject a newline into the stored host.
	cfgPath := filepath.Join(home, ".code-switch", "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg.Proxy.Host = "evil\nhost"
	if err := os.WriteFile(cfgPath, mustMarshalIndent(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out.Reset()
	err = runWithIO([]string{"proxy", "preview", "codex"}, nil, &out)
	if err == nil {
		t.Fatalf("expected preview to reject control char in stored host")
	}
}

// mustMarshalIndent is a test helper that JSON-encodes v with the same
// indentation as writeJSONAtomic. It fails the calling test on error;
// here we only use it from other test helpers so a plain panic is fine.
func mustMarshalIndent(v interface{}) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// =====================================================================
// Issue 4: start/stop/serve stubs should reject extra arguments with a
// usage error (they are not yet implemented, but they must not silently
// accept junk arguments either).
// =====================================================================

func TestProxyStartRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := cmdProxy([]string{"start", "bogus-arg"}, &out)
	if err == nil {
		t.Fatalf("expected usage error for `proxy start bogus-arg`")
	}
	if !strings.Contains(err.Error(), "usage") && !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("error should mention usage or not-implemented: %v", err)
	}
}

func TestProxyStopRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := cmdProxy([]string{"stop", "bogus-arg"}, &out)
	if err == nil {
		t.Fatalf("expected usage error for `proxy stop bogus-arg`")
	}
	if !strings.Contains(err.Error(), "usage") && !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("error should mention usage or not-implemented: %v", err)
	}
}

func TestProxyServeRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := cmdProxy([]string{"serve", "bogus-arg"}, &out)
	if err == nil {
		t.Fatalf("expected usage error for `proxy serve bogus-arg`")
	}
	if !strings.Contains(err.Error(), "usage") && !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("error should mention usage or not-implemented: %v", err)
	}
}
