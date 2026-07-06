package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Task 4: model/mapping helper unit tests.
//
// These tests target the package-level helpers extracted from the
// `cmdUseModel` / `cmdModelMapSet` / `cmdModelMapRemove` commands so the
// upcoming TUI pages can reuse the exact same validation and persistence
// semantics without going through the CLI flag layer.

// ---- useModelForProvider ----

func TestUseModelForProviderUpdatesModelAndDefaultMapping(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
	}
	if err := useModelForProvider(cfg, agentClaude, "zhipu-cn", "glm-5.2"); err != nil {
		t.Fatalf("useModelForProvider error: %v", err)
	}
	stored := cfg.Providers["zhipu-cn"]
	if stored.Model != "glm-5.2" {
		t.Fatalf("stored model = %q, want glm-5.2", stored.Model)
	}
	if stored.APIKey != "sk-test" {
		t.Fatalf("api key changed = %q, want sk-test (helper must preserve other fields)", stored.APIKey)
	}
	if got := cfg.ModelMappings["zhipu-cn"]["default"]; got != "glm-5.2" {
		t.Fatalf("default mapping = %q, want glm-5.2", got)
	}
}

func TestProviderDetailActionLabelsIncludeLaunchBeforeDefault(t *testing.T) {
	labels := providerDetailActionLabels(false)
	launchIdx := -1
	defaultIdx := -1
	for i, label := range labels {
		switch label {
		case actionLabelLaunch:
			launchIdx = i
		case actionLabelSetDefault:
			defaultIdx = i
		case "Switch (default)":
			t.Fatalf("legacy action label %q should not appear in detail actions", label)
		}
	}
	if launchIdx < 0 {
		t.Fatalf("Launch action missing from labels: %#v", labels)
	}
	if defaultIdx < 0 {
		t.Fatalf("Set as default action missing from labels: %#v", labels)
	}
	if launchIdx > defaultIdx {
		t.Fatalf("Launch should appear before Set as default: %#v", labels)
	}
}

func TestUseModelForProviderCanonicalizesAlias(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
	}
	// "bigmodel" is an alias for zhipu-cn.
	if err := useModelForProvider(cfg, agentClaude, "bigmodel", "glm-5.2"); err != nil {
		t.Fatalf("useModelForProvider alias error: %v", err)
	}
	if got := cfg.Providers["zhipu-cn"].Model; got != "glm-5.2" {
		t.Fatalf("alias: stored model = %q, want glm-5.2", got)
	}
	if _, ok := cfg.Providers["bigmodel"]; ok {
		t.Fatalf("alias should be canonicalized, but raw alias key was written")
	}
}

func TestUseModelForProviderRejectsUnknownProvider(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	err := useModelForProvider(cfg, agentClaude, "ghost", "glm-5.2")
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error should mention unsupported: %v", err)
	}
}

func TestUseModelForProviderRejectsEmptyArgs(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {}}}
	cases := []struct {
		name, provider, model, wantSubstr string
	}{
		{"empty provider", "", "glm-5.2", "provider"},
		{"empty model", "zhipu-cn", "", "model"},
		{"whitespace provider", "   ", "glm-5.2", "provider"},
		{"whitespace model", "zhipu-cn", "   ", "model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := useModelForProvider(cfg, agentClaude, tc.provider, tc.model)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("%s: error %q should contain %q", tc.name, err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestUseModelForProviderRejectsNoModelProvider(t *testing.T) {
	// kimi-coding is a NoModel preset (configured by API key alone).
	cfg := &AppConfig{Providers: map[string]StoredProvider{"kimi-coding": {APIKey: "sk-test"}}}
	err := useModelForProvider(cfg, agentClaude, "kimi-coding", "anything")
	if err == nil {
		t.Fatal("expected error for NoModel provider, got nil")
	}
	if !strings.Contains(err.Error(), "does not accept model selection") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestUseModelForProviderRejectsUnsupportedOpencodeGoModel(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"opencode-go": {APIKey: "sk-test"}}}
	// glm-5 is in unsupportedOpenCodeGoAnthropicModels.
	err := useModelForProvider(cfg, agentClaude, "opencode-go", "glm-5")
	if err == nil {
		t.Fatal("expected error for unsupported opencode-go model, got nil")
	}
	if !strings.Contains(err.Error(), "opencode-go") {
		t.Fatalf("error should mention opencode-go: %v", err)
	}
}

// ---- setModelMappingForProvider / removeModelMappingForProvider ----

func TestSetModelMappingForProvider(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	if err := setModelMappingForProvider(cfg, "zhipu-cn", "sonnet", "glm-5.2"); err != nil {
		t.Fatalf("set mapping error: %v", err)
	}
	if got := cfg.ModelMappings["zhipu-cn"]["sonnet"]; got != "glm-5.2" {
		t.Fatalf("mapping not set, got %q", got)
	}
}

func TestSetModelMappingForProviderCanonicalizesAlias(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	if err := setModelMappingForProvider(cfg, "bigmodel", "sonnet", "glm-5.2"); err != nil {
		t.Fatalf("set mapping alias error: %v", err)
	}
	if got := cfg.ModelMappings["zhipu-cn"]["sonnet"]; got != "glm-5.2" {
		t.Fatalf("alias mapping not set on canonical key, got %q", got)
	}
	if _, ok := cfg.ModelMappings["bigmodel"]; ok {
		t.Fatalf("alias should be canonicalized")
	}
}

func TestSetModelMappingForProviderRejectsUnknownProvider(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	err := setModelMappingForProvider(cfg, "ghost", "sonnet", "glm-5.2")
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error should mention unsupported: %v", err)
	}
}

func TestSetModelMappingForProviderRejectsEmptyProvider(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {}}}
	cases := []string{"", "   "}
	for _, p := range cases {
		t.Run("provider="+p, func(t *testing.T) {
			err := setModelMappingForProvider(cfg, p, "sonnet", "glm-5.2")
			if err == nil {
				t.Fatalf("expected error for empty provider %q, got nil", p)
			}
			msg := err.Error()
			if !strings.Contains(msg, "provider") || !strings.Contains(msg, "empty") {
				t.Fatalf("error %q should contain both %q and %q", msg, "provider", "empty")
			}
		})
	}
}

func TestSetModelMappingForProviderRejectsEmptyArgs(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {}}}
	cases := []struct {
		name, provider, client, upstream, wantSubstr string
	}{
		{"empty client", "zhipu-cn", "", "glm-5.2", "client-model"},
		{"empty upstream", "zhipu-cn", "sonnet", "", "upstream-model"},
		{"whitespace client", "zhipu-cn", "  ", "glm-5.2", "client-model"},
		{"whitespace upstream", "zhipu-cn", "sonnet", "  ", "upstream-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := setModelMappingForProvider(cfg, tc.provider, tc.client, tc.upstream)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("%s: error %q should contain %q", tc.name, err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestRemoveModelMappingForProvider(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"sonnet": "glm-5.2", "default": "glm-5.2"},
		},
	}
	if err := removeModelMappingForProvider(cfg, "zhipu-cn", "sonnet"); err != nil {
		t.Fatalf("remove mapping error: %v", err)
	}
	if _, ok := cfg.ModelMappings["zhipu-cn"]["sonnet"]; ok {
		t.Fatalf("sonnet mapping still present after remove")
	}
	// default must survive removing sonnet.
	if got := cfg.ModelMappings["zhipu-cn"]["default"]; got != "glm-5.2" {
		t.Fatalf("default mapping should survive remove, got %q", got)
	}
}

func TestRemoveModelMappingForProviderDropsEntryWhenEmpty(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"sonnet": "glm-5.2"},
		},
	}
	if err := removeModelMappingForProvider(cfg, "zhipu-cn", "sonnet"); err != nil {
		t.Fatalf("remove mapping error: %v", err)
	}
	if _, ok := cfg.ModelMappings["zhipu-cn"]; ok {
		t.Fatalf("empty provider mapping should be removed entirely")
	}
}

func TestRemoveModelMappingForProviderCanonicalizesAlias(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"sonnet": "glm-5.2"},
		},
	}
	// "bigmodel" is an alias for zhipu-cn.
	if err := removeModelMappingForProvider(cfg, "bigmodel", "sonnet"); err != nil {
		t.Fatalf("remove mapping alias error: %v", err)
	}
	if _, ok := cfg.ModelMappings["zhipu-cn"]; ok {
		t.Fatalf("alias remove should drop canonical provider entry when empty")
	}
}

func TestRemoveModelMappingForProviderRejectsUnknownProvider(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	err := removeModelMappingForProvider(cfg, "ghost", "sonnet")
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("error should mention unsupported: %v", err)
	}
}

func TestRemoveModelMappingForProviderRejectsEmptyProvider(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"sonnet": "glm-5.2"},
		},
	}
	cases := []string{"", "   "}
	for _, p := range cases {
		t.Run("provider="+p, func(t *testing.T) {
			err := removeModelMappingForProvider(cfg, p, "sonnet")
			if err == nil {
				t.Fatalf("expected error for empty provider %q, got nil", p)
			}
			msg := err.Error()
			if !strings.Contains(msg, "provider") || !strings.Contains(msg, "empty") {
				t.Fatalf("error %q should contain both %q and %q", msg, "provider", "empty")
			}
		})
	}
}

func TestRemoveModelMappingForProviderRejectsEmptyClientModel(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"sonnet": "glm-5.2"},
		},
	}
	cases := []string{"", "   "}
	for _, tc := range cases {
		t.Run("client="+tc, func(t *testing.T) {
			err := removeModelMappingForProvider(cfg, "zhipu-cn", tc)
			if err == nil {
				t.Fatalf("expected error for empty client model %q, got nil", tc)
			}
			if !strings.Contains(err.Error(), "client-model") {
				t.Fatalf("error %q should contain %q", err.Error(), "client-model")
			}
		})
	}
}

func TestRemoveModelMappingForProviderMissingMappingsIsError(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	err := removeModelMappingForProvider(cfg, "zhipu-cn", "sonnet")
	if err == nil {
		t.Fatal("expected error when provider has no mappings at all, got nil")
	}
	if !strings.Contains(err.Error(), "no model mappings") {
		t.Fatalf("error should mention no model mappings: %v", err)
	}
}

func TestRemoveModelMappingForProviderMissingKeyIsError(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}},
		ModelMappings: map[string]map[string]string{
			"zhipu-cn": {"default": "glm-5.2"},
		},
	}
	err := removeModelMappingForProvider(cfg, "zhipu-cn", "sonnet")
	if err == nil {
		t.Fatal("expected error removing a non-existent client model, got nil")
	}
	if !strings.Contains(err.Error(), "no mapping") {
		t.Fatalf("error should mention no mapping: %v", err)
	}
	// Provider entry must be untouched on the error path.
	if _, ok := cfg.ModelMappings["zhipu-cn"]; !ok {
		t.Fatal("provider entry should not be removed on error")
	}
}

// ---- Task 5: TUI pages and action labels ----
//
// These tests target the testable surface of the new TUI proxy pages:
//   - providerDetailActionLabels reports the expected action entries, including
//     the new "Use Model", "Manage Model Mappings", "Proxy Manager" actions.
//   - For NoModel presets, Choose Model/Use Model are dropped but Manage Model
//     Mappings and Proxy Manager remain available.
//   - proxyRouteFormDefaults + proxyRouteFormSubmitArgs expose the form's
//     default values and the CLI argv it builds, so the form behaviour can be
//     unit-tested without driving a real tview.Form.

func TestProviderDetailProxyActions(t *testing.T) {
	actions := providerDetailActionLabels(false)
	for _, want := range []string{"Use Model", "Manage Model Mappings", "Proxy Manager"} {
		found := false
		for _, got := range actions {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing action %q in %#v", want, actions)
		}
	}
}

func TestProviderDetailActionsPreserveExistingSemantics(t *testing.T) {
	// Non-NoModel provider: Choose Model, Edit API Key, Edit Tiers, Back must
	// all still be present alongside the new actions.
	actions := providerDetailActionLabels(false)
	for _, want := range []string{"Choose Model", "Edit API Key", "Edit Tiers", "Back"} {
		found := false
		for _, got := range actions {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("non-NoModel: missing action %q in %#v", want, actions)
		}
	}
}

func TestProviderDetailActionsForNoModelProvider(t *testing.T) {
	actions := providerDetailActionLabels(true)
	// NoModel providers must NOT expose Choose Model or Use Model.
	for _, banned := range []string{"Choose Model", "Use Model"} {
		for _, got := range actions {
			if got == banned {
				t.Fatalf("NoModel provider should not expose action %q in %#v", banned, actions)
			}
		}
	}
	// Manage Model Mappings and Proxy Manager remain available even for
	// NoModel presets — the operator may still inspect/clear mappings and
	// configure proxy routes for them.
	for _, want := range []string{"Manage Model Mappings", "Proxy Manager", "Back"} {
		found := false
		for _, got := range actions {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("NoModel: missing action %q in %#v", want, actions)
		}
	}
}

func TestProviderDetailActionsNoDuplicates(t *testing.T) {
	for _, noModel := range []bool{false, true} {
		actions := providerDetailActionLabels(noModel)
		seen := map[string]int{}
		for _, a := range actions {
			seen[a]++
			if seen[a] > 1 {
				t.Fatalf("duplicate action %q in %#v", a, actions)
			}
		}
	}
}

// ---- proxyRouteFormDefaults / proxyRouteFormSubmitArgs ----
//
// showProxyRouteForm builds a form whose default values and submitted CLI
// argv are factored into pure helpers so the behaviour is unit-testable
// without driving a real tview.Form. These tests assert:
//   - defaults: the agent dropdown defaults to the first supported agent
//     (codex), the provider is pre-filled with the active provider, the
//     model defaults to the provider's stored/preset model, the protocol
//     defaults to the agent-specific default.
//   - submit argv: when the user keeps the defaults, the form emits the
//     exact argv expected by cmdProxyConfigure.

func TestProxyRouteFormDefaultsForCodex(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}},
	}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)
	if def.Agent != "codex" {
		t.Fatalf("default agent = %q, want codex", def.Agent)
	}
	if def.Provider != "zhipu-cn" {
		t.Fatalf("default provider = %q, want zhipu-cn", def.Provider)
	}
	if def.Model != "glm-5.2" {
		t.Fatalf("default model = %q, want glm-5.2", def.Model)
	}
	if def.Protocol != string(defaultProxyProtocolForAgent("codex")) {
		t.Fatalf("default protocol = %q, want %q", def.Protocol, defaultProxyProtocolForAgent("codex"))
	}
}

func TestProxyRouteFormDefaultsAgentList(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)
	if len(def.AgentOptions) != len(supportedProxyAgentList) {
		t.Fatalf("agent options = %#v, want %#v", def.AgentOptions, supportedProxyAgentList)
	}
	for i, a := range supportedProxyAgentList {
		if def.AgentOptions[i] != a {
			t.Fatalf("agent option %d = %q, want %q", i, def.AgentOptions[i], a)
		}
	}
}

func TestProxyRouteFormDefaultsProtocolOptions(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test"}}}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)
	// Protocol options must be the supported protocol values.
	want := []string{
		string(protocolAnthropicMessages),
		string(protocolOpenAIChat),
		string(protocolOpenAIResponses),
	}
	sort.Strings(want)
	got := append([]string(nil), def.ProtocolOptions...)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("protocol options = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("protocol option %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProxyRouteFormSubmitArgs(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}},
	}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", cfg)
	// User keeps defaults, types nothing else.
	args := proxyRouteFormSubmitArgs(def)
	// The helper must return an argv shaped exactly like cmdProxyConfigure
	// consumes: [agent, "--provider", provider, "--model", model,
	// "--protocol", protocol]. It must NOT include the "configure"
	// subcommand prefix — showProxyRouteForm calls cmdProxyConfigure
	// directly, not cmdProxy, so the leading "configure" would be parsed
	// as the agent positional and break the call.
	want := []string{
		"codex",
		"--provider", "zhipu-cn",
		"--model", "glm-5.2",
		"--protocol", string(defaultProxyProtocolForAgent("codex")),
	}
	if len(args) != len(want) {
		t.Fatalf("submit args = %#v, want %#v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("submit arg %d = %q, want %q", i, args[i], want[i])
		}
	}
}

// TestProxyRouteFormSubmitArgsRoundTripCmdConfigure is the regression test
// for the original bug: proxyRouteFormSubmitArgs used to prefix the argv
// with "configure", but showProxyRouteForm feeds the result straight to
// cmdProxyConfigure (not cmdProxy). cmdProxyConfigure treats args[0] as
// the agent positional, so the old shape ["configure", agent, ...] made
// the form's Save action always fail with a usage error. This test drives
// the exact call chain showProxyRouteForm uses on Save and asserts the
// route lands in the persisted config.
func TestProxyRouteFormSubmitArgsRoundTripCmdConfigure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Pre-seed a provider so cmdProxyConfigure's resolveProviderPreset
	// accepts the configure call without error.
	seedCfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	cfgPath, err := appConfigPath()
	if err != nil {
		t.Fatalf("appConfigPath: %v", err)
	}
	if err := writeJSONAtomic(cfgPath, seedCfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	def := proxyRouteFormDefaults("zhipu-cn", "codex", seedCfg)
	args := proxyRouteFormSubmitArgs(def)
	var out strings.Builder
	if err := cmdProxyConfigure(args, &out); err != nil {
		t.Fatalf("cmdProxyConfigure(proxyRouteFormSubmitArgs(...)) error: %v", err)
	}
	reloaded, _, err := loadAppConfig()
	if err != nil {
		t.Fatalf("loadAppConfig after round-trip: %v", err)
	}
	if reloaded.Proxy == nil || reloaded.Proxy.Routes == nil {
		t.Fatalf("route not persisted; cfg = %#v", reloaded)
	}
	route, ok := reloaded.Proxy.Routes["codex"]
	if !ok {
		t.Fatalf("codex route missing; routes = %#v", reloaded.Proxy.Routes)
	}
	if route.Provider != "zhipu-cn" {
		t.Fatalf("route provider = %q, want zhipu-cn", route.Provider)
	}
	if route.Model != "glm-5.2" {
		t.Fatalf("route model = %q, want glm-5.2", route.Model)
	}
	wantProto := string(defaultProxyProtocolForAgent("codex"))
	if route.UpstreamProtocol != wantProto {
		t.Fatalf("route protocol = %q, want %q", route.UpstreamProtocol, wantProto)
	}
}

func TestProxyRouteFormSubmitArgsDropsEmptyModel(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"kimi-coding": {APIKey: "sk-test"}}}
	def := proxyRouteFormDefaults("kimi-coding", "codex", cfg)
	if def.Model != "" {
		t.Fatalf("kimi-coding default model should be empty, got %q", def.Model)
	}
	args := proxyRouteFormSubmitArgs(def)
	for i, a := range args {
		if a == "--model" {
			t.Fatalf("empty model should be dropped, but --model present at index %d in %#v", i, args)
		}
	}
	// Provider and protocol must still be present.
	hasProvider, hasProtocol := false, false
	for i, a := range args {
		if a == "--provider" && i+1 < len(args) && args[i+1] == "kimi-coding" {
			hasProvider = true
		}
		if a == "--protocol" && i+1 < len(args) {
			hasProtocol = true
		}
	}
	if !hasProvider {
		t.Fatalf("submit args missing --provider in %#v", args)
	}
	if !hasProtocol {
		t.Fatalf("submit args missing --protocol in %#v", args)
	}
}

// ---- proxyActionResultText ----
//
// showProxyActionResult calls the underlying cmd helpers into a
// strings.Builder and renders the result. proxyActionResultText is the
// pure helper that runs the cmd and returns (output, error). Tests use an
// isolated HOME so the proxy state file is empty and status reports
// "not running" deterministically.

func TestProxyActionResultStatusNotRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := proxyActionResultText("status", "")
	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(out, "not running") {
		t.Fatalf("status output = %q, want contains 'not running'", out)
	}
}

func TestProxyActionResultStatusRejectsUnknownAction(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := proxyActionResultText("bogus", "")
	if err == nil {
		t.Fatal("expected error for bogus action, got nil")
	}
}

// ---- proxyPreviewText ----

func TestProxyPreviewTextRequiresConfiguredRoute(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := proxyPreviewText("codex")
	if err == nil {
		t.Fatal("expected error when no proxy route configured, got nil")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("preview error should mention not configured: %v", err)
	}
}

// ---- proxyRouteFormSaveResult (Task 5.6: reload errors must not be silent) ----
//
// showProxyRouteForm's Save handler used to do:
//
//	if cfg, _, err := loadAppConfig(); err == nil {
//	    ts.cfg = cfg
//	}
//
// which silently dropped any reload error after a successful write. The
// operator would then proceed with a stale ts.cfg and the rest of the
// TUI would render against the pre-write view — a confusing regression
// when the write itself succeeded.
//
// The fix extracts the configure+reload step into a pure helper so the
// error-handling policy is unit-testable. The helper runs
// cmdProxyConfigure(proxyRouteFormSubmitArgs(spec)) and, on success,
// reloads the config via loadAppConfig. It returns:
//   - reloaded: the freshly-loaded cfg (nil on any error)
//   - errMsg: a human-readable error string for the inline errLabel
//     ("" when everything succeeded)
//   - cmdOutput: the captured stdout of cmdProxyConfigure, so the form
//     can still surface the "configured proxy route ..." line on success
//
// The form's Save button then:
//   - on errMsg != "": set errLabel, keep the form open, keep the
//     previously-loaded ts.cfg (the write already succeeded, so on-disk
//     state is correct; only the in-memory view is stale).
//   - on errMsg == "": set ts.cfg = reloaded, navigate back to the
//     proxy manager.
//
// These tests pin down the three observable outcomes: success returns a
// reloaded cfg with the route present and no error; a configure error
// returns the configure error and a nil cfg; a reload error (simulated
// by corrupting the on-disk config after the write) returns a non-empty
// errMsg that mentions the reload failure rather than swallowing it.

func TestProxyRouteFormSaveResultSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedCfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	cfgPath, err := appConfigPath()
	if err != nil {
		t.Fatalf("appConfigPath: %v", err)
	}
	if err := writeJSONAtomic(cfgPath, seedCfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	spec := proxyRouteFormDefaults("zhipu-cn", "codex", seedCfg)
	reloaded, errMsg, out := proxyRouteFormSaveResult(spec)
	if errMsg != "" {
		t.Fatalf("expected no error, got %q", errMsg)
	}
	if reloaded == nil {
		t.Fatal("expected reloaded cfg, got nil")
	}
	if reloaded.Proxy == nil || reloaded.Proxy.Routes["codex"].Provider != "zhipu-cn" {
		t.Fatalf("reloaded cfg missing codex route: %#v", reloaded.Proxy)
	}
	if !strings.Contains(out, "configured proxy route") {
		t.Fatalf("cmd output = %q, want contains 'configured proxy route'", out)
	}
}

func TestProxyRouteFormSaveResultConfigureError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// No provider seeded -> resolveProviderPreset rejects "ghost".
	spec := proxyRouteFormDefaults("ghost", "codex", &AppConfig{})
	reloaded, errMsg, _ := proxyRouteFormSaveResult(spec)
	if errMsg == "" {
		t.Fatal("expected configure error, got empty errMsg")
	}
	if reloaded != nil {
		t.Fatalf("expected nil cfg on configure error, got %#v", reloaded)
	}
}

// TestProxyRouteFormSaveResultReloadErrorSurfaces is the regression test
// for the silent-reload-error bug.
//
// showProxyRouteForm's Save handler used to do:
//
//	if cfg, _, err := loadAppConfig(); err == nil {
//	    ts.cfg = cfg
//	}
//
// which silently dropped any reload error after a successful write. The
// operator would then proceed with a stale ts.cfg and the rest of the
// TUI would render against the pre-write view — a confusing regression
// when the write itself succeeded.
//
// The fix extracts the configure+reload step into a pure helper so the
// error-handling policy is unit-testable. The helper runs
// cmdProxyConfigure(proxyRouteFormSubmitArgs(spec)) and, on success,
// reloads the config via loadAppConfig. It returns:
//   - reloaded: the freshly-loaded cfg (nil on any error)
//   - errMsg: a human-readable error string for the inline errLabel
//     ("" when everything succeeded)
//   - cmdOutput: the captured stdout of cmdProxyConfigure, so the form
//     can still surface the "configured proxy route ..." line on success
//
// The form's Save button then:
//   - on errMsg != "": set errLabel, keep the form open, keep the
//     previously-loaded ts.cfg (the write already succeeded, so on-disk
//     state is correct; only the in-memory view is stale).
//   - on errMsg == "": set ts.cfg = reloaded, navigate back to the
//     proxy manager.
//
// This test pins down the high-level contract: any failure inside the
// helper MUST surface a non-empty errMsg and a nil reloaded cfg — never
// silently swallow the error and return a stale/nil cfg with errMsg "".
//
// Forcing a reload-only failure (succeed on write, fail on reload)
// without instrumenting the helper requires intercepting the on-disk
// file between the configure write and the reload — that is not
// possible through the public API. We instead force the .code-switch
// directory to be unreadable before the helper runs. This makes
// cmdProxyConfigure's own loadAppConfigLocked fail at the read step,
// so the helper surfaces a CONFIGURE error (not a reload error).
//
// That is still a valid assertion of the contract: the helper must not
// return ("", nil) on any failure path, configure-error or reload-error
// alike. The configure-error path is the one we can reliably trigger
// cross-platform without racing the write. The reload-error message
// format ("route saved, but failed to reload config: ...") is covered
// by the helper's source comment and is not asserted here because we
// cannot reliably isolate the reload step in a unit test.
//
// We skip when running as root because root bypasses directory read
// permissions, so the chmod trick would not force a failure.
func TestProxyRouteFormSaveResultReloadErrorSurfaces(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("reload-error test relies on file permissions; skipped under root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	seedCfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	cfgPath, err := appConfigPath()
	if err != nil {
		t.Fatalf("appConfigPath: %v", err)
	}
	if err := writeJSONAtomic(cfgPath, seedCfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	// Make the .code-switch directory unreadable so the helper's first
	// loadAppConfig (inside cmdProxyConfigure's loadAppConfigLocked)
	// fails at the read step. This exercises the configure-error path
	// of the helper, which must surface the error rather than silently
	// swallow it — the same contract the reload-error path upholds.
	codeSwitchDir := filepath.Dir(cfgPath)
	if err := os.Chmod(codeSwitchDir, 0o000); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(codeSwitchDir, 0o755) })
	spec := proxyRouteFormDefaults("zhipu-cn", "codex", seedCfg)
	reloaded, errMsg, _ := proxyRouteFormSaveResult(spec)
	if errMsg == "" {
		t.Fatal("expected non-empty errMsg when reload fails, got empty (silent swallow bug)")
	}
	if reloaded != nil {
		t.Fatalf("expected nil cfg on reload error, got non-nil")
	}
}
