package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runWithIO(args, os.Stdin, os.Stdout)
}

func runWithIO(args []string, in io.Reader, out io.Writer) error {
	if isVersionRequest(args) {
		printVersion(out)
		return nil
	}

	if len(args) == 0 {
		return cmdConfigure(nil, in, out)
	}
	if strings.HasPrefix(args[0], "-") {
		return cmdConfigure(args, in, out)
	}

	switch args[0] {
	case "list":
		return cmdList(out)
	case "configure":
		return cmdConfigure(args[1:], in, out)
	case "current":
		return cmdCurrent(args[1:])
	case "set-key":
		return cmdSetKey(args[1:])
	case "switch":
		return cmdSwitch(args[1:])
	case "upgrade":
		return cmdUpgrade(args[1:], out)
	case "test":
		return cmdTest(args[1:], out)
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printVersion(out io.Writer) {
	fmt.Fprintf(out, "claude-switch %s\n", version)
}

func isVersionRequest(args []string) bool {
	if len(args) == 1 {
		return args[0] == "--version" || args[0] == "version"
	}
	if len(args) == 2 && args[1] == "--version" {
		switch args[0] {
		case "list", "configure", "current", "set-key", "switch", "upgrade", "help", "test":
			return true
		}
	}
	return false
}

func cmdList(out io.Writer) error {
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
		fmt.Fprintf(out, "%s\t%s\t%s\n", name, preset.BaseURL, preset.Model)
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
		return fmt.Errorf("usage: claude-switch set-key <provider> <api-key>")
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

func printUsage() {
	fmt.Println(`claude-switch

Usage:
  cs --version
  cs list
  cs [--reset-key]                     # interactive TUI
  cs current [--claude-dir DIR]
  cs set-key <provider> <api-key>
  cs switch <provider> [--api-key sk-xxx] [--model model-id] [--claude-dir DIR]
  cs test <provider> [--api-key sk-xxx] [--model model-id]
  cs upgrade

	Providers:
	  deepseek
	  minimax-cn
	  minimax-global
	  openrouter
	  opencode-go`)
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
	if _, exists := cfg.Providers[base]; !exists && !isPresetProvider(base) && !isProviderAlias(base) {
		return base
	}
	for i := 2; i < 10000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, exists := cfg.Providers[candidate]; !exists && !isPresetProvider(candidate) && !isProviderAlias(candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}

func normalizedURLHost(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Hostname() != "" {
		return strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	}
	parsed, err = url.Parse("https://" + rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
}
