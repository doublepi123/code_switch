package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCodexDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{Providers: map[string]StoredProvider{
		"minimax-cn": {APIKey: "sk-secret"},
	}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "codex", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"agent: codex",
		"provider: minimax-cn",
		"model: MiniMax-M3",
		"upstream_protocol: anthropic-messages",
		"CODEX_HOME=",
		"auth: command-backed (cs token code-switch-proxy --agent codex)",
		"codex config.toml",
		`model = "MiniMax-M3"`,
		`[model_providers.code-switch-proxy.auth]`,
		`args = ["token", "code-switch-proxy", "--agent", "codex"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q\noutput:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-secret") {
		t.Fatalf("dry-run output must not leak the upstream API key\noutput:\n%s", got)
	}
	if strings.Contains(got, "CODE_SWITCH_PROXY_API_KEY") {
		t.Fatalf("dry-run output should not require CODE_SWITCH_PROXY_API_KEY:\n%s", got)
	}
	if strings.Contains(got, "csproxy-") {
		t.Fatalf("dry-run output must not leak a real csproxy- token:\n%s", got)
	}
}

func TestRunCodexNonDryRunLaunchesAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := AppConfig{Providers: map[string]StoredProvider{
		"openrouter": {APIKey: "sk-secret"},
	}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	oldLookPath := lookPath
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	defer func() { lookPath = oldLookPath }()

	var launched launchInvocation
	oldLaunch := launchCommand
	launchCommand = func(inv launchInvocation) error {
		launched = inv
		return nil
	}
	defer func() { launchCommand = oldLaunch }()

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "codex", "--provider", "openrouter"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if launched.Agent != agentCodex {
		t.Fatalf("launched agent = %q, want codex", launched.Agent)
	}
}

func TestRunClaudeAndOpencodeDryRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := AppConfig{Providers: map[string]StoredProvider{
		"minimax-cn": {APIKey: "sk-claude"},
		"openrouter": {APIKey: "sk-open"},
	}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	cases := []struct {
		name     string
		agent    string
		provider string
		want     []string
	}{
		{"claude", "claude", "minimax-cn", []string{"agent: claude", "provider: minimax-cn", "ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic", "ANTHROPIC_MODEL=MiniMax-M3"}},
		{"opencode", "opencode", "openrouter", []string{"agent: opencode", "provider: openrouter", "OPENAI_BASE_URL=https://openrouter.ai/api/v1", "OPENAI_MODEL=anthropic/claude-sonnet-4.6"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			if err := runWithIO([]string{"run", tc.agent, "--provider", tc.provider, "--dry-run"}, strings.NewReader(""), out); err != nil {
				t.Fatalf("run returned error: %v", err)
			}
			got := out.String()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("dry-run output missing %q\noutput:\n%s", want, got)
				}
			}
			if strings.Contains(got, "sk-") {
				t.Fatalf("dry-run output leaked API key:\n%s", got)
			}
		})
	}
}

func TestRunMissingProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"run", "codex", "--dry-run"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for missing --provider, got nil")
	}
}

func TestRunMissingAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"run", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for missing agent, got nil")
	}
}

func TestRandomProxyTokenShape(t *testing.T) {
	tok, err := randomProxyToken()
	if err != nil {
		t.Fatalf("randomProxyToken returned error: %v", err)
	}
	if !strings.HasPrefix(tok, "csproxy-") {
		t.Fatalf("token %q should start with csproxy-", tok)
	}
	// "csproxy-" (8) + 32 hex chars = 40.
	if len(tok) != len("csproxy-")+32 {
		t.Fatalf("token %q should be %d chars long, got %d", tok, len("csproxy-")+32, len(tok))
	}
	hexPart := tok[len("csproxy-"):]
	for _, r := range hexPart {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("token %q contains non-hex char %q", tok, r)
		}
	}
}

func TestRandomProxyTokenUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 32)
	for i := 0; i < 32; i++ {
		tok, err := randomProxyToken()
		if err != nil {
			t.Fatalf("randomProxyToken returned error on iteration %d: %v", i, err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("randomProxyToken produced duplicate token %q on iteration %d", tok, i)
		}
		seen[tok] = struct{}{}
	}
}

func TestRenderProxyCodexConfigContainsProviderBlock(t *testing.T) {
	got := renderProxyCodexConfig("MiniMax-M3")
	for _, want := range []string{
		`model = "MiniMax-M3"`,
		`model_provider = "code-switch-proxy"`,
		`[model_providers.code-switch-proxy]`,
		`name = "code-switch proxy"`,
		`base_url = "http://127.0.0.1:<port>/v1"`,
		`wire_api = "responses"`,
		`[model_providers.code-switch-proxy.auth]`,
		`command = "cs"`,
		`args = ["token", "code-switch-proxy", "--agent", "codex"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderProxyCodexConfig missing %q\noutput:\n%s", want, got)
		}
	}
}

// TestRunCodexDryRunModelOverride verifies that an explicit --model value is
// reflected in both the dry-run plan and the rendered codex config.
func TestRunCodexDryRunModelOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{Providers: map[string]StoredProvider{
		"minimax-cn": {APIKey: "sk-secret"},
	}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "codex", "--provider", "minimax-cn", "--model", "custom-model", "--dry-run"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()

	for _, want := range []string{
		"model: custom-model",
		`model = "custom-model"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q\noutput:\n%s", want, got)
		}
	}
}

// TestRunUnknownProviderRejected ensures the resolver surfaces an error when
// the user names a provider that does not exist.
func TestRunUnknownProviderRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".code-switch"), 0o755)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"run", "codex", "--provider", "no-such-provider", "--dry-run"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for unknown provider, got nil")
	}
}

// TestRunNoArgs ensures that `cs run` with no arguments returns a usage error
// rather than falling through to flag parsing with an empty agent.
func TestRunNoArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"run"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for `run` with no args, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("expected usage error, got %q", err.Error())
	}
}

// TestRunFlagsBeforeAgentRejected verifies the strict argument shape: flags
// are NOT allowed before the agent positional argument. `cs run --provider X codex`
// must be reported as a usage error rather than silently re-interpreted.
func TestRunFlagsBeforeAgentRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"run", "--provider", "minimax-cn", "codex", "--dry-run"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for flags before agent, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("expected usage error, got %q", err.Error())
	}
}

// TestRunExtraPositionalAfterAgentRejected verifies that an extra positional
// argument after the agent is rejected (e.g. `cs run codex extra --provider ...`).
func TestRunExtraPositionalAfterAgentRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"run", "codex", "extra", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for extra positional after agent, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("expected usage error, got %q", err.Error())
	}
}

// TestRunExtraPositionalAfterFlagsRejected verifies that an extra positional
// argument trailing the flags is rejected (e.g.
// `cs run codex --provider minimax-cn extra --dry-run`).
func TestRunExtraPositionalAfterFlagsRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"run", "codex", "--provider", "minimax-cn", "extra", "--dry-run"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for trailing positional after flags, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("expected usage error, got %q", err.Error())
	}
}

// TestRunCodexUppercaseAgent verifies that an agent name with different casing
// (e.g. "Codex") is accepted, because parseAgentName lowercases its input.
func TestRunCodexUppercaseAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{Providers: map[string]StoredProvider{
		"minimax-cn": {APIKey: "sk-secret"},
	}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "Codex", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run returned error for uppercase agent: %v", err)
	}
	if !strings.Contains(out.String(), "agent: codex") {
		t.Fatalf("expected normalized agent name in output, got:\n%s", out.String())
	}
}

// TestRunCodexWhitespaceAgent verifies that surrounding whitespace in the agent
// argument is trimmed by parseAgentName before validation.
func TestRunCodexWhitespaceAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{Providers: map[string]StoredProvider{
		"minimax-cn": {APIKey: "sk-secret"},
	}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "  codex  ", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run returned error for whitespace agent: %v", err)
	}
	if !strings.Contains(out.String(), "agent: codex") {
		t.Fatalf("expected normalized agent name in output, got:\n%s", out.String())
	}
}

// TestRunVersionRequest ensures `cs run --version` prints the version through
// the global isVersionRequest path rather than being treated as a run error.
func TestRunVersionRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "--version"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run --version returned error: %v", err)
	}
	if !strings.Contains(out.String(), "code-switch") {
		t.Fatalf("expected version output, got %q", out.String())
	}
}

// TestRunUsageIncludesRunCommand ensures the top-level usage text documents the
// `run` subcommand so users can discover it.
func TestRunUsageIncludesRunCommand(t *testing.T) {
	out := &bytes.Buffer{}
	printUsage(out)
	usage := out.String()
	if !strings.Contains(usage, "cs run") {
		t.Fatalf("usage text missing `cs run` line:\n%s", usage)
	}
	if !strings.Contains(usage, "--provider <provider>") {
		t.Fatalf("usage text missing run --provider placeholder:\n%s", usage)
	}
}

// TestRunCompletionIncludesRunCommand ensures all three shell completion scripts
// advertise the `run` subcommand so tab completion can discover it.
func TestRunCompletionIncludesRunCommand(t *testing.T) {
	t.Run("bash", func(t *testing.T) {
		if !strings.Contains(bashCompletionString(), "run") {
			t.Fatal("bash completion missing run command")
		}
	})
	t.Run("zsh", func(t *testing.T) {
		if !strings.Contains(zshCompletionString(), "'run:") {
			t.Fatal("zsh completion missing run command")
		}
	})
	t.Run("fish", func(t *testing.T) {
		if !strings.Contains(fishCompletionString(), "-a 'run'") {
			t.Fatal("fish completion missing run command")
		}
	})
}

// TestRenderProxyCodexConfigSpecialChars verifies that models containing TOML
// special characters (quotes, backslashes, newlines) are rendered using proper
// TOML basic-string quoting so the resulting config is always parseable.
func TestRenderProxyCodexConfigSpecialChars(t *testing.T) {
	cases := []struct {
		name  string
		model string
		want  string
	}{
		{name: "double_quote", model: `model"with"quote`, want: `"model\"with\"quote"`},
		{name: "backslash", model: `back\slash`, want: `"back\\slash"`},
		{name: "newline", model: "line1\nline2", want: `"line1\nline2"`},
		{name: "tab", model: "col\ttab", want: `"col\ttab"`},
		{name: "carriage_return", model: "a\rb", want: `"a\rb"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderProxyCodexConfig(tc.model)
			// The model line is the first rendered line.
			line := strings.SplitN(got, "\n", 2)[0]
			if line != "model = "+tc.want {
				t.Fatalf("model line = %q, want %q\nfull output:\n%s", line, "model = "+tc.want, got)
			}
		})
	}
}

// TestRenderProxyCodexConfigRoundtripsTOML verifies the rendered model value
// survives a TOML basic-string round-trip: the decoded value equals the input.
// This guards against quoting regressions that would break Codex config parsing.
func TestRenderProxyCodexConfigRoundtripsTOML(t *testing.T) {
	cases := []string{
		`MiniMax-M3`,
		`model"with"quote`,
		`back\slash`,
		"line1\nline2",
		`path C:\Users\foo`,
		`mixed\"and\nand\ttabs`,
	}
	for _, model := range cases {
		got := renderProxyCodexConfig(model)
		line := strings.SplitN(got, "\n", 2)[0]
		prefix := "model = "
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("model line %q missing prefix %q", line, prefix)
		}
		quoted := line[len(prefix):]
		// Unquote using TOML basic-string semantics via strconv with Go's %q
		// semantics, which share the \b \t \n \f \r \" \\ \uXXXX escapes that
		// tomlQuoteBasicString emits. This is sufficient for a round-trip check
		// of the subset of escapes used.
		decoded, err := unquoteTOMLBasicString(quoted)
		if err != nil {
			t.Fatalf("model %q: unquote %q failed: %v", model, quoted, err)
		}
		if decoded != model {
			t.Fatalf("model %q did not round-trip: got %q (rendered %q)", model, decoded, quoted)
		}
	}
}

// unquoteTOMLBasicString reverses tomlQuoteBasicString for test assertions.
// It interprets the TOML basic-string escape set (\b \t \n \f \r \" \\ and
// \uXXXX / \UXXXXXXXX) and rejects anything else as an error.
func unquoteTOMLBasicString(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("not a quoted basic string: %q", s)
	}
	var b strings.Builder
	for i := 1; i < len(s)-1; i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s)-1 {
			return "", fmt.Errorf("trailing backslash in %q", s)
		}
		i++
		switch s[i] {
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case 'u':
			if i+4 >= len(s)-1 {
				return "", fmt.Errorf("short \\u escape in %q", s)
			}
			hex := s[i+1 : i+5]
			i += 4
			var v int
			if _, err := fmt.Sscanf(hex, "%X", &v); err != nil {
				return "", fmt.Errorf("bad \\u escape %q: %w", hex, err)
			}
			b.WriteRune(rune(v))
		case 'U':
			if i+8 >= len(s)-1 {
				return "", fmt.Errorf("short \\U escape in %q", s)
			}
			hex := s[i+1 : i+9]
			i += 8
			var v int
			if _, err := fmt.Sscanf(hex, "%X", &v); err != nil {
				return "", fmt.Errorf("bad \\U escape %q: %w", hex, err)
			}
			b.WriteRune(rune(v))
		default:
			return "", fmt.Errorf("unsupported escape \\%c in %q", s[i], s)
		}
	}
	return b.String(), nil
}
