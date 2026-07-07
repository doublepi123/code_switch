package main

import (
	"fmt"
	"strings"
)

type ConnectionMode string

const (
	connectionModeDirect ConnectionMode = "direct"
	connectionModeProxy  ConnectionMode = "proxy"
)

type ConnectionPlan struct {
	Mode             ConnectionMode
	Agent            AgentName
	Provider         string
	ClientProtocol   ProviderProtocol
	UpstreamProtocol ProviderProtocol
	Endpoint         ProtocolEndpoint
}

type ProtocolEndpoint struct {
	BaseURL string
	AuthEnv string
}

type AgentProfile struct {
	ClientProtocol          ProviderProtocol
	DirectProtocols         []ProviderProtocol
	ProxyUpstreamPreference []ProviderProtocol
}

var agentProfiles = map[AgentName]AgentProfile{
	agentClaude: {
		ClientProtocol:          protocolAnthropicMessages,
		DirectProtocols:         []ProviderProtocol{protocolAnthropicMessages},
		ProxyUpstreamPreference: []ProviderProtocol{protocolOpenAIResponses, protocolOpenAIChat, protocolAnthropicMessages},
	},
	agentCodex: {
		ClientProtocol:          protocolOpenAIResponses,
		DirectProtocols:         []ProviderProtocol{protocolOpenAIResponses, protocolOpenAIChat},
		ProxyUpstreamPreference: []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat, protocolOpenAIResponses},
	},
	agentOpencode: {
		ClientProtocol:          protocolOpenAIChat,
		DirectProtocols:         []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat},
		ProxyUpstreamPreference: []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat},
	},
}

func (p ProviderPreset) presetEndpoint(protocol ProviderProtocol) (ProtocolEndpoint, bool) {
	if endpoint, ok := p.Endpoints[protocol]; ok && strings.TrimSpace(endpoint.BaseURL) != "" {
		return endpoint, true
	}
	// BaseURL fallback only when Endpoints is nil/empty (legacy providers).
	// Providers that declare Endpoints explicitly (e.g. kimi-coding with only
	// openai-chat) must NOT be matched via the BaseURL fallback for
	// anthropic-messages — that would route proxy traffic to a non-existent
	// upstream.
	if len(p.Endpoints) == 0 && protocol == protocolAnthropicMessages && strings.TrimSpace(p.BaseURL) != "" {
		return ProtocolEndpoint{BaseURL: p.BaseURL, AuthEnv: p.AuthEnv}, true
	}
	return ProtocolEndpoint{}, false
}

func (p StoredProvider) providerProtocol() ProviderProtocol {
	protocol := ProviderProtocol(strings.TrimSpace(string(p.Protocol)))
	if protocol == "" {
		return protocolAnthropicMessages
	}
	return protocol
}

func resolveConnection(agent AgentName, provider string, preset ProviderPreset, via string) (ConnectionPlan, error) {
	profile, ok := agentProfiles[agent]
	if !ok {
		return ConnectionPlan{}, fmt.Errorf("unsupported agent %q", agent)
	}
	provider = canonicalProviderName(provider)
	via = strings.ToLower(strings.TrimSpace(via))
	switch via {
	case "", "auto":
		if plan, ok := resolveDirectConnection(agent, provider, preset, profile); ok {
			return plan, nil
		}
		if plan, ok := resolveProxyConnection(agent, provider, preset, profile); ok {
			return plan, nil
		}
	case "direct":
		if plan, ok := resolveDirectConnection(agent, provider, preset, profile); ok {
			return plan, nil
		}
		return ConnectionPlan{}, fmt.Errorf("agent %q 与 provider %q 无共同协议，跨协议必须通过代理路由", agent, provider)
	case "proxy":
		if plan, ok := resolveProxyConnection(agent, provider, preset, profile); ok {
			return plan, nil
		}
		return ConnectionPlan{}, fmt.Errorf("provider %q has no proxy-compatible endpoint for agent %q", provider, agent)
	default:
		return ConnectionPlan{}, fmt.Errorf("unknown connection mode %q (use auto, direct, or proxy)", via)
	}
	return ConnectionPlan{}, fmt.Errorf("provider %q has no compatible endpoint for agent %q", provider, agent)
}

func resolveDirectConnection(agent AgentName, provider string, preset ProviderPreset, profile AgentProfile) (ConnectionPlan, bool) {
	for _, protocol := range profile.DirectProtocols {
		endpoint, ok := preset.presetEndpoint(protocol)
		if !ok {
			continue
		}
		return ConnectionPlan{Mode: connectionModeDirect, Agent: agent, Provider: provider, ClientProtocol: profile.ClientProtocol, UpstreamProtocol: protocol, Endpoint: endpoint}, true
	}
	return ConnectionPlan{}, false
}

func resolveProxyConnection(agent AgentName, provider string, preset ProviderPreset, profile AgentProfile) (ConnectionPlan, bool) {
	for _, protocol := range profile.ProxyUpstreamPreference {
		endpoint, ok := preset.presetEndpoint(protocol)
		if !ok && len(preset.Endpoints) == 0 && strings.TrimSpace(preset.BaseURL) != "" {
			endpoint = ProtocolEndpoint{BaseURL: preset.BaseURL, AuthEnv: preset.AuthEnv}
			ok = true
		}
		if !ok {
			continue
		}
		if err := validateProxyAgentProtocol(string(agent), protocol); err != nil {
			continue
		}
		return ConnectionPlan{Mode: connectionModeProxy, Agent: agent, Provider: provider, ClientProtocol: profile.ClientProtocol, UpstreamProtocol: protocol, Endpoint: endpoint}, true
	}
	return ConnectionPlan{}, false
}
