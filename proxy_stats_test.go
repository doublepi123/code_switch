package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAggregateProxyStats_countsRequestEntriesAndTokens(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	entries := []proxyLogEntry{
		{
			Timestamp:    time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC),
			Kind:         proxyLogKindUpstream,
			Agent:        "codex",
			Method:       "POST",
			Path:         "/v1/responses",
			Provider:     "primary",
			Model:        "primary-model",
			StatusCode:   500,
			DurationMs:   10,
			Error:        "upstream failed",
			InputTokens:  0,
			OutputTokens: 0,
		},
		{
			Timestamp:    time.Date(2026, 7, 15, 1, 0, 0, 100, time.UTC),
			Kind:         proxyLogKindUpstream,
			Agent:        "codex",
			Method:       "POST",
			Path:         "/v1/responses",
			Provider:     "fallback",
			Model:        "fallback-model",
			StatusCode:   200,
			DurationMs:   40,
			InputTokens:  12,
			OutputTokens: 8,
			TotalTokens:  20,
		},
		{
			Timestamp:    time.Date(2026, 7, 15, 1, 0, 0, 200, time.UTC),
			Kind:         proxyLogKindRequest,
			Agent:        "codex",
			Method:       "POST",
			Path:         "/v1/responses",
			Provider:     "primary",
			Model:        "primary-model",
			StatusCode:   200,
			DurationMs:   55,
			InputTokens:  12,
			OutputTokens: 8,
			TotalTokens:  20,
		},
		{
			Timestamp:  time.Date(2026, 7, 15, 1, 5, 0, 0, time.UTC),
			Kind:       proxyLogKindRequest,
			Agent:      "claude",
			Method:     "POST",
			Path:       "/v1/messages",
			Provider:   "deepseek",
			Model:      "deepseek-v4-pro",
			StatusCode: 401,
			DurationMs: 5,
			Error:      "unauthorized",
		},
	}
	writeProxyLogFixture(t, logPath, entries)

	// When
	stats, err := aggregateProxyStats(logPath, proxyStatsFilter{})
	if err != nil {
		t.Fatalf("aggregateProxyStats: %v", err)
	}

	// Then
	if stats.Requests != 2 {
		t.Fatalf("Requests = %d, want 2", stats.Requests)
	}
	if stats.Successes != 1 {
		t.Fatalf("Successes = %d, want 1", stats.Successes)
	}
	if stats.Errors != 1 {
		t.Fatalf("Errors = %d, want 1", stats.Errors)
	}
	if stats.UpstreamAttempts != 2 {
		t.Fatalf("UpstreamAttempts = %d, want 2", stats.UpstreamAttempts)
	}
	if stats.InputTokens != 12 || stats.OutputTokens != 8 || stats.TotalTokens != 20 {
		t.Fatalf("tokens = in %d out %d total %d, want 12/8/20", stats.InputTokens, stats.OutputTokens, stats.TotalTokens)
	}
	if stats.AvgDurationMs != 30 { // (55+5)/2
		t.Fatalf("AvgDurationMs = %d, want 30", stats.AvgDurationMs)
	}
	if got := stats.ByProvider["primary"].Requests; got != 1 {
		t.Fatalf("ByProvider[primary].Requests = %d, want 1", got)
	}
	if got := stats.ByProvider["deepseek"].Errors; got != 1 {
		t.Fatalf("ByProvider[deepseek].Errors = %d, want 1", got)
	}
	if got := stats.ByAgent["codex"].Requests; got != 1 {
		t.Fatalf("ByAgent[codex].Requests = %d, want 1", got)
	}
	if got := stats.ByAgent["claude"].Errors; got != 1 {
		t.Fatalf("ByAgent[claude].Errors = %d, want 1", got)
	}
	if got := stats.ByModel["primary-model"].InputTokens; got != 12 {
		t.Fatalf("ByModel[primary-model].InputTokens = %d, want 12", got)
	}
}

func TestAggregateProxyStats_filtersBySinceAndAgent(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	entries := []proxyLogEntry{
		{
			Timestamp:  time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
			Kind:       proxyLogKindRequest,
			Agent:      "codex",
			Provider:   "old",
			Model:      "m-old",
			StatusCode: 200,
			DurationMs: 10,
		},
		{
			Timestamp:  time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
			Kind:       proxyLogKindRequest,
			Agent:      "codex",
			Provider:   "new",
			Model:      "m-new",
			StatusCode: 200,
			DurationMs: 20,
		},
		{
			Timestamp:  time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC),
			Kind:       proxyLogKindRequest,
			Agent:      "claude",
			Provider:   "new",
			Model:      "m-claude",
			StatusCode: 200,
			DurationMs: 30,
		},
	}
	writeProxyLogFixture(t, logPath, entries)

	// When
	stats, err := aggregateProxyStats(logPath, proxyStatsFilter{
		Since: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC),
		Agent: "codex",
	})
	if err != nil {
		t.Fatalf("aggregateProxyStats: %v", err)
	}

	// Then
	if stats.Requests != 1 {
		t.Fatalf("Requests = %d, want 1", stats.Requests)
	}
	if _, ok := stats.ByProvider["new"]; !ok {
		t.Fatalf("expected provider new, got %#v", stats.ByProvider)
	}
	if _, ok := stats.ByProvider["old"]; ok {
		t.Fatalf("old provider should be filtered out")
	}
	if _, ok := stats.ByAgent["claude"]; ok {
		t.Fatalf("claude agent should be filtered out")
	}
}

func TestAggregateProxyStats_missingLogIsEmptyNotError(t *testing.T) {
	// Given
	missing := filepath.Join(t.TempDir(), "missing.jsonl")

	// When
	stats, err := aggregateProxyStats(missing, proxyStatsFilter{})

	// Then
	if err != nil {
		t.Fatalf("aggregateProxyStats missing: %v", err)
	}
	if stats.Requests != 0 || stats.LogPath != missing {
		t.Fatalf("stats = %#v, want empty with log path", stats)
	}
}

func TestCmdProxyStats_printsSummary(t *testing.T) {
	// Given
	t.Setenv("HOME", t.TempDir())
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	writeProxyLogFixture(t, logPath, []proxyLogEntry{{
		Timestamp:    time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC),
		Kind:         proxyLogKindRequest,
		Agent:        "codex",
		Provider:     "zhipu-cn",
		Model:        "glm-5.2",
		StatusCode:   200,
		DurationMs:   42,
		InputTokens:  3,
		OutputTokens: 5,
		TotalTokens:  8,
	}})
	var out bytes.Buffer

	// When
	err := cmdProxyStats([]string{"--log", logPath}, &out)

	// Then
	if err != nil {
		t.Fatalf("cmdProxyStats: %v", err)
	}
	s := out.String()
	for _, want := range []string{
		"requests: 1",
		"successes: 1",
		"errors: 0",
		"input_tokens: 3",
		"output_tokens: 5",
		"total_tokens: 8",
		"avg_duration_ms: 42",
		"provider zhipu-cn:",
		"agent codex:",
		"model glm-5.2:",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("output missing %q\n%s", want, s)
		}
	}
}

func TestCmdProxyStats_jsonOutput(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	writeProxyLogFixture(t, logPath, []proxyLogEntry{{
		Timestamp:  time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC),
		Kind:       proxyLogKindRequest,
		Agent:      "claude",
		Provider:   "deepseek",
		Model:      "deepseek-v4-pro",
		StatusCode: 502,
		DurationMs: 9,
		Error:      "bad gateway",
	}})
	var out bytes.Buffer

	// When
	err := cmdProxyStats([]string{"--log", logPath, "--json"}, &out)

	// Then
	if err != nil {
		t.Fatalf("cmdProxyStats: %v", err)
	}
	var got proxyStats
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if got.Requests != 1 || got.Errors != 1 || got.Successes != 0 {
		t.Fatalf("got stats %#v", got)
	}
	if got.ByProvider["deepseek"].Errors != 1 {
		t.Fatalf("by provider = %#v", got.ByProvider)
	}
}

func TestDefaultProxyLogPath_underCodeSwitchDir(t *testing.T) {
	// Given
	home := t.TempDir()
	t.Setenv("HOME", home)

	// When
	path, err := defaultProxyLogPath()
	if err != nil {
		t.Fatalf("defaultProxyLogPath: %v", err)
	}

	// Then
	want := filepath.Join(home, ".code-switch", "proxy.jsonl")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestProxyLogEntry_marshalsExtendedFields(t *testing.T) {
	// Given
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.close() })
	entry := proxyLogEntry{
		Timestamp:    time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC),
		Kind:         proxyLogKindRequest,
		Agent:        "codex",
		Method:       "POST",
		Path:         "/v1/responses",
		Provider:     "zhipu-cn",
		Model:        "glm-5.2",
		StatusCode:   200,
		DurationMs:   11,
		InputTokens:  2,
		OutputTokens: 4,
		TotalTokens:  6,
	}

	// When
	logger.logRequest(entry)
	if err := logger.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Then
	got := readProxyLogEntries(t, logPath)
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1", len(got))
	}
	if got[0].Kind != proxyLogKindRequest || got[0].Agent != "codex" || got[0].TotalTokens != 6 {
		t.Fatalf("got %#v", got[0])
	}
}

func writeProxyLogFixture(t *testing.T, path string, entries []proxyLogEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			_ = f.Close()
			t.Fatalf("encode entry: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
}

func TestProxyHandlerLogger_recordsUsageTokensOnSuccessfulNonStream(t *testing.T) {
	// Given
	upstream, _ := startAnthropicUpstream(t, 0, `{"id":"msg_ok","type":"message","role":"assistant","model":"glm-5.2","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`)
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	logger, err := newProxyLogger(logPath)
	if err != nil {
		t.Fatalf("newProxyLogger: %v", err)
	}
	handler := newProxyHandlerWithRegistryLoggerAndAgent(ProxyRoute{
		Provider:         "zhipu-cn",
		Model:            "glm-5.2",
		UpstreamProtocol: protocolAnthropicMessages,
		UpstreamBaseURL:  upstream.URL,
		LocalToken:       "local-token",
	}, "provider-key", defaultProtocolRegistry(), logger, "codex")
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
	var requestEntry *proxyLogEntry
	for i := range entries {
		if entries[i].Kind == proxyLogKindRequest || entries[i].Kind == "" {
			requestEntry = &entries[i]
		}
	}
	if requestEntry == nil {
		t.Fatalf("no request entry in %#v", entries)
	}
	if requestEntry.Agent != "codex" {
		t.Fatalf("agent = %q, want codex", requestEntry.Agent)
	}
	if requestEntry.InputTokens != 7 || requestEntry.OutputTokens != 3 {
		t.Fatalf("tokens = %d/%d, want 7/3; entry=%#v", requestEntry.InputTokens, requestEntry.OutputTokens, *requestEntry)
	}
	if requestEntry.TotalTokens != 10 {
		t.Fatalf("totalTokens = %d, want 10", requestEntry.TotalTokens)
	}
}
