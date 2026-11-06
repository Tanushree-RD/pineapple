package main

import (
	"bufio"
	"fmt"
	"github.com/chethan-9817/pineapple/src/internal/envshell"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

// Job is one invocation of bin/torture.
type Job struct {
	Name  string
	Args  []string
	Total int // expected number of progress units, for the bar (0 = unknown)
}

// JobResult is what a finished job leaves behind.
type JobResult struct {
	Job     Job
	OK      bool
	Dur     time.Duration
	Passed  int
	Failed  int
	Verdict string   // the RESULT / COVERAGE line
	Failure []string // the failure block, if any
	Err     error
	Workdir string
}

// runJob runs bin/torture with a live dashboard (or plain lines when out is not a terminal).
// animate turns the live dashboard (progress bar, sparklines redrawn in place) on. It is off: HYDRA prints plain result
// lines, which read the same in a terminal, a log file and a screenshot.
var animate = false

func (e *Env) runJob(out io.Writer, j Job) JobResult {
	args := append([]string(nil), j.Args...)
	args = append(args, "--kvdb", e.kvdb)
	workdir := ""
	if !hasFlag(args, "--workdir") && !hasFlag(args, "--coverage") && !hasFlag(args, "--replay") {
		workdir = filepath.Join(e.runDir, "work-"+slug(j.Name))
		args = append(args, "--workdir", workdir)
	}
	res := JobResult{Job: j, Workdir: workdir}
	if workdir != "" { // the live viewer follows the database this run is killing
		envshell.SetActiveRun(workdir, "torture: "+j.Name)
		defer e.restoreActive()
	}

	cmd := exec.Command(e.torture, args...)
	cmd.Dir = e.runDir
	if e.cwd != "" { // failures/ and coverage-*.json belong in the directory the user works in (`--coverage` and `--replay failures/...` read them from there)
		cmd.Dir = e.cwd
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		res.Err = err
		return res
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.Err = err
		return res
	}
	pw.Close()

	lines := make(chan string, 256)
	go func() {
		br := bufio.NewReaderSize(pr, 1<<20)
		for {
			l, err := br.ReadString('\n')
			if l != "" {
				lines <- l
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	sub := strings.Join(displayArgs(j.Args), " ")
	d := newDash(j.Name, sub, j.Total)
	live := animate && tui.IsTTY()
	width := tui.Width()
	height := 0
	if live {
		tui.HideCursor(out)
		defer tui.ShowCursor(out)
	}
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()

	var failBlock []string
	capturing := false
	var tail []string
loop:
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				break loop
			}
			ev := parseLine(l)
			d.onEvent(ev)
			if ev.kind == evFail {
				capturing = true
			}
			if capturing && len(failBlock) < 60 {
				failBlock = append(failBlock, strings.TrimRight(l, "\r\n"))
			}
			if ev.kind == evResult || ev.kind == evCoverage {
				res.Verdict = ev.verdict
			}
			if ev.kind == evLog && strings.TrimSpace(ev.line) != "" {
				tail = append(tail, ev.line)
				if len(tail) > 6 {
					tail = tail[1:]
				}
			}
			if live {
				height = tui.Redraw(out, height, e.frame(d, width))
			} else if !e.quiet && !isFooter(ev.line) && ev.kind != evResult { // the verdict is printed once, by printJobResult
				fmt.Fprintln(out, plainLine(ev))
			}
		case <-ticker.C:
			if live {
				height = tui.Redraw(out, height, e.frame(d, width))
			}
		}
	}
	waitErr := cmd.Wait()
	d.finished = true
	res.Dur = time.Since(start)
	res.Passed, res.Failed = d.pass, d.fail
	res.Failure = failBlock
	res.OK = waitErr == nil && res.Failed == 0 && (strings.Contains(res.Verdict, "PASS"))
	if res.OK && d.done < d.total { // jobs that print no per-item lines (coverage, replay) still show a full bar
		d.done = d.total
	}
	if live {
		d.finished = true
		tui.Redraw(out, height, e.frame(d, width))
	}
	if !res.OK && len(res.Failure) == 0 {
		res.Failure = tail
	}
	if res.OK && workdir != "" && !e.keep {
		_ = os.RemoveAll(workdir) // the directory this session created for the job, nothing else
	}
	return res
}

// restoreActive points the live viewer back at the user's database (or at nothing) after a torture run.
func (e *Env) restoreActive() {
	if e.db != "" {
		envshell.SetActiveDB(e.db)
	} else {
		envshell.ClearActive()
	}
}

func plainLine(ev event) string {
	switch ev.kind {
	case evIter:
		return fmt.Sprintf("  %s iter %d/%d  kill %sms  %s ACKed  %s keys", tui.OK(), ev.i, ev.n, orDash(ev.kv["kill_ms"]), orDash(ev.kv["acked"]), orDash(ev.kv["keys"]))
	case evCase:
		if ev.pass {
			return "  " + tui.OK() + " " + ev.name
		}
		return "  " + tui.Fail() + " " + ev.name
	}
	return "  " + ev.line
}

// displayArgs hides plumbing flags from the dashboard subtitle.
func displayArgs(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--workdir" || args[i] == "--kvdb" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// printJobResult prints the verdict under a finished dashboard, and the failure block when there is one.
func printJobResult(out io.Writer, r JobResult) {
	if r.Err != nil {
		fmt.Fprintf(out, "%s %s: %v\n", tui.Fail(), r.Job.Name, r.Err)
		return
	}
	if r.OK {
		fmt.Fprintf(out, "%s %s  %s\n", tui.OK(), tui.Green(tui.Bold("PASS")), tui.Gray(strings.TrimSpace(r.Verdict)))
		return
	}
	fmt.Fprintf(out, "%s %s  %s\n", tui.Fail(), tui.Red(tui.Bold("FAIL")), tui.Gray(r.Job.Name))
	if len(r.Failure) > 0 {
		var lines []string
		for _, l := range r.Failure {
			lines = append(lines, tui.Trunc(strings.TrimRight(l, " "), tui.Width()-6))
		}
		for _, l := range tui.Box("what HYDRA found", lines, tui.Width(), tui.Red) {
			fmt.Fprintln(out, l)
		}
	}
	if r.Workdir != "" {
		fmt.Fprintf(out, "%s\n", tui.Gray("   files kept for debugging: "+r.Workdir))
	}
}

// frame renders the dashboard in the style the session asked for.
func (e *Env) frame(d *dash, width int) []string {
	if e.quiet {
		return []string{d.miniLine(width)}
	}
	return d.lines(width)
}

// isFooter hides torture's "files kept" notes: hydra removes the working directory of a passing job itself.
func isFooter(l string) bool {
	return strings.HasPrefix(l, "workdir kept") || strings.HasPrefix(l, "fixture files kept")
}
