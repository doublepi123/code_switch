package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// cmdDefault manages the saved default provider used by `cs switch` when no
// provider is given. With no arguments it prints the current default; with a
// provider it sets it; with --clear it removes it.
func cmdDefault(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("default", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	clear := fs.Bool("clear", false, "clear the saved default provider")
	agentFlag := fs.String("agent", string(agentClaude), "agent used to validate the default provider")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, path, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()

	if *clear {
		if cfg.Default == "" {
			fmt.Fprintln(out, "no default provider set")
			return nil
		}
		old := cfg.Default
		cfg.Default = ""
		if err := writeJSONAtomic(path, cfg); err != nil {
			return err
		}
		fmt.Fprintf(out, "cleared default provider (was %s)\n", old)
		return nil
	}

	if fs.NArg() == 0 {
		if cfg.Default == "" {
			fmt.Fprintln(out, "no default provider set")
		} else {
			fmt.Fprintf(out, "default provider: %s\n", cfg.Default)
			fmt.Fprintln(out, "use `cs switch` (no provider) to switch to it")
		}
		return nil
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: code-switch default [provider] [--clear] [--agent claude|codex|opencode]")
	}

	provider := canonicalProviderName(fs.Arg(0))
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	if _, err := resolveAgentProviderPreset(agent, provider, cfg); err != nil {
		return fmt.Errorf("cannot set default to %q: %w", fs.Arg(0), err)
	}

	cfg.Default = provider
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "default provider set to %s\n", provider)
	return nil
}
