package main

// Disk-full mode (SPEC 15.2, E1 to E5): the cap is set ONCE with `kvdb open --max-bytes`, which
// persists it in the superblock; every later plain `kvdb serve` must enforce it without being told.
// The workload fills the database to the cap and keeps it there while kills are interleaved.
//
// After every iteration:
//   - the data file is no larger than the cap,
//   - serve started without --max-bytes still reports the persisted cap,
//   - REJECTED (ERR FULL) ops never appear (the ordinary oracle rule).
// Every 5th iteration also runs a deterministic probe (fullProbe): put until ERR FULL, delete about
// 1 MiB, and a put must be ACKed again (E5: deleting at the cap reclaims space).

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

func init() { modes["disk-full"] = runDiskFull }

const (
	defaultCap     = 4194304 // 4 MiB, as in SPEC 15.2
	fullProbeEvery = 5
	probeValue     = 64 << 10
	probeMaxPuts   = 300
	probeReclaim   = 16 // keys deleted = 1 MiB
	replyTimeout   = 60 * time.Second
)

func runDiskFull(cfg *Config) int {
	if _, err := os.Stat(cfg.KVDB); err != nil {
		fmt.Fprintf(os.Stderr, "torture: kvdb binary not found at %s (run `make` first, or pass --kvdb)\n", cfg.KVDB)
		return 2
	}
	capBytes := cfg.MaxBytes
	if capBytes == 0 {
		capBytes = defaultCap
	}
	h, err := newHarness(cfg, DiskFullParams(), nil) // note: NO --max-bytes for serve: the cap is persisted
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	h.diskCap = capBytes
	passed := false
	defer func() {
		_ = h.oracle.Close()
		if passed && !cfg.Keep && cfg.WorkDir == "" {
			_ = os.RemoveAll(h.workdir)
		} else {
			fmt.Printf("workdir kept: %s\n", h.workdir)
		}
	}()
	if onTmpfs(h.workdir) {
		fmt.Printf("NOTE workdir %s is on tmpfs (RAM): use --workdir on a disk for realistic timings.\n", h.workdir)
	}

	out, errOut, code, err := runKVDB(cfg.KVDB, time.Minute, "open", h.dbPath, "--max-bytes", fmt.Sprint(capBytes))
	if err != nil || code != 0 {
		fmt.Fprintf(os.Stderr, "torture: kvdb open --max-bytes %d failed (exit %d): %v %s\n", capBytes, code, err, truncate(errOut+out, 200))
		return 2
	}
	fmt.Printf("cap set once: %s", out)

	start := time.Now()
	var fullSeen, reclaimed int
	for iter := 1; iter <= cfg.Iterations; iter++ {
		r := h.iteration(iter)
		if !r.Pass() {
			h.reportFailure(r)
			return 1
		}
		if r.Check != nil && r.Check.Rejected > 0 {
			fullSeen++
		}
		if strings.Contains(r.Note, "put-ok") {
			reclaimed++
		}
		fmt.Println(h.iterationLine(r))
	}
	passed = true
	fmt.Printf("RESULT PASS %d/%d (seed=%d, mode=disk-full, cap=%d, %d iterations saw ERR FULL, %d probes proved delete-then-put, %s)\n",
		cfg.Iterations, cfg.Iterations, cfg.Seed, capBytes, fullSeen, reclaimed, time.Since(start).Round(time.Second))
	return 0
}

// sendWait sends one request and waits for its reply.
func (h *Harness) sendWait(sess *Session, op *Op) (OpState, error) {
	if err := sess.SendNow(op); err != nil {
		return StateInFlight, err
	}
	if !waitUntil(replyTimeout, func() bool { st, _ := h.oracle.StateOf(op.ID); return st != StateInFlight }) {
		return StateInFlight, fmt.Errorf("op %d got no reply within %s", op.ID, replyTimeout)
	}
	st, _ := h.oracle.StateOf(op.ID)
	return st, nil
}

// fullProbe is the deterministic E1/E2/E5 check, run while serve is idle.
func (h *Harness) fullProbe(sess *Session, iter int, r *IterResult, problem func(string, ...any)) {
	sess.StopLanes()
	if !waitUntil(drainTimeout, func() bool { return h.oracle.Pending() == 0 }) {
		problem("%d requests never got a reply after the writers stopped", h.oracle.Pending())
		return
	}
	lane := h.cfg.Writers
	seq := uint32(1)
	mk := func(parts ...Part) *Op {
		seq++
		kind := KindPut
		if len(parts) != 1 || parts[0].Del {
			kind = KindBatch
		}
		return &Op{ID: opID(iter, lane, seq), Lane: lane, Kind: kind, Parts: parts}
	}

	var kept [][]byte
	sawFull := false
	for i := 0; i < probeMaxPuts && !sawFull; i++ {
		key := []byte(fmt.Sprintf("pf%d-%03d", iter, i))
		st, err := h.sendWait(sess, mk(Part{Key: key, Size: probeValue}))
		if err != nil {
			problem("probe: %v", err)
			return
		}
		switch st {
		case StateAcked:
			kept = append(kept, key)
		case StateRejected:
			sawFull = true
		}
	}
	if !sawFull {
		problem("probe: %d puts of %d KiB were all ACKed, but the cap is %d bytes: the cap is not enforced (E1)", probeMaxPuts, probeValue>>10, h.diskCap)
		return
	}
	// A second put must be refused too, and must change nothing (the oracle checks REJECTED never appears).
	if st, err := h.sendWait(sess, mk(Part{Key: []byte(fmt.Sprintf("pf%d-again", iter)), Size: probeValue})); err != nil || st != StateRejected {
		problem("probe: a put right after ERR FULL was %v (%v), expected it to be refused as well", st, err)
		return
	}
	// Free about 1 MiB. If the database was already full, nothing of ours was stored, so delete
	// existing large keys that this iteration has not touched (their size in the baseline is exact).
	var victims [][]byte
	var freed int
	if len(kept) > 0 {
		n := probeReclaim
		if n > len(kept) {
			n = len(kept)
		}
		victims, freed = kept[len(kept)-n:], n*probeValue
	} else {
		touched := map[string]bool{}
		for _, op := range h.oracle.Iteration() {
			for _, p := range op.Parts {
				touched[string(p.Key)] = true
			}
		}
		names := make([]string, 0, len(h.base))
		for k := range h.base {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			st := h.base[k]
			if st.Kind != KeyValue || st.Size < 4096 || touched[k] {
				continue
			}
			victims, freed = append(victims, []byte(k)), freed+st.Size
			if freed >= 3*probeValue*probeReclaim/2 || len(victims) >= 400 {
				break
			}
		}
	}
	if len(victims) == 0 || freed < probeValue*probeReclaim/2 {
		r.Note = "probe=full,nothing-to-delete"
		return
	}
	dels := make([]Part, 0, len(victims))
	for _, k := range victims {
		dels = append(dels, Part{Del: true, Key: k})
	}
	if st, err := h.sendWait(sess, mk(dels...)); err != nil || st != StateAcked {
		problem("probe: deleting %d keys (%d KiB) at the cap was %v (%v): a delete needs no tombstone space and must be accepted (E5)", len(victims), freed>>10, st, err)
		return
	}
	if st, err := h.sendWait(sess, mk(Part{Key: []byte(fmt.Sprintf("pf%d-after", iter)), Size: probeValue})); err != nil || st != StateAcked {
		problem("probe: a put after deleting %d keys (%d KiB) was %v (%v), but it must be ACKed again (E5)", len(victims), freed>>10, st, err)
		return
	}
	r.Note = fmt.Sprintf("probe=full,freed=%dKiB,put-ok", freed>>10)
}
