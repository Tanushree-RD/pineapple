//go:build windows

package engine

// Windows is NOT a supported target (SPEC 17 item 8, no directory fsync, and replacing an
// open file by rename needs a different swap). This file exists only so the engine builds
// and its unit tests run on a Windows development machine.

import (
	"os"
	"syscall"
	"time"
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

// renameImage on Windows (dev only): a file cannot be replaced while a handle without
// FILE_SHARE_DELETE is open, so close both handles, rename, and reopen. The caller holds
// the write lock around this, so readers wait instead of failing. This breaks the
// "old image stays usable until the swap" property, which is one more reason Windows is not
// a supported target.
func renameImage(tmpF *os.File, tmp, path string, old *os.File) (newF *os.File, oldClosed bool, err error) {
	if err := tmpF.Close(); err != nil {
		return nil, false, err
	}
	if err := old.Close(); err != nil {
		return nil, false, err
	}
	// Windows can refuse a rename for a few milliseconds while an antivirus or indexer holds
	// the file; retry briefly. (Dev-only path: Unix does one os.Rename.)
	var rerr error
	for i := 0; i < 50; i++ {
		if rerr = os.Rename(tmp, path); rerr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rerr != nil {
		return nil, true, rerr
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	return f, true, err
}

const renameNeedsExclusive = true
