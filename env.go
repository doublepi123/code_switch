package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

func cmdEnv(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("env", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch env <provider> [--agent claude|codex] [--api-key sk-xxx]")
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

	if agent == agentCodex {
		fmt.Fprintf(out, "# Codex uses command-based auth; set these env vars for shell use:\n")
		fmt.Fprintf(out, "export ANTHROPIC_BASE_URL=%s\n", shellSingleQuote(preset.BaseURL))
		fmt.Fprintf(out, "export ANTHROPIC_MODEL=%s\n", shellSingleQuote(preset.Model))
		authEnv := strings.TrimSpace(preset.AuthEnv)
		if authEnv == "" {
			authEnv = "ANTHROPIC_API_KEY"
		}
		fmt.Fprintf(out, "export %s=%s\n", authEnv, shellSingleQuote(pa.APIKey))
		if preset.ReasoningEffort != "" {
			fmt.Fprintf(out, "export CLAUDE_CODE_EFFORT_LEVEL=%s\n", shellSingleQuote(preset.ReasoningEffort))
		}
		for _, key := range sortedExtraEnv(preset.ExtraEnv) {
			val := preset.ExtraEnv[key]
			if val == nil {
				continue
			}
			fmt.Fprintf(out, "export %s=%s\n", key, shellSingleQuote(fmt.Sprint(val)))
		}
		return nil
	}

	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}
	fmt.Fprintf(out, "export ANTHROPIC_BASE_URL=%s\n", shellSingleQuote(preset.BaseURL))
	fmt.Fprintf(out, "export ANTHROPIC_MODEL=%s\n", shellSingleQuote(preset.Model))
	fmt.Fprintf(out, "export %s=%s\n", authEnv, shellSingleQuote(pa.APIKey))
	if preset.Haiku != "" {
		fmt.Fprintf(out, "export ANTHROPIC_DEFAULT_HAIKU_MODEL=%s\n", shellSingleQuote(preset.Haiku))
	}
	if preset.Sonnet != "" {
		fmt.Fprintf(out, "export ANTHROPIC_DEFAULT_SONNET_MODEL=%s\n", shellSingleQuote(preset.Sonnet))
	}
	if preset.Opus != "" {
		fmt.Fprintf(out, "export ANTHROPIC_DEFAULT_OPUS_MODEL=%s\n", shellSingleQuote(preset.Opus))
	}
	if preset.Subagent != "" {
		fmt.Fprintf(out, "export CLAUDE_CODE_SUBAGENT_MODEL=%s\n", shellSingleQuote(preset.Subagent))
	}
	if preset.ReasoningEffort != "" {
		fmt.Fprintf(out, "export CLAUDE_CODE_EFFORT_LEVEL=%s\n", shellSingleQuote(preset.ReasoningEffort))
	}
	for _, key := range sortedExtraEnv(preset.ExtraEnv) {
		val := preset.ExtraEnv[key]
		if val == nil {
			continue
		}
		fmt.Fprintf(out, "export %s=%s\n", key, shellSingleQuote(fmt.Sprint(val)))
	}
	return nil
}

func cmdToken(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch token <provider> [--agent claude|codex] [--api-key sk-xxx]")
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
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

func sortedExtraEnv(extra map[string]any) []string {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
