package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxSSEEventBytes = 1 << 20

type SSEEvent struct {
	Event string
	Data  string
	ID    string
	Retry int
}

type sseReader struct {
	r       *bufio.Reader
	maxSize int
}

func newSSEReader(r io.Reader, maxSize int) *sseReader {
	if maxSize <= 0 {
		maxSize = maxSSEEventBytes
	}
	return &sseReader{r: bufio.NewReader(r), maxSize: maxSize}
}

func (r *sseReader) ReadEvent() (SSEEvent, error) {
	var event SSEEvent
	var data []string
	size := 0
	for {
		line, err := r.r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return SSEEvent{}, err
		}
		if line == "" && errors.Is(err, io.EOF) {
			if event.Event == "" && event.ID == "" && len(data) == 0 {
				return SSEEvent{}, io.EOF
			}
			break
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		size += len(line) + 1
		if size > r.maxSize {
			return SSEEvent{}, fmt.Errorf("sse event exceeds limit of %d bytes", r.maxSize)
		}
		if line == "" {
			if event.Event == "" && event.ID == "" && len(data) == 0 {
				if errors.Is(err, io.EOF) {
					return SSEEvent{}, io.EOF
				}
				continue
			}
			break
		}
		if strings.HasPrefix(line, ":") {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		if strings.HasPrefix(value, " ") {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			event.Event = value
		case "data":
			data = append(data, value)
		case "id":
			event.ID = value
		case "retry":
			if n, parseErr := strconv.Atoi(value); parseErr == nil {
				event.Retry = n
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	event.Data = strings.Join(data, "\n")
	return event, nil
}

type sseWriter struct {
	w http.ResponseWriter
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return &sseWriter{w: w}
}

func (w *sseWriter) WriteEvent(event SSEEvent) error {
	if event.Event != "" {
		if _, err := fmt.Fprintf(w.w, "event: %s\n", event.Event); err != nil {
			return err
		}
	}
	if event.ID != "" {
		if _, err := fmt.Fprintf(w.w, "id: %s\n", event.ID); err != nil {
			return err
		}
	}
	if event.Retry > 0 {
		if _, err := fmt.Fprintf(w.w, "retry: %d\n", event.Retry); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(event.Data, "\n") {
		if _, err := fmt.Fprintf(w.w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w.w, "\n"); err != nil {
		return err
	}
	if f, ok := w.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
