// Command hydra is the control room for HYDRA, the crash-torture tool of LEAPFROG-R.
//
// It does not reimplement any testing: it runs the real bin/torture and bin/kvdb and shows what they do,
// with a live dashboard, an arrow-key menu, and a runner for each of the 32 scenarios of docs/SPEC.md
// section 16.
//
//	hydra                              interactive control room
//	hydra judge                        the 15-minute judge path, start to finish
//	hydra scenarios                    list the 32 scenarios
//	hydra scenario B5 A4 F4            run scenarios by id
//	hydra all                          run every scenario and print a scoreboard
//	hydra run <mode> [torture flags]   run one mode with the live dashboard
//	hydra replay <failures/dir>        re-run the check on a saved failure
//
// global flags (before the command): --quick  --seed N  --keep  --bin DIR  --no-color
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, bufio.NewReader(os.Stdin))) }

type modeInfo struct {
	name  string
	args  []string
	total int
	desc  string
}

var modeList = []modeInfo{
	{"default", []string{"--iterations", "100"}, 100, "random SIGKILL against a live server, verified by the oracle (the judge's mode)"},
	{"sweep", []string{"--mode", "sweep"}, 62, "kill at EXACTLY every named crash point, with torn writes"},
	{"fixtures", []string{"--mode", "fixtures"}, 31, "31 crafted crash images with exact expected results"},
	{"micro", []string{"--mode", "micro"}, 11, "11 tiny adversarial traces, killed at every crash point they pass"},
	{"compaction", []string{"--iterations", "50", "--mode", "compaction"}, 50, "COMPACT at a random moment; the file must really shrink"},
	{"recovery-crash", []string{"--iterations", "10", "--mode", "recovery-crash"}, 10, "kill `kvdb open` in the middle of recovery"},
	{"disk-full", []string{"--iterations", "50", "--mode", "disk-full"}, 50, "a persisted size cap: full, delete, put again"},
	{"large-values", []string{"--iterations", "20", "--mode", "large-values"}, 20, "values of 256 KiB to 1 MiB"},
	{"large-batches", []string{"--iterations", "10", "--mode", "large-batches"}, 10, "10,000-op batches, buffered and streamed (writes lots of data)"},
	{"coverage", []string{"--coverage"}, 1, "which persistence transitions have been hit (run sweep, micro, fixtures first)"},
}

func findMode(name string) (modeInfo, bool) {
	for _, m := range modeList {
		if m.name == name {
			return m, true
		}
	}
	return modeInfo{}, false
}

func banner(w io.Writer, big bool) {
	fmt.Fprintln(w)
	if big && tui.Width() >= 60 {
		for _, l := range tui.Banner("HYDRA", 196, 220) {
			fmt.Fprintln(w, "  "+l)
		}
	} else {
		fmt.Fprintln(w, "  "+tui.Bold(tui.Gradient("HYDRA", 196, 220)))
	}
	fmt.Fprintln(w, "  "+tui.Gray("crash torture for LEAPFROG-R · kills it, then checks every answer against its own record"))
	fmt.Fprintln(w)
}

func usage(w io.Writer) {
	banner(w, false)
	rows := [][2]string{
		{"hydra", "interactive control room (arrow keys)"},
		{"hydra judge", "the 15-minute judge path: kills, compaction, recovery, fixtures, sweep, micro, coverage"},
		{"hydra scenarios", "list the 32 scenarios of SPEC section 16"},
		{"hydra scenario B5 A4 F4", "run scenarios by id, with the exact checks behind each"},
		{"hydra all", "run all 32 scenarios, print a scoreboard"},
		{"hydra run <mode> [flags]", "run one mode live. modes: " + modeNames()},
		{"hydra replay <failures/dir>", "re-run the check on a saved failure"},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %s  %s\n", tui.Cyan(tui.PadRight(r[0], 28)), tui.Gray(r[1]))
	}
	fmt.Fprintln(w, "\n  global flags (before the command):")
	fmt.Fprintln(w, "    --quick      a tenth of the iterations: minutes instead of an hour")
	fmt.Fprintln(w, "    --seed N     use this seed everywhere (default: a fresh one, printed, so a run can be repeated)")
	fmt.Fprintln(w, "    --keep       keep every working directory (default: delete the ones of passing runs)")
	fmt.Fprintln(w, "    --bin DIR    where bin/torture and bin/kvdb are (default: next to this program, or ./bin)")
	fmt.Fprintln(w, "    --no-color   plain output (NO_COLOR=1 works too)")
}

func modeNames() string {
	var n []string
	for _, m := range modeList {
		n = append(n, m.name)
	}
	return strings.Join(n, ", ")
}

func run(args []string, out io.Writer, in *bufio.Reader) int {
	binDir, seed, db := "", "", ""
	quick, keep := false, false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch a := args[0]; a {
		case "--quick":
			quick = true
		case "--keep":
			keep = true
		case "--no-color":
			tui.SetColor(false)
		case "--bin", "--seed", "--db":
			if len(args) < 2 {
				fmt.Fprintf(out, "%s needs a value\n", a)
				return 2
			}
			switch a {
			case "--bin":
				binDir = args[1]
			case "--db":
				db = args[1]
			default:
				seed = args[1]
			}
			args = args[1:]
		case "-h", "--help":
			usage(out)
			return 0
		default:
			fmt.Fprintf(out, "unknown flag %s\n\n", a)
			usage(out)
			return 2
		}
		args = args[1:]
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h") {
		usage(out)
		return 0
	}
	if len(args) > 0 && args[0] == "scenarios" {
		listScenarios(out)
		return 0
	}
	e, err := discover(binDir)
	if err != nil {
		fmt.Fprintf(out, "%s %v\n", tui.Fail(), err)
		return 1
	}
	e.quick, e.keep = quick, keep
	e.seed = seed
	if e.seed == "" {
		e.seed = strconv.Itoa(int(time.Now().UnixNano()/1e6) % 100000)
	}
	if len(args) == 0 {
		return hydraEnv(e, db, out, in)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "menu":
		return controlRoom(e, out, in)
	case "env":
		return hydraEnv(e, db, out, in)
	case "attack":
		res := attack(e.kvdb, firstOr(rest, db), 20, time.Now().UnixNano()%100000, out)
		if res.Err != "" {
			fmt.Fprintf(out, "%s %s  %s\n", tui.Fail(), tui.Red(tui.Bold("FAIL")), res.Err)
			return 1
		}
		fmt.Fprintf(out, "%s %s  %d kill -9 rounds, data byte-identical\n", tui.OK(), tui.Green(tui.Bold("PASS")), res.Kills)
		return 0
	case "judge":
		return runJudge(e, out)
	case "all":
		return runAll(e, out)
	case "scenario":
		return runScenarios(e, out, rest)
	case "run":
		return runMode(e, out, rest)
	case "replay":
		return runReplay(e, out, rest)
	}
	fmt.Fprintf(out, "unknown command %q\n\n", cmd)
	usage(out)
	return 2
}

func sessionHeader(e *Env, out io.Writer, what string) {
	banner(out, false)
	mode := "full counts"
	if e.quick {
		mode = "quick (a tenth of the iterations)"
	}
	fmt.Fprintf(out, "  %s  %s · seed %s · %s\n", tui.Bold(what), tui.Gray("session files in "+e.runDir), tui.Cyan(e.seed), tui.Gray(mode))
}

func listScenarios(out io.Writer) {
	banner(out, false)
	group := ""
	for _, s := range scenarios() {
		if s.Group != group {
			group = s.Group
			fmt.Fprintf(out, "\n  %s\n", tui.Bold(tui.Magenta(group)))
		}
		var how []string
		for _, st := range s.Steps {
			switch st.Kind {
			case "torture":
				how = append(how, "torture")
			case "native":
				how = append(how, "by hand")
			default:
				how = append(how, "go test")
			}
		}
		fmt.Fprintf(out, "   %s  %s  %s\n", tui.Bold(tui.Cyan(s.ID)), tui.PadRight(tui.Trunc(s.Title, tui.Width()-34), tui.Width()-34), tui.Gray(strings.Join(uniq(how), ", ")))
	}
	fmt.Fprintf(out, "\n  %s\n", tui.Gray("run one with: hydra scenario B5   ·   everything: hydra all --quick"))
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func runScenarios(e *Env, out io.Writer, ids []string) int {
	if len(ids) == 0 {
		fmt.Fprintln(out, "usage: hydra scenario <ID>...   (hydra scenarios lists them)")
		return 2
	}
	sessionHeader(e, out, "scenarios")
	var rs []ScenarioResult
	for _, id := range ids {
		sc, ok := findScenario(id)
		if !ok {
			fmt.Fprintf(out, "%s no scenario %q (A1 to F4)\n", tui.Fail(), id)
			return 2
		}
		rs = append(rs, e.runScenario(out, sc, nil))
	}
	for _, r := range rs {
		mark, word := tui.OK(), tui.Green("PASS")
		if !r.OK {
			mark, word = tui.Fail(), tui.Red("FAIL")
		}
		fmt.Fprintf(out, "\n%s %s %s %s\n", mark, tui.Bold(r.S.ID), word, tui.Gray(tui.Dur(r.Dur)))
	}
	for _, r := range rs {
		if !r.OK {
			return 1
		}
	}
	return 0
}

func runAll(e *Env, out io.Writer) int {
	e.quiet = true
	sessionHeader(e, out, "all 32 scenarios")
	c := cache{}
	var rs []ScenarioResult
	start := time.Now()
	for _, sc := range scenarios() {
		rs = append(rs, e.runScenario(out, sc, c))
	}
	ok := scoreboard(out, rs, e.quick)
	fmt.Fprintf(out, "  %s\n", tui.Gray("total "+tui.Dur(time.Since(start))))
	if ok {
		return 0
	}
	return 1
}

func runMode(e *Env, out io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(out, "usage: hydra run <mode> [torture flags]\nmodes: %s\n", modeNames())
		return 2
	}
	m, ok := findMode(args[0])
	if !ok {
		fmt.Fprintf(out, "%s unknown mode %q. modes: %s\n", tui.Fail(), args[0], modeNames())
		return 2
	}
	jobArgs := scaleArgs(mergeArgs(m.args, args[1:]), e.quick)
	total := m.total
	if n := iterationsOf(jobArgs); n > 0 {
		total = n
	}
	if !hasFlag(jobArgs, "--seed") && m.name != "coverage" {
		jobArgs = append(jobArgs, "--seed", e.seed)
	}
	sessionHeader(e, out, "run "+m.name)
	fmt.Fprintln(out)
	r := e.runJob(out, Job{Name: m.name, Args: jobArgs, Total: total})
	printJobResult(out, r)
	if r.OK {
		return 0
	}
	return 1
}

func runReplay(e *Env, out io.Writer, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(out, "usage: hydra replay <failures/seed-iter directory>")
		return 2
	}
	sessionHeader(e, out, "replay")
	r := e.runJob(out, Job{Name: "replay " + args[0], Args: []string{"--replay", args[0]}, Total: 1})
	printJobResult(out, r)
	// A replay of a real failure is EXPECTED to fail; say so.
	if !r.OK {
		fmt.Fprintln(out, tui.Gray("  (a replay that fails again means the saved crash image still violates the rules)"))
	}
	if r.OK {
		return 0
	}
	return 1
}

// runJudge is the 15-minute path of SPEC section 18 in one command.
func runJudge(e *Env, out io.Writer) int {
	e.quiet = true
	sessionHeader(e, out, "the judge path")
	fmt.Fprintln(out, "  "+tui.Gray("SPEC 18: build, random kills, compaction and recovery, a manual kill test, fixtures, the crash points"))
	fmt.Fprintln(out)
	steps := []Step{
		tort("random kills (default mode)", 200, "--iterations", "200"),
		tort("compaction under kills", 50, "--iterations", "50", "--mode", "compaction"),
		tort("recovery killed mid-way", 10, "--iterations", "10", "--mode", "recovery-crash"),
		nat("kill"),
		tort("fixtures (31 crash images)", 31, "--mode", "fixtures"),
		tort("sweep (every crash point)", 62, "--mode", "sweep"),
		tort("micro (11 adversarial traces)", 11, "--mode", "micro"),
		tort("coverage (24 transitions)", 1, "--coverage"),
	}
	steps[3].Name = "manual kill test (3 rounds of kill -9)"
	var rows []string
	ok := true
	start := time.Now()
	for _, st := range steps {
		sr := e.runStep(out, st, nil)
		if !(st.Kind == "torture" && tui.IsTTY()) {
			printStepLine(out, sr)
		}
		mark := tui.OK()
		if !sr.OK {
			ok, mark = false, tui.Fail()
		}
		rows = append(rows, fmt.Sprintf("%s %s %s", mark, tui.PadRight(st.Name, 40), tui.Gray(tui.Dur(sr.Dur))))
	}
	fmt.Fprintln(out)
	title, border := "judge path: everything passed", tui.Green
	if !ok {
		title, border = "judge path: SOMETHING FAILED", tui.Red
	}
	for _, l := range tui.Box(title, rows, tui.Width(), border) {
		fmt.Fprintln(out, l)
	}
	fmt.Fprintf(out, "  %s\n", tui.Gray("total "+tui.Dur(time.Since(start))))
	next := []string{
		"Crash-here questions: " + tui.Cyan("grep -n 'CP-' src/engine/*.go") + " then read the row in BREAKING.md",
		"See the bytes:        " + tui.Cyan("bin/leapfrog inspect <db> -v") + "    Watch a kill:  " + tui.Cyan("bin/leapfrog demo"),
		"TTL extension:        a prepared design (docs/SPEC.md section 19), not code: say so",
	}
	for _, l := range tui.Box("what the judges ask next", next, tui.Width(), tui.Magenta) {
		fmt.Fprintln(out, l)
	}
	if ok {
		return 0
	}
	return 1
}

// controlRoom is the interactive menu.
func controlRoom(e *Env, out io.Writer, in *bufio.Reader) int {
	banner(out, true)
	for {
		items := []tui.MenuItem{
			{Label: "Judge path", Hint: "the 15-minute demo flow, start to finish"},
			{Label: "Run a scenario", Hint: "any of the 32, A1 to F4"},
			{Label: "Run a mode", Hint: modeNames()},
			{Label: "Run everything", Hint: "all 32 scenarios with a scoreboard"},
			{Label: "Quick mode: " + onOff(e.quick), Hint: "a tenth of the iterations (toggle)"},
			{Label: "Quit", Hint: ""},
		}
		idx, ok := tui.Menu(out, in, "", items)
		if !ok || idx == 5 {
			fmt.Fprintln(out, tui.Gray("\n  session files: "+e.runDir))
			return 0
		}
		fmt.Fprintln(out)
		switch idx {
		case 0:
			runJudge(e, out)
			e.quiet = false
		case 1:
			if id, ok := pickScenario(out, in); ok {
				runScenarios(e, out, []string{id})
			}
		case 2:
			if m, ok := pickMode(out, in); ok {
				runMode(e, out, []string{m})
			}
		case 3:
			runAll(e, out)
			e.quiet = false
		case 4:
			e.quick = !e.quick
		}
		fmt.Fprintln(out)
	}
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "off"
}

func pickScenario(out io.Writer, in *bufio.Reader) (string, bool) {
	all := scenarios()
	var groups []string
	seen := map[string]bool{}
	for _, s := range all {
		if !seen[s.Group] {
			seen[s.Group] = true
			groups = append(groups, s.Group)
		}
	}
	var gi []tui.MenuItem
	for _, g := range groups {
		gi = append(gi, tui.MenuItem{Label: g})
	}
	g, ok := tui.Menu(out, in, tui.Bold("  group"), gi)
	if !ok {
		return "", false
	}
	var items []tui.MenuItem
	var ids []string
	for _, s := range all {
		if s.Group == groups[g] {
			items = append(items, tui.MenuItem{Label: s.ID, Hint: s.Title})
			ids = append(ids, s.ID)
		}
	}
	i, ok := tui.Menu(out, in, tui.Bold("  scenario"), items)
	if !ok {
		return "", false
	}
	return ids[i], true
}

func pickMode(out io.Writer, in *bufio.Reader) (string, bool) {
	var items []tui.MenuItem
	for _, m := range modeList {
		items = append(items, tui.MenuItem{Label: m.name, Hint: m.desc})
	}
	i, ok := tui.Menu(out, in, tui.Bold("  mode"), items)
	if !ok {
		return "", false
	}
	return modeList[i].name, true
}

// mergeArgs applies the user's flags on top of a mode's defaults: a flag given twice keeps only the user's value.
func mergeArgs(base, user []string) []string {
	out := append([]string(nil), base...)
	for i := 0; i < len(user); i++ {
		a := user[i]
		if !strings.HasPrefix(a, "--") {
			out = append(out, a)
			continue
		}
		hasVal := i+1 < len(user) && !strings.HasPrefix(user[i+1], "--")
		replaced := false
		for j := 0; j < len(out); j++ {
			if out[j] == a {
				if hasVal && j+1 < len(out) && !strings.HasPrefix(out[j+1], "--") {
					out[j+1] = user[i+1]
				}
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, a)
			if hasVal {
				out = append(out, user[i+1])
			}
		}
		if hasVal {
			i++
		}
	}
	return out
}

func firstOr(a []string, d string) string {
	if len(a) > 0 {
		return a[0]
	}
	return d
}
