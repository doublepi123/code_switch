package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBytesAtomicTest(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func TestBuildProxyRouteNewSignatureNoConfig(t *testing.T) {
	preset := providerPresets["zhipu-cn"]
	route := buildProxyRoute(
		"zhipu-cn",
		preset,
		protocolAnthropicMessages,
		"local-token",
		map[string]string{"default": "glm-5.2"},
	)
	if route.Provider != "zhipu-cn" {
		t.Fatalf("Provider = %q, want zhipu-cn", route.Provider)
	}
	if route.LocalToken != "local-token" {
		t.Fatalf("LocalToken = %q, want local-token", route.LocalToken)
	}
	if route.UpstreamProtocol != protocolAnthropicMessages {
		t.Fatalf("UpstreamProtocol = %q, want %q", route.UpstreamProtocol, protocolAnthropicMessages)
	}
	if route.Model != preset.Model {
		t.Fatalf("Model = %q, want preset %q", route.Model, preset.Model)
	}
	if got := route.ModelMappings["default"]; got != "glm-5.2" {
		t.Fatalf("ModelMappings[default] = %q, want glm-5.2", got)
	}
}

func TestBuildProxyRouteNewSignatureDefensiveCopy(t *testing.T) {
	preset := providerPresets["zhipu-cn"]
	src := map[string]string{"default": "glm-5.2"}
	route := buildProxyRoute("zhipu-cn", preset, protocolAnthropicMessages, "tok", src)
	route.ModelMappings["default"] = "tampered"
	if src["default"] != "glm-5.2" {
		t.Fatalf("buildProxyRoute did not defensive-copy: src mutated to %q", src["default"])
	}
}

func TestBuildProxyRouteFromConfigRouteMappingsNotMergedWithProvider(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"sonnet": "provider-level-model"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:            "codex",
					Provider:         "zhipu-cn",
					Model:            "glm-5.2",
					UpstreamProtocol: string(protocolAnthropicMessages),
					ModelMappings:    map[string]string{"default": "route-level-model"},
				},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "local-token")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig: %v", err)
	}
	if got := route.ModelMappings["default"]; got != "route-level-model" {
		t.Fatalf("route-level mapping lost: default = %q, want route-level-model", got)
	}
	if got := route.ModelMappings["haiku"]; got != "glm-4.5-air" {
		t.Fatalf("resolved haiku tier missing: haiku = %q, want glm-4.5-air", got)
	}
	if got := route.ModelMappings["sonnet"]; got != "glm-5-turbo" {
		t.Fatalf("resolved sonnet tier missing: sonnet = %q, want glm-5-turbo", got)
	}
	if len(route.ModelMappings) != 5 {
		t.Fatalf("route.ModelMappings len = %d, want 5; got %#v",
			len(route.ModelMappings), route.ModelMappings)
	}
	cfg.Proxy.Routes["codex"].ModelMappings["default"] = "mutated"
	if got := route.ModelMappings["default"]; got != "route-level-model" {
		t.Fatalf("route mappings not defensively copied: default = %q", got)
	}
}

func TestBuildProxyRouteFromConfigEmptyRouteMappingsFallsBackToProvider(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"default": "provider-level-model", "sonnet": "provider-sonnet"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:            "codex",
					Provider:         "zhipu-cn",
					Model:            "glm-5.2",
					UpstreamProtocol: string(protocolAnthropicMessages),
					},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "local-token")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig: %v", err)
	}
	if got := route.ModelMappings["default"]; got != "glm-5-turbo" {
		t.Fatalf("tier fallback lost: default = %q, want glm-5-turbo", got)
	}
	if got := route.ModelMappings["sonnet"]; got != "glm-5-turbo" {
		t.Fatalf("tier fallback lost: sonnet = %q, want glm-5-turbo", got)
	}
}

func TestDryRunSurfacesPersistedModelMappings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{
			"minimax-cn": {APIKey: "sk-secret"},
		},
		ModelMappings: map[string]map[string]string{
			"minimax-cn": {"default": "MiniMax-M3", "sonnet": "MiniMax-M3"},
		},
	}
	configPath := filepath.Join(home, ".code-switch", "config.json")
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	out := &bytes.Buffer{}
	if err := runWithIO([]string{"run", "codex", "--provider", "minimax-cn", "--dry-run"}, strings.NewReader(""), out); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "model_mappings:") {
		t.Fatalf("dry-run output missing model_mappings line; persisted mappings did not reach the route\noutput:\n%s", got)
	}
	if strings.Contains(got, "model_mappings: 0") {
		t.Fatalf("dry-run reported 0 mappings despite persisted cfg.ModelMappings\noutput:\n%s", got)
	}
}

// =============================================================================
// Issue 3 (Important): proxy config defaults must have a single normalization
// entry point. normalizeAppConfig must run inside loadAppConfigFrom so every
// reader sees a normalized ProxyConfig (host filled, port clamped), without
// forcing proxy fields into an otherwise-empty config (preserve JSON omitempty).
// =============================================================================

// TestNormalizeAppConfigFillsProxyDefaults verifies that an AppConfig loaded
// with an explicitly-empty Proxy block gets normalized host/port without the
// caller having to remember to call normalizeProxyConfig.
func TestNormalizeAppConfigFillsProxyDefaults(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{},
		Proxy:     &ProxyConfig{Host: "  ", Port: -5},
	}
	normalizeAppConfig(cfg)
	if cfg.Proxy.Host != "127.0.0.1" {
		t.Fatalf("Proxy.Host = %q, want 127.0.0.1", cfg.Proxy.Host)
	}
	if cfg.Proxy.Port != 0 {
		t.Fatalf("Proxy.Port = %d, want 0", cfg.Proxy.Port)
	}
}

// TestNormalizeAppConfigPreservesExplicitHost verifies a user-supplied host
// is NOT overwritten (idempotency / non-clobbering).
func TestNormalizeAppConfigPreservesExplicitHost(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{},
		Proxy:     &ProxyConfig{Host: "0.0.0.0", Port: 8080},
	}
	normalizeAppConfig(cfg)
	if cfg.Proxy.Host != "0.0.0.0" {
		t.Fatalf("Proxy.Host = %q, want 0.0.0.0 (must not clobber)", cfg.Proxy.Host)
	}
	if cfg.Proxy.Port != 8080 {
		t.Fatalf("Proxy.Port = %d, want 8080", cfg.Proxy.Port)
	}
}

// TestLoadAppConfigFromNormalizesProxyDefaults writes a config file with an
// empty proxy block to disk and asserts that loading it yields a normalized
// ProxyConfig. This proves the normalization runs at the load entrypoint.
func TestLoadAppConfigFromNormalizesProxyDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".code-switch", "config.json")

	// Write a config with a Proxy block present but with a blank host and a
	// negative port. JSON omitempty is irrelevant here because we explicitly
	// emit the proxy object.
	seed := `{
  "providers": {},
  "proxy": {"host": "   ", "port": -7}
}`
	if err := writeBytesAtomicTest(configPath, []byte(seed)); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	cfg, err := loadAppConfigFrom(configPath)
	if err != nil {
		t.Fatalf("loadAppConfigFrom: %v", err)
	}
	if cfg.Proxy.Host != "127.0.0.1" {
		t.Fatalf("Proxy.Host after load = %q, want 127.0.0.1", cfg.Proxy.Host)
	}
	if cfg.Proxy.Port != 0 {
		t.Fatalf("Proxy.Port after load = %d, want 0", cfg.Proxy.Port)
	}
}

// TestLoadAppConfigFromPreservesJSONOmitEmpty verifies that loading a config
// with NO proxy block does NOT suddenly materialize a Proxy object on
// re-serialization. This guards the "must not break JSON omitempty semantics"
// requirement: an empty config should round-trip without proxy fields.
func TestLoadAppConfigFromPreservesJSONOmitEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".code-switch", "config.json")

	seed := `{"providers": {}}`
	if err := writeBytesAtomicTest(configPath, []byte(seed)); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	cfg, err := loadAppConfigFrom(configPath)
	if err != nil {
		t.Fatalf("loadAppConfigFrom: %v", err)
	}
	// Re-marshal and confirm no "proxy" key appears.
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		t.Fatalf("re-write: %v", err)
	}
	reload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read reload: %v", err)
	}
	if strings.Contains(string(reload), `"proxy"`) {
		t.Fatalf("normalize must not force proxy fields into an empty config; got:\n%s", string(reload))
	}
}

// =============================================================================
// Issue 4 (Important): an unknown upstream protocol value must NOT silently
// downgrade to anthropic-messages. An empty value defaults to
// anthropic-messages; a non-empty unrecognized value must return an error so
// misconfiguration is loud.
// =============================================================================

// TestResolveProxyProtocolEmptyDefaults verifies an empty/whitespace protocol
// resolves to anthropic-messages without error.
func TestResolveProxyProtocolEmptyDefaults(t *testing.T) {
	for _, in := range []string{"", "   ", "\t"} {
		got, err := resolveProxyProtocol(in)
		if err != nil {
			t.Fatalf("resolveProxyProtocol(%q) error: %v", in, err)
		}
		if got != protocolAnthropicMessages {
			t.Fatalf("resolveProxyProtocol(%q) = %q, want %q", in, got, protocolAnthropicMessages)
		}
	}
}

// TestResolveProxyProtocolKnownValues verifies each recognized protocol
// round-trips through the resolver.
func TestResolveProxyProtocolKnownValues(t *testing.T) {
	cases := []struct {
		in   string
		want ProviderProtocol
	}{
		{"anthropic-messages", protocolAnthropicMessages},
		{"openai-chat", protocolOpenAIChat},
		{"openai-responses", protocolOpenAIResponses},
		{"  openai-chat  ", protocolOpenAIChat},
	}
	for _, c := range cases {
		got, err := resolveProxyProtocol(c.in)
		if err != nil {
			t.Fatalf("resolveProxyProtocol(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("resolveProxyProtocol(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveProxyProtocolUnknownErrors verifies an unrecognized non-empty
// protocol value returns an error rather than silently downgrading.
func TestResolveProxyProtocolUnknownErrors(t *testing.T) {
	for _, in := range []string{"bogus", "anthropic", "openai", "grpc"} {
		_, err := resolveProxyProtocol(in)
		if err == nil {
			t.Fatalf("resolveProxyProtocol(%q) returned nil error; want error for unknown protocol", in)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "protocol") {
			t.Fatalf("error should mention protocol, got: %v", err)
		}
	}
}

// TestBuildProxyRouteFromConfigRejectsUnknownProtocol verifies the resolver
// path returns an error for an unknown persisted protocol (no silent
// downgrade end-to-end).
func TestBuildProxyRouteFromConfigRejectsUnknownProtocol(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:            "codex",
					Provider:         "zhipu-cn",
					UpstreamProtocol: "totally-bogus",
				},
			},
		},
	}
	_, err := buildProxyRouteFromConfig("codex", cfg, "tok")
	if err == nil {
		t.Fatal("expected error for unknown upstream protocol, got nil")
	}
}

// =============================================================================
// Issue 5: copyStringMap comment accuracy + normalizeProxyConfig must trim a
// non-empty (but whitespace-only) host.
// =============================================================================

// TestNormalizeProxyConfigTrimsWhitespaceHost verifies a whitespace-only host
// is treated as empty and replaced with the default. (Whitespace IS non-empty
// byte-wise, so this guards that normalization trims before checking.)
func TestNormalizeProxyConfigTrimsWhitespaceHost(t *testing.T) {
	got := normalizeProxyConfig(ProxyConfig{Host: "   \t  "})
	if got.Host != "127.0.0.1" {
		t.Fatalf("Host = %q, want 127.0.0.1 (whitespace host must be trimmed to default)", got.Host)
	}
}

// TestCopyStringMapNilRoundTrip documents the actual contract: nil/empty in
// returns nil out (not an empty allocated map). This matches the corrected
// comment.
func TestCopyStringMapNilRoundTrip(t *testing.T) {
	if got := copyStringMap(nil); got != nil {
		t.Fatalf("copyStringMap(nil) = %#v, want nil", got)
	}
	if got := copyStringMap(map[string]string{}); got != nil {
		t.Fatalf("copyStringMap(empty) = %#v, want nil", got)
	}
}

// =============================================================================
// Issue 6 (recommended): route.Model precedence and provider alias
// canonicalization coverage.
// =============================================================================

// TestBuildProxyRouteFromConfigRouteModelOverridesStored verifies a route's
// explicit Model takes precedence over the stored provider model.
func TestBuildProxyRouteFromConfigRouteModelOverridesStored(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:    "codex",
					Provider: "zhipu-cn",
					Model:    "glm-5.1", // route-level override
				},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "tok")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig: %v", err)
	}
	if route.Model != "glm-5.1" {
		t.Fatalf("route.Model = %q, want glm-5.1 (route override)", route.Model)
	}
}

// TestBuildProxyRouteFromConfigFallsBackToPresetDefaultWhenStoredMissing
// verifies that when neither the route nor the stored provider carries a
// model, the preset's default model is used.
func TestBuildProxyRouteFromConfigFallsBackToPresetDefaultWhenStoredMissing(t *testing.T) {
	preset := providerPresets["zhipu-cn"]
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test"}, // no Model
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {Agent: "codex", Provider: "zhipu-cn"}, // no Model
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "tok")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig: %v", err)
	}
	if route.Model != preset.Model {
		t.Fatalf("route.Model = %q, want preset default %q", route.Model, preset.Model)
	}
}

// TestBuildProxyRouteFromConfigCanonicalizesProviderAlias verifies a route
// that stores an alias (e.g. "zhipu") is resolved to its canonical provider
// name ("zhipu-cn") in the resulting ProxyRoute.Provider.
func TestBuildProxyRouteFromConfigCanonicalizesProviderAlias(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"},
		},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"default": "glm-5.2"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:    "codex",
					Provider: "zhipu", // alias, not canonical
				},
			},
		},
	}
	route, err := buildProxyRouteFromConfig("codex", cfg, "tok")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig: %v", err)
	}
	if route.Provider != "zhipu-cn" {
		t.Fatalf("route.Provider = %q, want canonical zhipu-cn", route.Provider)
	}
	// Mappings should also be looked up by the canonical name.
	if got := route.ModelMappings["default"]; got != "glm-5-turbo" {
		t.Fatalf("ModelMappings[default] = %q, want glm-5-turbo (looked up by canonical)", got)
	}
}
