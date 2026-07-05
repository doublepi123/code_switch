package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ProviderProtocol identifies the wire protocol spoken by an upstream
// provider and, when InboundPath is non-empty, by a local proxy client.
type ProviderProtocol string

const (
	protocolAnthropicMessages ProviderProtocol = "anthropic-messages"
	protocolOpenAIChat        ProviderProtocol = "openai-chat"
	protocolOpenAIResponses   ProviderProtocol = "openai-responses"
)

// ProtocolAdapter describes both sides of a protocol: optional inbound
// client parsing, upstream request/response translation, and client response
// rendering back through the inbound adapter.
type ProtocolAdapter interface {
	Name() ProviderProtocol
	InboundMethod() string
	InboundPath() string
	UpstreamPath() string
	ParseInboundRequest([]byte) (IRRequest, error)
	BuildUpstreamRequest(IRRequest) ([]byte, error)
	ParseUpstreamResponse([]byte, string) (IRResponse, error)
	WriteClientResponse(http.ResponseWriter, IRResponse, bool)
	ConfigureUpstreamRequest(*http.Request, string)
	CanProxyFrom(ProtocolAdapter) (bool, string)
}

type ProtocolRegistry struct {
	byName    map[ProviderProtocol]ProtocolAdapter
	byInbound map[string]ProtocolAdapter
}

func newProtocolRegistry(adapters ...ProtocolAdapter) *ProtocolRegistry {
	reg := &ProtocolRegistry{
		byName:    map[ProviderProtocol]ProtocolAdapter{},
		byInbound: map[string]ProtocolAdapter{},
	}
	for _, adapter := range adapters {
		reg.Register(adapter)
	}
	return reg
}

func defaultProtocolRegistry() *ProtocolRegistry {
	return newProtocolRegistry(
		anthropicMessagesAdapter{},
		openAIChatAdapter{},
		openAIResponsesAdapter{},
	)
}

func (r *ProtocolRegistry) Register(adapter ProtocolAdapter) {
	if r == nil || adapter == nil {
		return
	}
	r.byName[adapter.Name()] = adapter
	if adapter.InboundPath() != "" {
		r.byInbound[inboundKey(adapter.InboundMethod(), adapter.InboundPath())] = adapter
	}
}

func (r *ProtocolRegistry) Find(name string) (ProtocolAdapter, bool) {
	if r == nil {
		return nil, false
	}
	adapter, ok := r.byName[ProviderProtocol(strings.TrimSpace(name))]
	return adapter, ok
}

func (r *ProtocolRegistry) FindInbound(method, path string) (ProtocolAdapter, bool) {
	if r == nil {
		return nil, false
	}
	adapter, ok := r.byInbound[inboundKey(method, path)]
	return adapter, ok
}

func (r *ProtocolRegistry) SupportedNames() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.byName))
	for name := range r.byName {
		names = append(names, string(name))
	}
	sort.Strings(names)
	return names
}

func (r *ProtocolRegistry) SupportedInboundPaths() []string {
	if r == nil {
		return nil
	}
	paths := make([]string, 0, len(r.byInbound))
	for _, adapter := range r.byInbound {
		paths = append(paths, adapter.InboundPath())
	}
	sort.Strings(paths)
	return paths
}

func inboundKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + strings.TrimSpace(path)
}

type anthropicMessagesAdapter struct{}

func (anthropicMessagesAdapter) Name() ProviderProtocol { return protocolAnthropicMessages }
func (anthropicMessagesAdapter) InboundMethod() string  { return http.MethodPost }
func (anthropicMessagesAdapter) InboundPath() string    { return "/v1/messages" }
func (anthropicMessagesAdapter) UpstreamPath() string   { return "/v1/messages" }
func (anthropicMessagesAdapter) ParseInboundRequest(body []byte) (IRRequest, error) {
	return anthropicRequestToIR(body)
}
func (anthropicMessagesAdapter) BuildUpstreamRequest(req IRRequest) ([]byte, error) {
	return irToAnthropicRequest(req)
}
func (anthropicMessagesAdapter) ParseUpstreamResponse(body []byte, _ string) (IRResponse, error) {
	return anthropicResponseToIR(body)
}
func (anthropicMessagesAdapter) WriteClientResponse(w http.ResponseWriter, resp IRResponse, _ bool) {
	out, err := irToAnthropicResponse(resp)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
func (anthropicMessagesAdapter) ConfigureUpstreamRequest(req *http.Request, providerKey string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(proxyAnthropicVersionHeader, proxyAnthropicVersionValue)
	if providerKey != "" {
		req.Header.Set("Authorization", "Bearer "+providerKey)
	}
}
func (anthropicMessagesAdapter) CanProxyFrom(inbound ProtocolAdapter) (bool, string) {
	return true, ""
}

type openAIChatAdapter struct{}

func (openAIChatAdapter) Name() ProviderProtocol { return protocolOpenAIChat }
func (openAIChatAdapter) InboundMethod() string  { return http.MethodPost }
func (openAIChatAdapter) InboundPath() string    { return "/v1/chat/completions" }
func (openAIChatAdapter) UpstreamPath() string   { return "/v1/chat/completions" }
func (openAIChatAdapter) ParseInboundRequest(body []byte) (IRRequest, error) {
	return openAIChatRequestToIR(body)
}
func (openAIChatAdapter) BuildUpstreamRequest(req IRRequest) ([]byte, error) {
	return buildOpenAIChatUpstreamRequest(req)
}
func (openAIChatAdapter) ParseUpstreamResponse(body []byte, _ string) (IRResponse, error) {
	return parseOpenAIChatUpstreamResponse(body)
}
func (openAIChatAdapter) WriteClientResponse(w http.ResponseWriter, resp IRResponse, _ bool) {
	out, err := irToOpenAIChatResponse(resp)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
func (openAIChatAdapter) ConfigureUpstreamRequest(req *http.Request, providerKey string) {
	configureBearerJSONRequest(req, providerKey)
}
func (openAIChatAdapter) CanProxyFrom(ProtocolAdapter) (bool, string) { return true, "" }

type openAIResponsesAdapter struct{}

func (openAIResponsesAdapter) Name() ProviderProtocol { return protocolOpenAIResponses }
func (openAIResponsesAdapter) InboundMethod() string  { return http.MethodPost }
func (openAIResponsesAdapter) InboundPath() string    { return "/v1/responses" }
func (openAIResponsesAdapter) UpstreamPath() string   { return "/v1/responses" }
func (openAIResponsesAdapter) ParseInboundRequest(body []byte) (IRRequest, error) {
	return responsesRequestToIR(body)
}
func (openAIResponsesAdapter) BuildUpstreamRequest(req IRRequest) ([]byte, error) {
	return buildOpenAIResponsesUpstreamRequest(req)
}
func (openAIResponsesAdapter) ParseUpstreamResponse(body []byte, contentType string) (IRResponse, error) {
	return parseOpenAIResponsesUpstreamResponse(body, contentType)
}
func (openAIResponsesAdapter) WriteClientResponse(w http.ResponseWriter, resp IRResponse, stream bool) {
	if stream {
		writeResponsesSSE(w, resp)
		return
	}
	out, err := irToResponsesResponse(resp)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
func (openAIResponsesAdapter) ConfigureUpstreamRequest(req *http.Request, providerKey string) {
	configureBearerJSONRequest(req, providerKey)
}
func (openAIResponsesAdapter) CanProxyFrom(inbound ProtocolAdapter) (bool, string) {
	if inbound == nil {
		return false, fmt.Sprintf("upstream protocol %q requires a supported inbound protocol", protocolOpenAIResponses)
	}
	return true, ""
}

func configureBearerJSONRequest(req *http.Request, providerKey string) {
	req.Header.Set("Content-Type", "application/json")
	if providerKey != "" {
		req.Header.Set("Authorization", "Bearer "+providerKey)
	}
}
