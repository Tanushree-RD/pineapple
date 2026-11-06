package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DB is an open database. All methods are safe for concurrent use.
type DB struct {
	path string
	opts Options

	lockF *os.File

	// mu guards f, idx and the published metadata. Readers hold RLock across lookup AND
	// pread; the committer takes the write lock only to publish (P3).
	mu         sync.RWMutex
	f          *os.File
	idx        *index
	logicalEnd int64
	nextSeq    uint64
	sb         Superblock
	slot       int // 0 = slot A, 1 = slot B: the slot that holds sb

	// Committer-owned: only the committer goroutine touches these after Open.
	tail         string
	shortMissing int
	ckptTried    int64 // logical_end when a checkpoint was last skipped (cap), to avoid retrying every txn
	ckptCount    atomic.Int64
	fromCkpt     bool // recovery started from a valid checkpoint

	stats OpenStats

	submitMu sync.RWMutex
	closed   bool
	reqs     chan *txn
	exited   chan struct{}
	poisoned atomic.Bool
}

// Open opens or creates the database at path, runs read-only recovery, then applies a cap
// change if opts.SetMaxBytes asks for one (SPEC 7.1, 7.4).
func Open(path string, opts Options) (db *DB, err error) {
	if opts.SetMaxBytes && opts.MaxBytes < 0 {
		return nil, fmt.Errorf("%w: negative max_bytes", ErrInvalid)
	}
	start := time.Now()

	// R0: take the process lock; ErrLocked if another process holds it.
	lockF, err := lockFile(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			unlockFile(lockF)
		}
	}()

	// Stale temp files are never authoritative: delete them before touching anything else.
	for _, t := range []string{path + ".create.tmp", path + ".compact.tmp"} {
		if e := os.Remove(t); e != nil && !os.IsNotExist(e) {
			return nil, e
		}
	}
	if _, e := os.Stat(path); os.IsNotExist(e) {
		if err = createImage(path); err != nil {
			return nil, err
		}
	} else if e != nil {
		return nil, e
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	// R1: choose the superblock. R2: load the checkpoint it names, if valid. R3: scan the
	// frames after it. An invalid checkpoint falls back to a full scan (same index, slower).
	slot, sb, err := readSuperblocks(f, fi.Size())
	if err != nil {
		return nil, err
	}
	Hit("CP-R01")
	Hit("CP-R02") // checkpoint load (or the decision to fall back to a full scan)
	Hit("CP-R03") // strict tail scan
	res, err := recoverImage(f, fi.Size(), sb)
	if err != nil {
		return nil, err
	}

	db = &DB{
		path: path, opts: opts, lockF: lockF, f: f,
		idx: res.idx, logicalEnd: res.logicalEnd, nextSeq: res.nextSeq,
		sb: sb, slot: slot, tail: res.tail, shortMissing: res.shortMissing, fromCkpt: res.fromCkpt,
		reqs: make(chan *txn), exited: make(chan struct{}),
	}
	// Only now, with recovery finished: an explicit cap change (SPEC 7.4).
	if opts.SetMaxBytes && uint64(opts.MaxBytes) != sb.MaxBytes {
		next, e := sb.Next()
		if e != nil {
			err = e
			return nil, err
		}
		next.MaxBytes = uint64(opts.MaxBytes)
		if err = db.writeSuperblock(next); err != nil {
			return nil, err
		}
	}
	db.stats = OpenStats{
		Keys: len(db.idx.m), Recovery: time.Since(start), Tail: res.tail, MaxBytes: int64(db.sb.MaxBytes),
	}
	go db.committer()
	return db, nil
}

// createImage builds a new database through a temp file, so a file at path always has a
// valid superblock (SPEC 6.1): write tmp, fsync, rename, fsync dir.
func createImage(path string) error {
	epoch, err := NewEpoch()
	if err != nil {
		return err
	}
	tmp := path + ".create.tmp"
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(InitialImage(epoch, 0)); err != nil {
		f.Close()
		return err
	}
	if err := syncFile(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// writeSuperblock writes sb into the INACTIVE slot and fsyncs (CP-S01). A torn slot fails
// sb_crc, so the old slot stays current.
func (db *DB) writeSuperblock(sb Superblock) error {
	inactive := 1 - db.slot
	b := sb.Marshal()
	w := io.NewOffsetWriter(db.f, int64(inactive)*SuperblockSlotSize)
	hitWrite("CP-S01", w, b) // CP-S01: a kill here leaves a seeded, possibly torn, slot
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("%w: write superblock: %v", ErrIO, err)
	}
	if err := syncFile(db.f); err != nil {
		return fmt.Errorf("%w: fsync superblock: %v", ErrIO, err)
	}
	db.mu.Lock()
	db.sb, db.slot = sb, inactive
	db.mu.Unlock()
	return nil
}

// Stats describes the database as it was when Open finished recovery.
func (db *DB) Stats() OpenStats { return db.stats }

// keyCount is the current number of live keys.
func (db *DB) keyCount() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.idx.m)
}

// readValue preads a value. The caller holds mu (read or write).
func (db *DB) readValue(e entry) ([]byte, error) {
	v := make([]byte, e.len)
	if e.len == 0 {
		return v, nil
	}
	if _, err := db.f.ReadAt(v, int64(e.off)); err != nil {
		return nil, fmt.Errorf("%w: read value at %d: %v", ErrIO, e.off, err)
	}
	return v, nil
}

// Get is RLock -> lookup -> pread -> RUnlock, so it sees a batch all at once (D4).
func (db *DB) Get(key []byte) ([]byte, bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.f == nil {
		return nil, false, ErrClosed
	}
	e, ok := db.idx.m[string(key)]
	if !ok {
		return nil, false, nil
	}
	v, err := db.readValue(e)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// MultiGet reads several keys under ONE read lock, so the result is a consistent snapshot
// and never shows half of a batch. (An addition to the frozen API, needed for serve MGET.)
func (db *DB) MultiGet(keys [][]byte) (vals [][]byte, found []bool, err error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.f == nil {
		return nil, nil, ErrClosed
	}
	vals = make([][]byte, len(keys))
	found = make([]bool, len(keys))
	for i, k := range keys {
		e, ok := db.idx.m[string(k)]
		if !ok {
			continue
		}
		v, err := db.readValue(e)
		if err != nil {
			return nil, nil, err
		}
		vals[i], found[i] = v, true
	}
	return vals, found, nil
}

// ForEach visits every live key in ascending byte order. It holds the read lock for the
// whole walk, so fn must not call back into the DB.
func (db *DB) ForEach(fn func(key, value []byte) error) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.f == nil {
		return ErrClosed
	}
	keys := make([]string, 0, len(db.idx.m))
	for k := range db.idx.m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare([]byte(keys[i]), []byte(keys[j])) < 0 })
	for _, k := range keys {
		v, err := db.readValue(db.idx.m[k])
		if err != nil {
			return err
		}
		if err := fn([]byte(k), v); err != nil {
			return err
		}
	}
	return nil
}

// Close stops the committer, fsyncs (unless poisoned) and releases the lock.
func (db *DB) Close() error {
	db.submitMu.Lock()
	if db.closed {
		db.submitMu.Unlock()
		return nil
	}
	db.closed = true
	close(db.reqs)
	db.submitMu.Unlock()
	<-db.exited // the committer has finished every queued transaction

	var first error
	if !db.poisoned.Load() {
		first = syncFile(db.f)
	}
	db.mu.Lock()
	if err := db.f.Close(); err != nil && first == nil {
		first = err
	}
	db.f = nil
	db.mu.Unlock()
	if err := unlockFile(db.lockF); err != nil && first == nil {
		first = err
	}
	return first
}
