package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProxyRuntimeState struct {
	PID        int       `json:"pid"`
	InstanceID string    `json:"instanceID,omitempty"`
	Host       string    `json:"host"`
	Port       int       `json:"port"`
	BaseURL    string    `json:"baseURL"`
	LogPath    string    `json:"logPath,omitempty"`
	Token      string    `json:"token"`
	StartedAt  time.Time `json:"startedAt"`
	RoutesHash string    `json:"routesHash,omitempty"`
}

type proxyServeInstance struct {
	server *http.Server
	ln     net.Listener
	logger *proxyLogger
	state  ProxyRuntimeState
}

func proxyStatePath() string {
	path, err := appConfigPath()
	if err != nil {
		return filepath.Join(".code-switch", "proxy-state.json")
	}
	return filepath.Join(filepath.Dir(path), "proxy-state.json")
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
		return ProxyRuntimeState{}, fmt.Errorf("corrupt proxy state: %w", err)
	}
	return state, nil
}

func removeProxyRuntimeState() error {
	if err := os.Remove(proxyStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// proxyRoutesHash computes a deterministic fingerprint of the proxy routes in
// the app config. It captures the agent, provider, model, upstream protocol,
// route token, and model-mappings of every route. Two configs that produce the
// same hash will serve identical routes at the daemon level, so the daemon does
// NOT need to be restarted when the hash matches.
func proxyRoutesHash(cfg *AppConfig) string {
	if cfg == nil || cfg.Proxy == nil || len(cfg.Proxy.Routes) == 0 {
		return ""
	}
	agents := sortedProxyRouteAgents(cfg.Proxy.Routes)
	var b strings.Builder
	fmt.Fprintf(&b, "host:%s|port:%d\n", cfg.Proxy.Host, cfg.Proxy.Port)
	for _, agent := range agents {
		route := cfg.Proxy.Routes[agent]
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|", agent, route.Provider, route.Model, route.UpstreamProtocol, route.Token)
		keys := make([]string, 0, len(route.ModelMappings))
		for k := range route.ModelMappings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%s;", k, route.ModelMappings[k])
		}
		if route.Fallback != nil {
			fmt.Fprintf(&b, "fallback:%s|%s|%s|%s|", route.Fallback.Provider, route.Fallback.Model, route.Fallback.UpstreamProtocol, route.Fallback.BaseURL)
		}
		b.WriteString("\n")
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}

func proxyHealthURL(state ProxyRuntimeState) string {
	// Use net.JoinHostPort so IPv6 hosts are bracketed. A naive
	// fmt.Sprintf("%s:%d", host, port) would produce a malformed URL for an
	// IPv6 host like "::1" (the colon in the address would be read as the
	// host/port separator).
	return "http://" + net.JoinHostPort(state.Host, strconv.Itoa(state.Port)) + "/healthz"
}

// proxyHealthReport is the parsed body of a healthy /healthz response. It
// carries the instance id and pid of the responding server so callers can
// verify they are talking to the proxy the state file points at (and not an
// unrelated process that happened to bind the same port and answer 200).
type proxyHealthReport struct {
	OK         bool   `json:"ok"`
	InstanceID string `json:"instanceID"`
	PID        int    `json:"pid"`
}

// checkProxyHealth probes the recorded state's /healthz endpoint and, on a
// 200 response, returns the parsed health report. Callers MUST compare the
// report's InstanceID/PID against the state before acting on the result:
// a 200 from an unrelated server that happens to bind the same port is NOT
// sufficient evidence that the recorded PID is the proxy.
//
// Returns an error if the request fails or the response is not 200 / not
// parseable. On error the report is the zero value.
func checkProxyHealth(ctx context.Context, state ProxyRuntimeState) (proxyHealthReport, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyHealthURL(state), nil)
	if err != nil {
		return proxyHealthReport{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return proxyHealthReport{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return proxyHealthReport{}, fmt.Errorf("health status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return proxyHealthReport{}, err
	}
	var report proxyHealthReport
	if err := json.Unmarshal(body, &report); err != nil {
		return proxyHealthReport{}, fmt.Errorf("health body parse: %w", err)
	}
	return report, nil
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
		// A corrupt state file (json parse failed) is never useful: the
		// PID is meaningless and no /healthz probe would match. Treat it
		// as "not running" and remove the file so the next status call
		// shows the clean output instead of re-reporting the parse error.
		fmt.Fprintf(out, "proxy: stale state removed (corrupt: %v)\n", err)
		_ = removeProxyRuntimeState()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	report, healthErr := checkProxyHealth(ctx, state)
	if healthErr != nil {
		fmt.Fprintf(out, "proxy: stale state (pid %d, %s)\n", state.PID, healthErr)
		return nil
	}
	if report.InstanceID != state.InstanceID {
		fmt.Fprintf(out, "proxy: stale state (pid %d, instance mismatch)\n", state.PID)
		return nil
	}
	// PID CHECK: even when the instance id matches, a pid mismatch means
	// the recorded live process is NOT the proxy that answered /healthz.
	// Report stale/mismatch rather than "running" so the operator knows
	// the persisted PID is not the live proxy (mirrors cmdProxyStop).
	if report.PID == 0 || report.PID != state.PID {
		fmt.Fprintf(out, "proxy: stale state (pid mismatch: recorded %d, health %d)\n", state.PID, report.PID)
		return nil
	}
	fmt.Fprintf(out, "proxy: running\npid: %d\nbase_url: %s\nstarted_at: %s\n", state.PID, state.BaseURL, state.StartedAt.Format(time.RFC3339))
	if cfg, _, cfgErr := loadAppConfig(); cfgErr == nil && cfg.Proxy != nil && len(cfg.Proxy.Routes) > 0 {
		fmt.Fprintln(out, "routes:")
		for _, agent := range sortedProxyRouteAgents(cfg.Proxy.Routes) {
			route := cfg.Proxy.Routes[agent]
			protocol, err := route.ResolveProtocol(defaultProtocolRegistry())
			if err != nil {
				protocol = ProviderProtocol(strings.TrimSpace(route.UpstreamProtocol))
			}
			fmt.Fprintf(out, "- agent: %s\n  provider: %s\n  protocol: %s\n  token: %s\n",
				agent, canonicalProviderName(route.Provider), protocol, maskProxyToken(route.Token))
		}
	}
	return nil
}

// resolveProxyToken returns the effective local proxy token. As of the
// security hardening pass, the token is read EXCLUSIVELY from the
// CODE_SWITCH_PROXY_TOKEN environment variable (set programmatically by
// `proxy start`); the legacy `--token` flag is rejected by cmdProxyServe
// so secrets never appear in argv (/proc/<pid>/cmdline, `ps`). The
// flagToken parameter is retained for backwards-compatibility with any
// out-of-process caller, but is intentionally IGNORED — only the env
// value is honored.
//
// Keeping the function (rather than inlining os.Getenv) preserves a
// unit-testable seam and a single source of truth for the precedence
// rules.
func resolveProxyToken(flagToken, envToken string) string {
	_ = flagToken // intentionally ignored; token must come from env
	return envToken
}

func cmdProxyServe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("proxy serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", "codex", "agent route")
	hostFlag := fs.String("host", "", "host override")
	portFlag := fs.Int("port", -1, "port override")
	tokenFlag := fs.String("token", "", "local token (deprecated: use "+proxyTokenEnvName+" env)")
	logFlag := fs.String("log", "", "JSONL request log path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: code-switch proxy serve [--agent agent] [--host host] [--port port] [--log path]")
	}
	// SECURITY: the local proxy token must NEVER be passed via argv. argv is
	// world-readable via /proc/<pid>/cmdline and `ps`, so an explicit --token
	// is rejected with a descriptive error that points the user at the env
	// var. The start path forwards the token via env only.
	if strings.TrimSpace(*tokenFlag) != "" {
		return fmt.Errorf("--token is no longer accepted (it leaks via argv); set the %s env var instead", proxyTokenEnvName)
	}
	// Validate host/port overrides before building the instance. Only
	// validate values that were explicitly provided on the command line;
	// the config/default fallbacks are validated inside prepareProxyServe.
	hostProvided := false
	portProvided := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "host":
			hostProvided = true
		case "port":
			portProvided = true
		}
	})
	if hostProvided {
		if err := validateProxyHost(*hostFlag); err != nil {
			return err
		}
	}
	if portProvided {
		if *portFlag < 0 || *portFlag > 65535 {
			return fmt.Errorf("--port %d out of range (must be 0..65535)", *portFlag)
		}
	}
	// Token resolution: env-only. The env is set programmatically by
	// `proxy start` and is the single source of truth.
	token := os.Getenv(proxyTokenEnvName)
	if strings.TrimSpace(token) == "" {
		return errors.New("proxy token is required (set " + proxyTokenEnvName + " env)")
	}
	// Do NOT pre-fill an empty --host here. prepareProxyServe is the single
	// source of truth for host resolution: it falls back to cfg.Proxy.Host
	// and only then to the 127.0.0.1 default. Pre-filling 127.0.0.1 in the
	// flag handler would shadow a host the user explicitly configured via
	// `proxy configure --host`, defeating the configured-host feature.
	logPath := strings.TrimSpace(*logFlag)
	if logPath == "" {
		logPath = strings.TrimSpace(os.Getenv(proxyLogEnvName))
	}
	inst, err := prepareProxyServe(strings.TrimSpace(*agentFlag), strings.TrimSpace(*hostFlag), *portFlag, token, logPath)
	if err != nil {
		return err
	}
	defer func() { _ = inst.logger.close() }()
	fmt.Fprintf(out, "proxy listening on %s\n", inst.state.BaseURL)
	if cfg, _, cfgErr := loadAppConfig(); cfgErr == nil && cfg.Proxy != nil && len(cfg.Proxy.Routes) > 0 {
		fmt.Fprintln(out, "routes:")
		for _, agent := range sortedProxyRouteAgents(cfg.Proxy.Routes) {
			route := cfg.Proxy.Routes[agent]
			protocol, err := route.ResolveProtocol(defaultProtocolRegistry())
			if err != nil {
				protocol = ProviderProtocol(strings.TrimSpace(route.UpstreamProtocol))
			}
			fmt.Fprintf(out, "- agent: %s provider: %s protocol: %s token: %s\n", agent, canonicalProviderName(route.Provider), protocol, maskProxyToken(route.Token))
		}
	}
	err = inst.server.Serve(inst.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func prepareProxyServe(agent, host string, port int, token string, logPaths ...string) (*proxyServeInstance, error) {
	logPath := ""
	if len(logPaths) > 0 {
		logPath = strings.TrimSpace(logPaths[0])
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("proxy token is required")
	}
	agent = strings.TrimSpace(agent)
	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return nil, err
	}
	defer unlock()
	changed, err := ensureProxyRouteTokens(cfg)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := writeJSONAtomic(path, cfg); err != nil {
			return nil, err
		}
	}
	routes, err := buildProxyServedRoutesFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	// Backward compatibility: existing callers/tests that start a daemon for a
	// specific agent still receive an instance token through the runtime state.
	// Keep that token usable for the requested agent while the persisted
	// per-agent route token remains the primary credential for new clients.
	if requestedAgent := strings.TrimSpace(agent); requestedAgent != "" {
		for _, route := range routes {
			if route.Agent == requestedAgent && route.Route.LocalToken != token {
				alias := route
				alias.Route.LocalToken = token
				routes = append(routes, alias)
				break
			}
		}
	}
	proxyCfg := ProxyConfig{Host: host, Port: port}
	if strings.TrimSpace(proxyCfg.Host) == "" {
		if cfg.Proxy != nil {
			proxyCfg.Host = cfg.Proxy.Host
		}
		if strings.TrimSpace(proxyCfg.Host) == "" {
			proxyCfg.Host = "127.0.0.1"
		}
	}
	if proxyCfg.Port < 0 {
		if cfg.Proxy != nil {
			proxyCfg.Port = cfg.Proxy.Port
		} else {
			proxyCfg.Port = 0
		}
	}
	proxyCfg = normalizeProxyConfig(proxyCfg)
	if err := rejectControlChars("host", proxyCfg.Host); err != nil {
		return nil, err
	}
	if err := validateProxyHost(proxyCfg.Host); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(proxyCfg.Host, strconv.Itoa(proxyCfg.Port)))
	if err != nil {
		return nil, err
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	// IPv6-safe hostport: bracket the host so the BaseURL is well-formed for
	// an IPv6 listen address like "::1". Mirrors proxyHealthURL.
	baseURL := "http://" + net.JoinHostPort(proxyCfg.Host, strconv.Itoa(actualPort)) + "/v1"
	// Generate a per-instance identity so `proxy stop`/`status` can verify
	// the responding /healthz server is THIS proxy and not an unrelated
	// process that happened to bind the same port. The id is non-sensitive
	// (it is sent unauthenticated to anyone probing /healthz), so it uses
	// the same crypto/rand source as the token for unpredictability.
	instanceID, err := randomProxyInstanceID()
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("generate instance id: %w", err)
	}
	pid := os.Getpid()
	state := ProxyRuntimeState{
		PID:        pid,
		InstanceID: instanceID,
		Host:       proxyCfg.Host,
		Port:       actualPort,
		BaseURL:    baseURL,
		Token:      token,
		StartedAt:  time.Now().UTC(),
		RoutesHash: proxyRoutesHash(cfg),
		LogPath:    logPath,
	}
	logger, err := newProxyLogger(logPath)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Echo the instance id and pid so callers can verify they are
		// talking to the proxy they think they are. The token is NEVER
		// included.
		body, _ := json.Marshal(proxyHealthReport{OK: true, InstanceID: instanceID, PID: pid})
		_, _ = w.Write(body)
	})
	mux.Handle("/", newProxyMultiRouteHandlerWithLogger(routes, defaultProtocolRegistry(), logger))
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       65 * time.Second,
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := writeProxyRuntimeState(state); err != nil {
		_ = ln.Close()
		_ = logger.close()
		// server is not yet serving so Close is a no-op, but call it for
		// hygiene in case a future change wires the listener before this
		// point.
		_ = server.Close()
		return nil, err
	}
	return &proxyServeInstance{server: server, ln: ln, logger: logger, state: state}, nil
}

func sortedProxyRouteAgents(routes map[string]ProxyRouteConfig) []string {
	agents := make([]string, 0, len(routes))
	for agent := range routes {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	return agents
}

func buildProxyServedRoutesFromConfig(cfg *AppConfig) ([]proxyServedRoute, error) {
	if cfg == nil || cfg.Proxy == nil || len(cfg.Proxy.Routes) == 0 {
		return nil, errors.New("no proxy routes configured")
	}
	routes := make([]proxyServedRoute, 0, len(cfg.Proxy.Routes))
	for _, agent := range sortedProxyRouteAgents(cfg.Proxy.Routes) {
		persisted := cfg.Proxy.Routes[agent]
		persisted.Agent = agent
		if err := validateProxyRouteHasAPIKey(persisted, cfg); err != nil {
			return nil, err
		}
		token := strings.TrimSpace(persisted.Token)
		if token == "" {
			return nil, fmt.Errorf("proxy route for agent %q has no token", agent)
		}
		route, err := buildProxyRouteFromConfig(agent, cfg, token)
		if err != nil {
			return nil, err
		}
		profile, ok := agentProfiles[AgentName(agent)]
		if !ok {
			return nil, fmt.Errorf("unsupported agent %q", agent)
		}
		routes = append(routes, proxyServedRoute{
			Agent:          agent,
			ClientProtocol: profile.ClientProtocol,
			Route:          route,
			ProviderKey:    storedAPIKeyForAgent(cfg, AgentName(agent), route.Provider),
		})
	}
	return routes, nil
}

func maskProxyToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "<missing>"
	}
	if len(token) <= 12 {
		return token[:1] + "…" + token[len(token)-1:]
	}
	return token[:8] + "…" + token[len(token)-4:]
}

// randomProxyInstanceID returns a random non-sensitive identifier of the
// form "csproxy-inst-<16 hex chars>". It uses crypto/rand so the id is
// unpredictable even though it is shared over /healthz without
// authentication; this prevents a third party from guessing it and
// spoofing a healthy response on a hijacked port.
func randomProxyInstanceID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "csproxy-inst-" + hex.EncodeToString(buf[:]), nil
}

// proxyServeExec is the executable-spawn hook used by cmdProxyStart. It
// returns an *exec.Cmd whose Process has been started (cmd.Start already
// called). The default implementation re-execs the current binary as
// `cs proxy serve`; tests replace it with a stand-in so the start path can be
// exercised without spawning the real test binary. Centralizing the spawn in a
// single injectable variable keeps the production code path the source of
// truth and avoids any parallel "test-only" start function.
var proxyServeExec = defaultProxyServeExec
var proxyStartLogPath string

// proxyTokenEnvName is the name of the environment variable used to forward
// the local proxy token from `proxy start` to the spawned `proxy serve` child.
// Passing the token via env (rather than argv) keeps the secret out of
// /proc/<pid>/cmdline and `ps` output, which are world-readable on most
// systems. cmdProxyServe reads this env var as a fallback when --token is not
// explicitly passed on the command line.
const proxyTokenEnvName = "CODE_SWITCH_PROXY_TOKEN"
const proxyLogEnvName = "CODE_SWITCH_PROXY_LOG"

// proxyTokenEnv returns the environment variable name carrying the proxy
// token from start to serve. It is a function rather than a direct constant
// reference at call sites so tests can assert on it uniformly.
func proxyTokenEnv() string { return proxyTokenEnvName }

// buildProxyServeCommand constructs the *exec.Cmd that defaultProxyServeExec
// spawns. It is split out so tests can inspect the argv and env WITHOUT
// having to spawn (and reap) the real child. The argv deliberately OMITS the
// token: the token is forwarded ONLY via the CODE_SWITCH_PROXY_TOKEN env var
// so it never appears in /proc/<pid>/cmdline or `ps`.
//
// Returns the cmd (with Env set, Process NOT started) so the caller can call
// Start() at the chosen moment.
func buildProxyServeCommand(exe, agent, token string, logPaths ...string) *exec.Cmd {
	logPath := ""
	if len(logPaths) > 0 {
		logPath = strings.TrimSpace(logPaths[0])
	}
	cmd := exec.Command(exe, "proxy", "serve", "--agent", agent)
	// Forward the token via env, never via argv. The child inherits the
	// parent environment (HOME, etc.) plus the token, but with one
	// exception: an inherited CODE_SWITCH_PROXY_TOKEN is STRIPPED first
	// so the fresh value we append is the only one the child sees. Without
	// this, an attacker who controls the parent shell could set
	// CODE_SWITCH_PROXY_TOKEN=<their-key> and have the proxy accept
	// requests signed with that key, bypassing the freshly-generated
	// token from `cs proxy start`.
	inherited := os.Environ()
	cleanInherited := inherited[:0:0]
	for _, kv := range inherited {
		if strings.HasPrefix(kv, proxyTokenEnvName+"=") {
			continue
		}
		if strings.HasPrefix(kv, proxyLogEnvName+"=") {
			continue
		}
		cleanInherited = append(cleanInherited, kv)
	}
	cmd.Env = append(cleanInherited, proxyTokenEnvName+"="+token)
	if strings.TrimSpace(logPath) != "" {
		cmd.Env = append(cmd.Env, proxyLogEnvName+"="+strings.TrimSpace(logPath))
	}
	return cmd
}

// defaultProxyServeExec spawns `cs proxy serve` as a detached child for the
// recorded agent, using a freshly generated local token. The token is passed
// via the CODE_SWITCH_PROXY_TOKEN environment variable (NOT argv) so it does
// not leak into /proc/<pid>/cmdline or `ps` output. The child inherits the
// parent's environment so it resolves the same HOME and finds the same app
// config / proxy route.
func defaultProxyServeExec(agent, token string) (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := buildProxyServeCommand(exe, agent, token, proxyStartLogPath)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// cleanupOrphanedProxy is invoked when the started proxy child fails to
// become healthy. It terminates the child process and removes any state the
// child may have written before failing, so we never leak an orphan process
// or a stale state file that would later confuse `proxy status`/`stop`.
//
// Note: this unconditionally removes the state file the child wrote. The
// caller (cmdProxyStart) is responsible for RESTORING any pre-existing state
// that the child overwrote — see snapshotProxyRuntimeState. The split keeps
// this function focused on "kill the orphan + clear the child's state" while
// the caller owns the "preserve the prior proxy" concern.
func cleanupOrphanedProxy(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		terminateProxyProcess(cmd.Process.Pid)
		// Best-effort reap; terminateProxyProcess already sent the
		// signals, so Wait just reaps the zombie. We bound it to avoid
		// hanging a test if the child ignores everything.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	_ = removeProxyRuntimeState()
}

func cmdProxyStart(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("proxy start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", "codex", "agent route")
	logFlag := fs.String("log", "", "JSONL request log path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: code-switch proxy start [--agent agent] [--log path]")
	}
	token, err := randomProxyToken()
	if err != nil {
		return err
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	agent := strings.TrimSpace(*agentFlag)
	if _, err := buildProxyRouteFromConfig(agent, cfg, token); err != nil {
		return err
	}
	// PRE-SPAWN API KEY CHECK: a provider that requires a key (preset
	// NoAPIKey == false) MUST have a non-empty key in the app config
	// before we spawn the child. Without this pre-check the child would
	// spawn, fail inside prepareProxyServe, and force the cleanup path
	// to run — wasting a process and complicating the already-running
	// guard. The check mirrors prepareProxyServe exactly (via the shared
	// validateProxyRouteHasAPIKey helper) so the start path and the serve
	// path agree on what "configured" means.
	//
	// The persisted ProxyRouteConfig is read straight from cfg.Proxy.Routes
	// (the helper takes the persisted shape, not the resolved ProxyRoute).
	// If the route is somehow missing after the build above succeeded, we
	// fall through and let the child surface the error.
	if cfg.Proxy != nil && cfg.Proxy.Routes != nil {
		if persistedRC, ok := cfg.Proxy.Routes[agent]; ok {
			if err := validateProxyRouteHasAPIKey(persistedRC, cfg); err != nil {
				return err
			}
		}
	}
	// ALREADY-RUNNING GUARD: if a proxy is already healthy on this machine
	// (a state file exists whose recorded instance id matches the
	// /healthz-responding server's instance id), do NOT spawn a second
	// proxy. Spawning would either fail to bind (port in use) or — worse —
	// succeed on a different port and clobber the shared state file,
	// invalidating the original proxy. Report already-running with the
	// existing base_url and return success.
	if existingState, existErr := readProxyRuntimeState(); existErr == nil && existingState.InstanceID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		report, healthErr := checkProxyHealth(ctx, existingState)
		cancel()
		if healthErr == nil && report.InstanceID == existingState.InstanceID {
			freshCfg, _, cfgErr := loadAppConfig()
			if cfgErr != nil {
				return cfgErr
			}
			currentHash := proxyRoutesHash(freshCfg)
			if currentHash != "" && existingState.RoutesHash != "" && currentHash != existingState.RoutesHash {
				if existingState.PID > 0 && report.PID == existingState.PID && existingState.PID != os.Getpid() {
					terminateProxyProcess(existingState.PID)
				}
				if err := removeProxyRuntimeState(); err != nil {
					return err
				}
			} else {
				if err := refreshProxyClientConfigs(existingState, freshCfg); err != nil {
					return err
				}
				fmt.Fprintf(out, "proxy already running\npid: %d\nbase_url: %s\n", existingState.PID, existingState.BaseURL)
				return nil
			}
		}
	}
	// Snapshot any pre-existing state BEFORE spawning the child. The child
	// will overwrite proxy-state.json with its own values; if the spawn
	// fails to become healthy, we must RESTORE the pre-existing state so a
	// failed `start` does not silently invalidate a previously-healthy
	// proxy. Without this, `proxy start` against a host that already runs a
	// healthy proxy would (on failure) leave no state at all — `proxy
	// status` would report "not running" even though the original proxy is
	// still serving.
	preExistingState, preExistingHadState := snapshotProxyRuntimeState()
	previousLogPath := proxyStartLogPath
	proxyStartLogPath = strings.TrimSpace(*logFlag)
	cmd, err := proxyServeExec(agent, token)
	proxyStartLogPath = previousLogPath
	if err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		state, err := readProxyRuntimeState()
		if err == nil && state.PID == cmd.Process.Pid && state.InstanceID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			report, healthErr := checkProxyHealth(ctx, state)
			cancel()
			if healthErr == nil && report.InstanceID == state.InstanceID {
				freshCfg, _, cfgErr := loadAppConfig()
				if cfgErr != nil {
					return cfgErr
				}
				if err := refreshProxyClientConfigs(state, freshCfg); err != nil {
					return err
				}
				fmt.Fprintf(out, "proxy started\npid: %d\nbase_url: %s\n", state.PID, state.BaseURL)
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The child started but never became healthy. We MUST clean up: kill
	// the orphaned process. State handling:
	//   - cleanupOrphanedProxy removes whatever state the child wrote.
	//   - If there was a pre-existing state (snapshotProxyRuntimeState), we
	//     RESTORE it so a previously-healthy proxy is not silently
	//     invalidated by a failed `start` against the same machine.
	cleanupOrphanedProxy(cmd)
	if preExistingHadState {
		if err := writeProxyRuntimeState(preExistingState); err != nil {
			return fmt.Errorf("proxy process started with pid %d but did not become healthy (also failed to restore pre-existing state: %v)", cmd.Process.Pid, err)
		}
	}
	return fmt.Errorf("proxy process started with pid %d but did not become healthy", cmd.Process.Pid)
}

// snapshotProxyRuntimeState captures the current proxy state file (if any) so
// it can be restored later if a `proxy start` attempt fails. The bool return
// is false when no state file exists (so callers can distinguish "no prior
// state" from "prior state was the zero value"). A read error other than
// os.ErrNotExist is treated as "no state" since we cannot meaningfully
// restore a state we could not read.
func snapshotProxyRuntimeState() (ProxyRuntimeState, bool) {
	st, err := readProxyRuntimeState()
	if err != nil {
		return ProxyRuntimeState{}, false
	}
	return st, true
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
	// HARD REFUSAL: never signal pid <= 0. A state file with a non-positive
	// PID is either corrupt or hand-edited; signaling pid 0 (which on Unix
	// means "the calling process group") or a negative pid would be
	// catastrophic. Treat such state as stale, remove the file, and report
	// without ever touching terminateProxyProcess.
	if state.PID <= 0 {
		if err := removeProxyRuntimeState(); err != nil {
			return err
		}
		fmt.Fprintf(out, "proxy stale state removed (invalid pid %d)\n", state.PID)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	report, healthErr := checkProxyHealth(ctx, state)
	cancel()
	if healthErr != nil {
		// STALE state: nothing is serving /healthz for the recorded PID.
		// The recorded PID may belong to an unrelated live process now
		// (PID was recycled, or this is a leftover state file from a
		// crashed proxy). We MUST NOT kill the recorded PID in this
		// branch — doing so risks terminating an innocent process.
		// The correct action is to remove the stale state file and report
		// "stale" so the operator knows the previous proxy is gone.
		if err := removeProxyRuntimeState(); err != nil {
			return err
		}
		fmt.Fprintf(out, "proxy stopped stale state removed (pid %d)\n", state.PID)
		return nil
	}
	// INSTANCE + PID IDENTITY CHECK: /healthz responded 200, but that
	// ALONE is not sufficient evidence that the responding server is our
	// proxy AND that the recorded PID is the proxy. Two independent
	// checks are required, and BOTH must pass before any signal is sent:
	//
	//   1. The health-report InstanceID must be non-empty AND match the
	//      state's InstanceID. If the real proxy died, the kernel could
	//      hand its port to an unrelated process that happens to answer
	//      200 on /healthz; the instance id is the cryptographic tie
	//      between "the server answering" and "the proxy we started".
	//
	//   2. The health-report PID must be non-zero AND match the state's
	//      PID. Even when the instance id matches, a pid mismatch means
	//      the recorded live process is NOT the proxy that answered
	//      /healthz (e.g. an inconsistent state file, a recycled PID, or
	//      a concurrent proxy that overwrote the state). Killing the
	//      recorded PID in that case risks terminating an innocent
	//      bystander.
	//
	// If EITHER check fails, we treat the state as stale/mismatch and
	// remove the file WITHOUT killing the recorded PID — that PID is
	// almost certainly an innocent bystander.
	if report.InstanceID == "" || report.InstanceID != state.InstanceID {
		if err := removeProxyRuntimeState(); err != nil {
			return err
		}
		fmt.Fprintf(out, "proxy stale state removed (instance mismatch, pid %d)\n", state.PID)
		return nil
	}
	if report.PID == 0 || report.PID != state.PID {
		if err := removeProxyRuntimeState(); err != nil {
			return err
		}
		fmt.Fprintf(out, "proxy stale state removed (pid mismatch: recorded %d, health %d)\n", state.PID, report.PID)
		return nil
	}
	// RUNNING + VERIFIED: /healthz responded 200 AND the instance id
	// matches the state file AND the reported pid matches the recorded
	// pid. This is strong evidence that the recorded PID is still our
	// proxy. We terminate it.
	terminateProxyProcess(state.PID)
	if err := removeProxyRuntimeState(); err != nil {
		return err
	}
	fmt.Fprintf(out, "proxy stopped (pid %d)\n", state.PID)
	return nil
}

// validateProxyRouteHasAPIKey mirrors the API-key enforcement that
// prepareProxyServe runs, but without binding a listener or building the
// runtime route. It lets callers (notably cmdProxyStart) fail FAST — before
// spawning a child — when the configured provider requires a key but none is
// set in the app config. Without this pre-check, `proxy start` would spawn a
// child that itself only fails inside prepareProxyServe, wasting a process
// and complicating cleanup.
//
// Behavior:
//   - unknown provider -> descriptive error (so a misconfigured route surfaces
//     at start time rather than at child-serve time).
//   - preset with NoAPIKey==true (e.g. ollama talking to a local daemon) ->
//     nil; no key is required.
//   - any other preset -> error mentioning "API key" when the stored key is
//     empty/whitespace.
//
// The function reads cfg directly (no I/O) so it is cheap and side-effect
// free. Callers that already hold the *AppConfig can pass it through; callers
// that need a fresh snapshot should call loadAppConfig themselves.
func validateProxyRouteHasAPIKey(route ProxyRouteConfig, cfg *AppConfig) error {
	if cfg == nil {
		return fmt.Errorf("app config is nil")
	}
	provider := canonicalProviderName(strings.TrimSpace(route.Provider))
	if provider == "" {
		return fmt.Errorf("proxy route for agent %q has no provider", route.Agent)
	}
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q", provider)
	}
	if !preset.NoAPIKey && strings.TrimSpace(storedAPIKeyForAgent(cfg, AgentName(route.Agent), provider)) == "" {
		return fmt.Errorf("provider %q has no API key configured; run `cs set-key %s <key>` before starting the proxy", provider, provider)
	}
	return nil
}

func terminateProxyProcess(pid int) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		_ = proc.Kill()
		return
	}
	// For tests and for shells that ignore SIGINT, terminate promptly. This is
	// intentionally best-effort; state cleanup is handled by the caller.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = proc.Kill()
	}()
}
