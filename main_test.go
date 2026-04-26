package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestRunSubcommandVersionFlag(t *testing.T) {
	oldVersion := version
	version = "v-test"
	t.Cleanup(func() {
		version = oldVersion
	})

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "--version"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("runWithIO(switch --version) returned error: %v", err)
	}

	if got, want := output.String(), "claude-switch v-test\n"; got != want {
		t.Fatalf("version output = %q, want %q", got, want)
	}
}

func TestRunSwitchProviderNamedVersionIsNotVersionRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, "claude")

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"version": {
				Name:    "Version Provider",
				BaseURL: "https://version.example.com/anthropic",
				Model:   "version-model",
				APIKey:  "sk-version",
			},
		},
	}
	if err := writeJSONAtomic(filepath.Join(home, ".claude-switch", "config.json"), cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "version", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("runWithIO(switch version) returned error: %v", err)
	}
	if strings.Contains(output.String(), "claude-switch") {
		t.Fatalf("did not expect version output, got %q", output.String())
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
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://version.example.com/anthropic" {
		t.Fatalf("base url = %v, want %v", got, "https://version.example.com/anthropic")
	}
	if got := env["ANTHROPIC_MODEL"]; got != "version-model" {
		t.Fatalf("model = %v, want %v", got, "version-model")
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
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-opencode" {
		t.Fatalf("api key = %v, want %v", got, "sk-opencode")
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("expected auth token to be unset")
	}
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

	if err := switchProvider("openrouter", cfg, "sk-existing", "anthropic/claude-opus-4.7", claudeDir, io.Discard); err != nil {
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
		{baseURL: "https://proxy.example.com/openrouter.ai/api", want: "custom"},
		{baseURL: "https://evilopenrouter.ai/api", want: "custom"},
		{baseURL: "openrouter.ai/api", want: "openrouter"},
		{baseURL: "https://gateway.openrouter.ai/api", want: "openrouter"},
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

func TestSplitSwitchArgs(t *testing.T) {
	cases := []struct {
		args             []string
		wantProvider     string
		wantFlagArgs     []string
	}{
		{args: []string{"deepseek"}, wantProvider: "deepseek", wantFlagArgs: nil},
		{args: []string{"deepseek", "--api-key", "sk-xxx"}, wantProvider: "deepseek", wantFlagArgs: []string{"--api-key", "sk-xxx"}},
		{args: []string{"deepseek", "--api-key", "sk-xxx", "--model", "v4"}, wantProvider: "deepseek", wantFlagArgs: []string{"--api-key", "sk-xxx", "--model", "v4"}},
		{args: []string{"deepseek", "--api-key=sk-xxx"}, wantProvider: "deepseek", wantFlagArgs: []string{"--api-key=sk-xxx"}},
		{args: []string{"deepseek", "--claude-dir", "/tmp", "--model", "v4"}, wantProvider: "deepseek", wantFlagArgs: []string{"--claude-dir", "/tmp", "--model", "v4"}},
		{args: []string{"openrouter", "--api-key", "sk-or-xxx", "--claude-dir", "/tmp"}, wantProvider: "openrouter", wantFlagArgs: []string{"--api-key", "sk-or-xxx", "--claude-dir", "/tmp"}},
		{args: []string{}, wantProvider: "", wantFlagArgs: nil},
		{args: []string{"--api-key", "sk-xxx"}, wantProvider: "", wantFlagArgs: []string{"--api-key", "sk-xxx"}},
	}

	for _, tc := range cases {
		gotProvider, gotFlagArgs := splitSwitchArgs(tc.args)
		if gotProvider != tc.wantProvider {
			t.Fatalf("splitSwitchArgs(%v) provider = %q, want %q", tc.args, gotProvider, tc.wantProvider)
		}
		if !stringSlicesEqual(gotFlagArgs, tc.wantFlagArgs) {
			t.Fatalf("splitSwitchArgs(%v) flagArgs = %v, want %v", tc.args, gotFlagArgs, tc.wantFlagArgs)
		}
	}
}

func TestMakeCustomProviderKey(t *testing.T) {
	cases := map[string]string{
		"My Provider":           "my-provider",
		"  Test  Provider  ":   "test--provider",
		"Provider/With/Slash":   "provider-with-slash",
		"Provider_With_Underscores": "provider-with-underscores",
		"  --  ":                "custom-provider",
		"":                      "custom-provider",
		"simple":                "simple",
	}
	for input, want := range cases {
		if got := makeCustomProviderKey(input); got != want {
			t.Fatalf("makeCustomProviderKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "b", "c"); got != "b" {
		t.Fatalf("firstNonEmpty = %q, want %q", got, "b")
	}
	if got := firstNonEmpty("a", "b"); got != "a" {
		t.Fatalf("firstNonEmpty = %q, want %q", got, "a")
	}
	if got := firstNonEmpty("", "", "  "); got != "" {
		t.Fatalf("firstNonEmpty = %q, want %q", got, "")
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("firstNonEmpty() = %q, want %q", got, "")
	}
}

func TestNormalizedURLHost(t *testing.T) {
	cases := map[string]string{
		"":                                     "",
		"https://api.minimaxi.com/anthropic":   "api.minimaxi.com",
		"https://api.minimaxi.io/anthropic":    "api.minimaxi.io",
		"api.deepseek.com":                     "api.deepseek.com",
		"https://opencode.ai/zen/go":           "opencode.ai",
		"openrouter.ai/api":                    "openrouter.ai",
		"https://openrouter.ai/":               "openrouter.ai",
		":::invalid:::":                        ":::invalid::",
	}
	for input, want := range cases {
		if got := normalizedURLHost(input); got != want {
			t.Fatalf("normalizedURLHost(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseReleaseVersion(t *testing.T) {
	cases := []struct {
		input   string
		want    releaseVersion
		wantOk  bool
	}{
		{input: "v1.2.3", want: releaseVersion{numbers: []int{1, 2, 3}}, wantOk: true},
		{input: "v2.0.0-beta.1", want: releaseVersion{numbers: []int{2, 0, 0}, preRelease: "beta.1"}, wantOk: true},
		{input: "v1.2.3+build.1", want: releaseVersion{numbers: []int{1, 2, 3}}, wantOk: true},
		{input: "v1.2.3-beta+build", want: releaseVersion{numbers: []int{1, 2, 3}, preRelease: "beta"}, wantOk: true},
		{input: "", want: releaseVersion{}, wantOk: false},
		{input: "v", want: releaseVersion{}, wantOk: false},
		{input: "v1..3", want: releaseVersion{}, wantOk: false},
		{input: "v1.2.3.4", want: releaseVersion{numbers: []int{1, 2, 3, 4}}, wantOk: true},
	}
	for _, tc := range cases {
		got, ok := parseReleaseVersion(tc.input)
		if ok != tc.wantOk {
			t.Fatalf("parseReleaseVersion(%q) ok = %v, want %v", tc.input, ok, tc.wantOk)
		}
		if !ok {
			continue
		}
		if !releaseVersionsEqual(got, tc.want) {
			t.Fatalf("parseReleaseVersion(%q) = %+v, want %+v", tc.input, got, tc.want)
		}
	}
}

func TestCompareReleaseVersions(t *testing.T) {
	cases := []struct {
		current string
		target  string
		want    int
		wantOk  bool
	}{
		{current: "v1.0.0", target: "v2.0.0", want: -1, wantOk: true},
		{current: "v2.0.0", target: "v1.0.0", want: 1, wantOk: true},
		{current: "v1.0.0", target: "v1.0.0", want: 0, wantOk: true},
		{current: "v1.0.1", target: "v1.0.0", want: 1, wantOk: true},
		{current: "v1.10.0", target: "v1.2.0", want: 1, wantOk: true},
		{current: "v1.0.0-beta.1", target: "v1.0.0", want: -1, wantOk: true},
		{current: "v1.0.0", target: "v1.0.0-beta.1", want: 1, wantOk: true},
		{current: "v1.0.0-alpha", target: "v1.0.0-beta", want: -1, wantOk: true},
		{current: "v1.0.0-beta", target: "v1.0.0-beta.1", want: -1, wantOk: true},
		{current: "invalid", target: "v1.0.0", want: 0, wantOk: false},
		{current: "v1.0.0", target: "not-a-version", want: 0, wantOk: false},
	}
	for _, tc := range cases {
		got, ok := compareReleaseVersions(tc.current, tc.target)
		if ok != tc.wantOk {
			t.Fatalf("compareReleaseVersions(%q, %q) ok = %v, want %v", tc.current, tc.target, ok, tc.wantOk)
		}
		if !ok {
			continue
		}
		if got != tc.want {
			t.Fatalf("compareReleaseVersions(%q, %q) = %d, want %d", tc.current, tc.target, got, tc.want)
		}
	}
}

func TestTagFromReleaseURL(t *testing.T) {
	cases := []struct {
		urlStr string
		want   string
	}{
		{urlStr: "https://github.com/owner/repo/releases/tag/v1.2.3", want: "v1.2.3"},
		{urlStr: "https://github.com/owner/repo/releases/tag/v2.0.0-beta.1", want: "v2.0.0-beta.1"},
		{urlStr: "https://github.com/owner/repo/releases/latest", want: ""},
		{urlStr: "/owner/repo/releases/tag/v1.2.3", want: "v1.2.3"},
		{urlStr: "https://github.com/owner/repo/releases/tag/v1.2.3?query=1", want: "v1.2.3"},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.urlStr)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", tc.urlStr, err)
		}
		if got := tagFromReleaseURL(u); got != tc.want {
			t.Fatalf("tagFromReleaseURL(%q) = %q, want %q", tc.urlStr, got, tc.want)
		}
	}
}

func TestMigrateLegacyProviders(t *testing.T) {
	// Legacy "minimax" should migrate to "minimax-cn" when minimax-cn doesn't exist yet
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"minimax": {APIKey: "sk-legacy", Model: "MiniMax-M2"},
		},
	}
	migrateLegacyProviders(cfg)
	if _, ok := cfg.Providers["minimax"]; ok {
		t.Fatal("expected legacy minimax key to be deleted")
	}
	if got := cfg.Providers["minimax-cn"].APIKey; got != "sk-legacy" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-legacy")
	}
	if got := cfg.Providers["minimax-cn"].Model; got != "MiniMax-M2" {
		t.Fatalf("stored model = %q, want %q", got, "MiniMax-M2")
	}
}

func TestMigrateLegacyProvidersWhenBothExist(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"minimax":    {APIKey: "sk-legacy"},
			"minimax-cn": {APIKey: "sk-existing"},
		},
	}
	migrateLegacyProviders(cfg)
	if _, ok := cfg.Providers["minimax"]; ok {
		t.Fatal("expected legacy minimax key to be deleted")
	}
	if got := cfg.Providers["minimax-cn"].APIKey; got != "sk-existing" {
		t.Fatalf("expected existing minimax-cn key to be preserved, got %q", got)
	}
}

func TestMigrateLegacyProvidersEmptyKeySkipped(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"minimax": {APIKey: ""},
		},
	}
	migrateLegacyProviders(cfg)
	if _, ok := cfg.Providers["minimax"]; ok {
		t.Fatal("expected legacy minimax key to be deleted")
	}
	if _, ok := cfg.Providers["minimax-cn"]; ok {
		t.Fatal("expected minimax-cn to not be created for empty key")
	}
}

func TestProviderTitle(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	if got := providerTitle(customProviderOption, cfg); got != "custom..." {
		t.Fatalf("providerTitle(custom) = %q", got)
	}
	if got := providerTitle("deepseek", cfg); got != "deepseek" {
		t.Fatalf("providerTitle(deepseek) = %q", got)
	}
	// Custom provider with stored name
	cfg.Providers["my-custom"] = StoredProvider{Name: "My Custom", BaseURL: "https://example.com/api"}
	if got := providerTitle("my-custom", cfg); got != "My Custom" {
		t.Fatalf("providerTitle(my-custom) = %q", got)
	}
	// Preset provider ignores stored Name
	cfg.Providers["deepseek"] = StoredProvider{Name: "Custom DeepSeek Name"}
	if got := providerTitle("deepseek", cfg); got != "deepseek" {
		t.Fatalf("providerTitle(deepseek) = %q, want deepseek", got)
	}
}

func TestUniqueCustomProviderKeyBoundedLoop(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	cfg.Providers["test"] = StoredProvider{BaseURL: "https://example.com"}
	for i := 2; i <= 10; i++ {
		cfg.Providers[fmt.Sprintf("test-%d", i)] = StoredProvider{BaseURL: "https://example.com"}
	}
	got := uniqueCustomProviderKey(cfg, "test")
	if got != "test-11" {
		t.Fatalf("uniqueCustomProviderKey = %q, want %q", got, "test-11")
	}
}

func TestUniqueCustomProviderKeyWithAliases(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	// All aliases should be rejected
	if got := uniqueCustomProviderKey(cfg, "minimax"); got == "minimax" {
		t.Fatalf("expected alias minimax to be rejected, got %q", got)
	}
	if got := uniqueCustomProviderKey(cfg, "minimax-cn-token"); got == "minimax-cn-token" {
		t.Fatalf("expected alias minimax-cn-token to be rejected, got %q", got)
	}
}

func TestResolveProviderPresetCustomProvider(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-provider": {
				Name:    "My Provider",
				BaseURL: "https://my.example.com/anthropic",
				Model:   "my-model",
				APIKey:  "sk-test",
			},
		},
	}
	preset, err := resolveProviderPreset("my-provider", cfg)
	if err != nil {
		t.Fatalf("resolveProviderPreset returned error: %v", err)
	}
	if got := preset.Name; got != "My Provider" {
		t.Fatalf("name = %q, want %q", got, "My Provider")
	}
	if got := preset.BaseURL; got != "https://my.example.com/anthropic" {
		t.Fatalf("baseURL = %q, want %q", got, "https://my.example.com/anthropic")
	}
	if got := preset.Model; got != "my-model" {
		t.Fatalf("model = %q, want %q", got, "my-model")
	}
	if got := preset.Haiku; got != "my-model" {
		t.Fatalf("haiku = %q, want %q", got, "my-model")
	}
	if got := preset.Sonnet; got != "my-model" {
		t.Fatalf("sonnet = %q, want %q", got, "my-model")
	}
	if got := preset.Opus; got != "my-model" {
		t.Fatalf("opus = %q, want %q", got, "my-model")
	}
}

func TestCmdSetKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Set key for a preset provider
	if err := cmdSetKey([]string{"openrouter", "sk-test-123"}); err != nil {
		t.Fatalf("cmdSetKey returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(home, ".claude-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := cfg.Providers["openrouter"].APIKey; got != "sk-test-123" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-test-123")
	}
}

func TestCmdSetKeyWrongArgs(t *testing.T) {
	if err := cmdSetKey([]string{"openrouter"}); err == nil {
		t.Fatal("expected error for missing api-key arg")
	}
	if err := cmdSetKey([]string{}); err == nil {
		t.Fatal("expected error for empty args")
	}
}

func TestCmdSetKeyUnsupportedProvider(t *testing.T) {
	if err := cmdSetKey([]string{"nonexistent", "sk-test"}); err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestCmdCurrentNoSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")

	output := &bytes.Buffer{}
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := cmdCurrent([]string{"--claude-dir", claudeDir})

	w.Close()
	os.Stdout = oldStdout
	io.ReadAll(r)

	if err != nil {
		t.Fatalf("cmdCurrent returned error: %v", err)
	}
	_ = output
}

func TestCmdCurrentWithSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")

	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
			"ANTHROPIC_MODEL":    "deepseek-v4-pro[1m]",
		},
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := writeJSONAtomic(settingsPath, settings); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// cmdCurrent writes to stdout, capture it
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := cmdCurrent([]string{"--claude-dir", claudeDir})

	w.Close()
	os.Stdout = oldStdout
	outBytes, _ := io.ReadAll(r)

	if err != nil {
		t.Fatalf("cmdCurrent returned error: %v", err)
	}
	out := string(outBytes)
	if !strings.Contains(out, "deepseek") {
		t.Fatalf("expected deepseek in output, got %q", out)
	}
	if !strings.Contains(out, "deepseek-v4-pro[1m]") {
		t.Fatalf("expected model in output, got %q", out)
	}
}

func TestCmdCurrentUnknownProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")

	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL": "https://custom.example.com/anthropic",
			"ANTHROPIC_MODEL":    "custom-model",
		},
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := writeJSONAtomic(settingsPath, settings); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := cmdCurrent([]string{"--claude-dir", claudeDir})

	w.Close()
	os.Stdout = oldStdout
	outBytes, _ := io.ReadAll(r)

	if err != nil {
		t.Fatalf("cmdCurrent returned error: %v", err)
	}
	out := string(outBytes)
	if !strings.Contains(out, "custom") {
		t.Fatalf("expected custom in output, got %q", out)
	}
}

func TestSortedProviderNamesIncludesCustom(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-provider": {BaseURL: "https://example.com/api", Model: "test"},
		},
	}
	names := sortedProviderNames(cfg, false)
	if !containsString(names, "my-provider") {
		t.Fatalf("expected my-provider in names: %v", names)
	}
	// Should not include custom option when not requested
	if containsString(names, customProviderOption) {
		t.Fatalf("unexpected custom option in names: %v", names)
	}
	// Should include custom option when requested
	namesWithCustom := sortedProviderNames(cfg, true)
	if !containsString(namesWithCustom, customProviderOption) {
		t.Fatalf("expected custom option in names: %v", namesWithCustom)
	}
}

func TestSortedProviderNamesSkipsEmptyBaseURL(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"empty-provider": {BaseURL: ""},
			"valid-provider": {BaseURL: "https://example.com/api", Model: "test"},
		},
	}
	names := sortedProviderNames(cfg, false)
	if containsString(names, "empty-provider") {
		t.Fatalf("expected empty-provider to be skipped")
	}
	if !containsString(names, "valid-provider") {
		t.Fatalf("expected valid-provider in names")
	}
}

func TestSwitchProviderCustomProvider(t *testing.T) {
	claudeDir := t.TempDir()
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {
				Name:    "My Custom Provider",
				BaseURL: "https://custom.example.com/anthropic",
				Model:   "custom-model-v2",
				APIKey:  "sk-custom",
			},
		},
	}
	if err := switchProvider("my-custom", cfg, "sk-custom", "", claudeDir, io.Discard); err != nil {
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
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://custom.example.com/anthropic" {
		t.Fatalf("base url = %v, want %v", got, "https://custom.example.com/anthropic")
	}
	if got := env["ANTHROPIC_MODEL"]; got != "custom-model-v2" {
		t.Fatalf("model = %v, want %v", got, "custom-model-v2")
	}
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-custom" {
		t.Fatalf("api key = %v, want %v", got, "sk-custom")
	}
}

func TestSwitchProviderCustomWithModelOverride(t *testing.T) {
	claudeDir := t.TempDir()
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {
				Name:    "My Custom Provider",
				BaseURL: "https://custom.example.com/anthropic",
				Model:   "custom-model-v2",
				APIKey:  "sk-custom",
			},
		},
	}
	if err := switchProvider("my-custom", cfg, "sk-custom", "custom-model-v3", claudeDir, io.Discard); err != nil {
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
	if got := env["ANTHROPIC_MODEL"]; got != "custom-model-v3" {
		t.Fatalf("model = %v, want %v", got, "custom-model-v3")
	}
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "custom-model-v3" {
		t.Fatalf("haiku = %v, want %v", got, "custom-model-v3")
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "custom-model-v3" {
		t.Fatalf("sonnet = %v, want %v", got, "custom-model-v3")
	}
	if got := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; got != "custom-model-v3" {
		t.Fatalf("opus = %v, want %v", got, "custom-model-v3")
	}
}

func TestSwitchProviderSetsSubagentModel(t *testing.T) {
	claudeDir := t.TempDir()

	// deepseek sets subagent model
	if err := switchProvider("deepseek", &AppConfig{Providers: map[string]StoredProvider{}}, "sk-deepseek", "", claudeDir, io.Discard); err != nil {
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
	if got := env["CLAUDE_CODE_SUBAGENT_MODEL"]; got != "deepseek-v4-pro" {
		t.Fatalf("subagent model = %v, want %v", got, "deepseek-v4-pro")
	}
}

func TestSwitchProviderOpenRouterDoesNotSetSubagent(t *testing.T) {
	claudeDir := t.TempDir()

	if err := switchProvider("openrouter", &AppConfig{Providers: map[string]StoredProvider{}}, "sk-or", "", claudeDir, io.Discard); err != nil {
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
	if _, ok := env["CLAUDE_CODE_SUBAGENT_MODEL"]; ok {
		t.Fatal("expected subagent model to not be set for openrouter")
	}
}

func TestCmdSwitchNoAPIKeyError(t *testing.T) {
	if err := cmdSwitch([]string{"openrouter"}); err == nil {
		t.Fatal("expected error for missing api key")
	}
}

func TestCmdSwitchWithAPIKeyFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, "claude")

	if err := cmdSwitch([]string{"openrouter", "--api-key", "sk-flagged", "--claude-dir", claudeDir}); err != nil {
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
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-flagged" {
		t.Fatalf("api key = %v, want %v", got, "sk-flagged")
	}
}

func TestCmdSwitchUsesStoredKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, "claude")

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {APIKey: "sk-stored"},
		},
	}
	configPath := filepath.Join(home, ".claude-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := cmdSwitch([]string{"openrouter", "--claude-dir", claudeDir}); err != nil {
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
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-stored" {
		t.Fatalf("api key = %v, want %v", got, "sk-stored")
	}
}

func TestCmdConfigureRespectsStoredModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"minimax-cn": {APIKey: "sk-existing", Model: "MiniMax-M2.5"},
		},
	}
	configPath := filepath.Join(home, ".claude-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	input := strings.NewReader("minimax-cn\n\n")  // press enter to accept default model
	output := &bytes.Buffer{}

	if err := cmdConfigure(nil, input, output); err != nil {
		t.Fatalf("cmdConfigure returned error: %v", err)
	}

	// The stored model should be used as default in the prompt
	// and written to settings
	settingsBytes, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	env := settings["env"].(map[string]any)
	if got := env["ANTHROPIC_MODEL"]; got != "MiniMax-M2.5" {
		t.Fatalf("model = %v, want %v", got, "MiniMax-M2.5")
	}
}

func TestCmdConfigureCustomProviderFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	input := strings.NewReader("custom\nMyCustom\nhttps://custom.example.com/anthropic\nsk-custom-fallback\ncustom-model\n")
	output := &bytes.Buffer{}

	if err := cmdConfigure(nil, input, output); err != nil {
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

	// Find the custom provider
	var found StoredProvider
	foundOk := false
	for name, stored := range cfg.Providers {
		if strings.TrimSpace(stored.BaseURL) == "https://custom.example.com/anthropic" {
			found = stored
			foundOk = true
			_ = name
			break
		}
	}
	if !foundOk {
		t.Fatal("custom provider not found in config")
	}
	if got := found.Name; got != "MyCustom" {
		t.Fatalf("name = %q, want %q", got, "MyCustom")
	}
	if got := found.APIKey; got != "sk-custom-fallback" {
		t.Fatalf("api key = %q, want %q", got, "sk-custom-fallback")
	}
	if got := found.Model; got != "custom-model" {
		t.Fatalf("model = %q, want %q", got, "custom-model")
	}
}

func TestBackupIfExists(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.json")
	content := []byte(`{"key": "value"}`)

	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := backupIfExists(path); err != nil {
		t.Fatalf("backupIfExists returned error: %v", err)
	}

	// Check backup was created
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	backupCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "test.json.bak.") {
			backupCount++
			backupPath := filepath.Join(tmpDir, entry.Name())
			data, err := os.ReadFile(backupPath)
			if err != nil {
				t.Fatalf("read backup: %v", err)
			}
			if string(data) != string(content) {
				t.Fatalf("backup content = %q, want %q", string(data), string(content))
			}
		}
	}
	if backupCount != 1 {
		t.Fatalf("expected 1 backup file, got %d", backupCount)
	}
}

func TestBackupIfExistsNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.json")

	// Should not return error when file doesn't exist
	if err := backupIfExists(path); err != nil {
		t.Fatalf("backupIfExists on nonexistent file returned error: %v", err)
	}
}

func TestWriteJSONAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "subdir", "test.json")

	value := map[string]any{"hello": "world"}
	if err := writeJSONAtomic(path, value); err != nil {
		t.Fatalf("writeJSONAtomic returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := result["hello"]; got != "world" {
		t.Fatalf("value = %v, want %v", got, "world")
	}
	// Should end with newline
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("expected trailing newline in %q", string(data))
	}
}

func TestResolveSwitchPresetCustomProvider(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {
				Name:    "My Custom",
				BaseURL: "https://custom.example.com/api",
				Model:   "my-model",
				APIKey:  "sk-test",
			},
		},
	}
	preset, err := resolveSwitchPreset("my-custom", cfg, "")
	if err != nil {
		t.Fatalf("resolveSwitchPreset returned error: %v", err)
	}
	if got := preset.Model; got != "my-model" {
		t.Fatalf("model = %q, want %q", got, "my-model")
	}
}

func TestResolveSwitchPresetCustomProviderWithOverride(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {
				Name:    "My Custom",
				BaseURL: "https://custom.example.com/api",
				Model:   "my-model",
				APIKey:  "sk-test",
			},
		},
	}
	preset, err := resolveSwitchPreset("my-custom", cfg, "override-model")
	if err != nil {
		t.Fatalf("resolveSwitchPreset returned error: %v", err)
	}
	if got := preset.Model; got != "override-model" {
		t.Fatalf("model = %q, want %q", got, "override-model")
	}
}

func TestWithSelectedModelEmptyString(t *testing.T) {
	// withSelectedModel with empty model should return preset unchanged
	preset := providerPresets["openrouter"]
	result := withSelectedModel(preset, "")
	if result.Model != preset.Model {
		t.Fatalf("model changed with empty override")
	}
}

func TestWithSelectedModelCustomModel(t *testing.T) {
	preset := providerPresets["minimax-cn"]
	result := withSelectedModel(preset, "brand-new-model")
	if got := result.Model; got != "brand-new-model" {
		t.Fatalf("model = %q, want %q", got, "brand-new-model")
	}
	if got := result.Haiku; got != "brand-new-model" {
		t.Fatalf("haiku = %q, want %q", got, "brand-new-model")
	}
	if got := result.Sonnet; got != "brand-new-model" {
		t.Fatalf("sonnet = %q, want %q", got, "brand-new-model")
	}
	if got := result.Opus; got != "brand-new-model" {
		t.Fatalf("opus = %q, want %q", got, "brand-new-model")
	}
	if got := result.Subagent; got != "brand-new-model" {
		t.Fatalf("subagent = %q, want %q", got, "brand-new-model")
	}
	if got := result.Models[0]; got != "brand-new-model" {
		t.Fatalf("first model = %q, want %q", got, "brand-new-model")
	}
}

func TestRunEmptyArgsConfigures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	input := strings.NewReader("openrouter\n\nsk-empty-args\n")
	output := &bytes.Buffer{}

	if err := runWithIO([]string{}, input, output); err != nil {
		t.Fatalf("runWithIO returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(home, ".claude-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := cfg.Providers["openrouter"].APIKey; got != "sk-empty-args" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-empty-args")
	}
}

func TestRunConfigureSubcommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	input := strings.NewReader("openrouter\n\nsk-configure\n")
	output := &bytes.Buffer{}

	if err := runWithIO([]string{"configure"}, input, output); err != nil {
		t.Fatalf("runWithIO(configure) returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(home, ".claude-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if got := cfg.Providers["openrouter"].APIKey; got != "sk-configure" {
		t.Fatalf("stored api key = %q, want %q", got, "sk-configure")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	if err := runWithIO([]string{"unknown"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestRunSwitchProviderWithModelOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, "claude")

	if err := runWithIO([]string{"switch", "deepseek", "--api-key", "sk-ds", "--model", "deepseek-v4-flash", "--claude-dir", claudeDir}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("runWithIO returned error: %v", err)
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
	if got := env["ANTHROPIC_MODEL"]; got != "deepseek-v4-flash" {
		t.Fatalf("model = %v, want %v", got, "deepseek-v4-flash")
	}
}

func TestRunHelpFlag(t *testing.T) {
	output := &bytes.Buffer{}
	// --help triggers flag help, which returns an error; output goes to stderr
	if err := runWithIO([]string{"--help"}, strings.NewReader(""), output); err == nil {
		t.Fatal("expected error from --help flag")
	}
}

func TestRunHelpSubcommand(t *testing.T) {
	// printUsage() writes to os.Stdout, so capture it
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	runWithIO([]string{"help"}, strings.NewReader(""), &bytes.Buffer{})

	w.Close()
	os.Stdout = oldStdout
	outBytes, _ := io.ReadAll(r)
	out := string(outBytes)
	if !strings.Contains(out, "claude-switch") {
		t.Fatalf("expected help text, got %q", out)
	}
}

func TestIsVersionRequest(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{args: []string{"--version"}, want: true},
		{args: []string{"version"}, want: true},
		{args: []string{"switch", "--version"}, want: true},
		{args: []string{"configure", "--version"}, want: true},
		{args: []string{"deepseek"}, want: false},
		{args: []string{"switch", "openrouter"}, want: false},
		{args: []string{"--api-key", "sk-test"}, want: false},
		{args: []string{}, want: false},
	}
	for _, tc := range cases {
		if got := isVersionRequest(tc.args); got != tc.want {
			t.Fatalf("isVersionRequest(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestCmdList(t *testing.T) {
	output := &bytes.Buffer{}
	if err := cmdList(output); err != nil {
		t.Fatalf("cmdList returned error: %v", err)
	}
	out := output.String()
	for _, want := range []string{"deepseek", "minimax-cn", "openrouter", "opencode-go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("cmdList output missing %q, got %q", want, out)
		}
	}
}

func TestReplaceExecutableIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "bin", "target")
	src := filepath.Join(tmpDir, "src", "binary")
	content := []byte("test-binary-content")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(src, content, 0o755); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := replaceExecutable(src, target); err != nil {
		t.Fatalf("replaceExecutable first call: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("content = %q, want %q", string(data), string(content))
	}

	// Replace with new content
	src2 := filepath.Join(tmpDir, "src2", "binary2")
	content2 := []byte("updated-binary")
	if err := os.MkdirAll(filepath.Dir(src2), 0o755); err != nil {
		t.Fatalf("mkdir src2: %v", err)
	}
	if err := os.WriteFile(src2, content2, 0o755); err != nil {
		t.Fatalf("write src2: %v", err)
	}
	if err := replaceExecutable(src2, target); err != nil {
		t.Fatalf("replaceExecutable second call: %v", err)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target after second call: %v", err)
	}
	if string(data) != string(content2) {
		t.Fatalf("content = %q, want %q", string(data), string(content2))
	}
	// Backup should have been cleaned up
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".old.") {
			t.Fatalf("stale backup found: %s", entry.Name())
		}
	}
}

func TestMoveFileRename(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "a", "src")
	target := filepath.Join(tmpDir, "b", "target")
	content := []byte("move-me")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := moveFile(src, target); err != nil {
		t.Fatalf("moveFile: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("content = %q, want %q", string(data), string(content))
	}
	if _, err := os.Stat(src); err == nil {
		t.Fatal("expected src to be removed")
	}
}

func TestMoveFileCrossDevice(t *testing.T) {
	tmpDir := t.TempDir()
	src := filepath.Join(tmpDir, "src")
	target := filepath.Join(tmpDir, "target")
	content := []byte("cross-device-move")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := moveFile(src, target); err != nil {
		t.Fatalf("moveFile: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(data) != string(content) {
		t.Fatalf("content = %q, want %q", string(data), string(content))
	}
}

func TestDownloadFileURLEncoding(t *testing.T) {
	urlStr := releaseDownloadURL("https://github.com", "owner/repo", "v1.0.0", "name with spaces.tar.gz")
	if !strings.Contains(urlStr, "name%20with%20spaces.tar.gz") {
		t.Fatalf("expected URL-encoded asset name, got %q", urlStr)
	}
}

func TestDownloadFileLatestURL(t *testing.T) {
	urlStr := releaseDownloadURL("https://github.com", "owner/repo", "", "asset.tar.gz")
	if want := "https://github.com/owner/repo/releases/latest/download/asset.tar.gz"; urlStr != want {
		t.Fatalf("latest URL = %q, want %q", urlStr, want)
	}
	urlStr = releaseDownloadURL("https://github.com", "owner/repo", "latest", "asset.tar.gz")
	if want := "https://github.com/owner/repo/releases/latest/download/asset.tar.gz"; urlStr != want {
		t.Fatalf("latest URL = %q, want %q", urlStr, want)
	}
}

func TestSwitchFlagNeedsValue(t *testing.T) {
	if !switchFlagNeedsValue("--api-key") {
		t.Fatal("expected --api-key to need value")
	}
	if !switchFlagNeedsValue("--model") {
		t.Fatal("expected --model to need value")
	}
	if !switchFlagNeedsValue("--claude-dir") {
		t.Fatal("expected --claude-dir to need value")
	}
	if switchFlagNeedsValue("--api-key=sk-test") {
		t.Fatal("expected --api-key=sk-test to not need value")
	}
	if switchFlagNeedsValue("--version") {
		t.Fatal("expected --version to not need value")
	}
}

func TestCmdSwitchInvalidProvider(t *testing.T) {
	if err := cmdSwitch([]string{"nonexistent"}); err == nil {
		t.Fatal("expected error for invalid provider")
	}
}

func TestCmdListWithCustomProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {Name: "My Custom", BaseURL: "https://custom.example.com/api", Model: "my-model"},
		},
	}
	configPath := filepath.Join(home, ".claude-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := cmdList(output); err != nil {
		t.Fatalf("cmdList returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "my-custom") {
		t.Fatalf("cmdList output missing custom provider, got %q", out)
	}
}

func TestApplyPresetClearsOtherAuthEnv(t *testing.T) {
	// deepseek sets ANTHROPIC_AUTH_TOKEN, should clear ANTHROPIC_API_KEY
	root := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_API_KEY": "stale-key",
		},
	}
	applyPreset(root, providerPresets["deepseek"], "sk-ds", "")
	env := root["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Fatal("expected ANTHROPIC_API_KEY to be cleared")
	}
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-ds" {
		t.Fatalf("auth token = %v, want %v", got, "sk-ds")
	}
}

func TestApplyPresetClearsAuthTokenForAPIKeyProviders(t *testing.T) {
	root := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_AUTH_TOKEN": "stale-token",
		},
	}
	applyPreset(root, providerPresets["openrouter"], "sk-or", "")
	env := root["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatal("expected ANTHROPIC_AUTH_TOKEN to be cleared")
	}
	if got := env["ANTHROPIC_API_KEY"]; got != "sk-or" {
		t.Fatalf("api key = %v, want %v", got, "sk-or")
	}
}

func TestApplyPresetExtraEnvOverridesManagedDefaults(t *testing.T) {
	// minimax-cn ExtraEnv overrides API_TIMEOUT_MS and other managed keys
	root := map[string]any{}
	applyPreset(root, providerPresets["minimax-cn"], "sk-test", "")
	env := root["env"].(map[string]any)
	if got := env["API_TIMEOUT_MS"]; got != "3000000" {
		t.Fatalf("API_TIMEOUT_MS = %v, want %v", got, "3000000")
	}
	if got := env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"]; got != "1" {
		t.Fatalf("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = %v, want %v", got, "1")
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, v := range a {
		if v != b[i] {
			return false
		}
	}
	return true
}

func releaseVersionsEqual(a, b releaseVersion) bool {
	if len(a.numbers) != len(b.numbers) {
		return false
	}
	for i, n := range a.numbers {
		if n != b.numbers[i] {
			return false
		}
	}
	return a.preRelease == b.preRelease
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

func TestTestProviderOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("x-api-key") != "sk-test" {
			t.Fatalf("expected x-api-key header, got %q", r.Header.Get("x-api-key"))
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Fatalf("expected /v1/messages path, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hi"}]}`))
	}))
	defer server.Close()

	preset := ProviderPreset{
		Name:    "test-provider",
		BaseURL: server.URL,
		Model:   "test-model",
	}
	output := &bytes.Buffer{}
	if err := testProviderWithClient(output, preset, "sk-test", server.Client()); err != nil {
		t.Fatalf("testProvider returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK in output, got %q", out)
	}
	if !strings.Contains(out, "200") {
		t.Fatalf("expected status 200 in output, got %q", out)
	}
}

func TestTestProviderAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"AuthError","message":"Invalid API key"}}`))
	}))
	defer server.Close()

	preset := ProviderPreset{
		Name:    "test-provider",
		BaseURL: server.URL,
		Model:   "test-model",
	}
	output := &bytes.Buffer{}
	if err := testProviderWithClient(output, preset, "sk-bad", server.Client()); err != nil {
		t.Fatalf("testProvider returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("expected FAIL in output, got %q", out)
	}
	if !strings.Contains(out, "Invalid API key") {
		t.Fatalf("expected error message in output, got %q", out)
	}
}

func TestTestProviderNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	preset := ProviderPreset{
		Name:    "test-provider",
		BaseURL: server.URL,
		Model:   "test-model",
	}
	output := &bytes.Buffer{}
	if err := testProviderWithClient(output, preset, "sk-test", server.Client()); err != nil {
		t.Fatalf("testProvider returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("expected FAIL in output, got %q", out)
	}
	if !strings.Contains(out, "404") {
		t.Fatalf("expected 404 in output, got %q", out)
	}
}

func TestTestProviderServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`internal error`))
	}))
	defer server.Close()

	preset := ProviderPreset{
		Name:    "test-provider",
		BaseURL: server.URL,
		Model:   "test-model",
	}
	output := &bytes.Buffer{}
	if err := testProviderWithClient(output, preset, "sk-test", server.Client()); err != nil {
		t.Fatalf("testProvider returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("expected FAIL in output, got %q", out)
	}
	if !strings.Contains(out, "500") {
		t.Fatalf("expected 500 in output, got %q", out)
	}
}

func TestTestProviderNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	}))
	serverURL := server.URL
	server.Close()

	preset := ProviderPreset{
		Name:    "test-provider",
		BaseURL: serverURL,
		Model:   "test-model",
	}
	output := &bytes.Buffer{}
	if err := testProvider(output, preset, "sk-test"); err != nil {
		t.Fatalf("testProvider returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("expected FAIL in output, got %q", out)
	}
	if !strings.Contains(out, "Request failed") {
		t.Fatalf("expected 'Request failed' in output, got %q", out)
	}
}

func TestTestProviderBearerAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer sk-deepseek" {
			t.Fatalf("expected Authorization: Bearer header, got %q", auth)
		}
		if r.Header.Get("x-api-key") != "" {
			t.Fatalf("expected no x-api-key header for auth token provider")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	preset := ProviderPreset{
		Name:    "deepseek",
		BaseURL: server.URL,
		Model:   "deepseek-v4-pro",
		AuthEnv: "ANTHROPIC_AUTH_TOKEN",
	}
	output := &bytes.Buffer{}
	if err := testProviderWithClient(output, preset, "sk-deepseek", server.Client()); err != nil {
		t.Fatalf("testProvider returned error: %v", err)
	}
	if !strings.Contains(output.String(), "OK") {
		t.Fatalf("expected OK in output, got %q", output.String())
	}
}

func TestTestProviderReadErrorHandled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"partial`))
	}))
	defer server.Close()

	preset := ProviderPreset{
		Name:    "test-provider",
		BaseURL: server.URL,
		Model:   "test-model",
	}
	output := &bytes.Buffer{}
	if err := testProviderWithClient(output, preset, "sk-test", server.Client()); err != nil {
		t.Fatalf("testProvider returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("expected FAIL in output, got %q", out)
	}
	if !strings.Contains(out, "502") {
		t.Fatalf("expected 502 in output, got %q", out)
	}
}

func TestCmdTestNoProviderError(t *testing.T) {
	if err := cmdTest([]string{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestCmdTestMissingKeyError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := cmdTest([]string{"openrouter"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for missing api key")
	}
}

func TestBuildModelListNoCustom(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	models := buildModelList(cfg, "openrouter", nil)
	if len(models) != 3 {
		t.Fatalf("expected 3 openrouter models, got %d: %v", len(models), models)
	}
	if models[0] != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("expected sonnet as first model, got %q", models[0])
	}
}

func TestBuildModelListWithCustom(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	customModels := map[string]string{"openrouter": "my-custom-model"}
	models := buildModelList(cfg, "openrouter", customModels)
	if models[0] != "my-custom-model" {
		t.Fatalf("expected custom model first, got %q", models[0])
	}
	if len(models) != 4 {
		t.Fatalf("expected 4 models (custom + 3 preset), got %d: %v", len(models), models)
	}
}

func TestBuildModelListCustomSameAsPreset(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	customModels := map[string]string{"openrouter": "anthropic/claude-sonnet-4.6"}
	models := buildModelList(cfg, "openrouter", customModels)
	if models[0] != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("expected sonnet first, got %q", models[0])
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 models (no duplicate), got %d: %v", len(models), models)
	}
}

func TestBuildModelListEmptyCustom(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	customModels := map[string]string{"openrouter": "  "}
	models := buildModelList(cfg, "openrouter", customModels)
	if len(models) != 3 {
		t.Fatalf("expected 3 preset models when custom is whitespace, got %d", len(models))
	}
}

func TestSwitchProviderWritesToWriter(t *testing.T) {
	claudeDir := t.TempDir()
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	output := &bytes.Buffer{}

	if err := switchProvider("deepseek", cfg, "sk-test", "", claudeDir, output); err != nil {
		t.Fatalf("switchProvider returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "switched Claude to DeepSeek") {
		t.Fatalf("expected switch confirmation, got %q", out)
	}
	if !strings.Contains(out, "base_url:") {
		t.Fatalf("expected base_url in output, got %q", out)
	}
	if !strings.Contains(out, "model:") {
		t.Fatalf("expected model in output, got %q", out)
	}
	settingsBytes, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
}

func TestCmdTestIntegration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"my-test": {
				Name:    "Test Provider",
				BaseURL: server.URL,
				Model:   "test-model",
				APIKey:  "sk-test",
			},
		},
	}
	configPath := filepath.Join(home, ".claude-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := cmdTest([]string{"my-test", "--api-key", "sk-test"}, output); err != nil {
		t.Fatalf("cmdTest returned error: %v", err)
	}
	if !strings.Contains(output.String(), "OK") {
		t.Fatalf("expected OK, got %q", output.String())
	}
}

func TestCmdTestWithStoredKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"test-custom": {
				Name:    "Test Custom",
				BaseURL: server.URL,
				Model:   "test-model",
				APIKey:  "sk-stored-key",
			},
		},
	}
	configPath := filepath.Join(home, ".claude-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := cmdTest([]string{"test-custom"}, output); err != nil {
		t.Fatalf("cmdTest returned unexpected error: %v", err)
	}
	if !strings.Contains(output.String(), "OK") {
		t.Fatalf("expected OK, got %q", output.String())
	}
}
