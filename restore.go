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
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
	dryRun := fs.Bool("dry-run", false, "preview what would be restored without modifying config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch restore [--agent claude|codex|opencode] [--dry-run]")
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	switch agent {
	case agentCodex:
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			return err
		}
		defer unlock()
		if err := restoreCodexConfig(*codexDir, cfg, out, *dryRun); err != nil {
			return err
		}
		if !*dryRun {
			cfg.ManagedMCPNames = managedMCPServerNames(cfg)
			return writeJSONAtomic(path, cfg)
		}
		return nil
	case agentOpencode:
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			return err
		}
		defer unlock()
		if err := restoreOpencodeConfig(*opencodeDir, cfg, out, *dryRun); err != nil {
			return err
		}
		if !*dryRun {
			cfg.ManagedMCPNames = managedMCPServerNames(cfg)
			return writeJSONAtomic(path, cfg)
		}
		return nil
	default:
		cfg, path, unlock, err := loadAppConfigLocked()
		if err != nil {
			return err
		}
		defer unlock()
		if err := restoreClaudeConfig(*claudeDir, cfg, out, *dryRun); err != nil {
			return err
		}
		if !*dryRun {
			cfg.ManagedMCPNames = managedMCPServerNames(cfg)
			return writeJSONAtomic(path, cfg)
		}
		return nil
	}
}

func restoreClaudeConfig(claudeDir string, cfg *AppConfig, out io.Writer, dryRun bool) error {
	settingsPath := claudeSettingsPath(claudeDir)
	if dryRun {
		fmt.Fprintf(out, "[dry-run] would restore Claude official config\n")
		fmt.Fprintf(out, "[dry-run] settings: %s\n", settingsPath)
		return nil
	}
	cf := newConfigFile(settingsPath)
	unlock, err := cf.lock()
	if err != nil {
		return err
	}
	defer unlock()
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
	removeManagedMCPFromJSON(root, cfg)
	mergeMCPConfig(root, generateClaudeMCPConfig(cfg))
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", successPrefix("restored Claude official config"))
	fmt.Fprintf(out, "%s\n", formatLabel("settings", settingsPath))
	return nil
}
