package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ProxyConfig is the persistent proxy configuration stored inside
// AppConfig. Host/Port describe where the local proxy listens (Port == 0
// means "auto-allocate at serve time"), and Routes maps an agent name to
// its proxy route.
type ProxyConfig struct {
	Host   string                      `json:"host,omitempty"`
	Port   int                         `json:"port,omitempty"`
	Routes map[string]ProxyRouteConfig `json:"routes,omitempty"`
}

// ProxyRouteConfig is the persisted per-agent proxy route. It is a snapshot
// and does not itself resolve provider preset defaults; the route builder
// (buildProxyRouteFromConfig) turns it into a runtime ProxyRoute by
// resolving provider/model/mappings/protocol precedence.
type ProxyRouteConfig struct {
	Agent            string            `json:"agent"`
	Provider         string            `json:"provider"`
	Model            string            `json:"model,omitempty"`
	UpstreamProtocol string            `json:"upstreamProtocol,omitempty"`
	BaseURL          string            `json:"baseURL,omitempty"`
	ModelMappings    map[string]string `json:"modelMappings,omitempty"`
	Token            string            `json:"token,omitempty"`
	Fallback         *ProxyRouteConfig `json:"fallback,omitempty"`
}

func randomProxyRouteToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "csproxy-route-" + hex.EncodeToString(buf[:]), nil
}

func ensureProxyRouteTokens(cfg *AppConfig) (bool, error) {
	if cfg == nil || cfg.Proxy == nil || len(cfg.Proxy.Routes) == 0 {
		return false, nil
	}
	agents := make([]string, 0, len(cfg.Proxy.Routes))
	for agent := range cfg.Proxy.Routes {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	changed := false
	seen := map[string]struct{}{}
	for _, agent := range agents {
		route := cfg.Proxy.Routes[agent]
		token := strings.TrimSpace(route.Token)
		if token == "" {
			generated, err := randomProxyRouteToken()
			if err != nil {
				return false, err
			}
			route.Token = generated
			changed = true
			token = generated
		}
		if _, ok := seen[token]; ok {
			generated, err := randomProxyRouteToken()
			if err != nil {
				return false, err
			}
			route.Token = generated
			changed = true
			token = generated
		}
		seen[token] = struct{}{}
		cfg.Proxy.Routes[agent] = route
	}
	return changed, nil
}

func (rc ProxyRouteConfig) ResolveProtocol(reg *ProtocolRegistry) (ProviderProtocol, error) {
	agent := strings.TrimSpace(rc.Agent)
	if strings.TrimSpace(rc.UpstreamProtocol) == "" {
		return defaultProxyProtocolForAgent(agent), nil
	}
	return resolveProxyProtocolWithRegistry(rc.UpstreamProtocol, reg)
}

func (rc ProxyRouteConfig) ValidateProtocol(reg *ProtocolRegistry) error {
	protocol, err := rc.ResolveProtocol(reg)
	if err != nil {
		return err
	}
	return validateProxyAgentProtocol(rc.Agent, protocol)
}

// defaultProxyConfig returns the zero state used when no proxy block has
// been configured yet: listen on loopback with an OS-assigned port and no
// routes.
func defaultProxyConfig() ProxyConfig {
	return ProxyConfig{Host: "127.0.0.1", Port: 0}
}

// normalizeProxyConfig fills missing/invalid Host and Port values with the
// documented defaults. It is idempotent and side-effect free.
func normalizeProxyConfig(cfg ProxyConfig) ProxyConfig {
	if strings.TrimSpace(cfg.Host) == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port < 0 {
		cfg.Port = 0
	}
	return cfg
}

// normalizeAppConfig applies all per-section normalizations (currently just
// ProxyConfig host/port defaults) to an in-memory AppConfig. It is the single
// entry point used by loadAppConfigFrom so every reader sees a consistent,
// normalized config without each caller having to remember to call the
// per-section normalizers.
//
// To preserve JSON omitempty semantics, normalization only runs when a Proxy
// block is already present (non-nil pointer). An empty config (no proxy
// block) is left untouched so re-serializing it does not materialize a proxy
// object. Because AppConfig.Proxy is a *ProxyConfig, a truly absent proxy
// block stays absent through JSON round-trips.
//
// IMPORTANT: a host that is empty or whitespace-only is replaced with the
// default ("127.0.0.1"), but a non-empty host is NOT TrimSpace'd. Trimming
// here would silently clean control characters (e.g. a leading "\n" on
// "\n127.0.0.1") out of a hand-edited config, defeating the
// rejectControlChars guard that buildProxyRouteFromConfig runs on the raw
// value. Only the empty/whitespace-only case is normalized; any other value
// is preserved verbatim so the downstream control-char screen can reject it.
func normalizeAppConfig(cfg *AppConfig) {
	if cfg == nil || cfg.Proxy == nil {
		return
	}
	if strings.TrimSpace(cfg.Proxy.Host) == "" {
		cfg.Proxy.Host = "127.0.0.1"
	}
	if cfg.Proxy.Port < 0 {
		cfg.Proxy.Port = 0
	}
}

// copyStringMap returns a defensive copy of in. Returns nil when in is nil
// or empty so the resulting route/map can be cheaply tested with len()==0
// and does not allocate a pointless zero-length map. Callers that need to
// distinguish "no mapping configured" from "configured but empty" must do so
// before calling this helper (e.g. by checking the source map directly).
func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// buildProxyRouteFromConfig resolves a persisted per-agent route config into
// a runtime ProxyRoute. Resolution precedence:
//
//   - route provider is canonicalized via providerAliases.
//   - model: route.Model > stored provider model > preset default (through
//     resolveSwitchPreset, which also validates opencode-go models).
//   - model mappings: whole-source fallback. If the route declares any
//     non-empty ModelMappings, ONLY a defensive copy of the route-level
//     table is used and the provider-level cfg.ModelMappings[provider]
//     table is NOT merged in. Otherwise (route declares no mappings) a
//     defensive copy of cfg.ModelMappings[provider] is used as the whole
//     source. Provider-level mappings never mix with route-level mappings:
//     a route opts into one table or the other, it does not merge them.
//   - protocol: route.UpstreamProtocol. An empty/whitespace value resolves
//     to the agent-specific default via defaultProxyProtocolForAgent (so a
//     hand-edited config that omits the field produces the same route as a
//     fresh configure); a non-empty but unrecognized value returns an
//     error (no silent downgrade).
//
// Model mappings are defensively copied so callers cannot mutate the
// persisted config through the returned route. The function returns an
// error for nil cfg, missing route, unknown provider, invalid model, or
// unknown protocol.
func buildProxyRouteFromConfig(agent string, cfg *AppConfig, localToken string) (ProxyRoute, error) {
	if cfg == nil {
		return ProxyRoute{}, fmt.Errorf("app config is nil")
	}
	if cfg.Proxy == nil || cfg.Proxy.Routes == nil {
		return ProxyRoute{}, fmt.Errorf("proxy route for agent %q is not configured", agent)
	}
	agent = strings.TrimSpace(agent)
	rc, ok := cfg.Proxy.Routes[agent]
	if !ok {
		return ProxyRoute{}, fmt.Errorf("proxy route for agent %q is not configured", agent)
	}
	// Defensively screen every persisted string field (and the configured
	// listen host) for Unicode control characters. A hand-edited or
	// migrated config could carry an embedded line break that would
	// otherwise silently flow into rendered TOML/JSON/log lines and enable
	// header/config injection at preview or serve time. The raw stored
	// values are validated (not the trimmed ones) so a trailing newline
	// cannot be trimmed away before this guard runs.
	//
	// Note: rc.Agent (the persisted route field) is screened SEPARATELY
	// from the route key `agent`. They are allowed to differ in non-
	// control-char ways (e.g. a casing mismatch), but a control character
	// in rc.Agent is always illegal even when the route key is clean — a
	// hand-edit could poison the Agent field while leaving the map key
	// intact, and that poisoned value would later be rendered into logs
	// or status output.
	for label, value := range map[string]string{
		"agent":    agent,
		"provider": rc.Provider,
		"model":    rc.Model,
		"protocol": rc.UpstreamProtocol,
		"baseURL":  rc.BaseURL,
		"host":     cfg.Proxy.Host,
	} {
		if err := rejectControlChars(label, value); err != nil {
			return ProxyRoute{}, err
		}
	}
	if err := rejectControlChars("route agent", rc.Agent); err != nil {
		return ProxyRoute{}, err
	}
	provider := canonicalProviderName(strings.TrimSpace(rc.Provider))
	if provider == "" {
		return ProxyRoute{}, fmt.Errorf("proxy route for agent %q has no provider", agent)
	}
	preset, err := resolveSwitchPreset(provider, cfg, strings.TrimSpace(rc.Model))
	if err != nil {
		return ProxyRoute{}, err
	}
	resolvedTiers := resolveModelTiers(preset, cfg.Providers[provider], ModelTiers{})
	mappings := tierModelMappings(resolvedTiers)
	_, explicitDefault := rc.ModelMappings["default"]
	explicitSonnet := false
	for k, v := range rc.ModelMappings {
		if err := rejectControlChars("model mapping key", k); err != nil {
			return ProxyRoute{}, err
		}
		if err := rejectControlChars("model mapping value", v); err != nil {
			return ProxyRoute{}, err
		}
		if strings.TrimSpace(k) == "" {
			continue
		}
		mappings[k] = v
		if k == "sonnet" {
			explicitSonnet = true
		}
	}
	if !explicitDefault && explicitSonnet {
		mappings["default"] = mappings["sonnet"]
	}
	// Protocol resolution uses one registry instance for both parsing and
	// compatibility validation:
	//   - empty/whitespace UpstreamProtocol first resolves to the
	//     agent-specific default via defaultProxyProtocolForAgent(agent). If
	//     that default is unsupported by the selected provider (for example,
	//     kimi-coding only exposes openai-chat), we fall back to the same
	//     provider-aware proxy selection used by fresh configure/switch flows.
	//     This keeps legacy/hand-edited routes usable without silently changing
	//     explicitly requested protocols.
	//   - non-empty but unrecognized value -> error (no silent downgrade),
	//     via resolveProxyProtocol.
	routeForValidation := rc
	routeForValidation.Agent = agent
	registry := defaultProtocolRegistry()
	protocol, err := routeForValidation.ResolveProtocol(registry)
	if err != nil {
		return ProxyRoute{}, err
	}
	if !providerCanUseProxyProtocol(preset, protocol) {
		profile, ok := agentProfiles[AgentName(agent)]
		if ok {
			for _, fallback := range profile.ProxyUpstreamPreference {
				if providerCanUseProxyProtocol(preset, fallback) {
					protocol = fallback
					routeForValidation.UpstreamProtocol = string(protocol)
					break
				}
			}
		}
	}
	// Enforce the agent/protocol compatibility matrix at route-build time
	// too, so a stale/hand-edited config (or a configure that pre-dated this
	// check) surfaces loudly at preview rather than silently producing a
	// route the proxy would reject at request time.
	if err := routeForValidation.ValidateProtocol(registry); err != nil {
		return ProxyRoute{}, err
	}
	if !providerCanUseProxyProtocol(preset, protocol) {
		return ProxyRoute{}, fmt.Errorf("provider %q has no %s endpoint", provider, protocol)
	}
	route := buildProxyRoute(provider, preset, protocol, localToken, mappings)
	if strings.TrimSpace(rc.BaseURL) != "" {
		route.UpstreamBaseURL = strings.TrimSpace(rc.BaseURL)
	}
	if rc.Fallback != nil {
		fallback, err := buildProxyFallbackRoute(agent, route, *rc.Fallback, cfg, protocol)
		if err != nil {
			return ProxyRoute{}, err
		}
		route.Fallback = &fallback
	}
	return route, nil
}

func buildProxyFallbackRoute(agent string, primaryRoute ProxyRoute, rc ProxyRouteConfig, cfg *AppConfig, protocol ProviderProtocol) (ProxyRoute, error) {
	for label, value := range map[string]string{
		"fallback provider": rc.Provider,
		"fallback model":    rc.Model,
		"fallback protocol": rc.UpstreamProtocol,
		"fallback baseURL":  rc.BaseURL,
	} {
		if err := rejectControlChars(label, value); err != nil {
			return ProxyRoute{}, err
		}
	}
	provider := canonicalProviderName(strings.TrimSpace(rc.Provider))
	if provider == "" {
		return ProxyRoute{}, fmt.Errorf("fallback route for agent %q has no provider", agent)
	}
	model := strings.TrimSpace(rc.Model)
	preset, err := resolveSwitchPreset(provider, cfg, model)
	if err != nil {
		return ProxyRoute{}, err
	}
	if !providerCanUseProxyProtocol(preset, protocol) && strings.TrimSpace(rc.BaseURL) == "" {
		return ProxyRoute{}, fmt.Errorf("fallback provider %q has no %s endpoint", provider, protocol)
	}
	providerKey := storedAPIKeyForAgent(cfg, AgentName(agent), provider)
	if !sameCanonicalProvider(provider, primaryRoute.Provider) && !preset.NoAPIKey && strings.TrimSpace(providerKey) == "" {
		return ProxyRoute{}, fmt.Errorf("fallback provider %q has no API key configured; run `cs set-key %s <key>` before starting the proxy", provider, provider)
	}
	route := buildProxyRoute(provider, preset, protocol, "", nil)
	if model != "" {
		route.Model = model
	}
	if strings.TrimSpace(rc.BaseURL) != "" {
		route.UpstreamBaseURL = strings.TrimSpace(rc.BaseURL)
	}
	route.ProviderKey = providerKey
	return route, nil
}

func providerCanUseProxyProtocol(preset ProviderPreset, protocol ProviderProtocol) bool {
	if _, ok := preset.presetEndpoint(protocol); ok {
		return true
	}
	return len(preset.Endpoints) == 0 && protocol == protocolAnthropicMessages && strings.TrimSpace(preset.BaseURL) != ""
}

// resolveProxyProtocol normalizes a stored protocol string into a
// ProviderProtocol. An empty/whitespace value resolves to
// protocolAnthropicMessages (the MVP default). A non-empty but unrecognized
// value returns an error so a stale or hand-edited config surfaces a loud,
// descriptive failure rather than silently producing a route with the wrong
// protocol.
func resolveProxyProtocol(raw string) (ProviderProtocol, error) {
	return resolveProxyProtocolWithRegistry(raw, defaultProtocolRegistry())
}

func resolveProxyProtocolWithRegistry(raw string, reg *ProtocolRegistry) (ProviderProtocol, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return protocolAnthropicMessages, nil
	}
	adapter, ok := reg.Find(trimmed)
	if !ok {
		return "", fmt.Errorf("unknown upstream protocol %q (supported: %s)",
			raw, strings.Join(reg.SupportedNames(), ", "))
	}
	return adapter.Name(), nil
}

// supportedProxyAgents is the allow-list of agents the proxy MVP supports.
var supportedProxyAgents = map[string]struct{}{
	"codex":    {},
	"claude":   {},
	"opencode": {},
}

// supportedProxyAgentList is the ordered, human-readable companion to
// supportedProxyAgents used for error messages and the configure-time agent
// check. It is kept as a slice (not derived from the map at call time) so
// the ordering in error messages is deterministic.
var supportedProxyAgentList = []string{"codex", "claude", "opencode"}

// validateProxyAgentProtocol enforces the agent/upstream-protocol
// compatibility matrix through agentProfiles and ProtocolAdapter.CanProxyFrom.
// The function returns a descriptive error for any disallowed combination so
// configure/preview failures surface loudly rather than silently producing a
// route the proxy would reject at request time anyway.
func validateProxyAgentProtocol(agent string, protocol ProviderProtocol) error {
	agent = strings.TrimSpace(strings.ToLower(agent))
	if _, ok := supportedProxyAgents[agent]; !ok {
		return fmt.Errorf("unsupported agent %q for proxy (MVP supports: %s)", agent, strings.Join(supportedProxyAgentList, ", "))
	}
	profile, ok := agentProfiles[AgentName(agent)]
	if !ok {
		return fmt.Errorf("unsupported agent %q for proxy", agent)
	}
	reg := defaultProtocolRegistry()
	upstream, ok := reg.Find(string(protocol))
	if !ok {
		return fmt.Errorf("unknown upstream protocol %q for agent %q", protocol, agent)
	}
	inbound, ok := reg.Find(string(profile.ClientProtocol))
	if !ok {
		return fmt.Errorf("unknown client protocol %q for agent %q", profile.ClientProtocol, agent)
	}
	if ok, reason := upstream.CanProxyFrom(inbound); !ok {
		if reason != "" {
			return fmt.Errorf("%s", reason)
		}
		return fmt.Errorf("upstream protocol %q is not supported for agent %q", protocol, agent)
	}
	return nil
}

// defaultProxyProtocolForAgent returns the upstream protocol the proxy
// should use for an agent when the caller did not explicitly pass
// --protocol. The default must be a combination validateProxyAgentProtocol
// accepts, so the persisted route is valid by construction and a later
// `preview`/`serve` never surfaces a stale-route error caused purely by
// the omission of --protocol.
//
// Defaults:
//   - codex (OpenAI Responses client): protocolAnthropicMessages. Codex
//     is a Responses client; an Anthropic Messages upstream is the
//     well-tested MVP path and is explicitly allowed by the matrix.
//   - claude (Anthropic Messages client): protocolOpenAIResponses. A
//     Responses upstream is the natural target for a Messages client
//     through the proxy and is explicitly allowed by the matrix.
//   - unknown agents fall back to protocolAnthropicMessages; the caller
//     is expected to have already validated the agent name, and the
//     subsequent validateProxyAgentProtocol call will reject it anyway.
func defaultProxyProtocolForAgent(agent string) ProviderProtocol {
	agent = strings.TrimSpace(strings.ToLower(agent))
	profile, ok := agentProfiles[AgentName(agent)]
	if !ok {
		return protocolAnthropicMessages
	}
	for _, protocol := range profile.ProxyUpstreamPreference {
		if err := validateProxyAgentProtocol(agent, protocol); err == nil {
			return protocol
		}
	}
	return protocolAnthropicMessages
}

// rejectControlChars validates that value does not contain Unicode
// control characters that could enable header/log/config injection when
// the value is later embedded in TOML, JSON, log lines, or URLs. label is
// used only for the error message so the caller can identify which field
// was rejected.
//
// The check covers the full set of Unicode control characters:
//   - C0 controls (U+0000..U+001F) EXCEPT U+0009 (horizontal tab), which is
//     explicitly allowed because some legitimate identifiers carry it and
//     a tab is never a record/line separator.
//   - DEL (U+007F).
//   - C1 controls (U+0080..U+009F).
//   - Format characters (Unicode general category Cf): BOM/U+FEFF, the
//     direction marks (U+200B..U+200F, U+202A..U+202E, U+2060..U+2064,
//     U+206A..U+206F, U+061C, U+E0001, U+E0020..U+E007F). These can be used
//     to hide payload inside identifiers and logs, so they are rejected.
//
// Non-breaking space (U+00A0) is NOT a control character and is allowed;
// callers that want to trim it should do so explicitly. Visible,
// printable characters of any script (Latin, CJK, emoji, etc.) are
// allowed because host/model/agent values can legitimately contain them.
//
// The function operates on the raw rune stream of value — callers MUST
// pass the raw (pre-TrimSpace/pre-canonicalize) value so a trailing or
// leading newline is not silently trimmed into a valid-looking token
// before this guard runs.
func rejectControlChars(label, value string) error {
	for _, r := range value {
		if r == '\t' {
			// Tab is explicitly allowed: it is not a record/line
			// separator and some legitimate identifiers use it.
			continue
		}
		if isControlRune(r) {
			return fmt.Errorf("%s must not contain control characters (got U+%04X)", label, r)
		}
	}
	return nil
}

// validateProxyHost validates that a listen host is a safe, well-formed
// network identifier and NOT a URL. A proxy listen host is plugged into
// net.Listen("tcp", host:port) and into rendered base URLs; if a user
// (or a buggy migration) hands it something like "http://127.0.0.1",
// "host evil", or "host/path", the result ranges from confusing error
// messages to silent misconfiguration (binding the wrong interface,
// polluting the rendered codex config with embedded slashes/schemes).
//
// Accepted shapes:
//   - "localhost" (hostname)
//   - IPv4 dotted-quad ("127.0.0.1", "0.0.0.0")
//   - IPv6 literal ("::1", "fe80::1") — the surrounding brackets, if
//     present, are stripped before validation so "[::1]" is accepted too
//   - DNS hostname (letters/digits/dots/hyphens), e.g. "my-host.example.com"
//
// Rejected shapes (with descriptive errors):
//   - any embedded whitespace ("host evil", "   ")
//   - any URL scheme ("http://host", "https://host", "ftp://host")
//   - any path separator ("/", "\\") — "host/path" or "host\\path"
//   - empty string
//
// The function is intentionally permissive about the hostname grammar
// (it does NOT enforce RFC 1123 strictly) so unusual-but-valid identifiers
// are not rejected. The strict checks target the specific injection
// vectors (scheme, slash, whitespace) that have caused real bugs.
func validateProxyHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("proxy host cannot be empty")
	}
	// Whitespace anywhere in the host (after trimming the ends) means the
	// "host" is actually two tokens — a classic argv/header injection.
	// We trim then re-check so a value like "  " is caught by the empty
	// check above and a value like "a b" is caught here.
	if strings.ContainsAny(host, " \t\n\r\v\f") {
		return fmt.Errorf("proxy host %q must not contain whitespace", host)
	}
	// Strip a single pair of surrounding brackets so "[::1]" validates the
	// same way "::1" does. net.JoinHostPort adds them for IPv6; we want to
	// accept either form at config time so a paste from a rendered URL
	// works.
	probe := host
	if len(probe) >= 2 && probe[0] == '[' && probe[len(probe)-1] == ']' {
		probe = probe[1 : len(probe)-1]
	}
	// Reject URL schemes. "http://host" parses as scheme=http under
	// net/url; we treat the presence of ANY scheme as an error because a
	// bare listen host must never have one. We also catch the scheme-less
	// "//host" form (which url.Parse may treat as authority-only).
	if strings.Contains(probe, "://") || strings.HasPrefix(probe, "//") {
		return fmt.Errorf("proxy host %q must not include a URL scheme (use a bare host like 127.0.0.1)", host)
	}
	// Reject any path separator. A listen host with a slash would either
	// fail to bind (good) or — worse — silently end up in a rendered
	// base_url as a path component, hiding a misconfiguration.
	if strings.ContainsAny(probe, "/\\") {
		return fmt.Errorf("proxy host %q must not contain '/' or '\\'", host)
	}
	// Reject a bare host:port form (e.g. "127.0.0.1:8080",
	// "localhost:18080", "[::1]:8080", "my-host:8888"). The proxy listen
	// host is a BARE host — the port is a SEPARATE field (--port /
	// cfg.Proxy.Port). A user who types "127.0.0.1:8080" into --host has
	// confused the two fields and would either fail to bind (good) or
	// bind on a mangled host string that pollutes the rendered base_url.
	//
	// net.SplitHostPort is the canonical way to recognize the host:port
	// shape: it returns (host, port, nil) for well-formed inputs including
	// the bracketed IPv6+port form "[::1]:8080". A bare IPv6 literal like
	// "::1" or "[::1]" is NOT a valid host:port (SplitHostPort errors with
	// "missing port"), so those still pass through. We only reject when a
	// non-empty port was successfully split out.
	if h, p, err := net.SplitHostPort(host); err == nil && p != "" {
		_ = h
		return fmt.Errorf("proxy host %q must not include a port (use --port for %q)", host, p)
	}
	return nil
}

// isControlRune reports whether r is a Unicode control or format
// character that rejectControlChars should reject. It covers C0 (minus
// tab, handled by the caller), DEL, C1, and the Cf format category. The
// check is rune-based, so it works correctly even for multi-byte UTF-8
// sequences; utf8.RuneError (the replacement rune returned by
// range-over-string for invalid bytes) is also rejected so a malformed
// byte sequence cannot slip through.
func isControlRune(r rune) bool {
	if r == utf8.RuneError {
		return true
	}
	if r == '\t' {
		return false
	}
	if r < 0x20 || r == 0x7F { // C0 (minus tab handled above) and DEL
		return true
	}
	if r >= 0x80 && r <= 0x9F { // C1 controls
		return true
	}
	if unicode.Is(unicode.Cf, r) { // format chars (BOM, direction marks, etc.)
		return true
	}
	return false
}
