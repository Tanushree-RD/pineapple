package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open(t *testing.T, path string, opts ...Options) *DB {
	t.Helper()
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	db, err := Open(path, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func newPath(t *testing.T) string { return filepath.Join(t.TempDir(), "t.db") }

func mustGet(t *testing.T, db *DB, k string) (string, bool) {
	t.Helper()
	v, ok, err := db.Get([]byte(k))
	if err != nil {
		t.Fatalf("Get(%q): %v", k, err)
	}
	return string(v), ok
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestCreatePutGetReopen(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	if s := db.Stats(); s.Keys != 0 || s.Tail != TailComplete || s.MaxBytes != 0 {
		t.Fatalf("fresh stats %+v", s)
	}
	if fileSize(t, p) != DataStart {
		t.Fatalf("a new database is exactly 8192 bytes, got %d", fileSize(t, p))
	}
	for i := 0; i < 50; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%02d", i)), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Delete([]byte("k07")); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete([]byte("never-existed")); err != nil {
		t.Fatalf("deleting a missing key is not an error: %v", err)
	}
	if v, ok := mustGet(t, db, "k03"); !ok || v != "v3" {
		t.Fatalf("k03 = %q %v", v, ok)
	}
	if _, ok := mustGet(t, db, "k07"); ok {
		t.Fatal("deleted key present")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".create.tmp"); !os.IsNotExist(err) {
		t.Fatal("create temp must be gone after the rename")
	}

	db = open(t, p)
	defer db.Close()
	if db.Stats().Keys != 49 || db.Stats().Tail != TailComplete {
		t.Fatalf("reopen stats %+v", db.Stats())
	}
	if v, ok := mustGet(t, db, "k49"); !ok || v != "v49" {
		t.Fatalf("k49 after reopen = %q %v", v, ok)
	}
	if _, ok := mustGet(t, db, "k07"); ok {
		t.Fatal("deleted key came back")
	}
}

func TestEmptyValueIsNotADelete(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	db.Put([]byte("e"), nil)
	if v, ok := mustGet(t, db, "e"); !ok || v != "" {
		t.Fatalf("empty value: %q %v", v, ok)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if v, ok := mustGet(t, db, "e"); !ok || v != "" {
		t.Fatalf("empty value after reopen: %q %v", v, ok)
	}
}

func TestBatchLastOpWinsB5(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	k := []byte("X")
	if err := db.Batch([]Op{{Kind: OpPut, Key: k, Value: []byte("1")}, {Kind: OpDelete, Key: k}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustGet(t, db, "X"); ok {
		t.Fatal("PUT X; DEL X must leave X absent")
	}
	if err := db.Batch([]Op{{Kind: OpDelete, Key: k}, {Kind: OpPut, Key: k, Value: []byte("2")}}); err != nil {
		t.Fatal(err)
	}
	if v, ok := mustGet(t, db, "X"); !ok || v != "2" {
		t.Fatalf("DEL X; PUT X must leave X present, got %q %v", v, ok)
	}
	db.Close()
	db = open(t, p) // the same answers after recovery
	defer db.Close()
	if v, ok := mustGet(t, db, "X"); !ok || v != "2" {
		t.Fatalf("after reopen %q %v", v, ok)
	}
}

func TestInvalidInputIsRejectedAndNothingIsWritten(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	defer db.Close()
	before := fileSize(t, p)
	bad := [][]Op{
		nil,
		{{Kind: OpPut, Key: nil}},
		{{Kind: OpPut, Key: make([]byte, 1025)}},
		{{Kind: OpPut, Key: []byte("k"), Value: make([]byte, MaxValueLen+1)}},
		{{Kind: OpDelete, Key: []byte("k"), Value: []byte("v")}},
	}
	for i, ops := range bad {
		if err := db.Batch(ops); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if fileSize(t, p) != before {
		t.Fatal("rejected input changed the file")
	}
}

func TestLimitsWork(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	bigKey := bytes.Repeat([]byte{'k'}, MaxKeyLen)
	bigVal := bytes.Repeat([]byte{0xAB}, MaxValueLen)
	if err := db.Put(bigKey, bigVal); err != nil {
		t.Fatal(err)
	}
	ops := make([]Op, MaxOpsPerBatch)
	for i := range ops {
		ops[i] = Op{Kind: OpPut, Key: []byte(fmt.Sprintf("b%05d", i)), Value: []byte{byte(i)}}
	}
	if err := db.Batch(ops); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if v, ok, err := db.Get(bigKey); err != nil || !ok || !bytes.Equal(v, bigVal) {
		t.Fatalf("1 MiB value lost: %v %v", ok, err)
	}
	if db.Stats().Keys != 1+MaxOpsPerBatch {
		t.Fatalf("keys = %d", db.Stats().Keys)
	}
}

func TestSecondOpenIsLocked(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	if _, err := Open(p, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open: %v", err)
	}
	if _, err := Check(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("Check while open: %v", err)
	}
	db.Close()
	db2 := open(t, p) // lock released by Close
	db2.Close()
}

func TestStaleTempsAreDeletedNotUsed(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	db.Put([]byte("a"), []byte("1"))
	db.Close()
	os.WriteFile(p+".compact.tmp", []byte("garbage"), 0o644)
	os.WriteFile(p+".create.tmp", []byte("garbage"), 0o644)
	db = open(t, p)
	defer db.Close()
	if v, _ := mustGet(t, db, "a"); v != "1" {
		t.Fatal("old database must win over temp files")
	}
	for _, s := range []string{".compact.tmp", ".create.tmp"} {
		if _, err := os.Stat(p + s); !os.IsNotExist(err) {
			t.Fatalf("%s not deleted", s)
		}
	}
}

// ---------- recovery: tails ----------

func writeTwo(t *testing.T, p string) {
	t.Helper()
	db := open(t, p)
	db.Put([]byte("a"), []byte("one"))
	db.Put([]byte("b"), []byte("two"))
	db.Close()
}

func TestTornTailIsIgnoredAndRepairedByFirstWrite(t *testing.T) {
	p := newPath(t)
	writeTwo(t, p)
	good := fileSize(t, p)
	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte("LFR1 half a frame of garbage"))
	f.Close()

	db := open(t, p)
	if s := db.Stats(); s.Tail != TailTorn || s.Keys != 2 {
		t.Fatalf("stats %+v", s)
	}
	if fileSize(t, p) != good+int64(len("LFR1 half a frame of garbage")) {
		t.Fatal("open() must not change the file (C1)")
	}
	rep, err := Check(p + "") // db still holds the lock
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected locked, got %+v %v", rep, err)
	}
	if err := db.Put([]byte("c"), []byte("three")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if db.Stats().Tail != TailComplete || db.Stats().Keys != 3 {
		t.Fatalf("after repair %+v", db.Stats())
	}
	if v, _ := mustGet(t, db, "c"); v != "three" {
		t.Fatal("write after a torn tail lost")
	}
}

func TestRecoveryWritesNoBytes(t *testing.T) {
	p := newPath(t)
	writeTwo(t, p)
	f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte("torn"))
	f.Close()
	before, _ := os.ReadFile(p)
	for i := 0; i < 3; i++ { // repeated opens are identical (F2)
		db := open(t, p)
		db.Close()
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("open()+close() changed the data file")
	}
}

func TestF4OneByteTruncationKeepsTheAckedWrite(t *testing.T) {
	p := newPath(t)
	writeTwo(t, p)
	size := fileSize(t, p)
	if err := os.Truncate(p, size-1); err != nil { // cut the last byte of end_magic
		t.Fatal(err)
	}
	db := open(t, p)
	if s := db.Stats(); s.Tail != TailShortSentinel || s.Keys != 2 {
		t.Fatalf("stats %+v", s)
	}
	if v, ok := mustGet(t, db, "b"); !ok || v != "two" {
		t.Fatalf("ACKed last write lost: %q %v", v, ok)
	}
	// The first write restores the sentinel, then appends.
	if err := db.Put([]byte("c"), []byte("three")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if db.Stats().Tail != TailComplete || db.Stats().Keys != 3 {
		t.Fatalf("after F4 repair %+v", db.Stats())
	}
	if v, _ := mustGet(t, db, "b"); v != "two" {
		t.Fatal("frame b lost after sentinel repair")
	}
}

func TestEveryCutOfTheLastFrame(t *testing.T) {
	// Cut the file at every byte inside the last frame. The last frame must be present only
	// when just end_magic bytes are missing; every shorter cut loses exactly that frame.
	p := newPath(t)
	writeTwo(t, p)
	full, _ := os.ReadFile(p)
	// Frame b starts where frame a ends: find it by opening a copy with only frame a.
	pa := newPath(t)
	db := open(t, pa)
	db.Put([]byte("a"), []byte("one"))
	db.Close()
	aEnd := fileSize(t, pa)
	for cut := int(aEnd) + 1; cut <= len(full); cut++ {
		pc := newPath(t)
		os.WriteFile(pc, full[:cut], 0o644)
		d := open(t, pc)
		_, hasB := mustGet(t, d, "b")
		wantB := cut >= len(full)-8
		if hasB != wantB {
			t.Fatalf("cut at %d of %d: b present=%v want %v (tail %s)", cut, len(full), hasB, wantB, d.Stats().Tail)
		}
		if v, ok := mustGet(t, d, "a"); !ok || v != "one" {
			t.Fatalf("cut at %d: the older frame must survive", cut)
		}
		d.Close()
	}
}

func TestBitFlipInLastFrameDropsItAndNeverGarbage(t *testing.T) {
	p := newPath(t)
	writeTwo(t, p)
	data, _ := os.ReadFile(p)
	data[len(data)-30] ^= 0x01 // inside frame b's payload / CRC area
	os.WriteFile(p, data, 0o644)
	db := open(t, p)
	defer db.Close()
	if _, ok := mustGet(t, db, "b"); ok {
		t.Fatal("corrupt frame was applied")
	}
	if v, _ := mustGet(t, db, "a"); v != "one" {
		t.Fatal("older frame lost")
	}
}

func TestTornNewestSuperblockUsesOlderSlot(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	db.Put([]byte("a"), []byte("1"))
	db.Close()
	db = open(t, p, Options{MaxBytes: 1 << 20, SetMaxBytes: true}) // writes slot B, generation 2
	if db.Stats().MaxBytes != 1<<20 {
		t.Fatalf("cap not set: %+v", db.Stats())
	}
	db.Close()
	data, _ := os.ReadFile(p)
	data[SuperblockSlotSize+20] ^= 0xff // tear slot B (SPEC 15.8 torn-newest-sb)
	os.WriteFile(p, data, 0o644)
	db = open(t, p)
	defer db.Close()
	if db.Stats().MaxBytes != 0 {
		t.Fatalf("torn slot B must fall back to slot A (no cap), got %d", db.Stats().MaxBytes)
	}
	if v, _ := mustGet(t, db, "a"); v != "1" {
		t.Fatal("data lost")
	}
}

func TestNoValidSuperblockIsCorrupt(t *testing.T) {
	p := newPath(t)
	open(t, p).Close()
	data, _ := os.ReadFile(p)
	data[3] ^= 1
	os.WriteFile(p, data, 0o644)
	if _, err := Open(p, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v", err)
	}
	// And the file is not reinitialized.
	after, _ := os.ReadFile(p)
	if !bytes.Equal(data, after) {
		t.Fatal("a corrupt file must never be overwritten")
	}
}

// ---------- cap ----------

func TestPersistedCap(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{MaxBytes: 20000, SetMaxBytes: true})
	if db.Stats().MaxBytes != 20000 {
		t.Fatalf("%+v", db.Stats())
	}
	if err := db.Put([]byte("small"), []byte("ok")); err != nil {
		t.Fatal(err)
	}
	before := fileSize(t, p)
	if err := db.Put([]byte("big"), make([]byte, 50000)); !errors.Is(err, ErrFull) {
		t.Fatalf("over-cap put: %v", err)
	}
	if fileSize(t, p) != before {
		t.Fatal("ErrFull must be decided before any byte is written (E1)")
	}
	db.Close()
	db = open(t, p) // no flag: the persisted cap still applies
	defer db.Close()
	if db.Stats().MaxBytes != 20000 {
		t.Fatalf("cap not persisted: %+v", db.Stats())
	}
	if err := db.Put([]byte("big"), make([]byte, 50000)); !errors.Is(err, ErrFull) {
		t.Fatalf("later process must enforce the cap: %v", err)
	}
}

// ---------- ACK order, ApplyStream ----------

func TestOnCommitRunsInCommitOrder(t *testing.T) {
	p := newPath(t)
	var mu sync.Mutex
	var order []uint64
	db := open(t, p, Options{OnCommit: func(tag uint64) { mu.Lock(); order = append(order, tag); mu.Unlock() }})
	var wg sync.WaitGroup
	const n = 60
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(tag uint64) {
			defer wg.Done()
			if err := db.Apply([]Op{{Kind: OpPut, Key: []byte("hot"), Value: []byte(fmt.Sprint(tag))}}, tag); err != nil {
				t.Error(err)
			}
		}(uint64(i))
	}
	wg.Wait()
	if len(order) != n {
		t.Fatalf("%d hooks for %d txns", len(order), n)
	}
	last := order[len(order)-1]
	if v, _ := mustGet(t, db, "hot"); v != fmt.Sprint(last) {
		t.Fatalf("value %s but the last ACK was %d: ACK order is not commit order", v, last)
	}
	db.Close()
	db = open(t, p)
	defer db.Close()
	if v, _ := mustGet(t, db, "hot"); v != fmt.Sprint(last) {
		t.Fatalf("after reopen %s, last ACK %d", v, last)
	}
}

func TestOnCommitNotCalledForTagZeroOrFailures(t *testing.T) {
	var calls int
	db := open(t, newPath(t), Options{OnCommit: func(uint64) { calls++ }})
	defer db.Close()
	db.Put([]byte("a"), []byte("1"))
	db.Apply([]Op{{Kind: OpPut, Key: nil}}, 9)
	if calls != 0 {
		t.Fatalf("hook ran %d times", calls)
	}
	db.Apply([]Op{{Kind: OpPut, Key: []byte("a"), Value: []byte("2")}}, 9)
	if calls != 1 {
		t.Fatalf("hook ran %d times", calls)
	}
}

func streamOf(ops []Op) func() (Op, error) {
	i := 0
	return func() (Op, error) {
		if i >= len(ops) {
			return Op{}, errors.New("stream ended early")
		}
		op := ops[i]
		i++
		return op, nil
	}
}

func TestApplyStream(t *testing.T) {
	p := newPath(t)
	db := open(t, p)
	defer db.Close()
	ops := []Op{
		{Kind: OpPut, Key: []byte("s1"), Value: []byte("aaa")},
		{Kind: OpPut, Key: []byte("s2"), Value: bytes.Repeat([]byte{1}, 5000)},
		{Kind: OpDelete, Key: []byte("s1")},
	}
	size, _ := BatchPayloadSize(ops)
	if err := db.ApplyStream(3, size, streamOf(ops), 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustGet(t, db, "s1"); ok {
		t.Fatal("s1 deleted in the same batch")
	}
	if v, _ := mustGet(t, db, "s2"); len(v) != 5000 {
		t.Fatal("s2 wrong")
	}

	// Malformed streams: ErrInvalid, nothing published, file back to where it was.
	good := fileSize(t, p)
	mk := func(k string) []Op { return []Op{{Kind: OpPut, Key: []byte(k), Value: []byte("v")}} }
	type bad struct {
		name  string
		count int
		bytes int64
		ops   []Op
	}
	one, _ := BatchPayloadSize(mk("x1"))
	two, _ := BatchPayloadSize(append(mk("x1"), mk("x2")...))
	for _, c := range []bad{
		{"stream ends early", 2, two, mk("x1")},
		{"payload bytes too small", 1, one - 1, mk("x1")},
		{"payload bytes too big", 1, one + 1, mk("x1")},
		{"invalid op inside", 1, one, []Op{{Kind: OpPut, Key: nil}}},
	} {
		err := db.ApplyStream(c.count, c.bytes, streamOf(c.ops), 0)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", c.name, err)
		}
		if fileSize(t, p) != good {
			t.Errorf("%s: file not truncated back (%d vs %d)", c.name, fileSize(t, p), good)
		}
	}
	if _, ok := mustGet(t, db, "x1"); ok {
		t.Fatal("a malformed stream published data")
	}
	if err := db.Put([]byte("after"), []byte("ok")); err != nil {
		t.Fatalf("handle must stay usable after ErrInvalid: %v", err)
	}
}

func TestApplyStreamFullDoesNotCallNext(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{MaxBytes: 9000, SetMaxBytes: true})
	defer db.Close()
	called := false
	next := func() (Op, error) { called = true; return Op{}, errors.New("x") }
	err := db.ApplyStream(1, 5000, next, 0)
	if !errors.Is(err, ErrFull) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

// ---------- poison, close ----------

func TestPoisonAfterIOError(t *testing.T) {
	db := open(t, newPath(t))
	db.Put([]byte("a"), []byte("1"))
	db.f.Close() // simulate the disk failing under us
	if err := db.Put([]byte("b"), []byte("2")); !errors.Is(err, ErrIO) {
		t.Fatalf("first failure: %v", err)
	}
	if err := db.Put([]byte("c"), []byte("3")); !errors.Is(err, ErrIO) {
		t.Fatalf("poisoned handle must keep returning ErrIO: %v", err)
	}
	if err := db.Close(); err != nil && !strings.Contains(err.Error(), "close") {
		t.Logf("close after poison: %v", err)
	}
}

func TestUseAfterClose(t *testing.T) {
	db := open(t, newPath(t))
	db.Close()
	if err := db.Put([]byte("a"), []byte("1")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Put: %v", err)
	}
	if _, _, err := db.Get([]byte("a")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------- ForEach, MultiGet, concurrency ----------

func TestForEachSortedAndMultiGet(t *testing.T) {
	db := open(t, newPath(t))
	defer db.Close()
	for _, k := range []string{"b", "a", "\xff", "ab", "\x00"} {
		db.Put([]byte(k), []byte("v-"+k))
	}
	var got []string
	db.ForEach(func(k, v []byte) error { got = append(got, string(k)); return nil })
	want := []string{"\x00", "a", "ab", "b", "\xff"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("order %q want %q", got, want)
	}
	vals, found, err := db.MultiGet([][]byte{[]byte("a"), []byte("zzz"), []byte("b")})
	if err != nil || !found[0] || found[1] || !found[2] || string(vals[2]) != "v-b" {
		t.Fatalf("MultiGet %v %v %v", vals, found, err)
	}
}

func TestReadersNeverSeeHalfABatchD4(t *testing.T) {
	db := open(t, newPath(t))
	defer db.Close()
	keys := make([][]byte, 200)
	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("w%03d", i))
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for gen := 1; ; gen++ {
			select {
			case <-stop:
				return
			default:
			}
			ops := make([]Op, len(keys))
			for i, k := range keys {
				ops[i] = Op{Kind: OpPut, Key: k, Value: []byte(fmt.Sprint(gen))}
			}
			if err := db.Batch(ops); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for r := 0; r < 300; r++ {
		vals, found, err := db.MultiGet(keys)
		if err != nil {
			t.Fatal(err)
		}
		if !found[0] {
			continue // before the first batch
		}
		for i := range keys {
			if !found[i] || !bytes.Equal(vals[i], vals[0]) {
				close(stop)
				wg.Wait()
				t.Fatalf("saw a mixed batch: key %d = %q, key 0 = %q", i, vals[i], vals[0])
			}
		}
	}
	close(stop)
	wg.Wait()
}

// ---------- Check ----------

func TestCheck(t *testing.T) {
	p := newPath(t)
	writeTwo(t, p)
	rep, err := Check(p)
	if err != nil || !rep.OK || rep.Keys != 2 {
		t.Fatalf("healthy: %+v %v", rep, err)
	}
	data, _ := os.ReadFile(p)

	// A torn tail is INFO, not corruption.
	os.WriteFile(p, append(append([]byte(nil), data...), []byte("LFR1junk")...), 0o644)
	rep, _ = Check(p)
	if !rep.OK || !hasInfo(rep, "tail=TORN") {
		t.Fatalf("torn tail: %+v", rep)
	}
	// F4: a short sentinel is INFO too.
	os.WriteFile(p, data[:len(data)-1], 0o644)
	rep, _ = Check(p)
	if !rep.OK || !hasInfo(rep, "tail=SHORT_SENTINEL") {
		t.Fatalf("short sentinel: %+v", rep)
	}
	// A frame cut in its payload is a legal torn tail.
	os.WriteFile(p, data[:len(data)-20], 0o644)
	rep, _ = Check(p)
	if !rep.OK || !hasInfo(rep, "tail=TORN") {
		t.Fatalf("cut frame: %+v", rep)
	}
	// A completely present frame with a flipped byte can only be damage.
	bad := append([]byte(nil), data...)
	bad[len(bad)-30] ^= 1
	os.WriteFile(p, bad, 0o644)
	rep, _ = Check(p)
	if rep.OK || !strings.Contains(rep.Reason, "frame_crc") {
		t.Fatalf("flipped byte must be CORRUPT with frame_crc: %+v", rep)
	}
	// Damage in the FIRST frame is corruption even though valid frames follow.
	bad = append([]byte(nil), data...)
	bad[DataStart+45] ^= 1
	os.WriteFile(p, bad, 0o644)
	rep, _ = Check(p)
	if rep.OK || !strings.Contains(rep.Reason, "seq=1") {
		t.Fatalf("first frame damage: %+v", rep)
	}
}

func hasInfo(r Report, sub string) bool {
	for _, s := range r.Info {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
