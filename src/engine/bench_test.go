package engine

// F3 / SPEC 9 measurement, engine level. Runs only with KVDB_BENCH=1:
//
//	KVDB_BENCH=1 go test ./src/engine -run BenchF3 -v -timeout 30m
//
// It loads KVDB_BENCH_KEYS keys (default 1,000,000; 16-byte keys, 100-byte values, 10k-op
// batches), then times open() three ways: with the checkpoint, with a full scan, and with
// a checkpoint plus a tail. The full-scan throughput R sets CheckpointBytes = R x 0.5 s.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestBenchF3(t *testing.T) {
	if os.Getenv("KVDB_BENCH") != "1" {
		t.Skip("set KVDB_BENCH=1 to run")
	}
	keys := 1_000_000
	if s := os.Getenv("KVDB_BENCH_KEYS"); s != "" {
		keys, _ = strconv.Atoi(s)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "f3.db")
	val := make([]byte, 100)

	db, err := Open(p, Options{CheckpointBytes: 1 << 40}) // no checkpoint while loading
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < keys; i += MaxOpsPerBatch {
		n := MaxOpsPerBatch
		if keys-i < n {
			n = keys - i
		}
		ops := make([]Op, n)
		for j := range ops {
			ops[j] = Op{Kind: OpPut, Key: []byte(fmt.Sprintf("key-%012d", i+j)), Value: val}
		}
		if err := db.Batch(ops); err != nil {
			t.Fatal(err)
		}
	}
	load := time.Since(start)
	db.Close()
	size := fileSize(t, p)
	t.Logf("loaded %d keys in %v, file %.1f MiB", keys, load, float64(size)/(1<<20))

	// 1. Full scan (no checkpoint exists).
	db, err = Open(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	full := db.Stats().Recovery
	if db.Stats().Keys != keys || db.fromCkpt {
		t.Fatalf("keys=%d fromCkpt=%v", db.Stats().Keys, db.fromCkpt)
	}
	// 2. Take a checkpoint now (threshold 1 byte, one more tiny put triggers it), then a tail.
	db.Close()
	db, _ = Open(p, Options{CheckpointBytes: 1})
	db.Put([]byte("trigger"), []byte("1"))
	db.Put([]byte("sync"), []byte("1"))
	db.Close()
	db, err = Open(p, Options{CheckpointBytes: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	if !db.fromCkpt {
		t.Fatal("checkpoint not used")
	}
	ckpt := db.Stats().Recovery
	db.Close()

	R := float64(size) / (1 << 20) / full.Seconds()
	t.Logf("F3 full scan:        %v  (%.0f MiB/s replay throughput R)", full, R)
	t.Logf("F3 with checkpoint:  %v  (target <= 1.4 s)", ckpt)
	t.Logf("SPEC 9: CheckpointBytes = R x 0.5 s = %.0f MiB", R*0.5)
	if ckpt > 1400*time.Millisecond {
		t.Errorf("F3 missed: open with a checkpoint took %v", ckpt)
	}
}
