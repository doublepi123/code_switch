package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// checkResult is one doctor finding.
type checkResult struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

func okResult(name, detail string) checkResult {
	return checkResult{Name: name, Status: "ok", Detail: detail}
}
func warnResult(name, detail string) checkResult {
	return checkResult{Name: name, Status: "warn", Detail: detail}
}
func failResult(name, detail string) checkResult {
	return checkResult{Name: name, Status: "fail", Detail: detail}
}

// cmdDoctor runs a set of health checks against the app config and each agent's
// settings file and reports OK/WARN/FAIL per check.
func cmdDoctor(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "output check results as JSON")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch doctor [--json]")
	}

	results := runDoctor(*claudeDir, *codexDir, *opencodeDir)

	if *jsonOut {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		_, err = out.Write(data)
		return err
	}

	printDoctor(out, results)

	var failed int
	for _, r := range results {
		if r.Status == "fail" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	return nil
}

func runDoctor(claudeDir, codexDir, opencodeDir string) []checkResult {
	var results []checkResult
	results = append(results, checkAppConfig())
	results = append(results, checkClaudeFile(claudeSettingsPath(claudeDir)))
	results = append(results, checkCodexFile(codexConfigPath(codexDir)))
	results = append(results, checkOpencodeFile(opencodeConfigPath(opencodeDir)))
	results = append(results, checkProxyDaemon())
	if cfg, _, err := loadAppConfig(); err == nil {
		results = append(results, checkClaudeDrift(claudeDir, cfg))
		results = append(results, checkCodexDrift(codexDir, cfg))
		results = append(results, checkOpencodeDrift(opencodeDir, cfg))
	}
	results = append(results, checkOrphanedTempFiles(claudeDir, codexDir, opencodeDir))
	return results
}

func checkProxyDaemon() checkResult {
	cfg, _, err := loadAppConfig()
	if err != nil {
		return warnResult("proxy daemon", "app config unavailable: "+err.Error())
	}
	if cfg.Proxy == nil || len(cfg.Proxy.Routes) == 0 {
		return okResult("proxy daemon", "no proxy routes configured")
	}
	status := proxyDaemonStatusText()
	if strings.HasPrefix(status, "running") {
		return okResult("proxy daemon", status)
	}
	return warnResult("proxy daemon", status)
}

func printDoctor(out io.Writer, results []checkResult) {
	var ok, warn, fail int
	for _, r := range results {
		switch r.Status {
		case "ok":
			ok++
		case "warn":
			warn++
		case "fail":
			fail++
		}
		fmt.Fprintf(out, "  %s  %s — %s\n", doctorBadge(r.Status), r.Name, r.Detail)
	}
	fmt.Fprintf(out, "\n%d ok, %d warning(s), %d failed\n", ok, warn, fail)
}

func doctorBadge(status string) string {
	switch status {
	case "ok":
		return green("OK")
	case "warn":
		return yellow("WARN")
	default:
		return red("FAIL")
	}
}

func checkAppConfig() checkResult {
	path, err := appConfigPath()
	if err != nil {
		return failResult("app config", err.Error())
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return okResult("app config", "not created yet (created on first switch)")
		}
		return failResult("app config", err.Error())
	}
	cfg, err := loadAppConfigFrom(path)
	if err != nil {
		return failResult("app config parse", err.Error())
	}
	keys := countSavedKeys(cfg)
	detail := fmt.Sprintf("%s — %d provider key(s) saved", filepath.Base(path), keys)
	if cfg.Default != "" {
		detail += fmt.Sprintf(", default=%s", cfg.Default)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		return warnResult("app config permissions", fmt.Sprintf("mode %o, expected 0600; %s", mode, detail))
	}
	return okResult("app config", detail)
}

func countSavedKeys(cfg *AppConfig) int {
	if cfg == nil {
		return 0
	}
	n := 0
	for _, p := range cfg.Providers {
		if strings.TrimSpace(p.APIKey) != "" {
			n++
		}
	}
	for _, ac := range cfg.Agents {
		for _, p := range ac.Providers {
			if strings.TrimSpace(p.APIKey) != "" {
				n++
			}
		}
	}
	return n
}

func checkClaudeFile(path string) checkResult {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return okResult("claude settings", "no settings.json (using official defaults)")
		}
		return failResult("claude settings", err.Error())
	}
	root, err := readJSONMap(path)
	if err != nil {
		return failResult("claude settings parse", err.Error())
	}
	env := nestedMap(root, "env")
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)
	var detail string
	if baseURL == "" {
		detail = "present, no provider configured"
	} else {
		detail = fmt.Sprintf("provider=%s model=%s", detectProvider(baseURL, model), model)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		return warnResult("claude settings permissions", fmt.Sprintf("mode %o, expected 0600; %s", mode, detail))
	}
	return okResult("claude settings", detail)
}

func checkCodexFile(path string) checkResult {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return okResult("codex config", "no config.toml (not configured)")
		}
		return failResult("codex config", err.Error())
	}
	_, provider, model, _, err := currentCodexProvider(filepath.Dir(path))
	if err != nil {
		return failResult("codex config", err.Error())
	}
	if provider == "" {
		return okResult("codex config", fmt.Sprintf("%s present, no provider", filepath.Base(path)))
	}
	detail := fmt.Sprintf("provider=%s", codexTOMLProviderKey(provider))
	if model != "" {
		detail += " model=" + model
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		return warnResult("codex config permissions", fmt.Sprintf("mode %o, expected 0600; %s", mode, detail))
	}
	return okResult("codex config", detail)
}

func checkOpencodeFile(path string) checkResult {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return okResult("opencode config", "no opencode.json (not configured)")
		}
		return failResult("opencode config", err.Error())
	}
	_, model, baseURL, _, providerName, err := currentOpencodeProvider(filepath.Dir(path))
	if err != nil {
		return failResult("opencode config", err.Error())
	}
	detail := "present"
	if baseURL != "" {
		provider := providerName
		if provider == "" {
			provider = detectProvider(baseURL, model)
		}
		detail = fmt.Sprintf("provider=%s", provider)
		if model != "" {
			detail += " model=" + model
		}
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		return warnResult("opencode config permissions", fmt.Sprintf("mode %o, expected 0600; %s", mode, detail))
	}
	return okResult("opencode config", detail)
}
