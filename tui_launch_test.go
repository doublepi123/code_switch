package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentBuildsDirectEnvironmentForEachAgent(t *testing.T) {
	cases := []struct {
		name     string
		agent    AgentName
		provider string
		key      string
		want     map[string]string
	}{
		{
			name:     "claude anthropic",
			agent:    agentClaude,
			provider: "minimax-cn",
			key:      "sk-claude",
			want: map[string]string{
				"ANTHROPIC_BASE_URL":   "https://api.minimaxi.com/anthropic",
				"ANTHROPIC_AUTH_TOKEN": "sk-claude",
				"ANTHROPIC_MODEL":      "MiniMax-M3",
			},
		},
		{
			name:     "codex openai compatible",
			agent:    agentCodex,
			provider: "openrouter",
			key:      "sk-codex",
			want: map[string]string{
				"OPENAI_BASE_URL": "https://openrouter.ai/api/v1",
				"OPENAI_API_KEY":  "sk-codex",
				"OPENAI_MODEL":    "anthropic/claude-sonnet-4.6",
			},
		},
		{
			name:     "opencode openai compatible",
			agent:    agentOpencode,
			provider: "openrouter",
			key:      "sk-open",
			want: map[string]string{
				"OPENAI_BASE_URL": "https://openrouter.ai/api/v1",
				"OPENAI_API_KEY":  "sk-open",
				"OPENAI_MODEL":    "anthropic/claude-sonnet-4.6",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &AppConfig{Providers: map[string]StoredProvider{tc.provider: {APIKey: tc.key}}}
			preset, err := resolveAgentSwitchPreset(tc.agent, tc.provider, cfg, "")
			if err != nil {
				t.Fatalf("resolve preset: %v", err)
			}
			plan, err := resolveConnection(tc.agent, tc.provider, preset, "direct")
			if err != nil {
				t.Fatalf("resolve direct plan: %v", err)
			}
			plan = adjustLaunchConnectionPlan(tc.agent, plan, preset)
			pairs, err := launchEnvPairs(tc.agent, preset, plan, tc.key)
			if err != nil {
				t.Fatalf("launchEnvPairs: %v", err)
			}
			got := envPairsToMap(pairs)
			for key, want := range tc.want {
				if got[key] != want {
					t.Fatalf("%s=%q, want %q (all env: %#v)", key, got[key], want, got)
				}
			}
		})
	}
}

func TestLaunchAgentProxyRouteIsStartedAndCleanedUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := AppConfig{Providers: map[string]StoredProvider{"minimax-cn": {APIKey: "sk-secret"}}}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	started := false
	oldRunning, oldStart, oldStop := proxyDaemonIsRunning, startProxyDaemon, stopProxyDaemon
	proxyDaemonIsRunning = func(cfg *AppConfig) (bool, bool, error) { return false, false, nil }
	startProxyDaemon = func(cfg *AppConfig) error {
		started = true
		return writeProxyRuntimeState(ProxyRuntimeState{Host: "127.0.0.1", Port: 18080, PID: os.Getpid(), InstanceID: "test", RoutesHash: proxyRoutesHash(cfg)})
	}
	stopProxyDaemon = func() error { return removeProxyRuntimeState() }
	defer func() {
		proxyDaemonIsRunning = oldRunning
		startProxyDaemon = oldStart
		stopProxyDaemon = oldStop
		_ = removeProxyRuntimeState()
	}()

	oldLookPath := lookPath
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	defer func() { lookPath = oldLookPath }()

	var launched launchInvocation
	oldLaunch := launchCommand
	launchCommand = func(inv launchInvocation) error {
		launched = inv
		return nil
	}
	defer func() { launchCommand = oldLaunch }()

	if err := launchAgent(agentCodex, "minimax-cn", "", "", &bytes.Buffer{}); err != nil {
		t.Fatalf("launchAgent: %v", err)
	}
	if !started {
		t.Fatalf("expected proxy daemon to be auto-started")
	}
	env := envSliceToMap(launched.Env)
	if env["OPENAI_BASE_URL"] != "http://127.0.0.1:18080/v1" {
		t.Fatalf("OPENAI_BASE_URL=%q", env["OPENAI_BASE_URL"])
	}
	if !strings.HasPrefix(env["OPENAI_API_KEY"], "csproxy-route-") {
		t.Fatalf("OPENAI_API_KEY should be route token, got %q", env["OPENAI_API_KEY"])
	}

	reloaded, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("reload app config: %v", err)
	}
	if reloaded.Proxy != nil && reloaded.Proxy.Routes != nil {
		if _, ok := reloaded.Proxy.Routes[string(agentCodex)]; ok {
			t.Fatalf("temporary codex route was not cleaned up: %#v", reloaded.Proxy.Routes)
		}
	}
}

func envPairsToMap(pairs []envPair) map[string]string {
	out := map[string]string{}
	for _, p := range pairs {
		out[p.Key] = p.Value
	}
	return out

}

func envSliceToMap(env []string) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}
