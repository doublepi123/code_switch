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
	want := []string{actionLabelLaunch, actionLabelSetDefault, actionLabelModels, actionLabelEditAPIKey, actionLabelAdvanced, actionLabelBack}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("workspace actions = %#v, want %#v", labels, want)
	}
	if got := frontWorkspaceCurrentItem(t, ts); got != 0 {
		t.Fatalf("default workspace action index = %d, want Launch at 0", got)
	}
}

func TestTUIStateLaunchSelectedProviderWithSavedKeyFinishesLaunch(t *testing.T) {
	ts := newWorkspaceTestState(agentClaude)

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

	ts.saveSelectedProvider("openrouter")

	if ts.result.Provider != "openrouter" || ts.result.Model == "" {
		t.Fatalf("save result = %+v, want provider and default model", ts.result)
	}
	if ts.result.Launch {
		t.Fatalf("save selected provider should not set Launch=true, got %+v", ts.result)
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
		app:           tview.NewApplication(),
		pages:         tview.NewPages(),
		cfg:           &AppConfig{Providers: map[string]StoredProvider{"deepseek": {APIKey: "sk-deepseek"}, "openrouter": {APIKey: "sk-openrouter"}}},
		agent:         agent,
		currentModel:  "",
		typedAPIKeys:  map[string]string{},
		resetKeys:     map[string]bool{},
		customModels:  map[string]string{},
		tierOverrides: map[string]StoredProvider{},
		detailText:    tview.NewTextView(),
		tierInfo:      tview.NewTextView(),
	}
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
