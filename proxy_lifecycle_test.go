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
	"syscall"
	"testing"
	"time"
)

// --- State path ---

func TestProxyStatePathUsesCodeSwitchDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := proxyStatePath()
	want := filepath.Join(home, ".code-switch", "proxy-state.json")
	if path != want {
		t.Fatalf("proxyStatePath = %q, want %q", path, want)
	}
}

// --- Write / Read / Remove ---

func TestWriteReadProxyRuntimeState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := ProxyRuntimeState{
		PID:       123,
		Host:      "127.0.0.1",
		Port:      456,
		BaseURL:   "http://127.0.0.1:456/v1",
		Token:     "csproxy-test",
		StartedAt: time.Unix(10, 0).UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	got, err := readProxyRuntimeState()
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if got.PID != state.PID || got.BaseURL != state.BaseURL || got.Token != state.Token {
		t.Fatalf("state = %+v, want %+v", got, state)
	}
	if !got.StartedAt.Equal(state.StartedAt) {
		t.Fatalf("startedAt = %v, want %v", got.StartedAt, state.StartedAt)
	}
	// Raw file must be valid JSON (writeJSONAtomic emits indented JSON).
	data, err := os.ReadFile(proxyStatePath())
	if err != nil {
		t.Fatalf("read raw state: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("state is not json: %v", err)
	}
	if got, _ := raw["baseURL"].(string); got != state.BaseURL {
		t.Fatalf("raw baseURL = %q, want %q", got, state.BaseURL)
	}
}

func TestReadProxyRuntimeStateMissingIsNotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := readProxyRuntimeState()
	if err == nil {
		t.Fatal("expected error for missing state, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestRemoveProxyRuntimeStateIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Removing a non-existent state must not error.
	if err := removeProxyRuntimeState(); err != nil {
		t.Fatalf("remove missing state: %v", err)
	}
	if err := writeProxyRuntimeState(ProxyRuntimeState{PID: 7}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := removeProxyRuntimeState(); err != nil {
		t.Fatalf("remove existing state: %v", err)
	}
	if _, err := readProxyRuntimeState(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist after remove, got %v", err)
	}
}

// --- Status: no state ---

func TestCmdProxyStatusReportsNotRunningWhenNoState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdProxyStatus(nil, &out); err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("status output = %q", out.String())
	}
}

// --- Status: stale state (nothing listening) ---

func TestCmdProxyStatusReportsStaleState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Point at a port that is almost certainly not listening: bind a port and
	// release it immediately so the kernel can hand it to someone else, but
	// at call time nothing is listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	state := ProxyRuntimeState{
		PID:       os.Getpid(),
		Host:      "127.0.0.1",
		Port:      port,
		BaseURL:   fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:     "csproxy-stale-secret",
		StartedAt: time.Now().Add(-1 * time.Hour).UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStatus(nil, &out); err != nil {
		t.Fatalf("status returned error for stale state: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "stale") {
		t.Fatalf("status output missing stale marker: %q", got)
	}
	// Status must NEVER print the local token, even for stale state.
	if strings.Contains(got, state.Token) {
		t.Fatalf("status leaked token in stale output: %q", got)
	}
}

// --- Status: running (live /healthz) ---

func TestCmdProxyStatusReportsRunningWhenHealthy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const instanceID = "csproxy-status-running-instance"
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"instanceID":%q,"pid":%d}`, instanceID, os.Getpid())
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	state := ProxyRuntimeState{
		PID:        os.Getpid(),
		InstanceID: instanceID,
		Host:       "127.0.0.1",
		Port:       port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      "csproxy-running-secret",
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write state: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStatus(nil, &out); err != nil {
		t.Fatalf("status error: %v", err)
	}
	got := out.String()
	for _, want := range []string{"running", "pid:", "base_url:", state.BaseURL, "started_at:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}
	// Must NOT print the token.
	if strings.Contains(got, state.Token) {
		t.Fatalf("status leaked token:\n%s", got)
	}
}

// --- Status rejects extra args ---

func TestCmdProxyStatusRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdProxyStatus([]string{"unexpected"}, &out); err == nil {
		t.Fatal("expected usage error for extra args, got nil")
	}
}

// --- checkProxyHealth direct ---

func TestCheckProxyHealthSuccessAndFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"instanceID":"test-instance","pid":12345}`))
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	state := ProxyRuntimeState{Host: "127.0.0.1", Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	report, err := checkProxyHealth(ctx, state)
	if err != nil {
		t.Fatalf("checkProxyHealth on live server: %v", err)
	}
	if !report.OK {
		t.Fatalf("report.OK = false, want true")
	}
	if report.InstanceID != "test-instance" {
		t.Fatalf("report.InstanceID = %q, want test-instance", report.InstanceID)
	}
	if report.PID != 12345 {
		t.Fatalf("report.PID = %d, want 12345", report.PID)
	}

	// Failure: nothing listening on a released port.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen2: %v", err)
	}
	badPort := ln2.Addr().(*net.TCPAddr).Port
	_ = ln2.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if _, err := checkProxyHealth(ctx2, ProxyRuntimeState{Host: "127.0.0.1", Port: badPort}); err == nil {
		t.Fatal("expected health error on dead port, got nil")
	}
}

// --- Serve: error paths (pre-listen, testable without blocking) ---

func TestCmdProxyServeRequiresToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Configure a valid route so the failure is purely about --token.
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	var out bytes.Buffer
	err := cmdProxyServe([]string{"--agent", "codex"}, &out)
	if err == nil {
		t.Fatal("expected error when --token omitted, got nil")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("error does not mention token: %v", err)
	}
}

func TestCmdProxyServeErrorsOnMissingRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// No route configured at all; token provided via env so the failure is
	// the route. (--token is intentionally NOT used: argv must never carry
	// the token, and cmdProxyServe now rejects an explicit --token.)
	t.Setenv(proxyTokenEnvName, "csproxy-tok")
	var out bytes.Buffer
	err := cmdProxyServe([]string{"--agent", "codex"}, &out)
	if err == nil {
		t.Fatal("expected error for missing route, got nil")
	}
}

// --- Serve: prepareProxyServe writes state + serves /healthz ---

// deadPortNumber binds a TCP listener, reads its port, closes it and returns
// the port number. Useful for picking a port that is free at call time but
// almost certainly not listening moments later.
func deadPortNumber(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestPrepareProxyServeBindsAndWritesState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	inst, err := prepareProxyServe("codex", "127.0.0.1", 0, "csproxy-serve-tok")
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()
	if inst.state.PID != os.Getpid() {
		t.Fatalf("state PID = %d, want %d", inst.state.PID, os.Getpid())
	}
	if inst.state.Port == 0 {
		t.Fatal("state Port = 0, want OS-assigned")
	}
	if inst.state.BaseURL == "" || !strings.Contains(inst.state.BaseURL, "/v1") {
		t.Fatalf("state BaseURL = %q", inst.state.BaseURL)
	}
	if inst.state.Token != "csproxy-serve-tok" {
		t.Fatalf("state Token = %q", inst.state.Token)
	}

	// State file must reflect the same values.
	got, err := readProxyRuntimeState()
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if got.PID != inst.state.PID || got.Port != inst.state.Port || got.BaseURL != inst.state.BaseURL {
		t.Fatalf("persisted state = %+v, want %+v", got, inst.state)
	}

	// Serve in a goroutine and probe /healthz.
	go func() { _ = inst.server.Serve(inst.ln) }()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", inst.state.Port))
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("health body = %q", string(body))
	}

	// Token must NOT appear in the health body.
	if strings.Contains(string(body), inst.state.Token) {
		t.Fatalf("health body leaked token: %q", string(body))
	}
}

// --- Stop: no state ---

func TestCmdProxyStopNotRunningWhenNoState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdProxyStop(nil, &out); err != nil {
		t.Fatalf("stop error: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("stop output = %q", out.String())
	}
}

// --- Stop: stale state -> remove and report ---

// deadChildPID spawns a short-lived child, waits for it to exit, and returns
// its (now very likely unused) PID. PIDs are not reused instantly, so this is
// reliable for the duration of a test.
func deadChildPID(t *testing.T) int {
	t.Helper()
	// Use a cross-platform no-op. "true" exists on Unix; on Windows fall back
	// to cmd /c exit via the guard below.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "exit", "0")
	} else {
		cmd = exec.Command("true")
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn dead child: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		// "true" exits 0; ignore non-zero just in case.
		_ = err
	}
	return pid
}

// TestCmdProxyStopCleansStaleStateWithoutKilling covers the regression where
// `proxy stop` used to call terminateProxyProcess on stale state. Stale means
// the /healthz probe failed; the recorded PID may well belong to an unrelated
// live process (PID reuse, or a leftover state file from a crashed proxy on a
// machine that has since recycled the PID). Killing it would be a serious
// misbehavior. The correct behavior on stale state is to remove the state file
// and report "stale" WITHOUT sending any signal to the PID.
//
// To make this test meaningful we deliberately point the state at a LIVE,
// long-running child process (so PID is alive) but point /healthz at a dead
// port (so the health probe fails => state is classified stale). If stop tries
// to kill the PID, the child dies and the test fails.
func TestCmdProxyStopCleansStaleStateWithoutKilling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-based stop test is Unix-only")
	}
	t.Setenv("HOME", t.TempDir())
	// A live, long-running child whose PID will be recorded in the state.
	// If stop incorrectly kills it, cmd.Wait() will return promptly.
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		// Best-effort cleanup: kill the child if it somehow survived.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	state := ProxyRuntimeState{
		PID:       pid,
		Host:      "127.0.0.1",
		Port:      deadPortNumber(t), // nothing listening => health fails => stale
		BaseURL:   "http://127.0.0.1:1/v1",
		Token:     "csproxy-stop-stale",
		StartedAt: time.Now().Add(-1 * time.Hour).UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStop(nil, &out); err != nil {
		t.Fatalf("stop on stale state returned error: %v", err)
	}
	got := out.String()
	// State must have been removed.
	if _, err := readProxyRuntimeState(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale state file still present after stop: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "stale") {
		t.Fatalf("stop output for stale state must say stale: %q", got)
	}
	// The unrelated live process MUST still be alive. If it exited, stop
	// killed it — that is exactly the regression we are guarding against.
	// We probe by checking whether cmd.Wait returns. A still-running process
	// blocks Wait, so we use a channel with a short timeout.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-time.After(500 * time.Millisecond):
		// Good: the process is still running after the timeout window.
	case err := <-done:
		t.Fatalf("stop killed a stale-state PID that was an unrelated live process (wait returned %v)", err)
	}
}

// TestCmdProxyStopCleansStaleDeadPID covers the simpler stale case where the
// recorded PID is already dead (classic leftover state file).
func TestCmdProxyStopCleansStaleDeadPID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pid := deadChildPID(t)
	state := ProxyRuntimeState{
		PID:       pid,
		Host:      "127.0.0.1",
		Port:      deadPortNumber(t),
		BaseURL:   "http://127.0.0.1:1/v1",
		Token:     "csproxy-stop-stale",
		StartedAt: time.Now().Add(-1 * time.Hour).UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStop(nil, &out); err != nil {
		t.Fatalf("stop on stale state returned error: %v", err)
	}
	got := out.String()
	if _, err := readProxyRuntimeState(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale state file still present after stop: %v", err)
	}
	if !strings.Contains(strings.ToLower(got), "stale") {
		t.Fatalf("stop output for stale (dead PID) state must say stale: %q", got)
	}
}

// --- Stop: live child process serving /healthz -> signal + remove state ---

// TestCmdProxyStopTerminatesHealthyProxy covers the RUNNING case: the
// recorded PID is alive AND serves a healthy /healthz on the recorded port
// whose reported pid MATCHES the recorded pid. stop must then actually
// terminate the process and remove the state. We use a real HTTP server
// bound to a random port as the stand-in proxy; the health handler echoes
// the SAME pid we record in the state (the sleep child's pid) so the new
// instance+pid identity checks both pass.
func TestCmdProxyStopTerminatesHealthyProxy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-based stop test is Unix-only")
	}
	t.Setenv("HOME", t.TempDir())
	const instanceID = "csproxy-stop-healthy-instance"
	// proxyPIDHolder lets the healthz handler echo the recorded proxy pid
	// even though the handler is registered before the child is spawned.
	// The child pid is written here once it is known; the handler reads it
	// per-request so a concurrent stop observes the same value as the
	// state file.
	var proxyPIDHolder int
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"instanceID":%q,"pid":%d}`, instanceID, proxyPIDHolder)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux}
	srvDone := make(chan struct{})
	go func() { _ = srv.Serve(ln); close(srvDone) }()

	// Spawn a long-running child to act as the "proxy process" we expect
	// stop to terminate. The health server (in-process) is what makes the
	// state read as healthy; the PID we record is this child so we can
	// observe whether it actually got the signal.
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	proxyPIDHolder = pid
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-srvDone
	}()

	state := ProxyRuntimeState{
		PID:        pid,
		InstanceID: instanceID,
		Host:       "127.0.0.1",
		Port:       port, // health server is live here => running
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      "csproxy-stop-live",
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStop(nil, &out); err != nil {
		t.Fatalf("stop returned error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "stopped") {
		t.Fatalf("stop output = %q", got)
	}
	// The child (recorded PID) must have been terminated.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-time.After(3 * time.Second):
		t.Fatal("child process did not exit within 3s after stop")
	case err := <-done:
		_ = err // expected: signal: interrupt (or killed)
	}
	// State must have been removed.
	if _, err := readProxyRuntimeState(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file still present after stop: %v", err)
	}
}

// --- Stop rejects extra args ---

func TestCmdProxyStopRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdProxyStop([]string{"nope"}, &out); err == nil {
		t.Fatal("expected usage error for extra args, got nil")
	}
}

// --- Start: arg validation (no spawn, to keep the suite hermetic) ---

func TestCmdProxyStartRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdProxyStart([]string{"bogus"}, &out); err == nil {
		t.Fatal("expected usage error for extra args, got nil")
	}
}

// --- Issue 2: cmdProxyServe must respect an already-configured host ---

// TestCmdProxyServeRespectsConfiguredHost verifies that when --host is NOT
// passed on the command line, prepareProxyServe falls back to the host
// persisted in cfg.Proxy.Host (set via `proxy configure --host`) rather than
// hard-coding 127.0.0.1. The bug under test: cmdProxyServe pre-emptively
// turned an empty --host into "127.0.0.1" before calling prepareProxyServe,
// so the configured host was never consulted.
//
// We pick a distinctive non-default host ("localhost") and assert the bound
// state matches it. (Using "localhost" rather than a literal IPv4 keeps the
// test hermetic across environments; net.Listen resolves it to loopback.)
func TestCmdProxyServeRespectsConfiguredHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--host", "localhost"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	// Call prepareProxyServe with an EMPTY host so it must consult cfg.Proxy.
	inst, err := prepareProxyServe("codex", "", 0, "csproxy-host-tok")
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()
	// localhost and 127.0.0.1 resolve to loopback; either is acceptable as
	// long as it is NOT the bare empty string and reflects the configured
	// value. We assert the configured host was used verbatim.
	if inst.state.Host != "localhost" {
		t.Fatalf("state Host = %q, want %q (configured host must be respected)", inst.state.Host, "localhost")
	}
}

// TestCmdProxyServeEmptyHostFallsBackToConfigWhenUnset is the companion: with
// NO host configured at all (proxy block defaults), an empty --host must fall
// back to the default 127.0.0.1 inside prepareProxyServe, not in the flag
// handler. This guards against the regression by asserting the default path
// still works when cmdProxyServe does NOT pre-fill the host.
func TestCmdProxyServeEmptyHostFallsBackToConfigWhenUnset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	// Configure a route WITHOUT --host so cfg.Proxy.Host is left at the
	// configured default 127.0.0.1.
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	inst, err := prepareProxyServe("codex", "", 0, "csproxy-default-tok")
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()
	if inst.state.Host != "127.0.0.1" {
		t.Fatalf("state Host = %q, want default 127.0.0.1", inst.state.Host)
	}
}

// --- Issue 3 & 4: cmdProxyStart uses an injectable executable ---

// fakeProxyChild is a minimal stand-in for the `proxy serve` child process.
// It serves a healthy /healthz on the recorded port (or fails health if
// unhealthy is true), writes the runtime state file, and blocks until the
// caller closes the stop channel (mimicking a long-running server). It is
// driven via the injectable proxyServeExec hook so we never need to spawn the
// real test binary.
type fakeProxyChild struct {
	agent     string
	token     string
	unhealthy bool
	state     *ProxyRuntimeState
	done      chan struct{}
}

func (f *fakeProxyChild) run() (*exec.Cmd, error) {
	// Generate a per-fake-child instance id so the start/stop paths can
	// verify the responding /healthz server is THIS child. Mirrors the
	// production prepareProxyServe behavior.
	instanceID, err := randomProxyInstanceID()
	if err != nil {
		return nil, err
	}
	// proxyPIDHolder carries the sleep child's pid into the /healthz
	// handler, which is registered BEFORE the child is spawned. The holder
	// is written once the child is forked; the handler reads it per
	// request so concurrent health probes observe the same pid the state
	// file records. This mirrors production, where /healthz reports the
	// proxy's OWN pid (not the test process's pid).
	var proxyPIDHolder int
	// Create a real HTTP listener to back the health check (or skip it for
	// the unhealthy variant).
	var ln net.Listener
	var port int
	if !f.unhealthy {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		port = ln.Addr().(*net.TCPAddr).Port
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ok":true,"instanceID":%q,"pid":%d}`, instanceID, proxyPIDHolder)
		})
		srv := &http.Server{Handler: mux}
		go func() {
			<-f.done
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		}()
		go func() { _ = srv.Serve(ln) }()
	} else {
		// Pick a dead port so health checks fail. We bind then immediately
		// release a port inline rather than via deadPortNumber (which needs
		// a *testing.T) so this helper stays t-agnostic.
		tmp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		port = tmp.Addr().(*net.TCPAddr).Port
		_ = tmp.Close()
	}

	// Build a fake cmd with a no-op process; we synthesize a PID by forking
	// a sleep so the recorded PID is a real, killable process.
	sl := exec.Command("sleep", "3600")
	if err := sl.Start(); err != nil {
		if ln != nil {
			_ = ln.Close()
		}
		return nil, err
	}
	// Publish the child pid so the healthz handler echoes the SAME pid the
	// state file records (see the comment on proxyPIDHolder above).
	proxyPIDHolder = sl.Process.Pid
	state := ProxyRuntimeState{
		PID:        sl.Process.Pid,
		InstanceID: instanceID,
		Host:       "127.0.0.1",
		Port:       port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      f.token,
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		_ = sl.Process.Kill()
		_ = sl.Wait()
		if ln != nil {
			_ = ln.Close()
		}
		return nil, err
	}
	f.state = &state
	// Return a cmd whose Process is the sleep child, so cmdProxyStart can
	// observe a real PID and (on failure) kill it. We replace the Wait
	// behavior by spawning a goroutine that reaps the sleep when done fires.
	cmd := exec.Command("sleep", "0") // placeholder; Process replaced below
	cmd.Process = sl.Process
	go func() {
		<-f.done
		_ = sl.Wait() // reap if killed by the caller
	}()
	return cmd, nil
}

// withFakeProxyExec swaps in the injectable proxyServeExec hook and returns a
// restore function. The fake child's lifecycle is owned by the test via the
// returned done channel.
func withFakeProxyExec(unhealthy bool) (token string, child *fakeProxyChild, restore func()) {
	tok := "csproxy-start-secret-1234567890"
	child = &fakeProxyChild{
		token:     tok,
		unhealthy: unhealthy,
		done:      make(chan struct{}),
	}
	prev := proxyServeExec
	proxyServeExec = func(agent, token string) (*exec.Cmd, error) {
		child.agent = agent
		return child.run()
	}
	return tok, child, func() {
		proxyServeExec = prev
		close(child.done)
	}
}

// TestCmdProxyStartSuccessPath covers the happy path of `proxy start`:
//   - output contains pid: and base_url:
//   - output does NOT leak the local token
//   - the runtime state is persisted and `proxy status` reports running
//   - a subsequent `proxy stop` terminates the child and removes the state
func TestCmdProxyStartSuccessPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("injectable sleep-based child is Unix-only")
	}
	t.Setenv("HOME", t.TempDir())
	// Configure a valid route so buildProxyRouteFromConfig passes.
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}

	tok, child, restore := withFakeProxyExec(false)
	defer restore()

	var out bytes.Buffer
	if err := cmdProxyStart(nil, &out); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "pid:") {
		t.Fatalf("start output missing pid:\n%s", got)
	}
	if !strings.Contains(got, "base_url:") {
		t.Fatalf("start output missing base_url:\n%s", got)
	}
	if strings.Contains(got, tok) {
		t.Fatalf("start output leaked token:\n%s", got)
	}

	// Runtime state must be present and report running via status.
	state, err := readProxyRuntimeState()
	if err != nil {
		t.Fatalf("read state after start: %v", err)
	}
	if state.PID != child.state.PID {
		t.Fatalf("state PID = %d, want %d", state.PID, child.state.PID)
	}
	var statusOut bytes.Buffer
	if err := cmdProxyStatus(nil, &statusOut); err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(statusOut.String(), "running") {
		t.Fatalf("status after start should report running: %q", statusOut.String())
	}

	// Stop must terminate the recorded PID and remove the state.
	var stopOut bytes.Buffer
	if err := cmdProxyStop(nil, &stopOut); err != nil {
		t.Fatalf("stop error: %v", err)
	}
	if _, err := readProxyRuntimeState(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state still present after stop: %v", err)
	}
}

// TestCmdProxyStartCleansUpOnHealthFailure covers Issue 3: when the spawned
// child never becomes healthy, cmdProxyStart must (a) return an error, (b)
// terminate the child process, and (c) remove the runtime state file. Without
// cleanup we would leak an orphan process and a stale state file.
func TestCmdProxyStartCleansUpOnHealthFailure(t *testing.T) {
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

	_, child, restore := withFakeProxyExec(true)
	defer restore()

	var out bytes.Buffer
	err := cmdProxyStart(nil, &out)
	if err == nil {
		t.Fatal("start should return an error when child never becomes healthy")
	}
	spawnedPID := child.state.PID

	// State file MUST be removed (no stale state leak).
	if _, rerr := readProxyRuntimeState(); !errors.Is(rerr, os.ErrNotExist) {
		t.Fatalf("state file should be removed on health failure: %v", rerr)
	}

	// The orphaned child process MUST have been terminated. We poll the
	// recorded PID with signal 0 (existence probe) within a grace window.
	// Note: we use syscall.Signal(0), NOT os.Signal(nil) — the latter
	// passes a nil Signal (zero-value interface) whose behavior is
	// undefined and platform-dependent; syscall.Signal(0) is the
	// documented, reliable "am I alive?" probe on Unix.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		proc, perr := os.FindProcess(spawnedPID)
		if perr != nil {
			break // not findable -> gone
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			break // process is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	proc, _ := os.FindProcess(spawnedPID)
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("orphan child pid %d still alive after health failure cleanup", spawnedPID)
	}
}

// --- Issue 5: IPv6-safe hostport construction ---

// TestProxyHealthURLIsIPv6Safe asserts that proxyHealthURL uses
// net.JoinHostPort semantics for IPv6 hosts so the address is bracketed and
// the URL is well-formed. A naive fmt.Sprintf("%s:%d", host, port) would
// produce "fe80::1:8080/healthz" which parses host=fe80::1:8080 wrongly.
func TestProxyHealthURLIsIPv6Safe(t *testing.T) {
	state := ProxyRuntimeState{Host: "::1", Port: 8080}
	u := proxyHealthURL(state)
	// Must contain "[::1]" bracketed host.
	if !strings.Contains(u, "[::1]:8080") {
		t.Fatalf("proxyHealthURL for IPv6 host = %q, want bracketed [::1]:8080", u)
	}
	// Must NOT contain an unbracketed "::1:8080" (the broken form).
	if strings.Contains(u, "://::1:") {
		t.Fatalf("proxyHealthURL produced unbracketed IPv6 form: %q", u)
	}
}

// TestPrepareProxyServeBaseURLIsIPv6Safe asserts the recorded BaseURL is
// bracketed for IPv6 listen hosts.
func TestPrepareProxyServeBaseURLIsIPv6Safe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("IPv6 loopback listen is flaky on some Windows CI runners")
	}
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--host", "::1"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	inst, err := prepareProxyServe("codex", "", 0, "csproxy-v6-tok")
	if err != nil {
		t.Fatalf("prepareProxyServe on IPv6 loopback: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()
	if !strings.Contains(inst.state.BaseURL, "[::1]:") {
		t.Fatalf("BaseURL for IPv6 host should be bracketed, got %q", inst.state.BaseURL)
	}
}

// --- Issue: proxy start must NOT leak token via argv ---

// TestProxyServeExecDoesNotLeakTokenInArgv asserts the production spawn
// implementation does NOT pass the local token on the child's argv. argv is
// visible to every process on the system via /proc/<pid>/cmdline or `ps`, so
// it must never carry a secret. The token is instead forwarded via the
// CODE_SWITCH_PROXY_TOKEN environment variable, which `cmdProxyServe` reads
// as a fallback when --token is absent.
//
// We exercise buildProxyServeCommand (the argv/env builder used by the
// production defaultProxyServeExec) directly so the assertion is against the
// real production shape — no test-only spawn hook is involved.
func TestProxyServeExecDoesNotLeakTokenInArgv(t *testing.T) {
	tok := "csproxy-start-secret-1234567890"
	cmd := buildProxyServeCommand("/usr/local/bin/cs", "codex", tok)

	// The token must NOT appear in any argv element.
	for _, a := range cmd.Args {
		if strings.Contains(a, tok) {
			t.Fatalf("token leaked into argv element %q (full argv: %v)", a, cmd.Args)
		}
	}
	// Sanity: argv must contain the agent so the child knows the route.
	agentPresent := false
	for _, a := range cmd.Args {
		if a == "codex" {
			agentPresent = true
		}
	}
	if !agentPresent {
		t.Fatalf("agent codex missing from argv: %v", cmd.Args)
	}
	// argv must NOT contain --token at all (the old leaky form passed
	// --token <value>).
	for _, a := range cmd.Args {
		if a == "--token" {
			t.Fatalf("argv must not contain --token (token must flow via env): %v", cmd.Args)
		}
	}
	// The token MUST appear in the env slice under the proxy token env name.
	tokenInEnv := false
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, proxyTokenEnv()+"=") && strings.Contains(e, tok) {
			tokenInEnv = true
		}
	}
	if !tokenInEnv {
		t.Fatalf("token missing from cmd.Env under %s (must be forwarded via env)", proxyTokenEnv())
	}
}

// TestCmdProxyServeReadsTokenFromEnv covers the env-only token resolution
// path. As of the security hardening pass, cmdProxyServe reads the local
// proxy token EXCLUSIVELY from the CODE_SWITCH_PROXY_TOKEN env var and
// rejects an explicit --token (which would leak via argv). The
// resolveProxyToken helper is therefore env-only: the legacy flagToken
// parameter is accepted but IGNORED.
//
// We exercise the resolver helper directly so we don't have to spawn the
// blocking serve goroutine. cmdProxyServe is wired to read os.Getenv
// directly, and resolveProxyToken documents the precedence rule.
func TestCmdProxyServeReadsTokenFromEnv(t *testing.T) {
	// Env is the ONLY source: a non-empty flag must NOT override the env.
	tok := resolveProxyToken("flag-tok", "env-tok")
	if tok != "env-tok" {
		t.Fatalf("env token must always win over flag (now env-only): got %q, want env-tok", tok)
	}
	// Empty flag + non-empty env -> env value.
	tok = resolveProxyToken("", "env-tok")
	if tok != "env-tok" {
		t.Fatalf("env token should be used when flag empty: got %q", tok)
	}
	// Both empty -> empty string; cmdProxyServe returns the canonical
	// "proxy token is required" error in that case.
	tok = resolveProxyToken("", "")
	if tok != "" {
		t.Fatalf("both empty should yield empty: got %q", tok)
	}
	// Whitespace-only flag is ignored (env still wins).
	tok = resolveProxyToken("   ", "env-tok")
	if tok != "env-tok" {
		t.Fatalf("whitespace flag should be ignored (env-only): got %q", tok)
	}
	// Empty env -> empty (no flag fallback anymore).
	tok = resolveProxyToken("flag-tok", "")
	if tok != "" {
		t.Fatalf("empty env must yield empty (no flag fallback): got %q", tok)
	}
}

// --- Issue: start failure cleanup must NOT clobber a pre-existing state ---

// TestCmdProxyStartCleanupPreservesExistingState covers the regression where
// `proxy start` unconditionally removed the runtime state file on spawn
// failure. If a previous healthy proxy was already running and recorded its
// state, a failed `start` (whose child never became healthy) must NOT delete
// that pre-existing state — otherwise it would silently invalidate a working
// proxy. Cleanup may only remove state the CURRENT spawn wrote (matching by
// PID or token).
func TestCmdProxyStartCleanupPreservesExistingState(t *testing.T) {
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

	// Simulate a pre-existing healthy proxy's state (different PID, different
	// token, different port). This state must survive the failed start.
	existingPID := deadChildPID(t)
	existing := ProxyRuntimeState{
		PID:       existingPID,
		Host:      "127.0.0.1",
		Port:      deadPortNumber(t),
		BaseURL:   "http://127.0.0.1:1/v1",
		Token:     "csproxy-pre-existing-token-aaaa",
		StartedAt: time.Now().Add(-1 * time.Hour).UTC(),
	}
	if err := writeProxyRuntimeState(existing); err != nil {
		t.Fatalf("write pre-existing state: %v", err)
	}

	// Spawn a child that NEVER becomes healthy (unhealthy=true).
	_, child, restore := withFakeProxyExec(true)
	defer restore()

	var out bytes.Buffer
	err := cmdProxyStart(nil, &out)
	if err == nil {
		t.Fatal("start should return an error when child never becomes healthy")
	}

	// The spawned child PID must differ from the pre-existing state's PID so
	// the test is meaningful.
	if child.state.PID == existing.PID {
		t.Fatalf("test setup error: spawned PID %d == existing PID %d", child.state.PID, existing.PID)
	}

	// The PRE-EXISTING state MUST be preserved (NOT removed/clobbered).
	got, rerr := readProxyRuntimeState()
	if rerr != nil {
		t.Fatalf("pre-existing state was removed on failed start (should be preserved): %v", rerr)
	}
	if got.Token != existing.Token {
		t.Fatalf("state token changed after failed start: got %q, want %q (existing state must be preserved)", got.Token, existing.Token)
	}
	if got.PID != existing.PID {
		t.Fatalf("state PID changed after failed start: got %d, want %d (existing state must be preserved)", got.PID, existing.PID)
	}
}
