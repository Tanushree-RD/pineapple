// Package envshell holds what the interactive environments of the LEAPFROG and HYDRA tools share:
// a shell-style tokenizer, the switch from one environment to another, and the manager of the
// live data viewer that keeps running while you switch.
//
// The environments follow the idea of Novato: short slash commands (/help, /status, /live) and plain
// words, so nobody retypes a long command line again and again.
package envshell

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Tokenize splits a line the way a shell does, so that
//
//	put students.db student:101 "name=Asha;city=Bengaluru;marks=88"
//
// arrives as four words. Double quotes allow \" and \\ escapes, single quotes are literal, a backslash outside
// quotes escapes the next character, and adjacent quoted pieces join into one word.
func Tokenize(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			inWord = true
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				cur.WriteRune(rs[j])
				j++
			}
			if j >= len(rs) {
				return nil, errors.New("unterminated ' quote")
			}
			i = j
		case c == '"':
			inWord = true
			j := i + 1
			closed := false
			for j < len(rs) {
				if rs[j] == '\\' && j+1 < len(rs) && (rs[j+1] == '"' || rs[j+1] == '\\') {
					cur.WriteRune(rs[j+1])
					j += 2
					continue
				}
				if rs[j] == '"' {
					closed = true
					break
				}
				cur.WriteRune(rs[j])
				j++
			}
			if !closed {
				return nil, errors.New("unterminated \" quote")
			}
			i = j
		case c == '\\':
			inWord = true
			if i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
			}
		default:
			inWord = true
			cur.WriteRune(c)
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// StripPrefix drops a leading program name, so pasting a full command such as
// "./bin/kvdb put students.db k v" into an environment works as well as "put students.db k v".
func StripPrefix(tokens []string, names ...string) []string {
	if len(tokens) == 0 {
		return tokens
	}
	base := filepath.Base(tokens[0])
	for _, n := range names {
		if base == n {
			return tokens[1:]
		}
	}
	return tokens
}

// SiblingBinary finds another tool of this project: next to the running program, in ./bin, or on the PATH.
func SiblingBinary(name string) (string, error) {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if st, err := os.Stat(filepath.Join("bin", name)); err == nil && !st.IsDir() {
		abs, _ := filepath.Abs(filepath.Join("bin", name))
		return abs, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("cannot find the %s program next to this one: run `make ui`", name)
}

// ExecFunc replaces the current process; tests swap it out.
var ExecFunc = syscall.Exec

// SwitchTo replaces this process with another environment (leapfrog, hydra or pineapple), passing the current
// database along as --db. Replacing the process (rather than starting a child) releases every lock this one held.
func SwitchTo(name, db string) error {
	path, err := SiblingBinary(name)
	if err != nil {
		return err
	}
	argv := []string{name}
	if db != "" {
		argv = append(argv, "--db", db)
	}
	return ExecFunc(path, argv, os.Environ())
}

// ParseDBFlag removes "--db PATH" from args and returns the path.
func ParseDBFlag(args []string) (db string, rest []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--db" && i+1 < len(args) {
			db = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return db, rest
}
