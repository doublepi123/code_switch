package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStaticModelCatalogUsesModelsOrDefaultModel(t *testing.T) {
	catalog := staticModelCatalog("example", ProviderPreset{Model: "default-model", Models: []string{"b", "a"}})
	if catalog.Provider != "example" || catalog.Source != "static" || catalog.Err != "" {
		t.Fatalf("unexpected catalog metadata: %+v", catalog)
	}
	if got := modelIDs(catalog); !equalStringSlices(got, []string{"b", "a"}) {
		t.Fatalf("model ids = %v, want [b a]", got)
	}

	catalog = staticModelCatalog("example", ProviderPreset{Model: "only-model"})
	if got := modelIDs(catalog); !equalStringSlices(got, []string{"only-model"}) {
		t.Fatalf("model ids = %v, want [only-model]", got)
	}
}

func TestFetchOpenRouterModelCatalogParsesModelInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [{
				"id": "anthropic/claude-sonnet-4",
				"name": "Claude Sonnet 4",
				"description": "balanced model",
				"context_length": 200000,
				"pricing": {"prompt": "0.000003", "completion": "0.000015"},
				"supported_parameters": ["tools", "reasoning"]
			}]
		}`))
	}))
	defer server.Close()

	restore := overrideModelCatalogHTTP(t, server.URL, "")
	defer restore()

	catalog := fetchOpenRouterModelCatalog("test-key")
	if catalog.Source != "remote" || catalog.Err != "" {
		t.Fatalf("unexpected catalog metadata: %+v", catalog)
	}
	if len(catalog.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(catalog.Models))
	}
	model := catalog.Models[0]
	if model.ID != "anthropic/claude-sonnet-4" || model.Name != "Claude Sonnet 4" || model.Description != "balanced model" {
		t.Fatalf("unexpected model text fields: %+v", model)
	}
	if model.ContextWindow != 200000 || model.InputPrice != "0.000003" || model.OutputPrice != "0.000015" {
		t.Fatalf("unexpected model metadata: %+v", model)
	}
	if !equalStringSlices(model.Capabilities, []string{"tools", "reasoning"}) {
		t.Fatalf("capabilities = %v", model.Capabilities)
	}
}

func TestFetchOpenRouterModelCatalogFallsBackWithoutNetworkWhenKeyMissing(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	restore := overrideModelCatalogHTTP(t, server.URL, "")
	defer restore()

	catalog := fetchOpenRouterModelCatalog(" ")
	if called {
		t.Fatal("fetchOpenRouterModelCatalog made a request without an API key")
	}
	if catalog.Source != "fallback" || catalog.Err == "" {
		t.Fatalf("unexpected fallback catalog: %+v", catalog)
	}
	if len(catalog.Models) == 0 {
		t.Fatal("fallback catalog has no static models")
	}
}

func TestFetchOllamaModelCatalogFallsBackOnRemoteError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	restore := overrideModelCatalogHTTP(t, "", server.URL)
	defer restore()

	catalog := fetchOllamaModelCatalog()
	if catalog.Source != "fallback" || catalog.Err == "" {
		t.Fatalf("unexpected fallback catalog: %+v", catalog)
	}
	if got := modelIDs(catalog); len(got) == 0 {
		t.Fatal("fallback catalog has no static models")
	}
}

func TestProviderModelCatalogUsesOpenRouterForCodexAndFallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"remote/model"}]}`))
	}))
	defer server.Close()

	restore := overrideModelCatalogHTTP(t, server.URL, "")
	defer restore()

	catalog := providerModelCatalog(&AppConfig{}, agentCodex, "openrouter", "test-key")
	if catalog.Source != "remote" || !equalStringSlices(modelIDs(catalog), []string{"remote/model"}) {
		t.Fatalf("provider catalog = %+v", catalog)
	}
}

func TestProviderModelCatalogOpenRouterFallbackUsesResolvedPreset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	restore := overrideModelCatalogHTTP(t, server.URL, "")
	defer restore()

	cfg := &AppConfig{Providers: map[string]StoredProvider{}, Agents: map[string]AgentConfig{
		string(agentCodex): {Providers: map[string]StoredProvider{"openrouter": {Model: "custom/openrouter-model"}}},
	}}
	catalog := providerModelCatalog(cfg, agentCodex, "openrouter", "test-key")
	if catalog.Source != "fallback" || catalog.Err == "" {
		t.Fatalf("unexpected catalog metadata: %+v", catalog)
	}
	if got := modelIDs(catalog); len(got) == 0 || got[0] != "custom/openrouter-model" {
		t.Fatalf("fallback ids = %v, want custom stored model first", got)
	}
}

func TestModelCatalogSecondaryTextAndInfoText(t *testing.T) {
	model := ProviderModelInfo{
		ID:            "model-a",
		Name:          "Model A",
		Description:   "fast model",
		ContextWindow: 128000,
		MaxOutput:     8192,
		InputPrice:    "0.10",
		OutputPrice:   "0.20",
		Capabilities:  []string{"tools", "json"},
		RawProvider:   "remote-provider",
	}
	secondary := modelCatalogSecondaryText(model)
	for _, want := range []string{"ctx 128000", "max 8192", "in 0.10", "out 0.20", "tools,json"} {
		if !strings.Contains(secondary, want) {
			t.Fatalf("secondary text %q does not contain %q", secondary, want)
		}
	}

	info := modelInfoText("openrouter", ProviderModelCatalog{Provider: "openrouter", Source: "remote", Models: []ProviderModelInfo{model}}, model.ID)
	for _, want := range []string{"Provider: openrouter", "ID: model-a", "Name: Model A", "Description: fast model", "Context window: 128000", "Max output: 8192", "Input price: 0.10", "Output price: 0.20", "Capabilities: tools, json", "Raw provider: remote-provider"} {
		if !strings.Contains(info, want) {
			t.Fatalf("info text missing %q in:\n%s", want, info)
		}
	}
}

func overrideModelCatalogHTTP(t *testing.T, openRouterURL, ollamaURL string) func() {
	t.Helper()
	oldClient := modelCatalogHTTPClient
	oldOpenRouterURL := modelCatalogOpenRouterURL
	oldOllamaURL := modelCatalogOllamaURL
	modelCatalogHTTPClient = &http.Client{Timeout: time.Second}
	if openRouterURL != "" {
		modelCatalogOpenRouterURL = openRouterURL
	}
	if ollamaURL != "" {
		modelCatalogOllamaURL = ollamaURL
	}
	return func() {
		modelCatalogHTTPClient = oldClient
		modelCatalogOpenRouterURL = oldOpenRouterURL
		modelCatalogOllamaURL = oldOllamaURL
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
