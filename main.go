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
		return cmdList(args[1:], out)
	case "configure":
		return cmdConfigure(args[1:], in, out)
	case "current":
		return cmdCurrent(args[1:], out)
	case "set-key":
		return cmdSetKey(args[1:])
	case "switch":
		return cmdSwitch(args[1:])
	case "upgrade":
		return cmdUpgrade(args[1:], out)
	case "test":
		return cmdTest(args[1:], out)
	case "remove":
		return cmdRemove(args[1:])
	case "completion":
		return cmdCompletion(args[1:], out)
	case "help", "-h", "--help":
		printUsage(out)
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
		case "list", "configure", "current", "set-key", "switch", "upgrade", "help", "test", "remove", "completion":
			return true
		}
	}
	return false
}

func cmdList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	verbose := fs.Bool("verbose", false, "show all available models for each provider")
	if err := fs.Parse(args); err != nil {
		return err
	}

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
		if *verbose {
			fmt.Fprintf(out, "%s\t%s\t%s\t%v\n", name, preset.BaseURL, preset.Model, preset.Models)
		} else {
			fmt.Fprintf(out, "%s\t%s\t%s\n", name, preset.BaseURL, preset.Model)
		}
	}
	return nil
}

func cmdCurrent(args []string, out io.Writer) error {
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

	fmt.Fprintf(out, "settings: %s\n", settingsPath)
	if baseURL == "" {
		fmt.Fprintln(out, "provider: unknown")
		return nil
	}

	fmt.Fprintf(out, "provider: %s\n", detectProvider(baseURL, model))
	fmt.Fprintf(out, "base_url: %s\n", baseURL)
	if model != "" {
		fmt.Fprintf(out, "model: %s\n", model)
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

func cmdRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: claude-switch remove <provider> [--force]")
	}

	flags := flag.NewFlagSet("remove", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	force := flags.Bool("force", false, "skip confirmation prompt")
	if err := flags.Parse(args); err != nil {
		return err
	}

	providerArg := strings.TrimSpace(flags.Arg(0))
	if providerArg == "" {
		return fmt.Errorf("usage: claude-switch remove <provider> [--force]")
	}

	provider := canonicalProviderName(providerArg)
	cfg, path, err := loadAppConfig()
	if err != nil {
		return err
	}

	stored, ok := cfg.Providers[provider]
	if !ok {
		return fmt.Errorf("no saved configuration for provider %q", provider)
	}

	if !*force {
		showKey := maskAPIKey(stored.APIKey)
		fmt.Printf("Remove saved config for %s (key: %s)? [y/N]: ", provider, showKey)
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			fmt.Println("cancelled")
			return nil
		}
	}

	delete(cfg.Providers, provider)
	fmt.Fprintf(os.Stdout, "removed %s from %s\n", provider, path)
	return writeJSONAtomic(path, cfg)
}

func cmdCompletion(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: claude-switch completion bash|zsh|fish")
	}
	shell := strings.ToLower(strings.TrimSpace(args[0]))
	switch shell {
	case "bash":
		fmt.Fprint(out, bashCompletion)
	case "zsh":
		fmt.Fprint(out, zshCompletion)
	case "fish":
		fmt.Fprint(out, fishCompletion)
	default:
		return fmt.Errorf("unsupported shell %q, use bash, zsh, or fish", shell)
	}
	return nil
}

const bashCompletion = `# claude-switch bash completion
_cs() {
	local cur prev words cword
	_init_completion || return
	COMPREPLY=()

	case $cword in
	1)
		COMPREPLY=($(compgen -W "list configure current set-key switch test remove upgrade completion help --version --help" -- "$cur"))
		;;
	2)
		case ${words[1]} in
		switch|set-key|test|remove)
			COMPREPLY=($(compgen -W "deepseek minimax-cn minimax-global openrouter opencode-go xiaomimimo-cn ollama" -- "$cur"))
			;;
		completion)
			COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur"))
			;;
		esac
		;;
	esac
}
complete -F _cs cs
`

const zshCompletion = `#compdef cs

_cs() {
	local -a commands
	commands=(
		'list:list available providers'
		'configure:interactive TUI configuration'
		'current:show current provider'
		'set-key:save API key for a provider'
		'switch:switch Claude Code provider'
		'test:test provider API connectivity'
		'remove:remove saved provider config'
		'upgrade:upgrade to latest release'
		'completion:generate shell completion'
		'help:show help'
	)

	local -a providers
	providers=(
		'deepseek'
		'minimax-cn'
		'minimax-global'
		'openrouter'
		'opencode-go'
		'xiaomimimo-cn'
		'ollama'
	)

	local -a shells
	shells=('bash' 'zsh' 'fish')

	_arguments \
		'--version[show version]' \
		'--help[show help]' \
		'1:command:_describe command commands' \
		'2:provider:_describe provider providers' \
		'*::arg:->args'
}
_cs
`

const fishCompletion = `# claude-switch fish completion
complete -c cs -f

complete -c cs -n '__fish_use_subcommand' -a 'list' -d 'List available providers'
complete -c cs -n '__fish_use_subcommand' -a 'configure' -d 'Interactive TUI configuration'
complete -c cs -n '__fish_use_subcommand' -a 'current' -d 'Show current provider'
complete -c cs -n '__fish_use_subcommand' -a 'set-key' -d 'Save API key for a provider'
complete -c cs -n '__fish_use_subcommand' -a 'switch' -d 'Switch Claude Code provider'
complete -c cs -n '__fish_use_subcommand' -a 'test' -d 'Test provider API connectivity'
complete -c cs -n '__fish_use_subcommand' -a 'remove' -d 'Remove saved provider config'
complete -c cs -n '__fish_use_subcommand' -a 'upgrade' -d 'Upgrade to latest release'
complete -c cs -n '__fish_use_subcommand' -a 'completion' -d 'Generate shell completion'
complete -c cs -n '__fish_use_subcommand' -a 'help' -d 'Show help'

complete -c cs -n '__fish_seen_subcommand_from switch set-key test remove' -a 'deepseek minimax-cn minimax-global openrouter opencode-go xiaomimimo-cn ollama'
complete -c cs -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'

complete -c cs -l version -d 'Show version'
complete -c cs -l help -d 'Show help'
`

func printUsage(out io.Writer) {
	fmt.Fprintln(out, `claude-switch

Usage:
  cs --version
  cs list [--verbose]
  cs [--dry-run] [--reset-key]         # interactive TUI
  cs current [--claude-dir DIR]
  cs set-key <provider> <api-key>
  cs switch <provider> [--api-key sk-xxx] [--model model-id] [--claude-dir DIR] [--dry-run]
  cs test <provider> [--api-key sk-xxx] [--model model-id] [--path /custom/api/path]
  cs remove <provider> [--force]
  cs upgrade [--dry-run] [--tag vX.Y.Z]
  cs completion bash|zsh|fish

	Providers:
	  deepseek
	  minimax-cn
	  minimax-global
	  openrouter
	  opencode-go
	  xiaomimimo-cn
	  ollama`)
}

func makeCustomProviderKey(name string) string {
	normalized := normalizeProviderName(name)
	normalized = replaceNonAlphaNum(normalized, '-')
	normalized = compressRepeated(normalized, '-')
	normalized = strings.Trim(normalized, "-")
	if normalized == "" {
		return "custom-provider"
	}
	return normalized
}

func replaceNonAlphaNum(s string, replacement byte) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		} else {
			b.WriteByte(replacement)
		}
	}
	return b.String()
}

func compressRepeated(s string, b byte) string {
	if len(s) < 2 {
		return s
	}
	var result strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != b || i == 0 || s[i-1] != b {
			result.WriteByte(c)
		}
	}
	return result.String()
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

func validateBaseURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("base URL cannot be empty")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid base URL: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("base URL must use http or https scheme, got %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("base URL must have a valid host")
	}
	return nil
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
