package engine

// Compaction and the single replaceFile() (SPEC 10), shared by Compact() and, in E5, the
// disk-full REWRITE branch of admission.
//
//	build   path.compact.tmp = superblock + packed live frames + KEYDIR      CP-C01, CP-C02
//	replace fsync(tmp) -> rename -> fsync(dir) -> locked swap -> close old   CP-C03..CP-C06
//
// Persistence rules: P2 the old image is never retired before the replacement is complete
// and durable; P3 handle, index, epoch and metadata change under ONE write lock.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// liveItem is one key of the image being built. The value comes from the old file (old),
// or from memory when mem is set (the REWRITE branch uses this for the new transaction).
type liveItem struct {
	key    string
	old    entry
	mem    []byte
	hasMem bool
}

func (it liveItem) valueLen() uint32 {
	if it.hasMem {
		return uint32(len(it.mem))
	}
	return it.old.len
}

// builtImage is a complete replacement image sitting in path.compact.tmp.
type builtImage struct {
	f          *os.File
	idx        *index
	logicalEnd int64
	nextSeq    uint64
	sb         Superblock
}

// sizeUpperBound bounds the size of a compacted image (SPEC 8.2). One correction to the
// spec's formula: a frame also holds at most 10,000 ops, so the frame count is the larger
// of the byte-based and the op-based counts. That keeps the bound true for tiny records.
func sizeUpperBound(liveRec, liveKeyBytes, liveCount int64) int64 {
	const maxEntry = batchOpHeaderLen + MaxKeyLen + MaxValueLen
	perFrame := int64(MaxPackedPayload - maxEntry)
	byBytes := (liveRec + perFrame - 1) / perFrame
	byOps := (liveCount + MaxOpsPerBatch - 1) / MaxOpsPerBatch
	n := byBytes
	if byOps > n {
		n = byOps
	}
	n++
	return DataStart + n*60 + liveRec + (FrameOverhead + keydirHeaderLen + 14*liveCount + liveKeyBytes)
}

// compactTxn is Compact() running on the committer goroutine (writes are paused).
func (db *DB) compactTxn() error {
	items := make([]liveItem, 0, len(db.idx.m))
	for k, e := range db.idx.m {
		items = append(items, liveItem{key: k, old: e})
	}
	// E4 precheck: the replacement must fit the cap or nothing is touched.
	if limit := int64(db.sb.MaxBytes); limit != 0 &&
		sizeUpperBound(db.idx.liveRec, db.idx.liveKeyBytes, int64(len(db.idx.m))) > limit {
		return ErrFull
	}
	img, err := db.buildImage(items)
	if err != nil {
		return db.poison(err)
	}
	if err := db.replaceFile(img); err != nil {
		return db.poison(err)
	}
	return nil
}

// buildImage writes path.compact.tmp: a fresh superblock (new epoch, persisted cap copied),
// the live set in ascending key order as packed BATCH frames (payload <= 4 MiB, <= 10,000
// ops), and a KEYDIR frame that the superblock's checkpoint fields point at. Values are
// streamed from the old file through one reused buffer; the live set is never held in RAM.
func (db *DB) buildImage(items []liveItem) (*builtImage, error) {
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	tmpPath := db.path + ".compact.tmp"
	f, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close() // the stale temp is deleted by the next open
		}
	}()
	Hit("CP-C01") // CP-C01 (C1: temp created, empty; path untouched)

	epoch, err := NewEpoch()
	if err != nil {
		return nil, err
	}
	// Reserve the 8192-byte superblock area; slot A is filled in last, once the KEYDIR is placed.
	if _, err := f.WriteAt(make([]byte, DataStart), 0); err != nil {
		return nil, err
	}
	w := &trackW{w: io.NewOffsetWriter(f, DataStart), cp: "CP-C02"} // CP-C02 (C1: mid temp write; a kill leaves a seeded torn prefix in the temp, path untouched)
	newIdx := newIndexSize(len(items))
	off := int64(DataStart)
	seq := uint64(1)
	var vbuf []byte

	for i := 0; i < len(items); {
		// Pick the next packed frame: greedy, payload <= 4 MiB and <= 10,000 ops.
		payload := int64(4)
		j := i
		for j < len(items) && j-i < MaxOpsPerBatch {
			rec := recSize(len(items[j].key), items[j].valueLen())
			if payload+rec > MaxPackedPayload {
				break
			}
			payload += rec
			j++
		}
		if j == i { // cannot happen: one record is far below 4 MiB
			return nil, fmt.Errorf("compaction: record does not fit a packed frame")
		}
		bw, err := NewBatchWriter(w, epoch, seq, j-i, payload)
		if err != nil {
			return nil, err
		}
		pos := uint64(4)
		for _, it := range items[i:j] {
			val := it.mem
			if !it.hasMem {
				if cap(vbuf) < int(it.old.len) {
					vbuf = make([]byte, MaxValueLen)
				}
				val = vbuf[:it.old.len]
				if it.old.len > 0 {
					if _, err := db.f.ReadAt(val, int64(it.old.off)); err != nil {
						return nil, fmt.Errorf("compaction: read value at %d: %w", it.old.off, err)
					}
				}
			}
			if err := bw.Add(Op{Kind: OpPut, Key: []byte(it.key), Value: val}); err != nil {
				return nil, err
			}
			newIdx.set([]byte(it.key), entry{off: uint64(off) + FrameHeaderLen + pos + batchOpHeaderLen + uint64(len(it.key)), len: uint32(len(val))})
			pos += uint64(recSize(len(it.key), uint32(len(val))))
		}
		if err := bw.Close(); err != nil {
			return nil, err
		}
		off += FrameOverhead + payload
		seq++
		i = j
	}

	// KEYDIR of the new image (keys ascending, absolute offsets into the NEW file).
	entries := make([]KeydirEntry, len(items))
	for i, it := range items {
		e := newIdx.m[it.key]
		entries[i] = KeydirEntry{Key: []byte(it.key), ValueOff: e.off, ValueLen: e.len}
	}
	if err := WriteKeydirFrame(w, epoch, seq, entries); err != nil {
		return nil, err
	}
	end := off + FrameOverhead + KeydirPayloadSize(entries)

	sb := Superblock{
		Generation: 1, Epoch: epoch, MaxBytes: db.sb.MaxBytes,
		CkptOff: uint64(off), CkptSeq: seq, ReplayStart: uint64(end),
	}
	if _, err := f.WriteAt(sb.Marshal(), 0); err != nil { // slot A; slot B stays zero
		return nil, err
	}
	ok = true
	return &builtImage{f: f, idx: newIdx, logicalEnd: end, nextSeq: seq + 1, sb: sb}, nil
}

// replaceFile makes img the authoritative database. There is exactly one implementation.
//
//	1 fsync(tmp)          CP-C03: temp complete and durable, path still the old image
//	2                     CP-C04: just before the rename
//	3 rename(tmp, path)   CP-C05: path names the complete new image
//	4 fsync(dir)          the rename itself becomes durable, BEFORE any ACK that depends on it
//	5 swap under the write lock: file handle, index, epoch, superblock, logical_end, seq (P3)
//	                      CP-C06
//	6 close the old handle
func (db *DB) replaceFile(img *builtImage) error {
	tmpPath := db.path + ".compact.tmp"
	if err := syncFile(img.f); err != nil {
		return err
	}
	Hit("CP-C03") // CP-C03 (C2: temp complete and fsynced; path is still the OLD image)
	Hit("CP-C04") // CP-C04 (C2: just before rename; same disk state as CP-C03)

	if renameNeedsExclusive {
		db.mu.Lock() // dev-only Windows path: readers wait instead of failing
	}
	newF, oldClosed, err := renameImage(img.f, tmpPath, db.path, db.f)
	if err != nil {
		if renameNeedsExclusive {
			db.mu.Unlock()
		}
		return err
	}
	Hit("CP-C05") // CP-C05 (C3: rename returned; path names the NEW image; dir fsync and swap pending)
	if err := syncDir(filepath.Dir(db.path)); err != nil {
		if renameNeedsExclusive {
			db.mu.Unlock()
		}
		return err
	}

	if !renameNeedsExclusive {
		db.mu.Lock()
	}
	Hit("CP-C06") // CP-C06 (C4: swapping handle+index+epoch under the write lock; path is the NEW image)
	old := db.f
	db.f = newF
	db.idx = img.idx
	db.logicalEnd = img.logicalEnd
	db.nextSeq = img.nextSeq
	db.sb, db.slot = img.sb, 0
	db.tail, db.shortMissing = TailComplete, 0
	db.ckptTried = 0
	db.mu.Unlock()

	if !oldClosed {
		return old.Close()
	}
	return nil
}
