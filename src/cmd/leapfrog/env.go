package main

// The LEAPFROG environment: `leapfrog` (or /leapfrog from the pineapple launcher) opens a prompt where the database
// commands are typed directly, without ./bin/kvdb in front:
//
//	leapfrog ❯ put students.db student:101 "name=Asha;city=Bengaluru;marks=88"
//
// Every command runs the real bin/kvdb, so what you see is exactly what a judge would get on the command line.
// Slash commands (/help, /use, /seed, /live, /hydra, /exit) are the shortcuts around it.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chethan-9817/pineapple/src/internal/envshell"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

const defaultLivePort = 8099

// kvdbCommands are the 8 commands of bin/kvdb; typed in the environment they run unchanged.
var kvdbCommands = map[string]bool{"open": true, "put": true, "get": true, "del": true, "batch": true, "dump": true, "check": true, "serve": true}

type lenv struct {
	out    io.Writer
	in     *bufio.Reader
	kvdb   string
	db     string // the database the short forms use ("/use students.db")
	port   int
	stdout io.Writer // where kvdb output goes (the terminal)
	hydra  func(db string) error
	hist   *tui.History // up/down arrows recall earlier lines, kept between sessions
}

// mockData is the sample database: students, courses and settings. It is written as ONE atomic batch.
var mockData = [][2]string{
	{"student:101", "name=Asha;city=Bengaluru;marks=88"},
	{"student:102", "name=Ravi;city=Mysuru;marks=74"},
	{"student:103", "name=Meera;city=Chennai;marks=91"},
	{"student:104", "name=Kiran;city=Hyderabad;marks=67"},
	{"student:105", "name=Divya;city=Pune;marks=95"},
	{"student:106", "name=Arjun;city=Mumbai;marks=59"},
	{"student:107", "name=Sneha;city=Kochi;marks=82"},
	{"student:108", "name=Vikram;city=Delhi;marks=78"},
	{"course:CS101", "title=Intro-to-Programming;credits=4;teacher=Dr.Rao"},
	{"course:CS201", "title=Data-Structures;credits=4;teacher=Dr.Iyer"},
	{"course:CS301", "title=Databases;credits=3;teacher=Dr.Nair"},
	{"course:MA101", "title=Discrete-Maths;credits=3;teacher=Dr.Sen"},
	{"enroll:101:CS101", "grade=A;year=2026"},
	{"enroll:101:CS301", "grade=B+;year=2026"},
	{"enroll:103:CS201", "grade=A+;year=2026"},
	{"enroll:105:CS301", "grade=A+;year=2026"},
	{"config:college", "name=Pineapple-Institute-of-Technology"},
	{"config:semester", "2026-odd"},
}

func (e *lenv) run() int {
	banner(e.out, true)
	e.autoLive()
	fmt.Fprintln(e.out, "  "+tui.Gray("type commands directly, e.g.  put students.db student:101 \"name=Asha;marks=88\""))
	fmt.Fprintln(e.out, "  "+tui.Gray("/help lists everything · /seed makes sample data · /live shows it in the browser · /hydra attacks it"))
	if e.db != "" {
		fmt.Fprintf(e.out, "  %s using %s\n", tui.OK(), tui.Bold(e.db))
	}
	fmt.Fprintln(e.out)
	for {
		line, err := tui.ReadLine(e.out, e.in, e.prompt(), e.hist)
		if err == tui.ErrInterrupt {
			continue
		}
		line = strings.TrimSpace(line)
		if line != "" && e.exec(line) {
			return 0
		}
		if err != nil {
			fmt.Fprintln(e.out)
			return 0
		}
	}
}

func (e *lenv) prompt() string {
	name := "leapfrog"
	if e.db != "" {
		name += " " + tui.Gray("["+filepath.Base(e.db)+"]")
	}
	return tui.Bold(tui.Cyan(name)) + tui.Cyan(" ❯ ")
}

// exec runs one typed line; it returns true when the environment should end.
func (e *lenv) exec(line string) (quit bool) {
	toks, err := envshell.Tokenize(line)
	if err != nil {
		fmt.Fprintf(e.out, "%s %v\n", tui.Fail(), err)
		return false
	}
	if len(toks) == 0 {
		return false
	}
	toks = envshell.StripPrefix(toks, "kvdb", "leapfrog")
	if len(toks) == 0 {
		return false
	}
	cmd := strings.TrimPrefix(toks[0], "/")
	args := toks[1:]
	switch cmd {
	case "exit", "quit", "q":
		fmt.Fprintln(e.out, tui.Gray("  bye. The database file stays on disk, consistent."))
		return true
	case "help", "h", "?":
		e.help()
	case "use":
		e.use(args)
	case "seed":
		e.seed(args)
	case "live":
		e.live(args)
	case "status":
		e.status()
	case "inspect":
		if db := e.dbArg(args, 0); db != "" {
			cmdInspect(append([]string{db}, tail(args, 1)...), e.out)
		}
	case "compact":
		if db := e.dbArg(args, 0); db != "" {
			e.runKVDB("serve", db, "<<compact")
		}
	case "count":
		if db := e.dbArg(args, 0); db != "" {
			e.runKVDB("open", db)
		}
	case "hydra":
		if e.hydra == nil {
			fmt.Fprintln(e.out, "  switching is not available here")
			return false
		}
		if err := e.hydra(e.db); err != nil {
			fmt.Fprintf(e.out, "%s %v\n", tui.Fail(), err)
			return false
		}
		return true
	case "clear", "cls":
		fmt.Fprint(e.out, "\x1b[2J\x1b[H")
	default:
		if kvdbCommands[cmd] {
			e.kvdbLine(cmd, args)
		} else {
			fmt.Fprintf(e.out, "%s unknown command %q. Try /help.\n", tui.Fail(), cmd)
		}
	}
	return false
}

func tail(a []string, n int) []string {
	if len(a) <= n {
		return nil
	}
	return a[n:]
}

// looksLikePath: the first argument names a database when it ends in .db or is an existing file.
func looksLikePath(s string) bool {
	if strings.HasSuffix(s, ".db") {
		return true
	}
	st, err := os.Stat(s)
	return err == nil && !st.IsDir()
}

// dbArg returns the database named at args[i], or the current one, or "".
func (e *lenv) dbArg(args []string, i int) string {
	if len(args) > i && looksLikePath(args[i]) {
		return args[i]
	}
	if e.db != "" {
		return e.db
	}
	fmt.Fprintf(e.out, "%s no database yet: name one (put students.db k v) or type /use students.db\n", tui.Fail())
	return ""
}

// kvdbLine runs a kvdb command; with a current database (/use) the path may be left out.
func (e *lenv) kvdbLine(cmd string, args []string) {
	if len(args) == 0 || !looksLikePath(args[0]) {
		if e.db == "" {
			fmt.Fprintf(e.out, "%s the database path is missing: %s <db> ...   (or /use <db> first)\n", tui.Fail(), cmd)
			return
		}
		args = append([]string{e.db}, args...)
	}
	if e.db == "" {
		e.db = args[0] // the first database you touch becomes the current one
	}
	if cmd == "serve" {
		fmt.Fprintln(e.out, tui.Gray("  serve reads operations from this prompt; end with Ctrl-D"))
	}
	e.runKVDB(append([]string{cmd}, args...)...)
}

func (e *lenv) runKVDB(args ...string) {
	if e.kvdb == "" {
		fmt.Fprintf(e.out, "%s cannot find the kvdb program: run `make build`\n", tui.Fail())
		return
	}
	var stdin io.Reader = os.Stdin
	if len(args) == 3 && args[2] == "<<compact" {
		args, stdin = args[:2], strings.NewReader("compact\n")
	}
	c := exec.Command(e.kvdb, args...)
	c.Stdin, c.Stdout, c.Stderr = stdin, e.out, e.out
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			fmt.Fprintln(e.out, tui.Gray(fmt.Sprintf("  (exit code %d)", ee.ExitCode())))
		} else {
			fmt.Fprintf(e.out, "%s %v\n", tui.Fail(), err)
		}
	}
}

func (e *lenv) use(args []string) {
	if len(args) == 0 {
		if e.db == "" {
			fmt.Fprintln(e.out, "  no database selected: /use students.db")
		} else {
			fmt.Fprintln(e.out, "  using "+e.db)
		}
		return
	}
	e.db = args[0]
	fmt.Fprintf(e.out, "%s using %s: you can now type  put student:106 \"name=Zed\"  without the path\n", tui.OK(), tui.Bold(e.db))
}

// seed writes the sample data as one atomic batch (so it also shows the batch path working).
func (e *lenv) seed(args []string) {
	db := "students.db"
	if len(args) > 0 {
		db = args[0]
	} else if e.db != "" {
		db = e.db
	}
	f, err := os.CreateTemp("", "leapfrog-seed-*.txt")
	if err != nil {
		fmt.Fprintf(e.out, "%s %v\n", tui.Fail(), err)
		return
	}
	defer os.Remove(f.Name())
	for _, kv := range mockData { // a batch file is split on spaces, so the sample values contain none
		fmt.Fprintf(f, "put %s %s\n", kv[0], kv[1])
	}
	f.Close()
	e.runKVDB("batch", db, f.Name())
	e.db = db
	fmt.Fprintf(e.out, "%s %d sample records in %s (one atomic batch). Try: dump · get student:101 · /live\n", tui.OK(), len(mockData), db)
}

func (e *lenv) live(args []string) {
	port := e.port
	if len(args) > 0 && args[0] == "stop" {
		if envshell.StopLive(port) {
			fmt.Fprintln(e.out, "  live viewer stopped")
		} else {
			fmt.Fprintln(e.out, "  no live viewer was started from here")
		}
		return
	}
	if len(args) > 0 {
		if n, err := strconv.Atoi(args[0]); err == nil && n > 0 && n < 65536 {
			port, e.port = n, n
		}
	}
	db := e.dbArg(nil, 0)
	if db == "" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	url, err := envshell.StartLive(self, db, port)
	if err != nil {
		fmt.Fprintf(e.out, "%s %v\n", tui.Fail(), err)
		return
	}
	fmt.Fprintf(e.out, "%s live view of %s: %s\n  %s\n", tui.OK(), db, tui.Bold(tui.Cyan(url)), tui.Gray("open it in a browser; it keeps running when you switch to /hydra (stop it with /live stop)"))
}

func (e *lenv) status() {
	if e.db == "" {
		fmt.Fprintln(e.out, "  no database selected")
		return
	}
	snap := snapshotDB(e.db)
	if snap.Error != "" {
		fmt.Fprintf(e.out, "  %s: %s\n", e.db, snap.Error)
		return
	}
	fmt.Fprintf(e.out, "  %s: %d keys · %s on disk · %d frames · tail %s\n", e.db, snap.Keys, tui.Bytes(snap.Bytes), snap.Frames, snap.Tail)
	if db, ok := envshell.LiveRunning(e.port); ok {
		fmt.Fprintf(e.out, "  live viewer: %s (showing %s)\n", envshell.LiveURL(e.port), db)
	}
}

func (e *lenv) help() {
	rows := [][2]string{
		{"put <db> <key> <value>", "store a key (on disk before OK appears). Quote values with spaces or ;"},
		{"get <db> <key>", "print the value, or NOTFOUND"},
		{"del <db> <key>", "delete a key"},
		{"batch <db> <file>", "apply a file of put/del lines as one atomic batch"},
		{"dump <db>", "print every key and value"},
		{"check <db>", "verify the file: OK or CORRUPT <reason>"},
		{"open <db> [--max-bytes N]", "recover, print key count and recovery time (N persists a size cap)"},
		{"serve <db>", "long-running mode on this prompt (put/del/get/begin..commit/compact), Ctrl-D to end"},
		{"", ""},
		{"/use <db>", "make a database the current one: then leave the path out (put k v)"},
		{"/seed [db]", "write sample data (students, courses, enrollments) as one atomic batch"},
		{"/live [port|stop]", "show the database in the browser, updating in real time (default port 8099)"},
		{"inspect [db]", "decode the file: superblocks, frames, tail"},
		{"compact [db]", "compact the file (it must shrink after deletes)"},
		{"count [db]", "key count and recovery time"},
		{"/status", "keys, size and tail of the current database"},
		{"/hydra", "switch to the torture environment (the live viewer keeps running)"},
		{"/exit", "leave"},
	}
	for _, r := range rows {
		if r[0] == "" {
			fmt.Fprintln(e.out)
			continue
		}
		fmt.Fprintf(e.out, "  %s  %s\n", tui.Cyan(tui.PadRight(r[0], 28)), tui.Gray(r[1]))
	}
	fmt.Fprintln(e.out, "\n  "+tui.Gray("you may also paste a full line such as ./bin/kvdb put students.db k v: the program name is ignored."))
}

// cmdEnv is `leapfrog` with no subcommand or `leapfrog env [--db X]`.
func cmdEnv(args []string, out io.Writer, in *bufio.Reader) int {
	db, rest := envshell.ParseDBFlag(args)
	kv := ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--kvdb" && i+1 < len(rest) {
			kv = rest[i+1]
			i++
		}
	}
	e := &lenv{out: out, in: in, kvdb: findKVDB(kv), db: db, port: defaultLivePort, hist: tui.LoadHistory("leapfrog")}
	e.hydra = func(db string) error { return envshell.SwitchTo("hydra", db) }
	return e.run()
}

// autoLive keeps the data viewer running in the background: with no database chosen it uses students.db (sample data is
// written if the file does not exist yet).
func (e *lenv) autoLive() {
	if e.db == "" {
		e.db = "students.db"
	}
	if _, err := os.Stat(e.db); err != nil && e.db == "students.db" {
		e.seed([]string{e.db})
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	// The viewer runs in the background; nothing is printed and no browser is opened. /live prints the address.
	_, _ = envshell.StartLive(self, e.db, e.port)
}
