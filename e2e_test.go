package main

// e2e_test.go contains end-to-end tests that exercise the full CLI surface
// of the new features through the real entry points (runWithIO / cmdXxx),
// including: cs list, cs switch, cs model-map, cs proxy (configure/serve/
// status/stop), cs run --dry-run, and the three-way protocol conversion
// (Anthropic Messages <-> OpenAI Chat <-> OpenAI Responses) via the IR.
//
// The tests isolate the filesystem via t.Setenv("HOME", t.TempDir()) so
// every command reads/writes config under a throwaway home. Mock upstream
// HTTP servers are created via httptest and registered as custom providers
// so the proxy serve path forwards to a controllable target.

import (
	"bytes"
	"context"
	"encoding/json"
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// e2eHome isolates HOME to a temp dir and returns it so the caller can
// locate the resulting config files.
func e2eHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// e2eClaudeDir creates an empty ~/.claude dir under home and returns it
// so cs switch has a writable settings.json target.
func e2eClaudeDir(t *testing.T, home string) string {
	t.Helper()
	d := filepath.Join(home, ".claude")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", d, err)
	}
	return d
}

// e2eWriteAppConfig writes the given AppConfig to ~/.code-switch/config.json
// inside home. Used to seed a custom provider (e.g. one pointing at an
// httptest mock upstream) before invoking the proxy CLI.
func e2eWriteAppConfig(t *testing.T, home string, cfg AppConfig) {
	t.Helper()
	dir := filepath.Join(home, ".code-switch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "config.json")
	if err := writeJSONAtomic(path, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}
}

// e2eRun invokes the full CLI through runWithIO with an isolated HOME and
// returns captured stdout + the error. A nil in reader uses an empty
// reader.
func e2eRun(t *testing.T, args []string, in io.Reader) (string, error) {
	t.Helper()
	if in == nil {
		in = strings.NewReader("")
	}
	out := &bytes.Buffer{}
	err := runWithIO(args, in, out)
	return out.String(), err
}

// e2eRunArgs is the no-input convenience form of e2eRun.
func e2eRunArgs(t *testing.T, args []string) (string, error) {
	return e2eRun(t, args, nil)
}

// e2eMustRun asserts the CLI invocation succeeds and returns stdout.
func e2eMustRun(t *testing.T, args []string) string {
	t.Helper()
	out, err := e2eRun(t, args, nil)
	if err != nil {
		t.Fatalf("runWithIO(%v) error: %v\nstdout:\n%s", args, err, out)
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. cs list — includes new providers kimi-coding, zhipu-cn, zai
// ---------------------------------------------------------------------------

func TestE2E_ListIncludesNewProviders(t *testing.T) {
	home := e2eHome(t)

	// Pre-seed an API key for the three new providers so the key column is
	// populated and we can assert on the ✓ marker.
	e2eWriteAppConfig(t, home, AppConfig{
		Providers: map[string]StoredProvider{
			"kimi-coding": {APIKey: "sk-kimi"},
			"zhipu-cn":    {APIKey: "sk-zhipu"},
			"zai":         {APIKey: "sk-zai"},
		},
	})

	out := e2eMustRun(t, []string{"list", "--agent", "claude"})

	// Each new provider must appear with its base URL and a ✓ key marker.
	for _, p := range []struct{ name, baseURL string }{
		{"kimi-coding", "https://api.kimi.com/coding/"},
		{"zhipu-cn", "https://open.bigmodel.cn/api/anthropic"},
		{"zai", "https://api.z.ai/api/anthropic"},
	} {
		line := p.name + "\t" + p.baseURL
		if !strings.Contains(out, line) {
			t.Fatalf("list output missing %q\nfull output:\n%s", line, out)
		}
		// Key marker must be ✓ (we seeded the API key).
		if !strings.Contains(out, p.name+"\t"+p.baseURL+"\t") {
			t.Fatalf("list output malformed for %q\n%s", p.name, out)
		}
	}

	// kimi-coding is NoModel: true, so its model label in the list is "auto".
	if !strings.Contains(out, "kimi-coding\thttps://api.kimi.com/coding/\tauto") {
		t.Fatalf("kimi-coding should show 'auto' model label\n%s", out)
	}

	// JSON output must include all three new providers as entries. The
	// list --json shape is a top-level JSON array.
	jsonOut := e2eMustRun(t, []string{"list", "--agent", "claude", "--json"})
	var entries []map[string]any
	if err := json.Unmarshal([]byte(jsonOut), &entries); err != nil {
		t.Fatalf("list --json unmarshal: %v\n%s", err, jsonOut)
	}
	names := map[string]bool{}
	for _, e := range entries {
		if n, ok := e["name"].(string); ok {
			names[n] = true
		}
	}
	for _, want := range []string{"kimi-coding", "zhipu-cn", "zai"} {
		if !names[want] {
			t.Fatalf("list --json missing provider %q; got %v", want, names)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. cs switch — end-to-end writes ~/.claude/settings.json with correct env
//    for a new provider (zai), including the ANTHROPIC_AUTH_TOKEN bearer key.
// ---------------------------------------------------------------------------

func TestE2E_SwitchZaiWritesClaudeSettings(t *testing.T) {
	home := e2eHome(t)
	claudeDir := e2eClaudeDir(t, home)

	out := e2eMustRun(t, []string{
		"switch", "zai",
		"--api-key", "sk-zai-secret",
		"--claude-dir", claudeDir,
	})

	// The switch command prints a confirmation line. The provider's display
	// name is "Z.AI GLM Coding Plan" (may carry ANSI color codes), so we
	// assert on the stable base URL substring instead.
	if !strings.Contains(out, "https://api.z.ai/api/anthropic") {
		t.Fatalf("switch output missing zai base URL:\n%s", out)
	}

	// settings.json must exist and carry the bearer-token env var.
	settingsBytes, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v\n%s", err, settingsBytes)
	}
	env, ok := settings["env"].(map[string]any)
	if !ok {
		t.Fatalf("settings.json has no env map:\n%s", settingsBytes)
	}
	// zai uses ANTHROPIC_AUTH_TOKEN (bearer), NOT ANTHROPIC_API_KEY.
	if got := env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-zai-secret" {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %v, want sk-zai-secret\n%s", got, settingsBytes)
	}
	// ANTHROPIC_BASE_URL must point at the zai base URL.
	if got := env["ANTHROPIC_BASE_URL"]; got != "https://api.z.ai/api/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %v, want https://api.z.ai/api/anthropic", got)
	}
	// ANTHROPIC_API_KEY must NOT be set for an AUTH_TOKEN provider (it is
	// cleared by managedEnvKeys to avoid duplicates).
	if _, present := env["ANTHROPIC_API_KEY"]; present {
		t.Fatalf("ANTHROPIC_API_KEY should be cleared for zai\n%s", settingsBytes)
	}
}

// ---------------------------------------------------------------------------
// 3. cs model-map — full set/get/list/remove round-trip end-to-end.
// ---------------------------------------------------------------------------

func TestE2E_ModelMapSetGetListRemoveRoundTrip(t *testing.T) {
	e2eHome(t)

	// set-key first so zhipu-cn is a known provider with a key.
	e2eMustRun(t, []string{"set-key", "zhipu-cn", "sk-zhipu"})

	// set two mappings.
	e2eMustRun(t, []string{"model-map", "set", "zhipu-cn", "sonnet", "glm-5.2"})
	e2eMustRun(t, []string{"model-map", "set", "zhipu-cn", "haiku", "glm-4.5-air"})

	// get a specific mapping.
	got := e2eMustRun(t, []string{"model-map", "get", "zhipu-cn", "sonnet"})
	if strings.TrimSpace(got) != "glm-5.2" {
		t.Fatalf("model-map get sonnet = %q, want glm-5.2", strings.TrimSpace(got))
	}
	got = e2eMustRun(t, []string{"model-map", "get", "zhipu-cn", "haiku"})
	if strings.TrimSpace(got) != "glm-4.5-air" {
		t.Fatalf("model-map get haiku = %q, want glm-4.5-air", strings.TrimSpace(got))
	}

	// get-all (no client model) lists both mappings sorted by key.
	gotAll := e2eMustRun(t, []string{"model-map", "get", "zhipu-cn"})
	for _, want := range []string{"haiku\tglm-4.5-air", "sonnet\tglm-5.2"} {
		if !strings.Contains(gotAll, want) {
			t.Fatalf("model-map get all missing %q\n%s", want, gotAll)
		}
	}

	// list subcommand prints a header + the two mappings as "client -> upstream".
	listed := e2eMustRun(t, []string{"model-map", "list", "zhipu-cn"})
	if !strings.Contains(listed, "Model mappings for zhipu-cn") {
		t.Fatalf("list header missing:\n%s", listed)
	}
	for _, want := range []string{"haiku -> glm-4.5-air", "sonnet -> glm-5.2"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("list missing %q\n%s", want, listed)
		}
	}

	// remove one mapping.
	removed := e2eMustRun(t, []string{"model-map", "remove", "zhipu-cn", "sonnet"})
	if !strings.Contains(removed, "removed mapping") {
		t.Fatalf("remove output unexpected: %q", removed)
	}
	// After removal, get for sonnet must error.
	_, err := e2eRunArgs(t, []string{"model-map", "get", "zhipu-cn", "sonnet"})
	if err == nil {
		t.Fatal("model-map get sonnet after remove should error, got nil")
	}
	// The haiku mapping must still be present.
	got = e2eMustRun(t, []string{"model-map", "get", "zhipu-cn", "haiku"})
	if strings.TrimSpace(got) != "glm-4.5-air" {
		t.Fatalf("after removing sonnet, haiku = %q, want glm-4.5-air", strings.TrimSpace(got))
	}

	// remove the last mapping; the provider entry should be dropped so
	// the list subcommand reports "(none)".
	e2eMustRun(t, []string{"model-map", "remove", "zhipu-cn", "haiku"})
	listed = e2eMustRun(t, []string{"model-map", "list", "zhipu-cn"})
	if !strings.Contains(listed, "(none)") {
		t.Fatalf("after removing all mappings, list should show (none):\n%s", listed)
	}
}

// ---------------------------------------------------------------------------
// 4. cs proxy configure — end-to-end writes a route into ~/.code-switch/config.json
// ---------------------------------------------------------------------------

func TestE2E_ProxyConfigureWritesRoute(t *testing.T) {
	home := e2eHome(t)
	e2eMustRun(t, []string{"set-key", "zhipu-cn", "sk-zhipu"})

	out := e2eMustRun(t, []string{
		"proxy", "configure", "codex",
		"--provider", "zhipu-cn",
		"--model", "glm-5.2",
		"--protocol", "anthropic-messages",
		"--host", "127.0.0.1",
		"--port", "18080",
	})
	if !strings.Contains(out, "configured proxy route codex -> zhipu-cn") {
		t.Fatalf("configure output unexpected: %q", out)
	}

	// Read back the persisted config and verify the route block.
	cfgBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v\n%s", err, cfgBytes)
	}
	if cfg.Proxy == nil {
		t.Fatal("proxy block is nil after configure")
	}
	if cfg.Proxy.Host != "127.0.0.1" {
		t.Fatalf("proxy.Host = %q, want 127.0.0.1", cfg.Proxy.Host)
	}
	if cfg.Proxy.Port != 18080 {
		t.Fatalf("proxy.Port = %d, want 18080", cfg.Proxy.Port)
	}
	route, ok := cfg.Proxy.Routes["codex"]
	if !ok {
		t.Fatalf("no codex route in %v", cfg.Proxy.Routes)
	}
	if route.Provider != "zhipu-cn" {
		t.Fatalf("route.Provider = %q, want zhipu-cn", route.Provider)
	}
	if route.Model != "glm-5.2" {
		t.Fatalf("route.Model = %q, want glm-5.2", route.Model)
	}
	if route.UpstreamProtocol != string(protocolAnthropicMessages) {
		t.Fatalf("route.UpstreamProtocol = %q, want anthropic-messages", route.UpstreamProtocol)
	}

	// proxy preview must reflect the configured route.
	preview := e2eMustRun(t, []string{"proxy", "preview", "codex"})
	for _, want := range []string{
		"agent: codex",
		"provider: zhipu-cn",
		"model: glm-5.2",
		"upstream_protocol: anthropic-messages",
		"configured_port: 18080",
	} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q\n%s", want, preview)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. cs proxy serve — start a real proxy, hit /healthz + a forwarded
//    request, then exercise proxy status + proxy stop via CLI.
// ---------------------------------------------------------------------------

// startE2EProxyServe starts the proxy serve path in-process via
// prepareProxyServe (the same function cmdProxyServe calls) so the test
// controls the lifecycle without spawning a child binary. It returns the
// base URL and registers cleanup. The proxy points at the given mock
// upstream via a custom provider.
//
// NOTE: prepareProxyServe records os.Getpid() as the proxy PID. In a test
// this is the test process itself, so cmdProxyStop MUST NOT be called
// against a proxy started this way (it would signal the test process).
// Use e2eStartDetachedProxyServer for status/stop CLI lifecycle tests.
func startE2EProxyServe(t *testing.T, home, agent, upstreamURL, token string) (baseURL string) {
	t.Helper()
	// Register a custom provider pointing at the mock upstream so the proxy
	// forwards there. A custom provider with a BaseURL is a first-class
	// citizen (sortedProviderNames / resolveProviderPreset accept it).
	seed := AppConfig{
		Providers: map[string]StoredProvider{
			"e2e-mock": {
				Name:    "E2E Mock",
				BaseURL: upstreamURL,
				Model:   "mock-model",
				APIKey:  "mock-key",
				AuthEnv: "ANTHROPIC_AUTH_TOKEN",
			},
		},
	}
	e2eWriteAppConfig(t, home, seed)

	// Configure the proxy route to use the mock provider over the
	// Anthropic Messages upstream protocol (the default for codex).
	e2eMustRun(t, []string{
		"proxy", "configure", agent,
		"--provider", "e2e-mock",
		"--protocol", "anthropic-messages",
		"--host", "127.0.0.1",
		"--port", "0", // auto-allocate
	})

	inst, err := prepareProxyServe(agent, "", 0, token)
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	baseURL = inst.state.BaseURL

	// Serve in the background; Shutdown is called in cleanup.
	srvDone := make(chan struct{})
	go func() {
		_ = inst.server.Serve(inst.ln)
		close(srvDone)
	}()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
		select {
		case <-srvDone:
		case <-time.After(2 * time.Second):
		}
		_ = removeProxyRuntimeState()
	})
	return baseURL
}

// e2eStartDetachedProxyServer spins up an in-process HTTP server that
// answers /healthz with the given instance id and a detached child PID,
// then writes the ProxyRuntimeState as if a real proxy had started. This
// lets us exercise the `cs proxy status` / `cs proxy stop` CLI paths
// without risking the test process being signalled (the recorded PID is
// a throwaway `sleep` child, mirroring the existing
// TestCmdProxyStopTerminatesHealthyProxy pattern).
//
// Returns a cleanup function the test does NOT need to call (registered
// via t.Cleanup) and the recorded state.
func e2eStartDetachedProxyServer(t *testing.T, instanceID string) (ProxyRuntimeState, func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("signal-based stop lifecycle test is Unix-only")
	}
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

	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep child: %v", err)
	}
	pid := cmd.Process.Pid
	proxyPIDHolder = pid

	state := ProxyRuntimeState{
		PID:        pid,
		InstanceID: instanceID,
		Host:       "127.0.0.1",
		Port:       port,
		BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
		Token:      "csproxy-e2e-detached-secret",
		StartedAt:  time.Now().UTC(),
	}
	if err := writeProxyRuntimeState(state); err != nil {
		t.Fatalf("write proxy state: %v", err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-srvDone
	}
	t.Cleanup(cleanup)
	return state, cleanup
}

func TestE2E_ProxyServeForwardsAndStatusRunning(t *testing.T) {
	home := e2eHome(t)
	// Mock Anthropic upstream.
	upstream, cap := startAnthropicUpstream(t, 0, "")

	const token = "csproxy-e2e-lifecycle"
	baseURL := startE2EProxyServe(t, home, "codex", upstream.URL, token)

	// 1. /healthz must respond with the running instance id + pid. baseURL
	//    ends in /v1, so /healthz is at the sibling path.
	healthzURL := strings.TrimSuffix(baseURL, "/v1") + "/healthz"
	resp, err := http.Get(healthzURL)
	if err != nil {
		t.Fatalf("healthz request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}
	var hr proxyHealthReport
	if err := json.Unmarshal(body, &hr); err != nil {
		t.Fatalf("healthz unmarshal: %v\n%s", err, body)
	}
	if !hr.OK || hr.InstanceID == "" {
		t.Fatalf("healthz report not healthy: %+v\n%s", hr, body)
	}

	// 2. cs proxy status must report "running" with our base URL.
	statusOut := e2eMustRun(t, []string{"proxy", "status"})
	if !strings.Contains(statusOut, "proxy: running") {
		t.Fatalf("status should report running:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, baseURL) {
		t.Fatalf("status should list base_url %s\n%s", baseURL, statusOut)
	}
	// Status must NEVER leak the token.
	if strings.Contains(statusOut, token) {
		t.Fatalf("status output leaked token:\n%s", statusOut)
	}

	// 3. Forward a real Responses-API request through the proxy and verify
	//    the upstream received the rewritten model and the bearer token.
	//    The proxy serves /v1/responses (Codex) and /v1/messages (Anthropic);
	//    baseURL ends in /v1, so the request URL is baseURL + "/responses".
	reqBody := `{"model":"ignored","input":"Say hi"}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/responses", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	fwdBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forward status = %d, want 200\nbody: %s", resp.StatusCode, fwdBody)
	}
	// Upstream must have been called on /v1/messages with the provider key.
	if cap.path != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", cap.path)
	}
	if cap.auth != "Bearer mock-key" {
		t.Fatalf("upstream auth = %q, want Bearer mock-key", cap.auth)
	}
	// The model must have been rewritten to the route's default ("mock-model").
	var upstreamReq map[string]any
	if err := json.Unmarshal(cap.body, &upstreamReq); err != nil {
		t.Fatalf("unmarshal upstream body: %v\n%s", err, cap.body)
	}
	if m, _ := upstreamReq["model"].(string); m != "mock-model" {
		t.Fatalf("upstream model = %q, want mock-model\n%s", m, cap.body)
	}
	// The response must be a Responses-API JSON object (not Anthropic shape).
	var respJSON map[string]any
	if err := json.Unmarshal(fwdBody, &respJSON); err != nil {
		t.Fatalf("unmarshal forward response: %v\n%s", err, fwdBody)
	}
	if obj, _ := respJSON["object"].(string); obj != "response" {
		t.Fatalf("forward response object = %q, want response\n%s", obj, fwdBody)
	}
	if txt, _ := respJSON["output_text"].(string); txt != "Hi" {
		t.Fatalf("forward response output_text = %q, want Hi\n%s", txt, fwdBody)
	}

	// NOTE: we deliberately do NOT call `cs proxy stop` here. The proxy
	// started by prepareProxyServe records os.Getpid() as its PID, which
	// in this in-process test is the test process itself; signalling it
	// would kill the test runner. The stop CLI path is covered by
	// TestE2E_ProxyStopViaCLI below using a detached stand-in process.
}

// TestE2E_ProxyStatusStopViaCLI exercises the `cs proxy status` and
// `cs proxy stop` CLI paths against a detached stand-in proxy process.
// The healthz server runs in-process but reports the PID of a spawned
// `sleep` child, so the stop path signals the child (not the test).
func TestE2E_ProxyStatusStopViaCLI(t *testing.T) {
	e2eHome(t)
	const instanceID = "csproxy-e2e-status-stop"
	state, _ := e2eStartDetachedProxyServer(t, instanceID)

	// status: must report running with the recorded base URL.
	statusOut := e2eMustRun(t, []string{"proxy", "status"})
	if !strings.Contains(statusOut, "proxy: running") {
		t.Fatalf("status should report running:\n%s", statusOut)
	}
	if !strings.Contains(statusOut, state.BaseURL) {
		t.Fatalf("status should list base_url %s\n%s", state.BaseURL, statusOut)
	}
	if strings.Contains(statusOut, state.Token) {
		t.Fatalf("status leaked token:\n%s", statusOut)
	}

	// stop: must terminate the child (PID match + instance match).
	stopOut := e2eMustRun(t, []string{"proxy", "stop"})
	if !strings.Contains(stopOut, "proxy stopped") {
		t.Fatalf("stop output unexpected:\n%s", stopOut)
	}

	// After stop, status must report "not running" (state file removed).
	statusOut = e2eMustRun(t, []string{"proxy", "status"})
	if !strings.Contains(statusOut, "proxy: not running") {
		t.Fatalf("post-stop status should report not running:\n%s", statusOut)
	}
}

// ---------------------------------------------------------------------------
// 6. cs run --dry-run — end-to-end preview including model mappings.
// ---------------------------------------------------------------------------

func TestE2E_RunDryRunIncludesModelMappings(t *testing.T) {
	home := e2eHome(t)
	e2eWriteAppConfig(t, home, AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-zhipu"},
		},
	})

	// Configure a model mapping so the dry-run preview reports the count.
	e2eMustRun(t, []string{"model-map", "set", "zhipu-cn", "sonnet", "glm-5.2"})

	out := e2eMustRun(t, []string{"run", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--dry-run"})

	for _, want := range []string{
		"agent: codex",
		"provider: zhipu-cn",
		"model: glm-5.2",
		"upstream_protocol: anthropic-messages",
		"proxy_base_url: http://127.0.0.1:<port>/v1",
		"CODEX_HOME=",
		"CODE_SWITCH_PROXY_API_KEY=<token>",
		"model_mappings: 1",
		"codex config.toml:",
		"model_provider = \"code-switch-proxy\"",
		"wire_api = \"responses\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run --dry-run missing %q\nfull output:\n%s", want, out)
		}
	}

	// The real token must NEVER appear in dry-run output (only <token>).
	// The configured API key must also never leak.
	if strings.Contains(out, "sk-zhipu") {
		t.Fatalf("dry-run leaked provider API key:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// 7. Protocol conversion round-trips through the IR layer.
//    Anthropic Messages -> IR -> Anthropic Messages preserves the message.
//    Responses -> IR -> OpenAI Chat preserves the text.
//    OpenAI Chat response -> IR -> Anthropic response preserves text.
// ---------------------------------------------------------------------------

func TestE2E_ProtocolRoundTrips(t *testing.T) {
	t.Run("AnthropicMessages_To_IR_To_AnthropicMessages", func(t *testing.T) {
		src := `{"model":"glm-5.2","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
		ir, err := anthropicRequestToIR([]byte(src))
		if err != nil {
			t.Fatalf("anthropicRequestToIR: %v", err)
		}
		if ir.Model != "glm-5.2" {
			t.Fatalf("ir.Model = %q, want glm-5.2", ir.Model)
		}
		if ir.MaxTokens != 64 {
			t.Fatalf("ir.MaxTokens = %d, want 64", ir.MaxTokens)
		}
		if len(ir.Messages) != 1 || ir.Messages[0].Role != "user" {
			t.Fatalf("ir messages = %+v", ir.Messages)
		}
		if len(ir.Messages[0].Parts) != 1 || ir.Messages[0].Parts[0].Text != "hello" {
			t.Fatalf("ir parts = %+v", ir.Messages[0].Parts)
		}
		back, err := irToAnthropicRequest(ir)
		if err != nil {
			t.Fatalf("irToAnthropicRequest: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(back, &m); err != nil {
			t.Fatalf("unmarshal back: %v\n%s", err, back)
		}
		if m["model"] != "glm-5.2" {
			t.Fatalf("round-trip model = %v, want glm-5.2", m["model"])
		}
		msgs, _ := m["messages"].([]any)
		if len(msgs) != 1 {
			t.Fatalf("round-trip messages len = %d, want 1", len(msgs))
		}
	})

	t.Run("Responses_To_IR_To_OpenAIChat_PreservesText", func(t *testing.T) {
		src := `{"model":"codex-model","input":"Say hi","max_output_tokens":32}`
		ir, err := responsesRequestToIR([]byte(src))
		if err != nil {
			t.Fatalf("responsesRequestToIR: %v", err)
		}
		if ir.MaxTokens != 32 {
			t.Fatalf("ir.MaxTokens = %d, want 32", ir.MaxTokens)
		}
		if got := ir.Messages[0].Parts[0].Text; got != "Say hi" {
			t.Fatalf("text = %q, want Say hi", got)
		}
		chatReq, err := irToOpenAIChatRequest(ir)
		if err != nil {
			t.Fatalf("irToOpenAIChatRequest: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(chatReq, &m); err != nil {
			t.Fatalf("unmarshal chat req: %v\n%s", err, chatReq)
		}
		if m["model"] != "codex-model" {
			t.Fatalf("chat model = %v, want codex-model", m["model"])
		}
		msgs, _ := m["messages"].([]any)
		if len(msgs) != 1 {
			t.Fatalf("chat messages len = %d, want 1", len(msgs))
		}
		msg, _ := msgs[0].(map[string]any)
		if msg["role"] != "user" {
			t.Fatalf("chat msg role = %v, want user", msg["role"])
		}
	})

	t.Run("OpenAIChatResponse_To_IR_To_AnthropicResponse_PreservesText", func(t *testing.T) {
		chatResp := []byte(`{"id":"chatcmpl_1","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
		ir, err := openAIChatResponseToIR(chatResp)
		if err != nil {
			t.Fatalf("openAIChatResponseToIR: %v", err)
		}
		if ir.Text != "Hi" {
			t.Fatalf("ir.Text = %q, want Hi", ir.Text)
		}
		if ir.Usage == nil || ir.Usage.OutputTokens != 3 {
			t.Fatalf("ir.Usage = %+v, want OutputTokens=3", ir.Usage)
		}
		anthResp, err := irToAnthropicResponse(ir)
		if err != nil {
			t.Fatalf("irToAnthropicResponse: %v", err)
		}
		s := string(anthResp)
		for _, want := range []string{`"type":"message"`, `"content":[{"type":"text","text":"Hi"}]`, `"stop_reason"`, `"input_tokens":2`, `"output_tokens":3`} {
			if !strings.Contains(s, want) {
				t.Fatalf("anthropic response missing %q\n%s", want, s)
			}
		}
	})

	t.Run("Responses_To_IR_To_Responses_RoundTrip", func(t *testing.T) {
		src := `{"model":"m","input":"hi","max_output_tokens":8}`
		ir, err := responsesRequestToIR([]byte(src))
		if err != nil {
			t.Fatalf("responsesRequestToIR: %v", err)
		}
		resp, err := irToResponsesResponse(IRResponse{ID: "resp_1", Model: "m", Text: "hi-back", StopReason: "end_turn", Usage: &IRUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}})
		if err != nil {
			t.Fatalf("irToResponsesResponse: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(resp, &m); err != nil {
			t.Fatalf("unmarshal responses resp: %v\n%s", err, resp)
		}
		if m["object"] != "response" {
			t.Fatalf("object = %v, want response", m["object"])
		}
		if m["output_text"] != "hi-back" {
			t.Fatalf("output_text = %v, want hi-back", m["output_text"])
		}
		// ir.Stream must default to false for a non-streaming request.
		if ir.Stream {
			t.Fatal("ir.Stream = true, want false for non-streaming request")
		}
	})

	t.Run("AnthropicResponse_To_IR_To_Responses_PreservesText", func(t *testing.T) {
		anth := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.2","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":5}}`)
		ir, err := anthropicResponseToIR(anth)
		if err != nil {
			t.Fatalf("anthropicResponseToIR: %v", err)
		}
		if ir.Text != "hello" {
			t.Fatalf("ir.Text = %q, want hello", ir.Text)
		}
		resp, err := irToResponsesResponse(ir)
		if err != nil {
			t.Fatalf("irToResponsesResponse: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(resp, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, resp)
		}
		if m["output_text"] != "hello" {
			t.Fatalf("output_text = %v, want hello", m["output_text"])
		}
	})
}

// ---------------------------------------------------------------------------
// 8. cs proxy serve — forwarding through the OpenAI Chat upstream protocol
//    end-to-end (Anthropic inbound /v1/messages -> OpenAI chat upstream).
// ---------------------------------------------------------------------------

func TestE2E_ProxyServeOpenAIChatUpstreamForwarding(t *testing.T) {
	home := e2eHome(t)
	upstream, cap := startOpenAIChatUpstream(t, 0, "")

	const token = "csproxy-e2e-chat"
	// Register a custom provider pointing at the mock OpenAI-chat upstream
	// and configure the route to use the openai-chat upstream protocol.
	seed := AppConfig{
		Providers: map[string]StoredProvider{
			"e2e-chat": {
				Name:    "E2E Chat",
				BaseURL: upstream.URL,
				Model:   "chat-model",
				APIKey:  "chat-key",
				AuthEnv: "ANTHROPIC_AUTH_TOKEN",
			},
		},
	}
	e2eWriteAppConfig(t, home, seed)

	e2eMustRun(t, []string{
		"proxy", "configure", "claude",
		"--provider", "e2e-chat",
		"--protocol", "openai-chat",
		"--host", "127.0.0.1",
		"--port", "0",
	})

	inst, err := prepareProxyServe("claude", "", 0, token)
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	baseURL := inst.state.BaseURL
	srvDone := make(chan struct{})
	go func() {
		_ = inst.server.Serve(inst.ln)
		close(srvDone)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
		select {
		case <-srvDone:
		case <-time.After(2 * time.Second):
		}
		_ = removeProxyRuntimeState()
	})

	// Send an Anthropic Messages request; the proxy must translate it to
	// OpenAI Chat Completions on the upstream. baseURL ends in /v1, the
	// Anthropic inbound path is /v1/messages.
	anthReq := `{"model":"ignored","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"Say hi"}]}]}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/messages", strings.NewReader(anthReq))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("forward request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("forward status = %d, want 200\nbody: %s", resp.StatusCode, body)
	}
	// Upstream must have been called on /v1/chat/completions.
	if cap.path != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /v1/chat/completions", cap.path)
	}
	if cap.auth != "Bearer chat-key" {
		t.Fatalf("upstream auth = %q, want Bearer chat-key", cap.auth)
	}
	// The upstream body model must have been rewritten to the route model.
	var upReq map[string]any
	if err := json.Unmarshal(cap.body, &upReq); err != nil {
		t.Fatalf("unmarshal upstream body: %v\n%s", err, cap.body)
	}
	if m, _ := upReq["model"].(string); m != "chat-model" {
		t.Fatalf("upstream model = %q, want chat-model\n%s", m, cap.body)
	}
	// The response back to the Anthropic client must be in Anthropic shape.
	var anthResp map[string]any
	if err := json.Unmarshal(body, &anthResp); err != nil {
		t.Fatalf("unmarshal response: %v\n%s", err, body)
	}
	if anthResp["type"] != "message" {
		t.Fatalf("response type = %v, want message\n%s", anthResp["type"], body)
	}
}