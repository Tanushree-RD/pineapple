package main

// `failure` and `success`: a side-by-side demonstration that HYDRA has teeth.
//
// `failure` plants one real bug in a THROWAWAY COPY of the engine (the repository is never touched): the database says
// ACK as soon as it has a write in hand and writes it a moment later (a write-behind cache). Nothing looks wrong
// until the process is killed between the ACK and the write, and then an acknowledged write is gone. HYDRA runs its
// ordinary random-kill test against that copy and catches it.
// `success` runs exactly the same test (same seed, same iterations) against the real engine, whose commit path is
// write -> fsync -> publish -> ACK, so no ACK can ever get ahead of the data.

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chethan-9817/pineapple/src/internal/tui"
)

const (
	demoIterations = "20"
	demoSeed       = "12345"
)

// plantedBug is the whole bug: two edits to src/engine/commit.go, applied to the copy.
var plantedBug = []struct{ old, new string }{
	{"import (\n\t\"fmt\"\n\t\"io\"\n)", "import (\n\t\"fmt\"\n\t\"io\"\n\t\"time\"\n)"},
	{"\t// W1: stream the frame at logicalEnd.\n", `	// BUG (planted by the "failure" demo): ACK first, write later.
	if t.tag != 0 && db.opts.OnCommit != nil {
		db.opts.OnCommit(t.tag)
		t.tag = 0
	}
	time.Sleep(40 * time.Millisecond) // the write is "queued"

	// W1: stream the frame at logicalEnd.
`},
}

// applyPlantedBug returns the engine source with the bug in it; it fails loudly if the source has changed shape.
func applyPlantedBug(src string) (string, error) {
	for _, p := range plantedBug {
		if strings.Count(src, p.old) != 1 {
			return "", fmt.Errorf("commit.go no longer has the expected shape (cannot plant the bug): %q", lineOne(p.old))
		}
		src = strings.Replace(src, p.old, p.new, 1)
	}
	return src, nil
}

func lineOne(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }

// buildBrokenKVDB copies the repository's engine to the session directory, plants the bug, and builds bin/kvdb from it.
func (e *Env) buildBrokenKVDB() (string, error) {
	out := filepath.Join(e.runDir, "broken", "kvdb")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if e.root == "" || !haveGo() {
		return "", fmt.Errorf("`failure` builds a broken copy of the engine, so it needs the Go toolchain and the repository checkout")
	}
	copyDir := filepath.Join(e.runDir, "broken", "src")
	_ = os.RemoveAll(copyDir)
	for _, rel := range []string{"go.mod", "src"} {
		from := filepath.Join(e.root, rel)
		err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			r, _ := filepath.Rel(e.root, p)
			to := filepath.Join(copyDir, r)
			if d.IsDir() {
				return os.MkdirAll(to, 0o755)
			}
			if strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if r == filepath.Join("src", "engine", "commit.go") {
				s, err := applyPlantedBug(string(b))
				if err != nil {
					return err
				}
				b = []byte(s)
			}
			if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
				return err
			}
			return os.WriteFile(to, b, 0o644)
		})
		if err != nil {
			return "", err
		}
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, "./src/cmd/kvdb")
	cmd.Dir = copyDir
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the broken copy: %v\n%s", err, b)
	}
	return out, nil
}

func (h *henv) failure() {
	w := h.out
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+tui.Bold(tui.Red("FAILURE DEMO"))+"  "+tui.Gray("one planted bug: the database says ACK before the data is written"))
	fmt.Fprintln(w, "  "+tui.Gray("(a throwaway copy of the engine is built with the bug; the real engine is not touched)"))
	broken, err := h.e.buildBrokenKVDB()
	if err != nil {
		fmt.Fprintf(w, "%s %v\n", tui.Fail(), err)
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  the bug, in the commit path:")
	fmt.Fprintln(w, "    "+tui.Red("+ OnCommit(tag)                 // ACK now ..."))
	fmt.Fprintln(w, "    "+tui.Red("+ time.Sleep(40ms)              // ... the write happens a moment later"))
	fmt.Fprintln(w, "      pwrite(frame); fsync()           // (still there, but the client was already told OK)")
	fmt.Fprintln(w)
	args := []string{"--iterations", demoIterations, "--seed", demoSeed}
	fmt.Fprintln(w, "  "+tui.Gray("torture "+strings.Join(args, " ")+"   (against the broken copy)"))
	e2 := *h.e
	e2.kvdb = broken
	r := e2.runJob(w, Job{Name: "failure demo", Args: args, Total: 20})
	printJobResult(w, r)
	if r.OK {
		fmt.Fprintf(w, "%s the broken copy passed this time (the kill never landed between ACK and write); run `failure` again\n", tui.Warn())
		return
	}
	fmt.Fprintln(w, "  "+tui.Bold("what that means:")+" the client was told OK, the process was killed a few milliseconds later, and on restart")
	fmt.Fprintln(w, "  the write was not there. A database must never do that. HYDRA caught it from its own record of every ACK.")
	fmt.Fprintln(w, "  "+tui.Gray("the saved evidence is in failures/ in this directory; `--replay` re-checks it."))
	fmt.Fprintln(w, "  now type "+tui.Bold(tui.Green("success"))+" to run the same test on the fixed engine.")
	fmt.Fprintln(w)
}

func (h *henv) success() {
	w := h.out
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+tui.Bold(tui.Green("SUCCESS"))+"  "+tui.Gray("the fix: the ACK can only be reached after the data is on disk"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  the real commit path (src/engine/commit.go), in this order and no other:")
	fmt.Fprintln(w, "    "+tui.Green("1 pwrite(frame)        2 fsync()  <- durability point        3 publish to the index        4 OnCommit -> ACK"))
	fmt.Fprintln(w, "  "+tui.Gray("  - one committer goroutine runs these four steps for one transaction at a time, so nothing can ACK early"))
	fmt.Fprintln(w, "  "+tui.Gray("  - the ACK is a callback fired by the committer after step 3, not something a writer can send itself"))
	fmt.Fprintln(w, "  "+tui.Gray("  - 17 named crash points sit between the steps, and `--mode sweep` kills it at every one"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+tui.Gray("difference from the broken copy: only the order, ACK last instead of first (`diff` of the two commit paths is 8 lines)"))
	args := []string{"--iterations", demoIterations, "--seed", demoSeed}
	fmt.Fprintln(w, "  "+tui.Gray("torture "+strings.Join(args, " ")+"   (the same test, the real engine)"))
	r := h.e.runJob(w, Job{Name: "success demo", Args: args, Total: 20})
	printJobResult(w, r)
	fmt.Fprintln(w)
}
