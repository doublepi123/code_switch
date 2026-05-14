package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

type providerArgs struct {
	Agent     AgentName
	Provider  string
	APIKey    string
	Model     string
	ClaudeDir string
	CodexDir  string
	DryRun    bool
}

func resolveKey(agent AgentName, cfg *AppConfig, provider, apiKeyFlag string, preset ProviderPreset) (string, error) {
	key := strings.TrimSpace(apiKeyFlag)
	if key == "" {
		if agent == agentCodex {
			key = strings.TrimSpace(codexProviderConfig(cfg, provider).APIKey)
			if key == "" {
				key = strings.TrimSpace(cfg.Providers[provider].APIKey)
			}
		} else {
			key = strings.TrimSpace(cfg.Providers[provider].APIKey)
		}
	}
	if key == "" && !preset.NoAPIKey {
		return "", fmt.Errorf("missing api key for %s, run `cs set-key %s <api-key>` or pass --api-key", provider, provider)
	}
	if key == "" {
		key = provider
	}
	return key, nil
}

func resolveProviderAndKeyFromConfig(agent AgentName, cfg *AppConfig, providerArg, apiKeyFlag, model string) (*providerArgs, error) {
	provider := canonicalProviderName(providerArg)
	preset, err := resolveAgentProviderPreset(agent, provider, cfg)
	if err != nil {
		return nil, fmt.Errorf("unsupported provider %q", providerArg)
	}
	key, err := resolveKey(agent, cfg, provider, apiKeyFlag, preset)
	if err != nil {
		return nil, err
	}
	return &providerArgs{
		Agent:    agent,
		Provider: provider,
		APIKey:   key,
		Model:    strings.TrimSpace(model),
	}, nil
}

func resolveProviderAndKey(providerArg, apiKeyFlag, model string) (*providerArgs, *AppConfig, error) {
	pa, cfg, _, err := resolveProviderAndKeyForAgent(agentClaude, providerArg, apiKeyFlag, model)
	return pa, cfg, err
}

func resolveProviderAndKeyForAgent(agent AgentName, providerArg, apiKeyFlag, model string) (*providerArgs, *AppConfig, string, error) {
	cfg, path, err := loadAppConfig()
	if err != nil {
		return nil, nil, "", err
	}
	pa, err := resolveProviderAndKeyFromConfig(agent, cfg, providerArg, apiKeyFlag, model)
	if err != nil {
		return nil, nil, "", err
	}
	return pa, cfg, path, nil
}

func cmdSwitch(args []string) error {
	return cmdSwitchWithOutput(args, os.Stdout)
}

func cmdSwitchWithOutput(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	model := fs.String("model", "", "override model id")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	dryRun := fs.Bool("dry-run", false, "preview what would be written without modifying settings.json")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return errors.New("usage: code-switch switch <provider> [--agent claude|codex] [--api-key sk-xxx] [--model model-id]")
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}

	cfg, configPath, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	pa, err := resolveProviderAndKeyFromConfig(agent, cfg, providerArg, *apiKey, *model)
	if err != nil {
		return err
	}
	if agent == agentCodex {
		if !*dryRun {
			stored := codexProviderConfig(cfg, pa.Provider)
			stored.APIKey = pa.APIKey
			stored.Model = pa.Model
			setAgentProviderConfig(cfg, agentCodex, pa.Provider, stored)
			if err := writeJSONAtomic(configPath, cfg); err != nil {
				return err
			}
		}
		return switchCodexProvider(pa.Provider, cfg, pa.APIKey, pa.Model, *codexDir, out, *dryRun)
	}
	return switchProvider(pa.Provider, cfg, pa.APIKey, pa.Model, *claudeDir, out, *dryRun)
}

func splitSwitchArgs(args []string) (string, []string) {
	provider := ""
	flagArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if provider == "" && !strings.HasPrefix(arg, "-") {
			provider = arg
			continue
		}
		flagArgs = append(flagArgs, arg)
		if switchFlagNeedsValue(arg) && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return provider, flagArgs
}

func switchFlagNeedsValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "-api-key", "--api-key", "-model", "--model", "-path", "--path", "-claude-dir", "--claude-dir", "-codex-dir", "--codex-dir", "-agent", "--agent":
		return true
	default:
		return false
	}
}

func switchProvider(provider string, cfg *AppConfig, apiKey, modelOverride, claudeDir string, out io.Writer, dryRun bool) error {
	preset, err := resolveSwitchPreset(provider, cfg, modelOverride)
	if err != nil {
		return err
	}

	settingsPath := claudeSettingsPath(claudeDir)

	if dryRun {
		fmt.Fprintf(out, "[dry-run] would switch Claude to %s\n", preset.Name)
		fmt.Fprintf(out, "[dry-run] settings: %s\n", settingsPath)
		fmt.Fprintf(out, "[dry-run] base_url: %s\n", preset.BaseURL)
		fmt.Fprintf(out, "[dry-run] model: %s\n", preset.Model)
		return nil
	}

	root, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}

	if err := backupIfExists(settingsPath); err != nil {
		return err
	}

	applyPreset(root, preset, apiKey)
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		return err
	}

	fmt.Fprintf(out, "%s\n", successPrefix(fmt.Sprintf("switched Claude to %s", preset.Name)))
	fmt.Fprintf(out, "%s\n", formatLabel("settings", settingsPath))
	fmt.Fprintf(out, "%s\n", formatLabel("base_url", preset.BaseURL))
	fmt.Fprintf(out, "%s\n", formatLabel("model", preset.Model))
	return nil
}

func applyPreset(root map[string]any, preset ProviderPreset, apiKey string) {
	env := ensureNestedMap(root, "env")
	for _, key := range managedEnvKeys {
		delete(env, key)
	}

	env["ANTHROPIC_BASE_URL"] = preset.BaseURL
	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}
	env[authEnv] = apiKey
	env["ANTHROPIC_MODEL"] = preset.Model
	env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = preset.Haiku
	env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = preset.Sonnet
	env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = preset.Opus
	if preset.Subagent != "" {
		env["CLAUDE_CODE_SUBAGENT_MODEL"] = preset.Subagent
	}

	if preset.ReasoningEffort != "" {
		env["CLAUDE_CODE_EFFORT_LEVEL"] = preset.ReasoningEffort
	}
	for key, value := range preset.ExtraEnv {
		env[key] = value
	}
}
