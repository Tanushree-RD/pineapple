//go:build windows

package engine

// Windows is NOT a supported target (SPEC 17 item 8, no directory fsync, and replacing an
// open file by rename needs a different swap). This file exists only so the engine builds
// and its unit tests run on a Windows development machine.

import (
	"os"
	"syscall"
	"unsafe"
)

var procLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	const exclusiveFailImmediately = 0x2 | 0x1 // LOCKFILE_EXCLUSIVE_LOCK | LOCKFILE_FAIL_IMMEDIATELY
	var ol syscall.Overlapped
	r, _, errno := procLockFileEx.Call(f.Fd(), exclusiveFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		f.Close()
		if errno == syscall.Errno(33) { // ERROR_LOCK_VIOLATION
			return nil, ErrLocked
		}
		return nil, errno
	}
	return f, nil
}

func unlockFile(f *os.File) error { return f.Close() } // closing releases the lock

func syncFile(f *os.File) error { return f.Sync() }

func syncDir(dir string) error { return nil }
