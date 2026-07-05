package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type openAIChatMessage struct {
	Role       string               `json:"role"`
	Content    string               `json:"content"`
	ToolCalls  []openAIChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type openAIChatToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type openAIChatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type openAIChatRequest struct {
	Model     string              `json:"model"`
	Messages  []openAIChatMessage `json:"messages"`
	Tools     []openAIChatTool    `json:"tools,omitempty"`
	MaxTokens int                 `json:"max_tokens,omitempty"`
	Stream    bool                `json:"stream,omitempty"`
}

func openAIChatRequestToIR(body []byte) (IRRequest, error) {
	var raw openAIChatRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRRequest{}, fmt.Errorf("openai chat request: parse body: %w", err)
	}
	if raw.MaxTokens < 0 {
		return IRRequest{}, fmt.Errorf("openai chat request: max_tokens must be >= 0 (got %d)", raw.MaxTokens)
	}
	if len(raw.Messages) == 0 {
		return IRRequest{}, fmt.Errorf("openai chat request: messages must not be empty")
	}
	messages := make([]IRMessage, 0, len(raw.Messages))
	for i, msg := range raw.Messages {
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
			return IRRequest{}, fmt.Errorf("openai chat request: message %d has unsupported role %q", i, msg.Role)
		}
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			return IRRequest{}, fmt.Errorf("openai chat request: message %d content must not be empty", i)
		}
		if msg.Role == "tool" {
			messages = append(messages, IRMessage{Role: "user", Parts: []IRPart{{Type: irPartToolResult, ToolResult: &IRToolResult{ToolUseID: msg.ToolCallID, Content: msg.Content}}}})
			continue
		}
		parts := make([]IRPart, 0, 1+len(msg.ToolCalls))
		if msg.Content != "" {
			parts = append(parts, IRPart{Type: irPartText, Text: msg.Content})
		}
		for _, tc := range msg.ToolCalls {
			parts = append(parts, IRPart{Type: irPartToolUse, ToolCall: &IRToolCall{ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(tc.Function.Arguments)}})
		}
		messages = append(messages, IRMessage{Role: msg.Role, Parts: parts})
	}
	tools := make([]IRTool, 0, len(raw.Tools))
	for _, tool := range raw.Tools {
		if tool.Type == "" || tool.Type == "function" {
			tools = append(tools, IRTool{Name: tool.Function.Name, Description: tool.Function.Description, InputSchema: defaultToolInputSchema(tool.Function.Parameters)})
		}
	}
	req := IRRequest{Model: raw.Model, Messages: messages, Tools: tools, Stream: raw.Stream, MaxTokens: raw.MaxTokens}
	if err := req.validateTextOnlySkipModel(); err != nil {
		return IRRequest{}, fmt.Errorf("openai chat request: %w", err)
	}
	return req, nil
}

func irToOpenAIChatRequest(req IRRequest) ([]byte, error) {
	if err := req.ValidateTextOnly(); err != nil {
		return nil, fmt.Errorf("openai chat request: %w", err)
	}
	messages := make([]openAIChatMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		var b strings.Builder
		var toolCalls []openAIChatToolCall
		var toolResult *IRToolResult
		for _, part := range msg.Parts {
			switch part.Type {
			case irPartText:
				b.WriteString(part.Text)
			case irPartToolUse:
				var tc openAIChatToolCall
				tc.ID = part.ToolCall.ID
				tc.Type = "function"
				tc.Function.Name = part.ToolCall.Name
				tc.Function.Arguments = string(part.ToolCall.Input)
				toolCalls = append(toolCalls, tc)
			case irPartToolResult:
				toolResult = part.ToolResult
			}
		}
		if toolResult != nil {
			messages = append(messages, openAIChatMessage{Role: "tool", ToolCallID: toolResult.ToolUseID, Content: toolResult.Content})
			continue
		}
		messages = append(messages, openAIChatMessage{Role: msg.Role, Content: b.String(), ToolCalls: toolCalls})
	}
	tools := make([]openAIChatTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		var out openAIChatTool
		out.Type = "function"
		out.Function.Name = tool.Name
		out.Function.Description = tool.Description
		out.Function.Parameters = defaultToolInputSchema(tool.InputSchema)
		tools = append(tools, out)
	}
	out := openAIChatRequest{Model: req.Model, Messages: messages, Tools: tools, MaxTokens: req.MaxTokens, Stream: req.Stream}
	return json.Marshal(out)
}

func buildOpenAIChatUpstreamRequest(req IRRequest) ([]byte, error) {
	return irToOpenAIChatRequest(req)
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string               `json:"role"`
			Content   string               `json:"content"`
			ToolCalls []openAIChatToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

func irToOpenAIChatResponse(resp IRResponse) ([]byte, error) {
	finishReason := "stop"
	if resp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	} else if resp.StopReason == responsesStopReasonMaxTokens {
		finishReason = "length"
	}
	out := openAIChatResponse{ID: resp.ID, Object: "chat.completion", Model: resp.Model}
	out.Choices = append(out.Choices, struct {
		Index   int `json:"index"`
		Message struct {
			Role      string               `json:"role"`
			Content   string               `json:"content"`
			ToolCalls []openAIChatToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{FinishReason: finishReason})
	out.Choices[0].Message.Role = "assistant"
	out.Choices[0].Message.Content = resp.Text
	for _, call := range resp.ToolCalls {
		var tc openAIChatToolCall
		tc.ID = call.ID
		tc.Type = "function"
		tc.Function.Name = call.Name
		tc.Function.Arguments = string(call.Input)
		out.Choices[0].Message.ToolCalls = append(out.Choices[0].Message.ToolCalls, tc)
	}
	if resp.Usage != nil {
		out.Usage = &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{PromptTokens: resp.Usage.InputTokens, CompletionTokens: resp.Usage.OutputTokens, TotalTokens: resp.Usage.TotalTokens}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("openai chat response: marshal: %w", err)
	}
	return data, nil
}

func openAIChatResponseToIR(body []byte) (IRResponse, error) {
	var raw openAIChatResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return IRResponse{}, fmt.Errorf("openai chat response: parse body: %w", err)
	}
	if len(raw.Choices) == 0 {
		return IRResponse{}, fmt.Errorf("openai chat response: choices must not be empty")
	}
	choice := raw.Choices[0]
	if choice.Message.Role != "" && choice.Message.Role != "assistant" {
		return IRResponse{}, fmt.Errorf("openai chat response: unsupported message role %q", choice.Message.Role)
	}
	calls := make([]IRToolCall, 0, len(choice.Message.ToolCalls))
	for _, tc := range choice.Message.ToolCalls {
		calls = append(calls, IRToolCall{ID: tc.ID, Name: tc.Function.Name, Input: json.RawMessage(tc.Function.Arguments)})
	}
	resp := IRResponse{ID: raw.ID, Model: raw.Model, Text: choice.Message.Content, ToolCalls: calls, StopReason: openAIChatStopReasonToIR(choice.FinishReason)}
	if raw.Usage != nil {
		resp.Usage = &IRUsage{InputTokens: raw.Usage.PromptTokens, OutputTokens: raw.Usage.CompletionTokens, TotalTokens: raw.Usage.TotalTokens}
		if resp.Usage.TotalTokens == 0 {
			resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		}
	}
	return resp, nil
}

func parseOpenAIChatUpstreamResponse(body []byte) (IRResponse, error) {
	return openAIChatResponseToIR(body)
}

func openAIChatStopReasonToIR(reason string) string {
	switch reason {
	case "length":
		return responsesStopReasonMaxTokens
	case "tool_calls":
		return "tool_use"
	case "stop":
		return "stop"
	default:
		return reason
	}
}
