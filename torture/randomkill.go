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
	modes["compaction"] = func(cfg *Config) int { return runRandomKillOpts(cfg, DefaultParams(), nil, true) }
}

// Harness holds the state that lives across iterations of one run.
type Harness struct {
	cfg        *Config
	params     WorkloadParams
	serveExtra []string
	workdir    string
	dbPath     string
	oracle     *Oracle
	base       Baseline
	compaction bool
	reproHint  string // overrides the generic reproduce line (sweep prints its own)
	diskCap    int64  // disk-full: the persisted cap every serve must report and the file must respect
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
	Note     string // mode-specific detail shown on the iteration line
	CheckOut string
	Elapsed  time.Duration
}

// Pass reports whether the iteration found nothing wrong.
func (r *IterResult) Pass() bool { return len(r.Problems) == 0 && (r.Check == nil || r.Check.OK()) }

func newHarness(cfg *Config, params WorkloadParams, serveExtra []string) (*Harness, error) {
	dir := cfg.WorkDir
	if dir == "" {
		d, err := os.MkdirTemp(workBase(), "hydra-*")
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return newHarnessIn(cfg, dir, params, serveExtra)
}

// newHarnessIn builds a harness (own oracle log, own baseline) inside dir.
func newHarnessIn(cfg *Config, dir string, params WorkloadParams, serveExtra []string) (*Harness, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
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
	return runRandomKillOpts(cfg, params, serveExtra, false)
}

// runRandomKillOpts is the shared runner; compaction adds one COMPACT per iteration (SPEC 15.2).
func runRandomKillOpts(cfg *Config, params WorkloadParams, serveExtra []string, compaction bool) int {
	if _, err := os.Stat(cfg.KVDB); err != nil {
		fmt.Fprintf(os.Stderr, "torture: kvdb binary not found at %s (run `make` first, or pass --kvdb)\n", cfg.KVDB)
		return 2
	}
	h, err := newHarness(cfg, params, serveExtra)
	if h != nil {
		h.compaction = compaction
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	if cfg.Verbose && onTmpfs(h.workdir) {
		fmt.Printf("NOTE workdir %s is on tmpfs (RAM): fsync is free there, so runs are far slower and files far larger than on a real disk. Use --workdir on a disk for a realistic run.\n", h.workdir)
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

	var land landing
	for iter := 1; iter <= cfg.Iterations; iter++ {
		r := h.iteration(iter)
		land.add(r)
		if !r.Pass() {
			h.reportFailure(r)
			return 1
		}
		fmt.Println(h.iterationLine(r))
	}
	passed = true
	if cfg.Verbose {
		fmt.Println(land.line())
	}
	fmt.Printf("RESULT PASS %d/%d (seed=%d, mode=%s, %s)\n", cfg.Iterations, cfg.Iterations, cfg.Seed, cfg.Mode, time.Since(start).Round(time.Second))
	return 0
}

func (h *Harness) iterationLine(r *IterResult) string {
	note := ""
	if r.Note != "" {
		note = r.Note + " "
	}
	tail := r.Tail
	if tail == "" {
		tail = "-"
	}
	return fmt.Sprintf("iter %d/%d seed=%d mode=%s kill_ms=%d acked=%d inflight=%d rejected=%d keys=%d open_ms=%d tail=%s %scheck=OK PASS",
		r.Iter, h.cfg.Iterations, h.cfg.Seed, h.cfg.Mode, r.KillMS,
		r.Check.Acked, r.Check.InFlight, r.Check.Rejected, r.Keys, r.OpenMS, tail, note)
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
	if h.params.Profile == ProfileLargeBatches && iter%5 == 0 {
		// A 68 MB streamed batch needs far longer than 250 ms to cross the pipe. Every 5th iteration
		// gets a 10x window (100..2500 ms) so that streamed batches also get ACKed, not only torn.
		r.KillMS *= 10
	}
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
	if h.diskCap > 0 && rf["max_bytes"] != strconv.FormatInt(h.diskCap, 10) {
		problem("serve started without --max-bytes reports max_bytes=%s, but %d was persisted by `kvdb open --max-bytes` (SPEC 4.2, 7.4)", rf["max_bytes"], h.diskCap)
	}
	if want := len(h.base); rf["keys"] != strconv.Itoa(want) {
		problem("serve recovered keys=%s but the previous verified iteration left %d keys", rf["keys"], want)
	}

	// 2-4. lanes, random delay, SIGKILL, drain stdout.
	sess.StartLanes()
	h.runUntilKill(sess, iter, is, r, problem)
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
	h.verifyAfterKill(r, problem)
	return r
}

// verifyAfterKill is steps 5 and 6: open / check / dump, prove that recovery wrote no bytes (C1),
// let the oracle decide, and fold a passing iteration into the baseline.
func (h *Harness) verifyAfterKill(r *IterResult, problem func(string, ...any)) {
	pre, preSize, derr := fileDigest(h.dbPath)
	if derr != nil {
		// Keep going: the comparison below still reports exactly which ACKed writes are gone.
		problem("database file is unreadable after the kill: %v", derr)
	}
	for _, op := range r.Ops {
		if op.State == StateRejected && op.Reason == "INVALID" {
			problem("op %d (%s, %d parts) was answered ERR INVALID, but HYDRA only sends valid requests: a bug in the generator or in the database", op.ID, op.Kind, len(op.Parts))
			break
		}
	}
	if h.diskCap > 0 {
		if sz, err := fileSize(h.dbPath); err == nil && sz > h.diskCap {
			problem("database file is %d bytes, larger than the persisted cap of %d (E1 to E5)", sz, h.diskCap)
		}
	}
	dump := h.observe(r, problem)
	if derr == nil {
		if post, postSize, err := fileDigest(h.dbPath); err != nil {
			problem("cannot re-read database file: %v", err)
		} else if post != pre || postSize != preSize {
			problem("open/check/dump changed the database file (size %d -> %d): recovery must write no bytes (SPEC 5.4)", preSize, postSize)
		}
	}
	if dump == nil {
		return
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

// landing records WHERE the kills landed, as evidence that they land during operations and not between
// them (the problem statement calls "kills that never land during a write" a rigged tool). Every number
// comes from what the recovered file and the oracle show, never from a hook.
type landing struct {
	kills, withInflight, inflightSum int
	durableUnacked                   int // an unACKed transaction was found durable: killed between fsync and ACK
	torn, short                      int // a partial frame / missing end marker on disk: killed mid-write
}

func (l *landing) add(r *IterResult) {
	if r.Check == nil {
		return
	}
	l.kills++
	if r.Check.InFlight > 0 {
		l.withInflight++
		l.inflightSum += r.Check.InFlight
	}
	if r.Check.Surprise != nil {
		l.durableUnacked++
	}
	switch r.Tail {
	case "TORN":
		l.torn++
	case "SHORT_SENTINEL":
		l.short++
	}
}

func (l *landing) line() string {
	if l.kills == 0 {
		return "kill landing: no data"
	}
	return fmt.Sprintf("kill landing over %d kills: %d had operations in flight (avg %.0f per kill); %d left a durable but unACKed transaction (killed between fsync and ACK); %d left a torn tail and %d a short end marker (killed mid-write)",
		l.kills, l.withInflight, float64(l.inflightSum)/float64(l.kills), l.durableUnacked, l.torn, l.short)
}
