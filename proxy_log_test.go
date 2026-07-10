package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyLoggerLogRequest_writesExpectedJSONFields(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.close() })
	entry := proxyLogEntry{
		Timestamp:  time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		Method:     "POST",
		Path:       "/v1/responses",
		Provider:   "zhipu-cn",
		Model:      "glm-5.2",
		StatusCode: 200,
		DurationMs: 15,
	}

	// When
	logger.logRequest(entry)
	if err := logger.close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	// Then
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var got proxyLogEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal log entry: %v\n%s", err, string(data))
	}
	if !got.Timestamp.Equal(entry.Timestamp) || got.Method != entry.Method || got.Path != entry.Path || got.Provider != entry.Provider || got.Model != entry.Model || got.StatusCode != entry.StatusCode || got.DurationMs != entry.DurationMs {
		t.Fatalf("log entry = %#v, want %#v", got, entry)
	}
}

func TestProxyLoggerNoop_doesNotPanicOrCreateFile_whenPathEmpty(t *testing.T) {
	// Given
	logger, err := newProxyLogger("")
	if err != nil {
		t.Fatalf("newProxyLogger empty: %v", err)
	}

	// When
	logger.logRequest(proxyLogEntry{Method: "POST", Path: "/v1/responses", Provider: "zhipu-cn"})
	err = logger.close()

	// Then
	if err != nil {
		t.Fatalf("close noop logger: %v", err)
	}
}

func TestProxyLoggerConcurrentLogAndClose_hasNoDataRace(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			logger.logRequest(proxyLogEntry{Method: http.MethodPost, Path: "/v1/responses", Provider: "zhipu-cn"})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = logger.close()
	}()

	// When
	close(start)
	wg.Wait()

	// Then
	if err := logger.close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestProxyHandlerLogger_recordsErrorOutcome_whenUpstreamNetworkFails(t *testing.T) {
	// Given
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	handler := newProxyHandlerWithRegistryAndLogger(ProxyRoute{Provider: "primary", Model: "primary-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: deadURL, LocalToken: "local-token"}, "provider-key", defaultProtocolRegistry(), logger)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)
	if err := logger.close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	// Then
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502\nbody: %s", rec.Code, rec.Body.String())
	}
	entries := readProxyLogEntries(t, logPath)
	if len(entries) != 2 {
		t.Fatalf("log entry count = %d, want 2: %#v", len(entries), entries)
	}
	for i, got := range entries {
		if got.StatusCode != http.StatusBadGateway {
			t.Fatalf("entry %d status = %d, want 502", i, got.StatusCode)
		}
		if got.Error == "" || !strings.Contains(got.Error, "upstream request") {
			t.Fatalf("entry %d error = %q, want upstream request message", i, got.Error)
		}
		if got.DurationMs < 0 {
			t.Fatalf("entry %d duration = %d, want non-negative", i, got.DurationMs)
		}
	}
}

func TestProxyMultiRouteLogger_recordsUnauthorizedInboundRequest(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	handler := newProxyMultiRouteHandlerWithLogger([]proxyServedRoute{{
		Agent:          "codex",
		ClientProtocol: protocolOpenAIResponses,
		Route:          ProxyRoute{Provider: "primary", Model: "primary-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: "http://127.0.0.1:1", LocalToken: "route-token"},
		ProviderKey:    "provider-key",
	}}, defaultProtocolRegistry(), logger)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)
	if err := logger.close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	// Then
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401\nbody: %s", rec.Code, rec.Body.String())
	}
	entries := readProxyLogEntries(t, logPath)
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1: %#v", len(entries), entries)
	}
	got := entries[0]
	if got.Method != http.MethodPost || got.Path != "/v1/responses" || got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("entry = %#v, want POST /v1/responses 401", got)
	}
	if got.RemoteAddr == "" {
		t.Fatalf("remoteAddr is empty in entry %#v", got)
	}
	if got.Error == "" || !strings.Contains(got.Error, "unauthorized") {
		t.Fatalf("error = %q, want unauthorized message", got.Error)
	}
}

func TestProxyMultiRouteLogger_recordsUnsupportedPathInboundRequest(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	handler := newProxyMultiRouteHandlerWithLogger([]proxyServedRoute{{
		Agent:          "codex",
		ClientProtocol: protocolOpenAIResponses,
		Route:          ProxyRoute{Provider: "primary", Model: "primary-model", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: "http://127.0.0.1:1", LocalToken: "route-token"},
		ProviderKey:    "provider-key",
	}}, defaultProtocolRegistry(), logger)
	req := httptest.NewRequest(http.MethodPost, "/v1/unknown", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer route-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)
	if err := logger.close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	// Then
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404\nbody: %s", rec.Code, rec.Body.String())
	}
	entries := readProxyLogEntries(t, logPath)
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1: %#v", len(entries), entries)
	}
	got := entries[0]
	if got.Method != http.MethodPost || got.Path != "/v1/unknown" || got.StatusCode != http.StatusNotFound {
		t.Fatalf("entry = %#v, want POST /v1/unknown 404", got)
	}
	if got.RemoteAddr == "" {
		t.Fatalf("remoteAddr is empty in entry %#v", got)
	}
	if got.Error == "" || !strings.Contains(got.Error, "not supported") {
		t.Fatalf("error = %q, want not supported message", got.Error)
	}
}

func TestProxyLogger_recordsPrimaryAndFallbackUpstreamAttempts(t *testing.T) {
	// Given
	primary := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"primary failed"}`))
	}))
	t.Cleanup(primary.Close)
	fallback, _ := startAnthropicUpstream(t, 0, `{"id":"msg_fb","type":"message","role":"assistant","model":"fallback-model","content":[{"type":"text","text":"Fallback hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	handler := newProxyHandlerWithRegistryAndLogger(ProxyRoute{
		Provider:         "primary",
		Model:            "primary-model",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  primary.URL,
		LocalToken:       "local-token",
		Fallback: &ProxyRoute{
			Provider:         "fallback",
			Model:            "fallback-model",
			UpstreamProtocol: protocolAnthropicMessages,
			UpstreamBaseURL:  fallback.URL,
			ProviderKey:      "fallback-provider-key",
		},
	}, "provider-key", defaultProtocolRegistry(), logger)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer local-token")
	rec := httptest.NewRecorder()

	// When
	handler.ServeHTTP(rec, req)
	if err := logger.close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	entries := readProxyLogEntries(t, logPath)
	if len(entries) != 3 {
		t.Fatalf("log entry count = %d, want 3: %#v", len(entries), entries)
	}
	want := []struct {
		provider string
		model    string
		status   int
	}{
		{provider: "primary", model: "primary-model", status: http.StatusInternalServerError},
		{provider: "fallback", model: "fallback-model", status: http.StatusOK},
		{provider: "primary", model: "primary-model", status: http.StatusOK},
	}
	for i, wantEntry := range want {
		got := entries[i]
		if got.Provider != wantEntry.provider || got.Model != wantEntry.model || got.StatusCode != wantEntry.status {
			t.Fatalf("entry %d = %#v, want provider=%s model=%s status=%d", i, got, wantEntry.provider, wantEntry.model, wantEntry.status)
		}
		if got.DurationMs < 0 {
			t.Fatalf("entry %d duration = %d, want non-negative", i, got.DurationMs)
		}
	}
}

func readProxyLogEntries(t *testing.T, path string) []proxyLogEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	entries := make([]proxyLogEntry, 0, len(lines))
	for _, line := range lines {
		var entry proxyLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("unmarshal log line: %v\n%s", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}
