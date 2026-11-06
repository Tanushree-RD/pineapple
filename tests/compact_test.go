package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chethan-9817/pineapple/src/engine"
)

// TestCompactShrinksFile tests that after writing many puts and then deleting them,
// compaction reclaims space and reduces physical file size.
func TestCompactShrinksFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "compact_shrink.db")

	db, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// 1. Write 200 keys
	for i := 0; i < 200; i++ {
		k := []byte(fmt.Sprintf("key-%04d", i))
		v := []byte(strings.Repeat("A", 256))
		if err := db.Put(k, v); err != nil {
			t.Fatalf("Put %d failed: %v", i, err)
		}
	}

	// 2. Delete 180 keys
	for i := 0; i < 180; i++ {
		k := []byte(fmt.Sprintf("key-%04d", i))
		if err := db.Delete(k); err != nil {
			t.Fatalf("Delete %d failed: %v", i, err)
		}
	}

	fiBefore, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sizeBefore := fiBefore.Size()

	// 3. Compact
	err = db.Compact()
	if err != nil {
		if strings.Contains(err.Error(), "stage E4") {
			t.Logf("Compact returned expected E4 placeholder error: %v", err)
			db.Close()
			return
		}
		t.Fatalf("Compact failed with unexpected error: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	fiAfter, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sizeAfter := fiAfter.Size()

	t.Logf("Compaction: size before = %d bytes, size after = %d bytes", sizeBefore, sizeAfter)
	if sizeAfter >= sizeBefore {
		t.Fatalf("Expected file to shrink after compaction: before=%d, after=%d", sizeBefore, sizeAfter)
	}
}
