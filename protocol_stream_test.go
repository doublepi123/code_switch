package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const anthropicStreamFixture = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":7,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

const chatStreamFixture = "data: " +
	`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
	"data: " +
	`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}` + "\n\n" +
	"data: " +
	`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}` + "\n\n" +
	"data: " +
	`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}` + "\n\n" +
	"data: [DONE]\n\n"

const responsesStreamFixture = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-test","output":[]}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hel"}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"lo"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output_text":"Hello","usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}` + "\n\n"

var wantStreamEvents = []StreamEvent{
	{Type: streamEventStart, ID: "msg_1", Model: "claude-test", Usage: &IRUsage{InputTokens: 7}},
	{Type: streamEventTextDelta, Text: "Hel"},
	{Type: streamEventTextDelta, Text: "lo"},
	{Type: streamEventUsage, Usage: &IRUsage{OutputTokens: 2}},
	{Type: streamEventStop, StopReason: "stop"},
}

func TestAnthropicSSEToStreamEventToAnthropicSSERoundTrip(t *testing.T) {
	events := decodeStreamFixture(t, protocolAnthropicMessages, anthropicStreamFixture)
	assertStreamText(t, events, "Hello")
	out := encodeStreamFixture(t, protocolAnthropicMessages, events)
	assertStreamText(t, decodeStreamFixture(t, protocolAnthropicMessages, out), "Hello")
}

func TestAnthropicSSEToChatSSE(t *testing.T) {
	events := decodeStreamFixture(t, protocolAnthropicMessages, anthropicStreamFixture)
	out := encodeStreamFixture(t, protocolOpenAIChat, events)
	if !strings.Contains(out, "data: [DONE]") {
		t.Fatalf("chat stream missing [DONE]:\n%s", out)
	}
	assertStreamText(t, decodeStreamFixture(t, protocolOpenAIChat, out), "Hello")
}

func TestAnthropicSSEToResponsesSSE(t *testing.T) {
	events := decodeStreamFixture(t, protocolAnthropicMessages, anthropicStreamFixture)
	out := encodeStreamFixture(t, protocolOpenAIResponses, events)
	if !strings.Contains(out, "event: response.created") || !strings.Contains(out, "event: response.completed") {
		t.Fatalf("responses stream missing lifecycle events:\n%s", out)
	}
	assertInOrder(t, out,
		"event: response.created",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.output_item.done",
		"event: response.completed",
	)
	assertStreamText(t, decodeStreamFixture(t, protocolOpenAIResponses, out), "Hello")
}

func TestChatSSEToAnthropicSSE(t *testing.T) {
	events := decodeStreamFixture(t, protocolOpenAIChat, chatStreamFixture)
	assertStreamText(t, events, "Hello")
	out := encodeStreamFixture(t, protocolAnthropicMessages, events)
	if !strings.Contains(out, "event: message_start") || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("anthropic stream missing lifecycle events:\n%s", out)
	}
	assertStreamText(t, decodeStreamFixture(t, protocolAnthropicMessages, out), "Hello")
}

func TestResponsesSSEToAnthropicSSE(t *testing.T) {
	events := decodeStreamFixture(t, protocolOpenAIResponses, responsesStreamFixture)
	assertStreamText(t, events, "Hello")
	out := encodeStreamFixture(t, protocolAnthropicMessages, events)
	assertStreamText(t, decodeStreamFixture(t, protocolAnthropicMessages, out), "Hello")
}

func TestChatSSEToResponsesSSEAndBack(t *testing.T) {
	events := decodeStreamFixture(t, protocolOpenAIChat, chatStreamFixture)
	out := encodeStreamFixture(t, protocolOpenAIResponses, events)
	assertStreamText(t, decodeStreamFixture(t, protocolOpenAIResponses, out), "Hello")
}

func TestAnthropicSSEToolUseEvents(t *testing.T) {
	fixture := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":7,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"hi\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}` + "\n\n"
	events := decodeStreamFixture(t, protocolAnthropicMessages, fixture)
	want := []StreamEvent{
		{Type: streamEventStart, ID: "msg_1", Model: "claude-test", Usage: &IRUsage{InputTokens: 7}},
		{Type: streamEventToolStart, ToolID: "call_1", ToolName: "lookup"},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `{"q":`},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `"hi"}`},
		{Type: streamEventToolStop, ToolID: "call_1"},
		{Type: streamEventStop, StopReason: "tool_use"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestOpenAIChatSSEToolUseEvents(t *testing.T) {
	fixture := "data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hi\"}"}}]},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	events := decodeStreamFixture(t, protocolOpenAIChat, fixture)
	want := []StreamEvent{
		{Type: streamEventStart, ID: "chatcmpl_1", Model: "gpt-test"},
		{Type: streamEventToolStart, ToolID: "call_1", ToolName: "lookup"},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `{"q":`},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `"hi"}`},
		{Type: streamEventStop, StopReason: "tool_use"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestOpenAIResponsesSSEToolUseEvents(t *testing.T) {
	fixture := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-test","output":[]}}` + "\n\n" +
		"event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}` + "\n\n" +
		"event: response.function_call_arguments.delta\n" +
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":"}` + "\n\n" +
		"event: response.function_call_arguments.delta\n" +
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"hi\"}"}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hi\"}"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-test","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hi\"}"}],"output_text":""}}` + "\n\n"
	events := decodeStreamFixture(t, protocolOpenAIResponses, fixture)
	want := []StreamEvent{
		{Type: streamEventStart, ID: "resp_1", Model: "gpt-test"},
		{Type: streamEventToolStart, ToolID: "call_1", ToolName: "lookup"},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `{"q":`},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `"hi"}`},
		{Type: streamEventToolStop, ToolID: "call_1"},
		{Type: streamEventStop, StopReason: "tool_use"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestAnthropicEncoderToolOnlyStreamUsesSingleToolBlock(t *testing.T) {
	events := []StreamEvent{
		{Type: streamEventStart, ID: "msg_1", Model: "claude-test"},
		{Type: streamEventToolStart, ToolID: "call_1", ToolName: "lookup"},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `{"q":"hi"}`},
		{Type: streamEventToolStop, ToolID: "call_1"},
		{Type: streamEventStop, StopReason: "tool_use"},
	}
	out := encodeStreamFixture(t, protocolAnthropicMessages, events)
	if strings.Contains(out, `"type":"text"`) {
		t.Fatalf("anthropic tool-only stream unexpectedly opened text block:\n%s", out)
	}
	for _, want := range []string{`"type":"tool_use"`, `"id":"call_1"`, `"name":"lookup"`, `"partial_json":"{\"q\":\"hi\"}"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("anthropic stream missing %s:\n%s", want, out)
		}
	}
}

func TestResponsesEncoderToolStreamCompletesFunctionCallOutput(t *testing.T) {
	events := []StreamEvent{
		{Type: streamEventStart, ID: "resp_1", Model: "gpt-test"},
		{Type: streamEventToolStart, ToolID: "call_1", ToolName: "lookup"},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `{"q":`},
		{Type: streamEventToolDelta, ToolID: "call_1", Text: `"hi"}`},
		{Type: streamEventToolStop, ToolID: "call_1"},
		{Type: streamEventStop, StopReason: "tool_use"},
	}
	out := encodeStreamFixture(t, protocolOpenAIResponses, events)
	for _, want := range []string{`"type":"function_call"`, `"call_id":"call_1"`, `"name":"lookup"`, `"arguments":"{\"q\":\"hi\"}"`, `"output":[{"arguments":"{\"q\":\"hi\"}"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("responses stream missing %s:\n%s", want, out)
		}
	}
}

func TestSSEReaderRejectsOversizedEvent(t *testing.T) {
	r := newSSEReader(strings.NewReader("data: "+strings.Repeat("x", maxSSEEventBytes+1)+"\n\n"), maxSSEEventBytes)
	_, err := r.ReadEvent()
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("ReadEvent error = %v, want exceeds limit", err)
	}
}

func TestProxyHandlerStreamsUpstreamSSEToClientProtocol(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body anthropicRequestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		if !body.Stream {
			t.Fatalf("upstream stream = false, want true")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicStreamFixture)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{Provider: "anthropic-upstream", Model: "claude-test", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: upstream.URL, LocalToken: "local-token"}, "provider-key")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"ignored","stream":true,"input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	assertStreamText(t, decodeStreamFixture(t, protocolOpenAIResponses, rec.Body.String()), "Hello")
}

func TestAnthropicStreamUsagePreservesInputTokensWhenEncodedAsResponses(t *testing.T) {
	events := decodeStreamFixture(t, protocolAnthropicMessages, anthropicStreamFixture)
	out := encodeStreamFixture(t, protocolOpenAIResponses, events)
	decoded := decodeStreamFixture(t, protocolOpenAIResponses, out)
	for _, event := range decoded {
		if event.Type == streamEventUsage && event.Usage != nil {
			if event.Usage.InputTokens != 7 {
				t.Fatalf("input_tokens = %d, want 7; events=%#v", event.Usage.InputTokens, decoded)
			}
			if event.Usage.OutputTokens != 2 {
				t.Fatalf("output_tokens = %d, want 2; events=%#v", event.Usage.OutputTokens, decoded)
			}
			return
		}
	}
	t.Fatalf("missing usage event; events=%#v\nencoded:\n%s", decoded, out)
}

func TestOpenAIChatStreamUsageOnlyChunkPreservesTokens(t *testing.T) {
	fixture := "data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}` + "\n\n" +
		"data: [DONE]\n\n"
	events := decodeStreamFixture(t, protocolOpenAIChat, fixture)
	for _, event := range events {
		if event.Type == streamEventUsage && event.Usage != nil {
			if event.Usage.InputTokens != 4 || event.Usage.OutputTokens != 6 || event.Usage.TotalTokens != 10 {
				t.Fatalf("usage = %#v, want 4/6/10", event.Usage)
			}
			return
		}
	}
	t.Fatalf("missing usage event; events=%#v", events)
}

func TestOpenAIChatStreamUsageOnlyChunkEncodesUsageBeforeStop(t *testing.T) {
	fixture := "data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: " +
		`{"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-test","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}` + "\n\n" +
		"data: [DONE]\n\n"
	events := decodeStreamFixture(t, protocolOpenAIChat, fixture)
	out := encodeStreamFixture(t, protocolAnthropicMessages, events)
	decoded := decodeStreamFixture(t, protocolAnthropicMessages, out)
	for _, event := range decoded {
		if event.Type == streamEventUsage && event.Usage != nil {
			if event.Usage.InputTokens != 4 || event.Usage.OutputTokens != 6 {
				t.Fatalf("usage = %#v, want input=4 output=6", event.Usage)
			}
			return
		}
	}
	t.Fatalf("missing encoded usage event; events=%#v\nencoded:\n%s", decoded, out)
}

func TestProxyStreamingRequestContextCancelsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
		close(cancelled)
	}))
	t.Cleanup(upstream.Close)

	handler := newProxyHandler(ProxyRoute{Provider: "anthropic-upstream", Model: "claude-test", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: upstream.URL}, "provider-key")
	ctx, cancel := contextWithCancelForTest()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"ignored","stream":true,"input":"Say hi"}`)).WithContext(ctx)
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()
	handler.ServeHTTP(httptest.NewRecorder(), req)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not cancelled after client context cancellation")
	}
}

func decodeStreamFixture(t *testing.T, protocol ProviderProtocol, fixture string) []StreamEvent {
	t.Helper()
	decoder, err := streamDecoderForProtocol(protocol)
	if err != nil {
		t.Fatal(err)
	}
	var events []StreamEvent
	if err := decoder.DecodeStream(strings.NewReader(fixture), func(event StreamEvent) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("DecodeStream(%s): %v", protocol, err)
	}
	return events
}

func encodeStreamFixture(t *testing.T, protocol ProviderProtocol, events []StreamEvent) string {
	t.Helper()
	encoder, err := streamEncoderForProtocol(protocol)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, event := range events {
		if err := encoder.EncodeStreamEvent(&buf, event); err != nil {
			t.Fatalf("EncodeStreamEvent(%s): %v", protocol, err)
		}
	}
	return buf.String()
}

func assertStreamText(t *testing.T, events []StreamEvent, want string) {
	t.Helper()
	var got strings.Builder
	for _, event := range events {
		if event.Type == streamEventTextDelta {
			got.WriteString(event.Text)
		}
	}
	if got.String() != want {
		t.Fatalf("stream text = %q, want %q; events=%#v", got.String(), want, events)
	}
}

func assertInOrder(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	start := 0
	for _, needle := range needles {
		idx := strings.Index(haystack[start:], needle)
		if idx < 0 {
			t.Fatalf("missing %q after byte %d:\n%s", needle, start, haystack)
		}
		start += idx + len(needle)
	}
}

func TestStreamDecoderNormalizesCanonicalFixtures(t *testing.T) {
	got := decodeStreamFixture(t, protocolAnthropicMessages, anthropicStreamFixture)
	if !reflect.DeepEqual(got, wantStreamEvents) {
		t.Fatalf("anthropic events = %#v, want %#v", got, wantStreamEvents)
	}
}

func contextWithCancelForTest() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
