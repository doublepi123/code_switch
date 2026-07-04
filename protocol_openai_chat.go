package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model    string              `json:"model"`
	Messages []openAIChatMessage `json:"messages"`
	Stream   bool                `json:"stream,omitempty"`
}

func irToOpenAIChatRequest(req IRRequest) ([]byte, error) {
	if err := req.ValidateTextOnly(); err != nil {
		return nil, fmt.Errorf("openai chat request: %w", err)
	}
	messages := make([]openAIChatMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		var b strings.Builder
		for _, part := range msg.Parts {
			b.WriteString(part.Text)
		}
		messages = append(messages, openAIChatMessage{Role: msg.Role, Content: b.String()})
	}
	out := openAIChatRequest{Model: req.Model, Messages: messages}
	return json.Marshal(out)
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
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
	resp := IRResponse{ID: raw.ID, Model: raw.Model, Text: choice.Message.Content, StopReason: openAIChatStopReasonToIR(choice.FinishReason)}
	if raw.Usage != nil {
		resp.Usage = &IRUsage{InputTokens: raw.Usage.PromptTokens, OutputTokens: raw.Usage.CompletionTokens, TotalTokens: raw.Usage.TotalTokens}
		if resp.Usage.TotalTokens == 0 {
			resp.Usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		}
	}
	return resp, nil
}

func openAIChatStopReasonToIR(reason string) string {
	switch reason {
	case "length":
		return responsesStopReasonMaxTokens
	default:
		return "end_turn"
	}
}
