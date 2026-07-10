package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type proxyLogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	RemoteAddr string    `json:"remoteAddr,omitempty"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model,omitempty"`
	StatusCode int       `json:"statusCode,omitempty"`
	Error      string    `json:"error,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
}

type proxyLogger struct {
	mu   sync.Mutex
	file *os.File
}

func newProxyLogger(path string) (*proxyLogger, error) {
	if path == "" {
		return &proxyLogger{}, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &proxyLogger{file: file}, nil
}

func (l *proxyLogger) logRequest(entry proxyLogEntry) {
	if l == nil {
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	_, _ = l.file.Write(append(line, '\n'))
}

func (l *proxyLogger) close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
