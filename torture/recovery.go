package main

// Recovery-crash mode (SPEC 15.2, C1, C2, F2, F3): kill the database while it is RECOVERING.
//
// Setup builds a database of 200,000 keys (--big: 1,000,000) with 10,000-op batches and verifies it
// through the oracle. Then every iteration SIGKILLs `kvdb open` at a seeded fraction of its clean
// duration, 1 to 3 times, and finally runs a full open + check + dump:
//   - nothing was written (the file's CRC64 and size never change: recovery writes no bytes),
//   - the state still equals the baseline exactly,
//   - the final open takes less than 2 s.

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"
)

func init() { modes["recovery-crash"] = runRecoveryCrash }

const (
	recoveryKeys    = 200000
	recoveryKeysBig = 1000000
	recoveryBatch   = 10000
	finalOpenBudget = 2000 // ms, SPEC 15.2
)

func runRecoveryCrash(cfg *Config) int {
	if _, err := os.Stat(cfg.KVDB); err != nil {
		fmt.Fprintf(os.Stderr, "torture: kvdb binary not found at %s (run `make` first, or pass --kvdb)\n", cfg.KVDB)
		return 2
	}
	h, err := newHarness(cfg, DefaultParams(), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	passed := false
	defer func() {
		_ = h.oracle.Close()
		if passed && !cfg.Keep && cfg.WorkDir == "" {
			_ = os.RemoveAll(h.workdir)
		} else {
			fmt.Printf("workdir kept: %s\n", h.workdir)
		}
	}()
	if cfg.Verbose && onTmpfs(h.workdir) {
		fmt.Printf("NOTE workdir %s is on tmpfs (RAM): use --workdir on a disk for realistic timings.\n", h.workdir)
	}

	start := time.Now()
	keys := recoveryKeys
	if cfg.Big {
		keys = recoveryKeysBig
	}
	fmt.Printf("setup: building %d keys with %d-op batches ...\n", keys, recoveryBatch)
	if r := h.setupBigDB(keys); !r.Pass() {
		h.reportFailure(r)
		return 1
	}
	clean := h.cleanOpenTime(3)
	fmt.Printf("setup done in %s: %d keys, clean `kvdb open` takes %s\n", time.Since(start).Round(time.Millisecond), len(h.base), clean.Round(time.Millisecond))

	var landed, total int
	for iter := 1; iter <= cfg.Iterations; iter++ {
		r, kills := h.recoveryIteration(iter, clean)
		for _, k := range kills {
			total++
			if k.landed {
				landed++
			}
		}
		if !r.Pass() {
			h.reportFailure(r)
			return 1
		}
		fmt.Println(h.recoveryLine(r, kills))
	}
	passed = true
	fmt.Printf("RESULT PASS %d/%d (seed=%d, mode=recovery-crash, %d keys, %d of %d open kills landed mid-recovery, %s)\n",
		cfg.Iterations, cfg.Iterations, cfg.Seed, len(h.base), landed, total, time.Since(start).Round(time.Second))
	return 0
}

// setupBigDB writes n keys in 10,000-op batches through serve, waits for every ACK, then kills serve
// (everything is durable by then) and verifies the result like any other iteration.
func (h *Harness) setupBigDB(n int) *IterResult {
	r := &IterResult{Iter: 0}
	problem := func(format string, args ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, args...)) }
	h.oracle.BeginIteration(0, iterSeed(h.cfg.Seed, 0))
	sess, err := StartSession(h.cfg.KVDB, h.dbPath, h.oracle, 1)
	if err != nil {
		problem("%v", err)
		return r
	}
	if _, err := sess.WaitReady(); err != nil {
		sess.Close()
		problem("setup: serve did not start: %v", err)
		return r
	}
	rg := newRNG(iterSeed(h.cfg.Seed, 0))
	var seq uint32
	for sent := 0; sent < n; sent += recoveryBatch {
		cnt := recoveryBatch
		if n-sent < cnt {
			cnt = n - sent
		}
		seq++
		op := &Op{ID: opID(0, 0, seq), Lane: 0, Kind: KindBatch}
		for i := 0; i < cnt; i++ {
			op.Parts = append(op.Parts, Part{Key: []byte(fmt.Sprintf("rk%07d", sent+i)), Size: rg.between(valueHeaderLen, 64)})
		}
		if err := sess.SendNow(op); err != nil {
			problem("setup: cannot send batch %d: %v", seq, err)
			sess.Close()
			return r
		}
		// Keep at most 4 big batches in flight.
		if !waitUntil(10*time.Minute, func() bool { return h.oracle.Pending() <= 4 }) {
			problem("setup: serve stopped answering")
			sess.Close()
			return r
		}
	}
	if !waitUntil(10*time.Minute, func() bool { return h.oracle.Pending() == 0 }) {
		problem("setup: %d batches never got a reply", h.oracle.Pending())
		sess.Close()
		return r
	}
	exit := sess.KillAndDrain()
	if exit.EarlyExit {
		problem("setup: serve exited by itself (%s)", exit)
	}
	for _, a := range sess.Anomalies() {
		problem("setup: serve protocol anomaly: %s", a)
	}
	r.Ops = h.oracle.Iteration()
	h.verifyAfterKill(r, problem)
	if r.Pass() && len(h.base) != n {
		problem("setup: expected %d keys, baseline holds %d", n, len(h.base))
	}
	return r
}

// cleanOpenTime is the median wall time of an uninterrupted `kvdb open`.
func (h *Harness) cleanOpenTime(n int) time.Duration {
	ds := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		t0 := time.Now()
		_, _, _, _ = runKVDB(h.cfg.KVDB, 5*time.Minute, "open", h.dbPath)
		ds = append(ds, time.Since(t0))
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

type openKill struct {
	after  time.Duration
	landed bool // true if the SIGKILL arrived before open finished
}

func (h *Harness) recoveryIteration(iter int, clean time.Duration) (*IterResult, []openKill) {
	began := time.Now()
	r := &IterResult{Iter: iter}
	defer func() { r.Elapsed = time.Since(began) }()
	problem := func(format string, args ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, args...)) }

	is := iterSeed(h.cfg.Seed, iter)
	h.oracle.BeginIteration(iter, is)
	rg := newRNG(is)
	n := 1 + rg.intn(3)

	pre, preSize, derr := fileDigest(h.dbPath)
	if derr != nil {
		problem("cannot read the database: %v", derr)
		return r, nil
	}
	var kills []openKill
	for i := 0; i < n; i++ {
		frac := 0.05 + 0.9*float64(rg.intn(1000))/1000.0 // 5% .. 95% of the clean duration
		k := openKill{after: time.Duration(float64(clean) * frac)}
		cmd := exec.Command(h.cfg.KVDB, "open", h.dbPath)
		if err := cmd.Start(); err != nil {
			problem("cannot start kvdb open: %v", err)
			return r, kills
		}
		timer := time.AfterFunc(k.after, func() { _ = cmd.Process.Kill() })
		_ = cmd.Wait()
		timer.Stop()
		k.landed = cmd.ProcessState != nil && !cmd.ProcessState.Success()
		kills = append(kills, k)
	}
	r.KillMS = int(kills[len(kills)-1].after / time.Millisecond)

	// The file must be byte-identical: recovery writes nothing, however it dies (C1, C2, F2).
	if post, postSize, err := fileDigest(h.dbPath); err != nil || post != pre || postSize != preSize {
		problem("killing kvdb open changed the database file (size %d -> %d, err %v): recovery must write no bytes", preSize, postSize, err)
	}

	dump := h.observe(r, problem)
	if post, postSize, err := fileDigest(h.dbPath); err == nil && (post != pre || postSize != preSize) {
		problem("a full open/check/dump after the kills changed the database file (size %d -> %d)", preSize, postSize)
	}
	if dump == nil {
		return r, kills
	}
	r.Keys = len(dump)
	if r.OpenMS >= finalOpenBudget {
		problem("final open took %d ms, the budget is %d ms (F3)", r.OpenMS, finalOpenBudget)
	}
	// No operation ran, so the state must equal the baseline exactly.
	r.Check = CheckIteration(h.base, nil, dump, h.oracle.StateOf)
	return r, kills
}

func (h *Harness) recoveryLine(r *IterResult, kills []openKill) string {
	s := ""
	for i, k := range kills {
		if i > 0 {
			s += ","
		}
		mark := "done"
		if k.landed {
			mark = "KILLED"
		}
		s += fmt.Sprintf("%dms:%s", k.after/time.Millisecond, mark)
	}
	return fmt.Sprintf("iter %d/%d seed=%d mode=recovery-crash opens=[%s] keys=%d open_ms=%d check=OK PASS",
		r.Iter, h.cfg.Iterations, h.cfg.Seed, s, r.Keys, r.OpenMS)
}
