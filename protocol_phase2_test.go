package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatRequestToIRAndResponseFromIR(t *testing.T) {
	req, err := openAIChatRequestToIR([]byte(`{"model":"gpt-test","max_tokens":32,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"Say hi"}]}`))
	if err != nil {
		t.Fatalf("openAIChatRequestToIR: %v", err)
	}
	if req.Model != "gpt-test" || req.MaxTokens != 32 || req.Stream {
		t.Fatalf("request metadata = %#v", req)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[0].Parts[0].Text != "be brief" || req.Messages[1].Role != "user" || req.Messages[1].Parts[0].Text != "Say hi" {
		t.Fatalf("messages = %#v", req.Messages)
	}

	body, err := irToOpenAIChatResponse(IRResponse{ID: "chatcmpl_1", Model: "gpt-test", Text: "Hi", StopReason: "end_turn", Usage: &IRUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}})
	if err != nil {
		t.Fatalf("irToOpenAIChatResponse: %v", err)
	}
	var raw openAIChatResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal chat response: %v\nbody: %s", err, string(body))
	}
	if raw.ID != "chatcmpl_1" || raw.Model != "gpt-test" || len(raw.Choices) != 1 || raw.Choices[0].Message.Content != "Hi" || raw.Choices[0].FinishReason != "stop" {
		t.Fatalf("chat response = %#v", raw)
	}
	if raw.Usage == nil || raw.Usage.PromptTokens != 2 || raw.Usage.CompletionTokens != 3 || raw.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", raw.Usage)
	}
}

func TestOpenAIChatUpstreamRequestAndResponseRoundTrip(t *testing.T) {
	body, err := buildOpenAIChatUpstreamRequest(IRRequest{Model: "gpt-test", MaxTokens: 16, Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "Say hi"}}}}})
	if err != nil {
		t.Fatalf("buildOpenAIChatUpstreamRequest: %v", err)
	}
	var raw openAIChatRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal upstream request: %v\nbody: %s", err, string(body))
	}
	if raw.Model != "gpt-test" || raw.MaxTokens != 16 || len(raw.Messages) != 1 || raw.Messages[0].Content != "Say hi" {
		t.Fatalf("upstream request = %#v", raw)
	}

	resp, err := parseOpenAIChatUpstreamResponse([]byte(`{"id":"chatcmpl_1","model":"gpt-test","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"length"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	if err != nil {
		t.Fatalf("parseOpenAIChatUpstreamResponse: %v", err)
	}
	if resp.ID != "chatcmpl_1" || resp.Model != "gpt-test" || resp.Text != "Hi" || resp.StopReason != responsesStopReasonMaxTokens {
		t.Fatalf("response IR = %#v", resp)
	}
}

func TestOpenAIResponsesUpstreamRequestAndResponseRoundTrip(t *testing.T) {
	body, err := buildOpenAIResponsesUpstreamRequest(IRRequest{Model: "gpt-test", MaxTokens: 16, Messages: []IRMessage{{Role: "system", Parts: []IRPart{{Type: irPartText, Text: "be brief"}}}, {Role: "user", Parts: []IRPart{{Type: irPartText, Text: "Say hi"}}}}})
	if err != nil {
		t.Fatalf("buildOpenAIResponsesUpstreamRequest: %v", err)
	}
	var raw responsesOutboundRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal responses request: %v\nbody: %s", err, string(body))
	}
	if raw.Model != "gpt-test" || raw.Instructions != "be brief" || raw.MaxOutputTokens != 16 || raw.Stream {
		t.Fatalf("responses request = %#v", raw)
	}
	if len(raw.Input) != 1 {
		t.Fatalf("input len = %d, want 1", len(raw.Input))
	}

	resp, err := parseOpenAIResponsesUpstreamResponse([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output_text":"Hi","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`), "application/json")
	if err != nil {
		t.Fatalf("parseOpenAIResponsesUpstreamResponse: %v", err)
	}
	if resp.ID != "resp_1" || resp.Model != "gpt-test" || resp.Text != "Hi" || resp.StopReason != "stop" {
		t.Fatalf("response IR = %#v", resp)
	}
}

func TestProxyHandlerOpenAIChatInboundToAnthropicUpstream(t *testing.T) {
	upstream, cap := startAnthropicUpstream(t, 0, "")
	handler := newProxyHandler(ProxyRoute{Provider: "anthropic-upstream", Model: "claude-test", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"client-model","max_tokens":16,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/v1/messages" || cap.auth != "Bearer provider-key" || cap.anthropicVersion != proxyAnthropicVersionValue {
		t.Fatalf("upstream path/auth/version = %q/%q/%q", cap.path, cap.auth, cap.anthropicVersion)
	}
	var upstreamBody anthropicRequestBody
	if err := json.Unmarshal(cap.body, &upstreamBody); err != nil {
		t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, string(cap.body))
	}
	if upstreamBody.Model != "claude-test" || upstreamBody.System != "be brief" || len(upstreamBody.Messages) != 1 || upstreamBody.Messages[0].Role != "user" {
		t.Fatalf("upstream body = %#v", upstreamBody)
	}
	var chatResp openAIChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("unmarshal chat response: %v\nbody: %s", err, rec.Body.String())
	}
	if len(chatResp.Choices) != 1 || chatResp.Choices[0].Message.Content != "Hi" {
		t.Fatalf("chat response = %#v", chatResp)
	}
}

func TestProxySameProtocolPassthroughRewritesOnlyModelAndAuth(t *testing.T) {
	cap := &upstreamCapture{}
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_passthrough")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_passthrough","model":"upstream-model","choices":[{"message":{"role":"assistant","content":"raw"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{Provider: "chat-upstream", ModelMappings: map[string]string{"client-model": "upstream-model"}, UpstreamProtocol: protocolOpenAIChat, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"client-model","temperature":0.7,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if cap.path != "/v1/chat/completions" || cap.auth != "Bearer provider-key" || cap.contentType != "application/json" {
		t.Fatalf("upstream path/auth/content-type = %q/%q/%q", cap.path, cap.auth, cap.contentType)
	}
	var upstreamBody map[string]any
	if err := json.Unmarshal(cap.body, &upstreamBody); err != nil {
		t.Fatalf("unmarshal passthrough body: %v\nbody: %s", err, string(cap.body))
	}
	if upstreamBody["model"] != "upstream-model" || upstreamBody["temperature"].(float64) != 0.7 {
		t.Fatalf("passthrough body = %#v", upstreamBody)
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl_passthrough") || !strings.Contains(rec.Body.String(), "raw") {
		t.Fatalf("response was not raw upstream body: %s", rec.Body.String())
	}
	if got := rec.Header().Get("X-Request-Id"); got != "req_passthrough" {
		t.Fatalf("X-Request-Id = %q, want upstream passthrough header", got)
	}
}

func TestProxySameProtocolPassthroughRejectsOpenAIChatToolMissingFunctionName(t *testing.T) {
	upstreamCalled := false
	upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"unexpected"}`))
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{Provider: "chat-upstream", Model: "upstream-model", UpstreamProtocol: protocolOpenAIChat, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"client-model","tools":[{"type":"function","function":{"description":"Lookup","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if upstreamCalled {
		t.Fatal("upstream was called; invalid passthrough request should be rejected locally")
	}
	if !strings.Contains(rec.Body.String(), "tool") || !strings.Contains(rec.Body.String(), "name") {
		t.Fatalf("error body = %q, want mention tool name", rec.Body.String())
	}
}

func TestProxySameProtocolPassthroughResponsesAndAnthropic(t *testing.T) {
	for _, tt := range []struct {
		name             string
		protocol         ProviderProtocol
		path             string
		reqBody          string
		upstreamRespBody string
	}{
		{
			name:             "responses",
			protocol:         protocolOpenAIResponses,
			path:             "/v1/responses",
			reqBody:          `{"model":"client-model","temperature":0.7,"input":"Say hi"}`,
			upstreamRespBody: `{"id":"resp_passthrough","object":"response","status":"completed","model":"upstream-model","output_text":"raw responses"}`,
		},
		{
			name:             "anthropic",
			protocol:         protocolAnthropicMessages,
			path:             "/v1/messages",
			reqBody:          `{"model":"client-model","metadata":{"trace":"keep"},"messages":[{"role":"user","content":"Say hi"}]}`,
			upstreamRespBody: `{"id":"msg_passthrough","type":"message","role":"assistant","model":"upstream-model","content":[{"type":"text","text":"raw anthropic"}],"stop_reason":"end_turn"}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cap := &upstreamCapture{}
			upstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cap.path = r.URL.Path
				cap.auth = r.Header.Get("Authorization")
				cap.anthropicVersion = r.Header.Get(proxyAnthropicVersionHeader)
				cap.body, _ = io.ReadAll(r.Body)
				_ = r.Body.Close()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.upstreamRespBody))
			}))
			t.Cleanup(upstream.Close)

			handler := newProxyHandler(ProxyRoute{Provider: "same-protocol", ModelMappings: map[string]string{"client-model": "upstream-model"}, UpstreamProtocol: tt.protocol, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.reqBody))
			req.Header.Set("Authorization", "Bearer local-token")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
			}
			if cap.path != tt.path || cap.auth != "Bearer provider-key" {
				t.Fatalf("upstream path/auth = %q/%q", cap.path, cap.auth)
			}
			if tt.protocol == protocolAnthropicMessages && cap.anthropicVersion != proxyAnthropicVersionValue {
				t.Fatalf("anthropic version = %q", cap.anthropicVersion)
			}
			var upstreamBody map[string]any
			if err := json.Unmarshal(cap.body, &upstreamBody); err != nil {
				t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, string(cap.body))
			}
			if upstreamBody["model"] != "upstream-model" {
				t.Fatalf("model = %v, body=%s", upstreamBody["model"], string(cap.body))
			}
			if !strings.Contains(rec.Body.String(), "passthrough") {
				t.Fatalf("response was not raw upstream body: %s", rec.Body.String())
			}
		})
	}
}


