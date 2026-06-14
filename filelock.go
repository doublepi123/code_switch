package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	// Total budget ~5s: 50 attempts × ~100ms, with linear backoff capped at 200ms.
	const maxAttempts = 50
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			unlock := func() {
				syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}
			return unlock, nil
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
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
	f.Close()
	return nil, fmt.Errorf("config file is locked by another process (try again in a few seconds)")
}
