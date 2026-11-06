package engine

// KEYDIR checkpoint and watermark (SPEC 9). A checkpoint is a normal frame (type KEYDIR)
// that lists every live key with the absolute offset of its value, followed by a flip of
// the INACTIVE superblock slot that records where it is and where replay resumes.
//
//	KEYDIR frame --fsync--> inactive slot (ckpt_off, ckpt_seq, replay_start) --fsync--> current
//
// A kill between the two fsyncs leaves the old slot current; recovery then scans over the
// new KEYDIR as a valid, skipped frame and builds the same index.

import (
	"io"
	"sort"
)

// DefaultCheckpointBytes is the checkpoint threshold when Options.CheckpointBytes is 0.
// SPEC 9 says (full-scan replay throughput R) x 0.5 s. Measured on the Windows dev laptop:
// R = 151 MiB/s -> 76 MiB, so 64 MiB is the same order and slightly conservative. Re-run
// KVDB_BENCH=1 go test ./src/engine -run BenchF3 -v on the demo laptop and adjust.
const DefaultCheckpointBytes = 64 << 20

func (db *DB) checkpointThreshold() int64 {
	if db.opts.CheckpointBytes > 0 {
		return db.opts.CheckpointBytes
	}
	return DefaultCheckpointBytes
}

// maybeCheckpoint runs after a transaction has been acknowledged. It checkpoints when the
// bytes appended since replay_start (or since the image start) reach the threshold.
func (db *DB) maybeCheckpoint() {
	base := int64(db.sb.ReplayStart)
	if base == 0 {
		base = DataStart
	}
	if db.ckptTried > base {
		base = db.ckptTried // a skipped attempt is not retried until another threshold of bytes
	}
	if db.logicalEnd-base < db.checkpointThreshold() {
		return
	}
	if err := db.checkpoint(); err != nil {
		db.poison(err) // any write or fsync failure poisons the handle
	}
}

// checkpoint appends a KEYDIR frame of the current index and flips the superblock.
// Only the committer calls it, so it may read the index without the lock (readers never
// write it, and the committer is the only writer).
func (db *DB) checkpoint() error {
	keys := make([]string, 0, len(db.idx.m))
	for k := range db.idx.m {
		keys = append(keys, k)
	}
	sort.Strings(keys) // string order == bytes.Compare order
	entries := make([]KeydirEntry, len(keys))
	for i, k := range keys {
		e := db.idx.m[k]
		entries[i] = KeydirEntry{Key: []byte(k), ValueOff: e.off, ValueLen: e.len}
	}
	size := KeydirPayloadSize(entries)
	F := FrameOverhead + size

	// The cap applies to checkpoints too: skip rather than overshoot (SPEC 8.3, 9).
	if limit := int64(db.sb.MaxBytes); (limit != 0 && db.logicalEnd+F > limit) || size > MaxPayload {
		db.ckptTried = db.logicalEnd
		return nil
	}

	base, seq := db.logicalEnd, db.nextSeq
	tw := &trackW{w: io.NewOffsetWriter(db.f, base)}
	if err := WriteKeydirFrame(tw, db.sb.Epoch, seq, entries); err != nil {
		return err
	}
	if err := syncFile(db.f); err != nil { // KEYDIR is durable before any slot points at it
		return err
	}
	db.mu.Lock()
	db.logicalEnd = base + F
	db.nextSeq = seq + 1
	db.mu.Unlock()

	next, err := db.sb.Next()
	if err != nil {
		return err
	}
	next.CkptOff, next.CkptSeq, next.ReplayStart = uint64(base), seq, uint64(base+F)
	if err := db.writeSuperblock(next); err != nil { // CP-S01
		return err
	}
	db.ckptCount.Add(1)
	return nil
}
