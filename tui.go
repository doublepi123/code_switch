package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/term"
)

func cmdConfigure(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
	resetKey := fs.Bool("reset-key", false, "force re-enter api key for the selected provider")
	dryRun := fs.Bool("dry-run", false, "preview what would be written without modifying settings.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	agentExplicit := flagWasProvided(fs, "agent")

	cfg, configPath, err := loadAppConfig()
	if err != nil {
		return err
	}

	var currentProvider, currentModel string
	switch agent {
	case agentCodex:
		_, cp, cm, _, _ := currentCodexProvider(*codexDir)
		currentProvider = codexTOMLProviderKey(cp)
		currentModel = cm
	case agentOpencode:
		currentProvider, currentModel = detectOpencodeCurrentProvider(cfg, *opencodeDir)
	default:
		currentProvider, currentModel = currentConfiguredProvider(cfg, *claudeDir)
	}
	reader := bufio.NewReader(in)
	var selection ConfigureSelection
	if file, ok := in.(*os.File); ok && shouldUseArrowTUI(file) {
		selection, err = runArrowTUI(cfg, agent, !agentExplicit, currentProvider, currentModel, *claudeDir, *codexDir, *opencodeDir)
		if err != nil {
			return err
		}
	} else {
		selection, err = promptConfigureSelectionFallback(reader, out, cfg, agent, currentProvider, currentModel)
		if err != nil {
			return err
		}
	}
	if selection.Agent == "" {
		selection.Agent = string(agent)
	}
	agent, err = parseAgentName(selection.Agent)
	if err != nil {
		return err
	}
	provider := selection.Provider
	if provider == restoreProviderOption {
		switch agent {
		case agentCodex:
			return restoreCodexConfig(*codexDir, cfg, out, *dryRun)
		case agentOpencode:
			return restoreOpencodeConfig(*opencodeDir, cfg, out, *dryRun)
		default:
			return restoreClaudeConfig(*claudeDir, out, *dryRun)
		}
	}
	if (agent == agentClaude || agent == agentOpencode) && strings.TrimSpace(selection.BaseURL) != "" {
		existingKey := strings.TrimSpace(cfg.Providers[selection.Provider].APIKey)
		keyToSave := strings.TrimSpace(selection.APIKey)
		if keyToSave == "" {
			keyToSave = existingKey
		}
		upsertProviderConfig(cfg, selection, keyToSave)
		mirrorOpencodeCustomProviderToShared(cfg, selection)
	}

	preset, err := resolveAgentProviderPreset(agent, provider, cfg)
	if err != nil {
		return err
	}

	existingKey := storedAPIKeyForAgent(cfg, agent, provider)
	apiKey := existingKey
	if preset.NoAPIKey {
		if apiKey == "" {
			apiKey = provider
		}
	} else if selection.APIKey != "" {
		apiKey = selection.APIKey
	} else if apiKey == "" || *resetKey || selection.ResetKey {
		apiKey, err = promptAPIKey(in, reader, out, provider)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "using saved api key for %s\n", provider)
	}
	if *dryRun {
		preset, err := resolveAgentSwitchPreset(agent, provider, cfg, selection.Model)
		if err != nil {
			return err
		}
		if selection.Launch {
			fmt.Fprintf(out, "[dry-run] would launch %s with %s temporarily\n", agentDisplayName(agent), preset.Name)
		} else {
			fmt.Fprintf(out, "[dry-run] would save provider config for %s in %s\n", provider, configPath)
			fmt.Fprintf(out, "[dry-run] would switch %s to %s\n", agentDisplayName(agent), preset.Name)
		}
		fmt.Fprintf(out, "[dry-run] base_url: %s\n", preset.BaseURL)
		fmt.Fprintf(out, "[dry-run] model: %s\n", preset.Model)
		return nil
	}
	if selection.Launch {
		return launchAgentWithConfig(agent, provider, selection.Model, apiKey, cfg, configPath, out)
	}

	cf := newConfigFile(configPath)
	unlock, lockErr := cf.lock()
	if lockErr != nil {
		return lockErr
	}

	cfg, err = loadAppConfigFrom(configPath)
	if err != nil {
		unlock()
		return err
	}

	if agent == agentClaude && strings.TrimSpace(selection.BaseURL) != "" {
		existingKey := strings.TrimSpace(cfg.Providers[selection.Provider].APIKey)
		keyToSave := strings.TrimSpace(selection.APIKey)
		if keyToSave == "" {
			keyToSave = existingKey
		}
		upsertProviderConfig(cfg, selection, keyToSave)
	}
	upsertProviderConfig(cfg, selection, apiKey)
	mirrorOpencodeCustomProviderToShared(cfg, selection)

	if err := writeJSONAtomic(configPath, cfg); err != nil {
		unlock()
		return err
	}
	unlock()
	fmt.Fprintf(out, "saved provider config for %s in %s\n", provider, configPath)

	if msg, changed, err := configureProxyRouteForCrossProtocolSelection(cfg, selection); err != nil {
		return err
	} else if changed {
		if err := writeJSONAtomic(configPath, cfg); err != nil {
			return err
		}
		fmt.Fprintln(out, msg)
		pa := &providerArgs{Agent: agent, Provider: provider, APIKey: apiKey, Model: selection.Model, Haiku: selection.Haiku, Sonnet: selection.Sonnet, Opus: selection.Opus, Subagent: selection.Subagent}
		persist := func() error { return writeJSONAtomic(configPath, cfg) }
		plan, err := resolveConnection(agent, provider, mustResolveAgentSwitchPreset(agent, provider, cfg, selection.Model), "proxy")
		if err != nil {
			return err
		}
		return switchProxyProvider(pa, cfg, persist, plan, *claudeDir, *codexDir, *opencodeDir, out, false)
	}

	switch agent {
	case agentCodex:
		if err := switchCodexProvider(provider, cfg, apiKey, selection.Model, *codexDir, out, false); err != nil {
			return err
		}
	case agentOpencode:
		if err := switchOpencodeProvider(provider, cfg, apiKey, selection.Model, *opencodeDir, out, false); err != nil {
			return err
		}
	default:
		if err := switchProvider(provider, cfg, apiKey, selection.Model, *claudeDir, out, false); err != nil {
			return err
		}
	}
	return nil
}

func mustResolveAgentSwitchPreset(agent AgentName, provider string, cfg *AppConfig, model string) ProviderPreset {
	preset, _ := resolveAgentSwitchPreset(agent, provider, cfg, model)
	return preset
}

func configureProxyRouteForCrossProtocolSelection(cfg *AppConfig, selection ConfigureSelection) (string, bool, error) {
	agent, err := parseAgentName(selection.Agent)
	if err != nil {
		return "", false, err
	}
	provider := canonicalProviderName(selection.Provider)
	preset, err := resolveAgentSwitchPreset(agent, provider, cfg, selection.Model)
	if err != nil {
		return "", false, err
	}
	plan, err := resolveConnection(agent, provider, preset, "auto")
	if err != nil {
		return "", false, err
	}
	if plan.Mode != connectionModeProxy {
		return "", false, nil
	}
	model := strings.TrimSpace(selection.Model)
	if model == "" {
		model = strings.TrimSpace(preset.Model)
	}
	if err := writeProxyRouteConfig(cfg, plan, model, ""); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("cross-protocol selection will use local proxy route: %s -> %s (%s)", plan.ClientProtocol, plan.UpstreamProtocol, provider), true, nil
}

type tuiState struct {
	app             *tview.Application
	pages           *tview.Pages
	cfg             *AppConfig
	agent           AgentName
	selectAgent     bool
	currentProvider string
	currentModel    string
	claudeDir       string
	codexDir        string
	opencodeDir     string
	names           []string
	displayNames    []string

	selectedProvider string
	selectedModel    map[string]string
	advancedReturn   map[string]bool
	typedAPIKeys     map[string]string
	resetKeys        map[string]bool
	customModels     map[string]string
	tierOverrides    map[string]StoredProvider

	result    ConfigureSelection
	resultErr error

	providerList *tview.List
	providerPage *tview.Flex
	detailText   *tview.TextView
	tierInfo     *tview.TextView
}

func (ts *tuiState) buildModels(provider string) []string {
	if ts.agent == agentCodex {
		return buildModelListForAgentWithAPIKey(ts.cfg, ts.agent, provider, ts.customModels, ts.typedAPIKeys[provider])
	}
	return buildModelList(ts.cfg, provider, ts.customModels)
}

func (ts *tuiState) finishSelection(provider, model string) {
	ov, editedTiers := ts.tierOverrides[provider]
	if !editedTiers && ts.cfg != nil {
		// User never opened/saved Edit Tiers this session. Fall back to the
		// stored values so previously persisted tier overrides (from
		// `cs switch --haiku ...` or a prior Edit Tiers session) are preserved
		// instead of being overwritten with empty strings by upsertProviderConfig.
		if ts.agent == agentOpencode {
			ov = opencodeProviderConfig(ts.cfg, provider)
		} else {
			ov = ts.cfg.Providers[provider]
		}
	}
	authEnv := ""
	if ts.cfg != nil && ts.agent == agentClaude {
		authEnv = ts.cfg.Providers[provider].AuthEnv
	}
	ts.result = ConfigureSelection{
		Agent:    string(ts.agent),
		Provider: provider,
		Model:    model,
		ResetKey: ts.resetKeys[provider],
		APIKey:   strings.TrimSpace(ts.typedAPIKeys[provider]),
		AuthEnv:  authEnv,
		Haiku:    ov.Haiku,
		Sonnet:   ov.Sonnet,
		Opus:     ov.Opus,
		Subagent: ov.Subagent,
	}
	ts.resultErr = nil
	ts.app.Stop()
}

func (ts *tuiState) finishLaunch(provider, model string) {
	ts.finishSelection(provider, model)
	ts.result.Launch = true
}

func (ts *tuiState) showProviders() {
	ts.names = providerNamesForAgent(ts.agent, ts.cfg, true, true)
	ts.rebuildProviderList()
	ts.pages.SwitchToPage("providers")
	ts.app.SetFocus(ts.providerList)
}

func (ts *tuiState) selectedModelForProvider(provider string) string {
	if ts.selectedModel != nil {
		if model := strings.TrimSpace(ts.selectedModel[provider]); model != "" {
			return model
		}
	}
	return defaultSelectionModelForAgent(ts.cfg, ts.agent, provider, ts.currentProvider, ts.currentModel)
}

func (ts *tuiState) keyStatusForProvider(provider string, preset ProviderPreset) string {
	if preset.NoAPIKey {
		return "not required"
	}
	if strings.TrimSpace(ts.typedAPIKeys[provider]) != "" {
		return "session"
	}
	if storedAPIKeyForAgent(ts.cfg, ts.agent, provider) != "" {
		return "saved"
	}
	return "missing"
}

func (ts *tuiState) showProviderWorkspace(provider string) {
	ts.selectedProvider = provider
	if ts.advancedReturn != nil {
		delete(ts.advancedReturn, provider)
	}
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}
	model := ts.selectedModelForProvider(provider)

	var b strings.Builder
	fmt.Fprintf(&b, "Provider: %s                 Agent: %s\n", providerTitle(provider, ts.cfg), ts.agent)
	if plan, err := resolveConnection(ts.agent, provider, preset, "auto"); err == nil {
		fmt.Fprintf(&b, "Connection: %s/%s        Key: %s\n", plan.Mode, plan.UpstreamProtocol, ts.keyStatusForProvider(provider, preset))
	} else {
		fmt.Fprintf(&b, "Connection: unavailable        Key: %s\n", ts.keyStatusForProvider(provider, preset))
	}
	fmt.Fprintf(&b, "Selected model: %s\n", model)

	info := tview.NewTextView()
	info.SetDynamicColors(true)
	info.SetWrap(true)
	info.SetBorder(true)
	info.SetTitle(" Provider Workspace ")
	info.SetText(b.String())

	actions := tview.NewList()
	actions.ShowSecondaryText(false)
	actions.SetBorder(true)
	actions.SetTitle(" Actions ")
	actions.AddItem(actionLabelLaunch, "", 'l', func() { ts.launchSelectedProvider(provider) })
	actions.AddItem(actionLabelSetDefault, "", 's', func() { ts.saveSelectedProvider(provider) })
	if !preset.NoModel {
		actions.AddItem(actionLabelModels, "", 'm', func() { ts.showModels(provider, "provider-workspace") })
	}
	if !preset.NoAPIKey {
		actions.AddItem(actionLabelEditAPIKey, "", 'k', func() {
			ts.showKeyForm(provider, "provider-workspace", func() { ts.showProviderWorkspace(provider) })
		})
	}
	actions.AddItem(actionLabelAdvanced, "", 'a', func() { ts.showAdvancedMenu(provider) })
	actions.AddItem(actionLabelBack, "", 'b', ts.showProviders)
	actions.SetCurrentItem(0)
	actions.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
			ts.showProviders()
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q' || event.Rune() == 'b' || event.Rune() == 'B':
			ts.showProviders()
			return nil
		case event.Rune() == 'l' || event.Rune() == 'L':
			ts.launchSelectedProvider(provider)
			return nil
		case event.Rune() == 's' || event.Rune() == 'S':
			ts.saveSelectedProvider(provider)
			return nil
		case !preset.NoModel && (event.Rune() == 'm' || event.Rune() == 'M'):
			ts.showModels(provider, "provider-workspace")
			return nil
		case !preset.NoAPIKey && (event.Rune() == 'k' || event.Rune() == 'K'):
			ts.showKeyForm(provider, "provider-workspace", func() { ts.showProviderWorkspace(provider) })
			return nil
		case event.Rune() == 'a' || event.Rune() == 'A':
			ts.showAdvancedMenu(provider)
			return nil
		}
		return event
	})

	page := tview.NewFlex().SetDirection(tview.FlexRow)
	page.AddItem(info, 0, 1, false)
	page.AddItem(actions, 9, 0, true)
	ts.pages.AddAndSwitchToPage("provider-workspace", page, true)
	ts.app.SetFocus(actions)
}

func (ts *tuiState) launchSelectedProvider(provider string) {
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}
	if !preset.NoAPIKey && !hasConfigurableKey(storedAPIKeyForAgent(ts.cfg, ts.agent, provider), ts.typedAPIKeys[provider], ts.resetKeys[provider]) {
		ts.showKeyFormWithCancel(provider, "provider-workspace", func() { ts.showProviderWorkspace(provider) }, func() { ts.showProviderWorkspace(provider) })
		return
	}
	ts.finishLaunch(provider, ts.selectedModelForProvider(provider))
}

func (ts *tuiState) saveSelectedProvider(provider string) {
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}
	if !preset.NoAPIKey && !hasConfigurableKey(storedAPIKeyForAgent(ts.cfg, ts.agent, provider), ts.typedAPIKeys[provider], ts.resetKeys[provider]) {
		ts.showKeyFormWithCancel(provider, "provider-workspace", func() { ts.showProviderWorkspace(provider) }, func() { ts.showProviderWorkspace(provider) })
		return
	}
	ts.finishSelection(provider, ts.selectedModelForProvider(provider))
}

func (ts *tuiState) showAdvancedMenu(provider string) {
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}
	actions := tview.NewList()
	actions.ShowSecondaryText(false)
	actions.SetBorder(true)
	actions.SetTitle(" Advanced ")
	if ts.agent != agentOpencode && !preset.NoModel {
		actions.AddItem(actionLabelEditTiers, "", 't', func() { ts.showTierConfig(provider, "provider-workspace") })
	}
	actions.AddItem(actionLabelManageMappings, "", 'g', func() {
		ts.markAdvancedReturn(provider)
		ts.showModelMappings(provider)
	})
	if ts.agent == agentCodex && !preset.NoModel {
		actions.AddItem(actionLabelEditContextWindow, "", 'c', func() {
			ts.markAdvancedReturn(provider)
			ts.showContextWindowForm(provider)
		})
	}
	actions.AddItem(actionLabelProxyManager, "", 'p', func() {
		ts.markAdvancedReturn(provider)
		ts.showProxyManager(provider)
	})
	actions.AddItem("Provider Details", "", 'd', func() {
		if ts.advancedReturn != nil {
			delete(ts.advancedReturn, provider)
		}
		ts.showDetail(provider, "provider-workspace")
	})
	actions.AddItem(actionLabelBack, "", 'b', func() { ts.showProviderWorkspace(provider) })
	actions.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape || event.Rune() == 'q' || event.Rune() == 'Q' || event.Rune() == 'b' || event.Rune() == 'B' {
			ts.showProviderWorkspace(provider)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s", providerTitle(provider, ts.cfg)))
	page := tview.NewFlex().SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(actions, 0, 1, true)
	ts.pages.AddAndSwitchToPage("advanced", page, true)
	ts.app.SetFocus(actions)
}

func (ts *tuiState) markAdvancedReturn(provider string) {
	if ts.advancedReturn == nil {
		ts.advancedReturn = map[string]bool{}
	}
	ts.advancedReturn[provider] = true
}

func (ts *tuiState) returnToAdvancedOrDetail(provider string) {
	if ts.advancedReturn != nil && ts.advancedReturn[provider] {
		ts.showAdvancedMenu(provider)
		return
	}
	ts.showDetail(provider, "detail")
}

func (ts *tuiState) rebuildProviderList() {
	ts.providerList.Clear()
	ts.displayNames = nil
	filteredNames := make([]string, 0, len(ts.names))
	selectedIndex := 0
	for _, name := range ts.names {
		isSelected := name == ts.selectedProvider
		if name == customProviderOption {
			if isSelected {
				selectedIndex = len(filteredNames)
			}
			filteredNames = append(filteredNames, name)
			ts.providerList.AddItem("custom...", "Add a custom provider", 0, nil)
			ts.displayNames = append(ts.displayNames, name)
			continue
		}
		if name == restoreProviderOption {
			if isSelected {
				selectedIndex = len(filteredNames)
			}
			filteredNames = append(filteredNames, name)
			ts.providerList.AddItem("Restore official config...", agentDisplayName(ts.agent), 0, nil)
			ts.displayNames = append(ts.displayNames, name)
			continue
		}
		preset, err := resolveAgentProviderPreset(ts.agent, name, ts.cfg)
		if err != nil {
			continue
		}
		if isSelected {
			selectedIndex = len(filteredNames)
		}
		filteredNames = append(filteredNames, name)
		title, secondary := providerListItemText(ts.agent, ts.cfg, name, preset, ts.currentProvider, storedAPIKeyForAgent(ts.cfg, ts.agent, name))
		suffix := []string{}
		if name == ts.currentProvider {
			suffix = append(suffix, "current")
		}
		if preset.NoAPIKey {
			suffix = append(suffix, "no key needed")
		} else if storedAPIKeyForAgent(ts.cfg, ts.agent, name) != "" {
			suffix = append(suffix, "saved")
		}
		if len(suffix) > 0 {
			title += " [" + strings.Join(suffix, ", ") + "]"
		}
		ts.providerList.AddItem(title, secondary, 0, nil)
		ts.displayNames = append(ts.displayNames, name)
	}
	ts.names = filteredNames
	ts.providerList.SetCurrentItem(selectedIndex)
}

func providerListItemText(agent AgentName, cfg *AppConfig, name string, preset ProviderPreset, currentProvider, savedKey string) (string, string) {
	title := providerTitle(name, cfg)
	badges := protocolBadges(providerProtocols(preset))
	if badges != "" {
		title += " " + badges
	}
	if plan, err := resolveConnection(agent, name, preset, "auto"); err == nil {
		title += " [" + string(plan.Mode) + "]"
	}
	return title, preset.BaseURL
}

func providerProtocols(preset ProviderPreset) []ProviderProtocol {
	seen := map[ProviderProtocol]bool{}
	protocols := []ProviderProtocol{}
	add := func(protocol ProviderProtocol) {
		if protocol == "" || seen[protocol] {
			return
		}
		seen[protocol] = true
		protocols = append(protocols, protocol)
	}
	if strings.TrimSpace(preset.BaseURL) != "" {
		add(protocolAnthropicMessages)
	}
	for _, protocol := range []ProviderProtocol{protocolAnthropicMessages, protocolOpenAIChat, protocolOpenAIResponses} {
		if endpoint, ok := preset.Endpoints[protocol]; ok && strings.TrimSpace(endpoint.BaseURL) != "" {
			add(protocol)
		}
	}
	return protocols
}

func protocolBadges(protocols []ProviderProtocol) string {
	badges := []string{}
	for _, protocol := range protocols {
		switch protocol {
		case protocolAnthropicMessages:
			badges = append(badges, "[A]")
		case protocolOpenAIChat:
			badges = append(badges, "[C]")
		case protocolOpenAIResponses:
			badges = append(badges, "[R]")
		}
	}
	return strings.Join(badges, "")
}

func (ts *tuiState) showDetail(provider, backPage string) {
	ts.selectedProvider = provider
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}

	hasSavedKey := storedAPIKeyForAgent(ts.cfg, ts.agent, provider) != ""
	var b strings.Builder
	b.WriteString(providerDetailInfoText(ts.agent, ts.cfg, provider, preset, ts.currentProvider, ts.currentModel, preset.NoAPIKey, hasSavedKey))
	if preset.Website != "" {
		fmt.Fprintf(&b, "[::b]Website[::-]     %s\n", preset.Website)
	}
	if preset.APIKeyURL != "" {
		fmt.Fprintf(&b, "[::b]Get Key[::-]     %s\n", preset.APIKeyURL)
	}
	if preset.NoModel {
		fmt.Fprintf(&b, "[::b]Model[::-]    [yellow]auto — provider routes by thinking mode[-]\n")
	}
	if !preset.NoAPIKey {
		if ts.resetKeys[provider] {
			fmt.Fprintf(&b, "[yellow]Pending key update on apply[-]\n")
		} else if !hasSavedKey {
			fmt.Fprintf(&b, "[yellow]No saved key yet[-]\n")
		}
	}
	ts.detailText.SetText(b.String())

	actions := tview.NewList()
	actions.ShowSecondaryText(false)
	actions.SetBorder(true)
	actions.SetTitle(" Actions ")
	// Action labels come from the named constants shared with
	// providerDetailActionLabels so the spelling/wording cannot drift
	// between the superset advertised by the helper and what showDetail
	// actually renders. The visibility rules (NoModel, NoAPIKey,
	// canSwitch, agent != opencode) refine the superset at render time.
	if !preset.NoModel {
		actions.AddItem(actionLabelChooseModel, "", 'm', func() {
			ts.showModels(provider, "detail")
		})
		actions.AddItem(actionLabelUseModel, "", 'u', func() {
			ts.showUseModelForm(provider)
		})
	}
	// Manage Model Mappings and Proxy Manager are reachable for every
	// provider (including NoModel presets) so the operator can inspect and
	// clear mappings and configure proxy routes regardless of preset flags.
	actions.AddItem(actionLabelManageMappings, "", 'g', func() {
		ts.showModelMappings(provider)
	})
	actions.AddItem(actionLabelProxyManager, "", 'p', func() {
		ts.showProxyManager(provider)
	})
	canSwitch := preset.NoAPIKey || hasConfigurableKey(storedAPIKeyForAgent(ts.cfg, ts.agent, provider), ts.typedAPIKeys[provider], ts.resetKeys[provider])
	if canSwitch {
		actions.AddItem(actionLabelLaunch, "", 'l', func() {
			ts.finishLaunch(provider, preset.Model)
		})
		actions.AddItem(actionLabelSetDefault, "", 's', func() {
			ts.finishSelection(provider, preset.Model)
		})
	}
	if !preset.NoAPIKey {
		actions.AddItem(actionLabelEditAPIKey, "", 'k', func() {
			ts.showKeyForm(provider, backPage, func() {
				ts.showDetail(provider, backPage)
			})
		})
	}
	if ts.agent == agentCodex && !preset.NoModel {
		actions.AddItem(actionLabelEditContextWindow, "", 'c', func() {
			ts.showContextWindowForm(provider)
		})
	}
	if ts.agent != agentOpencode && !preset.NoModel {
		actions.AddItem(actionLabelEditTiers, "", 't', func() {
			ts.showTierConfig(provider, backPage)
		})
	}
	actions.AddItem(actionLabelBack, "", 'b', ts.showProviders)
	actions.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
			ts.showProviders()
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			ts.showProviders()
			return nil
		case !preset.NoAPIKey && (event.Rune() == 'k' || event.Rune() == 'K'):
			ts.showKeyForm(provider, backPage, func() {
				ts.showDetail(provider, backPage)
			})
			return nil
		case canSwitch && (event.Rune() == 'l' || event.Rune() == 'L'):
			ts.finishLaunch(provider, preset.Model)
			return nil
		case canSwitch && (event.Rune() == 's' || event.Rune() == 'S'):
			ts.finishSelection(provider, preset.Model)
			return nil
		case ts.agent == agentCodex && !preset.NoModel && (event.Rune() == 'c' || event.Rune() == 'C'):
			ts.showContextWindowForm(provider)
			return nil
		case ts.agent != agentOpencode && !preset.NoModel && (event.Rune() == 't' || event.Rune() == 'T'):
			ts.showTierConfig(provider, backPage)
			return nil
		case !preset.NoModel && (event.Rune() == 'u' || event.Rune() == 'U'):
			ts.showUseModelForm(provider)
			return nil
		case event.Rune() == 'g' || event.Rune() == 'G':
			ts.showModelMappings(provider)
			return nil
		}
		return event
	})

	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(ts.detailText, 0, 1, false)
	page.AddItem(actions, 12, 0, true)
	ts.pages.AddAndSwitchToPage("detail", page, true)
	ts.app.SetFocus(actions)
}

func providerDetailInfoText(agent AgentName, cfg *AppConfig, provider string, preset ProviderPreset, currentProvider, currentModel string, noAPIKey, hasSavedKey bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]Provider[::-]  %s\n", providerTitle(provider, cfg))
	fmt.Fprintf(&b, "[::b]Preset[::-]    %s\n", preset.Name)
	fmt.Fprintf(&b, "[::b]Base URL[::-]  %s\n", preset.BaseURL)
	if noAPIKey {
		fmt.Fprintf(&b, "[::b]API Key[::-]   [green]Not required[-]\n")
	} else {
		fmt.Fprintf(&b, "[::b]Saved Key[::-] %s\n", maskAPIKey(storedAPIKeyForAgent(cfg, agent, provider)))
	}
	fmt.Fprintf(&b, "[::b]Active[::-]    %s / %s\n", currentProviderLabel(currentProvider), currentModelLabel(currentModel))
	fmt.Fprintf(&b, "[::b]Protocol Endpoints[::-]\n")
	for _, protocol := range providerProtocols(preset) {
		if endpoint, ok := preset.presetEndpoint(protocol); ok {
			fmt.Fprintf(&b, "  %s  %s\n", protocol, endpoint.BaseURL)
		}
	}
	if plan, err := resolveConnection(agent, provider, preset, "auto"); err == nil {
		fmt.Fprintf(&b, "[::b]Connection[::-] %s (%s -> %s)\n", plan.Mode, plan.ClientProtocol, plan.UpstreamProtocol)
		if plan.Mode == connectionModeProxy {
			fmt.Fprintf(&b, "[yellow]Cross-protocol selection will be routed through a local proxy route.[-]\n")
		}
	}
	if agent == agentCodex {
		model := defaultSelectionModelForAgent(cfg, agent, provider, currentProvider, currentModel)
		stored := codexProviderConfig(cfg, provider)
		window := resolveModelContextWindow(model, stored.ContextWindow)
		if stored.ContextWindow > 0 {
			fmt.Fprintf(&b, "[::b]Context[::-]   %d tokens (custom)\n", window)
		} else {
			fmt.Fprintf(&b, "[::b]Context[::-]   %d tokens (auto)\n", window)
		}
	}
	return b.String()
}

func (ts *tuiState) updateTierInfo(provider, model string) {
	if ts.tierInfo == nil {
		return
	}
	if model == "" {
		ts.tierInfo.SetText("")
		return
	}
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.tierInfo.SetText("")
		return
	}
	preset = withSelectedModel(preset, model)
	if preset.ForceModelTiers {
		ts.tierInfo.SetText(fmt.Sprintf("all tiers: %s", preset.Model))
	} else {
		ts.tierInfo.SetText(fmt.Sprintf("haiku: %s | sonnet: %s | opus: %s | sub: %s",
			preset.Haiku, preset.Sonnet, preset.Opus, preset.Subagent))
	}
}

func (ts *tuiState) showTierConfig(provider, backPage string) {
	ts.selectedProvider = provider
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}

	models := ts.buildModels(provider)
	modelOptions := append([]string{""}, models...)

	override := ts.tierOverrides[provider]
	stored := ts.cfg.Providers[provider]

	haikuDefault := firstNonEmpty(strings.TrimSpace(override.Haiku), strings.TrimSpace(stored.Haiku), preset.Haiku)
	sonnetDefault := firstNonEmpty(strings.TrimSpace(override.Sonnet), strings.TrimSpace(stored.Sonnet), preset.Sonnet)
	opusDefault := firstNonEmpty(strings.TrimSpace(override.Opus), strings.TrimSpace(stored.Opus), preset.Opus)
	subagentDefault := firstNonEmpty(strings.TrimSpace(override.Subagent), strings.TrimSpace(stored.Subagent), preset.Subagent)

	var haikuVal, sonnetVal, opusVal, subagentVal string

	modelIndexFor := func(val string) int {
		for i, m := range modelOptions {
			if m == val {
				return i
			}
		}
		return 0
	}

	const labelWidth = 10
	createDD := func(label, defaultVal string, val *string) *tview.DropDown {
		dd := tview.NewDropDown().
			SetLabel(label).
			SetLabelWidth(labelWidth).
			SetOptions(modelOptions, func(text string, idx int) {
				if idx == 0 {
					*val = ""
				} else {
					*val = text
				}
			})
		dd.SetCurrentOption(modelIndexFor(defaultVal))
		return dd
	}

	haikuDD := createDD("Haiku: ", haikuDefault, &haikuVal)
	sonnetDD := createDD("Sonnet: ", sonnetDefault, &sonnetVal)
	opusDD := createDD("Opus: ", opusDefault, &opusVal)
	subagentDD := createDD("Subagent: ", subagentDefault, &subagentVal)

	saveBtn := tview.NewButton("  Save  ").SetSelectedFunc(func() {
		ov := ts.tierOverrides[provider]
		ov.Haiku = haikuVal
		ov.Sonnet = sonnetVal
		ov.Opus = opusVal
		ov.Subagent = subagentVal
		ts.tierOverrides[provider] = ov
		if backPage == "provider-workspace" {
			ts.showAdvancedMenu(provider)
		} else {
			ts.showDetail(provider, backPage)
		}
	})
	cancelBtn := tview.NewButton(" Cancel ").SetSelectedFunc(func() {
		if backPage == "provider-workspace" {
			ts.showAdvancedMenu(provider)
		} else {
			ts.showDetail(provider, backPage)
		}
	})

	btnRow := tview.NewFlex().SetDirection(tview.FlexColumn)
	btnRow.AddItem(saveBtn, 10, 0, false)
	btnRow.AddItem(nil, 1, 0, false)
	btnRow.AddItem(cancelBtn, 10, 0, false)

	inner := tview.NewFlex().SetDirection(tview.FlexRow)
	inner.AddItem(haikuDD, 1, 0, true)
	inner.AddItem(sonnetDD, 1, 0, false)
	inner.AddItem(opusDD, 1, 0, false)
	inner.AddItem(subagentDD, 1, 0, false)
	inner.AddItem(nil, 1, 0, false)
	inner.AddItem(btnRow, 1, 0, false)
	inner.SetBorder(true)
	inner.SetTitle(" Edit Tier Models ")

	items := []tview.Primitive{haikuDD, sonnetDD, opusDD, subagentDD, saveBtn, cancelBtn}
	navigate := func(from tview.Primitive, delta int) {
		for i, item := range items {
			if item == from {
				ts.app.SetFocus(items[(i+delta+len(items))%len(items)])
				return
			}
		}
	}
	goBack := func() {
		if backPage == "provider-workspace" {
			ts.showAdvancedMenu(provider)
		} else {
			ts.showDetail(provider, backPage)
		}
	}

	for _, dd := range []*tview.DropDown{haikuDD, sonnetDD, opusDD, subagentDD} {
		dd := dd
		dd.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if dd.IsOpen() {
				return event
			}
			switch event.Key() {
			case tcell.KeyUp:
				navigate(dd, -1)
				return nil
			case tcell.KeyDown:
				navigate(dd, 1)
				return nil
			case tcell.KeyEscape:
				goBack()
				return nil
			}
			return event
		})
	}

	for _, btn := range []*tview.Button{saveBtn, cancelBtn} {
		btn := btn
		btn.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			switch event.Key() {
			case tcell.KeyUp:
				navigate(btn, -1)
				return nil
			case tcell.KeyDown:
				navigate(btn, 1)
				return nil
			case tcell.KeyEscape:
				goBack()
				return nil
			}
			return event
		})
	}

	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s  |  ↑↓ navigate  Enter select  |  Leave empty for preset default  |  Esc cancel", providerTitle(provider, ts.cfg)))

	page := tview.NewFlex().SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(inner, 0, 1, true)
	ts.pages.AddAndSwitchToPage("tier-config", page, true)
	ts.app.SetFocus(haikuDD)
}

func (ts *tuiState) showModels(provider, backPage string) {
	ts.selectedProvider = provider
	allModels := ts.buildModels(provider)
	defaultModel := defaultSelectionModelForAgent(ts.cfg, ts.agent, provider, ts.currentProvider, ts.currentModel)

	modelList := tview.NewList()
	modelList.ShowSecondaryText(false)
	modelList.SetBorder(true)
	modelList.SetTitle(" Models ")

	searchInput := tview.NewInputField()
	searchInput.SetLabel("Filter: ")
	searchInput.SetPlaceholder("type to filter models...")
	searchInput.SetFieldBackgroundColor(tcell.ColorDefault)

	populateModels := func(filter string) {
		modelList.Clear()
		filter = strings.ToLower(strings.TrimSpace(filter))
		for _, model := range allModels {
			if filter != "" && !strings.Contains(strings.ToLower(model), filter) {
				continue
			}
			label := model
			if model == defaultModel {
				label += " [default]"
			}
			modelName := model
			modelList.AddItem(label, "", 0, func() {
				if backPage == "provider-workspace" {
					if ts.selectedModel == nil {
						ts.selectedModel = map[string]string{}
					}
					ts.selectedModel[provider] = modelName
					ts.showProviderWorkspace(provider)
					return
				}
				ts.selectModelForAction(provider, backPage, modelName)
			})
		}
		modelList.AddItem("Custom model...", "", 0, func() {
			ts.showCustomModelForm(provider, backPage)
		})
		selectedIndex := modelIndexForAgent(ts.cfg, ts.agent, provider, ts.currentProvider, ts.currentModel)
		if filter == "" {
			if customModel := strings.TrimSpace(ts.customModels[provider]); customModel != "" {
				selectedIndex = 0
			}
			if selectedIndex >= 0 && selectedIndex < modelList.GetItemCount() {
				modelList.SetCurrentItem(selectedIndex)
			}
		}
	}

	searchInput.SetChangedFunc(func(text string) {
		populateModels(text)
	})

	searchInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			searchInput.SetText("")
			ts.app.SetFocus(modelList)
			return nil
		}
		if event.Key() == tcell.KeyDown {
			ts.app.SetFocus(modelList)
			return nil
		}
		return event
	})

	modelList.SetDoneFunc(func() {
		if backPage == "provider-workspace" {
			ts.showProviderWorkspace(provider)
		} else {
			ts.showDetail(provider, backPage)
		}
	})
	modelList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
			if backPage == "provider-workspace" {
				ts.showProviderWorkspace(provider)
			} else {
				ts.showDetail(provider, backPage)
			}
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			if backPage == "provider-workspace" {
				ts.showProviderWorkspace(provider)
			} else {
				ts.showDetail(provider, backPage)
			}
			return nil
		case event.Rune() == '/':
			ts.app.SetFocus(searchInput)
			return nil
		case event.Rune() == 'k' || event.Rune() == 'K':
			ts.showKeyForm(provider, backPage, func() {
				ts.showModels(provider, backPage)
			})
			return nil
		case event.Rune() == 'c' || event.Rune() == 'C':
			ts.showCustomModelForm(provider, backPage)
			return nil
		case event.Rune() == 'r' || event.Rune() == 'R':
			allModels = ts.buildModels(provider)
			populateModels(searchInput.GetText())
			return nil
		case event.Rune() == '1':
			filter := strings.ToLower(strings.TrimSpace(searchInput.GetText()))
			filtered := []string{}
			for _, m := range allModels {
				if filter != "" && !strings.Contains(strings.ToLower(m), filter) {
					continue
				}
				filtered = append(filtered, m)
			}
			idx := modelList.GetCurrentItem()
			if idx >= 0 && idx < len(filtered) {
				model := filtered[idx]
				if strings.HasSuffix(model, "[1m]") {
					model = strings.TrimSuffix(model, "[1m]")
				} else {
					model = model + "[1m]"
				}
				if backPage == "provider-workspace" {
					if ts.selectedModel == nil {
						ts.selectedModel = map[string]string{}
					}
					ts.selectedModel[provider] = model
					ts.showProviderWorkspace(provider)
				} else {
					ts.selectModelForAction(provider, backPage, model)
				}
			}
			return nil
		}
		return event
	})

	populateModels("")
	ts.updateTierInfo(provider, defaultModel)

	modelList.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		filter := strings.ToLower(strings.TrimSpace(searchInput.GetText()))
		filtered := []string{}
		for _, m := range allModels {
			if filter != "" && !strings.Contains(strings.ToLower(m), filter) {
				continue
			}
			filtered = append(filtered, m)
		}
		if index >= 0 && index < len(filtered) {
			ts.updateTierInfo(provider, filtered[index])
		} else if index == len(filtered) {
			ts.tierInfo.SetText("enter a custom model name")
		} else {
			ts.tierInfo.SetText("")
		}
	})

	help := tview.NewTextView()
	help.SetText("Enter actions   / filter   c custom   k edit key   1 toggle 1m   r refresh   q/esc/← back")

	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(searchInput, 1, 0, false)
	page.AddItem(ts.tierInfo, 1, 0, false)
	page.AddItem(modelList, 0, 1, true)
	page.AddItem(help, 1, 0, false)
	ts.pages.AddAndSwitchToPage("models", page, true)
	ts.app.SetFocus(modelList)
}

type modelSelectionActionSpec struct {
	Label    string
	Shortcut rune
}

func modelSelectionActionSpecs() []modelSelectionActionSpec {
	return []modelSelectionActionSpec{
		{Label: actionLabelLaunch, Shortcut: 'l'},
		{Label: actionLabelSetDefault, Shortcut: 's'},
		{Label: actionLabelBack, Shortcut: 'b'},
	}
}

func modelSelectionActionSpecsForAgent(agent AgentName) []modelSelectionActionSpec {
	return modelSelectionActionSpecs()
}

func modelSelectionActionLabels() []string {
	specs := modelSelectionActionSpecs()
	labels := make([]string, 0, len(specs))
	for _, spec := range specs {
		labels = append(labels, spec.Label)
	}
	return labels
}

func (ts *tuiState) selectModelForAction(provider, backPage, model string) {
	preset, err := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}
	if !preset.NoAPIKey && !hasConfigurableKey(storedAPIKeyForAgent(ts.cfg, ts.agent, provider), ts.typedAPIKeys[provider], ts.resetKeys[provider]) {
		ts.showKeyFormWithCancel(provider, backPage, func() {
			ts.showModelActions(provider, model, backPage)
		}, func() {
			ts.showModels(provider, backPage)
		})
		return
	}
	ts.showModelActions(provider, model, backPage)
}

// buildQuickRunCommand constructs a `cs run` command string for display
// in the model actions help bar. If model is empty or matches the
// provider's preset default model, the --model flag is omitted.
func buildQuickRunCommand(agent AgentName, provider, model string) string {
	cmd := fmt.Sprintf("cs run %s --provider %s", string(agent), provider)
	if model != "" {
		if preset, ok := providerPresets[provider]; !ok || preset.Model != model {
			cmd += fmt.Sprintf(" --model %s", model)
		}
	}
	return cmd
}

func (ts *tuiState) showModelActions(provider, model, backPage string) {
	actions := tview.NewList()
	actions.ShowSecondaryText(false)
	actions.SetBorder(true)
	actions.SetTitle(" Model Actions ")
	for _, spec := range modelSelectionActionSpecsForAgent(ts.agent) {
		spec := spec
		actions.AddItem(spec.Label, "", spec.Shortcut, func() {
			ts.runModelSelectionAction(spec.Label, provider, model, backPage)
		})
	}
	actions.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
			ts.runModelSelectionAction(actionLabelBack, provider, model, backPage)
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			ts.runModelSelectionAction(actionLabelBack, provider, model, backPage)
			return nil
		case event.Rune() == 'l' || event.Rune() == 'L':
			ts.runModelSelectionAction(actionLabelLaunch, provider, model, backPage)
			return nil
		case event.Rune() == 's' || event.Rune() == 'S':
			ts.runModelSelectionAction(actionLabelSetDefault, provider, model, backPage)
			return nil
		}
		return event
	})

	help := tview.NewTextView()
	helpText := "l launch   s set as default   b/esc/q back"
	helpRows := 2
	fullHelp := fmt.Sprintf("Provider: %s  |  Model: %s  |  %s", providerTitle(provider, ts.cfg), model, helpText)
	fullHelp += "\nQuick: " + buildQuickRunCommand(ts.agent, provider, model)
	help.SetText(fullHelp)

	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, helpRows, 0, false)
	page.AddItem(actions, 0, 1, true)
	ts.pages.AddAndSwitchToPage("model-actions", page, true)
	ts.app.SetFocus(actions)
}

func (ts *tuiState) runModelSelectionAction(actionLabel, provider, model, backPage string) {
	switch actionLabel {
	case actionLabelLaunch:
		ts.finishLaunch(provider, model)
	case actionLabelSetDefault:
		ts.finishSelection(provider, model)
	case actionLabelBack:
		ts.showModels(provider, backPage)
	}
}

func (ts *tuiState) showKeyForm(provider, backPage string, onSave func()) {
	ts.showKeyFormWithCancel(provider, backPage, onSave, onSave)
}

func (ts *tuiState) showKeyFormWithCancel(provider, backPage string, onSave func(), onCancel func()) {
	currentValue := strings.TrimSpace(ts.typedAPIKeys[provider])
	keyValue := currentValue
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	form.AddPasswordField("API Key", currentValue, 0, '*', func(text string) {
		keyValue = text
	})
	form.AddButton("Save", func() {
		keyValue = strings.TrimSpace(keyValue)
		if keyValue == "" {
			errLabel.SetText("API key cannot be empty")
			return
		}
		ts.typedAPIKeys[provider] = keyValue
		ts.resetKeys[provider] = true
		onSave()
	})
	form.AddButton("Cancel", onCancel)
	form.SetBorder(true)
	form.SetTitle(" Edit API Key ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			onCancel()
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
	ts.pages.AddAndSwitchToPage("key", page, true)
	ts.app.SetFocus(form)
}

func (ts *tuiState) showCustomModelForm(provider, backPage string) {
	modelValue := strings.TrimSpace(ts.customModels[provider])
	form := tview.NewForm()
	form.AddInputField("Model", modelValue, 0, nil, func(text string) {
		modelValue = text
	})
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form.AddButton("Save", func() {
		modelValue = strings.TrimSpace(modelValue)
		if modelValue == "" {
			errLabel.SetText("Model name cannot be empty")
			return
		}
		ts.customModels[provider] = modelValue
		ts.showModels(provider, backPage)
	})
	form.AddButton("Cancel", func() {
		ts.showModels(provider, backPage)
	})
	form.SetBorder(true)
	form.SetTitle(" Custom Model ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showModels(provider, backPage)
			return nil
		}
		return event
	})
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("custom-model", page, true)
	ts.app.SetFocus(form)
}

func (ts *tuiState) showCustomProviderForm() {
	nameValue := ""
	baseURLValue := ""
	apiKeyValue := ""
	modelValue := ""
	authEnvValue := ""
	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)
	form := tview.NewForm()
	form.AddInputField("Name", "", 0, nil, func(text string) {
		nameValue = text
	})
	form.AddInputField("Base URL", "", 0, nil, func(text string) {
		baseURLValue = text
	})
	form.AddPasswordField("API Key", "", 0, '*', func(text string) {
		apiKeyValue = text
	})
	form.AddInputField("Model", "", 0, nil, func(text string) {
		modelValue = text
	})
	var defaultProtocolIdx int
	var defaultProtocol ProviderProtocol
	switch ts.agent {
	case agentCodex:
		defaultProtocol = protocolOpenAIResponses
		defaultProtocolIdx = 2
	case agentOpencode:
		defaultProtocol = protocolOpenAIChat
		defaultProtocolIdx = 1
	default:
		defaultProtocol = protocolAnthropicMessages
		defaultProtocolIdx = 0
	}
	protocolValue := defaultProtocol
	protocolOptions := []string{string(protocolAnthropicMessages), string(protocolOpenAIChat), string(protocolOpenAIResponses)}
	form.AddDropDown("Protocol", protocolOptions, defaultProtocolIdx, func(_ string, idx int) {
		if idx >= 0 && idx < len(protocolOptions) {
			protocolValue = ProviderProtocol(protocolOptions[idx])
		}
	})
	form.AddDropDown("Auth Style", []string{"x-api-key (default)", "Bearer token"}, 0, func(option string, idx int) {
		if idx == 1 {
			authEnvValue = "ANTHROPIC_AUTH_TOKEN"
		} else {
			authEnvValue = ""
		}
	})
	form.AddButton("Save", func() {
		nameValue = strings.TrimSpace(nameValue)
		baseURLValue = strings.TrimSpace(baseURLValue)
		apiKeyValue = strings.TrimSpace(apiKeyValue)
		modelValue = strings.TrimSpace(modelValue)
		authEnvValue = strings.TrimSpace(authEnvValue)
		if nameValue == "" {
			errLabel.SetText("Name cannot be empty")
			return
		}
		if baseURLValue == "" {
			errLabel.SetText("Base URL cannot be empty")
			return
		}
		if err := validateBaseURL(baseURLValue); err != nil {
			errLabel.SetText(err.Error())
			return
		}
		if apiKeyValue == "" {
			errLabel.SetText("API Key cannot be empty")
			return
		}
		if modelValue == "" {
			errLabel.SetText("Model cannot be empty")
			return
		}
		ts.result = ConfigureSelection{
			Agent:    string(ts.agent),
			Provider: uniqueCustomProviderKey(ts.cfg, makeCustomProviderKey(nameValue)),
			Name:     nameValue,
			BaseURL:  baseURLValue,
			APIKey:   apiKeyValue,
			Model:    modelValue,
			Protocol: protocolValue,
			AuthEnv:  authEnvValue,
		}
		ts.resultErr = nil
		ts.app.Stop()
	})
	form.AddButton("Cancel", ts.showProviders)
	form.SetBorder(true)
	form.SetTitle(" Custom Provider ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showProviders()
			return nil
		}
		return event
	})
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("custom-provider", page, true)
	ts.app.SetFocus(form)
}

func (ts *tuiState) refreshCurrentConfig() {
	switch ts.agent {
	case agentCodex:
		_, cp, cm, _, _ := currentCodexProvider(ts.codexDir)
		ts.currentProvider = codexTOMLProviderKey(cp)
		ts.currentModel = cm
	case agentOpencode:
		ts.currentProvider, ts.currentModel = detectOpencodeCurrentProvider(ts.cfg, ts.opencodeDir)
	default:
		ts.currentProvider, ts.currentModel = currentConfiguredProvider(ts.cfg, ts.claudeDir)
	}
}

func (ts *tuiState) showAgents() {
	agentList := tview.NewList()
	agentList.ShowSecondaryText(false)
	agentList.SetBorder(true)
	agentList.SetTitle(" Agents ")
	agents := sortedAgentNames()
	for _, agent := range agents {
		agentName := agent
		agentList.AddItem(agentDisplayName(agentName), "", 0, func() {
			ts.agent = agentName
			ts.selectedProvider = ""
			ts.refreshCurrentConfig()
			ts.showProviders()
		})
	}
	agentList.SetDoneFunc(func() {
		ts.resultErr = errCancelled
		ts.app.Stop()
	})
	agentList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Rune() == 'q' || event.Rune() == 'Q' {
			ts.resultErr = errCancelled
			ts.app.Stop()
			return nil
		}
		return event
	})
	ts.pages.AddAndSwitchToPage("agents", agentList, true)
	ts.app.SetFocus(agentList)
}

func (ts *tuiState) showRestoreConfirm() {
	confirm := tview.NewList()
	confirm.ShowSecondaryText(false)
	confirm.SetBorder(true)
	confirm.SetTitle(" Restore Official Config ")
	confirm.AddItem("Restore", "", 'r', func() {
		ts.result = ConfigureSelection{
			Agent:    string(ts.agent),
			Provider: restoreProviderOption,
		}
		ts.resultErr = nil
		ts.app.Stop()
	})
	confirm.AddItem("Cancel", "", 'c', ts.showProviders)
	confirm.SetDoneFunc(ts.showProviders)
	confirm.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Rune() == 'q' || event.Rune() == 'Q' {
			ts.showProviders()
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(agentDisplayName(ts.agent))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(confirm, 0, 1, true)
	ts.pages.AddAndSwitchToPage("restore", page, true)
	ts.app.SetFocus(confirm)
}

func runArrowTUI(cfg *AppConfig, agent AgentName, selectAgent bool, currentProvider, currentModel, claudeDir, codexDir, opencodeDir string) (ConfigureSelection, error) {
	names := providerNamesForAgent(agent, cfg, agent == agentClaude || agent == agentOpencode, true)
	if len(names) == 0 {
		return ConfigureSelection{}, errors.New("no providers configured")
	}

	selectedProvider := names[0]
	for _, name := range names {
		if name == currentProvider {
			selectedProvider = name
			break
		}
	}

	ts := &tuiState{
		app:              tview.NewApplication(),
		pages:            tview.NewPages(),
		cfg:              cfg,
		agent:            agent,
		selectAgent:      selectAgent,
		currentProvider:  currentProvider,
		currentModel:     currentModel,
		claudeDir:        claudeDir,
		codexDir:         codexDir,
		opencodeDir:      opencodeDir,
		names:            names,
		selectedProvider: selectedProvider,
		typedAPIKeys:     map[string]string{},
		resetKeys:        map[string]bool{},
		selectedModel:    map[string]string{},
		advancedReturn:   map[string]bool{},
		customModels:     map[string]string{},
		tierOverrides:    map[string]StoredProvider{},
		resultErr:        nil,
	}

	ts.providerList = tview.NewList()
	ts.providerList.ShowSecondaryText(true)
	ts.providerList.SetBorder(true)
	ts.providerList.SetTitle(" " + agentDisplayName(ts.agent) + " Providers ")

	providerHelp := tview.NewTextView()
	providerHelp.SetText("Enter/→ workspace   q/esc quit")

	ts.providerPage = tview.NewFlex()
	ts.providerPage.SetDirection(tview.FlexRow)
	ts.providerPage.AddItem(ts.providerList, 0, 1, true)
	ts.providerPage.AddItem(providerHelp, 1, 0, false)
	ts.pages.AddPage("providers", ts.providerPage, true, true)

	ts.detailText = tview.NewTextView()
	ts.detailText.SetDynamicColors(true)
	ts.detailText.SetWrap(true)
	ts.detailText.SetBorder(true)
	ts.detailText.SetTitle(" Provider Details ")

	ts.tierInfo = tview.NewTextView()
	ts.tierInfo.SetDynamicColors(true)
	ts.tierInfo.SetWrap(true)

	ts.providerList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index >= 0 && index < len(ts.displayNames) {
			ts.selectedProvider = ts.displayNames[index]
		}
	})
	ts.providerList.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(ts.displayNames) {
			return
		}
		ts.selectedProvider = ts.displayNames[index]
		if ts.selectedProvider == customProviderOption {
			ts.showCustomProviderForm()
			return
		}
		if ts.selectedProvider == restoreProviderOption {
			ts.showRestoreConfirm()
			return
		}
		ts.showProviderWorkspace(ts.selectedProvider)
	})
	ts.providerList.SetDoneFunc(func() {
		ts.resultErr = errCancelled
		ts.app.Stop()
	})
	ts.providerList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyRight:
			index := ts.providerList.GetCurrentItem()
			if index >= 0 && index < len(ts.displayNames) {
				ts.selectedProvider = ts.displayNames[index]
				if ts.selectedProvider == customProviderOption {
					ts.showCustomProviderForm()
				} else if ts.selectedProvider == restoreProviderOption {
					ts.showRestoreConfirm()
				} else {
					ts.showProviderWorkspace(ts.selectedProvider)
				}
			}
			return nil
		case event.Key() == tcell.KeyEscape:
			ts.resultErr = errCancelled
			ts.app.Stop()
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			ts.resultErr = errCancelled
			ts.app.Stop()
			return nil
		}
		return event
	})

	ts.app.SetRoot(ts.pages, true)
	ts.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			ts.resultErr = errCancelled
			ts.app.Stop()
			return nil
		}
		return event
	})
	if ts.selectAgent {
		ts.showAgents()
	} else {
		ts.showProviders()
	}
	if err := ts.app.Run(); err != nil {
		return ConfigureSelection{}, err
	}
	if ts.resultErr != nil {
		return ConfigureSelection{}, ts.resultErr
	}
	return ts.result, nil
}

func promptConfigureSelectionFallback(reader *bufio.Reader, out io.Writer, cfg *AppConfig, agent AgentName, currentProvider, currentModel string) (ConfigureSelection, error) {
	names := providerNamesForAgent(agent, cfg, agent == agentClaude || agent == agentOpencode, true)

	for {
		fmt.Fprintf(out, "%s providers:\n", agentDisplayName(agent))
		for i, name := range names {
			if name == customProviderOption {
				fmt.Fprintf(out, "  %d) custom...\n", i+1)
				continue
			}
			if name == restoreProviderOption {
				fmt.Fprintf(out, "  %d) Restore official config...\n", i+1)
				continue
			}
			preset, err := resolveAgentProviderPreset(agent, name, cfg)
			if err != nil {
				continue
			}
			label := providerTitle(name, cfg)
			if name == currentProvider {
				label += " [current]"
			}
			fmt.Fprintf(out, "  %d) %s - %s\n", i+1, label, preset.BaseURL)
		}
		fmt.Fprint(out, "Provider: ")
		text, err := readLine(reader)
		if err != nil {
			return ConfigureSelection{}, err
		}
		provider, err := resolveProviderSelection(text, names)
		if err == nil {
			if provider == customProviderOption {
				return promptCustomProviderFallback(reader, out, cfg)
			}
			if provider == restoreProviderOption {
				return ConfigureSelection{Agent: string(agent), Provider: restoreProviderOption}, nil
			}

			defaultModel := defaultSelectionModelForAgent(cfg, agent, provider, currentProvider, currentModel)

			fmt.Fprintf(out, "Model (default: %s): ", defaultModel)
			modelText, err := readLine(reader)
			if err != nil {
				return ConfigureSelection{}, err
			}
			modelText = strings.TrimSpace(modelText)
			if modelText == "" {
				modelText = defaultModel
			}

			fmt.Fprint(out, "Action ([L]aunch/[s]ave as default): ")
			actionText, err := readLine(reader)
			if err != nil && err != io.EOF {
				return ConfigureSelection{}, err
			}
			launch := true
			apiKeyText := ""
			if err == io.EOF {
				launch = false
			}
			switch normalizedAction := strings.ToLower(strings.TrimSpace(actionText)); normalizedAction {
			case "", "l", "launch":
				// Keep the new default: pressing Enter launches.
			case "s", "save":
				launch = false
			default:
				// Backward compatibility for existing non-TTY fallback flows:
				// before this prompt existed, the next line was the API key.
				launch = false
				apiKeyText = strings.TrimSpace(actionText)
				fmt.Fprint(out, "API key: ")
			}

			stored := cfg.Providers[provider]
			if agent == agentOpencode {
				stored = opencodeProviderConfig(cfg, provider)
			} else if agent == agentCodex {
				stored = codexProviderConfig(cfg, provider)
			}

			return ConfigureSelection{
				Agent:    string(agent),
				Provider: provider,
				Model:    modelText,
				Launch:   launch,
				APIKey:   apiKeyText,
				AuthEnv:  stored.AuthEnv,
				Haiku:    stored.Haiku,
				Sonnet:   stored.Sonnet,
				Opus:     stored.Opus,
				Subagent: stored.Subagent,
			}, nil
		}
		fmt.Fprintf(out, "\nInvalid provider: %s\n", strings.TrimSpace(text))
	}
}

func promptAPIKey(in io.Reader, reader *bufio.Reader, out io.Writer, provider string) (string, error) {
	fmt.Fprintf(out, "Enter API key for %s:\n", provider)
	useTerminalMasking := false
	var maskFd uintptr
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		useTerminalMasking = true
		maskFd = f.Fd()
	}
	for {
		fmt.Fprint(out, "API key: ")
		var key string
		if useTerminalMasking {
			raw, err := term.ReadPassword(int(maskFd))
			fmt.Fprintln(out)
			if err != nil {
				return "", err
			}
			key = strings.TrimSpace(string(raw))
		} else {
			text, err := readLine(reader)
			if err != nil {
				return "", err
			}
			key = strings.TrimSpace(text)
		}
		if key != "" {
			return key, nil
		}
		fmt.Fprintln(out, "API key cannot be empty.")
	}
}

func promptCustomProviderFallback(reader *bufio.Reader, out io.Writer, cfg *AppConfig) (ConfigureSelection, error) {
	fmt.Fprintln(out, "Create custom provider")
	fmt.Fprint(out, "Name: ")
	name, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ConfigureSelection{}, errors.New("custom provider name cannot be empty")
	}
	fmt.Fprint(out, "Base URL: ")
	baseURL, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ConfigureSelection{}, errors.New("custom provider base url cannot be empty")
	}
	if err := validateBaseURL(baseURL); err != nil {
		return ConfigureSelection{}, err
	}
	fmt.Fprint(out, "API Key: ")
	apiKey, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ConfigureSelection{}, errors.New("custom provider api key cannot be empty")
	}
	fmt.Fprint(out, "Model: ")
	model, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ConfigureSelection{}, errors.New("custom provider model cannot be empty")
	}
	fmt.Fprint(out, "Use Bearer token auth instead of x-api-key? (y/N): ")
	bearerText, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	authEnv := ""
	if strings.ToLower(strings.TrimSpace(bearerText)) == "y" {
		authEnv = "ANTHROPIC_AUTH_TOKEN"
	}

	return ConfigureSelection{
		Provider: uniqueCustomProviderKey(cfg, makeCustomProviderKey(name)),
		Name:     name,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
		AuthEnv:  authEnv,
	}, nil
}

func hasConfigurableKey(savedKey, typedKey string, resetKey bool) bool {
	if strings.TrimSpace(typedKey) != "" {
		return true
	}
	if resetKey {
		return false
	}
	return strings.TrimSpace(savedKey) != ""
}

func readLine(reader *bufio.Reader) (string, error) {
	text, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && text == "" {
		return "", io.EOF
	}
	return strings.TrimRight(text, "\r\n"), nil
}

func resolveProviderSelection(input string, names []string) (string, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return "", errors.New("empty provider")
	}

	if idx, err := strconv.Atoi(text); err == nil {
		if idx >= 1 && idx <= len(names) {
			return names[idx-1], nil
		}
		return "", errors.New("provider index out of range")
	}

	provider := canonicalProviderName(text)
	if provider == "custom" || provider == "custom..." {
		return customProviderOption, nil
	}
	if provider == "restore" || provider == "restore official config" || provider == "restore official config..." {
		return restoreProviderOption, nil
	}
	for _, name := range names {
		if name == provider {
			return provider, nil
		}
	}
	return "", errors.New("unsupported provider")
}

func flagWasProvided(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func shouldUseArrowTUI(in *os.File) bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := in.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func buildModelList(cfg *AppConfig, provider string, customModels map[string]string) []string {
	models := providerModels(cfg, provider)
	customModel := strings.TrimSpace(customModels[provider])
	if customModel == "" {
		return models
	}
	filtered := []string{customModel}
	for _, model := range models {
		if model != customModel {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// mirrorOpencodeCustomProviderToShared copies a custom opencode provider's base
// URL/name/model/authEnv into the shared cfg.Providers map. upsertProviderConfig
// routes opencode providers to the agent-scoped cfg.Agents[opencode].Providers
// map, but resolveProviderPreset (used by resolveAgentProviderPreset and the
// opencode switch via resolveSwitchPreset) only consults the shared cfg.Providers
// map. Without this mirror, a newly-created custom opencode provider is
// unresolvable and cmdConfigure fails with "unsupported provider".
func mirrorOpencodeCustomProviderToShared(cfg *AppConfig, selection ConfigureSelection) {
	if selection.Agent != string(agentOpencode) {
		return
	}
	if strings.TrimSpace(selection.BaseURL) == "" {
		return
	}
	shared := cfg.Providers[selection.Provider]
	shared.BaseURL = strings.TrimSpace(selection.BaseURL)
	if strings.TrimSpace(selection.Name) != "" {
		shared.Name = strings.TrimSpace(selection.Name)
	}
	if strings.TrimSpace(selection.Model) != "" {
		shared.Model = strings.TrimSpace(selection.Model)
	}
	if strings.TrimSpace(selection.AuthEnv) != "" {
		shared.AuthEnv = strings.TrimSpace(selection.AuthEnv)
	}
	cfg.Providers[selection.Provider] = shared
}

// detectOpencodeCurrentProvider determines the current opencode provider key and
// model from the opencode config. It uses detectProvider(baseURL, model) for
// deterministic preset identification (so two providers sharing the same default
// model no longer jump randomly during Go map iteration), falling back to the
// model-match scan over shared and agent-scoped stored providers only when
// detection returns the custom/unknown sentinel.
func detectOpencodeCurrentProvider(cfg *AppConfig, opencodeDir string) (string, string) {
	_, cm, baseURL, _, _, err := currentOpencodeProvider(opencodeDir)
	if err != nil {
		return "", ""
	}
	if provider := detectProvider(baseURL, cm); provider != customDetectedProvider {
		return provider, cm
	}
	if cm != "" {
		var matches []string
		for name, stored := range cfg.Providers {
			if stored.Model == cm {
				matches = append(matches, name)
			}
		}
		for name, stored := range agentConfig(cfg, agentOpencode).Providers {
			if stored.Model == cm {
				matches = append(matches, name)
			}
		}
		if len(matches) > 0 {
			sort.Strings(matches)
			return matches[0], cm
		}
	}
	return "", cm
}
