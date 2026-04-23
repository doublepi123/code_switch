package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProviderPreset struct {
	Name      string
	BaseURL   string
	Model     string
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

var providerPresets = map[string]ProviderPreset{
	"minimax": {
		Name:      "MiniMax",
		BaseURL:   "https://api.minimaxi.com/anthropic",
		Model:     "MiniMax-M2.7",
		Haiku:     "MiniMax-M2.7",
		Sonnet:    "MiniMax-M2.7",
		Opus:      "MiniMax-M2.7",
		Website:   "https://platform.minimaxi.com",
		APIKeyURL: "https://platform.minimaxi.com/subscribe/coding-plan",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": 1,
		},
	},
	"openrouter": {
		Name:      "OpenRouter",
		BaseURL:   "https://openrouter.ai/api",
		Model:     "anthropic/claude-sonnet-4.6",
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
		Haiku:     "minimax-m2.7",
		Sonnet:    "minimax-m2.7",
		Opus:      "minimax-m2.7",
		Website:   "https://opencode.ai/docs/go/",
		APIKeyURL: "https://opencode.ai",
	},
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
		printUsage()
		return nil
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

	reader := bufio.NewReader(in)
	provider, err := promptProviderSelection(reader, out, cfg, currentConfiguredProvider(*claudeDir))
	if err != nil {
		return err
	}

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

	if err := switchProvider(provider, apiKey, "", *claudeDir); err != nil {
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
	provider := normalizeProviderName(args[0])
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

	provider := normalizeProviderName(fs.Arg(0))
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
  minimax
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

func detectProvider(baseURL, model string) string {
	switch {
	case strings.Contains(baseURL, "minimax"):
		return "minimax"
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

func promptProviderSelection(reader *bufio.Reader, out io.Writer, cfg *AppConfig, currentProvider string) (string, error) {
	names := sortedProviderNames()

	for {
		renderConfigureScreen(out, names, cfg, currentProvider)
		fmt.Fprint(out, "Provider: ")
		text, err := readLine(reader)
		if err != nil {
			return "", err
		}
		provider, err := resolveProviderSelection(text, names)
		if err == nil {
			return provider, nil
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

func renderConfigureScreen(out io.Writer, names []string, cfg *AppConfig, currentProvider string) {
	fmt.Fprint(out, "\033[H\033[2J")
	fmt.Fprintln(out, "claude-switch")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Configure provider")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Select a provider by number or name:")
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

		fmt.Fprintf(out, "  %d) %s%s\n", i+1, name, label)
		fmt.Fprintf(out, "     %s\n", preset.BaseURL)
		fmt.Fprintf(out, "     default model: %s\n", preset.Model)
	}
	fmt.Fprintln(out)
}

func currentConfiguredProvider(claudeDir string) string {
	root, err := readJSONMap(claudeSettingsPath(claudeDir))
	if err != nil {
		return ""
	}
	env := nestedMap(root, "env")
	if env == nil {
		return ""
	}
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)
	return detectProvider(baseURL, model)
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

	provider := normalizeProviderName(text)
	if _, ok := providerPresets[provider]; !ok {
		return "", errors.New("unsupported provider")
	}
	return provider, nil
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
	return cfg, path, nil
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
