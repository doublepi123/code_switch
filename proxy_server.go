package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// proxyMaxRequestBodyBytes caps the size of a client request body the
// proxy will accept. It protects against unbounded ReadAll from
// ill-behaved or hostile clients. 10 MiB comfortably covers any
// reasonable single chat turn; larger uploads are not part of the MVP.
const proxyMaxRequestBodyBytes = 10 << 20

// proxyMaxUpstreamBodyBytes caps the size of an upstream response body
// the proxy will buffer. A non-streaming Responses/Anthropic payload is
// always far smaller than this; a response that exceeds it almost
// always indicates a misconfigured upstream or an error page, and
// truncating it surfaces as an explicit 502 instead of an OOM.
const proxyMaxUpstreamBodyBytes = 10 << 20

// proxyHTTPClient is the package-level client used for all upstream
// calls. Using a shared client (instead of http.DefaultClient) gives
// the proxy a bounded total request timeout so a wedged upstream cannot
// hold a handler goroutine forever.
var proxyHTTPClient = &http.Client{Timeout: 60 * time.Second}

// proxyAnthropicVersionHeader is the version string the proxy sends to
// every Anthropic Messages upstream. Anthropic's API rejects requests
// without an explicit anthropic-version header, so omitting it would
// surface as opaque upstream 400s.
const proxyAnthropicVersionHeader = "anthropic-version"

// proxyAnthropicVersionValue is the pinned Anthropic Messages API
// version the proxy speaks upstream. Pinning the version keeps proxy
// behaviour deterministic as Anthropic publishes new versions.
const proxyAnthropicVersionValue = "2023-06-01"

// ProxyRoute describes a single local -> upstream mapping served by the
// proxy. Provider is the configured provider name (informational), Model
// overrides the model in the incoming request so the upstream always
// receives the route's canonical model, ModelMappings (optional) rewrites
// the incoming client model name to a specific upstream model (with a
// "default" entry used as a fallback for unmapped client models),
// UpstreamProtocol selects the adapter, UpstreamBaseURL is the provider
// origin (with or without a trailing slash), and LocalToken is the bearer
// token clients must present. When LocalToken is empty the proxy performs
// no auth check, which is convenient for local-only operation.
type ProxyRoute struct {
	Provider         string
	Model            string
	ModelMappings    map[string]string
	UpstreamProtocol ProviderProtocol
	UpstreamBaseURL  string
	UpstreamAuthEnv  string
	LocalToken       string
}

type proxyServedRoute struct {
	Agent          string
	ClientProtocol ProviderProtocol
	Route          ProxyRoute
	ProviderKey    string
}

// newProxyHandler returns an http.Handler that translates OpenAI
// Responses API requests on POST /v1/responses or Anthropic Messages API
// requests on POST /v1/messages into the route's upstream protocol.
func newProxyHandler(route ProxyRoute, providerKey string) http.Handler {
	return newProxyHandlerWithRegistry(route, providerKey, defaultProtocolRegistry())
}

func newProxyHandlerWithRegistry(route ProxyRoute, providerKey string, registry *ProtocolRegistry) http.Handler {
	if registry == nil {
		registry = defaultProtocolRegistry()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Method and path are checked before auth so a misconfigured client
		// surfaces the routing error rather than a misleading 401.
		if r.Method != http.MethodPost {
			writeProxyError(w, http.StatusMethodNotAllowed,
				fmt.Sprintf("method %q is not supported (MVP serves POST %s)", r.Method, strings.Join(registry.SupportedInboundPaths(), " or ")))
			return
		}
		inboundAdapter, ok := registry.FindInbound(r.Method, r.URL.Path)
		if !ok {
			writeProxyError(w, http.StatusNotFound,
				fmt.Sprintf("path %q is not supported (MVP serves POST %s)", r.URL.Path, strings.Join(registry.SupportedInboundPaths(), " or ")))
			return
		}

		// LocalToken auth: when set the request must carry a matching
		// bearer token. An absent or mismatched token is a 401.
		if route.LocalToken != "" {
			if !verifyBearerToken(r, route.LocalToken) {
				writeProxyError(w, http.StatusUnauthorized,
					"unauthorized: missing or invalid local token")
				return
			}
		}

		// Cap the client request body before reading so an unbounded
		// ReadAll cannot exhaust memory. MaxBytesReader also arranges
		// for the underlying http machinery to surface a 413 to the
		// client if the limit is exceeded, but here we always read it
		// ourselves and translate any error into our JSON 400/502
		// shape, so the limit simply bounds how much we buffer.
		r.Body = http.MaxBytesReader(w, r.Body, proxyMaxRequestBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeProxyError(w, http.StatusBadRequest,
				fmt.Sprintf("read request body: %v", err))
			return
		}
		if err := r.Body.Close(); err != nil {
			writeProxyError(w, http.StatusBadRequest,
				fmt.Sprintf("close request body: %v", err))
			return
		}

		upstreamAdapter, ok := registry.Find(string(route.UpstreamProtocol))
		if !ok {
			writeProxyError(w, http.StatusBadRequest,
				fmt.Sprintf("upstream protocol %q is not supported in the MVP (supported: %s)",
					route.UpstreamProtocol, strings.Join(registry.SupportedNames(), ", ")))
			return
		}
		if inboundAdapter.Name() == upstreamAdapter.Name() && supportsSameProtocolPassthrough(inboundAdapter.Name()) {
			if handled := serveSameProtocolPassthrough(w, r, route, providerKey, body, upstreamAdapter); handled {
				return
			}
		}

		ir, err := inboundAdapter.ParseInboundRequest(body)
		if err != nil {
			writeProxyError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Resolve the upstream model. Order of precedence:
		//   1. route.ModelMappings[incomingModel] — explicit client-model
		//      rewrite (e.g. "sonnet" -> "glm-5.2").
		//   2. route.ModelMappings["default"] — fallback for any client
		//      model not explicitly mapped, including an empty one.
		//   3. route.Model — the route's canonical model, used when no
		//      mappings are configured at all (preserves the original
		//      proxy behaviour).
		// An empty incoming model is treated like any other unmapped
		// model: it falls through to "default" then route.Model. The
		// inbound adapters (responsesRequestToIR, anthropicRequestToIR)
		// intentionally allow an empty model to reach this resolution
		// layer rather than short-circuiting with a 400.
		if upstream, ok := resolveProxyModel(ir.Model, route); ok {
			ir.Model = upstream
		} else {
			ir.Model = route.Model
		}
		// After resolution the upstream model must be non-empty; if the
		// route itself has no Model and no mapping resolved, we cannot
		// forward a meaningful request upstream.
		if ir.Model == "" {
			writeProxyError(w, http.StatusBadRequest,
				"model must not be empty and no route model or \"default\" mapping is configured")
			return
		}

		if ok, message := upstreamAdapter.CanProxyFrom(inboundAdapter); !ok {
			writeProxyError(w, http.StatusBadRequest, message)
			return
		}
		serveProtocolUpstream(w, r, route, providerKey, ir, inboundAdapter, upstreamAdapter)
	})
}

func newProxyMultiRouteHandler(routes []proxyServedRoute, registry *ProtocolRegistry) http.Handler {
	if registry == nil {
		registry = defaultProtocolRegistry()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selected, ok := findProxyRouteByBearerToken(r, routes)
		if !ok {
			writeProxyError(w, http.StatusUnauthorized, "unauthorized: missing or invalid local token")
			return
		}
		clientAdapter, ok := registry.Find(string(selected.ClientProtocol))
		if !ok || clientAdapter.InboundPath() == "" || r.URL.Path != clientAdapter.InboundPath() {
			writeProxyError(w, http.StatusNotFound,
				fmt.Sprintf("path %q is not supported for agent %q", r.URL.Path, selected.Agent))
			return
		}
		newProxyHandlerWithRegistry(selected.Route, selected.ProviderKey, registry).ServeHTTP(w, r)
	})
}

func findProxyRouteByBearerToken(r *http.Request, routes []proxyServedRoute) (proxyServedRoute, bool) {
	token := bearerTokenFromRequest(r)
	if token == "" {
		return proxyServedRoute{}, false
	}
	match := -1
	for i, route := range routes {
		expected := route.Route.LocalToken
		if expected == "" || len(token) != len(expected) {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1 {
			match = i
			break // tokens are unique; first match wins
		}
	}
	if match < 0 {
		return proxyServedRoute{}, false
	}
	return routes[match], true
}

func bearerTokenFromRequest(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func supportsSameProtocolPassthrough(protocol ProviderProtocol) bool {
	switch protocol {
	case protocolAnthropicMessages, protocolOpenAIChat, protocolOpenAIResponses:
		return true
	default:
		return false
	}
}

func serveSameProtocolPassthrough(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, body []byte, upstreamAdapter ProtocolAdapter) bool {
	model, stream, err := passthroughRequestModelAndStream(body)
	if err != nil {
		writeProxyError(w, http.StatusBadRequest, err.Error())
		return true
	}
	if stream {
		return false
	}
	upstreamModel, ok := resolveProxyModel(model, route)
	if !ok {
		upstreamModel = route.Model
	}
	if upstreamModel == "" {
		writeProxyError(w, http.StatusBadRequest, "model must not be empty and no route model or \"default\" mapping is configured")
		return true
	}
	if err := validatePassthroughTools(upstreamAdapter.Name(), body); err != nil {
		writeProxyError(w, http.StatusBadRequest, err.Error())
		return true
	}
	upstreamBody, err := rewritePassthroughModel(body, upstreamModel)
	if err != nil {
		writeProxyError(w, http.StatusBadRequest, err.Error())
		return true
	}
	serveRawProtocolUpstream(w, r, route, providerKey, upstreamBody, upstreamAdapter)
	return true
}

func validatePassthroughTools(protocol ProviderProtocol, body []byte) error {
	switch protocol {
	case protocolOpenAIChat:
		var raw struct {
			Tools []struct {
				Type     string `json:"type"`
				Function *struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("passthrough request: parse tools: %w", err)
		}
		for i, tool := range raw.Tools {
			if tool.Type != "" && tool.Type != "function" {
				continue
			}
			if tool.Function == nil || strings.TrimSpace(tool.Function.Name) == "" {
				return fmt.Errorf("passthrough request: tool %d function.name must not be empty", i)
			}
		}
	case protocolAnthropicMessages:
		var raw struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return fmt.Errorf("passthrough request: parse tools: %w", err)
		}
		for i, tool := range raw.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				return fmt.Errorf("passthrough request: tool %d name must not be empty", i)
			}
		}
	}
	return nil
}

func passthroughRequestModelAndStream(body []byte) (string, bool, error) {
	var raw struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false, fmt.Errorf("passthrough request: parse body: %w", err)
	}
	return raw.Model, raw.Stream, nil
}

func rewritePassthroughModel(body []byte, model string) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("passthrough request: parse body object: %w", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("passthrough request: body must be a JSON object")
	}
	modelJSON, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("passthrough request: marshal model: %w", err)
	}
	raw["model"] = modelJSON
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("passthrough request: marshal body: %w", err)
	}
	return out, nil
}

func serveRawProtocolUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, upstreamBody []byte, upstreamAdapter ProtocolAdapter) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL(route.UpstreamBaseURL, upstreamAdapter.UpstreamPath()), bytes.NewReader(upstreamBody))
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError,
			fmt.Sprintf("build upstream request: %v", err))
		return
	}
	upstreamAdapter.ConfigureUpstreamRequest(req, providerKey)
	configureProxyUpstreamAuth(req, route, upstreamAdapter.Name(), providerKey)
	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("upstream request: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	limitedUpstream := io.LimitReader(resp.Body, proxyMaxUpstreamBodyBytes+1)
	respBody, err := io.ReadAll(limitedUpstream)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("read upstream response: %v", err))
		return
	}
	if int64(len(respBody)) > proxyMaxUpstreamBodyBytes {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("upstream response exceeds limit of %d bytes", proxyMaxUpstreamBodyBytes))
		return
	}
	copyProxyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func copyProxyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func serveProtocolUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, ir IRRequest, inboundAdapter, upstreamAdapter ProtocolAdapter) {
	upstreamBody, err := upstreamAdapter.BuildUpstreamRequest(ir)
	if err != nil {
		writeProxyError(w, http.StatusBadRequest,
			fmt.Sprintf("translate to %s request: %v", upstreamAdapter.Name(), err))
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL(route.UpstreamBaseURL, upstreamAdapter.UpstreamPath()), bytes.NewReader(upstreamBody))
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError,
			fmt.Sprintf("build upstream request: %v", err))
		return
	}
	upstreamAdapter.ConfigureUpstreamRequest(req, providerKey)
	configureProxyUpstreamAuth(req, route, upstreamAdapter.Name(), providerKey)
	if ir.Stream {
		serveStreamingProtocolUpstream(w, r, req, inboundAdapter, upstreamAdapter)
		return
	}
	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("upstream request: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	limitedUpstream := io.LimitReader(resp.Body, proxyMaxUpstreamBodyBytes+1)
	respBody, err := io.ReadAll(limitedUpstream)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("read upstream response: %v", err))
		return
	}
	if int64(len(respBody)) > proxyMaxUpstreamBodyBytes {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("upstream response exceeds limit of %d bytes", proxyMaxUpstreamBodyBytes))
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return
	}
	irResp, err := upstreamAdapter.ParseUpstreamResponse(respBody, resp.Header.Get("Content-Type"))
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("parse upstream response: %v", err))
		return
	}
	inboundAdapter.WriteClientResponse(w, irResp, ir.Stream)
}

func configureProxyUpstreamAuth(req *http.Request, route ProxyRoute, protocol ProviderProtocol, providerKey string) {
	if strings.TrimSpace(providerKey) == "" {
		return
	}
	if protocol == protocolAnthropicMessages {
		if proxyRouteAuthEnv(route) == "" {
			req.Header.Set("x-api-key", providerKey)
			return
		}
		req.Header.Set("Authorization", "Bearer "+providerKey)
		return
	}
	if req.Header.Get("Authorization") == "" {
		req.Header.Del("Authorization")
		req.Header.Set("Authorization", "Bearer "+providerKey)
	}
}

func proxyRouteAuthEnv(route ProxyRoute) string {
	if authEnv := strings.TrimSpace(route.UpstreamAuthEnv); authEnv != "" {
		return authEnv
	}
	preset, ok := providerPresets[canonicalProviderName(route.Provider)]
	if !ok {
		return "ANTHROPIC_AUTH_TOKEN"
	}
	if endpoint, ok := preset.presetEndpoint(route.UpstreamProtocol); ok && strings.TrimSpace(endpoint.AuthEnv) != "" {
		return strings.TrimSpace(endpoint.AuthEnv)
	}
	return strings.TrimSpace(preset.AuthEnv)
}

func serveStreamingProtocolUpstream(w http.ResponseWriter, r *http.Request, req *http.Request, inboundAdapter, upstreamAdapter ProtocolAdapter) {
	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("upstream request: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limitedUpstream := io.LimitReader(resp.Body, proxyMaxUpstreamBodyBytes+1)
		respBody, readErr := io.ReadAll(limitedUpstream)
		if readErr != nil {
			writeProxyError(w, http.StatusBadGateway,
				fmt.Sprintf("read upstream response: %v", readErr))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return
	}
	decoder, err := streamDecoderForProtocol(upstreamAdapter.Name())
	if err != nil {
		writeProxyError(w, http.StatusBadGateway, err.Error())
		return
	}
	encoder, err := streamEncoderForProtocol(inboundAdapter.Name())
	if err != nil {
		writeProxyError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if err := decoder.DecodeStream(resp.Body, func(event StreamEvent) error {
		return encoder.EncodeStreamEvent(w, event)
	}); err != nil && r.Context().Err() == nil {
		_ = encoder.EncodeStreamEvent(w, StreamEvent{Type: streamEventError, Err: err.Error()})
	}
}

func upstreamURL(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	lower := strings.ToLower(base)
	if path != "/v1/messages" && (strings.HasSuffix(lower, "/v1") || strings.HasSuffix(lower, "/v4")) {
		return base + strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

// resolveProxyModel applies the route's ModelMappings to the incoming
// client model. It returns the upstream model name and true when a
// mapping applies (either an explicit client-model match or the
// "default" fallback). It returns false when no mapping is configured
// (route.ModelMappings is nil/empty or neither the client model nor
// "default" is present), so the caller falls back to route.Model.
func resolveProxyModel(incoming string, route ProxyRoute) (string, bool) {
	if len(route.ModelMappings) == 0 {
		return "", false
	}
	if upstream, ok := route.ModelMappings[incoming]; ok {
		return upstream, true
	}
	if upstream, ok := route.ModelMappings["default"]; ok {
		return upstream, true
	}
	return "", false
}

// verifyBearerToken reports whether the request carries an
// "Authorization: Bearer <want>" header. The scheme prefix is matched
// case-insensitively (per RFC 7235) but the token itself must match
// exactly: surrounding whitespace on the token is NOT tolerated, and
// the comparison uses crypto/subtle.ConstantTimeCompare to avoid timing
// side channels. The header must split into exactly two fields (scheme
// and token); anything else is a mismatch. An absent or too-short header
// is a mismatch.
func verifyBearerToken(r *http.Request, want string) bool {
	if want == "" {
		// A non-empty expected token is required; an empty want would
		// let ConstantTimeCompare succeed for an empty presented token,
		// which is never a valid credential.
		return false
	}
	h := r.Header.Get("Authorization")
	// Require exactly two whitespace-separated fields: the scheme and
	// the token. strings.Fields collapses the separator run, so one or
	// more spaces/tabs between "Bearer" and the token are accepted, but
	// whitespace embedded inside the token yields three fields and is
	// rejected.
	fields := strings.Fields(h)
	if len(fields) != 2 {
		return false
	}
	if !strings.EqualFold(fields[0], "Bearer") {
		return false
	}
	// strings.Fields also strips trailing whitespace, so "Bearer abc "
	// would otherwise look like a clean two-field header. Enforce that
	// the raw header ends with the presented token — only true when the
	// token body carries no trailing whitespace.
	if !strings.HasSuffix(h, fields[1]) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(fields[1]), []byte(want)) == 1
}

// writeProxyError emits a small JSON error object. Proxy error bodies
// are always JSON so clients can parse them uniformly regardless of
// which stage of the pipeline failed.
func writeProxyError(w http.ResponseWriter, status int, message string) {
	payload := map[string]any{
		"error": map[string]string{"message": message},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		// Marshalling a map of plain strings cannot fail in practice;
		// fall back to a literal body so the client still receives a
		// syntactically valid JSON document.
		data = []byte(`{"error":{"message":"proxy error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// writeSSEEvent writes a single Server-Sent Events frame to w. Each
// frame consists of one or more "field: value\n" lines followed by a
// blank line that delimits the event. If w implements http.Flusher the
// buffer is flushed immediately so the client receives the event
// without waiting for the next one. A write error is swallowed because
// once the status and headers have been sent the proxy cannot recover
// the connection — the client will simply see a truncated stream.
func writeSSEEvent(w http.ResponseWriter, eventType string, data []byte) {
	var b strings.Builder
	if eventType != "" {
		b.WriteString("event: ")
		b.WriteString(eventType)
		b.WriteString("\n")
	}
	b.WriteString("data: ")
	b.Write(data)
	b.WriteString("\n\n")
	_, _ = io.WriteString(w, b.String())
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeResponsesSSE renders an IRResponse as a minimal OpenAI Responses
// SSE event stream. The proxy obtains the full response from the
// upstream non-streaming and then synthesises the SSE envelope
// client-side; it does not implement Anthropic SSE upstream. The event
// sequence is the minimum Codex consumes for a simple text response:
//
//   - response.created          — the response object in "in_progress".
//   - response.output_item.added — the message item is added.
//   - response.content_part.added — the output_text part is added.
//   - response.output_text.delta — the full assistant text as a single delta.
//   - response.output_text.done  — the output_text part is finished.
//   - response.output_item.done  — the message item is finished.
//   - response.completed         — the completed response object.
//
// Each event's data field is a JSON object whose "type" matches the
// event name (matching the OpenAI Responses streaming wire shape).
func writeResponsesSSE(w http.ResponseWriter, resp IRResponse) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	respID := responsesResponseID(resp.ID)
	msgID := responsesMessageID(resp.ID)
	status := responsesStatusFor(resp.StopReason)

	// response.created: the response object starts in_progress with an
	// empty output array.
	writeSSEEvent(w, "response.created", mustMarshalJSON(map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":     respID,
			"object": "response",
			"status": "in_progress",
			"model":  resp.Model,
			"output": []any{},
		},
	}))

	// response.output_item.added: the message item is added.
	writeSSEEvent(w, "response.output_item.added", mustMarshalJSON(map[string]any{
		"type":         "response.output_item.added",
		"output_index": 0,
		"item": map[string]any{
			"type":    "message",
			"role":    "assistant",
			"status":  "in_progress",
			"id":      msgID,
			"content": []any{},
		},
	}))

	// response.content_part.added: the output_text part is added to the
	// message item.
	writeSSEEvent(w, "response.content_part.added", mustMarshalJSON(map[string]any{
		"type":          "response.content_part.added",
		"output_index":  0,
		"content_index": 0,
		"part": map[string]any{
			"type": responsesOutputTextType,
			"text": "",
		},
	}))

	// response.output_text.delta: the full assistant text is delivered
	// as a single delta. (A future streaming implementation could chunk
	// this; the MVP emits one delta carrying the whole text.)
	writeSSEEvent(w, "response.output_text.delta", mustMarshalJSON(map[string]any{
		"type":          "response.output_text.delta",
		"output_index":  0,
		"content_index": 0,
		"delta":         resp.Text,
	}))

	// response.output_text.done: the output_text part is finished.
	writeSSEEvent(w, "response.output_text.done", mustMarshalJSON(map[string]any{
		"type":          "response.output_text.done",
		"output_index":  0,
		"content_index": 0,
		"text":          resp.Text,
	}))

	// response.output_item.done: the message item is finished with its
	// full content.
	writeSSEEvent(w, "response.output_item.done", mustMarshalJSON(map[string]any{
		"type":         "response.output_item.done",
		"output_index": 0,
		"item": map[string]any{
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"id":     msgID,
			"content": []map[string]string{{
				"type": responsesOutputTextType,
				"text": resp.Text,
			}},
		},
	}))

	// response.completed: the final response object mirrors the
	// non-streaming payload shape (minus the SSE-specific event types).
	completed := map[string]any{
		"id":     respID,
		"object": "response",
		"status": status,
		"model":  resp.Model,
		"output": []map[string]any{{
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"id":     msgID,
			"content": []map[string]string{{
				"type": responsesOutputTextType,
				"text": resp.Text,
			}},
		}},
		"output_text": resp.Text,
	}
	if resp.Usage != nil {
		completed["usage"] = map[string]int{
			"input_tokens":  resp.Usage.InputTokens,
			"output_tokens": resp.Usage.OutputTokens,
			"total_tokens":  resp.Usage.TotalTokens,
		}
	}
	writeSSEEvent(w, "response.completed", mustMarshalJSON(map[string]any{
		"type":     "response.completed",
		"response": completed,
	}))
}

// mustMarshalJSON encodes v as JSON. It is used only for static,
// in-memory structures built from already-validated IR fields, so a
// marshal failure would indicate a programming error rather than a
// recoverable runtime condition; panicking keeps the call sites clean.
func mustMarshalJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("proxy SSE: marshal failed: %v", err))
	}
	return data
}
