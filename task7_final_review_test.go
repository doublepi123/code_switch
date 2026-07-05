package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// This file holds the regression tests for the final-review blocker/important
// issues. They were written FIRST (TDD, RED) and drove the GREEN changes in
// proxy_lifecycle.go / proxy_cmd.go / run.go / proxy_config.go / main.go; they
// now pass and guard against regressions.

// ---------------------------------------------------------------------------
// Task 1 (Critical): proxy stop must NOT kill based on /healthz 200 alone.
// It needs an instance identity check via InstanceID.
// ---------------------------------------------------------------------------

// TestHealthzExposesInstanceID verifies the serve-time /healthz handler
// returns the instance id (and pid) in its JSON body so that stop can
// verify the responding server is actually the proxy the state file points
// at (and not an unrelated process that happened to bind the same port and
// answer 200). The instance id must be non-empty and non-sensitive.
func TestHealthzExposesInstanceID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	inst, err := prepareProxyServe("codex", "127.0.0.1", 0, "csproxy-instance-tok")
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()
	go func() { _ = inst.server.Serve(inst.ln) }()

	if inst.state.InstanceID == "" {
		t.Fatalf("state.InstanceID is empty; serve must generate a non-sensitive id")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", inst.state.Port))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var hb struct {
		OK         bool   `json:"ok"`
		InstanceID string `json:"instanceID"`
		PID        int    `json:"pid"`
	}
	if err := json.Unmarshal(body, &hb); err != nil {
		t.Fatalf("health body not json: %v (body=%s)", err, body)
	}
	if !hb.OK {
		t.Fatalf("health ok=false: %s", body)
	}
	if hb.InstanceID == "" {
		t.Fatalf("health body missing instanceID: %s", body)
	}
	if hb.InstanceID != inst.state.InstanceID {
		t.Fatalf("health instanceID %q != state instanceID %q", hb.InstanceID, inst.state.InstanceID)
	}
	if hb.PID != inst.state.PID {
		t.Fatalf("health pid %d != state pid %d", hb.PID, inst.state.PID)
	}
}

// TestCmdProxyStopRefusesKillOnInstanceMismatch is the critical regression:
// when an UNRELATED server is serving /healthz on the recorded port (e.g.
// the real proxy died, port got recycled, and some other process bound it
// and answers 200), stop MUST NOT terminate the recorded PID. The recorded
// PID here is a live, long-running child that is NOT the proxy. The
// health-responding server is a separate stand-in. The instance id of the
// stand-in server differs from the state's instance id, so stop must
// classify this as "mismatch" and NOT kill.
func TestCmdProxyStopRefusesKillOnInstanceMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-based stop test is Unix-only")
	}
	t.Setenv("HOME", t.TempDir())
	// An unrelated health server that returns a DIFFERENT instance id.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"instanceID":"someone-else-instance","pid":999999}`))
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
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	state := ProxyRuntimeState{
		PID:        pid,
		Host:       "127.0.0.1",
		Port:       port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      "csproxy-mismatch",
		InstanceID: "our-proxy-instance",
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStop(nil, &out); err != nil {
		t.Fatalf("stop returned error on mismatch: %v", err)
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "mismatch") && !strings.Contains(strings.ToLower(got), "stale") {
		t.Fatalf("stop output for mismatch must mention mismatch/stale: %q", got)
	}
	// The unrelated live process MUST still be alive.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-time.After(500 * time.Millisecond):
		// Good: process still alive.
	case err := <-done:
		t.Fatalf("stop killed a recorded PID even though the responding health server was a different instance (wait returned %v)", err)
	}
}

// TestCmdProxyStopRefusesKillOnNonZeroPID asserts that stop never calls
// terminate on pid <= 0. A state file with PID 0 or negative must be
// treated as stale and only the state file removed.
func TestCmdProxyStopRefusesKillOnNonZeroPID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, pid := range []int{0, -1, -999} {
		state := ProxyRuntimeState{
			PID:       pid,
			Host:      "127.0.0.1",
			Port:      deadPortNumber(t),
			BaseURL:   "http://127.0.0.1:1/v1",
			Token:     "csproxy-badpid",
			StartedAt: time.Now().UTC(),
		}
		if err := writeProxyRuntimeState(state); err != nil {
			t.Fatalf("write: %v", err)
		}
		var out bytes.Buffer
		if err := cmdProxyStop(nil, &out); err != nil {
			t.Fatalf("stop returned error for pid=%d: %v", pid, err)
		}
		// State must have been removed regardless.
		if _, err := readProxyRuntimeState(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("state file still present after stop for pid=%d: %v", pid, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Task 2 (Important): proxy start must NOT launch a second proxy when an
// existing healthy proxy state exists. It should report already-running
// with the base_url and not spawn.
// ---------------------------------------------------------------------------

func TestCmdProxyStartRefusesWhenAlreadyRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("injectable sleep-based child is Unix-only")
	}
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}

	// Simulate an already-running healthy proxy: serve a /healthz with a
	// stable instance id, write a matching state.
	mux := http.NewServeMux()
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
	existingInstID := "existing-running-instance"
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// pid: real os.Getpid so the health pid is a live pid.
		fmt.Fprintf(w, `{"ok":true,"instanceID":%q,"pid":%d}`, existingInstID, os.Getpid())
	})
	state := ProxyRuntimeState{
		PID:        os.Getpid(),
		Host:       "127.0.0.1",
		Port:       port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      "csproxy-existing-secret",
		InstanceID: existingInstID,
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Hook the spawn so we can detect whether cmdProxyStart spawned.
	spawnCalled := false
	prev := proxyServeExec
	proxyServeExec = func(agent, token string) (*exec.Cmd, error) {
		spawnCalled = true
		return nil, errors.New("spawn should not have been called")
	}
	defer func() { proxyServeExec = prev }()

	var out bytes.Buffer
	err = cmdProxyStart(nil, &out)
	if err != nil {
		t.Fatalf("start should return nil when already running, got: %v", err)
	}
	if spawnCalled {
		t.Fatalf("start must NOT spawn a child when a healthy proxy already exists")
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "running") {
		t.Fatalf("start output should mention running: %q", got)
	}
	if !strings.Contains(got, state.BaseURL) {
		t.Fatalf("start output should include the existing base_url %q: %q", state.BaseURL, got)
	}
}

// ---------------------------------------------------------------------------
// Task 3 (Important): start/serve must error when the selected provider has
// no API key, EXCEPT for preset.NoAPIKey providers (e.g. ollama).
// We use zhipu-cn (which requires a key) with NO key set.
// ---------------------------------------------------------------------------

func TestPrepareProxyServeErrorsWithoutAPIKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Configure the route but DO NOT set a key for zhipu-cn.
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	_, err := prepareProxyServe("codex", "127.0.0.1", 0, "csproxy-nokey-tok")
	if err == nil {
		t.Fatal("expected error when provider has no API key, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "api key") && !strings.Contains(strings.ToLower(err.Error()), "apikey") {
		t.Fatalf("error should mention api key: %v", err)
	}
}

func TestPrepareProxyServeAllowsNoAPIKeyPreset(t *testing.T) {
	// ollama is NoAPIKey; even with no key it must be allowed (it falls back
	// to local ollama that needs no auth).
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "ollama", "--model", "qwen3-coder"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	inst, err := prepareProxyServe("codex", "127.0.0.1", 0, "csproxy-ollama-tok")
	if err != nil {
		t.Fatalf("prepareProxyServe should succeed for NoAPIKey provider: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()
}

// ---------------------------------------------------------------------------
// Task 4 (Important): token must NOT be accepted via argv.
// cmdProxyServe should reject explicit --token and read CODE_SWITCH_PROXY_TOKEN.
// ---------------------------------------------------------------------------

func TestCmdProxyServeRejectsTokenFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	err := cmdProxyServe([]string{"--agent", "codex", "--token", "leaked-via-argv"}, &out)
	if err == nil {
		t.Fatal("expected error when --token is passed, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "token") {
		t.Fatalf("error should mention token: %v", err)
	}
}

func TestCmdProxyServeReadsTokenFromEnvOnly(t *testing.T) {
	// resolveProxyToken is now env-only: a non-empty flagToken must NOT
	// override the env value, and an empty env yields empty (no flag
	// fallback). This is the unit-level assertion that cmdProxyServe reads
	// the token from CODE_SWITCH_PROXY_TOKEN only.
	if tok := resolveProxyToken("flag-leak", "env-only-token"); tok != "env-only-token" {
		t.Fatalf("resolveProxyToken flag should be ignored (env-only): got %q", tok)
	}
	if tok := resolveProxyToken("flag-leak", ""); tok != "" {
		t.Fatalf("resolveProxyToken empty env should yield empty (no flag fallback): got %q", tok)
	}
	// Also assert the env var name is exactly CODE_SWITCH_PROXY_TOKEN so
	// the start-side spawn wiring and the serve-side reader agree.
	if proxyTokenEnv() != "CODE_SWITCH_PROXY_TOKEN" {
		t.Fatalf("proxyTokenEnv() = %q, want CODE_SWITCH_PROXY_TOKEN", proxyTokenEnv())
	}
}

// ---------------------------------------------------------------------------
// Task 5 (Important): cs run --dry-run must NOT print the real proxy token.
// ---------------------------------------------------------------------------

func TestRunCodexDryRunMasksProxyToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := AppConfig{Providers: map[string]StoredProvider{
		"minimax-cn": {APIKey: "sk-secret"},
	}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}
	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "codex", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	// The line must be present but with a placeholder, never the real token.
	if !strings.Contains(got, "CODE_SWITCH_PROXY_API_KEY=") {
		t.Fatalf("dry-run missing CODE_SWITCH_PROXY_API_KEY line:\n%s", got)
	}
	// Must NOT contain a real csproxy- token anywhere.
	if strings.Contains(got, "csproxy-") {
		t.Fatalf("dry-run output leaked a real csproxy- token:\n%s", got)
	}
	// Must contain the masked placeholder.
	if !strings.Contains(got, "CODE_SWITCH_PROXY_API_KEY=<token>") {
		t.Fatalf("dry-run must mask the token with <token>:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Task 6 (Important): host validation in `proxy configure`.
// Reject hosts containing spaces, schemes, or slashes.
// Allow localhost / IP / hostname / 0.0.0.0 / ::1.
// ---------------------------------------------------------------------------

func TestCmdProxyConfigureRejectsInvalidHosts(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"with_space", "127.0.0.1 evil"},
		{"with_scheme_http", "http://127.0.0.1"},
		{"with_scheme_https", "https://127.0.0.1"},
		{"with_slash", "127.0.0.1/path"},
		{"with_scheme_and_slash", "http://host:8080/x"},
		{"only_space", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var out bytes.Buffer
			err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "ollama", "--host", tc.host}, nil, &out)
			if err == nil {
				t.Fatalf("expected error for host %q, got nil", tc.host)
			}
		})
	}
}

func TestCmdProxyConfigureAcceptsValidHosts(t *testing.T) {
	hosts := []string{"127.0.0.1", "localhost", "0.0.0.0", "::1", "my-host.example.com"}
	for _, host := range hosts {
		t.Run(host, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var out bytes.Buffer
			err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "ollama", "--host", host}, nil, &out)
			if err != nil {
				t.Fatalf("expected host %q to be accepted, got error: %v", host, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Task 7 (Minor): usage text must not contain "Task 3".
// ---------------------------------------------------------------------------

func TestUsageTextHasNoTaskReferences(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)
	if strings.Contains(out.String(), "Task 3") {
		t.Fatalf("usage text must not contain 'Task 3':\n%s", out.String())
	}
}
