package tests

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/chethan-9817/pineapple/src/engine"
)

// TestF4OneByteTruncationSentinel verifies SPEC 7.3:
// If the data file is truncated by exactly 1 byte, the last frame's header,
// payload, and CRC64 are intact, with 1 byte of the end_magic sentinel missing.
// Recovery accepts the frame (tail state = SHORT_SENTINEL), so an ACKed last
// write survives. A subsequent write repairs the sentinel and continues.
func TestF4OneByteTruncationSentinel(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "f4_test.db")

	// 1. Open and perform an ACKed write
	db, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	key := []byte("f4-key")
	val := []byte("f4-value-must-survive")
	if err := db.Put(key, val); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// 2. Truncate file by exactly 1 byte
	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	origSize := fi.Size()
	if origSize <= 8192+56 {
		t.Fatalf("File size too small: %d", origSize)
	}

	if err := os.Truncate(dbPath, origSize-1); err != nil {
		t.Fatalf("Truncate by 1 byte failed: %v", err)
	}

	// 3. Open again: key must be present!
	db2, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open after truncation failed: %v", err)
	}

	stats := db2.Stats()
	if stats.Tail != "SHORT_SENTINEL" {
		t.Logf("Notice: stats.Tail is %s (expected SHORT_SENTINEL)", stats.Tail)
	}

	gotVal, found, err := db2.Get(key)
	if err != nil || !found {
		t.Fatalf("F4 failed: ACKed key lost after 1-byte truncation! found=%v, err=%v", found, err)
	}
	if !bytes.Equal(gotVal, val) {
		t.Fatalf("F4 failed: value mismatch: got %q, want %q", gotVal, val)
	}

	// 4. Perform a new write: the first write repairs the sentinel (CP-W02) and appends
	key2 := []byte("f4-key-2")
	val2 := []byte("f4-value-2")
	if err := db2.Put(key2, val2); err != nil {
		t.Fatalf("Write after F4 repair failed: %v", err)
	}

	if err := db2.Close(); err != nil {
		t.Fatalf("Close after repair failed: %v", err)
	}

	// 5. Verify both keys survive cleanly
	db3, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open after repair failed: %v", err)
	}
	defer db3.Close()

	v1, f1, _ := db3.Get(key)
	v2, f2, _ := db3.Get(key2)
	if !f1 || !f2 || !bytes.Equal(v1, val) || !bytes.Equal(v2, val2) {
		t.Fatalf("State corrupted after F4 repair: k1=(%v,%s), k2=(%v,%s)", f1, v1, f2, v2)
	}
}

