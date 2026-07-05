# TUI Proxy Manager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add TUI support for model mapping, quick model switching, and full local proxy start/stop/status management.

**Architecture:** Keep proxy lifecycle and configuration logic outside TUI in reusable helpers and CLI commands. TUI pages call the same helpers used by CLI, so behavior remains testable without terminal automation. Runtime proxy serving uses existing `newProxyHandler` and `ProxyRoute`, with a small process/state layer around it.

**Tech Stack:** Go 1.22, package `main`, `tview`/`tcell` for TUI, standard `net/http`, `os/exec`, config JSON under `~/.code-switch/`.

---

## File Structure

- Create `proxy_config.go`: persistent proxy config structs, defaults, route config helpers, route builder from app config.
- Create `proxy_lifecycle.go`: runtime state path, health check, start/stop/status helpers, `proxy serve` HTTP server.
- Create `proxy_cmd.go`: CLI command dispatch for `cs proxy configure/start/stop/status/preview/serve`.
- Create `proxy_config_test.go`: tests for proxy config, route builder, mapping precedence.
- Create `proxy_lifecycle_test.go`: tests for state handling, health checks, start/stop behavior where practical.
- Create `proxy_cmd_test.go`: tests for CLI behavior and output shape.
- Modify `presets.go`: add `Proxy ProxyConfig` to `AppConfig`.
- Modify `main.go`: register `proxy` command, help, version request, shell completion.
- Modify `tui.go`: add detail actions and pages for Use Model, Manage Model Mappings, Proxy Manager.
- Create or extend `tui_proxy_test.go`: tests for TUI helper behavior and action presence.
- Modify `model_cmd.go`: extract reusable helper functions if not already exported in package scope.
- Modify `run.go`: reuse new route builder for dry-run if it supersedes existing `buildProxyRoute`.

Do not commit during task execution unless the user explicitly asks. Use staged diffs only for review if needed.

---

### Task 1: Persistent Proxy Config and Route Builder

**Files:**
- Modify: `presets.go`
- Create: `proxy_config.go`
- Create: `proxy_config_test.go`
- Modify: `run.go`

- [ ] **Step 1: Write failing tests for config defaults and route mapping precedence**

Add to `proxy_config_test.go`:

```go
package main

import "testing"

func TestDefaultProxyConfig(t *testing.T) {
	cfg := defaultProxyConfig()
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("Host = %q, want 127.0.0.1", cfg.Host)
	}
	if cfg.Port != 0 {
		t.Fatalf("Port = %d, want 0", cfg.Port)
	}
}

func TestBuildProxyRouteFromConfigUsesRouteMappingsFirst(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"default": "global-model"},
		},
		Proxy: ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:            "codex",
					Provider:         "zhipu-cn",
					Model:            "glm-5.2",
					UpstreamProtocol: string(protocolAnthropicMessages),
					ModelMappings:    map[string]string{"default": "route-model"},
				},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "local-token")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig error: %v", err)
	}
	if route.Provider != "zhipu-cn" || route.Model != "glm-5.2" {
		t.Fatalf("route provider/model = %s/%s", route.Provider, route.Model)
	}
	if got := route.ModelMappings["default"]; got != "route-model" {
		t.Fatalf("default mapping = %q, want route-model", got)
	}
	cfg.Proxy.Routes["codex"].ModelMappings["default"] = "mutated"
	if got := route.ModelMappings["default"]; got != "route-model" {
		t.Fatalf("route mappings not defensively copied, got %q", got)
	}
}

func TestBuildProxyRouteFromConfigFallsBackToProviderMappings(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"default": "global-model"},
		},
		Proxy: ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {Agent: "codex", Provider: "zhipu-cn"},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "local-token")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig error: %v", err)
	}
	if route.Model != "glm-5.2" {
		t.Fatalf("route.Model = %q, want stored model", route.Model)
	}
	if got := route.ModelMappings["default"]; got != "global-model" {
		t.Fatalf("default mapping = %q, want global-model", got)
	}
}

func TestBuildProxyRouteFromConfigRejectsMissingRoute(t *testing.T) {
	_, err := buildProxyRouteFromConfig("codex", &AppConfig{Providers: map[string]StoredProvider{}}, "token")
	if err == nil {
		t.Fatal("expected missing route error")
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
source ~/.zshrc && go test -run 'TestDefaultProxyConfig|TestBuildProxyRouteFromConfig' .
```

Expected: compile failure for missing `ProxyConfig`, `ProxyRouteConfig`, `defaultProxyConfig`, or `buildProxyRouteFromConfig`.

- [ ] **Step 3: Implement config structs and route builder**

In `presets.go`, add to `AppConfig`:

```go
	Proxy        ProxyConfig              `json:"proxy,omitempty"`
```

Create `proxy_config.go`:

```go
package main

import (
	"fmt"
	"strings"
)

type ProxyConfig struct {
	Host   string                      `json:"host,omitempty"`
	Port   int                         `json:"port,omitempty"`
	Routes map[string]ProxyRouteConfig `json:"routes,omitempty"`
}

type ProxyRouteConfig struct {
	Agent            string            `json:"agent"`
	Provider         string            `json:"provider"`
	Model            string            `json:"model,omitempty"`
	UpstreamProtocol string            `json:"upstreamProtocol,omitempty"`
	ModelMappings    map[string]string `json:"modelMappings,omitempty"`
}

func defaultProxyConfig() ProxyConfig {
	return ProxyConfig{Host: "127.0.0.1", Port: 0}
}

func normalizeProxyConfig(cfg ProxyConfig) ProxyConfig {
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port < 0 {
		cfg.Port = 0
	}
	return cfg
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func buildProxyRouteFromConfig(agent string, cfg *AppConfig, localToken string) (ProxyRoute, error) {
	if cfg == nil {
		return ProxyRoute{}, fmt.Errorf("app config is nil")
	}
	agent = strings.TrimSpace(agent)
	rc, ok := cfg.Proxy.Routes[agent]
	if !ok {
		return ProxyRoute{}, fmt.Errorf("proxy route for agent %q is not configured", agent)
	}
	provider := canonicalProviderName(strings.TrimSpace(rc.Provider))
	if provider == "" {
		return ProxyRoute{}, fmt.Errorf("proxy route for agent %q has no provider", agent)
	}
	preset, err := resolveSwitchPreset(provider, cfg, strings.TrimSpace(rc.Model))
	if err != nil {
		return ProxyRoute{}, err
	}
	mappings := copyStringMap(rc.ModelMappings)
	if len(mappings) == 0 {
		mappings = copyStringMap(cfg.ModelMappings[provider])
	}
	protocol := ProxyProtocol(strings.TrimSpace(rc.UpstreamProtocol))
	if protocol == "" {
		protocol = protocolAnthropicMessages
	}
	return buildProxyRoute(provider, preset, cfg, protocol, localToken, mappings), nil
}
```

Update `run.go` helper signature so it accepts mappings:

```go
func buildProxyRoute(provider string, preset ProviderPreset, cfg *AppConfig, upstreamProtocol ProxyProtocol, localToken string, mappings map[string]string) ProxyRoute {
	return ProxyRoute{
		Provider:         provider,
		Model:            preset.Model,
		UpstreamProtocol: upstreamProtocol,
		UpstreamBaseURL:  preset.BaseURL,
		LocalToken:       localToken,
		ModelMappings:    copyStringMap(mappings),
	}
}
```

Update existing callers to pass `cfg.ModelMappings[provider]`.

- [ ] **Step 4: Run tests to verify GREEN**

Run:

```bash
source ~/.zshrc && go test -run 'TestDefaultProxyConfig|TestBuildProxyRouteFromConfig|TestBuildProxyRoute' .
```

Expected: PASS.

---

### Task 2: CLI Proxy Configure, Status, Preview

**Files:**
- Create: `proxy_cmd.go`
- Create: `proxy_cmd_test.go`
- Modify: `main.go`

- [ ] **Step 1: Write failing CLI tests**

Add to `proxy_cmd_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdProxyConfigureWritesRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	out.Reset()
	err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2", "--protocol", string(protocolAnthropicMessages), "--host", "127.0.0.1", "--port", "0"}, nil, &out)
	if err != nil {
		t.Fatalf("proxy configure error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	route := cfg.Proxy.Routes["codex"]
	if route.Provider != "zhipu-cn" || route.Model != "glm-5.2" || route.Agent != "codex" {
		t.Fatalf("route = %+v", route)
	}
}

func TestCmdProxyPreviewDoesNotLeakProviderKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-secret"}, nil, &out); err != nil {
		t.Fatalf("set-key error: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, &out); err != nil {
		t.Fatalf("proxy configure error: %v", err)
	}
	out.Reset()
	if err := runWithIO([]string{"proxy", "preview", "codex"}, nil, &out); err != nil {
		t.Fatalf("proxy preview error: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "agent: codex") || !strings.Contains(s, "provider: zhipu-cn") || !strings.Contains(s, "model: glm-5.2") {
		t.Fatalf("preview missing expected fields:\n%s", s)
	}
	if strings.Contains(s, "sk-secret") {
		t.Fatalf("preview leaked provider key:\n%s", s)
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
source ~/.zshrc && go test -run 'TestCmdProxyConfigure|TestCmdProxyPreview' .
```

Expected: FAIL with unknown command `proxy`.

- [ ] **Step 3: Implement `proxy` command dispatch**

Create `proxy_cmd.go`:

```go
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func cmdProxy(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: code-switch proxy <configure|start|stop|status|preview|serve> ...")
	}
	switch args[0] {
	case "configure":
		return cmdProxyConfigure(args[1:], out)
	case "preview":
		return cmdProxyPreview(args[1:], out)
	case "status":
		return cmdProxyStatus(args[1:], out)
	case "start":
		return cmdProxyStart(args[1:], out)
	case "stop":
		return cmdProxyStop(args[1:], out)
	case "serve":
		return cmdProxyServe(args[1:], out)
	default:
		return fmt.Errorf("unknown proxy subcommand %q (supported: configure, start, stop, status, preview, serve)", args[0])
	}
}

func cmdProxyConfigure(args []string, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: code-switch proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port]")
	}
	agent := strings.TrimSpace(args[0])
	fs := flag.NewFlagSet("proxy configure", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	providerFlag := fs.String("provider", "", "provider")
	modelFlag := fs.String("model", "", "model")
	protocolFlag := fs.String("protocol", string(protocolAnthropicMessages), "upstream protocol")
	hostFlag := fs.String("host", "127.0.0.1", "host")
	portFlag := fs.Int("port", 0, "port")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || strings.TrimSpace(*providerFlag) == "" {
		return errors.New("usage: code-switch proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port]")
	}
	provider := canonicalProviderName(*providerFlag)
	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	if cfg.Proxy.Routes == nil {
		cfg.Proxy.Routes = map[string]ProxyRouteConfig{}
	}
	cfg.Proxy.Host = strings.TrimSpace(*hostFlag)
	cfg.Proxy.Port = *portFlag
	cfg.Proxy = normalizeProxyConfig(cfg.Proxy)
	cfg.Proxy.Routes[agent] = ProxyRouteConfig{Agent: agent, Provider: provider, Model: strings.TrimSpace(*modelFlag), UpstreamProtocol: strings.TrimSpace(*protocolFlag)}
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "configured proxy route %s -> %s\n", agent, provider)
	return nil
}

func cmdProxyPreview(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: code-switch proxy preview <agent>")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	route, err := buildProxyRouteFromConfig(args[0], cfg, "<token>")
	if err != nil {
		return err
	}
	proxyCfg := normalizeProxyConfig(cfg.Proxy)
	fmt.Fprintf(out, "agent: %s\n", args[0])
	fmt.Fprintf(out, "provider: %s\n", route.Provider)
	fmt.Fprintf(out, "model: %s\n", route.Model)
	fmt.Fprintf(out, "upstream_protocol: %s\n", route.UpstreamProtocol)
	fmt.Fprintf(out, "proxy_base_url: http://%s:<port>/v1\n", proxyCfg.Host)
	fmt.Fprintf(out, "configured_port: %d\n", proxyCfg.Port)
	fmt.Fprintf(out, "model_mappings: %d\n", len(route.ModelMappings))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "codex config.toml:")
	fmt.Fprint(out, renderProxyCodexConfig(route.Model))
	return nil
}
```

Add temporary stubs in `proxy_cmd.go` so tests compile until lifecycle task:

```go
func cmdProxyStatus(args []string, out io.Writer) error { return errors.New("proxy status is not implemented") }
func cmdProxyStart(args []string, out io.Writer) error  { return errors.New("proxy start is not implemented") }
func cmdProxyStop(args []string, out io.Writer) error   { return errors.New("proxy stop is not implemented") }
func cmdProxyServe(args []string, out io.Writer) error  { return errors.New("proxy serve is not implemented") }
```

Modify `main.go`:

```go
	case "proxy":
		return cmdProxy(args[1:], out)
```

- [ ] **Step 4: Run tests to verify GREEN**

Run:

```bash
source ~/.zshrc && go test -run 'TestCmdProxyConfigure|TestCmdProxyPreview' .
```

Expected: PASS.

---

### Task 3: Proxy Lifecycle Serve, Start, Stop, Status

**Files:**
- Create: `proxy_lifecycle.go`
- Create: `proxy_lifecycle_test.go`
- Modify: `proxy_cmd.go`

- [ ] **Step 1: Write failing lifecycle tests**

Add to `proxy_lifecycle_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProxyStatePathUsesCodeSwitchDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := proxyStatePath()
	want := filepath.Join(home, ".code-switch", "proxy-state.json")
	if path != want {
		t.Fatalf("proxyStatePath = %q, want %q", path, want)
	}
}

func TestWriteReadProxyRuntimeState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := ProxyRuntimeState{PID: 123, Host: "127.0.0.1", Port: 456, BaseURL: "http://127.0.0.1:456/v1", Token: "csproxy-test", StartedAt: time.Unix(10, 0).UTC()}
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
	data, err := os.ReadFile(proxyStatePath())
	if err != nil {
		t.Fatalf("read raw state: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("state is not json: %v", err)
	}
}

func TestCmdProxyStatusReportsNotRunningWhenNoState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out strings.Builder
	err := cmdProxyStatus(nil, &out)
	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(out.String(), "not running") {
		t.Fatalf("status output = %q", out.String())
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
source ~/.zshrc && go test -run 'TestProxyStatePath|TestWriteReadProxyRuntimeState|TestCmdProxyStatusReportsNotRunning' .
```

Expected: compile failure for missing runtime state helpers.

- [ ] **Step 3: Implement state and status helpers**

Create `proxy_lifecycle.go`:

```go
package main

import (
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
	"strconv"
	"strings"
	"time"
)

type ProxyRuntimeState struct {
	PID       int       `json:"pid"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	BaseURL   string    `json:"baseURL"`
	Token     string    `json:"token"`
	StartedAt time.Time `json:"startedAt"`
}

func proxyStatePath() string {
	return filepath.Join(appConfigDir(), "proxy-state.json")
}

func writeProxyRuntimeState(state ProxyRuntimeState) error {
	return writeJSONAtomic(proxyStatePath(), state)
}

func readProxyRuntimeState() (ProxyRuntimeState, error) {
	data, err := os.ReadFile(proxyStatePath())
	if err != nil {
		return ProxyRuntimeState{}, err
	}
	var state ProxyRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return ProxyRuntimeState{}, err
	}
	return state, nil
}

func removeProxyRuntimeState() error {
	if err := os.Remove(proxyStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func proxyHealthURL(state ProxyRuntimeState) string {
	return fmt.Sprintf("http://%s:%d/healthz", state.Host, state.Port)
}

func checkProxyHealth(ctx context.Context, state ProxyRuntimeState) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyHealthURL(state), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

func cmdProxyStatus(args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: code-switch proxy status")
	}
	state, err := readProxyRuntimeState()
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "proxy: not running")
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := checkProxyHealth(ctx, state); err != nil {
		fmt.Fprintf(out, "proxy: stale state (pid %d, %s)\n", state.PID, err)
		return nil
	}
	fmt.Fprintf(out, "proxy: running\npid: %d\nbase_url: %s\nstarted_at: %s\n", state.PID, state.BaseURL, state.StartedAt.Format(time.RFC3339))
	return nil
}
```

- [ ] **Step 4: Implement serve/start/stop minimal lifecycle**

Add to `proxy_lifecycle.go`:

```go
func cmdProxyServe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("proxy serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", "codex", "agent route")
	hostFlag := fs.String("host", "", "host override")
	portFlag := fs.Int("port", -1, "port override")
	tokenFlag := fs.String("token", "", "local token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	token := strings.TrimSpace(*tokenFlag)
	if token == "" {
		return errors.New("--token is required")
	}
	route, err := buildProxyRouteFromConfig(*agentFlag, cfg, token)
	if err != nil {
		return err
	}
	proxyCfg := normalizeProxyConfig(cfg.Proxy)
	if strings.TrimSpace(*hostFlag) != "" {
		proxyCfg.Host = strings.TrimSpace(*hostFlag)
	}
	if *portFlag >= 0 {
		proxyCfg.Port = *portFlag
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(proxyCfg.Host, strconv.Itoa(proxyCfg.Port)))
	if err != nil {
		return err
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	state := ProxyRuntimeState{PID: os.Getpid(), Host: proxyCfg.Host, Port: actualPort, BaseURL: fmt.Sprintf("http://%s:%d/v1", proxyCfg.Host, actualPort), Token: token, StartedAt: time.Now().UTC()}
	if err := writeProxyRuntimeState(state); err != nil {
		_ = ln.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.Handle("/", newProxyHandler(route))
	fmt.Fprintf(out, "proxy listening on %s\n", state.BaseURL)
	server := &http.Server{Handler: mux}
	err = server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func cmdProxyStart(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("proxy start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", "codex", "agent route")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: code-switch proxy start [--agent agent]")
	}
	token, err := randomProxyToken()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "proxy", "serve", "--agent", *agentFlag, "--token", token)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	for i := 0; i < 30; i++ {
		state, err := readProxyRuntimeState()
		if err == nil && state.PID == cmd.Process.Pid {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			healthErr := checkProxyHealth(ctx, state)
			cancel()
			if healthErr == nil {
				fmt.Fprintf(out, "proxy started\npid: %d\nbase_url: %s\n", state.PID, state.BaseURL)
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("proxy process started with pid %d but did not become healthy", cmd.Process.Pid)
}

func cmdProxyStop(args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: code-switch proxy stop")
	}
	state, err := readProxyRuntimeState()
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "proxy: not running")
		return nil
	}
	if err != nil {
		return err
	}
	proc, err := os.FindProcess(state.PID)
	if err != nil {
		return err
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return err
	}
	if err := removeProxyRuntimeState(); err != nil {
		return err
	}
	fmt.Fprintf(out, "proxy stopped (pid %d)\n", state.PID)
	return nil
}
```

Add missing imports to `proxy_lifecycle.go`: `flag`.

- [ ] **Step 5: Run lifecycle tests**

Run:

```bash
source ~/.zshrc && go test -run 'TestProxyStatePath|TestWriteReadProxyRuntimeState|TestCmdProxyStatusReportsNotRunning' .
```

Expected: PASS.

---

### Task 4: Model and Mapping Helpers for TUI Reuse

**Files:**
- Modify: `model_cmd.go`
- Create: `tui_proxy_test.go`

- [ ] **Step 1: Write failing helper tests**

Add to `tui_proxy_test.go`:

```go
package main

import "testing"

func TestUseModelForProviderUpdatesModelAndDefaultMapping(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	if err := useModelForProvider(cfg, "zhipu-cn", "glm-5.2"); err != nil {
		t.Fatalf("useModelForProvider error: %v", err)
	}
	if cfg.Providers["zhipu-cn"].Model != "glm-5.2" {
		t.Fatalf("stored model = %q", cfg.Providers["zhipu-cn"].Model)
	}
	if cfg.ModelMappings["zhipu-cn"]["default"] != "glm-5.2" {
		t.Fatalf("default mapping = %q", cfg.ModelMappings["zhipu-cn"]["default"])
	}
}

func TestSetModelMappingForProvider(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	if err := setModelMappingForProvider(cfg, "zhipu-cn", "sonnet", "glm-5.2"); err != nil {
		t.Fatalf("set mapping error: %v", err)
	}
	if cfg.ModelMappings["zhipu-cn"]["sonnet"] != "glm-5.2" {
		t.Fatalf("mapping not set")
	}
	if err := removeModelMappingForProvider(cfg, "zhipu-cn", "sonnet"); err != nil {
		t.Fatalf("remove mapping error: %v", err)
	}
	if _, ok := cfg.ModelMappings["zhipu-cn"]; ok {
		t.Fatalf("empty provider mapping should be removed")
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
source ~/.zshrc && go test -run 'TestUseModelForProvider|TestSetModelMappingForProvider' .
```

Expected: compile failure for missing helper functions.

- [ ] **Step 3: Extract helper functions**

Add to `model_cmd.go` near existing helpers:

```go
func useModelForProvider(cfg *AppConfig, provider, model string) error {
	provider = canonicalProviderName(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return fmt.Errorf("provider and model must not be empty")
	}
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	if err := validateModelSelectionForProvider(provider, model, isPresetProvider(provider), preset); err != nil {
		return err
	}
	stored := cfg.Providers[provider]
	stored.Model = model
	cfg.Providers[provider] = stored
	mappings := ensureProviderModelMappings(cfg, provider)
	mappings["default"] = model
	return nil
}

func setModelMappingForProvider(cfg *AppConfig, provider, clientModel, upstreamModel string) error {
	provider = canonicalProviderName(strings.TrimSpace(provider))
	clientModel = strings.TrimSpace(clientModel)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if clientModel == "" || upstreamModel == "" {
		return fmt.Errorf("client-model and upstream-model must not be empty")
	}
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	ensureProviderModelMappings(cfg, provider)[clientModel] = upstreamModel
	return nil
}

func removeModelMappingForProvider(cfg *AppConfig, provider, clientModel string) error {
	provider = canonicalProviderName(strings.TrimSpace(provider))
	clientModel = strings.TrimSpace(clientModel)
	if clientModel == "" {
		return fmt.Errorf("client-model must not be empty")
	}
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	mappings := cfg.ModelMappings[provider]
	if mappings == nil {
		return fmt.Errorf("no model mappings for provider %q", provider)
	}
	if _, ok := mappings[clientModel]; !ok {
		return fmt.Errorf("no mapping for client model %q on provider %q", clientModel, provider)
	}
	delete(mappings, clientModel)
	if len(mappings) == 0 {
		delete(cfg.ModelMappings, provider)
	}
	return nil
}
```

Refactor `cmdUseModel`, `cmdModelMapSet`, and `cmdModelMapRemove` to call these helpers before `writeJSONAtomic`.

- [ ] **Step 4: Run tests to verify GREEN**

Run:

```bash
source ~/.zshrc && go test -run 'TestUseModelForProvider|TestSetModelMappingForProvider|TestCmdUseModel|TestCmdModelMap' .
```

Expected: PASS.

---

### Task 5: TUI Pages and Actions

**Files:**
- Modify: `tui.go`
- Modify: `tui_proxy_test.go`

- [ ] **Step 1: Write failing tests for action labels**

Add to `tui_proxy_test.go`:

```go
func TestProviderDetailProxyActions(t *testing.T) {
	actions := providerDetailActionLabels(false)
	for _, want := range []string{"Use Model", "Manage Model Mappings", "Proxy Manager"} {
		found := false
		for _, got := range actions {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing action %q in %#v", want, actions)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
source ~/.zshrc && go test -run 'TestProviderDetailProxyActions' .
```

Expected: compile failure for missing `providerDetailActionLabels` or missing labels.

- [ ] **Step 3: Add action labels helper and wire `showDetail`**

In `tui.go`, add:

```go
func providerDetailActionLabels(noModel bool) []string {
	labels := []string{}
	if !noModel {
		labels = append(labels, "Choose Model", "Use Model")
	}
	labels = append(labels, "Manage Model Mappings", "Proxy Manager", "Edit API Key", "Edit Tiers", "Back")
	return labels
}
```

Modify `showDetail()` action list construction to use this helper. Map callbacks:

```go
case "Use Model":
	ts.showUseModelForm(provider)
case "Manage Model Mappings":
	ts.showModelMappings(provider)
case "Proxy Manager":
	ts.showProxyManager(provider)
```

- [ ] **Step 4: Implement Use Model form**

Add to `tui.go`:

```go
func (ts *tuiState) showUseModelForm(provider string) {
	form := tview.NewForm()
	errLabel := tview.NewTextView().SetTextColor(tcell.ColorRed)
	model := ""
	form.AddInputField("Model", "", 50, nil, func(v string) { model = v })
	form.AddButton("Save", func() {
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil { errLabel.SetText(err.Error()); return }
		defer unlock()
		if err := useModelForProvider(cfg, provider, model); err != nil { errLabel.SetText(err.Error()); return }
		if err := writeJSONAtomic(path, cfg); err != nil { errLabel.SetText(err.Error()); return }
		ts.cfg = cfg
		ts.showDetail(provider)
	})
	form.AddButton("Cancel", func() { ts.showDetail(provider) })
	form.SetBorder(true).SetTitle("Use Model")
	panel := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(form, 0, 1, true).AddItem(errLabel, 1, 0, false)
	ts.pages.AddAndSwitchToPage("use-model", panel, true)
	ts.app.SetFocus(form)
}
```

Adjust formatting to match existing `tui.go` style and use existing page naming conventions.

- [ ] **Step 5: Implement Manage Model Mappings page**

Add to `tui.go`:

```go
func (ts *tuiState) showModelMappings(provider string) {
	list := tview.NewList().ShowSecondaryText(false)
	mappings := ts.cfg.ModelMappings[provider]
	keys := make([]string, 0, len(mappings))
	for k := range mappings { keys = append(keys, k) }
	sort.Strings(keys)
	for _, k := range keys { list.AddItem(fmt.Sprintf("%s -> %s", k, mappings[k]), "", 0, nil) }
	list.AddItem("Add / Update Mapping", "", 'a', func() { ts.showModelMappingForm(provider) })
	list.AddItem("Remove Mapping", "", 'r', func() { ts.showRemoveModelMappingForm(provider) })
	list.AddItem("Back", "", 'b', func() { ts.showDetail(provider) })
	list.SetBorder(true).SetTitle("Model Mappings")
	ts.pages.AddAndSwitchToPage("model-mappings", list, true)
	ts.app.SetFocus(list)
}
```

Add forms `showModelMappingForm(provider string)` and `showRemoveModelMappingForm(provider string)` using `setModelMappingForProvider` and `removeModelMappingForProvider` with locked config write.

- [ ] **Step 6: Implement Proxy Manager page skeleton**

Add to `tui.go`:

```go
func (ts *tuiState) showProxyManager(provider string) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("Configure Route", "", 'c', func() { ts.showProxyRouteForm(provider) })
	list.AddItem("Start Proxy", "", 's', func() { ts.showProxyActionResult(provider, "start") })
	list.AddItem("Stop Proxy", "", 'x', func() { ts.showProxyActionResult(provider, "stop") })
	list.AddItem("Status", "", 't', func() { ts.showProxyActionResult(provider, "status") })
	list.AddItem("Agent Config Preview", "", 'p', func() { ts.showProxyPreview(provider) })
	list.AddItem("Back", "", 'b', func() { ts.showDetail(provider) })
	list.SetBorder(true).SetTitle("Proxy Manager")
	ts.pages.AddAndSwitchToPage("proxy-manager", list, true)
	ts.app.SetFocus(list)
}
```

Implement forms and result pages by calling `cmdProxyConfigure`, `cmdProxyStart`, `cmdProxyStop`, `cmdProxyStatus`, and `cmdProxyPreview` into a `strings.Builder`, then displaying the output in a `tview.TextView` with a Back action.

- [ ] **Step 7: Run TUI tests**

Run:

```bash
source ~/.zshrc && go test -run 'TestProviderDetailProxyActions|TestUseModelForProvider|TestSetModelMappingForProvider' .
```

Expected: PASS.

---

### Task 6: Help, Completion, and End-to-End Verification

**Files:**
- Modify: `main.go`
- Modify: `proxy_cmd_test.go`

- [ ] **Step 1: Write failing help/completion tests**

Add to `proxy_cmd_test.go`:

```go
func TestHelpMentionsProxyCommand(t *testing.T) {
	var out bytes.Buffer
	if err := runWithIO([]string{"help"}, nil, &out); err != nil {
		t.Fatalf("help error: %v", err)
	}
	if !strings.Contains(out.String(), "cs proxy") {
		t.Fatalf("help missing proxy command:\n%s", out.String())
	}
}

func TestBashCompletionMentionsProxy(t *testing.T) {
	s := bashCompletionString()
	if !strings.Contains(s, "proxy") || !strings.Contains(s, "configure start stop status preview") {
		t.Fatalf("bash completion missing proxy support")
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```bash
source ~/.zshrc && go test -run 'TestHelpMentionsProxyCommand|TestBashCompletionMentionsProxy' .
```

Expected: FAIL until help/completion include proxy.

- [ ] **Step 3: Update `main.go` help and completions**

Add `proxy` to:

- `runWithIO`
- `isVersionRequest`
- bash completion top-level commands
- bash proxy subcommands: `configure start stop status preview`
- zsh command descriptions
- fish command descriptions and subcommands
- `printUsage`

Use usage lines:

```text
cs proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port]
cs proxy start [--agent agent]
cs proxy stop
cs proxy status
cs proxy preview <agent>
```

- [ ] **Step 4: Run targeted tests**

Run:

```bash
source ~/.zshrc && go test -run 'TestHelpMentionsProxyCommand|TestBashCompletionMentionsProxy|TestCmdProxy' .
```

Expected: PASS.

- [ ] **Step 5: Run full verification**

Run:

```bash
source ~/.zshrc && go vet ./... && go test -count=1 ./... && go build -o cs .
```

Expected: all commands exit 0.

- [ ] **Step 6: Manual CLI smoke test with isolated HOME**

Run:

```bash
tmp_home=$(mktemp -d "/tmp/opencode/cs-proxy-e2e.XXXXXX") && \
HOME="$tmp_home" ./cs set-key zhipu-cn sk-test >/tmp/opencode/cs-proxy-setkey.out && \
HOME="$tmp_home" ./cs use-model zhipu-cn glm-5.2 >/tmp/opencode/cs-proxy-use-model.out && \
HOME="$tmp_home" ./cs proxy configure codex --provider zhipu-cn --model glm-5.2 >/tmp/opencode/cs-proxy-configure.out && \
HOME="$tmp_home" ./cs proxy preview codex >/tmp/opencode/cs-proxy-preview.out && \
python3 - <<'PY' "$tmp_home"
import json, pathlib, sys
home=pathlib.Path(sys.argv[1])
cfg=json.loads((home/'.code-switch/config.json').read_text())
assert cfg['proxy']['routes']['codex']['provider']=='zhipu-cn', cfg
assert cfg['modelMappings']['zhipu-cn']['default']=='glm-5.2', cfg
preview=pathlib.Path('/tmp/opencode/cs-proxy-preview.out').read_text()
assert 'agent: codex' in preview, preview
assert 'provider: zhipu-cn' in preview, preview
assert 'sk-test' not in preview, preview
print('PROXY_TUI_CLI_E2E_OK')
PY
```

Expected: `PROXY_TUI_CLI_E2E_OK`.

---

## Self-Review Checklist

- Spec coverage: TUI model switching, mapping management, proxy route config, start/stop/status, preview, state file, stale handling, and tests all have tasks.
- Placeholder scan: no TBD/TODO/implement-later placeholders are present.
- Type consistency: `ProxyConfig`, `ProxyRouteConfig`, `ProxyRuntimeState`, `buildProxyRouteFromConfig`, and helper names are consistent across tasks.
- Scope: plan includes a real local proxy lifecycle but does not add multi-provider fallback, benchmarking, or real agent config mutation.
