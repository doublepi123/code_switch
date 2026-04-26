package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func cmdSwitch(args []string) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	apiKey := fs.String("api-key", "", "API key for the target provider")
	model := fs.String("model", "", "override model id")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return errors.New("usage: claude-switch switch <provider> [--api-key sk-xxx] [--model model-id]")
	}

	provider := canonicalProviderName(providerArg)
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", providerArg)
	}

	key := strings.TrimSpace(*apiKey)
	if key == "" {
		key = strings.TrimSpace(cfg.Providers[provider].APIKey)
	}
	if key == "" {
		return fmt.Errorf("missing api key for %s, run `claude-switch set-key %s <api-key>` or pass --api-key", provider, provider)
	}

	return switchProvider(provider, cfg, key, strings.TrimSpace(*model), *claudeDir, os.Stdout)
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
	case "-api-key", "--api-key", "-model", "--model", "-claude-dir", "--claude-dir":
		return true
	default:
		return false
	}
}

func switchProvider(provider string, cfg *AppConfig, apiKey, modelOverride, claudeDir string, out io.Writer) error {
	preset, err := resolveSwitchPreset(provider, cfg, modelOverride)
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

	applyPreset(root, preset, apiKey, "")
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		return err
	}

	fmt.Fprintf(out, "switched Claude to %s\n", preset.Name)
	fmt.Fprintf(out, "settings: %s\n", settingsPath)
	fmt.Fprintf(out, "base_url: %s\n", preset.BaseURL)
	fmt.Fprintf(out, "model: %s\n", preset.Model)
	return nil
}

func applyPreset(root map[string]any, preset ProviderPreset, apiKey, overrideModel string) {
	env := ensureNestedMap(root, "env")
	for _, key := range managedEnvKeys {
		delete(env, key)
	}

	preset = withSelectedModel(preset, overrideModel)
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

	for key, value := range preset.ExtraEnv {
		env[key] = value
	}
}
