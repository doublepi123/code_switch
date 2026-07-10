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

var csCommandNames = []string{
	"list", "models", "model", "model-map", "use-model", "configure", "current",
	"set-key", "switch", "default", "env", "token", "restore", "diff", "upgrade",
	"test", "remove", "backups", "doctor", "export", "import", "completion", "run", "proxy", "help",
}

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
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(out)
		return nil
	}
	if strings.HasPrefix(args[0], "-") {
		return cmdConfigure(args, in, out)
	}

	switch args[0] {
	case "list":
		return cmdList(args[1:], out)
	case "models":
		return cmdModels(args[1:], out)
	case "model":
		return cmdModel(args[1:], out)
	case "model-map":
		return cmdModelMap(args[1:], out)
	case "use-model":
		return cmdUseModel(args[1:], out)
	case "configure":
		return cmdConfigure(args[1:], in, out)
	case "current":
		return cmdCurrent(args[1:], out)
	case "set-key":
		return cmdSetKey(args[1:], out)
	case "switch":
		return cmdSwitchWithOutput(args[1:], out)
	case "default":
		return cmdDefault(args[1:], out)
	case "env":
		return cmdEnv(args[1:], out)
	case "token":
		return cmdToken(args[1:], out)
	case "restore":
		return cmdRestore(args[1:], out)
	case "diff":
		return cmdDiff(args[1:], out)
	case "upgrade":
		return cmdUpgrade(args[1:], out)
	case "test":
		return cmdTest(args[1:], out)
	case "remove":
		return cmdRemove(args[1:], in, out)
	case "backups":
		return cmdBackups(args[1:], out)
	case "doctor":
		return cmdDoctor(args[1:], out)
	case "export":
		return cmdExport(args[1:], out)
	case "import":
		return cmdImport(args[1:], in, out)
	case "completion":
		return cmdCompletion(args[1:], out)
	case "run":
		return cmdRun(args[1:], out)
	case "proxy":
		return cmdProxy(args[1:], out)
	case "mcp":
		return cmdMCP(args[1:], out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printVersion(out io.Writer) {
	fmt.Fprintf(out, "code-switch %s\n", version)
}

func isVersionRequest(args []string) bool {
	if len(args) == 1 {
		return args[0] == "--version" || args[0] == "-version" || args[0] == "version"
	}
	if len(args) == 2 && (args[1] == "--version" || args[1] == "-version") {
		for _, cmd := range csCommandNames {
			if args[0] == cmd {
				return true
			}
		}
	}
	return false
}

func cmdList(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	verbose := fs.Bool("verbose", false, "show all available models for each provider")
	jsonOut := fs.Bool("json", false, "output provider list as JSON")
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
	if *jsonOut {
		return renderListJSON(out, agent, cfg)
	}
	names := providerNamesForAgent(agent, cfg, false, false)
	for _, name := range names {
		preset, err := resolveAgentProviderPreset(agent, name, cfg)
		if err != nil {
			// Skip providers we can't resolve (e.g. a custom provider whose
			// preset was removed in a later version). Continue listing the
			// remaining providers so one broken entry doesn't make the whole
			// command unusable.
			fmt.Fprintf(os.Stderr, "warning: skip provider %q: %v\n", name, err)
			continue
		}
		keyStatus := "✗"
		if preset.NoAPIKey {
			keyStatus = "—"
		} else if storedAPIKeyForAgent(cfg, agent, name) != "" {
			keyStatus = "✓"
		}
		modelLabel := preset.Model
		if preset.NoModel {
			modelLabel = "auto"
		}
		if *verbose {
			models := providerModelsForAgent(cfg, agent, name)
			if len(models) == 0 {
				models = preset.Models
			}
			fmt.Fprintf(out, "%s\t%s\t%s\t%v\t%s\n", name, preset.BaseURL, modelLabel, models, keyStatus)
		} else {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", name, preset.BaseURL, modelLabel, keyStatus)
		}
	}
	return nil
}

func cmdCurrent(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("current", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", "", "target agent: claude, codex, or opencode (default: show both)")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
	jsonOut := fs.Bool("json", false, "output current configuration as JSON")
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

	if *jsonOut {
		return renderCurrentJSON(out, *claudeDir, *codexDir, *opencodeDir, showBoth, agent)
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
		} else if route, ok := proxyRouteForLocalConfig(agentClaude, baseURL, stringFromMap(env, "ANTHROPIC_AUTH_TOKEN")); ok {
			fmt.Fprintf(out, "  %s\n", formatLabel("mode", "proxy"))
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", canonicalProviderName(route.Provider)))
			fmt.Fprintf(out, "  %s\n", formatLabel("upstream_protocol", route.UpstreamProtocol))
			fmt.Fprintf(out, "  %s\n", formatLabel("daemon", proxyDaemonStatusText()))
			fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
		} else {
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", detectProvider(baseURL, model)))
			fmt.Fprintf(out, "  %s\n", formatLabel("mode", "direct"))
			fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
			if model != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
			}
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
		} else if route, ok := proxyRouteForLocalConfig(agentCodex, baseURL, ""); ok {
			fmt.Fprintf(out, "  %s\n", formatLabel("mode", "proxy"))
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", canonicalProviderName(route.Provider)))
			fmt.Fprintf(out, "  %s\n", formatLabel("upstream_protocol", route.UpstreamProtocol))
			fmt.Fprintf(out, "  %s\n", formatLabel("daemon", proxyDaemonStatusText()))
			fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
			if model != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
			}
		} else {
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", providerKey))
			fmt.Fprintf(out, "  %s\n", formatLabel("mode", "direct"))
			if baseURL != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
			}
			if model != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
			}
		}
		if showBoth {
			fmt.Fprintln(out)
		}
	}

	if showBoth || agent == agentOpencode {
		configPath, model, baseURL, authEnv, providerName, err := currentOpencodeProvider(*opencodeDir)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "OpenCode\n")
		fmt.Fprintf(out, "  %s\n", formatLabel("config", configPath))
		if baseURL == "" {
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", "unknown"))
		} else if route, ok := proxyRouteForLocalConfig(agentOpencode, baseURL, ""); ok {
			fmt.Fprintf(out, "  %s\n", formatLabel("mode", "proxy"))
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", canonicalProviderName(route.Provider)))
			fmt.Fprintf(out, "  %s\n", formatLabel("upstream_protocol", route.UpstreamProtocol))
			fmt.Fprintf(out, "  %s\n", formatLabel("daemon", proxyDaemonStatusText()))
			fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
			if model != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
			}
		} else {
			displayProvider := providerName
			if displayProvider == "" {
				displayProvider = detectProvider(baseURL, model)
			}
			fmt.Fprintf(out, "  %s\n", formatLabel("provider", displayProvider))
			if baseURL != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("base_url", baseURL))
			}
			if model != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("model", model))
			}
			if authEnv != "" {
				fmt.Fprintf(out, "  %s\n", formatLabel("api_key_env", authEnv))
			}
		}
	}
	return nil
}

func stringFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func cmdSetKey(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("set-key", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: code-switch set-key <provider> <api-key> [--agent claude|codex|opencode]")
	}

	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	provider := canonicalProviderName(remaining[0])

	if agent == agentCodex {
		preset, err := presetForAgentDirectProtocol(agentCodex, provider)
		if err != nil {
			return err
		}
		if preset.NoAPIKey {
			return fmt.Errorf("provider %q does not require an API key", provider)
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

	if agent == agentOpencode {
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			return err
		}
		defer unlock()
		preset, err := resolveProviderPreset(provider, cfg)
		if err != nil {
			// Use the canonical name (e.g. "zhipu-cn") rather than the raw
			// alias the user typed (e.g. "bigModel") so the error reflects
			// what the lookup actually tried, and so the alias the user
			// thought they were using is at least visible if they want to
			// debug a typo.
			return fmt.Errorf("unsupported provider %q for agent opencode (canonicalized from %q)", provider, remaining[0])
		}
		if preset.NoAPIKey {
			return fmt.Errorf("provider %q does not require an API key", provider)
		}
		agentCfg := agentConfig(cfg, agentOpencode)
		stored := agentCfg.Providers[provider]
		stored.APIKey = remaining[1]
		agentCfg.Providers[provider] = stored
		cfg.Agents[string(agentOpencode)] = agentCfg
		if err := writeJSONAtomic(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "saved api key for %s (opencode) in %s\n", provider, path)
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
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	force := fs.Bool("force", false, "skip confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	providerArg := strings.TrimSpace(fs.Arg(0))
	if providerArg == "" {
		return fmt.Errorf("usage: code-switch remove <provider> [--agent claude|codex|opencode] [--force]")
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
		if err := writeJSONAtomic(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed %s (codex) from %s\n", provider, path)
		return nil
	}

	if agent == agentOpencode {
		agentCfg := agentConfig(cfg, agentOpencode)
		_, ok := agentCfg.Providers[provider]
		if !ok {
			return fmt.Errorf("no saved configuration for provider %q for agent opencode", provider)
		}
		if !*force {
			stored := opencodeProviderConfig(cfg, provider)
			showKey := maskAPIKey(stored.APIKey)
			fmt.Fprintf(out, "Remove saved config for %s (opencode, key: %s)? [y/N]: ", provider, showKey)
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
		cfg.Agents[string(agentOpencode)] = agentCfg
		if err := writeJSONAtomic(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed %s (opencode) from %s\n", provider, path)
		return nil
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
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s from %s\n", provider, path)
	return nil
}
func cmdCompletion(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: code-switch completion bash|zsh|fish")
	}
	if len(args) > 1 {
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
		COMPREPLY=($(compgen -W "list models model model-map use-model configure current set-key switch default env token restore diff test remove backups doctor export import upgrade run proxy completion help --version --help" -- "$cur"))
		;;
	2)
		case ${words[1]} in
		switch|set-key|env|token|test|remove|models|default|use-model|diff)
			COMPREPLY=($(compgen -W "%s" -- "$cur"))
			;;
		model)
			COMPREPLY=($(compgen -W "get set list" -- "$cur"))
			;;
		model-map)
			COMPREPLY=($(compgen -W "set get list remove" -- "$cur"))
			;;
		completion)
			COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur"))
			;;
		proxy)
			COMPREPLY=($(compgen -W "configure start stop status preview serve" -- "$cur"))
			;;
		esac
		;;
	*)
		# --via value completion for: cs switch <provider> --via <TAB>
		if [ ${words[1]} = switch ] && [ $cword -ge 3 ]; then
			if [ "${words[cword-1]}" = "--via" ] || [ "${words[cword-1]}" = "-via" ]; then
				COMPREPLY=($(compgen -W "auto direct proxy" -- "$cur"))
			fi
		fi
		;;
	esac
}
complete -F _cs cs
`, providerCompletionWordList())
}

func zshCompletionString() string {
	var b strings.Builder
	b.WriteString("#compdef cs\n\n_cs() {\n\tlocal -a commands\n\tcommands=(\n\t\t'list:list available providers'\n\t\t'models:list models for a provider'\n\t\t'model:get/set/list the default model for a provider'\n\t\t'model-map:map client model names to upstream provider models'\n\t\t'use-model:set a provider default model and the proxy default mapping in one step'\n\t\t'configure:interactive TUI configuration'\n\t\t'current:show current provider'\n\t\t'set-key:save API key for a provider'\n\t\t'switch:switch agent provider [--via auto|direct|proxy]'\n\t\t'default:get/set the default provider'\n\t\t'env:print shell exports for a provider'\n\t\t'token:print raw API token for command-backed auth'\n\t\t'restore:restore official agent config'\n\t\t'diff:preview env changes for a switch'\n\t\t'test:test provider API connectivity'\n\t\t'remove:remove saved provider config'\n\t\t'upgrade:upgrade to latest release'\n\t\t'backups:list or prune config backups'\n\t\t'doctor:health-check configs and permissions'\n\t\t'export:dump app config to stdout'\n\t\t'import:merge an exported config'\n\t\t'completion:generate shell completion'\n\t\t'run:launch an agent through the local code-switch proxy (MVP: --dry-run only)'\n\t\t'proxy:configure/preview/status/start/stop/serve the multi-route code-switch proxy'\n\t\t'help:show help'\n\t)\n\n\tlocal -a providers\n\tproviders=(\n")
	for _, name := range sortedPresetNames() {
		fmt.Fprintf(&b, "\t\t'%s'\n", name)
	}
	b.WriteString("\t)\n\n\tlocal -a shells\n\tshells=('bash' 'zsh' 'fish')\n\n\tlocal -a proxy_subcommands\n\tproxy_subcommands=(\n\t\t'configure:write a proxy route for an agent (multi-route daemon)'\n\t\t'preview:show the resolved proxy route and codex config for an agent'\n\t\t'status:show proxy daemon runtime status (all configured routes)'\n\t\t'start:launch the multi-route proxy daemon as a background process'\n\t\t'stop:terminate a running proxy daemon'\n\t\t'serve:run the multi-route proxy HTTP daemon in the foreground'\n\t)\n\n\t_arguments \\\n\t\t'--version[show version]' \\\n\t\t'--help[show help]' \\\n\t\t'--via[connection mode for switch]:connection mode:(auto direct proxy)' \\\n\t\t'1:command:_describe command commands' \\\n\t\t'2: :->second' \\\n\t\t'*::arg:->args'\n\n\tcase $state in\n\tsecond)\n\t\tcase ${words[1]} in\n\t\tswitch)\n\t\t\t_values 'provider' $providers\n\t\t\t;;\n\t\tset-key|env|token|test|remove|models|default|model-map|use-model|diff)\n\t\t\t_values 'provider' $providers\n\t\t\t;;\n\t\tmodel)\n\t\t\t_values 'subcommand' 'get' 'set' 'list'\n\t\t\t;;\n\t\tmodel-map)\n\t\t\t_values 'subcommand' 'set' 'get' 'list' 'remove'\n\t\t\t;;\n\t\tcompletion)\n\t\t\t_values 'shell' $shells\n\t\t\t;;\n\t\tproxy)\n\t\t\t_describe 'proxy subcommand' proxy_subcommands\n\t\t\t;;\n\t\tesac\n\t\t;;\n\tesac\n}\n_cs\n")
	return b.String()
}

func fishCompletionString() string {
	return fmt.Sprintf(`# code-switch fish completion
complete -c cs -f

complete -c cs -n '__fish_use_subcommand' -a 'list' -d 'List available providers'
complete -c cs -n '__fish_use_subcommand' -a 'models' -d 'List models for a provider'
complete -c cs -n '__fish_use_subcommand' -a 'model' -d 'Get/set/list the default model for a provider'
complete -c cs -n '__fish_use_subcommand' -a 'model-map' -d 'Map client model names to upstream provider models'
complete -c cs -n '__fish_use_subcommand' -a 'use-model' -d 'Set a provider default model and the proxy default mapping in one step'
complete -c cs -n '__fish_use_subcommand' -a 'configure' -d 'Interactive TUI configuration'
complete -c cs -n '__fish_use_subcommand' -a 'current' -d 'Show current provider'
complete -c cs -n '__fish_use_subcommand' -a 'set-key' -d 'Save API key for a provider'
complete -c cs -n '__fish_use_subcommand' -a 'switch' -d 'Switch agent provider (--via auto|direct|proxy)'
complete -c cs -n '__fish_use_subcommand' -a 'default' -d 'Get or set the default provider'
complete -c cs -n '__fish_use_subcommand' -a 'env' -d 'Print shell exports for a provider'
complete -c cs -n '__fish_use_subcommand' -a 'token' -d 'Print raw API token for command-backed auth'
complete -c cs -n '__fish_use_subcommand' -a 'restore' -d 'Restore official agent config'
complete -c cs -n '__fish_use_subcommand' -a 'diff' -d 'Preview env changes for a switch'
complete -c cs -n '__fish_use_subcommand' -a 'test' -d 'Test provider API connectivity'
complete -c cs -n '__fish_use_subcommand' -a 'remove' -d 'Remove saved provider config'
complete -c cs -n '__fish_use_subcommand' -a 'upgrade' -d 'Upgrade to latest release'
complete -c cs -n '__fish_use_subcommand' -a 'backups' -d 'List or prune config backups'
complete -c cs -n '__fish_use_subcommand' -a 'doctor' -d 'Health-check configs and permissions'
complete -c cs -n '__fish_use_subcommand' -a 'export' -d 'Dump app config to stdout'
complete -c cs -n '__fish_use_subcommand' -a 'import' -d 'Merge an exported config'
complete -c cs -n '__fish_use_subcommand' -a 'completion' -d 'Generate shell completion'
complete -c cs -n '__fish_use_subcommand' -a 'run' -d 'Launch an agent through the local code-switch proxy (MVP: --dry-run only)'
complete -c cs -n '__fish_use_subcommand' -a 'proxy' -d 'Configure/control the multi-route code-switch proxy daemon'
complete -c cs -n '__fish_use_subcommand' -a 'help' -d 'Show help'

complete -c cs -n '__fish_seen_subcommand_from switch set-key env token test remove models default model model-map use-model diff' -a '%s'
complete -c cs -n '__fish_seen_subcommand_from switch' -l via -d 'Connection mode' -a 'auto direct proxy' -r
complete -c cs -n '__fish_seen_subcommand_from model' -a 'get set list'
complete -c cs -n '__fish_seen_subcommand_from model-map' -a 'set get list remove'
complete -c cs -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c cs -n '__fish_seen_subcommand_from proxy' -a 'configure start stop status preview serve'

complete -c cs -l version -d 'Show version'
complete -c cs -l help -d 'Show help'
`, providerCompletionWordList())
}

func providerCompletionWordList() string {
	return providerCompletionWordListForAgent(agentClaude)
}

func providerCompletionWordListForAgent(agent AgentName) string {
	names := presetNamesForAgentDirectProtocols(agent)
	cfg, _, err := loadAppConfig()
	if err == nil {
		profile, ok := agentProfiles[agent]
		if ok {
			for name, stored := range cfg.Providers {
				if strings.TrimSpace(stored.BaseURL) == "" || isPresetProvider(name) {
					continue
				}
				protocol := stored.providerProtocol()
				for _, allowed := range profile.DirectProtocols {
					if protocol == allowed {
						names = append(names, name)
						break
					}
				}
			}
		}
	}
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
	var b strings.Builder
	b.WriteString("code-switch\n\nUsage:\n")
	b.WriteString("  cs --version\n")
	b.WriteString("  cs version\n")
	b.WriteString("  cs list [--agent claude|codex|opencode] [--verbose] [--json]\n")
	b.WriteString("  cs models [provider] [--agent claude|codex|opencode] [--api-key sk-xxx] [--json]\n")
	b.WriteString("  cs model get <provider>                      # show the provider's default model\n")
	b.WriteString("  cs model set <provider> <model>              # persist the provider's default model (key untouched)\n")
	b.WriteString("  cs model list <provider>                     # list the provider's available models\n")
	b.WriteString("  cs model-map set <provider> <client-model> <upstream-model>\n")
	b.WriteString("  cs model-map get <provider> [client-model]\n")
	b.WriteString("  cs model-map list <provider>\n")
	b.WriteString("  cs model-map remove <provider> <client-model>\n")
	b.WriteString("  cs use-model <provider> <model>              # = model set + model-map default set\n")
	b.WriteString("  cs [--dry-run] [--reset-key]         # interactive TUI\n")
	b.WriteString("  cs configure [--agent claude|codex|opencode] [--dry-run] [--reset-key]\n")
	b.WriteString("  cs current [--agent claude|codex|opencode] [--claude-dir DIR] [--codex-dir DIR] [--opencode-dir DIR]\n")
	b.WriteString("  cs set-key <provider> <api-key> [--agent claude|codex|opencode]\n")
	b.WriteString("  cs switch <provider> [--agent claude|codex|opencode] [--via auto|direct|proxy] [--api-key sk-xxx] [--model model-id] [--haiku model] [--sonnet model] [--opus model] [--subagent model] [--claude-dir DIR] [--codex-dir DIR] [--opencode-dir DIR] [--dry-run]\n")
	b.WriteString("  cs default [provider] [--clear]            # get/set the default provider used by bare `cs switch`\n")
	b.WriteString("  cs env <provider> [--agent claude|codex|opencode] [--api-key sk-xxx] [--shell bash|fish|pwsh]\n")
	b.WriteString("  cs token <provider> [--agent claude|codex|opencode] [--api-key sk-xxx]\n")
	b.WriteString("  cs restore [--agent claude|codex|opencode] [--dry-run]\n")
	b.WriteString("  cs diff <provider> [--model model-id] [--api-key sk-xxx] [--claude-dir DIR]   # preview env-var changes vs current settings\n")
	b.WriteString("  cs test <provider> [--agent claude|codex|opencode] [--api-key sk-xxx] [--model model-id] [--path /custom/api/path] [--all]\n")
	b.WriteString("  cs remove <provider> [--agent claude|codex|opencode] [--force]\n")
	b.WriteString("  cs upgrade [--dry-run] [--tag vX.Y.Z]\n")
	b.WriteString("  cs backups list|prune [--keep N] [--days N] [--all] [--dry-run] [--json]\n")
	b.WriteString("  cs doctor [--json]                        # health-check configs, permissions, drift\n")
	b.WriteString("  cs export [--redact-keys]                 # dump app config to stdout (for another machine)\n")
	b.WriteString("  cs import <file> [--force]                # merge an exported config into your app config\n")
	b.WriteString("  cs mcp list                               # list configured MCP servers\n")
	b.WriteString("  cs mcp add <name> --transport stdio --command <cmd> [-- arg1 arg2 ...]\n")
	b.WriteString("  cs mcp add <name> --transport sse --url <url>\n")
	b.WriteString("  cs mcp remove <name>                      # remove a configured MCP server\n")
	b.WriteString("  cs mcp test <name>                        # probe an MCP server\n")
	b.WriteString("  cs completion bash|zsh|fish\n")
	b.WriteString("  cs run <agent> --provider <provider> [--model model-id] [--dry-run]   # MVP: codex --dry-run only\n")
	b.WriteString("  cs proxy configure <agent> --provider <provider> [--model model] [--protocol protocol] [--host host] [--port port]   # write one route of the multi-route daemon\n")
	b.WriteString("  cs proxy preview <agent>                  # show the resolved proxy route + codex config for <agent>\n")
	b.WriteString("  cs proxy status                            # show proxy runtime status (all configured routes)\n")
	b.WriteString("  cs proxy start                            # launch the multi-route proxy daemon as a background process\n")
	b.WriteString("  cs proxy stop                             # terminate a running proxy daemon\n")
	b.WriteString("  cs proxy serve                            # run the multi-route proxy HTTP daemon in the foreground\n")
	b.WriteString("\nClaude providers:\n")
	fmt.Fprint(out, b.String())
	for _, name := range presetNamesForAgentDirectProtocols(agentClaude) {
		fmt.Fprintf(out, "  %s\n", name)
	}
	fmt.Fprint(out, "\nCodex providers:\n")
	for _, name := range presetNamesForAgentDirectProtocols(agentCodex) {
		fmt.Fprintf(out, "  %s\n", name)
	}
	fmt.Fprint(out, "\nOpenCode providers:\n")
	for _, name := range presetNamesForAgentDirectProtocols(agentOpencode) {
		fmt.Fprintf(out, "  %s\n", name)
	}
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
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
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
	if parsed.User != nil {
		return fmt.Errorf("base URL must not contain embedded credentials; pass credentials via --api-key or stored config")
	}
	// Reject fragments and query strings: a base URL is the upstream origin
	// root, not a request URL. A trailing "?key=abc" or "#fragment" would be
	// silently embedded in every outbound request, leaking credentials or
	// producing surprising request shapes.
	if parsed.Fragment != "" || parsed.RawQuery != "" {
		return fmt.Errorf("base URL must not contain a query string or fragment")
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
