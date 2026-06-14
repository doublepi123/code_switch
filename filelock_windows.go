//go:build windows

package main

import (
	"errors"
	"os"
)

func lockFile(lockPath string) (*os.File, error) {
	return os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
}

func unlockFile(f *os.File, lockPath string) {
	_ = f.Close()
	_ = os.Remove(lockPath)
}

func isLockBusy(err error) bool {
	return errors.Is(err, os.ErrExist)
}
