package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// startAnthropicUpstream returns a test server that mimics an Anthropic
// Messages API endpoint. It records the last request it received so the
// caller can assert on the upstream path, headers, and body. The
// upstream always answers 200 with a single text content block carrying
// "Hi" unless a custom status/respBody is supplied.
func startAnthropicUpstream(t *testing.T, wantStatus int, respBody string) (*httptest.Server, *upstreamCapture) {
	t.Helper()
	if wantStatus == 0 {
		wantStatus = http.StatusOK
	}
	if respBody == "" {
		respBody = `{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`
	}
	cap := &upstreamCapture{}
	srv := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.method = r.Method
		cap.auth = r.Header.Get("Authorization")
		cap.xAPIKey = r.Header.Get("x-api-key")
		cap.contentType = r.Header.Get("Content-Type")
		cap.anthropicVersion = r.Header.Get(proxyAnthropicVersionHeader)
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(wantStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func startOpenAIChatUpstream(t *testing.T, wantStatus int, respBody string) (*httptest.Server, *upstreamCapture) {
	t.Helper()
	if wantStatus == 0 {
		wantStatus = http.StatusOK
	}
	if respBody == "" {
		respBody = `{"id":"chatcmpl_1","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`
	}
	cap := &upstreamCapture{}
	srv := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.method = r.Method
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(wantStatus)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

// upstreamCapture records the last upstream request observed by a test
// server. Fields are populated via pointer indirection from the handler.
type upstreamCapture struct {
	path             string
	method           string
	auth             string
	xAPIKey          string
	contentType      string
	anthropicVersion string
	body             []byte
}

func TestProxyHandlerAnthropicUpstreamUsesXAPIKeyWhenProviderHasNoAuthEnv(t *testing.T) {
	upstream, cap := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "opencode-go",
		Model:            "minimax-m2.7",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.auth != "" {
		t.Fatalf("upstream authorization = %q, want empty", cap.auth)
	}
	if cap.xAPIKey != "provider-key" {
		t.Fatalf("upstream x-api-key = %q, want provider-key", cap.xAPIKey)
	}
}

func TestProxyHandlerHappyPath(t *testing.T) {
	upstream, cap := startAnthropicUpstream(t, 0, "")

	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	reqBody := `{"model":"codex-model","input":"Say hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer local-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// Upstream should have been called on /v1/messages with the
	// provider's bearer token, and the request body model should have
	// been overridden to the route's Model.
	if cap.path != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", cap.path)
	}
	if cap.method != http.MethodPost {
		t.Fatalf("upstream method = %q, want POST", cap.method)
	}
	if cap.auth != "Bearer provider-key" {
		t.Fatalf("upstream auth = %q, want Bearer provider-key", cap.auth)
	}
	if cap.contentType != "application/json" {
		t.Fatalf("upstream content-type = %q, want application/json", cap.contentType)
	}
	if cap.anthropicVersion != proxyAnthropicVersionValue {
		t.Fatalf("upstream %q header = %q, want %q",
			proxyAnthropicVersionHeader, cap.anthropicVersion, proxyAnthropicVersionValue)
	}

	var got map[string]any
	if err := json.Unmarshal(cap.body, &got); err != nil {
		t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, string(cap.body))
	}
	if m, _ := got["model"].(string); m != "MiniMax-M3" {
		t.Fatalf("upstream body model = %q, want MiniMax-M3 (route override)\nbody: %s",
			m, string(cap.body))
	}

	// Response must contain the assistant text translated into the
	// Responses API shape.
	var resp responsesRawResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, rec.Body.String())
	}
	if resp.OutputText != "Hi" {
		t.Fatalf("output_text = %q, want Hi\nbody: %s", resp.OutputText, rec.Body.String())
	}
	if resp.Object != "response" {
		t.Fatalf("object = %q, want response", resp.Object)
	}
}

func TestProxyHandlerOpenAIChatUpstream(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "openai-compatible",
		Model:            "glm-5.2",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"ignored","instructions":"be brief","input":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", cap.path)
	}
	if cap.auth != "Bearer provider-key" {
		t.Fatalf("upstream auth = %q, want Bearer provider-key", cap.auth)
	}
	var upstreamBody map[string]any
	if err := json.Unmarshal(cap.body, &upstreamBody); err != nil {
		t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, string(cap.body))
	}
	if got, _ := upstreamBody["model"].(string); got != "glm-5.2" {
		t.Fatalf("upstream model = %q", got)
	}
	messages, _ := upstreamBody["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2; body=%s", len(messages), string(cap.body))
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Fatalf("first message = %#v", first)
	}
	var resp responsesRawResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, rec.Body.String())
	}
	if resp.OutputText != "Hi" {
		t.Fatalf("output_text = %q", resp.OutputText)
	}
}

func TestProxyHandlerClaudeMessagesToOpenAIResponsesUpstream(t *testing.T) {
	cap := &upstreamCapture{}
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.method = r.Method
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"glm-5.2","output_text":"Hi","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{Provider: "responses-upstream", Model: "glm-5.2", UpstreamProtocol: protocolOpenAIResponses, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","system":"be brief","max_tokens":16,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", cap.path)
	}
	if cap.auth != "Bearer provider-key" {
		t.Fatalf("auth = %q", cap.auth)
	}
	var upstreamBody map[string]any
	if err := json.Unmarshal(cap.body, &upstreamBody); err != nil {
		t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, string(cap.body))
	}
	if got := upstreamBody["model"]; got != "glm-5.2" {
		t.Fatalf("upstream model = %v", got)
	}
	if got := upstreamBody["instructions"]; got != "be brief" {
		t.Fatalf("instructions = %v", got)
	}
	if !strings.Contains(rec.Body.String(), `"text":"Hi"`) || !strings.Contains(rec.Body.String(), `"type":"message"`) {
		t.Fatalf("anthropic response body = %s", rec.Body.String())
	}
}

func TestProxyHandlerClaudeMessagesToOpenAIResponsesNonStreamingUpstream(t *testing.T) {
	cap := &upstreamCapture{}
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\n")
		_, _ = io.WriteString(w, `data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.5","output":[]}}`+"\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"o"}`+"\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, `data: {"type":"response.output_text.delta","delta":"k"}`+"\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output_text":"ok","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`+"\n\n")
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{Provider: "responses-upstream", Model: "gpt-5.5", UpstreamProtocol: protocolOpenAIResponses, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	var upstreamBody map[string]any
	if err := json.Unmarshal(cap.body, &upstreamBody); err != nil {
		t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, string(cap.body))
	}
	if _, ok := upstreamBody["stream"]; ok {
		t.Fatalf("upstream stream field present, want non-streaming request; body=%s", string(cap.body))
	}
	if !strings.Contains(rec.Body.String(), `"text":"ok"`) {
		t.Fatalf("anthropic response body = %s", rec.Body.String())
	}
}

func TestProxyHandlerClaudeMessagesRejectsUnsupportedResponsesUpstreamPathVersion(t *testing.T) {
	cap := &upstreamCapture{}
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"glm-5.2","output_text":"Hi"}`))
	}))
	t.Cleanup(upstream.Close)
	handler := newProxyHandler(ProxyRoute{Provider: "responses-upstream", Model: "glm-5.2", UpstreamProtocol: protocolOpenAIResponses, UpstreamBaseURL: upstream.URL + "/api/paas/v4", LocalToken: "local-token"}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/api/paas/v4/responses" {
		t.Fatalf("upstream path = %q, want /api/paas/v4/responses", cap.path)
	}
}

func TestProxyHandlerOpenAIChatUpstreamRespectsVersionedBaseURL(t *testing.T) {
	upstream, cap := startOpenAIChatUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "zhipu-openai",
		Model:            "glm-5.2",
		UpstreamProtocol: protocolOpenAIChat,
		UpstreamBaseURL:  upstream.URL + "/api/paas/v4",
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"ignored","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/api/paas/v4/chat/completions" {
		t.Fatalf("upstream path = %q, want /api/paas/v4/chat/completions", cap.path)
	}
}

func TestProxyHandlerRejectsWrongLocalToken(t *testing.T) {
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "local token") {
		t.Fatalf("error body = %q, want mention local token", rec.Body.String())
	}
}

func TestProxyHandlerRejectsMissingToken(t *testing.T) {
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for missing token", rec.Code)
	}
}

func TestProxyHandlerAllowsMissingLocalTokenWhenUnset(t *testing.T) {
	upstream, cap := startAnthropicUpstream(t, 0, "")
	// LocalToken empty: no auth check is performed, and the upstream
	// request must also omit the Authorization header (because
	// providerKey is empty too here).
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "",
	}, "")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when no auth configured\nbody: %s",
			rec.Code, rec.Body.String())
	}
	if cap.auth != "" {
		t.Fatalf("upstream auth = %q, want empty when no providerKey", cap.auth)
	}
}

func TestProxyHandlerRejectsUnsupportedPath(t *testing.T) {
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/not-supported",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unsupported path", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/v1/responses") {
		t.Fatalf("error body = %q, want mention /v1/responses", rec.Body.String())
	}
}

func TestProxyHandlerRejectsWrongMethod(t *testing.T) {
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 for GET", rec.Code)
	}
}

// TestProxyHandlerStreamTrueReturnsSSE verifies that a stream:true request is
// forwarded upstream as streaming SSE and converted back to the inbound OpenAI
// Responses SSE shape one event at a time.
func TestProxyHandlerStreamTrueReturnsSSE(t *testing.T) {
	cap := &upstreamCapture{}
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicStreamFixture)
	}))
	t.Cleanup(upstream.Close)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for stream:true\nbody: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream*", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q\nbody:\n%s", want, body)
		}
	}
	// The upstream text deltas must appear in the converted stream.
	if !strings.Contains(body, `"delta":"Hel"`) || !strings.Contains(body, `"delta":"lo"`) {
		t.Fatalf("SSE body missing upstream text in delta\nbody:\n%s", body)
	}
	// Stage 3 forwards stream:true to the upstream instead of buffering a
	// non-streaming response client-side.
	if !strings.Contains(string(cap.body), `"stream":true`) {
		t.Fatalf("upstream body should carry stream:true: %s", string(cap.body))
	}
}

func TestProxyHandlerStreamingIgnoresNonStreamingHTTPClientTimeout(t *testing.T) {
	originalClient := proxyHTTPClient
	proxyHTTPClient = &http.Client{Timeout: 20 * time.Millisecond}
	t.Cleanup(func() { proxyHTTPClient = originalClient })

	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":7,"output_tokens":0}}}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "event: content_block_start\n"+
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\n"+
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`+"\n\n"+
			"event: content_block_stop\n"+
			`data: {"type":"content_block_stop","index":0}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
			"event: message_stop\n"+
			`data: {"type":"message_stop"}`+"\n\n")
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for stream:true\nbody: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: response.completed") {
		t.Fatalf("streaming request was cut off before completion; body:\n%s", body)
	}
	if strings.Contains(body, "context deadline exceeded") || strings.Contains(body, "Client.Timeout") {
		t.Fatalf("streaming body contains client timeout error:\n%s", body)
	}
}

func TestProxyHandlerStreamingIgnoresServerWriteTimeout(t *testing.T) {
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\n"+
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":7,"output_tokens":0}}}`+"\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, "event: content_block_start\n"+
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\n"+
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`+"\n\n"+
			"event: content_block_stop\n"+
			`data: {"type":"content_block_stop","index":0}`+"\n\n"+
			"event: message_delta\n"+
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
			"event: message_stop\n"+
			`data: {"type":"message_stop"}`+"\n\n")
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")
	proxy := httptest.NewUnstartedServer(handler)
	proxy.Config.WriteTimeout = 20 * time.Millisecond
	proxy.Start()
	t.Cleanup(proxy.Close)

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer local-token")
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read streaming response: %v", err)
	}
	body := string(bodyBytes)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for stream:true\nbody: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "event: response.completed") {
		t.Fatalf("server WriteTimeout cut off streaming response before completion; body:\n%s", body)
	}
}

func TestProxyHandlerRejectsMalformedBody(t *testing.T) {
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{not-json`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed body", rec.Code)
	}
}

func TestProxyHandlerPassesThroughUpstreamError(t *testing.T) {
	// Upstream returns 500 with a provider-shaped error body. The
	// proxy must forward the same status and body verbatim rather
	// than swallowing it into a generic error.
	anthropicErr := `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	upstream, _ := startAnthropicUpstream(t, http.StatusInternalServerError, anthropicErr)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 passthrough", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != strings.TrimSpace(anthropicErr) {
		t.Fatalf("body = %q, want passthrough %q", got, anthropicErr)
	}
}

func TestProxyHandlerPassesThroughUpstream400(t *testing.T) {
	// Upstream returns 400 (e.g. invalid model). The proxy must
	// forward that status and body too, since the upstream is the
	// authority on whether the request is acceptable.
	anthropicErr := `{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`
	upstream, _ := startAnthropicUpstream(t, http.StatusBadRequest, anthropicErr)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 passthrough", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bad model") {
		t.Fatalf("body = %q, want upstream error text", rec.Body.String())
	}
}

func TestProxyHandlerRejectsUnsupportedUpstreamProtocol(t *testing.T) {
	// No upstream server needed: the protocol check happens before
	// any network call.
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: "totally-bogus",
		UpstreamBaseURL:  "http://example.invalid",
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unsupported upstream protocol", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "totally-bogus") {
		t.Fatalf("error body = %q, want mention bogus protocol", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), string(protocolAnthropicMessages)) || !strings.Contains(rec.Body.String(), string(protocolOpenAIChat)) {
		t.Fatalf("error body = %q, want mention supported protocols", rec.Body.String())
	}
}

func TestProxyHandlerAcceptsBaseURLWithTrailingSlash(t *testing.T) {
	upstream, cap := startAnthropicUpstream(t, 0, "")
	// Trailing slash on the configured base URL must be normalised
	// away so the upstream path is /v1/messages, not //v1/messages.
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL + "/",
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages (trailing slash normalisation)", cap.path)
	}
}

func TestProxyHandlerEmptyRouteModelSurfacesAs400(t *testing.T) {
	// Spec compliance: route.Model unconditionally overrides the IR
	// model. When the route is misconfigured with an empty Model, the
	// IR model becomes empty and the Anthropic adapter's
	// ValidateTextOnly rejects it, surfacing as a 400 to the client
	// rather than silently passing the client's model through.
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"client-model","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty route.Model\nbody: %s",
			rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "model must not be empty") {
		t.Fatalf("error body = %q, want mention of empty model", body)
	}
}

func TestProxyHandlerRejectsUnsupportedTopLevelField(t *testing.T) {
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	// temperature is not supported in the MVP; the proxy must surface a
	// 400 rather than silently dropping the field. (tools/tool_choice
	// are now accepted-and-ignored — see the dedicated acceptance test.)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi","temperature":0.7}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unsupported temperature field", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "temperature") {
		t.Fatalf("error body = %q, want mention temperature", rec.Body.String())
	}
}

func TestProxyHandlerErrorCodeIsJSON(t *testing.T) {
	// Error responses must always be valid JSON regardless of which
	// stage produced them, so clients can parse them uniformly.
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{bad`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var e map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
}

func TestProxyHandlerBearerSchemeIsCaseInsensitive(t *testing.T) {
	// RFC 7235: the auth scheme is case-insensitive. A "bearer"
	// (lowercase) prefix must still authenticate.
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for lowercase bearer scheme\nbody: %s",
			rec.Code, rec.Body.String())
	}
}

func TestVerifyBearerToken(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
		ok     bool
	}{
		{"exact match", "Bearer abc", "abc", true},
		{"lowercase scheme", "bearer abc", "abc", true},
		{"mixed case scheme", "BeArEr abc", "abc", true},
		// The token itself is matched exactly — no trimming. Trailing
		// whitespace on the token must be rejected (strings.Fields would
		// otherwise strip it), and whitespace embedded inside the token
		// shows up as a third field and is also rejected.
		{"token with surrounding whitespace", "Bearer  abc ", "abc", false},
		{"token with trailing whitespace only", "Bearer abc ", "abc", false},
		{"token split across fields", "Bearer abc def", "abc", false},
		// strings.Fields collapses runs of whitespace between scheme and
		// token, so a single space vs. a tab vs. multiple spaces between
		// them is still accepted as long as there are exactly two fields.
		{"multiple spaces between scheme and token", "Bearer    abc", "abc", true},
		{"tab between scheme and token", "Bearer\tabc", "abc", true},
		{"wrong token", "Bearer xyz", "abc", false},
		{"missing header", "", "abc", false},
		{"no scheme", "abc", "abc", false},
		{"basic scheme", "Basic abc", "abc", false},
		{"empty token", "Bearer ", "abc", false},
		// An empty expected token must never authenticate, even if the
		// client also sends an empty bearer token.
		{"empty want rejects empty presented token", "Bearer ", "", false},
		{"empty want rejects nonempty token", "Bearer abc", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			if got := verifyBearerToken(req, c.want); got != c.ok {
				t.Fatalf("verifyBearerToken = %v, want %v", got, c.ok)
			}
		})
	}
}

// TestProxyHandlerSendsAnthropicVersionHeader asserts that the proxy
// always sets the anthropic-version header on upstream requests, even
// when the client omits it. Anthropic's API rejects requests that lack
// this header, so the proxy must inject it explicitly.
func TestProxyHandlerSendsAnthropicVersionHeader(t *testing.T) {
	upstream, cap := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "",
	}, "")

	// The client deliberately omits anthropic-version; the proxy must
	// still set it on the upstream call.
	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.anthropicVersion != proxyAnthropicVersionValue {
		t.Fatalf("upstream %q header = %q, want %q (proxy must inject it)",
			proxyAnthropicVersionHeader, cap.anthropicVersion, proxyAnthropicVersionValue)
	}
}

// TestProxyHandlerRejectsOversizedRequestBody verifies the client
// request body size limit. A request larger than proxyMaxRequestBodyBytes
// must be rejected before the upstream is ever contacted.
func TestProxyHandlerRejectsOversizedRequestBody(t *testing.T) {
	// Use an upstream that would fail the test if reached: it always
	// returns 500 so a misconfigured limit surfaces as a clear failure
	// rather than a silent 200.
	upstream, _ := startAnthropicUpstream(t, http.StatusInternalServerError, `{"oops":1}`)
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "",
	}, "")

	// Build a body that is one byte larger than the limit. The input
	// field is a single giant string so the JSON is otherwise valid
	// (apart from exceeding the cap). The limit is enforced before
	// parsing so we never have to construct a fully valid payload of
	// that size.
	over := proxyMaxRequestBodyBytes + 1
	// {"model":"m","input":"AAAA..."}: header is 22 bytes, plus quotes
	// around the string plus the closing brace, but we don't need to be
	// precise: any body strictly larger than the cap is rejected.
	pad := over - 22
	body := make([]byte, 0, over)
	body = append(body, []byte(`{"model":"m","input":"`)...)
	for i := 0; i < pad; i++ {
		body = append(body, 'A')
	}
	body = append(body, []byte(`"}`)...)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// MaxBytesReader surfaces as http.MaxBytesError, which the handler
	// maps to a 400. The exact status is not load-bearing on the
	// client contract; what matters is that the upstream was never
	// reached and the request was rejected.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversized request body\nbody: %s",
			rec.Code, rec.Body.String())
	}
}

// TestProxyHandlerRejectsOversizedUpstreamBody verifies the upstream
// response body size limit. An upstream that returns more than
// proxyMaxUpstreamBodyBytes must surface as a 502 rather than being
// buffered indefinitely.
func TestProxyHandlerRejectsOversizedUpstreamBody(t *testing.T) {
	// Build a response larger than the cap. We don't need valid JSON
	// because the size check runs before parsing.
	big := make([]byte, proxyMaxUpstreamBodyBytes+8)
	for i := range big {
		big[i] = 'A'
	}
	upstream, _ := startAnthropicUpstream(t, http.StatusOK, string(big))
	handler := newProxyHandler(ProxyRoute{
		Provider:         "minimax-cn",
		Model:            "MiniMax-M3",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "",
	}, "")

	req := httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for oversized upstream body\nbody: %s",
			rec.Code, rec.Body.String())
	}
}
