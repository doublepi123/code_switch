package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// cmdExport writes the app config (the same JSON stored in config.json) to
// stdout so it can be moved to another machine. Use --redact-keys to share a
// provider setup without leaking secrets.
func cmdExport(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	redact := fs.Bool("redact-keys", false, "omit API keys from the export (for sharing provider setup)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch export [--redact-keys]")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	if *redact {
		cfg = redactConfigKeys(cfg)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = out.Write(data)
	return err
}

// cmdImport reads an exported config from a file and merges it into the app
// config. Existing providers with the same name are overwritten; others are
// preserved.
func cmdImport(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	force := fs.Bool("force", false, "skip the confirmation prompt")
	// Allow flags in any position relative to the file argument (stdlib flag stops
	// at the first positional), so both `import file --force` and `import --force
	// file` work.
	var positionals []string
	flagArgs := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			flagArgs = append(flagArgs, a)
		} else {
			positionals = append(positionals, a)
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("usage: code-switch import <file> [--force]")
	}
	path := positionals[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var imported AppConfig
	if err := json.Unmarshal(data, &imported); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	ensureAppConfigMaps(&imported)

	providerCount := len(imported.Providers)
	agentCount := 0
	for _, ac := range imported.Agents {
		agentCount += len(ac.Providers)
	}

	if !*force {
		fmt.Fprintf(out, "Import %d provider(s) and %d agent provider(s) from %s?\n", providerCount, agentCount, path)
		fmt.Fprintf(out, "Existing providers with the same name will be overwritten. [y/N]: ")
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

	cfg, cfgPath, unlock, err := loadAppConfigLocked()
	if err != nil {
		return err
	}
	defer unlock()
	mergeAppConfig(cfg, &imported)
	if err := writeJSONAtomic(cfgPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "imported %d provider(s) into %s\n", providerCount, cfgPath)
	return nil
}

// mergeAppConfig merges src into dst, with src taking precedence for any
// provider present in both. Providers only in dst are preserved.
func mergeAppConfig(dst *AppConfig, src *AppConfig) {
	ensureAppConfigMaps(dst)
	for k, v := range src.Providers {
		dst.Providers[k] = v
	}
	for agentName, srcAgent := range src.Agents {
		dstAgent := agentConfig(dst, AgentName(agentName))
		for k, v := range srcAgent.Providers {
			dstAgent.Providers[k] = v
		}
		dst.Agents[agentName] = dstAgent
	}
	if strings.TrimSpace(src.Default) != "" {
		dst.Default = src.Default
	}
}

// redactConfigKeys returns a deep copy of cfg with all API keys blanked.
func redactConfigKeys(cfg *AppConfig) *AppConfig {
	clone := *cfg
	clone.Providers = map[string]StoredProvider{}
	for k, v := range cfg.Providers {
		v.APIKey = ""
		clone.Providers[k] = v
	}
	clone.Agents = map[string]AgentConfig{}
	for agentName, agentCfg := range cfg.Agents {
		providers := map[string]StoredProvider{}
		for k, v := range agentCfg.Providers {
			v.APIKey = ""
			providers[k] = v
		}
		clone.Agents[agentName] = AgentConfig{Providers: providers}
	}
	return &clone
}
