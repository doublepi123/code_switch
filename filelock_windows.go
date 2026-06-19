//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediate = 0x00000001
	lockfileExclusiveLock = 0x00000002
	errorLockViolation    = syscall.Errno(33)
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockFile(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	// Lock a single byte range on the file handle. The lock is tied to the
	// handle and auto-released by the OS on process exit or handle close,
	// mirroring flock(2) semantics on unix. A crashed or Ctrl-C'd process
	// does not leave a stuck lock: even if the .lock file lingers on disk,
	// its presence alone does not block new lockers (the gate is the
	// byte-range lock, not file existence).
	var ol syscall.Overlapped
	r1, _, e1 := syscall.Syscall6(
		procLockFileEx.Addr(),
		6,
		f.Fd(),
		lockfileFailImmediate|lockfileExclusiveLock,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		f.Close()
		if e1 != 0 {
			return nil, e1
		}
		return nil, syscall.EINVAL
	}
	return f, nil
}

func unlockFile(f *os.File, lockPath string) {
	_ = f.Close()
	_ = os.Remove(lockPath)
}

func isLockBusy(err error) bool {
	return errors.Is(err, errorLockViolation)
}
