package engine

// The durable commit path (SPEC 3, 5.1). ONE committer goroutine runs transactions one at
// a time, in order:
//
//	W0 validate + admission (+ tail repair)   CP-W01, CP-W02
//	W1 stream the frame with pwrite           CP-W03
//	W2 whole frame written, fsync running     CP-W04
//	W3 fsync returned (DURABILITY POINT)      CP-W05
//	W4 publish to the index (write lock)      CP-W06
//	W5 OnCommit(tag): serve prints the ACK    (CP-W07 is in serve)
//
// and only then takes the next transaction. So the ACK order is the commit order.

import (
	"fmt"
	"io"
)

type txn struct {
	count        int
	payloadBytes int64
	next         func() (Op, error)
	tag          uint64
	stream       bool
	reply        chan error
}

// submit hands a transaction to the committer and waits for its outcome.
func (db *DB) submit(t *txn) error {
	t.reply = make(chan error, 1)
	db.submitMu.RLock()
	if db.closed {
		db.submitMu.RUnlock()
		return ErrClosed
	}
	db.reqs <- t
	db.submitMu.RUnlock()
	return <-t.reply
}

func (db *DB) committer() {
	defer close(db.exited)
	for t := range db.reqs {
		t.reply <- db.commit(t)
	}
}

// poison marks the handle failed: any write, fsync, truncate or rename error ends up here.
// The outcome of the failing transaction is UNKNOWN, and we never retry (SPEC 11).
func (db *DB) poison(cause error) error {
	db.poisoned.Store(true)
	return fmt.Errorf("%w: %v", ErrIO, cause)
}

// trackW remembers whether a failure came from the file (I/O) rather than from validation.
type trackW struct {
	w   io.Writer
	err error
}

func (t *trackW) Write(p []byte) (int, error) {
	Hit("CP-W03") // W1: mid-pwrite of the frame
	n, err := t.w.Write(p)
	if err != nil {
		t.err = err
	}
	return n, err
}

func (db *DB) commit(t *txn) error {
	if db.poisoned.Load() {
		return ErrIO
	}
	F := FrameOverhead + t.payloadBytes // exact frame size

	// W0: admission, decided before any byte is written (E1, E2).
	Hit("CP-W01")
	if cap := int64(db.sb.MaxBytes); cap != 0 && db.logicalEnd+F > cap {
		return ErrFull
	}
	// P4: never append behind an invalid tail. Repair on the FIRST write, never in open().
	if db.tail != TailComplete {
		if err := db.repairTail(); err != nil {
			return db.poison(err)
		}
	}

	// W1: stream the frame at logicalEnd.
	base := db.logicalEnd
	seq := db.nextSeq
	tw := &trackW{w: io.NewOffsetWriter(db.f, base)}
	bw, err := NewBatchWriter(tw, db.sb.Epoch, seq, t.count, t.payloadBytes)
	if err != nil {
		return err
	}
	ents := make([]BatchEntry, 0, t.count)
	pos := uint64(4) // payload position, after op_count
	var werr error
	for i := 0; i < t.count && werr == nil; i++ {
		op, err := t.next()
		if err != nil {
			werr = fmt.Errorf("%w: op %d: %v", ErrInvalid, i, err)
			break
		}
		if err := bw.Add(op); err != nil {
			werr = err
			break
		}
		ents = append(ents, BatchEntry{
			Kind: op.Kind, Key: append([]byte(nil), op.Key...), ValueLen: uint32(len(op.Value)),
			ValueOff: pos + batchOpHeaderLen + uint64(len(op.Key)),
		})
		pos += uint64(opSize(op))
	}
	if werr == nil {
		werr = bw.Close() // fails with ErrInvalid if the size or count announced was wrong
	}
	if werr != nil {
		if tw.err != nil { // the file failed
			return db.poison(tw.err)
		}
		// Malformed or short stream: never fsync the frame; truncate it away and fsync.
		if err := db.f.Truncate(base); err != nil {
			return db.poison(err)
		}
		if err := syncFile(db.f); err != nil {
			return db.poison(err)
		}
		return werr
	}

	// W2: the whole frame is in the file; fsync is about to run. A kill here may leave the
	// frame present although it was never acknowledged, which the invariant allows.
	Hit("CP-W04")
	if err := syncFile(db.f); err != nil {
		return db.poison(err)
	}
	// W3: fsync returned. The frame is durable.
	Hit("CP-W05")

	// W4: publish under ONE write lock (P1, P3): index entries, logical_end and seq together.
	db.mu.Lock()
	db.idx.applyBatch(base, ents)
	db.logicalEnd = base + F
	db.nextSeq = seq + 1
	db.mu.Unlock()
	Hit("CP-W06")

	// W5: the ACK hook runs here, before the next transaction starts.
	if t.tag != 0 && db.opts.OnCommit != nil {
		db.opts.OnCommit(t.tag)
	}
	return nil
}

// repairTail fixes an invalid tail before the first write after open (CP-W02, P4):
// TORN -> ftruncate(logical_end); SHORT_SENTINEL -> write the missing end_magic bytes.
// Either way fsync before anything is appended.
func (db *DB) repairTail() error {
	Hit("CP-W02")
	switch db.tail {
	case TailTorn:
		if err := db.f.Truncate(db.logicalEnd); err != nil {
			return err
		}
	case TailShortSentinel:
		present := 8 - db.shortMissing
		if _, err := db.f.WriteAt(EndMagic[present:], db.logicalEnd-int64(db.shortMissing)); err != nil {
			return err
		}
	}
	Hit("CP-W02")
	if err := syncFile(db.f); err != nil {
		return err
	}
	db.tail, db.shortMissing = TailComplete, 0
	return nil
}
