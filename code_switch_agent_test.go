package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppConfigMigratesClaudeSwitchConfigToCodeSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldPath := filepath.Join(home, ".claude-switch", "config.json")
	oldCfg := AppConfig{
		Providers: map[string]StoredProvider{
			"minimax":      {APIKey: "sk-mini", Model: "MiniMax-M2.7"},
			"ollama-cloud": {APIKey: "ollama-sk", Model: "qwen3-coder:480b"},
		},
	}
	if err := writeJSONAtomic(oldPath, oldCfg); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	cfg, path, err := loadAppConfig()
	if err != nil {
		t.Fatalf("loadAppConfig returned error: %v", err)
	}

	newPath := filepath.Join(home, ".code-switch", "config.json")
	if path != newPath {
		t.Fatalf("config path = %q, want %q", path, newPath)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old config should remain untouched: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new config was not written: %v", err)
	}
	if _, ok := cfg.Providers["minimax"]; ok {
		t.Fatalf("legacy minimax key should be migrated away")
	}
	if got := cfg.Providers["minimax-cn"].APIKey; got != "sk-mini" {
		t.Fatalf("migrated minimax-cn key = %q, want %q", got, "sk-mini")
	}
}

func TestLoadAppConfigPrefersCodeSwitchConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldPath := filepath.Join(home, ".claude-switch", "config.json")
	newPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(oldPath, AppConfig{Providers: map[string]StoredProvider{"openrouter": {APIKey: "old"}}}); err != nil {
		t.Fatalf("write old config: %v", err)
	}
	if err := writeJSONAtomic(newPath, AppConfig{Providers: map[string]StoredProvider{"deepseek": {APIKey: "new"}}}); err != nil {
		t.Fatalf("write new config: %v", err)
	}

	cfg, path, err := loadAppConfig()
	if err != nil {
		t.Fatalf("loadAppConfig returned error: %v", err)
	}
	if path != newPath {
		t.Fatalf("config path = %q, want %q", path, newPath)
	}
	if _, ok := cfg.Providers["openrouter"]; ok {
		t.Fatalf("old config should not be merged when new config exists")
	}
	if got := cfg.Providers["deepseek"].APIKey; got != "new" {
		t.Fatalf("new config key = %q, want %q", got, "new")
	}
}

func TestCodexSwitchWritesResponsesConfigAndStoresAgentKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "ollama-cloud", "--agent", "codex", "--api-key", "ollama-sk", "--model", "qwen3-coder:480b", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{
		`model = "qwen3-coder:480b"`,
		`model_provider = "ollama-cloud"`,
		`approvals_reviewer = "user"`,
		`[model_providers.ollama-cloud]`,
		`name = "Ollama Cloud"`,
		`base_url = "https://ollama.com/v1"`,
		`wire_api = "responses"`,
		`[model_providers.ollama-cloud.auth]`,
		`command = "cs"`,
		`args = ["token", "ollama-cloud", "--agent", "codex"]`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("codex config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, `env_key = "OLLAMA_API_KEY"`) {
		t.Fatalf("codex config should use command auth instead of env_key:\n%s", config)
	}
	if strings.Contains(config, `env_key_instructions`) {
		t.Fatalf("codex config should not include env_key instructions when using command auth:\n%s", config)
	}
	if strings.Contains(config, `codex-auto-review`) || strings.Contains(config, `guardian_subagent`) || strings.Contains(config, `auto_review`) {
		t.Fatalf("codex config should force user approvals reviewer for ollama-cloud:\n%s", config)
	}
	if strings.Contains(config, "ollama-sk") {
		t.Fatalf("codex config must not contain plaintext api key:\n%s", config)
	}
	if strings.Contains(config, `wire_api = "chat"`) {
		t.Fatalf("codex config must not use chat wire api:\n%s", config)
	}

	appBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(appBytes, &cfg); err != nil {
		t.Fatalf("unmarshal app config: %v", err)
	}
	if got := cfg.Agents["codex"].Providers["ollama-cloud"].APIKey; got != "ollama-sk" {
		t.Fatalf("codex stored key = %q, want %q", got, "ollama-sk")
	}
	if _, ok := cfg.Providers["ollama-cloud"]; ok {
		t.Fatalf("codex switch should not write top-level claude provider config")
	}
}

func TestCodexSwitchDeepSeekV4SetsReasoningEffortXhigh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "ollama-cloud", "--agent", "codex", "--api-key", "ollama-sk", "--model", "deepseek-v4-pro", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	config := string(configBytes)
	if !strings.Contains(config, `reasoning_effort = "xhigh"`) {
		t.Fatalf("codex config missing reasoning_effort = xhigh:\n%s", config)
	}
}


func TestCodexSwitchPrintsCommandAuthForSavedKey(t *testing.T) {
	origNoColor := noColor
	noColor = true
	t.Cleanup(func() { noColor = origNoColor })

	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "ollama-cloud", "--agent", "codex", "--api-key", "ollama-sk", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex switch returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, `auth: cs token ollama-cloud --agent codex`) {
		t.Fatalf("codex switch output missing token auth helper:\n%s", out)
	}
	if strings.Contains(out, `eval "$(cs env`) {
		t.Fatalf("codex switch output should not require shell env setup:\n%s", out)
	}
	if strings.Contains(out, "ollama-sk") {
		t.Fatalf("codex switch output must not print plaintext api key:\n%s", out)
	}
}

func TestCodexEnvPrintsSavedAgentKeyExport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{},
		Agents: map[string]AgentConfig{
			"codex": {
				Providers: map[string]StoredProvider{
					"ollama-cloud": {APIKey: "ollama-sk'quoted"},
				},
			},
		},
	}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"env", "ollama-cloud", "--agent", "codex"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex env returned error: %v", err)
	}

	if got, want := output.String(), "# Codex uses command-based auth; set these env vars for shell use:\nexport ANTHROPIC_BASE_URL='https://ollama.com/v1'\nexport ANTHROPIC_MODEL='qwen3-coder:480b'\nexport OLLAMA_API_KEY='ollama-sk'\\''quoted'\n"; got != want {
		t.Fatalf("env output = %q, want %q", got, want)
	}
}

func TestCodexTokenPrintsSavedAgentKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{},
		Agents: map[string]AgentConfig{
			"codex": {
				Providers: map[string]StoredProvider{
					"ollama-cloud": {APIKey: "ollama-sk'quoted"},
				},
			},
		},
	}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"token", "ollama-cloud", "--agent", "codex"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex token returned error: %v", err)
	}

	if got, want := output.String(), "ollama-sk'quoted\n"; got != want {
		t.Fatalf("token output = %q, want %q", got, want)
	}
}

func TestCodexSwitchWritesTopLevelSettingsBeforeExistingSections(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	existing := `approval_policy = "on-request"
approvals_reviewer = "guardian_subagent"

[profiles.work]
model = "gpt-5.5"
model_provider = "openai"
`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("write codex config: %v", err)
	}

	if err := runWithIO([]string{"switch", "ollama-cloud", "--agent", "codex", "--api-key", "ollama-sk", "--model", "qwen3-coder:480b", "--codex-dir", codexDir}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("codex switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	config := string(configBytes)
	provider, model, baseURL, err := parseCodexTopLevel(config)
	if err != nil {
		t.Fatalf("parse codex config: %v", err)
	}
	if provider != "ollama-cloud" || model != "qwen3-coder:480b" || baseURL != "https://ollama.com/v1" {
		t.Fatalf("parsed codex config = provider %q model %q baseURL %q\n%s", provider, model, baseURL, config)
	}
	if strings.Index(config, `model_provider = "ollama-cloud"`) > strings.Index(config, `[profiles.work]`) {
		t.Fatalf("managed top-level provider was written after an existing section:\n%s", config)
	}
	if !strings.Contains(config, `approvals_reviewer = "user"`) {
		t.Fatalf("codex config did not force user approvals reviewer:\n%s", config)
	}
	if strings.Contains(config, `approvals_reviewer = "guardian_subagent"`) {
		t.Fatalf("codex config kept stale guardian approvals reviewer:\n%s", config)
	}
	for _, want := range []string{`approval_policy = "on-request"`, `[profiles.work]`, `model = "gpt-5.5"`, `model_provider = "openai"`} {
		if !strings.Contains(config, want) {
			t.Fatalf("codex config lost %q:\n%s", want, config)
		}
	}
}

func TestCodexSwitchCanReuseMigratedClaudeOllamaCloudKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldCfg := AppConfig{Providers: map[string]StoredProvider{"ollama-cloud": {APIKey: "old-ollama-sk"}}}
	if err := writeJSONAtomic(filepath.Join(home, ".claude-switch", "config.json"), oldCfg); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	if err := runWithIO([]string{"switch", "ollama-cloud", "--agent", "codex", "--codex-dir", filepath.Join(home, ".codex")}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("codex switch returned error: %v", err)
	}
}

func TestCodexConfigureFallbackWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	input := strings.NewReader("ollama-cloud\n\nollama-sk\n")
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"configure", "--agent", "codex", "--codex-dir", codexDir}, input, output); err != nil {
		t.Fatalf("codex configure returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	if !strings.Contains(string(configBytes), `model_provider = "ollama-cloud"`) {
		t.Fatalf("codex config missing provider:\n%s", string(configBytes))
	}

	appBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(appBytes, &cfg); err != nil {
		t.Fatalf("unmarshal app config: %v", err)
	}
	if got := cfg.Agents["codex"].Providers["ollama-cloud"].APIKey; got != "ollama-sk" {
		t.Fatalf("codex stored key = %q, want %q", got, "ollama-sk")
	}
	if !strings.Contains(output.String(), "switched Codex to Ollama Cloud") {
		t.Fatalf("expected codex switch output, got %q", output.String())
	}
}

func TestCodexCurrentReadsConfig(t *testing.T) {
	origNoColor := noColor
	noColor = true
	t.Cleanup(func() { noColor = origNoColor })

	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	config := `model = "qwen3-coder:480b"
model_provider = "ollama-cloud"

[model_providers.ollama-cloud]
base_url = "https://ollama.com/v1"
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write codex config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"current", "--agent", "codex", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex current returned error: %v", err)
	}
	out := output.String()
	for _, want := range []string{"provider: ollama-cloud", "base_url: https://ollama.com/v1", "model: qwen3-coder:480b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("current output missing %q: %q", want, out)
		}
	}
}

func TestCodexRestoreRemovesOnlyManagedSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	initial := `approval_policy = "on-request"
model = "qwen3-coder:480b"
model_provider = "ollama-cloud"
approvals_reviewer = "user"

[model_providers.ollama-cloud]
name = "Ollama Cloud"
base_url = "https://ollama.com/v1"
env_key = "OLLAMA_API_KEY"
wire_api = "responses"

[model_providers.ollama-cloud.auth]
command = "cs"
args = ["token", "ollama-cloud", "--agent", "codex"]

[profiles.work]
model = "gpt-5.5"
model_provider = "openai"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write codex config: %v", err)
	}

	if err := runWithIO([]string{"restore", "--agent", "codex", "--codex-dir", codexDir}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("codex restore returned error: %v", err)
	}

	restoredBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored codex config: %v", err)
	}
	restored := string(restoredBytes)
	for _, unwanted := range []string{`model_provider = "ollama-cloud"`, `model = "qwen3-coder:480b"`, `approvals_reviewer = "user"`, `[model_providers.ollama-cloud]`, `[model_providers.ollama-cloud.auth]`, `wire_api = "responses"`, `command = "cs"`} {
		if strings.Contains(restored, unwanted) {
			t.Fatalf("restored codex config still contains %q:\n%s", unwanted, restored)
		}
	}
	for _, want := range []string{`approval_policy = "on-request"`, `[profiles.work]`, `model = "gpt-5.5"`, `model_provider = "openai"`} {
		if !strings.Contains(restored, want) {
			t.Fatalf("restored codex config lost %q:\n%s", want, restored)
		}
	}
}

func TestClaudeRestoreRemovesManagedEnvOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	root := map[string]any{
		"permissions": map[string]any{"allow": []any{"Bash(go test ./...)"}},
		"env": map[string]any{
			"ANTHROPIC_BASE_URL":   "https://api.deepseek.com/anthropic",
			"ANTHROPIC_MODEL":      "deepseek-v4-pro[1m]",
			"ANTHROPIC_AUTH_TOKEN": "sk-deepseek",
			"UNMANAGED":            "keep",
		},
	}
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if err := runWithIO([]string{"restore", "--agent", "claude", "--claude-dir", claudeDir}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("claude restore returned error: %v", err)
	}

	restoredBytes, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var restored map[string]any
	if err := json.Unmarshal(restoredBytes, &restored); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	env := restored["env"].(map[string]any)
	if got := env["UNMANAGED"]; got != "keep" {
		t.Fatalf("unmanaged env = %v, want keep", got)
	}
	if _, ok := env["ANTHROPIC_BASE_URL"]; ok {
		t.Fatalf("managed env key was not removed")
	}
	if _, ok := restored["permissions"]; !ok {
		t.Fatalf("unrelated settings were not preserved")
	}
}

func TestCodexProviderTestUsesResponsesEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_test"}`))
	}))
	defer server.Close()

	output := &bytes.Buffer{}
	preset := codexOllamaCloudPreset()
	preset.BaseURL = server.URL
	if err := testCodexProviderWithClient(context.Background(), output, preset, "ollama-sk", server.Client()); err != nil {
		t.Fatalf("testCodexProviderWithClient returned error: %v", err)
	}

	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gotAuth != "Bearer ollama-sk" {
		t.Fatalf("authorization = %q, want bearer token", gotAuth)
	}
	if !strings.Contains(gotBody, `"input":"Say hi"`) {
		t.Fatalf("responses request body = %s", gotBody)
	}
}

func TestAgentFlagsAndRenameSurfaces(t *testing.T) {
	if got, want := defaultUpgradeRepo, "doublepi123/code_switch"; got != want {
		t.Fatalf("defaultUpgradeRepo = %q, want %q", got, want)
	}
	if got, want := upgradeAssetName("linux", "amd64"); got != "code-switch-linux-amd64.tar.gz" || want != nil {
		t.Fatalf("upgradeAssetName linux/amd64 = %q, %v", got, want)
	}

	oldVersion := version
	version = "v-test"
	t.Cleanup(func() { version = oldVersion })
	output := &bytes.Buffer{}
	if err := runWithIO([]string{"--version"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("version returned error: %v", err)
	}
	if got, want := output.String(), "code-switch v-test\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestCodexListAndTUIProviderNamesIncludeRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := &bytes.Buffer{}
	if err := cmdList([]string{"--agent", "codex"}, output); err != nil {
		t.Fatalf("cmdList codex returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "ollama-cloud") {
		t.Fatalf("codex list missing ollama-cloud: %q", out)
	}
	if strings.Contains(out, "deepseek") {
		t.Fatalf("codex list should not include Claude-only providers: %q", out)
	}

	names := providerNamesForAgent(agentCodex, &AppConfig{Providers: map[string]StoredProvider{}}, false, true)
	if len(names) != 3 || names[0] != "ollama-cloud" || names[1] != "openrouter" || names[2] != restoreProviderOption {
		t.Fatalf("codex TUI provider names = %v", names)
	}
}

func TestCodexSwitchOpenRouterWritesCorrectTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "openrouter", "--agent", "codex", "--api-key", "or-sk-test", "--model", "anthropic/claude-sonnet-4.6", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex openrouter switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{
		`model = "anthropic/claude-sonnet-4.6"`,
		`model_provider = "OpenRouter"`,
		`approvals_reviewer = "user"`,
		`[model_providers.OpenRouter]`,
		`name = "OpenRouter"`,
		`base_url = "https://openrouter.ai/api/v1"`,
		`wire_api = "responses"`,
		`[model_providers.OpenRouter.auth]`,
		`command = "cs"`,
		`args = ["token", "openrouter", "--agent", "codex"]`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("codex openrouter config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, "or-sk-test") {
		t.Fatalf("codex config must not contain plaintext api key:\n%s", config)
	}
	if strings.Contains(config, `model_provider = "ollama-cloud"`) {
		t.Fatalf("codex config should not contain ollama-cloud:\n%s", config)
	}

	appBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(appBytes, &cfg); err != nil {
		t.Fatalf("unmarshal app config: %v", err)
	}
	if got := cfg.Agents["codex"].Providers["openrouter"].APIKey; got != "or-sk-test" {
		t.Fatalf("codex stored openrouter key = %q, want %q", got, "or-sk-test")
	}
	if _, ok := cfg.Providers["openrouter"]; ok {
		t.Fatalf("codex switch should not write top-level claude provider config")
	}
}

func TestCodexSwitchOpenRouterPrintsCorrectOutput(t *testing.T) {
	origNoColor := noColor
	noColor = true
	t.Cleanup(func() { noColor = origNoColor })

	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "openrouter", "--agent", "codex", "--api-key", "or-sk-test", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex openrouter switch returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, `auth: cs token openrouter --agent codex`) {
		t.Fatalf("codex openrouter switch output missing token auth helper:\n%s", out)
	}
	if strings.Contains(out, "or-sk-test") {
		t.Fatalf("codex switch output must not print plaintext api key:\n%s", out)
	}
}

func TestCodexRestoreRemovesOpenRouterSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	initial := `approval_policy = "on-request"
model = "anthropic/claude-sonnet-4.6"
model_provider = "OpenRouter"
approvals_reviewer = "user"

[model_providers.OpenRouter]
name = "OpenRouter"
base_url = "https://openrouter.ai/api/v1"
wire_api = "responses"

[model_providers.OpenRouter.auth]
command = "cs"
args = ["token", "openrouter", "--agent", "codex"]

[profiles.work]
model = "gpt-5.5"
model_provider = "openai"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write codex config: %v", err)
	}

	if err := runWithIO([]string{"restore", "--agent", "codex", "--codex-dir", codexDir}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("codex restore returned error: %v", err)
	}

	restoredBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored codex config: %v", err)
	}
	restored := string(restoredBytes)
	for _, unwanted := range []string{`model_provider = "OpenRouter"`, `model = "anthropic/claude-sonnet-4.6"`, `approvals_reviewer = "user"`, `[model_providers.OpenRouter]`, `[model_providers.OpenRouter.auth]`, `wire_api = "responses"`, `command = "cs"`} {
		if strings.Contains(restored, unwanted) {
			t.Fatalf("restored codex config still contains %q:\n%s", unwanted, restored)
		}
	}
	for _, want := range []string{`approval_policy = "on-request"`, `[profiles.work]`, `model = "gpt-5.5"`, `model_provider = "openai"`} {
		if !strings.Contains(restored, want) {
			t.Fatalf("restored codex config lost %q:\n%s", want, restored)
		}
	}
}

func TestCodexSwitchRejectsUnsupportedProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := &bytes.Buffer{}
	err := runWithIO([]string{"switch", "deepseek", "--agent", "codex"}, strings.NewReader(""), output)
	if err == nil {
		t.Fatalf("expected error for unsupported provider deepseek on codex")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("expected unsupported provider error, got: %v", err)
	}
}

func TestCodexListIncludesOpenRouter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := &bytes.Buffer{}
	if err := cmdList([]string{"--agent", "codex"}, output); err != nil {
		t.Fatalf("cmdList codex returned error: %v", err)
	}
	out := output.String()
	for _, want := range []string{"ollama-cloud", "openrouter"} {
		if !strings.Contains(out, want) {
			t.Fatalf("codex list missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "deepseek") {
		t.Fatalf("codex list should not include Claude-only providers: %q", out)
	}

	names := providerNamesForAgent(agentCodex, &AppConfig{Providers: map[string]StoredProvider{}}, false, true)
	if len(names) != 3 || names[0] != "ollama-cloud" || names[1] != "openrouter" || names[2] != restoreProviderOption {
		t.Fatalf("codex TUI provider names = %v, want [ollama-cloud openrouter __restore__]", names)
	}
}

func TestCodexEnvOpenRouterPrintsCorrectOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{},
		Agents: map[string]AgentConfig{
			"codex": {
				Providers: map[string]StoredProvider{
					"openrouter": {APIKey: "or-sk-test"},
				},
			},
		},
	}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"env", "openrouter", "--agent", "codex"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex env openrouter returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, "export ANTHROPIC_BASE_URL='https://openrouter.ai/api/v1'") {
		t.Fatalf("env output missing base_url: %s", out)
	}
	if !strings.Contains(out, "export OPENROUTER_API_KEY='or-sk-test'") {
		t.Fatalf("env output missing auth env: %s", out)
	}
}

func TestDiscoverOpenRouterModelsEmptyKey(t *testing.T) {
	models := discoverOpenRouterModels("")
	if models != nil {
		t.Fatalf("expected nil for empty key, got %v", models)
	}
}
