package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type ProviderPreset struct {
	Name      string
	BaseURL   string
	Model     string
	Models    []string
	Haiku     string
	Sonnet    string
	Opus      string
	Subagent  string
	AuthEnv   string
	ExtraEnv  map[string]any
	Website   string
	APIKeyURL string
}

type StoredProvider struct {
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	Model   string `json:"model,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
}

type AppConfig struct {
	Providers map[string]StoredProvider `json:"providers"`
}

type ConfigureSelection struct {
	Provider   string
	Model      string
	ResetKey   bool
	APIKey     string
	Name       string
	BaseURL    string
	SavedModel string
}

var providerPresets = map[string]ProviderPreset{
	"minimax-cn": {
		Name:      "MiniMax CN Token Plan",
		BaseURL:   "https://api.minimaxi.com/anthropic",
		Model:     "MiniMax-M2.7",
		Models:    []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"},
		Haiku:     "MiniMax-M2.7",
		Sonnet:    "MiniMax-M2.7",
		Opus:      "MiniMax-M2.7",
		Website:   "https://platform.minimaxi.com",
		APIKeyURL: "https://platform.minimaxi.com/docs/token-plan/claude-code",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		},
	},
	"minimax-global": {
		Name:      "MiniMax Global Token Plan",
		BaseURL:   "https://api.minimax.io/anthropic",
		Model:     "MiniMax-M2.7",
		Models:    []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"},
		Haiku:     "MiniMax-M2.7",
		Sonnet:    "MiniMax-M2.7",
		Opus:      "MiniMax-M2.7",
		Website:   "https://platform.minimax.io",
		APIKeyURL: "https://platform.minimax.io/docs/token-plan/claude-code",
		ExtraEnv: map[string]any{
			"API_TIMEOUT_MS": "3000000",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		},
	},
	"openrouter": {
		Name:      "OpenRouter",
		BaseURL:   "https://openrouter.ai/api",
		Model:     "anthropic/claude-sonnet-4.6",
		Models:    []string{"anthropic/claude-sonnet-4.6", "anthropic/claude-haiku-4.5", "anthropic/claude-opus-4.7"},
		Haiku:     "anthropic/claude-haiku-4.5",
		Sonnet:    "anthropic/claude-sonnet-4.6",
		Opus:      "anthropic/claude-opus-4.7",
		Website:   "https://openrouter.ai",
		APIKeyURL: "https://openrouter.ai/keys",
	},
	"deepseek": {
		Name:      "DeepSeek",
		BaseURL:   "https://api.deepseek.com/anthropic",
		Model:     "deepseek-v4-pro[1m]",
		Models:    []string{"deepseek-v4-pro[1m]", "deepseek-v4-pro", "deepseek-v4-flash"},
		Haiku:     "deepseek-v4-flash",
		Sonnet:    "deepseek-v4-pro",
		Opus:      "deepseek-v4-pro",
		Subagent:  "deepseek-v4-pro",
		AuthEnv:   "ANTHROPIC_AUTH_TOKEN",
		Website:   "https://platform.deepseek.com",
		APIKeyURL: "https://platform.deepseek.com/api_keys",
		ExtraEnv: map[string]any{
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":  "1",
			"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "1",
			"CLAUDE_CODE_EFFORT_LEVEL":                  "max",
		},
	},
	"opencode-go": {
		Name:      "OpenCode Go",
		BaseURL:   "https://opencode.ai/zen/go",
		Model:     "minimax-m2.7",
		Models:    []string{"minimax-m2.7", "minimax-m2.5"},
		Haiku:     "minimax-m2.7",
		Sonnet:    "minimax-m2.7",
		Opus:      "minimax-m2.7",
		Website:   "https://opencode.ai/docs/go/",
		APIKeyURL: "https://opencode.ai",
	},
}

var providerAliases = map[string]string{
	"minimax":              "minimax-cn",
	"minimax-cn-token":     "minimax-cn",
	"minimax-global-token": "minimax-global",
}

const customProviderOption = "__custom__"
const customDetectedProvider = "custom"
const defaultUpgradeRepo = "doublepi123/claude_switch"

var version = "dev"

var managedEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"API_TIMEOUT_MS",
	"CLAUDE_CODE_SUBAGENT_MODEL",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
	"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK",
	"CLAUDE_CODE_EFFORT_LEVEL",
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return runWithIO(args, os.Stdin, os.Stdout)
}

func runWithIO(args []string, in io.Reader, out io.Writer) error {
	// Check for --version or version anywhere in args so that
	// "cs switch --version" and "cs configure --version" work.
	for _, arg := range args {
		if arg == "--version" || arg == "version" {
			printVersion(out)
			return nil
		}
	}

	if len(args) == 0 {
		return cmdConfigure(nil, in, out)
	}
	if strings.HasPrefix(args[0], "-") {
		return cmdConfigure(args, in, out)
	}

	switch args[0] {
	case "list":
		return cmdList(out)
	case "configure":
		return cmdConfigure(args[1:], in, out)
	case "current":
		return cmdCurrent(args[1:])
	case "set-key":
		return cmdSetKey(args[1:])
	case "switch":
		return cmdSwitch(args[1:])
	case "upgrade":
		return cmdUpgrade(args[1:], out)
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printVersion(out io.Writer) {
	fmt.Fprintf(out, "claude-switch %s\n", version)
}

func cmdList(out io.Writer) error {
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	names := sortedProviderNames(cfg, false)
	for _, name := range names {
		preset, err := resolveProviderPreset(name, cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\t%s\t%s\n", name, preset.BaseURL, preset.Model)
	}
	return nil
}

func cmdConfigure(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	resetKey := fs.Bool("reset-key", false, "force re-enter api key for the selected provider")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, configPath, err := loadAppConfig()
	if err != nil {
		return err
	}

	currentProvider, currentModel := currentConfiguredProvider(cfg, *claudeDir)
	reader := bufio.NewReader(in)
	var selection ConfigureSelection
	if file, ok := in.(*os.File); ok && shouldUseArrowTUI(file) {
		selection, err = runArrowTUI(cfg, currentProvider, currentModel)
		if err != nil {
			return err
		}
	} else {
		selection, err = promptConfigureSelectionFallback(reader, out, cfg, currentProvider, currentModel)
		if err != nil {
			return err
		}
	}
	provider := selection.Provider

	existingKey := strings.TrimSpace(cfg.Providers[provider].APIKey)
	apiKey := existingKey
	if selection.APIKey != "" {
		apiKey = selection.APIKey
	} else if apiKey == "" || *resetKey || selection.ResetKey {
		apiKey, err = promptAPIKey(reader, out, provider)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "using saved api key for %s\n", provider)
	}
	upsertProviderConfig(cfg, selection, apiKey)

	if err := writeJSONAtomic(configPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved provider config for %s in %s\n", provider, configPath)

	if err := switchProvider(provider, cfg, apiKey, selection.Model, *claudeDir); err != nil {
		return err
	}
	return nil
}

func cmdCurrent(args []string) error {
	fs := flag.NewFlagSet("current", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	if err := fs.Parse(args); err != nil {
		return err
	}

	settingsPath := claudeSettingsPath(*claudeDir)
	root, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}

	env := nestedMap(root, "env")
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)

	fmt.Printf("settings: %s\n", settingsPath)
	if baseURL == "" {
		fmt.Println("provider: unknown")
		return nil
	}

	fmt.Printf("provider: %s\n", detectProvider(baseURL, model))
	fmt.Printf("base_url: %s\n", baseURL)
	if model != "" {
		fmt.Printf("model: %s\n", model)
	}
	return nil
}

func cmdSetKey(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: claude-switch set-key <provider> <api-key>")
	}
	cfg, path, err := loadAppConfig()
	if err != nil {
		return err
	}
	provider := canonicalProviderName(args[0])
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", args[0])
	}
	stored := cfg.Providers[provider]
	stored.APIKey = args[1]
	cfg.Providers[provider] = stored
	if err := writeJSONAtomic(path, cfg); err != nil {
		return err
	}

	fmt.Printf("saved api key for %s in %s\n", provider, path)
	return nil
}

func cmdSwitch(args []string) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	apiKey := fs.String("api-key", "", "API key for the target provider")
	model := fs.String("model", "", "override model id")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return errors.New("usage: claude-switch switch <provider> [--api-key sk-xxx] [--model model-id]")
	}

	provider := canonicalProviderName(providerArg)
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	if _, err := resolveProviderPreset(provider, cfg); err != nil {
		return fmt.Errorf("unsupported provider %q", providerArg)
	}

	key := strings.TrimSpace(*apiKey)
	if key == "" {
		key = strings.TrimSpace(cfg.Providers[provider].APIKey)
	}
	if key == "" {
		return fmt.Errorf("missing api key for %s, run `claude-switch set-key %s <api-key>` or pass --api-key", provider, provider)
	}

	return switchProvider(provider, cfg, key, strings.TrimSpace(*model), *claudeDir)
}

func cmdUpgrade(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repo := fs.String("repo", defaultUpgradeRepo, "GitHub repository in owner/repo form")
	tag := fs.String("tag", "", "release tag to install instead of latest")
	installPath := fs.String("install-path", "", "override target executable path")
	dryRun := fs.Bool("dry-run", false, "print the download URL and target path without installing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: claude-switch upgrade [--tag vX.Y.Z] [--install-path PATH]")
	}

	target := strings.TrimSpace(*installPath)
	if target == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate current executable: %w", err)
		}
		target = exe
	}

	opts := upgradeOptions{
		repo:        strings.TrimSpace(*repo),
		tag:         strings.TrimSpace(*tag),
		installPath: target,
		baseURL:     "https://github.com",
		client:      http.DefaultClient,
		out:         out,
		dryRun:      *dryRun,
	}
	return performUpgrade(opts)
}

type upgradeOptions struct {
	repo        string
	tag         string
	installPath string
	baseURL     string
	client      *http.Client
	out         io.Writer
	dryRun      bool
}

func performUpgrade(opts upgradeOptions) error {
	if opts.repo == "" {
		opts.repo = defaultUpgradeRepo
	}
	if opts.baseURL == "" {
		opts.baseURL = "https://github.com"
	}
	if opts.client == nil {
		opts.client = &http.Client{Timeout: 5 * time.Minute}
	}
	if opts.out == nil {
		opts.out = io.Discard
	}

	asset, err := upgradeAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if opts.installPath == "" {
		return errors.New("missing install path")
	}

	targetTag := strings.TrimSpace(opts.tag)
	if targetTag == "" || targetTag == "latest" {
		targetTag, err = latestReleaseTag(opts.client, opts.baseURL, opts.repo)
		if err != nil {
			return err
		}
	}
	if shouldSkipUpgrade(version, targetTag) {
		fmt.Fprintf(opts.out, "claude-switch is already up to date (%s)\n", version)
		return nil
	}
	if strings.TrimSpace(version) != "" && version != "dev" {
		fmt.Fprintf(opts.out, "current: %s\n", version)
	}
	if targetTag != "" {
		fmt.Fprintf(opts.out, "latest: %s\n", targetTag)
	}

	downloadURL := releaseDownloadURL(opts.baseURL, opts.repo, targetTag, asset)
	fmt.Fprintf(opts.out, "target: %s\n", opts.installPath)
	fmt.Fprintf(opts.out, "download: %s\n", downloadURL)
	if opts.dryRun {
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "claude-switch-upgrade-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, asset)
	if err := downloadFile(opts.client, downloadURL, archivePath); err != nil {
		return err
	}

	binaryName := "cs"
	if runtime.GOOS == "windows" {
		binaryName = "cs.exe"
	}
	extractedPath := filepath.Join(tmpDir, binaryName)
	if strings.HasSuffix(asset, ".zip") {
		err = extractZipBinary(archivePath, binaryName, extractedPath)
	} else {
		err = extractTarGzBinary(archivePath, binaryName, extractedPath)
	}
	if err != nil {
		return err
	}

	mode := os.FileMode(0o755)
	if info, err := os.Stat(opts.installPath); err == nil {
		mode = info.Mode().Perm() | 0o111
	}
	if err := os.Chmod(extractedPath, mode); err != nil {
		return err
	}
	if err := replaceExecutable(extractedPath, opts.installPath); err != nil {
		return err
	}

	fmt.Fprintf(opts.out, "upgraded claude-switch to latest release\n")
	return nil
}

func latestReleaseTag(client *http.Client, baseURL, repo string) (string, error) {
	latestURL := fmt.Sprintf("%s/%s/releases/latest", strings.TrimRight(strings.TrimSpace(baseURL), "/"), strings.Trim(strings.TrimSpace(repo), "/"))
	req, err := http.NewRequest(http.MethodGet, latestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "claude-switch/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("check latest release failed: %s", resp.Status)
	}
	tag := tagFromReleaseURL(resp.Request.URL)
	if tag == "" {
		return "", fmt.Errorf("could not determine latest release tag from %s", resp.Request.URL.String())
	}
	return tag, nil
}

func tagFromReleaseURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "tag" {
			continue
		}
		tag, err := url.PathUnescape(parts[i+1])
		if err != nil {
			return ""
		}
		return tag
	}
	return ""
}

func shouldSkipUpgrade(current, target string) bool {
	current = strings.TrimSpace(current)
	target = strings.TrimSpace(target)
	if current == "" || current == "dev" || target == "" {
		return false
	}
	cmp, ok := compareReleaseVersions(current, target)
	if !ok {
		return current == target
	}
	return cmp >= 0
}

func compareReleaseVersions(current, target string) (int, bool) {
	currentVersion, ok := parseReleaseVersion(current)
	if !ok {
		return 0, false
	}
	targetVersion, ok := parseReleaseVersion(target)
	if !ok {
		return 0, false
	}
	for i := 0; i < len(currentVersion.numbers) || i < len(targetVersion.numbers); i++ {
		left := 0
		if i < len(currentVersion.numbers) {
			left = currentVersion.numbers[i]
		}
		right := 0
		if i < len(targetVersion.numbers) {
			right = targetVersion.numbers[i]
		}
		switch {
		case left > right:
			return 1, true
		case left < right:
			return -1, true
		}
	}
	switch {
	case currentVersion.preRelease == "" && targetVersion.preRelease != "":
		return 1, true
	case currentVersion.preRelease != "" && targetVersion.preRelease == "":
		return -1, true
	case currentVersion.preRelease > targetVersion.preRelease:
		return 1, true
	case currentVersion.preRelease < targetVersion.preRelease:
		return -1, true
	default:
		return 0, true
	}
}

type releaseVersion struct {
	numbers    []int
	preRelease string
}

func parseReleaseVersion(value string) (releaseVersion, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	value = strings.TrimPrefix(value, "V")
	if value == "" {
		return releaseVersion{}, false
	}
	if plus := strings.Index(value, "+"); plus >= 0 {
		value = value[:plus]
	}
	preRelease := ""
	if dash := strings.Index(value, "-"); dash >= 0 {
		preRelease = value[dash+1:]
		value = value[:dash]
	}
	parts := strings.Split(value, ".")
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return releaseVersion{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return releaseVersion{}, false
		}
		numbers = append(numbers, n)
	}
	return releaseVersion{numbers: numbers, preRelease: preRelease}, true
}

func upgradeAssetName(goos, goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported architecture for upgrade: %s", goarch)
	}

	switch goos {
	case "darwin", "linux":
		return fmt.Sprintf("claude-switch-%s-%s.tar.gz", goos, goarch), nil
	case "windows":
		return fmt.Sprintf("claude-switch-%s-%s.zip", goos, goarch), nil
	default:
		return "", fmt.Errorf("unsupported OS for upgrade: %s", goos)
	}
}

func releaseDownloadURL(baseURL, repo, tag, asset string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	asset = url.PathEscape(asset)
	if strings.TrimSpace(tag) == "" || strings.TrimSpace(tag) == "latest" {
		return fmt.Sprintf("%s/%s/releases/latest/download/%s", baseURL, repo, asset)
	}
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", baseURL, repo, url.PathEscape(strings.TrimSpace(tag)), asset)
}

func downloadFile(client *http.Client, downloadURL, dest string) error {
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "claude-switch/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, resp.Body)
	return err
}

func extractTarGzBinary(archivePath, binaryName, dest string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != binaryName {
			continue
		}
		return writeExtractedBinary(reader, dest)
	}
	return fmt.Errorf("archive does not contain %s", binaryName)
}

func extractZipBinary(archivePath, binaryName, dest string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		if file.FileInfo().IsDir() || filepath.Base(file.Name) != binaryName {
			continue
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		return writeExtractedBinary(src, dest)
	}
	return fmt.Errorf("archive does not contain %s", binaryName)
}

func writeExtractedBinary(src io.Reader, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, src); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func replaceExecutable(src, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	backup := fmt.Sprintf("%s.old.%d", target, time.Now().UnixNano())
	renamedExisting := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("prepare existing executable for replacement: %w", err)
		}
		renamedExisting = true
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := moveFile(src, target); err != nil {
		if renamedExisting {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("install upgraded executable: %w", err)
	}
	if renamedExisting {
		_ = os.Remove(backup)
	}
	return nil
}

func moveFile(src, target string) error {
	if err := os.Rename(src, target); err == nil {
		return nil
	}
	if err := copyFile(src, target); err != nil {
		return err
	}
	_ = os.Remove(src)
	return nil
}

func copyFile(src, target string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return err
	}
	dest, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dest, source); err != nil {
		dest.Close()
		return err
	}
	return dest.Close()
}

func splitSwitchArgs(args []string) (string, []string) {
	provider := ""
	flagArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if provider == "" && !strings.HasPrefix(arg, "-") {
			provider = arg
			continue
		}
		flagArgs = append(flagArgs, arg)
		if switchFlagNeedsValue(arg) && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return provider, flagArgs
}

func switchFlagNeedsValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch arg {
	case "-api-key", "--api-key", "-model", "--model", "-claude-dir", "--claude-dir":
		return true
	default:
		return false
	}
}

func switchProvider(provider string, cfg *AppConfig, apiKey, modelOverride, claudeDir string) error {
	preset, err := resolveSwitchPreset(provider, cfg, modelOverride)
	if err != nil {
		return err
	}

	settingsPath := claudeSettingsPath(claudeDir)
	root, err := readJSONMap(settingsPath)
	if err != nil {
		return err
	}

	if err := backupIfExists(settingsPath); err != nil {
		return err
	}

	applyPreset(root, preset, apiKey, "")
	if err := writeJSONAtomic(settingsPath, root); err != nil {
		return err
	}

	fmt.Printf("switched Claude to %s\n", preset.Name)
	fmt.Printf("settings: %s\n", settingsPath)
	fmt.Printf("base_url: %s\n", preset.BaseURL)
	fmt.Printf("model: %s\n", preset.Model)
	return nil
}

func printUsage() {
	fmt.Println(`claude-switch

Usage:
  cs --version
  cs list
  cs [--reset-key]                     # interactive TUI
  cs current [--claude-dir DIR]
  cs set-key <provider> <api-key>
  cs switch <provider> [--api-key sk-xxx] [--model model-id] [--claude-dir DIR]
  cs upgrade

	Providers:
	  deepseek
	  minimax-cn
	  minimax-global
	  openrouter
	  opencode-go`)
}

func sortedProviderNames(cfg *AppConfig, includeCustomOption bool) []string {
	providerCount := len(providerPresets)
	if cfg.Providers != nil {
		providerCount += len(cfg.Providers)
	}
	names := make([]string, 0, providerCount+1)
	for name := range providerPresets {
		names = append(names, name)
	}
	for name, stored := range cfg.Providers {
		if _, ok := providerPresets[name]; ok {
			continue
		}
		if strings.TrimSpace(stored.BaseURL) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if includeCustomOption {
		names = append(names, customProviderOption)
	}
	return names
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func canonicalProviderName(name string) string {
	normalized := normalizeProviderName(name)
	if canonical, ok := providerAliases[normalized]; ok {
		return canonical
	}
	return normalized
}

func resolveProviderPreset(provider string, cfg *AppConfig) (ProviderPreset, error) {
	if preset, ok := providerPresets[provider]; ok {
		if stored, ok := cfg.Providers[provider]; ok && strings.TrimSpace(stored.Model) != "" {
			preset = withSelectedModel(preset, stored.Model)
		}
		return preset, nil
	}

	stored, ok := cfg.Providers[provider]
	if !ok || strings.TrimSpace(stored.BaseURL) == "" {
		return ProviderPreset{}, fmt.Errorf("unsupported provider %q", provider)
	}
	model := strings.TrimSpace(stored.Model)
	if model == "" {
		model = "custom-model"
	}
	return ProviderPreset{
		Name:     firstNonEmpty(stored.Name, provider),
		BaseURL:  strings.TrimSpace(stored.BaseURL),
		Model:    model,
		Models:   []string{model},
		Haiku:    model,
		Sonnet:   model,
		Opus:     model,
		Subagent: model,
	}, nil
}

func resolveSwitchPreset(provider string, cfg *AppConfig, modelOverride string) (ProviderPreset, error) {
	if preset, ok := providerPresets[provider]; ok {
		model := strings.TrimSpace(modelOverride)
		if model == "" {
			model = strings.TrimSpace(cfg.Providers[provider].Model)
		}
		return withSelectedModel(preset, model), nil
	}

	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return ProviderPreset{}, err
	}
	return withSelectedModel(preset, modelOverride), nil
}

func upsertProviderConfig(cfg *AppConfig, selection ConfigureSelection, apiKey string) {
	stored := cfg.Providers[selection.Provider]
	stored.APIKey = apiKey
	stored.Model = strings.TrimSpace(selection.Model)
	if selection.Name != "" {
		stored.Name = strings.TrimSpace(selection.Name)
	}
	if selection.BaseURL != "" {
		stored.BaseURL = strings.TrimSpace(selection.BaseURL)
	}
	cfg.Providers[selection.Provider] = stored
}

func detectProvider(baseURL, model string) string {
	switch {
	case strings.Contains(baseURL, "api.minimaxi.com"):
		return "minimax-cn"
	case strings.Contains(baseURL, "api.minimax.io"):
		return "minimax-global"
	case strings.Contains(baseURL, "openrouter.ai"):
		return "openrouter"
	case strings.Contains(baseURL, "api.deepseek.com"):
		return "deepseek"
	case strings.Contains(baseURL, "opencode.ai") || strings.HasPrefix(model, "opencode-go/"):
		return "opencode-go"
	default:
		return customDetectedProvider
	}
}

func withSelectedModel(preset ProviderPreset, model string) ProviderPreset {
	model = strings.TrimSpace(model)
	if model == "" {
		return preset
	}

	isPresetModel := containsString(preset.Models, model)
	preset.Model = model
	if !isPresetModel {
		preset.Models = append([]string{model}, preset.Models...)
		preset.Haiku = model
		preset.Sonnet = model
		preset.Opus = model
		preset.Subagent = model
	}
	return preset
}

func promptConfigureSelectionFallback(reader *bufio.Reader, out io.Writer, cfg *AppConfig, currentProvider, currentModel string) (ConfigureSelection, error) {
	names := sortedProviderNames(cfg, true)

	for {
		fmt.Fprintln(out, "Providers:")
		for i, name := range names {
			if name == customProviderOption {
				fmt.Fprintf(out, "  %d) custom...\n", i+1)
				continue
			}
			preset, err := resolveProviderPreset(name, cfg)
			if err != nil {
				continue
			}
			label := providerTitle(name, cfg)
			if name == currentProvider {
				label += " [current]"
			}
			fmt.Fprintf(out, "  %d) %s - %s\n", i+1, label, preset.BaseURL)
		}
		fmt.Fprint(out, "Provider: ")
		text, err := readLine(reader)
		if err != nil {
			return ConfigureSelection{}, err
		}
		provider, err := resolveProviderSelection(text, names)
		if err == nil {
			if provider == customProviderOption {
				return promptCustomProviderFallback(reader, out, cfg)
			}

			defaultModel := defaultSelectionModel(cfg, provider, currentProvider, currentModel)
			preset, _ := resolveProviderPreset(provider, cfg)

			fmt.Fprintf(out, "Model (default: %s): ", defaultModel)
			modelText, err := readLine(reader)
			if err != nil {
				return ConfigureSelection{}, err
			}
			modelText = strings.TrimSpace(modelText)
			if modelText == "" {
				modelText = defaultModel
			}

			savedModel := ""
			if modelText != defaultModel && !containsString(preset.Models, modelText) {
				savedModel = modelText
			}

			return ConfigureSelection{
				Provider:   provider,
				Model:      modelText,
				SavedModel: savedModel,
			}, nil
		}
		fmt.Fprintf(out, "\nInvalid provider: %s\n", strings.TrimSpace(text))
	}
}

func runArrowTUI(cfg *AppConfig, currentProvider, currentModel string) (ConfigureSelection, error) {
	names := sortedProviderNames(cfg, true)
	if len(names) == 0 {
		return ConfigureSelection{}, errors.New("no providers configured")
	}

	app := tview.NewApplication()
	pages := tview.NewPages()

	selectedProvider := names[0]
	for _, name := range names {
		if name == currentProvider {
			selectedProvider = name
			break
		}
	}

	typedAPIKeys := map[string]string{}
	resetKeys := map[string]bool{}
	customModels := map[string]string{}

	var (
		result    ConfigureSelection
		resultErr error = errors.New("cancelled")
	)

	buildModels := func(provider string) []string {
		models := providerModels(cfg, provider)
		customModel := strings.TrimSpace(customModels[provider])
		if customModel == "" {
			return models
		}
		filtered := []string{customModel}
		for _, model := range models {
			if model != customModel {
				filtered = append(filtered, model)
			}
		}
		return filtered
	}

	finishSelection := func(provider, model string) {
		customModel := strings.TrimSpace(customModels[provider])
		savedModel := ""
		if model == customModel && !containsString(providerModels(cfg, provider), model) {
			savedModel = model
		}
		result = ConfigureSelection{
			Provider:   provider,
			Model:      model,
			ResetKey:   resetKeys[provider],
			APIKey:     strings.TrimSpace(typedAPIKeys[provider]),
			SavedModel: savedModel,
		}
		resultErr = nil
		app.Stop()
	}

	var showProviders func()
	var showDetail func(string, string)
	var showModels func(string, string)
	var showKeyForm func(string, string, func())
	var showCustomModelForm func(string)
	var showCustomProviderForm func()

	providerList := tview.NewList()
	providerList.ShowSecondaryText(true)
	providerList.SetBorder(true)
	providerList.SetTitle(" Providers ")

	providerHelp := tview.NewTextView()
	providerHelp.SetText("Enter/→ details   q/esc quit")

	providerPage := tview.NewFlex()
	providerPage.SetDirection(tview.FlexRow)
	providerPage.AddItem(providerList, 0, 1, true)
	providerPage.AddItem(providerHelp, 1, 0, false)
	pages.AddPage("providers", providerPage, true, true)

	rebuildProviderList := func() {
		providerList.Clear()
		selectedIndex := 0
		for i, name := range names {
			if name == selectedProvider {
				selectedIndex = i
			}
			if name == customProviderOption {
				providerList.AddItem("custom...", "Add a custom Anthropic-compatible provider", 0, nil)
				continue
			}
			preset, err := resolveProviderPreset(name, cfg)
			if err != nil {
				continue
			}
			suffix := []string{}
			if name == currentProvider {
				suffix = append(suffix, "current")
			}
			if strings.TrimSpace(cfg.Providers[name].APIKey) != "" {
				suffix = append(suffix, "saved")
			}
			title := providerTitle(name, cfg)
			if len(suffix) > 0 {
				title += " [" + strings.Join(suffix, ", ") + "]"
			}
			providerList.AddItem(title, preset.BaseURL, 0, nil)
		}
		providerList.SetCurrentItem(selectedIndex)
	}

	providerList.SetChangedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index >= 0 && index < len(names) {
			selectedProvider = names[index]
		}
	})
	providerList.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(names) {
			return
		}
		selectedProvider = names[index]
		if selectedProvider == customProviderOption {
			showCustomProviderForm()
			return
		}
		showDetail(selectedProvider, "providers")
	})
	providerList.SetDoneFunc(func() {
		app.Stop()
	})
	providerList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch {
		case event.Key() == tcell.KeyRight:
			index := providerList.GetCurrentItem()
			if index >= 0 && index < len(names) {
				selectedProvider = names[index]
				if selectedProvider == customProviderOption {
					showCustomProviderForm()
				} else {
					showDetail(selectedProvider, "providers")
				}
			}
			return nil
		case event.Key() == tcell.KeyEscape:
			app.Stop()
			return nil
		case event.Rune() == 'q' || event.Rune() == 'Q':
			app.Stop()
			return nil
		}
		return event
	})

	detailText := tview.NewTextView()
	detailText.SetDynamicColors(true)
	detailText.SetWrap(true)
	detailText.SetBorder(true)
	detailText.SetTitle(" Provider Details ")

	showProviders = func() {
		rebuildProviderList()
		pages.SwitchToPage("providers")
		app.SetFocus(providerList)
	}

	showDetail = func(provider, backPage string) {
		selectedProvider = provider
		preset, err := resolveProviderPreset(provider, cfg)
		if err != nil {
			resultErr = err
			app.Stop()
			return
		}
		hasSavedKey := strings.TrimSpace(cfg.Providers[provider].APIKey) != ""
		var b strings.Builder
		fmt.Fprintf(&b, "[::b]Provider[::-]  %s\n", providerTitle(provider, cfg))
		fmt.Fprintf(&b, "[::b]Preset[::-]    %s\n", preset.Name)
		fmt.Fprintf(&b, "[::b]Base URL[::-]  %s\n", preset.BaseURL)
		fmt.Fprintf(&b, "[::b]Saved Key[::-] %s\n", maskAPIKey(cfg.Providers[provider].APIKey))
		fmt.Fprintf(&b, "[::b]Active[::-]    %s / %s\n", currentProviderLabel(currentProvider), currentModelLabel(currentModel))
		if resetKeys[provider] {
			fmt.Fprintf(&b, "[yellow]Pending key update on apply[-]\n")
		} else if !hasSavedKey {
			fmt.Fprintf(&b, "[yellow]No saved key yet[-]\n")
		}
		detailText.SetText(b.String())

		actions := tview.NewList()
		actions.ShowSecondaryText(false)
		actions.SetBorder(true)
		actions.SetTitle(" Actions ")
		actions.AddItem("Choose Model", "", 'm', func() {
			showModels(provider, "detail")
		})
		actions.AddItem("Edit API Key", "", 'k', func() {
			showKeyForm(provider, backPage, func() {
				showDetail(provider, backPage)
			})
		})
		actions.AddItem("Back", "", 'b', showProviders)
		actions.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			switch {
			case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
				showProviders()
				return nil
			case event.Rune() == 'q' || event.Rune() == 'Q':
				showProviders()
				return nil
			case event.Rune() == 'k' || event.Rune() == 'K':
				showKeyForm(provider, backPage, func() {
					showDetail(provider, backPage)
				})
				return nil
			}
			return event
		})

		page := tview.NewFlex()
		page.SetDirection(tview.FlexRow)
		page.AddItem(detailText, 0, 1, false)
		page.AddItem(actions, 8, 0, true)
		pages.AddAndSwitchToPage("detail", page, true)
		app.SetFocus(actions)
	}

	showKeyForm = func(provider, backPage string, onSave func()) {
		currentValue := strings.TrimSpace(typedAPIKeys[provider])
		keyValue := currentValue
		form := tview.NewForm()
		form.AddPasswordField("API Key", currentValue, 0, '*', func(text string) {
			keyValue = text
		})
		form.AddButton("Save", func() {
			keyValue = strings.TrimSpace(keyValue)
			typedAPIKeys[provider] = keyValue
			resetKeys[provider] = true
			onSave()
		})
		form.AddButton("Cancel", onSave)
		form.SetBorder(true)
		form.SetTitle(" Edit API Key ")
		form.SetButtonsAlign(tview.AlignLeft)
		form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				onSave()
				return nil
			}
			return event
		})
		help := tview.NewTextView()
		help.SetText(fmt.Sprintf("Provider: %s", providerTitle(provider, cfg)))
		page := tview.NewFlex()
		page.SetDirection(tview.FlexRow)
		page.AddItem(help, 1, 0, false)
		page.AddItem(form, 0, 1, true)
		pages.AddAndSwitchToPage("key", page, true)
		app.SetFocus(form)
	}

	showCustomModelForm = func(provider string) {
		modelValue := strings.TrimSpace(customModels[provider])
		form := tview.NewForm()
		form.AddInputField("Model", modelValue, 0, nil, func(text string) {
			modelValue = text
		})
		form.AddButton("Save", func() {
			modelValue = strings.TrimSpace(modelValue)
			if modelValue == "" {
				return
			}
			customModels[provider] = modelValue
			showModels(provider, "detail")
		})
		form.AddButton("Cancel", func() {
			showModels(provider, "detail")
		})
		form.SetBorder(true)
		form.SetTitle(" Custom Model ")
		form.SetButtonsAlign(tview.AlignLeft)
		form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				showModels(provider, "detail")
				return nil
			}
			return event
		})
		pages.AddAndSwitchToPage("custom-model", form, true)
		app.SetFocus(form)
	}

	showModels = func(provider, backPage string) {
		selectedProvider = provider
		models := buildModels(provider)
		modelList := tview.NewList()
		modelList.ShowSecondaryText(false)
		modelList.SetBorder(true)
		modelList.SetTitle(" Models ")
		for _, model := range models {
			label := model
			if model == defaultSelectionModel(cfg, provider, currentProvider, currentModel) {
				label += " [default]"
			}
			modelName := model
			modelList.AddItem(label, "", 0, func() {
				if !hasConfigurableKey(strings.TrimSpace(cfg.Providers[provider].APIKey), typedAPIKeys[provider], resetKeys[provider]) {
					showKeyForm(provider, backPage, func() {
						showModels(provider, backPage)
					})
					return
				}
				finishSelection(provider, modelName)
			})
		}
		modelList.AddItem("Custom model...", "", 0, func() {
			showCustomModelForm(provider)
		})
		selectedIndex := modelIndex(cfg, provider, currentProvider, currentModel)
		if customModel := strings.TrimSpace(customModels[provider]); customModel != "" {
			selectedIndex = 0
		}
		if selectedIndex >= 0 && selectedIndex < len(models) {
			modelList.SetCurrentItem(selectedIndex)
		}
		modelList.SetDoneFunc(func() {
			showDetail(provider, backPage)
		})
		modelList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			switch {
			case event.Key() == tcell.KeyLeft || event.Key() == tcell.KeyEscape:
				showDetail(provider, backPage)
				return nil
			case event.Rune() == 'q' || event.Rune() == 'Q':
				showDetail(provider, backPage)
				return nil
			case event.Rune() == 'k' || event.Rune() == 'K':
				showKeyForm(provider, backPage, func() {
					showModels(provider, backPage)
				})
				return nil
			case event.Rune() == 'c' || event.Rune() == 'C':
				showCustomModelForm(provider)
				return nil
			}
			return event
		})
		help := tview.NewTextView()
		help.SetText("Enter apply   c custom model   k edit key   q/esc/← back")
		page := tview.NewFlex()
		page.SetDirection(tview.FlexRow)
		page.AddItem(modelList, 0, 1, true)
		page.AddItem(help, 1, 0, false)
		pages.AddAndSwitchToPage("models", page, true)
		app.SetFocus(modelList)
	}

	showCustomProviderForm = func() {
		nameValue := ""
		baseURLValue := ""
		apiKeyValue := ""
		modelValue := ""
		form := tview.NewForm()
		form.AddInputField("Name", "", 0, nil, func(text string) {
			nameValue = text
		})
		form.AddInputField("Base URL", "", 0, nil, func(text string) {
			baseURLValue = text
		})
		form.AddPasswordField("API Key", "", 0, '*', func(text string) {
			apiKeyValue = text
		})
		form.AddInputField("Model", "", 0, nil, func(text string) {
			modelValue = text
		})
		form.AddButton("Save", func() {
			nameValue = strings.TrimSpace(nameValue)
			baseURLValue = strings.TrimSpace(baseURLValue)
			apiKeyValue = strings.TrimSpace(apiKeyValue)
			modelValue = strings.TrimSpace(modelValue)
			if nameValue == "" || baseURLValue == "" || apiKeyValue == "" || modelValue == "" {
				return
			}
			result = ConfigureSelection{
				Provider: uniqueCustomProviderKey(cfg, makeCustomProviderKey(nameValue)),
				Name:     nameValue,
				BaseURL:  baseURLValue,
				APIKey:   apiKeyValue,
				Model:    modelValue,
			}
			resultErr = nil
			app.Stop()
		})
		form.AddButton("Cancel", showProviders)
		form.SetBorder(true)
		form.SetTitle(" Custom Provider ")
		form.SetButtonsAlign(tview.AlignLeft)
		form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				showProviders()
				return nil
			}
			return event
		})
		pages.AddAndSwitchToPage("custom-provider", form, true)
		app.SetFocus(form)
	}

	app.SetRoot(pages, true)
	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlC {
			app.Stop()
			return nil
		}
		return event
	})
	showProviders()
	if err := app.Run(); err != nil {
		return ConfigureSelection{}, err
	}
	if resultErr != nil {
		return ConfigureSelection{}, resultErr
	}
	return result, nil
}

func promptAPIKey(reader *bufio.Reader, out io.Writer, provider string) (string, error) {
	fmt.Fprintf(out, "Enter API key for %s:\n", provider)
	for {
		fmt.Fprint(out, "API key: ")
		text, err := readLine(reader)
		if err != nil {
			return "", err
		}
		key := strings.TrimSpace(text)
		if key != "" {
			return key, nil
		}
		fmt.Fprintln(out, "API key cannot be empty.")
	}
}

func promptCustomProviderFallback(reader *bufio.Reader, out io.Writer, cfg *AppConfig) (ConfigureSelection, error) {
	fmt.Fprintln(out, "Create custom provider")
	fmt.Fprint(out, "Name: ")
	name, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ConfigureSelection{}, errors.New("custom provider name cannot be empty")
	}
	fmt.Fprint(out, "Base URL: ")
	baseURL, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ConfigureSelection{}, errors.New("custom provider base url cannot be empty")
	}
	fmt.Fprint(out, "API Key: ")
	apiKey, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ConfigureSelection{}, errors.New("custom provider api key cannot be empty")
	}
	fmt.Fprint(out, "Model: ")
	model, err := readLine(reader)
	if err != nil {
		return ConfigureSelection{}, err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return ConfigureSelection{}, errors.New("custom provider model cannot be empty")
	}

	return ConfigureSelection{
		Provider: uniqueCustomProviderKey(cfg, makeCustomProviderKey(name)),
		Name:     name,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
	}, nil
}

func hasConfigurableKey(savedKey, typedKey string, resetKey bool) bool {
	if strings.TrimSpace(typedKey) != "" {
		return true
	}
	if resetKey {
		return false
	}
	return strings.TrimSpace(savedKey) != ""
}

func providerTitle(name string, cfg *AppConfig) string {
	if name == customProviderOption {
		return "custom..."
	}
	if stored, ok := cfg.Providers[name]; ok && strings.TrimSpace(stored.Name) != "" && !isPresetProvider(name) {
		return strings.TrimSpace(stored.Name)
	}
	return name
}

func isPresetProvider(name string) bool {
	_, ok := providerPresets[name]
	return ok
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func makeCustomProviderKey(name string) string {
	normalized := normalizeProviderName(name)
	normalized = strings.ReplaceAll(normalized, " ", "-")
	normalized = strings.ReplaceAll(normalized, "/", "-")
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.Trim(normalized, "-")
	if normalized == "" {
		return "custom-provider"
	}
	return normalized
}

func uniqueCustomProviderKey(cfg *AppConfig, base string) string {
	if _, exists := cfg.Providers[base]; !exists && !isPresetProvider(base) && !isProviderAlias(base) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, exists := cfg.Providers[candidate]; !exists && !isPresetProvider(candidate) && !isProviderAlias(candidate) {
			return candidate
		}
	}
}

func isProviderAlias(name string) bool {
	_, ok := providerAliases[name]
	return ok
}

func currentConfiguredProvider(cfg *AppConfig, claudeDir string) (string, string) {
	root, err := readJSONMap(claudeSettingsPath(claudeDir))
	if err != nil {
		return "", ""
	}
	env := nestedMap(root, "env")
	if env == nil {
		return "", ""
	}
	baseURL, _ := env["ANTHROPIC_BASE_URL"].(string)
	model, _ := env["ANTHROPIC_MODEL"].(string)
	if provider := detectProvider(baseURL, model); provider != customDetectedProvider {
		return provider, model
	}
	for name, stored := range cfg.Providers {
		if strings.TrimSpace(stored.BaseURL) == strings.TrimSpace(baseURL) {
			return name, model
		}
	}
	return customDetectedProvider, model
}

func readLine(reader *bufio.Reader) (string, error) {
	text, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && text == "" {
		return "", io.EOF
	}
	return strings.TrimRight(text, "\r\n"), nil
}

func resolveProviderSelection(input string, names []string) (string, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return "", errors.New("empty provider")
	}

	if idx, err := strconv.Atoi(text); err == nil {
		if idx >= 1 && idx <= len(names) {
			return names[idx-1], nil
		}
		return "", errors.New("provider index out of range")
	}

	provider := canonicalProviderName(text)
	if provider == "custom" || provider == "custom..." {
		return customProviderOption, nil
	}
	if _, ok := providerPresets[provider]; !ok {
		for _, name := range names {
			if name == provider {
				return provider, nil
			}
		}
		return "", errors.New("unsupported provider")
	}
	return provider, nil
}

func defaultSelectionModel(cfg *AppConfig, provider, currentProvider, currentModel string) string {
	if provider == currentProvider && currentModel != "" {
		for _, model := range providerModels(cfg, provider) {
			if model == currentModel {
				return currentModel
			}
		}
	}
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return ""
	}
	return preset.Model
}

func providerModels(cfg *AppConfig, provider string) []string {
	preset, err := resolveProviderPreset(provider, cfg)
	if err != nil {
		return nil
	}
	if len(preset.Models) == 0 {
		return []string{preset.Model}
	}
	return preset.Models
}

func modelIndex(cfg *AppConfig, provider, currentProvider, currentModel string) int {
	selected := defaultSelectionModel(cfg, provider, currentProvider, currentModel)
	for i, model := range providerModels(cfg, provider) {
		if model == selected {
			return i
		}
	}
	return 0
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "not saved"
	}
	if len(key) <= 6 {
		return strings.Repeat("*", len(key))
	}
	return key[:3] + strings.Repeat("*", len(key)-6) + key[len(key)-3:]
}

func currentProviderLabel(provider string) string {
	if provider == "" {
		return "none"
	}
	return provider
}

func currentModelLabel(model string) string {
	if model == "" {
		return "none"
	}
	return model
}

func shouldUseArrowTUI(in *os.File) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := in.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func applyPreset(root map[string]any, preset ProviderPreset, apiKey, overrideModel string) {
	env := ensureNestedMap(root, "env")
	for _, key := range managedEnvKeys {
		delete(env, key)
	}

	preset = withSelectedModel(preset, overrideModel)
	env["ANTHROPIC_BASE_URL"] = preset.BaseURL
	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "" {
		authEnv = "ANTHROPIC_API_KEY"
	}
	env[authEnv] = apiKey
	env["ANTHROPIC_MODEL"] = preset.Model
	env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = preset.Haiku
	env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = preset.Sonnet
	env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = preset.Opus
	if preset.Subagent != "" {
		env["CLAUDE_CODE_SUBAGENT_MODEL"] = preset.Subagent
	}

	for key, value := range preset.ExtraEnv {
		env[key] = value
	}
}

func claudeSettingsPath(overrideDir string) string {
	dir := strings.TrimSpace(overrideDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".claude", "settings.json")
		}
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, "settings.json")
}

func appConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude-switch", "config.json"), nil
}

func loadAppConfig() (*AppConfig, string, error) {
	path, err := appConfigPath()
	if err != nil {
		return nil, "", err
	}
	cfg := &AppConfig{Providers: map[string]StoredProvider{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, path, nil
		}
		return nil, "", err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]StoredProvider{}
	}
	migrateLegacyProviders(cfg)
	return cfg, path, nil
}

func migrateLegacyProviders(cfg *AppConfig) {
	legacy, ok := cfg.Providers["minimax"]
	if ok {
		if _, exists := cfg.Providers["minimax-cn"]; !exists && strings.TrimSpace(legacy.APIKey) != "" {
			cfg.Providers["minimax-cn"] = legacy
		}
		delete(cfg.Providers, "minimax")
	}
}

func readJSONMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func nestedMap(root map[string]any, key string) map[string]any {
	raw, ok := root[key]
	if !ok {
		return nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

func ensureNestedMap(root map[string]any, key string) map[string]any {
	if obj := nestedMap(root, key); obj != nil {
		return obj
	}
	obj := map[string]any{}
	root[key] = obj
	return obj
}

func backupIfExists(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backupDir := filepath.Dir(path)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	backupPath := fmt.Sprintf("%s.bak.%d", path, time.Now().UnixNano())
	return os.WriteFile(backupPath, data, 0o600)
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
