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
var hydraWords = map[string]bool{"attack": true, "judge": true, "all": true, "scenarios": true, "scenario": true, "run": true, "replay": true, "modes": true, "quick": true,
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
	a.startViewer()
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+tui.Bold(tui.Gradient("PINEAPPLE", 39, 220))+"  "+tui.Gray("LEAPFROG-R database + HYDRA torture, one prompt"))
	fmt.Fprintln(out, "  "+tui.Gray("type /help to see the commands · ↑ recalls earlier lines · /live shows the live data page"))
	fmt.Fprintln(out)
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

func (a *app) help() {
	rows := [][3]string{
		{"DATABASE", "", ""},
		{"put <db> <key> <value>", "store a key (quote values with spaces or ;)", "put demo.db city Mumbai"},
		{"get <db> <key> · del <db> <key>", "read or delete a key", "get demo.db city"},
		{"batch <db> <file>", "apply a file of put/del lines as ONE atomic batch", "batch demo.db ops.txt"},
		{"dump <db> · check <db> · open <db>", "list everything · verify the file · recover and count", "check demo.db"},
		{"serve <db>", "long-running server on this prompt (ACK per durable op); kill -9 it from another terminal", "serve kill.db"},
		{"compact · count · inspect", "on the current database", ""},
		{"/use <db>", "choose a database so the path can be left out afterwards", "/use demo.db"},
		{"TORTURE", "", ""},
		{"--iterations N --seed S", "random kill -9 during writes, every answer checked (the judge's run)", "--iterations 200 --seed 12345"},
		{"--mode compaction|recovery-crash|disk-full|large-values|large-batches|sweep|fixtures|micro", "targeted modes", "--mode compaction --seed 7"},
		{"attack <db> [--rounds N]", "kill -9 a server that is rewriting YOUR data; prove it comes back identical", "attack demo.db"},
		{"judge · all · scenarios · scenario B5", "the 15-minute path · all 32 scenarios · the list · one scenario", "scenario B5 A4"},
		{"OTHER", "", ""},
		{"/live [stop]", "the live data page (empty at start; shows only what is happening)", ""},
		{"/help · /exit", "", ""},
	}
	for _, r := range rows {
		switch {
		case r[1] == "" && r[2] == "" && strings.ToUpper(r[0]) == r[0]:
			fmt.Fprintf(a.out, "\n  %s\n", tui.Bold(r[0]))
		default:
			fmt.Fprintf(a.out, "  %s\n", tui.Cyan(r[0]))
			if r[1] != "" {
				fmt.Fprintf(a.out, "      %s\n", tui.Gray(r[1]))
			}
		}
	}
	fmt.Fprintln(a.out)
}
