package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const lockStaleAge = 30 * time.Second

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
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			unlock := func() {
				os.Remove(lockPath)
			}
			return unlock, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create lock file: %w", err)
		}
		info, statErr := os.Stat(lockPath)
		if statErr != nil {
			continue
		}
		if time.Since(info.ModTime()) > lockStaleAge {
			os.Remove(lockPath)
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("config file is locked by another process (try again in a few seconds)")
}