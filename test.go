package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

func cmdTest(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude, codex, or opencode")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	model := fs.String("model", "", "model id to test with")
	testPath := fs.String("path", "", "override API path (default: /v1/messages)")
	allProviders := fs.Bool("all", false, "test all configured providers for the agent and print a summary")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
	}

	if *allProviders {
		cfg, _, err := loadAppConfig()
		if err != nil {
			return err
		}
		return runAllTestsAndPrint(agent, cfg, *apiKey, strings.TrimSpace(*testPath), &http.Client{Timeout: 20 * time.Second}, out)
	}

	if providerArg == "" || fs.NArg() != 0 {
		return errors.New("usage: code-switch test <provider> [--agent claude|codex|opencode] [--api-key sk-xxx] [--model model-id] [--path /custom/api/path] [--all]")
	}

	pa, cfg, _, err := resolveProviderAndKeyForAgent(agent, providerArg, *apiKey, *model)
	if err != nil {
		return err
	}

	preset, err := resolveAgentSwitchPreset(agent, pa.Provider, cfg, pa.Model)
	if err != nil {
		return err
	}
	if agent == agentCodex {
		return testCodexProvider(out, preset, pa.APIKey)
	}
	if agent == agentOpencode {
		return testProvider(out, preset, pa.APIKey, strings.TrimSpace(*testPath))
	}

	return testProvider(out, preset, pa.APIKey, strings.TrimSpace(*testPath))
}

type testRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []testMessage `json:"messages"`
}

type testMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// testOutcome captures the raw result of a single connectivity probe.
type testOutcome struct {
	OK         bool
	StatusCode int
	URL        string
	Body       []byte
	RequestErr error
	ReadErr    error
}

// runTestRequest executes one connectivity probe and returns a structured
// outcome plus an error that mirrors the historical error semantics of
// testProviderWithClient (non-nil for request/read failures and non-2xx status).
func runTestRequest(ctx context.Context, preset ProviderPreset, apiKey, testPath string, client *http.Client) (testOutcome, error) {
	baseURL := strings.TrimRight(preset.BaseURL, "/")
	tp := strings.TrimSpace(testPath)
	if tp == "" {
		tp = "/v1/messages"
	}
	testURL := baseURL + tp

	reqBody := testRequest{
		MaxTokens: 10,
		Messages: []testMessage{
			{Role: "user", Content: "Say hi"},
		},
	}
	if !preset.NoModel {
		reqBody.Model = preset.Model
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return testOutcome{URL: testURL}, fmt.Errorf("marshal test request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, testURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return testOutcome{URL: testURL}, fmt.Errorf("create test request: %w", err)
	}

	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "ANTHROPIC_API_KEY" || authEnv == "" {
		httpReq.Header.Set("x-api-key", apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "code-switch/"+version)

	resp, err := client.Do(httpReq)
	if err != nil {
		return testOutcome{URL: testURL, RequestErr: err}, fmt.Errorf("test %s: request failed: %w", preset.Name, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	oc := testOutcome{StatusCode: resp.StatusCode, URL: testURL, Body: body, ReadErr: readErr}
	if readErr != nil {
		return oc, fmt.Errorf("test %s: failed to read response body: %w", preset.Name, readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		oc.OK = true
		return oc, nil
	}
	return oc, fmt.Errorf("test %s: status %d", preset.Name, resp.StatusCode)
}

func runCodexTestRequest(ctx context.Context, preset ProviderPreset, apiKey string, client *http.Client) testOutcome {
	testURL := codexResponsesURL(preset.BaseURL)
	bodyBytes, err := json.Marshal(codexTestRequest{Model: preset.Model, Input: "Say hi"})
	if err != nil {
		return testOutcome{URL: testURL}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, testURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return testOutcome{URL: testURL}
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "code-switch/"+version)

	resp, err := client.Do(httpReq)
	if err != nil {
		return testOutcome{URL: testURL, RequestErr: err}
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	oc := testOutcome{StatusCode: resp.StatusCode, URL: testURL, Body: body, ReadErr: readErr}
	if readErr != nil {
		return oc
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		oc.OK = true
	}
	return oc
}

func testProvider(out io.Writer, preset ProviderPreset, apiKey, testPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return testProviderWithClient(ctx, out, preset, apiKey, testPath, &http.Client{})
}

func testProviderWithClient(ctx context.Context, out io.Writer, preset ProviderPreset, apiKey, testPath string, client *http.Client) error {
	fmt.Fprintf(out, "Testing %s (%s)...\n", preset.Name, preset.BaseURL)

	oc, err := runTestRequest(ctx, preset, apiKey, testPath, client)

	// Pre-request setup errors (marshal/create-request) print no FAIL block,
	// matching the original implementation.
	if oc.StatusCode == 0 && oc.RequestErr == nil && oc.ReadErr == nil && !oc.OK {
		return err
	}

	if oc.RequestErr != nil {
		fmt.Fprintf(out, "FAIL\n")
		fmt.Fprintf(out, "  URL: %s\n", oc.URL)
		fmt.Fprintf(out, "  Request failed: %v\n", oc.RequestErr)
		return err
	}
	if oc.ReadErr != nil {
		fmt.Fprintf(out, "FAIL\n")
		fmt.Fprintf(out, "  URL: %s\n", oc.URL)
		fmt.Fprintf(out, "  Status: %d\n", oc.StatusCode)
		fmt.Fprintf(out, "  Failed to read response body\n")
		return err
	}
	if oc.OK {
		fmt.Fprintf(out, "OK\n")
		fmt.Fprintf(out, "  Status: %d\n", oc.StatusCode)
		return nil
	}

	fmt.Fprintf(out, "FAIL\n")
	fmt.Fprintf(out, "  URL: %s\n", oc.URL)
	fmt.Fprintf(out, "  Status: %d\n", oc.StatusCode)
	if len(oc.Body) > 0 {
		var parsed map[string]any
		if json.Unmarshal(oc.Body, &parsed) == nil {
			if errInfo, ok := parsed["error"]; ok {
				fmt.Fprintf(out, "  Error: %v\n", errInfo)
			} else {
				fmt.Fprintf(out, "  Response: %s\n", strings.TrimSpace(string(oc.Body)))
			}
		} else {
			fmt.Fprintf(out, "  Response: %s\n", strings.TrimSpace(string(oc.Body)))
		}
	}
	return err
}

// allTestRow is one row of the `test --all` summary.
type allTestRow struct {
	Name     string
	Status   string // "ok" | "fail" | "skipped"
	HTTP     int
	Detail   string
	Duration time.Duration
}

// runAllTests probes every provider configured for the agent concurrently and
// returns one row per provider, in the same order as providerNamesForAgent.
func runAllTests(agent AgentName, cfg *AppConfig, apiKeyFlag, testPath string, client *http.Client) []allTestRow {
	names := providerNamesForAgent(agent, cfg, false, false)
	rows := make([]allTestRow, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			rows[i] = probeOneForAll(agent, cfg, name, apiKeyFlag, testPath, client)
		}(i, name)
	}
	wg.Wait()
	return rows
}

func probeOneForAll(agent AgentName, cfg *AppConfig, name, apiKeyFlag, testPath string, client *http.Client) allTestRow {
	row := allTestRow{Name: name}
	preset, err := resolveAgentSwitchPreset(agent, name, cfg, "")
	if err != nil {
		row.Status = "skipped"
		row.Detail = "unsupported preset"
		return row
	}
	key, err := resolveKey(agent, cfg, name, apiKeyFlag, preset)
	if err != nil {
		row.Status = "skipped"
		row.Detail = "no api key"
		return row
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	var oc testOutcome
	switch agent {
	case agentCodex:
		oc = runCodexTestRequest(ctx, preset, key, client)
	default:
		oc, _ = runTestRequest(ctx, preset, key, testPath, client)
	}
	row.Duration = time.Since(start)
	row.HTTP = oc.StatusCode
	if oc.OK {
		row.Status = "ok"
		return row
	}
	row.Status = "fail"
	switch {
	case oc.RequestErr != nil:
		row.Detail = "request failed"
	case oc.ReadErr != nil:
		row.Detail = "read error"
	case len(oc.Body) > 0:
		row.Detail = truncateForTable(strings.TrimSpace(string(oc.Body)))
	default:
		row.Detail = fmt.Sprintf("status %d", oc.StatusCode)
	}
	return row
}

func truncateForTable(s string) string {
	const max = 60
	// Collapse whitespace/newlines for a single-line table cell.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	// Truncate on rune boundaries so multi-byte UTF-8 characters are not
	// split. Counting runes (not bytes) keeps the resulting width stable
	// across ASCII and CJK content.
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max-1]) + "…"
	}
	return s
}

// runAllTestsAndPrint runs every provider probe and renders a summary table.
func runAllTestsAndPrint(agent AgentName, cfg *AppConfig, apiKeyFlag, testPath string, client *http.Client, out io.Writer) error {
	rows := runAllTests(agent, cfg, apiKeyFlag, testPath, client)
	return printAllTestSummary(out, agent, rows)
}

// printAllTestSummary renders the summary table for a set of probe rows and
// returns an error if any provider failed.
func printAllTestSummary(out io.Writer, agent AgentName, rows []allTestRow) error {
	var ok, failed, skipped int
	for _, row := range rows {
		switch row.Status {
		case "ok":
			ok++
		case "fail":
			failed++
		case "skipped":
			skipped++
		}
	}

	fmt.Fprintf(out, "Testing %d providers (agent: %s)...\n\n", len(rows), agent)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.Name,
			allTestStatusBadge(row.Status),
			allTestHTTPLabel(row.HTTP),
			row.Duration.Round(time.Millisecond),
			row.Detail,
		)
	}
	tw.Flush()
	fmt.Fprintln(out)
	fmt.Fprintf(out, "summary: %d ok, %d failed, %d skipped\n", ok, failed, skipped)
	if failed > 0 {
		return fmt.Errorf("%d/%d providers failed", failed, len(rows))
	}
	return nil
}

func allTestStatusBadge(status string) string {
	switch status {
	case "ok":
		return green("ok")
	case "fail":
		return red("fail")
	default:
		return dim("skip")
	}
}

func allTestHTTPLabel(code int) string {
	if code == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", code)
}
