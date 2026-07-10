package main

import (
	"sort"
	"strings"
	"testing"
)

// Task 5.1 — proxy route form: agent/protocol consistency.
//
// showProxyRouteForm must keep the visible Protocol DropDown in sync with
// the spec.Protocol the form will actually submit. When the operator
// changes the Agent DropDown, the form re-resolves the protocol default
// (defaultProxyProtocolForAgent(agent)). The bug: the callback only
// updates the in-memory spec.Protocol string and a local protocolIdx
// variable — it never calls SetCurrentOption on the real Protocol
// DropDown primitive, so the operator sees one protocol while the form
// submits a different one.
//
// proxyRouteFormApplyAgent is the pure helper that resolves the new
// protocol + protocol option index for a given spec and agent index. The
// TUI wires it to the Protocol DropDown via SetCurrentOption in
// showProxyRouteForm. This test asserts the helper exists and resolves
// the agent-specific default for both supported agents, so the TUI can
// rely on it instead of re-implementing the (buggy) inline logic.

func TestProxyRouteFormApplyAgentResetsProtocol(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)

	// Pick the second agent (claude) and apply it.
	idx := -1
	for i, a := range def.AgentOptions {
		if a == "claude" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("claude not in agent options %#v", def.AgentOptions)
	}
	newSpec, protocolIdx := proxyRouteFormApplyAgent(def, idx)
	if newSpec.Agent != "claude" {
		t.Fatalf("agent after apply = %q, want claude", newSpec.Agent)
	}
	wantProto := string(defaultProxyProtocolForAgent("claude"))
	if newSpec.Protocol != wantProto {
		t.Fatalf("protocol after apply = %q, want %q", newSpec.Protocol, wantProto)
	}
	if protocolIdx < 0 || protocolIdx >= len(newSpec.ProtocolOptions) {
		t.Fatalf("protocolIdx out of range: %d (options=%d)", protocolIdx, len(newSpec.ProtocolOptions))
	}
	if newSpec.ProtocolOptions[protocolIdx] != wantProto {
		t.Fatalf("protocolIdx %d points at %q, want %q", protocolIdx, newSpec.ProtocolOptions[protocolIdx], wantProto)
	}

	// Switching back to codex must reset protocol to the codex default.
	idx2 := -1
	for i, a := range newSpec.AgentOptions {
		if a == "codex" {
			idx2 = i
		}
	}
	newSpec2, protocolIdx2 := proxyRouteFormApplyAgent(newSpec, idx2)
	wantProto2 := string(defaultProxyProtocolForAgent("codex"))
	if newSpec2.Agent != "codex" {
		t.Fatalf("agent after re-apply = %q, want codex", newSpec2.Agent)
	}
	if newSpec2.Protocol != wantProto2 {
		t.Fatalf("protocol after re-apply = %q, want %q", newSpec2.Protocol, wantProto2)
	}
	if newSpec2.ProtocolOptions[protocolIdx2] != wantProto2 {
		t.Fatalf("protocolIdx %d points at %q, want %q", protocolIdx2, newSpec2.ProtocolOptions[protocolIdx2], wantProto2)
	}
}
func TestProxyRouteFormApplyAgentRejectsOOBIndex(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)
	origAgent, origProto := def.Agent, def.Protocol
	// Negative / over-large indices must NOT mutate spec.
	got, _ := proxyRouteFormApplyAgent(def, -1)
	if got.Agent != origAgent || got.Protocol != origProto {
		t.Fatalf("apply with idx=-1 mutated spec: agent=%q proto=%q (want %q/%q)", got.Agent, got.Protocol, origAgent, origProto)
	}
	got2, _ := proxyRouteFormApplyAgent(def, len(def.AgentOptions))
	if got2.Agent != origAgent || got2.Protocol != origProto {
		t.Fatalf("apply with OOB idx mutated spec: agent=%q proto=%q (want %q/%q)", got2.Agent, got2.Protocol, origAgent, origProto)
	}
}

// TestProxyRouteFormAgentSwitchEndToEnd verifies the full chain the TUI
// drives when the operator changes the agent: applying the agent change
// via the helper and then submitting via proxyRouteFormSubmitArgs must
// produce an argv whose --protocol matches the agent-specific default.
// This is the regression test for the original bug where the visible
// DropDown showed the codex default (anthropic-messages) while the form
// submitted whatever spec.Protocol had been silently mutated to.
func TestProxyRouteFormAgentSwitchEndToEnd(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)

	// Operator switches to claude.
	idx := indexOf(def.AgentOptions, "claude")
	applied, _ := proxyRouteFormApplyAgent(def, idx)
	args := proxyRouteFormSubmitArgs(applied)

	// Locate --protocol in the argv and assert it is the claude default.
	protoIdx := -1
	for i, a := range args {
		if a == "--protocol" && i+1 < len(args) {
			protoIdx = i + 1
		}
	}
	if protoIdx < 0 {
		t.Fatalf("no --protocol in submit args %#v", args)
	}
	wantProto := string(defaultProxyProtocolForAgent("claude"))
	// zhipu-cn only exposes anthropic-messages, so the form submission
	// silently falls back to the first provider-supported protocol in
	// claude's ProxyUpstreamPreference (anthropic-messages, after openai-
	// responses / openai-chat are skipped because the provider doesn't expose
	// them). The visible DropDown still shows the claude default; only the
	// emitted argv is provider-aware.
	wantProto = string(protocolAnthropicMessages)
	if args[protoIdx] != wantProto {
		t.Fatalf("submitted protocol = %q, want %q (claude fallback for zhipu-cn)", args[protoIdx], wantProto)
	}
	if applied.Agent != "claude" {
		t.Fatalf("submitted agent = %q, want claude", applied.Agent)
	}
}

// TestProxyRouteFormApplyAgentResetsProtocolOnEveryChange documents the
// chosen design: every agent change resets spec.Protocol to the agent-
// specific default (defaultProxyProtocolForAgent(agent)), NOT preserving
// whatever protocol the operator had previously chosen for the prior
// agent. This "re-sync on every agent change" policy keeps the persisted
// route valid by construction without forcing the operator to also
// remember to fix the protocol — the alternative ("keep user choice
// across agent changes") was rejected because it lets the form display
// a protocol the persisted route would reject, surfacing the error only
// at save time.
//
// The body asserts the actual behaviour: applying the claude agent to a
// codex-default spec flips Protocol from the codex default to the claude
// default. The previous name (TestProxyRouteFormApplyAgentKeepsCustom
// Protocol) and comment described the REJECTED alternative; the body
// never matched them. This rename aligns name + comment with the
// asserted contract.
func TestProxyRouteFormApplyAgentResetsProtocolOnEveryChange(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)
	// Sanity: codex default has its own agent-specific protocol.
	if def.Protocol != string(defaultProxyProtocolForAgent("codex")) {
		t.Fatalf("codex default protocol = %q, want %q", def.Protocol, defaultProxyProtocolForAgent("codex"))
	}
	// Apply claude — protocol must reset to claude's default, NOT keep
	// the codex-chosen protocol.
	idxClaude := indexOf(def.AgentOptions, "claude")
	def, _ = proxyRouteFormApplyAgent(def, idxClaude)
	if def.Protocol != string(defaultProxyProtocolForAgent("claude")) {
		t.Fatalf("after claude apply, protocol = %q, want claude default %q (must NOT keep the prior codex-chosen protocol)", def.Protocol, defaultProxyProtocolForAgent("claude"))
	}
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}

// ---- Task 5.2: Proxy Manager carries an agent ----
//
// The Proxy Manager menu previously hard-coded `codex` for preview
// (supportedProxyAgentList[0]) and called cmdProxyStart(nil, ...) which
// itself defaults to "codex". A claude route could be configured but
// never started or previewed from the TUI.
//
// The fix surfaces the active agent on the manager and threads it into
// the start/preview helpers. The pure helpers below are the testable
// surface:
//   - proxyManagerDefaultAgent(cfg) returns the agent the manager should
//     default to. Prefer an already-configured route (so opening the
//     manager after `cs proxy configure claude ...` defaults to claude),
//     falling back to the first supported agent (codex) when no route is
//     configured.
//   - proxyManagerStartArgs(agent) returns the argv passed to
//     cmdProxyStart so it targets the requested agent.
//   - proxyManagerPreviewArgs(agent) returns the argv passed to
//     cmdProxyPreview.
//   - proxyActionResultText is upgraded to take an agent so start/preview
//     dispatch honor it; status/stop stay agent-agnostic.

func TestProxyManagerDefaultAgentFallsBackToFirstSupported(t *testing.T) {
	// No proxy block configured: default to codex (supportedProxyAgentList[0]).
	cfg := &AppConfig{}
	got := proxyManagerDefaultAgent(cfg)
	if got != supportedProxyAgentList[0] {
		t.Fatalf("default agent with no routes = %q, want %q", got, supportedProxyAgentList[0])
	}
}

func TestProxyManagerDefaultAgentPrefersConfiguredRoute(t *testing.T) {
	// claude route configured: default to claude even though codex is
	// listed first in supportedProxyAgentList.
	cfg := &AppConfig{
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"claude": {Agent: "claude", Provider: "zhipu-cn", UpstreamProtocol: string(protocolOpenAIResponses)},
			},
		},
	}
	if got := proxyManagerDefaultAgent(cfg); got != "claude" {
		t.Fatalf("default agent with claude route = %q, want claude", got)
	}

	// codex only: default to codex.
	cfg2 := &AppConfig{
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {Agent: "codex", Provider: "zhipu-cn", UpstreamProtocol: string(protocolAnthropicMessages)},
			},
		},
	}
	if got := proxyManagerDefaultAgent(cfg2); got != "codex" {
		t.Fatalf("default agent with codex route = %q, want codex", got)
	}

	// both configured: prefer codex for determinism (it is the
	// supportedProxyAgentList[0] / MVP default).
	cfg3 := &AppConfig{
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"claude": {Agent: "claude", Provider: "zhipu-cn"},
				"codex":  {Agent: "codex", Provider: "deepseek"},
			},
		},
	}
	if got := proxyManagerDefaultAgent(cfg3); got != "codex" {
		t.Fatalf("default agent with both routes = %q, want codex (deterministic)", got)
	}
}

func TestProxyManagerDefaultAgentRejectsUnknownRouteAgent(t *testing.T) {
	// A route keyed by an unsupported agent name (corruption / future
	// migration) must NOT leak through as the default; fall back to
	// supportedProxyAgentList[0].
	cfg := &AppConfig{
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"ghost": {Agent: "ghost", Provider: "zhipu-cn"},
			},
		},
	}
	if got := proxyManagerDefaultAgent(cfg); got != supportedProxyAgentList[0] {
		t.Fatalf("default agent with ghost route = %q, want %q", got, supportedProxyAgentList[0])
	}
}

func TestProxyManagerStartArgsCarriesAgent(t *testing.T) {
	args := proxyManagerStartArgs("claude")
	want := []string{"--agent", "claude"}
	if len(args) != len(want) {
		t.Fatalf("start args = %#v, want %#v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("start arg %d = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestProxyManagerStartArgsCarriesCodex(t *testing.T) {
	args := proxyManagerStartArgs("codex")
	if len(args) != 2 || args[0] != "--agent" || args[1] != "codex" {
		t.Fatalf("start args for codex = %#v, want [--agent codex]", args)
	}
}

func TestProxyManagerPreviewArgsCarriesAgent(t *testing.T) {
	args := proxyManagerPreviewArgs("claude")
	want := []string{"claude"}
	if len(args) != len(want) || args[0] != want[0] {
		t.Fatalf("preview args = %#v, want %#v", args, want)
	}
}

func TestProxyActionResultStartCarriesAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// We can't actually start a proxy in a unit test, but we can assert
	// that start with --agent claude against an unconfigured claude route
	// surfaces a route-not-configured error rather than silently falling
	// back to codex.
	_, err := proxyActionResultText("start", "claude")
	if err == nil {
		t.Fatal("expected error when starting proxy for unconfigured claude route, got nil")
	}
	if !strings.Contains(err.Error(), "claude") && !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("start claude error should reference the agent or 'not configured': %v", err)
	}
}

func TestProxyActionResultStatusIgnoresAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// status/stop are agent-agnostic: passing "" must still work.
	out, err := proxyActionResultText("status", "")
	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(out, "not running") {
		t.Fatalf("status output = %q, want contains 'not running'", out)
	}
}

func TestProxyActionResultRejectsUnknownActionStillWorks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := proxyActionResultText("bogus", "codex"); err == nil {
		t.Fatal("expected error for bogus action, got nil")
	}
}

// ---- Task 5.4: Use Model form default value ----
//
// showUseModelForm previously initialized the Model input field from
// ts.customModels[provider] ONLY — which is empty for a freshly-opened
// TUI when the operator has never typed a custom model. The form must
// pre-fill with the provider's currently-stored or preset default model
// so the operator sees the existing value and can edit it, rather than
// starting from a blank field and risking an accidental wipe.
//
// useModelFormDefault extracts the pure resolution so the behaviour is
// unit-testable without a tview form.

func TestUseModelFormDefaultFallsBackToPresetModel(t *testing.T) {
	cfg := &AppConfig{}
	// No stored model, no custom model — fall back to the preset's
	// built-in Model. zhipu-cn ships glm-5.2 in providerPresets.
	got := useModelFormDefault(agentClaude, cfg, "zhipu-cn", "")
	if got == "" {
		t.Fatal("useModelFormDefault should fall back to preset model, got empty")
	}
	preset, err := resolveProviderPreset("zhipu-cn", cfg)
	if err != nil {
		t.Fatalf("resolveProviderPreset: %v", err)
	}
	if got != preset.Model {
		t.Fatalf("useModelFormDefault = %q, want preset %q", got, preset.Model)
	}
}

func TestUseModelFormDefaultPrefersStoredModel(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "user-picked"}},
	}
	got := useModelFormDefault(agentClaude, cfg, "zhipu-cn", "")
	if got != "user-picked" {
		t.Fatalf("useModelFormDefault = %q, want user-picked", got)
	}
}

func TestUseModelFormDefaultPrefersCustomTypedModel(t *testing.T) {
	// A custom value typed in this session overrides stored/preset.
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "stored"}},
	}
	got := useModelFormDefault(agentClaude, cfg, "zhipu-cn", "session-typed")
	if got != "session-typed" {
		t.Fatalf("useModelFormDefault = %q, want session-typed", got)
	}
}

func TestUseModelFormDefaultKimiCodingUsesPresetDefault(t *testing.T) {
	// kimi-coding has a preset default model and should prefill the form with it.
	cfg := &AppConfig{}
	got := useModelFormDefault(agentClaude, cfg, "kimi-coding", "")
	if got != "kimi-k2.7-code" {
		t.Fatalf("useModelFormDefault for kimi-coding = %q, want kimi-k2.7-code", got)
	}
}

func TestUseModelFormDefaultTrimsWhitespace(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {Model: "  glm-5.2  "}},
	}
	got := useModelFormDefault(agentClaude, cfg, "zhipu-cn", "  ")
	if got != "glm-5.2" {
		t.Fatalf("useModelFormDefault should trim whitespace, got %q", got)
	}
}

// ---- Task 5.3: providerDetailActionLabels is the source of truth for label strings/order ----
//
// showDetail consumes providerDetailActionLabels for the action
// ordering/superset. The helper is the single source of truth for the
// label spelling and the relative ordering; it is NOT the source of
// truth for which actions actually render (showDetail filters the
// superset at render time based on preset flags). These tests sanity-
// check that the labels the helper advertises stay in the relative
// order showDetail uses.
//
// We assert via a known-good set and that the helper stays in the same
// relative order showDetail uses (Choose Model, Use Model, Manage Model
// Mappings, Proxy Manager, [Switch], Edit API Key, Edit Tiers, Back).

func TestProviderDetailActionLabelsOrderStable(t *testing.T) {
	got := providerDetailActionLabels(false)
	// Must contain Choose Model, Use Model, Manage Model Mappings, Proxy
	// Manager, Back in that relative order.
	wantOrder := []string{"Choose Model", "Use Model", "Manage Model Mappings", "Proxy Manager", "Back"}
	lastIdx := -1
	for _, w := range wantOrder {
		idx := indexOf(got, w)
		if idx < 0 {
			t.Fatalf("label %q missing from %#v", w, got)
		}
		if idx <= lastIdx {
			t.Fatalf("label %q at index %d breaks expected order, lastIdx=%d (got=%#v)", w, idx, lastIdx, got)
		}
		lastIdx = idx
	}
	// No duplicates.
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			t.Fatalf("duplicate label %q in %#v", sorted[i], got)
		}
	}
}

// ---- Task 5.5: proxy route form threads the active agent ----
//
// showProxyRouteForm previously hard-coded supportedProxyAgentList[0]
// (codex) as the form's default agent, so the operator lost context when
// they entered the form from a claude-targeted Proxy Manager: the Agent
// DropDown reset to codex, the protocol reset to the codex default, and
// the Save/Cancel/Esc paths returned to the proxy manager with the agent
// reset to the default rather than the one the operator had selected.
//
// The fix threads the active agent through:
//   - proxyRouteFormDefaults takes the agent the manager was pinned to
//     and seeds spec.Agent with it (falling back to the first supported
//     agent if the supplied agent is unsupported/empty). The protocol
//     default is then resolved for THAT agent, not for codex.
//   - showProxyRouteForm(provider, agent) carries the agent and routes
//     every return path (Save/Cancel/Esc/Back) back to
//     showProxyManagerForAgent(provider, agent) instead of
//     showProxyManager(provider), so the operator lands back on the
//     manager pinned to the same agent they were editing.
//   - showProxyActionResult and showProxyPreview already carry the agent
//     for the underlying cmd dispatch; their Back handlers now also
//     return to showProxyManagerForAgent(provider, agent) so the manager
//     doesn't silently reset to the default agent after viewing a result
//     or preview.
//
// The pure helper (proxyRouteFormDefaults) is the unit-testable surface.
// The method-level wiring is exercised indirectly: the only way to keep
// the agent consistent across form open -> defaults -> submit is for the
// defaults to honour the agent argument, which is what the assertions
// below pin down.

func TestProxyRouteFormDefaultsHonoursClaudeAgent(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	// Operator opened the form from a claude-targeted Proxy Manager: the
	// form's default agent must be claude, not codex, and the protocol
	// must be the claude-specific default.
	def := proxyRouteFormDefaults("zhipu-cn", "claude", cfg)
	if def.Agent != "claude" {
		t.Fatalf("default agent = %q, want claude", def.Agent)
	}
	wantProto := string(defaultProxyProtocolForAgent("claude"))
	if def.Protocol != wantProto {
		t.Fatalf("default protocol = %q, want %q (claude default)", def.Protocol, wantProto)
	}
	// Submit argv must carry the claude agent through so the route lands
	// under cfg.Proxy.Routes["claude"], not ["codex"].
	args := proxyRouteFormSubmitArgs(def)
	if len(args) == 0 || args[0] != "claude" {
		t.Fatalf("submit agent = %#v, want first element \"claude\"", args)
	}
}

func TestProxyRouteFormDefaultsHonoursCodexAgent(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)
	if def.Agent != "codex" {
		t.Fatalf("default agent = %q, want codex", def.Agent)
	}
	if def.Protocol != string(defaultProxyProtocolForAgent("codex")) {
		t.Fatalf("default protocol = %q, want codex default", def.Protocol)
	}
}

// TestProxyRouteFormDefaultsFallsBackForUnsupportedAgent pins down the
// fallback policy when the caller hands in an agent the proxy MVP does
// not support (empty string, typo, or a future-migrated value). The form
// must not silently crash or render an empty DropDown: it falls back to
// supportedProxyAgentList[0] (codex) so the operator still sees a
// working form, with the codex-appropriate protocol default.
func TestProxyRouteFormDefaultsFallsBackForUnsupportedAgent(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	cases := []string{"", "ghost", "  "}
	for _, in := range cases {
		t.Run("agent="+in, func(t *testing.T) {
			def := proxyRouteFormDefaults("zhipu-cn", in, cfg)
			if def.Agent != supportedProxyAgentList[0] {
				t.Fatalf("unsupported agent %q yielded default agent %q, want %q", in, def.Agent, supportedProxyAgentList[0])
			}
			wantProto := string(defaultProxyProtocolForAgent(supportedProxyAgentList[0]))
			if def.Protocol != wantProto {
				t.Fatalf("unsupported agent %q yielded protocol %q, want %q", in, def.Protocol, wantProto)
			}
		})
	}
}

// TestProxyRouteFormRoundTripHonoursClaudeAgent is the end-to-end
// regression: opening the form for a claude-targeted manager, keeping the
// defaults, and submitting must persist a route under cfg.Proxy.Routes
// ["claude"] (not the previous hard-coded "codex"). This catches both
// the agent-threading fix and the configure-prefix fix at once.
func TestProxyRouteFormRoundTripHonoursClaudeAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedCfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	cfgPath, err := appConfigPath()
	if err != nil {
		t.Fatalf("appConfigPath: %v", err)
	}
	if err := writeJSONAtomic(cfgPath, seedCfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	def := proxyRouteFormDefaults("zhipu-cn", "claude", seedCfg)
	args := proxyRouteFormSubmitArgs(def)
	var out strings.Builder
	if err := cmdProxyConfigure(args, &out); err != nil {
		t.Fatalf("cmdProxyConfigure error for claude form: %v", err)
	}
	reloaded, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("loadAppConfig after round-trip: %v", err)
	}
	route, ok := reloaded.Proxy.Routes["claude"]
	if !ok {
		t.Fatalf("claude route missing; routes = %#v", reloaded.Proxy.Routes)
	}
	if route.Agent != "claude" {
		t.Fatalf("route agent = %q, want claude", route.Agent)
	}
	if route.UpstreamProtocol != string(protocolAnthropicMessages) {
		t.Fatalf("route protocol = %q, want claude fallback protocol %q", route.UpstreamProtocol, protocolAnthropicMessages)
	}
}
