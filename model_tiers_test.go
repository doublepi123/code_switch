package main

import "testing"

func TestResolveModelTiersUsesFlagBeforeStoredBeforePresetBeforeBaseModel(t *testing.T) {
	preset := ProviderPreset{Model: "base-model", Haiku: "preset-haiku", Sonnet: "preset-sonnet", Opus: "preset-opus", Subagent: "preset-subagent"}
	stored := StoredProvider{Haiku: "stored-haiku", Sonnet: "stored-sonnet", Opus: "stored-opus", Subagent: "stored-subagent"}
	flags := ModelTiers{Haiku: "flag-haiku", Sonnet: "", Opus: "", Subagent: ""}

	got := resolveModelTiers(preset, stored, flags)

	if got.Haiku != "flag-haiku" {
		t.Fatalf("Haiku = %q, want flag-haiku", got.Haiku)
	}
	if got.Sonnet != "stored-sonnet" {
		t.Fatalf("Sonnet = %q, want stored-sonnet", got.Sonnet)
	}
	if got.Opus != "stored-opus" {
		t.Fatalf("Opus = %q, want stored-opus", got.Opus)
	}
	if got.Subagent != "stored-subagent" {
		t.Fatalf("Subagent = %q, want stored-subagent", got.Subagent)
	}
}

func TestResolveModelTiersUsesAllTiers(t *testing.T) {
	preset := ProviderPreset{Model: "base-model", Haiku: "preset-haiku", Sonnet: "preset-sonnet", Opus: "preset-opus", Subagent: "preset-subagent"}
	stored := StoredProvider{Haiku: "stored-haiku", Sonnet: "stored-sonnet", Opus: "stored-opus", Subagent: "stored-subagent"}
	flags := ModelTiers{Haiku: "flag-haiku", Sonnet: "flag-sonnet", Opus: "flag-opus", Subagent: "flag-subagent"}

	got := resolveModelTiers(preset, stored, flags)

	want := ModelTiers{Haiku: "flag-haiku", Sonnet: "flag-sonnet", Opus: "flag-opus", Subagent: "flag-subagent"}
	if got != want {
		t.Fatalf("resolveModelTiers() = %#v, want %#v", got, want)
	}
}

func TestTierModelMappingsIncludesDefaultAndSkipsEmptyValues(t *testing.T) {
	tiers := ModelTiers{Haiku: "haiku-model", Sonnet: "sonnet-model", Opus: "", Subagent: "subagent-model"}

	got := tierModelMappings(tiers)

	if got["haiku"] != "haiku-model" {
		t.Fatalf("haiku = %q, want haiku-model", got["haiku"])
	}
	if got["sonnet"] != "sonnet-model" {
		t.Fatalf("sonnet = %q, want sonnet-model", got["sonnet"])
	}
	if _, ok := got["opus"]; ok {
		t.Fatalf("opus entry should be skipped, got %#v", got["opus"])
	}
	if got["subagent"] != "subagent-model" {
		t.Fatalf("subagent = %q, want subagent-model", got["subagent"])
	}
	if got["default"] != "sonnet-model" {
		t.Fatalf("default = %q, want sonnet-model", got["default"])
	}
}

func TestBuildProxyRouteFromConfigMergesResolvedTiersWithExplicitMappings(t *testing.T) {
	cfg := &AppConfig{
		Providers: map[string]StoredProvider{
			"deepseek": {APIKey: "sk-test", Model: "deepseek-v4-pro", Sonnet: "stored-sonnet"},
		},
		Proxy: &ProxyConfig{
			Routes: map[string]ProxyRouteConfig{
				"codex": {
					Agent:         "codex",
					Provider:      "deepseek",
					ModelMappings: map[string]string{"sonnet": "explicit-sonnet", "custom": "custom-model"},
				},
			},
		},
	}

	route, err := buildProxyRouteFromConfig("codex", cfg, "local-token")
	if err != nil {
		t.Fatalf("buildProxyRouteFromConfig: %v", err)
	}
	if got := route.ModelMappings["haiku"]; got != "deepseek-v4-flash" {
		t.Fatalf("haiku = %q, want deepseek-v4-flash", got)
	}
	if got := route.ModelMappings["sonnet"]; got != "explicit-sonnet" {
		t.Fatalf("sonnet = %q, want explicit-sonnet", got)
	}
	if got := route.ModelMappings["default"]; got != "explicit-sonnet" {
		t.Fatalf("default = %q, want explicit-sonnet", got)
	}
	if got := route.ModelMappings["custom"]; got != "custom-model" {
		t.Fatalf("custom = %q, want custom-model", got)
	}
}
