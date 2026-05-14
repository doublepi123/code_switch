package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type configFile struct {
	path string
}

func newConfigFile(path string) *configFile {
	return &configFile{path: path}
}

func (cf *configFile) lockPath() string {
	return cf.path + ".lock"
}

func (cf *configFile) lock() (func(), error) {
	lockPath := cf.lockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
				f.Close()
				os.Remove(lockPath)
				return nil, fmt.Errorf("write lock file: %w", err)
			}
			f.Close()
			unlock := func() {
				os.Remove(lockPath)
			}
			return unlock, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create lock file: %w", err)
		}
		if pid := readLockPID(lockPath); pid > 0 && !processExists(pid) {
			os.Remove(lockPath)
			// Immediately retry without sleeping so another process can't
			// race between our Remove and the next OpenFile attempt.
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("config file is locked by another process (try again in a few seconds)")
}

func readLockPID(lockPath string) int {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
