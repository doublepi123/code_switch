package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

func cmdRestore(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	dryRun := fs.Bool("dry-run", false, "preview what would be restored without modifying config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch restore [--agent claude|codex] [--dry-run]")
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	switch agent {
	case agentCodex:
		return restoreCodexConfig(*codexDir, cfg, out, *dryRun)
	default:
		return restoreClaudeConfig(*claudeDir, out, *dryRun)
	}
}

func restoreClaudeConfig(claudeDir string, out io.Writer, dryRun bool) error {
	settingsPath := claudeSettingsPath(claudeDir)
	if dryRun {
		fmt.Fprintf(out, "[dry-run] would restore Claude official config\n")
		fmt.Fprintf(out, "[dry-run] settings: %s\n", settingsPath)
		return nil
	}
	root, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}
	if err := backupIfExists(settingsPath); err != nil {
		return err
	}
	if env := nestedMap(root, "env"); env != nil {
		for _, key := range managedEnvKeys {
			delete(env, key)
		}
		if len(env) == 0 {
			delete(root, "env")
		}
	}
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", successPrefix("restored Claude official config"))
	fmt.Fprintf(out, "%s\n", formatLabel("settings", settingsPath))
	return nil
}
