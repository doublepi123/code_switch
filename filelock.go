package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type configFile struct {
	path string
}

func newConfigFile(path string) *configFile {
	return &configFile{path: path}
}

func (cf *configFile) lock() (func(), error) {
	lockPath := cf.path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	// Total budget ~5s: 50 attempts × ~100ms, with linear backoff capped at 200ms.
	const maxAttempts = 50
	for attempt := 0; attempt < maxAttempts; attempt++ {
		f, err := lockFile(lockPath)
		if err == nil {
			unlock := func() {
				unlockFile(f, lockPath)
			}
			return unlock, nil
		}
		if !isLockBusy(err) {
			return nil, fmt.Errorf("acquire lock: %w", err)
		}
		// Linear backoff capped at 200ms so we don't burn CPU but still
		// recover promptly once the holder finishes.
		delay := time.Duration(attempt+1) * 20 * time.Millisecond
		if delay > 200*time.Millisecond {
			delay = 200 * time.Millisecond
		}
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("config file is locked by another process (try again in a few seconds)")
}
