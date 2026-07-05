package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// providerDetailActionLabels returns the ordered superset of action labels
// the provider detail page may render. It is the single source of truth
// for the LABEL STRINGS and their ordering, so the TUI can be tested for
// action presence without driving the real tview.List. It is NOT the
// single source of truth for which actions actually appear on a given
// provider: showDetail filters this superset at render time based on
// preset flags (NoModel, NoAPIKey, agent != opencode, etc.).
//
// Rules:
//   - Choose Model and Use Model only appear for providers whose preset
//     advertises a model list (NoModel == false). NoModel providers
//     (e.g. kimi-coding) are configured by API key alone and ignore the
//     model field, so model selection is intentionally hidden.
//   - Manage Model Mappings and Proxy Manager always appear, regardless of
//     NoModel. The operator may still inspect/clear mappings and configure
//     proxy routes for NoModel providers — the proxy uses its own model
//     resolution and does not depend on the provider preset's NoModel flag.
//   - Switch (default), Edit API Key, Edit Tiers, and Back preserve
//     their existing semantics and are surfaced here in the relative
//     order showDetail renders them (Switch between Proxy Manager and
//     Edit API Key, Edit Tiers after Edit API Key, Back last). The
//     actual visibility of Switch, Edit API Key, and Edit Tiers is
//     still refined inside showDetail based on preset flags (canSwitch,
//     NoAPIKey, agent != opencode, NoModel). The labels list is the
//     superset; showDetail filters at rendering time.
//
// Action label constants shared between providerDetailActionLabels and
// showDetail. They are the single source of truth for the action labels'
// spelling so a typo in either place cannot drift away from the other.
// New actions should be added here, referenced from
// providerDetailActionLabels, and rendered in showDetail.
const (
	actionLabelChooseModel    = "Choose Model"
	actionLabelUseModel       = "Use Model"
	actionLabelManageMappings = "Manage Model Mappings"
	actionLabelProxyManager   = "Proxy Manager"
	actionLabelEditAPIKey     = "Edit API Key"
	actionLabelEditTiers         = "Edit Tiers"
	actionLabelEditContextWindow = "Edit Context Window"
	actionLabelSwitchDefault  = "Switch (default)"
	actionLabelBack           = "Back"
)

func providerDetailActionLabels(noModel bool) []string {
	labels := make([]string, 0, 8)
	if !noModel {
		labels = append(labels, actionLabelChooseModel, actionLabelUseModel)
	}
	labels = append(labels,
		actionLabelManageMappings,
		actionLabelProxyManager,
		actionLabelSwitchDefault,
		actionLabelEditAPIKey,
		actionLabelEditTiers,
		actionLabelBack,
	)
	return labels
}

// useModelFormDefault resolves the initial value the "Use Model" form
// should pre-fill its Model input with. Resolution order:
//  1. A session-typed custom model (ts.customModels[provider]) wins —
//     if the operator already typed/edited something this session, keep
//     it so they can correct a typo without retyping.
//  2. The provider's stored model in the agent-appropriate namespace.
//  3. The preset's built-in Model (resolveAgentProviderPreset) —
//     for a provider that has never had its model edited, fall back to
//     the preset default so the field is not empty.
//
// NoModel providers (e.g. kimi-coding) yield "" — the page itself is
// hidden for them, but the helper must still be safe to call. Whitespace
// is trimmed so the form never starts with "  ".
func useModelFormDefault(agent AgentName, cfg *AppConfig, provider, customModel string) string {
	if custom := strings.TrimSpace(customModel); custom != "" {
		return custom
	}
	if cfg != nil {
		switch agent {
		case agentCodex:
			if stored := codexProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
				return strings.TrimSpace(stored.Model)
			}
		case agentOpencode:
			if stored := opencodeProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
				return strings.TrimSpace(stored.Model)
			}
		default:
			if stored := cfg.Providers[provider]; strings.TrimSpace(stored.Model) != "" {
				return strings.TrimSpace(stored.Model)
			}
		}
		if preset, err := resolveAgentProviderPreset(agent, provider, cfg); err == nil && !preset.NoModel {
			return strings.TrimSpace(preset.Model)
		}
	}
	return ""
}

// showUseModelForm opens a single-field form that lets the operator set the
// provider's default model (and the matching "default" model mapping) in one
// locked transaction. The form mirrors `cs use-model <provider> <model>`:
//
//   - On Save: loadAppConfigLocked -> useModelForProvider (validates provider,
//     rejects NoModel presets, rejects unsupported opencode-go models,
//     canonicalizes aliases, persists model + default mapping) ->
//     writeJSONAtomic -> refresh ts.cfg -> return to detail page.
//   - On Cancel or Esc: return to the detail page without writing.
//   - Validation errors are surfaced inline via errLabel so the operator can
//     correct the input without losing context.
func (ts *tuiState) showUseModelForm(provider string) {
	modelValue := useModelFormDefault(ts.agent, ts.cfg, provider, ts.customModels[provider])
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	form.AddInputField("Model", modelValue, 0, nil, func(text string) {
		modelValue = text
	})
	form.AddButton("Save", func() {
		modelValue = strings.TrimSpace(modelValue)
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		if err := useModelForProvider(cfg, ts.agent, provider, modelValue); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		if err := writeJSONAtomic(path, cfg); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		unlock()
		ts.cfg = cfg
		ts.showDetail(provider, "detail")
	})
	form.AddButton("Cancel", func() { ts.showDetail(provider, "detail") })
	form.SetBorder(true)
	form.SetTitle(" Use Model ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showDetail(provider, "detail")
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s  |  Sets the provider's default model and the 'default' client-model mapping.", providerTitle(provider, ts.cfg)))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("use-model", page, true)
	ts.app.SetFocus(form)
}

func contextWindowFormDefault(agent AgentName, cfg *AppConfig, provider, model string) string {
	if cfg == nil {
		return ""
	}
	var stored StoredProvider
	switch agent {
	case agentCodex:
		stored = codexProviderConfig(cfg, provider)
	default:
		stored = cfg.Providers[provider]
	}
	if stored.ContextWindow > 0 {
		return strconv.Itoa(stored.ContextWindow)
	}
	return ""
}

// showContextWindowForm lets the operator override the Codex model-catalog
// context window for a provider/model pair. An empty value clears the override
// and falls back to name-based auto detection on the next switch.
func (ts *tuiState) showContextWindowForm(provider string) {
	model := useModelFormDefault(ts.agent, ts.cfg, provider, ts.customModels[provider])
	autoWindow := resolveModelContextWindow(model, 0)
	windowValue := contextWindowFormDefault(ts.agent, ts.cfg, provider, model)
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	form.AddInputField("Context", windowValue, 0, nil, func(text string) {
		windowValue = text
	})
	form.AddButton("Save", func() {
		window, err := parseContextWindowInput(windowValue)
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		agentCfg := agentConfig(cfg, agentCodex)
		stored := agentCfg.Providers[provider]
		stored.ContextWindow = window
		setAgentProviderConfig(cfg, agentCodex, provider, stored)
		if err := writeJSONAtomic(path, cfg); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		unlock()
		ts.cfg = cfg
		if err := refreshCodexModelCatalogForProvider(cfg, ts.codexDir, provider); err != nil {
			errLabel.SetText(err.Error())
			return
		}
		ts.showDetail(provider, "detail")
	})
	form.AddButton("Cancel", func() { ts.showDetail(provider, "detail") })
	form.SetBorder(true)
	form.SetTitle(" Context Window ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showDetail(provider, "detail")
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s  |  Model: %s  |  Auto: %d tokens  |  Empty = auto  |  Examples: 128000, 128k, 1m",
		providerTitle(provider, ts.cfg), model, autoWindow))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 2, 0, false)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("context-window", page, true)
	ts.app.SetFocus(form)
}

// showModelMappings renders the provider's current client-model ->
// upstream-model mappings, sorted by client model, with Add/Update and
// Remove entries. Selecting Add/Update opens a two-field form; selecting
// Remove opens a single-field form. Both write paths use a locked
// transaction (loadAppConfigLocked -> helper -> writeJSONAtomic) and refresh
// ts.cfg so the list re-renders from persisted state.
//
// The page is reachable even for NoModel providers so the operator can
// inspect and clear mappings the proxy might still consume.
func (ts *tuiState) showModelMappings(provider string) {
	list := tview.NewList()
	list.ShowSecondaryText(false)
	mappings := map[string]string{}
	for k, v := range ts.cfg.ModelMappings[provider] {
		mappings[k] = v
	}
	keys := make([]string, 0, len(mappings))
	for k := range mappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		list.AddItem(fmt.Sprintf("%s -> %s", k, mappings[k]), "", 0, nil)
	}
	if len(keys) == 0 {
		list.AddItem("(no mappings)", "", 0, nil)
	}
	list.AddItem("Add / Update Mapping", "", 'a', func() { ts.showModelMappingForm(provider) })
	list.AddItem("Remove Mapping", "", 'r', func() { ts.showRemoveModelMappingForm(provider) })
	list.AddItem("Back", "", 'b', func() { ts.showDetail(provider, "detail") })
	list.SetBorder(true)
	list.SetTitle(" Model Mappings ")
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Rune() == 'q' || event.Rune() == 'Q' {
			ts.showDetail(provider, "detail")
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s  |  Enter select   a add/update   r remove   b/esc/q back", providerTitle(provider, ts.cfg)))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(list, 0, 1, true)
	ts.pages.AddAndSwitchToPage("model-mappings", page, true)
	ts.app.SetFocus(list)
}

// showModelMappingForm is the Add/Update form for a single client-model ->
// upstream-model mapping. It mirrors `cs model-map set <provider> <client>
// <upstream>`.
func (ts *tuiState) showModelMappingForm(provider string) {
	clientValue := ""
	upstreamValue := ""
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	form.AddInputField("Client Model", "", 0, nil, func(text string) { clientValue = text })
	form.AddInputField("Upstream Model", "", 0, nil, func(text string) { upstreamValue = text })
	form.AddButton("Save", func() {
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		if err := setModelMappingForProvider(cfg, provider, clientValue, upstreamValue); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		if err := writeJSONAtomic(path, cfg); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		unlock()
		ts.cfg = cfg
		ts.showModelMappings(provider)
	})
	form.AddButton("Cancel", func() { ts.showModelMappings(provider) })
	form.SetBorder(true)
	form.SetTitle(" Add / Update Mapping ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showModelMappings(provider)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s", providerTitle(provider, ts.cfg)))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("model-mapping-form", page, true)
	ts.app.SetFocus(form)
}

// showRemoveModelMappingForm is the Remove form. It mirrors
// `cs model-map remove <provider> <client>`.
func (ts *tuiState) showRemoveModelMappingForm(provider string) {
	clientValue := ""
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	form.AddInputField("Client Model", "", 0, nil, func(text string) { clientValue = text })
	form.AddButton("Remove", func() {
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		if err := removeModelMappingForProvider(cfg, provider, clientValue); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		if err := writeJSONAtomic(path, cfg); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		unlock()
		ts.cfg = cfg
		ts.showModelMappings(provider)
	})
	form.AddButton("Cancel", func() { ts.showModelMappings(provider) })
	form.SetBorder(true)
	form.SetTitle(" Remove Mapping ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showModelMappings(provider)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s", providerTitle(provider, ts.cfg)))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("remove-mapping-form", page, true)
	ts.app.SetFocus(form)
}

// proxyManagerDefaultAgent resolves the agent the Proxy Manager page
// should default to. The manager carries an agent through the
// start/preview helpers so the operator can manage a claude route as
// well as a codex one, instead of the previous hard-coded "codex".
//
// Resolution order:
//  1. Iterate supportedProxyAgentList in order and return the first agent
//     that has a route in cfg.Proxy.Routes. This makes the default
//     deterministic when both agents are configured (codex wins because
//     it is the MVP default and supportedProxyAgentList[0]) while still
//     honouring a claude-only configuration.
//  2. Otherwise fall back to supportedProxyAgentList[0] (codex), the
//     proxy MVP default. Routes with an unknown/unsupported agent name
//     are skipped (treated as no route) so a corrupted or future-migrated
//     config cannot leak an unsupported agent through this helper.
func proxyManagerDefaultAgent(cfg *AppConfig) string {
	if cfg != nil && cfg.Proxy != nil {
		for _, agent := range supportedProxyAgentList {
			if route, ok := cfg.Proxy.Routes[agent]; ok && strings.EqualFold(route.Agent, agent) {
				return agent
			}
		}
	}
	return supportedProxyAgentList[0]
}

// proxyManagerStartArgs assembles the argv the Proxy Manager's "Start"
// action passes to cmdProxyStart so it targets the requested agent
// rather than the cmdProxyStart default of "codex".
func proxyManagerStartArgs(agent string) []string {
	return []string{"--agent", agent}
}

// proxyManagerPreviewArgs assembles the argv the Proxy Manager's "Agent
// Config Preview" action passes to cmdProxyPreview. cmdProxyPreview
// takes the agent as a single positional argument.
func proxyManagerPreviewArgs(agent string) []string {
	return []string{agent}
}

func proxyManagerRouteSummaries(cfg *AppConfig) []string {
	if cfg == nil || cfg.Proxy == nil || len(cfg.Proxy.Routes) == 0 {
		return []string{"(no routes configured)"}
	}
	agents := make([]string, 0, len(cfg.Proxy.Routes))
	for agent := range cfg.Proxy.Routes {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	summaries := make([]string, 0, len(agents))
	for _, agent := range agents {
		route := cfg.Proxy.Routes[agent]
		summaries = append(summaries, fmt.Sprintf("agent: %s  provider: %s  protocol: %s  token: %s", route.Agent, route.Provider, route.UpstreamProtocol, maskProxyToken(route.Token)))
	}
	return summaries
}

func proxyManagerRemoveRoute(cfg *AppConfig, agent string) (bool, string) {
	agent = strings.TrimSpace(agent)
	if cfg == nil || cfg.Proxy == nil || cfg.Proxy.Routes == nil {
		return false, "route not found"
	}
	if _, ok := cfg.Proxy.Routes[agent]; !ok {
		return false, "route not found"
	}
	delete(cfg.Proxy.Routes, agent)
	return true, "route removed; restart daemon to apply route changes"
}

// showProxyManager renders the proxy manager menu for the active provider.
// The menu aggregates the proxy lifecycle subcommands so the operator can
// configure a route, start/stop the proxy, check status, and preview the
// resolved route + agent config without leaving the TUI.
//
// The provider is carried through so the route configuration form can
// pre-fill the provider field with the provider the operator came from.
// The agent is carried through so start/preview target the operator's
// selected route (codex or claude) instead of the previous hard-coded
// "codex". The manager defaults to proxyManagerDefaultAgent(cfg) — i.e.
// an already-configured route, preferring codex when both exist — and
// the operator can switch the active agent from the menu via the
// "Switch to Agent: ..." entries, which re-open the same page with the
// new agent.
func (ts *tuiState) showProxyManager(provider string) {
	ts.showProxyManagerForAgent(provider, proxyManagerDefaultAgent(ts.cfg))
}

// showProxyManagerForAgent renders the proxy manager menu pinned to a
// specific agent. It is the body of showProxyManager and is also the
// target of the per-agent switch entries.
func (ts *tuiState) showProxyManagerForAgent(provider, agent string) {
	list := tview.NewList()
	list.ShowSecondaryText(false)
	// Surface the active agent at the top of the menu and let the
	// operator switch to any other supported agent. Without these
	// entries a claude-only route could never be started/previewed from
	// the TUI, because every action used to default to codex.
	list.AddItem(fmt.Sprintf("Active Agent: %s", agent), "", 'a', nil)
	for _, alt := range supportedProxyAgentList {
		if alt == agent {
			continue
		}
		// Capture alt in a local for the closure.
		alt := alt
		list.AddItem(fmt.Sprintf("Switch to Agent: %s", alt), "", 0, func() {
			ts.showProxyManagerForAgent(provider, alt)
		})
	}
	for _, summary := range proxyManagerRouteSummaries(ts.cfg) {
		list.AddItem(summary, "", 0, nil)
	}
	list.AddItem("Configure Route", "", 'c', func() { ts.showProxyRouteForm(provider, agent) })
	list.AddItem("Delete Route", "", 'd', func() { ts.showProxyRemoveRouteForm(provider, agent) })
	list.AddItem("Start Proxy", "", 's', func() { ts.showProxyActionResult(provider, "start", agent) })
	list.AddItem("Stop Proxy", "", 'x', func() { ts.showProxyActionResult(provider, "stop", agent) })
	list.AddItem("Status", "", 't', func() { ts.showProxyActionResult(provider, "status", agent) })
	list.AddItem("Agent Config Preview", "", 'p', func() { ts.showProxyPreview(provider, agent) })
	list.AddItem("Back", "", 'b', func() { ts.showDetail(provider, "detail") })
	list.SetBorder(true)
	list.SetTitle(" Proxy Manager ")
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Rune() == 'q' || event.Rune() == 'Q' {
			ts.showDetail(provider, "detail")
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s  Agent: %s  |  c configure   d delete   s start   x stop   t status   p preview   b/esc/q back", providerTitle(provider, ts.cfg), agent))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(list, 0, 1, true)
	ts.pages.AddAndSwitchToPage("proxy-manager", page, true)
	ts.app.SetFocus(list)
}

func (ts *tuiState) showProxyRemoveRouteForm(provider, agent string) {
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	form.AddButton("Delete", func() {
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		removed, msg := proxyManagerRemoveRoute(cfg, agent)
		if !removed {
			unlock()
			errLabel.SetText(msg)
			return
		}
		if err := writeJSONAtomic(path, cfg); err != nil {
			unlock()
			errLabel.SetText(err.Error())
			return
		}
		unlock()
		ts.cfg = cfg
		errLabel.SetText(msg)
		ts.showProxyManagerForAgent(provider, agent)
	})
	form.AddButton("Cancel", func() { ts.showProxyManagerForAgent(provider, agent) })
	form.SetBorder(true)
	form.SetTitle(" Delete Proxy Route ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showProxyManagerForAgent(provider, agent)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Delete route for agent %s. Restart daemon to apply route changes.", agent))
	page := tview.NewFlex().SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("proxy-remove-route", page, true)
	ts.app.SetFocus(form)
}

// proxyRouteFormSpec is the pure-data view of the route configuration form
// used by proxyRouteFormDefaults and proxyRouteFormSubmitArgs. It is the
// unit-testable surface of showProxyRouteForm: the form's defaults are
// resolved by proxyRouteFormDefaults, and the argv passed to
// cmdProxyConfigure is assembled by proxyRouteFormSubmitArgs.
type proxyRouteFormSpec struct {
	Agent           string
	AgentOptions    []string
	Provider        string
	Model           string
	Protocol        string
	ProtocolOptions []string
}

// proxyRouteFormDefaults resolves the default values for the route
// configuration form. The agent argument is the agent the Proxy Manager
// was pinned to when the operator opened the form; the form honours it
// so the operator does not lose context (DropDown selection, protocol
// default) when entering the form from a claude-targeted manager.
//
// Defaults:
//   - AgentOptions: the supportedProxyAgentList (codex, claude), in order.
//   - Agent: the supplied agent if it is one of the supported agents;
//     otherwise supportedProxyAgentList[0] (codex). An empty/typo/
//     future-migrated value falls back to codex so the form still renders
//     a valid DropDown rather than an empty one.
//   - Provider: the provider the operator entered the proxy manager from.
//   - Model: the provider's resolved preset/stored model. For NoModel
//     providers (e.g. kimi-coding) this is empty so the form leaves the
//     field blank and proxyRouteFormSubmitArgs drops --model from the argv.
//   - Protocol: defaultProxyProtocolForAgent(Agent), so the persisted
//     route is always valid by construction (matches cmdProxyConfigure).
//     Resolving the protocol against the honoured Agent (not always
//     codex) is what keeps a claude-form submission valid by
//     construction — claude rejects anthropic-messages.
//   - ProtocolOptions: the full set of supported ProviderProtocol values,
//     presented in the protocol DropDown so the operator can override the
//     default.
func proxyRouteFormDefaults(provider, agent string, cfg *AppConfig) proxyRouteFormSpec {
	honoured := supportedProxyAgentList[0]
	for _, a := range supportedProxyAgentList {
		if a == agent {
			honoured = agent
			break
		}
	}
	protocolOptions := []string{
		string(protocolAnthropicMessages),
		string(protocolOpenAIChat),
		string(protocolOpenAIResponses),
	}
	model := ""
	if cfg != nil {
		agentName := AgentName(honoured)
		switch agentName {
		case agentCodex:
			if stored := codexProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
				model = strings.TrimSpace(stored.Model)
			}
		case agentOpencode:
			if stored := opencodeProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
				model = strings.TrimSpace(stored.Model)
			}
		}
		if model == "" {
			if stored := cfg.Providers[provider]; strings.TrimSpace(stored.Model) != "" {
				model = strings.TrimSpace(stored.Model)
			}
		}
		if model == "" {
			if preset, err := resolveAgentProviderPreset(agentName, provider, cfg); err == nil && !preset.NoModel {
				model = strings.TrimSpace(preset.Model)
			} else if preset, err := resolveProviderPreset(provider, cfg); err == nil && !preset.NoModel {
				model = strings.TrimSpace(preset.Model)
			}
		}
	}
	return proxyRouteFormSpec{
		Agent:           honoured,
		AgentOptions:    append([]string(nil), supportedProxyAgentList...),
		Provider:        provider,
		Model:           model,
		Protocol:        string(defaultProxyProtocolForAgent(honoured)),
		ProtocolOptions: protocolOptions,
	}
}

// proxyRouteFormApplyAgent re-resolves the protocol default for a new
// agent selection in the route configuration form. It is the pure helper
// behind the Agent DropDown's selected callback: when the operator
// changes the agent, the form must (a) update spec.Agent, (b) re-resolve
// the protocol to defaultProxyProtocolForAgent(agent) so the persisted
// route stays valid by construction, and (c) report the index of the new
// protocol in spec.ProtocolOptions so the caller can drive the visible
// Protocol DropDown via SetCurrentOption.
//
// Design choice (sync-on-agent-change): every agent change resets the
// protocol to the agent-specific default rather than preserving the
// operator's previous manual choice. This keeps the persisted route
// valid by construction without forcing the operator to remember to
// also fix the protocol when switching agents. The alternative ("keep
// user choice, validate on save") was rejected because it lets the form
// display a protocol the persisted route would reject, surfacing the
// error only at save time — worse UX than just keeping them in sync.
//
// agentIdx is validated against the bounds of spec.AgentOptions. Out-of-
// range indices return the spec unchanged with a -1 protocolIdx, so the
// caller can safely ignore spurious callbacks (tview occasionally emits
// one with index -1 when the DropDown has no selection).
func proxyRouteFormApplyAgent(spec proxyRouteFormSpec, agentIdx int) (proxyRouteFormSpec, int) {
	if agentIdx < 0 || agentIdx >= len(spec.AgentOptions) {
		return spec, -1
	}
	spec.Agent = spec.AgentOptions[agentIdx]
	spec.Protocol = string(defaultProxyProtocolForAgent(spec.Agent))
	for i, p := range spec.ProtocolOptions {
		if p == spec.Protocol {
			return spec, i
		}
	}
	// The agent-specific default should always be present in
	// ProtocolOptions; if a future edit to the options list drops it,
	// surface that as -1 rather than panic.
	return spec, -1
}

// proxyRouteFormSubmitArgs assembles the cmdProxyConfigure argv for a given
// form spec. The argv shape matches what cmdProxyConfigure consumes
// directly (i.e. it is one level below cmdProxy):
//
//	[agent, "--provider", provider, "--model", model, "--protocol", protocol]
//
// It must NOT be prefixed with the "configure" subcommand token: the form
// calls cmdProxyConfigure (the configure subcommand handler) directly, not
// cmdProxy (the top-level proxy dispatcher). cmdProxyConfigure treats
// args[0] as the agent positional, so a leading "configure" would be
// parsed as the agent and the call would always fail with a usage error.
//
// The --model flag is OMITTED when the form's Model field is empty (NoModel
// presets) so cmdProxyConfigure does not reject the call on a whitespace
// model. The argv always includes --provider and --protocol so the
// persisted route is valid by construction.
func proxyRouteFormSubmitArgs(spec proxyRouteFormSpec) []string {
	args := []string{spec.Agent, "--provider", spec.Provider}
	if strings.TrimSpace(spec.Model) != "" {
		args = append(args, "--model", spec.Model)
	}
	args = append(args, "--protocol", spec.Protocol)
	return args
}

// proxyRouteFormSaveResult is the pure helper behind the route
// configuration form's Save button. It runs the configure+reload step
// that showProxyRouteForm used to inline, so the error-handling policy
// (never silently swallow a reload error) is unit-testable without
// driving a real tview form.
//
// Sequence:
//  1. Build the cmdProxyConfigure argv from spec via
//     proxyRouteFormSubmitArgs and capture stdout into a builder.
//  2. If cmdProxyConfigure returns an error, surface it as errMsg and
//     return (nil, errMsg, out). The form keeps its previous cfg and
//     stays open so the operator can correct the input.
//  3. Otherwise reload the persisted config via loadAppConfig. The
//     write already succeeded, so on-disk state is correct; a reload
//     failure here means the rest of the TUI would render against a
//     stale in-memory view, which is a confusing regression. Surface
//     the failure as an "route saved, but failed to reload config: ..."
//     message and return (nil, errMsg, out) so the form stays open and
//     the operator can decide to retry or back out.
//  4. On full success, return (reloaded, "", out). The form sets
//     ts.cfg = reloaded and navigates back to the proxy manager.
//
// The returned errMsg is "" only when everything succeeded. The
// returned cmdOutput is the captured stdout of cmdProxyConfigure
// regardless of outcome (it may be partial/empty on error).
func proxyRouteFormSaveResult(spec proxyRouteFormSpec) (reloaded *AppConfig, errMsg, cmdOutput string) {
	args := proxyRouteFormSubmitArgs(spec)
	var out strings.Builder
	if err := cmdProxyConfigure(args, &out); err != nil {
		return nil, err.Error(), out.String()
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return nil, "route saved, but failed to reload config: " + err.Error(), out.String()
	}
	return cfg, "", out.String()
}

// showProxyRouteForm renders the route configuration form. The form has
// three fields (Agent DropDown, Model InputField, Protocol DropDown) and on
// Save runs cmdProxyConfigure via proxyRouteFormSaveResult and surfaces
// either a success message or the error inline. Provider is pre-filled
// from the form spec and is intentionally NOT editable from this form —
// the form is always reached via the per-provider detail page.
//
// The agent argument is the agent the Proxy Manager was pinned to when
// the form was opened. It seeds the Agent DropDown (via
// proxyRouteFormDefaults) so the operator does not lose context when
// entering the form from a claude-targeted manager, and every return
// path (Save/Cancel/Esc) routes back to showProxyManagerForAgent so the
// manager stays pinned to that agent instead of resetting to the
// default. If the reload of ts.cfg after a successful write fails, the
// error is surfaced in errLabel and the form is NOT closed — the
// operator keeps the form context and the previously-loaded cfg (the
// write itself already succeeded, so the on-disk state is correct).
func (ts *tuiState) showProxyRouteForm(provider, agent string) {
	spec := proxyRouteFormDefaults(provider, agent, ts.cfg)
	agentIdx := 0
	protocolIdx := 0
	for i, a := range spec.AgentOptions {
		if a == spec.Agent {
			agentIdx = i
		}
	}
	for i, p := range spec.ProtocolOptions {
		if p == spec.Protocol {
			protocolIdx = i
		}
	}
	modelValue := spec.Model
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	// Build the Protocol DropDown BEFORE the Agent DropDown's selected
	// callback can fire. form.AddDropDown internally calls
	// SetCurrentOption(initialOption), which synchronously invokes the
	// selected callback (see tview.DropDown.SetCurrentOption docs:
	// "this function will also trigger the 'selected' callback"). If the
	// Agent callback dereferences protocolDropDown while it is still
	// nil, the form panics during construction. Declaring and populating
	// protocolDropDown first — and only then registering the Agent
	// callback that closes over it — guarantees the pointer is non-nil
	// when the callback fires, without changing the visible field order
	// (Agent, Model, Protocol) on the rendered form.
	protocolDropDown := tview.NewDropDown().
		SetLabel("Protocol").
		SetOptions(spec.ProtocolOptions, func(_ string, idx int) {
			if idx >= 0 && idx < len(spec.ProtocolOptions) {
				spec.Protocol = spec.ProtocolOptions[idx]
			}
		})
	protocolDropDown.SetCurrentOption(protocolIdx)
	form.AddDropDown("Agent", spec.AgentOptions, agentIdx, func(_ string, idx int) {
		// Re-resolve the protocol default when the agent changes so
		// the persisted route stays valid by construction. The helper
		// also returns the index of the new protocol in
		// ProtocolOptions, which we apply to the visible Protocol
		// DropDown via SetCurrentOption — without this the operator
		// would see one protocol while the form submitted another.
		var protoIdx int
		spec, protoIdx = proxyRouteFormApplyAgent(spec, idx)
		if protoIdx >= 0 {
			protocolDropDown.SetCurrentOption(protoIdx)
		}
	})
	form.AddInputField("Model", modelValue, 0, nil, func(text string) { modelValue = text })
	form.AddFormItem(protocolDropDown)
	form.AddButton("Save", func() {
		spec.Model = strings.TrimSpace(modelValue)
		reloaded, errMsg, _ := proxyRouteFormSaveResult(spec)
		if errMsg != "" {
			errLabel.SetText(errMsg)
			return
		}
		ts.cfg = reloaded
		ts.showProxyManagerForAgent(provider, spec.Agent)
	})
	form.AddButton("Cancel", func() { ts.showProxyManagerForAgent(provider, agent) })
	form.SetBorder(true)
	form.SetTitle(" Configure Proxy Route ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showProxyManagerForAgent(provider, agent)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s  Agent: %s  |  Writes a route for the agent using this provider.", providerTitle(provider, ts.cfg), agent))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("proxy-route-form", page, true)
	ts.app.SetFocus(form)
}

// proxyActionResultText runs the underlying cmd helper for a proxy lifecycle
// action into a strings.Builder and returns the captured output and error.
// It is the pure helper behind showProxyActionResult, surfaced for unit
// testing so the action dispatch can be exercised without driving the real
// tview.TextView.
//
// Supported actions:
//   - "start": threads agent through proxyManagerStartArgs so the proxy
//     targets the operator's selected route (codex or claude) instead of
//     the cmdProxyStart default of "codex".
//   - "stop": agent-agnostic; agent is ignored.
//   - "status": agent-agnostic; agent is ignored.
//
// Any other action returns an error so a future rename is surfaced loudly
// rather than silently doing nothing. An empty agent is acceptable for
// stop/status (the TUI passes the manager's active agent for symmetry but
// those commands do not consume it).
func proxyActionResultText(action, agent string) (string, error) {
	var out strings.Builder
	var err error
	switch action {
	case "start":
		err = cmdProxyStart(proxyManagerStartArgs(agent), &out)
	case "stop":
		err = cmdProxyStop(nil, &out)
	case "status":
		err = cmdProxyStatus(nil, &out)
	default:
		return "", fmt.Errorf("unknown proxy action %q (supported: start, stop, status)", action)
	}
	return out.String(), err
}

// showProxyActionResult runs the underlying cmd helper for a proxy lifecycle
// action and renders the captured output (or error) in a TextView with a
// Back action. The page is informational only — it never blocks on input
// beyond the Back action. agent is carried through for "start" so the
// proxy targets the operator's selected route.
func (ts *tuiState) showProxyActionResult(provider, action, agent string) {
	out, err := proxyActionResultText(action, agent)
	text := tview.NewTextView()
	text.SetDynamicColors(true)
	text.SetWrap(true)
	if err != nil {
		fmt.Fprintf(text, "[red]error: %s[-]\n", err.Error())
	}
	if out != "" {
		fmt.Fprintf(text, "%s", out)
	}
	if out == "" && err == nil {
		text.SetText("(no output)")
	}
	text.SetBorder(true)
	text.SetTitle(fmt.Sprintf(" Proxy %s ", action))
	text.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Rune() == 'q' || event.Rune() == 'Q' ||
			event.Key() == tcell.KeyEnter {
			ts.showProxyManagerForAgent(provider, agent)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText("Enter/esc/q back")
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(text, 0, 1, true)
	ts.pages.AddAndSwitchToPage("proxy-action-result", page, true)
	ts.app.SetFocus(text)
}

// proxyPreviewText runs cmdProxyPreview for the given agent and returns the
// captured output and error. It is the pure helper behind showProxyPreview,
// surfaced for unit testing so the preview dispatch can be exercised
// without driving the real tview.TextView.
func proxyPreviewText(agent string) (string, error) {
	var out strings.Builder
	err := cmdProxyPreview(proxyManagerPreviewArgs(agent), &out)
	return out.String(), err
}

// showProxyPreview renders the resolved proxy route and agent config
// fragment in a TextView with a Back action. The preview is read-only and
// never writes to disk; the provider API key is never printed (the
// underlying cmdProxyPreview uses a literal "<token>" placeholder). The
// agent is carried through so the operator can preview a claude route as
// well as a codex route — previously the preview was hard-wired to the
// first supported agent (codex).
func (ts *tuiState) showProxyPreview(provider, agent string) {
	out, err := proxyPreviewText(agent)
	text := tview.NewTextView()
	text.SetDynamicColors(true)
	text.SetWrap(true)
	if err != nil {
		fmt.Fprintf(text, "[red]error: %s[-]\n", err.Error())
	}
	if out != "" {
		fmt.Fprintf(text, "%s", out)
	}
	if out == "" && err == nil {
		text.SetText("(no output)")
	}
	text.SetBorder(true)
	text.SetTitle(" Proxy Preview ")
	text.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Rune() == 'q' || event.Rune() == 'Q' ||
			event.Key() == tcell.KeyEnter {
			ts.showProxyManagerForAgent(provider, agent)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText("Enter/esc/q back")
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(text, 0, 1, true)
	ts.pages.AddAndSwitchToPage("proxy-preview", page, true)
	ts.app.SetFocus(text)
}
