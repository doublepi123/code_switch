package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadTestAppConfig reads the app config from the test HOME's
// ~/.code-switch/config.json into a fresh AppConfig and returns it. Used
// to assert that commands persisted the right state to disk.
func loadTestAppConfig(t *testing.T) *AppConfig {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	path := filepath.Join(home, ".code-switch", "config.json")
	cfg, err := loadAppConfigFrom(path)
	if err != nil {
		t.Fatalf("loadAppConfigFrom(%s): %v", path, err)
	}
	return cfg
}

// ---- model get/set/list ----

func TestCmdModelSetPersistsModelForPreset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model", "set", "zhipu-cn", "glm-5.2"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model set: %v\noutput: %s", err, out.String())
	}

	cfg := loadTestAppConfig(t)
	stored := cfg.Providers["zhipu-cn"]
	if stored.Model != "glm-5.2" {
		t.Fatalf("stored model = %q, want glm-5.2", stored.Model)
	}
	// set-key must remain unchanged (empty here).
	if stored.APIKey != "" {
		t.Fatalf("stored api key = %q, want empty (model set must not touch keys)", stored.APIKey)
	}
}

func TestCmdModelSetPreservesExistingAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed an api key first via set-key, then change the model.
	out := &bytes.Buffer{}
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-secret"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"model", "set", "zhipu-cn", "glm-5.2"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model set: %v", err)
	}

	cfg := loadTestAppConfig(t)
	stored := cfg.Providers["zhipu-cn"]
	if stored.Model != "glm-5.2" {
		t.Fatalf("model = %q, want glm-5.2", stored.Model)
	}
	if stored.APIKey != "sk-secret" {
		t.Fatalf("api key = %q, want sk-secret (model set must preserve keys)", stored.APIKey)
	}
}

func TestCmdModelSetRejectsUnknownProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model", "set", "does-not-exist", "glm-5.2"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for unknown provider, got nil\noutput: %s", out.String())
	}
}

func TestCmdModelSetWorksForExistingCustomProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(home, ".code-switch", "config.json")
	seed := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {Name: "My Custom", BaseURL: "https://example.com/api", Model: "old-model"},
		},
	}
	if err := writeJSONAtomic(cfgPath, seed); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model", "set", "my-custom", "new-model"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model set custom: %v", err)
	}

	cfg := loadTestAppConfig(t)
	if got := cfg.Providers["my-custom"].Model; got != "new-model" {
		t.Fatalf("custom stored model = %q, want new-model", got)
	}
	if got := cfg.Providers["my-custom"].BaseURL; got != "https://example.com/api" {
		t.Fatalf("custom base url clobbered = %q", got)
	}
}

func TestCmdModelGetFallsBackToPresetDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model", "get", "zhipu-cn"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model get: %v\noutput: %s", err, out.String())
	}
	// zhipu-cn preset default model is glm-5.2.
	if !strings.Contains(out.String(), "glm-5.2") {
		t.Fatalf("model get output = %q, want glm-5.2 default", out.String())
	}
}

func TestCmdModelGetReturnsStoredModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model", "set", "zhipu-cn", "glm-5.2-custom"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model set: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"model", "get", "zhipu-cn"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model get: %v", err)
	}
	if !strings.Contains(out.String(), "glm-5.2-custom") {
		t.Fatalf("model get output = %q, want stored glm-5.2-custom", out.String())
	}
}

func TestCmdModelGetCanonicalizesProviderAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	// "bigmodel" is an alias for zhipu-cn.
	if err := runWithIO([]string{"model", "get", "bigmodel"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model get alias: %v\noutput: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "glm-5.2") {
		t.Fatalf("alias model get output = %q, want glm-5.2", out.String())
	}
}

func TestCmdModelListPresetModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model", "list", "zhipu-cn"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model list: %v\noutput: %s", err, out.String())
	}
	body := out.String()
	if !strings.Contains(body, "glm-5.2") {
		t.Fatalf("model list output missing glm-5.2: %q", body)
	}
}

func TestCmdModelListCustomProviderWithoutModelsShowsCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := filepath.Join(home, ".code-switch", "config.json")
	seed := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {Name: "My Custom", BaseURL: "https://example.com/api", Model: "only-model"},
		},
	}
	if err := writeJSONAtomic(cfgPath, seed); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model", "list", "my-custom"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model list custom: %v\noutput: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "only-model") {
		t.Fatalf("model list custom should show current model only-model: %q", out.String())
	}
}

// ---- use-model ----

func TestCmdUseModelSetsDefaultModelAndMapping(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"use-model", "zhipu-cn", "glm-5.2"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("use-model: %v\noutput: %s", err, out.String())
	}

	cfg := loadTestAppConfig(t)
	if got := cfg.Providers["zhipu-cn"].Model; got != "glm-5.2" {
		t.Fatalf("default model = %q, want glm-5.2", got)
	}
	mappings := cfg.ModelMappings["zhipu-cn"]
	if mappings == nil {
		t.Fatalf("ModelMappings[zhipu-cn] is nil after use-model")
	}
	if got := mappings["default"]; got != "glm-5.2" {
		t.Fatalf("ModelMappings[zhipu-cn][default] = %q, want glm-5.2", got)
	}
}

func TestCmdUseModelRejectsUnknownProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"use-model", "ghost", "glm-5.2"}, strings.NewReader(""), out); err == nil {
		t.Fatalf("expected error for unknown provider, got nil\noutput: %s", out.String())
	}
}

// ---- model-map set/get/list/remove ----

func TestCmdModelMapSetGetListRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	set := func(args ...string) *bytes.Buffer {
		out := &bytes.Buffer{}
		if err := runWithIO(args, strings.NewReader(""), out); err != nil {
			t.Fatalf("%v: %v\noutput: %s", args, err, out.String())
		}
		return out
	}

	// set: sonnet -> glm-5.2
	set("model-map", "set", "zhipu-cn", "sonnet", "glm-5.2")
	cfg := loadTestAppConfig(t)
	if got := cfg.ModelMappings["zhipu-cn"]["sonnet"]; got != "glm-5.2" {
		t.Fatalf("after set sonnet: got %q, want glm-5.2", got)
	}

	// get: sonnet returns glm-5.2
	out := set("model-map", "get", "zhipu-cn", "sonnet")
	if !strings.Contains(out.String(), "glm-5.2") {
		t.Fatalf("model-map get sonnet = %q, want glm-5.2", out.String())
	}

	// set a second mapping
	set("model-map", "set", "zhipu-cn", "default", "glm-5.2")

	// list: shows both entries
	out = set("model-map", "list", "zhipu-cn")
	body := out.String()
	if !strings.Contains(body, "sonnet") || !strings.Contains(body, "glm-5.2") {
		t.Fatalf("model-map list missing entries: %q", body)
	}

	// remove: drops the sonnet mapping
	set("model-map", "remove", "zhipu-cn", "sonnet")
	cfg = loadTestAppConfig(t)
	if _, ok := cfg.ModelMappings["zhipu-cn"]["sonnet"]; ok {
		t.Fatalf("sonnet mapping still present after remove: %#v", cfg.ModelMappings["zhipu-cn"])
	}
	// default should still be present
	if got, ok := cfg.ModelMappings["zhipu-cn"]["default"]; !ok || got != "glm-5.2" {
		t.Fatalf("default mapping should survive remove of sonnet, got %#v", cfg.ModelMappings["zhipu-cn"])
	}
}

func TestCmdModelMapGetAllWhenNoClientModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	seed := &AppConfig{
		Providers: map[string]StoredProvider{},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"sonnet": "glm-5.2", "default": "glm-5.2"},
		},
	}
	cfgPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(cfgPath, seed); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model-map", "get", "zhipu-cn"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model-map get all: %v\noutput: %s", err, out.String())
	}
	body := out.String()
	if !strings.Contains(body, "sonnet") || !strings.Contains(body, "default") {
		t.Fatalf("model-map get (no client-model) should list all mappings: %q", body)
	}
}

func TestCmdModelMapRemoveMissingProviderIsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model-map", "remove", "zhipu-cn", "sonnet"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error removing mapping for provider with no mappings")
	}
}

// ---- proxy model mapping ----

func TestProxyHandlerMapsIncomingModelByName(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider: "zhipu-cn",
		Model:    "fallback-model",
		ModelMappings: map[string]string{
			"sonnet":  "glm-5.2",
			"default": "glm-5.2",
		},
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	// Client requests "sonnet"; the proxy should map it to glm-5.2 upstream.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"sonnet","input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(cap.body, &got); err != nil {
		t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, string(cap.body))
	}
	if m, _ := got["model"].(string); m != "glm-5.2" {
		t.Fatalf("upstream model = %q, want glm-5.2 (mapped)\nbody: %s", m, string(cap.body))
	}
}

func TestProxyHandlerDefaultFallbackWhenMappingMisses(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider: "zhipu-cn",
		Model:    "fallback-model",
		ModelMappings: map[string]string{
			"default": "glm-5.2",
		},
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	// Client requests an unmapped model; should fall back to "default".
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"some-unknown-model","input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(cap.body, &got); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if m, _ := got["model"].(string); m != "glm-5.2" {
		t.Fatalf("upstream model = %q, want glm-5.2 (default fallback)", m)
	}
}

func TestProxyHandlerFallsBackToRouteModelWithoutMappings(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "route-model",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"whatever","input":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(cap.body, &got); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if m, _ := got["model"].(string); m != "route-model" {
		t.Fatalf("upstream model = %q, want route-model (no mappings: route.Model)", m)
	}
}
