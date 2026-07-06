package main

import (
	"reflect"
	"testing"

	"github.com/rivo/tview"
)

func TestModelSelectionActionLabels(t *testing.T) {
	want := []string{actionLabelLaunch, actionLabelSetDefault, actionLabelBack}
	if got := modelSelectionActionLabels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("model selection actions = %#v, want %#v", got, want)
	}
}

func TestTUIStateShowModelActionsForClaudeOmitsLaunch(t *testing.T) {
	ts := newModelActionTestState()

	ts.showModelActions("deepseek", "deepseek-v3.2-exp", "detail")

	got := frontModelActionLabels(t, ts)
	want := []string{actionLabelSetDefault, actionLabelBack}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claude model action labels = %#v, want %#v", got, want)
	}
}

func TestTUIStateShowModelActionsForOpencodeIncludesLaunch(t *testing.T) {
	ts := newModelActionTestState()
	ts.agent = agentOpencode

	ts.showModelActions("deepseek", "deepseek-v3.2-exp", "detail")

	got := frontModelActionLabels(t, ts)
	want := []string{actionLabelLaunch, actionLabelSetDefault, actionLabelBack}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("opencode model action labels = %#v, want %#v", got, want)
	}
}

func frontModelActionLabels(t *testing.T, ts *tuiState) []string {
	t.Helper()
	pageName, primitive := ts.pages.GetFrontPage()
	if pageName != "model-actions" {
		t.Fatalf("front page = %q, want model-actions", pageName)
	}
	page, ok := primitive.(*tview.Flex)
	if !ok {
		t.Fatalf("model-actions page type = %T, want *tview.Flex", primitive)
	}
	actions, ok := page.GetItem(1).(*tview.List)
	if !ok {
		t.Fatalf("model-actions list type = %T, want *tview.List", page.GetItem(1))
	}
	labels := make([]string, 0, actions.GetItemCount())
	for i := 0; i < actions.GetItemCount(); i++ {
		main, _ := actions.GetItemText(i)
		labels = append(labels, main)
	}
	return labels
}

func TestTUIStateShowModelActionsDoesNotFinishUntilActionChosen(t *testing.T) {
	ts := newModelActionTestState()

	ts.showModelActions("deepseek", "deepseek-v3.2-exp", "detail")

	pageName, _ := ts.pages.GetFrontPage()
	if pageName != "model-actions" {
		t.Fatalf("front page = %q, want model-actions", pageName)
	}
	if ts.result.Provider != "" || ts.result.Model != "" || ts.result.Launch {
		t.Fatalf("model action page should not finish selection, got %+v", ts.result)
	}
}

func TestTUIStateSelectModelForActionShowsActionsWithSavedKey(t *testing.T) {
	ts := newModelActionTestState()

	ts.selectModelForAction("deepseek", "detail", "deepseek-v3.2-exp")

	pageName, _ := ts.pages.GetFrontPage()
	if pageName != "model-actions" {
		t.Fatalf("front page = %q, want model-actions", pageName)
	}
	if ts.result.Provider != "" || ts.result.Model != "" || ts.result.Launch {
		t.Fatalf("selecting a model should wait for an action, got %+v", ts.result)
	}
}

func TestTUIStateSelectModelForActionRequestsMissingKeyBeforeActions(t *testing.T) {
	ts := newModelActionTestState()
	ts.cfg = &AppConfig{Providers: map[string]StoredProvider{}}

	ts.selectModelForAction("deepseek", "detail", "deepseek-v3.2-exp")

	pageName, _ := ts.pages.GetFrontPage()
	if pageName != "key" {
		t.Fatalf("front page = %q, want key", pageName)
	}
	if ts.result.Provider != "" || ts.result.Model != "" || ts.result.Launch {
		t.Fatalf("missing-key flow should wait for key/action, got %+v", ts.result)
	}
}

func TestTUIStateRunModelSelectionActionLaunch(t *testing.T) {
	ts := newModelActionTestState()
	ts.agent = agentOpencode

	ts.runModelSelectionAction(actionLabelLaunch, "deepseek", "deepseek-v3.2-exp", "detail")

	if ts.result.Provider != "deepseek" || ts.result.Model != "deepseek-v3.2-exp" {
		t.Fatalf("result = %+v", ts.result)
	}
	if !ts.result.Launch {
		t.Fatalf("Launch action should set Launch=true, got %+v", ts.result)
	}
}

func TestTUIStateRunModelSelectionActionLaunchIgnoredForClaude(t *testing.T) {
	ts := newModelActionTestState()

	ts.runModelSelectionAction(actionLabelLaunch, "deepseek", "deepseek-v3.2-exp", "detail")

	if ts.result.Provider != "" || ts.result.Model != "" || ts.result.Launch {
		t.Fatalf("claude Launch action should be ignored, got %+v", ts.result)
	}
}

func TestTUIStateRunModelSelectionActionSetDefault(t *testing.T) {
	ts := newModelActionTestState()

	ts.runModelSelectionAction(actionLabelSetDefault, "deepseek", "deepseek-v3.2-exp", "detail")

	if ts.result.Provider != "deepseek" || ts.result.Model != "deepseek-v3.2-exp" {
		t.Fatalf("result = %+v", ts.result)
	}
	if ts.result.Launch {
		t.Fatalf("Set as default should not set Launch=true, got %+v", ts.result)
	}
}

func TestTUIStateRunModelSelectionActionBack(t *testing.T) {
	ts := newModelActionTestState()

	ts.runModelSelectionAction(actionLabelBack, "deepseek", "deepseek-v3.2-exp", "detail")

	pageName, _ := ts.pages.GetFrontPage()
	if pageName != "models" {
		t.Fatalf("front page = %q, want models", pageName)
	}
	if ts.result.Provider != "" || ts.result.Model != "" || ts.result.Launch {
		t.Fatalf("Back should not finish selection, got %+v", ts.result)
	}
}

func newModelActionTestState() *tuiState {
	return &tuiState{
		app:           tview.NewApplication(),
		pages:         tview.NewPages(),
		cfg:           &AppConfig{Providers: map[string]StoredProvider{"deepseek": {APIKey: "sk-test"}}},
		agent:         agentClaude,
		typedAPIKeys:  map[string]string{},
		resetKeys:     map[string]bool{},
		customModels:  map[string]string{},
		tierOverrides: map[string]StoredProvider{},
		tierInfo:      tview.NewTextView(),
	}
}
