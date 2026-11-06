package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Env is where the tools live and where a session's files go.
type Env struct {
	torture, kvdb string
	root          string // repository root (for `go test` steps), "" if not found
	runDir        string // working directory of every torture run: failures/ and coverage files land here
	quick         bool
	quiet         bool // compact output: one live line per job (used by `all` and `judge`)
	keep          bool
	seed          string
}

// discover finds bin/torture and bin/kvdb next to this program, in ./bin, or in --bin.
func discover(binDir string) (*Env, error) {
	var cands []string
	if binDir != "" {
		cands = append(cands, binDir)
	}
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Dir(exe))
	}
	cands = append(cands, "bin")
	for _, d := range cands {
		t, k := filepath.Join(d, "torture"), filepath.Join(d, "kvdb")
		if isFile(t) && isFile(k) {
			at, _ := filepath.Abs(t)
			ak, _ := filepath.Abs(k)
			root := findRoot(filepath.Dir(at))
			if root == "" {
				if wd, err := os.Getwd(); err == nil {
					root = findRoot(wd)
				}
			}
			e := &Env{torture: at, kvdb: ak, root: root}
			return e, e.makeRunDir()
		}
	}
	return nil, fmt.Errorf("cannot find bin/torture and bin/kvdb: run `make` first, or pass --bin DIR")
}

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

// findRoot walks up from dir looking for this repository's go.mod.
func findRoot(dir string) string {
	for i := 0; i < 6; i++ {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(b), "pineapple") {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

// makeRunDir creates a fresh session directory on a real disk (the user cache), not /tmp, which is RAM on many systems.
func (e *Env) makeRunDir() error {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	base = filepath.Join(base, "hydra-ui")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	d, err := os.MkdirTemp(base, "run-"+time.Now().Format("0102-1504")+"-")
	if err != nil {
		return err
	}
	e.runDir = d
	return nil
}

// scaleArgs shrinks --iterations for quick runs (a tenth, at least 3). Full runs are left alone.
func scaleArgs(args []string, quick bool) []string {
	out := append([]string(nil), args...)
	if !quick {
		return out
	}
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--iterations" {
			n, err := strconv.Atoi(out[i+1])
			if err == nil {
				n /= 10
				if n < 3 {
					n = 3
				}
				out[i+1] = strconv.Itoa(n)
			}
		}
	}
	return out
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// iterationsOf returns the --iterations value in args, or 0.
func iterationsOf(args []string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--iterations" {
			n, _ := strconv.Atoi(args[i+1])
			return n
		}
	}
	return 0
}

func slug(s string) string {
	var sb strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
			dash = false
		} else if !dash {
			sb.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(sb.String(), "-")
}

func haveGo() bool { _, err := exec.LookPath("go"); return err == nil }
