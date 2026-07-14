package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// proxyLogKind distinguishes final client-facing request outcomes from
// individual upstream attempts (including fallback retries). Stats only
// count request-kind entries for request totals, while still counting
// upstream attempts separately.
const (
	proxyLogKindRequest  = "request"
	proxyLogKindUpstream = "upstream"
)

type proxyLogEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Kind         string    `json:"kind,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	RemoteAddr   string    `json:"remoteAddr,omitempty"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model,omitempty"`
	StatusCode   int       `json:"statusCode,omitempty"`
	Error        string    `json:"error,omitempty"`
	DurationMs   int64     `json:"durationMs,omitempty"`
	InputTokens  int       `json:"inputTokens,omitempty"`
	OutputTokens int       `json:"outputTokens,omitempty"`
	TotalTokens  int       `json:"totalTokens,omitempty"`
}

type proxyLogger struct {
	mu   sync.Mutex
	file *os.File
}

func newProxyLogger(path string) (*proxyLogger, error) {
	if path == "" {
		return &proxyLogger{}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
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
	if entry.Kind == "" {
		entry.Kind = proxyLogKindRequest
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

// defaultProxyLogPath returns ~/.code-switch/proxy.jsonl so request stats
// work out of the box without requiring operators to pass --log every time.
func defaultProxyLogPath() (string, error) {
	cfgPath, err := appConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cfgPath), "proxy.jsonl"), nil
}
