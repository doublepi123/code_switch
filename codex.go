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
			return filepath.Join(os.TempDir(), ".codex", "config.toml")
		}
		dir = filepath.Join(home, ".codex")
	}
	return filepath.Join(dir, "config.toml")
}

func codexTOMLProviderName(provider string) string {
	switch provider {
	case "deepseek":
		return "DeepSeek"
	case "openrouter":
		return "OpenRouter"
	case "kimi-coding":
		return "Kimi"
	default:
		return provider
	}
}

func codexTOMLProviderKey(providerName string) string {
	switch providerName {
	case "DeepSeek":
		return "deepseek"
	case "OpenRouter":
		return "openrouter"
	case "ollama-cloud":
		return "ollama-cloud"
	case "Kimi":
		return "kimi-coding"
	default:
		return providerName
	}
}

func switchCodexProvider(provider string, cfg *AppConfig, apiKey, modelOverride, codexDir string, out io.Writer, dryRun bool) error {
	provider = canonicalProviderName(provider)
	preset, err := resolveAgentSwitchPreset(agentCodex, provider, cfg, modelOverride)
	if err != nil {
		return err
	}
	configPath := codexConfigPath(codexDir)

	if !dryRun {
		cf := newConfigFile(configPath)
		unlock, err := cf.lock()
		if err != nil {
			return err
		}
		defer unlock()
	}

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

	updated := applyCodexPresetTOML(existing, preset, provider)
	if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
		return err
	}

	stored := codexProviderConfig(cfg, provider)
	if apiKey != "" {
		stored.APIKey = apiKey
	}
	stored.Model = preset.Model
	setAgentProviderConfig(cfg, agentCodex, provider, stored)

	fmt.Fprintf(out, "%s\n", successPrefix(fmt.Sprintf("switched Codex to %s", preset.Name)))
	fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
	fmt.Fprintf(out, "%s\n", formatLabel("base_url", preset.BaseURL))
	fmt.Fprintf(out, "%s\n", formatLabel("model", preset.Model))
	fmt.Fprintf(out, "%s\n", formatLabel("auth", fmt.Sprintf("cs token %s --agent codex", provider)))
	return nil
}

func applyCodexPresetTOML(existing string, preset ProviderPreset, provider string) string {
	provider = canonicalProviderName(provider)
	cleaned := removeCodexManagedTOML(existing, true, true, nil)
	topLevel, sections := splitBeforeFirstTOMLSection(cleaned)
	var b strings.Builder

	topLevel = strings.TrimRight(topLevel, "\n")
	if strings.TrimSpace(topLevel) != "" {
		b.WriteString(topLevel)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "model = %q\n", preset.Model)
	providerName := codexTOMLProviderName(provider)
	fmt.Fprintf(&b, "model_provider = %q\n", providerName)
	b.WriteString("approvals_reviewer = \"user\"\n")
	if preset.ReasoningEffort != "" {
		fmt.Fprintf(&b, "reasoning_effort = %q\n", preset.ReasoningEffort)
	}

	sections = strings.TrimSpace(sections)
	if sections != "" {
		b.WriteString("\n")
		b.WriteString(sections)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "[model_providers.%s]\n", providerName)
	fmt.Fprintf(&b, "name = %q\n", preset.Name)
	fmt.Fprintf(&b, "base_url = %q\n", preset.BaseURL)
	b.WriteString("wire_api = \"responses\"\n")
	b.WriteString(fmt.Sprintf("\n[model_providers.%s.auth]\n", providerName))
	b.WriteString("command = \"cs\"\n")
	fmt.Fprintf(&b, "args = [\"token\", %q, \"--agent\", \"codex\"]\n", provider)
	return b.String()
}

func isMultilineStringBoundary(line string) (enter bool, exit bool) {
	text := strings.TrimSpace(line)
	if text == `"""` {
		return true, true
	}
	if text == "'''" {
		return true, true
	}
	if strings.HasPrefix(text, `"""`) && strings.HasSuffix(text, `"""`) && len(text) >= 6 {
		return false, false
	}
	if strings.HasPrefix(text, "'''") && strings.HasSuffix(text, "'''") && len(text) >= 6 {
		return false, false
	}
	// A line like `key = """` opens a multi-line string; treat it as enter
	// even when the trimmed text also ends with `"""`.
	if multilineValueSide(text, `"""`) {
		return true, false
	}
	if multilineValueSide(text, "'''") {
		return true, false
	}
	if strings.HasPrefix(text, `"""`) {
		return true, false
	}
	if strings.HasPrefix(text, "'''") {
		return true, false
	}
	if strings.HasSuffix(text, `"""`) {
		return false, true
	}
	if strings.HasSuffix(text, "'''") {
		return false, true
	}
	return false, false
}

// multilineValueSide reports whether the line is a `key = """` style opener
// where `"""` (or `'''`) appears as the start of the value side and the value
// is not closed on the same line.
func multilineValueSide(text, quote string) bool {
	parts := strings.SplitN(text, "=", 2)
	if len(parts) != 2 {
		return false
	}
	value := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(value, quote) {
		return false
	}
	return !strings.HasSuffix(value, quote) || len(value) < 6
}

func splitBeforeFirstTOMLSection(content string) (string, string) {
	offset := 0
	inMultiline := false
	for _, line := range strings.SplitAfter(content, "\n") {
		enter, exit := isMultilineStringBoundary(line)
		if inMultiline {
			if exit {
				inMultiline = false
			}
			offset += len(line)
			continue
		}
		if enter {
			inMultiline = true
			offset += len(line)
			continue
		}
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
	updated := removeCodexManagedTOML(existing, true, true, cfg)
	if dryRun {
		fmt.Fprintf(out, "[dry-run] would restore Codex official config\n")
		fmt.Fprintf(out, "[dry-run] config: %s\n", configPath)
		return nil
	}
	cf := newConfigFile(configPath)
	unlock, err := cf.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := backupIfExists(configPath); err != nil {
		return err
	}
	if strings.TrimSpace(updated) == "" {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := writeTextAtomic(configPath, updated, 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%s\n", successPrefix("restored Codex official config"))
	fmt.Fprintf(out, "%s\n", formatLabel("config", configPath))
	return nil
}

func removeCodexManagedTOML(existing string, removeTopLevelModel bool, removeTopLevelProvider bool, cfg *AppConfig) string {
	removeTopLevelApprovalsReviewer := removeTopLevelModel && removeTopLevelProvider

	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines))
	section := ""
	skipSection := false
	inMultiline := false
	for _, line := range lines {
		enter, exit := isMultilineStringBoundary(line)
		if inMultiline {
			if exit {
				inMultiline = false
			}
			out = append(out, line)
			continue
		}
		if enter {
			inMultiline = true
			out = append(out, line)
			continue
		}
		if name, ok := tomlSectionName(line); ok {
			section = name
			skipSection = false
			for _, p := range []string{"ollama-cloud", "openrouter", "deepseek", "kimi-coding"} {
				pName := codexTOMLProviderName(p)
				if name == "model_providers."+pName || strings.HasPrefix(name, "model_providers."+pName+".") {
					skipSection = true
					break
				}
			}
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
				if key == "approvals_reviewer" && removeTopLevelApprovalsReviewer {
					continue
				}
				if key == "reasoning_effort" && removeTopLevelApprovalsReviewer {
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
	for _, fn := range []func() ProviderPreset{codexDeepSeekPreset, codexOllamaCloudPreset, codexOpenRouterPreset, codexKimiCodingPreset} {
		preset := fn()
		if model == preset.Model || containsString(preset.Models, model) {
			return true
		}
	}
	if cfg != nil {
		for _, p := range []string{"ollama-cloud", "openrouter", "deepseek", "kimi-coding"} {
			if strings.TrimSpace(codexProviderConfig(cfg, p).Model) == model {
				return true
			}
		}
	}
	return false
}

func parseCodexTopLevel(content string) (provider string, model string, baseURL string, err error) {
	lines := strings.Split(content, "\n")
	section := ""
	inMultiline := false
	for _, line := range lines {
		enter, exit := isMultilineStringBoundary(line)
		if inMultiline {
			if exit {
				inMultiline = false
			}
			continue
		}
		if enter {
			inMultiline = true
			continue
		}
		if name, ok := tomlSectionName(line); ok {
			section = name
			continue
		}
		key, value, ok := tomlKeyValue(line)
		if !ok {
			continue
		}
		if section == "" {
			switch key {
			case "model_provider":
				provider = tomlStringValue(value)
			case "model":
				model = tomlStringValue(value)
			}
		}
	}

	// Second pass: read base_url only from the section matching the current provider.
	section = ""
	inMultiline = false
	for _, line := range lines {
		enter, exit := isMultilineStringBoundary(line)
		if inMultiline {
			if exit {
				inMultiline = false
			}
			continue
		}
		if enter {
			inMultiline = true
			continue
		}
		if name, ok := tomlSectionName(line); ok {
			section = name
			continue
		}
		key, value, ok := tomlKeyValue(line)
		if !ok {
			continue
		}
		if key == "base_url" && section == "model_providers."+provider {
			baseURL = tomlStringValue(value)
		}
	}
	return provider, model, baseURL, nil
}

func tomlSectionName(line string) (string, bool) {
	text := strings.TrimSpace(line)
	if !strings.HasPrefix(text, "[") || !strings.HasSuffix(text, "]") {
		return "", false
	}
	// Handle array-of-tables [[...]]
	if strings.HasPrefix(text, "[[") && strings.HasSuffix(text, "]]") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "[["), "]]")), true
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
	key := strings.TrimSpace(parts[0])
	rawValue := strings.TrimSpace(parts[1])
	// Reject multi-line string starters that span multiple lines;
	// this simplistic parser cannot track them.
	if strings.HasPrefix(rawValue, `"""`) && !strings.HasSuffix(rawValue, `"""`) {
		return "", "", false
	}
	if strings.HasPrefix(rawValue, "'''") && !strings.HasSuffix(rawValue, "'''") {
		return "", "", false
	}
	return key, rawValue, true
}

func tomlStringValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch value[0] {
	case '"':
		return tomlUnquoteString(value)
	case '\'':
		return tomlUnquoteLiteralString(value)
	case '[':
		return strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
	default:
		return strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
	}
}

func tomlUnquoteString(value string) string {
	value = strings.TrimPrefix(value, `"`)
	var b strings.Builder
	escaped := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if escaped {
			escaped = false
			switch c {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				b.WriteByte('\\')
				b.WriteByte(c)
			}
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			return b.String()
		}
		b.WriteByte(c)
	}
	return b.String()
}

func tomlUnquoteLiteralString(value string) string {
	value = strings.TrimPrefix(value, "'")
	end := strings.Index(value, "'")
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
	os.Chmod(filepath.Dir(path), 0o755)
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
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
		os.Remove(tmp)
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
		return fmt.Errorf("test %s: request failed: %w", preset.Name, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		fmt.Fprintf(out, "FAIL\n")
		fmt.Fprintf(out, "  URL: %s\n", testURL)
		fmt.Fprintf(out, "  Status: %d\n", resp.StatusCode)
		fmt.Fprintf(out, "  Failed to read response body\n")
		return fmt.Errorf("test %s: failed to read response body: %w", preset.Name, readErr)
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
	return fmt.Errorf("test %s: status %d", preset.Name, resp.StatusCode)
}

// codexResponsesURL constructs the Responses API test URL.
// It assumes the standard /v1/responses path; custom endpoints may need adjustment.
func codexResponsesURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/responses"
	}
	return base + "/v1/responses"
}
