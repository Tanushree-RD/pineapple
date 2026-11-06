//go:build unix

package engine

// Unix-only primitives: process lock (flock), directory fsync, and the fsync choice.

import (
	"errors"
	"os"
	"syscall"
)

// plainFsync selects plain fsync(2) instead of Go's File.Sync. On macOS File.Sync is
// fcntl(F_FULLFSYNC), which can be slow; SPEC 5.3 lets us pick plain fsync for the
// process-kill crash model. Off by default; set KVDB_PLAIN_FSYNC=1 to choose it.
var plainFsync = os.Getenv("KVDB_PLAIN_FSYNC") == "1"

// lockFile takes flock(LOCK_EX|LOCK_NB) on path (the .lock file, never the data file).
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}

func unlockFile(f *os.File) error {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}

// syncFile makes the file's data and size durable. Every data-file fsync goes through here.
func syncFile(f *os.File) error {
	if plainFsync {
		return syscall.Fsync(int(f.Fd()))
	}
	return f.Sync()
}

// syncDir makes a rename in dir durable (SPEC 5.3). If File.Sync on a directory is
// refused (seen on some macOS setups) fall back to plain fsync.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if err2 := syscall.Fsync(int(d.Fd())); err2 != nil {
			return err
		}
	}
	return nil
}

// renameImage installs tmp as path. On Unix the open handle of tmp stays valid and simply
// becomes the new image, and readers keep using the old file until the swap, so nothing
// needs closing here. exclusive=false tells the caller no lock is needed around it.
func renameImage(tmpF *os.File, tmp, path string, old *os.File) (newF *os.File, oldClosed bool, err error) {
	if err := os.Rename(tmp, path); err != nil {
		return nil, false, err
	}
	return tmpF, false, nil
}

const renameNeedsExclusive = false
