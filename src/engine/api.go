// Package engine is the LEAPFROG-R storage engine.
//
// TEMPORARY STUB — replaced in E2. This is an in-memory, NON-durable
// implementation of the frozen API (MASTER_PROMPT 8.1) so the CLI and HYDRA can
// run end to end before the real engine exists.
package engine

import (
	"bytes"
	"errors"
	"sort"
	"sync"
	"time"
)

type OpKind uint8

const (
	OpPut    OpKind = 1
	OpDelete OpKind = 2
)

type Op struct {
	Kind  OpKind
	Key   []byte // 1..1024 bytes
	Value []byte // 0..1 MiB; must be nil/empty for OpDelete
}

type Options struct {
	// The size cap is PERSISTED in the superblock (SPEC 4.2, 7.4).
	// SetMaxBytes=false (the default) means "use the persisted cap".
	// SetMaxBytes=true means "set the cap to MaxBytes and persist it" (0 = no cap).
	MaxBytes    int64
	SetMaxBytes bool

	CheckpointBytes int64 // 0 = default 64 MiB until measured (SPEC 9)

	// OnCommit is called by the committer after a tagged transaction is
	// published, before the next transaction starts.
	OnCommit func(tag uint64)
}

type OpenStats struct {
	Keys     int
	Recovery time.Duration
	Tail     string // "COMPLETE" | "TORN" | "SHORT_SENTINEL"
	MaxBytes int64  // effective (persisted) cap; 0 = none
}

type Report struct {
	OK     bool
	Reason string
	Info   []string
	Keys   int
}

var (
	ErrFull    = errors.New("full")
	ErrInvalid = errors.New("invalid")
	ErrIO      = errors.New("io")
	ErrClosed  = errors.New("closed")
	ErrLocked  = errors.New("locked")
	ErrCorrupt = errors.New("corrupt")
)

const (
	maxKey   = 1024
	maxValue = 1 << 20
	maxOps   = 10000
)

type DB struct {
	mu     sync.RWMutex
	commit sync.Mutex // serializes transactions, like the real committer
	m      map[string][]byte
	opts   Options
	closed bool
}

func Open(path string, opts Options) (*DB, error) {
	return &DB{m: map[string][]byte{}, opts: opts}, nil
}

func (db *DB) Stats() OpenStats {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return OpenStats{Keys: len(db.m), Tail: "COMPLETE", MaxBytes: db.opts.MaxBytes}
}

func (db *DB) Put(key, value []byte) error {
	return db.Apply([]Op{{Kind: OpPut, Key: key, Value: value}}, 0)
}

func (db *DB) Delete(key []byte) error {
	return db.Apply([]Op{{Kind: OpDelete, Key: key}}, 0)
}

func (db *DB) Batch(ops []Op) error { return db.Apply(ops, 0) }

func validOp(op Op) bool {
	if len(op.Key) < 1 || len(op.Key) > maxKey || len(op.Value) > maxValue {
		return false
	}
	switch op.Kind {
	case OpPut:
		return true
	case OpDelete:
		return len(op.Value) == 0
	}
	return false
}

func (db *DB) Get(key []byte) ([]byte, bool, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, false, ErrClosed
	}
	v, ok := db.m[string(key)]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v...), true, nil
}

func (db *DB) Apply(ops []Op, tag uint64) error {
	if len(ops) < 1 || len(ops) > maxOps {
		return ErrInvalid
	}
	for _, op := range ops {
		if !validOp(op) {
			return ErrInvalid
		}
	}
	db.commit.Lock()
	defer db.commit.Unlock()
	db.mu.Lock()
	if db.closed {
		db.mu.Unlock()
		return ErrClosed
	}
	db.applyLocked(ops)
	db.mu.Unlock()
	if tag != 0 && db.opts.OnCommit != nil {
		db.opts.OnCommit(tag)
	}
	return nil
}

func (db *DB) applyLocked(ops []Op) {
	for _, op := range ops {
		if op.Kind == OpPut {
			db.m[string(op.Key)] = append([]byte(nil), op.Value...)
		} else {
			delete(db.m, string(op.Key))
		}
	}
}

func (db *DB) ApplyStream(count int, payloadBytes int64, next func() (Op, error), tag uint64) error {
	if count < 1 || count > maxOps {
		return ErrInvalid
	}
	ops := make([]Op, 0, count)
	var size int64 = 4
	for i := 0; i < count; i++ {
		op, err := next()
		if err != nil || !validOp(op) {
			return ErrInvalid
		}
		size += 7 + int64(len(op.Key)) + int64(len(op.Value))
		ops = append(ops, op)
	}
	if size != payloadBytes {
		return ErrInvalid
	}
	return db.Apply(ops, tag)
}

func (db *DB) Compact() error { return nil }

func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.closed = true
	return nil
}

func (db *DB) ForEach(fn func(key, value []byte) error) error {
	db.mu.RLock()
	keys := make([]string, 0, len(db.m))
	for k := range db.m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare([]byte(keys[i]), []byte(keys[j])) < 0 })
	vals := make([][]byte, len(keys))
	for i, k := range keys {
		vals[i] = db.m[k]
	}
	db.mu.RUnlock()
	for i, k := range keys {
		if err := fn([]byte(k), vals[i]); err != nil {
			return err
		}
	}
	return nil
}

func Check(path string) (Report, error) { return Report{OK: true}, nil }
