package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chethan-9817/pineapple/src/engine"
)

// TestBenchF3Recovery runs the F3 benchmark harness.
// It is activated only when KVDB_BENCH=1 is set in the environment.
func TestBenchF3Recovery(t *testing.T) {
	if os.Getenv("KVDB_BENCH") != "1" {
		t.Skip("Skipping F3 benchmark; set KVDB_BENCH=1 to run")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bench_f3.db")

	t.Log("Starting F3 Benchmark: loading 1,000,000 keys in 10,000-op batches...")

	db, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	const totalKeys = 1000000
	const batchSize = 10000
	const keyLen = 16
	const valLen = 100

	valBytes := make([]byte, valLen)
	for i := range valBytes {
		valBytes[i] = byte(i % 250)
	}

	startLoad := time.Now()
	for b := 0; b < totalKeys/batchSize; b++ {
		ops := make([]engine.Op, batchSize)
		for i := 0; i < batchSize; i++ {
			k := fmt.Sprintf("k%015d", b*batchSize+i)
			ops[i] = engine.Op{
				Kind:  engine.OpPut,
				Key:   []byte(k[:keyLen]),
				Value: valBytes,
			}
		}
		if err := db.Batch(ops); err != nil {
			t.Fatalf("Batch %d failed: %v", b, err)
		}
	}
	loadDuration := time.Since(startLoad)
	t.Logf("Loaded %d keys in %v (%.2f ops/sec)", totalKeys, loadDuration, float64(totalKeys)/loadDuration.Seconds())

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Benchmark recovery (open)
	startOpen := time.Now()
	db2, err := engine.Open(dbPath, engine.Options{})
	if err != nil {
		t.Fatalf("Recovery Open failed: %v", err)
	}
	openDuration := time.Since(startOpen)
	defer db2.Close()

	stats := db2.Stats()
	t.Logf("F3 Recovery complete: keys=%d, open_time=%v (target <= 1.4s)", stats.Keys, openDuration)

	if openDuration > 1400*time.Millisecond {
		t.Logf("WARNING: open time %v exceeded target 1.4s", openDuration)
	}
}
