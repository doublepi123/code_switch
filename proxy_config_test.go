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

func TestNormalizeProxyConfigFillsDefaults(t *testing.T) {
	got := normalizeProxyConfig(ProxyConfig{Host: "  ", Port: -5})
	if got.Host != "127.0.0.1" {
		t.Fatalf("Host = %q, want 127.0.0.1", got.Host)
	}
	if got.Port != 0 {
		t.Fatalf("Port = %d, want 0", got.Port)
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
		Proxy: &ProxyConfig{
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
	// Defensive copy: mutating the source config must not affect the route.
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
		Proxy: &ProxyConfig{
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

func TestBuildProxyRouteFromConfigDefaultsProtocol(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {Agent: "codex", Provider: "zhipu-cn"},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "tok")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig error: %v", err)
	}
	if route.UpstreamProtocol != protocolAnthropicMessages {
		t.Fatalf("UpstreamProtocol = %q, want %q", route.UpstreamProtocol, protocolAnthropicMessages)
	}
}

func TestBuildProxyRouteFromConfigRejectsMissingRoute(t *testing.T) {
	_, err := buildProxyRouteFromConfig("codex", &AppConfig{Providers: map[string]StoredProvider{}}, "token")
	if err == nil {
		t.Fatal("expected missing route error")
	}
}

func TestBuildProxyRouteFromConfigRejectsNilConfig(t *testing.T) {
	_, err := buildProxyRouteFromConfig("codex", nil, "token")
	if err == nil {
		t.Fatal("expected nil config error")
	}
}

func TestBuildProxyRouteFromConfigRejectsUnknownProvider(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {Agent: "codex", Provider: "no-such-provider"},
			},
		},
	}
	if _, err := buildProxyRouteFromConfig("codex", cfg, "tok"); err == nil {
		t.Fatal("expected unknown provider error")
	}
}
