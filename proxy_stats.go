package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// proxyStatsBucket holds counters for one dimension (provider / agent / model).
type proxyStatsBucket struct {
	Requests     int   `json:"requests"`
	Successes    int   `json:"successes"`
	Errors       int   `json:"errors"`
	InputTokens  int   `json:"inputTokens,omitempty"`
	OutputTokens int   `json:"outputTokens,omitempty"`
	TotalTokens  int   `json:"totalTokens,omitempty"`
	DurationMs   int64 `json:"-"`
}

// proxyStats is the aggregated view of a proxy JSONL log.
type proxyStats struct {
	LogPath          string                      `json:"logPath"`
	Requests         int                         `json:"requests"`
	Successes        int                         `json:"successes"`
	Errors           int                         `json:"errors"`
	UpstreamAttempts int                         `json:"upstreamAttempts"`
	InputTokens      int                         `json:"inputTokens"`
	OutputTokens     int                         `json:"outputTokens"`
	TotalTokens      int                         `json:"totalTokens"`
	AvgDurationMs    int64                       `json:"avgDurationMs"`
	ByProvider       map[string]proxyStatsBucket `json:"byProvider,omitempty"`
	ByAgent          map[string]proxyStatsBucket `json:"byAgent,omitempty"`
	ByModel          map[string]proxyStatsBucket `json:"byModel,omitempty"`
	durationTotal    int64                       `json:"-"`
}

// proxyStatsFilter selects a subset of log entries for aggregation.
type proxyStatsFilter struct {
	Since time.Time
	Until time.Time
	Agent string
}

func aggregateProxyStats(logPath string, filter proxyStatsFilter) (proxyStats, error) {
	stats := proxyStats{
		LogPath:    logPath,
		ByProvider: map[string]proxyStatsBucket{},
		ByAgent:    map[string]proxyStatsBucket{},
		ByModel:    map[string]proxyStatsBucket{},
	}
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stats, nil
		}
		return proxyStats{}, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	// Proxy log lines are compact JSON; 1 MiB is ample for one entry.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry proxyLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip corrupt lines rather than failing the whole report; operators
			// still get a usable summary of the well-formed history.
			continue
		}
		if !proxyStatsEntryMatches(entry, filter) {
			continue
		}
		kind := entry.Kind
		if kind == "" {
			kind = proxyLogKindRequest
		}
		switch kind {
		case proxyLogKindUpstream:
			stats.UpstreamAttempts++
		case proxyLogKindRequest:
			recordProxyStatsRequest(&stats, entry)
		default:
			// Unknown kinds are ignored so future log shapes stay forward-compatible.
		}
	}
	if err := scanner.Err(); err != nil {
		return proxyStats{}, err
	}
	if stats.Requests > 0 {
		stats.AvgDurationMs = stats.durationTotal / int64(stats.Requests)
	}
	return stats, nil
}

func proxyStatsEntryMatches(entry proxyLogEntry, filter proxyStatsFilter) bool {
	if !filter.Since.IsZero() && entry.Timestamp.Before(filter.Since) {
		return false
	}
	if !filter.Until.IsZero() && !entry.Timestamp.Before(filter.Until) {
		return false
	}
	if agent := strings.TrimSpace(filter.Agent); agent != "" {
		if !strings.EqualFold(strings.TrimSpace(entry.Agent), agent) {
			return false
		}
	}
	return true
}

func recordProxyStatsRequest(stats *proxyStats, entry proxyLogEntry) {
	stats.Requests++
	success := entry.StatusCode >= 200 && entry.StatusCode < 400 && entry.Error == ""
	if success {
		stats.Successes++
	} else {
		stats.Errors++
	}
	stats.InputTokens += entry.InputTokens
	stats.OutputTokens += entry.OutputTokens
	total := entry.TotalTokens
	if total == 0 {
		total = entry.InputTokens + entry.OutputTokens
	}
	stats.TotalTokens += total
	stats.durationTotal += entry.DurationMs

	if provider := strings.TrimSpace(entry.Provider); provider != "" {
		stats.ByProvider[provider] = bumpProxyStatsBucket(stats.ByProvider[provider], entry, success, total)
	}
	if agent := strings.TrimSpace(entry.Agent); agent != "" {
		stats.ByAgent[agent] = bumpProxyStatsBucket(stats.ByAgent[agent], entry, success, total)
	}
	if model := strings.TrimSpace(entry.Model); model != "" {
		stats.ByModel[model] = bumpProxyStatsBucket(stats.ByModel[model], entry, success, total)
	}
}

func bumpProxyStatsBucket(b proxyStatsBucket, entry proxyLogEntry, success bool, total int) proxyStatsBucket {
	b.Requests++
	if success {
		b.Successes++
	} else {
		b.Errors++
	}
	b.InputTokens += entry.InputTokens
	b.OutputTokens += entry.OutputTokens
	b.TotalTokens += total
	b.DurationMs += entry.DurationMs
	return b
}

const proxyStatsUsage = "usage: code-switch proxy stats [--log path] [--since duration|RFC3339] [--agent name] [--json]"

func cmdProxyStats(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("proxy stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	logFlag := fs.String("log", "", "proxy JSONL log path (default: ~/.code-switch/proxy.jsonl)")
	sinceFlag := fs.String("since", "", "only include entries at or after this time (duration like 24h, or RFC3339)")
	agentFlag := fs.String("agent", "", "only include entries for this agent")
	jsonFlag := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(proxyStatsUsage)
	}

	logPath := strings.TrimSpace(*logFlag)
	if logPath == "" {
		if state, err := readProxyRuntimeState(); err == nil && strings.TrimSpace(state.LogPath) != "" {
			logPath = strings.TrimSpace(state.LogPath)
		}
	}
	if logPath == "" {
		path, err := defaultProxyLogPath()
		if err != nil {
			return err
		}
		logPath = path
	}

	filter := proxyStatsFilter{Agent: strings.TrimSpace(*agentFlag)}
	if raw := strings.TrimSpace(*sinceFlag); raw != "" {
		since, err := parseProxyStatsSince(raw, time.Now().UTC())
		if err != nil {
			return err
		}
		filter.Since = since
	}

	stats, err := aggregateProxyStats(logPath, filter)
	if err != nil {
		return err
	}
	if *jsonFlag {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}
	printProxyStats(out, stats)
	return nil
}

func parseProxyStatsSince(raw string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(raw); err == nil {
		if d < 0 {
			return time.Time{}, fmt.Errorf("--since duration must be non-negative")
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("--since must be a duration (e.g. 24h) or RFC3339 timestamp")
}

func printProxyStats(out io.Writer, stats proxyStats) {
	fmt.Fprintf(out, "log: %s\n", stats.LogPath)
	fmt.Fprintf(out, "requests: %d\n", stats.Requests)
	fmt.Fprintf(out, "successes: %d\n", stats.Successes)
	fmt.Fprintf(out, "errors: %d\n", stats.Errors)
	fmt.Fprintf(out, "upstream_attempts: %d\n", stats.UpstreamAttempts)
	fmt.Fprintf(out, "input_tokens: %d\n", stats.InputTokens)
	fmt.Fprintf(out, "output_tokens: %d\n", stats.OutputTokens)
	fmt.Fprintf(out, "total_tokens: %d\n", stats.TotalTokens)
	fmt.Fprintf(out, "avg_duration_ms: %d\n", stats.AvgDurationMs)
	printProxyStatsBuckets(out, "provider", stats.ByProvider)
	printProxyStatsBuckets(out, "agent", stats.ByAgent)
	printProxyStatsBuckets(out, "model", stats.ByModel)
}

func printProxyStatsBuckets(out io.Writer, label string, buckets map[string]proxyStatsBucket) {
	if len(buckets) == 0 {
		return
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(out)
	for _, key := range keys {
		b := buckets[key]
		fmt.Fprintf(out, "%s %s: requests=%d successes=%d errors=%d tokens=%d\n",
			label, key, b.Requests, b.Successes, b.Errors, b.TotalTokens)
	}
}
