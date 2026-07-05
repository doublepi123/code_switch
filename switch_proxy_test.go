package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteClaudeProxyConfigWritesLoopbackBaseURLAndToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"env":{"KEEP":"yes"}}`), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	if err := writeClaudeProxyConfig(18080, "route-token"); err != nil {
		t.Fatalf("writeClaudeProxyConfig: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("settings json: %v", err)
	}
	env := root["env"].(map[string]any)
	if got := env["ANTHROPIC_BASE_URL"]; got != "http://127.0.0.1:18080" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", got)
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "route-token" {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %q", got)
	}
	if got := env["KEEP"]; got != "yes" {
		t.Fatalf("existing env removed: %q", got)
	}
	backups, err := filepath.Glob(settingsPath + ".bak-*")
	if err != nil || len(backups) == 0 {
		t.Fatalf("backup missing: %v %v", backups, err)
	}
}

func TestWriteCodexProxyConfigWritesProxyProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{Proxy: &ProxyConfig{Routes: map[string]ProxyRouteConfig{
		"codex": {Agent: "codex", Provider: "xiaomimimo-cn", Model: "mimo-v2.5-pro[1m]", Token: "route-token"},
	}}}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	if err := writeCodexProxyConfigInDir("", 18081, "route-token", protocolAnthropicMessages, "mimo-v2.5-pro[1m]", nil, "xiaomimimo-cn"); err != nil {
		t.Fatalf("writeCodexProxyConfig: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	got := string(content)
	for _, want := range []string{
		`model = "mimo-v2.5-pro[1m]"`,
		`model_provider = "code-switch-proxy"`,
		`model_catalog_json = "` + filepath.Join(home, ".codex", "code-switch-model-catalog.json") + `"`,
		`base_url = "http://127.0.0.1:18081/v1"`,
		`wire_api = "responses"`,
		`[model_providers.code-switch-proxy.auth]`,
		`command = "cs"`,
		`args = ["token", "code-switch-proxy", "--agent", "codex"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("codex config missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, `env_key = "CODE_SWITCH_PROXY_API_KEY"`) {
		t.Fatalf("codex proxy config should not require shell env key:\n%s", got)
	}

	catalog, err := os.ReadFile(filepath.Join(home, ".codex", "code-switch-model-catalog.json"))
	if err != nil {
		t.Fatalf("read codex model catalog: %v", err)
	}
	for _, want := range []string{
		`"slug": "mimo-v2.5-pro[1m]"`,
		`"default_reasoning_level": "medium"`,
		`"supported_reasoning_levels"`,
		`"effort": "medium"`,
		`"shell_type": "shell_command"`,
		`"visibility": "list"`,
		`"supported_in_api": true`,
		`"truncation_policy"`,
		`"effective_context_window_percent": 95`,
		`"input_modalities"`,
		`"context_window": 1000000`,
		`"max_context_window": 1000000`,
	} {
		if !strings.Contains(string(catalog), want) {
			t.Fatalf("catalog missing %q\n%s", want, string(catalog))
		}
	}

	output := &bytes.Buffer{}
	if err := cmdToken([]string{"code-switch-proxy", "--agent", "codex"}, output); err != nil {
		t.Fatalf("proxy token command: %v", err)
	}
	if got := strings.TrimSpace(output.String()); got != "route-token" {
		t.Fatalf("proxy token command = %q", got)
	}
}

func TestWriteOpencodeProxyConfigWritesOpenAICompatibleProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := writeOpencodeProxyConfig(18082, "route-token"); err != nil {
		t.Fatalf("writeOpencodeProxyConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("opencode json: %v", err)
	}
	providers := root["provider"].(map[string]any)
	entry := providers["code-switch-proxy"].(map[string]any)
	if got := entry["npm"]; got != "@ai-sdk/openai-compatible" {
		t.Fatalf("npm = %q", got)
	}
	options := entry["options"].(map[string]any)
	if got := options["baseURL"]; got != "http://127.0.0.1:18082/v1" {
		t.Fatalf("baseURL = %q", got)
	}
	if got := options["apiKey"]; got != "route-token" {
		t.Fatalf("apiKey = %q", got)
	}
}

func TestCmdSwitchViaDirectCrossProtocolErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestConfig(t, AppConfig{Providers: map[string]StoredProvider{"zai": {APIKey: "sk-test"}}})

	err := cmdSwitchWithOutput([]string{"zai", "--agent", "codex", "--via", "direct"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("cmdSwitchWithOutput error = nil")
	}
	if !strings.Contains(err.Error(), "跨协议必须通过代理路由") {
		t.Fatalf("error = %v", err)
	}
}

func TestCmdSwitchViaProxyForcesProxyForDirectCapableProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeTestConfig(t, AppConfig{Providers: map[string]StoredProvider{"deepseek": {APIKey: "sk-test"}}})
	restore := stubProxyLifecycle(t, false)
	defer restore()

	var out bytes.Buffer
	if err := cmdSwitchWithOutput([]string{"deepseek", "--agent", "codex", "--via", "proxy"}, &out); err != nil {
		t.Fatalf("cmdSwitchWithOutput: %v", err)
	}
	if !strings.Contains(out.String(), "mode:") || !strings.Contains(out.String(), "proxy") {
		t.Fatalf("output missing proxy mode:\n%s", out.String())
	}
	content, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	if !strings.Contains(string(content), `model_provider = "code-switch-proxy"`) {
		t.Fatalf("codex config not proxied:\n%s", content)
	}
}

func TestEnsureProxyDaemonStartsAndReloadsOnRouteChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := []string{}
	proxyDaemonIsRunning = func(*AppConfig) (bool, bool, error) { return false, false, nil }
	startProxyDaemon = func(*AppConfig) error { calls = append(calls, "start"); return nil }
	stopProxyDaemon = func() error { calls = append(calls, "stop"); return nil }
	t.Cleanup(func() { resetProxyDaemonHooks() })

	if restarted, err := ensureProxyDaemon(&AppConfig{}); err != nil {
		t.Fatalf("ensureProxyDaemon start: %v", err)
	} else if restarted {
		t.Fatalf("ensureProxyDaemon start: expected restarted=false")
	}
	proxyDaemonIsRunning = func(*AppConfig) (bool, bool, error) { return true, true, nil }
	if restarted, err := ensureProxyDaemon(&AppConfig{}); err != nil {
		t.Fatalf("ensureProxyDaemon reload: %v", err)
	} else if !restarted {
		t.Fatalf("ensureProxyDaemon reload: expected restarted=true")
	}
	want := strings.Join([]string{"start", "stop", "start"}, ",")
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
}

func TestCmdCurrentRecognizesClaudeProxyMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestConfig(t, AppConfig{
		Providers: map[string]StoredProvider{"zai": {APIKey: "sk-test"}},
		Proxy: &ProxyConfig{Host: "127.0.0.1", Port: 18083, Routes: map[string]ProxyRouteConfig{
			"claude": {Agent: "claude", Provider: "zai", UpstreamProtocol: string(protocolAnthropicMessages), Token: "route-token"},
		}},
	})
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	settings := map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "http://127.0.0.1:18083", "ANTHROPIC_AUTH_TOKEN": "route-token"}}
	if err := writeJSONAtomic(filepath.Join(home, ".claude", "settings.json"), settings); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := writeProxyRuntimeState(ProxyRuntimeState{PID: os.Getpid(), InstanceID: "inst", Host: "127.0.0.1", Port: 18083, BaseURL: "http://127.0.0.1:18083/v1", StartedAt: time.Now()}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	var out bytes.Buffer
	if err := cmdCurrent([]string{"--agent", "claude"}, &out); err != nil {
		t.Fatalf("cmdCurrent: %v", err)
	}
	for _, want := range []string{"mode:", "proxy", "provider:", "zai", "upstream_protocol:", "anthropic-messages", "daemon:"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("current output missing %q:\n%s", want, out.String())
		}
	}
}

func writeTestConfig(t *testing.T, cfg AppConfig) {
	t.Helper()
	path, err := appConfigPath()
	if err != nil {
		t.Fatalf("appConfigPath: %v", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]StoredProvider{}
	}
	if err := writeJSONAtomic(path, &cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}
}

func stubProxyLifecycle(t *testing.T, running bool) func() {
	t.Helper()
	proxyDaemonIsRunning = func(*AppConfig) (bool, bool, error) { return running, false, nil }
	startProxyDaemon = func(*AppConfig) error {
		return writeProxyRuntimeState(ProxyRuntimeState{PID: os.Getpid(), InstanceID: "inst", Host: "127.0.0.1", Port: 18080, BaseURL: "http://127.0.0.1:18080/v1", StartedAt: time.Now()})
	}
	stopProxyDaemon = func() error { return removeProxyRuntimeState() }
	return func() { resetProxyDaemonHooks() }
}

func TestProxyDisplayBaseURLShowsAutoPlaceholderForPortZero(t *testing.T) {
	for _, tt := range []struct {
		port int
		v1   bool
		want string
	}{
		{0, true, "http://127.0.0.1:<auto>/v1"},
		{0, false, "http://127.0.0.1:<auto>"},
		{18080, true, "http://127.0.0.1:18080/v1"},
		{18080, false, "http://127.0.0.1:18080"},
	} {
		if got := proxyDisplayBaseURL(tt.port, tt.v1); got != tt.want {
			t.Fatalf("proxyDisplayBaseURL(%d, %v) = %q, want %q", tt.port, tt.v1, got, tt.want)
		}
	}
}

func TestProxyRoutesHashDeterministic(t *testing.T) {
	cfg1 := &AppConfig{Proxy: &ProxyConfig{Routes: map[string]ProxyRouteConfig{
		"codex": {Agent: "codex", Provider: "deepseek", Model: "deepseek-v4-pro", UpstreamProtocol: "anthropic-messages"},
	}}}
	cfg2 := &AppConfig{Proxy: &ProxyConfig{Routes: map[string]ProxyRouteConfig{
		"codex": {Agent: "codex", Provider: "deepseek", Model: "deepseek-v4-pro", UpstreamProtocol: "anthropic-messages"},
	}}}
	// Different token should NOT affect the hash (tokens are excluded).
	r := cfg2.Proxy.Routes["codex"]
	r.Token = "different-token"
	cfg2.Proxy.Routes["codex"] = r

	h1 := proxyRoutesHash(cfg1)
	h2 := proxyRoutesHash(cfg2)
	if h1 == "" {
		t.Fatal("proxyRoutesHash returned empty for non-empty config")
	}
	if h1 != h2 {
		t.Fatalf("same routes (different token) should have same hash: %q vs %q", h1, h2)
	}

	// Different model should change the hash.
	cfg3 := &AppConfig{Proxy: &ProxyConfig{Routes: map[string]ProxyRouteConfig{
		"codex": {Agent: "codex", Provider: "deepseek", Model: "deepseek-v4-flash", UpstreamProtocol: "anthropic-messages"},
	}}}
	h3 := proxyRoutesHash(cfg3)
	if h1 == h3 {
		t.Fatal("different model should produce different hash")
	}

	// Empty/nil config should return empty hash.
	if h := proxyRoutesHash(nil); h != "" {
		t.Fatalf("nil config hash should be empty, got %q", h)
	}
}
