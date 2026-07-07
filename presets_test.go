package main

import (
	"testing"
)

func TestKimiCodingPreset(t *testing.T) {
	preset, ok := providerPresets["kimi-coding"]
	if !ok {
		t.Fatal("kimi-coding preset not found")
	}

	if preset.NoModel {
		t.Errorf("NoModel = true, want false")
	}
	if preset.Model != "kimi-k2.7-code" {
		t.Errorf("Model = %q, want kimi-k2.7-code", preset.Model)
	}
	for _, field := range []struct {
		name string
		got  string
	}{
		{"Haiku", preset.Haiku},
		{"Sonnet", preset.Sonnet},
		{"Opus", preset.Opus},
		{"Subagent", preset.Subagent},
	} {
		if field.got != "" {
			t.Errorf("%s = %q, want empty", field.name, field.got)
		}
	}

	// Thinking must be enabled so requests route to K2.7 Code instead of K2.6.
	if preset.ReasoningEffort != "xhigh" {
		t.Errorf("ReasoningEffort = %q, want %q (thinking on)", preset.ReasoningEffort, "xhigh")
	}

	if preset.BaseURL != "https://api.kimi.com/coding/" {
		t.Errorf("BaseURL = %q, want %q", preset.BaseURL, "https://api.kimi.com/coding/")
	}
	if preset.AuthEnv != "ANTHROPIC_AUTH_TOKEN" {
		t.Errorf("AuthEnv = %q, want ANTHROPIC_AUTH_TOKEN", preset.AuthEnv)
	}
	if got := preset.ExtraEnv["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; got != "262144" {
		t.Errorf("CLAUDE_CODE_AUTO_COMPACT_WINDOW = %v, want 262144", got)
	}
	if got := preset.ExtraEnv["MAX_THINKING_TOKENS"]; got != "31999" {
		t.Errorf("MAX_THINKING_TOKENS = %v, want 31999", got)
	}
}
