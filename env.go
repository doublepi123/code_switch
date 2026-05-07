package main

import (
	"flag"
	"fmt"
	"io"
	"os"
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

	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}
	fmt.Fprintf(out, "export %s=%s\n", authEnv, shellSingleQuote(pa.APIKey))
	return nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
