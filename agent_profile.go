package main

func getAgentProfile(cfg *AppConfig, agent AgentName) (AgentProfile, bool) {
	if cfg != nil && cfg.AgentProfiles != nil {
		if profile, ok := cfg.AgentProfiles[string(agent)]; ok {
			return profile, true
		}
	}
	profile, ok := agentProfiles[agent]
	return profile, ok
}
