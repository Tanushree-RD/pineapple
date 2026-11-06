// Command pineapple is the whole demo at one prompt: the database commands (put, get, del, batch, dump, check, open,
// serve) AND the torture tool, without switching anywhere:
//
//	pineapple ❯ put demo.db city Mumbai
//	pineapple ❯ --iterations 200 --seed 12345
//	pineapple ❯ --mode compaction --seed 7
//
// Database commands run the real bin/kvdb. A line that starts with "-", or a word such as attack, judge, scenario,
// runs the HYDRA torture tool (bin/hydra, which drives the real bin/torture). The live viewer (/live) starts empty and
// shows only what is actually happening: the database you are working on, or the one a torture run is killing.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chethan-9817/pineapple/src/internal/envshell"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

const port = 8099

var kvdbCommands = map[string]bool{"open": true, "put": true, "get": true, "del": true, "batch": true, "dump": true, "check": true, "serve": true}
var hydraWords = map[string]bool{"failure": true, "success": true, "attack": true, "judge": true, "all": true, "scenarios": true, "scenario": true, "run": true, "replay": true, "modes": true, "quick": true,
	"default": true, "sweep": true, "fixtures": true, "micro": true, "compaction": true, "recovery-crash": true, "disk-full": true, "large-values": true, "large-batches": true, "coverage": true}

// tortureFlags are the flag names of bin/torture; typed bare at the start of a line they get their dashes.
var tortureFlags = map[string]bool{"iterations": true, "seed": true, "mode": true, "writers": true, "max-bytes": true, "cp": true, "verbose": true}

type app struct {
	out  io.Writer
	in   *bufio.Reader
	db   string
	hist *tui.History
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, bufio.NewReader(os.Stdin))) }

func run(args []string, out io.Writer, in *bufio.Reader) int {
	a := &app{out: out, in: in, hist: tui.LoadHistory("pineapple")}
	if db, _ := envshell.ParseDBFlag(args); db != "" {
		a.db = db
	}
	envshell.ClearActive() // the viewer starts empty
	envshell.ClearVerdict()
	a.startViewer()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+tui.Bold(tui.Gradient("PINEAPPLE", 39, 220))+"  "+tui.Gray("LEAPFROG-R database + HYDRA torture, one prompt"))
	fmt.Fprintln(out, "  "+tui.Gray("every command below works as typed · ↑ recalls earlier lines · /help shows this list again"))
	a.help()
	for {
		name := "pineapple"
		if a.db != "" {
			name += " " + tui.Gray("["+filepath.Base(a.db)+"]")
		}
		line, err := tui.ReadLine(out, in, tui.Bold(tui.Yellow(name))+tui.Yellow(" ❯ "), a.hist)
		if err == tui.ErrInterrupt {
			continue
		}
		if line = strings.TrimSpace(line); line != "" && a.exec(line) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(out)
			return 0
		}
	}
}

// startViewer runs the data page in the background (quietly: no link printed, no browser opened).
func (a *app) startViewer() {
	if lf, err := envshell.SiblingBinary("leapfrog"); err == nil {
		fallback := filepath.Join(os.TempDir(), "pineapple-nothing-yet.db") // does not exist: the page shows 0 keys
		_, _ = envshell.StartLive(lf, fallback, port)
	}
}

// exec runs one line; it returns true to leave.
func (a *app) exec(line string) bool {
	toks, err := envshell.Tokenize(line)
	if err != nil {
		fmt.Fprintf(a.out, "%s %v\n", tui.Fail(), err)
		return false
	}
	toks = envshell.StripPrefix(toks, "kvdb", "torture", "hydra", "leapfrog")
	if len(toks) == 0 {
		return false
	}
	if toks[0] != "failure" && toks[0] != "success" {
		envshell.ClearVerdict() // the red/green banner on the page belongs to the demo that set it
	}
	if tortureFlags[toks[0]] { // "iterations 200 --seed 1" without the dashes is read as "--iterations 200 --seed 1"
		toks[0] = "--" + toks[0]
	}
	if strings.HasPrefix(toks[0], "-") { // torture flags, typed directly
		a.hydra(toks)
		return false
	}
	cmd := strings.TrimPrefix(toks[0], "/")
	args := toks[1:]
	switch {
	case cmd == "exit" || cmd == "quit" || cmd == "q":
		return true
	case cmd == "help" || cmd == "h" || cmd == "?":
		a.help()
	case cmd == "use":
		if len(args) > 0 {
			a.setDB(args[0])
			fmt.Fprintf(a.out, "%s using %s\n", tui.OK(), tui.Bold(a.db))
		} else {
			fmt.Fprintln(a.out, "  database: "+orNone(a.db))
		}
	case cmd == "live":
		a.live(args)
	case cmd == "clear" || cmd == "cls":
		fmt.Fprint(a.out, "\x1b[2J\x1b[H")
	case cmd == "leapfrog" || cmd == "hydra":
		fmt.Fprintln(a.out, "  no need to switch: database commands and torture flags both work at this prompt (/help)")
	case kvdbCommands[cmd]:
		a.kvdb(cmd, args)
	case cmd == "compact" || cmd == "count" || cmd == "inspect":
		a.extra(cmd, args)
	case hydraWords[cmd]:
		a.hydra(toks)
	default:
		fmt.Fprintf(a.out, "%s unknown command %q. Try /help.\n", tui.Fail(), toks[0])
	}
	return false
}

func orNone(s string) string {
	if s == "" {
		return "none yet (name one: put demo.db k v)"
	}
	return s
}

func (a *app) setDB(db string) {
	a.db = db
	envshell.SetActiveDB(db)
}

func looksLikePath(s string) bool {
	if strings.HasSuffix(s, ".db") {
		return true
	}
	st, err := os.Stat(s)
	return err == nil && !st.IsDir()
}

// kvdb runs a real bin/kvdb command; once a database is chosen its path may be left out.
func (a *app) kvdb(cmd string, args []string) {
	if len(args) == 0 || !looksLikePath(args[0]) {
		if a.db == "" {
			fmt.Fprintf(a.out, "%s name the database: %s demo.db ...\n", tui.Fail(), cmd)
			return
		}
		args = append([]string{a.db}, args...)
	}
	a.setDB(args[0])
	a.child("kvdb", append([]string{cmd}, args...), nil)
}

// extra: compact, count and inspect on the current (or named) database.
func (a *app) extra(cmd string, args []string) {
	db := a.db
	if len(args) > 0 && looksLikePath(args[0]) {
		db = args[0]
	}
	if db == "" {
		fmt.Fprintf(a.out, "%s name the database: %s demo.db\n", tui.Fail(), cmd)
		return
	}
	a.setDB(db)
	switch cmd {
	case "compact":
		a.child("kvdb", []string{"serve", db}, strings.NewReader("compact\n"))
	case "count":
		a.child("kvdb", []string{"open", db}, nil)
	case "inspect":
		a.child("leapfrog", []string{"inspect", db}, nil)
	}
}

// hydra runs one torture command through bin/hydra (which drives bin/torture) and prints its plain results.
func (a *app) hydra(toks []string) {
	args := []string{}
	if a.db != "" {
		args = append(args, "--db", a.db)
	}
	a.child("hydra", append(append(args, "do"), toks...), nil)
}

// child runs a sibling program with the terminal attached; the terminal is in normal mode here.
func (a *app) child(name string, args []string, stdin io.Reader) {
	bin, err := envshell.SiblingBinary(name)
	if err != nil {
		fmt.Fprintf(a.out, "%s %v\n", tui.Fail(), err)
		return
	}
	c := exec.Command(bin, args...)
	if stdin != nil {
		c.Stdin = stdin
	} else {
		c.Stdin = os.Stdin
	}
	c.Stdout, c.Stderr = a.out, a.out
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			fmt.Fprintln(a.out, tui.Gray(fmt.Sprintf("  (exit code %d)", ee.ExitCode())))
		} else {
			fmt.Fprintf(a.out, "%s %v\n", tui.Fail(), err)
		}
	}
}

func (a *app) live(args []string) {
	if len(args) > 0 && args[0] == "stop" {
		envshell.StopLive(port)
		fmt.Fprintln(a.out, "  live page stopped")
		return
	}
	a.startViewer()
	fmt.Fprintf(a.out, "  live page: %s  %s\n", tui.Bold(tui.Cyan(envshell.LiveURL(port))), tui.Gray("(open it in a browser; it shows what is happening now)"))
}

// commandList is the grouped command reference shown when the prompt starts and by /help: one aligned
// "command    what it does" line per command, the way the commands are written in the README.
var commandList = []struct {
	group string
	rows  [][2]string
}{
	{"DATABASE", [][2]string{
		{"open demo.db", "create, recover, print key count"},
		{"put demo.db city Mumbai", "store a value (overwrites)"},
		{"get demo.db city", "read it back (NOTFOUND if missing)"},
		{"del demo.db city", "delete it (missing is fine)"},
		{"batch demo.db ops.txt", "file of put/del = ONE atomic batch"},
		{"dump demo.db", "every key and value, sorted"},
		{"check demo.db", "verify the file: OK or a reason"},
		{"serve demo.db", "server, ACK per durable op"},
		{"compact", "rewrite live keys; file shrinks"},
		{"inspect demo.db -v", "decode file, label frame 1 bytes"},
		{"/use demo.db", "pick a db, then omit the name"},
	}},
	{"TORTURE (random kill -9)", [][2]string{
		{"--iterations 200 --seed 12345", "random kills, every answer checked"},
		{"--iterations 100 --seed 12345 --mode compaction", "kill the file rewrite midway"},
		{"--iterations 30 --seed 12345 --mode recovery-crash", "kill it while it recovers"},
		{"--iterations 10 --seed 999 --mode large-batches", "10,000-op batch: all or nothing"},
		{"--iterations 20 --seed 999 --mode large-values", "1 MiB values never torn"},
		{"--iterations 50 --seed 12345 --mode disk-full", "size cap: ERR FULL leaves no trace"},
	}},
	{"TORTURE (exact crash points)", [][2]string{
		{"--mode sweep --seed 1", "dies at every named crash point"},
		{"--mode fixtures", "31 damaged files all open"},
		{"--mode micro --seed 1", "11 tiny tricky traces"},
		{"--coverage", "proof of coverage (after the 3 above)"},
		{"--replay failures/12345-87", "re-check a saved failure"},
	}},
	{"PROVE IT ON YOUR DATA, AND THE FULL RUN", [][2]string{
		{"attack demo.db --rounds 10", "kill -9 while rewriting YOUR data"},
		{"judge", "the whole 15-minute path"},
		{"scenarios", "list the 32 scenarios"},
		{"scenario B5 A4 F4", "run just those scenarios"},
		{"all --quick", "all 32, a tenth of the counts (~1 min)"},
		{"all", "all 32 at full counts, scoreboard"},
	}},
	{"SEE IT CATCH A BUG, THEN THE FIX", [][2]string{
		{"failure", "plant a bug (ACK before write); HYDRA catches it"},
		{"success", "same test on the real engine: PASS, and why"},
	}},
	{"OTHER", [][2]string{
		{"/live [stop]", "live data page (starts empty)"},
		{"/help · /exit", "this list · leave"},
	}},
}

func (a *app) help() {
	width := 0
	for _, g := range commandList {
		for _, r := range g.rows {
			if n := len([]rune(r[0])); n > width {
				width = n
			}
		}
	}
	for _, g := range commandList {
		fmt.Fprintf(a.out, "\n  %s\n", tui.Bold(g.group))
		for _, r := range g.rows {
			fmt.Fprintf(a.out, "  %s  %s\n", tui.Cyan(tui.PadRight(r[0], width)), tui.Gray(r[1]))
		}
	}
	fmt.Fprintln(a.out)
}
