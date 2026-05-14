package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

var errCancelled = errors.New("cancelled")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errCancelled) {
			os.Exit(0)
		}
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
		return cmdSetKey(args[1:], out)
	case "switch":
		return cmdSwitchWithOutput(args[1:], out)
	case "env":
		return cmdEnv(args[1:], out)
	case "token":
		return cmdToken(args[1:], out)
	case "restore":
		return cmdRestore(args[1:], out)
	case "upgrade":
		return cmdUpgrade(args[1:], out)
	case "test":
		return cmdTest(args[1:], out)
	case "remove":
		return cmdRemove(args[1:], in, out)
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
	fmt.Fprintf(out, "code-switch %s\n", version)
}

func isVersionRequest(args []string) bool {
	if len(args) == 1 {
		return args[0] == "--version" || args[0] == "version"
	}
	if len(args) == 2 && args[1] == "--version" {
		switch args[0] {
		case "list", "configure", "current", "set-key", "switch", "env", "token", "restore", "upgrade", "help", "test", "remove", "completion":
			return true
		}
	}
	return false
}

func cmdList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	verbose := fs.Bool("verbose", false, "show all available models for each provider")
	if err := fs.Parse(args); err != nil {
		return err
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}

	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	names := providerNamesForAgent(agent, cfg, false, false)
	for _, name := range names {
		preset, err := resolveAgentProviderPreset(agent, name, cfg)
		if err != nil {
			return err
		}
		keyStatus := "✗"
		if preset.NoAPIKey {
			keyStatus = "—"
		} else if storedAPIKeyForAgent(cfg, agent, name) != "" {
			keyStatus = "✓"
		}
		if *verbose {
			models := providerModelsForAgent(cfg, agent, name)
			if len(models) == 0 {
				models = preset.Models
			}
			fmt.Fprintf(out, "%s\t%s\t%s\t%v\t%s\n", name, preset.BaseURL, preset.Model, models, keyStatus)
		} else {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", name, preset.BaseURL, preset.Model, keyStatus)
		}
	}
	return nil
}

func cmdCurrent(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("current", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", "", "target agent: claude or codex (default: show both)")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	showBoth := *agentFlag == ""
	var agent AgentName
	if !showBoth {
		var err error
		agent, err = parseAgentName(*agentFlag)
		if err != nil {
			return err
		}
	}

	if showBoth || agent == agentClaude {
		settingsPath := claudeSettingsPath(*claudeDir)
		root, err := readJSONMap(settingsPath)
		if err != nil {
			return err
		}

		env := nestedMap(root, "env")
		baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
		model, _ := env["ANTHROPIC_MODEL"].(string)

		fmt.Fprintf(out, "Claude Code\n")
		fmt.Fprintf(out, "  %s\n", formatLabel("settings", settingsPath))
		if baseURL == "" {
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", "unknown"))
		} else {
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", detectProvider(baseURL, model)))
			fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
			if model != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
			}
		}
		if showBoth {
			fmt.Fprintln(out)
		}
	}

	if showBoth || agent == agentCodex {
		configPath, provider, model, baseURL, err := currentCodexProvider(*codexDir)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Codex\n")
		providerKey := codexTOMLProviderKey(provider)
		fmt.Fprintf(out, "  %s\n", formatLabel("config", configPath))
		if provider == "" {
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", "unknown"))
		} else {
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", providerKey))
			if baseURL != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
			}
			if model != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
			}
		}
	}
	return nil
}

func cmdSetKey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("set-key", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: code-switch set-key <provider> <api-key> [--agent claude|codex]")
	}

	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	provider := canonicalProviderName(remaining[0])

	if agent == agentCodex {
		if provider != "ollama-cloud" && provider != "openrouter" && provider != "deepseek" {
			return fmt.Errorf("unsupported provider %q for agent codex", remaining[0])
		}
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			return err
		}
		defer unlock()
		agentCfg := agentConfig(cfg, agentCodex)
		stored := agentCfg.Providers[provider]
		stored.APIKey = remaining[1]
		agentCfg.Providers[provider] = stored
		cfg.Agents[string(agentCodex)] = agentCfg
		if err := writeJSONAtomic(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "saved api key for %s (codex) in %s\n", provider, path)
		return nil
	}

	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return fmt.Errorf("resolve provider %q: %w", remaining[0], err)
	}
	if preset.NoAPIKey {
		return fmt.Errorf("provider %q does not require an API key", provider)
	}
	stored := cfg.Providers[provider]
	stored.APIKey = remaining[1]
	cfg.Providers[provider] = stored
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}

	fmt.Fprintf(out, "saved api key for %s in %s\n", provider, path)
	return nil
}


func cmdRemove(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	force := fs.Bool("force", false, "skip confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	providerArg := strings.TrimSpace(fs.Arg(0))
	if providerArg == "" {
		return fmt.Errorf("usage: code-switch remove <provider> [--agent claude|codex] [--force]")
	}

	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	provider := canonicalProviderName(providerArg)
	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	if agent == agentCodex {
		agentCfg := agentConfig(cfg, agentCodex)
		_, ok := agentCfg.Providers[provider]
		if !ok {
			return fmt.Errorf("no saved configuration for provider %q for agent codex", provider)
		}
		if !*force {
			stored := codexProviderConfig(cfg, provider)
			showKey := maskAPIKey(stored.APIKey)
			fmt.Fprintf(out, "Remove saved config for %s (codex, key: %s)? [y/N]: ", provider, showKey)
			reader := bufio.NewReader(in)
			response, err := readLine(reader)
			if err != nil {
				return fmt.Errorf("read confirmation: %w", err)
			}
			if strings.ToLower(strings.TrimSpace(response)) != "y" {
				fmt.Fprintln(out, "cancelled")
				return nil
			}
		}
		delete(agentCfg.Providers, provider)
		cfg.Agents[string(agentCodex)] = agentCfg
		fmt.Fprintf(out, "removed %s (codex) from %s\n", provider, path)
		return writeJSONAtomic(path, cfg)
	}

	stored, ok := cfg.Providers[provider]
	if !ok {
		return fmt.Errorf("no saved configuration for provider %q", provider)
	}
	if !*force {
		showKey := maskAPIKey(stored.APIKey)
		fmt.Fprintf(out, "Remove saved config for %s (key: %s)? [y/N]: ", provider, showKey)
		reader := bufio.NewReader(in)
		response, err := readLine(reader)
		if err != nil {
			return fmt.Errorf("read confirmation: %w", err)
		}
		if strings.ToLower(strings.TrimSpace(response)) != "y" {
			fmt.Fprintln(out, "cancelled")
			return nil
		}
	}

	delete(cfg.Providers, provider)
	fmt.Fprintf(out, "removed %s from %s\n", provider, path)
	return writeJSONAtomic(path, cfg)
}
func cmdCompletion(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: code-switch completion bash|zsh|fish")
	}
	shell := strings.ToLower(strings.TrimSpace(args[0]))
	switch shell {
	case "bash":
		fmt.Fprint(out, bashCompletionString())
	case "zsh":
		fmt.Fprint(out, zshCompletionString())
	case "fish":
		fmt.Fprint(out, fishCompletionString())
	default:
		return fmt.Errorf("unsupported shell %q, use bash, zsh, or fish", shell)
	}
	return nil
}

func bashCompletionString() string {
	return fmt.Sprintf(`# code-switch bash completion
_cs() {
	local cur prev words cword
	_init_completion || return
	COMPREPLY=()

	case $cword in
	1)
		COMPREPLY=($(compgen -W "list configure current set-key switch env token restore test remove upgrade completion help --version --help" -- "$cur"))
		;;
	2)
		case ${words[1]} in
		switch|set-key|env|token|test|remove)
			COMPREPLY=($(compgen -W "%s" -- "$cur"))
			;;
		completion)
			COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur"))
			;;
		esac
		;;
	esac
}
complete -F _cs cs
`, providerCompletionWordList())
}

func zshCompletionString() string {
	var b strings.Builder
	b.WriteString("#compdef cs\n\n_cs() {\n\tlocal -a commands\n\tcommands=(\n\t\t'list:list available providers'\n\t\t'configure:interactive TUI configuration'\n\t\t'current:show current provider'\n\t\t'set-key:save API key for a provider'\n\t\t'switch:switch agent provider'\n\t\t'env:print shell exports for a provider'\n\t\t'token:print raw API token for command-backed auth'\n\t\t'restore:restore official agent config'\n\t\t'test:test provider API connectivity'\n\t\t'remove:remove saved provider config'\n\t\t'upgrade:upgrade to latest release'\n\t\t'completion:generate shell completion'\n\t\t'help:show help'\n\t)\n\n\tlocal -a providers\n\tproviders=(\n")
	for _, name := range sortedPresetNames() {
		fmt.Fprintf(&b, "\t\t'%s'\n", name)
	}
	b.WriteString("\t)\n\n\tlocal -a shells\n\tshells=('bash' 'zsh' 'fish')\n\n\t_arguments \\\n\t\t'--version[show version]' \\\n\t\t'--help[show help]' \\\n\t\t'1:command:_describe command commands' \\\n\t\t'2:provider:_describe provider providers' \\\n\t\t'*::arg:->args'\n}\n_cs\n")
	return b.String()
}

func fishCompletionString() string {
	return fmt.Sprintf(`# code-switch fish completion
complete -c cs -f

complete -c cs -n '__fish_use_subcommand' -a 'list' -d 'List available providers'
complete -c cs -n '__fish_use_subcommand' -a 'configure' -d 'Interactive TUI configuration'
complete -c cs -n '__fish_use_subcommand' -a 'current' -d 'Show current provider'
complete -c cs -n '__fish_use_subcommand' -a 'set-key' -d 'Save API key for a provider'
complete -c cs -n '__fish_use_subcommand' -a 'switch' -d 'Switch agent provider'
complete -c cs -n '__fish_use_subcommand' -a 'env' -d 'Print shell exports for a provider'
complete -c cs -n '__fish_use_subcommand' -a 'token' -d 'Print raw API token for command-backed auth'
complete -c cs -n '__fish_use_subcommand' -a 'restore' -d 'Restore official agent config'
complete -c cs -n '__fish_use_subcommand' -a 'test' -d 'Test provider API connectivity'
complete -c cs -n '__fish_use_subcommand' -a 'remove' -d 'Remove saved provider config'
complete -c cs -n '__fish_use_subcommand' -a 'upgrade' -d 'Upgrade to latest release'
complete -c cs -n '__fish_use_subcommand' -a 'completion' -d 'Generate shell completion'
complete -c cs -n '__fish_use_subcommand' -a 'help' -d 'Show help'

complete -c cs -n '__fish_seen_subcommand_from switch set-key env token test remove' -a '%s'
complete -c cs -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'

complete -c cs -l version -d 'Show version'
complete -c cs -l help -d 'Show help'
`, providerCompletionWordList())
}

func providerCompletionWordList() string {
	names := sortedPresetNames()
	cfg, _, err := loadAppConfig()
	if err == nil {
		for name, stored := range cfg.Providers {
			if strings.TrimSpace(stored.BaseURL) != "" && !isPresetProvider(name) {
				names = append(names, name)
			}
		}
	}
	// Weak dependency: failing to load app config for shell completion
	// gracefully falls back to preset-only completion words.
	sort.Strings(names)
	return strings.Join(names, " ")
}

func sortedPresetNames() []string {
	names := make([]string, 0, len(providerPresets))
	for name := range providerPresets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func printUsage(out io.Writer) {
	fmt.Fprint(out, "code-switch\n\nUsage:\n  cs --version\n  cs version\n  cs list [--agent claude|codex] [--verbose]\n  cs [--dry-run] [--reset-key]         # interactive TUI\n  cs configure [--agent claude|codex] [--dry-run] [--reset-key]\n  cs current [--agent claude|codex] [--claude-dir DIR] [--codex-dir DIR]\n  cs set-key <provider> <api-key> [--agent claude|codex]\n  cs switch <provider> [--agent claude|codex] [--api-key sk-xxx] [--model model-id] [--haiku model] [--sonnet model] [--opus model] [--subagent model] [--claude-dir DIR] [--codex-dir DIR] [--dry-run]\n  cs env <provider> [--agent claude|codex] [--api-key sk-xxx]\n  cs token <provider> [--agent claude|codex] [--api-key sk-xxx]\n  cs restore [--agent claude|codex] [--dry-run]\n  cs test <provider> [--agent claude|codex] [--api-key sk-xxx] [--model model-id] [--path /custom/api/path]\n  cs remove <provider> [--agent claude|codex] [--force]\n  cs upgrade [--dry-run] [--tag vX.Y.Z]\n  cs completion bash|zsh|fish\n\nClaude providers:\n")
	for _, name := range sortedPresetNames() {
		fmt.Fprintf(out, "  %s\n", name)
	}
	fmt.Fprint(out, "\nCodex providers:\n  deepseek\n  ollama-cloud\n  openrouter\n")
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
