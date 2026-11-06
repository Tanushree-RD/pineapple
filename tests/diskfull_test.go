package tests

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiskFullPersistedCap verifies that --max-bytes persists into the superblock (SPEC 7.4),
// and subsequent plain commands (without --max-bytes) continue to enforce the cap.
func TestDiskFullPersistedCap(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "diskfull_cap.db")

	// Set small cap: 9000 bytes (superblocks are 8192 bytes, so ~800 bytes of frame space)
	const smallCap = 9000

	// 1. open with --max-bytes smallCap
	stdout, _, code := runKVDB(t, "open", dbPath, "--max-bytes", fmt.Sprintf("%d", smallCap))
	if code != 0 {
		t.Fatalf("open --max-bytes failed: %s", stdout)
	}
	if !strings.Contains(stdout, fmt.Sprintf("max_bytes=%d", smallCap)) {
		t.Fatalf("Expected max_bytes=%d in open output, got: %s", smallCap, stdout)
	}

	// 2. Put a couple of items within cap
	stdout, _, code = runKVDB(t, "put", dbPath, "k1", "small-val-1")
	if code != 0 || strings.TrimSpace(stdout) != "OK" {
		t.Fatalf("Initial put failed: code=%d, out=%s", code, stdout)
	}

	// 3. Put larger value that exceeds cap -> must return ERR FULL and exit code 2
	largeVal := strings.Repeat("X", 1000)
	stdout, _, code = runKVDB(t, "put", dbPath, "k-overflow", largeVal)
	if code != 2 || !strings.Contains(stdout, "ERR FULL") {
		t.Fatalf("Expected exit code 2 and ERR FULL, got code=%d, out=%s", code, stdout)
	}

	// 4. Verify prior data is intact (E3)
	stdout, _, code = runKVDB(t, "get", dbPath, "k1")
	if code != 0 || strings.TrimSpace(stdout) != "small-val-1" {
		t.Fatalf("Prior data corrupted: code=%d, out=%s", code, stdout)
	}

	// 5. Subsequent plain open (NO --max-bytes flag) must report the PERSISTED cap
	stdout, _, code = runKVDB(t, "open", dbPath)
	if code != 0 {
		t.Fatalf("plain open failed: %s", stdout)
	}
	if !strings.Contains(stdout, fmt.Sprintf("max_bytes=%d", smallCap)) {
		t.Fatalf("Persisted cap lost! Expected max_bytes=%d, got: %s", smallCap, stdout)
	}

	// 6. Plain put (NO --max-bytes flag) must STILL return ERR FULL past the cap!
	stdout, _, code = runKVDB(t, "put", dbPath, "k-overflow-2", largeVal)
	if code != 2 || !strings.Contains(stdout, "ERR FULL") {
		t.Fatalf("Persisted cap not enforced on subsequent plain put! code=%d, out=%s", code, stdout)
	}
}
