package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	modelCatalogSourceStatic   = "static"
	modelCatalogSourceRemote   = "remote"
	modelCatalogSourceFallback = "fallback"
)

var (
	modelCatalogHTTPClient    = &http.Client{Timeout: 3 * time.Second}
	modelCatalogOpenRouterURL = "https://openrouter.ai/api/v1/models"
	modelCatalogOllamaURL     = "http://localhost:11434/api/tags"
	modelCatalogDeepSeekURL   = "https://api.deepseek.com/v1"
)

type openAICompatibleModelsResponse struct {
	Data []openAICompatibleModelData `json:"data"`
}

type openAICompatibleModelData struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type ProviderModelInfo struct {
	ID            string
	Name          string
	Description   string
	ContextWindow int
	MaxOutput     int
	InputPrice    string
	OutputPrice   string
	Capabilities  []string
	RawProvider   string
}

type ProviderModelCatalog struct {
	Provider string
	Source   string
	Models   []ProviderModelInfo
	Err      string
}

func providerModelCatalog(cfg *AppConfig, agent AgentName, provider, apiKey string) ProviderModelCatalog {
	provider = canonicalProviderName(provider)
	preset, err := resolveAgentProviderPreset(agent, provider, cfg)
	if err != nil {
		preset, err = resolveProviderPreset(provider, cfg)
	}
	if err != nil {
		return ProviderModelCatalog{Provider: provider, Source: modelCatalogSourceFallback, Err: err.Error()}
	}

	switch provider {
	case "ollama":
		return catalogWithResolvedFallback(fetchOllamaModelCatalog(), staticModelCatalog(provider, preset))
	case "openrouter":
		if strings.TrimSpace(apiKey) == "" {
			apiKey = storedAPIKeyForAgent(cfg, agent, provider)
		}
		if strings.TrimSpace(apiKey) == "" && agent != agentCodex {
			apiKey = storedAPIKeyForAgent(cfg, agentCodex, provider)
		}
		return catalogWithResolvedFallback(fetchOpenRouterModelCatalog(apiKey), staticModelCatalog(provider, preset))
	case "deepseek":
		apiKey = modelCatalogAPIKey(cfg, agent, provider, apiKey)
		return catalogWithResolvedFallback(withCatalogProvider(fetchDeepSeekModelCatalog(apiKey), provider), staticModelCatalog(provider, preset))
	case "zhipu-cn":
		if endpoint, ok := openAICompatibleEndpoint(preset); ok {
			apiKey = modelCatalogAPIKey(cfg, agent, provider, apiKey)
			return catalogWithResolvedFallback(withCatalogProvider(fetchOpenAICompatibleModelCatalog(endpoint.BaseURL, apiKey), provider), staticModelCatalog(provider, preset))
		}
		return staticModelCatalog(provider, preset)
	default:
		if endpoint, ok := openAICompatibleEndpoint(preset); ok {
			apiKey = modelCatalogAPIKey(cfg, agent, provider, apiKey)
			return catalogWithResolvedFallback(withCatalogProvider(fetchOpenAICompatibleModelCatalog(endpoint.BaseURL, apiKey), provider), staticModelCatalog(provider, preset))
		}
		return staticModelCatalog(provider, preset)
	}
}

func withCatalogProvider(catalog ProviderModelCatalog, provider string) ProviderModelCatalog {
	catalog.Provider = provider
	return catalog
}

func modelCatalogAPIKey(cfg *AppConfig, agent AgentName, provider, apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		return apiKey
	}
	return storedAPIKeyForAgent(cfg, agent, provider)
}

func openAICompatibleEndpoint(preset ProviderPreset) (ProtocolEndpoint, bool) {
	if endpoint, ok := preset.presetEndpoint(protocolOpenAIChat); ok {
		return endpoint, true
	}
	if endpoint, ok := preset.presetEndpoint(protocolOpenAIResponses); ok {
		return endpoint, true
	}
	return ProtocolEndpoint{}, false
}

func catalogWithResolvedFallback(catalog, resolvedStatic ProviderModelCatalog) ProviderModelCatalog {
	if catalog.Source != modelCatalogSourceFallback {
		return catalog
	}
	resolvedStatic.Source = modelCatalogSourceFallback
	resolvedStatic.Err = catalog.Err
	return resolvedStatic
}

func staticModelCatalog(provider string, preset ProviderPreset) ProviderModelCatalog {
	ids := preset.Models
	if len(ids) == 0 && strings.TrimSpace(preset.Model) != "" {
		ids = []string{preset.Model}
	}
	models := make([]ProviderModelInfo, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		models = append(models, ProviderModelInfo{ID: id})
	}
	return ProviderModelCatalog{Provider: provider, Source: modelCatalogSourceStatic, Models: models}
}

func fetchOllamaModelCatalog() ProviderModelCatalog {
	static := staticModelCatalog("ollama", providerPresets["ollama"])
	resp, err := modelCatalogHTTPClient.Get(modelCatalogOllamaURL)
	if err != nil {
		return fallbackModelCatalog(static, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallbackModelCatalog(static, fmt.Errorf("ollama models request returned HTTP %d", resp.StatusCode))
	}
	var data ollamaTagResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fallbackModelCatalog(static, err)
	}
	models := make([]ProviderModelInfo, 0, len(data.Models))
	for _, item := range data.Models {
		id := strings.TrimSpace(item.Name)
		if id != "" {
			models = append(models, ProviderModelInfo{ID: id})
		}
	}
	if len(models) == 0 {
		return fallbackModelCatalog(static, fmt.Errorf("ollama returned no models"))
	}
	sortModelInfoByID(models)
	return ProviderModelCatalog{Provider: "ollama", Source: modelCatalogSourceRemote, Models: models}
}

func fetchOpenRouterModelCatalog(apiKey string) ProviderModelCatalog {
	static := staticModelCatalog("openrouter", providerPresets["openrouter"])
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fallbackModelCatalog(static, fmt.Errorf("openrouter API key is required to fetch remote models"))
	}
	req, err := http.NewRequest(http.MethodGet, modelCatalogOpenRouterURL, nil)
	if err != nil {
		return fallbackModelCatalog(static, err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := modelCatalogHTTPClient.Do(req)
	if err != nil {
		return fallbackModelCatalog(static, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallbackModelCatalog(static, fmt.Errorf("openrouter models request returned HTTP %d", resp.StatusCode))
	}
	var data openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fallbackModelCatalog(static, err)
	}
	models := make([]ProviderModelInfo, 0, len(data.Data))
	for _, item := range data.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		models = append(models, ProviderModelInfo{
			ID:            id,
			Name:          strings.TrimSpace(item.Name),
			Description:   strings.TrimSpace(item.Description),
			ContextWindow: item.ContextLength,
			InputPrice:    strings.TrimSpace(item.Pricing.Prompt),
			OutputPrice:   strings.TrimSpace(item.Pricing.Completion),
			Capabilities:  compactStrings(item.SupportedParameters),
			RawProvider:   "openrouter",
		})
	}
	if len(models) == 0 {
		return fallbackModelCatalog(static, fmt.Errorf("openrouter returned no models"))
	}
	sortModelInfoByID(models)
	return ProviderModelCatalog{Provider: "openrouter", Source: modelCatalogSourceRemote, Models: models}
}

func fetchDeepSeekModelCatalog(apiKey string) ProviderModelCatalog {
	static := staticModelCatalog("deepseek", providerPresets["deepseek"])
	return catalogWithResolvedFallback(withCatalogProvider(fetchOpenAICompatibleModelCatalog(modelCatalogDeepSeekURL, apiKey), "deepseek"), static)
}

func fetchOpenAICompatibleModelCatalog(baseURL, apiKey string) ProviderModelCatalog {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: "OpenAI-compatible base URL is required to fetch remote models"}
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: "API key is required to fetch remote models"}
	}
	modelsURL, err := appendURLPath(baseURL, "models")
	if err != nil {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: err.Error()}
	}
	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := modelCatalogHTTPClient.Do(req)
	if err != nil {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: fmt.Sprintf("OpenAI-compatible models request returned HTTP %d", resp.StatusCode)}
	}
	var data openAICompatibleModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: err.Error()}
	}
	models := make([]ProviderModelInfo, 0, len(data.Data))
	for _, item := range data.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		models = append(models, ProviderModelInfo{ID: id, RawProvider: strings.TrimSpace(item.OwnedBy)})
	}
	if len(models) == 0 {
		return ProviderModelCatalog{Source: modelCatalogSourceFallback, Err: "OpenAI-compatible endpoint returned no models"}
	}
	sortModelInfoByID(models)
	return ProviderModelCatalog{Source: modelCatalogSourceRemote, Models: models}
}

func appendURLPath(baseURL, elem string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid OpenAI-compatible base URL %q", baseURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(elem, "/")
	return u.String(), nil
}

func fallbackModelCatalog(static ProviderModelCatalog, err error) ProviderModelCatalog {
	static.Source = modelCatalogSourceFallback
	if err != nil {
		static.Err = err.Error()
	}
	return static
}

func modelIDs(catalog ProviderModelCatalog) []string {
	ids := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		id := strings.TrimSpace(model.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func sortModelInfoByID(models []ProviderModelInfo) {
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
}

func compactStrings(values []string) []string {
	compact := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			compact = append(compact, value)
		}
	}
	return compact
}

func modelCatalogSecondaryText(model ProviderModelInfo) string {
	parts := []string{}
	if model.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("ctx %d", model.ContextWindow))
	}
	if model.MaxOutput > 0 {
		parts = append(parts, fmt.Sprintf("max %d", model.MaxOutput))
	}
	if model.InputPrice != "" {
		parts = append(parts, "in "+model.InputPrice)
	}
	if model.OutputPrice != "" {
		parts = append(parts, "out "+model.OutputPrice)
	}
	if len(model.Capabilities) > 0 {
		parts = append(parts, strings.Join(model.Capabilities, ","))
	}
	return strings.Join(parts, "  ")
}

func modelCatalogStatusText(catalog ProviderModelCatalog) string {
	status := fmt.Sprintf("Source: %s", catalog.Source)
	if catalog.Source == modelCatalogSourceRemote {
		status += fmt.Sprintf(" (%d models)", len(catalog.Models))
	}
	if strings.TrimSpace(catalog.Err) != "" {
		status += " - remote fetch failed: " + catalog.Err
	}
	return status
}

func modelInfoText(provider string, catalog ProviderModelCatalog, modelID string) string {
	model, ok := findModelInfo(catalog, modelID)
	if !ok {
		model = ProviderModelInfo{ID: modelID}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "Provider: %s\n", provider)
	fmt.Fprintf(&b, "Catalog source: %s\n", catalog.Source)
	if catalog.Err != "" {
		fmt.Fprintf(&b, "Catalog error: %s\n", catalog.Err)
	}
	fmt.Fprintf(&b, "ID: %s\n", model.ID)
	writeModelInfoField(&b, "Name", model.Name)
	writeModelInfoField(&b, "Description", model.Description)
	writeModelInfoIntField(&b, "Context window", model.ContextWindow)
	writeModelInfoIntField(&b, "Max output", model.MaxOutput)
	writeModelInfoField(&b, "Input price", model.InputPrice)
	writeModelInfoField(&b, "Output price", model.OutputPrice)
	if len(model.Capabilities) > 0 {
		fmt.Fprintf(&b, "Capabilities: %s\n", strings.Join(model.Capabilities, ", "))
	}
	writeModelInfoField(&b, "Raw provider", model.RawProvider)
	return b.String()
}

func findModelInfo(catalog ProviderModelCatalog, modelID string) (ProviderModelInfo, bool) {
	for _, model := range catalog.Models {
		if model.ID == modelID {
			return model, true
		}
	}
	return ProviderModelInfo{}, false
}

func writeModelInfoField(b *bytes.Buffer, label, value string) {
	if strings.TrimSpace(value) != "" {
		fmt.Fprintf(b, "%s: %s\n", label, value)
	}
}

func writeModelInfoIntField(b *bytes.Buffer, label string, value int) {
	if value > 0 {
		fmt.Fprintf(b, "%s: %d\n", label, value)
	}
}
