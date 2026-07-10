package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckMCPHealthOkWhenNoServersConfigured(t *testing.T) {
	// Given
	cfg := &AppConfig{}

	// When
	result := checkMCPHealth(cfg)

	// Then
	if result.Status != "ok" {
		t.Fatalf("expected ok, got %s (%s)", result.Status, result.Detail)
	}
	if result.Name != "mcp servers" {
		t.Fatalf("expected mcp servers, got %q", result.Name)
	}
}

func TestCheckMCPHealthOkWhenAllServersHealthy(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	cfg := &AppConfig{MCPServers: map[string]MCPServerConfig{
		"stdio-ok": {Name: "stdio-ok", Transport: "stdio", Command: "true"},
		"sse-ok":   {Name: "sse-ok", Transport: "sse", URL: server.URL},
	}}

	// When
	result := checkMCPHealth(cfg)

	// Then
	if result.Status != "ok" {
		t.Fatalf("expected ok, got %s (%s)", result.Status, result.Detail)
	}
}

func TestCheckMCPHealthWarnsWhenOneServerFails(t *testing.T) {
	// Given
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	cfg := &AppConfig{MCPServers: map[string]MCPServerConfig{
		"stdio-bad": {Name: "stdio-bad", Transport: "stdio", Command: "false"},
		"sse-ok":    {Name: "sse-ok", Transport: "sse", URL: server.URL},
	}}

	// When
	result := checkMCPHealth(cfg)

	// Then
	if result.Status != "warn" {
		t.Fatalf("expected warn, got %s (%s)", result.Status, result.Detail)
	}
	if result.Name != "mcp servers" {
		t.Fatalf("expected mcp servers, got %q", result.Name)
	}
	if result.Detail == "" {
		t.Fatal("expected failure detail")
	}
}

func TestTestMCPServerStdioTrueAndFalse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if err := testMCPServer(ctx, MCPServerConfig{Name: "ok", Transport: "stdio", Command: "true"}); err != nil {
		t.Fatalf("true command returned error: %v", err)
	}
	if err := testMCPServer(ctx, MCPServerConfig{Name: "bad", Transport: "stdio", Command: "false"}); err == nil {
		t.Fatalf("false command returned nil error")
	}
}

func TestTestMCPServerSSEReachableAndUnreachable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	if err := testMCPServer(ctx, MCPServerConfig{Name: "ok", Transport: "sse", URL: server.URL}); err != nil {
		t.Fatalf("reachable sse url returned error: %v", err)
	}

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := testMCPServer(ctxTimeout, MCPServerConfig{Name: "bad", Transport: "sse", URL: "http://127.0.0.1:1/does-not-exist"}); err == nil {
		t.Fatalf("unreachable sse url returned nil error")
	}
}
