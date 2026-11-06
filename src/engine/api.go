// Package engine is LEAPFROG-R: a crash-safe single-file key-value store.
// Contracts are frozen in MASTER_PROMPT 8.1; the on-disk format is SPEC 4.
package engine

import (
	"errors"
	"fmt"
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

	// OnCommit is called BY THE COMMITTER GOROUTINE right after a transaction with a
	// non-zero tag is published (W4), and BEFORE the next transaction starts.
	// It must be fast and must never call back into the DB.
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
	Reason string   // first precise failure reason when !OK
	Info   []string // e.g. "tail=TORN bytes=123 (unacknowledged)", "max_bytes=4194304"
	Keys   int
}

var (
	ErrFull    = errors.New("full")    // definitely NOT applied
	ErrInvalid = errors.New("invalid") // bad key/value/op count, or a malformed stream; NOT applied
	ErrIO      = errors.New("io")      // handle poisoned; outcome UNKNOWN
	ErrClosed  = errors.New("closed")
	ErrLocked  = errors.New("locked")  // another process holds path.lock
	ErrCorrupt = errors.New("corrupt") // only for damage outside our crash model
)

const (
	TailComplete      = "COMPLETE"
	TailTorn          = "TORN"
	TailShortSentinel = "SHORT_SENTINEL"
)

func (db *DB) Put(key, value []byte) error {
	return db.Apply([]Op{{Kind: OpPut, Key: key, Value: value}}, 0)
}

// Delete of a missing key is not an error.
func (db *DB) Delete(key []byte) error {
	return db.Apply([]Op{{Kind: OpDelete, Key: key}}, 0)
}

// Batch is atomic: 1..10,000 ops, the last op on a key wins.
func (db *DB) Batch(ops []Op) error { return db.Apply(ops, 0) }

// Apply is Batch with a tag: after publish, the committer calls opts.OnCommit(tag).
// tag 0 = no hook. A nil error means the batch is durable (fsync returned).
func (db *DB) Apply(ops []Op, tag uint64) error {
	size, err := BatchPayloadSize(ops)
	if err != nil {
		return err
	}
	i := 0
	next := func() (Op, error) { op := ops[i]; i++; return op, nil }
	return db.submit(&txn{count: len(ops), payloadBytes: size, next: next, tag: tag})
}

// ApplyStream commits ONE atomic batch of exactly count ops whose BATCH payload is exactly
// payloadBytes long, pulling ops from next() while writing (SPEC 4.7). It can only APPEND:
// if the append does not fit it returns ErrFull WITHOUT calling next(). A next() error or
// a count/size mismatch returns ErrInvalid; the partial frame is truncated and nothing
// is published.
func (db *DB) ApplyStream(count int, payloadBytes int64, next func() (Op, error), tag uint64) error {
	if count < 1 || count > MaxOpsPerBatch {
		return fmt.Errorf("%w: %d ops, want 1..%d", ErrInvalid, count, MaxOpsPerBatch)
	}
	if payloadBytes < 4+int64(count)*(batchOpHeaderLen+1) || payloadBytes > MaxPayload {
		return fmt.Errorf("%w: payload size %d impossible for %d ops", ErrInvalid, payloadBytes, count)
	}
	return db.submit(&txn{count: count, payloadBytes: payloadBytes, next: next, tag: tag, stream: true})
}

// Compact arrives in E4.
func (db *DB) Compact() error { return errors.New("compact: not implemented yet (stage E4)") }
