// Command pineapple is the front door of the two environments:
//
//	/leapfrog [db]   the database environment: type put/get/del/dump/... directly
//	/hydra [db]      the torture environment: type torture flags directly, or attack <db>
//
// Inside either one, /hydra and /leapfrog switch to the other, carrying the current database along.
// It is built with `make ui`.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chethan-9817/pineapple/src/internal/envshell"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, bufio.NewReader(os.Stdin), envshell.SwitchTo)) }

func run(args []string, out io.Writer, in *bufio.Reader, sw func(name, db string) error) int {
	if len(args) > 0 {
		return dispatch(strings.Join(args, " "), out, sw)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  "+tui.Bold(tui.Gradient("PINEAPPLE", 39, 220))+"  "+tui.Gray("LEAPFROG-R database + HYDRA torture"))
	fmt.Fprintln(out)
	help(out)
	hist := tui.LoadHistory("pineapple")
	for {
		line, err := tui.ReadLine(out, in, tui.Bold(tui.Yellow("pineapple"))+tui.Yellow(" ❯ "), hist)
		if err == tui.ErrInterrupt {
			continue
		}
		if line = strings.TrimSpace(line); line != "" {
			if code := dispatch(line, out, sw); code >= 0 {
				return code
			}
		}
		if err != nil {
			fmt.Fprintln(out)
			return 0
		}
	}
}

func help(out io.Writer) {
	fmt.Fprintf(out, "  %s  %s\n", tui.Cyan(tui.PadRight("/leapfrog [db]", 16)), tui.Gray("the database: put, get, del, dump, check ... typed directly, plus /seed and /live"))
	fmt.Fprintf(out, "  %s  %s\n", tui.Red(tui.PadRight("/hydra [db]", 16)), tui.Gray("the torture tool: flags typed directly, or  attack <db>"))
	fmt.Fprintf(out, "  %s  %s\n\n", tui.PadRight("/exit", 16), tui.Gray("leave"))
}

// dispatch returns -1 to keep the prompt open, or an exit code.
func dispatch(line string, out io.Writer, sw func(name, db string) error) int {
	toks, err := envshell.Tokenize(line)
	if err != nil || len(toks) == 0 {
		fmt.Fprintln(out, "  type /leapfrog, /hydra or /exit")
		return -1
	}
	db := ""
	if len(toks) > 1 {
		db = toks[1]
	}
	switch strings.TrimPrefix(toks[0], "/") {
	case "leapfrog", "hydra":
		name := strings.TrimPrefix(toks[0], "/")
		if err := sw(name, db); err != nil {
			fmt.Fprintf(out, "%s %v\n", tui.Fail(), err)
			return -1
		}
		return 0
	case "exit", "quit", "q":
		return 0
	case "help", "h", "?":
		help(out)
		return -1
	}
	fmt.Fprintf(out, "%s unknown %q. %s\n", tui.Fail(), toks[0], tui.Gray("Use /leapfrog, /hydra or /exit."))
	return -1
}
