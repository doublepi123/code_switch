package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ollamaTagResponse struct {
	Models []ollamaModel `json:"models"`
}

type ollamaModel struct {
	Name string `json:"name"`
}

type openRouterModelsResponse struct {
	Data []openRouterModelData `json:"data"`
}

type openRouterModelData struct {
	ID string `json:"id"`
}

func discoverOllamaModels() []string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var data ollamaTagResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	if len(data.Models) == 0 {
		return nil
	}

	models := make([]string, 0, len(data.Models))
	for _, m := range data.Models {
		name := strings.TrimSpace(m.Name)
		if name != "" {
			models = append(models, name)
		}
	}
	sort.Strings(models)
	return models
}

func ollamaModels() []string {
	if discovered := discoverOllamaModels(); len(discovered) > 0 {
		return discovered
	}
	return providerPresets["ollama"].Models
}

func discoverOpenRouterModels(apiKey string) []string {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var data openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}
	if len(data.Data) == 0 {
		return nil
	}
	models := make([]string, 0, len(data.Data))
	for _, m := range data.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models
}

func openRouterModels(cfg *AppConfig) []string {
	return openRouterModelsWithAPIKey(cfg, "")
}

func openRouterModelsWithAPIKey(cfg *AppConfig, apiKey string) []string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		apiKey = storedAPIKeyForAgent(cfg, agentCodex, "openrouter")
	}
	if apiKey == "" {
		apiKey = storedAPIKeyForAgent(cfg, agentClaude, "openrouter")
	}
	if discovered := discoverOpenRouterModels(apiKey); len(discovered) > 0 {
		return discovered
	}
	return providerPresets["openrouter"].Models
}

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
	ForceModelTiers    bool
	ModelTierOverrides map[string]ModelTiers
	AuthEnv            string
	ExtraEnv           map[string]any
	Website            string
	APIKeyURL          string
	NoAPIKey           bool
	ReasoningEffort      string
	ModelReasoningEffort map[string]string
}

type StoredProvider struct {
	Name     string `json:"name,omitempty"`
	BaseURL  string `json:"baseUrl,omitempty"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
	AuthEnv  string `json:"authEnv,omitempty"`
	Haiku    string `json:"haiku,omitempty"`
	Sonnet   string `json:"sonnet,omitempty"`
	Opus     string `json:"opus,omitempty"`
	Subagent string `json:"subagent,omitempty"`
}

type AgentConfig struct {
	Providers map[string]StoredProvider `json:"providers,omitempty"`
}

type AppConfig struct {
	Providers map[string]StoredProvider `json:"providers"`
	Agents    map[string]AgentConfig    `json:"agents,omitempty"`
}

type ConfigureSelection struct {
	Agent    string
	Provider string
	Model    string
	ResetKey bool
	APIKey   string
	Name     string
	BaseURL  string
	AuthEnv  string
	Haiku    string
	Sonnet   string
	Opus     string
	Subagent string
}

type AgentName string

const (
	agentClaude AgentName = "claude"
	agentCodex  AgentName = "codex"
)

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
			"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "0",
			"CLAUDE_CODE_EFFORT_LEVEL":                  "xhigh",
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
		ExtraEnv: map[string]any{
			"CLAUDE_CODE_EFFORT_LEVEL": "xhigh",
		},
	},
	"xiaomimimo-cn": {
		Name:      "Xiaomi MiMo Token Plan CN",
		BaseURL:   "https://token-plan-cn.xiaomimimo.com/anthropic",
		Model:     "mimo-v2.5-pro",
		Models:    []string{"mimo-v2.5-pro", "mimo-v2.5", "mimo-v2-pro", "mimo-v2-omni", "mimo-v2-flash"},
		Haiku:     "mimo-v2.5-pro",
		Sonnet:    "mimo-v2.5-pro",
		Opus:      "mimo-v2.5-pro",
		AuthEnv:   "ANTHROPIC_AUTH_TOKEN",
		Website:   "https://platform.xiaomimimo.com",
		APIKeyURL: "https://platform.xiaomimimo.com/#/console/plan-manage",
	},
	"ollama": {
		Name:    "Ollama (Local)",
		BaseURL: "http://localhost:11434",
		Model:   "qwen3-coder",
		Models: []string{"qwen3-coder", "gpt-oss:20b", "qwen2.5:14b", "qwen2.5:7b", "qwen2.5:32b", "qwen2.5:72b",
			"qwen2.5-coder:14b", "qwen2.5-coder:7b", "deepseek-r1:14b", "deepseek-r1:32b",
			"llama3.1:8b", "llama3.1:70b", "gemma3:12b", "gemma3:27b",
			"mistral:7b", "codellama:13b", "codellama:34b", "phi4:14b",
			"glm-4.7:cloud", "minimax-m2.1:cloud"},
		Haiku:     "qwen2.5:7b",
		Sonnet:    "qwen3-coder",
		Opus:      "qwen2.5:32b",
		Subagent:  "qwen2.5-coder:7b",
		AuthEnv:   "ANTHROPIC_AUTH_TOKEN",
		Website:   "https://ollama.com",
		APIKeyURL: "https://ollama.com",
		NoAPIKey:  true,
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "600000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		},
	},
	"zai": {
		Name:      "Z.AI GLM Coding Plan",
		BaseURL:   "https://api.z.ai/api/anthropic",
		Model:     "glm-5.1",
		Models:    []string{"glm-5.1", "glm-5-turbo", "glm-4.7", "glm-4.5-air"},
		Haiku:     "glm-4.5-air",
		Sonnet:    "glm-5-turbo",
		Opus:      "glm-5.1",
		Subagent:  "glm-5-turbo",
		AuthEnv:   "ANTHROPIC_AUTH_TOKEN",
		Website:   "https://open.z.ai",
		APIKeyURL: "https://open.z.ai",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
		},
	},
	"ollama-cloud": {
		Name:    "Ollama Cloud",
		BaseURL: "https://ollama.com",
		Model:   "qwen3-coder:480b",
		Models: []string{
			"qwen3-coder:480b",
			"minimax-m2.7",
			"kimi-k2.6",
			"kimi-k2.5",
			"glm-5.1",
			"glm-5",
			"deepseek-v4-pro",
			"deepseek-v4-flash",
			"qwen3.5:397b",
			"gpt-oss:120b",
			"gpt-oss:20b",
		},
		Haiku:           "qwen3-coder:480b",
		Sonnet:          "qwen3-coder:480b",
		Opus:            "qwen3-coder:480b",
		Subagent:        "qwen3-coder:480b",
		ForceModelTiers: true,
		ModelReasoningEffort: map[string]string{
			"deepseek-v4-pro":   "xhigh",
			"deepseek-v4-flash": "xhigh",
		},
		AuthEnv:         "ANTHROPIC_AUTH_TOKEN",
		Website:         "https://ollama.com/cloud",
		APIKeyURL:       "https://ollama.com/settings/keys",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "600000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		},
	},
	"volcengine": {
		Name:      "Volcengine Ark Coding Plan",
		BaseURL:   "https://ark.cn-beijing.volces.com/api/coding",
		Model:     "ark-code-latest",
		Models:    []string{"ark-code-latest", "doubao-seed-2.0-code", "doubao-seed-2.0-pro", "doubao-seed-2.0-lite", "doubao-seed-code", "minimax-latest", "glm-5.1", "deepseek-v3.2", "kimi-k2.6"},
		Haiku:     "ark-code-latest",
		Sonnet:    "ark-code-latest",
		Opus:      "ark-code-latest",
		Subagent:  "ark-code-latest",
		ForceModelTiers: true,
		AuthEnv:   "ANTHROPIC_AUTH_TOKEN",
		Website:   "https://www.volcengine.com/activity/codingplan",
		APIKeyURL: "https://console.volcengine.com/ark/region:ark+cn-beijing/apikey",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		},
	},
}

var unsupportedOpenCodeGoAnthropicModels = map[string]string{
	"glm-5":                 "GLM is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"glm-5.1":               "GLM is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"kimi-k2.5":            "Kimi is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"kimi-k2.6":            "Kimi is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2-pro":          "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2-omni":         "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2.5-pro":        "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2.5":            "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"mimo-v2-flash":         "MiMo is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"qwen3.6-plus":         "Qwen is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"qwen3.5-plus":         "Qwen is exposed by OpenCode Go on chat/completions, not Anthropic messages",
	"doubao-seed-code":     "Doubao is available via Volcengine Ark, not OpenCode Go",
	"doubao-seed-2.0-code": "Doubao is available via Volcengine Ark, not OpenCode Go",
	"doubao-seed-2.0-pro":  "Doubao is available via Volcengine Ark, not OpenCode Go",
	"doubao-seed-2.0-lite": "Doubao is available via Volcengine Ark, not OpenCode Go",
	"ark-code-latest":     "ark-code-latest is available via Volcengine Ark, not OpenCode Go",
}

var providerAliases = map[string]string{
	"minimax":              "minimax-cn",
	"minimax-cn-token":     "minimax-cn",
	"minimax-global-token": "minimax-global",
	"xiaomimimo":           "xiaomimimo-cn",
	"xiaomimio":            "xiaomimimo-cn",
	"mimo":                 "xiaomimimo-cn",
	"ollamacloud":          "ollama-cloud",
	"ollama.com":           "ollama-cloud",
	"z.ai":                 "zai",
	"zai":                  "zai",
	"glm":                  "zai",
	"ark":                  "volcengine",
	"volcengine-ark":       "volcengine",
	"volcengine.com":       "volcengine",
	"ark.cn-beijing.volces.com": "volcengine",
}

const customProviderOption = "__custom__"
const restoreProviderOption = "__restore__"
const customDetectedProvider = "custom"
const defaultUpgradeRepo = "doublepi123/code_switch"

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
	if err := validateBaseURL(stored.BaseURL); err != nil {
		return ProviderPreset{}, fmt.Errorf("invalid base URL for provider %q: %w", provider, err)
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
		Haiku:    firstNonEmpty(stored.Haiku, model),
		Sonnet:   firstNonEmpty(stored.Sonnet, model),
		Opus:     firstNonEmpty(stored.Opus, model),
		Subagent: firstNonEmpty(stored.Subagent, model),
		AuthEnv:  strings.TrimSpace(stored.AuthEnv),
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
		preset = withSelectedModel(preset, model)
		applyStoredTierOverrides(&preset, cfg.Providers[provider])
		return preset, nil
	}

	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return ProviderPreset{}, err
	}
	preset = withSelectedModel(preset, modelOverride)
	applyStoredTierOverrides(&preset, cfg.Providers[provider])
	return preset, nil
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
		return applyDefaultModel(preset)
	}
	return applyModelOverride(preset, model)
}

func applyDefaultModel(preset ProviderPreset) ProviderPreset {
	if re, ok := preset.ModelReasoningEffort[preset.Model]; ok {
		preset.ReasoningEffort = re
	}
	if preset.ForceModelTiers {
		return withSingleModelTiers(preset, preset.Model)
	}
	return preset
}

func applyModelOverride(preset ProviderPreset, model string) ProviderPreset {
	isKnown := containsString(preset.Models, model)
	preset.Model = model

	if re, ok := preset.ModelReasoningEffort[model]; ok {
		preset.ReasoningEffort = re
	}
	if !isKnown {
		preset.Models = append([]string{model}, preset.Models...)
	}

	if preset.ForceModelTiers {
		return withSingleModelTiers(preset, model)
	}

	if !isKnown {
		preset = withSingleModelTiers(preset, model)
	} else if tiers, ok := preset.ModelTierOverrides[model]; ok {
		preset = withOverrideTiers(preset, tiers)
	}
	return preset
}

func withSingleModelTiers(preset ProviderPreset, model string) ProviderPreset {
	preset.Haiku = model
	preset.Sonnet = model
	preset.Opus = model
	preset.Subagent = model
	return preset
}

func withOverrideTiers(preset ProviderPreset, tiers ModelTiers) ProviderPreset {
	preset.Haiku = tiers.Haiku
	preset.Sonnet = tiers.Sonnet
	preset.Opus = tiers.Opus
	preset.Subagent = tiers.Subagent
	return preset
}

func applyStoredTierOverrides(preset *ProviderPreset, stored StoredProvider) {
	if v := strings.TrimSpace(stored.Haiku); v != "" {
		preset.Haiku = v
	}
	if v := strings.TrimSpace(stored.Sonnet); v != "" {
		preset.Sonnet = v
	}
	if v := strings.TrimSpace(stored.Opus); v != "" {
		preset.Opus = v
	}
	if v := strings.TrimSpace(stored.Subagent); v != "" {
		preset.Subagent = v
	}
}

func detectProvider(baseURL, model string) string {
	host := normalizedURLHost(baseURL)
	switch {
	case (host == "localhost" || host == "127.0.0.1" || host == "::1") && strings.Contains(baseURL, ":11434"):
		return "ollama"
	case host == "ollama.com":
		return "ollama-cloud"
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
	case strings.HasSuffix(host, ".xiaomimimo.com"):
		return "xiaomimimo-cn"
	case host == "api.z.ai" || strings.HasSuffix(host, ".z.ai"):
		return "zai"
	case host == "ark.cn-beijing.volces.com" || strings.HasSuffix(host, ".volces.com"):
		return "volcengine"
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
	if provider == "ollama" {
		if models := ollamaModels(); len(models) > 0 {
			return models
		}
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
