package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Part types for the typed IR. These string constants mirror the Anthropic
// content-block naming where applicable so adapters can map them directly.
const (
	irPartText       = "text"
	irPartToolUse    = "tool_use"
	irPartToolResult = "tool_result"
	irPartImage      = "image"
	irPartReasoning  = "reasoning"
)

// IRRequest is the provider-agnostic intermediate representation of an
// inference request. Adapters translate between wire formats (Anthropic
// Messages API, OpenAI Chat Completions, etc.) and this struct.
type IRRequest struct {
	Model     string
	Messages  []IRMessage
	Tools     []IRTool
	Stream    bool
	MaxTokens int
}

type IRTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type IRToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

type IRToolResult struct {
	ToolUseID string
	Content   string
}

// IRMessage is a single conversation turn. Role is one of "system", "user",
// or "assistant". Parts carries the content blocks for the turn.
type IRMessage struct {
	Role  string
	Parts []IRPart
}

// IRPart is a single content block.
type IRPart struct {
	Type       string
	Text       string
	ToolCall   *IRToolCall
	ToolResult *IRToolResult
}

// IRResponse is the provider-agnostic intermediate representation of a
// completed (non-streaming) inference response.
type IRResponse struct {
	ID         string
	Model      string
	Text       string
	ToolCalls  []IRToolCall
	StopReason string
	Usage      *IRUsage
}

// IRUsage reports token accounting for an IRResponse.
type IRUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

func defaultToolInputSchema(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 || string(schema) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return schema
}

// ValidateTextOnly verifies that the request is well-formed and contains only
// text content. It is used by adapters that target chat/completions-only
// providers (e.g. opencode-go via MiniMax) which cannot emit or consume
// tool-use blocks. The check is deliberately strict so that any unsupported
// part type surfaces as an explicit, actionable error rather than a silent
// degradation.
func (req IRRequest) ValidateTextOnly() error {
	if req.Model == "" {
		return fmt.Errorf("ir request: model must not be empty")
	}
	return req.validateTextOnlySkipModel()
}

// validateTextOnlySkipModel performs the same message/part checks as
// ValidateTextOnly but does NOT require a non-empty Model. It is used by
// inbound adapters (responsesRequestToIR, anthropicRequestToIR) so an
// incoming request with an absent/empty model can still be parsed: the
// proxy's model-resolution layer (route.ModelMappings["default"] then
// route.Model) is responsible for supplying the upstream model, and only
// after that resolution does the proxy enforce a non-empty model. This
// keeps the inbound adapters focused on parsing/validating the message
// payload and lets the fallback path run instead of being short-circuited
// by an early "model must not be empty" 400.
func (req IRRequest) validateTextOnlySkipModel() error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("ir request: at least one message is required")
	}
	for i, msg := range req.Messages {
		switch msg.Role {
		case "system", "user", "assistant":
		default:
			return fmt.Errorf("ir request: message %d has invalid role %q (want one of system, user, assistant)", i, msg.Role)
		}
		if len(msg.Parts) == 0 {
			return fmt.Errorf("ir request: message %d (role %q) has no parts", i, msg.Role)
		}
		for j, part := range msg.Parts {
			switch part.Type {
			case irPartText:
			case irPartToolUse:
				if part.ToolCall == nil || part.ToolCall.ID == "" || part.ToolCall.Name == "" {
					return fmt.Errorf("ir request: message %d part %d has invalid tool_use part", i, j)
				}
			case irPartToolResult:
				if part.ToolResult == nil || part.ToolResult.ToolUseID == "" {
					return fmt.Errorf("ir request: message %d part %d has invalid tool_result part", i, j)
				}
			case irPartImage:
				return fmt.Errorf("ir request: message %d part %d has unsupported type %q (images are not supported)", i, j, part.Type)
			default:
				return fmt.Errorf("ir request: message %d part %d has unsupported type %q", i, j, part.Type)
			}
		}
	}
	for i, tool := range req.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return fmt.Errorf("ir request: tool %d has invalid name", i)
		}
	}
	return nil
}
