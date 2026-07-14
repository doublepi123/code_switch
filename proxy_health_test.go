package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdProxyHealthReportsConfiguredRouteReachability(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
		wantStatus string
	}{
		{name: "healthy upstream", statusCode: http.StatusNoContent, wantStatus: "ok"},
		{name: "unhealthy upstream", statusCode: http.StatusServiceUnavailable, wantErr: true, wantStatus: "fail"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			home := t.TempDir()
			t.Setenv("HOME", home)
			seenMethod := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenMethod <- r.Method
				w.WriteHeader(tt.statusCode)
			}))
			defer upstream.Close()
			cfg := AppConfig{
				Providers: map[string]StoredProvider{
					"health-test": {
						Name:     "health-test",
						BaseURL:  upstream.URL,
						Protocol: protocolOpenAIChat,
						Model:    "health-model",
					},
				},
				Proxy: &ProxyConfig{Routes: map[string]ProxyRouteConfig{
					"codex": {
						Agent:            "codex",
						Provider:         "health-test",
						UpstreamProtocol: string(protocolOpenAIChat),
						Token:            "local-token",
					},
				}},
			}
			if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
				t.Fatalf("write config: %v", err)
			}

			// When
			var out bytes.Buffer
			err := cmdProxyHealth(nil, &out)

			// Then
			if tt.wantErr && err == nil {
				t.Fatal("expected health error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("health error: %v", err)
			}
			method := <-seenMethod
			if method != http.MethodHead {
				t.Fatalf("method = %q, want HEAD", method)
			}
			got := out.String()
			for _, want := range []string{"codex", "health-test", tt.wantStatus} {
				if !strings.Contains(got, want) {
					t.Fatalf("health output missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestCmdProxyHealthRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	if err := cmdProxyHealth([]string{"unexpected"}, &out); err == nil {
		t.Fatal("expected usage error for extra args, got nil")
	}
}
