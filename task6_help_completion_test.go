package main

import (
	"bytes"
	"strings"
	"testing"
)

// Task 6 — Help, Completion, and End-to-End Verification.
//
// These tests pin the user-facing surface that must light up once Task 6 is
// complete:
//
//  1. `proxy --version` is recognized as a version request (so `cs proxy
//     --version` prints the version instead of dispatching into the proxy
//     subcommand parser, which would otherwise reject `--version` as an
//     unknown proxy subcommand).
//  2. printUsage advertises every `cs proxy <sub>` entry a user can invoke:
//     configure / start / stop / status / preview / serve.
//  3. The three shell-completion generators (bash/zsh/fish) surface the
//     top-level `proxy` command AND the second-level proxy subcommand list
//     so tab-completion works for `cs proxy <TAB>` and `cs proxy se<TAB>`.

// ---- proxy --version ----

func TestIsVersionRequestProxyVersion(t *testing.T) {
	cases := [][]string{
		{"proxy", "--version"},
	}
	for _, args := range cases {
		if !isVersionRequest(args) {
			t.Fatalf("isVersionRequest(%v) = false, want true", args)
		}
	}
}

func TestProxyVersionRunsWithoutError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	// `cs proxy --version` must be intercepted by the version-request path
	// BEFORE the proxy dispatcher runs. If it leaks into cmdProxy, the
	// dispatcher treats "--version" as an unknown proxy subcommand and
	// returns an error. So a nil error AND a non-empty version line is the
	// success signal.
	if err := runWithIO([]string{"proxy", "--version"}, nil, &out); err != nil {
		t.Fatalf("runWithIO(proxy --version) error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "code-switch") {
		t.Fatalf("proxy --version output missing program name: %q", got)
	}
	if !strings.Contains(got, version) {
		t.Fatalf("proxy --version output missing version %q: got %q", version, got)
	}
}

// ---- printUsage advertises every proxy subcommand ----

func TestPrintUsageIncludesAllProxySubcommandsTask6(t *testing.T) {
	var out bytes.Buffer
	printUsage(&out)
	s := out.String()
	// Every documented subcommand must appear in usage.
	for _, want := range []string{
		"cs proxy configure",
		"cs proxy preview",
		"cs proxy status",
		"cs proxy start",
		"cs proxy stop",
		"cs proxy serve",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("printUsage missing %q\noutput:\n%s", want, s)
		}
	}
}

// ---- completion: top-level + second-level proxy entries ----

func TestBashCompletionIncludesProxyTopLevelAndSubcommandsTask6(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := bashCompletionString()
	// Top-level: the word list at cword==1 must include "proxy".
	if !strings.Contains(s, " proxy ") {
		t.Fatalf("bash completion top-level word list missing proxy:\n%s", s)
	}
	// Second-level: the proxy case must enumerate all six subcommands.
	for _, sub := range []string{"configure", "start", "stop", "status", "preview", "serve"} {
		marker := "\"" + sub + "\""
		// bash completion string contains the subcommand list inside the
		// proxy case; assert each word is present at least once.
		if !strings.Contains(s, sub) {
			t.Fatalf("bash completion missing proxy subcommand %s:\n%s", sub, s)
		}
		_ = marker
	}
	// Sanity: the proxy second-level completion line is present verbatim.
	if !strings.Contains(s, "configure start stop status preview serve") {
		t.Fatalf("bash completion missing proxy subcommand word list:\n%s", s)
	}
}

func TestZshCompletionIncludesProxyTopLevelAndSubcommandsTask6(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := zshCompletionString()
	// Top-level: zsh wraps commands as 'name:description'.
	if !strings.Contains(s, "'proxy:") {
		t.Fatalf("zsh completion top-level missing proxy entry:\n%s", s)
	}
	// Second-level: proxy_subcommands array lists all six.
	for _, want := range []string{
		"'configure:",
		"'preview:",
		"'status:",
		"'start:",
		"'stop:",
		"'serve:",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("zsh completion missing proxy subcommand entry %s:\n%s", want, s)
		}
	}
}

func TestFishCompletionIncludesProxyTopLevelAndSubcommandsTask6(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := fishCompletionString()
	// Top-level: fish uses -a 'proxy' -d '...'.
	if !strings.Contains(s, "'proxy'") {
		t.Fatalf("fish completion top-level missing proxy:\n%s", s)
	}
	// Second-level: a single complete line listing all six subcommands.
	if !strings.Contains(s, "configure start stop status preview serve") {
		t.Fatalf("fish completion missing proxy subcommand list:\n%s", s)
	}
}
