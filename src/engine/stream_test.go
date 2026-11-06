package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"testing"
)

// pattern makes a 1 MiB value that names its key, so a mixed-up value is detectable.
func pattern(i int) []byte {
	v := make([]byte, MaxValueLen)
	for j := range v {
		v[j] = byte(i*31 + j)
	}
	return v
}

func TestStreamedBatchOfMaxSizeValuesNeverBuildsTheFrameInMemory(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	const n = 48 // 48 MiB of values in ONE atomic frame
	var size int64 = 4
	for i := 0; i < n; i++ {
		size += batchOpHeaderLen + 6 + MaxValueLen
	}
	i := 0
	next := func() (Op, error) {
		op := Op{Kind: OpPut, Key: []byte(fmt.Sprintf("big%03d", i)), Value: pattern(i)} // one value alive at a time
		i++
		return op, nil
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := db.ApplyStream(n, size, next, 0); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if got := int64(after.HeapInuse) - int64(before.HeapInuse); got > 24<<20 {
		t.Fatalf("heap grew by %d MiB while streaming a 48 MiB frame", got>>20)
	}
	if fileSize(t, p) != DataStart+FrameOverhead+size {
		t.Fatalf("file %d, want %d", fileSize(t, p), DataStart+FrameOverhead+size)
	}
	db.Close()

	db = open(t, p)
	defer db.Close()
	for _, k := range []int{0, 1, 17, n - 1} {
		v, ok, err := db.Get([]byte(fmt.Sprintf("big%03d", k)))
		if err != nil || !ok || !bytes.Equal(v, pattern(k)) {
			t.Fatalf("key big%03d wrong after recovery (ok=%v err=%v)", k, ok, err)
		}
	}
	db.Close() // Check needs the lock
	rep, err := Check(p)
	if err != nil || !rep.OK {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestStreamedTenThousandOps(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	ops := make([]Op, MaxOpsPerBatch)
	for i := range ops {
		ops[i] = Op{Kind: OpPut, Key: []byte(fmt.Sprintf("s%05d", i)), Value: []byte(fmt.Sprintf("val-%d", i))}
	}
	ops[9999] = Op{Kind: OpDelete, Key: []byte("s00000")} // last op on a key wins, across the stream
	size, _ := BatchPayloadSize(ops)
	if err := db.ApplyStream(len(ops), size, streamOf(ops), 0); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if db.Stats().Keys != MaxOpsPerBatch-2 { // s00000 deleted; s09999 replaced by the delete
		t.Fatalf("keys %d", db.Stats().Keys)
	}
}

func TestKilledMidBigFrameLeavesNoTrace(t *testing.T) {
	// Write a 10 MiB frame, then cut the file at assorted points inside it (a SIGKILL during
	// the pwrite loop leaves a prefix). Every cut that loses non-sentinel bytes must lose the
	// WHOLE batch: B1/B2/B6.
	p := newPath(t)
	db := open(t, p)
	db.Put([]byte("before"), []byte("1"))
	ops := make([]Op, 10)
	for i := range ops {
		ops[i] = Op{Kind: OpPut, Key: []byte(fmt.Sprintf("m%d", i)), Value: pattern(i)}
	}
	db.Batch(ops)
	db.Close()
	full, _ := os.ReadFile(p)
	for _, cut := range []int{len(full) - 9, len(full) - 20, len(full) - 1<<20, len(full) / 2, DataStart + 200, DataStart + 70} {
		pc := newPath(t)
		os.WriteFile(pc, full[:cut], 0o644)
		d := open(t, pc)
		for i := 0; i < 10; i++ {
			if _, ok := mustGet(t, d, fmt.Sprintf("m%d", i)); ok {
				t.Fatalf("cut at %d: key m%d of a torn batch is visible", cut, i)
			}
		}
		if cut > DataStart+70 && !(func() bool { _, ok := mustGet(t, d, "before"); return ok })() {
			t.Fatalf("cut at %d: the earlier frame was lost", cut)
		}
		d.Close()
	}
}

func TestErrFullIsNeverAPartialBatch(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{MaxBytes: 60000, SetMaxBytes: true})
	defer db.Close()
	before := fileSize(t, p)
	ops := make([]Op, 20)
	for i := range ops {
		ops[i] = Op{Kind: OpPut, Key: []byte(fmt.Sprintf("f%02d", i)), Value: make([]byte, 5000)}
	}
	if err := db.Batch(ops); !errors.Is(err, ErrFull) { // 100 KB batch, 60 KB cap
		t.Fatalf("err = %v", err)
	}
	if fileSize(t, p) != before || db.Stats().Keys != 0 {
		t.Fatal("a rejected batch left a trace (E2)")
	}
}
