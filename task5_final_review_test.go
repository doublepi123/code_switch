package main

import (
	"testing"

	"github.com/rivo/tview"
)

// task5_final_review_test.go — Task5 final review fixes (RED tests).
//
// These tests pin down three issues identified in the Task5 final review:
//
//  1. showProxyRouteForm panics with a nil-pointer dereference when the
//     tview Form's Agent DropDown is added, because form.AddDropDown
//     internally calls SetCurrentOption(initialOption) which fires the
//     selected callback synchronously during construction — and the
//     callback dereferences the protocolDropDown variable, which is not
//     assigned until AFTER the Agent DropDown is added. This test
//     constructs the form via the real tuiState method and asserts it
//     does not panic.
//  2. providerDetailActionLabels is documented as the "ordered superset"
//     of provider detail actions and its doc comment explicitly lists
//     "Switch (default)" as one of the labels it surfaces "for
//     ordering/test purposes". But the function body omits
//     actionLabelSwitchDefault entirely, so the superset is NOT a true
//     superset of what showDetail renders. This test pins down the
//     contract by asserting actionLabelSwitchDefault is present and in
//     the relative order showDetail renders (between Proxy Manager and
//     Edit API Key).
//  3. TestProxyRouteFormApplyAgentKeepsCustomProtocol (in
//     task5_tui_test.go) had a name + comment that described a behaviour
//     the helper does NOT implement (it talked about "keeping" a custom
//     protocol across agent changes, while the actual design choice is
//     "re-sync the protocol to the agent-specific default on every
//     agent change"). The test body actually asserted the re-sync
//     behaviour, so only the name/comment drifted. The fix renames it to
//     TestProxyRouteFormApplyAgentResetsProtocolOnEveryChange and
//     rewrites the comment; this file does not duplicate that test.

// ---- Issue 1: showProxyRouteForm must not panic on construction ----
//
// The form previously wired the Agent DropDown's selected callback to a
// closure that dereferences a protocolDropDown *tview.DropDown variable
// declared but not yet assigned. form.AddDropDown internally calls
// SetCurrentOption(initialOption), which fires the selected callback
// synchronously (see tview dropdown.go: SetCurrentOption "will also
// trigger the 'selected' callback"). When the callback runs during
// construction, protocolDropDown is still nil and
// protocolDropDown.SetCurrentOption(protoIdx) dereferences a nil
// pointer.
//
// This test drives the real showProxyRouteForm against a minimal but
// valid tuiState (pages + app + cfg) and asserts the form builds without
// panicking. It does NOT drive the tview event loop — the panic, if
// present, fires during form construction itself.
func TestShowProxyRouteFormBuildsWithoutPanic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("showProxyRouteForm panicked during construction: %v", r)
		}
	}()
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	ts := &tuiState{
		app:          tview.NewApplication(),
		pages:        tview.NewPages(),
		cfg:          cfg,
		agent:        agentCodex,
		typedAPIKeys: map[string]string{},
		resetKeys:    map[string]bool{},
		customModels: map[string]string{},
	}
	// showProxyRouteForm adds a page to ts.pages and switches to it. We
	// never call app.Run(), so no rendering happens — but the form is
	// fully constructed (Agent DropDown, Model InputField, Protocol
	// DropDown, Save/Cancel buttons, InputCapture) which is exactly the
	// surface where the panic fires.
	ts.showProxyRouteForm("zhipu-cn", "codex")
}

// Same regression for a claude-targeted form: the protocol default for
// claude is openai-responses, which exercises a non-zero protocolIdx
// path through the same callback.
func TestShowProxyRouteFormBuildsForClaudeAgentWithoutPanic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("showProxyRouteForm(claude) panicked during construction: %v", r)
		}
	}()
	cfg := &AppConfig{Providers: map[string]StoredProvider{"zhipu-cn": {APIKey: "sk-test", Model: "glm-5.2"}}}
	ts := &tuiState{
		app:          tview.NewApplication(),
		pages:        tview.NewPages(),
		cfg:          cfg,
		agent:        agentCodex,
		typedAPIKeys: map[string]string{},
		resetKeys:    map[string]bool{},
		customModels: map[string]string{},
	}
	ts.showProxyRouteForm("zhipu-cn", "claude")
}

func TestProviderDetailActionLabelsIncludesSetDefault(t *testing.T) {
	// Non-NoModel provider: Set as default must be present.
	labels := providerDetailActionLabels(false)
	if indexOf(labels, actionLabelSetDefault) < 0 {
		t.Fatalf("actionLabelSetDefault %q missing from non-NoModel superset %#v", actionLabelSetDefault, labels)
	}
	// NoModel providers can also be switched (NoAPIKey presets, or
	// hasConfigurableKey), so Set as default must be present in the
	// NoModel superset too. showDetail's canSwitch is independent of
	// NoModel (it depends on NoAPIKey / configurable key), so the
	// superset must always advertise Set as default.
	labelsNoModel := providerDetailActionLabels(true)
	if indexOf(labelsNoModel, actionLabelSetDefault) < 0 {
		t.Fatalf("actionLabelSetDefault %q missing from NoModel superset %#v", actionLabelSetDefault, labelsNoModel)
	}
}

// The relative order showDetail renders is:
//
//	Choose Model, Use Model, Manage Model Mappings, Proxy Manager,
//	Launch, Set as default, Edit API Key, Edit Tiers, Back
//
// providerDetailActionLabels must keep that relative order so a test
// that drives the helper stays aligned with what showDetail actually
// renders. This is the order-pinning regression for the Switch (default)
// insertion point.
func TestProviderDetailActionLabelsSetDefaultOrder(t *testing.T) {
	labels := providerDetailActionLabels(false)
	wantOrder := []string{
		actionLabelChooseModel,
		actionLabelUseModel,
		actionLabelManageMappings,
		actionLabelProxyManager,
		actionLabelLaunch,
		actionLabelSetDefault,
		actionLabelEditAPIKey,
		actionLabelEditTiers,
		actionLabelBack,
	}
	lastIdx := -1
	for _, w := range wantOrder {
		idx := indexOf(labels, w)
		if idx < 0 {
			t.Fatalf("label %q missing from %#v", w, labels)
		}
		if idx <= lastIdx {
			t.Fatalf("label %q at index %d breaks expected order, lastIdx=%d (got=%#v)", w, idx, lastIdx, labels)
		}
		lastIdx = idx
	}
}
