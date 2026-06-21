package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// cmdDiff previews the env-var changes that `cs switch <provider>` would make to
// the Claude settings.json, without writing anything.
func cmdDiff(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	model := fs.String("model", "", "override model id")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	if agent != agentClaude {
		return fmt.Errorf("diff supports the claude agent only (compares settings.json env vars)")
	}
	if providerArg == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch diff <provider> [--model model-id] [--api-key sk-xxx] [--claude-dir DIR]")
	}

	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	provider := canonicalProviderName(providerArg)
	preset, err := resolveSwitchPreset(provider, cfg, strings.TrimSpace(*model))
	if err != nil {
		return err
	}
	key, err := resolveKey(agent, cfg, provider, strings.TrimSpace(*apiKey), preset)
	if err != nil {
		return err
	}

	// Build the desired env by reusing applyPreset on an empty root so the diff
	// always reflects the exact write path used by `switch`.
	desiredRoot := map[string]any{}
	applyPreset(desiredRoot, preset, key)
	desired := stringifyEnvMap(nestedMap(desiredRoot, "env"))

	settingsPath := claudeSettingsPath(*claudeDir)
	currentRoot, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}
	current := stringifyEnvMap(nestedMap(currentRoot, "env"))

	authKey := preset.AuthEnv
	if strings.TrimSpace(authKey) == "" {
		authKey = "ANTHROPIC_API_KEY"
	}
	printEnvDiff(out, settingsPath, preset.Name, current, desired, authKey)
	return nil
}

type envChange struct {
	Key    string
	Status string // "added" | "changed" | "removed" | "unchanged"
	New    string
	Old    string
}

// computeEnvDiff classifies the difference between the current and desired env
// maps. Only managed keys are considered for removal, matching applyPreset which
// clears exactly managedEnvKeys before writing.
func computeEnvDiff(current, desired map[string]string) []envChange {
	changes := map[string]envChange{}
	order := map[string]int{"added": 0, "changed": 1, "removed": 2, "unchanged": 3}

	for k, newVal := range desired {
		oldVal, existed := current[k]
		switch {
		case !existed || strings.TrimSpace(oldVal) == "":
			changes[k] = envChange{Key: k, Status: "added", New: newVal}
		case oldVal != newVal:
			changes[k] = envChange{Key: k, Status: "changed", New: newVal, Old: oldVal}
		default:
			changes[k] = envChange{Key: k, Status: "unchanged", New: newVal, Old: oldVal}
		}
	}
	for k, oldVal := range current {
		if _, exists := desired[k]; exists {
			continue
		}
		if !isManagedEnvKey(k) {
			continue
		}
		changes[k] = envChange{Key: k, Status: "removed", Old: oldVal}
	}

	result := make([]envChange, 0, len(changes))
	for _, c := range changes {
		result = append(result, c)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if order[result[i].Status] != order[result[j].Status] {
			return order[result[i].Status] < order[result[j].Status]
		}
		return result[i].Key < result[j].Key
	})
	return result
}

func printEnvDiff(out io.Writer, settingsPath, name string, current, desired map[string]string, authKey string) {
	changes := computeEnvDiff(current, desired)
	fmt.Fprintf(out, "Diff for switching Claude to %s\n", name)
	fmt.Fprintf(out, "  %s\n\n", formatLabel("settings", settingsPath))

	counts := map[string]int{}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, c := range changes {
		counts[c.Status]++
		switch c.Status {
		case "added":
			fmt.Fprintf(tw, "%s  %s\t%s\n", green("+"), c.Key, displayEnvValue(c.Key, c.New, authKey))
		case "changed":
			fmt.Fprintf(tw, "%s  %s\t%s\t(%s: %s)\n", yellow("~"), c.Key, displayEnvValue(c.Key, c.New, authKey), dim("was"), displayEnvValue(c.Key, c.Old, authKey))
		case "removed":
			fmt.Fprintf(tw, "%s  %s\t%s\t(%s: %s)\n", red("-"), c.Key, dim("(removed)"), dim("was"), displayEnvValue(c.Key, c.Old, authKey))
		default:
			fmt.Fprintf(tw, "%s  %s\t%s\n", dim("="), c.Key, dim(displayEnvValue(c.Key, c.New, authKey)))
		}
	}
	tw.Flush()

	fmt.Fprintf(out, "\n%d added, %d changed, %d removed, %d unchanged\n",
		counts["added"], counts["changed"], counts["removed"], counts["unchanged"])
}

func displayEnvValue(key, val, authKey string) string {
	if key == authKey || key == "ANTHROPIC_API_KEY" || key == "ANTHROPIC_AUTH_TOKEN" {
		return maskAPIKey(val)
	}
	return val
}

func stringifyEnvMap(env map[string]any) map[string]string {
	out := map[string]string{}
	if env == nil {
		return out
	}
	for k, v := range env {
		if v == nil {
			out[k] = ""
			continue
		}
		out[k] = fmt.Sprint(v)
	}
	return out
}

func isManagedEnvKey(key string) bool {
	for _, k := range managedEnvKeys {
		if k == key {
			return true
		}
	}
	return false
}
