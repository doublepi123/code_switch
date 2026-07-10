package main

import (
	"fmt"
	"sort"
	"strings"
)

func appendCodexMCPConfigTOML(content string, cfg *AppConfig) string {
	generated := generateCodexMCPConfig(cfg)
	raw, ok := generated["mcp_servers"]
	if !ok {
		return content
	}
	servers, ok := raw.(map[string]any)
	if !ok || len(servers) == 0 {
		return content
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(content, "\n"))
	if strings.TrimSpace(content) != "" {
		b.WriteString("\n\n")
	}
	names := sortedMapKeys(servers)
	for i, name := range names {
		if i > 0 {
			b.WriteString("\n")
		}
		entry, ok := servers[name].(map[string]any)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "[mcp_servers.%s]\n", tomlKeySegment(name))
		fmt.Fprintf(&b, "command = %s\n", tomlQuoteBasicString(stringValue(entry["command"])))
		if args, ok := entry["args"].([]string); ok && len(args) > 0 {
			fmt.Fprintf(&b, "args = %s\n", tomlStringArray(args))
		}
		if env, ok := entry["env"].(map[string]string); ok && len(env) > 0 {
			fmt.Fprintf(&b, "\n[mcp_servers.%s.env]\n", tomlKeySegment(name))
			for _, key := range sortedStringMapKeys(env) {
				fmt.Fprintf(&b, "%s = %s\n", tomlKeySegment(key), tomlQuoteBasicString(env[key]))
			}
		}
	}
	return b.String()
}

func removeCodexMCPConfigTOML(existing string, cfg *AppConfig) string {
	if cfg == nil || len(cfg.ManagedMCPNames) == 0 {
		return existing
	}
	managed := map[string]bool{}
	for _, name := range cfg.ManagedMCPNames {
		managed[name] = true
	}

	lines := strings.Split(existing, "\n")
	out := make([]string, 0, len(lines))
	skipSection := false
	inMultiline := false
	for _, line := range lines {
		enter, exit := isMultilineStringBoundary(line)
		if inMultiline {
			if exit {
				inMultiline = false
			}
			if !skipSection {
				out = append(out, line)
			}
			continue
		}
		if enter {
			inMultiline = true
			if !skipSection {
				out = append(out, line)
			}
			continue
		}
		if section, ok := tomlSectionName(line); ok {
			skipSection = isManagedMCPSection(section, managed)
			if skipSection {
				continue
			}
		}
		if skipSection {
			continue
		}
		out = append(out, line)
	}
	result := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if strings.TrimSpace(result) == "" {
		return ""
	}
	return result + "\n"
}

func isManagedMCPSection(section string, managed map[string]bool) bool {
	for name := range managed {
		for _, root := range []string{"mcp_servers", "mcpServers"} {
			prefix := root + "." + tomlKeySegment(name)
			if section == prefix || strings.HasPrefix(section, prefix+".") {
				return true
			}
		}
	}
	return false
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func tomlStringArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, tomlQuoteBasicString(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func tomlKeySegment(value string) string {
	if isBareTOMLKey(value) {
		return value
	}
	return tomlQuoteBasicString(value)
}

func isBareTOMLKey(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
