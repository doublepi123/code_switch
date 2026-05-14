# Per-Tier Model Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow users to configure Haiku/Sonnet/Opus/Subagent model tiers independently per provider, with smart preset defaults as fallback.

**Architecture:** Extend `StoredProvider` and `ConfigureSelection` with optional tier fields. Apply stored tier overrides in `resolveSwitchPreset` after the preset's own tier mapping. Add TUI "Edit Tiers" page and CLI `--haiku/--sonnet/--opus/--subagent` flags. Show tier info in `current` output.

**Tech Stack:** Go, tview TUI library

---

### Task 1: Add tier fields to StoredProvider and ConfigureSelection

**Files:**
- Modify: `presets.go:148-174`

- [ ] **Step 1: Add tier fields to StoredProvider**

In `presets.go`, extend `StoredProvider` (line 148):

```go
type StoredProvider struct {
	Name     string `json:"name,omitempty"`
	BaseURL  string `json:"baseUrl,omitempty"`
	Model    string `json:"model,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
	AuthEnv  string `json:"authEnv,omitempty"`
	Haiku    string `json:"haiku,omitempty"`
	Sonnet   string `json:"sonnet,omitempty"`
	Opus     string `json:"opus,omitempty"`
	Subagent string `json:"subagent,omitempty"`
}
```

- [ ] **Step 2: Add tier fields to ConfigureSelection**

In `presets.go`, extend `ConfigureSelection` (line 165):

```go
type ConfigureSelection struct {
	Agent    string
	Provider string
	Model    string
	ResetKey bool
	APIKey   string
	Name     string
	BaseURL  string
	AuthEnv  string
	Haiku    string
	Sonnet   string
	Opus     string
	Subagent string
}
```

- [ ] **Step 3: Build and verify compilation**

Run: `go build -o cs .`
Expected: compiles without errors (existing code doesn't reference the new fields yet, so `omitempty` ensures backwards compatibility)

- [ ] **Step 4: Commit**

```bash
git add presets.go
git commit -m "feat: add tier fields to StoredProvider and ConfigureSelection"
```

---

### Task 2: Apply stored tier overrides in resolveSwitchPreset

**Files:**
- Modify: `presets.go:466-483`
- Test: `main_test.go`

- [ ] **Step 1: Write failing test for stored tier override**

Add to `main_test.go`:

```go
func TestResolveSwitchPresetStoredTierOverrides(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {
				Model:    "anthropic/claude-opus-4.7",
				Haiku:    "anthropic/claude-haiku-4.5-custom",
				Sonnet:   "anthropic/claude-sonnet-4.6-custom",
				Opus:     "anthropic/claude-opus-4.7-custom",
				Subagent: "anthropic/claude-haiku-4.5-custom",
			},
		},
	}

	preset, err := resolveSwitchPreset("openrouter", cfg, "")
	if err != nil {
		t.Fatalf("resolveSwitchPreset returned error: %v", err)
	}
	if got := preset.Haiku; got != "anthropic/claude-haiku-4.5-custom" {
		t.Fatalf("haiku = %v, want %v", got, "anthropic/claude-haiku-4.5-custom")
	}
	if got := preset.Sonnet; got != "anthropic/claude-sonnet-4.6-custom" {
		t.Fatalf("sonnet = %v, want %v", got, "anthropic/claude-sonnet-4.6-custom")
	}
	if got := preset.Opus; got != "anthropic/claude-opus-4.7-custom" {
		t.Fatalf("opus = %v, want %v", got, "anthropic/claude-opus-4.7-custom")
	}
	if got := preset.Subagent; got != "anthropic/claude-haiku-4.5-custom" {
		t.Fatalf("subagent = %v, want %v", got, "anthropic/claude-haiku-4.5-custom")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestResolveSwitchPresetStoredTierOverrides -v`
Expected: FAIL — stored tier overrides are not applied yet, preset defaults still show

- [ ] **Step 3: Write failing test for partial tier override**

Add to `main_test.go`:

```go
func TestResolveSwitchPresetPartialTierOverride(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {
				Model:  "anthropic/claude-opus-4.7",
				Haiku:  "anthropic/claude-haiku-4.5-custom",
				Sonnet: "", // no override, should use preset default
			},
		},
	}

	preset, err := resolveSwitchPreset("openrouter", cfg, "")
	if err != nil {
		t.Fatalf("resolveSwitchPreset returned error: %v", err)
	}
	if got := preset.Haiku; got != "anthropic/claude-haiku-4.5-custom" {
		t.Fatalf("haiku = %v, want %v", got, "anthropic/claude-haiku-4.5-custom")
	}
	if got := preset.Sonnet; got != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("sonnet = %v, want %v (preset default)", got, "anthropic/claude-sonnet-4.6")
	}
	if got := preset.Opus; got != "anthropic/claude-opus-4.7" {
		t.Fatalf("opus = %v, want %v (preset default)", got, "anthropic/claude-opus-4.7")
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `go test -run TestResolveSwitchPresetPartialTierOverride -v`
Expected: FAIL

- [ ] **Step 5: Implement applyStoredTierOverrides**

Add helper function in `presets.go` after `withOverrideTiers` (after line 553):

```go
func applyStoredTierOverrides(preset *ProviderPreset, stored StoredProvider) {
	if v := strings.TrimSpace(stored.Haiku); v != "" {
		preset.Haiku = v
	}
	if v := strings.TrimSpace(stored.Sonnet); v != "" {
		preset.Sonnet = v
	}
	if v := strings.TrimSpace(stored.Opus); v != "" {
		preset.Opus = v
	}
	if v := strings.TrimSpace(stored.Subagent); v != "" {
		preset.Subagent = v
	}
}
```

- [ ] **Step 6: Call applyStoredTierOverrides in resolveSwitchPreset**

Modify `resolveSwitchPreset` in `presets.go` (line 466-483):

```go
func resolveSwitchPreset(provider string, cfg *AppConfig, modelOverride string) (ProviderPreset, error) {
	if preset, ok := providerPresets[provider]; ok {
		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(cfg.Providers[provider].Model)
		}
		if err := validateProviderModel(provider, model); err != nil {
			return ProviderPreset{}, err
		}
		preset = withSelectedModel(preset, model)
		applyStoredTierOverrides(&preset, cfg.Providers[provider])
		return preset, nil
	}

	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return ProviderPreset{}, err
	}
	preset = withSelectedModel(preset, modelOverride)
	applyStoredTierOverrides(&preset, cfg.Providers[provider])
	return preset, nil
}
```

- [ ] **Step 7: Run both tests to verify they pass**

Run: `go test -run "TestResolveSwitchPresetStoredTierOverrides|TestResolveSwitchPresetPartialTierOverride" -v`
Expected: PASS

- [ ] **Step 8: Run all existing tests to verify no regressions**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 9: Commit**

```bash
git add presets.go main_test.go
git commit -m "feat: apply stored tier overrides in resolveSwitchPreset"
```

---

### Task 3: Persist tier overrides in upsertProviderConfig

**Files:**
- Modify: `config.go:230-246`
- Test: `main_test.go`

- [ ] **Step 1: Write failing test for tier persistence**

Add to `main_test.go`:

```go
func TestUpsertProviderConfigPersistsTierOverrides(t *testing.T) {
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	selection := ConfigureSelection{
		Agent:    string(agentClaude),
		Provider: "openrouter",
		Model:    "anthropic/claude-opus-4.7",
		Haiku:    "anthropic/claude-haiku-4.5-custom",
		Sonnet:   "anthropic/claude-sonnet-4.6-custom",
		Opus:     "anthropic/claude-opus-4.7-custom",
		Subagent: "anthropic/claude-haiku-4.5-custom",
	}
	upsertProviderConfig(cfg, selection, "sk-test")

	stored := cfg.Providers["openrouter"]
	if got := stored.Haiku; got != "anthropic/claude-haiku-4.5-custom" {
		t.Fatalf("haiku = %v, want %v", got, "anthropic/claude-haiku-4.5-custom")
	}
	if got := stored.Sonnet; got != "anthropic/claude-sonnet-4.6-custom" {
		t.Fatalf("sonnet = %v, want %v", got, "anthropic/claude-sonnet-4.6-custom")
	}
	if got := stored.Opus; got != "anthropic/claude-opus-4.7-custom" {
		t.Fatalf("opus = %v, want %v", got, "anthropic/claude-opus-4.7-custom")
	}
	if got := stored.Subagent; got != "anthropic/claude-haiku-4.5-custom" {
		t.Fatalf("subagent = %v, want %v", got, "anthropic/claude-haiku-4.5-custom")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestUpsertProviderConfigPersistsTierOverrides -v`
Expected: FAIL

- [ ] **Step 3: Update upsertProviderConfig to persist tier fields**

Modify `upsertProviderConfig` in `config.go` (line 230-246):

```go
func upsertProviderConfig(cfg *AppConfig, selection ConfigureSelection, apiKey string) {
	if selection.Agent == string(agentCodex) {
		upsertAgentProviderConfig(cfg, agentCodex, selection, apiKey)
		return
	}
	stored := cfg.Providers[selection.Provider]
	stored.APIKey = apiKey
	stored.Model = strings.TrimSpace(selection.Model)
	stored.AuthEnv = strings.TrimSpace(selection.AuthEnv)
	stored.Haiku = strings.TrimSpace(selection.Haiku)
	stored.Sonnet = strings.TrimSpace(selection.Sonnet)
	stored.Opus = strings.TrimSpace(selection.Opus)
	stored.Subagent = strings.TrimSpace(selection.Subagent)
	if selection.Name != "" {
		stored.Name = strings.TrimSpace(selection.Name)
	}
	if selection.BaseURL != "" {
		stored.BaseURL = strings.TrimSpace(selection.BaseURL)
	}
	cfg.Providers[selection.Provider] = stored
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestUpsertProviderConfigPersistsTierOverrides -v`
Expected: PASS

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 6: Commit**

```bash
git add config.go main_test.go
git commit -m "feat: persist tier overrides in upsertProviderConfig"
```

---

### Task 4: Add tier flags to switch and configure commands

**Files:**
- Modify: `switch.go:82-126`
- Modify: `tui.go:17-29` (cmdConfigure flag parsing)
- Test: `main_test.go`

- [ ] **Step 1: Write failing test for switch with tier flags**

Add to `main_test.go`:

```go
func TestSwitchWithTierFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	output := &bytes.Buffer{}
	if err := runWithIO([]string{"switch", "openrouter", "--model", "anthropic/claude-opus-4.7", "--haiku", "anthropic/claude-haiku-4.5", "--sonnet", "anthropic/claude-sonnet-4.6", "--api-key", "sk-test", "--claude-dir", claudeDir}, strings.NewReader(""), output); err != nil {
		t.Fatalf("runWithIO returned error: %v", err)
	}

	settingsBytes, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	env := settings["env"].(map[string]any)
	if got := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; got != "anthropic/claude-haiku-4.5" {
		t.Fatalf("haiku = %v, want anthropic/claude-haiku-4.5", got)
	}
	if got := env["ANTHROPIC_DEFAULT_SONNET_MODEL"]; got != "anthropic/claude-sonnet-4.6" {
		t.Fatalf("sonnet = %v, want anthropic/claude-sonnet-4.6", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSwitchWithTierFlags -v`
Expected: FAIL — `--haiku` and `--sonnet` flags don't exist yet

- [ ] **Step 3: Add tier flags to cmdSwitchWithOutput**

Modify `cmdSwitchWithOutput` in `switch.go`. Add flag declarations after the `model` flag (around line 89):

```go
haiku := fs.String("haiku", "", "override haiku tier model")
sonnet := fs.String("sonnet", "", "override sonnet tier model")
opus := fs.String("opus", "", "override opus tier model")
subagent := fs.String("subagent", "", "override subagent tier model")
```

Update the call to `switchProvider` (line 125) to pass tier overrides:

```go
return switchProvider(pa.Provider, cfg, pa.APIKey, pa.Model, *claudeDir, out, *dryRun,
	withTierOverrides{Haiku: *haiku, Sonnet: *sonnet, Opus: *opus, Subagent: *subagent})
```

- [ ] **Step 4: Add withTierOverrides type and update switchProvider**

Add to `switch.go`:

```go
type withTierOverrides struct {
	Haiku    string
	Sonnet   string
	Opus     string
	Subagent string
}
```

Change `switchProvider` signature to accept tier overrides:

```go
func switchProvider(provider string, cfg *AppConfig, apiKey, modelOverride, claudeDir string, out io.Writer, dryRun bool, tierOverrides ...withTierOverrides) error {
```

At the end of `switchProvider`, before calling `applyPreset`, apply any CLI tier overrides to the preset:

```go
	if len(tierOverrides) > 0 {
		o := tierOverrides[0]
		if v := strings.TrimSpace(o.Haiku); v != "" {
			preset.Haiku = v
		}
		if v := strings.TrimSpace(o.Sonnet); v != "" {
			preset.Sonnet = v
		}
		if v := strings.TrimSpace(o.Opus); v != "" {
			preset.Opus = v
		}
		if v := strings.TrimSpace(o.Subagent); v != "" {
			preset.Subagent = v
		}
	}
```

Also store the tier overrides in config for persistence. After `switchProvider` resolves the preset and applies CLI overrides, store them:

```go
	// Persist tier overrides from CLI flags
	if len(tierOverrides) > 0 {
		o := tierOverrides[0]
		stored := cfg.Providers[provider]
		if v := strings.TrimSpace(o.Haiku); v != "" {
			stored.Haiku = v
		}
		if v := strings.TrimSpace(o.Sonnet); v != "" {
			stored.Sonnet = v
		}
		if v := strings.TrimSpace(o.Opus); v != "" {
			stored.Opus = v
		}
		if v := strings.TrimSpace(o.Subagent); v != "" {
			stored.Subagent = v
		}
		cfg.Providers[provider] = stored
	}
```

Wait — `switchProvider` doesn't have write access to `configPath`. The config is saved in `cmdSwitchWithOutput` after `switchProvider` returns. So we need to store the tier overrides on the `providerArgs` struct or handle it in `cmdSwitchWithOutput`.

Better approach: add tier fields to `providerArgs`, set them in `cmdSwitchWithOutput`, and persist them before the config write.

- [ ] **Step 5: Add tier fields to providerArgs**

In `switch.go`, extend `providerArgs` (line 12):

```go
type providerArgs struct {
	Agent     AgentName
	Provider  string
	APIKey    string
	Model     string
	ClaudeDir string
	CodexDir  string
	DryRun    bool
	Haiku     string
	Sonnet    string
	Opus      string
	Subagent  string
}
```

- [ ] **Step 6: Set tier fields from flags in cmdSwitchWithOutput**

In `cmdSwitchWithOutput`, after resolving `pa`, set tier fields:

```go
	pa.Haiku = strings.TrimSpace(*haiku)
	pa.Sonnet = strings.TrimSpace(*sonnet)
	pa.Opus = strings.TrimSpace(*opus)
	pa.Subagent = strings.TrimSpace(*subagent)
```

Then, before `switchProvider` call, store tier overrides in config and pass them:

```go
	// Persist CLI tier overrides to config
	if pa.Haiku != "" || pa.Sonnet != "" || pa.Opus != "" || pa.Subagent != "" {
		stored := cfg.Providers[pa.Provider]
		if pa.Haiku != "" {
			stored.Haiku = pa.Haiku
		}
		if pa.Sonnet != "" {
			stored.Sonnet = pa.Sonnet
		}
		if pa.Opus != "" {
			stored.Opus = pa.Opus
		}
		if pa.Subagent != "" {
			stored.Subagent = pa.Subagent
		}
		cfg.Providers[pa.Provider] = stored
	}
```

Pass tier overrides to `switchProvider`:

```go
	return switchProvider(pa.Provider, cfg, pa.APIKey, pa.Model, *claudeDir, out, *dryRun, withTierOverrides{Haiku: pa.Haiku, Sonnet: pa.Sonnet, Opus: pa.Opus, Subagent: pa.Subagent})
```

- [ ] **Step 7: Update switchProvider to accept optional tier overrides**

Update `switchProvider` signature:

```go
func switchProvider(provider string, cfg *AppConfig, apiKey, modelOverride, claudeDir string, out io.Writer, dryRun bool, tierOverrides ...withTierOverrides) error {
```

After `preset, err := resolveSwitchPreset(...)` (line 159), add:

```go
	if len(tierOverrides) > 0 {
		o := tierOverrides[0]
		if v := strings.TrimSpace(o.Haiku); v != "" {
			preset.Haiku = v
		}
		if v := strings.TrimSpace(o.Sonnet); v != "" {
			preset.Sonnet = v
		}
		if v := strings.TrimSpace(o.Opus); v != "" {
			preset.Opus = v
		}
		if v := strings.TrimSpace(o.Subagent); v != "" {
			preset.Subagent = v
		}
	}
```

- [ ] **Step 8: Update all callers of switchProvider that don't pass tier overrides**

Find all callers of `switchProvider` and make sure they compile with the variadic parameter. Since it's variadic, existing calls without tier overrides will still compile. Verify:

Run: `go build -o cs .`
Expected: compiles

- [ ] **Step 9: Update usage text in printUsage**

In `main.go`, update the switch usage line (around line 449) to show tier flags:

```
  cs switch <provider> [--agent claude|codex] [--api-key sk-xxx] [--model model-id] [--haiku model] [--sonnet model] [--opus model] [--subagent model] [--claude-dir DIR] [--codex-dir DIR] [--dry-run]
```

- [ ] **Step 10: Run the failing test**

Run: `go test -run TestSwitchWithTierFlags -v`
Expected: PASS

- [ ] **Step 11: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 12: Commit**

```bash
git add switch.go main.go main_test.go
git commit -m "feat: add --haiku/--sonnet/--opus/--subagent flags to switch command"
```

---

### Task 5: Show tier models in cmdCurrent output

**Files:**
- Modify: `main.go:137-204`
- Test: `main_test.go`

- [ ] **Step 1: Write failing test for current with tier models**

Add to `main_test.go`:

```go
func TestCmdCurrentShowsTierModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")

	settings := map[string]any{
		"env": map[string]any{
			"ANTHROPIC_BASE_URL":            "https://openrouter.ai/api",
			"ANTHROPIC_MODEL":               "anthropic/claude-opus-4.7",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "anthropic/claude-haiku-4.5",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "anthropic/claude-sonnet-4.6",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":  "anthropic/claude-opus-4.7",
		},
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := writeJSONAtomic(settingsPath, settings); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	output := &bytes.Buffer{}
	if err := cmdCurrent([]string{"--claude-dir", claudeDir}, output); err != nil {
		t.Fatalf("cmdCurrent returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "haiku:") {
		t.Fatalf("expected haiku tier in output, got %q", out)
	}
	if !strings.Contains(out, "sonnet:") {
		t.Fatalf("expected sonnet tier in output, got %q", out)
	}
	if !strings.Contains(out, "opus:") {
		t.Fatalf("expected opus tier in output, got %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCmdCurrentShowsTierModels -v`
Expected: FAIL

- [ ] **Step 3: Add tier model display to cmdCurrent**

In `main.go`, in `cmdCurrent` (around line 175, after the `model` display), add:

```go
				haikuModel, _ := env["ANTHROPIC_DEFAULT_HAIKU_MODEL"].(string)
				sonnetModel, _ := env["ANTHROPIC_DEFAULT_SONNET_MODEL"].(string)
				opusModel, _ := env["ANTHROPIC_DEFAULT_OPUS_MODEL"].(string)
				if haikuModel != "" {
					fmt.Fprintf(out, "  %s\n", formatLabel("haiku", haikuModel))
				}
				if sonnetModel != "" {
					fmt.Fprintf(out, "  %s\n", formatLabel("sonnet", sonnetModel))
				}
				if opusModel != "" {
					fmt.Fprintf(out, "  %s\n", formatLabel("opus", opusModel))
				}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestCmdCurrentShowsTierModels -v`
Expected: PASS

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: show haiku/sonnet/opus tier models in current output"
```

---

### Task 6: Add TUI Edit Tiers page

**Files:**
- Modify: `tui.go`

- [ ] **Step 1: Add tierOverrides field to tuiState**

In `tui.go`, add to `tuiState` struct (around line 144):

```go
	tierOverrides map[string]StoredProvider
```

Initialize in `runArrowTUI` (around line 721):

```go
		tierOverrides: map[string]StoredProvider{},
```

- [ ] **Step 2: Add showTierConfig method**

Add new method to `tuiState` in `tui.go`:

```go
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

	// Get current effective tier values
	override := ts.tierOverrides[provider]
	stored := ts.cfg.Providers[provider]

	haikuDefault := firstNonEmpty(strings.TrimSpace(override.Haiku), strings.TrimSpace(stored.Haiku), preset.Haiku)
	sonnetDefault := firstNonEmpty(strings.TrimSpace(override.Sonnet), strings.TrimSpace(stored.Sonnet), preset.Sonnet)
	opusDefault := firstNonEmpty(strings.TrimSpace(override.Opus), strings.TrimSpace(stored.Opus), preset.Opus)
	subagentDefault := firstNonEmpty(strings.TrimSpace(override.Subagent), strings.TrimSpace(stored.Subagent), preset.Subagent)

	var haikuVal, sonnetVal, opusVal, subagentVal string

	form := tview.NewForm()

	// Helper to find index in modelOptions for a value, default to 0 ("")
	modelIndexFor := func(val string) int {
		for i, m := range modelOptions {
			if m == val {
				return i
			}
		}
		return 0
	}

	form.AddDropDown("Haiku", modelOptions, modelIndexFor(haikuDefault), func(option string, idx int) {
		if idx == 0 {
			haikuVal = ""
		} else {
			haikuVal = option
		}
	})
	form.AddDropDown("Sonnet", modelOptions, modelIndexFor(sonnetDefault), func(option string, idx int) {
		if idx == 0 {
			sonnetVal = ""
		} else {
			sonnetVal = option
		}
	})
	form.AddDropDown("Opus", modelOptions, modelIndexFor(opusDefault), func(option string, idx int) {
		if idx == 0 {
			opusVal = ""
		} else {
			opusVal = option
		}
	})
	form.AddDropDown("Subagent", modelOptions, modelIndexFor(subagentDefault), func(option string, idx int) {
		if idx == 0 {
			subagentVal = ""
		} else {
			subagentVal = option
		}
	})

	errLabel := tview.NewTextView()
	errLabel.SetTextColor(tcell.ColorRed)

	form.AddButton("Save", func() {
		ov := ts.tierOverrides[provider]
		ov.Haiku = haikuVal
		ov.Sonnet = sonnetVal
		ov.Opus = opusVal
		ov.Subagent = subagentVal
		ts.tierOverrides[provider] = ov
		ts.showDetail(provider, backPage)
	})
	form.AddButton("Cancel", func() {
		ts.showDetail(provider, backPage)
	})
	form.SetBorder(true)
	form.SetTitle(" Edit Tier Models ")
	form.SetButtonsAlign(tview.AlignLeft)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			ts.showDetail(provider, backPage)
			return nil
		}
		return event
	})

	help := tview.NewTextView()
	help.SetText(fmt.Sprintf("Provider: %s  |  Leave empty to use preset default  |  Esc cancel", providerTitle(provider, ts.cfg)))

	page := tview.NewFlex()
	page.SetDirection(tview.FlexRow)
	page.AddItem(help, 1, 0, false)
	page.AddItem(errLabel, 1, 0, false)
	page.AddItem(form, 0, 1, true)
	ts.pages.AddAndSwitchToPage("tier-config", page, true)
	ts.app.SetFocus(form)
}
```

- [ ] **Step 3: Add "Edit Tiers" action to detail page**

In `tui.go`, in `showDetail` method, add the "Edit Tiers" item to the actions list. Add after the "Edit API Key" item (around line 287):

```go
		actions.AddItem("Edit Tiers", "", 't', func() {
			ts.showTierConfig(provider, backPage)
		})
```

Also add 't' keybinding to the `SetInputCapture` of actions (add before the Back/quit case):

```go
		case event.Rune() == 't' || event.Rune() == 'T':
			ts.showTierConfig(provider, backPage)
			return nil
```

- [ ] **Step 4: Apply tier overrides in finishSelection**

In `tui.go`, modify `finishSelection` to include tier overrides:

```go
func (ts *tuiState) finishSelection(provider, model string) {
	ov := ts.tierOverrides[provider]
	ts.result = ConfigureSelection{
		Agent:    string(ts.agent),
		Provider: provider,
		Model:    model,
		ResetKey: ts.resetKeys[provider],
		APIKey:   strings.TrimSpace(ts.typedAPIKeys[provider]),
		Haiku:    ov.Haiku,
		Sonnet:   ov.Sonnet,
		Opus:     ov.Opus,
		Subagent: ov.Subagent,
	}
	ts.resultErr = nil
	ts.app.Stop()
}
```

- [ ] **Step 5: Build and verify compilation**

Run: `go build -o cs .`
Expected: compiles without errors

- [ ] **Step 6: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 7: Commit**

```bash
git add tui.go
git commit -m "feat: add TUI Edit Tiers page with dropdown model selection"
```

---

### Task 7: Update cmdEnv to output tier env vars

**Files:**
- Modify: `env.go:60-77`
- Test: `main_test.go`

- [ ] **Step 1: Write failing test for env with tier vars**

Add to `main_test.go`:

```go
func TestCmdEnvShowsTierModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".code-switch")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"openrouter": {APIKey: "sk-test"},
		},
	}
	cfgData, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(configDir, "config.json"), cfgData, 0o600)

	output := &bytes.Buffer{}
	if err := cmdEnv([]string{"openrouter"}, output); err != nil {
		t.Fatalf("cmdEnv returned error: %v", err)
	}
	out := output.String()
	if !strings.Contains(out, "ANTHROPIC_DEFAULT_HAIKU_MODEL") {
		t.Fatalf("expected ANTHROPIC_DEFAULT_HAIKU_MODEL in env output, got %q", out)
	}
	if !strings.Contains(out, "ANTHROPIC_DEFAULT_SONNET_MODEL") {
		t.Fatalf("expected ANTHROPIC_DEFAULT_SONNET_MODEL in env output, got %q", out)
	}
	if !strings.Contains(out, "ANTHROPIC_DEFAULT_OPUS_MODEL") {
		t.Fatalf("expected ANTHROPIC_DEFAULT_OPUS_MODEL in env output, got %q", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCmdEnvShowsTierModels -v`
Expected: FAIL

- [ ] **Step 3: Add tier env var output to cmdEnv**

In `env.go`, in the Claude agent branch (after line 66, after the `ANTHROPIC_MODEL` export), add:

```go
	if preset.Haiku != "" {
		fmt.Fprintf(out, "export ANTHROPIC_DEFAULT_HAIKU_MODEL=%s\n", shellSingleQuote(preset.Haiku))
	}
	if preset.Sonnet != "" {
		fmt.Fprintf(out, "export ANTHROPIC_DEFAULT_SONNET_MODEL=%s\n", shellSingleQuote(preset.Sonnet))
	}
	if preset.Opus != "" {
		fmt.Fprintf(out, "export ANTHROPIC_DEFAULT_OPUS_MODEL=%s\n", shellSingleQuote(preset.Opus))
	}
	if preset.Subagent != "" {
		fmt.Fprintf(out, "export CLAUDE_CODE_SUBAGENT_MODEL=%s\n", shellSingleQuote(preset.Subagent))
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestCmdEnvShowsTierModels -v`
Expected: PASS

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 6: Commit**

```bash
git add env.go main_test.go
git commit -m "feat: output tier model env vars in env command"
```

---

### Task 8: End-to-end verification

**Files:**
- No new files

- [ ] **Step 1: Build binary**

Run: `go build -o cs .`
Expected: compiles without errors

- [ ] **Step 2: Run all tests**

Run: `go test ./...`
Expected: all tests pass

- [ ] **Step 3: Test switch with tier flags**

Run: `cs switch openrouter --model anthropic/claude-opus-4.7 --haiku anthropic/claude-haiku-4.5 --sonnet anthropic/claude-sonnet-4.6 --api-key sk-test --dry-run`
Expected: shows dry-run output with correct model and tiers

- [ ] **Step 4: Test current output shows tiers**

Run: `cs current`
Expected: shows haiku/sonnet/opus lines if tier env vars are set

- [ ] **Step 5: Test env output shows tiers**

Run: `cs env openrouter`
Expected: exports ANTHROPIC_DEFAULT_HAIKU_MODEL, ANTHROPIC_DEFAULT_SONNET_MODEL, ANTHROPIC_DEFAULT_OPUS_MODEL

- [ ] **Step 6: Final commit if any fixups needed**

```bash
git add -A
git commit -m "fix: minor fixups from end-to-end verification"
```
