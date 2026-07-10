package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// cmdBackups lists or prunes the .bak-* backup files this tool writes into each
// agent's config directory before mutating settings.
func cmdBackups(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: code-switch backups list|prune [--keep N] [--days N] [--all] [--dry-run]")
	}
	switch args[0] {
	case "list":
		return listBackups(args[1:], out)
	case "prune":
		return pruneBackups(args[1:], out)
	default:
		return fmt.Errorf("unknown backups subcommand %q, use list or prune", args[0])
	}
}

type backupEntry struct {
	Path  string    `json:"path"`
	Agent string    `json:"agent"`
	Base  string    `json:"base"`
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
}

func collectBackups(claudeDir, codexDir, opencodeDir string) []backupEntry {
	dirs := []struct {
		agent string
		path  string
	}{
		{"claude", filepath.Dir(claudeSettingsPath(claudeDir))},
		{"codex", filepath.Dir(codexConfigPath(codexDir))},
		{"opencode", filepath.Dir(opencodeConfigPath(opencodeDir))},
	}
	var entries []backupEntry
	for _, d := range dirs {
		entries = append(entries, scanBackups(d.agent, d.path)...)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Mtime.After(entries[j].Mtime)
	})
	return entries
}

func scanBackups(agent, dir string) []backupEntry {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var entries []backupEntry
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !isBackupFile(f.Name()) {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		entries = append(entries, backupEntry{
			Path:  filepath.Join(dir, f.Name()),
			Agent: agent,
			Base:  backupBase(f.Name()),
			Size:  info.Size(),
			Mtime: info.ModTime(),
		})
	}
	return entries
}

func isBackupFile(name string) bool {
	const marker = ".bak-"
	i := strings.LastIndex(name, marker)
	if i <= 0 {
		return false
	}
	suffix := name[i+len(marker):]
	return suffix != "" && !strings.ContainsAny(suffix, ".\\/")
}

func backupBase(name string) string {
	if i := strings.LastIndex(name, ".bak-"); i >= 0 {
		return name[:i]
	}
	return name
}

func listBackups(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backups list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
	jsonOut := fs.Bool("json", false, "output backups as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch backups list [--json]")
	}
	entries := collectBackups(*claudeDir, *codexDir, *opencodeDir)

	if *jsonOut {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		_, err = out.Write(data)
		return err
	}

	if len(entries) == 0 {
		fmt.Fprintln(out, "no backups found")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "AGENT\tBASE\tSIZE\tMODIFIED\tFILE\n")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Agent, e.Base, humanSize(e.Size), e.Mtime.Format("2006-01-02 15:04:05"), filepath.Base(e.Path))
	}
	tw.Flush()
	return nil
}

func pruneBackups(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backups prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	keep := fs.Int("keep", -1, "keep the N most recent backups per source file (use 0 to keep none)")
	days := fs.Int("days", 0, "also delete backups older than N days")
	all := fs.Bool("all", false, "delete all backups")
	dryRun := fs.Bool("dry-run", false, "show what would be deleted without removing anything")
	claudeDir := fs.String("claude-dir", "", "override Claude config dir")
	codexDir := fs.String("codex-dir", "", "override Codex config dir")
	opencodeDir := fs.String("opencode-dir", "", "override OpenCode config dir")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: code-switch backups prune [--keep N] [--days N] [--all] [--dry-run]")
	}
	if !*all && *keep < 0 && *days <= 0 {
		return fmt.Errorf("nothing to do; specify --keep N, --days N, or --all")
	}

	entries := collectBackups(*claudeDir, *codexDir, *opencodeDir)
	deletable := selectDeletable(entries, *all, *keep, *days)

	if len(deletable) == 0 {
		fmt.Fprintln(out, "no backups to prune")
		return nil
	}

	verb := "deleted"
	if *dryRun {
		verb = "would delete"
	}
	var attempted, removed int
	for _, e := range deletable {
		fmt.Fprintf(out, "[%s] %s %s (%s)\n", e.Agent, verb, filepath.Base(e.Path), humanSize(e.Size))
		if *dryRun {
			attempted++
			continue
		}
		if err := os.Remove(e.Path); err != nil {
			// A concurrent prune already removed the file is NOT an error;
			// anything else (permissions, vanished dir, etc.) is reported.
			if !os.IsNotExist(err) {
				fmt.Fprintf(out, "  warning: failed to remove %s: %v\n", e.Path, err)
				continue
			}
		}
		removed++
		attempted++
	}
	if *dryRun {
		fmt.Fprintf(out, "\n%d backup(s) %s\n", attempted, verb)
	} else {
		fmt.Fprintf(out, "\n%d backup(s) %s (failed: %d)\n", removed, verb, attempted-removed)
	}
	return nil
}

// selectDeletable returns the backup entries that should be removed given the
// pruning policy.
func selectDeletable(entries []allBackupEntry, all bool, keep, days int) []allBackupEntry {
	if all {
		return append([]allBackupEntry(nil), entries...)
	}
	cutoff := time.Time{}
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	// Group by base filename (and dir) to apply keep-N-most-recent.
	groups := map[string][]allBackupEntry{}
	for _, e := range entries {
		key := filepath.Dir(e.Path) + string(filepath.Separator) + e.Base
		groups[key] = append(groups[key], e)
	}

	var deletable []allBackupEntry
	for _, group := range groups {
		// entries arrive sorted by mtime desc globally; re-sort the group to be safe.
		sort.Slice(group, func(i, j int) bool { return group[i].Mtime.After(group[j].Mtime) })
		for i, e := range group {
			byAge := days > 0 && e.Mtime.Before(cutoff)
			byKeep := keep >= 0 && i >= keep
			if byAge || byKeep {
				deletable = append(deletable, e)
			}
		}
	}
	// Preserve a stable, readable order (newest first) in the output.
	sort.Slice(deletable, func(i, j int) bool { return deletable[i].Mtime.After(deletable[j].Mtime) })
	return deletable
}

// allBackupEntry is an alias so the pruning logic reads clearly; it mirrors backupEntry.
type allBackupEntry = backupEntry

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for s := n / unit; s >= unit; s /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
