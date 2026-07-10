package main

import (
	"fmt"
	"io"
)

const (
	streamEventStart     = "start"
	streamEventTextDelta = "text_delta"
	streamEventToolStart = "tool_start"
	streamEventToolDelta = "tool_delta"
	streamEventToolStop  = "tool_stop"
	streamEventUsage     = "usage"
	streamEventStop      = "stop"
	streamEventError     = "error"
)

type StreamEvent struct {
	Type       string
	ID         string
	Model      string
	Text       string
	ToolID     string
	ToolName   string
	StopReason string
	Usage      *IRUsage
	Err        string
}

type StreamDecoder interface {
	DecodeStream(io.Reader, func(StreamEvent) error) error
}

type StreamEncoder interface {
	EncodeStreamEvent(io.Writer, StreamEvent) error
}

func mergeStreamUsage(existing, next *IRUsage) *IRUsage {
	if existing == nil {
		return next
	}
	if next == nil {
		return existing
	}
	merged := *existing
	if next.InputTokens != 0 {
		merged.InputTokens = next.InputTokens
	}
	if next.OutputTokens != 0 {
		merged.OutputTokens = next.OutputTokens
	}
	if next.TotalTokens != 0 {
		merged.TotalTokens = next.TotalTokens
	} else if next.InputTokens != 0 || next.OutputTokens != 0 {
		merged.TotalTokens = merged.InputTokens + merged.OutputTokens
	}
	return &merged
}

func streamDecoderForProtocol(protocol ProviderProtocol) (StreamDecoder, error) {
	switch protocol {
	case protocolAnthropicMessages:
		return anthropicStreamDecoder{}, nil
	case protocolOpenAIChat:
		return openAIChatStreamDecoder{}, nil
	case protocolOpenAIResponses:
		return openAIResponsesStreamDecoder{}, nil
	default:
		return nil, fmt.Errorf("stream decoder for protocol %q is not supported", protocol)
	}
}

func streamEncoderForProtocol(protocol ProviderProtocol) (StreamEncoder, error) {
	switch protocol {
	case protocolAnthropicMessages:
		return &anthropicStreamEncoder{}, nil
	case protocolOpenAIChat:
		return &openAIChatStreamEncoder{}, nil
	case protocolOpenAIResponses:
		return &openAIResponsesStreamEncoder{}, nil
	default:
		return nil, fmt.Errorf("stream encoder for protocol %q is not supported", protocol)
	}
}
