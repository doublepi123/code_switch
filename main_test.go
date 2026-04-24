package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	oldVersion := version
	version = "v-test"
	t.Cleanup(func() {
		version = oldVersion
	})

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"--version"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("runWithIO(--version) returned error: %v", err)
	}

	if got, want := output.String(), "claude-switch v-test\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestDefaultVersionMatchesCurrentGitHubTag(t *testing.T) {
	if version != "v0.0.4" {
		t.Fatalf("version = %q, want current GitHub tag %q", version, "v0.0.4")
	}
}

func TestApplyPresetPreservesUnmanagedFields(t *testing.T) {
	root := map[string]any{
		"permissions": map[string]any{
			"allow_file_access": true,
		},
		"env": map[string]any{
			"FOO":            "bar",
			"API_TIMEOUT_MS": "1",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": 0,
		},
	}

	applyPreset(root, providerPresets["openrouter"], "sk-test", "")

	env := root["env"].(map[string]any)
	if env["FOO"] != "bar" {
		t.Fatalf("expected unmanaged env to be preserved")
	}
	if _, ok := env["API_TIMEOUT_MS"]; ok {
		t.Fatalf("expected stale managed key to be removed")
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://openrouter.ai/api" {
		t.Fatalf("unexpected base url: %v", got)
	}
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-test" {
		t.Fatalf("unexpected api key: %v", got)
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("expected auth token to be unset")
	}
}

func TestApplyPresetOverrideModel(t *testing.T) {
	root := map[string]any{}
	applyPreset(root, providerPresets["minimax-cn"], "sk-test", "custom-model")

	env := root["env"].(map[string]any)
	for _, key := range []string{
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
	} {
		if got := env[key]; got != "custom-model" {
			t.Fatalf("expected %s to be custom-model, got %v", key, got)
		}
	}
}

func TestApplyPresetOpenRouterOfficialModelKeepsTierMapping(t *testing.T) {
	root := map[string]any{}
	applyPreset(root, providerPresets["openrouter"], "sk-test", "anthropic/claude-opus-4.7")

	env := root["env"].(map[string]any)
	if got := env["ANTHROPIC_MODEL"]; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("model = %v, want %v", got, "anthropic/claude-opus-4.7")
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "anthropic/claude-haiku-4.5" {
		t.Fatalf("haiku model = %v, want %v", got, "anthropic/claude-haiku-4.5")
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("sonnet model = %v, want %v", got, "anthropic/claude-sonnet-4.6")
	}
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("opus model = %v, want %v", got, "anthropic/claude-opus-4.7")
	}
}

func TestApplyPresetOpenRouterCustomModelOverridesAllTiers(t *testing.T) {
	root := map[string]any{}
	applyPreset(root, providerPresets["openrouter"], "sk-test", "openrouter/custom-model")

	env := root["env"].(map[string]any)
	for _, key := range []string{
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
	} {
		if got := env[key]; got != "openrouter/custom-model" {
			t.Fatalf("expected %s to be openrouter/custom-model, got %v", key, got)
		}
	}
}

func TestApplyPresetDeepSeekUsesAuthTokenAndExtraEnv(t *testing.T) {
	root := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY":                         "stale-api-key",
			"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "0",
			"CLAUDE_CODE_EFFORT_LEVEL":                  "low",
		},
	}

	applyPreset(root, providerPresets["deepseek"], "sk-deepseek", "")

	env := root["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("expected api key to be unset")
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-deepseek" {
		t.Fatalf("auth token = %v, want %v", got, "sk-deepseek")
	}
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://api.deepseek.com/anthropic" {
		t.Fatalf("base url = %v, want %v", got, "https://api.deepseek.com/anthropic")
	}
	if got := env["ANTHROPIC_MODEL"]; got != "deepseek-v4-pro[1m]" {
		t.Fatalf("model = %v, want %v", got, "deepseek-v4-pro[1m]")
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "deepseek-v4-flash" {
		t.Fatalf("haiku model = %v, want %v", got, "deepseek-v4-flash")
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("sonnet model = %v, want %v", got, "deepseek-v4-pro")
	}
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("opus model = %v, want %v", got, "deepseek-v4-pro")
	}
	if got := env["CLAUDE_CODE_SUBAGENT_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("subagent model = %v, want %v", got, "deepseek-v4-pro")
	}
	if got := env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"]; got != "1" {
		t.Fatalf("disable traffic = %v, want %v", got, "1")
	}
	if got := env["CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK"]; got != "1" {
		t.Fatalf("disable fallback = %v, want %v", got, "1")
	}
	if got := env["CLAUDE_CODE_EFFORT_LEVEL"]; got != "max" {
		t.Fatalf("effort level = %v, want %v", got, "max")
	}
}

func TestApplyPresetDeepSeekCustomModelOverridesAllModels(t *testing.T) {
	root := map[string]any{}
	applyPreset(root, providerPresets["deepseek"], "sk-deepseek", "deepseek-custom")

	env := root["env"].(map[string]any)
	for _, key := range []string{
		"ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
	} {
		if got := env[key]; got != "deepseek-custom" {
			t.Fatalf("expected %s to be deepseek-custom, got %v", key, got)
		}
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-deepseek" {
		t.Fatalf("auth token = %v, want %v", got, "sk-deepseek")
	}
}

func TestResolveProviderPresetOpenRouterSavedCustomModelOverridesAllTiers(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {Model: "openrouter/custom-model"},
		},
	}

	preset, err := resolveProviderPreset("openrouter", cfg)
	if err != nil {
		t.Fatalf("resolveProviderPreset returned error: %v", err)
	}

	if got := preset.Model; got != "openrouter/custom-model" {
		t.Fatalf("model = %v, want %v", got, "openrouter/custom-model")
	}
	for name, got := range map[string]string{
		"haiku":  preset.Haiku,
		"sonnet": preset.Sonnet,
		"opus":   preset.Opus,
	} {
		if got != "openrouter/custom-model" {
			t.Fatalf("%s model = %v, want %v", name, got, "openrouter/custom-model")
		}
	}
}

func TestResolveProviderPresetOpenRouterSavedOfficialModelKeepsTierMapping(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {Model: "anthropic/claude-opus-4.7"},
		},
	}

	preset, err := resolveProviderPreset("openrouter", cfg)
	if err != nil {
		t.Fatalf("resolveProviderPreset returned error: %v", err)
	}

	if got := preset.Model; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("model = %v, want %v", got, "anthropic/claude-opus-4.7")
	}
	if got := preset.Haiku; got != "anthropic/claude-haiku-4.5" {
		t.Fatalf("haiku model = %v, want %v", got, "anthropic/claude-haiku-4.5")
	}
	if got := preset.Sonnet; got != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("sonnet model = %v, want %v", got, "anthropic/claude-sonnet-4.6")
	}
	if got := preset.Opus; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("opus model = %v, want %v", got, "anthropic/claude-opus-4.7")
	}
}

func TestSwitchProviderOpenRouterOfficialOverrideResetsSavedCustomTierMapping(t *testing.T) {
	claudeDir := t.TempDir()
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {
				APIKey: "sk-existing",
				Model:  "openrouter/custom-model",
			},
		},
	}

	if err := switchProvider("openrouter", cfg, "sk-existing", "anthropic/claude-opus-4.7", claudeDir); err != nil {
		t.Fatalf("switchProvider returned error: %v", err)
	}

	settingsBytes, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	env := settings["env"].(map[string]any)
	if got := env["ANTHROPIC_MODEL"]; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("model = %v, want %v", got, "anthropic/claude-opus-4.7")
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "anthropic/claude-haiku-4.5" {
		t.Fatalf("haiku model = %v, want %v", got, "anthropic/claude-haiku-4.5")
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("sonnet model = %v, want %v", got, "anthropic/claude-sonnet-4.6")
	}
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("opus model = %v, want %v", got, "anthropic/claude-opus-4.7")
	}
}

func TestDetectProvider(t *testing.T) {
	cases := []struct {
		baseURL string
		model   string
		want    string
	}{
		{baseURL: "https://api.minimaxi.com/anthropic", want: "minimax-cn"},
		{baseURL: "https://api.minimax.io/anthropic", want: "minimax-global"},
		{baseURL: "https://openrouter.ai/api", want: "openrouter"},
		{baseURL: "https://api.deepseek.com/anthropic", want: "deepseek"},
		{baseURL: "https://opencode.ai/zen/go", model: "minimax-m2.7", want: "opencode-go"},
		{baseURL: "https://example.com", model: "opencode-go/kimi-k2.5", want: "opencode-go"},
		{baseURL: "https://example.com", want: "custom"},
	}

	for _, tc := range cases {
		if got := detectProvider(tc.baseURL, tc.model); got != tc.want {
			t.Fatalf("detectProvider(%q, %q) = %q, want %q", tc.baseURL, tc.model, got, tc.want)
		}
	}
}

func TestResolveProviderSelection(t *testing.T) {
	names := sortedProviderNames(&AppConfig{Providers: map[string]StoredProvider{}}, true)

	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "1", want: names[0], ok: true},
		{input: " deepseek ", want: "deepseek", ok: true},
		{input: " openrouter ", want: "openrouter", ok: true},
		{input: "minimax-cn-token", want: "minimax-cn", ok: true},
		{input: "99", ok: false},
		{input: "unknown", ok: false},
	}

	for _, tc := range cases {
		got, err := resolveProviderSelection(tc.input, names)
		if tc.ok {
			if err != nil {
				t.Fatalf("resolveProviderSelection(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("resolveProviderSelection(%q) = %q, want %q", tc.input, got, tc.want)
			}
			continue
		}

		if err == nil {
			t.Fatalf("resolveProviderSelection(%q) expected error, got %q", tc.input, got)
		}
	}
}

func TestCanonicalProviderName(t *testing.T) {
	cases := map[string]string{
		"minimax":              "minimax-cn",
		"MiniMax-CN":           "minimax-cn",
		"minimax-cn-token":     "minimax-cn",
		"minimax-global-token": "minimax-global",
		" openrouter ":         "openrouter",
	}

	for input, want := range cases {
		if got := canonicalProviderName(input); got != want {
			t.Fatalf("canonicalProviderName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCmdConfigureSwitchesAndStoresAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeDir := filepath.Join(home, "custom-claude")
	input := strings.NewReader("openrouter\n\nsk-interactive\n")
	output := &bytes.Buffer{}

	if err := cmdConfigure([]string{"--claude-dir", claudeDir}, input, output); err != nil {
		t.Fatalf("cmdConfigure returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(home, ".claude-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var cfg AppConfig
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := cfg.Providers["openrouter"].APIKey; got != "sk-interactive" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-interactive")
	}

	settingsBytes, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	env := settings["env"].(map[string]any)
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://openrouter.ai/api" {
		t.Fatalf("base url = %v, want %v", got, "https://openrouter.ai/api")
	}
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-interactive" {
		t.Fatalf("api key = %v, want %v", got, "sk-interactive")
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("expected auth token to be unset")
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "anthropic/claude-haiku-4.5" {
		t.Fatalf("haiku model = %v, want %v", got, "anthropic/claude-haiku-4.5")
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("sonnet model = %v, want %v", got, "anthropic/claude-sonnet-4.6")
	}
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("opus model = %v, want %v", got, "anthropic/claude-opus-4.7")
	}

	if !strings.Contains(output.String(), "saved provider config for openrouter") {
		t.Fatalf("expected save message in output, got %q", output.String())
	}
}

func TestCmdSwitchAcceptsProviderBeforeFlagsForDeepSeek(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, "claude")

	if err := cmdSwitch([]string{"deepseek", "--api-key", "sk-deepseek", "--claude-dir", claudeDir}); err != nil {
		t.Fatalf("cmdSwitch returned error: %v", err)
	}

	settingsBytes, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	env := settings["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatalf("expected api key to be unset")
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-deepseek" {
		t.Fatalf("auth token = %v, want %v", got, "sk-deepseek")
	}
	if got := env["ANTHROPIC_MODEL"]; got != "deepseek-v4-pro[1m]" {
		t.Fatalf("model = %v, want %v", got, "deepseek-v4-pro[1m]")
	}
}

func TestCmdConfigureReusesExistingAPIKeyWithoutPrompting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"minimax-cn": {APIKey: "sk-existing"},
		},
	}
	configPath := filepath.Join(home, ".claude-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	input := strings.NewReader("minimax-cn\n\n")
	output := &bytes.Buffer{}

	if err := cmdConfigure(nil, input, output); err != nil {
		t.Fatalf("cmdConfigure returned error: %v", err)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var updated AppConfig
	if err := json.Unmarshal(configBytes, &updated); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := updated.Providers["minimax-cn"].APIKey; got != "sk-existing" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-existing")
	}

	if strings.Contains(output.String(), "API key:") {
		t.Fatalf("did not expect api key prompt, got %q", output.String())
	}
	if !strings.Contains(output.String(), "using saved api key for minimax-cn") {
		t.Fatalf("expected saved-key reuse message, got %q", output.String())
	}
}

func TestCmdConfigureResetKeyPromptsForNewValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"minimax-cn": {APIKey: "sk-existing"},
		},
	}
	configPath := filepath.Join(home, ".claude-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	input := strings.NewReader("minimax-cn\n\nsk-new\n")
	output := &bytes.Buffer{}

	if err := cmdConfigure([]string{"--reset-key"}, input, output); err != nil {
		t.Fatalf("cmdConfigure returned error: %v", err)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var updated AppConfig
	if err := json.Unmarshal(configBytes, &updated); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := updated.Providers["minimax-cn"].APIKey; got != "sk-new" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-new")
	}

	if !strings.Contains(output.String(), "API key:") {
		t.Fatalf("expected api key prompt, got %q", output.String())
	}
}

func TestRenderProviderListScreenShowsSavedState(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {APIKey: "sk-existing"},
		},
	}
	output := &bytes.Buffer{}

	renderProviderListScreen(output, sortedProviderNames(cfg, true), cfg, "minimax-cn", 0, "")

	text := stripANSI(output.String())
	if !strings.Contains(text, "minimax-cn") || !strings.Contains(text, "current") {
		t.Fatalf("expected current provider marker, got %q", text)
	}
	if !strings.Contains(text, "openrouter") || !strings.Contains(text, "saved") {
		t.Fatalf("expected saved-key marker, got %q", text)
	}
	if strings.Contains(text, "Models") {
		t.Fatalf("did not expect models page content on list screen, got %q", text)
	}
}

func TestRenderProviderInfoScreenShowsSummaryOnly(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {APIKey: "sk-existing"},
		},
	}
	output := &bytes.Buffer{}

	renderProviderInfoScreen(output, sortedProviderNames(cfg, true), cfg, "minimax-cn", "MiniMax-M2.7", 0, "", false)

	text := stripANSI(output.String())
	if !strings.Contains(text, "Saved Key not saved") {
		t.Fatalf("expected saved key summary, got %q", text)
	}
	if strings.Contains(text, "> MiniMax-M2.7") {
		t.Fatalf("did not expect model list on provider info screen, got %q", text)
	}
}

func TestRenderProviderModelsScreenShowsModelList(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {APIKey: "sk-existing"},
		},
	}
	output := &bytes.Buffer{}

	names := sortedProviderNames(cfg, true)
	renderProviderModelsScreen(output, names, cfg, "minimax-cn", "MiniMax-M2.7", providerIndexForTest(t, names, "minimax-cn"), 0, "", false)

	text := stripANSI(output.String())
	if !strings.Contains(text, "> MiniMax-M2.7") {
		t.Fatalf("expected selected model marker, got %q", text)
	}
}

func TestRenderProviderInfoScreenShowsKeyResetState(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {APIKey: "sk-existing"},
		},
	}
	output := &bytes.Buffer{}

	names := sortedProviderNames(cfg, true)
	renderProviderInfoScreen(output, names, cfg, "openrouter", "anthropic/claude-sonnet-4.6", providerIndexForTest(t, names, "openrouter"), "", true)

	text := stripANSI(output.String())
	if !strings.Contains(text, "Key Action re-enter on apply") {
		t.Fatalf("expected key reset state, got %q", text)
	}
}

func TestMaskAPIKey(t *testing.T) {
	if got := maskAPIKey(""); got != "not saved" {
		t.Fatalf("maskAPIKey(empty) = %q", got)
	}
	if got := maskAPIKey("sk-1234567890"); got != "sk-1*****7890" {
		t.Fatalf("maskAPIKey = %q", got)
	}
}

func TestHasConfigurableKey(t *testing.T) {
	cases := []struct {
		saved    string
		typed    string
		reset    bool
		expected bool
	}{
		{saved: "sk-old", typed: "", reset: false, expected: true},
		{saved: "", typed: "sk-new", reset: false, expected: true},
		{saved: "sk-old", typed: "", reset: true, expected: false},
		{saved: "", typed: "", reset: false, expected: false},
	}

	for _, tc := range cases {
		if got := hasConfigurableKey(tc.saved, tc.typed, tc.reset); got != tc.expected {
			t.Fatalf("hasConfigurableKey(%q, %q, %v) = %v, want %v", tc.saved, tc.typed, tc.reset, got, tc.expected)
		}
	}
}

func stripANSI(text string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return re.ReplaceAllString(text, "")
}

func providerIndexForTest(t *testing.T, names []string, provider string) int {
	t.Helper()
	for i, name := range names {
		if name == provider {
			return i
		}
	}
	t.Fatalf("provider %q not found in %v", provider, names)
	return 0
}
