package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type proxyUpstreamHealth struct {
	StatusCode int
	Err        error
}

func (h proxyUpstreamHealth) healthy() bool {
	return h.Err == nil && h.StatusCode >= 200 && h.StatusCode < 300
}

func cmdProxyHealth(args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: code-switch proxy health")
	}
	cfg, _, err := loadAppConfig()
	if err != nil {
		return err
	}
	if cfg.Proxy == nil || len(cfg.Proxy.Routes) == 0 {
		fmt.Fprintln(out, "proxy health: no routes configured")
		return nil
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	ctx := context.Background()
	fmt.Fprintln(out, "proxy upstream health:")
	healthy := true
	for _, agent := range sortedProxyRouteAgents(cfg.Proxy.Routes) {
		route, err := buildProxyRouteFromConfig(agent, cfg, "<token>")
		if err != nil {
			healthy = false
			fmt.Fprintf(out, "- agent: %s\n  status: fail\n  error: %v\n", agent, err)
			continue
		}
		result := probeProxyUpstream(ctx, client, route.UpstreamBaseURL)
		status := "ok"
		if !result.healthy() {
			healthy = false
			status = "fail"
		}
		fmt.Fprintf(out, "- agent: %s\n  provider: %s\n  upstream: %s\n  status: %s\n", agent, route.Provider, route.UpstreamBaseURL, status)
		if result.StatusCode != 0 {
			fmt.Fprintf(out, "  http_status: %d\n", result.StatusCode)
		}
		if result.Err != nil {
			fmt.Fprintf(out, "  error: %v\n", result.Err)
		}
	}
	if !healthy {
		return errors.New("one or more proxy routes are unhealthy")
	}
	return nil
}

func probeProxyUpstream(ctx context.Context, client *http.Client, upstream string) proxyUpstreamHealth {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimSpace(upstream), nil)
	if err != nil {
		return proxyUpstreamHealth{Err: err}
	}
	resp, err := client.Do(req)
	if err != nil {
		return proxyUpstreamHealth{Err: err}
	}
	defer resp.Body.Close()
	return proxyUpstreamHealth{StatusCode: resp.StatusCode}
}
