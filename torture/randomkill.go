package main

// Default mode (SPEC 15.2): random SIGKILL against `kvdb serve --b64`, verified against the oracle.
//
// One iteration:
//  1. spawn serve, wait for READY
//  2. start N lanes, each pipelining up to 8 ops (put, del, batch with witness, mixed)
//  3. one reader parses COMPLETE reply lines only and assigns ack_seq in arrival order
//  4. sleep a seeded 10..250 ms, then SIGKILL
//  5. read stdout to EOF (flushed ACKs are still in the pipe)
//  6. kvdb open, kvdb check, kvdb dump
//  7. compare against the oracle (per-key rule 14.4 and commit-order rules 14.5)
//
// This mode never uses crash hooks: the kill instant is purely a random timer. A torture tool that
// only passes because of hooks counts as rigged (SPEC 15.6).

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() {
	modes["default"] = func(cfg *Config) int { return runRandomKill(cfg, DefaultParams(), nil) }
	modes["large-values"] = func(cfg *Config) int { return runRandomKill(cfg, LargeValuesParams(), nil) }
	modes["large-batches"] = func(cfg *Config) int { return runRandomKill(cfg, LargeBatchesParams(), nil) }
}

const iterationBudget = 600 * time.Millisecond // SPEC 15.5

// Harness holds the state that lives across iterations of one run.
type Harness struct {
	cfg        *Config
	params     WorkloadParams
	serveExtra []string
	workdir    string
	dbPath     string
	oracle     *Oracle
	base       Baseline
}

// IterResult is everything one iteration learned.
type IterResult struct {
	Iter     int
	KillMS   int
	Problems []string
	Check    *CheckResult
	Ops      []*Op
	Keys     int
	OpenMS   int
	Tail     string
	OpenKeys string
	CheckOut string
	Elapsed  time.Duration
}

// Pass reports whether the iteration found nothing wrong.
func (r *IterResult) Pass() bool { return len(r.Problems) == 0 && (r.Check == nil || r.Check.OK()) }

func newHarness(cfg *Config, params WorkloadParams, serveExtra []string) (*Harness, error) {
	dir := cfg.WorkDir
	if dir == "" {
		d, err := os.MkdirTemp("", "hydra-*")
		if err != nil {
			return nil, err
		}
		dir = d
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	orc, err := NewOracle(filepath.Join(dir, "oracle.log"))
	if err != nil {
		return nil, err
	}
	return &Harness{
		cfg: cfg, params: params, serveExtra: serveExtra, workdir: dir,
		dbPath: filepath.Join(dir, "database.db"), oracle: orc, base: Baseline{},
	}, nil
}

func runRandomKill(cfg *Config, params WorkloadParams, serveExtra []string) int {
	if _, err := os.Stat(cfg.KVDB); err != nil {
		fmt.Fprintf(os.Stderr, "torture: kvdb binary not found at %s (run `make` first, or pass --kvdb)\n", cfg.KVDB)
		return 2
	}
	h, err := newHarness(cfg, params, serveExtra)
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	start := time.Now()
	passed := false
	defer func() {
		_ = h.oracle.Close()
		if passed && !cfg.Keep && cfg.WorkDir == "" {
			_ = os.RemoveAll(h.workdir)
		} else {
			fmt.Printf("workdir kept: %s\n", h.workdir)
		}
	}()

	var total, worst time.Duration
	for iter := 1; iter <= cfg.Iterations; iter++ {
		r := h.iteration(iter)
		total += r.Elapsed
		if r.Elapsed > worst {
			worst = r.Elapsed
		}
		if !r.Pass() {
			h.reportFailure(r)
			return 1
		}
		fmt.Println(h.iterationLine(r))
	}
	passed = true
	fmt.Printf("RESULT PASS %d/%d (seed=%d, mode=%s, %s)\n", cfg.Iterations, cfg.Iterations, cfg.Seed, cfg.Mode, time.Since(start).Round(time.Second))
	if avg := total / time.Duration(cfg.Iterations); avg > iterationBudget {
		fmt.Printf("NOTE average iteration %s exceeds the %s budget (worst %s): the judge's 200 iterations would not fit in about 3 minutes\n",
			avg.Round(time.Millisecond), iterationBudget, worst.Round(time.Millisecond))
	}
	return 0
}

func (h *Harness) iterationLine(r *IterResult) string {
	return fmt.Sprintf("iter %d/%d seed=%d mode=%s kill_ms=%d acked=%d inflight=%d rejected=%d keys=%d open_ms=%d check=OK PASS",
		r.Iter, h.cfg.Iterations, h.cfg.Seed, h.cfg.Mode, r.KillMS,
		r.Check.Acked, r.Check.InFlight, r.Check.Rejected, r.Keys, r.OpenMS)
}

// iteration runs one kill/verify cycle. It never returns early without filling Problems, so a
// failure always says what went wrong.
func (h *Harness) iteration(iter int) *IterResult {
	began := time.Now()
	r := &IterResult{Iter: iter}
	defer func() { r.Elapsed = time.Since(began) }()
	problem := func(format string, args ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, args...)) }

	is := iterSeed(h.cfg.Seed, iter)
	r.KillMS = killDelayMS(is)
	h.oracle.BeginIteration(iter, is)

	// 1. start serve and wait for READY (open + recovery succeeded).
	sess, err := StartSession(h.cfg.KVDB, h.dbPath, h.oracle, h.cfg.Writers, h.serveExtra...)
	if err != nil {
		problem("%v", err)
		return r
	}
	gens := make([]*Generator, h.cfg.Writers)
	for lane := range gens {
		gens[lane] = NewGenerator(h.params, h.cfg.Seed, iter, lane)
	}
	sess.PrepareLanes(gens) // encode the first requests while serve is still recovering
	ready, err := sess.WaitReady()
	if err != nil {
		sess.Close()
		problem("INVARIANT 5 violated: open() failed on a file our database produced: %v", err)
		return r
	}
	rf := parseKV(ready)
	if want := len(h.base); rf["keys"] != strconv.Itoa(want) {
		problem("serve recovered keys=%s but the previous verified iteration left %d keys", rf["keys"], want)
	}

	// 2-4. lanes, random delay, SIGKILL, drain stdout.
	sess.StartLanes()
	time.Sleep(time.Duration(r.KillMS) * time.Millisecond)
	h.oracle.Kill(iter, int64(r.KillMS))
	exit := sess.KillAndDrain()
	switch {
	case exit.EarlyExit:
		problem("serve exited by itself before the kill (%s); stderr: %s", exit, truncate(strings.TrimSpace(sess.Stderr()), 400))
	case !exit.Signaled || exit.Signal != syscall.SIGKILL:
		problem("serve did not end by our SIGKILL (%s)", exit)
	}
	for _, a := range sess.Anomalies() {
		problem("serve protocol anomaly: %s", a)
	}
	r.Ops = h.oracle.Iteration()

	// 5. open / check / dump, and prove that recovery wrote no bytes (C1).
	pre, preSize, derr := fileDigest(h.dbPath)
	if derr != nil {
		problem("cannot read database file after the kill: %v", derr)
		return r
	}
	dump := h.observe(r, problem)
	if post, postSize, derr := fileDigest(h.dbPath); derr != nil {
		problem("cannot re-read database file: %v", derr)
	} else if post != pre || postSize != preSize {
		problem("open/check/dump changed the database file (size %d -> %d): recovery must write no bytes (SPEC 5.4)", preSize, postSize)
	}
	if dump == nil {
		return r
	}
	r.Keys = len(dump)
	if r.OpenKeys != "" && r.OpenKeys != strconv.Itoa(len(dump)) {
		problem("kvdb open reports keys=%s but kvdb dump lists %d keys", r.OpenKeys, len(dump))
	}

	// 6. the oracle decides.
	r.Check = CheckIteration(h.base, r.Ops, dump, h.oracle.StateOf)
	if r.Pass() {
		changes := map[string]KeyState{}
		for k, st := range r.Check.Observed {
			if cur, ok := h.base[k]; !ok || !sameState(cur, st) {
				changes[k] = st
			}
		}
		for k := range h.base {
			if _, ok := r.Check.Observed[k]; !ok {
				changes[k] = KeyState{}
			}
		}
		h.oracle.Observed(changes)
		h.oracle.Retire(r.Check.SeenOps)
		UpdateBaseline(h.base, r.Check)
	}
	return r
}

// observe runs kvdb open, check and dump and returns the parsed dump (nil if it could not be obtained).
func (h *Harness) observe(r *IterResult, problem func(string, ...any)) map[string][]byte {
	const timeout = 2 * time.Minute
	out, errOut, code, err := runKVDB(h.cfg.KVDB, timeout, "open", h.dbPath)
	switch {
	case err != nil:
		problem("kvdb open: %v", err)
	case code != 0:
		problem("kvdb open exited %d: %s", code, truncate(strings.TrimSpace(errOut+out), 300))
	default:
		of := parseKV(strings.TrimSpace(out))
		if ms, e := strconv.ParseFloat(of["recovery_ms"], 64); e == nil {
			r.OpenMS = int(ms)
		} else {
			problem("kvdb open printed no recovery_ms: %q", truncate(out, 120))
		}
		r.Tail = of["tail"]
		if r.OpenKeys = of["keys"]; r.OpenKeys == "" {
			problem("kvdb open printed no keys=: %q", truncate(out, 120))
		}
	}

	out, errOut, code, err = runKVDB(h.cfg.KVDB, timeout, "check", h.dbPath)
	r.CheckOut = strings.TrimSpace(out)
	switch {
	case err != nil:
		problem("kvdb check: %v", err)
	case code != 0 || !strings.HasPrefix(out, "OK"):
		problem("kvdb check failed (exit %d): %s", code, truncate(strings.TrimSpace(out+errOut), 300))
	}

	out, errOut, code, err = runKVDB(h.cfg.KVDB, timeout, "dump", h.dbPath)
	if err != nil {
		problem("kvdb dump: %v", err)
		return nil
	}
	if code != 0 {
		problem("kvdb dump exited %d: %s", code, truncate(strings.TrimSpace(errOut), 300))
		return nil
	}
	dump, perr := ParseDump(strings.NewReader(out))
	if perr != nil {
		problem("kvdb dump output is malformed: %v", perr)
		return nil
	}
	return dump
}

// parseKV parses "a=1 b=2 ..." into a map. Tokens without '=' are ignored.
func parseKV(line string) map[string]string {
	m := map[string]string{}
	for _, tok := range strings.Fields(line) {
		if i := strings.IndexByte(tok, '='); i > 0 {
			m[tok[:i]] = tok[i+1:]
		}
	}
	return m
}
