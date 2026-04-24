package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProviderPreset struct {
	Name      string
	BaseURL   string
	Model     string
	Models    []string
	Haiku     string
	Sonnet    string
	Opus      string
	ExtraEnv  map[string]any
	Website   string
	APIKeyURL string
}

type StoredProvider struct {
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	Model   string `json:"model,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
}

type AppConfig struct {
	Providers map[string]StoredProvider `json:"providers"`
}

type ConfigureSelection struct {
	Provider string
	Model    string
	ResetKey bool
	APIKey   string
	Name     string
	BaseURL  string
}

type tuiPage int

const (
	tuiPageProviders tuiPage = iota
	tuiPageProviderDetail
	tuiPageModels
)

var providerPresets = map[string]ProviderPreset{
	"minimax-cn": {
		Name:      "MiniMax CN Token Plan",
		BaseURL:   "https://api.minimaxi.com/anthropic",
		Model:     "MiniMax-M2.7",
		Models:    []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"},
		Haiku:     "MiniMax-M2.7",
		Sonnet:    "MiniMax-M2.7",
		Opus:      "MiniMax-M2.7",
		Website:   "https://platform.minimaxi.com",
		APIKeyURL: "https://platform.minimaxi.com/docs/token-plan/claude-code",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": 1,
		},
	},
	"minimax-global": {
		Name:      "MiniMax Global Token Plan",
		BaseURL:   "https://api.minimax.io/anthropic",
		Model:     "MiniMax-M2.7",
		Models:    []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"},
		Haiku:     "MiniMax-M2.7",
		Sonnet:    "MiniMax-M2.7",
		Opus:      "MiniMax-M2.7",
		Website:   "https://platform.minimax.io",
		APIKeyURL: "https://platform.minimax.io/docs/token-plan/claude-code",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": 1,
		},
	},
	"openrouter": {
		Name:      "OpenRouter",
		BaseURL:   "https://openrouter.ai/api",
		Model:     "anthropic/claude-sonnet-4.6",
		Models:    []string{"anthropic/claude-sonnet-4.6", "anthropic/claude-haiku-4.5", "anthropic/claude-opus-4.7"},
		Haiku:     "anthropic/claude-haiku-4.5",
		Sonnet:    "anthropic/claude-sonnet-4.6",
		Opus:      "anthropic/claude-opus-4.7",
		Website:   "https://openrouter.ai",
		APIKeyURL: "https://openrouter.ai/keys",
	},
	"opencode-go": {
		Name:      "OpenCode Go",
		BaseURL:   "https://opencode.ai/zen/go",
		Model:     "minimax-m2.7",
		Models:    []string{"minimax-m2.7", "minimax-m2.5"},
		Haiku:     "minimax-m2.7",
		Sonnet:    "minimax-m2.7",
		Opus:      "minimax-m2.7",
		Website:   "https://opencode.ai/docs/go/",
		APIKeyURL: "https://opencode.ai",
	},
}

var providerAliases = map[string]string{
	"minimax":             "minimax-cn",
	"minimax-cn-token":    "minimax-cn",
	"minimax-global-token": "minimax-global",
}

const customProviderOption = "__custom__"

var managedEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"API_TIMEOUT_MS",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return cmdConfigure(nil, os.Stdin, os.Stdout)
	}

	switch args[0] {
	case "list":
		return cmdList()
	case "configure":
		return cmdConfigure(args[1:], os.Stdin, os.Stdout)
	case "current":
		return cmdCurrent(args[1:])
	case "set-key":
		return cmdSetKey(args[1:])
	case "switch":
		return cmdSwitch(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func cmdList() error {
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	names := sortedProviderNames(cfg, false)
	for _, name := range names {
		preset, err := resolveProviderPreset(name, cfg)
		if err != nil {
			return err
		}
		fmt.Printf("%s\t%s\t%s\n", name, preset.BaseURL, preset.Model)
	}
	return nil
}

func cmdConfigure(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	resetKey := fs.Bool("reset-key", false, "force re-enter api key for the selected provider")
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
		selection, err = runArrowTUI(file, out, cfg, currentProvider, currentModel)
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
	if err := writeJSONAtomic(configPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved provider config for %s in %s\n", provider, configPath)

	if err := switchProvider(provider, cfg, apiKey, selection.Model, *claudeDir); err != nil {
		return err
	}
	return nil
}

func cmdCurrent(args []string) error {
	fs := flag.NewFlagSet("current", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	if err := fs.Parse(args); err != nil {
		return err
	}

	settingsPath := claudeSettingsPath(*claudeDir)
	root, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}

	env := nestedMap(root, "env")
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)

	fmt.Printf("settings: %s\n", settingsPath)
	if baseURL == "" {
		fmt.Println("provider: unknown")
		return nil
	}

	fmt.Printf("provider: %s\n", detectProvider(baseURL, model))
	fmt.Printf("base_url: %s\n", baseURL)
	if model != "" {
		fmt.Printf("model: %s\n", model)
	}
	return nil
}

func cmdSetKey(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: claude-switch set-key <provider> <api-key>")
	}
	cfg, path, err := loadAppConfig()
	if err != nil {
		return err
	}
	provider := canonicalProviderName(args[0])
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", args[0])
	}
	stored := cfg.Providers[provider]
	stored.APIKey = args[1]
	cfg.Providers[provider] = stored
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}

	fmt.Printf("saved api key for %s in %s\n", provider, path)
	return nil
}

func cmdSwitch(args []string) error {
	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	apiKey := fs.String("api-key", "", "API key for the target provider")
	model := fs.String("model", "", "override model id")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: claude-switch switch <provider> [--api-key sk-xxx] [--model model-id]")
	}

	provider := canonicalProviderName(fs.Arg(0))
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", fs.Arg(0))
	}

	key := strings.TrimSpace(*apiKey)
	if key == "" {
		key = strings.TrimSpace(cfg.Providers[provider].APIKey)
	}
	if key == "" {
		return fmt.Errorf("missing api key for %s, run `claude-switch set-key %s <api-key>` or pass --api-key", provider, provider)
	}

	return switchProvider(provider, cfg, key, strings.TrimSpace(*model), *claudeDir)
}

func switchProvider(provider string, cfg *AppConfig, apiKey, modelOverride, claudeDir string) error {
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return err
	}

	settingsPath := claudeSettingsPath(claudeDir)
	root, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}

	if err := backupIfExists(settingsPath); err != nil {
		return err
	}

	applyPreset(root, preset, apiKey, modelOverride)
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		return err
	}

	fmt.Printf("switched Claude to %s\n", preset.Name)
	fmt.Printf("settings: %s\n", settingsPath)
	fmt.Printf("base_url: %s\n", preset.BaseURL)
	fmt.Printf("model: %s\n", effectiveModel(preset, modelOverride))
	return nil
}

func printUsage() {
	fmt.Println(`claude-switch

Usage:
  cs list
  cs [--reset-key]                     # interactive TUI
  cs current [--claude-dir DIR]
  cs set-key <provider> <api-key>
  cs switch <provider> [--api-key sk-xxx] [--model model-id] [--claude-dir DIR]

	Providers:
	  minimax-cn
	  minimax-global
	  openrouter
	  opencode-go`)
}

func sortedProviderNames(cfg *AppConfig, includeCustomOption bool) []string {
	names := make([]string, 0, len(providerPresets)+len(cfg.Providers)+1)
	for name := range providerPresets {
		names = append(names, name)
	}
	for name, stored := range cfg.Providers {
		if _, ok := providerPresets[name]; ok {
			continue
		}
		if strings.TrimSpace(stored.BaseURL) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if includeCustomOption {
		names = append(names, customProviderOption)
	}
	return names
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func canonicalProviderName(name string) string {
	normalized := normalizeProviderName(name)
	if canonical, ok := providerAliases[normalized]; ok {
		return canonical
	}
	return normalized
}

func resolveProviderPreset(provider string, cfg *AppConfig) (ProviderPreset, error) {
	if preset, ok := providerPresets[provider]; ok {
		if stored, ok := cfg.Providers[provider]; ok && strings.TrimSpace(stored.Model) != "" {
			preset.Model = strings.TrimSpace(stored.Model)
			if !containsString(preset.Models, preset.Model) {
				preset.Models = append([]string{preset.Model}, preset.Models...)
			}
		}
		return preset, nil
	}

	stored, ok := cfg.Providers[provider]
	if !ok || strings.TrimSpace(stored.BaseURL) == "" {
		return ProviderPreset{}, fmt.Errorf("unsupported provider %q", provider)
	}
	model := strings.TrimSpace(stored.Model)
	if model == "" {
		model = "custom-model"
	}
	return ProviderPreset{
		Name:    firstNonEmpty(stored.Name, provider),
		BaseURL: strings.TrimSpace(stored.BaseURL),
		Model:   model,
		Models:  []string{model},
		Haiku:   model,
		Sonnet:  model,
		Opus:    model,
	}, nil
}

func upsertProviderConfig(cfg *AppConfig, selection ConfigureSelection, apiKey string) {
	stored := cfg.Providers[selection.Provider]
	stored.APIKey = apiKey
	stored.Model = strings.TrimSpace(selection.Model)
	if selection.Name != "" {
		stored.Name = strings.TrimSpace(selection.Name)
	}
	if selection.BaseURL != "" {
		stored.BaseURL = strings.TrimSpace(selection.BaseURL)
	}
	cfg.Providers[selection.Provider] = stored
}

func detectProvider(baseURL, model string) string {
	switch {
	case strings.Contains(baseURL, "api.minimaxi.com"):
		return "minimax-cn"
	case strings.Contains(baseURL, "api.minimax.io"):
		return "minimax-global"
	case strings.Contains(baseURL, "openrouter.ai"):
		return "openrouter"
	case strings.Contains(baseURL, "opencode.ai") || strings.HasPrefix(model, "opencode-go/"):
		return "opencode-go"
	default:
		return "custom"
	}
}

func effectiveModel(preset ProviderPreset, override string) string {
	if override != "" {
		return override
	}
	return preset.Model
}

func promptConfigureSelectionFallback(reader *bufio.Reader, out io.Writer, cfg *AppConfig, currentProvider, currentModel string) (ConfigureSelection, error) {
	names := sortedProviderNames(cfg, true)

	for {
		renderProviderListScreen(out, names, cfg, currentProvider, 0, "")
		fmt.Fprint(out, "Provider: ")
		text, err := readLine(reader)
		if err != nil {
			return ConfigureSelection{}, err
		}
		provider, err := resolveProviderSelection(text, names)
		if err == nil {
			if provider == customProviderOption {
				return promptCustomProviderFallback(reader, out)
			}
			return ConfigureSelection{
				Provider: provider,
				Model:    defaultSelectionModel(provider, currentProvider, currentModel),
			}, nil
		}
		fmt.Fprintf(out, "\nInvalid provider: %s\n", strings.TrimSpace(text))
	}
}

func runArrowTUI(in *os.File, out io.Writer, cfg *AppConfig, currentProvider, currentModel string) (ConfigureSelection, error) {
	restore, err := enterRawMode(in)
	if err != nil {
		return promptConfigureSelectionFallback(bufio.NewReader(in), out, cfg, currentProvider, currentModel)
	}
	defer restore()

	names := sortedProviderNames(cfg, true)
	if len(names) == 0 {
		return ConfigureSelection{}, errors.New("no providers configured")
	}

	reader := bufio.NewReader(in)
	selectedProvider := 0
	for i, name := range names {
		if name == currentProvider {
			selectedProvider = i
			break
		}
	}
	selectedModel := modelIndex(names[selectedProvider], currentProvider, currentModel)
	status := ""
	page := tuiPageProviders
	resetKey := false
	typedAPIKey := ""

	for {
		if page == tuiPageProviders {
			renderProviderListScreen(out, names, cfg, currentProvider, selectedProvider, status)
		} else if page == tuiPageProviderDetail {
			renderProviderInfoScreen(out, names, cfg, currentProvider, currentModel, selectedProvider, status, resetKey)
		} else {
			renderProviderModelsScreen(out, names, cfg, currentProvider, currentModel, selectedProvider, selectedModel, status, resetKey)
		}
		key, err := readKey(reader)
		if err != nil {
			return ConfigureSelection{}, err
		}

		switch key {
		case "up":
			if page == tuiPageProviders {
				if selectedProvider > 0 {
					selectedProvider--
					selectedModel = modelIndex(names[selectedProvider], currentProvider, currentModel)
				}
			} else if page == tuiPageModels && selectedModel > 0 {
				selectedModel--
			}
			status = ""
		case "down":
			if page == tuiPageProviders {
				if selectedProvider < len(names)-1 {
					selectedProvider++
					selectedModel = modelIndex(names[selectedProvider], currentProvider, currentModel)
				}
			} else if page == tuiPageModels {
				models := providerModels(names[selectedProvider])
				if selectedModel < len(models)-1 {
					selectedModel++
				}
			}
			status = ""
		case "left":
			if page == tuiPageModels {
				page = tuiPageProviderDetail
			} else if page == tuiPageProviderDetail {
				page = tuiPageProviders
			}
			status = ""
		case "right":
			if page == tuiPageProviders {
				if names[selectedProvider] == customProviderOption {
					return promptCustomProviderWizard(reader, out, cfg)
				}
				page = tuiPageProviderDetail
			} else if page == tuiPageProviderDetail {
				if !hasConfigurableKey(strings.TrimSpace(cfg.Providers[names[selectedProvider]].APIKey), typedAPIKey, resetKey) {
					keyValue, promptErr := promptAPIKeyMasked(reader, out, names[selectedProvider])
					if promptErr != nil {
						status = "API key input cancelled."
						continue
					}
					typedAPIKey = keyValue
					resetKey = true
					status = "API key captured. Now choose a model."
				}
				page = tuiPageModels
			}
			status = ""
		case "enter":
			if page == tuiPageProviders {
				if names[selectedProvider] == customProviderOption {
					return promptCustomProviderWizard(reader, out, cfg)
				}
				page = tuiPageProviderDetail
				status = ""
				continue
			}
			if page == tuiPageProviderDetail {
				if !hasConfigurableKey(strings.TrimSpace(cfg.Providers[names[selectedProvider]].APIKey), typedAPIKey, resetKey) {
					keyValue, promptErr := promptAPIKeyMasked(reader, out, names[selectedProvider])
					if promptErr != nil {
						status = "API key input cancelled."
						continue
					}
					typedAPIKey = keyValue
					resetKey = true
					status = "API key captured. Now choose a model."
				}
				page = tuiPageModels
				status = ""
				continue
			}
			models := providerModels(names[selectedProvider])
			return ConfigureSelection{
				Provider: names[selectedProvider],
				Model:    models[selectedModel],
				ResetKey: resetKey,
				APIKey:   typedAPIKey,
			}, nil
		case "quit":
			if page == tuiPageModels {
				page = tuiPageProviderDetail
				status = ""
				continue
			}
			if page == tuiPageProviderDetail {
				page = tuiPageProviders
				status = ""
				continue
			}
			return ConfigureSelection{}, errors.New("cancelled")
		default:
			if page == tuiPageProviders {
				status = "Use ↑ ↓ to choose provider, Enter or → to open details, q to quit."
			} else if page == tuiPageProviderDetail {
				status = "Enter or → to choose model, k to edit key, ← or q to go back."
			} else {
				status = "Use ↑ ↓ to choose model, Enter to apply, k to edit key, ← or q to go back."
			}
		case "key":
			if page == tuiPageProviderDetail || page == tuiPageModels {
				keyValue, promptErr := promptAPIKeyMasked(reader, out, names[selectedProvider])
				if promptErr != nil {
					status = "API key input cancelled."
					continue
				}
				typedAPIKey = keyValue
				resetKey = true
				status = "New API key captured. It will be saved on apply."
			}
		case "custom_model":
			if page == tuiPageModels {
				modelValue, promptErr := promptTextInput(reader, out, "Custom Model", "Model", "Enter any model name. It will be saved as this provider's default model.", false)
				if promptErr != nil {
					status = "Custom model input cancelled."
					continue
				}
				stored := cfg.Providers[names[selectedProvider]]
				stored.Model = modelValue
				cfg.Providers[names[selectedProvider]] = stored
				selectedModel = 0
				status = "Custom model captured."
			}
		}
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

func promptCustomProviderFallback(reader *bufio.Reader, out io.Writer) (ConfigureSelection, error) {
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

	return ConfigureSelection{
		Provider: makeCustomProviderKey(name),
		Name:     name,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
	}, nil
}

func promptCustomProviderWizard(reader *bufio.Reader, out io.Writer, cfg *AppConfig) (ConfigureSelection, error) {
	name, err := promptTextInput(reader, out, "Custom Provider", "Provider name", "Enter a display name for this provider.", false)
	if err != nil {
		return ConfigureSelection{}, err
	}
	baseURL, err := promptTextInput(reader, out, "Custom Provider", "Base URL", "Enter the Anthropic-compatible base URL.", false)
	if err != nil {
		return ConfigureSelection{}, err
	}
	apiKey, err := promptTextInput(reader, out, "Custom Provider", "API Key", "Type the API key. Characters are hidden.", true)
	if err != nil {
		return ConfigureSelection{}, err
	}
	model, err := promptTextInput(reader, out, "Custom Provider", "Model", "Enter the default model name for this provider.", false)
	if err != nil {
		return ConfigureSelection{}, err
	}

	key := uniqueCustomProviderKey(cfg, makeCustomProviderKey(name))
	return ConfigureSelection{
		Provider: key,
		Name:     name,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
	}, nil
}

func promptAPIKeyMasked(reader *bufio.Reader, out io.Writer, provider string) (string, error) {
	return promptTextInput(reader, out, "Edit API Key", "API key", "Provider: "+provider+"\nType a new key. Characters are hidden. Press Enter to confirm, Esc to cancel.", true)
}

func promptTextInput(reader *bufio.Reader, out io.Writer, title, label, hint string, secret bool) (string, error) {
	var value []byte
	writeTerminalFrame(out, "\033[H\033[2J"+styleTitle("claude-switch")+"\n"+styleSection(title)+"\n\n"+styleMuted(hint)+"\n\n"+styleLabel(label)+": ")
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		switch b {
		case '\r', '\n':
			if len(value) == 0 {
				writeTerminalFrame(out, "\r\n"+styleWarning("Value cannot be empty.")+"\r\n")
				writeTerminalFrame(out, styleLabel(label)+": ")
				continue
			}
			writeTerminalFrame(out, "\r\n")
			return string(value), nil
		case 27:
			return "", errors.New("cancelled")
		case 127, 8:
			if len(value) > 0 {
				value = value[:len(value)-1]
				writeTerminalFrame(out, "\b \b")
			}
		default:
			if b >= 32 && b <= 126 {
				value = append(value, b)
				if secret {
					writeTerminalFrame(out, "*")
				} else {
					writeTerminalFrame(out, string(b))
				}
			}
		}
	}
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

func providerTitle(name string, cfg *AppConfig) string {
	if name == customProviderOption {
		return "custom..."
	}
	if stored, ok := cfg.Providers[name]; ok && strings.TrimSpace(stored.Name) != "" && !isPresetProvider(name) {
		return strings.TrimSpace(stored.Name)
	}
	return name
}

func isPresetProvider(name string) bool {
	_, ok := providerPresets[name]
	return ok
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func makeCustomProviderKey(name string) string {
	normalized := normalizeProviderName(name)
	normalized = strings.ReplaceAll(normalized, " ", "-")
	normalized = strings.ReplaceAll(normalized, "/", "-")
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.Trim(normalized, "-")
	if normalized == "" {
		return "custom-provider"
	}
	return normalized
}

func uniqueCustomProviderKey(cfg *AppConfig, base string) string {
	if _, exists := cfg.Providers[base]; !exists && !isPresetProvider(base) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, exists := cfg.Providers[candidate]; !exists && !isPresetProvider(candidate) {
			return candidate
		}
	}
}

func renderProviderListScreen(out io.Writer, names []string, cfg *AppConfig, currentProvider string, selectedProvider int, statusLine string) {
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	b.WriteString(styleTitle("claude-switch"))
	b.WriteString("\n")
	b.WriteString(styleMuted("Provider switcher for Claude Code"))
	b.WriteString("\n\n")
	b.WriteString(styleRule())
	b.WriteString("\n")
	b.WriteString(styleSection("Providers"))
	b.WriteString("\n")
	b.WriteString(styleMuted("Enter or → to open details"))
	b.WriteString("\n")
	if statusLine != "" {
		b.WriteString(styleWarning(statusLine))
		b.WriteString("\n\n")
	}
	for i, name := range names {
		if name == customProviderOption {
			cursor := styleMuted(" ")
			title := "custom..."
			if i == selectedProvider {
				cursor = styleSelected(">")
				title = styleSelected(title)
			}
			fmt.Fprintf(&b, "  %s %s\n", cursor, title)
			fmt.Fprintf(&b, "    %s\n", styleMuted("Add a custom Anthropic-compatible provider"))
			fmt.Fprintf(&b, "    %s\n", styleDim("Save name, base URL, key, and model"))
			continue
		}
		preset, err := resolveProviderPreset(name, cfg)
		if err != nil {
			continue
		}
		status := []string{}
		if name == currentProvider {
			status = append(status, styleBadgeCurrent("current"))
		}
		if strings.TrimSpace(cfg.Providers[name].APIKey) != "" {
			status = append(status, styleBadgeSaved("saved"))
		}

		label := ""
		if len(status) > 0 {
			label = " " + strings.Join(status, " ")
		}

		cursor := styleMuted(" ")
		title := providerTitle(name, cfg)
		if i == selectedProvider {
			cursor = styleSelected(">")
			title = styleSelected(title)
		}
		fmt.Fprintf(&b, "  %s %s%s\n", cursor, title, label)
		fmt.Fprintf(&b, "    %s\n", styleMuted(preset.Name))
		fmt.Fprintf(&b, "    %s\n", styleDim(preset.BaseURL))
	}
	b.WriteString("\n")
	b.WriteString(styleRule())
	b.WriteString("\n")
	b.WriteString(styleMuted("↑↓ provider   enter details   → details   q quit"))
	b.WriteString("\n")

	writeTerminalFrame(out, b.String())
}

func renderProviderInfoScreen(out io.Writer, names []string, cfg *AppConfig, currentProvider, currentModel string, selectedProvider int, statusLine string, resetKey bool) {
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	b.WriteString(styleTitle("claude-switch"))
	b.WriteString("\n")
	b.WriteString(styleMuted("Provider details"))
	b.WriteString("\n\n")
	if len(names) == 0 {
		writeTerminalFrame(out, b.String())
		return
	}
	provider := names[selectedProvider]
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		writeTerminalFrame(out, b.String())
		return
	}
	hasSavedKey := strings.TrimSpace(cfg.Providers[provider].APIKey) != ""
	b.WriteString(styleRule())
	b.WriteString("\n")
	b.WriteString(styleSection("Selection"))
	b.WriteString("\n")
	if statusLine != "" {
		b.WriteString(styleWarning(statusLine))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Provider"), styleSelected(providerTitle(provider, cfg)))
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Preset"), preset.Name)
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Base URL"), styleDim(preset.BaseURL))
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Saved Key"), maskAPIKey(cfg.Providers[provider].APIKey))
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Active"), currentProviderLabel(currentProvider)+" / "+currentModelLabel(currentModel))
	if resetKey {
		fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Key Action"), styleWarning("re-enter on apply"))
	} else if !hasSavedKey {
		fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Key Status"), styleWarning("no saved key"))
	}
	b.WriteString("\n")
	b.WriteString(styleSection("Next"))
	b.WriteString("\n")
	if hasSavedKey && !resetKey {
		fmt.Fprintf(&b, "  %s\n", styleMuted("Press Enter to continue to model selection"))
		fmt.Fprintf(&b, "  %s\n", styleMuted("Press k to replace the saved key"))
	} else {
		fmt.Fprintf(&b, "  %s\n", styleWarning("Press Enter to add a key and continue"))
		fmt.Fprintf(&b, "  %s\n", styleMuted("Press k to enter a new key now"))
	}
	fmt.Fprintf(&b, "  %s %s\n", styleLabel("Default"), preset.Model)
	fmt.Fprintf(&b, "  %s %d available\n", styleLabel("Models"), len(providerModels(provider)))
	b.WriteString("\n")
	b.WriteString(styleRule())
	b.WriteString("\n")
	b.WriteString(styleMuted("enter next   → next   k edit key   ← back   q back"))
	b.WriteString("\n")
	writeTerminalFrame(out, b.String())
}

func renderProviderModelsScreen(out io.Writer, names []string, cfg *AppConfig, currentProvider, currentModel string, selectedProvider, selectedModel int, statusLine string, resetKey bool) {
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	b.WriteString(styleTitle("claude-switch"))
	b.WriteString("\n")
	b.WriteString(styleMuted("Model selection"))
	b.WriteString("\n\n")
	if len(names) == 0 {
		writeTerminalFrame(out, b.String())
		return
	}
	provider := names[selectedProvider]
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		writeTerminalFrame(out, b.String())
		return
	}
	models := providerModels(provider)
	if selectedModel < 0 || selectedModel >= len(models) {
		selectedModel = 0
	}
	b.WriteString(styleRule())
	b.WriteString("\n")
	b.WriteString(styleSection("Models"))
	b.WriteString("\n")
	if statusLine != "" {
		b.WriteString(styleWarning(statusLine))
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Provider"), styleSelected(providerTitle(provider, cfg)))
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Saved Key"), maskAPIKey(cfg.Providers[provider].APIKey))
	fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Active"), currentProviderLabel(currentProvider)+" / "+currentModelLabel(currentModel))
	if resetKey {
		fmt.Fprintf(&b, "  %-14s %s\n", styleLabel("Key Action"), styleWarning("re-enter on apply"))
	}
	b.WriteString("\n")
	for i, model := range models {
		cursor := styleMuted("•")
		line := model
		if i == selectedModel {
			cursor = styleSelected(">")
			line = styleSelected(model)
		}
		if model == preset.Model {
			line += " " + styleBadgeDefault("default")
		}
		fmt.Fprintf(&b, "  %s %s\n", cursor, line)
	}
	b.WriteString("\n")
	b.WriteString(styleRule())
	b.WriteString("\n")
	b.WriteString(styleMuted("↑↓ model   enter apply   c custom model   k edit key   ← back   q back"))
	b.WriteString("\n")
	writeTerminalFrame(out, b.String())
}

func currentConfiguredProvider(cfg *AppConfig, claudeDir string) (string, string) {
	root, err := readJSONMap(claudeSettingsPath(claudeDir))
	if err != nil {
		return "", ""
	}
	env := nestedMap(root, "env")
	if env == nil {
		return "", ""
	}
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)
	if provider := detectProvider(baseURL, model); provider != "custom" {
		return provider, model
	}
	for name, stored := range cfg.Providers {
		if strings.TrimSpace(stored.BaseURL) == strings.TrimSpace(baseURL) {
			return name, model
		}
	}
	return "custom", model
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

func defaultSelectionModel(provider, currentProvider, currentModel string) string {
	if provider == currentProvider && currentModel != "" {
		for _, model := range providerModels(provider) {
			if model == currentModel {
				return currentModel
			}
		}
	}
	return providerPresets[provider].Model
}

func providerModels(provider string) []string {
	preset := providerPresets[provider]
	if len(preset.Models) == 0 {
		return []string{preset.Model}
	}
	return preset.Models
}

func modelIndex(provider, currentProvider, currentModel string) int {
	selected := defaultSelectionModel(provider, currentProvider, currentModel)
	for i, model := range providerModels(provider) {
		if model == selected {
			return i
		}
	}
	return 0
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "not saved"
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func currentProviderLabel(provider string) string {
	if provider == "" {
		return "none"
	}
	return provider
}

func currentModelLabel(model string) string {
	if model == "" {
		return "none"
	}
	return model
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

func enterRawMode(in *os.File) (func(), error) {
	stateCmd := exec.Command("stty", "-g")
	stateCmd.Stdin = in
	savedState, err := stateCmd.Output()
	if err != nil {
		return nil, err
	}

	rawCmd := exec.Command("stty", "raw", "-echo")
	rawCmd.Stdin = in
	if err := rawCmd.Run(); err != nil {
		return nil, err
	}

	return func() {
		restoreCmd := exec.Command("stty", strings.TrimSpace(string(savedState)))
		restoreCmd.Stdin = in
		_ = restoreCmd.Run()
	}, nil
}

func readKey(reader *bufio.Reader) (string, error) {
	b, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	switch b {
	case '\r', '\n':
		return "enter", nil
	case 'c', 'C':
		return "custom_model", nil
	case 'k', 'K':
		return "key", nil
	case 'q', 'Q':
		return "quit", nil
	case 27:
		// ESC key: may be standalone or start of escape sequence.
		// For standalone ESC, the next ReadByte would block.
		// Use a goroutine to detect if a sequence follows within 50ms.
		type result struct {
			b   byte
			err error
		}
		ch := make(chan result, 1)
		go func() {
			b, err := reader.ReadByte()
			ch <- result{b, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				return "", r.err
			}
			if r.b != '[' {
				// Not an arrow key sequence; unread by returning empty
				return "", nil
			}
			arrow, err := reader.ReadByte()
			if err != nil {
				return "", err
			}
			switch arrow {
			case 'A':
				return "up", nil
			case 'B':
				return "down", nil
			case 'C':
				return "right", nil
			case 'D':
				return "left", nil
			}
			return "", nil
		case <-time.After(50 * time.Millisecond):
			// No follow-up byte within 50ms; treat as standalone ESC
			return "escape", nil
		}
	}
	return "", nil
}

func writeTerminalFrame(out io.Writer, content string) {
	if runtime.GOOS == "windows" {
		fmt.Fprint(out, strings.ReplaceAll(content, "\n", "\r\n"))
	} else {
		fmt.Fprint(out, content)
	}
}

func styleTitle(text string) string {
	return "\033[1;96m" + text + "\033[0m"
}

func styleSection(text string) string {
	return "\033[1m" + text + "\033[0m"
}

func styleLabel(text string) string {
	return "\033[1;37m" + text + "\033[0m"
}

func styleMuted(text string) string {
	return "\033[38;5;245m" + text + "\033[0m"
}

func styleDim(text string) string {
	return "\033[38;5;242m" + text + "\033[0m"
}

func styleSelected(text string) string {
	return "\033[1;97m" + text + "\033[0m"
}

func styleWarning(text string) string {
	return "\033[1;33m" + text + "\033[0m"
}

func styleBadgeCurrent(text string) string {
	return "\033[30;46m " + text + " \033[0m"
}

func styleBadgeSaved(text string) string {
	return "\033[30;42m " + text + " \033[0m"
}

func styleBadgeDefault(text string) string {
	return "\033[30;47m " + text + " \033[0m"
}

func styleRule() string {
	return styleDim(strings.Repeat("─", 72))
}

func applyPreset(root map[string]any, preset ProviderPreset, apiKey, overrideModel string) {
	env := ensureNestedMap(root, "env")
	for _, key := range managedEnvKeys {
		delete(env, key)
	}

	env["ANTHROPIC_BASE_URL"] = preset.BaseURL
	env["ANTHROPIC_API_KEY"] = apiKey
	env["ANTHROPIC_MODEL"] = effectiveModel(preset, overrideModel)
	env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = preset.Haiku
	env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = preset.Sonnet
	env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = preset.Opus
	if overrideModel != "" {
		env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = overrideModel
		env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = overrideModel
		env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = overrideModel
	}

	for key, value := range preset.ExtraEnv {
		env[key] = value
	}
}

func claudeSettingsPath(overrideDir string) string {
	dir := strings.TrimSpace(overrideDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".claude", "settings.json")
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

func appConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude-switch", "config.json"), nil
}

func loadAppConfig() (*AppConfig, string, error) {
	path, err := appConfigPath()
	if err != nil {
		return nil, "", err
	}
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, path, nil
		}
		return nil, "", err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]StoredProvider{}
	}
	migrateLegacyProviders(cfg)
	return cfg, path, nil
}

func migrateLegacyProviders(cfg *AppConfig) {
	legacy, ok := cfg.Providers["minimax"]
	if ok {
		if _, exists := cfg.Providers["minimax-cn"]; !exists && strings.TrimSpace(legacy.APIKey) != "" {
			cfg.Providers["minimax-cn"] = legacy
		}
		delete(cfg.Providers, "minimax")
	}
}

func readJSONMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func nestedMap(root map[string]any, key string) map[string]any {
	raw, ok := root[key]
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

func ensureNestedMap(root map[string]any, key string) map[string]any {
	if obj := nestedMap(root, key); obj != nil {
		return obj
	}
	obj := map[string]any{}
	root[key] = obj
	return obj
}

func backupIfExists(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backupPath := fmt.Sprintf("%s.bak.%s", path, time.Now().Format("20060102_150405"))
	return os.WriteFile(backupPath, data, 0o600)
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
