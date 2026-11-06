package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
)

func TestCompactShrinksToEmptyImage(t *testing.T) {
	// SPEC 10: puts + deletes + Compact leaves 8192 + 72 bytes.
	p := newPath(t)
	db := open(t, p)
	for i := 0; i < 500; i++ {
		db.Put([]byte(fmt.Sprintf("k%04d", i)), bytes.Repeat([]byte{1}, 200))
	}
	for i := 0; i < 500; i++ {
		db.Delete([]byte(fmt.Sprintf("k%04d", i)))
	}
	if fileSize(t, p) < 100000 {
		t.Fatalf("setup too small: %d", fileSize(t, p))
	}
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	if got := fileSize(t, p); got != DataStart+72 {
		t.Fatalf("compacted empty database is %d bytes, want %d", got, DataStart+72)
	}
	if db.keyCount() != 0 {
		t.Fatal("live keys after compacting an empty set")
	}
	// The handle keeps working on the NEW file.
	if err := db.Put([]byte("again"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if v, _ := mustGet(t, db, "again"); v != "1" || db.Stats().Keys != 1 {
		t.Fatalf("after reopen: %q keys=%d", v, db.Stats().Keys)
	}
}

func TestCompactKeepsExactlyTheLiveSet(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	want := map[string]string{}
	for i := 0; i < 300; i++ {
		k, v := fmt.Sprintf("key%03d", i%120), fmt.Sprintf("v%d", i)
		db.Put([]byte(k), []byte(v))
		want[k] = v
		if i%7 == 0 {
			d := fmt.Sprintf("key%03d", (i*3)%120)
			db.Delete([]byte(d))
			delete(want, d)
		}
	}
	db.Put([]byte("empty"), nil)
	want["empty"] = ""
	big := bytes.Repeat([]byte{0xEE}, MaxValueLen)
	db.Put([]byte("big"), big)
	before := fileSize(t, p)
	oldEpoch := db.sb.Epoch
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	if fileSize(t, p) >= before {
		t.Fatalf("file did not shrink: %d -> %d", before, fileSize(t, p))
	}
	if db.sb.Epoch == oldEpoch || db.sb.Generation != 1 || db.slot != 0 {
		t.Fatalf("new image must have a new epoch, generation 1, slot A: %+v", db.sb)
	}
	check := func(d *DB) {
		t.Helper()
		if d.keyCount() != len(want)+1 {
			t.Fatalf("keys %d want %d", d.keyCount(), len(want)+1)
		}
		for k, v := range want {
			if got, ok := mustGet(t, d, k); !ok || got != v {
				t.Fatalf("%q = %q %v, want %q", k, got, ok, v)
			}
		}
		if got, ok, _ := d.Get([]byte("big")); !ok || !bytes.Equal(got, big) {
			t.Fatal("1 MiB value lost by compaction")
		}
	}
	check(db)
	db.Close()
	db = open(t, p)
	defer db.Close()
	if !db.fromCkpt {
		t.Fatal("a compacted image carries a checkpoint; recovery should use it")
	}
	check(db)
	sameIndex(t, p, db)
}

func TestCompactedImageIsValidToCheckAndContinues(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	for i := 0; i < 50; i++ {
		db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v"))
	}
	db.Compact()
	db.Put([]byte("post"), []byte("1")) // frame after the KEYDIR
	db.Close()
	rep, err := Check(p)
	if err != nil || !rep.OK || !hasInfo(rep, "checkpoint ok") {
		t.Fatalf("%+v %v", rep, err)
	}
	// Compact twice in a row, and with nothing to do.
	db = open(t, p)
	defer db.Close()
	for i := 0; i < 2; i++ {
		if err := db.Compact(); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := mustGet(t, db, "post"); v != "1" || db.keyCount() != 51 {
		t.Fatal("data lost across repeated compaction")
	}
}

func TestCompactPacksFramesWithinLimits(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	for i := 0; i < 9; i++ { // 9 MiB of values: needs several packed frames of <= 4 MiB
		db.Put([]byte(fmt.Sprintf("big%d", i)), bytes.Repeat([]byte{byte(i)}, MaxValueLen))
	}
	// 25,000 tiny keys: frames are also limited to 10,000 ops.
	for i := 0; i < 25; i++ {
		ops := make([]Op, 1000)
		for j := range ops {
			ops[j] = Op{Kind: OpPut, Key: []byte(fmt.Sprintf("t%05d", i*1000+j)), Value: []byte{1}}
		}
		db.Batch(ops)
	}
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	f, _ := os.Open(p)
	defer f.Close()
	fi, _ := f.Stat()
	var sb Superblock
	hdr := make([]byte, DataStart)
	f.ReadAt(hdr, 0)
	_, sb, _ = ChooseSuperblock(hdr[:SuperblockSlotSize], hdr[SuperblockSlotSize:])
	off, seq, frames := int64(DataStart), uint64(1), 0
	for off < fi.Size() {
		info, err := ScanFrame(f, fi.Size(), off, sb.Epoch, seq)
		if err != nil {
			t.Fatalf("frame seq %d at %d: %v", seq, off, err)
		}
		if info.Header.Type == FrameTypeBatch {
			if info.Header.PayloadLen > MaxPackedPayload {
				t.Fatalf("packed frame payload %d > 4 MiB", info.Header.PayloadLen)
			}
			ents, err := ParseBatch(bytes.NewReader(mustRead(t, f, info.PayloadOff(), int64(info.Header.PayloadLen))), info.Header.PayloadLen)
			if err != nil || len(ents) > MaxOpsPerBatch {
				t.Fatalf("seq %d: %d ops, %v", seq, len(ents), err)
			}
			frames++
		}
		off, seq = info.End, seq+1
	}
	if frames < 4 {
		t.Fatalf("expected several packed frames, got %d", frames)
	}
	db = open(t, p)
	defer db.Close()
	if db.Stats().Keys != 9+25000 {
		t.Fatalf("keys %d", db.Stats().Keys)
	}
	if v, _, _ := db.Get([]byte("big8")); len(v) != MaxValueLen || v[0] != 8 {
		t.Fatal("big8 wrong")
	}
}

func mustRead(t *testing.T, f *os.File, off, n int64) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := f.ReadAt(b, off); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSizeUpperBoundHolds(t *testing.T) {
	for _, n := range []int{0, 1, 7, 100, 10001, 25000} {
		for _, vlen := range []int{0, 1, 100, 5000} {
			p := newPath(t)
			db := open(t, p)
			for i := 0; i < n; i += 1000 {
				m := 1000
				if n-i < m {
					m = n - i
				}
				ops := make([]Op, m)
				for j := range ops {
					ops[j] = Op{Kind: OpPut, Key: []byte(fmt.Sprintf("key%07d", i+j)), Value: make([]byte, vlen)}
				}
				db.Batch(ops)
			}
			ub := sizeUpperBound(db.idx.liveRec, db.idx.liveKeyBytes, int64(len(db.idx.m)))
			db.Compact()
			if got := fileSize(t, p); got > ub {
				t.Fatalf("n=%d vlen=%d: compacted size %d exceeds the bound %d", n, vlen, got, ub)
			}
			db.Close()
		}
	}
}

func TestCompactReturnsErrFullWhenTheImageCannotFit(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	for i := 0; i < 100; i++ {
		db.Put([]byte(fmt.Sprintf("k%03d", i)), make([]byte, 500))
	}
	db.Close()
	db = open(t, p, Options{MaxBytes: 20000, SetMaxBytes: true}) // live data alone is > 50 KB
	defer db.Close()
	before, _ := os.ReadFile(p)
	if err := db.Compact(); !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("a refused compaction changed the file (E4)")
	}
	if _, err := os.Stat(p + ".compact.tmp"); !os.IsNotExist(err) {
		t.Fatal("precheck must not even create the temp file")
	}
	if err := db.Put([]byte("x"), nil); errors.Is(err, ErrIO) {
		t.Fatal("ErrFull must not poison the handle")
	}
}

func TestCompactKeepsThePersistedCap(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{MaxBytes: 1 << 20, SetMaxBytes: true})
	db.Put([]byte("a"), []byte("1"))
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if db.Stats().MaxBytes != 1<<20 {
		t.Fatalf("cap lost by compaction: %d", db.Stats().MaxBytes)
	}
}

func TestCompactLeavesTheLockAlone(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	defer db.Close()
	db.Put([]byte("a"), []byte("1"))
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	// The lock is on path.lock, never on the data file, so a rename cannot release it.
	if _, err := Open(p, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock lost after compaction: %v", err)
	}
}

func TestCompactDropsATornTail(t *testing.T) {
	p := newPath(t)
	writeTwo(t, p)
	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte("torn tail bytes"))
	f.Close()
	db := open(t, p)
	defer db.Close()
	if db.Stats().Tail != TailTorn {
		t.Fatal("setup")
	}
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("c"), []byte("3")); err != nil {
		t.Fatal(err)
	}
	if v, _ := mustGet(t, db, "a"); v != "one" {
		t.Fatal("data lost")
	}
}

func TestReadersAndWritersDuringCompaction(t *testing.T) {
	// D4 / E4: a reader sees OLD file + OLD index or NEW file + NEW index, never a mix, and
	// no acknowledged write is lost across swaps.
	p := newPath(t)
	db := open(t, p)
	defer db.Close()
	const n = 300
	keys := make([][]byte, n)
	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("stable%03d", i))
		db.Put(keys[i], []byte(fmt.Sprintf("val-%d", i)))
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	errc := make(chan error, 16)
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				vals, found, err := db.MultiGet(keys)
				if err != nil {
					errc <- err
					return
				}
				for i := range keys {
					if !found[i] || string(vals[i]) != fmt.Sprintf("val-%d", i) {
						errc <- fmt.Errorf("key %d read as %q (found=%v) during compaction", i, vals[i], found[i])
						return
					}
				}
			}
		}()
	}
	acked := make(chan int, 4096)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := db.Put([]byte(fmt.Sprintf("w%05d", i)), []byte("x")); err != nil {
				errc <- err
				return
			}
			acked <- i
		}
	}()
	for c := 0; c < 12; c++ {
		if err := db.Compact(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errc:
		t.Fatal(err)
	default:
	}
	close(acked)
	count := 0
	for i := range acked {
		if _, ok := mustGet(t, db, fmt.Sprintf("w%05d", i)); !ok {
			t.Fatalf("acknowledged write %d lost across compaction", i)
		}
		count++
	}
	if count == 0 {
		t.Fatal("writer made no progress")
	}
}

func TestPoisonedHandleRefusesCompaction(t *testing.T) {
	db := open(t, newPath(t))
	defer db.Close()
	db.Put([]byte("a"), []byte("1"))
	db.f.Close()
	db.Put([]byte("b"), []byte("2")) // poisons
	if err := db.Compact(); !errors.Is(err, ErrIO) {
		t.Fatalf("Compact on a poisoned handle: %v", err)
	}
}
