package engine

// SPEC 5.3 measurement for the demo laptop. Runs only with KVDB_BENCH=1:
//
//	KVDB_BENCH=1 go test ./src/engine -run 'BenchFsync|BenchF3' -v -timeout 30m
//
// It prints: 1000 x (write + File.Sync) per second, the same with plain fsync(2) on Unix,
// and whether directory Sync works. Copy the three F3 lines and these into PROGRESS.md.
// Decision rule (SPEC 5.3): keep File.Sync unless this is a Mac and it runs below about 200
// per second; then set KVDB_PLAIN_FSYNC=1 for the demo and record it as a known power-loss
// weakness.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBenchFsync(t *testing.T) {
	if os.Getenv("KVDB_BENCH") != "1" {
		t.Skip("set KVDB_BENCH=1 to run")
	}
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "fsync.dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 100)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		if _, err := f.WriteAt(buf, int64(i*100)); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatalf("File.Sync: %v", err)
		}
	}
	d := time.Since(start)
	t.Logf("FSYNC File.Sync (fsync on Linux, F_FULLFSYNC on macOS): 1000 writes in %v = %.0f per second", d, 1000/d.Seconds())
	benchPlainFsync(t, f)

	if err := syncDir(dir); err != nil {
		t.Logf("DIRSYNC syncDir(%s): FAILED: %v", dir, err)
	} else {
		t.Logf("DIRSYNC syncDir works (File.Sync on the directory, falling back to plain fsync if refused)")
	}
	dh, err := os.Open(dir)
	if err == nil {
		if e := dh.Sync(); e != nil {
			t.Logf("DIRSYNC File.Sync on a directory handle: %v (the plain-fsync fallback is needed here)", e)
		} else {
			t.Logf("DIRSYNC File.Sync on a directory handle: OK")
		}
		dh.Close()
	}
}
