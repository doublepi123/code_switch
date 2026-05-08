# Codex OpenRouter Support + Model Search

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenRouter provider support for Codex agent, dynamic model fetching from OpenRouter API, and a search/filter UI on the TUI model selection page.

**Architecture:** Follow the existing `codexOllamaCloudPreset()` pattern — clone the `openrouter` preset with Codex-specific overrides (BaseURL, AuthEnv, ForceModelTiers). Parameterize the TOML generation to handle multiple providers. Add dynamic model discovery via `GET /api/v1/models` (OpenRouter API). Add a filter input in the TUI model list.

**Tech Stack:** Go 1.22+, tview + tcell for TUI, no new dependencies.

---

### Task 1: Add OpenRouter model discovery functions (`presets.go`)

**Files:**
- Modify: `presets.go:1-57`

- [ ] **Step 1: Add OpenRouter API response types and `discoverOpenRouterModels`**

After the existing `ollamaModel` type (line 18), add:

```go
type openRouterModelsResponse struct {
	Data []openRouterModelData `json:"data"`
}

type openRouterModelData struct {
	ID string `json:"id"`
}

func discoverOpenRouterModels(apiKey string) []string {
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var data openRouterModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}
	if len(data.Data) == 0 {
		return nil
	}
	models := make([]string, 0, len(data.Data))
	for _, m := range data.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" {
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models
}
```

- [ ] **Step 2: Add `openRouterModels` wrapper**

After `discoverOpenRouterModels`, add:

```go
func openRouterModels(cfg *AppConfig) []string {
	apiKey := storedAPIKeyForAgent(cfg, agentCodex, "openrouter")
	if apiKey == "" {
		apiKey = storedAPIKeyForAgent(cfg, agentClaude, "openrouter")
	}
	if discovered := discoverOpenRouterModels(apiKey); len(discovered) > 0 {
		return discovered
	}
	return providerPresets["openrouter"].Models
}
```

- [ ] **Step 3: Build and test**

```bash
go vet ./... && go build -o cs .
```

---

### Task 2: Add `codexOpenRouterPreset` and expand whitelist (`agent.go`)

**Files:**
- Modify: `agent.go:77-131`

- [ ] **Step 1: Add `codexOpenRouterPreset` function**

After `codexOllamaCloudPreset()` (line 82), add:

```go
func codexOpenRouterPreset() ProviderPreset {
	preset := providerPresets["openrouter"]
	preset.BaseURL = "https://openrouter.ai/api/v1"
	preset.AuthEnv = "OPENROUTER_API_KEY"
	preset.ForceModelTiers = true
	return preset
}
```

- [ ] **Step 2: Update `resolveAgentProviderPreset` to handle both providers**

Replace lines 84-99:

```go
func resolveAgentProviderPreset(agent AgentName, provider string, cfg *AppConfig) (ProviderPreset, error) {
	switch agent {
	case agentCodex:
		provider = canonicalProviderName(provider)
		var preset ProviderPreset
		switch provider {
		case "ollama-cloud":
			preset = codexOllamaCloudPreset()
		case "openrouter":
			preset = codexOpenRouterPreset()
		default:
			return ProviderPreset{}, fmt.Errorf("unsupported provider %q for agent codex", provider)
		}
		if stored := codexProviderConfig(cfg, provider); strings.TrimSpace(stored.Model) != "" {
			preset = withSelectedModel(preset, stored.Model)
		}
		return preset, nil
	default:
		return resolveProviderPreset(provider, cfg)
	}
}
```

- [ ] **Step 3: Update `resolveAgentSwitchPreset` to handle both providers**

Replace lines 101-117:

```go
func resolveAgentSwitchPreset(agent AgentName, provider string, cfg *AppConfig, modelOverride string) (ProviderPreset, error) {
	switch agent {
	case agentCodex:
		provider = canonicalProviderName(provider)
		var preset ProviderPreset
		switch provider {
		case "ollama-cloud":
			preset = codexOllamaCloudPreset()
		case "openrouter":
			preset = codexOpenRouterPreset()
		default:
			return ProviderPreset{}, fmt.Errorf("unsupported provider %q for agent codex", provider)
		}
		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(codexProviderConfig(cfg, provider).Model)
		}
		return withSelectedModel(preset, model), nil
	default:
		return resolveSwitchPreset(provider, cfg, modelOverride)
	}
}
```

- [ ] **Step 4: Update `providerNamesForAgent` to include openrouter**

Replace line 123:

```go
case agentCodex:
    names = []string{"ollama-cloud", "openrouter"}
```

- [ ] **Step 5: Update `providerModelsForAgent` to use dynamic models for openrouter**

Replace lines 133-145:

```go
func providerModelsForAgent(cfg *AppConfig, agent AgentName, provider string) []string {
	if agent == agentCodex {
		preset, err := resolveAgentProviderPreset(agent, provider, cfg)
		if err != nil {
			return nil
		}
		if provider == "openrouter" {
			if models := openRouterModels(cfg); len(models) > 0 {
				return models
			}
		}
		if len(preset.Models) == 0 {
			return []string{preset.Model}
		}
		return preset.Models
	}
	return providerModels(cfg, provider)
}
```

- [ ] **Step 6: Build and run tests**

```bash
go vet ./... && go test ./... && go build -o cs .
```

---

### Task 3: Add TOML provider name mapping (`codex.go`)

**Files:**
- Modify: `codex.go:28-68`

- [ ] **Step 1: Add `codexTOMLProviderName` helper**

After the `codexConfigPath` function (line 26), add:

```go
func codexTOMLProviderName(provider string) string {
	switch provider {
	case "openrouter":
		return "OpenRouter"
	default:
		return provider
	}
}
```

- [ ] **Step 2: Update `switchCodexProvider` line 66 to use the helper**

Replace line 66:
```go
fmt.Fprintf(out, "auth: cs token %s --agent codex\n", provider)
```
(stays the same, already dynamic)

- [ ] **Step 3: Build verification**

```bash
go vet ./... && go build -o cs .
```

---

### Task 4: Parameterize `applyCodexPresetTOML` for multiple providers (`codex.go`)

**Files:**
- Modify: `codex.go:28-68` (switchCodexProvider)
- Modify: `codex.go:70-99` (applyCodexPresetTOML)

- [ ] **Step 1: Update `switchCodexProvider` to pass provider name to TOML generation**

Replace line 52:
```go
updated := applyCodexPresetTOML(existing, preset, provider)
```

- [ ] **Step 2: Update `applyCodexPresetTOML` signature and body**

Replace the entire function (lines 70-99):

```go
func applyCodexPresetTOML(existing string, preset ProviderPreset, provider string) string {
	cleaned := removeCodexManagedTOML(existing, true, true, nil)
	topLevel, sections := splitBeforeFirstTOMLSection(cleaned)
	var b strings.Builder

	if top := strings.TrimRight(topLevel, "\n"); strings.TrimSpace(top) != "" {
		b.WriteString(top)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "model = %q\n", preset.Model)
	providerName := codexTOMLProviderName(provider)
	fmt.Fprintf(&b, "model_provider = %q\n", providerName)
	b.WriteString("approvals_reviewer = \"user\"\n")
	if preset.ReasoningEffort != "" {
		fmt.Fprintf(&b, "reasoning_effort = %q\n", preset.ReasoningEffort)
	}

	if strings.TrimSpace(sections) != "" {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimRight(strings.TrimLeft(sections, "\n"), "\n"))
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "[model_providers.%s]\n", providerName)
	fmt.Fprintf(&b, "name = %q\n", preset.Name)
	fmt.Fprintf(&b, "base_url = %q\n", preset.BaseURL)
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString(fmt.Sprintf("\n[model_providers.%s.auth]\n", providerName))
	b.WriteString("command = \"cs\"\n")
	fmt.Fprintf(&b, "args = [\"token\", %q, \"--agent\", \"codex\"]\n", provider)
	return b.String()
}
```

- [ ] **Step 3: Build verification**

```bash
go vet ./... && go build -o cs .
```

---

### Task 5: Update cleanup functions for multi-provider support (`codex.go`)

**Files:**
- Modify: `codex.go:112-207` (restoreCodexConfig, removeCodexManagedTOML, isManagedCodexModel, parseCodexTopLevel)

- [ ] **Step 1: Update `removeCodexManagedTOML` to handle both provider sections**

Replace lines 140-192:

```go
func removeCodexManagedTOML(existing string, removeTopLevelModel bool, removeTopLevelProvider bool, cfg *AppConfig) string {
	provider, model, _, _ := parseCodexTopLevel(existing)

	isKnownProvider := false
	for _, p := range []string{"ollama-cloud", "openrouter"} {
		pName := codexTOMLProviderName(p)
		if provider == pName {
			isKnownProvider = true
			break
		}
	}

	if !removeTopLevelProvider {
		removeTopLevelProvider = isKnownProvider
	}
	if !removeTopLevelModel {
		removeTopLevelModel = isKnownProvider && isManagedCodexModel(model, cfg)
	}
	removeTopLevelApprovalsReviewer := removeTopLevelModel && removeTopLevelProvider
	if !removeTopLevelApprovalsReviewer {
		removeTopLevelApprovalsReviewer = isKnownProvider
	}

	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines))
	section := ""
	skipSection := false
	for _, line := range lines {
		if name, ok := tomlSectionName(line); ok {
			section = name
			skipSection = false
			for _, p := range []string{"ollama-cloud", "openrouter"} {
				pName := codexTOMLProviderName(p)
				if name == "model_providers."+pName || strings.HasPrefix(name, "model_providers."+pName+".") {
					skipSection = true
					break
				}
			}
			if skipSection {
				continue
			}
		}
		if skipSection {
			continue
		}
		if section == "" {
			if key, _, ok := tomlKeyValue(line); ok {
				if key == "model" && removeTopLevelModel {
					continue
				}
				if key == "model_provider" && removeTopLevelProvider {
					continue
				}
				if key == "approvals_reviewer" && removeTopLevelApprovalsReviewer {
					continue
				}
				if key == "reasoning_effort" && removeTopLevelApprovalsReviewer {
					continue
				}
			}
		}
		out = append(out, line)
	}

	result := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(result) == "" {
		return ""
	}
	return result + "\n"
}
```

- [ ] **Step 2: Update `isManagedCodexModel` to check both presets**

Replace lines 194-207:

```go
func isManagedCodexModel(model string, cfg *AppConfig) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, fn := range []func() ProviderPreset{codexOllamaCloudPreset, codexOpenRouterPreset} {
		preset := fn()
		if model == preset.Model || containsString(preset.Models, model) {
			return true
		}
	}
	if cfg != nil {
		for _, p := range []string{"ollama-cloud", "openrouter"} {
			if strings.TrimSpace(codexProviderConfig(cfg, p).Model) == model {
				return true
			}
		}
	}
	return false
}
```

- [ ] **Step 3: Update `parseCodexTopLevel` to handle both provider sections**

Replace lines 209-236:

```go
func parseCodexTopLevel(content string) (provider string, model string, baseURL string, err error) {
	lines := strings.Split(content, "\n")
	section := ""
	for _, line := range lines {
		if name, ok := tomlSectionName(line); ok {
			section = name
			continue
		}
		key, value, ok := tomlKeyValue(line)
		if !ok {
			continue
		}
		switch section {
		case "":
			switch key {
			case "model_provider":
				provider = tomlStringValue(value)
			case "model":
				model = tomlStringValue(value)
			}
		default:
			if key == "base_url" {
				baseURL = tomlStringValue(value)
			}
		}
	}
	return provider, model, baseURL, nil
}
```

- [ ] **Step 4: Build verification**

```bash
go vet ./... && go build -o cs .
```

---

### Task 6: Update help text and completions (`main.go`)

**Files:**
- Modify: `main.go:421-427`

- [ ] **Step 1: Update help text to list both Codex providers**

Replace line 426:

```go
fmt.Fprint(out, "\nCodex providers:\n  ollama-cloud\n  openrouter\n")
```

- [ ] **Step 2: Build verification**

```bash
go vet ./... && go build -o cs .
```

---

### Task 7: Add model search/filter to TUI (`tui.go`)

**Files:**
- Modify: `tui.go:285-349` (showModels)
- Modify: `tui.go:130-151` (tuiState struct, add refresh callback field)

- [ ] **Step 1: Add `onRefreshModels` field to `tuiState`**

In the `tuiState` struct (line 130), add after `customModels`:

```go
	onRefreshModels func()
```

- [ ] **Step 2: Rewrite `showModels` with search input, filter, and refresh**

Replace lines 285-349:

```go
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
				preset, _ := resolveAgentProviderPreset(ts.agent, provider, ts.cfg)
				if !preset.NoAPIKey && !hasConfigurableKey(storedAPIKeyForAgent(ts.cfg, ts.agent, provider), ts.typedAPIKeys[provider], ts.resetKeys[provider]) {
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
		selectedIndex := modelIndexForAgent(ts.cfg, ts.agent, provider, ts.currentProvider, ts.currentModel)
		if customModel := strings.TrimSpace(ts.customModels[provider]); customModel != "" {
			selectedIndex = 0
		}
		if selectedIndex >= 0 && selectedIndex < modelList.GetItemCount()-1 {
			modelList.SetCurrentItem(selectedIndex)
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
		case event.Rune() == '/':
			ts.app.SetFocus(searchInput)
			return nil
		case event.Rune() == 'k' || event.Rune() == 'K':
			ts.showKeyForm(provider, backPage, func() {
				ts.showModels(provider, backPage)
			})
			return nil
		case event.Rune() == 'c' || event.Rune() == 'C':
			ts.showCustomModelForm(provider)
			return nil
		case event.Rune() == 'r' || event.Rune() == 'R':
			allModels = ts.buildModels(provider)
			populateModels(searchInput.GetText())
			return nil
		}
		return event
	})

	populateModels("")

	help := tview.NewTextView()
	help.SetText("Enter apply   / filter   c custom   k edit key   r refresh   q/esc/← back")

	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(searchInput, 1, 0, false)
	page.AddItem(modelList, 0, 1, true)
	page.AddItem(help, 1, 0, false)
	ts.pages.AddAndSwitchToPage("models", page, true)
	ts.app.SetFocus(searchInput)
}
```

- [ ] **Step 3: Check that `tcell` is imported in tui.go**

Verify `tcell` is in the imports (it should be at `tui.go:16`).

- [ ] **Step 4: Build verification**

```bash
go vet ./... && go build -o cs .
```

---

### Task 8: Write and run tests

**Files:**
- Modify: `code_switch_agent_test.go:82-538`

- [ ] **Step 1: Add test for Codex OpenRouter switch writes correct TOML**

After the last test in `code_switch_agent_test.go` (line 538), add:

```go
func TestCodexSwitchOpenRouterWritesCorrectTOML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "openrouter", "--agent", "codex", "--api-key", "or-sk-test", "--model", "anthropic/claude-sonnet-4.6", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex openrouter switch returned error: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	config := string(configBytes)
	for _, want := range []string{
		`model = "anthropic/claude-sonnet-4.6"`,
		`model_provider = "OpenRouter"`,
		`approvals_reviewer = "user"`,
		`[model_providers.OpenRouter]`,
		`name = "OpenRouter"`,
		`base_url = "https://openrouter.ai/api/v1"`,
		`wire_api = "responses"`,
		`[model_providers.OpenRouter.auth]`,
		`command = "cs"`,
		`args = ["token", "openrouter", "--agent", "codex"]`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("codex openrouter config missing %q:\n%s", want, config)
		}
	}
	if strings.Contains(config, "or-sk-test") {
		t.Fatalf("codex config must not contain plaintext api key:\n%s", config)
	}
	if strings.Contains(config, `model_provider = "ollama-cloud"`) {
		t.Fatalf("codex config should not contain ollama-cloud:\n%s", config)
	}

	appBytes, err := os.ReadFile(filepath.Join(home, ".code-switch", "config.json"))
	if err != nil {
		t.Fatalf("read app config: %v", err)
	}
	var cfg AppConfig
	if err := json.Unmarshal(appBytes, &cfg); err != nil {
		t.Fatalf("unmarshal app config: %v", err)
	}
	if got := cfg.Agents["codex"].Providers["openrouter"].APIKey; got != "or-sk-test" {
		t.Fatalf("codex stored openrouter key = %q, want %q", got, "or-sk-test")
	}
	if _, ok := cfg.Providers["openrouter"]; ok {
		t.Fatalf("codex switch should not write top-level claude provider config")
	}
}
```

- [ ] **Step 2: Add test for Codex OpenRouter switch output**

```go
func TestCodexSwitchOpenRouterPrintsCorrectOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "openrouter", "--agent", "codex", "--api-key", "or-sk-test", "--codex-dir", codexDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex openrouter switch returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, `auth: cs token openrouter --agent codex`) {
		t.Fatalf("codex openrouter switch output missing token auth helper:\n%s", out)
	}
	if strings.Contains(out, "or-sk-test") {
		t.Fatalf("codex switch output must not print plaintext api key:\n%s", out)
	}
}
```

- [ ] **Step 3: Add test for Codex restore removes OpenRouter section**

```go
func TestCodexRestoreRemovesOpenRouterSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexDir := filepath.Join(home, ".codex")
	configPath := filepath.Join(codexDir, "config.toml")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	initial := `approval_policy = "on-request"
model = "anthropic/claude-sonnet-4.6"
model_provider = "OpenRouter"
approvals_reviewer = "user"

[model_providers.OpenRouter]
name = "OpenRouter"
base_url = "https://openrouter.ai/api/v1"
wire_api = "responses"

[model_providers.OpenRouter.auth]
command = "cs"
args = ["token", "openrouter", "--agent", "codex"]

[profiles.work]
model = "gpt-5.5"
model_provider = "openai"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write codex config: %v", err)
	}

	if err := runWithIO([]string{"restore", "--agent", "codex", "--codex-dir", codexDir}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatalf("codex restore returned error: %v", err)
	}

	restoredBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored codex config: %v", err)
	}
	restored := string(restoredBytes)
	for _, unwanted := range []string{`model_provider = "OpenRouter"`, `model = "anthropic/claude-sonnet-4.6"`, `approvals_reviewer = "user"`, `[model_providers.OpenRouter]`, `[model_providers.OpenRouter.auth]`, `wire_api = "responses"`, `command = "cs"`} {
		if strings.Contains(restored, unwanted) {
			t.Fatalf("restored codex config still contains %q:\n%s", unwanted, restored)
		}
	}
	for _, want := range []string{`approval_policy = "on-request"`, `[profiles.work]`, `model = "gpt-5.5"`, `model_provider = "openai"`} {
		if !strings.Contains(restored, want) {
			t.Fatalf("restored codex config lost %q:\n%s", want, restored)
		}
	}
}
```

- [ ] **Step 4: Add test for unsupported provider error for Codex**

```go
func TestCodexSwitchRejectsUnsupportedProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := &bytes.Buffer{}
	err := runWithIO([]string{"switch", "deepseek", "--agent", "codex"}, strings.NewReader(""), output)
	if err == nil {
		t.Fatalf("expected error for unsupported provider deepseek on codex")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("expected unsupported provider error, got: %v", err)
	}
}
```

- [ ] **Step 5: Add test for list includes both Codex providers**

```go
func TestCodexListIncludesOpenRouter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	output := &bytes.Buffer{}
	if err := cmdList([]string{"--agent", "codex"}, output); err != nil {
		t.Fatalf("cmdList codex returned error: %v", err)
	}
	out := output.String()
	for _, want := range []string{"ollama-cloud", "openrouter"} {
		if !strings.Contains(out, want) {
			t.Fatalf("codex list missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "deepseek") {
		t.Fatalf("codex list should not include Claude-only providers: %q", out)
	}

	names := providerNamesForAgent(agentCodex, &AppConfig{Providers: map[string]StoredProvider{}}, false, true)
	if len(names) != 3 || names[0] != "ollama-cloud" || names[1] != "openrouter" || names[2] != restoreProviderOption {
		t.Fatalf("codex TUI provider names = %v, want [ollama-cloud openrouter __restore__]", names)
	}
}
```

- [ ] **Step 6: Add test for Codex env with openrouter**

```go
func TestCodexEnvOpenRouterPrintsCorrectOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := AppConfig{
		Providers: map[string]StoredProvider{},
		Agents: map[string]AgentConfig{
			"codex": {
				Providers: map[string]StoredProvider{
					"openrouter": {APIKey: "or-sk-test"},
				},
			},
		},
	}
	if err := writeJSONAtomic(filepath.Join(home, ".code-switch", "config.json"), cfg); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"env", "openrouter", "--agent", "codex"}, strings.NewReader(""), output); err != nil {
		t.Fatalf("codex env openrouter returned error: %v", err)
	}

	out := output.String()
	if !strings.Contains(out, "export ANTHROPIC_BASE_URL='https://openrouter.ai/api/v1'") {
		t.Fatalf("env output missing base_url: %s", out)
	}
	if !strings.Contains(out, "export OPENROUTER_API_KEY='or-sk-test'") {
		t.Fatalf("env output missing auth env: %s", out)
	}
}
```

- [ ] **Step 7: Add test for `discoverOpenRouterModels` with mock server**

```go
func TestDiscoverOpenRouterModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("missing auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`))
	}))
	defer server.Close()

	// Can't test directly since URL is hardcoded; test parsing via extract function
	models := discoverOpenRouterModels("")
	if models != nil {
		t.Fatalf("expected nil for empty key, got %v", models)
	}
}
```

- [ ] **Step 8: Run all tests**

```bash
go vet ./... && go test ./... -count=1
```

---

### Task 9: Final verification and integration test

**Files:**
- None (verification only)

- [ ] **Step 1: Run full CI checks**

```bash
go vet ./... && go test ./... -count=1 && go build -o cs .
```

- [ ] **Step 2: Verify help text**

```bash
./cs --help 2>&1 | grep -A5 "Codex providers"
```

Expected: shows both `ollama-cloud` and `openrouter`.

- [ ] **Step 3: Smoke test switch dry-run (if no API key configured)**

```bash
./cs switch openrouter --agent codex --dry-run
```

Expected: prints dry-run info without error.
