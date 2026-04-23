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
	APIKey string `json:"apiKey,omitempty"`
}

type AppConfig struct {
	Providers map[string]StoredProvider `json:"providers"`
}

type ConfigureSelection struct {
	Provider string
	Model    string
}

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
	names := sortedProviderNames()
	for _, name := range names {
		preset := providerPresets[name]
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
	if fs.NArg() != 0 {
		return errors.New("usage: claude-switch configure [--claude-dir DIR] [--reset-key]")
	}

	cfg, configPath, err := loadAppConfig()
	if err != nil {
		return err
	}

	currentProvider, currentModel := currentConfiguredProvider(*claudeDir)
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
	if apiKey == "" || *resetKey {
		apiKey, err = promptAPIKey(reader, out, provider)
		if err != nil {
			return err
		}
		cfg.Providers[provider] = StoredProvider{APIKey: apiKey}
		if err := writeJSONAtomic(configPath, cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "saved api key for %s in %s\n", provider, configPath)
	} else {
		fmt.Fprintf(out, "using saved api key for %s\n", provider)
	}

	if err := switchProvider(provider, apiKey, selection.Model, *claudeDir); err != nil {
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
	provider := canonicalProviderName(args[0])
	if _, ok := providerPresets[provider]; !ok {
		return fmt.Errorf("unsupported provider %q", args[0])
	}

	cfg, path, err := loadAppConfig()
	if err != nil {
		return err
	}
	cfg.Providers[provider] = StoredProvider{APIKey: args[1]}
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
	if _, ok := providerPresets[provider]; !ok {
		return fmt.Errorf("unsupported provider %q", fs.Arg(0))
	}

	key := strings.TrimSpace(*apiKey)
	if key == "" {
		cfg, _, err := loadAppConfig()
		if err != nil {
			return err
		}
		key = strings.TrimSpace(cfg.Providers[provider].APIKey)
	}
	if key == "" {
		return fmt.Errorf("missing api key for %s, run `claude-switch set-key %s <api-key>` or pass --api-key", provider, provider)
	}

	return switchProvider(provider, key, strings.TrimSpace(*model), *claudeDir)
}

func switchProvider(provider, apiKey, modelOverride, claudeDir string) error {
	preset, ok := providerPresets[provider]
	if !ok {
		return fmt.Errorf("unsupported provider %q", provider)
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
  claude-switch list
  claude-switch configure [--claude-dir DIR] [--reset-key]
  claude-switch current [--claude-dir DIR]
  claude-switch set-key <provider> <api-key>
  claude-switch switch <provider> [--api-key sk-xxx] [--model model-id] [--claude-dir DIR]

	Providers:
	  minimax-cn
	  minimax-global
	  openrouter
	  opencode-go`)
}

func sortedProviderNames() []string {
	names := make([]string, 0, len(providerPresets))
	for name := range providerPresets {
		names = append(names, name)
	}
	sort.Strings(names)
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
	names := sortedProviderNames()

	for {
		renderConfigureScreen(out, names, cfg, currentProvider, currentModel, 0, 0, "")
		fmt.Fprint(out, "Provider: ")
		text, err := readLine(reader)
		if err != nil {
			return ConfigureSelection{}, err
		}
		provider, err := resolveProviderSelection(text, names)
		if err == nil {
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

	names := sortedProviderNames()
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

	for {
		renderConfigureScreen(out, names, cfg, currentProvider, currentModel, selectedProvider, selectedModel, status)
		key, err := readKey(reader)
		if err != nil {
			return ConfigureSelection{}, err
		}

		switch key {
		case "up":
			if selectedProvider > 0 {
				selectedProvider--
				selectedModel = modelIndex(names[selectedProvider], currentProvider, currentModel)
			}
			status = ""
		case "down":
			if selectedProvider < len(names)-1 {
				selectedProvider++
				selectedModel = modelIndex(names[selectedProvider], currentProvider, currentModel)
			}
			status = ""
		case "left":
			if selectedModel > 0 {
				selectedModel--
			}
			status = ""
		case "right":
			models := providerModels(names[selectedProvider])
			if selectedModel < len(models)-1 {
				selectedModel++
			}
			status = ""
		case "enter":
			models := providerModels(names[selectedProvider])
			return ConfigureSelection{
				Provider: names[selectedProvider],
				Model:    models[selectedModel],
			}, nil
		case "quit":
			return ConfigureSelection{}, errors.New("cancelled")
		default:
			status = "Use arrow keys to choose provider/model, Enter to apply, q to quit."
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

func renderConfigureScreen(out io.Writer, names []string, cfg *AppConfig, currentProvider, currentModel string, selectedProvider, selectedModel int, statusLine string) {
	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	b.WriteString("claude-switch\n\n")
	b.WriteString("Configure provider\n\n")
	b.WriteString("Use ↑ ↓ to choose provider, ← → to choose model, Enter to apply, q to quit.\n")
	if statusLine != "" {
		b.WriteString(statusLine)
		b.WriteString("\n\n")
	}
	for i, name := range names {
		preset := providerPresets[name]
		status := []string{}
		if name == currentProvider {
			status = append(status, "current")
		}
		if strings.TrimSpace(cfg.Providers[name].APIKey) != "" {
			status = append(status, "saved-key")
		}

		label := ""
		if len(status) > 0 {
			label = " [" + strings.Join(status, ", ") + "]"
		}

		cursor := " "
		if i == selectedProvider {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %d) %s%s\n", cursor, i+1, name, label)
		fmt.Fprintf(&b, "     %s\n", preset.BaseURL)
		fmt.Fprintf(&b, "     default model: %s\n", preset.Model)
	}
	b.WriteString("\n")

	if len(names) == 0 {
		writeTerminalFrame(out, b.String())
		return
	}
	provider := names[selectedProvider]
	preset := providerPresets[provider]
	models := providerModels(provider)
	if selectedModel < 0 || selectedModel >= len(models) {
		selectedModel = 0
	}
	fmt.Fprintf(&b, "Selected provider: %s\n", provider)
	fmt.Fprintf(&b, "Provider name: %s\n", preset.Name)
	fmt.Fprintf(&b, "Base URL: %s\n", preset.BaseURL)
	fmt.Fprintf(&b, "Saved key: %s\n", maskAPIKey(cfg.Providers[provider].APIKey))
	fmt.Fprintf(&b, "Current active: %s / %s\n", currentProviderLabel(currentProvider), currentModelLabel(currentModel))
	b.WriteString("Models:\n")
	for i, model := range models {
		cursor := " "
		if i == selectedModel {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %s\n", cursor, model)
	}
	b.WriteString("\n")

	writeTerminalFrame(out, b.String())
}

func currentConfiguredProvider(claudeDir string) (string, string) {
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
	return detectProvider(baseURL, model), model
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
	if _, ok := providerPresets[provider]; !ok {
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
	case 'q', 'Q':
		return "quit", nil
	case 27:
		next, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if next != '[' {
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
	}
	return "", nil
}

func writeTerminalFrame(out io.Writer, content string) {
	fmt.Fprint(out, strings.ReplaceAll(content, "\n", "\r\n"))
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
