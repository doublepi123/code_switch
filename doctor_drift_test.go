package main

import (
	"path/filepath"
	"strings"
	"testing"
)

type doctorDriftFileConfig struct {
	Provider string
	Model    string
	BaseURL  string
}

func TestDoctorDrift_ok_when_agent_configs_match(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	codexDir := filepath.Join(home, ".codex")
	opencodeDir := filepath.Join(home, ".config", "opencode")
	cfg := driftTestConfig()

	writeDoctorDriftClaudeSettings(t, claudeDir, doctorDriftFileConfig{BaseURL: "https://example.test/claude", Model: "claude-model"})
	writeDoctorDriftCodexConfig(t, codexDir, doctorDriftFileConfig{Provider: "codex-provider", Model: "codex-model", BaseURL: "https://example.test/codex"})
	writeDoctorDriftOpencodeConfig(t, opencodeDir, doctorDriftFileConfig{Provider: "opencode-provider", Model: "opencode-model", BaseURL: "https://example.test/opencode"})

	// When
	claudeResult := checkClaudeDrift(claudeDir, cfg)
	codexResult := checkCodexDrift(codexDir, cfg)
	opencodeResult := checkOpencodeDrift(opencodeDir, cfg)

	// Then
	requireDoctorStatus(t, claudeResult, "ok")
	requireDoctorStatus(t, codexResult, "ok")
	requireDoctorStatus(t, opencodeResult, "ok")
}

func TestDoctorDrift_warns_when_model_or_base_url_differs(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	codexDir := filepath.Join(home, ".codex")
	opencodeDir := filepath.Join(home, ".config", "opencode")
	cfg := driftTestConfig()

	writeDoctorDriftClaudeSettings(t, claudeDir, doctorDriftFileConfig{BaseURL: "https://example.test/other-claude", Model: "other-claude-model"})
	writeDoctorDriftCodexConfig(t, codexDir, doctorDriftFileConfig{Provider: "codex-provider", Model: "other-codex-model", BaseURL: "https://example.test/other-codex"})
	writeDoctorDriftOpencodeConfig(t, opencodeDir, doctorDriftFileConfig{Provider: "opencode-provider", Model: "other-opencode-model", BaseURL: "https://example.test/other-opencode"})

	// When
	claudeResult := checkClaudeDrift(claudeDir, cfg)
	codexResult := checkCodexDrift(codexDir, cfg)
	opencodeResult := checkOpencodeDrift(opencodeDir, cfg)

	// Then
	requireDoctorStatus(t, claudeResult, "warn")
	requireDoctorStatus(t, codexResult, "warn")
	requireDoctorStatus(t, opencodeResult, "warn")
}

func TestDoctorDrift_ok_when_no_provider_configured(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	codexDir := filepath.Join(home, ".codex")
	opencodeDir := filepath.Join(home, ".config", "opencode")
	cfg := &AppConfig{}

	writeDoctorDriftClaudeSettings(t, claudeDir, doctorDriftFileConfig{BaseURL: "https://example.test/claude", Model: "claude-model"})
	writeDoctorDriftCodexConfig(t, codexDir, doctorDriftFileConfig{Provider: "codex-provider", Model: "codex-model", BaseURL: "https://example.test/codex"})
	writeDoctorDriftOpencodeConfig(t, opencodeDir, doctorDriftFileConfig{Provider: "opencode-provider", Model: "opencode-model", BaseURL: "https://example.test/opencode"})

	// When
	claudeResult := checkClaudeDrift(claudeDir, cfg)
	codexResult := checkCodexDrift(codexDir, cfg)
	opencodeResult := checkOpencodeDrift(opencodeDir, cfg)

	// Then
	requireDoctorStatus(t, claudeResult, "ok")
	requireDoctorStatus(t, codexResult, "ok")
	requireDoctorStatus(t, opencodeResult, "ok")
}

func TestDoctorDrift_uses_proxy_route_when_active(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	codexDir := filepath.Join(home, ".codex")
	opencodeDir := filepath.Join(home, ".config", "opencode")
	cfg := driftTestConfig()
	cfg.Proxy = &ProxyConfig{Routes: map[string]ProxyRouteConfig{
		"claude":   {Provider: "proxy-claude", Model: "proxy-claude-model", BaseURL: "https://proxy.test/claude"},
		"codex":    {Provider: "proxy-codex", Model: "proxy-codex-model", BaseURL: "https://proxy.test/codex"},
		"opencode": {Provider: "proxy-opencode", Model: "proxy-opencode-model", BaseURL: "https://proxy.test/opencode"},
	}}

	writeDoctorDriftClaudeSettings(t, claudeDir, doctorDriftFileConfig{BaseURL: "https://proxy.test/claude", Model: "proxy-claude-model"})
	writeDoctorDriftCodexConfig(t, codexDir, doctorDriftFileConfig{Provider: "proxy-codex", Model: "proxy-codex-model", BaseURL: "https://proxy.test/codex"})
	writeDoctorDriftOpencodeConfig(t, opencodeDir, doctorDriftFileConfig{Provider: "proxy-opencode", Model: "proxy-opencode-model", BaseURL: "https://proxy.test/opencode"})

	// When
	claudeResult := checkClaudeDrift(claudeDir, cfg)
	codexResult := checkCodexDrift(codexDir, cfg)
	opencodeResult := checkOpencodeDrift(opencodeDir, cfg)

	// Then
	requireDoctorStatus(t, claudeResult, "ok")
	requireDoctorStatus(t, codexResult, "ok")
	requireDoctorStatus(t, opencodeResult, "ok")
}

func TestRunDoctor_includes_all_agent_drift_checks(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	codexDir := filepath.Join(home, ".codex")
	opencodeDir := filepath.Join(home, ".config", "opencode")
	cfg := driftTestConfig()
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}
	writeDoctorDriftClaudeSettings(t, claudeDir, doctorDriftFileConfig{BaseURL: "https://example.test/claude", Model: "claude-model"})
	writeDoctorDriftCodexConfig(t, codexDir, doctorDriftFileConfig{Provider: "codex-provider", Model: "codex-model", BaseURL: "https://example.test/codex"})
	writeDoctorDriftOpencodeConfig(t, opencodeDir, doctorDriftFileConfig{Provider: "opencode-provider", Model: "opencode-model", BaseURL: "https://example.test/opencode"})

	// When
	results := runDoctor(claudeDir, codexDir, opencodeDir)

	// Then
	requireDoctorResultNamed(t, results, "claude config drift")
	requireDoctorResultNamed(t, results, "codex config drift")
	requireDoctorResultNamed(t, results, "opencode config drift")
}

func driftTestConfig() *AppConfig {
	cfg := &AppConfig{Providers: map[string]StoredProvider{
		"claude-provider": {BaseURL: "https://example.test/claude", Model: "claude-model"},
	}}
	setAgentProviderConfig(cfg, agentCodex, "codex-provider", StoredProvider{BaseURL: "https://example.test/codex", Model: "codex-model"})
	setAgentProviderConfig(cfg, agentOpencode, "opencode-provider", StoredProvider{BaseURL: "https://example.test/opencode", Model: "opencode-model"})
	return cfg
}

func writeDoctorDriftClaudeSettings(t *testing.T, claudeDir string, config doctorDriftFileConfig) {
	t.Helper()
	settings := map[string]any{"env": map[string]any{
		"ANTHROPIC_BASE_URL": config.BaseURL,
		"ANTHROPIC_MODEL":    config.Model,
	}}
	if err := writeJSONAtomic(filepath.Join(claudeDir, "settings.json"), settings); err != nil {
		t.Fatalf("write claude settings: %v", err)
	}
}

func writeDoctorDriftCodexConfig(t *testing.T, codexDir string, config doctorDriftFileConfig) {
	t.Helper()
	content := strings.Join([]string{
		`model = "` + config.Model + `"`,
		`model_provider = "` + config.Provider + `"`,
		"",
		`[model_providers.` + config.Provider + `]`,
		`base_url = "` + config.BaseURL + `"`,
		"",
	}, "\n")
	if err := writeTextAtomic(filepath.Join(codexDir, "config.toml"), content, 0o600); err != nil {
		t.Fatalf("write codex config: %v", err)
	}
}

func writeDoctorDriftOpencodeConfig(t *testing.T, opencodeDir string, config doctorDriftFileConfig) {
	t.Helper()
	root := map[string]any{
		"model": config.Model,
		"provider": map[string]any{
			config.Provider: map[string]any{
				"options": map[string]any{"baseURL": config.BaseURL},
				"models":  map[string]any{config.Model: map[string]any{"name": config.Model}},
			},
		},
	}
	if err := writeJSONAtomic(filepath.Join(opencodeDir, "opencode.json"), root); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}
}

func requireDoctorStatus(t *testing.T, result checkResult, want string) {
	t.Helper()
	if result.Status != want {
		t.Fatalf("expected %s for %s, got %s (%s)", want, result.Name, result.Status, result.Detail)
	}
}

func requireDoctorResultNamed(t *testing.T, results []checkResult, name string) {
	t.Helper()
	for _, result := range results {
		if result.Name == name {
			return
		}
	}
	t.Fatalf("missing doctor result %q in %#v", name, results)
}
