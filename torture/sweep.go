package main

// Sweep mode (SPEC 15.2, 15.6, 12): deterministic crash points. For each crash point of SPEC 12 and
// hit counts n = 1..N, the database is started with KVDB_CRASH=<cp>:<n>; it kills itself at exactly
// that line (at the write points after writing a seeded random PREFIX of the bytes, so the file
// really holds a torn frame or slot). Then the file is opened and judged by the oracle, plus
// per-crash-point expectations taken from the SPEC 12 table.
//
// This is a separate mode on purpose: the default mode stays a pure random timer and never sets
// these variables (a torture tool that only passes because of hooks counts as rigged).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func init() { modes["sweep"] = runSweep }

const (
	warmupIters = 2
	capChange   = int64(1) << 34
	capSearch   = 600 // see capCase: how many seeds to try while looking for a torn slot
)

type sweepResult struct {
	Scenario string `json:"scenario"`
	CP       string `json:"cp"`
	N        int    `json:"n"`
	Reached  bool   `json:"reached"`
	Outcome  string `json:"outcome"`
	FP       string `json:"fingerprint,omitempty"`
}

type sweep struct {
	cfg     *Config
	h       *Harness // the shared database: write, repair, recover, compact and cap scenarios
	caseNo  int
	capN    int
	pre     imgSnap // on-disk state before the current case
	salt    uint64  // mixed into the crash seed so that retries of one case differ
	results []sweepResult
	failed  bool
}

func runSweep(cfg *Config) int {
	if _, err := os.Stat(cfg.KVDB); err != nil {
		fmt.Fprintf(os.Stderr, "torture: kvdb binary not found at %s (run `make` first, or pass --kvdb)\n", cfg.KVDB)
		return 2
	}
	h, err := newHarness(cfg, DefaultParams(), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	s := &sweep{cfg: cfg, h: h}
	passed := false
	defer func() {
		_ = h.oracle.Close()
		if passed && !cfg.Keep && cfg.WorkDir == "" {
			_ = os.RemoveAll(h.workdir)
		} else {
			fmt.Printf("workdir kept: %s\n", h.workdir)
		}
	}()
	start := time.Now()

	// Give the shared database some content and garbage first.
	for i := 1; i <= warmupIters; i++ {
		if r := h.iteration(i); !r.Pass() {
			h.reportFailure(r)
			return 1
		}
	}
	fmt.Printf("warm-up: %d keys in the shared database\n", len(h.base))

	want := func(cp string) bool { return cfg.CP == "" || cfg.CP == cp }
	for _, cp := range []string{"CP-W01", "CP-W02", "CP-W03", "CP-W04", "CP-W05", "CP-W06", "CP-W07",
		"CP-R01", "CP-R02", "CP-R03", "CP-C01", "CP-C02", "CP-C03", "CP-C04", "CP-C05", "CP-C06", "CP-S01"} {
		if !want(cp) {
			continue
		}
		if !s.runCP(cp) {
			return 1
		}
	}
	s.writeCoverage()
	passed = true
	reached := 0
	for _, r := range s.results {
		if r.Reached {
			reached++
		}
	}
	s.printFingerprints()
	fmt.Printf("RESULT PASS %d cases, %d of them killed exactly at their crash point (seed=%d, mode=sweep, %s)\n",
		len(s.results), reached, cfg.Seed, time.Since(start).Round(time.Second))
	return 0
}

func (s *sweep) runCP(cp string) bool {
	switch cp {
	case "CP-W01", "CP-W03", "CP-W04", "CP-W05", "CP-W06", "CP-W07":
		return s.upTo(cp, func(n int) (bool, bool) { return s.writeCase(cp, n) })
	case "CP-W02":
		for _, kind := range []string{"torn", "short"} {
			kind := kind
			if !s.upTo(cp, func(n int) (bool, bool) { return s.repairCase(kind, n) }) {
				return false
			}
		}
		return true
	case "CP-R01", "CP-R02", "CP-R03":
		_, ok := s.recoverCase(cp)
		return ok
	case "CP-C01", "CP-C02", "CP-C03", "CP-C04", "CP-C05", "CP-C06":
		maxN := 1
		if cp == "CP-C02" {
			maxN = 3
		}
		for n := 1; n <= maxN; n++ {
			reached, ok := s.compactCase(cp, n)
			if !ok {
				return false
			}
			if !reached {
				break
			}
		}
		if _, ok := s.rewriteCase(cp); !ok {
			return false
		}
		return true
	case "CP-S01":
		// First an ordinary hit, then a search for a seed whose torn prefix really tears the slot.
		if _, ok := s.capCase(false); !ok {
			return false
		}
		if _, ok := s.capCase(true); !ok {
			return false
		}
		_, ok := s.checkpointCase()
		return ok
	}
	return true
}

// upTo runs fn for n = 1..Hits and stops early when a hit count is never reached.
func (s *sweep) upTo(cp string, fn func(n int) (reached, ok bool)) bool {
	for n := 1; n <= s.cfg.Hits; n++ {
		reached, ok := fn(n)
		if !ok {
			return false
		}
		if !reached {
			break
		}
	}
	return true
}

func (s *sweep) env(cp string, n int) []string {
	return []string{fmt.Sprintf("KVDB_CRASH=%s:%d", cp, n),
		fmt.Sprintf("KVDB_CRASH_SEED=%d", splitmix64(s.cfg.Seed+uint64(s.caseNo)+s.salt*1000003)>>1)}
}

// begin starts a new case on harness h.
func (s *sweep) begin(h *Harness) (*IterResult, func(string, ...any), int) {
	s.caseNo++
	iter := 1000 + s.caseNo
	r := &IterResult{Iter: iter}
	problem := func(format string, args ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, args...)) }
	h.oracle.BeginIteration(iter, iterSeed(s.cfg.Seed, iter))
	s.pre = snapImage(h.dbPath)
	return r, problem, iter
}

// imgSnap is what a state fingerprint is built from (SPEC 15.10).
type imgSnap struct {
	ok                  bool
	frames, keys        int
	tail                string
	ckpt                bool
	maxBytes            uint64
	logicalEnd, fileLen int64
}

func snapImage(path string) imgSnap {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return imgSnap{ok: true, tail: "COMPLETE"} // no file yet is the empty database
	}
	im, data, err := readImage(path)
	if err != nil {
		return imgSnap{}
	}
	return imgSnap{ok: true, frames: len(im.Frames), keys: len(im.State), tail: im.Tail, ckpt: im.Super.CkptOff != 0,
		maxBytes: im.Super.MaxBytes, logicalEnd: im.LogicalEnd, fileLen: int64(len(data))}
}

// fingerprint describes the crash image RELATIVE to the state before the case, so that injections at
// different crash points that leave equivalent states land in one group.
func fingerprint(pre, post imgSnap) string {
	if !pre.ok || !post.ok {
		return "unreadable"
	}
	extra := post.fileLen - post.logicalEnd
	if extra < 0 {
		extra = 0
	}
	capState := "cap=same"
	if post.maxBytes != pre.maxBytes {
		capState = "cap=changed"
	}
	return fmt.Sprintf("frames%+d keys%+d tail=%s torn_bytes=%v ckpt=%v %s", post.frames-pre.frames, post.keys-pre.keys, post.tail, extra > 0, post.ckpt, capState)
}

// finish judges a case and prints its line. It returns false (and reports) on failure.
func (s *sweep) finish(h *Harness, r *IterResult, scenario, cp string, n int, reached bool, outcome string) bool {
	fp := ""
	if reached {
		fp = fingerprint(s.pre, snapImage(h.dbPath))
	}
	s.results = append(s.results, sweepResult{Scenario: scenario, CP: cp, N: n, Reached: reached, Outcome: outcome, FP: fp})
	if !r.Pass() {
		h.reproHint = fmt.Sprintf("%s --mode sweep --seed %d --cp %s --hits %d", os.Args[0], s.cfg.Seed, cp, s.cfg.Hits)
		fmt.Printf("sweep %s:%d [%s] FAIL\n", cp, n, scenario)
		h.reportFailure(r)
		s.failed = true
		return false
	}
	tag := "reached"
	if !reached {
		tag = "not reached"
	}
	fmt.Printf("sweep %-7s n=%d %-10s %-11s %s  check=OK PASS\n", cp, n, "["+scenario+"]", tag, outcome)
	return true
}

func isSelfKill(e ExitInfo) bool {
	return e.EarlyExit && e.Signaled && e.Signal == syscall.SIGKILL
}

// sweepOp is the sequential workload: unique keys only, so the presence of the in-flight
// transaction is visible. Every 4th op is a batch with a witness key.
func sweepOp(iter, k int) *Op {
	id := opID(iter, 0, uint32(k))
	if k%4 == 0 {
		op := &Op{ID: id, Lane: 0, Kind: KindBatch}
		for i := 0; i < 3; i++ {
			op.Parts = append(op.Parts, Part{Key: []byte(fmt.Sprintf("sw%d-%d-%d", iter, k, i)), Size: 40 + 7*i})
		}
		w := []byte(fmt.Sprintf("sww%d-%d", iter, k))
		op.Witness = w
		op.Parts = append(op.Parts, Part{Key: w, Size: valueShortMin})
		return op
	}
	return &Op{ID: id, Lane: 0, Kind: KindPut, Parts: []Part{{Key: []byte(fmt.Sprintf("sw%d-%d", iter, k)), Size: 50 + k}}}
}

// driveOps sends ops one at a time (pipeline depth 1) until serve dies or maxOps were answered.
func (s *sweep) driveOps(h *Harness, sess *Session, mk func(k int) *Op, maxOps int) {
	for k := 1; k <= maxOps; k++ {
		op := mk(k)
		if err := sess.SendNow(op); err != nil {
			return
		}
		if !waitUntil(replyTimeout, func() bool {
			st, _ := h.oracle.StateOf(op.ID)
			return st != StateInFlight || sess.Dead()
		}) {
			return
		}
		if sess.Dead() {
			return
		}
	}
}

// inflightOp is the op that was sent but never answered (at most one at pipeline depth 1).
func inflightOp(ops []*Op) *Op {
	for _, op := range ops {
		if op.State == StateInFlight && op.Kind != KindCompact {
			return op
		}
	}
	return nil
}

func surprise(r *IterResult) *Op {
	if r.Check == nil {
		return nil
	}
	return r.Check.Surprise
}

// ---------------------------------------------------------------------------------------------
// Scenario: write path (W01, W03 to W07).

func (s *sweep) writeCase(cp string, n int) (reached, ok bool) {
	return s.serveWriteCase("write", cp, n, "")
}

// serveWriteCase runs serve with the crash variable and a sequential put workload.
func (s *sweep) serveWriteCase(scenario, cp string, n int, note string) (reached, ok bool) {
	h := s.h
	r, problem, iter := s.begin(h)
	sess, err := StartSessionEnv(h.cfg.KVDB, h.dbPath, h.oracle, 1, s.env(cp, n))
	if err != nil {
		problem("%v", err)
		return false, s.finish(h, r, scenario, cp, n, false, "")
	}
	if _, err := sess.WaitReady(); err != nil {
		sess.Close()
		problem("serve did not start: %v", err)
		return false, s.finish(h, r, scenario, cp, n, false, "")
	}
	s.driveOps(h, sess, func(k int) *Op { return sweepOp(iter, k) }, n+3)
	exit := sess.KillAndDrain()
	reached = isSelfKill(exit)
	r.Ops = h.oracle.Iteration()
	if !reached && n == 1 {
		problem("%s was never hit: the scenario cannot reach it (a coverage gap)", cp)
	}
	for _, a := range sess.Anomalies() {
		problem("serve protocol anomaly: %s", a)
	}
	inf := inflightOp(r.Ops) // before verification: verifying retires unobserved ops to DEAD
	h.verifyAfterKill(r, problem)

	sup := surprise(r)
	outcome := "no transaction in flight"
	if inf != nil {
		outcome = fmt.Sprintf("op %d absent", inf.ID)
		if sup != nil && sup.ID == inf.ID {
			outcome = fmt.Sprintf("op %d PRESENT", inf.ID)
		}
	}
	if reached && r.Check != nil {
		switch cp {
		case "CP-W01", "CP-W02":
			if sup != nil {
				problem("%s is before any byte is written, but the unACKed transaction %d is present", cp, sup.ID)
			}
		case "CP-W05", "CP-W06":
			if inf == nil || sup == nil || sup.ID != inf.ID {
				problem("%s is after fsync returned (W3/W4): the unACKed transaction must be present, found %s", cp, outcome)
			}
		case "CP-W07":
			// The n-th ACK was flushed, then the process died. The driver may already have sent the
			// next op into the dying process, so look at the ACK count, not at what is left unanswered.
			if r.Check.Acked != n {
				problem("CP-W07:%d is after the %d-th ACK was flushed, but HYDRA read %d ACKs", n, n, r.Check.Acked)
			}
		}
	}
	outcome += " tail=" + r.Tail
	if note != "" {
		outcome += " " + note
	}
	return reached, s.finish(h, r, scenario, cp, n, reached, outcome)
}

// ---------------------------------------------------------------------------------------------
// Scenario: tail repair (W02). The tail is damaged by hand between runs, so the first write after
// open has to repair it.

func (s *sweep) repairCase(kind string, n int) (reached, ok bool) {
	h := s.h
	im, data, err := readImage(h.dbPath)
	if err != nil {
		return false, s.fail(h, "CP-W02", n, "repair", "cannot read the shared database: %v", err)
	}
	switch kind {
	case "torn":
		frame := EncodeBatchFrame(im.Super.Epoch, uint64(len(im.Frames))+1, []FileOp{{Key: []byte("never"), Value: make([]byte, 60)}})
		data = append(data, frame[:len(frame)/2]...)
	case "short":
		data = data[:len(data)-3] // 3 bytes of the last frame's end_magic: SHORT_SENTINEL, frame still accepted
	}
	if err := os.WriteFile(h.dbPath, data, 0o644); err != nil {
		return false, s.fail(h, "CP-W02", n, "repair", "%v", err)
	}
	return s.serveWriteCase("repair-"+kind, "CP-W02", n, "")
}

func (s *sweep) fail(h *Harness, cp string, n int, scenario, format string, args ...any) bool {
	r := &IterResult{Iter: 1000 + s.caseNo, Problems: []string{fmt.Sprintf(format, args...)}}
	return s.finish(h, r, scenario, cp, n, false, "")
}

// ---------------------------------------------------------------------------------------------
// Scenario: recovery (R01 to R03). `kvdb open` dies at the crash point; the file must not change.

func runKVDBEnv(kvdb string, env []string, args ...string) (killed bool, code int, err error) {
	cmd := exec.Command(kvdb, args...)
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		return false, -1, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Minute):
		_ = cmd.Process.Kill()
		<-done
		return false, -1, fmt.Errorf("kvdb %s timed out", strings.Join(args, " "))
	}
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ws.Signaled() && ws.Signal() == syscall.SIGKILL {
		return true, 0, nil
	}
	return false, cmd.ProcessState.ExitCode(), nil
}

func (s *sweep) recoverCase(cp string) (reached, ok bool) {
	h := s.h
	r, problem, _ := s.begin(h)
	pre, preSize, derr := fileDigest(h.dbPath)
	if derr != nil {
		problem("cannot read the database: %v", derr)
		return false, s.finish(h, r, "recover", cp, 1, false, "")
	}
	killed, code, err := runKVDBEnv(h.cfg.KVDB, s.env(cp, 1), "open", h.dbPath)
	if err != nil {
		problem("%v", err)
	}
	if !killed {
		problem("%s was never hit by `kvdb open` (exit %d): a coverage gap", cp, code)
	}
	if post, postSize, _ := fileDigest(h.dbPath); post != pre || postSize != preSize {
		problem("`kvdb open` killed at %s changed the database file (size %d -> %d): recovery must write no bytes (R1 to R3)", cp, preSize, postSize)
	}
	h.verifyAfterKill(r, problem)
	return killed, s.finish(h, r, "recover", cp, 1, killed, "file byte-identical, state = baseline")
}

// ---------------------------------------------------------------------------------------------
// Scenario: compaction (C01 to C06) on the shared database.

func (s *sweep) compactCase(cp string, n int) (reached, ok bool) {
	h := s.h
	r, problem, iter := s.begin(h)
	sess, err := StartSessionEnv(h.cfg.KVDB, h.dbPath, h.oracle, 1, s.env(cp, n))
	if err != nil {
		problem("%v", err)
		return false, s.finish(h, r, "compact", cp, n, false, "")
	}
	if _, err := sess.WaitReady(); err != nil {
		sess.Close()
		problem("serve did not start: %v", err)
		return false, s.finish(h, r, "compact", cp, n, false, "")
	}
	s.driveOps(h, sess, func(k int) *Op { return sweepOp(iter, k) }, 6) // garbage to compact; no C hit yet
	before, beforeSize, _ := fileDigest(h.dbPath)
	cop := &Op{ID: opID(iter, 0, 99), Lane: 0, Kind: KindCompact}
	_ = sess.SendNow(cop)
	waitUntil(replyTimeout, func() bool { st, _ := h.oracle.StateOf(cop.ID); return st != StateInFlight || sess.Dead() })
	exit := sess.KillAndDrain()
	reached = isSelfKill(exit)
	r.Ops = h.oracle.Iteration()
	if !reached && n == 1 {
		problem("%s was never hit by COMPACT: a coverage gap", cp)
	}
	outcome := "n/a"
	if reached {
		tmpExists := fileExists(h.dbPath + ".compact.tmp")
		after, afterSize, _ := fileDigest(h.dbPath)
		switch cp {
		case "CP-C01", "CP-C02", "CP-C03", "CP-C04":
			outcome = "old image, temp present"
			if !tmpExists {
				problem("%s: path.compact.tmp should exist (the compaction was building it), but it does not", cp)
			}
			if after != before || afterSize != beforeSize {
				problem("%s is before the rename: the data file must be byte-identical (P2), but it changed (size %d -> %d)", cp, beforeSize, afterSize)
			}
		case "CP-C05", "CP-C06":
			outcome = "new image, temp gone"
			if tmpExists {
				problem("%s is after the rename: path.compact.tmp should be gone", cp)
			}
			if after == before {
				problem("%s is after the rename: the data file should be the NEW image, but it is unchanged", cp)
			} else if afterSize > beforeSize {
				problem("%s: the compacted image (%d bytes) is larger than the old file (%d bytes)", cp, afterSize, beforeSize)
			}
		}
	}
	for _, a := range sess.Anomalies() {
		problem("serve protocol anomaly: %s", a)
	}
	h.verifyAfterKill(r, problem)
	return reached, s.finish(h, r, "compact", cp, n, reached, outcome+" tail="+r.Tail)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// ---------------------------------------------------------------------------------------------
// Scenario: rewrite on a full database (E5). A fresh database with a small cap; overwriting one
// key until the append no longer fits takes the REWRITE branch, which shares replaceFile with
// compaction. Killed at C01..C06 the put is absent before the rename and present after it.

func (s *sweep) rewriteCase(cp string) (reached, ok bool) {
	const capB = 100000
	dir := filepath.Join(s.h.workdir, "rewrite-"+strings.ToLower(cp))
	_ = os.RemoveAll(dir)
	h, err := newHarnessIn(s.cfg, dir, DefaultParams(), nil)
	if err != nil {
		return false, s.fail(s.h, cp, 1, "rewrite", "%v", err)
	}
	defer h.oracle.Close()
	h.diskCap = capB
	if out, errOut, code, err := runKVDB(h.cfg.KVDB, time.Minute, "open", h.dbPath, "--max-bytes", fmt.Sprint(capB)); err != nil || code != 0 {
		return false, s.fail(h, cp, 1, "rewrite", "cannot set the cap: %v %s", err, truncate(out+errOut, 160))
	}
	r, problem, iter := s.begin(h)
	sess, err := StartSessionEnv(h.cfg.KVDB, h.dbPath, h.oracle, 1, s.env(cp, 1))
	if err != nil {
		problem("%v", err)
		return false, s.finish(h, r, "rewrite", cp, 1, false, "")
	}
	if _, err := sess.WaitReady(); err != nil {
		sess.Close()
		problem("serve did not start: %v", err)
		return false, s.finish(h, r, "rewrite", cp, 1, false, "")
	}
	var lastDigest uint64
	var lastSize int64
	mk := func(k int) *Op {
		lastDigest, lastSize, _ = fileDigest(h.dbPath) // state before this put
		return &Op{ID: opID(iter, 0, uint32(k)), Lane: 0, Kind: KindPut, Parts: []Part{{Key: []byte("x"), Size: 30000}}}
	}
	s.driveOps(h, sess, mk, 8)
	exit := sess.KillAndDrain()
	reached = isSelfKill(exit)
	r.Ops = h.oracle.Iteration()
	if !reached {
		problem("%s was never hit: the put never took the REWRITE branch (cap %d)", cp, capB)
	}
	outcome := "n/a"
	if reached {
		after, afterSize, _ := fileDigest(h.dbPath)
		inf := inflightOp(r.Ops)
		tmpExists := fileExists(h.dbPath + ".compact.tmp")
		switch cp {
		case "CP-C01", "CP-C02", "CP-C03", "CP-C04":
			outcome = "old image, put absent"
			if after != lastDigest || afterSize != lastSize {
				problem("%s is before the rename: the data file must be byte-identical to before the put (P2), size %d -> %d", cp, lastSize, afterSize)
			}
			if !tmpExists {
				problem("%s: path.compact.tmp should exist", cp)
			}
		case "CP-C05", "CP-C06":
			outcome = "new image, put present"
			if tmpExists {
				problem("%s is after the rename: path.compact.tmp should be gone", cp)
			}
			if after == lastDigest {
				problem("%s is after the rename: the data file should be the new image", cp)
			}
			if inf == nil {
				problem("%s: no put was in flight", cp)
			}
		}
		h.verifyAfterKill(r, problem)
		if r.Check != nil {
			sup := surprise(r)
			switch cp {
			case "CP-C01", "CP-C02", "CP-C03", "CP-C04":
				if sup != nil {
					problem("%s: the put must be absent (old image), but op %d is present", cp, sup.ID)
				}
			case "CP-C05", "CP-C06":
				if inf != nil && (sup == nil || sup.ID != inf.ID) {
					problem("%s: the rename happened, so the put (op %d) must be present in the new image", cp, inf.ID)
				}
			}
		}
	} else {
		h.verifyAfterKill(r, problem)
	}
	return reached, s.finish(h, r, "rewrite", cp, 1, reached, outcome+" tail="+r.Tail)
}

// ---------------------------------------------------------------------------------------------
// Scenario: superblock updates (S01). Cap change via `open --max-bytes`, and a real checkpoint.

// capCase kills `kvdb open --max-bytes N` inside the superblock write. The engine's hook writes a
// random prefix of the whole 4096-byte slot buffer, but only 68 bytes of it are meaningful, so about
// 98% of seeds leave a COMPLETE valid slot and only a prefix under 68 bytes tears it. With search
// set, seeds are tried until one tears the slot, so both outcomes are proven.
func (s *sweep) capCase(search bool) (reached, ok bool) {
	h := s.h
	attempts := 1
	if search {
		attempts = capSearch
	}
	for a := 0; a < attempts; a++ {
		s.capN++
		s.salt = uint64(s.capN)
		newCap := capChange + int64(s.capN) // a different value each time, otherwise nothing is written
		r, problem, _ := s.begin(h)
		im0, data0, err := readImage(h.dbPath)
		if err != nil {
			problem("cannot read the database: %v", err)
			return false, s.finish(h, r, "cap-change", "CP-S01", 1, false, "")
		}
		killed, code, err := runKVDBEnv(h.cfg.KVDB, s.env("CP-S01", 1), "open", h.dbPath, "--max-bytes", fmt.Sprint(newCap))
		if err != nil {
			problem("%v", err)
		}
		if !killed {
			problem("CP-S01 was never hit by `kvdb open --max-bytes` (exit %d): a coverage gap", code)
		}
		im1, data1, err := readImage(h.dbPath)
		outcome, torn := "n/a", false
		if err != nil {
			problem("after the kill HYDRA's reader finds no valid superblock: %v", err)
		} else if killed {
			if len(data1) != len(data0) || string(data1[firstFrameOff:]) != string(data0[firstFrameOff:]) {
				problem("a cap change killed in the superblock write changed the frames (size %d -> %d): only the inactive slot may be written", len(data0), len(data1))
			}
			if string(data1[im0.Chosen*sbSlotSize:(im0.Chosen+1)*sbSlotSize]) != string(data0[im0.Chosen*sbSlotSize:(im0.Chosen+1)*sbSlotSize]) {
				problem("the ACTIVE superblock slot was modified: a cap change must write only the inactive one")
			}
			switch im1.Super.MaxBytes {
			case im0.Super.MaxBytes:
				outcome, torn = "slot TORN, old cap kept", true
			case uint64(newCap):
				outcome = "slot complete, new cap"
			default:
				problem("max_bytes is %d after the kill, expected the old %d or the new %d", im1.Super.MaxBytes, im0.Super.MaxBytes, newCap)
			}
			if torn {
				// A torn slot must be rejected by sb_crc and recovery must still report the OLD cap.
				if out, _, code, err := runKVDB(h.cfg.KVDB, time.Minute, "open", h.dbPath); err != nil || code != 0 || parseKV(strings.TrimSpace(out))["max_bytes"] != fmt.Sprint(im0.Super.MaxBytes) {
					problem("with a torn newest slot `kvdb open` should report the old max_bytes=%d, got %q (%v)", im0.Super.MaxBytes, strings.TrimSpace(out), err)
				}
			}
		}
		h.verifyAfterKill(r, problem)
		if search && !torn && r.Pass() && a < attempts-1 {
			s.caseNo-- // quiet retry: do not print a line per attempt
			continue
		}
		s.salt = 0
		if search && !torn && r.Pass() {
			problem("no seed out of %d tore the superblock slot", attempts)
		}
		if search {
			outcome += fmt.Sprintf(" (found after %d attempts)", a+1)
		}
		return killed, s.finish(h, r, "cap-change", "CP-S01", 1, killed, outcome)
	}
	return false, true
}

// checkpointCase writes more than the 64 MiB checkpoint threshold in a fresh database so that the
// engine appends a KEYDIR and flips the superblock, and kills it inside that flip.
func (s *sweep) checkpointCase() (reached, ok bool) {
	dir := filepath.Join(s.h.workdir, "checkpoint")
	_ = os.RemoveAll(dir)
	h, err := newHarnessIn(s.cfg, dir, DefaultParams(), nil)
	if err != nil {
		return false, s.fail(s.h, "CP-S01", 1, "checkpoint", "%v", err)
	}
	defer h.oracle.Close()
	defer os.RemoveAll(dir)
	r, problem, iter := s.begin(h)
	sess, err := StartSessionEnv(h.cfg.KVDB, h.dbPath, h.oracle, 1, s.env("CP-S01", 1))
	if err != nil {
		problem("%v", err)
		return false, s.finish(h, r, "checkpoint", "CP-S01", 1, false, "")
	}
	if _, err := sess.WaitReady(); err != nil {
		sess.Close()
		problem("serve did not start: %v", err)
		return false, s.finish(h, r, "checkpoint", "CP-S01", 1, false, "")
	}
	mk := func(k int) *Op {
		return &Op{ID: opID(iter, 0, uint32(k)), Lane: 0, Kind: KindPut, Parts: []Part{{Key: []byte(fmt.Sprintf("ck%03d", k)), Size: 1 << 20}}}
	}
	s.driveOps(h, sess, mk, 100) // 100 MiB > the 64 MiB default threshold
	exit := sess.KillAndDrain()
	reached = isSelfKill(exit)
	r.Ops = h.oracle.Iteration()
	if !reached {
		problem("CP-S01 was never hit: 100 MiB of writes did not trigger a checkpoint")
	}
	outcome := "n/a"
	if reached {
		if im, _, err := readImage(h.dbPath); err != nil {
			problem("after the kill HYDRA's reader finds no valid superblock: %v", err)
		} else {
			hasKeydir := false
			for _, f := range im.Frames {
				if f.Type == typeKeydir {
					hasKeydir = true
				}
			}
			if !hasKeydir {
				problem("the checkpoint's KEYDIR frame should be durable before the superblock flip begins, but the file has none")
			}
			if im.Super.CkptOff == 0 {
				outcome = "slot torn, KEYDIR durable, older slot used"
			} else {
				outcome = "slot complete, checkpoint active"
			}
		}
	}
	h.verifyAfterKill(r, problem)
	return reached, s.finish(h, r, "checkpoint", "CP-S01", 1, reached, outcome)
}

// writeCoverage records what the sweep actually hit, for `--coverage` (stage H5).
func (s *sweep) writeCoverage() {
	if err := os.MkdirAll("failures", 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(map[string]any{"sweep": s.results}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join("failures", "coverage-sweep.json"), append(b, '\n'), 0o644)
}

// runKVDBEnvTimed is runKVDBEnv with an external SIGKILL after d.
func runKVDBEnvTimed(kvdb string, env []string, d time.Duration, args ...string) (killed bool, code int, err error) {
	cmd := exec.Command(kvdb, args...)
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		return false, -1, err
	}
	timer := time.AfterFunc(d, func() { _ = cmd.Process.Kill() })
	_ = cmd.Wait()
	timer.Stop()
	ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ws.Signaled() && ws.Signal() == syscall.SIGKILL {
		return true, 0, nil
	}
	return false, cmd.ProcessState.ExitCode(), nil
}

// printFingerprints groups the reached injections by the state they left behind (SPEC 15.10).
func (s *sweep) printFingerprints() {
	groups := map[string][]string{}
	total := 0
	for _, r := range s.results {
		if r.FP == "" {
			continue
		}
		total++
		groups[r.FP] = append(groups[r.FP], fmt.Sprintf("%s/%s:%d", strings.TrimPrefix(r.CP, "CP-"), r.Scenario, r.N))
	}
	if total == 0 {
		return
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(groups[keys[i]]) != len(groups[keys[j]]) {
			return len(groups[keys[i]]) > len(groups[keys[j]])
		}
		return keys[i] < keys[j]
	})
	fmt.Printf("state fingerprints: %d injections left %d distinct on-disk states\n", total, len(groups))
	for _, k := range keys {
		names := groups[k]
		more := ""
		if len(names) > 6 {
			more = fmt.Sprintf(" ... +%d", len(names)-6)
			names = names[:6]
		}
		fmt.Printf("  x%-3d %-62s %s%s\n", len(groups[k]), k, strings.Join(names, " "), more)
	}
}
