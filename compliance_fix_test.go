package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// =============================================================================
// Issue 1 (Critical): an incoming empty model must fall back to the route's
// mappings / route.Model, rather than being rejected by the inbound adapter
// before the proxy's model resolution runs.
// =============================================================================

// TestProxyResponsesEmptyModelFallsBackToDefaultMapping: a /v1/responses
// request with NO "model" field should be accepted when the route has a
// "default" mapping; the upstream must receive the default-mapped model.
func TestProxyResponsesEmptyModelFallsBackToDefaultMapping(t *testing.T) {
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

	// No "model" field on the request body.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"input":[{"role":"user","content":"hi"}]}`))
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
		t.Fatalf("upstream model = %q, want glm-5.2 (default fallback for empty model)", m)
	}
}

// TestProxyResponsesExplicitEmptyModelFallsBackToDefaultMapping: a
// /v1/responses request with an explicitly empty "model":"" field should
// behave identically to a missing field.
func TestProxyResponsesExplicitEmptyModelFallsBackToDefaultMapping(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"","input":[{"role":"user","content":"hi"}]}`))
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
		t.Fatalf("upstream model = %q, want glm-5.2 (explicit empty model falls back to default mapping)", m)
	}
}

// TestProxyResponsesEmptyModelFallsBackToRouteModel: when there are no
// mappings at all, an empty incoming model should fall through to
// route.Model.
func TestProxyResponsesEmptyModelFallsBackToRouteModel(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "route-model",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"input":[{"role":"user","content":"hi"}]}`))
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
		t.Fatalf("upstream model = %q, want route-model (no mappings: route.Model fallback for empty model)", m)
	}
}

// TestProxyAnthropicEmptyModelFallsBackToDefaultMapping: a /v1/messages
// request (Anthropic inbound) with NO "model" field should also fall back
// to default mapping when configured. The Anthropic inbound adapter is
// exercised by routing to an OpenAI-chat upstream (Anthropic->Anthropic
// passthrough is not implemented in the MVP).
func TestProxyAnthropicEmptyModelFallsBackToDefaultMapping(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider: "minimax-cn",
		Model:    "fallback-model",
		ModelMappings: map[string]string{
			"default": "MiniMax-M3",
		},
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	// No "model" field on the Anthropic request body.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
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
	if m, _ := got["model"].(string); m != "MiniMax-M3" {
		t.Fatalf("upstream model = %q, want MiniMax-M3 (default fallback for empty Anthropic model)", m)
	}
}

// TestProxyAnthropicExplicitEmptyModelFallsBackToRouteModel: Anthropic
// inbound with an explicit empty model and no mappings should fall through
// to route.Model.
func TestProxyAnthropicExplicitEmptyModelFallsBackToRouteModel(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "route-model",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
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
		t.Fatalf("upstream model = %q, want route-model (no mappings: route.Model fallback for empty Anthropic model)", m)
	}
}

// =============================================================================
// Issue 2 (Important): model-map get/list/remove must validate the provider
// exists (canonicalized) before operating on mappings.
// =============================================================================

func TestCmdModelMapGetRejectsUnknownProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model-map", "get", "ghost-provider"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for unknown provider on model-map get, got nil\noutput: %s", out.String())
	}
	if !strings.Contains(err.Error(), "ghost-provider") {
		t.Fatalf("error should mention unknown provider, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error should say unsupported provider, got: %v", err)
	}
}

func TestCmdModelMapListRejectsUnknownProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model-map", "list", "ghost-provider"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for unknown provider on model-map list, got nil\noutput: %s", out.String())
	}
	if !strings.Contains(err.Error(), "ghost-provider") {
		t.Fatalf("error should mention unknown provider, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error should say unsupported provider, got: %v", err)
	}
}

func TestCmdModelMapRemoveRejectsUnknownProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model-map", "remove", "ghost-provider", "sonnet"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for unknown provider on model-map remove, got nil\noutput: %s", out.String())
	}
	if !strings.Contains(err.Error(), "ghost-provider") {
		t.Fatalf("error should mention unknown provider, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error should say unsupported provider, got: %v", err)
	}
}

// A known provider with mappings should still work after the validation
// gate is added — this protects the happy path while the previous tests
// exercise the rejection path.
func TestCmdModelMapGetAcceptsKnownProviderAfterValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model-map", "set", "zhipu-cn", "sonnet", "glm-5.2"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model-map set: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"model-map", "get", "zhipu-cn", "sonnet"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model-map get on known provider: %v\noutput: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "glm-5.2") {
		t.Fatalf("model-map get on known provider should still return mapping: %q", out.String())
	}
}

// =============================================================================
// Issue 4 (Minor): model get/list must reject extra positional args;
// model-map get <provider> "" must reject an empty client-model.
// =============================================================================

func TestCmdModelGetRejectsExtraArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model", "get", "zhipu-cn", "extra"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for extra args on model get, got nil\noutput: %s", out.String())
	}
}

func TestCmdModelListRejectsExtraArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model", "list", "zhipu-cn", "extra"}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for extra args on model list, got nil\noutput: %s", out.String())
	}
}

func TestCmdModelMapGetRejectsEmptyClientModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := &bytes.Buffer{}
	err := runWithIO([]string{"model-map", "get", "zhipu-cn", ""}, strings.NewReader(""), out)
	if err == nil {
		t.Fatalf("expected error for empty client-model on model-map get, got nil\noutput: %s", out.String())
	}
}

// =============================================================================
// Issue 5 (Minor): a custom provider with no stored model must NOT print
// the synthetic "custom-model" placeholder as if it were a real model;
// it should print "(no models available)".
// =============================================================================

func TestCmdModelListCustomProviderNoModelShowsPlaceholder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgPath := home + "/.code-switch/config.json"
	seed := &AppConfig{
		Providers: map[string]StoredProvider{
			"my-custom": {Name: "My Custom", BaseURL: "https://example.com/api"}, // no Model
		},
	}
	if err := writeJSONAtomic(cfgPath, seed); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"model", "list", "my-custom"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("model list custom no model: %v\noutput: %s", err, out.String())
	}
	body := out.String()
	if strings.Contains(body, "custom-model") {
		t.Fatalf("model list should NOT show the synthetic custom-model placeholder: %q", body)
	}
	if !strings.Contains(body, "(no models available)") {
		t.Fatalf("model list should print (no models available): %q", body)
	}
}

// =============================================================================
// Issue 3 (Important): bash completion must allow model|model-map to reach
// their subcommand branch. The bash completion script's case arms must not
// shadow the model|model-map subcommand suggestions with the earlier
// provider-list arm.
// =============================================================================

func TestBashCompletionModelAndModelMapReachable(t *testing.T) {
	out := &bytes.Buffer{}
	if err := cmdCompletion([]string{"bash"}, out); err != nil {
		t.Fatalf("cmdCompletion bash: %v", err)
	}
	script := out.String()

	// The bash completion script must contain distinct branches that
	// offer the subcommand suggestions for both `model` and `model-map`.
	// `model` supports get/set/list; `model-map` supports
	// set/get/list/remove.
	if !strings.Contains(script, `model)`) {
		t.Fatalf("bash completion missing model subcommand arm\nscript:\n%s", script)
	}
	if !strings.Contains(script, `model-map)`) {
		t.Fatalf("bash completion missing model-map subcommand arm\nscript:\n%s", script)
	}
	if !strings.Contains(script, `$(compgen -W "get set list"`) {
		t.Fatalf("bash completion missing get set list suggestions for model\nscript:\n%s", script)
	}
	if !strings.Contains(script, `$(compgen -W "set get list remove"`) {
		t.Fatalf("bash completion missing set get list remove suggestions for model-map\nscript:\n%s", script)
	}

	// The bug: when the first case arm in the cword==2 case matches
	// `switch|set-key|...|model|model-map|use-model|diff` and emits the
	// provider list, AND a later arm matches `model|model-map` and emits
	// the subcommands, the later arm is unreachable in shell `case`
	// evaluation (first match wins). The fix removes model|model-map
	// from the provider-list arm.
	//
	// We assert: in the provider-list arm, the alternation must NOT
	// include "model" or "model-map" — otherwise the subcommand arm is
	// shadowed and bash completion of `cs model <TAB>` would suggest
	// providers instead of "get/set/list".
	for _, leaked := range []string{
		`|model|model-map|use-model|diff)`,
		`|model-map|use-model|diff)`,
		`|model|use-model|diff)`,
	} {
		if strings.Contains(script, leaked) {
			t.Fatalf("bash completion provider-list arm must not include model/model-map (would shadow subcommand arm); found %q\nscript:\n%s", leaked, script)
		}
	}
}
