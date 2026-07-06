package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultProtocolRegistryRegistersBuiltInAdapters(t *testing.T) {
	reg := defaultProtocolRegistry()

	for _, tt := range []struct {
		name string
		path string
	}{
		{string(protocolAnthropicMessages), "/v1/messages"},
		{string(protocolOpenAIChat), "/v1/chat/completions"},
		{string(protocolOpenAIResponses), "/v1/responses"},
	} {
		adapter, ok := reg.Find(tt.name)
		if !ok {
			t.Fatalf("Find(%q) ok = false", tt.name)
		}
		if adapter.Name() != ProviderProtocol(tt.name) {
			t.Fatalf("adapter.Name() = %q, want %q", adapter.Name(), tt.name)
		}
		if adapter.UpstreamPath() != tt.path {
			t.Fatalf("%s UpstreamPath() = %q, want %q", tt.name, adapter.UpstreamPath(), tt.path)
		}
	}
}

func TestDefaultProtocolRegistryDiscoversInboundAdaptersByPath(t *testing.T) {
	reg := defaultProtocolRegistry()

	for _, tt := range []struct {
		path string
		want ProviderProtocol
	}{
		{"/v1/messages", protocolAnthropicMessages},
		{"/v1/chat/completions", protocolOpenAIChat},
		{"/v1/responses", protocolOpenAIResponses},
	} {
		adapter, ok := reg.FindInbound(http.MethodPost, tt.path)
		if !ok {
			t.Fatalf("FindInbound(POST, %q) ok = false", tt.path)
		}
		if adapter.Name() != tt.want {
			t.Fatalf("FindInbound(POST, %q) = %q, want %q", tt.path, adapter.Name(), tt.want)
		}
	}

	if _, ok := reg.FindInbound(http.MethodGet, "/v1/responses"); ok {
		t.Fatal("FindInbound(GET, /v1/responses) ok = true, want false")
	}
}

func TestResolveProxyProtocolUsesProtocolRegistry(t *testing.T) {
	reg := defaultProtocolRegistry()

	got, err := resolveProxyProtocolWithRegistry(" openai-chat ", reg)
	if err != nil {
		t.Fatalf("resolveProxyProtocolWithRegistry error: %v", err)
	}
	if got != protocolOpenAIChat {
		t.Fatalf("protocol = %q, want %q", got, protocolOpenAIChat)
	}

	if _, err := resolveProxyProtocolWithRegistry("missing", reg); err == nil {
		t.Fatal("expected unknown protocol error")
	}
}

func TestOpenAIChatAdapterWriteClientResponseReturnsChatCompletion(t *testing.T) {
	rec := httptest.NewRecorder()

	openAIChatAdapter{}.WriteClientResponse(rec, IRResponse{Text: "hi"}, false)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if !strings.Contains(rec.Body.String(), `"object":"chat.completion"`) || !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestProxyHandlerUsesInjectedRegistryForInboundAndUpstreamDiscovery(t *testing.T) {
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/custom" {
			t.Fatalf("upstream path = %q, want /v1/custom", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal upstream body: %v", err)
		}
		if got["model"] != "upstream-model" || got["prompt"] != "hello" {
			t.Fatalf("upstream body = %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"custom hi"}`))
	}))
	t.Cleanup(upstream.Close)

	registry := newProtocolRegistry(customProtocolAdapter{})
	handler := newProxyHandlerWithRegistry(ProxyRoute{
		Provider:         "custom",
		Model:            "upstream-model",
		UpstreamProtocol: "custom-protocol",
		UpstreamBaseURL:  upstream.URL,
	}, "", registry)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/custom", strings.NewReader(`{"model":"client-model","input":"hello"}`))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"text":"custom hi"}` {
		t.Fatalf("response body = %s", rec.Body.String())
	}
}

type customProtocolAdapter struct{}

func (customProtocolAdapter) Name() ProviderProtocol { return "custom-protocol" }
func (customProtocolAdapter) InboundMethod() string  { return http.MethodPost }
func (customProtocolAdapter) InboundPath() string    { return "/v1/custom" }
func (customProtocolAdapter) UpstreamPath() string   { return "/v1/custom" }
func (customProtocolAdapter) ParseInboundRequest(body []byte) (IRRequest, error) {
	var raw struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRRequest{}, err
	}
	return IRRequest{Model: raw.Model, Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: raw.Input}}}}}, nil
}
func (customProtocolAdapter) BuildUpstreamRequest(req IRRequest) ([]byte, error) {
	return json.Marshal(map[string]string{"model": req.Model, "prompt": req.Messages[0].Parts[0].Text})
}
func (customProtocolAdapter) ParseUpstreamResponse(body []byte, _ string) (IRResponse, error) {
	var raw struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRResponse{}, err
	}
	return IRResponse{Text: raw.Text}, nil
}
func (customProtocolAdapter) WriteClientResponse(w http.ResponseWriter, resp IRResponse, _ bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"text":"` + resp.Text + `"}`))
}
func (customProtocolAdapter) ConfigureUpstreamRequest(req *http.Request, providerKey string) {
	req.Header.Set("Content-Type", "application/json")
}
func (customProtocolAdapter) CanProxyFrom(ProtocolAdapter) (bool, string) { return true, "" }
