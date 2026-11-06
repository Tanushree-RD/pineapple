package engine

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fill puts keys until at least want checkpoints have been taken, and returns how many puts
// it made. The checkpoint runs on the committer after the ACK, so we poll through Put.
func fillUntilCheckpoint(t *testing.T, db *DB, want int64) int {
	t.Helper()
	n := 0
	for db.ckptCount.Load() < want {
		if n > 5000 {
			t.Fatal("no checkpoint after 5000 puts")
		}
		if err := db.Put([]byte(fmt.Sprintf("key-%05d", n)), []byte(fmt.Sprintf("value-%d", n))); err != nil {
			t.Fatal(err)
		}
		n++
		db.Put([]byte("sync"), []byte("x")) // a round trip through the committer: the checkpoint is done
	}
	return n
}

// sameIndex asserts the index recovered through a checkpoint equals a full scan (F3 safety).
func sameIndex(t *testing.T, p string, db *DB) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	full, err := scanImage(f, fi.Size(), db.sb.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.idx.m) != len(db.idx.m) {
		t.Fatalf("checkpoint index has %d keys, full scan %d", len(db.idx.m), len(full.idx.m))
	}
	for k, e := range full.idx.m {
		if db.idx.m[k] != e {
			t.Fatalf("key %q: full scan %+v, checkpoint %+v", k, e, db.idx.m[k])
		}
	}
	if full.idx.liveRec != db.idx.liveRec || full.idx.liveKeyBytes != db.idx.liveKeyBytes {
		t.Fatalf("accounting differs: %d/%d vs %d/%d", full.idx.liveRec, full.idx.liveKeyBytes, db.idx.liveRec, db.idx.liveKeyBytes)
	}
}

func TestCheckpointIsTakenAndUsedByRecovery(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 2000})
	n := fillUntilCheckpoint(t, db, 2)
	db.Delete([]byte("key-00003"))
	db.Put([]byte("key-00004"), []byte("overwritten after the checkpoint"))
	big := bytes.Repeat([]byte{7}, MaxValueLen)
	db.Put([]byte("big"), big)
	db.Close()

	db = open(t, p, Options{CheckpointBytes: 2000})
	defer db.Close()
	if !db.fromCkpt {
		t.Fatal("recovery did not start from the checkpoint")
	}
	if sb := db.sb; sb.CkptOff == 0 || sb.CkptSeq == 0 || sb.ReplayStart <= sb.CkptOff {
		t.Fatalf("superblock does not describe a checkpoint: %+v", sb)
	}
	if _, ok := mustGet(t, db, "key-00003"); ok {
		t.Fatal("delete after the checkpoint lost")
	}
	if v, _ := mustGet(t, db, "key-00004"); v != "overwritten after the checkpoint" {
		t.Fatalf("overwrite after the checkpoint lost: %q", v)
	}
	if v, ok := mustGet(t, db, "key-00000"); !ok || v != "value-0" {
		t.Fatalf("value held in a frame BEFORE the checkpoint: %q %v", v, ok)
	}
	if v, ok, _ := db.Get([]byte("big")); !ok || !bytes.Equal(v, big) {
		t.Fatal("1 MiB value after the checkpoint lost")
	}
	if db.Stats().Keys < n-1 {
		t.Fatalf("keys %d", db.Stats().Keys)
	}
	sameIndex(t, p, db)
}

func TestCheckpointKeydirIsAValidSkippedFrameInAFullScan(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 1500})
	fillUntilCheckpoint(t, db, 1)
	db.Put([]byte("after"), []byte("1"))
	db.Close()
	rep, err := Check(p)
	if err != nil || !rep.OK {
		t.Fatalf("Check: %+v %v", rep, err)
	}
}

func TestCrashBetweenKeydirFsyncAndSlotFlip(t *testing.T) {
	// The KEYDIR is on disk but the OLD superblock is still current (kill between the two fsyncs).
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 2000})
	for i := 0; db.ckptCount.Load() == 0; i++ {
		db.Put([]byte(fmt.Sprintf("a%d", i)), []byte("v"))
		db.Put([]byte("sync"), []byte("x"))
	}
	db.Close()
	data, _ := os.ReadFile(p)
	oldSupers := make([]byte, DataStart)
	copy(oldSupers, InitialImage(db.sb.Epoch, 0)) // generation 1, no checkpoint = the old slot
	copy(data[:DataStart], oldSupers)
	os.WriteFile(p, data, 0o644)

	db2 := open(t, p, Options{CheckpointBytes: 2000})
	if db2.fromCkpt {
		t.Fatal("old superblock names no checkpoint")
	}
	if _, ok := mustGet(t, db2, "sync"); !ok {
		t.Fatal("data lost")
	}
	// The scan counted the KEYDIR frame's seq, so the next frame must continue after it.
	if err := db2.Put([]byte("next"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	db2.Close()
	db3 := open(t, p)
	defer db3.Close()
	if v, _ := mustGet(t, db3, "next"); v != "1" || db3.Stats().Tail != TailComplete {
		t.Fatalf("write after the skipped KEYDIR lost: %q tail=%s", v, db3.Stats().Tail)
	}
}

func TestBadCheckpointFallsBackToFullScan(t *testing.T) {
	setup := func() (string, int) {
		p := newPath(t)
		db := open(t, p, Options{CheckpointBytes: 2000})
		fillUntilCheckpoint(t, db, 1)
		db.Close()
		return p, int(db.sb.CkptOff) // read after Close: the committer is stopped
	}
	// 1. KEYDIR payload byte flipped (the KEYDIR is the last frame: everything before it stays).
	p, off := setup()
	data, _ := os.ReadFile(p)
	data[off+FrameHeaderLen+20] ^= 1
	os.WriteFile(p, data, 0o644)
	db := open(t, p)
	if db.fromCkpt {
		t.Fatal("a corrupt KEYDIR was trusted")
	}
	if _, ok := mustGet(t, db, "key-00000"); !ok {
		t.Fatal("data before the bad KEYDIR lost")
	}
	db.Close()

	// 2. Superblock with a valid CRC but a WRONG replay_start: the binding check rejects it.
	p, _ = setup()
	data, _ = os.ReadFile(p)
	idx, sb, _ := ChooseSuperblock(data[:SuperblockSlotSize], data[SuperblockSlotSize:DataStart])
	sb.ReplayStart++
	copy(data[idx*SuperblockSlotSize:], sb.Marshal())
	os.WriteFile(p, data, 0o644)
	db = open(t, p)
	if db.fromCkpt {
		t.Fatal("wrong replay_start was trusted")
	}
	if _, ok := mustGet(t, db, "key-00000"); !ok {
		t.Fatal("data lost on fallback")
	}
	db.Close()

	// 3. ckpt_seq not equal to the KEYDIR frame's seq.
	p, _ = setup()
	data, _ = os.ReadFile(p)
	idx, sb, _ = ChooseSuperblock(data[:SuperblockSlotSize], data[SuperblockSlotSize:DataStart])
	sb.CkptSeq++
	copy(data[idx*SuperblockSlotSize:], sb.Marshal())
	os.WriteFile(p, data, 0o644)
	db = open(t, p)
	defer db.Close()
	if db.fromCkpt {
		t.Fatal("wrong ckpt_seq was trusted")
	}
}

func TestTornCheckpointSuperblockSlotUsesOlderSlot(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 2000})
	fillUntilCheckpoint(t, db, 1)
	db.Close()
	slot := db.slot
	data, _ := os.ReadFile(p)
	data[slot*SuperblockSlotSize+20] ^= 0xff // tear the slot that holds the checkpoint (CP-S01)
	os.WriteFile(p, data, 0o644)
	db = open(t, p)
	defer db.Close()
	if db.fromCkpt {
		t.Fatal("torn slot used")
	}
	if _, ok := mustGet(t, db, "key-00000"); !ok {
		t.Fatal("data lost")
	}
}

func TestCheckpointPreservesPersistedCapAndEpoch(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 2000, MaxBytes: 1 << 20, SetMaxBytes: true})
	fillUntilCheckpoint(t, db, 1)
	db.Close()
	epoch := db.sb.Epoch
	db = open(t, p)
	defer db.Close()
	if db.Stats().MaxBytes != 1<<20 || db.sb.Epoch != epoch {
		t.Fatalf("cap=%d epoch %#x vs %#x", db.Stats().MaxBytes, db.sb.Epoch, epoch)
	}
}

func TestCheckpointIsSkippedWhenItWouldExceedTheCap(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 500, MaxBytes: 12000, SetMaxBytes: true})
	for i := 0; i < 20; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%02d", i)), make([]byte, 100)); err != nil {
			break // ErrFull at the cap is fine
		}
	}
	db.Close()
	if fileSize(t, p) > 12000 {
		t.Fatalf("file %d bytes exceeds the cap 12000: checkpoints must obey the cap", fileSize(t, p))
	}
	rep, _ := Check(p)
	if !rep.OK {
		t.Fatalf("%+v", rep)
	}
}

func TestEmptyDatabaseCheckpointRoundTrip(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 1})
	db.Put([]byte("a"), []byte("1"))
	db.Put([]byte("sync"), nil)
	db.Delete([]byte("a"))
	db.Delete([]byte("sync"))
	db.Put([]byte("zz"), nil) // checkpoint of a (nearly) empty live set must work
	db.Close()
	db = open(t, p, Options{CheckpointBytes: 1})
	defer db.Close()
	if db.Stats().Keys != 1 {
		t.Fatalf("keys %d", db.Stats().Keys)
	}
	sameIndex(t, p, db)
}

func TestWindowReader(t *testing.T) {
	data := make([]byte, 3*windowSize+123)
	for i := range data {
		data[i] = byte(i * 7)
	}
	w := newWindowReader(bytes.NewReader(data))
	buf := make([]byte, 5000)
	for _, off := range []int64{0, 4999, int64(windowSize) - 10, int64(windowSize) + 7, 2*int64(windowSize) + 99, int64(len(data)) - 5000} {
		n, err := w.ReadAt(buf, off)
		if err != nil || n != len(buf) || !bytes.Equal(buf, data[off:off+5000]) {
			t.Fatalf("off %d: n=%d err=%v", off, n, err)
		}
	}
	// A read that crosses EOF returns what exists plus EOF, like os.File.
	n, err := w.ReadAt(buf, int64(len(data))-100)
	if n != 100 || err == nil || !bytes.Equal(buf[:100], data[len(data)-100:]) {
		t.Fatalf("EOF read: n=%d err=%v", n, err)
	}
	if n, err := w.ReadAt(buf, int64(len(data))+10); n != 0 || err == nil {
		t.Fatalf("read past end: n=%d err=%v", n, err)
	}
	big := make([]byte, windowSize+5)
	if n, err := w.ReadAt(big, 17); err != nil || n != len(big) || !bytes.Equal(big, data[17:17+len(big)]) {
		t.Fatalf("big read: n=%d err=%v", n, err)
	}
}

func TestCheckCatchesAKeydirThatDisagreesWithItsFrames(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 2000})
	fillUntilCheckpoint(t, db, 1)
	db.Close()
	data, _ := os.ReadFile(p)
	off := int64(db.sb.CkptOff)
	info, err := ScanFrame(bytes.NewReader(data), int64(len(data)), off, db.sb.Epoch, db.sb.CkptSeq)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := ParseKeydir(bytes.NewReader(data[info.PayloadOff():]), info.Header.PayloadLen, db.sb.CkptSeq, uint64(off))
	if err != nil {
		t.Fatal(err)
	}
	ents[0].ValueLen-- // a lie that keeps every structural rule (same size, valid CRC, binding intact)
	var buf bytes.Buffer
	if err := WriteKeydirFrame(&buf, db.sb.Epoch, db.sb.CkptSeq, ents); err != nil {
		t.Fatal(err)
	}
	if int64(buf.Len()) != info.End-off {
		t.Fatalf("rewritten KEYDIR is %d bytes, was %d", buf.Len(), info.End-off)
	}
	copy(data[off:], buf.Bytes())
	os.WriteFile(p, data, 0o644)
	rep, err := Check(p)
	if err != nil || rep.OK || !strings.Contains(rep.Reason, "disagrees") {
		t.Fatalf("a lying KEYDIR must be CORRUPT: %+v %v", rep, err)
	}
}

func TestCheckReportsABrokenBindingAsInfoNotCorruption(t *testing.T) {
	p := newPath(t)
	db := open(t, p, Options{CheckpointBytes: 2000})
	fillUntilCheckpoint(t, db, 1)
	db.Close()
	data, _ := os.ReadFile(p)
	idx, sb, _ := ChooseSuperblock(data[:SuperblockSlotSize], data[SuperblockSlotSize:DataStart])
	sb.ReplayStart++ // valid CRC, wrong binding: recovery ignores it and scans
	copy(data[idx*SuperblockSlotSize:], sb.Marshal())
	os.WriteFile(p, data, 0o644)
	rep, err := Check(p)
	if err != nil || !rep.OK || !hasInfo(rep, "checkpoint at offset") {
		t.Fatalf("%+v %v", rep, err)
	}
}
