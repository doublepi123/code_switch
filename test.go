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
	"time"
)

func cmdTest(args []string, out io.Writer) error {
	providerArg, flagArgs := splitSwitchArgs(args)
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	agentFlag := fs.String("agent", string(agentClaude), "target agent: claude or codex")
	apiKey := fs.String("api-key", "", "API key for the target provider")
	model := fs.String("model", "", "model id to test with")
	testPath := fs.String("path", "", "override API path (default: /v1/messages)")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if providerArg == "" || fs.NArg() != 0 {
		return errors.New("usage: code-switch test <provider> [--agent claude|codex] [--api-key sk-xxx] [--model model-id] [--path /custom/api/path]")
	}
	agent, err := parseAgentName(*agentFlag)
	if err != nil {
		return err
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

func testProvider(out io.Writer, preset ProviderPreset, apiKey, testPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return testProviderWithClient(ctx, out, preset, apiKey, testPath, &http.Client{})
}

func testProviderWithClient(ctx context.Context, out io.Writer, preset ProviderPreset, apiKey, testPath string, client *http.Client) error {
	baseURL := strings.TrimRight(preset.BaseURL, "/")
	tp := strings.TrimSpace(testPath)
	if tp == "" {
		tp = "/v1/messages"
	}
	testURL := baseURL + tp

	fmt.Fprintf(out, "Testing %s (%s)...\n", preset.Name, preset.BaseURL)

	reqBody := testRequest{
		Model:     preset.Model,
		MaxTokens: 10,
		Messages: []testMessage{
			{Role: "user", Content: "Say hi"},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal test request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, testURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create test request: %w", err)
	}

	authEnv := strings.TrimSpace(preset.AuthEnv)
	if authEnv == "ANTHROPIC_AUTH_TOKEN" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	} else {
		httpReq.Header.Set("x-api-key", apiKey)
	}
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
		var parsed map[string]any
		if json.Unmarshal(body, &parsed) == nil {
			if errInfo, ok := parsed["error"]; ok {
				fmt.Fprintf(out, "  Error: %v\n", errInfo)
			} else {
				fmt.Fprintf(out, "  Response: %s\n", strings.TrimSpace(string(body)))
			}
		} else {
			fmt.Fprintf(out, "  Response: %s\n", strings.TrimSpace(string(body)))
		}
	}
	return fmt.Errorf("test %s: status %d", preset.Name, resp.StatusCode)
}
