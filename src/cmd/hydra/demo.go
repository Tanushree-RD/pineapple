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
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chethan-9817/pineapple/src/internal/envshell"
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

// raceBug is the second planted bug (`failure race`): the batch is published to the in-RAM index in chunks of 100 with
// the write lock released between chunks, so a reader can run in the middle and see half of an atomic batch.
var raceBug = []struct{ old, new string }{
	{"\tdb.mu.Lock()\n\tdb.idx.applyBatch(base, ents)\n\tdb.logicalEnd = base + F\n\tdb.nextSeq = seq + 1\n\tdb.mu.Unlock()\n",
		`	// BUG (planted by "failure race"): the lock is dropped between chunks of 100 entries.
	for i := 0; i < len(ents); i += 100 {
		j := i + 100
		if j > len(ents) {
			j = len(ents)
		}
		db.mu.Lock()
		db.idx.applyBatch(base, ents[i:j])
		db.mu.Unlock()
		time.Sleep(50 * time.Microsecond)
	}
	db.mu.Lock()
	db.logicalEnd = base + F
	db.nextSeq = seq + 1
	db.mu.Unlock()
`},
	plantedBug[0], // the "time" import
}

// applyPlantedBug returns the engine source with the bug in it; it fails loudly if the source has changed shape.
func applyPlantedBug(src string) (string, error) { return applyBug(src, plantedBug) }

func applyBug(src string, bug []struct{ old, new string }) (string, error) {
	for _, p := range bug {
		if strings.Count(src, p.old) != 1 {
			return "", fmt.Errorf("commit.go no longer has the expected shape (cannot plant the bug): %q", lineOne(p.old))
		}
		src = strings.Replace(src, p.old, p.new, 1)
	}
	return src, nil
}

func lineOne(s string) string { return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0] }

// buildBrokenKVDB copies the repository's engine to the session directory, plants the bug, and builds bin/kvdb from it.
func (e *Env) buildBrokenKVDB() (string, error) { return e.buildBroken("ack", plantedBug, false) }

// buildBroken builds the copy with the given bug planted; withTests also copies tests/ (the race demo runs them).
// It returns the path of the built kvdb; the copy's source directory is next to it.
func (e *Env) buildBroken(name string, bug []struct{ old, new string }, withTests bool) (string, error) {
	out := filepath.Join(e.runDir, "broken-"+name, "kvdb")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if e.root == "" || !haveGo() {
		return "", fmt.Errorf("`failure` builds a broken copy of the engine, so it needs the Go toolchain and the repository checkout")
	}
	copyDir := filepath.Join(e.runDir, "broken-"+name, "src")
	_ = os.RemoveAll(copyDir)
	rels := []string{"go.mod", "src"}
	if withTests {
		rels = append(rels, "tests")
	}
	for _, rel := range rels {
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
			if strings.HasSuffix(p, "_test.go") && !strings.HasPrefix(r, "tests"+string(filepath.Separator)) {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if r == filepath.Join("src", "engine", "commit.go") {
				s, err := applyBug(string(b), bug)
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

// lostKeys turns HYDRA's failure block into the page's "told OK, but not there" rows.
func lostKeys(block []string) (missing []string, acked string) {
	for _, l := range block {
		if i := strings.Index(l, "oracle: acked="); i >= 0 {
			acked = strings.Fields(l[i+len("oracle: acked="):])[0]
		}
		if i := strings.Index(l, "key="); i >= 0 && strings.Contains(l, "ACKed write LOST") {
			hexKey := strings.TrimRight(strings.Fields(l[i+4:])[0], ":")
			key := hexKey
			if b, err := hex.DecodeString(hexKey); err == nil {
				key = string(b)
			}
			seq := ""
			if j := strings.Index(l, "(ack_seq "); j >= 0 {
				seq = " (ack #" + strings.TrimRight(strings.Fields(l[j+9:])[0], ")") + ")"
			}
			missing = append(missing, key+": acknowledged"+seq+", absent from the database after the kill")
		}
	}
	return
}

func (h *henv) failure(args []string) {
	if len(args) > 0 && args[0] == "race" {
		h.raceDemo(false)
		return
	}
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
	args = []string{"--iterations", demoIterations, "--seed", demoSeed}
	fmt.Fprintln(w, "  "+tui.Gray("torture "+strings.Join(args, " ")+"   (against the broken copy)"))
	e2 := *h.e
	e2.kvdb = broken
	e2.keep, e2.holdActive = true, true // the page keeps showing the broken database
	r := e2.runJob(w, Job{Name: "failure demo: ACK before write", Args: args, Total: 20})
	printJobResult(w, r)
	if r.OK {
		fmt.Fprintf(w, "%s the broken copy passed this time (the kill never landed between ACK and write); run `failure` again\n", tui.Warn())
		return
	}
	missing, acked := lostKeys(r.Failure)
	v := envshell.Verdict{Kind: "fail", Title: "FAILURE: the database said OK, then lost the data (planted bug: ACK before write)", Missing: missing}
	if acked != "" {
		v.Lines = append(v.Lines, acked+" writes were acknowledged before the kill; the table below is what is actually on disk.")
	}
	envshell.SetVerdict(v)
	fmt.Fprintln(w, "  "+tui.Bold("what that means:")+" the client was told OK, the process was killed a few milliseconds later, and on restart")
	fmt.Fprintln(w, "  the write was not there. A database must never do that. HYDRA caught it from its own record of every ACK.")
	fmt.Fprintln(w, "  "+tui.Gray("the saved evidence is in failures/ in this directory; `--replay` re-checks it. The live page now shows the gap in red."))
	fmt.Fprintln(w, "  now type "+tui.Bold(tui.Green("success"))+" to run the same test on the fixed engine.")
	fmt.Fprintln(w)
}

func (h *henv) success(args []string) {
	if len(args) > 0 && args[0] == "race" {
		h.raceDemo(true)
		return
	}
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
	fmt.Fprintln(w, "  "+tui.Gray("difference from the broken copy: only the order, ACK last instead of first (the planted bug is 8 added lines)"))
	args = []string{"--iterations", demoIterations, "--seed", demoSeed}
	fmt.Fprintln(w, "  "+tui.Gray("torture "+strings.Join(args, " ")+"   (the same test, the real engine)"))
	e2 := *h.e
	e2.keep, e2.holdActive = true, true
	r := e2.runJob(w, Job{Name: "success demo: real engine", Args: args, Total: 20})
	printJobResult(w, r)
	if r.OK {
		envshell.SetVerdict(envshell.Verdict{Kind: "pass", Title: "SUCCESS: every acknowledged write survived every kill (real engine, same test that failed)",
			Lines: []string{strings.TrimSpace(r.Verdict), "gap between what was acknowledged and what is on disk: none"}})
	}
	fmt.Fprintln(w)
}

// raceDemo: `failure race` plants a publish-in-chunks bug and runs the repository's own D4 test (a reader running
// during 10,000-operation atomic batches must never see half of one) on the broken copy; `success race` runs that
// same test on the real engine.
func (h *henv) raceDemo(real bool) {
	w := h.out
	test := []string{"test", "./tests", "-run", "TestD4ConcurrentReaderNoPartialBatch", "-count=1"}
	fmt.Fprintln(w)
	if real {
		fmt.Fprintln(w, "  "+tui.Bold(tui.Green("SUCCESS (race)"))+"  "+tui.Gray("the fix: a batch is published to the index under ONE write lock"))
		fmt.Fprintln(w, "  "+tui.Gray("  readers hold the read lock across lookup and read, so they see a whole batch or none of it"))
	} else {
		fmt.Fprintln(w, "  "+tui.Bold(tui.Red("FAILURE (race)"))+"  "+tui.Gray("one planted bug: a batch becomes visible to readers a piece at a time"))
		fmt.Fprintln(w, "  "+tui.Gray("(a throwaway copy of the engine is built with the bug; the real engine is not touched)"))
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  the bug, in the publish step:")
		fmt.Fprintln(w, "    "+tui.Red("+ for each chunk of 100 entries { Lock(); apply chunk; Unlock(); sleep 50us }   // lock dropped between chunks"))
		fmt.Fprintln(w, "      (the real engine applies ALL entries under one Lock)")
	}
	if h.e.root == "" || !haveGo() {
		fmt.Fprintf(w, "%s the race demo runs the repository's own Go test, so it needs the Go toolchain and the checkout\n", tui.Fail())
		return
	}
	dir := h.e.root
	if !real {
		if _, err := h.e.buildBroken("race", raceBug, true); err != nil {
			fmt.Fprintf(w, "%s %v\n", tui.Fail(), err)
			return
		}
		dir = filepath.Join(h.e.runDir, "broken-race", "src")
	}
	envshell.ClearVerdict()
	fmt.Fprintln(w, "  "+tui.Gray("go "+strings.Join(test, " ")+"   (a reader asks for two keys of a 10,000-op batch while the batch is being written)"))
	cmd := exec.Command("go", test...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-buildvcs=false") // the copy is not a git checkout
	b, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(b))
	if err == nil {
		fmt.Fprintf(w, "%s %s  reader never saw half a batch\n", tui.OK(), tui.Green(tui.Bold("PASS")))
		if real {
			envshell.SetVerdict(envshell.Verdict{Kind: "pass", Title: "SUCCESS (race): readers saw a whole batch or none of it, never half",
				Lines: []string{"same test that failed on the planted bug; real engine publishes under one write lock", "gap between the batch the writer committed and what a reader could see: none"}})
		} else {
			fmt.Fprintf(w, "%s the broken copy passed this time; run `failure race` again\n", tui.Warn())
		}
		fmt.Fprintln(w)
		return
	}
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "D4 violation") {
			line = strings.TrimSpace(l)
		}
	}
	if line == "" {
		line = lastLine(text)
	}
	fmt.Fprintf(w, "%s %s  %s\n", tui.Fail(), tui.Red(tui.Bold("FAIL")), line)
	if real {
		return
	}
	envshell.SetVerdict(envshell.Verdict{Kind: "fail", Title: "FAILURE (race): a reader saw HALF of an atomic batch (planted bug: lock dropped while publishing)",
		Lines:   []string{"a batch of 10,000 writes must appear all at once; the reader caught it part-way"},
		Missing: []string{line}})
	fmt.Fprintln(w, "  "+tui.Bold("what that means:")+" two keys written in the SAME batch were seen with different values (or one missing), so the")
	fmt.Fprintln(w, "  batch was not atomic to readers. The live page shows the gap in red. Now type "+tui.Bold(tui.Green("success race"))+".")
	fmt.Fprintln(w)
}
