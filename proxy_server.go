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

// ProviderProtocol identifies the wire protocol spoken by an upstream
// provider. The MVP proxy only implements the Anthropic Messages API as
// an upstream protocol; the OpenAI constants are declared so ProxyRoute
// values can carry them and future tasks can extend the dispatch in
// newProxyHandler without re-shaping the struct.
type ProviderProtocol string

const (
	protocolAnthropicMessages ProviderProtocol = "anthropic-messages"
	protocolOpenAIChat        ProviderProtocol = "openai-chat"
	protocolOpenAIResponses   ProviderProtocol = "openai-responses"
)

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
	LocalToken       string
}

// newProxyHandler returns an http.Handler that translates OpenAI
// Responses API requests on POST /v1/responses or Anthropic Messages API
// requests on POST /v1/messages into the route's upstream protocol.
func newProxyHandler(route ProxyRoute, providerKey string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The MVP only serves POST /v1/responses and POST /v1/messages. Method and path are
		// checked before auth so a misconfigured client surfaces the
		// routing error rather than a misleading 401.
		if r.Method != http.MethodPost {
			writeProxyError(w, http.StatusMethodNotAllowed,
				fmt.Sprintf("method %q is not supported (MVP serves POST /v1/responses or /v1/messages)", r.Method))
			return
		}
		if r.URL.Path != "/v1/responses" && r.URL.Path != "/v1/messages" {
			writeProxyError(w, http.StatusNotFound,
				fmt.Sprintf("path %q is not supported (MVP serves POST /v1/responses or /v1/messages)", r.URL.Path))
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

		var ir IRRequest
		if r.URL.Path == "/v1/responses" {
			ir, err = responsesRequestToIR(body)
		} else {
			ir, err = anthropicRequestToIR(body)
		}
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

		// Dispatch on upstream protocol. Only Anthropic Messages is
		// wired up in the MVP.
		switch route.UpstreamProtocol {
		case protocolAnthropicMessages:
			if r.URL.Path == "/v1/messages" {
				writeProxyError(w, http.StatusBadRequest, "anthropic messages inbound to anthropic upstream passthrough is not implemented in the MVP")
				return
			}
			serveAnthropicUpstream(w, r, route, providerKey, ir)
		case protocolOpenAIChat:
			serveOpenAIChatUpstream(w, r, route, providerKey, ir)
		case protocolOpenAIResponses:
			if r.URL.Path != "/v1/messages" {
				writeProxyError(w, http.StatusBadRequest, fmt.Sprintf("upstream protocol %q is supported for /v1/messages clients only in the MVP (supported for /v1/responses: %q, %q)", protocolOpenAIResponses, protocolAnthropicMessages, protocolOpenAIChat))
				return
			}
			serveOpenAIResponsesUpstream(w, r, route, providerKey, ir)
		default:
			writeProxyError(w, http.StatusBadRequest,
				fmt.Sprintf("upstream protocol %q is not supported in the MVP (supported: %q, %q)",
					route.UpstreamProtocol, protocolAnthropicMessages, protocolOpenAIChat))
		}
	})
}

func serveOpenAIChatUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, ir IRRequest) {
	upstreamBody, err := irToOpenAIChatRequest(ir)
	if err != nil {
		writeProxyError(w, http.StatusBadRequest,
			fmt.Sprintf("translate to openai chat request: %v", err))
		return
	}
	upstreamURL := openAIChatCompletionsURL(route.UpstreamBaseURL)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError,
			fmt.Sprintf("build upstream request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if providerKey != "" {
		req.Header.Set("Authorization", "Bearer "+providerKey)
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
	irResp, err := openAIChatResponseToIR(respBody)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("parse upstream response: %v", err))
		return
	}
	// The response is rendered back to the client in the SAME protocol family
	// as the inbound request: a /v1/messages (Anthropic) client gets an
	// Anthropic Messages payload, a /v1/responses (OpenAI Responses) client
	// gets an OpenAI Responses payload (JSON or SSE). This keeps the response
	// shape consistent with what the client sent rather than always forcing
	// the OpenAI Responses shape onto Anthropic callers.
	if r.URL.Path == "/v1/messages" {
		// Anthropic SSE upstream is not implemented in the MVP, and the
		// Anthropic inbound adapter already rejects stream:true requests,
		// so ir.Stream is always false here. Render the non-streaming
		// Anthropic Messages payload.
		out, err := irToAnthropicResponse(irResp)
		if err != nil {
			writeProxyError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
		return
	}
	if ir.Stream {
		writeResponsesSSE(w, irResp)
		return
	}
	out, err := irToResponsesResponse(irResp)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func serveOpenAIResponsesUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, ir IRRequest) {
	upstreamIR := ir
	// Some Responses-compatible upstreams (including the new_api_gpt
	// endpoint used by OpenCode here) require stream=true. The proxy still
	// returns a non-streaming Anthropic response to the Claude client by
	// aggregating the upstream Responses SSE stream below.
	upstreamIR.Stream = true
	upstreamBody, err := irToResponsesRequest(upstreamIR)
	if err != nil {
		writeProxyError(w, http.StatusBadRequest,
			fmt.Sprintf("translate to responses request: %v", err))
		return
	}
	upstreamURL := openAIResponsesURL(route.UpstreamBaseURL)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError,
			fmt.Sprintf("build upstream request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if providerKey != "" {
		req.Header.Set("Authorization", "Bearer "+providerKey)
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
	var irResp IRResponse
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		irResp, err = responsesStreamToIR(respBody)
	} else {
		irResp, err = responsesResponseToIR(respBody)
	}
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("parse upstream response: %v", err))
		return
	}
	out, err := irToAnthropicResponse(irResp)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func openAIResponsesURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, "/v1") || strings.HasSuffix(lower, "/v4") {
		return base + "/responses"
	}
	return base + "/v1/responses"
}

func openAIChatCompletionsURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	lower := strings.ToLower(base)
	if strings.HasSuffix(lower, "/v1") || strings.HasSuffix(lower, "/v4") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
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

// serveAnthropicUpstream translates the IR into an Anthropic Messages
// API request, forwards it to the upstream, and renders the upstream
// response back as an OpenAI Responses payload. Upstream non-2xx
// responses are passed through verbatim (status + body) so the client
// sees the provider's native error shape rather than a generic one.
func serveAnthropicUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, ir IRRequest) {
	upstreamBody, err := irToAnthropicRequest(ir)
	if err != nil {
		// Should not happen after responsesRequestToIR validation, but
		// surface it as a 400 rather than panicking: the IR content is
		// derived from client input and must never crash the proxy.
		writeProxyError(w, http.StatusBadRequest,
			fmt.Sprintf("translate to anthropic request: %v", err))
		return
	}

	// Trim any trailing slash(es) so the route may be configured with
	// or without one and still produce a clean upstream URL.
	upstreamURL := strings.TrimRight(route.UpstreamBaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(upstreamBody))
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError,
			fmt.Sprintf("build upstream request: %v", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// Anthropic Messages API rejects requests that omit the
	// anthropic-version header. Sending it explicitly here keeps the
	// proxy behaviour deterministic rather than depending on the
	// upstream's default and avoids opaque upstream 400s.
	req.Header.Set(proxyAnthropicVersionHeader, proxyAnthropicVersionValue)
	if providerKey != "" {
		req.Header.Set("Authorization", "Bearer "+providerKey)
	}

	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("upstream request: %v", err))
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Cap the upstream response body so a runaway upstream cannot
	// exhaust memory. A normal Responses/Anthropic payload is far
	// smaller than proxyMaxUpstreamBodyBytes; exceeding it surfaces as
	// an explicit 502 rather than an OOM.
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

	// Non-2xx: pass the upstream status and body through verbatim so
	// the client sees the provider's error shape (e.g. Anthropic's
	// {"type":"error",...}) rather than a generic proxy error.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(respBody)
		return
	}

	irResp, err := anthropicResponseToIR(respBody)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("parse upstream response: %v", err))
		return
	}

	// Streaming clients: the proxy still calls the upstream
	// non-streaming (it does not implement Anthropic SSE) and then
	// wraps the completed IRResponse as a minimal OpenAI Responses
	// SSE event stream. Non-streaming clients get the standard JSON
	// payload.
	if ir.Stream {
		writeResponsesSSE(w, irResp)
		return
	}

	out, err := irToResponsesResponse(irResp)
	if err != nil {
		writeProxyError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
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
