// Command leapfrog is the friendly front end of LEAPFROG-R, the crash-safe key-value database:
// an interactive shell, a live kill -9 demo, and a byte-level file inspector.
//
//	leapfrog                       launcher menu
//	leapfrog shell <db>            interactive shell (put, get, begin/commit, dump, check, compact, cap, ...)
//	leapfrog demo [--rounds N]     kill -9 a live server and prove nothing acknowledged was lost
//	leapfrog inspect <db> [-v]     decode the file: superblocks, frames, tail; -v labels every field
//
// It is built with `make ui`; the database itself and its test tools (bin/kvdb, bin/torture) are unchanged.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, bufio.NewReader(os.Stdin))) }

func run(args []string, out io.Writer, in *bufio.Reader) int {
	if len(args) == 0 || args[0] == "--db" || args[0] == "--kvdb" {
		return cmdEnv(args, out, in)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "env":
		return cmdEnv(rest, out, in)
	case "menu":
		return launcher(out, in)
	case "shell":
		return cmdShell(rest, out, in)
	case "demo":
		return cmdDemo(rest, out)
	case "inspect":
		return cmdInspect(rest, out)
	case "live":
		return cmdLive(rest, out)
	case "help", "-h", "--help":
		usage(out)
		return 0
	}
	fmt.Fprintf(out, "unknown command %q\n\n", cmd)
	usage(out)
	return 2
}

func usage(w io.Writer) {
	banner(w, false)
	rows := [][2]string{
		{"leapfrog", "launcher menu"},
		{"leapfrog shell <db> [--max-bytes N]", "interactive shell on a database file (created if missing)"},
		{"leapfrog demo [--rounds N]", "kill -9 a live server N times (default 5) and check what survived"},
		{"leapfrog inspect <db> [-v]", "decode the file; -v labels every byte of the first frame"},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %s  %s\n", tui.Cyan(tui.PadRight(r[0], 38)), tui.Gray(r[1]))
	}
	fmt.Fprintln(w, "\n  flags: --kvdb PATH (the kvdb binary used by the demo; default: next to this program or ./bin)\n  NO_COLOR=1 turns colors off.")
}

func banner(w io.Writer, big bool) {
	fmt.Fprintln(w)
	if big && tui.Width() >= 76 {
		for _, l := range tui.Banner("LEAPFROG-R", 39, 51) {
			fmt.Fprintln(w, "  "+l)
		}
	} else {
		fmt.Fprintln(w, "  "+tui.Bold(tui.Gradient("LEAPFROG-R", 39, 51)))
	}
	fmt.Fprintln(w, "  "+tui.Gray("a crash-safe, single-file key-value database · Go, standard library only"))
	fmt.Fprintln(w)
}

// findKVDB locates the kvdb binary the demo drives.
func findKVDB(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "kvdb")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if _, err := os.Stat("bin/kvdb"); err == nil {
		abs, _ := filepath.Abs("bin/kvdb")
		return abs
	}
	if p, err := exec.LookPath("kvdb"); err == nil {
		return p
	}
	return ""
}

func cmdShell(args []string, out io.Writer, in *bufio.Reader) int {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	capB := fs.Int64("max-bytes", 0, "")
	kv := fs.String("kvdb", "", "")
	path, flagArgs := splitPath(args)
	if err := fs.Parse(flagArgs); err != nil || path == "" {
		fmt.Fprintln(out, "usage: leapfrog shell <db> [--max-bytes N] [--kvdb PATH]")
		return 2
	}
	setCap := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "max-bytes" {
			setCap = true
		}
	})
	return startShell(out, in, path, *capB, setCap, findKVDB(*kv))
}

// splitPath takes the first non-flag argument as the path so that flags may come before or after it.
func splitPath(args []string) (string, []string) {
	var path string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && a != "-v" {
				rest = append(rest, args[i+1])
				i++
			}
		} else if path == "" {
			path = a
		}
	}
	return path, rest
}

func startShell(out io.Writer, in *bufio.Reader, path string, capB int64, setCap bool, kvdb string) int {
	s, err := openShell(path, capB, setCap, out, in, kvdb)
	if err != nil {
		fmt.Fprintf(out, "%s cannot open %s: %v\n", tui.Fail(), path, err)
		return 1
	}
	defer func() { _ = s.db.Close() }() // the CURRENT handle: check, cap and crash reopen the database
	banner(out, false)
	st := s.opened
	fmt.Fprintf(out, "  %s %s  %s\n", tui.OK(), tui.Bold(path), tui.Gray(fmt.Sprintf("%d keys · tail %s · recovered in %s%s", st.Keys, st.Tail, st.Recovery.Round(1000), capNote(st.MaxBytes))))
	fmt.Fprintf(out, "  %s\n\n", tui.Gray("type help for commands · every put below is a real write + fsync · quit to leave"))
	s.run()
	fmt.Fprintln(out, tui.Gray("  bye. The file is consistent whatever happens next: try `leapfrog inspect "+path+"`."))
	return 0
}

func capNote(c int64) string {
	if c == 0 {
		return ""
	}
	return " · cap " + tui.Bytes(c)
}

func cmdDemo(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rounds := fs.Int("rounds", 5, "")
	kv := fs.String("kvdb", "", "")
	if err := fs.Parse(args); err != nil || *rounds < 1 || *rounds > 50 {
		fmt.Fprintln(out, "usage: leapfrog demo [--rounds N (1..50)] [--kvdb PATH]")
		return 2
	}
	kvdb := findKVDB(*kv)
	if kvdb == "" {
		fmt.Fprintf(out, "%s the demo drives the kvdb binary and cannot find it. Run `make`, or pass --kvdb PATH.\n", tui.Fail())
		return 1
	}
	banner(out, false)
	if runDemo(out, kvdb, *rounds) {
		return 0
	}
	return 1
}

func cmdInspect(args []string, out io.Writer) int {
	path, flagArgs := splitPath(args)
	verbose := false
	for _, a := range flagArgs {
		if a == "-v" || a == "--verbose" {
			verbose = true
		}
	}
	if path == "" {
		fmt.Fprintln(out, "usage: leapfrog inspect <db> [-v]")
		return 2
	}
	im, err := readImage(path)
	if err != nil {
		fmt.Fprintf(out, "%s %v\n", tui.Fail(), err)
		return 1
	}
	printImage(out, path, im, verbose)
	return 0
}

func ask(out io.Writer, in *bufio.Reader, q, def string) string {
	fmt.Fprintf(out, "  %s %s ", q, tui.Gray("["+def+"]"))
	line, _ := in.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

func launcher(out io.Writer, in *bufio.Reader) int {
	banner(out, true)
	items := []tui.MenuItem{
		{Label: "Open a database shell", Hint: "put, get, batches, dump, check, compact, size cap"},
		{Label: "Watch it survive kill -9", Hint: "a live crash demo with a verdict"},
		{Label: "Inspect a database file", Hint: "decode the bytes: superblocks, frames, tail"},
		{Label: "Quit", Hint: ""},
	}
	idx, ok := tui.Menu(out, in, "", items)
	if !ok {
		return 0
	}
	switch idx {
	case 0:
		return startShell(out, in, ask(out, in, "database file", "demo.leapfrog"), 0, false, findKVDB(""))
	case 1:
		return cmdDemo(nil, out)
	case 2:
		return cmdInspect([]string{ask(out, in, "database file", "demo.leapfrog"), "-v"}, out)
	}
	return 0
}
