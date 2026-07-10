package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"bytes"
)

// responsesInputTextType is the content-block type used by OpenAI's
// Responses API for plain user-supplied text.
const responsesInputTextType = "input_text"

// responsesOutputTextType is the content-block type used by OpenAI's
// Responses API for assistant output text in the response payload.
const responsesOutputTextType = "output_text"

// responsesStatusCompleted is the Responses API status for a finished,
// fully-formed response.
const responsesStatusCompleted = "completed"

// responsesStatusIncomplete is the Responses API status for a response
// that stopped because of a limit (e.g. max_output_tokens) before
// reaching a natural end.
const responsesStatusIncomplete = "incomplete"

// responsesStopReasonMaxTokens is the IR stop reason that maps to the
// incomplete Responses status.
const responsesStopReasonMaxTokens = "max_tokens"

// responsesRawRequest models the subset of the OpenAI Responses API
// request schema that this adapter understands for the MVP. The Input
// field is decoded lazily because the wire shape is a union: either a
// plain string or an array of message objects.
//
// Honoured fields: model/input/stream/max_output_tokens/instructions/text.
// Instructions is a top-level system-prompt string mapped onto a leading
// IR system message. text.format=json_schema is translated into an
// additional system instruction because downstream Anthropic translation
// cannot carry the Responses text config natively.
//
// Accepted-and-ignored fields (non-semantic for a simple text response):
//   - prompt_cache_key — Codex cache hint.
//   - client_metadata — Codex client telemetry.
//   - store — Responses-API server-side history toggle; the proxy never
//     persists server-side, so this is a no-op.
//   - include — extra payload sections the client wants echoed back
//     (e.g. file_search_call.results); none are honoured, so ignored.
//   - tools / tool_choice / parallel_tool_calls — Codex always sends a
//     tool definition array. The MVP does NOT convert tool definitions
//     or execute tool calls; accepting and ignoring these fields ONLY
//     lets simple-text requests that carry an unused tools array run
//     end-to-end. Tool-call round-trips are not supported.
//   - reasoning — Codex sends reasoning hints for some requests; the MVP
//     does not honour them, so they are accepted and ignored.
//   - service_tier — OpenAI routing hint; no equivalent in the local
//     proxy path, so it is accepted and ignored.
//
// Every other top-level key (temperature, top_p, previous_response_id,
// metadata, user, ...) is rejected by
// responsesRejectUnsupportedTopLevelFields so that unsupported features
// surface as 400s rather than being silently dropped (which would change
// request semantics).
type responsesRawRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    json.RawMessage `json:"instructions"`
	Reasoning       json.RawMessage `json:"reasoning"`
	Text            json.RawMessage `json:"text"`
	Tools           []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

type responsesTextConfig struct {
	Verbosity string               `json:"verbosity,omitempty"`
	Format    *responsesTextFormat `json:"format,omitempty"`
}

type responsesTextFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Strict bool            `json:"strict,omitempty"`
}

type responsesOutboundRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []any           `json:"input"`
	Tools           []responsesTool `json:"tools,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// responsesAllowedTopLevelFields is the set of top-level OpenAI Responses
// request fields the MVP either honours or explicitly accepts-and-ignores.
// Any other key surfaces as a 400.
var responsesAllowedTopLevelFields = map[string]bool{
	"model":               true,
	"input":               true,
	"stream":              true,
	"max_output_tokens":   true,
	"instructions":        true,
	"prompt_cache_key":    true,
	"client_metadata":     true,
	"store":               true,
	"include":             true,
	"tools":               true,
	"tool_choice":         true,
	"parallel_tool_calls": true,
	"reasoning":           true,
	"text":                true,
	"service_tier":        true,
}

// responsesRawMessage models a single element of the Input array form.
// RolePtr is a pointer so we can distinguish an absent role key from an
// explicitly empty one; the MVP requires role to be present and equal to
// "user", "assistant", or "developer". Developer messages are mapped to
// IR system messages. Content is left as RawMessage because each part may carry a
// type that this adapter does not support; we decode and validate
// explicitly.
type responsesRawMessage struct {
	Type    string          `json:"type,omitempty"`
	RolePtr *string         `json:"role"`
	Content json.RawMessage `json:"content"`
	CallID  string          `json:"call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	Args    string          `json:"arguments,omitempty"`
	Output  string          `json:"output,omitempty"`
}

// responsesRawContentPart models a content part inside a message.
type responsesRawContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responsesRejectUnsupportedTopLevelFields returns an error if any
// top-level field of the request JSON object is not accepted by the MVP.
// Every rejected field is named explicitly so callers can fix the
// request rather than hitting a silent behaviour change.
//
// The accepted set is responsesAllowedTopLevelFields, which includes
// both honoured fields and a set of explicitly accepted-and-ignored
// fields that real Codex sends but that have no semantic effect on a
// simple text response (prompt_cache_key, client_metadata, store,
// include, tools, tool_choice, parallel_tool_calls, reasoning). All
// other keys (temperature, top_p, previous_response_id, metadata,
// user, ...) are rejected with a message that includes the offending
// field name.
func responsesRejectUnsupportedTopLevelFields(body []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		// A malformed object surfaces a parse error earlier in the
		// pipeline; here we cannot inspect keys so we return nil and
		// let the strict decode below report the failure.
		return nil
	}
	for name := range probe {
		if !responsesAllowedTopLevelFields[name] {
			return fmt.Errorf("responses request: %q is not supported in the MVP", name)
		}
	}
	return nil
}

// responsesRequestToIR translates an OpenAI Responses API request body
// into the provider-agnostic IRRequest. Only text content is supported
// in the MVP; any tool/image/audio part surfaces as an error. A
// stream:true request sets IRRequest.Stream=true (the proxy wraps the
// completed response as an SSE event stream client-side; it does not
// implement Anthropic SSE upstream). Any top-level field outside the
// accepted set is rejected rather than silently dropped. The accepted
// non-semantic fields (prompt_cache_key, client_metadata, store,
// include, tools, tool_choice, parallel_tool_calls, reasoning,
// service_tier) are ignored. instructions (when a non-empty string) is
// mapped onto a leading IR system message. text.format=json_schema is
// translated into a system instruction that asks for schema-shaped JSON.
// A negative max_output_tokens is rejected rather than silently clamped
// to the default.
//
// An absent or empty "model" field is NOT rejected here: the proxy's
// model-resolution layer (route.ModelMappings["default"] then
// route.Model) supplies the upstream model, so an empty incoming model
// must be allowed to reach that layer. The model-empty check is
// enforced by the proxy after resolution.
func responsesRequestToIR(body []byte) (IRRequest, error) {
	if err := responsesRejectUnsupportedTopLevelFields(body); err != nil {
		return IRRequest{}, err
	}
	var raw responsesRawRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRRequest{}, fmt.Errorf("responses request: parse body: %w", err)
	}
	if raw.MaxOutputTokens < 0 {
		return IRRequest{}, fmt.Errorf("responses request: max_output_tokens must be >= 0 (got %d)", raw.MaxOutputTokens)
	}

	messages, err := responsesInputToMessages(raw.Input)
	if err != nil {
		return IRRequest{}, err
	}

	// instructions is a top-level system prompt. The MVP maps it onto a
	// leading IR system message so downstream adapters (e.g. the
	// Anthropic adapter) can hoist it into the provider's system field.
	// An absent instructions is a no-op; an explicitly empty string is
	// an error rather than a silent empty system message, and a
	// non-string value is rejected because the MVP only supports the
	// documented string form.
	var instructions string
	if len(raw.Instructions) > 0 && !bytes.Equal(raw.Instructions, []byte("null")) {
		if err := json.Unmarshal(raw.Instructions, &instructions); err != nil {
			return IRRequest{}, fmt.Errorf("responses request: instructions must be a string")
		}
		if instructions == "" {
			return IRRequest{}, fmt.Errorf("responses request: instructions must not be empty")
		}
	}
	textInstruction, err := responsesTextConfigInstruction(raw.Text)
	if err != nil {
		return IRRequest{}, err
	}
	if textInstruction != "" {
		if instructions != "" {
			instructions += "\n\n" + textInstruction
		} else {
			instructions = textInstruction
		}
	}
	if instructions != "" {
		sysMsg := IRMessage{
			Role:  "system",
			Parts: []IRPart{{Type: irPartText, Text: instructions}},
		}
		messages = append([]IRMessage{sysMsg}, messages...)
	}

	req := IRRequest{
		Model:     raw.Model,
		Messages:  messages,
		Tools:     responsesToolsToIR(raw.Tools),
		Stream:    raw.Stream,
		MaxTokens: raw.MaxOutputTokens,
	}
	if err := req.validateTextOnlySkipModel(); err != nil {
		return IRRequest{}, fmt.Errorf("responses request: %w", err)
	}
	return req, nil
}

func responsesTextConfigInstruction(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var cfg responsesTextConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("responses request: text must be an object")
	}
	if cfg.Format == nil {
		return "", nil
	}
	switch cfg.Format.Type {
	case "text":
		return "", nil
	case "json_schema":
		if len(cfg.Format.Schema) == 0 || string(cfg.Format.Schema) == "null" {
			return "", fmt.Errorf("responses request: text.format.schema must be present for json_schema")
		}
		var schema any
		if err := json.Unmarshal(cfg.Format.Schema, &schema); err != nil {
			return "", fmt.Errorf("responses request: text.format.schema must be valid JSON")
		}
		compactSchema, err := json.Marshal(schema)
		if err != nil {
			return "", fmt.Errorf("responses request: text.format.schema: %w", err)
		}
		name := cfg.Format.Name
		if name == "" {
			name = "response_schema"
		}
		return fmt.Sprintf("Respond only with JSON matching the requested schema. Do not include markdown fences or explanatory text. Schema name: %s. Schema: %s", name, string(compactSchema)), nil
	case "":
		return "", fmt.Errorf("responses request: text.format.type must be present")
	default:
		return "", fmt.Errorf("responses request: text.format.type %q is not supported in the MVP", cfg.Format.Type)
	}
}

func irToResponsesRequest(req IRRequest) ([]byte, error) {
	if err := req.ValidateTextOnly(); err != nil {
		return nil, fmt.Errorf("responses request: %w", err)
	}
	var instructions []string
	input := make([]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			var b strings.Builder
			for _, part := range msg.Parts {
				if part.Type == irPartText {
					b.WriteString(part.Text)
				}
			}
			instructions = append(instructions, b.String())
			continue
		}
		var pendingText strings.Builder
		flushText := func() error {
			if pendingText.Len() == 0 {
				return nil
			}
			contentType := responsesInputTextType
			if msg.Role == "assistant" {
				contentType = responsesOutputTextType
			}
			content, err := json.Marshal([]responsesRawContentPart{{Type: contentType, Text: pendingText.String()}})
			if err != nil {
				return fmt.Errorf("responses request: marshal content: %w", err)
			}
			role := msg.Role
			input = append(input, responsesRawMessage{RolePtr: &role, Content: content})
			pendingText.Reset()
			return nil
		}
		for _, part := range msg.Parts {
			switch part.Type {
			case irPartText:
				pendingText.WriteString(part.Text)
			case irPartToolUse:
				if err := flushText(); err != nil {
					return nil, err
				}
				args := string(part.ToolCall.Input)
				if args == "" {
					args = "{}"
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": part.ToolCall.ID, "name": part.ToolCall.Name, "arguments": args})
			case irPartToolResult:
				if err := flushText(); err != nil {
					return nil, err
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": part.ToolResult.ToolUseID, "output": part.ToolResult.Content})
			}
		}
		if err := flushText(); err != nil {
			return nil, err
		}
	}
	out := responsesOutboundRequest{Model: req.Model, Instructions: strings.Join(instructions, "\n"), Input: input, Tools: irToolsToResponses(req.Tools), MaxOutputTokens: req.MaxTokens, Stream: req.Stream}
	return json.Marshal(out)
}

func responsesToolsToIR(tools []responsesTool) []IRTool {
	out := make([]IRTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type == "function" || tool.Type == "" {
			out = append(out, IRTool{Name: tool.Name, Description: tool.Description, InputSchema: defaultToolInputSchema(tool.Parameters)})
		}
	}
	return out
}

func irToolsToResponses(tools []IRTool) []responsesTool {
	out := make([]responsesTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, responsesTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: defaultToolInputSchema(tool.InputSchema)})
	}
	return out
}

func buildOpenAIResponsesUpstreamRequest(req IRRequest) ([]byte, error) {
	return irToResponsesRequest(req)
}

// responsesInputToMessages normalises the Input field (string or array)
// into IRMessage values carrying only text parts.
func responsesInputToMessages(input json.RawMessage) ([]IRMessage, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("input must not be empty")
	}

	// String form: a single user turn whose content is the whole string.
	var asString string
	if err := json.Unmarshal(input, &asString); err == nil {
		if asString == "" {
			return nil, fmt.Errorf("input string must not be empty")
		}
		return []IRMessage{{
			Role:  "user",
			Parts: []IRPart{{Type: irPartText, Text: asString}},
		}}, nil
	}

	// Array form: a sequence of message objects. To support Codex CLI's
	// normal conversation payload, the proxy accepts text-only user and
	// assistant messages, plus developer messages which map to IR system
	// instructions. Any absent or different role is rejected rather than
	// silently coerced.
	var rawMessages []responsesRawMessage
	if err := json.Unmarshal(input, &rawMessages); err != nil {
		return nil, fmt.Errorf("input must be a string or an array of messages: %w", err)
	}
	if len(rawMessages) == 0 {
		return nil, fmt.Errorf("input array must not be empty")
	}

	messages := make([]IRMessage, 0, len(rawMessages))
	for i, rm := range rawMessages {
		if rm.Type == "function_call" {
			messages = append(messages, IRMessage{Role: "assistant", Parts: []IRPart{{Type: irPartToolUse, ToolCall: &IRToolCall{ID: rm.CallID, Name: rm.Name, Input: json.RawMessage(rm.Args)}}}})
			continue
		}
		if rm.Type == "function_call_output" {
			messages = append(messages, IRMessage{Role: "user", Parts: []IRPart{{Type: irPartToolResult, ToolResult: &IRToolResult{ToolUseID: rm.CallID, Content: rm.Output}}}})
			continue
		}
		if rm.RolePtr == nil || *rm.RolePtr == "" {
			return nil, fmt.Errorf("input[%d]: role is required (MVP supports only \"developer\", \"user\", and \"assistant\")", i)
		}
		role := *rm.RolePtr
		if role != "developer" && role != "user" && role != "assistant" {
			return nil, fmt.Errorf("input[%d]: role %q is not supported in the MVP (only \"developer\", \"user\", and \"assistant\")", i, role)
		}
		if role == "developer" {
			role = "system"
		}
		parts, err := responsesContentToParts(i, role, rm.Content)
		if err != nil {
			return nil, err
		}
		messages = append(messages, IRMessage{Role: role, Parts: parts})
	}
	return messages, nil
}

// responsesContentToParts decodes a message's Content field into text
// IRParts. Content may be either a plain string or an array of typed
// parts; non-text part types and empty text are rejected.
func responsesContentToParts(msgIndex int, role string, content json.RawMessage) ([]IRPart, error) {
	if len(content) == 0 {
		return nil, fmt.Errorf("message %d has no content", msgIndex)
	}

	// String content.
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		if asString == "" {
			return nil, fmt.Errorf("message %d content string must not be empty", msgIndex)
		}
		return []IRPart{{Type: irPartText, Text: asString}}, nil
	}

	// Array of typed parts.
	var parts []responsesRawContentPart
	if err := json.Unmarshal(content, &parts); err != nil {
		return nil, fmt.Errorf("message %d content must be a string or an array of parts: %w", msgIndex, err)
	}
	if len(parts) == 0 {
		return nil, fmt.Errorf("message %d content array must not be empty", msgIndex)
	}

	out := make([]IRPart, 0, len(parts))
	for j, p := range parts {
		wantType := responsesInputTextType
		if role == "assistant" {
			wantType = responsesOutputTextType
		}
		if p.Type != wantType {
			return nil, fmt.Errorf("message %d part %d has unsupported content type %q (only %q is supported)", msgIndex, j, p.Type, wantType)
		}
		if p.Text == "" {
			return nil, fmt.Errorf("message %d part %d has empty text (input_text.text is required and must be non-empty)", msgIndex, j)
		}
		out = append(out, IRPart{Type: irPartText, Text: p.Text})
	}
	return out, nil
}

// responsesMessageOutput is the wire shape of a single entry in the
// Response object's output array. The MVP only ever emits a single
// message part carrying the assistant text.
type responsesMessageOutput struct {
	Type    string                    `json:"type"` // always "message"
	Role    string                    `json:"role"` // always "assistant"
	Status  string                    `json:"status"`
	ID      string                    `json:"id"`
	Content []responsesMessageContent `json:"content"`
	CallID  string                    `json:"call_id,omitempty"`
	Name    string                    `json:"name,omitempty"`
	Args    string                    `json:"arguments,omitempty"`
}

// responsesMessageContent is a single content part inside the message
// output array.
type responsesMessageContent struct {
	Type string `json:"type"` // always "output_text"
	Text string `json:"text"`
}

// responsesUsage is the usage object in the response payload.
type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// responsesRawResponse is the wire shape of a completed (non-streaming)
// Responses API payload. Usage is a pointer so it can be omitted from the
// JSON output when the upstream provider did not report token accounting.
type responsesRawResponse struct {
	ID         string                   `json:"id"`
	Object     string                   `json:"object"` // always "response"
	Status     string                   `json:"status"` // "completed" or "incomplete"
	Model      string                   `json:"model"`
	Output     []responsesMessageOutput `json:"output"`
	OutputText string                   `json:"output_text"`
	Usage      *responsesUsage          `json:"usage,omitempty"`
}

// irToResponsesResponse renders an IRResponse as an OpenAI Responses
// API completion payload. The MVP only supports non-streaming responses.
// The top-level response id is normalised to the "resp_" namespace and
// the emitted message id to the "msg_" namespace; when resp.Usage is nil
// the usage field is omitted entirely.
func irToResponsesResponse(resp IRResponse) ([]byte, error) {
	output := make([]responsesMessageOutput, 0, 1+len(resp.ToolCalls))
	if resp.Text != "" || len(resp.ToolCalls) == 0 {
		output = append(output, responsesMessageOutput{
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			ID:     responsesMessageID(resp.ID),
			Content: []responsesMessageContent{{
				Type: responsesOutputTextType,
				Text: resp.Text,
			}},
		})
	}
	for _, call := range resp.ToolCalls {
		output = append(output, responsesMessageOutput{Type: "function_call", Status: "completed", CallID: call.ID, Name: call.Name, Args: string(call.Input)})
	}
	out := responsesRawResponse{
		ID:         responsesResponseID(resp.ID),
		Object:     "response",
		Status:     responsesStatusFor(resp.StopReason),
		Model:      resp.Model,
		Output:     output,
		OutputText: resp.Text,
	}
	if resp.Usage != nil {
		out.Usage = &responsesUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("responses response: marshal: %w", err)
	}
	return data, nil
}

func responsesResponseToIR(body []byte) (IRResponse, error) {
	var raw responsesRawResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRResponse{}, fmt.Errorf("responses response: parse body: %w", err)
	}
	text := raw.OutputText
	var calls []IRToolCall
	var b strings.Builder
	for _, item := range raw.Output {
		if item.Type == "function_call" {
			calls = append(calls, IRToolCall{ID: item.CallID, Name: item.Name, Input: json.RawMessage(item.Args)})
			continue
		}
		if text == "" {
			for _, part := range item.Content {
				if part.Type != responsesOutputTextType {
					return IRResponse{}, fmt.Errorf("responses response: unsupported output content type %q", part.Type)
				}
				b.WriteString(part.Text)
			}
		}
	}
	if text == "" {
		text = b.String()
	}
	stopReason := responsesStatusToIRStopReason(raw.Status)
	if len(calls) > 0 {
		stopReason = "tool_use"
	}
	resp := IRResponse{ID: raw.ID, Model: raw.Model, Text: text, ToolCalls: calls, StopReason: stopReason}
	if raw.Usage != nil {
		resp.Usage = &IRUsage{InputTokens: raw.Usage.InputTokens, OutputTokens: raw.Usage.OutputTokens, TotalTokens: raw.Usage.TotalTokens}
	}
	return resp, nil
}

func parseOpenAIResponsesUpstreamResponse(body []byte, contentType string) (IRResponse, error) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return responsesStreamToIR(body)
	}
	return responsesResponseToIR(body)
}

func responsesStatusToIRStopReason(status string) string {
	if status == responsesStatusIncomplete {
		return responsesStopReasonMaxTokens
	}
	return "stop"
}

func responsesStreamToIR(body []byte) (IRResponse, error) {
	// Normalise CRLF to LF so the SSE frame/line splitters below work
	// regardless of which line ending the upstream emits. Some upstreams
	// (and intermediary proxies) emit "\r\n\r\n" frame delimiters and
	// "\r\n" line terminators; without this normalisation the "\n\n"
	// split would not recognise the delimiter and the whole stream would
	// collapse into a single unparseable frame.
	normalised := strings.ReplaceAll(string(body), "\r\n", "\n")
	frames := strings.Split(normalised, "\n\n")
	var text strings.Builder
	var completed *IRResponse
	var calls []IRToolCall
	callIDByIndex := map[int]string{}
	nameByIndex := map[int]string{}
	argsByIndex := map[int]string{}
	for _, frame := range frames {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		var dataLines []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(dataLines) == 0 {
			continue
		}
		data := strings.Join(dataLines, "\n")
		if data == "[DONE]" {
			continue
		}
		var event struct {
			Type        string          `json:"type"`
			Delta       string          `json:"delta"`
			OutputIndex int             `json:"output_index"`
			Item        struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			Response json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return IRResponse{}, fmt.Errorf("responses stream: parse event: %w", err)
		}
		switch event.Type {
		case "response.output_text.delta":
			text.WriteString(event.Delta)
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				callIDByIndex[event.OutputIndex] = event.Item.CallID
				nameByIndex[event.OutputIndex] = event.Item.Name
				if event.Item.Arguments != "" {
					argsByIndex[event.OutputIndex] = event.Item.Arguments
				}
			}
		case "response.function_call_arguments.delta":
			if _, ok := callIDByIndex[event.OutputIndex]; ok {
				argsByIndex[event.OutputIndex] += event.Delta
			}
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				id := event.Item.CallID
				if id == "" {
					id = callIDByIndex[event.OutputIndex]
				}
				name := event.Item.Name
				if name == "" {
					name = nameByIndex[event.OutputIndex]
				}
				args := event.Item.Arguments
				if args == "" {
					args = argsByIndex[event.OutputIndex]
				}
				calls = append(calls, IRToolCall{ID: id, Name: name, Input: json.RawMessage(args)})
			}
		case "response.completed":
			if len(event.Response) > 0 {
				resp, err := responsesResponseToIR(event.Response)
				if err != nil {
					return IRResponse{}, err
				}
				completed = &resp
			}
		}
	}
	if completed != nil {
		if completed.Text == "" {
			completed.Text = text.String()
		}
		if len(completed.ToolCalls) == 0 {
			completed.ToolCalls = calls
		}
		if len(completed.ToolCalls) > 0 {
			completed.StopReason = "tool_use"
		}
		return *completed, nil
	}
	if text.Len() == 0 && len(calls) == 0 {
		return IRResponse{}, fmt.Errorf("responses stream: no completed response or text delta")
	}
	resp := IRResponse{Text: text.String(), ToolCalls: calls, StopReason: "stop"}
	if len(calls) > 0 {
		resp.StopReason = "tool_use"
	}
	return resp, nil
}

// responsesResponseID normalises the top-level response identifier so it
// always carries the "resp_" prefix used by the Responses API. An ID that
// already begins with "resp_" is preserved verbatim; any other non-empty
// value is prefixed; an empty input falls back to a stable placeholder.
func responsesResponseID(id string) string {
	if id == "" {
		return "resp_code_switch"
	}
	if strings.HasPrefix(id, "resp_") {
		return id
	}
	return "resp_" + id
}

// responsesStatusFor maps an IR stop reason onto a Responses API status
// string. A max_tokens stop reason indicates the response was truncated
// by an output limit and maps to "incomplete"; everything else (end_turn,
// stop, unknown, etc.) maps to "completed".
func responsesStatusFor(stopReason string) string {
	if stopReason == responsesStopReasonMaxTokens {
		return responsesStatusIncomplete
	}
	return responsesStatusCompleted
}

// responsesMessageID derives a stable identifier for the emitted message
// object from the response id. The Responses API uses a separate id
// namespace for message objects ("msg_..."); when the input is already a
// "msg_" id it is preserved verbatim, when it is a "resp_" id the prefix
// is swapped, any other non-empty value is prefixed, and an empty input
// falls back to a deterministic placeholder.
func responsesMessageID(respID string) string {
	if respID == "" {
		return "msg_code_switch"
	}
	if strings.HasPrefix(respID, "msg_") {
		return respID
	}
	if strings.HasPrefix(respID, "resp_") {
		return "msg_" + respID[len("resp_"):]
	}
	return "msg_" + respID
}
