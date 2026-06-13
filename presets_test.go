package main

import (
	"slices"
	"testing"
)

func TestKimiCodingDefaultModel(t *testing.T) {
	preset, ok := providerPresets["kimi-coding"]
	if !ok {
		t.Fatal("kimi-coding preset not found")
	}

	if preset.Model != "kimi-k2.7-code" {
		t.Errorf("default model = %q, want %q", preset.Model, "kimi-k2.7-code")
	}

	wantModels := []string{"kimi-k2.7-code", "kimi-for-coding"}
	for _, want := range wantModels {
		if !slices.Contains(preset.Models, want) {
			t.Errorf("models %v does not contain %q", preset.Models, want)
		}
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
		if field.got != "kimi-k2.7-code" {
			t.Errorf("%s = %q, want %q", field.name, field.got, "kimi-k2.7-code")
		}
	}
}
