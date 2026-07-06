package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRandomProxyRouteTokenShape(t *testing.T) {
	tok, err := randomProxyRouteToken()
	if err != nil {
		t.Fatalf("randomProxyRouteToken: %v", err)
	}
	if !strings.HasPrefix(tok, "csproxy-route-") {
		t.Fatalf("token prefix = %q, want csproxy-route-", tok)
	}
	if got := len(strings.TrimPrefix(tok, "csproxy-route-")); got != 64 {
		t.Fatalf("token hex length = %d, want 64", got)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(tok, "csproxy-route-")); err != nil {
		t.Fatalf("token suffix is not hex: %v", err)
	}
}

func TestCmdProxyConfigurePersistsRouteToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure: %v", err)
	}
	cfg, path, err := loadAppConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	tok := cfg.Proxy.Routes["codex"].Token
	if tok == "" {
		t.Fatal("configured route token is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config permissions = %o, want 600", mode)
	}
}

func TestPrepareProxyServeGeneratesAndPersistsMissingRouteTokens(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	cfg, path, err := loadAppConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Proxy = &ProxyConfig{Host: "127.0.0.1", Routes: map[string]ProxyRouteConfig{
		"codex":  {Agent: "codex", Provider: "zhipu-cn", Model: "glm-5.2", UpstreamProtocol: string(protocolAnthropicMessages)},
		"claude": {Agent: "claude", Provider: "zhipu-cn", Model: "glm-5.2", UpstreamProtocol: string(protocolOpenAIResponses)},
	}}
	if err := writeJSONAtomic(path, cfg); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	inst, err := prepareProxyServe("", "127.0.0.1", 0, "legacy-instance-token")
	if err != nil {
		t.Fatalf("prepareProxyServe: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = inst.server.Shutdown(ctx)
	}()

	reloaded, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, agent := range []string{"codex", "claude"} {
		if reloaded.Proxy.Routes[agent].Token == "" {
			t.Fatalf("route %s token was not generated", agent)
		}
	}
	if reloaded.Proxy.Routes["codex"].Token == reloaded.Proxy.Routes["claude"].Token {
		t.Fatal("route tokens must be distinct")
	}
}

func TestProxyMultiRouteHandlerBearerTokenDispatch(t *testing.T) {
	codexUpstream, codexCap := startAnthropicUpstream(t, 0, "")
	claudeCap := &upstreamCapture{}
	claudeUpstream := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeCap.path = r.URL.Path
		claudeCap.auth = r.Header.Get("Authorization")
		claudeCap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","model":"glm-5.2","output_text":"Hi","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(claudeUpstream.Close)

	handler := newProxyMultiRouteHandler([]proxyServedRoute{
		{Agent: "codex", ClientProtocol: protocolOpenAIResponses, Route: ProxyRoute{Provider: "zhipu-cn", Model: "glm-5.2", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: codexUpstream.URL, LocalToken: "codex-token"}, ProviderKey: "codex-provider-key"},
		{Agent: "claude", ClientProtocol: protocolAnthropicMessages, Route: ProxyRoute{Provider: "zhipu-cn", Model: "glm-5.2", UpstreamProtocol: protocolOpenAIResponses, UpstreamBaseURL: claudeUpstream.URL, LocalToken: "claude-token"}, ProviderKey: "claude-provider-key"},
	}, defaultProtocolRegistry())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
	req.Header.Set("Authorization", "Bearer codex-token")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("codex status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if codexCap.auth != "Bearer codex-provider-key" {
		t.Fatalf("codex upstream auth = %q", codexCap.auth)
	}
	if claudeCap.auth != "" {
		t.Fatalf("claude upstream should not be called for codex token; auth=%q", claudeCap.auth)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer claude-token")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("claude status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if claudeCap.auth != "Bearer claude-provider-key" {
		t.Fatalf("claude upstream auth = %q", claudeCap.auth)
	}
	if claudeCap.path != "/v1/responses" {
		t.Fatalf("claude upstream path = %q", claudeCap.path)
	}
}

func TestProxyMultiRouteHandlerUnauthorizedAndPathMismatch(t *testing.T) {
	upstream, _ := startAnthropicUpstream(t, 0, "")
	handler := newProxyMultiRouteHandler([]proxyServedRoute{
		{Agent: "codex", ClientProtocol: protocolOpenAIResponses, Route: ProxyRoute{Provider: "zhipu-cn", Model: "glm-5.2", UpstreamProtocol: protocolAnthropicMessages, UpstreamBaseURL: upstream.URL, LocalToken: "codex-token"}, ProviderKey: "provider-key"},
	}, defaultProtocolRegistry())

	for _, tc := range []struct {
		name string
		auth string
	}{
		{name: "missing token"},
		{name: "wrong token", auth: "Bearer wrong-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex-model","input":"Say hi"}`))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"Say hi"}]}`))
	req.Header.Set("Authorization", "Bearer codex-token")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for protocol path mismatch; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCmdProxyStatusReportsAllConfiguredRoutesBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := runWithIO([]string{"set-key", "zhipu-cn", "sk-test"}, nil, io.Discard); err != nil {
		t.Fatalf("set-key: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "codex", "--provider", "zhipu-cn", "--model", "glm-5.2"}, nil, io.Discard); err != nil {
		t.Fatalf("configure codex: %v", err)
	}
	if err := runWithIO([]string{"proxy", "configure", "claude", "--provider", "zhipu-cn", "--model", "glm-5.2", "--protocol", string(protocolOpenAIResponses)}, nil, io.Discard); err != nil {
		t.Fatalf("configure claude: %v", err)
	}
	cfg, path, err := loadAppConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	codexToken := cfg.Proxy.Routes["codex"].Token
	claudeToken := cfg.Proxy.Routes["claude"].Token
	const instanceID = "csproxy-status-multiroute-instance"
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

	if err := writeProxyRuntimeState(ProxyRuntimeState{PID: os.Getpid(), InstanceID: instanceID, Host: "127.0.0.1", Port: port, BaseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", port), StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	// Ensure the status command reads the same config file path after state setup.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config missing: %v", err)
	}
	var out bytes.Buffer
	if err := cmdProxyStatus(nil, &out); err != nil {
		t.Fatalf("status: %v", err)
	}
	got := out.String()
	for _, want := range []string{"routes:", "agent: codex", "agent: claude", "provider: zhipu-cn", string(protocolAnthropicMessages), string(protocolOpenAIResponses)} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, codexToken) || strings.Contains(got, claudeToken) {
		t.Fatalf("status leaked full token:\n%s", got)
	}
	if !strings.Contains(got, maskProxyToken(codexToken)) || !strings.Contains(got, maskProxyToken(claudeToken)) {
		t.Fatalf("status missing masked tokens:\n%s", got)
	}
}
