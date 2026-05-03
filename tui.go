package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func cmdConfigure(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	resetKey := fs.Bool("reset-key", false, "force re-enter api key for the selected provider")
	dryRun := fs.Bool("dry-run", false, "preview what would be written without modifying settings.json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, configPath, err := loadAppConfig()
	if err != nil {
		return err
	}

	currentProvider, currentModel := currentConfiguredProvider(cfg, *claudeDir)
	reader := bufio.NewReader(in)
	var selection ConfigureSelection
	if file, ok := in.(*os.File); ok && shouldUseArrowTUI(file) {
		selection, err = runArrowTUI(cfg, currentProvider, currentModel)
		if err != nil {
			return err
		}
	} else {
		selection, err = promptConfigureSelectionFallback(reader, out, cfg, currentProvider, currentModel)
		if err != nil {
			return err
		}
	}
	provider := selection.Provider

	existingKey := strings.TrimSpace(cfg.Providers[provider].APIKey)
	apiKey := existingKey
	if selection.APIKey != "" {
		apiKey = selection.APIKey
	} else if apiKey == "" || *resetKey || selection.ResetKey {
		apiKey, err = promptAPIKey(reader, out, provider)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "using saved api key for %s\n", provider)
	}
	upsertProviderConfig(cfg, selection, apiKey)

	if *dryRun {
		preset, err := resolveSwitchPreset(provider, cfg, selection.Model)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "[dry-run] would save provider config for %s in %s\n", provider, configPath)
		fmt.Fprintf(out, "[dry-run] would switch Claude to %s\n", preset.Name)
		fmt.Fprintf(out, "[dry-run] base_url: %s\n", preset.BaseURL)
		fmt.Fprintf(out, "[dry-run] model: %s\n", preset.Model)
		return nil
	}

	if err := writeJSONAtomic(configPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved provider config for %s in %s\n", provider, configPath)

	if err := switchProvider(provider, cfg, apiKey, selection.Model, *claudeDir, out, false); err != nil {
		return err
	}
	return nil
}

type tuiState struct {
	app             *tview.Application
	pages           *tview.Pages
	cfg             *AppConfig
	currentProvider string
	currentModel    string
	names           []string

	selectedProvider string
	typedAPIKeys     map[string]string
	resetKeys        map[string]bool
	customModels     map[string]string

	result   ConfigureSelection
	resultErr error

	providerList *tview.List
	providerPage *tview.Flex
	detailText   *tview.TextView
}

func (ts *tuiState) buildModels(provider string) []string {
	return buildModelList(ts.cfg, provider, ts.customModels)
}

func (ts *tuiState) finishSelection(provider, model string) {
	ts.result = ConfigureSelection{
		Provider: provider,
		Model:    model,
		ResetKey: ts.resetKeys[provider],
		APIKey:   strings.TrimSpace(ts.typedAPIKeys[provider]),
	}
	ts.resultErr = nil
	ts.app.Stop()
}

func (ts *tuiState) showProviders() {
	ts.rebuildProviderList()
	ts.pages.SwitchToPage("providers")
	ts.app.SetFocus(ts.providerList)
}

func (ts *tuiState) rebuildProviderList() {
	ts.providerList.Clear()
	selectedIndex := 0
	for i, name := range ts.names {
		if name == ts.selectedProvider {
			selectedIndex = i
		}
		if name == customProviderOption {
			ts.providerList.AddItem("custom...", "Add a custom Anthropic-compatible provider", 0, nil)
			continue
		}
		preset, err := resolveProviderPreset(name, ts.cfg)
		if err != nil {
			continue
		}
		suffix := []string{}
		if name == ts.currentProvider {
			suffix = append(suffix, "current")
		}
		if strings.TrimSpace(ts.cfg.Providers[name].APIKey) != "" {
			suffix = append(suffix, "saved")
		}
		title := providerTitle(name, ts.cfg)
		if len(suffix) > 0 {
			title += " [" + strings.Join(suffix, ", ") + "]"
		}
		ts.providerList.AddItem(title, preset.BaseURL, 0, nil)
	}
	ts.providerList.SetCurrentItem(selectedIndex)
}

func (ts *tuiState) showDetail(provider, backPage string) {
	ts.selectedProvider = provider
	preset, err := resolveProviderPreset(provider, ts.cfg)
	if err != nil {
		ts.resultErr = err
		ts.app.Stop()
		return
	}

	hasSavedKey := strings.TrimSpace(ts.cfg.Providers[provider].APIKey) != ""
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]Provider[::-]  %s\n", providerTitle(provider, ts.cfg))
	fmt.Fprintf(&b, "[::b]Preset[::-]    %s\n", preset.Name)
	fmt.Fprintf(&b, "[::b]Base URL[::-]  %s\n", preset.BaseURL)
	fmt.Fprintf(&b, "[::b]Saved Key[::-] %s\n", maskAPIKey(ts.cfg.Providers[provider].APIKey))
	fmt.Fprintf(&b, "[::b]Active[::-]    %s / %s\n", currentProviderLabel(ts.currentProvider), currentModelLabel(ts.currentModel))
	if ts.resetKeys[provider] {
		fmt.Fprintf(&b, "[yellow]Pending key update on apply[-]\n")
	} else if !hasSavedKey {
		fmt.Fprintf(&b, "[yellow]No saved key yet[-]\n")
	}
	ts.detailText.SetText(b.String())

	actions := tview.NewList()
	actions.ShowSecondaryText(false)
	actions.SetBorder(true)
	actions.SetTitle(" Actions ")
	actions.AddItem("Choose Model", "", 'm', func() {
		ts.showModels(provider, "detail")
	})
	actions.AddItem("Edit API Key", "", 'k', func() {
		ts.showKeyForm(provider, backPage, func() {
			ts.showDetail(provider, backPage)
		})
	})
	actions.AddItem("Back", "", 'b', ts.showProviders)
	actions.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
			ts.showProviders()
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			ts.showProviders()
			return nil
		case event.Rune() == 'k' || event.Rune() == 'K':
			ts.showKeyForm(provider, backPage, func() {
				ts.showDetail(provider, backPage)
			})
			return nil
		}
		return event
	})

	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(ts.detailText, 0, 1, false)
	page.AddItem(actions, 8, 0, true)
	ts.pages.AddAndSwitchToPage("detail", page, true)
	ts.app.SetFocus(actions)
}

func (ts *tuiState) showModels(provider, backPage string) {
	ts.selectedProvider = provider
	models := ts.buildModels(provider)
	modelList := tview.NewList()
	modelList.ShowSecondaryText(false)
	modelList.SetBorder(true)
	modelList.SetTitle(" Models ")
	for _, model := range models {
		label := model
		if model == defaultSelectionModel(ts.cfg, provider, ts.currentProvider, ts.currentModel) {
			label += " [default]"
		}
		modelName := model
		modelList.AddItem(label, "", 0, func() {
			if !hasConfigurableKey(strings.TrimSpace(ts.cfg.Providers[provider].APIKey), ts.typedAPIKeys[provider], ts.resetKeys[provider]) {
				ts.showKeyForm(provider, backPage, func() {
					ts.showModels(provider, backPage)
				})
				return
			}
			ts.finishSelection(provider, modelName)
		})
	}
	modelList.AddItem("Custom model...", "", 0, func() {
		ts.showCustomModelForm(provider)
	})
	selectedIndex := modelIndex(ts.cfg, provider, ts.currentProvider, ts.currentModel)
	if customModel := strings.TrimSpace(ts.customModels[provider]); customModel != "" {
		selectedIndex = 0
	}
	if selectedIndex >= 0 && selectedIndex < len(models) {
		modelList.SetCurrentItem(selectedIndex)
	}
	modelList.SetDoneFunc(func() {
		ts.showDetail(provider, backPage)
	})
	modelList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
			ts.showDetail(provider, backPage)
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			ts.showDetail(provider, backPage)
			return nil
		case event.Rune() == 'k' || event.Rune() == 'K':
			ts.showKeyForm(provider, backPage, func() {
				ts.showModels(provider, backPage)
			})
			return nil
		case event.Rune() == 'c' || event.Rune() == 'C':
			ts.showCustomModelForm(provider)
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText("Enter apply   c custom model   k edit key   q/esc/← back")
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(modelList, 0, 1, true)
	page.AddItem(help, 1, 0, false)
	ts.pages.AddAndSwitchToPage("models", page, true)
	ts.app.SetFocus(modelList)
}

func (ts *tuiState) showKeyForm(provider, backPage string, onSave func()) {
	currentValue := strings.TrimSpace(ts.typedAPIKeys[provider])
	keyValue := currentValue
	form := tview.NewForm()
	form.AddPasswordField("API Key", currentValue, 0, '*', func(text string) {
		keyValue = text
	})
	form.AddButton("Save", func() {
		keyValue = strings.TrimSpace(keyValue)
		ts.typedAPIKeys[provider] = keyValue
		ts.resetKeys[provider] = true
		onSave()
	})
	form.AddButton("Cancel", onSave)
	form.SetBorder(true)
	form.SetTitle(" Edit API Key ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			onSave()
			return nil
		}
		return event
	})
	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s", providerTitle(provider, ts.cfg)))
	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("key", page, true)
	ts.app.SetFocus(form)
}

func (ts *tuiState) showCustomModelForm(provider string) {
	modelValue := strings.TrimSpace(ts.customModels[provider])
	form := tview.NewForm()
	form.AddInputField("Model", modelValue, 0, nil, func(text string) {
		modelValue = text
	})
	form.AddButton("Save", func() {
		modelValue = strings.TrimSpace(modelValue)
		if modelValue == "" {
			return
		}
		ts.customModels[provider] = modelValue
		ts.showModels(provider, "detail")
	})
	form.AddButton("Cancel", func() {
		ts.showModels(provider, "detail")
	})
	form.SetBorder(true)
	form.SetTitle(" Custom Model ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showModels(provider, "detail")
			return nil
		}
		return event
	})
	ts.pages.AddAndSwitchToPage("custom-model", form, true)
	ts.app.SetFocus(form)
}

func (ts *tuiState) showCustomProviderForm() {
	nameValue := ""
	baseURLValue := ""
	apiKeyValue := ""
	modelValue := ""
	authEnvValue := ""
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
		if nameValue == "" || baseURLValue == "" || apiKeyValue == "" || modelValue == "" {
			return
		}
		if err := validateBaseURL(baseURLValue); err != nil {
			return
		}
		ts.result = ConfigureSelection{
			Provider: uniqueCustomProviderKey(ts.cfg, makeCustomProviderKey(nameValue)),
			Name:     nameValue,
			BaseURL:  baseURLValue,
			APIKey:   apiKeyValue,
			Model:    modelValue,
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
	ts.pages.AddAndSwitchToPage("custom-provider", form, true)
	ts.app.SetFocus(form)
}

func runArrowTUI(cfg *AppConfig, currentProvider, currentModel string) (ConfigureSelection, error) {
	names := sortedProviderNames(cfg, true)
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
		currentProvider:  currentProvider,
		currentModel:     currentModel,
		names:            names,
		selectedProvider: selectedProvider,
		typedAPIKeys:     map[string]string{},
		resetKeys:        map[string]bool{},
		customModels:     map[string]string{},
		resultErr:        errors.New("cancelled"),
	}

	ts.providerList = tview.NewList()
	ts.providerList.ShowSecondaryText(true)
	ts.providerList.SetBorder(true)
	ts.providerList.SetTitle(" Providers ")

	providerHelp := tview.NewTextView()
	providerHelp.SetText("Enter/→ details   q/esc quit")

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

	ts.providerList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index >= 0 && index < len(names) {
			ts.selectedProvider = names[index]
		}
	})
	ts.providerList.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(names) {
			return
		}
		ts.selectedProvider = names[index]
		if ts.selectedProvider == customProviderOption {
			ts.showCustomProviderForm()
			return
		}
		ts.showDetail(ts.selectedProvider, "providers")
	})
	ts.providerList.SetDoneFunc(func() {
		ts.app.Stop()
	})
	ts.providerList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyRight:
			index := ts.providerList.GetCurrentItem()
			if index >= 0 && index < len(names) {
				ts.selectedProvider = names[index]
				if ts.selectedProvider == customProviderOption {
					ts.showCustomProviderForm()
				} else {
					ts.showDetail(ts.selectedProvider, "providers")
				}
			}
			return nil
		case event.Key() == tcell.KeyEscape:
			ts.app.Stop()
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			ts.app.Stop()
			return nil
		}
		return event
	})

	ts.app.SetRoot(ts.pages, true)
	ts.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			ts.app.Stop()
			return nil
		}
		return event
	})
	ts.showProviders()
	if err := ts.app.Run(); err != nil {
		return ConfigureSelection{}, err
	}
	if ts.resultErr != nil {
		return ConfigureSelection{}, ts.resultErr
	}
	return ts.result, nil
}

func promptConfigureSelectionFallback(reader *bufio.Reader, out io.Writer, cfg *AppConfig, currentProvider, currentModel string) (ConfigureSelection, error) {
	names := sortedProviderNames(cfg, true)

	for {
		fmt.Fprintln(out, "Providers:")
		for i, name := range names {
			if name == customProviderOption {
				fmt.Fprintf(out, "  %d) custom...\n", i+1)
				continue
			}
			preset, err := resolveProviderPreset(name, cfg)
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

			defaultModel := defaultSelectionModel(cfg, provider, currentProvider, currentModel)

			fmt.Fprintf(out, "Model (default: %s): ", defaultModel)
			modelText, err := readLine(reader)
			if err != nil {
				return ConfigureSelection{}, err
			}
			modelText = strings.TrimSpace(modelText)
			if modelText == "" {
				modelText = defaultModel
			}

			return ConfigureSelection{
				Provider: provider,
				Model:    modelText,
			}, nil
		}
		fmt.Fprintf(out, "\nInvalid provider: %s\n", strings.TrimSpace(text))
	}
}

func promptAPIKey(reader *bufio.Reader, out io.Writer, provider string) (string, error) {
	fmt.Fprintf(out, "Enter API key for %s:\n", provider)
	for {
		fmt.Fprint(out, "API key: ")
		text, err := readLine(reader)
		if err != nil {
			return "", err
		}
		key := strings.TrimSpace(text)
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
	if _, ok := providerPresets[provider]; !ok {
		for _, name := range names {
			if name == provider {
				return provider, nil
			}
		}
		return "", errors.New("unsupported provider")
	}
	return provider, nil
}

func shouldUseArrowTUI(in *os.File) bool {
	if runtime.GOOS == "windows" {
		return false
	}
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
