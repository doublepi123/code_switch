package main

import (
	"encoding/json"
	"fmt"
	"io"
)

type anthropicStreamDecoder struct{}

func (anthropicStreamDecoder) DecodeStream(r io.Reader, emit func(StreamEvent) error) error {
	sr := newSSEReader(r, maxSSEEventBytes)
	toolIDsByIndex := map[int]string{}
	for {
		event, err := sr.ReadEvent()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if event.Data == "" {
			continue
		}
		var raw struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string          `json:"id"`
				Model string          `json:"model"`
				Usage *anthropicUsage `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Usage *anthropicUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(event.Data), &raw); err != nil {
			return fmt.Errorf("anthropic stream: parse event: %w", err)
		}
		switch raw.Type {
		case "message_start":
			se := StreamEvent{Type: streamEventStart, ID: raw.Message.ID, Model: raw.Message.Model}
			if raw.Message.Usage != nil {
				se.Usage = &IRUsage{InputTokens: raw.Message.Usage.InputTokens, OutputTokens: raw.Message.Usage.OutputTokens}
			}
			if err := emit(se); err != nil {
				return err
			}
		case "content_block_start":
			if raw.ContentBlock.Type == irPartToolUse {
				toolIDsByIndex[raw.Index] = raw.ContentBlock.ID
				if err := emit(StreamEvent{Type: streamEventToolStart, ToolID: raw.ContentBlock.ID, ToolName: raw.ContentBlock.Name}); err != nil {
					return err
				}
			}
		case "content_block_delta":
			if raw.Delta.Text != "" {
				if err := emit(StreamEvent{Type: streamEventTextDelta, Text: raw.Delta.Text}); err != nil {
					return err
				}
			}
			if raw.Delta.Type == "input_json_delta" && raw.Delta.PartialJSON != "" {
				if err := emit(StreamEvent{Type: streamEventToolDelta, ToolID: toolIDsByIndex[raw.Index], Text: raw.Delta.PartialJSON}); err != nil {
					return err
				}
			}
		case "content_block_stop":
			if toolID := toolIDsByIndex[raw.Index]; toolID != "" {
				if err := emit(StreamEvent{Type: streamEventToolStop, ToolID: toolID}); err != nil {
					return err
				}
			}
		case "message_delta":
			if raw.Usage != nil {
				if err := emit(StreamEvent{Type: streamEventUsage, Usage: &IRUsage{InputTokens: raw.Usage.InputTokens, OutputTokens: raw.Usage.OutputTokens}}); err != nil {
					return err
				}
			}
			if raw.Delta.StopReason != "" {
				stopReason := raw.Delta.StopReason
				if stopReason == "end_turn" {
					stopReason = "stop"
				}
				if err := emit(StreamEvent{Type: streamEventStop, StopReason: stopReason}); err != nil {
					return err
				}
			}
		case "error":
			if err := emit(StreamEvent{Type: streamEventError, Err: event.Data}); err != nil {
				return err
			}
		}
	}
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicStreamEncoder struct {
	started      bool
	stopped      bool
	blockOpen    bool
	currentIndex int
	nextIndex    int
	usage        *IRUsage
}

func (e *anthropicStreamEncoder) EncodeStreamEvent(w io.Writer, event StreamEvent) error {
	switch event.Type {
	case streamEventStart:
		e.started = true
		id := event.ID
		if id == "" {
			id = "msg_code_switch"
		}
		usage := anthropicUsage{}
		if event.Usage != nil {
			usage.InputTokens = event.Usage.InputTokens
			usage.OutputTokens = event.Usage.OutputTokens
		}
		if err := writeRawSSE(w, "message_start", mustMarshalJSON(map[string]any{
			"type":    "message_start",
			"message": map[string]any{"id": id, "type": "message", "role": "assistant", "model": event.Model, "content": []any{}, "stop_reason": nil, "usage": usage},
		})); err != nil {
			return err
		}
		return nil
	case streamEventTextDelta:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		if !e.blockOpen {
			e.currentIndex = e.nextIndex
			e.nextIndex++
			e.blockOpen = true
			if err := writeRawSSE(w, "content_block_start", mustMarshalJSON(map[string]any{"type": "content_block_start", "index": e.currentIndex, "content_block": map[string]string{"type": irPartText, "text": ""}})); err != nil {
				return err
			}
		}
		return writeRawSSE(w, "content_block_delta", mustMarshalJSON(map[string]any{"type": "content_block_delta", "index": e.currentIndex, "delta": map[string]string{"type": "text_delta", "text": event.Text}}))
	case streamEventToolStart:
		if !e.started {
			if err := e.EncodeStreamEvent(w, StreamEvent{Type: streamEventStart}); err != nil {
				return err
			}
		}
		if e.blockOpen {
			if err := writeRawSSE(w, "content_block_stop", mustMarshalJSON(map[string]any{"type": "content_block_stop", "index": e.currentIndex})); err != nil {
				return err
			}
		}
		e.currentIndex = e.nextIndex
		e.nextIndex++
		e.blockOpen = true
		return writeRawSSE(w, "content_block_start", mustMarshalJSON(map[string]any{"type": "content_block_start", "index": e.currentIndex, "content_block": map[string]any{"type": irPartToolUse, "id": event.ToolID, "name": event.ToolName, "input": map[string]any{}}}))
	case streamEventToolDelta:
		return writeRawSSE(w, "content_block_delta", mustMarshalJSON(map[string]any{"type": "content_block_delta", "index": e.currentIndex, "delta": map[string]string{"type": "input_json_delta", "partial_json": event.Text}}))
	case streamEventToolStop:
		if !e.blockOpen {
			return nil
		}
		e.blockOpen = false
		return writeRawSSE(w, "content_block_stop", mustMarshalJSON(map[string]any{"type": "content_block_stop", "index": e.currentIndex}))
	case streamEventStop:
		if e.stopped {
			return nil
		}
		e.stopped = true
		if e.blockOpen {
			e.blockOpen = false
			if err := writeRawSSE(w, "content_block_stop", mustMarshalJSON(map[string]any{"type": "content_block_stop", "index": e.currentIndex})); err != nil {
				return err
			}
		}
		stopReason := event.StopReason
		if stopReason == "" || stopReason == "stop" {
			stopReason = "end_turn"
		}
		messageDelta := map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": stopReason}}
		if e.usage != nil {
			messageDelta["usage"] = anthropicUsage{InputTokens: e.usage.InputTokens, OutputTokens: e.usage.OutputTokens}
		}
		if err := writeRawSSE(w, "message_delta", mustMarshalJSON(messageDelta)); err != nil {
			return err
		}
		return writeRawSSE(w, "message_stop", mustMarshalJSON(map[string]string{"type": "message_stop"}))
	case streamEventUsage:
		e.usage = event.Usage
		return nil
	case streamEventError:
		return writeRawSSE(w, "error", mustMarshalJSON(map[string]string{"type": "error", "error": event.Err}))
	}
	return nil
}

func writeRawSSE(w io.Writer, event string, data []byte) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	if f, ok := w.(httpFlusher); ok {
		f.Flush()
	}
	return nil
}

type httpFlusher interface{ Flush() }
