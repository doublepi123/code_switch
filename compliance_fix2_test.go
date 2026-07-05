package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Issue 1 (Critical): an inbound /v1/messages request whose upstream protocol
// is openai-chat must be returned to the client in Anthropic Messages format
// (type:"message", content[0].type:"text"), NOT in OpenAI Responses format
// (object:"response", output_text). The proxy already supports Anthropic->Chat
// semantics for the request direction; the response direction must match.
// Non-stream only (Anthropic SSE upstream is not implemented in the MVP).
// =============================================================================

// TestProxyMessagesInboundOpenAIChatUpstreamReturnsAnthropicFormat: an
// Anthropic-shaped inbound request (/v1/messages) routed to an openai-chat
// upstream must come back as an Anthropic Messages response payload, not as
// an OpenAI Responses payload.
func TestProxyMessagesInboundOpenAIChatUpstreamReturnsAnthropicFormat(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "glm-5.2",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	// Upstream must still be hit on the chat/completions path.
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", cap.path)
	}

	// Response body must be the Anthropic Messages shape.
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, rec.Body.String())
	}
	if typ, _ := got["type"].(string); typ != "message" {
		t.Fatalf("response type = %q, want \"message\" (Anthropic shape)\nbody: %s", typ, rec.Body.String())
	}
	if obj, ok := got["object"]; ok {
		t.Fatalf("response must NOT carry OpenAI Responses \"object\" field, got %q\nbody: %s", obj, rec.Body.String())
	}
	content, _ := got["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1\nbody: %s", len(content), rec.Body.String())
	}
	block, _ := content[0].(map[string]any)
	if bt, _ := block["type"].(string); bt != "text" {
		t.Fatalf("content[0].type = %q, want \"text\"\nbody: %s", bt, rec.Body.String())
	}
	if txt, _ := block["text"].(string); txt != "Hi" {
		t.Fatalf("content[0].text = %q, want \"Hi\"\nbody: %s", txt, rec.Body.String())
	}
	// Must NOT contain the OpenAI Responses output_text field.
	if strings.Contains(rec.Body.String(), `"output_text"`) {
		t.Fatalf("response leaked OpenAI Responses shape:\n%s", rec.Body.String())
	}
}

// TestProxyResponsesInboundOpenAIChatUpstreamStillReturnsResponsesFormat: a
// /v1/responses inbound request routed to an openai-chat upstream must STILL
// come back as an OpenAI Responses payload (regression guard for issue 1).
func TestProxyResponsesInboundOpenAIChatUpstreamStillReturnsResponsesFormat(t *testing.T) {
	upstream, _ := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "glm-5.2",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"ignored","input":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, rec.Body.String())
	}
	if obj, _ := got["object"].(string); obj != "response" {
		t.Fatalf("response object = %q, want \"response\" (Responses shape)\nbody: %s", obj, rec.Body.String())
	}
}

// =============================================================================
// Issue 2 (Important): buildProxyRoute must inject cfg.ModelMappings[provider]
// into the returned ProxyRoute.ModelMappings so persisted mappings reach the
// proxy layer. Verified via a pure helper (no real daemon).
// =============================================================================

// TestBuildProxyRouteInjectsModelMappings: a provider with stored model
// mappings must yield a ProxyRoute whose ModelMappings mirror the config.
func TestBuildProxyRouteInjectsModelMappings(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-secret"},
		},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {
				"default":  "glm-5.2",
				"sonnet":   "glm-5.1",
				"haiku":    "glm-5",
			},
		},
	}
	preset := providerPresets["zhipu-cn"]
	route := buildProxyRoute("zhipu-cn", preset, protocolAnthropicMessages, "local-token", cfg.ModelMappings["zhipu-cn"])
	if route.Provider != "zhipu-cn" {
		t.Fatalf("route.Provider = %q, want zhipu-cn", route.Provider)
	}
	if len(route.ModelMappings) != 3 {
		t.Fatalf("ModelMappings len = %d, want 3 (got %#v)", len(route.ModelMappings), route.ModelMappings)
	}
	if route.ModelMappings["default"] != "glm-5.2" {
		t.Fatalf("ModelMappings[default] = %q, want glm-5.2", route.ModelMappings["default"])
	}
	if route.ModelMappings["sonnet"] != "glm-5.1" {
		t.Fatalf("ModelMappings[sonnet] = %q, want glm-5.1", route.ModelMappings["sonnet"])
	}
	// LocalToken must be propagated.
	if route.LocalToken != "local-token" {
		t.Fatalf("LocalToken = %q, want local-token", route.LocalToken)
	}
	// UpstreamProtocol must be propagated.
	if route.UpstreamProtocol != protocolAnthropicMessages {
		t.Fatalf("UpstreamProtocol = %q, want %q", route.UpstreamProtocol, protocolAnthropicMessages)
	}
	// Model must equal the preset's canonical model.
	if route.Model != preset.Model {
		t.Fatalf("Model = %q, want preset %q", route.Model, preset.Model)
	}
}

// TestBuildProxyRouteNilModelMappingsWhenAbsent: a provider with NO stored
// model mappings must yield a ProxyRoute whose ModelMappings is empty (so
// the proxy falls back to route.Model rather than panicking on a nil map).
func TestBuildProxyRouteNilModelMappingsWhenAbsent(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-secret"},
		},
	}
	preset := providerPresets["zhipu-cn"]
	route := buildProxyRoute("zhipu-cn", preset, protocolAnthropicMessages, "tok", cfg.ModelMappings["zhipu-cn"])
	if len(route.ModelMappings) != 0 {
		t.Fatalf("ModelMappings len = %d, want 0 when none stored", len(route.ModelMappings))
	}
}

// TestBuildProxyRouteDefensiveCopy: mutating the returned ModelMappings must
// NOT mutate the underlying cfg.ModelMappings (defensive copy).
func TestBuildProxyRouteDefensiveCopy(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-secret"},
		},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"default": "glm-5.2"},
		},
	}
	preset := providerPresets["zhipu-cn"]
	route := buildProxyRoute("zhipu-cn", preset, protocolAnthropicMessages, "tok", cfg.ModelMappings["zhipu-cn"])
	if route.ModelMappings == nil {
		t.Fatal("ModelMappings is nil; buildProxyRoute must return a populated map")
	}
	if len(route.ModelMappings) == 0 {
		t.Fatal("ModelMappings is empty; buildProxyRoute must copy the stored mappings")
	}
	route.ModelMappings["default"] = "tampered"
	// Underlying config must be unaffected.
	if cfg.ModelMappings["zhipu-cn"]["default"] != "glm-5.2" {
		t.Fatalf("buildProxyRoute did not defensive-copy: cfg mutated to %q",
			cfg.ModelMappings["zhipu-cn"]["default"])
	}
}

// =============================================================================
// Issue 3 (Important): `model set` / `use-model` must reuse
// validateProviderModel(provider, model) before persisting. In particular:
//   - opencode-go must reject models in unsupportedOpenCodeGoAnthropicModels.
//   - NoModel providers (e.g. kimi-coding) must reject default-model setting.
//   - Custom providers must NOT be rejected by validateProviderModel.
// =============================================================================

// TestCmdModelSetRejectsUnsupportedOpenCodeGoModel: a model that lives in
// unsupportedOpenCodeGoAnthropicModels must be rejected by `model set`.
func TestCmdModelSetRejectsUnsupportedOpenCodeGoModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model", "set", "opencode-go", "glm-5"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for unsupported opencode-go model, got nil\noutput: %s", out.String())
	}
	// Must mention both provider and model so the message is actionable.
	if !strings.Contains(err.Error(), "opencode-go") {
		t.Fatalf("error should mention provider, got: %v", err)
	}
	if !strings.Contains(err.Error(), "glm-5") {
		t.Fatalf("error should mention model, got: %v", err)
	}
}

// TestCmdModelSetRejectsNoModelProvider: a NoModel provider (kimi-coding)
// must reject setting a default model because it does not accept model
// selection at all.
func TestCmdModelSetRejectsNoModelProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model", "set", "kimi-coding", "some-model"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for NoModel provider, got nil\noutput: %s", out.String())
	}
	if !strings.Contains(err.Error(), "kimi-coding") {
		t.Fatalf("error should mention provider kimi-coding, got: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "model") {
		t.Fatalf("error should explain model selection is not accepted, got: %v", err)
	}
}

// TestCmdModelSetAllowsCustomProviderModel: a custom (non-preset) provider
// must NOT be rejected by validateProviderModel even though it is not in the
// preset table.
func TestCmdModelSetAllowsCustomProviderModel(t *testing.T) {
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
	if err := runWithIO([]string{"model", "set", "my-custom", "fancy-model"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model set custom: %v", err)
	}
	cfg := loadTestAppConfig(t)
	if got := cfg.Providers["my-custom"].Model; got != "fancy-model" {
		t.Fatalf("custom model = %q, want fancy-model", got)
	}
}

// TestCmdUseModelRejectsUnsupportedOpenCodeGoModel: use-model must apply the
// same validateProviderModel guard as model set.
func TestCmdUseModelRejectsUnsupportedOpenCodeGoModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"use-model", "opencode-go", "glm-5"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for unsupported opencode-go model, got nil\noutput: %s", out.String())
	}
}

// TestCmdUseModelRejectsNoModelProvider: use-model must reject a NoModel
// provider.
func TestCmdUseModelRejectsNoModelProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"use-model", "kimi-coding", "some-model"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for NoModel provider, got nil\noutput: %s", out.String())
	}
}

// =============================================================================
// Issue 4 (Important): responsesStreamToIR must tolerate CRLF line endings in
// the SSE wire format. Some upstreams/proxies emit frames separated by
// "\r\n\r\n" and lines separated by "\r\n"; the parser must still extract the
// completed response and the delta text.
// =============================================================================

// TestResponsesStreamToIRCRLFFrames: a SSE body whose frames are delimited by
// CRLFCRLF and whose lines are CRLF-terminated must parse identically to a
// plain LF body.
func TestResponsesStreamToIRCRLFFrames(t *testing.T) {
	// Identical payload to TestProxyHandlerClaudeMessagesToOpenAIResponsesStreamingUpstream
	// but with \r\n everywhere instead of \n.
	body := strings.Join([]string{
		"event: response.created\r",
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.5","output":[]}}`,
		"",
		"event: response.output_text.delta\r",
		`data: {"type":"response.output_text.delta","delta":"o"}`,
		"",
		"event: response.output_text.delta\r",
		`data: {"type":"response.output_text.delta","delta":"k"}`,
		"",
		"event: response.completed\r",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output_text":"ok","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`,
		"",
		"",
	}, "\r\n")

	resp, err := responsesStreamToIR([]byte(body))
	if err != nil {
		t.Fatalf("responsesStreamToIR CRLF: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q, want \"ok\"", resp.Text)
	}
	if resp.ID != "resp_1" {
		t.Fatalf("ID = %q, want resp_1", resp.ID)
	}
	if resp.Model != "gpt-5.5" {
		t.Fatalf("Model = %q, want gpt-5.5", resp.Model)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("Usage = %#v, want total 5", resp.Usage)
	}
}

// =============================================================================
// Issue 5 (Important): Anthropic inbound with max_tokens < 0 must return 400
// with a descriptive error, mirroring the existing
// responsesRequestToIR check on negative max_output_tokens.
// =============================================================================

// TestProxyAnthropicInboundNegativeMaxTokensRejected: a /v1/messages request
// whose max_tokens is negative must be rejected with HTTP 400 by the proxy
// (mirroring the existing max_output_tokens check for /v1/responses).
func TestProxyAnthropicInboundNegativeMaxTokensRejected(t *testing.T) {
	upstream, _ := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "glm-5.2",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude","max_tokens":-5,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for negative max_tokens\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "max_tokens") {
		t.Fatalf("error body should mention max_tokens, got: %s", rec.Body.String())
	}
}

// TestAnthropicRequestToIRNegativeMaxTokensRejected: the adapter-level check
// must also reject a negative max_tokens directly.
func TestAnthropicRequestToIRNegativeMaxTokensRejected(t *testing.T) {
	_, err := anthropicRequestToIR([]byte(`{"model":"claude","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`))
	if err == nil {
		t.Fatal("expected error for negative max_tokens, got nil")
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("error should mention max_tokens, got: %v", err)
	}
}
