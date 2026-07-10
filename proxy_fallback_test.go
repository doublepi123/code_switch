package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsRetryableUpstreamError_returnsTrue_whenNetworkErrorOrRetryableStatus(t *testing.T) {
	// Given
	tests := []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{name: "network error", err: errors.New("dial failed"), want: true},
		{name: "500", status: http.StatusInternalServerError, want: true},
		{name: "429", status: http.StatusTooManyRequests, want: true},
		{name: "408", status: http.StatusRequestTimeout, want: true},
		{name: "200", status: http.StatusOK, want: false},
		{name: "302", status: http.StatusFound, want: false},
		{name: "400", status: http.StatusBadRequest, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			got := isRetryableUpstreamError(tt.status, tt.err)

			// Then
			if got != tt.want {
				t.Fatalf("isRetryableUpstreamError(%d, %v) = %v, want %v", tt.status, tt.err, got, tt.want)
			}
		})
	}
}

func TestProxyFallback_returnsFallbackResponse_whenPrimaryReturns500(t *testing.T) {
	// Given
	var primaryCalls int
	primary := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"primary failed"}`))
	}))
	t.Cleanup(primary.Close)
	fallback, fallbackCap := startAnthropicUpstream(t, 0, `{"id":"msg_fb","type":"message","role":"assistant","model":"fallback-model","content":[{"type":"text","text":"Fallback hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "primary",
		Model:            "primary-model",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  primary.URL,
		LocalToken:       "local-token",
		Fallback: &ProxyRoute{
			Provider:         "fallback",
			Model:            "fallback-model",
			UpstreamProtocol: protocolAnthropicMessages,
			UpstreamBaseURL:  fallback.URL,
			ProviderKey:      "fallback-provider-key",
		},
	}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if primaryCalls != 1 {
		t.Fatalf("primary calls = %d, want 1", primaryCalls)
	}
	if fallbackCap.path != "/v1/messages" {
		t.Fatalf("fallback path = %q, want /v1/messages", fallbackCap.path)
	}
	if !strings.Contains(rec.Body.String(), "Fallback hi") {
		t.Fatalf("response did not come from fallback: %s", rec.Body.String())
	}
}

func TestProxyFallback_usesPrimaryOnly_whenPrimaryReturns200(t *testing.T) {
	// Given
	primary, _ := startAnthropicUpstream(t, 0, `{"id":"msg_primary","type":"message","role":"assistant","model":"primary-model","content":[{"type":"text","text":"Primary hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	var fallbackCalls int
	fallback := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fallback.Close)
	handler := newProxyHandler(ProxyRoute{Provider: "primary", Model: "primary-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: primary.URL, LocalToken: "local-token", Fallback: &ProxyRoute{Provider: "fallback", Model: "fallback-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: fallback.URL}}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallbackCalls)
	}
	if !strings.Contains(rec.Body.String(), "Primary hi") {
		t.Fatalf("response did not come from primary: %s", rec.Body.String())
	}
}

func TestProxyFallback_doesNotCallFallback_whenPrimaryReturns400(t *testing.T) {
	// Given
	primary, _ := startAnthropicUpstream(t, http.StatusBadRequest, `{"error":"bad request"}`)
	var fallbackCalls int
	fallback := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fallback.Close)
	handler := newProxyHandler(ProxyRoute{Provider: "primary", Model: "primary-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: primary.URL, LocalToken: "local-token", Fallback: &ProxyRoute{Provider: "fallback", Model: "fallback-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: fallback.URL}}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback calls = %d, want 0", fallbackCalls)
	}
}

func TestProxyFallback_callsFallback_whenPrimaryNetworkError(t *testing.T) {
	// Given
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	fallback, fallbackCap := startAnthropicUpstream(t, 0, `{"id":"msg_fb","type":"message","role":"assistant","model":"fallback-model","content":[{"type":"text","text":"Fallback hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	handler := newProxyHandler(ProxyRoute{Provider: "primary", Model: "primary-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: deadURL, LocalToken: "local-token", Fallback: &ProxyRoute{Provider: "fallback", Model: "fallback-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: fallback.URL, ProviderKey: "fallback-provider-key"}}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if fallbackCap.path != "/v1/messages" {
		t.Fatalf("fallback path = %q, want /v1/messages", fallbackCap.path)
	}
}

func TestProxyFallback_doesNotSendPrimaryKeyToDifferentFallbackProvider_whenFallbackKeyMissing(t *testing.T) {
	// Given
	primary := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"primary failed"}`))
	}))
	t.Cleanup(primary.Close)
	fallbackCalled := false
	fallbackAuth := ""
	fallback := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalled = true
		fallbackAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_fb","type":"message","role":"assistant","model":"fallback-model","content":[{"type":"text","text":"Fallback hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(fallback.Close)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "primary-model",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  primary.URL,
		LocalToken:       "local-token",
		Fallback: &ProxyRoute{
			Provider:         "openrouter",
			Model:            "fallback-model",
			UpstreamProtocol: protocolAnthropicMessages,
			UpstreamBaseURL:  fallback.URL,
			ProviderKey:      "",
		},
	}, "primary-provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if fallbackAuth == "Bearer primary-provider-key" {
		t.Fatalf("fallback endpoint received primary provider key")
	}
	if fallbackCalled && rec.Code == http.StatusOK {
		t.Fatalf("fallback without its own key unexpectedly succeeded with auth %q", fallbackAuth)
	}
}

func TestProxyFallback_doesNotReusePrimaryKeyForDifferentProvider_whenFallbackURLMatchesPrimary(t *testing.T) {
	// Given
	var callCount int
	var fallbackAuth string
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"primary failed"}`))
			return
		}
		fallbackAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_fb","type":"message","role":"assistant","model":"fallback-model","content":[{"type":"text","text":"Fallback hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(upstream.Close)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "primary-model",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
		Fallback: &ProxyRoute{
			Provider:         "openrouter",
			Model:            "fallback-model",
			UpstreamProtocol: protocolAnthropicMessages,
			UpstreamBaseURL:  upstream.URL,
			ProviderKey:      "",
		},
	}, "primary-provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if fallbackAuth == "Bearer primary-provider-key" {
		t.Fatalf("same-URL fallback endpoint received primary provider key")
	}
	if callCount > 1 && rec.Code == http.StatusOK {
		t.Fatalf("different-provider same-URL fallback without key unexpectedly succeeded with auth %q", fallbackAuth)
	}
}

func TestProxyFallback_streamsFallbackResponse_whenPrimaryReturnsRetryableStatusBeforeStream(t *testing.T) {
	// Given
	var primaryCalls int
	primary := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"primary unavailable"}`))
	}))
	t.Cleanup(primary.Close)
	var fallbackCalls int
	fallback := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls++
		var body anthropicRequestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode fallback body: %v", err)
		}
		if !body.Stream {
			t.Fatalf("fallback stream = false, want true")
		}
		if body.Model != "fallback-model" {
			t.Fatalf("fallback model = %q, want fallback-model", body.Model)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicStreamFixture)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(fallback.Close)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "primary",
		Model:            "primary-model",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  primary.URL,
		LocalToken:       "local-token",
		Fallback: &ProxyRoute{
			Provider:         "fallback",
			Model:            "fallback-model",
			UpstreamProtocol: protocolAnthropicMessages,
			UpstreamBaseURL:  fallback.URL,
			ProviderKey:      "fallback-provider-key",
		},
	}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","stream":true,"input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if primaryCalls != 1 {
		t.Fatalf("primary calls = %d, want 1", primaryCalls)
	}
	if fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", fallbackCalls)
	}
	assertStreamText(t, decodeStreamFixture(t, protocolOpenAIResponses, rec.Body.String()), "Hello")
}

func TestProxyFallbackPassthrough_rewritesFallbackModel_whenPrimaryReturns500(t *testing.T) {
	// Given
	primary := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"primary failed"}`))
	}))
	t.Cleanup(primary.Close)
	fallback, fallbackCap := startAnthropicUpstream(t, 0, `{"id":"msg_fb","type":"message","role":"assistant","model":"fallback-model","content":[{"type":"text","text":"Fallback raw"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "primary",
		Model:            "primary-model",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  primary.URL,
		LocalToken:       "local-token",
		Fallback: &ProxyRoute{
			Provider:         "fallback",
			Model:            "fallback-model",
			UpstreamProtocol: protocolAnthropicMessages,
			UpstreamBaseURL:  fallback.URL,
			ProviderKey:      "fallback-provider-key",
		},
	}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"client-model","max_tokens":16,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	var upstreamBody map[string]any
	if err := json.Unmarshal(fallbackCap.body, &upstreamBody); err != nil {
		t.Fatalf("unmarshal fallback body: %v\nbody: %s", err, string(fallbackCap.body))
	}
	if got, _ := upstreamBody["model"].(string); got != "fallback-model" {
		t.Fatalf("fallback upstream model = %q, want fallback-model\nbody: %s", got, string(fallbackCap.body))
	}
}

func TestCmdProxyConfigurePersistsFallbackRoute(t *testing.T) {
	// Given
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-primary"}, nil, io.Discard); err != nil {
		t.Fatalf("set primary key: %v", err)
	}
	if err := runWithIO([]string{"set-key", "openrouter", "sk-fallback"}, nil, io.Discard); err != nil {
		t.Fatalf("set fallback key: %v", err)
	}

	// When
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--fallback-provider", "openrouter", "--fallback-url", "https://fallback.example/v1"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// Then
	cfg, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	fallback := cfg.Proxy.Routes["codex"].Fallback
	if fallback == nil {
		t.Fatal("fallback route was not persisted")
	}
	if fallback.Provider != "openrouter" {
		t.Fatalf("fallback provider = %q, want openrouter", fallback.Provider)
	}
	if fallback.UpstreamProtocol != string(protocolAnthropicMessages) {
		t.Fatalf("fallback protocol = %q, want %s", fallback.UpstreamProtocol, protocolAnthropicMessages)
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "local-token")
	if err != nil {
		t.Fatalf("build route: %v", err)
	}
	if route.Fallback == nil {
		t.Fatal("runtime fallback route is nil")
	}
	if route.Fallback.UpstreamBaseURL != "https://fallback.example/v1" {
		t.Fatalf("fallback URL = %q", route.Fallback.UpstreamBaseURL)
	}
}

func TestCmdProxyConfigureRejectsDifferentFallbackProviderWithoutAPIKey(t *testing.T) {
	// Given
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-primary"}, nil, io.Discard); err != nil {
		t.Fatalf("set primary key: %v", err)
	}

	// When
	err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--fallback-provider", "openrouter", "--fallback-url", "https://fallback.example/v1"}, nil, io.Discard)

	// Then
	if err == nil {
		t.Fatal("configure succeeded without fallback provider API key")
	}
	if !strings.Contains(err.Error(), "fallback provider \"openrouter\" has no API key") {
		t.Fatalf("error = %v, want missing fallback key message", err)
	}
}

func TestCmdProxyConfigureUsesPrimaryProvider_whenOnlyFallbackURLProvided(t *testing.T) {
	// Given
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-primary"}, nil, io.Discard); err != nil {
		t.Fatalf("set primary key: %v", err)
	}

	// When
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--fallback-url", "https://fallback.example/v1"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// Then
	cfg, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	fallback := cfg.Proxy.Routes["codex"].Fallback
	if fallback == nil {
		t.Fatal("fallback route was not persisted")
	}
	if fallback.Provider != "zhipu-cn" {
		t.Fatalf("fallback provider = %q, want primary provider zhipu-cn", fallback.Provider)
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "local-token")
	if err != nil {
		t.Fatalf("build route: %v", err)
	}
	if route.Fallback == nil || route.Fallback.UpstreamBaseURL != "https://fallback.example/v1" {
		t.Fatalf("runtime fallback = %#v, want URL override", route.Fallback)
	}
}
