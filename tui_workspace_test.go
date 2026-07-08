package main

import (
	"bufio"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/rivo/tview"
)

func TestTUIStateShowProviderWorkspaceLaunchFirst(t *testing.T) {
	ts := newWorkspaceTestState(agentCodex)

	ts.showProviderWorkspace("openrouter")

	labels := frontWorkspaceActionLabels(t, ts)
	want := []string{actionLabelLaunch, actionLabelSetDefault, actionLabelModels, actionLabelEditAPIKey, actionLabelEditBaseURL, actionLabelChangeProtocol, actionLabelAdvanced, actionLabelBack}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("workspace actions = %#v, want %#v", labels, want)
	}
	if got := frontWorkspaceCurrentItem(t, ts); got != 0 {
		t.Fatalf("default workspace action index = %d, want Launch at 0", got)
	}
}

func TestTUIStateShowProviderWorkspaceDisplaysCustomBaseURLAndProtocol(t *testing.T) {
	ts := newWorkspaceTestState(agentClaude)
	ts.customBaseURLs["deepseek"] = "https://custom.example.com/anthropic"
	ts.customProtocols["deepseek"] = protocolOpenAIResponses

	ts.showProviderWorkspace("deepseek")

	text := frontWorkspaceInfoText(t, ts)
	for _, want := range []string{
		"Base URL: https://custom.example.com/anthropic (custom)",
		"API Protocol: openai-responses",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("workspace text missing %q:\n%s", want, text)
		}
	}
}

func TestTUIStateLaunchSelectedProviderWithSavedKeyFinishesLaunch(t *testing.T) {
	ts := newWorkspaceTestState(agentClaude)
	ts.customBaseURLs["deepseek"] = "https://launch.example.com/v1"
	ts.customProtocols["deepseek"] = protocolOpenAIChat

	ts.launchSelectedProvider("deepseek")

	pageName, _ := ts.pages.GetFrontPage()
	if pageName != "agent-select" {
		t.Fatalf("front page = %q, want agent-select", pageName)
	}
	if ts.result.Provider != "" || ts.result.Launch {
		t.Fatalf("launch should wait for agent selection, got %+v", ts.result)
	}

	ts.selectLaunchAgent(agentClaude, "deepseek")

	if ts.result.Provider != "deepseek" || ts.result.Model == "" {
		t.Fatalf("launch result = %+v, want provider and default model", ts.result)
	}
	if ts.result.Agent != string(agentClaude) {
		t.Fatalf("launch agent = %q, want %q", ts.result.Agent, agentClaude)
	}
	if !ts.result.Launch {
		t.Fatalf("launch selected provider should set Launch=true, got %+v", ts.result)
	}
	if ts.result.BaseURL != "https://launch.example.com/v1" || ts.result.Protocol != protocolOpenAIChat {
		t.Fatalf("launch custom endpoint = (%q, %q), want custom values", ts.result.BaseURL, ts.result.Protocol)
	}
}

func TestTUIStateLaunchAgentSelectionLabels(t *testing.T) {
	ts := newWorkspaceTestState(agentClaude)

	ts.showLaunchAgentSelect("deepseek")

	pageName, primitive := ts.pages.GetFrontPage()
	if pageName != "agent-select" {
		t.Fatalf("front page = %q, want agent-select", pageName)
	}
	page, ok := primitive.(*tview.Flex)
	if !ok {
		t.Fatalf("agent-select page type = %T, want *tview.Flex", primitive)
	}
	list, ok := page.GetItem(1).(*tview.List)
	if !ok {
		t.Fatalf("agent-select list type = %T, want *tview.List", page.GetItem(1))
	}
	labels := make([]string, 0, list.GetItemCount())
	for i := 0; i < list.GetItemCount(); i++ {
		main, _ := list.GetItemText(i)
		labels = append(labels, main)
	}
	want := []string{agentDisplayName(agentClaude), agentDisplayName(agentCodex), agentDisplayName(agentOpencode), actionLabelBack}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("agent labels = %#v, want %#v", labels, want)
	}
}

func TestTUIStateLaunchSelectedProviderMissingKeyOpensKeyForm(t *testing.T) {
	ts := newWorkspaceTestState(agentClaude)
	ts.cfg = &AppConfig{Providers: map[string]StoredProvider{}}

	ts.launchSelectedProvider("deepseek")

	pageName, _ := ts.pages.GetFrontPage()
	if pageName != "agent-select" {
		t.Fatalf("front page after Launch = %q, want agent-select", pageName)
	}
	ts.selectLaunchAgent(agentClaude, "deepseek")

	pageName, _ = ts.pages.GetFrontPage()
	if pageName != "key" {
		t.Fatalf("front page = %q, want key", pageName)
	}
	if ts.result.Provider != "" || ts.result.Launch {
		t.Fatalf("missing-key launch should wait for key, got %+v", ts.result)
	}
}

func TestTUIStateSaveSelectedProviderWithSavedKeyDoesNotLaunch(t *testing.T) {
	ts := newWorkspaceTestState(agentCodex)
	ts.customBaseURLs["openrouter"] = "https://save.example.com/v1"
	ts.customProtocols["openrouter"] = protocolOpenAIResponses

	ts.saveSelectedProvider("openrouter")

	if ts.result.Provider != "openrouter" || ts.result.Model == "" {
		t.Fatalf("save result = %+v, want provider and default model", ts.result)
	}
	if ts.result.Launch {
		t.Fatalf("save selected provider should not set Launch=true, got %+v", ts.result)
	}
	if ts.result.BaseURL != "https://save.example.com/v1" || ts.result.Protocol != protocolOpenAIResponses {
		t.Fatalf("save custom endpoint = (%q, %q), want custom values", ts.result.BaseURL, ts.result.Protocol)
	}
}

func TestResolveSwitchPresetUsesStoredEndpointOverrideForBuiltinProvider(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{
		"deepseek": {
			BaseURL:  "https://override.example.com/v1",
			Protocol: protocolOpenAIChat,
			Model:    "deepseek-v3.2-exp",
		},
	}}

	preset, err := resolveAgentSwitchPreset(agentClaude, "deepseek", cfg, "")
	if err != nil {
		t.Fatalf("resolveAgentSwitchPreset: %v", err)
	}
	endpoint, ok := preset.presetEndpoint(protocolOpenAIChat)
	if !ok {
		t.Fatalf("expected openai-chat endpoint in preset %+v", preset)
	}
	if endpoint.BaseURL != "https://override.example.com/v1" {
		t.Fatalf("endpoint BaseURL = %q, want override", endpoint.BaseURL)
	}
}

func TestResolveAgentSwitchPresetUsesCodexStoredEndpointOverride(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{},
		Agents: map[string]AgentConfig{
			string(agentCodex): {Providers: map[string]StoredProvider{
				"deepseek": {
					BaseURL:  "https://codex-override.example.com/v1",
					Protocol: protocolOpenAIResponses,
					Model:    "deepseek-v3.2-exp",
				},
			}},
		},
	}

	preset, err := resolveAgentSwitchPreset(agentCodex, "deepseek", cfg, "")
	if err != nil {
		t.Fatalf("resolveAgentSwitchPreset: %v", err)
	}
	endpoint, ok := preset.presetEndpoint(protocolOpenAIResponses)
	if !ok {
		t.Fatalf("expected openai-responses endpoint in preset %+v", preset)
	}
	if endpoint.BaseURL != "https://codex-override.example.com/v1" {
		t.Fatalf("endpoint BaseURL = %q, want codex override", endpoint.BaseURL)
	}
}

func TestResolveAgentSwitchPresetUsesOpencodeStoredEndpointOverride(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{},
		Agents: map[string]AgentConfig{
			string(agentOpencode): {Providers: map[string]StoredProvider{
				"deepseek": {
					BaseURL:  "https://opencode-override.example.com/v1",
					Protocol: protocolOpenAIChat,
					Model:    "deepseek-v3.2-exp",
				},
			}},
		},
	}

	preset, err := resolveAgentSwitchPreset(agentOpencode, "deepseek", cfg, "")
	if err != nil {
		t.Fatalf("resolveAgentSwitchPreset: %v", err)
	}
	endpoint, ok := preset.presetEndpoint(protocolOpenAIChat)
	if !ok {
		t.Fatalf("expected openai-chat endpoint in preset %+v", preset)
	}
	if endpoint.BaseURL != "https://opencode-override.example.com/v1" {
		t.Fatalf("endpoint BaseURL = %q, want opencode override", endpoint.BaseURL)
	}
}

func TestTUIStateRunModelSelectionActionLaunchAllowsClaude(t *testing.T) {
	ts := newModelActionTestState()

	ts.runModelSelectionAction(actionLabelLaunch, "deepseek", "deepseek-v3.2-exp", "detail")

	if ts.result.Provider != "deepseek" || ts.result.Model != "deepseek-v3.2-exp" {
		t.Fatalf("result = %+v", ts.result)
	}
	if !ts.result.Launch {
		t.Fatalf("Claude Launch action should set Launch=true, got %+v", ts.result)
	}
}

func TestPromptConfigureSelectionFallbackDefaultsToLaunch(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{"deepseek": {APIKey: "sk-test", AuthEnv: "ANTHROPIC_AUTH_TOKEN"}}}
	var out bytes.Buffer
	selection, err := promptConfigureSelectionFallback(bufio.NewReader(strings.NewReader("deepseek\n\n\n")), &out, cfg, agentClaude, "", "")
	if err != nil {
		t.Fatalf("promptConfigureSelectionFallback: %v", err)
	}
	if selection.Provider != "deepseek" || selection.Model == "" {
		t.Fatalf("selection = %+v, want deepseek with default model", selection)
	}
	if !selection.Launch {
		t.Fatalf("fallback default action should launch, got %+v", selection)
	}
}

func newWorkspaceTestState(agent AgentName) *tuiState {
	return &tuiState{
		app:             tview.NewApplication(),
		pages:           tview.NewPages(),
		cfg:             &AppConfig{Providers: map[string]StoredProvider{"deepseek": {APIKey: "sk-deepseek"}, "openrouter": {APIKey: "sk-openrouter"}}},
		agent:           agent,
		currentModel:    "",
		typedAPIKeys:    map[string]string{},
		resetKeys:       map[string]bool{},
		customModels:    map[string]string{},
		customBaseURLs:  map[string]string{},
		customProtocols: map[string]ProviderProtocol{},
		tierOverrides:   map[string]StoredProvider{},
		detailText:      tview.NewTextView(),
		tierInfo:        tview.NewTextView(),
	}
}

func frontWorkspaceInfoText(t *testing.T, ts *tuiState) string {
	t.Helper()
	pageName, primitive := ts.pages.GetFrontPage()
	if pageName != "provider-workspace" {
		t.Fatalf("front page = %q, want provider-workspace", pageName)
	}
	page, ok := primitive.(*tview.Flex)
	if !ok {
		t.Fatalf("workspace page type = %T, want *tview.Flex", primitive)
	}
	info, ok := page.GetItem(0).(*tview.TextView)
	if !ok {
		t.Fatalf("workspace info type = %T, want *tview.TextView", page.GetItem(0))
	}
	return info.GetText(true)
}

func frontWorkspaceActionLabels(t *testing.T, ts *tuiState) []string {
	t.Helper()
	list := frontWorkspaceActionList(t, ts)
	labels := make([]string, 0, list.GetItemCount())
	for i := 0; i < list.GetItemCount(); i++ {
		main, _ := list.GetItemText(i)
		labels = append(labels, main)
	}
	return labels
}

func frontWorkspaceCurrentItem(t *testing.T, ts *tuiState) int {
	t.Helper()
	return frontWorkspaceActionList(t, ts).GetCurrentItem()
}

func frontWorkspaceActionList(t *testing.T, ts *tuiState) *tview.List {
	t.Helper()
	pageName, primitive := ts.pages.GetFrontPage()
	if pageName != "provider-workspace" {
		t.Fatalf("front page = %q, want provider-workspace", pageName)
	}
	page, ok := primitive.(*tview.Flex)
	if !ok {
		t.Fatalf("workspace page type = %T, want *tview.Flex", primitive)
	}
	list, ok := page.GetItem(1).(*tview.List)
	if !ok {
		t.Fatalf("workspace action list type = %T, want *tview.List", page.GetItem(1))
	}
	return list
}
