package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func codexConfigPath(overrideDir string) string {
	dir := strings.TrimSpace(overrideDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".codex", "config.toml")
		}
		dir = filepath.Join(home, ".codex")
	}
	return filepath.Join(dir, "config.toml")
}

func switchCodexProvider(provider string, cfg *AppConfig, apiKey, modelOverride, codexDir string, out io.Writer, dryRun bool) error {
	provider = canonicalProviderName(provider)
	preset, err := resolveAgentSwitchPreset(agentCodex, provider, cfg, modelOverride)
	if err != nil {
		return err
	}
	configPath := codexConfigPath(codexDir)

	if dryRun {
		fmt.Fprintf(out, "[dry-run] would switch Codex to %s\n", preset.Name)
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		fmt.Fprintf(out, "[dry-run] base_url: %s\n", preset.BaseURL)
		fmt.Fprintf(out, "[dry-run] model: %s\n", preset.Model)
		return nil
	}

	existing, err := readTextFileIfExists(configPath)
	if err != nil {
		return err
	}
	if err := backupIfExists(configPath); err != nil {
		return err
	}

	updated := applyCodexPresetTOML(existing, preset)
	if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
		return err
	}

	stored := codexProviderConfig(cfg, provider)
	stored.APIKey = apiKey
	stored.Model = preset.Model
	setAgentProviderConfig(cfg, agentCodex, provider, stored)

	fmt.Fprintf(out, "switched Codex to %s\n", preset.Name)
	fmt.Fprintf(out, "config: %s\n", configPath)
	fmt.Fprintf(out, "base_url: %s\n", preset.BaseURL)
	fmt.Fprintf(out, "model: %s\n", preset.Model)
	fmt.Fprintf(out, "run: eval \"$(cs env %s --agent codex)\"\n", provider)
	return nil
}

func applyCodexPresetTOML(existing string, preset ProviderPreset) string {
	cleaned := removeCodexManagedTOML(existing, true, true, nil)
	topLevel, sections := splitBeforeFirstTOMLSection(cleaned)
	var b strings.Builder

	if top := strings.TrimRight(topLevel, "\n"); strings.TrimSpace(top) != "" {
		b.WriteString(top)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "model = %q\n", preset.Model)
	fmt.Fprintf(&b, "model_provider = %q\n", "ollama-cloud")

	if strings.TrimSpace(sections) != "" {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimRight(strings.TrimLeft(sections, "\n"), "\n"))
	}
	b.WriteString("\n\n")
	b.WriteString("[model_providers.ollama-cloud]\n")
	fmt.Fprintf(&b, "name = %q\n", preset.Name)
	fmt.Fprintf(&b, "base_url = %q\n", preset.BaseURL)
	b.WriteString("env_key = \"OLLAMA_API_KEY\"\n")
	b.WriteString("env_key_instructions = \"Set OLLAMA_API_KEY to your Ollama API key\"\n")
	b.WriteString("wire_api = \"responses\"\n")
	return b.String()
}

func splitBeforeFirstTOMLSection(content string) (string, string) {
	offset := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		if _, ok := tomlSectionName(line); ok {
			return content[:offset], content[offset:]
		}
		offset += len(line)
	}
	return content, ""
}

func restoreCodexConfig(codexDir string, cfg *AppConfig, out io.Writer, dryRun bool) error {
	configPath := codexConfigPath(codexDir)
	existing, err := readTextFileIfExists(configPath)
	if err != nil {
		return err
	}
	updated := removeCodexManagedTOML(existing, false, false, cfg)
	if dryRun {
		fmt.Fprintf(out, "[dry-run] would restore Codex official config\n")
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		return nil
	}
	if err := backupIfExists(configPath); err != nil {
		return err
	}
	if strings.TrimSpace(existing) == "" && strings.TrimSpace(updated) == "" {
		fmt.Fprintf(out, "restored Codex official config\n")
		fmt.Fprintf(out, "config: %s\n", configPath)
		return nil
	}
	if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "restored Codex official config\n")
	fmt.Fprintf(out, "config: %s\n", configPath)
	return nil
}

func removeCodexManagedTOML(existing string, removeTopLevelModel bool, removeTopLevelProvider bool, cfg *AppConfig) string {
	provider, model, _, _ := parseCodexTopLevel(existing)
	if !removeTopLevelProvider {
		removeTopLevelProvider = provider == "ollama-cloud"
	}
	if !removeTopLevelModel {
		removeTopLevelModel = provider == "ollama-cloud" && isManagedCodexModel(model, cfg)
	}

	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines))
	section := ""
	skipSection := false
	for _, line := range lines {
		if name, ok := tomlSectionName(line); ok {
			section = name
			skipSection = name == "model_providers.ollama-cloud"
			if skipSection {
				continue
			}
		}
		if skipSection {
			continue
		}
		if section == "" {
			if key, _, ok := tomlKeyValue(line); ok {
				if key == "model" && removeTopLevelModel {
					continue
				}
				if key == "model_provider" && removeTopLevelProvider {
					continue
				}
			}
		}
		out = append(out, line)
	}

	result := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(result) == "" {
		return ""
	}
	return result + "\n"
}

func isManagedCodexModel(model string, cfg *AppConfig) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	preset := codexOllamaCloudPreset()
	if model == preset.Model || containsString(preset.Models, model) {
		return true
	}
	if cfg != nil && strings.TrimSpace(codexProviderConfig(cfg, "ollama-cloud").Model) == model {
		return true
	}
	return false
}

func parseCodexTopLevel(content string) (provider string, model string, baseURL string, err error) {
	lines := strings.Split(content, "\n")
	section := ""
	for _, line := range lines {
		if name, ok := tomlSectionName(line); ok {
			section = name
			continue
		}
		key, value, ok := tomlKeyValue(line)
		if !ok {
			continue
		}
		switch section {
		case "":
			switch key {
			case "model_provider":
				provider = tomlStringValue(value)
			case "model":
				model = tomlStringValue(value)
			}
		case "model_providers.ollama-cloud":
			if key == "base_url" {
				baseURL = tomlStringValue(value)
			}
		}
	}
	return provider, model, baseURL, nil
}

func tomlSectionName(line string) (string, bool) {
	text := strings.TrimSpace(line)
	if strings.HasPrefix(text, "[[") || !strings.HasPrefix(text, "[") || !strings.HasSuffix(text, "]") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "["), "]")), true
}

func tomlKeyValue(line string) (string, string, bool) {
	text := strings.TrimSpace(line)
	if text == "" || strings.HasPrefix(text, "#") {
		return "", "", false
	}
	parts := strings.SplitN(text, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func tomlStringValue(value string) string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, `"`) {
		return strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
	}
	value = strings.TrimPrefix(value, `"`)
	end := strings.Index(value, `"`)
	if end < 0 {
		return value
	}
	return value[:end]
}

func readTextFileIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func writeTextAtomic(path, content string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

func currentCodexProvider(codexDir string) (string, string, string, string, error) {
	configPath := codexConfigPath(codexDir)
	content, err := readTextFileIfExists(configPath)
	if err != nil {
		return "", "", "", "", err
	}
	provider, model, baseURL, err := parseCodexTopLevel(content)
	return configPath, provider, model, baseURL, err
}

type codexTestRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

func testCodexProvider(out io.Writer, preset ProviderPreset, apiKey string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return testCodexProviderWithClient(ctx, out, preset, apiKey, &http.Client{})
}

func testCodexProviderWithClient(ctx context.Context, out io.Writer, preset ProviderPreset, apiKey string, client *http.Client) error {
	testURL := codexResponsesURL(preset.BaseURL)
	fmt.Fprintf(out, "Testing %s (%s)...\n", preset.Name, preset.BaseURL)

	bodyBytes, err := json.Marshal(codexTestRequest{Model: preset.Model, Input: "Say hi"})
	if err != nil {
		return fmt.Errorf("marshal test request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, testURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create test request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "code-switch/"+version)

	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(out, "FAIL\n")
		fmt.Fprintf(out, "  URL: %s\n", testURL)
		fmt.Fprintf(out, "  Request failed: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		fmt.Fprintf(out, "FAIL\n")
		fmt.Fprintf(out, "  URL: %s\n", testURL)
		fmt.Fprintf(out, "  Status: %d\n", resp.StatusCode)
		fmt.Fprintf(out, "  Failed to read response body\n")
		return nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Fprintf(out, "OK\n")
		fmt.Fprintf(out, "  Status: %d\n", resp.StatusCode)
		return nil
	}
	fmt.Fprintf(out, "FAIL\n")
	fmt.Fprintf(out, "  URL: %s\n", testURL)
	fmt.Fprintf(out, "  Status: %d\n", resp.StatusCode)
	if len(body) > 0 {
		fmt.Fprintf(out, "  Response: %s\n", strings.TrimSpace(string(body)))
	}
	return nil
}

func codexResponsesURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/responses"
	}
	return base + "/v1/responses"
}
