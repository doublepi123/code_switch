package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

// proxyHTTPClient is the package-level client used for buffered upstream
// calls. Using a shared client (instead of http.DefaultClient) gives
// non-streaming proxy requests a bounded total request timeout so a wedged
// upstream cannot hold a handler goroutine forever.
var proxyHTTPClient = &http.Client{Timeout: 60 * time.Second}

// proxyStreamingHTTPClient is used for SSE upstream calls. It deliberately has
// no total request timeout: long-running model streams can be idle for more
// than proxyHTTPClient's buffered-request deadline between chunks. The inbound
// request context still cancels the upstream request when the client disconnects
// or the server shuts down.
var proxyStreamingHTTPClient = &http.Client{}

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
	ProviderKey      string
	Model            string
	ModelMappings    map[string]string
	UpstreamProtocol ProviderProtocol
	UpstreamBaseURL  string
	UpstreamAuthEnv  string
	LocalToken       string
	Fallback         *ProxyRoute
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
	return newProxyHandlerWithRegistryAndLogger(route, providerKey, registry, nil)
}

func newProxyHandlerWithRegistryAndLogger(route ProxyRoute, providerKey string, registry *ProtocolRegistry, logger *proxyLogger) http.Handler {
	return newProxyHandlerWithRegistryLoggerAndAgent(route, providerKey, registry, logger, "")
}

func newProxyHandlerWithRegistryLoggerAndAgent(route ProxyRoute, providerKey string, registry *ProtocolRegistry, logger *proxyLogger, agent string) http.Handler {
	if registry == nil {
		registry = defaultProtocolRegistry()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &proxyLoggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK, agent: agent}
		defer func() {
			logger.logRequest(proxyLogEntry{
				Timestamp:    start.UTC(),
				Kind:         proxyLogKindRequest,
				Agent:        lw.agent,
				Method:       r.Method,
				Path:         r.URL.Path,
				Provider:     route.Provider,
				Model:        route.Model,
				StatusCode:   lw.statusCode,
				Error:        lw.errorMessage,
				DurationMs:   time.Since(start).Milliseconds(),
				InputTokens:  lw.inputTokens,
				OutputTokens: lw.outputTokens,
				TotalTokens:  lw.totalTokens,
			})
		}()
		w = lw
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
			// http.MaxBytesReader produces a *http.MaxBytesError when the body
			// exceeds proxyMaxRequestBodyBytes. Map that to 413 (Payload Too
			// Large) instead of the generic 400 so clients can distinguish
			// "your request is too big" from "your JSON is malformed" without
			// parsing the error message.
			if _, ok := err.(*http.MaxBytesError); ok {
				writeProxyError(w, http.StatusRequestEntityTooLarge,
					fmt.Sprintf("request body exceeds %d bytes", proxyMaxRequestBodyBytes))
				return
			}
			writeProxyError(w, http.StatusBadRequest,
				fmt.Sprintf("read request body: %v", err))
			return
		}
		// r.Body.Close failure after io.ReadAll is almost always harmless
		// (the connection is fine, the body is already fully buffered in
		// memory). Surface it as a debug-level note rather than a 400: the
		// client's request is fully understood, and a 400 here would
		// mask a successful translation.
		if err := r.Body.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "proxy: close request body: %v\n", err)
		}

		upstreamAdapter, ok := registry.Find(string(route.UpstreamProtocol))
		if !ok {
			writeProxyError(w, http.StatusBadRequest,
				fmt.Sprintf("upstream protocol %q is not supported in the MVP (supported: %s)",
					route.UpstreamProtocol, strings.Join(registry.SupportedNames(), ", ")))
			return
		}
		if inboundAdapter.Name() == upstreamAdapter.Name() && supportsSameProtocolPassthrough(inboundAdapter.Name()) {
			if handled := serveSameProtocolPassthrough(w, r, route, providerKey, body, upstreamAdapter, logger); handled {
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
		usage := serveProtocolUpstream(w, r, route, providerKey, ir, inboundAdapter, upstreamAdapter, logger)
		if usage != nil {
			lw.inputTokens = usage.InputTokens
			lw.outputTokens = usage.OutputTokens
			lw.totalTokens = usage.TotalTokens
			if lw.totalTokens == 0 {
				lw.totalTokens = usage.InputTokens + usage.OutputTokens
			}
		}
	})
}

type proxyLoggingResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	errorMessage string
	routed       bool
	agent        string
	inputTokens  int
	outputTokens int
	totalTokens  int
}

func (w *proxyLoggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *proxyLoggingResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *proxyLoggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *proxyLoggingResponseWriter) setProxyError(message string) {
	w.errorMessage = message
}

func newProxyMultiRouteHandler(routes []proxyServedRoute, registry *ProtocolRegistry) http.Handler {
	return newProxyMultiRouteHandlerWithLogger(routes, registry, nil)
}

func newProxyMultiRouteHandlerWithLogger(routes []proxyServedRoute, registry *ProtocolRegistry, logger *proxyLogger) http.Handler {
	if registry == nil {
		registry = defaultProtocolRegistry()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &proxyLoggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		defer func() {
			if lw.routed {
				return
			}
			logger.logRequest(proxyLogEntry{
				Timestamp:  start.UTC(),
				Kind:       proxyLogKindRequest,
				Method:     r.Method,
				Path:       r.URL.Path,
				RemoteAddr: r.RemoteAddr,
				StatusCode: lw.statusCode,
				Error:      lw.errorMessage,
				DurationMs: time.Since(start).Milliseconds(),
			})
		}()
		w = lw
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
		lw.routed = true
		newProxyHandlerWithRegistryLoggerAndAgent(selected.Route, selected.ProviderKey, registry, logger, selected.Agent).ServeHTTP(w, r)
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
	h := r.Header.Get("Authorization")
	// Mirror verifyBearerToken's strict 2-field split so the route selector
	// and the per-route handler agree on what counts as a valid bearer
	// token. strings.Fields collapses the separator run, so a header like
	// "Bearer  abc" (two spaces) is treated as one scheme + one token, but
	// "Bearer abc def" becomes three fields and is rejected.
	fields := strings.Fields(h)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	// Reject trailing whitespace: verifyBearerToken checks strings.HasSuffix
	// so a token like "abc " passes Fields but fails there. Matching that
	// check here keeps the route selector and the handler consistent.
	if !strings.HasSuffix(h, fields[1]) {
		return ""
	}
	return fields[1]
}

func supportsSameProtocolPassthrough(protocol ProviderProtocol) bool {
	switch protocol {
	case protocolAnthropicMessages, protocolOpenAIChat, protocolOpenAIResponses:
		return true
	default:
		return false
	}
}

func serveSameProtocolPassthrough(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, body []byte, upstreamAdapter ProtocolAdapter, logger *proxyLogger) bool {
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
	serveRawProtocolUpstream(w, r, route, providerKey, upstreamBody, upstreamAdapter, logger)
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
	case protocolOpenAIResponses:
		// Responses tools carry the name at the top level
		// (`{"type":"function","name":"..."}`), not inside a nested
		// `function` object. Validate the same invariant — empty name is
		// rejected — so the upstream provider never receives a tool that
		// would be rejected there anyway, and the proxy surfaces the error
		// at the request boundary instead of after a wasted upstream call.
		var respRaw struct {
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &respRaw); err != nil {
			return fmt.Errorf("passthrough request: parse tools: %w", err)
		}
		for i, tool := range respRaw.Tools {
			if tool.Type != "" && tool.Type != "function" {
				continue
			}
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

type proxyRawUpstreamResult struct {
	header     http.Header
	body       []byte
	statusCode int
	err        error
	usage      *IRUsage
}

func serveRawProtocolUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, upstreamBody []byte, upstreamAdapter ProtocolAdapter, logger *proxyLogger) {
	start := time.Now()
	result := doRawProtocolUpstream(r, route, providerKey, upstreamBody, upstreamAdapter)
	logRawUpstreamAttempt(logger, r, route, result, start)
	if isRetryableUpstreamError(result.statusCode, result.err) && route.Fallback != nil {
		fallbackKey, ok := fallbackProviderKey(*route.Fallback, route, providerKey)
		if !ok {
			writeRawProtocolResult(w, result)
			return
		}
		fallbackBody := upstreamBody
		if route.Fallback.Model != "" {
			if rewritten, err := rewritePassthroughModel(upstreamBody, route.Fallback.Model); err == nil {
				fallbackBody = rewritten
			}
		}
		fallbackStart := time.Now()
		result = doRawProtocolUpstream(r, *route.Fallback, fallbackKey, fallbackBody, upstreamAdapter)
		logRawUpstreamAttempt(logger, r, *route.Fallback, result, fallbackStart)
	}
	writeRawProtocolResult(w, result)
	if result.usage != nil {
		if lw, ok := w.(*proxyLoggingResponseWriter); ok {
			lw.inputTokens, lw.outputTokens, lw.totalTokens = proxyUsageTotals(result.usage)
		}
	}
}

func logRawUpstreamAttempt(logger *proxyLogger, r *http.Request, route ProxyRoute, result proxyRawUpstreamResult, start time.Time) {
	entry := proxyLogEntry{
		Timestamp:  start.UTC(),
		Kind:       proxyLogKindUpstream,
		Method:     r.Method,
		Path:       r.URL.Path,
		RemoteAddr: r.RemoteAddr,
		Provider:   route.Provider,
		Model:      route.Model,
		StatusCode: result.statusCode,
		Error:      upstreamResultError(result.err),
		DurationMs: time.Since(start).Milliseconds(),
	}
	entry.InputTokens, entry.OutputTokens, entry.TotalTokens = proxyUsageTotals(result.usage)
	logger.logRequest(entry)
}

func doRawProtocolUpstream(r *http.Request, route ProxyRoute, providerKey string, upstreamBody []byte, upstreamAdapter ProtocolAdapter) proxyRawUpstreamResult {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL(route.UpstreamBaseURL, upstreamAdapter.UpstreamPath()), bytes.NewReader(upstreamBody))
	if err != nil {
		return proxyRawUpstreamResult{statusCode: http.StatusInternalServerError, err: fmt.Errorf("build upstream request: %w", err)}
	}
	upstreamAdapter.ConfigureUpstreamRequest(req, providerKey)
	configureProxyUpstreamAuth(req, route, upstreamAdapter.Name(), providerKey)
	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		return proxyRawUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("upstream request: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	limitedUpstream := io.LimitReader(resp.Body, proxyMaxUpstreamBodyBytes+1)
	respBody, err := io.ReadAll(limitedUpstream)
	if err != nil {
		return proxyRawUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("read upstream response: %w", err)}
	}
	if int64(len(respBody)) > proxyMaxUpstreamBodyBytes {
		return proxyRawUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("upstream response exceeds limit of %d bytes", proxyMaxUpstreamBodyBytes)}
	}
	result := proxyRawUpstreamResult{statusCode: resp.StatusCode, header: resp.Header.Clone(), body: respBody}
	// Best-effort usage extraction for the stats log. The body is passed to
	// the client verbatim regardless, so a parse failure must not change the
	// response — it only means this attempt logs no tokens.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if irResp, parseErr := upstreamAdapter.ParseUpstreamResponse(respBody, resp.Header.Get("Content-Type")); parseErr == nil {
			result.usage = irResp.Usage
		}
	}
	return result
}

func writeRawProtocolResult(w http.ResponseWriter, result proxyRawUpstreamResult) {
	if result.err != nil {
		writeProxyError(w, result.statusCode, result.err.Error())
		return
	}
	copyProxyResponseHeaders(w.Header(), result.header)
	w.WriteHeader(result.statusCode)
	_, _ = w.Write(result.body)
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

type proxyProtocolUpstreamResult struct {
	header      http.Header
	body        []byte
	contentType string
	statusCode  int
	err         error
}

func serveProtocolUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, ir IRRequest, inboundAdapter, upstreamAdapter ProtocolAdapter, logger *proxyLogger) *IRUsage {
	start := time.Now()
	result, streamUsage := doProtocolUpstream(w, r, route, providerKey, ir, inboundAdapter, upstreamAdapter)
	if result == nil {
		// Streaming response was already written to the client. Log the
		// attempt (with any usage the decoder captured) so request stats
		// treat streamed calls like any other upstream call.
		logProtocolStreamSuccess(logger, r, route, streamUsage, start)
		return streamUsage
	}
	logProtocolUpstreamAttempt(logger, r, route, result, start)
	if isRetryableUpstreamError(result.statusCode, result.err) && route.Fallback != nil {
		fallbackIR := ir
		if route.Fallback.Model != "" {
			fallbackIR.Model = route.Fallback.Model
		}
		fallbackKey, ok := fallbackProviderKey(*route.Fallback, route, providerKey)
		if !ok {
			return writeProtocolResult(w, result, ir.Stream, inboundAdapter, upstreamAdapter)
		}
		fallbackStart := time.Now()
		result, streamUsage = doProtocolUpstream(w, r, *route.Fallback, fallbackKey, fallbackIR, inboundAdapter, upstreamAdapter)
		if result == nil {
			logProtocolStreamSuccess(logger, r, *route.Fallback, streamUsage, fallbackStart)
			return streamUsage
		}
		logProtocolUpstreamAttempt(logger, r, *route.Fallback, result, fallbackStart)
	}
	return writeProtocolResult(w, result, ir.Stream, inboundAdapter, upstreamAdapter)
}

// logProtocolStreamSuccess records a successful streamed upstream attempt.
// The client already received a 200 + SSE stream, so the entry always
// carries status 200; token usage is attached when the upstream emitted
// usage events.
func logProtocolStreamSuccess(logger *proxyLogger, r *http.Request, route ProxyRoute, usage *IRUsage, start time.Time) {
	entry := proxyLogEntry{
		Timestamp:  start.UTC(),
		Kind:       proxyLogKindUpstream,
		Method:     r.Method,
		Path:       r.URL.Path,
		RemoteAddr: r.RemoteAddr,
		Provider:   route.Provider,
		Model:      route.Model,
		StatusCode: http.StatusOK,
		DurationMs: time.Since(start).Milliseconds(),
	}
	entry.InputTokens, entry.OutputTokens, entry.TotalTokens = proxyUsageTotals(usage)
	logger.logRequest(entry)
}

// proxyUsageTotals flattens an IRUsage into log fields, deriving the total
// when the upstream did not report one.
func proxyUsageTotals(usage *IRUsage) (input, output, total int) {
	if usage == nil {
		return 0, 0, 0
	}
	total = usage.TotalTokens
	if total == 0 {
		total = usage.InputTokens + usage.OutputTokens
	}
	return usage.InputTokens, usage.OutputTokens, total
}

func logProtocolUpstreamAttempt(logger *proxyLogger, r *http.Request, route ProxyRoute, result *proxyProtocolUpstreamResult, start time.Time) {
	logger.logRequest(proxyLogEntry{
		Timestamp:  start.UTC(),
		Kind:       proxyLogKindUpstream,
		Method:     r.Method,
		Path:       r.URL.Path,
		RemoteAddr: r.RemoteAddr,
		Provider:   route.Provider,
		Model:      route.Model,
		StatusCode: result.statusCode,
		Error:      upstreamResultError(result.err),
		DurationMs: time.Since(start).Milliseconds(),
	})
}

func upstreamResultError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func doProtocolUpstream(w http.ResponseWriter, r *http.Request, route ProxyRoute, providerKey string, ir IRRequest, inboundAdapter, upstreamAdapter ProtocolAdapter) (*proxyProtocolUpstreamResult, *IRUsage) {
	upstreamBody, err := upstreamAdapter.BuildUpstreamRequest(ir)
	if err != nil {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusBadRequest, err: fmt.Errorf("translate to %s request: %w", upstreamAdapter.Name(), err)}, nil
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL(route.UpstreamBaseURL, upstreamAdapter.UpstreamPath()), bytes.NewReader(upstreamBody))
	if err != nil {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusInternalServerError, err: fmt.Errorf("build upstream request: %w", err)}, nil
	}
	upstreamAdapter.ConfigureUpstreamRequest(req, providerKey)
	configureProxyUpstreamAuth(req, route, upstreamAdapter.Name(), providerKey)
	if ir.Stream {
		return serveStreamingProtocolUpstream(w, r, req, inboundAdapter, upstreamAdapter)
	}
	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("upstream request: %w", err)}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	limitedUpstream := io.LimitReader(resp.Body, proxyMaxUpstreamBodyBytes+1)
	respBody, err := io.ReadAll(limitedUpstream)
	if err != nil {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("read upstream response: %w", err)}, nil
	}
	if int64(len(respBody)) > proxyMaxUpstreamBodyBytes {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("upstream response exceeds limit of %d bytes", proxyMaxUpstreamBodyBytes)}, nil
	}
	return &proxyProtocolUpstreamResult{statusCode: resp.StatusCode, header: resp.Header.Clone(), body: respBody, contentType: resp.Header.Get("Content-Type")}, nil
}

func writeProtocolResult(w http.ResponseWriter, result *proxyProtocolUpstreamResult, stream bool, inboundAdapter, upstreamAdapter ProtocolAdapter) *IRUsage {
	if result.err != nil {
		writeProxyError(w, result.statusCode, result.err.Error())
		return nil
	}
	if result.statusCode < 200 || result.statusCode >= 300 {
		// Forward upstream headers (excluding hop-by-hop) so the client sees
		// the same metadata the upstream sent — e.g. request-id, retry-after.
		copyProxyResponseHeaders(w.Header(), result.header)
		// Ensure a content type is set if the upstream omitted one; otherwise
		// the body might be served as application/octet-stream which clients
		// cannot parse as an API error.
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(result.statusCode)
		_, _ = w.Write(result.body)
		return nil
	}
	irResp, err := upstreamAdapter.ParseUpstreamResponse(result.body, result.contentType)
	if err != nil {
		writeProxyError(w, http.StatusBadGateway,
			fmt.Sprintf("parse upstream response: %v", err))
		return nil
	}
	inboundAdapter.WriteClientResponse(w, irResp, stream)
	return irResp.Usage
}

func fallbackProviderKey(route ProxyRoute, primaryRoute ProxyRoute, primaryKey string) (string, bool) {
	if strings.TrimSpace(route.ProviderKey) != "" {
		return route.ProviderKey, true
	}
	if sameCanonicalProvider(route.Provider, primaryRoute.Provider) {
		return primaryKey, true
	}
	return "", false
}

func sameCanonicalProvider(a, b string) bool {
	return canonicalProviderName(strings.TrimSpace(a)) == canonicalProviderName(strings.TrimSpace(b))
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

func serveStreamingProtocolUpstream(w http.ResponseWriter, r *http.Request, req *http.Request, inboundAdapter, upstreamAdapter ProtocolAdapter) (*proxyProtocolUpstreamResult, *IRUsage) {
	resp, err := proxyStreamingHTTPClient.Do(req)
	if err != nil {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("upstream request: %w", err)}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		limitedUpstream := io.LimitReader(resp.Body, proxyMaxUpstreamBodyBytes+1)
		respBody, readErr := io.ReadAll(limitedUpstream)
		if readErr != nil {
			return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("read upstream response: %w", readErr)}, nil
		}
		if int64(len(respBody)) > proxyMaxUpstreamBodyBytes {
			return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: fmt.Errorf("upstream response exceeds limit of %d bytes", proxyMaxUpstreamBodyBytes)}, nil
		}
		return &proxyProtocolUpstreamResult{statusCode: resp.StatusCode, header: resp.Header.Clone(), body: respBody, contentType: resp.Header.Get("Content-Type")}, nil
	}
	decoder, err := streamDecoderForProtocol(upstreamAdapter.Name())
	if err != nil {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: err}, nil
	}
	encoder, err := streamEncoderForProtocol(inboundAdapter.Name())
	if err != nil {
		return &proxyProtocolUpstreamResult{statusCode: http.StatusBadGateway, err: err}, nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	var streamUsage *IRUsage
	if err := decoder.DecodeStream(resp.Body, func(event StreamEvent) error {
		// Usage can ride on any event type: Anthropic attaches the input
		// count to message_start and the output count to message_delta,
		// while OpenAI shapes emit a dedicated usage event. Merging is
		// field-wise and idempotent, so overlapping reports are safe.
		if event.Usage != nil {
			streamUsage = mergeStreamUsage(streamUsage, event.Usage)
		}
		return encoder.EncodeStreamEvent(w, event)
	}); err != nil && r.Context().Err() == nil {
		_ = encoder.EncodeStreamEvent(w, StreamEvent{Type: streamEventError, Err: err.Error()})
	}
	return nil, streamUsage
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
	if recorder, ok := w.(interface{ setProxyError(string) }); ok {
		recorder.setProxyError(message)
	}
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
