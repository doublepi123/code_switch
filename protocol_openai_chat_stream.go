package main

import (
	"encoding/json"
	"fmt"
	"io"
)

type openAIChatStreamDecoder struct{}

func (openAIChatStreamDecoder) DecodeStream(r io.Reader, emit func(StreamEvent) error) error {
	sr := newSSEReader(r, maxSSEEventBytes)
	toolIDsByIndex := map[int]string{}
	pendingArgsByIndex := map[int][]string{}
	pendingStopReason := ""
	flushStop := func() error {
		if pendingStopReason == "" {
			return nil
		}
		stopReason := pendingStopReason
		pendingStopReason = ""
		return emit(StreamEvent{Type: streamEventStop, StopReason: stopReason})
	}
	for {
		event, err := sr.ReadEvent()
		if err == io.EOF {
			if len(pendingArgsByIndex) > 0 {
				return fmt.Errorf("openai chat stream: tool arguments received without tool id")
			}
			return flushStop()
		}
		if err != nil {
			return err
		}
		if event.Data == "" {
			continue
		}
		if event.Data == "[DONE]" {
			if len(pendingArgsByIndex) > 0 {
				return fmt.Errorf("openai chat stream: tool arguments received without tool id")
			}
			if err := flushStop(); err != nil {
				return err
			}
			continue
		}
		var raw struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(event.Data), &raw); err != nil {
			return fmt.Errorf("openai chat stream: parse event: %w", err)
		}
		if raw.Usage != nil {
			if err := emit(StreamEvent{Type: streamEventUsage, Usage: &IRUsage{InputTokens: raw.Usage.PromptTokens, OutputTokens: raw.Usage.CompletionTokens, TotalTokens: raw.Usage.TotalTokens}}); err != nil {
				return err
			}
		}
		if len(raw.Choices) == 0 {
			continue
		}
		choice := raw.Choices[0]
		if pendingStopReason != "" && (choice.Delta.Role != "" || choice.Delta.Content != "" || len(choice.Delta.ToolCalls) > 0) {
			if err := flushStop(); err != nil {
				return err
			}
		}
		if choice.Delta.Role == "assistant" {
			if err := emit(StreamEvent{Type: streamEventStart, ID: raw.ID, Model: raw.Model}); err != nil {
				return err
			}
		}
		if choice.Delta.Content != "" {
			if err := emit(StreamEvent{Type: streamEventTextDelta, Text: choice.Delta.Content}); err != nil {
				return err
			}
		}
		for _, tc := range choice.Delta.ToolCalls {
			if tc.ID != "" {
				toolIDsByIndex[tc.Index] = tc.ID
				if err := emit(StreamEvent{Type: streamEventToolStart, ToolID: tc.ID, ToolName: tc.Function.Name}); err != nil {
					return err
				}
				for _, arg := range pendingArgsByIndex[tc.Index] {
					if err := emit(StreamEvent{Type: streamEventToolDelta, ToolID: tc.ID, Text: arg}); err != nil {
						return err
					}
				}
				delete(pendingArgsByIndex, tc.Index)
			}
			if tc.Function.Arguments != "" {
				id := toolIDsByIndex[tc.Index]
				if id == "" {
					pendingArgsByIndex[tc.Index] = append(pendingArgsByIndex[tc.Index], tc.Function.Arguments)
				} else {
					if err := emit(StreamEvent{Type: streamEventToolDelta, ToolID: id, Text: tc.Function.Arguments}); err != nil {
						return err
					}
				}
			}
		}
		if choice.FinishReason != "" {
			pendingStopReason = openAIChatStopReasonToIR(choice.FinishReason)
		}
	}
}

type openAIChatStreamEncoder struct {
	started   bool
	stopped   bool
	id        string
	model     string
	usage     *IRUsage
	toolIndex int
}

func (e *openAIChatStreamEncoder) EncodeStreamEvent(w io.Writer, event StreamEvent) error {
	switch event.Type {
	case streamEventStart:
		e.started = true
		e.id = event.ID
		e.model = event.Model
		if event.Usage != nil {
			e.usage = event.Usage
		}
		return writeRawSSE(w, "", mustMarshalJSON(e.chunk(map[string]string{"role": "assistant"}, "", nil)))
	case streamEventTextDelta:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		return writeRawSSE(w, "", mustMarshalJSON(e.chunk(map[string]string{"content": event.Text}, "", nil)))
	case streamEventToolStart:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		e.toolIndex = e.nextToolIndex()
		return writeRawSSE(w, "", mustMarshalJSON(e.toolChunk(event, true)))
	case streamEventToolDelta:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		return writeRawSSE(w, "", mustMarshalJSON(e.toolChunk(event, false)))
	case streamEventStop:
		if e.stopped {
			return nil
		}
		e.stopped = true
		finishReason := "stop"
		if event.StopReason == "tool_use" {
			finishReason = "tool_calls"
		} else if event.StopReason == responsesStopReasonMaxTokens {
			finishReason = "length"
		}
		if err := writeRawSSE(w, "", mustMarshalJSON(e.chunk(map[string]string{}, finishReason, e.usage))); err != nil {
			return err
		}
		return writeRawSSE(w, "", []byte("[DONE]"))
	case streamEventUsage:
		if event.Usage != nil {
			e.usage = mergeStreamUsage(e.usage, event.Usage)
		}
		return nil
	}
	return nil
}

func (e *openAIChatStreamEncoder) nextToolIndex() int {
	idx := e.toolIndex
	e.toolIndex++
	return idx
}

func (e *openAIChatStreamEncoder) toolChunk(event StreamEvent, start bool) map[string]any {
	id := e.id
	if id == "" {
		id = "chatcmpl_code_switch"
	}
	args := event.Text
	if args == "" {
		args = "{}"
	}
	function := map[string]any{"arguments": args}
	if start {
		function["name"] = event.ToolName
	}
	toolCall := map[string]any{"index": e.toolIndex, "function": function}
	if start {
		toolCall["id"] = event.ToolID
		toolCall["type"] = "function"
	}
	choice := map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{toolCall}}, "finish_reason": nil}
	return map[string]any{"id": id, "object": "chat.completion.chunk", "model": e.model, "choices": []any{choice}}
}

func (e *openAIChatStreamEncoder) chunk(delta map[string]string, finishReason string, usage *IRUsage) map[string]any {
	id := e.id
	if id == "" {
		id = "chatcmpl_code_switch"
	}
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	out := map[string]any{"id": id, "object": "chat.completion.chunk", "model": e.model, "choices": []any{choice}}
	if usage != nil {
		out["usage"] = map[string]int{"prompt_tokens": usage.InputTokens, "completion_tokens": usage.OutputTokens, "total_tokens": usage.TotalTokens}
	}
	return out
}
