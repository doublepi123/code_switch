//go:build windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func lockFile(lockPath string) (*os.File, error) {
	// Try to create the lock file exclusively first (fast path).
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		// Write PID to enable stale lock detection.
		_, _ = fmt.Fprintf(f, "%d", os.Getpid())
		_, _ = f.Seek(0, io.SeekStart)
		return f, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	// Stale lock detection: read PID from lock file and check if process exists.
	data, readErr := os.ReadFile(lockPath)
	if readErr == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, parseErr := strconv.Atoi(pidStr); parseErr == nil {
			if !processExists(pid) {
				_ = os.Remove(lockPath)
				f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
				if err == nil {
					_, _ = fmt.Fprintf(f, "%d", os.Getpid())
					_, _ = f.Seek(0, io.SeekStart)
					return f, nil
				}
				if !errors.Is(err, os.ErrExist) {
					return nil, err
				}
			}
		}
	}
	return nil, err
}

// processExists reports whether a process with the given PID is still running.
// On Windows, this uses os.FindProcess followed by a no-op syscall to probe
// whether the process is alive.
func processExists(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// os.FindProcess always returns a process on Windows; we need to probe it.
	return p.Signal(syscall.Signal(0)) == nil
}

func unlockFile(f *os.File, lockPath string) {
	_ = f.Close()
	_ = os.Remove(lockPath)
}

func isLockBusy(err error) bool {
	return errors.Is(err, os.ErrExist)
}
