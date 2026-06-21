package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// cmdModels lists the available models for a provider. With no provider argument
// it falls back to the agent's currently active provider. Discovery is performed
// for providers that support it (ollama, openrouter).
func cmdModels(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	apiKey := fs.String("api-key", "", "API key for model discovery (e.g. openrouter)")
	jsonOut := fs.Bool("json", false, "output models as JSON")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch models [provider] [--agent claude|codex|opencode] [--api-key sk-xxx] [--json]")
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}

	provider := canonicalProviderName(strings.TrimSpace(providerArg))
	if provider == "" {
		provider = currentProviderForAgent(agent, cfg, *claudeDir, *codexDir, *opencodeDir)
		if provider == "" || provider == customDetectedProvider {
			return fmt.Errorf("no provider specified and no current provider detected; run `cs models <provider>`")
		}
	}

	preset, err := resolveAgentProviderPreset(agent, provider, cfg)
	if err != nil {
		return fmt.Errorf("unsupported provider %q for agent %s", provider, agent)
	}

	models := providerModelsForAgentWithAPIKey(cfg, agent, provider, strings.TrimSpace(*apiKey))

	if *jsonOut {
		result := modelsResult{
			Provider: provider,
			Agent:    string(agent),
			Current:  preset.Model,
			Models:   models,
		}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		_, err = out.Write(data)
		return err
	}

	fmt.Fprintf(out, "Models for %s (%s):\n", provider, agent)
	if preset.NoModel {
		fmt.Fprintf(out, "  provider does not pin a model (model auto-resolved by upstream)\n")
	}
	if len(models) == 0 {
		fmt.Fprintf(out, "  (no models available)\n")
		return nil
	}
	for _, m := range models {
		if m == preset.Model {
			fmt.Fprintf(out, "  %s %s\n", m, dim("(default)"))
		} else {
			fmt.Fprintf(out, "  %s\n", m)
		}
	}
	return nil
}

type modelsResult struct {
	Provider string   `json:"provider"`
	Agent    string   `json:"agent"`
	Current  string   `json:"current,omitempty"`
	Models   []string `json:"models"`
}

// currentProviderForAgent returns the provider name currently active for the given
// agent, or "" (or customDetectedProvider) if none can be determined.
func currentProviderForAgent(agent AgentName, cfg *AppConfig, claudeDir, codexDir, opencodeDir string) string {
	switch agent {
	case agentCodex:
		_, provider, _, _, err := currentCodexProvider(codexDir)
		if err != nil {
			return ""
		}
		return provider
	case agentOpencode:
		_, _, baseURL, _, providerName, err := currentOpencodeProvider(opencodeDir)
		if err != nil {
			return ""
		}
		if strings.TrimSpace(providerName) != "" {
			return providerName
		}
		if baseURL == "" {
			return ""
		}
		return detectProvider(baseURL, "")
	default:
		provider, _ := currentConfiguredProvider(cfg, claudeDir)
		return provider
	}
}
