package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkOrphanedTempFiles(claudeDir, codexDir, opencodeDir string) checkResult {
	dirs := []string{
		filepath.Dir(claudeSettingsPath(claudeDir)),
		filepath.Dir(codexConfigPath(codexDir)),
		filepath.Dir(opencodeConfigPath(opencodeDir)),
	}
	if appCfgPath, err := appConfigPath(); err == nil {
		if dir := filepath.Dir(appCfgPath); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	var found []string
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.Contains(e.Name(), ".tmp-") || strings.HasSuffix(e.Name(), ".tmp") {
				found = append(found, filepath.Join(d, e.Name()))
			}
		}
	}
	if len(found) == 0 {
		return okResult("orphaned temp files", "none")
	}
	return warnResult("orphaned temp files", fmt.Sprintf("%d leftover .tmp file(s) from interrupted writes (safe to remove): %s", len(found), strings.Join(found, ", ")))
}
