package main

import (
	"fmt"
	"sort"
	"strings"
)

type ModelTiers struct {
	Haiku    string
	Sonnet   string
	Opus     string
	Subagent string
}

type ProviderPreset struct {
	Name               string
	BaseURL            string
	Model              string
	Models             []string
	Haiku              string
	Sonnet             string
	Opus               string
	Subagent           string
	ModelTierOverrides map[string]ModelTiers
	AuthEnv            string
	ExtraEnv           map[string]any
	Website            string
	APIKeyURL          string
}

type StoredProvider struct {
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	Model   string `json:"model,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
}

type AppConfig struct {
	Providers map[string]StoredProvider `json:"providers"`
}

type ConfigureSelection struct {
	Provider string
	Model    string
	ResetKey bool
	APIKey   string
	Name     string
	BaseURL  string
}

var providerPresets = map[string]ProviderPreset{
	"minimax-cn": {
		Name:      "MiniMax CN Token Plan",
		BaseURL:   "https://api.minimaxi.com/anthropic",
		Model:     "MiniMax-M2.7",
		Models:    []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"},
		Haiku:     "MiniMax-M2.7",
		Sonnet:    "MiniMax-M2.7",
		Opus:      "MiniMax-M2.7",
		Website:   "https://platform.minimaxi.com",
		APIKeyURL: "https://platform.minimaxi.com/docs/token-plan/claude-code",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		},
	},
	"minimax-global": {
		Name:      "MiniMax Global Token Plan",
		BaseURL:   "https://api.minimax.io/anthropic",
		Model:     "MiniMax-M2.7",
		Models:    []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"},
		Haiku:     "MiniMax-M2.7",
		Sonnet:    "MiniMax-M2.7",
		Opus:      "MiniMax-M2.7",
		Website:   "https://platform.minimax.io",
		APIKeyURL: "https://platform.minimax.io/docs/token-plan/claude-code",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		},
	},
	"openrouter": {
		Name:      "OpenRouter",
		BaseURL:   "https://openrouter.ai/api",
		Model:     "anthropic/claude-sonnet-4.6",
		Models:    []string{"anthropic/claude-sonnet-4.6", "anthropic/claude-haiku-4.5", "anthropic/claude-opus-4.7"},
		Haiku:     "anthropic/claude-haiku-4.5",
		Sonnet:    "anthropic/claude-sonnet-4.6",
		Opus:      "anthropic/claude-opus-4.7",
		Website:   "https://openrouter.ai",
		APIKeyURL: "https://openrouter.ai/keys",
	},
	"deepseek": {
		Name:      "DeepSeek",
		BaseURL:   "https://api.deepseek.com/anthropic",
		Model:     "deepseek-v4-pro[1m]",
		Models:    []string{"deepseek-v4-pro[1m]", "deepseek-v4-pro", "deepseek-v4-flash"},
		Haiku:     "deepseek-v4-flash",
		Sonnet:    "deepseek-v4-pro",
		Opus:      "deepseek-v4-pro",
		Subagent:  "deepseek-v4-pro",
		AuthEnv:   "ANTHROPIC_AUTH_TOKEN",
		Website:   "https://platform.deepseek.com",
		APIKeyURL: "https://platform.deepseek.com/api_keys",
		ExtraEnv: map[string]any{
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":  "1",
			"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "1",
			"CLAUDE_CODE_EFFORT_LEVEL":                  "max",
		},
	},
	"opencode-go": {
		Name:    "OpenCode Go",
		BaseURL: "https://opencode.ai/zen/go",
		Model:   "minimax-m2.7",
		Models:  []string{"minimax-m2.7", "minimax-m2.5", "deepseek-v4-pro", "deepseek-v4-flash"},
		Haiku:   "minimax-m2.7",
		Sonnet:  "minimax-m2.7",
		Opus:    "minimax-m2.7",
		ModelTierOverrides: map[string]ModelTiers{
			"deepseek-v4-pro":   {Haiku: "deepseek-v4-flash", Sonnet: "deepseek-v4-pro", Opus: "deepseek-v4-pro", Subagent: "deepseek-v4-pro"},
			"deepseek-v4-flash": {Haiku: "deepseek-v4-flash", Sonnet: "deepseek-v4-flash", Opus: "deepseek-v4-flash", Subagent: "deepseek-v4-flash"},
		},
		Website:   "https://opencode.ai/docs/go/",
		APIKeyURL: "https://opencode.ai",
	},
}

var unsupportedOpenCodeGoAnthropicModels = map[string]string{
	"glm-5":         "GLM is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"glm-5.1":       "GLM is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"kimi-k2.5":     "Kimi is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"kimi-k2.6":     "Kimi is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2-pro":   "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2-omni":  "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2.5-pro": "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2.5":     "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"qwen3.6-plus":  "Qwen is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"qwen3.5-plus":  "Qwen is exposed by OpenCode Go on chat/completions, not Anthropic messages",
}

var providerAliases = map[string]string{
	"minimax":              "minimax-cn",
	"minimax-cn-token":     "minimax-cn",
	"minimax-global-token": "minimax-global",
}

const customProviderOption = "__custom__"
const customDetectedProvider = "custom"
const defaultUpgradeRepo = "doublepi123/claude_switch"

var version = "dev"

var managedEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"API_TIMEOUT_MS",
	"CLAUDE_CODE_SUBAGENT_MODEL",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK",
	"CLAUDE_CODE_EFFORT_LEVEL",
}

func canonicalProviderName(name string) string {
	normalized := normalizeProviderName(name)
	if canonical, ok := providerAliases[normalized]; ok {
		return canonical
	}
	return normalized
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func sortedProviderNames(cfg *AppConfig, includeCustomOption bool) []string {
	providerCount := len(providerPresets)
	if cfg.Providers != nil {
		providerCount += len(cfg.Providers)
	}
	names := make([]string, 0, providerCount+1)
	for name := range providerPresets {
		names = append(names, name)
	}
	for name, stored := range cfg.Providers {
		if _, ok := providerPresets[name]; ok {
			continue
		}
		if strings.TrimSpace(stored.BaseURL) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if includeCustomOption {
		names = append(names, customProviderOption)
	}
	return names
}

func resolveProviderPreset(provider string, cfg *AppConfig) (ProviderPreset, error) {
	if preset, ok := providerPresets[provider]; ok {
		if stored, ok := cfg.Providers[provider]; ok && strings.TrimSpace(stored.Model) != "" {
			preset = withSelectedModel(preset, stored.Model)
		}
		return preset, nil
	}

	stored, ok := cfg.Providers[provider]
	if !ok || strings.TrimSpace(stored.BaseURL) == "" {
		return ProviderPreset{}, fmt.Errorf("unsupported provider %q", provider)
	}
	model := strings.TrimSpace(stored.Model)
	if model == "" {
		model = "custom-model"
	}
	return ProviderPreset{
		Name:     firstNonEmpty(stored.Name, provider),
		BaseURL:  strings.TrimSpace(stored.BaseURL),
		Model:    model,
		Models:   []string{model},
		Haiku:    model,
		Sonnet:   model,
		Opus:     model,
		Subagent: model,
	}, nil
}

func resolveSwitchPreset(provider string, cfg *AppConfig, modelOverride string) (ProviderPreset, error) {
	if preset, ok := providerPresets[provider]; ok {
		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(cfg.Providers[provider].Model)
		}
		if err := validateProviderModel(provider, model); err != nil {
			return ProviderPreset{}, err
		}
		return withSelectedModel(preset, model), nil
	}

	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return ProviderPreset{}, err
	}
	return withSelectedModel(preset, modelOverride), nil
}

func validateProviderModel(provider, model string) error {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	if provider == "opencode-go" {
		if reason, ok := unsupportedOpenCodeGoAnthropicModels[model]; ok {
			return fmt.Errorf("%s cannot be used with provider opencode-go in Claude Code: %s. Use minimax-m2.7/minimax-m2.5 with opencode-go, or switch to provider deepseek for DeepSeek's Anthropic-compatible API", model, reason)
		}
	}
	return nil
}

func withSelectedModel(preset ProviderPreset, model string) ProviderPreset {
	model = strings.TrimSpace(model)
	if model == "" {
		return preset
	}

	isPresetModel := containsString(preset.Models, model)
	preset.Model = model
	if !isPresetModel {
		preset.Models = append([]string{model}, preset.Models...)
		preset.Haiku = model
		preset.Sonnet = model
		preset.Opus = model
		preset.Subagent = model
	} else if tiers, ok := preset.ModelTierOverrides[model]; ok {
		preset.Haiku = tiers.Haiku
		preset.Sonnet = tiers.Sonnet
		preset.Opus = tiers.Opus
		preset.Subagent = tiers.Subagent
	}
	return preset
}

func detectProvider(baseURL, model string) string {
	host := normalizedURLHost(baseURL)
	switch {
	case host == "api.minimaxi.com":
		return "minimax-cn"
	case host == "api.minimax.io":
		return "minimax-global"
	case host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai"):
		return "openrouter"
	case host == "api.deepseek.com":
		return "deepseek"
	case host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai") || strings.HasPrefix(model, "opencode-go/"):
		return "opencode-go"
	default:
		return customDetectedProvider
	}
}

func isPresetProvider(name string) bool {
	_, ok := providerPresets[name]
	return ok
}

func isProviderAlias(name string) bool {
	_, ok := providerAliases[name]
	return ok
}

func providerTitle(name string, cfg *AppConfig) string {
	if name == customProviderOption {
		return "custom..."
	}
	if stored, ok := cfg.Providers[name]; ok && strings.TrimSpace(stored.Name) != "" && !isPresetProvider(name) {
		return strings.TrimSpace(stored.Name)
	}
	return name
}

func providerModels(cfg *AppConfig, provider string) []string {
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return nil
	}
	if len(preset.Models) == 0 {
		return []string{preset.Model}
	}
	return preset.Models
}

func modelIndex(cfg *AppConfig, provider, currentProvider, currentModel string) int {
	selected := defaultSelectionModel(cfg, provider, currentProvider, currentModel)
	for i, model := range providerModels(cfg, provider) {
		if model == selected {
			return i
		}
	}
	return 0
}

func defaultSelectionModel(cfg *AppConfig, provider, currentProvider, currentModel string) string {
	if provider == currentProvider && currentModel != "" {
		for _, model := range providerModels(cfg, provider) {
			if model == currentModel {
				return currentModel
			}
		}
	}
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return ""
	}
	return preset.Model
}

func currentProviderLabel(provider string) string {
	if provider == "" {
		return "none"
	}
	return provider
}

func currentModelLabel(model string) string {
	if model == "" {
		return "none"
	}
	return model
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
