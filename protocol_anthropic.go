package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// anthropicDefaultMaxTokens is used when an inbound protocol does not provide
// an explicit output cap. Codex often omits max_output_tokens; low defaults can
// yield max-token stops with no visible assistant text on larger review/fix
// turns. Xiaomi's Anthropic-compatible endpoint rejects values above 131072, so
// use that provider-supported maximum as the proxy default.
const anthropicDefaultMaxTokens = 131072

// anthropicRequestMessage is a single message entry in the Anthropic
// Messages API request body. The MVP carries only text content blocks.
type anthropicRequestMessage struct {
	Role    string                    `json:"role"`
	Content []anthropicRequestContent `json:"content"`
}

// anthropicRequestContent is a single content block. The MVP only ever
// emits the "text" type.
type anthropicRequestContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

// anthropicRequestBody is the wire shape of a non-streaming Anthropic
// Messages API request. System is the top-level system-prompt field; the
// MVP carries it as a single string (multiple system parts are
// concatenated with newlines).
type anthropicRequestBody struct {
	Model     string                    `json:"model"`
	MaxTokens int                       `json:"max_tokens"`
	System    string                    `json:"system,omitempty"`
	Stream    bool                      `json:"stream,omitempty"`
	Messages  []anthropicRequestMessage `json:"messages,omitempty"`
	Tools     []anthropicTool           `json:"tools,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicRawRequest struct {
	Model     string                `json:"model"`
	MaxTokens int                   `json:"max_tokens"`
	System    string                `json:"system,omitempty"`
	Stream    bool                  `json:"stream,omitempty"`
	Messages  []anthropicRawMessage `json:"messages,omitempty"`
	Tools     []anthropicTool       `json:"tools,omitempty"`
}

type anthropicRawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthropicRequestToIR translates an Anthropic Messages API request body
// into the provider-agnostic IRRequest. Only text content is supported
// in the MVP; any tool/image/audio part surfaces as an error. Streaming
// is rejected. An absent or empty "model" field is NOT rejected here:
// the proxy's model-resolution layer (route.ModelMappings["default"]
// then route.Model) supplies the upstream model, so an empty incoming
// model must be allowed to reach that layer. The model-empty check is
// enforced by the proxy after resolution.
func anthropicRequestToIR(body []byte) (IRRequest, error) {
	var raw anthropicRawRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRRequest{}, fmt.Errorf("anthropic request: parse body: %w", err)
	}
	if raw.MaxTokens < 0 {
		return IRRequest{}, fmt.Errorf("anthropic request: max_tokens must be >= 0 (got %d)", raw.MaxTokens)
	}
	messages := make([]IRMessage, 0, len(raw.Messages)+1)
	if raw.System != "" {
		messages = append(messages, IRMessage{Role: "system", Parts: []IRPart{{Type: irPartText, Text: raw.System}}})
	}
	for i, msg := range raw.Messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			return IRRequest{}, fmt.Errorf("anthropic request: message %d has unsupported role %q", i, msg.Role)
		}
		parts, err := anthropicContentToIRParts(i, msg.Content)
		if err != nil {
			return IRRequest{}, err
		}
		messages = append(messages, IRMessage{Role: msg.Role, Parts: parts})
	}
	tools := make([]IRTool, 0, len(raw.Tools))
	for _, tool := range raw.Tools {
		tools = append(tools, IRTool{Name: tool.Name, Description: tool.Description, InputSchema: defaultToolInputSchema(tool.InputSchema)})
	}
	req := IRRequest{Model: raw.Model, Messages: messages, Tools: tools, Stream: raw.Stream, MaxTokens: raw.MaxTokens}
	if err := req.validateTextOnlySkipModel(); err != nil {
		return IRRequest{}, fmt.Errorf("anthropic request: %w", err)
	}
	return req, nil
}

func anthropicContentToIRParts(messageIndex int, content json.RawMessage) ([]IRPart, error) {
	if len(content) == 0 {
		return nil, fmt.Errorf("anthropic request: message %d content must not be empty", messageIndex)
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		if text == "" {
			return nil, fmt.Errorf("anthropic request: message %d content text must not be empty", messageIndex)
		}
		return []IRPart{{Type: irPartText, Text: text}}, nil
	}
	var blocks []anthropicRequestContent
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, fmt.Errorf("anthropic request: message %d content must be a string or text blocks: %w", messageIndex, err)
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("anthropic request: message %d content array must not be empty", messageIndex)
	}
	parts := make([]IRPart, 0, len(blocks))
	for j, part := range blocks {
		switch part.Type {
		case irPartText:
			if part.Text == "" {
				return nil, fmt.Errorf("anthropic request: message %d content block %d text must not be empty", messageIndex, j)
			}
			parts = append(parts, IRPart{Type: irPartText, Text: part.Text})
		case irPartToolUse:
			parts = append(parts, IRPart{Type: irPartToolUse, ToolCall: &IRToolCall{ID: part.ID, Name: part.Name, Input: part.Input}})
		case irPartToolResult:
			parts = append(parts, IRPart{Type: irPartToolResult, ToolResult: &IRToolResult{ToolUseID: part.ToolUseID, Content: part.Content}})
		case irPartImage:
			return nil, fmt.Errorf("anthropic request: message %d content block %d has unsupported type %q", messageIndex, j, part.Type)
		default:
			return nil, fmt.Errorf("anthropic request: message %d content block %d has unsupported type %q", messageIndex, j, part.Type)
		}
	}
	return parts, nil
}

// irToAnthropicRequest renders an IRRequest as an Anthropic Messages API
// request body. The MVP targets text-only traffic: it calls ValidateTextOnly
// so any tool/image/other part surfaces as an explicit error rather than a
// silent degradation. System messages are hoisted into the Anthropic
// top-level "system" field (the Anthropic API models the system prompt as a
// top-level field rather than a message); if multiple system turns are
// present they are concatenated with newlines. Only user and assistant
// turns are emitted as message entries.
func irToAnthropicRequest(req IRRequest) ([]byte, error) {
	if err := req.ValidateTextOnly(); err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = anthropicDefaultMaxTokens
	}

	var systemParts []string
	messages := make([]anthropicRequestMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		// System turns are hoisted into the top-level "system" field.
		// If multiple system turns appear they are concatenated with
		// newlines (the MVP flattens Anthropic's multi-block system
		// shape to a single string for its text-only scope).
		if msg.Role == "system" {
			for _, part := range msg.Parts {
				systemParts = append(systemParts, part.Text)
			}
			continue
		}
		content := make([]anthropicRequestContent, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			switch part.Type {
			case irPartText:
				content = append(content, anthropicRequestContent{Type: irPartText, Text: part.Text})
			case irPartToolUse:
				content = append(content, anthropicRequestContent{Type: irPartToolUse, ID: part.ToolCall.ID, Name: part.ToolCall.Name, Input: part.ToolCall.Input})
			case irPartToolResult:
				content = append(content, anthropicRequestContent{Type: irPartToolResult, ToolUseID: part.ToolResult.ToolUseID, Content: part.ToolResult.Content})
			}
		}
		messages = append(messages, anthropicRequestMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	tools := make([]anthropicTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, anthropicTool{Name: tool.Name, Description: tool.Description, InputSchema: defaultToolInputSchema(tool.InputSchema)})
	}
	body := anthropicRequestBody{
		Model:     req.Model,
		MaxTokens: maxTokens,
		Stream:    req.Stream,
		System:    strings.Join(systemParts, "\n"),
		Messages:  messages,
		Tools:     tools,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: marshal: %w", err)
	}
	return data, nil
}

func irToAnthropicResponse(resp IRResponse) ([]byte, error) {
	stopReason := "end_turn"
	if resp.StopReason == "tool_use" {
		stopReason = "tool_use"
	} else if resp.StopReason == responsesStopReasonMaxTokens {
		stopReason = "max_tokens"
	}
	content := make([]anthropicResponseContent, 0, 1+len(resp.ToolCalls))
	if resp.Text != "" || len(resp.ToolCalls) == 0 {
		content = append(content, anthropicResponseContent{Type: irPartText, Text: resp.Text})
	}
	for _, call := range resp.ToolCalls {
		content = append(content, anthropicResponseContent{Type: irPartToolUse, ID: call.ID, Name: call.Name, Input: call.Input})
	}
	out := anthropicResponseBody{
		ID:         responsesMessageID(resp.ID),
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		Content:    content,
		StopReason: stopReason,
	}
	if resp.Usage != nil {
		out.Usage = &anthropicResponseUsage{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("anthropic response: marshal: %w", err)
	}
	return data, nil
}

// anthropicResponseContent is a single content block inside the Anthropic
// message response payload.
type anthropicResponseContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// anthropicResponseUsage is the token accounting reported by the Anthropic
// API.
type anthropicResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anthropicResponseBody is the wire shape of a completed (non-streaming)
// Anthropic Messages API response.
type anthropicResponseBody struct {
	ID         string                     `json:"id"`
	Type       string                     `json:"type"`
	Role       string                     `json:"role"`
	Model      string                     `json:"model"`
	Content    []anthropicResponseContent `json:"content"`
	StopReason string                     `json:"stop_reason"`
	Usage      *anthropicResponseUsage    `json:"usage"`
}

// anthropicResponseToIR parses an Anthropic Messages API response body and
// produces a provider-agnostic IRResponse. Only text content blocks are
// supported; any other content type (e.g. tool_use) surfaces as an error
// that names the offending type so callers can act on it. Text from
// multiple text blocks is concatenated in order. Total tokens are computed
// as the sum of input and output when usage is present.
func anthropicResponseToIR(body []byte) (IRResponse, error) {
	var raw anthropicResponseBody
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRResponse{}, fmt.Errorf("anthropic response: parse body: %w", err)
	}

	var b strings.Builder
	var calls []IRToolCall
	for i, block := range raw.Content {
		switch block.Type {
		case irPartText:
			b.WriteString(block.Text)
		case irPartToolUse:
			calls = append(calls, IRToolCall{ID: block.ID, Name: block.Name, Input: block.Input})
		default:
			return IRResponse{}, fmt.Errorf("anthropic response: content block %d has unsupported type %q", i, block.Type)
		}
	}
	stopReason := raw.StopReason
	if stopReason == "end_turn" {
		stopReason = "stop"
	}
	if raw.StopReason == "max_tokens" && b.String() == "" && len(calls) == 0 {
		return IRResponse{}, fmt.Errorf("anthropic response: empty text with max_tokens stop")
	}

	resp := IRResponse{
		ID:         raw.ID,
		Model:      raw.Model,
		Text:       b.String(),
		ToolCalls:  calls,
		StopReason: stopReason,
	}
	if raw.Usage != nil {
		resp.Usage = &IRUsage{
			InputTokens:  raw.Usage.InputTokens,
			OutputTokens: raw.Usage.OutputTokens,
			TotalTokens:  raw.Usage.InputTokens + raw.Usage.OutputTokens,
		}
	}
	return resp, nil
}
