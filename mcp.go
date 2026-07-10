package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

type MCPServerConfig struct {
	Name         string            `json:"name"`
	Transport    string            `json:"transport"`
	Command      string            `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	URL          string            `json:"url,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Disabled     bool              `json:"disabled,omitempty"`
	AllowedTools []string          `json:"allowedTools,omitempty"`
	BlockedTools []string          `json:"blockedTools,omitempty"`
}

func validateMCPServerConfig(s MCPServerConfig) error {
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return fmt.Errorf("mcp server name must not be empty")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("mcp server name must not contain control characters")
		}
	}

	switch strings.TrimSpace(s.Transport) {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("stdio mcp server %q requires a command", s.Name)
		}
	case "sse":
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("sse mcp server %q requires a url", s.Name)
		}
	default:
		return fmt.Errorf("mcp server %q has invalid transport %q", s.Name, s.Transport)
	}

	return nil
}

func testMCPServer(ctx context.Context, s MCPServerConfig) error {
	if err := validateMCPServerConfig(s); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	switch strings.TrimSpace(s.Transport) {
	case "stdio":
		cmd := exec.CommandContext(ctx, s.Command, s.Args...)
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-time.After(200 * time.Millisecond):
			_ = cmd.Process.Kill()
			return <-done
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return ctx.Err()
		}
	case "sse":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("mcp sse endpoint returned %s", resp.Status)
		}
		return nil
	default:
		return fmt.Errorf("mcp server %q has invalid transport %q", s.Name, s.Transport)
	}
}
