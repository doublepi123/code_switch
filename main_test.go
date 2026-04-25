package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestDefaultVersionIsDev(t *testing.T) {
	if version != "dev" {
		t.Fatalf("version = %q, want %q", version, "dev")
	}
}

func TestUpgradeAssetName(t *testing.T) {
	cases := []struct {
		goos   string
		goarch string
		want   string
	}{
		{goos: "darwin", goarch: "amd64", want: "claude-switch-darwin-amd64.tar.gz"},
		{goos: "darwin", goarch: "arm64", want: "claude-switch-darwin-arm64.tar.gz"},
		{goos: "linux", goarch: "amd64", want: "claude-switch-linux-amd64.tar.gz"},
		{goos: "linux", goarch: "arm64", want: "claude-switch-linux-arm64.tar.gz"},
		{goos: "windows", goarch: "amd64", want: "claude-switch-windows-amd64.zip"},
		{goos: "windows", goarch: "arm64", want: "claude-switch-windows-arm64.zip"},
	}

	for _, tc := range cases {
		got, err := upgradeAssetName(tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("upgradeAssetName(%q, %q) returned error: %v", tc.goos, tc.goarch, err)
		}
		if got != tc.want {
			t.Fatalf("upgradeAssetName(%q, %q) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestReleaseDownloadURL(t *testing.T) {
	if got, want := releaseDownloadURL("https://github.com/", "owner/repo", "", "asset.tar.gz"), "https://github.com/owner/repo/releases/latest/download/asset.tar.gz"; got != want {
		t.Fatalf("latest URL = %q, want %q", got, want)
	}
	if got, want := releaseDownloadURL("https://github.com", "owner/repo", "v1.2.3", "asset.tar.gz"), "https://github.com/owner/repo/releases/download/v1.2.3/asset.tar.gz"; got != want {
		t.Fatalf("tag URL = %q, want %q", got, want)
	}
}

func TestPerformUpgradeDownloadsAndReplacesExecutable(t *testing.T) {
	oldVersion := version
	version = "dev"
	t.Cleanup(func() {
		version = oldVersion
	})

	asset, err := upgradeAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	binaryName := "cs"
	archiveBytes := makeTarGzArchive(t, binaryName, "new-binary")
	if strings.HasSuffix(asset, ".zip") {
		binaryName = "cs.exe"
		archiveBytes = makeZipArchive(t, binaryName, "new-binary")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.Redirect(w, r, "/owner/repo/releases/tag/v9.9.9", http.StatusFound)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/releases/tag/v9.9.9") {
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/"+asset) {
			t.Fatalf("unexpected download path: %s", r.URL.Path)
		}
		_, _ = w.Write(archiveBytes)
	}))
	defer server.Close()

	installPath := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(installPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}

	output := &bytes.Buffer{}
	err = performUpgrade(upgradeOptions{
		repo:        "owner/repo",
		installPath: installPath,
		baseURL:     server.URL,
		client:      server.Client(),
		out:         output,
	})
	if err != nil {
		t.Fatalf("performUpgrade returned error: %v", err)
	}

	data, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if got, want := string(data), "new-binary"; got != want {
		t.Fatalf("installed binary = %q, want %q", got, want)
	}
	if !strings.Contains(output.String(), "upgraded claude-switch to latest release") {
		t.Fatalf("expected success output, got %q", output.String())
	}
}

func TestPerformUpgradeSkipsWhenAlreadyLatest(t *testing.T) {
	oldVersion := version
	version = "v2.0.0"
	t.Cleanup(func() {
		version = oldVersion
	})

	assetRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.Redirect(w, r, "/owner/repo/releases/tag/v2.0.0", http.StatusFound)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/releases/tag/v2.0.0") {
			return
		}
		assetRequested = true
		t.Fatalf("did not expect asset download, got path: %s", r.URL.Path)
	}))
	defer server.Close()

	installPath := filepath.Join(t.TempDir(), "cs")
	if runtime.GOOS == "windows" {
		installPath += ".exe"
	}
	if err := os.WriteFile(installPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write old binary: %v", err)
	}

	output := &bytes.Buffer{}
	err := performUpgrade(upgradeOptions{
		repo:        "owner/repo",
		installPath: installPath,
		baseURL:     server.URL,
		client:      server.Client(),
		out:         output,
	})
	if err != nil {
		t.Fatalf("performUpgrade returned error: %v", err)
	}
	if assetRequested {
		t.Fatalf("asset was downloaded")
	}
	data, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if got, want := string(data), "old-binary"; got != want {
		t.Fatalf("installed binary = %q, want %q", got, want)
	}
	if !strings.Contains(output.String(), "already up to date") {
		t.Fatalf("expected up-to-date output, got %q", output.String())
	}
}

func TestShouldSkipUpgrade(t *testing.T) {
	cases := []struct {
		current string
		target  string
		want    bool
	}{
		{current: "dev", target: "v2.0.0", want: false},
		{current: "v2.0.0", target: "v2.0.0", want: true},
		{current: "v2.0.1", target: "v2.0.0", want: true},
		{current: "v2.0.0", target: "v2.0.1", want: false},
		{current: "v2.0.0-beta.1", target: "v2.0.0", want: false},
		{current: "v2.0.0", target: "v2.0.0-beta.1", want: true},
	}

	for _, tc := range cases {
		if got := shouldSkipUpgrade(tc.current, tc.target); got != tc.want {
			t.Fatalf("shouldSkipUpgrade(%q, %q) = %v, want %v", tc.current, tc.target, got, tc.want)
		}
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

func TestOpenCodeGoIncludesAnthropicMessagesModels(t *testing.T) {
	models := providerPresets["opencode-go"].Models
	for _, want := range []string{"minimax-m2.7", "minimax-m2.5", "deepseek-v4-pro", "deepseek-v4-flash"} {
		if !containsString(models, want) {
			t.Fatalf("opencode-go models missing %q: %v", want, models)
		}
	}
	for _, unsupported := range []string{"glm-5", "kimi-k2.6", "qwen3.6-plus"} {
		if containsString(models, unsupported) {
			t.Fatalf("opencode-go models should not include %q: %v", unsupported, models)
		}
	}
}

func TestApplyPresetOpenCodeGoMiniMaxModel(t *testing.T) {
	root := map[string]any{}
	applyPreset(root, providerPresets["opencode-go"], "sk-opencode", "minimax-m2.5")

	env := root["env"].(map[string]any)
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://opencode.ai/zen/go" {
		t.Fatalf("base url = %v, want %v", got, "https://opencode.ai/zen/go")
	}
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-opencode" {
		t.Fatalf("api key = %v, want %v", got, "sk-opencode")
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("expected auth token to be unset")
	}
	if got := env["ANTHROPIC_MODEL"]; got != "minimax-m2.5" {
		t.Fatalf("model = %v, want %v", got, "minimax-m2.5")
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "minimax-m2.7" {
		t.Fatalf("haiku = %v, want %v", got, "minimax-m2.7")
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "minimax-m2.7" {
		t.Fatalf("sonnet = %v, want %v", got, "minimax-m2.7")
	}
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "minimax-m2.7" {
		t.Fatalf("opus = %v, want %v", got, "minimax-m2.7")
	}
	if _, ok := env["CLAUDE_CODE_SUBAGENT_MODEL"]; ok {
		t.Fatalf("expected subagent model to be unset")
	}
}

func TestApplyPresetOpenCodeGoDeepSeekV4Model(t *testing.T) {
	root := map[string]any{}
	applyPreset(root, providerPresets["opencode-go"], "sk-opencode", "deepseek-v4-pro")

	env := root["env"].(map[string]any)
	if got := env["ANTHROPIC_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("model = %v, want %v", got, "deepseek-v4-pro")
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "deepseek-v4-flash" {
		t.Fatalf("haiku = %v, want %v", got, "deepseek-v4-flash")
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("sonnet = %v, want %v", got, "deepseek-v4-pro")
	}
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("opus = %v, want %v", got, "deepseek-v4-pro")
	}
	if got := env["CLAUDE_CODE_SUBAGENT_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("subagent = %v, want %v", got, "deepseek-v4-pro")
	}
}

func TestResolveSwitchPresetRejectsOpenCodeGoChatCompletionsModel(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}

	_, err := resolveSwitchPreset("opencode-go", cfg, "glm-5")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cannot be used with provider opencode-go") {
		t.Fatalf("unexpected error: %v", err)
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

func TestRunTopLevelResetKeyConfigures(t *testing.T) {
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

	input := strings.NewReader("minimax-cn\n\nsk-top-level\n")
	output := &bytes.Buffer{}

	if err := runWithIO([]string{"--reset-key"}, input, output); err != nil {
		t.Fatalf("runWithIO returned error: %v", err)
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var updated AppConfig
	if err := json.Unmarshal(configBytes, &updated); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := updated.Providers["minimax-cn"].APIKey; got != "sk-top-level" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-top-level")
	}
}

func TestMaskAPIKey(t *testing.T) {
	if got := maskAPIKey(""); got != "not saved" {
		t.Fatalf("maskAPIKey(empty) = %q", got)
	}
	if got := maskAPIKey("abc"); got != "***" {
		t.Fatalf("maskAPIKey(short) = %q", got)
	}
	if got := maskAPIKey("sk-1234567890"); got != "sk-*******890" {
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

func TestUniqueCustomProviderKeyRejectsAliases(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	if got := uniqueCustomProviderKey(cfg, "minimax"); got == "minimax" {
		t.Fatalf("expected minimax alias to be rejected, got %q", got)
	}
	if got := uniqueCustomProviderKey(cfg, "minimax-cn-token"); got == "minimax-cn-token" {
		t.Fatalf("expected minimax-cn-token alias to be rejected, got %q", got)
	}
	if got := uniqueCustomProviderKey(cfg, "my-provider"); got != "my-provider" {
		t.Fatalf("expected my-provider to be accepted, got %q", got)
	}
}

func makeTarGzArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	header := &tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := io.WriteString(tw, content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func makeZipArchive(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writer, err := zw.Create(name)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := io.WriteString(writer, content); err != nil {
		t.Fatalf("write zip content: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
