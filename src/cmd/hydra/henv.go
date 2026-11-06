package main

// The HYDRA environment: `hydra` (or /hydra from the leapfrog environment) opens a prompt where torture flags are
// typed directly:
//
//	hydra ❯ --iterations 20 --seed 999 --mode large-values
//
// A line that starts with "-" goes straight to bin/torture (with the live dashboard). Words are shortcuts:
// judge, all, scenarios, scenario B5, run <mode>, replay, attack <db>, and the slash commands.

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chethan-9817/pineapple/src/internal/envshell"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

type henv struct {
	e        *Env
	out      io.Writer
	in       *bufio.Reader
	db       string
	port     int
	leapfrog func(db string) error
	hist     *tui.History // up/down arrows recall earlier lines, kept between sessions
}

func hydraEnv(e *Env, db string, out io.Writer, in *bufio.Reader) int {
	h := &henv{e: e, out: out, in: in, db: db, port: 8099, hist: tui.LoadHistory("hydra")}
	h.leapfrog = func(db string) error { return envshell.SwitchTo("leapfrog", db) }
	return h.run()
}

func (h *henv) run() int {
	banner(h.out, true)
	fmt.Fprintln(h.out, "  "+tui.Gray("type torture flags directly, e.g.  --iterations 20 --seed 999 --mode large-values"))
	fmt.Fprintln(h.out, "  "+tui.Gray("/help lists everything · attack <db> kills YOUR data's server and proves it survives · /leapfrog goes back"))
	if h.db != "" {
		fmt.Fprintf(h.out, "  %s target database: %s\n", tui.OK(), tui.Bold(h.db))
	}
	h.e.db = h.db
	if h.db != "" { // keep the data page running in the background (no link, no browser)
		envshell.SetActiveDB(h.db)
		if lf, err := envshell.SiblingBinary("leapfrog"); err == nil {
			_, _ = envshell.StartLive(lf, h.db, h.port) // background only: no link printed, no browser opened
		}
	}
	fmt.Fprintln(h.out)
	for {
		name := "hydra"
		if h.db != "" {
			name += " " + tui.Gray("["+filepath.Base(h.db)+"]")
		}
		line, err := tui.ReadLine(h.out, h.in, tui.Bold(tui.Red(name))+tui.Red(" ❯ "), h.hist)
		if err == tui.ErrInterrupt {
			continue
		}
		line = strings.TrimSpace(line)
		if line != "" && h.exec(line) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(h.out)
			return 0
		}
	}
}

func (h *henv) exec(line string) (quit bool) {
	toks, err := envshell.Tokenize(line)
	if err != nil {
		fmt.Fprintf(h.out, "%s %v\n", tui.Fail(), err)
		return false
	}
	toks = envshell.StripPrefix(toks, "torture", "hydra")
	return h.execToks(toks)
}

// execToks runs one already-split command (also used by `hydra do ...`, which the pineapple prompt calls).
func (h *henv) execToks(toks []string) (quit bool) {
	if len(toks) == 0 {
		return false
	}
	if strings.HasPrefix(toks[0], "-") { // raw torture flags
		h.torture(toks)
		return false
	}
	cmd := strings.TrimPrefix(toks[0], "/")
	args := toks[1:]
	switch cmd {
	case "exit", "quit", "q":
		fmt.Fprintln(h.out, tui.Gray("  session files: "+h.e.runDir))
		return true
	case "help", "h", "?":
		h.help()
	case "leapfrog":
		if h.leapfrog == nil {
			fmt.Fprintln(h.out, "  no need to switch: database commands (put, get, dump ...) work at this prompt")
			return false
		}
		if err := h.leapfrog(h.db); err != nil {
			fmt.Fprintf(h.out, "%s %v\n", tui.Fail(), err)
			return false
		}
		return true
	case "use":
		if len(args) == 0 {
			fmt.Fprintln(h.out, "  target: "+h.db)
		} else {
			h.db = args[0]
			fmt.Fprintf(h.out, "%s target database: %s\n", tui.OK(), tui.Bold(h.db))
		}
	case "attack":
		h.attack(args)
	case "live":
		h.live(args)
	case "judge":
		runJudge(h.e, h.out)
		h.e.quiet = false
	case "all":
		runAll(h.e, h.out)
		h.e.quiet = false
	case "scenarios":
		listScenarios(h.out)
	case "scenario":
		runScenarios(h.e, h.out, args)
	case "run":
		runMode(h.e, h.out, args)
	case "replay":
		runReplay(h.e, h.out, args)
	case "modes":
		for _, m := range modeList {
			fmt.Fprintf(h.out, "  %s  %s\n", tui.Cyan(tui.PadRight(m.name, 15)), tui.Gray(m.desc))
		}
	case "quick":
		h.e.quick = !h.e.quick
		fmt.Fprintf(h.out, "  quick mode: %s\n", onOff(h.e.quick))
	case "clear", "cls":
		fmt.Fprint(h.out, "\x1b[2J\x1b[H")
	default:
		if _, ok := findMode(cmd); ok { // "compaction" alone is the same as "run compaction"
			runMode(h.e, h.out, toks)
			return false
		}
		fmt.Fprintf(h.out, "%s unknown command %q. Try /help.\n", tui.Fail(), cmd)
	}
	return false
}

// torture runs bin/torture with exactly the flags typed, under the live dashboard.
func (h *henv) torture(args []string) {
	if !hasFlag(args, "--seed") && !hasFlag(args, "--coverage") && !hasFlag(args, "--replay") {
		args = append(append([]string(nil), args...), "--seed", h.e.seed)
	}
	name := "random kill -9"
	for i, a := range args {
		if a == "--mode" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	total := iterationsOf(args)
	if total == 0 {
		total = 1
		if m, ok := findMode(name); ok {
			total = m.total
		} else if name == "random kill -9" {
			total = 500
		}
	}
	sessionHeader(h.e, h.out, "torture "+strings.Join(args, " "))
	fmt.Fprintln(h.out)
	r := h.e.runJob(h.out, Job{Name: name, Args: args, Total: total})
	printJobResult(h.out, r)
	fmt.Fprintln(h.out)
}

func (h *henv) attack(args []string) {
	db := h.db
	rounds, seed := 20, time.Now().UnixNano()%100000
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--rounds" && i+1 < len(args):
			rounds, _ = strconv.Atoi(args[i+1])
			i++
		case a == "--seed" && i+1 < len(args):
			seed, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case !strings.HasPrefix(a, "-"):
			db = a
		}
	}
	if db == "" || rounds < 1 {
		fmt.Fprintln(h.out, "usage: attack <db> [--rounds N] [--seed S]   (the database must already exist: /seed students.db in the leapfrog environment)")
		return
	}
	h.db, h.e.db = db, db
	envshell.SetActiveDB(db)
	res := attack(h.e.kvdb, db, rounds, seed, h.out)
	if res.Err != "" {
		fmt.Fprintf(h.out, "%s %s  %s\n", tui.Fail(), tui.Red(tui.Bold("FAIL")), res.Err)
		return
	}
	fmt.Fprintf(h.out, "%s %s  %d kill -9 rounds, %d operations acknowledged; your data is byte-identical to before. (seed %d)\n", tui.OK(), tui.Green(tui.Bold("PASS")), res.Kills, res.Acked, seed)
}

func (h *henv) live(args []string) {
	if len(args) > 0 && args[0] == "stop" {
		envshell.StopLive(h.port)
		fmt.Fprintln(h.out, "  live viewer stopped")
		return
	}
	if h.db == "" {
		fmt.Fprintf(h.out, "%s no database yet: /use students.db\n", tui.Fail())
		return
	}
	lf, err := envshell.SiblingBinary("leapfrog")
	if err != nil {
		fmt.Fprintf(h.out, "%s %v\n", tui.Fail(), err)
		return
	}
	url, err := envshell.StartLive(lf, h.db, h.port)
	if err != nil {
		fmt.Fprintf(h.out, "%s %v\n", tui.Fail(), err)
		return
	}
	fmt.Fprintf(h.out, "%s live view of %s: %s\n", tui.OK(), h.db, tui.Bold(tui.Cyan(url)))
}

func (h *henv) help() {
	rows := [][2]string{
		{"--iterations 20 --seed 999 --mode large-values", "any torture flags, typed directly (no ./bin/torture)"},
		{"attack <db> [--rounds N] [--seed S]", "kill -9 a server that is rewriting YOUR data; prove it comes back byte-identical"},
		{"judge", "the 15-minute judge path (kills, compaction, recovery, fixtures, sweep, micro, coverage)"},
		{"all", "all 32 scenarios with a scoreboard"},
		{"scenarios · scenario B5 A4", "list the 32 scenarios · run some by id"},
		{"run <mode> [flags] · <mode>", "modes: " + modeNames()},
		{"modes", "describe every mode"},
		{"replay <failure-dir>", "replay a saved failure"},
		{"/use <db> · /live [stop]", "pick the database to attack · show it live in the browser"},
		{"quick", "toggle quick mode (a tenth of the iterations)"},
		{"/leapfrog", "switch to the database environment (the live viewer keeps running)"},
		{"/exit", "leave"},
	}
	for _, r := range rows {
		fmt.Fprintf(h.out, "  %s\n      %s\n", tui.Cyan(r[0]), tui.Gray(r[1]))
	}
	fmt.Fprintln(h.out, "\n  "+tui.Gray("a pasted line such as ./bin/torture --iterations 20 --seed 999 works too: the program name is ignored."))
}
