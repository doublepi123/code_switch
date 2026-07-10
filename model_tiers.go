package main

func resolveModelTiers(preset ProviderPreset, stored StoredProvider, flags ModelTiers) ModelTiers {
	resolve := func(flagValue, storedValue, presetValue string) string {
		if flagValue != "" {
			return flagValue
		}
		if storedValue != "" {
			return storedValue
		}
		if presetValue != "" {
			return presetValue
		}
		return preset.Model
	}

	return ModelTiers{
		Haiku:    resolve(flags.Haiku, stored.Haiku, preset.Haiku),
		Sonnet:   resolve(flags.Sonnet, stored.Sonnet, preset.Sonnet),
		Opus:     resolve(flags.Opus, stored.Opus, preset.Opus),
		Subagent: resolve(flags.Subagent, stored.Subagent, preset.Subagent),
	}
}

func tierModelMappings(tiers ModelTiers) map[string]string {
	mappings := map[string]string{}
	if tiers.Haiku != "" {
		mappings["haiku"] = tiers.Haiku
	}
	if tiers.Sonnet != "" {
		mappings["sonnet"] = tiers.Sonnet
		mappings["default"] = tiers.Sonnet
	} else {
		mappings["default"] = ""
	}
	if tiers.Opus != "" {
		mappings["opus"] = tiers.Opus
	}
	if tiers.Subagent != "" {
		mappings["subagent"] = tiers.Subagent
	}
	return mappings
}
