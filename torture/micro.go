package main

// Micro mode (SPEC 15.7): tiny adversarial traces. Ordering bugs show up faster in an 11-op
// sequence killed at every line than in a huge random workload. Each trace starts from a pristine
// image, is killed at EVERY crash point it passes (found by sweeping KVDB_CRASH=<cp>:<n> until the
// trace runs to the end without dying) plus 20 random instants, and after each kill:
//   - the oracle checks the recovered state (ACKed present, at most one unACKed, all-or-none),
//   - recovery wrote no bytes,
//   - the database is still writable: one more `kvdb put` works and the result passes check.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func init() { modes["micro"] = runMicro }

const (
	microRandomKills = 20
	microMaxHits     = 4
)

var allCPs = []string{"CP-W01", "CP-W02", "CP-W03", "CP-W04", "CP-W05", "CP-W06", "CP-W07",
	"CP-R01", "CP-R02", "CP-R03", "CP-C01", "CP-C02", "CP-C03", "CP-C04", "CP-C05", "CP-C06", "CP-S01"}

type step struct {
	kind  OpKind
	parts []Part
}

func putStep(key string, size int) step {
	return step{kind: KindPut, parts: []Part{{Key: []byte(key), Size: size}}}
}
func delStep(key string) step {
	return step{kind: KindDel, parts: []Part{{Del: true, Key: []byte(key)}}}
}
func batchStep(parts ...Part) step { return step{kind: KindBatch, parts: parts} }
func compactStep() step            { return step{kind: KindCompact} }

type microTrace struct {
	id, desc  string
	setup     []step                            // acknowledged before the pristine image is taken
	prepare   func(me *microEnv) error          // optional file surgery on the pristine image
	steps     []step                            // the trace that gets killed
	cmd       []string                          // instead of steps: a CLI command with {db} replaced
	cps       []string                          // crash points to explore (nil = all)
	random    bool                              // also kill at random instants
	capBytes  func(me *microEnv) (int64, error) // T11: cap chosen after setup
	noPost    bool                              // skip the post-crash write
	searchS01 bool                              // T10: look for a seed that tears the slot
}

func microTraces() []microTrace {
	return []microTrace{
		{id: "T1", desc: "PUT A", steps: []step{putStep("A", 40)}, random: true},
		{id: "T2", desc: "PUT A; PUT A", steps: []step{putStep("A", 40), putStep("A", 41)}, random: true},
		{id: "T3", desc: "PUT A; DEL A", steps: []step{putStep("A", 40), delStep("A")}, random: true},
		{id: "T4", desc: "BATCH(PUT A, PUT B)", steps: []step{batchStep(Part{Key: []byte("A"), Size: 40}, Part{Key: []byte("B"), Size: 41})}, random: true},
		{id: "T5", desc: "BATCH(PUT X, DEL X)", steps: []step{batchStep(Part{Key: []byte("X"), Size: 40}, Part{Del: true, Key: []byte("X")})}, random: true},
		{id: "T6", desc: "PUT A; COMPACT", steps: []step{putStep("A", 40), compactStep()}, random: true},
		{id: "T7", desc: "DEL A; COMPACT; PUT A", setup: []step{putStep("A", 40)}, steps: []step{delStep("A"), compactStep(), putStep("A", 41)}, random: true},
		{id: "T8", desc: "write past the checkpoint threshold, kill inside the superblock flip", steps: func() []step {
			var st []step
			for i := 0; i < 100; i++ {
				st = append(st, putStep(fmt.Sprintf("ck%03d", i), 1<<20))
			}
			return st
		}(), cps: []string{"CP-S01"}, noPost: false},
		{id: "T9", desc: "F4: truncate 1 byte; PUT B", setup: []step{putStep("A", 40)},
			prepare: func(me *microEnv) error { return me.truncateImage(1) }, steps: []step{putStep("B", 41)}, random: true},
		{id: "T10", desc: "open --max-bytes N (cap change)", setup: []step{putStep("A", 40)},
			cmd: []string{"open", "{db}", "--max-bytes", "123456789"}, cps: []string{"CP-S01"}, random: true, searchS01: true},
		{id: "T11", desc: "at the cap: DEL A (rewrite path)",
			setup:    []step{putStep("a", 30000), putStep("f", 30000), putStep("f", 30001)},
			capBytes: func(me *microEnv) (int64, error) { sz, err := fileSize(me.h.dbPath); return sz + 20, err },
			steps:    []step{delStep("a")}, noPost: true, random: true},
	}
}

type microEnv struct {
	cfg   *Config
	tr    microTrace
	h     *Harness
	image []byte
	snap  Baseline
	runNo int
	cap   int64
}

type microResult struct {
	Trace   string `json:"trace"`
	CP      string `json:"cp,omitempty"`
	N       int    `json:"n,omitempty"`
	Random  bool   `json:"random,omitempty"`
	Reached bool   `json:"reached"`
}

func runMicro(cfg *Config) int {
	if _, err := os.Stat(cfg.KVDB); err != nil {
		fmt.Fprintf(os.Stderr, "torture: kvdb binary not found at %s (run `make` first, or pass --kvdb)\n", cfg.KVDB)
		return 2
	}
	root := cfg.WorkDir
	if root == "" {
		d, err := os.MkdirTemp("", "hydra-micro-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "torture: %v\n", err)
			return 2
		}
		root = d
	}
	start := time.Now()
	var all []microResult
	for _, tr := range microTraces() {
		if cfg.CP != "" && !(len(tr.cps) == 0 || contains(tr.cps, cfg.CP)) {
			continue
		}
		me, err := newMicroEnv(cfg, tr, filepath.Join(root, tr.id))
		if err != nil {
			fmt.Printf("micro %-3s %s: setup failed: %v\n", tr.id, tr.desc, err)
			return 1
		}
		res, ok := me.exploreAll()
		all = append(all, res...)
		me.h.oracle.Close()
		if !ok {
			return 1
		}
		reached, randoms := 0, 0
		cps := map[string]bool{}
		for _, r := range res {
			switch {
			case r.Random:
				randoms++
			case r.Reached:
				reached++
				cps[r.CP] = true
			}
		}
		fmt.Printf("micro %-3s %-52s %2d crash-point runs (%2d distinct points), %d random kills  PASS\n", tr.id, tr.desc, reached, len(cps), randoms)
	}
	writeCoverageFile("micro", all)
	if cfg.WorkDir == "" && !cfg.Keep {
		_ = os.RemoveAll(root)
	}
	fmt.Printf("RESULT PASS micro traces (seed=%d, %s)\n", cfg.Seed, time.Since(start).Round(time.Second))
	return 0
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func writeCoverageFile(name string, v any) {
	if err := os.MkdirAll("failures", 0o755); err != nil {
		return
	}
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join("failures", "coverage-"+name+".json"), append(b, '\n'), 0o644)
	}
}

func newMicroEnv(cfg *Config, tr microTrace, dir string) (*microEnv, error) {
	_ = os.RemoveAll(dir)
	h, err := newHarnessIn(cfg, dir, DefaultParams(), nil)
	if err != nil {
		return nil, err
	}
	me := &microEnv{cfg: cfg, tr: tr, h: h}
	if out, errOut, code, err := runKVDB(cfg.KVDB, time.Minute, "open", h.dbPath); err != nil || code != 0 {
		return nil, fmt.Errorf("cannot create the database: %v %s", err, truncate(out+errOut, 160))
	}
	if len(tr.setup) > 0 {
		if err := me.runSetup(tr.setup); err != nil {
			return nil, err
		}
	}
	if tr.capBytes != nil {
		c, err := tr.capBytes(me)
		if err != nil {
			return nil, err
		}
		if out, errOut, code, err := runKVDB(cfg.KVDB, time.Minute, "open", h.dbPath, "--max-bytes", fmt.Sprint(c)); err != nil || code != 0 {
			return nil, fmt.Errorf("cannot set the cap: %v %s", err, truncate(out+errOut, 160))
		}
		me.cap, h.diskCap = c, c
	}
	if tr.prepare != nil {
		if err := tr.prepare(me); err != nil {
			return nil, err
		}
	}
	if me.image, err = os.ReadFile(h.dbPath); err != nil {
		return nil, err
	}
	me.snap = Baseline{}
	for k, v := range h.base {
		me.snap[k] = v
	}
	return me, nil
}

func (me *microEnv) truncateImage(n int) error {
	data, err := os.ReadFile(me.h.dbPath)
	if err != nil {
		return err
	}
	return os.WriteFile(me.h.dbPath, data[:len(data)-n], 0o644)
}

func (me *microEnv) makeOp(iter, k int, st step) *Op {
	return &Op{ID: opID(iter, 0, uint32(k)), Lane: 0, Kind: st.kind, Parts: st.parts}
}

// runSetup runs acknowledged steps to completion and verifies them, so the pristine image and its
// baseline are known-good.
func (me *microEnv) runSetup(steps []step) error {
	h := me.h
	r := &IterResult{Iter: 0}
	problem := func(f string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(f, a...)) }
	h.oracle.BeginIteration(0, 0)
	sess, err := StartSession(me.cfg.KVDB, h.dbPath, h.oracle, 1)
	if err != nil {
		return err
	}
	if _, err := sess.WaitReady(); err != nil {
		sess.Close()
		return err
	}
	for i, st := range steps {
		if _, err := h.sendWait(sess, me.makeOp(0, i+1, st)); err != nil {
			sess.Close()
			return err
		}
	}
	sess.KillAndDrain()
	r.Ops = h.oracle.Iteration()
	h.verifyAfterKill(r, problem)
	if !r.Pass() {
		msg := strings.Join(r.Problems, "; ")
		if r.Check != nil {
			for _, f := range r.Check.Failures {
				msg += "; " + strings.ReplaceAll(f.String(), "\n", " ")
			}
		}
		return fmt.Errorf("setup did not verify: %s", msg)
	}
	return nil
}

func (me *microEnv) restore() error {
	h := me.h
	for _, p := range []string{h.dbPath + ".compact.tmp", h.dbPath + ".create.tmp"} {
		_ = os.Remove(p)
	}
	if err := os.WriteFile(h.dbPath, me.image, 0o644); err != nil {
		return err
	}
	h.base = Baseline{}
	for k, v := range me.snap {
		h.base[k] = v
	}
	return nil
}

// exploreAll kills the trace at every crash point it passes, then at random instants.
func (me *microEnv) exploreAll() ([]microResult, bool) {
	var res []microResult
	cps := me.tr.cps
	if len(cps) == 0 {
		cps = allCPs
	}
	if me.cfg.CP != "" { // --cp narrows a run to one crash point (and skips the random kills)
		cps = []string{me.cfg.CP}
	}
	hits := me.cfg.Hits
	if hits > microMaxHits {
		hits = microMaxHits
	}
	for _, cp := range cps {
		for n := 1; n <= hits; n++ {
			reached, r := me.runOnce(cp, n, 0, false)
			res = append(res, microResult{Trace: me.tr.id, CP: cp, N: n, Reached: reached})
			if !r.Pass() {
				me.report(r, cp, n)
				return res, false
			}
			if !reached {
				break
			}
		}
	}
	if me.tr.random && me.cfg.CP == "" {
		full := me.fullDuration()
		rg := newRNG(iterSeed(me.cfg.Seed, 7000+len(me.tr.id)))
		for i := 0; i < microRandomKills; i++ {
			after := time.Duration(rg.next() % uint64(full+full/4+1))
			reached, r := me.runOnce("", 0, after, true)
			res = append(res, microResult{Trace: me.tr.id, Random: true, Reached: reached})
			if !r.Pass() {
				me.report(r, "random", i)
				return res, false
			}
		}
	}
	return res, true
}

func (me *microEnv) report(r *IterResult, cp string, n int) {
	me.h.reproHint = fmt.Sprintf("%s --mode micro --seed %d --cp %s --hits %d   (trace %s: %s)", os.Args[0], me.cfg.Seed, cp, n, me.tr.id, me.tr.desc)
	fmt.Printf("micro %s %s FAIL at %s:%d\n", me.tr.id, me.tr.desc, cp, n)
	me.h.reportFailure(r)
}

// fullDuration is how long an uninterrupted run of the trace takes after READY.
func (me *microEnv) fullDuration() time.Duration {
	t0 := time.Now()
	me.runTrace("", 0, 0, false)
	d := time.Since(t0)
	if d < time.Millisecond {
		d = time.Millisecond
	}
	return d
}

// runOnce restores the pristine image, runs the trace with the given crash point (or random kill
// after `after`), verifies the result and checks that the database still accepts a write.
func (me *microEnv) runOnce(cp string, n int, after time.Duration, random bool) (reached bool, r *IterResult) {
	if me.tr.searchS01 && cp == "CP-S01" {
		return me.searchTornSlot()
	}
	return me.runTrace(cp, n, after, random)
}

func (me *microEnv) runTrace(cp string, n int, after time.Duration, random bool) (reached bool, r *IterResult) {
	h := me.h
	me.runNo++
	iter := 5000 + me.runNo
	r = &IterResult{Iter: iter}
	problem := func(f string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(f, a...)) }
	if err := me.restore(); err != nil {
		problem("%v", err)
		return false, r
	}
	h.oracle.BeginIteration(iter, iterSeed(me.cfg.Seed, iter))
	var env []string
	if cp != "" {
		env = []string{fmt.Sprintf("KVDB_CRASH=%s:%d", cp, n), fmt.Sprintf("KVDB_CRASH_SEED=%d", splitmix64(me.cfg.Seed+uint64(iter))>>1)}
	}

	if me.tr.cmd != nil {
		args := make([]string, len(me.tr.cmd))
		for i, a := range me.tr.cmd {
			args[i] = strings.ReplaceAll(a, "{db}", h.dbPath)
		}
		killed, _, err := runKVDBEnvAfter(me.cfg.KVDB, env, after, args...)
		if err != nil {
			problem("%v", err)
		}
		reached = killed
		r.Ops = h.oracle.Iteration()
		h.verifyAfterKill(r, problem)
		me.postWrite(r, problem)
		return reached, r
	}

	sess, err := StartSessionEnv(me.cfg.KVDB, h.dbPath, h.oracle, 1, env)
	if err != nil {
		problem("%v", err)
		return false, r
	}
	if _, err := sess.WaitReady(); err != nil {
		// serve died before READY: legitimate when the crash point is in recovery (CP-R01..R03).
		exit := sess.KillAndDrain()
		if isSelfKill(exit) && strings.HasPrefix(cp, "CP-R") {
			reached = true
		} else {
			problem("serve did not start: %v", err)
		}
	} else {
		var timer *time.Timer
		if random {
			timer = time.AfterFunc(after, sess.Kill)
		}
		for i, st := range me.tr.steps {
			op := me.makeOp(iter, i+1, st)
			if err := sess.SendNow(op); err != nil {
				break
			}
			if !waitUntil(replyTimeout, func() bool { s, _ := h.oracle.StateOf(op.ID); return s != StateInFlight || sess.Dead() }) || sess.Dead() {
				break
			}
		}
		if timer != nil {
			timer.Stop()
		}
		exit := sess.KillAndDrain()
		if random {
			reached = exit.EarlyExit || exit.Signaled
		} else {
			reached = isSelfKill(exit)
		}
	}
	for _, a := range sess.Anomalies() {
		problem("serve protocol anomaly: %s", a)
	}
	r.Ops = h.oracle.Iteration()
	h.verifyAfterKill(r, problem)
	me.postWrite(r, problem)
	return reached, r
}

// postWrite proves the crashed database is still usable: one more put is accepted and the file checks out.
func (me *microEnv) postWrite(r *IterResult, problem func(string, ...any)) {
	if me.tr.noPost || !r.Pass() {
		return
	}
	h := me.h
	out, errOut, code, err := runKVDB(me.cfg.KVDB, time.Minute, "put", h.dbPath, fmt.Sprintf("post-%d", r.Iter), "1")
	if err != nil || code != 0 || strings.TrimSpace(out) != "OK" {
		problem("after the crash a plain `kvdb put` failed (exit %d): %v %s", code, err, truncate(strings.TrimSpace(out+errOut), 160))
		return
	}
	out, _, code, err = runKVDB(me.cfg.KVDB, time.Minute, "check", h.dbPath)
	if err != nil || code != 0 || !strings.HasPrefix(out, "OK") {
		problem("after the post-crash write `kvdb check` says: %s", truncate(strings.TrimSpace(out), 200))
	}
	if !strings.Contains(out, "tail=COMPLETE") {
		problem("after the post-crash write the tail should be COMPLETE (the write repairs first, P4): %s", truncate(strings.TrimSpace(out), 200))
	}
}

// searchTornSlot is T10: look for a seed whose torn prefix really tears the superblock slot.
func (me *microEnv) searchTornSlot() (bool, *IterResult) {
	var last *IterResult
	for a := 0; a < capSearch; a++ {
		reached, r := me.runTrace("CP-S01", 1, 0, false)
		last = r
		if !r.Pass() || !reached {
			return reached, r
		}
		if im, _, err := readImage(me.h.dbPath); err == nil && im.Super.MaxBytes == 0 {
			return true, r // torn slot: the old cap (0) is still in force
		}
	}
	last.Problems = append(last.Problems, fmt.Sprintf("no seed out of %d tore the superblock slot", capSearch))
	return true, last
}

// runKVDBEnvAfter runs a CLI command with extra env and an optional external SIGKILL after d.
func runKVDBEnvAfter(kvdb string, env []string, after time.Duration, args ...string) (killed bool, code int, err error) {
	if after <= 0 {
		return runKVDBEnv(kvdb, env, args...)
	}
	return runKVDBEnvTimed(kvdb, env, after, args...)
}
