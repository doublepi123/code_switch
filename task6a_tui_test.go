package main

import (
	"strings"
	"testing"
)

func TestProviderListItemShowsProtocolBadgesAndConnectionMode(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	preset := ProviderPreset{
		Name:    "All Protocols",
		BaseURL: "https://anthropic.example.test",
		Endpoints: map[ProviderProtocol]ProtocolEndpoint{
			protocolOpenAIChat:      {BaseURL: "https://chat.example.test/v1"},
			protocolOpenAIResponses: {BaseURL: "https://responses.example.test/v1"},
		},
	}
	title, secondary := providerListItemText(agentClaude, cfg, "all-protocols", preset, "", "")
	for _, want := range []string{"[A]", "[C]", "[R]", "direct"} {
		if !strings.Contains(title, want) {
			t.Fatalf("provider list title %q missing %q (secondary=%q)", title, want, secondary)
		}
	}
	zaiCfg := &AppConfig{Providers: map[string]StoredProvider{"zai": {APIKey: "sk-test"}}}
	zaiPreset, err := resolveAgentProviderPreset(agentCodex, "zai", zaiCfg)
	if err != nil {
		t.Fatalf("resolveAgentProviderPreset(zai): %v", err)
	}
	proxyTitle, _ := providerListItemText(agentCodex, zaiCfg, "zai", zaiPreset, "", "")
	if !strings.Contains(proxyTitle, "proxy") {
		t.Fatalf("provider list title %q missing proxy mode", proxyTitle)
	}
}

func TestProviderDetailTextShowsProtocolsAndRouteHint(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zai": {APIKey: "sk-test"}}}
	preset, err := resolveAgentProviderPreset(agentCodex, "zai", cfg)
	if err != nil {
		t.Fatalf("resolveAgentProviderPreset: %v", err)
	}
	text := providerDetailInfoText(agentCodex, cfg, "zai", preset, "", "", false, false)
	for _, want := range []string{"Protocol Endpoints", string(protocolAnthropicMessages), "Connection", "proxy", "local proxy route"} {
		if !strings.Contains(text, want) {
			t.Fatalf("detail text missing %q:\n%s", want, text)
		}
	}
}

func TestCustomProviderSelectionPersistsProtocol(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	selection := ConfigureSelection{
		Agent:    string(agentClaude),
		Provider: "custom-openai",
		Name:     "Custom OpenAI",
		BaseURL:  "https://example.test/v1",
		APIKey:   "sk-test",
		Model:    "gpt-test",
		Protocol: protocolOpenAIChat,
	}
	upsertProviderConfig(cfg, selection, selection.APIKey)
	if got := cfg.Providers[selection.Provider].Protocol; got != protocolOpenAIChat {
		t.Fatalf("stored protocol = %q, want %q", got, protocolOpenAIChat)
	}
}

func TestProxyManagerRouteSummariesShowAllRoutesMasked(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{},
		Proxy: &ProxyConfig{Routes: map[string]ProxyRouteConfig{
			"claude": {Agent: "claude", Provider: "zai", UpstreamProtocol: string(protocolOpenAIResponses), Token: "csproxy-route-abcdefghijklmnopqrstuvwxyz"},
			"codex":  {Agent: "codex", Provider: "zhipu-cn", UpstreamProtocol: string(protocolAnthropicMessages), Token: "csproxy-route-1234567890"},
		}},
	}
	items := proxyManagerRouteSummaries(cfg)
	if len(items) != 2 {
		t.Fatalf("route summaries len = %d, want 2 (%#v)", len(items), items)
	}
	joined := strings.Join(items, "\n")
	for _, want := range []string{"agent: claude", "agent: codex", "provider: zai", "provider: zhipu-cn", string(protocolOpenAIResponses), string(protocolAnthropicMessages), "token:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("route summaries missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "abcdefghijklmnopqrstuvwxyz") || strings.Contains(joined, "1234567890") {
		t.Fatalf("route summaries leaked full token:\n%s", joined)
	}
}

func TestCrossProtocolSwitchPromptConfiguresProxyRoute(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zai": {APIKey: "sk-test", Model: "glm-test"}}}
	selection := ConfigureSelection{Agent: string(agentCodex), Provider: "zai", Model: "glm-test"}
	msg, changed, err := configureProxyRouteForCrossProtocolSelection(cfg, selection)
	if err != nil {
		t.Fatalf("configureProxyRouteForCrossProtocolSelection: %v", err)
	}
	if !changed {
		t.Fatalf("changed = false, want true")
	}
	if !strings.Contains(msg, "local proxy route") {
		t.Fatalf("message %q missing local proxy route hint", msg)
	}
	if cfg.Proxy == nil || cfg.Proxy.Routes == nil {
		t.Fatalf("proxy routes not initialized: %#v", cfg.Proxy)
	}
	route, ok := cfg.Proxy.Routes[string(agentCodex)]
	if !ok {
		t.Fatalf("codex route missing: %#v", cfg.Proxy.Routes)
	}
	if route.Provider != "zai" || route.UpstreamProtocol != string(protocolAnthropicMessages) || route.Token == "" {
		t.Fatalf("route = %#v, want zai/%s/with token", route, protocolAnthropicMessages)
	}
}
