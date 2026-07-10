package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This file holds the regression tests for the second-round final-review
// fixes. They were written FIRST (TDD, RED) and drove the GREEN changes in
// proxy_lifecycle.go / proxy_config.go; they now pass and guard against
// regressions in:
//
//   1. proxy stop validating BOTH the health report InstanceID AND PID.
//      An instanceID match with a DIFFERENT pid means the recorded live
//      process is NOT the proxy; it must not be killed.
//   2. proxy status validating the PID too; a pid mismatch must be
//      reported as stale/mismatch, not as running.
//   3. cmdProxyStart pre-validating the provider API key (same logic
//      as prepareProxyServe) BEFORE spawning the child. A missing key
//      for a non-NoAPIKey provider fails fast with an API key /
//      provider error and never calls proxyServeExec.
//   4. host validation rejecting bare host:port forms like
//      "127.0.0.1:8080", "localhost:18080", "[::1]:8080"; "::1" and
//      "[::1]" (bare bracketed IPv6) remain allowed.

// ---------------------------------------------------------------------------
// Task 1: proxy stop must verify the health-report PID too.
// ---------------------------------------------------------------------------

// TestCmdProxyStopRefusesKillOnPIDMismatch is the critical regression: when
// /healthz responds with an instance id that MATCHES the state but a PID that
// DIFFERS from the recorded PID, the recorded live process is NOT the proxy
// (it's almost certainly an unrelated process that happens to share the
// instance id via some bug, or the state file is inconsistent). stop MUST NOT
// kill the recorded PID in this case — only the (now-mismatched) state file
// is removed and a mismatch reported.
//
// The recorded PID is a live, long-running child; the health-responding
// server is a separate stand-in that echoes the matching instance id but a
// different pid. If stop kills the child, the test fails.
func TestCmdProxyStopRefusesKillOnPIDMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-based stop test is Unix-only")
	}
	t.Setenv("HOME", t.TempDir())
	const instanceID = "shared-instance-id"
	// An unrelated health server that returns the SAME instance id but a
	// DIFFERENT pid. The pid reported here is the test process's own pid
	// (so it's a real live pid), but it intentionally does not equal the
	// recorded state's pid (which is the sleep child below).
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"instanceID":%q,"pid":%d}`, instanceID, os.Getpid())
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux}
	srvDone := make(chan struct{})
	go func() { _ = srv.Serve(ln); close(srvDone) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-srvDone
	}()

	// A live, long-running child whose PID will be recorded in the state.
	// If stop incorrectly kills it, cmd.Wait() will return promptly.
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	// Sanity: the test-process pid (reported by healthz) MUST differ from
	// the recorded sleep child pid, otherwise the test setup is invalid.
	if pid == os.Getpid() {
		t.Fatalf("test setup error: sleep pid %d == test pid %d", pid, os.Getpid())
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	state := ProxyRuntimeState{
		PID:        pid,
		Host:       "127.0.0.1",
		Port:       port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      "csproxy-pidmismatch",
		InstanceID: instanceID,
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStop(nil, &out); err != nil {
		t.Fatalf("stop returned error on pid mismatch: %v", err)
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "mismatch") && !strings.Contains(strings.ToLower(got), "stale") {
		t.Fatalf("stop output for pid mismatch must mention mismatch/stale: %q", got)
	}
	// State must have been removed.
	if _, err := readProxyRuntimeState(); !os.IsNotExist(err) {
		t.Fatalf("state file should be removed after pid-mismatch stop: %v", err)
	}
	// The unrelated live process MUST still be alive.
	select {
	case <-time.After(500 * time.Millisecond):
		// Good: process still alive.
	case err := <-done:
		waited = true
		t.Fatalf("stop killed a recorded PID even though the health-reported pid differed (wait returned %v)", err)
	}
}

// ---------------------------------------------------------------------------
// Task 2: proxy status must verify the PID too.
// ---------------------------------------------------------------------------

// TestCmdProxyStatusReportsMismatchOnPIDMismatch verifies that when /healthz
// responds with the matching instance id but a DIFFERENT pid, `proxy status`
// must NOT report "running". It should report a stale/mismatch state so the
// operator knows the persisted PID is not the live proxy.
func TestCmdProxyStatusReportsMismatchOnPIDMismatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const instanceID = "status-shared-instance"
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Report the test process pid, which intentionally differs from
		// the recorded state pid (an arbitrary unrelated value).
		fmt.Fprintf(w, `{"ok":true,"instanceID":%q,"pid":%d}`, instanceID, os.Getpid())
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux}
	srvDone := make(chan struct{})
	go func() { _ = srv.Serve(ln); close(srvDone) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-srvDone
	}()

	// Recorded pid is an arbitrary non-zero value that is NOT the
	// health-reported pid. We avoid using a real live child here so the
	// test stays hermetic on all platforms; status never sends signals,
	// it only reads.
	recordedPID := os.Getpid() + 1000
	state := ProxyRuntimeState{
		PID:        recordedPID,
		Host:       "127.0.0.1",
		Port:       port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      "csproxy-status-mismatch",
		InstanceID: instanceID,
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStatus(nil, &out); err != nil {
		t.Fatalf("status returned error: %v", err)
	}
	got := strings.ToLower(out.String())
	if strings.Contains(got, "running") {
		t.Fatalf("status must NOT report running on pid mismatch: %q", out.String())
	}
	if !strings.Contains(got, "mismatch") && !strings.Contains(got, "stale") {
		t.Fatalf("status output for pid mismatch must mention mismatch/stale: %q", out.String())
	}
}

// ---------------------------------------------------------------------------
// Task 3: cmdProxyStart must pre-validate the provider API key.
// ---------------------------------------------------------------------------

// TestCmdProxyStartErrorsWithoutAPIKey verifies that `proxy start` fails fast
// with an API key / provider error when the configured provider requires a
// key but none is set. Crucially, the spawn hook (proxyServeExec) MUST NOT
// be called — no child process should be spawned when the route is doomed.
func TestCmdProxyStartErrorsWithoutAPIKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("spawn-hook assertion is Unix-only")
	}
	t.Setenv("HOME", t.TempDir())
	// Configure a route for zhipu-cn (a key-requiring provider) but DO NOT
	// set an API key.
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}

	spawnCalled := false
	prev := proxyServeExec
	proxyServeExec = func(agent, token string) (*exec.Cmd, error) {
		spawnCalled = true
		return nil, fmt.Errorf("spawn must not be called when provider has no API key")
	}
	defer func() { proxyServeExec = prev }()

	var out bytes.Buffer
	err := cmdProxyStart(nil, &out)
	if err == nil {
		t.Fatal("expected error when provider has no API key, got nil")
	}
	low := strings.ToLower(err.Error())
	if !strings.Contains(low, "api key") && !strings.Contains(low, "apikey") && !strings.Contains(low, "provider") {
		t.Fatalf("error should mention api key / provider: %v", err)
	}
	if spawnCalled {
		t.Fatalf("proxyServeExec must NOT be called when provider has no API key")
	}
}

// TestValidateProxyRouteHasAPIKey covers the extracted helper directly so the
// pre-check logic has its own unit-level coverage independent of the start
// path. The helper mirrors the prepareProxyServe API-key check: error for
// key-requiring providers with no key, nil for NoAPIKey providers.
func TestValidateProxyRouteHasAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Build a minimal AppConfig in memory rather than going through the
	// CLI; the helper takes a *ProxyRouteConfig and *AppConfig so we can
	// exercise the matrix directly.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Seed the on-disk config so resolveProviderPreset finds the preset.
	cfg := AppConfig{Providers: map[string]StoredProvider{}}
	cfgPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(cfgPath, cfg); err != nil {
		t.Fatalf("write cfg: %v", err)
	}

	// Case 1: zhipu-cn (key required), no key set -> error mentioning key.
	route := ProxyRouteConfig{Agent: "codex", Provider: "zhipu-cn", Model: "glm-5.2"}
	if err := validateProxyRouteHasAPIKey(route, &cfg); err == nil {
		t.Fatalf("expected error for zhipu-cn without API key, got nil")
	} else if low := strings.ToLower(err.Error()); !strings.Contains(low, "api key") && !strings.Contains(low, "apikey") {
		t.Fatalf("error should mention api key: %v", err)
	}

	// Case 2: zhipu-cn with a key set -> nil.
	cfg2 := AppConfig{Providers: map[string]StoredProvider{
		"zhipu-cn": {APIKey: "sk-set"},
	}}
	if err := validateProxyRouteHasAPIKey(route, &cfg2); err != nil {
		t.Fatalf("expected nil for zhipu-cn with API key, got: %v", err)
	}

	// Case 3: ollama (NoAPIKey) with no key -> nil.
	routeOllama := ProxyRouteConfig{Agent: "codex", Provider: "ollama", Model: "qwen3-coder"}
	if err := validateProxyRouteHasAPIKey(routeOllama, &cfg); err != nil {
		t.Fatalf("expected nil for ollama (NoAPIKey) without key, got: %v", err)
	}

	// Case 4: unknown provider -> error mentioning provider.
	routeUnknown := ProxyRouteConfig{Agent: "codex", Provider: "no-such", Model: "x"}
	if err := validateProxyRouteHasAPIKey(routeUnknown, &cfg); err == nil {
		t.Fatalf("expected error for unknown provider, got nil")
	}
}

// ---------------------------------------------------------------------------
// Task 4: host validation must reject bare host:port forms.
// ---------------------------------------------------------------------------

func TestValidateProxyHostRejectsBareHostPort(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"ipv4_with_port", "127.0.0.1:8080"},
		{"localhost_with_port", "localhost:18080"},
		{"ipv6_bracketed_with_port", "[::1]:8080"},
		{"hostname_with_port", "my-host.example.com:8888"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateProxyHost(tc.host)
			if err == nil {
				t.Fatalf("expected error for host %q, got nil", tc.host)
			}
		})
	}
}

func TestValidateProxyHostAcceptsBareHosts(t *testing.T) {
	hosts := []string{
		"127.0.0.1",
		"localhost",
		"0.0.0.0",
		"::1",
		"[::1]",
		"my-host.example.com",
	}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			if err := validateProxyHost(host); err != nil {
				t.Fatalf("expected host %q to be accepted, got: %v", host, err)
			}
		})
	}
}

// TestCmdProxyConfigureRejectsBareHostPort is the end-to-end assertion: a
// `proxy configure --host 127.0.0.1:8080` must fail at the CLI layer too,
// not just at the validator layer.
func TestCmdProxyConfigureRejectsBareHostPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "ollama", "--host", "127.0.0.1:8080"}, nil, &out)
	if err == nil {
		t.Fatal("expected error for host 127.0.0.1:8080, got nil")
	}
}
