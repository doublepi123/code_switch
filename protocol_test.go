package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentProfilesDeclareClientAndRoutingProtocols(t *testing.T) {
	for _, tt := range []struct {
		agent  AgentName
		client ProviderProtocol
		direct []ProviderProtocol
		proxy  []ProviderProtocol
	}{
		{agentClaude, protocolAnthropicMessages, []ProviderProtocol{protocolAnthropicMessages}, []ProviderProtocol{protocolOpenAIResponses, protocolOpenAIChat, protocolAnthropicMessages}},
		{agentCodex, protocolOpenAIResponses, []ProviderProtocol{protocolOpenAIResponses, protocolOpenAIChat}, []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat, protocolOpenAIResponses}},
		{agentOpencode, protocolOpenAIChat, []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat}, []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat}},
	} {
			profile, ok := getAgentProfile(nil, tt.agent)
		if !ok {
			t.Fatalf("agentProfiles[%q] missing", tt.agent)
		}
		if profile.ClientProtocol != tt.client {
			t.Fatalf("%s client protocol = %q, want %q", tt.agent, profile.ClientProtocol, tt.client)
		}
		assertProtocolSlice(t, string(tt.agent)+" direct", profile.DirectProtocols, tt.direct)
		assertProtocolSlice(t, string(tt.agent)+" proxy", profile.ProxyUpstreamPreference, tt.proxy)
	}
}

func TestProviderPresetEndpointFallsBackToAnthropicBaseURL(t *testing.T) {
	preset := ProviderPreset{BaseURL: "https://example.test/anthropic", AuthEnv: "ANTHROPIC_AUTH_TOKEN"}

	endpoint, ok := preset.presetEndpoint(protocolAnthropicMessages)

	if !ok {
		t.Fatal("presetEndpoint(anthropic-messages) ok = false")
	}
	if endpoint.BaseURL != preset.BaseURL || endpoint.AuthEnv != preset.AuthEnv {
		t.Fatalf("endpoint = %#v, want base/auth from preset", endpoint)
	}
}

func TestSelectedProviderPresetsDeclareOpenAIChatEndpoints(t *testing.T) {
	for _, tt := range []struct {
		provider string
		baseURL  string
		authEnv  string
	}{
		{"deepseek", "https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
		{"openrouter", "https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
		{"ollama-cloud", "https://ollama.com/v1", "OLLAMA_API_KEY"},
		{"kimi-coding", "https://api.kimi.com/coding/v1", "KIMI_API_KEY"},
		{"ollama", "http://localhost:11434/v1", ""},
	} {
		endpoint, ok := providerPresets[tt.provider].presetEndpoint(protocolOpenAIChat)
		if !ok {
			t.Fatalf("%s openai-chat endpoint missing", tt.provider)
		}
		if endpoint.BaseURL != tt.baseURL || endpoint.AuthEnv != tt.authEnv {
			t.Fatalf("%s endpoint = %#v, want base=%q auth=%q", tt.provider, endpoint, tt.baseURL, tt.authEnv)
		}
	}
}

func TestStoredProviderProtocolDefaultsToAnthropicMessagesForOldConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"providers":{"custom":{"baseUrl":"https://example.test/anthropic","apiKey":"sk-test"}}}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := loadAppConfigFrom(configPath)
	if err != nil {
		t.Fatalf("loadAppConfigFrom: %v", err)
	}

	stored := cfg.Providers["custom"]
	if stored.Protocol != "" {
		t.Fatalf("stored Protocol = %q, want empty to preserve old config shape", stored.Protocol)
	}
	if got := stored.providerProtocol(); got != protocolAnthropicMessages {
		t.Fatalf("providerProtocol() = %q, want %q", got, protocolAnthropicMessages)
	}
}

func TestResolveConnectionSelectsDirectOrProxyByAgentProfileAndProviderEndpoints(t *testing.T) {
	chatOnly := ProviderPreset{
		Name: "Chat Only",
		Endpoints: map[ProviderProtocol]ProtocolEndpoint{
			protocolOpenAIChat: {BaseURL: "https://chat.example.test/v1", AuthEnv: "CHAT_API_KEY"},
		},
	}

	for _, tt := range []struct {
		name     string
		agent    AgentName
		provider string
		preset   ProviderPreset
		via      string
		mode     ConnectionMode
		protocol ProviderProtocol
		baseURL  string
		wantErr  bool
	}{
		{
			name:  "claude zai direct",
			agent: agentClaude, provider: "zai", preset: providerPresets["zai"],
			mode: connectionModeDirect, protocol: protocolAnthropicMessages, baseURL: "https://api.z.ai/api/anthropic",
		},
		{
			name:  "codex zai proxy",
			agent: agentCodex, provider: "zai", preset: providerPresets["zai"],
			mode: connectionModeProxy, protocol: protocolAnthropicMessages, baseURL: "https://api.z.ai/api/anthropic",
		},
		{
			name:  "codex deepseek direct",
			agent: agentCodex, provider: "deepseek", preset: providerPresets["deepseek"],
			mode: connectionModeDirect, protocol: protocolOpenAIChat, baseURL: "https://api.deepseek.com/v1",
		},
		{
			name:  "codex deepseek forced proxy",
			agent: agentCodex, provider: "deepseek", preset: providerPresets["deepseek"], via: "proxy",
			mode: connectionModeProxy, protocol: protocolAnthropicMessages, baseURL: "https://api.deepseek.com/anthropic",
		},
		{
			name:  "claude chat only proxy",
			agent: agentClaude, provider: "chat-only", preset: chatOnly,
			mode: connectionModeProxy, protocol: protocolOpenAIChat, baseURL: "https://chat.example.test/v1",
		},
		{
			name:  "codex zai forced direct errors",
			agent: agentCodex, provider: "zai", preset: providerPresets["zai"], via: "direct",
			wantErr: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := resolveConnection(tt.agent, nil, tt.provider, tt.preset, tt.via)
			if tt.wantErr {
				if err == nil {
					t.Fatal("resolveConnection error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveConnection error: %v", err)
			}
			if plan.Mode != tt.mode || plan.UpstreamProtocol != tt.protocol || plan.Endpoint.BaseURL != tt.baseURL {
				t.Fatalf("plan = %#v, want mode=%q protocol=%q baseURL=%q", plan, tt.mode, tt.protocol, tt.baseURL)
			}
		})
	}
}

func TestPresetEndpointNoBaseURLFallbackWhenEndpointsDeclared(t *testing.T) {
	// Simulates kimi-coding: has Endpoints with only openai-chat, plus a BaseURL.
	preset := ProviderPreset{
		BaseURL: "https://api.kimi.com/coding/",
		AuthEnv: "ANTHROPIC_AUTH_TOKEN",
		Endpoints: map[ProviderProtocol]ProtocolEndpoint{
			protocolOpenAIChat: {BaseURL: "https://api.kimi.com/coding/v1", AuthEnv: "KIMI_API_KEY"},
		},
	}

	// openai-chat should be found via Endpoints
	ep, ok := preset.presetEndpoint(protocolOpenAIChat)
	if !ok || ep.BaseURL != "https://api.kimi.com/coding/v1" {
		t.Fatalf("presetEndpoint(openai-chat) = (%#v, %v), want base=https://api.kimi.com/coding/v1", ep, ok)
	}

	// anthropic-messages should NOT be found via BaseURL fallback
	_, ok = preset.presetEndpoint(protocolAnthropicMessages)
	if ok {
		t.Fatal("presetEndpoint(anthropic-messages) should be false when Endpoints is non-empty")
	}
}

func TestCodexKimiCodingResolvesToOpenAIChat(t *testing.T) {
	// Codex ProxyUpstreamPreference: [anthropic-messages, openai-chat, openai-responses]
	// kimi-coding only has openai-chat → must resolve to openai-chat, not anthropic-messages.
	plan, err := resolveConnection(agentCodex, nil, "kimi-coding", providerPresets["kimi-coding"], "")
	if err != nil {
		t.Fatalf("resolveConnection error: %v", err)
	}
	if plan.UpstreamProtocol != protocolOpenAIChat {
		t.Fatalf("UpstreamProtocol = %q, want %q", plan.UpstreamProtocol, protocolOpenAIChat)
	}
	if plan.Endpoint.BaseURL != "https://api.kimi.com/coding/v1" {
		t.Fatalf("Endpoint.BaseURL = %q, want https://api.kimi.com/coding/v1", plan.Endpoint.BaseURL)
	}
}

func assertProtocolSlice(t *testing.T, label string, got, want []ProviderProtocol) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s len = %d, want %d (%v)", label, len(got), len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q, want %q", label, i, got[i], want[i])
		}
	}
}
