package main

import (
	"encoding/json"
	"fmt"
	"io"
)

type openAIResponsesStreamDecoder struct{}

func (openAIResponsesStreamDecoder) DecodeStream(r io.Reader, emit func(StreamEvent) error) error {
	sr := newSSEReader(r, maxSSEEventBytes)
	toolIDsByIndex := map[int]string{}
	hasToolCall := false
	for {
		event, err := sr.ReadEvent()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if event.Data == "" || event.Data == "[DONE]" {
			continue
		}
		var raw struct {
			Type        string `json:"type"`
			OutputIndex int    `json:"output_index"`
			Delta       string `json:"delta"`
			Item        struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			} `json:"item"`
			Response struct {
				ID     string `json:"id"`
				Model  string `json:"model"`
				Status string `json:"status"`
				Output []struct {
					Type string `json:"type"`
				} `json:"output"`
				Usage responsesUsage `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(event.Data), &raw); err != nil {
			return fmt.Errorf("responses stream: parse event: %w", err)
		}
		switch raw.Type {
		case "response.created":
			if err := emit(StreamEvent{Type: streamEventStart, ID: raw.Response.ID, Model: raw.Response.Model}); err != nil {
				return err
			}
		case "response.output_text.delta":
			if raw.Delta != "" {
				if err := emit(StreamEvent{Type: streamEventTextDelta, Text: raw.Delta}); err != nil {
					return err
				}
			}
		case "response.output_item.added":
			if raw.Item.Type == "function_call" {
				hasToolCall = true
				toolIDsByIndex[raw.OutputIndex] = raw.Item.CallID
				if err := emit(StreamEvent{Type: streamEventToolStart, ToolID: raw.Item.CallID, ToolName: raw.Item.Name}); err != nil {
					return err
				}
			}
		case "response.function_call_arguments.delta":
			if raw.Delta != "" {
				if err := emit(StreamEvent{Type: streamEventToolDelta, ToolID: toolIDsByIndex[raw.OutputIndex], Text: raw.Delta}); err != nil {
					return err
				}
			}
		case "response.output_item.done":
			if raw.Item.Type == "function_call" {
				hasToolCall = true
				toolID := raw.Item.CallID
				if toolID == "" {
					toolID = toolIDsByIndex[raw.OutputIndex]
				}
				if err := emit(StreamEvent{Type: streamEventToolStop, ToolID: toolID}); err != nil {
					return err
				}
			}
		case "response.completed":
			for _, item := range raw.Response.Output {
				if item.Type == "function_call" {
					hasToolCall = true
				}
			}
			if raw.Response.Usage.InputTokens != 0 || raw.Response.Usage.OutputTokens != 0 || raw.Response.Usage.TotalTokens != 0 {
				if err := emit(StreamEvent{Type: streamEventUsage, Usage: &IRUsage{InputTokens: raw.Response.Usage.InputTokens, OutputTokens: raw.Response.Usage.OutputTokens, TotalTokens: raw.Response.Usage.TotalTokens}}); err != nil {
					return err
				}
			}
			stopReason := responsesStatusToIRStopReason(raw.Response.Status)
			if hasToolCall {
				stopReason = "tool_use"
			}
			if err := emit(StreamEvent{Type: streamEventStop, StopReason: stopReason}); err != nil {
				return err
			}
		case "error":
			if err := emit(StreamEvent{Type: streamEventError, Err: event.Data}); err != nil {
				return err
			}
		}
	}
}

type openAIResponsesStreamEncoder struct {
	started         bool
	stopped         bool
	textItemStarted bool
	textItemDone    bool
	id              string
	messageID       string
	model           string
	text            string
	toolID          string
	toolName        string
	toolArgs        string
	usage           *IRUsage
}

func (e *openAIResponsesStreamEncoder) EncodeStreamEvent(w io.Writer, event StreamEvent) error {
	switch event.Type {
	case streamEventStart:
		e.started = true
		e.id = responsesResponseID(event.ID)
		e.messageID = responsesMessageID(event.ID)
		e.model = event.Model
		if event.Usage != nil {
			e.usage = event.Usage
		}
		return writeRawSSE(w, "response.created", mustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": e.id, "object": "response", "status": "in_progress", "model": e.model, "output": []any{}}}))
	case streamEventTextDelta:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		if !e.textItemStarted {
			e.textItemStarted = true
			if e.messageID == "" {
				e.messageID = responsesMessageID(e.id)
			}
			if err := writeRawSSE(w, "response.output_item.added", mustMarshalJSON(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "role": "assistant", "status": "in_progress", "id": e.messageID, "content": []any{}}})); err != nil {
				return err
			}
			if err := writeRawSSE(w, "response.content_part.added", mustMarshalJSON(map[string]any{"type": "response.content_part.added", "output_index": 0, "content_index": 0, "part": map[string]any{"type": responsesOutputTextType, "text": ""}})); err != nil {
				return err
			}
		}
		e.text += event.Text
		return writeRawSSE(w, "response.output_text.delta", mustMarshalJSON(map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": event.Text}))
	case streamEventToolStart:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		e.toolID = event.ToolID
		e.toolName = event.ToolName
		e.toolArgs = ""
		return writeRawSSE(w, "response.output_item.added", mustMarshalJSON(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "call_id": event.ToolID, "name": event.ToolName, "arguments": ""}}))
	case streamEventToolDelta:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		e.toolArgs += event.Text
		return writeRawSSE(w, "response.function_call_arguments.delta", mustMarshalJSON(map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": event.Text}))
	case streamEventToolStop:
		toolID := event.ToolID
		if toolID == "" {
			toolID = e.toolID
		}
		return writeRawSSE(w, "response.output_item.done", mustMarshalJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "function_call", "status": "completed", "call_id": toolID, "name": e.toolName, "arguments": e.toolArgs}}))
	case streamEventUsage:
		if event.Usage != nil {
			e.usage = mergeStreamUsage(e.usage, event.Usage)
		}
	case streamEventStop:
		if e.stopped {
			return nil
		}
		e.stopped = true
		if e.textItemStarted && !e.textItemDone {
			e.textItemDone = true
			if err := writeRawSSE(w, "response.output_text.done", mustMarshalJSON(map[string]any{"type": "response.output_text.done", "output_index": 0, "content_index": 0, "text": e.text})); err != nil {
				return err
			}
			if err := writeRawSSE(w, "response.output_item.done", mustMarshalJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message", "role": "assistant", "status": "completed", "id": e.messageID, "content": []map[string]string{{"type": responsesOutputTextType, "text": e.text}}}})); err != nil {
				return err
			}
		}
		status := responsesStatusFor(event.StopReason)
		output := make([]any, 0, 2)
		if e.textItemStarted || e.toolID == "" {
			output = append(output, map[string]any{"type": "message", "role": "assistant", "status": "completed", "id": e.messageID, "content": []map[string]string{{"type": responsesOutputTextType, "text": e.text}}})
		}
		if e.toolID != "" {
			output = append(output, map[string]any{"type": "function_call", "status": "completed", "call_id": e.toolID, "name": e.toolName, "arguments": e.toolArgs})
		}
		resp := map[string]any{"id": e.id, "object": "response", "status": status, "model": e.model, "output": output, "output_text": e.text}
		if e.messageID == "" {
			e.messageID = responsesMessageID(e.id)
		}
		if e.usage != nil {
			resp["usage"] = map[string]int{"input_tokens": e.usage.InputTokens, "output_tokens": e.usage.OutputTokens, "total_tokens": e.usage.TotalTokens}
		}
		return writeRawSSE(w, "response.completed", mustMarshalJSON(map[string]any{"type": "response.completed", "response": resp}))
	case streamEventError:
		return writeRawSSE(w, "error", mustMarshalJSON(map[string]string{"type": "error", "error": event.Err}))
	}
	return nil
}
