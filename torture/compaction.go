package main

// Compaction mode (SPEC 15.2): the default workload plus ONE COMPACT at a seeded moment, then a kill.
// Every 10th iteration is different: let the workload run, drain it, then let a compaction finish
// and assert the file physically shrank (C3 to C6).
//
// COMPACT has no effect on any key, so the oracle's rules are unchanged: whatever the kill does to
// the compaction, the database must still hold exactly the ACKed state. The compaction's crash
// states (CP-C01..C06) are swept deterministically in sweep mode; here the kill is a random timer.

import (
	"fmt"
	"os"
	"time"
)

const (
	shrinkEvery    = 10
	drainTimeout   = 30 * time.Second
	compactTimeout = 60 * time.Second
)

// runUntilKill runs the iteration's workload until the kill instant. Plain modes just sleep.
func (h *Harness) runUntilKill(sess *Session, iter int, is uint64, r *IterResult, problem func(string, ...any)) {
	kill := time.Duration(r.KillMS) * time.Millisecond
	switch {
	case h.compaction && iter%shrinkEvery == 0:
		time.Sleep(kill)
		h.shrinkPhase(sess, iter, r, problem)
	case h.compaction:
		at := time.Duration(splitmix64(is^0xc0ffee) % uint64(r.KillMS+1))
		op := h.compactOp(iter)
		timer := time.AfterFunc(at*time.Millisecond, func() { _ = sess.SendNow(op) })
		time.Sleep(kill)
		timer.Stop()
		switch st, _ := h.oracle.StateOf(op.ID); st {
		case StateAcked:
			r.Note = "compact=ACKED"
		default:
			r.Note = "compact=killed-or-unsent"
		}
	case h.diskCap > 0 && iter%fullProbeEvery == 0:
		time.Sleep(kill)
		h.fullProbe(sess, iter, r, problem)
	default:
		time.Sleep(kill)
	}
}

func (h *Harness) compactOp(iter int) *Op {
	return &Op{ID: opID(iter, h.cfg.Writers, 1), Lane: h.cfg.Writers, Kind: KindCompact}
}

// shrinkPhase stops the writers, lets everything in flight finish, runs one COMPACT to completion
// and asserts the file got smaller. The caller then kills serve as usual.
func (h *Harness) shrinkPhase(sess *Session, iter int, r *IterResult, problem func(string, ...any)) {
	sess.StopLanes()
	if !waitUntil(drainTimeout, func() bool { return h.oracle.Pending() == 0 }) {
		problem("%d requests never got a reply after the writers stopped", h.oracle.Pending())
		return
	}
	before, err := fileSize(h.dbPath)
	if err != nil {
		problem("cannot stat database before compaction: %v", err)
		return
	}
	op := h.compactOp(iter)
	if err := sess.SendNow(op); err != nil {
		problem("cannot send COMPACT: %v", err)
		return
	}
	if !waitUntil(compactTimeout, func() bool { st, _ := h.oracle.StateOf(op.ID); return st != StateInFlight }) {
		problem("COMPACT was not answered within %s", compactTimeout)
		return
	}
	if st, _ := h.oracle.StateOf(op.ID); st != StateAcked {
		problem("COMPACT was not ACKed (state %s)", st)
		return
	}
	after, err := fileSize(h.dbPath)
	if err != nil {
		problem("cannot stat database after compaction: %v", err)
		return
	}
	r.Note = fmt.Sprintf("shrink=%d->%d", before, after)
	// Nothing to reclaim in a file that is already close to its minimum is not a failure.
	if before > firstFrameOff+64<<10 && after >= before {
		problem("COMPACT finished but the file did not shrink: %d -> %d bytes (C3 to C6)", before, after)
	}
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}
