package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

var supportedEnvShells = map[string]bool{
	"bash": true,
	"sh":   true,
	"zsh":  true,
	"fish": true,
	"pwsh": true,
}

type envPair struct {
	Key   string
	Value string
}

func cmdEnv(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	shell := fs.String("shell", "bash", "shell syntax: bash, sh, zsh, fish, or pwsh (powershell)")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch env <provider> [--agent claude|codex|opencode] [--api-key sk-xxx] [--shell bash|sh|zsh|fish|pwsh]")
	}
	shellName := strings.ToLower(strings.TrimSpace(*shell))
	if !supportedEnvShells[shellName] {
		return fmt.Errorf("unsupported shell %q, use bash, sh, zsh, fish, or pwsh", *shell)
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}

	pa, cfg, _, err := resolveProviderAndKeyForAgent(agent, providerArg, *apiKey, "")
	if err != nil {
		return err
	}
	preset, err := resolveAgentSwitchPreset(agent, pa.Provider, cfg, "")
	if err != nil {
		return err
	}

	comment, pairs := buildEnvExports(agent, preset, pa.APIKey)
	renderEnvExports(out, shellName, comment, pairs)
	return nil
}

// buildEnvExports assembles the ordered environment pairs for an agent's preset.
// The ordering and content reproduce the historical POSIX output exactly so that
// the default `--shell bash` rendering is byte-identical to prior versions.
func buildEnvExports(agent AgentName, preset ProviderPreset, apiKey string) (string, []envPair) {
	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}
	pairs := make([]envPair, 0, 12)
	add := func(k, v string) { pairs = append(pairs, envPair{Key: k, Value: v}) }

	add("ANTHROPIC_BASE_URL", preset.BaseURL)
	if !preset.NoModel {
		add("ANTHROPIC_MODEL", preset.Model)
	}
	add(authEnv, apiKey)
	if agent == agentClaude && !preset.NoModel {
		if preset.Haiku != "" {
			add("ANTHROPIC_DEFAULT_HAIKU_MODEL", preset.Haiku)
		}
		if preset.Sonnet != "" {
			add("ANTHROPIC_DEFAULT_SONNET_MODEL", preset.Sonnet)
		}
		if preset.Opus != "" {
			add("ANTHROPIC_DEFAULT_OPUS_MODEL", preset.Opus)
		}
		if preset.Subagent != "" {
			add("CLAUDE_CODE_SUBAGENT_MODEL", preset.Subagent)
		}
	}
	if agent != agentOpencode && preset.ReasoningEffort != "" {
		add("CLAUDE_CODE_EFFORT_LEVEL", preset.ReasoningEffort)
	}
	if agent != agentOpencode {
		for _, key := range sortedExtraEnv(preset.ExtraEnv) {
			val := preset.ExtraEnv[key]
			if val == nil {
				continue
			}
			add(key, fmt.Sprint(val))
		}
	}

	comment := ""
	switch agent {
	case agentCodex:
		comment = "# Codex uses config.toml with command-based auth; these env vars are for reference or other tools:"
	case agentOpencode:
		comment = "# OpenCode uses env-based auth; export these variables:"
	}
	return comment, pairs
}

// renderEnvExports writes the environment pairs in the requested shell's syntax.
func renderEnvExports(out io.Writer, shell, comment string, pairs []envPair) {
	switch shell {
	case "fish":
		if comment != "" {
			fmt.Fprintln(out, comment)
		}
		for _, p := range pairs {
			fmt.Fprintf(out, "set -gx %s %s\n", p.Key, shellSingleQuote(p.Value))
		}
	case "pwsh":
		if comment != "" {
			fmt.Fprintln(out, comment)
		}
		for _, p := range pairs {
			fmt.Fprintf(out, "$env:%s = %s\n", p.Key, powerShellSingleQuote(p.Value))
		}
	default: // bash, sh, zsh — POSIX export
		if comment != "" {
			fmt.Fprintln(out, comment)
		}
		for _, p := range pairs {
			fmt.Fprintf(out, "export %s=%s\n", p.Key, shellSingleQuote(p.Value))
		}
	}
}

func cmdToken(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch token <provider> [--agent claude|codex|opencode] [--api-key sk-xxx]")
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	if strings.TrimSpace(providerArg) == "code-switch-proxy" {
		cfg, _, err := loadAppConfig()
		if err != nil {
			return err
		}
		if cfg.Proxy == nil || cfg.Proxy.Routes == nil {
			return fmt.Errorf("proxy route for agent %q is not configured", agent)
		}
		route, ok := cfg.Proxy.Routes[string(agent)]
		if !ok || strings.TrimSpace(route.Token) == "" {
			return fmt.Errorf("proxy route for agent %q has no token", agent)
		}
		fmt.Fprintln(out, strings.TrimSpace(route.Token))
		return nil
	}
	pa, _, _, err := resolveProviderAndKeyForAgent(agent, providerArg, *apiKey, "")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, pa.APIKey)
	return nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func powerShellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func sortedExtraEnv(extra map[string]any) []string {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
