package main

// The 32 scenarios of docs/SPEC.md section 16, each mapped to the exact way it is exercised:
// HYDRA modes (bin/torture), checks that drive bin/kvdb directly, and Go tests.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

// Step is one thing that must pass for a scenario to pass.
type Step struct {
	Kind  string // "torture", "native" or "gotest"
	Name  string
	Args  []string
	Total int
	Nat   string
	Env   []string
}

// Scenario is one row of the SPEC 16 table.
type Scenario struct {
	ID, Group, Title string
	Steps            []Step
}

func tort(name string, total int, args ...string) Step {
	return Step{Kind: "torture", Name: name, Args: args, Total: total}
}
func nat(name string) Step { return Step{Kind: "native", Name: "by hand: " + name, Nat: name} }
func gotest(name, pkg, run string) Step {
	return Step{Kind: "gotest", Name: name, Args: []string{"test", pkg, "-run", run, "-count=1"}}
}

func sweep(cp string, total int) Step {
	return tort("sweep "+cp, total, "--mode", "sweep", "--cp", cp, "--seed", "1")
}

var (
	stDefault = tort("default random kills", 100, "--iterations", "100")
	stFixture = tort("fixtures (31 crash images)", 31, "--mode", "fixtures")
	stMicro   = tort("micro (11 adversarial traces)", 11, "--mode", "micro")
	stBatches = tort("large-batches (10,000-op batches)", 10, "--iterations", "10", "--mode", "large-batches")
	stRecCr   = tort("recovery-crash (kill open mid-recovery)", 10, "--iterations", "10", "--mode", "recovery-crash")
	stCompact = tort("compaction mode", 50, "--iterations", "50", "--mode", "compaction")
	stDiskful = tort("disk-full mode", 50, "--iterations", "50", "--mode", "disk-full")
)

// scenarios is the whole table, in SPEC order.
func scenarios() []Scenario {
	return []Scenario{
		{"A1", "A · single writes", "Nothing new on disk (admission before any write)", []Step{sweep("CP-W01", 6)}},
		{"A2", "A · single writes", "Partial frame: a torn write", []Step{sweep("CP-W03", 6), stFixture}},
		{"A3", "A · single writes", "Complete frame, fsync not yet returned", []Step{sweep("CP-W04", 6)}},
		{"A4", "A · single writes", "fsync returned, no ACK yet: the write must survive", []Step{sweep("CP-W05", 6), sweep("CP-W06", 6)}},
		{"A5", "A · single writes", "An ACKed write survives kill -9", []Step{stDefault, nat("kill")}},
		{"A6", "A · single writes", "A delete in any state (a delete is a one-op batch)", []Step{stDefault, stMicro}},
		{"A7", "A · single writes", "A torn or whole 1 MiB value", []Step{tort("large-values", 20, "--iterations", "20", "--mode", "large-values"), nat("onemib")}},

		{"B1", "B · batches", "3 of 10 ops written: the batch is ignored whole", []Step{sweep("CP-W03", 6), stMicro}},
		{"B2", "B · batches", "All ops written, CRC64 missing", []Step{stFixture}},
		{"B3", "B · batches", "CRC64 written, not fsynced: all or none", []Step{sweep("CP-W04", 6)}},
		{"B4", "B · batches", "An ACKed batch is fully recovered", []Step{stDefault}},
		{"B5", "B · batches", "PUT X then DEL X: the last op wins", []Step{nat("b5"), gotest("go test: batch order", "./tests", "TestBatchB5Ordering|TestEmptyValueVsDelete")}},
		{"B6", "B · batches", "Half of a 10,000-op frame", []Step{stBatches}},

		{"C1", "C · recovery and compaction", "Recovery writes no bytes", []Step{stRecCr, nat("recover")}},
		{"C2", "C · recovery and compaction", "Tail repair is deferred to the first write", []Step{sweep("CP-W02", 6), nat("recover")}},
		{"C3", "C · recovery and compaction", "Old file + partial temp: temp deleted, never used", []Step{stCompact, sweep("CP-C02", 3), sweep("CP-C03", 2), nat("recover")}},
		{"C4", "C · recovery and compaction", "Old or new image at the path, never a mix", []Step{sweep("CP-C04", 2), sweep("CP-C05", 2), nat("compact")}},
		{"C5", "C · recovery and compaction", "New image, no temp file left", []Step{sweep("CP-C05", 2), sweep("CP-C06", 2)}},
		{"C6", "C · recovery and compaction", "A clean close keeps everything", []Step{stFixture}},

		{"D1", "D · concurrency", "Frames from two lanes are serialized whole", []Step{stDefault, gotest("go test: ACK order = commit order", "./tests", "TestACKOrderMatchesCommitOrder")}},
		{"D2", "D · concurrency", "Two frames on the same key: exactly one writer wins", []Step{stDefault}},
		{"D3", "D · concurrency", "A big batch and single writes together", []Step{stBatches}},
		{"D4", "D · concurrency", "No reader ever sees half a batch", []Step{gotest("go test: reader vs 10k-op batches", "./tests", "TestD4ConcurrentReaderNoPartialBatch"), gotest("go test: engine D4", "./src/engine", "TestReadersNeverSeeHalfABatchD4"), nat("lock")}},

		{"E1", "E · disk full", "ERR FULL before any write", []Step{nat("cap"), stDiskful}},
		{"E2", "E · disk full", "A whole batch is admitted or rejected", []Step{nat("e2"), stDiskful}},
		{"E3", "E · disk full", "The old image stays intact", []Step{nat("cap")}},
		{"E4", "E · disk full", "Compaction precheck and write errors (ERR IO, exit 3)", []Step{stFixture, nat("e4")}},
		{"E5", "E · disk full", "Delete at the cap reclaims space", []Step{nat("cap"), stDiskful}},

		{"F1", "F · everything else", "Everything at once: 500 random kills", []Step{tort("default, 500 kills", 500, "--iterations", "500")}},
		{"F2", "F · everything else", "Read-only recovery is idempotent", []Step{stRecCr, nat("recover")}},
		{"F3", "F · everything else", "A checkpoint bounds the scan (1M keys in well under 1.4 s)", []Step{{Kind: "gotest", Name: "1,000,000-key recovery benchmark", Args: []string{"test", "./src/engine", "-run", "BenchF3", "-v", "-count=1"}, Env: []string{"KVDB_BENCH=1"}}}},
		{"F4", "F · everything else", "ONE byte cut from the last frame: the ACKed write survives", []Step{nat("f4"), stFixture, gotest("go test: F4", "./tests", "TestF4OneByteTruncationSentinel")}},
	}
}

func findScenario(id string) (Scenario, bool) {
	for _, s := range scenarios() {
		if strings.EqualFold(s.ID, id) {
			return s, true
		}
	}
	return Scenario{}, false
}

// StepResult is the outcome of one step.
type StepResult struct {
	Step    Step
	OK      bool
	Skip    bool
	Dur     time.Duration
	Detail  string
	Checks  []Check
	Failure []string
	Cached  bool
}

// ScenarioResult is the outcome of one scenario.
type ScenarioResult struct {
	S     Scenario
	Steps []StepResult
	OK    bool
	Dur   time.Duration
}

// cache lets `all` run each distinct torture job once, however many scenarios use it.
type cache map[string]StepResult

func stepKey(st Step, quick bool) string {
	return st.Kind + "|" + st.Nat + "|" + strings.Join(scaleArgs(st.Args, quick), " ") + "|" + strings.Join(st.Env, ",")
}

// runStep executes one step and prints its progress.
func (e *Env) runStep(out io.Writer, st Step, c cache) StepResult {
	key := stepKey(st, e.quick)
	if c != nil {
		if r, ok := c[key]; ok {
			r.Cached = true
			return r
		}
	}
	start := time.Now()
	r := StepResult{Step: st}
	switch st.Kind {
	case "torture":
		args := scaleArgs(st.Args, e.quick)
		total := st.Total
		if n := iterationsOf(args); n > 0 {
			total = n
		}
		if e.seed != "" && !hasFlag(args, "--seed") {
			args = append(args, "--seed", e.seed)
		}
		jr := e.runJob(out, Job{Name: st.Name, Args: args, Total: total})
		r.OK, r.Failure, r.Detail = jr.OK, jr.Failure, strings.TrimSpace(jr.Verdict)
		if jr.Err != nil {
			r.Detail = jr.Err.Error()
		}
		if !e.quiet {
			printJobResult(out, jr)
		}
	case "native":
		r.Checks = e.runNative(st.Nat)
		r.OK = true
		for _, ck := range r.Checks {
			if !ck.OK {
				r.OK = false
			}
		}
		if !e.quiet {
			printChecks(out, st.Name, r.Checks)
		}
	case "gotest":
		r = e.runGoTest(out, st)
	}
	r.Dur = time.Since(start)
	if c != nil && !r.Skip {
		c[key] = r
	}
	return r
}

func (e *Env) runGoTest(out io.Writer, st Step) StepResult {
	r := StepResult{Step: st}
	if e.root == "" || !haveGo() {
		r.OK, r.Skip, r.Detail = true, true, "skipped: needs the Go toolchain and the repository checkout"
		if !e.quiet {
			fmt.Fprintf(out, "  %s %s  %s\n", tui.Warn(), st.Name, tui.Gray(r.Detail))
		}
		return r
	}
	cmd := exec.Command("go", st.Args...)
	cmd.Dir = e.root
	cmd.Env = append(os.Environ(), st.Env...)
	if len(st.Env) > 0 { // benchmarks write big files: keep them on a real disk, not RAM
		cmd.Env = append(cmd.Env, "TMPDIR="+e.runDir)
	}
	if !e.quiet {
		fmt.Fprintf(out, "  %s %s ", tui.Cyan("▸"), st.Name)
	}
	b, err := cmd.CombinedOutput()
	text := string(b)
	r.OK = err == nil
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "F3 with checkpoint") || strings.Contains(l, "F3 full scan") {
			r.Detail += strings.TrimSpace(l[strings.Index(l, "F3"):]) + "  "
		}
	}
	if r.Detail == "" {
		r.Detail = lastLine(text)
	}
	if !r.OK {
		r.Failure = strings.Split(strings.TrimSpace(text), "\n")
		if len(r.Failure) > 25 {
			r.Failure = r.Failure[len(r.Failure)-25:]
		}
	}
	if !e.quiet {
		if r.OK {
			fmt.Fprintf(out, "%s %s\n", tui.OK(), tui.Gray(tui.Trunc(r.Detail, 70)))
		} else {
			fmt.Fprintf(out, "%s\n", tui.Fail())
		}
	}
	return r
}

func printChecks(out io.Writer, title string, cs []Check) {
	fmt.Fprintf(out, "  %s %s\n", tui.Cyan("▸"), tui.Bold(title))
	for _, c := range cs {
		mark := tui.OK()
		switch {
		case c.Skip:
			mark = tui.Warn()
		case !c.OK:
			mark = tui.Fail()
		}
		line := fmt.Sprintf("    %s %s", mark, c.Desc)
		if c.Detail != "" {
			line += "  " + tui.Gray(tui.Trunc(c.Detail, 46))
		}
		fmt.Fprintln(out, line)
	}
}

// runScenario runs every step of a scenario.
func (e *Env) runScenario(out io.Writer, sc Scenario, c cache) ScenarioResult {
	start := time.Now()
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s %s  %s\n", tui.Bold(tui.Gradient(sc.ID, 39, 45)), tui.Bold(sc.Title), tui.Gray("["+sc.Group+"]"))
	res := ScenarioResult{S: sc, OK: true}
	for _, st := range sc.Steps {
		sr := e.runStep(out, st, c)
		res.Steps = append(res.Steps, sr)
		if !sr.OK {
			res.OK = false
		}
		if e.quiet && !(st.Kind == "torture" && tui.IsTTY() && !sr.Cached) {
			printStepLine(out, sr)
		}
	}
	res.Dur = time.Since(start)
	return res
}

// scoreboard prints one row per scenario.
func scoreboard(out io.Writer, rs []ScenarioResult, quick bool) bool {
	width := tui.Width()
	var lines []string
	pass, skip := 0, 0
	for _, r := range rs {
		mark := tui.OK()
		if !r.OK {
			mark = tui.Fail()
		} else {
			pass++
		}
		var parts []string
		for _, s := range r.Steps {
			switch {
			case s.Skip:
				skip++
				parts = append(parts, tui.Yellow("skip"))
			case s.OK:
				parts = append(parts, tui.Green("✔"))
			default:
				parts = append(parts, tui.Red("✘"))
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s %s  %s", mark, tui.Bold(r.S.ID), tui.PadRight(tui.Trunc(r.S.Title, width-30), width-30), strings.Join(parts, " ")))
	}
	title := fmt.Sprintf("scoreboard: %d of %d scenarios pass", pass, len(rs))
	border := tui.Green
	if pass != len(rs) {
		border = tui.Red
	}
	fmt.Fprintln(out)
	for _, l := range tui.Box(title, lines, width, border) {
		fmt.Fprintln(out, l)
	}
	if quick {
		fmt.Fprintln(out, tui.Gray("  quick mode: a tenth of the iterations. Run without --quick for the full counts."))
	}
	if skip > 0 {
		fmt.Fprintln(out, tui.Gray(fmt.Sprintf("  %d step(s) skipped (no Go toolchain here).", skip)))
	}
	return pass == len(rs)
}

// printStepLine is the one-line summary used in compact mode.
func printStepLine(out io.Writer, r StepResult) {
	mark := tui.OK()
	switch {
	case r.Skip:
		mark = tui.Warn()
	case !r.OK:
		mark = tui.Fail()
	}
	detail := r.Detail
	if r.Step.Kind == "native" {
		n := 0
		for _, c := range r.Checks {
			if c.OK {
				n++
			}
		}
		detail = fmt.Sprintf("%d of %d checks held", n, len(r.Checks))
	}
	if r.Cached {
		detail = "same run as above · " + detail
	}
	fmt.Fprintf(out, "  %s %s  %s\n", mark, tui.PadRight(tui.Trunc(r.Step.Name, 40), 40), tui.Gray(tui.Trunc(detail, tui.Width()-50)))
	if !r.OK && !r.Skip {
		for _, c := range r.Checks {
			if !c.OK {
				fmt.Fprintf(out, "      %s %s  %s\n", tui.Fail(), c.Desc, tui.Gray(c.Detail))
			}
		}
		for i, l := range r.Failure {
			if i >= 12 {
				break
			}
			fmt.Fprintf(out, "      %s\n", tui.Red(tui.Trunc(strings.TrimRight(l, " "), tui.Width()-10)))
		}
	}
}
