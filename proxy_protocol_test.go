package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestIRRequestValidateTextOnlyAcceptsPlainText(t *testing.T) {
	req := IRRequest{
		Model:     "MiniMax-M3",
		Messages:  []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "Say hi"}}}},
		MaxTokens: 32,
	}
	if err := req.ValidateTextOnly(); err != nil {
		t.Fatalf("ValidateTextOnly returned error: %v", err)
	}
}

func TestIRRequestValidateTextOnlyAcceptsToolUse(t *testing.T) {
	req := IRRequest{
		Model:     "MiniMax-M3",
		Messages:  []IRMessage{{Role: "assistant", Parts: []IRPart{{Type: irPartToolUse, ToolCall: &IRToolCall{ID: "call_1", Name: "lookup", Input: json.RawMessage(`{"q":"hi"}`)}}}}},
		MaxTokens: 32,
	}
	if err := req.ValidateTextOnly(); err != nil {
		t.Fatalf("ValidateTextOnly returned error: %v", err)
	}
}

func TestIRRequestValidateTextOnlyRejectsEmptyModel(t *testing.T) {
	req := IRRequest{
		Model:    "",
		Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "hi"}}}},
	}
	err := req.ValidateTextOnly()
	if err == nil {
		t.Fatal("ValidateTextOnly returned nil, want error for empty model")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Fatalf("error = %q, want mention model", err.Error())
	}
}

func TestIRRequestValidateTextOnlyRejectsEmptyMessages(t *testing.T) {
	req := IRRequest{
		Model:    "MiniMax-M3",
		Messages: nil,
	}
	err := req.ValidateTextOnly()
	if err == nil {
		t.Fatal("ValidateTextOnly returned nil, want error for empty messages")
	}
	if !strings.Contains(err.Error(), "message") {
		t.Fatalf("error = %q, want mention message", err.Error())
	}
}

func TestIRRequestValidateTextOnlyRejectsInvalidRole(t *testing.T) {
	req := IRRequest{
		Model:    "MiniMax-M3",
		Messages: []IRMessage{{Role: "tool", Parts: []IRPart{{Type: irPartText, Text: "hi"}}}},
	}
	err := req.ValidateTextOnly()
	if err == nil {
		t.Fatal("ValidateTextOnly returned nil, want error for invalid role")
	}
	if !strings.Contains(err.Error(), "role") {
		t.Fatalf("error = %q, want mention role", err.Error())
	}
}

func TestIRRequestValidateTextOnlyRejectsEmptyParts(t *testing.T) {
	req := IRRequest{
		Model:    "MiniMax-M3",
		Messages: []IRMessage{{Role: "user", Parts: nil}},
	}
	err := req.ValidateTextOnly()
	if err == nil {
		t.Fatal("ValidateTextOnly returned nil, want error for empty parts")
	}
	if !strings.Contains(err.Error(), "part") {
		t.Fatalf("error = %q, want mention part", err.Error())
	}
}

func TestIRRequestValidateTextOnlyAllowsToolsButRejectsImageAndUnknownParts(t *testing.T) {
	validToolParts := []IRPart{
		{Type: irPartToolUse, ToolCall: &IRToolCall{ID: "call_1", Name: "lookup", Input: json.RawMessage(`{}`)}},
		{Type: irPartToolResult, ToolResult: &IRToolResult{ToolUseID: "call_1", Content: "ok"}},
	}
	for _, part := range validToolParts {
		req := IRRequest{Model: "MiniMax-M3", Messages: []IRMessage{{Role: "user", Parts: []IRPart{part}}}}
		if err := req.ValidateTextOnly(); err != nil {
			t.Fatalf("ValidateTextOnly returned error for %q: %v", part.Type, err)
		}
	}
	for _, pt := range []string{irPartImage, irPartReasoning, "unknown"} {
		t.Run(pt, func(t *testing.T) {
			req := IRRequest{
				Model:    "MiniMax-M3",
				Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: pt}}}},
			}
			err := req.ValidateTextOnly()
			if err == nil {
				t.Fatalf("ValidateTextOnly returned nil for part type %q", pt)
			}
			if !strings.Contains(err.Error(), pt) {
				t.Fatalf("error = %q, want mention part type %q", err.Error(), pt)
			}
		})
	}
}

func TestAnthropicToolsAndToolBlocksRoundTripIR(t *testing.T) {
	body := []byte(`{"model":"claude-model","max_tokens":32,"tools":[{"name":"lookup","description":"Lookup data","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"hi"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]}]}`)
	req, err := anthropicRequestToIR(body)
	if err != nil {
		t.Fatalf("anthropicRequestToIR returned error: %v", err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "lookup" || !strings.Contains(string(req.Tools[0].InputSchema), `"q"`) {
		t.Fatalf("tools = %#v", req.Tools)
	}
	call := req.Messages[0].Parts[0].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "lookup" || string(call.Input) != `{"q":"hi"}` {
		t.Fatalf("tool call = %#v", call)
	}
	result := req.Messages[1].Parts[0].ToolResult
	if result == nil || result.ToolUseID != "call_1" || result.Content != "result" {
		t.Fatalf("tool result = %#v", result)
	}
	out, err := irToAnthropicRequest(req)
	if err != nil {
		t.Fatalf("irToAnthropicRequest returned error: %v", err)
	}
	for _, want := range []string{`"tools":[`, `"input_schema":{"type":"object"`, `"type":"tool_use"`, `"id":"call_1"`, `"type":"tool_result"`, `"tool_use_id":"call_1"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("anthropic request missing %s: %s", want, out)
		}
	}
}

func TestAnthropicRequestToIRRejectsToolDefinitionMissingName(t *testing.T) {
	body := []byte(`{"model":"claude-model","max_tokens":32,"tools":[{"description":"Lookup data","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],"messages":[{"role":"user","content":"hi"}]}`)

	_, err := anthropicRequestToIR(body)
	if err == nil {
		t.Fatal("anthropicRequestToIR returned nil, want error for tool definition without name")
	}
	if !strings.Contains(err.Error(), "tool 0") || !strings.Contains(err.Error(), "name") {
		t.Fatalf("error = %q, want mention tool index and name", err.Error())
	}
}

func TestOpenAIChatToolsCallsAndResultsRoundTripIR(t *testing.T) {
	body := []byte(`{"model":"gpt","tools":[{"type":"function","function":{"name":"lookup","description":"Lookup data","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}],"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"hi\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`)
	req, err := openAIChatRequestToIR(body)
	if err != nil {
		t.Fatalf("openAIChatRequestToIR returned error: %v", err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "lookup" || !strings.Contains(string(req.Tools[0].InputSchema), `"q"`) {
		t.Fatalf("tools = %#v", req.Tools)
	}
	call := req.Messages[1].Parts[0].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "lookup" || string(call.Input) != `{"q":"hi"}` {
		t.Fatalf("tool call = %#v", call)
	}
	result := req.Messages[2].Parts[0].ToolResult
	if result == nil || result.ToolUseID != "call_1" || result.Content != "result" {
		t.Fatalf("tool result = %#v", result)
	}
	out, err := irToOpenAIChatRequest(req)
	if err != nil {
		t.Fatalf("irToOpenAIChatRequest returned error: %v", err)
	}
	for _, want := range []string{`"tools":[`, `"type":"function"`, `"tool_calls":[`, `"arguments":"{\"q\":\"hi\"}"`, `"role":"tool"`, `"tool_call_id":"call_1"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("chat request missing %s: %s", want, out)
		}
	}
}

func TestResponsesToolsAndFunctionCallsRoundTripIR(t *testing.T) {
	body := []byte(`{"model":"gpt","tools":[{"type":"function","name":"lookup","description":"Lookup data","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hi\"}"}]}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "lookup" || !strings.Contains(string(req.Tools[0].InputSchema), `"q"`) {
		t.Fatalf("tools = %#v", req.Tools)
	}
	call := req.Messages[1].Parts[0].ToolCall
	if call == nil || call.ID != "call_1" || call.Name != "lookup" || string(call.Input) != `{"q":"hi"}` {
		t.Fatalf("tool call = %#v", call)
	}
	out, err := irToResponsesRequest(req)
	if err != nil {
		t.Fatalf("irToResponsesRequest returned error: %v", err)
	}
	for _, want := range []string{`"tools":[`, `"type":"function"`, `"parameters":{"type":"object"`, `"type":"function_call"`, `"call_id":"call_1"`, `"arguments":"{\"q\":\"hi\"}"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("responses request missing %s: %s", want, out)
		}
	}
}

func TestResponsesAssistantOutputTextHistoryRoundTripIR(t *testing.T) {
	body := []byte(`{"model":"gpt","input":[{"role":"assistant","content":[{"type":"output_text","text":"previous"}]},{"role":"user","content":[{"type":"input_text","text":"next"}]}]}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if got := req.Messages[0].Parts[0].Text; got != "previous" {
		t.Fatalf("assistant history text = %q", got)
	}
	out, err := irToResponsesRequest(req)
	if err != nil {
		t.Fatalf("irToResponsesRequest returned error: %v", err)
	}
	if !strings.Contains(string(out), `"type":"output_text"`) {
		t.Fatalf("responses request missing output_text for assistant history: %s", out)
	}
}

func TestMixedTextAndToolResponsePreservesBoth(t *testing.T) {
	resp := IRResponse{ID: "resp_1", Model: "m", Text: "checking", StopReason: "tool_use", ToolCalls: []IRToolCall{{ID: "call_1", Name: "lookup", Input: json.RawMessage(`{"q":"hi"}`)}}}
	anthropic, err := irToAnthropicResponse(resp)
	if err != nil {
		t.Fatalf("irToAnthropicResponse returned error: %v", err)
	}
	for _, want := range []string{`"type":"text"`, `"text":"checking"`, `"type":"tool_use"`, `"id":"call_1"`} {
		if !strings.Contains(string(anthropic), want) {
			t.Fatalf("anthropic mixed response missing %s: %s", want, anthropic)
		}
	}
	responses, err := irToResponsesResponse(resp)
	if err != nil {
		t.Fatalf("irToResponsesResponse returned error: %v", err)
	}
	for _, want := range []string{`"output_text":"checking"`, `"type":"message"`, `"type":"function_call"`, `"call_id":"call_1"`} {
		if !strings.Contains(string(responses), want) {
			t.Fatalf("responses mixed response missing %s: %s", want, responses)
		}
	}
	parsed, err := responsesResponseToIR(responses)
	if err != nil {
		t.Fatalf("responsesResponseToIR returned error: %v", err)
	}
	if parsed.Text != "checking" || len(parsed.ToolCalls) != 1 || parsed.ToolCalls[0].ID != "call_1" {
		t.Fatalf("parsed mixed response = %#v", parsed)
	}
}

func TestToolCallResponsesAndStopReasonsMapToIR(t *testing.T) {
	anthropicResp, err := anthropicResponseToIR([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"hi"}}],"stop_reason":"tool_use"}`))
	if err != nil {
		t.Fatalf("anthropicResponseToIR returned error: %v", err)
	}
	if anthropicResp.StopReason != "tool_use" || len(anthropicResp.ToolCalls) != 1 || anthropicResp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("anthropic resp = %#v", anthropicResp)
	}
	chatResp, err := openAIChatResponseToIR([]byte(`{"id":"chat_1","model":"gpt","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"hi\"}"}}]},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatalf("openAIChatResponseToIR returned error: %v", err)
	}
	if chatResp.StopReason != "tool_use" || len(chatResp.ToolCalls) != 1 || chatResp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("chat resp = %#v", chatResp)
	}
	responsesResp, err := responsesResponseToIR([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"gpt","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hi\"}"}],"output_text":""}`))
	if err != nil {
		t.Fatalf("responsesResponseToIR returned error: %v", err)
	}
	if responsesResp.StopReason != "tool_use" || len(responsesResp.ToolCalls) != 1 || responsesResp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("responses resp = %#v", responsesResp)
	}
	if got := openAIChatStopReasonToIR("stop"); got != "stop" {
		t.Fatalf("chat stop = %q, want stop", got)
	}
	if got := responsesStatusToIRStopReason(responsesStatusCompleted); got != "stop" {
		t.Fatalf("responses completed = %q, want stop", got)
	}
}

func TestIRRequestValidateTextOnlyAcceptsAllValidRoles(t *testing.T) {
	for _, role := range []string{"system", "user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			req := IRRequest{
				Model:    "MiniMax-M3",
				Messages: []IRMessage{{Role: role, Parts: []IRPart{{Type: irPartText, Text: "hi"}}}},
			}
			if err := req.ValidateTextOnly(); err != nil {
				t.Fatalf("ValidateTextOnly returned error for role %q: %v", role, err)
			}
		})
	}
}

func TestResponsesRequestToIRText(t *testing.T) {
	body := []byte(`{"model":"codex-model","input":"Say hi","max_output_tokens":12}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if req.Model != "codex-model" {
		t.Fatalf("Model = %q", req.Model)
	}
	if req.MaxTokens != 12 {
		t.Fatalf("MaxTokens = %d", req.MaxTokens)
	}
	if got := req.Messages[0].Parts[0].Text; got != "Say hi" {
		t.Fatalf("text = %q", got)
	}
}

// TestResponsesRequestToIRStreamSetsIRStreamFlag verifies that a
// stream:true request no longer errors (the proxy MVP wraps the
// completed response as SSE) but instead sets IRRequest.Stream=true so
// the proxy handler knows to emit an event stream.
func TestResponsesRequestToIRStreamSetsIRStreamFlag(t *testing.T) {
	body := []byte(`{"model":"codex-model","input":"Say hi","stream":true}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if !req.Stream {
		t.Fatal("req.Stream = false, want true")
	}
	if got := req.Messages[0].Parts[0].Text; got != "Say hi" {
		t.Fatalf("text = %q, want Say hi", got)
	}
}

func TestIRToResponsesResponse(t *testing.T) {
	data, err := irToResponsesResponse(IRResponse{ID: "msg_1", Model: "MiniMax-M3", Text: "Hi", StopReason: "end_turn", Usage: &IRUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}})
	if err != nil {
		t.Fatalf("irToResponsesResponse returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"object":"response"`, `"status":"completed"`, `"output_text":"Hi"`, `"input_tokens":2`, `"output_tokens":3`} {
		if !strings.Contains(text, want) {
			t.Fatalf("response JSON missing %s: %s", want, text)
		}
	}
}

// TestResponsesRequestToIRAcceptsTopLevelTools verifies that the MVP
// tolerates the Codex tools array (and tool_choice) by accepting and
// ignoring it. The MVP does NOT convert tool definitions or execute
// tool calls; this only lets simple-text requests that happen to carry
// an (unused) tools array run end-to-end.
func TestResponsesRequestToIRAcceptsTopLevelTools(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","tools":[{"type":"function","name":"x"}],"tool_choice":"auto","parallel_tool_calls":false}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if got := req.Messages[0].Parts[0].Text; got != "hi" {
		t.Fatalf("text = %q, want hi", got)
	}
}

// TestResponsesRequestToIRAcceptsTopLevelReasoningObject verifies that
// Codex reasoning hints are accepted and ignored. The MVP does not honour
// reasoning effort, but rejecting the field interrupts Codex guardian turns.
func TestResponsesRequestToIRAcceptsTopLevelReasoningObject(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","reasoning":{"effort":"high"}}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error for top-level reasoning object: %v", err)
	}
	if got := req.Messages[0].Parts[0].Text; got != "hi" {
		t.Fatalf("text = %q, want hi", got)
	}
}

// TestResponsesRequestToIRAcceptsReasoningNull verifies that a JSON null
// reasoning value is accepted and ignored (Codex sends reasoning:null
// when reasoning is disabled).
func TestResponsesRequestToIRAcceptsReasoningNull(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","reasoning":null}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error for reasoning null: %v", err)
	}
	if got := req.Messages[0].Parts[0].Text; got != "hi" {
		t.Fatalf("text = %q, want hi", got)
	}
}

// TestResponsesRequestToIRAcceptsCodexGuardianTextFormat verifies that a
// Codex auto-approval guardian request carrying the Responses text config
// (including a json_schema output format), reasoning hints, and service tier
// is accepted. The proxy cannot enforce the output schema when translating to
// Anthropic, but it must not reject the request before the guardian can run.
func TestResponsesRequestToIRAcceptsCodexGuardianTextFormat(t *testing.T) {
	body := []byte(`{
		"model":"m",
		"input":"Review this command: ls",
		"text":{
			"verbosity":"low",
			"format":{
				"type":"json_schema",
				"strict":true,
				"name":"codex_output_schema",
				"schema":{
					"type":"object",
					"properties":{"outcome":{"type":"string","enum":["allow","deny"]}},
					"required":["outcome"],
					"additionalProperties":false
				}
			}
		},
		"reasoning":{"effort":"minimal"},
		"service_tier":"default"
	}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if got := req.Messages[0].Role; got != "system" {
		t.Fatalf("first role = %q, want system", got)
	}
	if got := req.Messages[0].Parts[0].Text; !strings.Contains(got, "codex_output_schema") || !strings.Contains(got, "Respond only with JSON") {
		t.Fatalf("system instruction = %q, want schema guidance", got)
	}
	if got := req.Messages[1].Parts[0].Text; got != "Review this command: ls" {
		t.Fatalf("user text = %q, want guardian prompt", got)
	}
}

// TestResponsesRequestToIRMapsInstructionsToSystem verifies that a
// top-level instructions string is mapped onto a leading IR system
// message rather than rejected.
func TestResponsesRequestToIRMapsInstructionsToSystem(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","instructions":"be brief"}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if len(req.Messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + user), got %d", len(req.Messages))
	}
	if got := req.Messages[0].Role; got != "system" {
		t.Fatalf("first message role = %q, want system", got)
	}
	if got := req.Messages[0].Parts[0].Text; got != "be brief" {
		t.Fatalf("system text = %q, want be brief", got)
	}
	// The user turn must still be present after the system message.
	var foundUser bool
	for _, m := range req.Messages {
		if m.Role == "user" && len(m.Parts) > 0 && m.Parts[0].Text == "hi" {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatalf("user turn missing from messages: %+v", req.Messages)
	}
}

// TestResponsesRequestToIRRejectsEmptyInstructions verifies that an
// empty instructions string surfaces as an error rather than producing
// an empty system message.
func TestResponsesRequestToIRRejectsEmptyInstructions(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","instructions":""}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for empty instructions")
	}
	if !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("error = %q, want mention instructions", err.Error())
	}
}

// TestResponsesRequestToIRRejectsNonStringInstructions verifies that a
// non-string instructions value surfaces as an error.
func TestResponsesRequestToIRRejectsNonStringInstructions(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","instructions":42}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for non-string instructions")
	}
	if !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("error = %q, want mention instructions", err.Error())
	}
}

// TestResponsesRequestToIRAcceptsCodexCommonFields verifies that the
// set of fields a real Codex v0.140.0 request carries but that the MVP
// treats as non-semantic are all accepted and ignored in a single
// simple-text request.
func TestResponsesRequestToIRAcceptsCodexCommonFields(t *testing.T) {
	body := []byte(`{
		"model":"m",
		"input":"hi",
		"client_metadata":{"version":"0.140.0"},
		"include":["file_search_call.results"],
		"store":false,
		"prompt_cache_key":"cache-1",
		"tools":[{"type":"function","name":"shell"}],
		"tool_choice":"auto",
		"parallel_tool_calls":false,
		"reasoning":null
	}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if got := req.Messages[len(req.Messages)-1].Parts[0].Text; got != "hi" {
		t.Fatalf("user text = %q, want hi", got)
	}
}

// TestResponsesRequestToIRRejectsUnknownFields verifies that top-level
// keys outside the supported set are rejected with a field-specific
// error rather than silently ignored. temperature, previous_response_id,
// metadata, and user are common OpenAI Responses fields the MVP does not
// honour. (store, include, text, service_tier, and reasoning are now
// accepted because real Codex sends them; see the dedicated acceptance tests.)
func TestResponsesRequestToIRRejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"temperature", `{"model":"m","input":"hi","temperature":0.7}`, "temperature"},
		{"top_p", `{"model":"m","input":"hi","top_p":0.9}`, "top_p"},
		{"previous_response_id", `{"model":"m","input":"hi","previous_response_id":"resp_1"}`, "previous_response_id"},
		{"metadata", `{"model":"m","input":"hi","metadata":{"k":"v"}}`, "metadata"},
		{"user", `{"model":"m","input":"hi","user":"u-1"}`, "user"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := responsesRequestToIR([]byte(c.body))
			if err == nil {
				t.Fatalf("responsesRequestToIR returned nil, want error for unknown field %q", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %q, want mention %q", err.Error(), c.want)
			}
			// The error must also indicate the field is not supported,
			// so the client knows it cannot simply drop the value.
			if !strings.Contains(err.Error(), "not supported") {
				t.Fatalf("error = %q, want mention \"not supported\"", err.Error())
			}
		})
	}
}

// TestResponsesRequestToIRRejectsNegativeMaxOutputTokens verifies that a
// negative max_output_tokens surfaces as an explicit error rather than
// being silently clamped to the default 1024. Zero is allowed (treated
// as unset).
func TestResponsesRequestToIRRejectsNegativeMaxOutputTokens(t *testing.T) {
	for _, n := range []int{-1, -100} {
		t.Run(fmt.Sprintf("n_%d", n), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"m","input":"hi","max_output_tokens":%d}`, n))
			_, err := responsesRequestToIR(body)
			if err == nil {
				t.Fatalf("responsesRequestToIR returned nil, want error for max_output_tokens %d", n)
			}
			if !strings.Contains(err.Error(), "max_output_tokens") {
				t.Fatalf("error = %q, want mention max_output_tokens", err.Error())
			}
		})
	}
}

// TestResponsesRequestToIRAcceptsZeroMaxOutputTokens confirms that zero
// (the JSON zero value / unset sentinel) is accepted; the Anthropic
// adapter later defaults it to 1024.
func TestResponsesRequestToIRAcceptsZeroMaxOutputTokens(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","max_output_tokens":0}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error for max_output_tokens 0: %v", err)
	}
	if req.MaxTokens != 0 {
		t.Fatalf("MaxTokens = %d, want 0 (defaulting happens in the adapter)", req.MaxTokens)
	}
}

func TestResponsesRequestToIRAcceptsPromptCacheKey(t *testing.T) {
	body := []byte(`{"model":"m","input":"hi","prompt_cache_key":"cache-key"}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if got := req.Messages[0].Parts[0].Text; got != "hi" {
		t.Fatalf("text = %q", got)
	}
}

func TestResponsesRequestToIRInputArrayRequiresExplicitRole(t *testing.T) {
	// role key entirely absent
	body := []byte(`{"model":"m","input":[{"content":"hi"}]}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for missing role")
	}
	if !strings.Contains(err.Error(), "role") {
		t.Fatalf("error = %q, want mention role", err.Error())
	}

	// role present but empty string
	body = []byte(`{"model":"m","input":[{"role":"","content":"hi"}]}`)
	_, err = responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for empty role")
	}
	if !strings.Contains(err.Error(), "role") {
		t.Fatalf("error = %q, want mention role", err.Error())
	}
}

func TestResponsesRequestToIRInputArrayRejectsNonUserRole(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"system","content":"hi"}]}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for unsupported role")
	}
	if !strings.Contains(err.Error(), "role") {
		t.Fatalf("error = %q, want mention role", err.Error())
	}
}

func TestResponsesRequestToIRInputArrayMapsDeveloperRoleToSystem(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"developer","content":"be brief"},{"role":"user","content":"hi"}]}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
	}
	if got := req.Messages[0].Role; got != "system" {
		t.Fatalf("first role = %q, want system", got)
	}
	if got := req.Messages[0].Parts[0].Text; got != "be brief" {
		t.Fatalf("developer text = %q", got)
	}
}

func TestResponsesRequestToIRInputArrayAcceptsMultipleTextMessages(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":[{"type":"input_text","text":"there"}]}]}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(req.Messages))
	}
	if got := req.Messages[1].Role; got != "assistant" {
		t.Fatalf("second role = %q, want assistant", got)
	}
	if got := req.Messages[2].Parts[0].Text; got != "there" {
		t.Fatalf("third text = %q", got)
	}
}

func TestResponsesRequestToIRInputArrayRejectsEmptyArray(t *testing.T) {
	body := []byte(`{"model":"m","input":[]}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for empty input array")
	}
	msg := err.Error()
	if !strings.Contains(msg, "empty") {
		t.Fatalf("error = %q, want mention empty", msg)
	}
	if !strings.Contains(msg, "input") {
		t.Fatalf("error = %q, want mention input", msg)
	}
}

func TestResponsesRequestToIRInputArrayRequiresContent(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user"}]}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for missing content")
	}
	if !strings.Contains(err.Error(), "content") {
		t.Fatalf("error = %q, want mention content", err.Error())
	}
}

func TestResponsesRequestToIRRejectsEmptyInputString(t *testing.T) {
	body := []byte(`{"model":"m","input":""}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for empty input string")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %q, want mention empty", err.Error())
	}
}

func TestResponsesRequestToIRRejectsInputTextMissingText(t *testing.T) {
	// input_text part with no text field at all
	body := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text"}]}]}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for input_text missing text")
	}
	if !strings.Contains(err.Error(), "text") {
		t.Fatalf("error = %q, want mention text", err.Error())
	}

	// input_text part with explicitly empty text
	body = []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":""}]}]}`)
	_, err = responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for input_text empty text")
	}
	if !strings.Contains(err.Error(), "text") {
		t.Fatalf("error = %q, want mention text", err.Error())
	}
}

func TestResponsesRequestToIRRejectsUnsupportedContentType(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`)
	_, err := responsesRequestToIR(body)
	if err == nil {
		t.Fatal("responsesRequestToIR returned nil, want error for unsupported content type")
	}
	if !strings.Contains(err.Error(), "input_text") {
		t.Fatalf("error = %q, want mention input_text", err.Error())
	}
}

func TestResponsesRequestToIRAcceptsValidInputArray(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if got := req.Messages[0].Role; got != "user" {
		t.Fatalf("role = %q, want user", got)
	}
	if got := req.Messages[0].Parts[0].Text; got != "hi" {
		t.Fatalf("text = %q, want hi", got)
	}
}

func TestResponsesRequestToIRAcceptsStringContent(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":"hi there"}]}`)
	req, err := responsesRequestToIR(body)
	if err != nil {
		t.Fatalf("responsesRequestToIR returned error: %v", err)
	}
	if got := req.Messages[0].Parts[0].Text; got != "hi there" {
		t.Fatalf("text = %q, want hi there", got)
	}
}

func TestIRToResponsesResponseStatusIncompleteForMaxTokens(t *testing.T) {
	data, err := irToResponsesResponse(IRResponse{ID: "resp_1", Model: "m", Text: "Hi", StopReason: "max_tokens", Usage: &IRUsage{}})
	if err != nil {
		t.Fatalf("irToResponsesResponse returned error: %v", err)
	}
	var got responsesRawResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, string(data))
	}
	if got.Status != "incomplete" {
		t.Fatalf("status = %q, want incomplete\nbody: %s", got.Status, string(data))
	}
}

func TestIRToResponsesResponseStatusCompletedForEndTurn(t *testing.T) {
	for _, sr := range []string{"end_turn", "stop", "", "unknown"} {
		t.Run(sr, func(t *testing.T) {
			data, err := irToResponsesResponse(IRResponse{ID: "resp_1", Model: "m", Text: "Hi", StopReason: sr, Usage: &IRUsage{}})
			if err != nil {
				t.Fatalf("irToResponsesResponse returned error: %v", err)
			}
			var got responsesRawResponse
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v\nbody: %s", err, string(data))
			}
			if got.Status != "completed" {
				t.Fatalf("status for stop reason %q = %q, want completed\nbody: %s", sr, got.Status, string(data))
			}
		})
	}
}

func TestIRToResponsesResponseOmitsUsageWhenNil(t *testing.T) {
	data, err := irToResponsesResponse(IRResponse{ID: "resp_1", Model: "m", Text: "Hi", StopReason: "end_turn", Usage: nil})
	if err != nil {
		t.Fatalf("irToResponsesResponse returned error: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v\nbody: %s", err, string(data))
	}
	if _, ok := raw["usage"]; ok {
		t.Fatalf("response JSON should not contain usage field when Usage is nil\nbody: %s", string(data))
	}
}

func TestIRToResponsesResponseIncludesUsageWhenPresent(t *testing.T) {
	data, err := irToResponsesResponse(IRResponse{ID: "resp_1", Model: "m", Text: "Hi", StopReason: "end_turn", Usage: &IRUsage{InputTokens: 7, OutputTokens: 11, TotalTokens: 18}})
	if err != nil {
		t.Fatalf("irToResponsesResponse returned error: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v\nbody: %s", err, string(data))
	}
	usageRaw, ok := raw["usage"]
	if !ok {
		t.Fatalf("response JSON should contain usage field when Usage is present\nbody: %s", string(data))
	}
	var u responsesUsage
	if err := json.Unmarshal(usageRaw, &u); err != nil {
		t.Fatalf("unmarshal usage: %v\nbody: %s", err, string(data))
	}
	if u.InputTokens != 7 || u.OutputTokens != 11 || u.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want {7 11 18}\nbody: %s", u, string(data))
	}
}

func TestIRToResponsesResponseIDNormalisation(t *testing.T) {
	cases := []struct {
		name       string
		inputID    string
		wantRespID string
		wantMsgID  string
	}{
		{"empty falls back to placeholders", "", "resp_code_switch", "msg_code_switch"},
		{"already resp_ preserved", "resp_abc123", "resp_abc123", "msg_abc123"},
		{"already msg_ preserved", "msg_xyz", "resp_msg_xyz", "msg_xyz"},
		{"bare id prefixed", "abc123", "resp_abc123", "msg_abc123"},
		{"resp_ with empty tail", "resp_", "resp_", "msg_"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, err := irToResponsesResponse(IRResponse{ID: c.inputID, Model: "m", Text: "Hi", StopReason: "end_turn", Usage: nil})
			if err != nil {
				t.Fatalf("irToResponsesResponse returned error: %v", err)
			}
			var got responsesRawResponse
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v\nbody: %s", err, string(data))
			}
			if got.ID != c.wantRespID {
				t.Fatalf("response id = %q, want %q\nbody: %s", got.ID, c.wantRespID, string(data))
			}
			if len(got.Output) != 1 {
				t.Fatalf("expected exactly one output message, got %d\nbody: %s", len(got.Output), string(data))
			}
			if got.Output[0].ID != c.wantMsgID {
				t.Fatalf("message id = %q, want %q\nbody: %s", got.Output[0].ID, c.wantMsgID, string(data))
			}
		})
	}
}

func TestResponsesResponseIDUnit(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "resp_code_switch"},
		{"resp_x", "resp_x"},
		{"abc", "resp_abc"},
		{"msg_y", "resp_msg_y"},
	}
	for _, c := range cases {
		if got := responsesResponseID(c.in); got != c.want {
			t.Errorf("responsesResponseID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResponsesMessageIDUnit(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "msg_code_switch"},
		{"msg_x", "msg_x"},
		{"resp_x", "msg_x"},
		{"abc", "msg_abc"},
		{"resp_", "msg_"},
	}
	for _, c := range cases {
		if got := responsesMessageID(c.in); got != c.want {
			t.Errorf("responsesMessageID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIRToAnthropicRequest(t *testing.T) {
	req := IRRequest{Model: "MiniMax-M3", MaxTokens: 24, Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "Say hi"}}}}}
	data, err := irToAnthropicRequest(req)
	if err != nil {
		t.Fatalf("irToAnthropicRequest returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"model":"MiniMax-M3"`, `"max_tokens":24`, `"role":"user"`, `"text":"Say hi"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("request JSON missing %s: %s", want, text)
		}
	}
}

func TestAnthropicResponseToIR(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
	resp, err := anthropicResponseToIR(body)
	if err != nil {
		t.Fatalf("anthropicResponseToIR returned error: %v", err)
	}
	if resp.ID != "msg_1" || resp.Model != "MiniMax-M3" || resp.Text != "Hi" || resp.StopReason != "stop" {
		t.Fatalf("unexpected IR response: %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v, want total 5", resp.Usage)
	}
}

func TestAnthropicRequestToIRText(t *testing.T) {
	body := []byte(`{"model":"claude-model","system":"be brief","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"Say hi"}]},{"role":"assistant","content":"Hello"}]}`)
	req, err := anthropicRequestToIR(body)
	if err != nil {
		t.Fatalf("anthropicRequestToIR returned error: %v", err)
	}
	if req.Model != "claude-model" || req.MaxTokens != 16 {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Parts[0].Text != "be brief" {
		t.Fatalf("system message = %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Parts[0].Text != "Say hi" {
		t.Fatalf("user message = %+v", req.Messages[1])
	}
	if req.Messages[2].Role != "assistant" || req.Messages[2].Parts[0].Text != "Hello" {
		t.Fatalf("assistant message = %+v", req.Messages[2])
	}
}

func TestAnthropicRequestToIRPreservesStream(t *testing.T) {
	body := []byte(`{"model":"claude-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := anthropicRequestToIR(body)
	if err != nil {
		t.Fatalf("anthropicRequestToIR returned error: %v", err)
	}
	if !req.Stream {
		t.Fatalf("Stream = false, want true")
	}
}

func TestAnthropicRequestToIRAcceptsToolUseContent(t *testing.T) {
	body := []byte(`{"model":"claude-model","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"x","input":{}}]}]}`)
	req, err := anthropicRequestToIR(body)
	if err != nil {
		t.Fatalf("anthropicRequestToIR returned error: %v", err)
	}
	if got := req.Messages[0].Parts[0].ToolCall; got == nil || got.ID != "t1" || got.Name != "x" {
		t.Fatalf("tool call = %#v", got)
	}
}

func TestIRToResponsesRequest(t *testing.T) {
	req := IRRequest{Model: "glm-5.2", MaxTokens: 32, Messages: []IRMessage{
		{Role: "system", Parts: []IRPart{{Type: irPartText, Text: "be brief"}}},
		{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "Say hi"}}},
	}}
	data, err := irToResponsesRequest(req)
	if err != nil {
		t.Fatalf("irToResponsesRequest returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"model":"glm-5.2"`, `"instructions":"be brief"`, `"max_output_tokens":32`, `"role":"user"`, `"input_text"`, `"Say hi"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("responses request missing %s: %s", want, text)
		}
	}
}

func TestResponsesResponseToIR(t *testing.T) {
	body := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"glm-5.2","output_text":"Hi","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
	resp, err := responsesResponseToIR(body)
	if err != nil {
		t.Fatalf("responsesResponseToIR returned error: %v", err)
	}
	if resp.ID != "resp_1" || resp.Model != "glm-5.2" || resp.Text != "Hi" || resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected IR response: %+v", resp)
	}
}

func TestIRToAnthropicResponse(t *testing.T) {
	data, err := irToAnthropicResponse(IRResponse{ID: "resp_1", Model: "glm-5.2", Text: "Hi", StopReason: "end_turn", Usage: &IRUsage{InputTokens: 2, OutputTokens: 3}})
	if err != nil {
		t.Fatalf("irToAnthropicResponse returned error: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"type":"message"`, `"role":"assistant"`, `"model":"glm-5.2"`, `"type":"text"`, `"text":"Hi"`, `"stop_reason":"end_turn"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("anthropic response missing %s: %s", want, text)
		}
	}
}

// TestIRToAnthropicRequestDefaultsMaxTokens verifies that an unset or
// non-positive MaxTokens falls back to the upstream-supported maximum rather than
// being emitted as zero (which the Anthropic API would reject). The default
// must be high enough for Codex review/fix turns; lower caps caused upstream
// max-token stops with no visible text, while 512k is rejected by the Xiaomi
// Anthropic-compatible endpoint.
func TestIRToAnthropicRequestDefaultsMaxTokens(t *testing.T) {
	for _, max := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("max_%d", max), func(t *testing.T) {
			req := IRRequest{Model: "m", MaxTokens: max, Messages: []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "hi"}}}}}
			data, err := irToAnthropicRequest(req)
			if err != nil {
				t.Fatalf("irToAnthropicRequest returned error: %v", err)
			}
			if !strings.Contains(string(data), `"max_tokens":131072`) {
				t.Fatalf("expected default max_tokens 131072, body: %s", string(data))
			}
		})
	}
}

// TestAnthropicResponseToIRRejectsEmptyMaxTokenResponse verifies that a
// max-token stop with no text and no tool call is surfaced as an error instead
// of being rendered to Codex as a successful empty assistant turn.
func TestAnthropicResponseToIRRejectsEmptyMaxTokenResponse(t *testing.T) {
	_, err := anthropicResponseToIR([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":""}],"stop_reason":"max_tokens","usage":{"input_tokens":11424,"output_tokens":1024}}`))
	if err == nil {
		t.Fatal("anthropicResponseToIR returned nil, want error for empty max-token response")
	}
	if !strings.Contains(err.Error(), "empty") || !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("error = %q, want mention empty max_tokens response", err.Error())
	}
}

// TestIRToAnthropicRequestMapsSystemToTopLevel verifies that an IR
// system message is emitted as the Anthropic top-level "system" field
// rather than being dropped or rejected. When multiple system parts are
// present they are concatenated with newlines (matching the Anthropic
// multi-block system shape, but flattened to a single string for the
// MVP's text-only scope).
func TestIRToAnthropicRequestMapsSystemToTopLevel(t *testing.T) {
	t.Run("system_then_user", func(t *testing.T) {
		req := IRRequest{
			Model:     "m",
			MaxTokens: 16,
			Messages: []IRMessage{
				{Role: "system", Parts: []IRPart{{Type: irPartText, Text: "be brief"}}},
				{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "hi"}}},
			},
		}
		data, err := irToAnthropicRequest(req)
		if err != nil {
			t.Fatalf("irToAnthropicRequest returned error: %v", err)
		}
		var got struct {
			Model    string                    `json:"model"`
			System   string                    `json:"system"`
			Messages []anthropicRequestMessage `json:"messages"`
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v\nbody: %s", err, string(data))
		}
		if got.System != "be brief" {
			t.Fatalf("system = %q, want \"be brief\"\nbody: %s", got.System, string(data))
		}
		// Only the user turn should appear in messages; system is hoisted.
		if len(got.Messages) != 1 {
			t.Fatalf("messages len = %d, want 1 (system hoisted)\nbody: %s", len(got.Messages), string(data))
		}
		if got.Messages[0].Role != "user" {
			t.Fatalf("messages[0].role = %q, want user\nbody: %s", got.Messages[0].Role, string(data))
		}
		// Ensure system text is NOT also duplicated inside messages.
		for _, m := range got.Messages {
			for _, c := range m.Content {
				if c.Text == "be brief" {
					t.Fatalf("system text leaked into messages: %+v\nbody: %s", got.Messages, string(data))
				}
			}
		}
	})

	t.Run("system_only_still_emits_messages", func(t *testing.T) {
		// A system-only IR request is degenerate but must not crash;
		// the adapter emits the system field and an empty messages
		// array. (ValidateTextOnly already requires >=1 message, and a
		// system message satisfies that, so we don't reject here.)
		req := IRRequest{
			Model:     "m",
			MaxTokens: 16,
			Messages: []IRMessage{
				{Role: "system", Parts: []IRPart{{Type: irPartText, Text: "be brief"}}},
			},
		}
		data, err := irToAnthropicRequest(req)
		if err != nil {
			t.Fatalf("irToAnthropicRequest returned error: %v", err)
		}
		if !strings.Contains(string(data), `"system":"be brief"`) {
			t.Fatalf("body missing system field: %s", string(data))
		}
	})

	t.Run("multiple_system_parts_concatenated", func(t *testing.T) {
		req := IRRequest{
			Model:     "m",
			MaxTokens: 16,
			Messages: []IRMessage{
				{Role: "system", Parts: []IRPart{
					{Type: irPartText, Text: "rule one"},
					{Type: irPartText, Text: "rule two"},
				}},
				{Role: "user", Parts: []IRPart{{Type: irPartText, Text: "hi"}}},
			},
		}
		data, err := irToAnthropicRequest(req)
		if err != nil {
			t.Fatalf("irToAnthropicRequest returned error: %v", err)
		}
		if !strings.Contains(string(data), `"system":"rule one\nrule two"`) {
			t.Fatalf("body missing concatenated system: %s", string(data))
		}
	})
}

// TestIRToAnthropicRequestRejectsNonTextPart verifies that the text-only
// validation surfaces an actionable error when the IR carries an
// unsupported part type.
func TestIRToAnthropicRequestRejectsImagePart(t *testing.T) {
	req := IRRequest{
		Model:     "m",
		MaxTokens: 16,
		Messages:  []IRMessage{{Role: "user", Parts: []IRPart{{Type: irPartImage}}}},
	}
	_, err := irToAnthropicRequest(req)
	if err == nil {
		t.Fatal("irToAnthropicRequest returned nil, want error for image part")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Fatalf("error = %q, want mention image", err.Error())
	}
}

func TestAnthropicResponseToIRAcceptsToolUseContent(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"tool_use","id":"x","name":"n","input":{}}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
	resp, err := anthropicResponseToIR(body)
	if err != nil {
		t.Fatalf("anthropicResponseToIR returned error: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "x" || resp.ToolCalls[0].Name != "n" {
		t.Fatalf("tool calls = %#v", resp.ToolCalls)
	}
}

// TestAnthropicResponseToIRConcatenatesMultipleTextBlocks verifies that
// when the response carries several text blocks their text is concatenated
// in order.
func TestAnthropicResponseToIRConcatenatesMultipleTextBlocks(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":3}}`)
	resp, err := anthropicResponseToIR(body)
	if err != nil {
		t.Fatalf("anthropicResponseToIR returned error: %v", err)
	}
	if resp.Text != "Hello world" {
		t.Fatalf("text = %q, want %q", resp.Text, "Hello world")
	}
}

// TestAnthropicResponseToIROmitsUsageWhenAbsent verifies that a missing
// usage object yields a nil Usage pointer in the IR.
func TestAnthropicResponseToIROmitsUsageWhenAbsent(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"MiniMax-M3","content":[{"type":"text","text":"Hi"}],"stop_reason":"end_turn"}`)
	resp, err := anthropicResponseToIR(body)
	if err != nil {
		t.Fatalf("anthropicResponseToIR returned error: %v", err)
	}
	if resp.Usage != nil {
		t.Fatalf("usage = %+v, want nil when upstream omitted it", resp.Usage)
	}
}
