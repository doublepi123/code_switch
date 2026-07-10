package main

import "testing"

func TestGetAgentProfileUsesConfigOverride(t *testing.T) {
	cfg := &AppConfig{
		AgentProfiles: map[string]AgentProfile{
			string(agentClaude): {
				ClientProtocol:          protocolOpenAIChat,
				DirectProtocols:         []ProviderProtocol{protocolOpenAIChat},
				ProxyUpstreamPreference: []ProviderProtocol{protocolOpenAIChat},
			},
		},
	}

	profile, ok := getAgentProfile(cfg, agentClaude)

	if !ok {
		t.Fatal("getAgentProfile ok = false, want true")
	}
	if profile.ClientProtocol != protocolOpenAIChat {
		t.Fatalf("ClientProtocol = %q, want %q", profile.ClientProtocol, protocolOpenAIChat)
	}
}

func TestGetAgentProfileFallsBackToBuiltInWhenConfigMissing(t *testing.T) {
	profile, ok := getAgentProfile(nil, agentCodex)

	if !ok {
		t.Fatal("getAgentProfile ok = false, want true")
	}
	if profile.ClientProtocol != protocolOpenAIResponses {
		t.Fatalf("ClientProtocol = %q, want %q", profile.ClientProtocol, protocolOpenAIResponses)
	}
}

func TestGetAgentProfileReturnsFalseForUnknownAgent(t *testing.T) {
	profile, ok := getAgentProfile(&AppConfig{}, AgentName("unknown"))

	if ok {
		t.Fatalf("getAgentProfile ok = true, want false; profile=%#v", profile)
	}
}

func TestGetAgentProfileResolvesExistingBuiltInAgent(t *testing.T) {
	profile, ok := getAgentProfile(&AppConfig{}, agentOpencode)

	if !ok {
		t.Fatal("getAgentProfile ok = false, want true")
	}
	if profile.ClientProtocol != protocolOpenAIChat {
		t.Fatalf("ClientProtocol = %q, want %q", profile.ClientProtocol, protocolOpenAIChat)
	}
}
