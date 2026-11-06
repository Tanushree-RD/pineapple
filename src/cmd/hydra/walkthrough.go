package main

// `walkthrough`: the whole working of the database, shown as it really happens.
//
// Every step runs the REAL bin/kvdb on a scratch database and reports what is actually on disk afterwards (file
// size, tail state, what recovery returns). The seven steps of one write are shown by killing the engine at each of
// its named crash points (KVDB_CRASH=CP-W0x:1; the engine SIGKILLs itself there, after writing a torn prefix at CP-W03),
// so nothing here is a drawing: each line is read back from the file. The live page follows the scratch database and
// shows a "STEP n" banner for the step being shown.

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chethan-9817/pineapple/src/internal/envshell"
	"github.com/chethan-9817/pineapple/src/internal/tui"
)

type wt struct {
	h    *henv
	w    io.Writer
	dir  string
	fast bool
	n    int // step counter
}

// kv runs the real kvdb with extra environment; it returns trimmed stdout+stderr and the exit code.
func (t *wt) kv(env []string, args ...string) (string, int) {
	cmd := exec.Command(t.h.e.kvdb, args...)
	cmd.Env = append(os.Environ(), env...)
	b, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	var keep []string // the engine's INFO diagnostics go to stderr; the walkthrough shows only the answers
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if !strings.HasPrefix(l, "INFO ") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n"), code
}

func (t *wt) path(name string) string { return filepath.Join(t.dir, name) }

func (t *wt) size(db string) int64 {
	st, err := os.Stat(db)
	if err != nil {
		return 0
	}
	return st.Size()
}

func (t *wt) digest(db string) string {
	b, err := os.ReadFile(db)
	if err != nil {
		return "?"
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))[:12]
}

// tail returns the tail state ("COMPLETE", "TORN", ...) that recovery reports for the file.
func (t *wt) open(db string) (keys, tail string) {
	out, _ := t.kv(nil, "open", db)
	for _, f := range strings.Fields(out) {
		if v, ok := strings.CutPrefix(f, "keys="); ok {
			keys = v
		}
		if v, ok := strings.CutPrefix(f, "tail="); ok {
			tail = v
		}
	}
	return
}

func (t *wt) get(db, key string) string {
	out, _ := t.kv(nil, "get", db, key)
	return out
}

func (t *wt) copyDB(from, to string) {
	b, _ := os.ReadFile(from)
	_ = os.WriteFile(to, b, 0o644)
}

func (t *wt) pause() {
	if !t.fast {
		time.Sleep(1800 * time.Millisecond)
	}
}

// show points the live page at db and puts a banner on it for this step.
func (t *wt) show(db, title string, lines ...string) {
	envshell.SetActiveDB(db)
	envshell.SetVerdict(envshell.Verdict{Kind: "step", Title: title, Lines: lines})
}

func (t *wt) say(format string, a ...any) { fmt.Fprintf(t.w, format+"\n", a...) }

func (t *wt) heading(title string) {
	t.say("")
	t.say("  %s", tui.Bold(tui.Cyan(title)))
}

func (h *henv) walkthrough(args []string) {
	t := &wt{h: h, w: h.out, fast: hasFlag(args, "fast") || hasFlag(args, "--fast")}
	dir, err := os.MkdirTemp(h.e.runDir, "walkthrough-")
	if err != nil {
		fmt.Fprintf(h.out, "%s %v\n", tui.Fail(), err)
		return
	}
	t.dir = dir
	defer func() {
		if h.db != "" {
			envshell.SetActiveDB(h.db)
		} else {
			envshell.ClearActive()
		}
	}()
	t.run()
}

func (t *wt) run() {
	k := func(env []string, args ...string) string { o, _ := t.kv(env, args...); return o }
	t.say("")
	t.say("  %s  %s", tui.Bold(tui.Gradient("WALKTHROUGH", 39, 220)), tui.Gray("how LEAPFROG-R works, every line read back from a real file"))
	t.say("  %s", tui.Gray("open the live page to watch the same database change: it follows each step below"))

	// ---- 1. one write, seven steps ------------------------------------------------------------------------
	base := t.path("base.db")
	k(nil, "put", base, "city", "Mumbai")
	baseSize := t.size(base)
	t.heading("PART 1 · the life of one write:  put city Delhi   (the file already holds city = Mumbai)")
	t.say("  %s", tui.Gray("one committer goroutine runs these steps for one transaction at a time. To prove what each step does, the engine is"))
	t.say("  %s", tui.Gray("killed at the end of it (its own named crash point) and we look at what is left on disk and what recovery returns."))
	t.say("  file before: %d bytes, 1 key, city = %s", baseSize, tui.Bold(t.get(base, "city")))
	t.show(base, "BEFORE: the file holds city = Mumbai", fmt.Sprintf("%d bytes on disk, one frame after the two superblock slots", baseSize))
	t.pause()

	type stepDef struct {
		cp, title, does string
		serve           bool
	}
	steps := []stepDef{
		{"CP-W01", "check it fits", "decide: append, rewrite the file, or answer ERR FULL. Nothing is written yet.", false},
		{"CP-W02", "repair a torn tail, if there is one", "a half-written frame from an earlier crash is cut off before anything new is appended.", false},
		{"CP-W03", "write the frame to disk", "header, key, value, CRC checksums, end marker. Killed halfway, a torn prefix is left in the file.", false},
		{"CP-W04", "the whole frame is written, fsync not yet returned", "the frame is in the file but not yet forced to the disk.", false},
		{"CP-W05", "fsync returned: DURABLE", "the durability point. From here the write survives any crash, even though nobody has been told yet.", false},
		{"CP-W06", "update the in-memory index", "readers can now see the new value; the ACK has not been printed yet.", false},
		{"CP-W07", "print ACK n", "the client is told OK. After this the database owes the client that write forever.", true},
	}
	var tornDB string
	for i, s := range steps {
		t.n = i + 1
		work := t.path(fmt.Sprintf("step%d.db", i+1))
		note := ""
		switch s.cp {
		case "CP-W02":
			// Needs a torn tail to repair: take the file CP-W03 leaves behind, then let a normal put repair it.
			torn := t.path("torn.db")
			t.copyDB(base, torn)
			k([]string{"KVDB_CRASH=CP-W03:1"}, "put", torn, "city", "Delhi")
			tornDB = torn
			t.copyDB(torn, work)
			_, tailBefore := t.open(work)
			sizeBefore := t.size(work)
			k(nil, "put", work, "city", "Delhi")
			keys, tail := t.open(work)
			note = fmt.Sprintf("before: %d bytes, tail=%s  ->  after a normal put: %d bytes, tail=%s, keys=%s, city = %s",
				sizeBefore, tailBefore, t.size(work), tail, keys, t.get(work, "city"))
		case "CP-W07":
			t.copyDB(base, work)
			cmd := exec.Command(t.h.e.kvdb, "serve", work)
			cmd.Env = append(os.Environ(), "KVDB_CRASH=CP-W07:1")
			cmd.Stdin = strings.NewReader("put city Delhi\n")
			out, _ := cmd.CombinedOutput()
			acked := strings.Contains(string(out), "ACK 1")
			keys, tail := t.open(work)
			note = fmt.Sprintf("client saw %q, then the process died.  On disk: %d bytes, tail=%s, keys=%s, city = %s",
				map[bool]string{true: "ACK 1", false: "no ACK"}[acked], t.size(work), tail, keys, t.get(work, "city"))
		default:
			t.copyDB(base, work)
			k([]string{"KVDB_CRASH=" + s.cp + ":1"}, "put", work, "city", "Delhi")
			keys, tail := t.open(work)
			note = fmt.Sprintf("killed here.  On disk: %d bytes (%+d), tail=%s, keys=%s, city = %s",
				t.size(work), t.size(work)-baseSize, tail, keys, t.get(work, "city"))
		}
		t.say("")
		t.say("  %s  %s  %s", tui.Bold(tui.Yellow(fmt.Sprintf("STEP %d of 7", i+1))), tui.Bold(s.title), tui.Gray("["+s.cp+"]"))
		t.say("    %s", s.does)
		t.say("    %s", tui.Green(note))
		t.show(work, fmt.Sprintf("STEP %d of 7: %s  (%s)", i+1, s.title, s.cp), s.does, note)
		t.pause()
	}
	_ = tornDB
	t.say("")
	t.say("  %s", tui.Bold("reading it:"))
	t.say("    killed in steps 1-3 (frame missing or torn) the OLD value is what comes back: an unfinished write is simply not there;")
	t.say("    once the whole frame is written (step 4) the NEW value is there, but only from step 5 (fsync) is it guaranteed to survive a power cut;")
	t.say("    step 7 is the promise: an ACK is printed only after step 5. That order is the whole design: %s",
		tui.Bold("write, fsync, publish, ACK, next"))
	t.say("    %s", tui.Gray("(a kill is process death: the OS still has what was written. A power cut at step 4 may lose the frame; recovery handles both.)"))

	// ---- 2. recovery -----------------------------------------------------------------------------------------
	t.heading("PART 2 · recovery: opening a damaged file writes nothing")
	torn := t.path("recover.db")
	t.copyDB(base, torn)
	k([]string{"KVDB_CRASH=CP-W03:1"}, "put", torn, "city", "Delhi")
	before := t.digest(torn)
	keys, tail := t.open(torn)
	chk := k(nil, "check", torn)
	after := t.digest(torn)
	t.say("  file with a torn tail: tail=%s, keys=%s, check=%s", tail, keys, chk)
	t.say("  file fingerprint before open %s, after open %s  ->  %s", before, after,
		map[bool]string{true: tui.Green("identical: recovery wrote no bytes"), false: tui.Red("CHANGED")}[before == after])
	t.say("    %s", tui.Gray("the engine reads the file, finds the last valid frame by its CRCs, and ignores what follows. The torn bytes are only"))
	t.say("    %s", tui.Gray("removed when the next write comes (step 2 above), so a crash during recovery cannot damage anything."))
	t.show(torn, "RECOVERY: open reads, validates, ignores the torn tail, and writes no bytes",
		fmt.Sprintf("tail=%s, keys=%s, fingerprint before %s / after %s", tail, keys, before, after))
	t.pause()

	// ---- 3. atomic batch --------------------------------------------------------------------------------------
	t.heading("PART 3 · a batch is all or nothing")
	ops := t.path("ops.txt")
	_ = os.WriteFile(ops, []byte("put a 1\nput b 2\nput c 3\n"), 0o644)
	cut := t.path("batch-killed.db")
	t.copyDB(base, cut)
	k([]string{"KVDB_CRASH=CP-W03:1"}, "batch", cut, ops)
	okDB := t.path("batch-ok.db")
	t.copyDB(base, okDB)
	k(nil, "batch", okDB, ops)
	dumpKilled := strings.ReplaceAll(k(nil, "dump", cut), "\n", " | ")
	dumpOK := strings.ReplaceAll(k(nil, "dump", okDB), "\n", " | ")
	t.say("  batch of three puts killed while its frame is being written:  %s", tui.Bold(dumpKilled))
	t.say("  the same batch allowed to finish:                             %s", tui.Bold(dumpOK))
	t.say("    %s", tui.Gray("the three puts live in ONE frame with ONE checksum, so a reader or a crash sees either none of them or all three."))
	t.show(cut, "BATCH killed halfway: none of the three keys exist", "one frame, one checksum: "+dumpKilled)
	t.pause()
	t.show(okDB, "BATCH finished: all three keys exist", dumpOK)
	t.pause()

	// ---- 4. kill -9 during a flood of writes ----------------------------------------------------------------
	t.heading("PART 4 · kill -9 in the middle of a flood of writes")
	flood := t.path("flood.db")
	var in strings.Builder
	for i := 1; i <= 20000; i++ {
		fmt.Fprintf(&in, "put key%d value%d\n", i, i)
	}
	cmd := exec.Command(t.h.e.kvdb, "serve", flood)
	cmd.Stdin = strings.NewReader(in.String())
	pr, pw, _ := os.Pipe()
	cmd.Stdout = pw
	_ = cmd.Start()
	_ = pw.Close()
	done := make(chan string)
	go func() { b, _ := io.ReadAll(pr); done <- string(b) }()
	time.Sleep(350 * time.Millisecond)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	out := <-done
	acks := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "ACK ") {
			if n, err := strconv.Atoi(strings.TrimPrefix(l, "ACK ")); err == nil && n > acks {
				acks = n
			}
		}
	}
	keys, tail = t.open(flood)
	nk, _ := strconv.Atoi(keys)
	verdict := tui.Green("every acknowledged write is on disk")
	if nk < acks {
		verdict = tui.Red("ACKED WRITES MISSING")
	}
	t.say("  SIGKILL after 350 ms: the client had been told OK for %d writes; after restart the file holds %d keys, tail=%s, check=%s",
		acks, nk, tail, k(nil, "check", flood))
	t.say("    keys on disk >= ACKs: %s  %s", verdict, tui.Gray("(a few unacknowledged writes may also have made it: allowed)"))
	t.show(flood, fmt.Sprintf("KILL -9 during a flood: %d writes acknowledged, %d keys on disk", acks, nk),
		"every ACKed write must be on disk; the tail may be torn, recovery ignores it")
	t.pause()

	// ---- 5. compaction ------------------------------------------------------------------------------------------
	t.heading("PART 5 · deleting leaves dead frames; compaction rewrites only what is live")
	cmp := t.path("compact.db")
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "put k%d %s\n", i, strings.Repeat("v", 60))
	}
	for i := 1; i <= 290; i++ {
		fmt.Fprintf(&b, "del k%d\n", i)
	}
	cmd = exec.Command(t.h.e.kvdb, "serve", cmp)
	cmd.Stdin = strings.NewReader(b.String())
	_, _ = cmd.CombinedOutput()
	grown := t.size(cmp)
	k0, _ := t.open(cmp)
	t.say("  after 300 puts and 290 deletes: %d keys live, file is %d bytes", mustInt(k0), grown)
	t.show(cmp, "BEFORE COMPACT: 10 live keys but a big file", fmt.Sprintf("%d bytes on disk", grown))
	t.pause()
	cmd = exec.Command(t.h.e.kvdb, "serve", cmp)
	cmd.Stdin = strings.NewReader("compact\n")
	_, _ = cmd.CombinedOutput()
	k1, _ := t.open(cmp)
	t.say("  after compact: %d keys live, file is %d bytes  (%s)", mustInt(k1), t.size(cmp),
		map[bool]string{true: tui.Green("it shrank"), false: tui.Red("did not shrink")}[t.size(cmp) < grown])
	t.say("    %s", tui.Gray("a new file is built beside the old one, fsynced, then renamed over it: a kill at any moment leaves the old file or the new one, never a mix."))
	t.show(cmp, "AFTER COMPACT: same 10 keys, small file", fmt.Sprintf("%d bytes -> %d bytes", grown, t.size(cmp)))
	t.pause()

	envshell.SetVerdict(envshell.Verdict{Kind: "pass", Title: "WALKTHROUGH DONE: that is the whole design, read back from real files",
		Lines: []string{"write, fsync, publish, ACK, next; recovery writes nothing; batches are one frame; compaction swaps whole files"}})
	t.say("")
	t.say("  %s  %s", tui.OK(), "done. `failure` / `success` show a broken engine failing the same idea; `--mode sweep` kills it at all 17 crash points.")
	t.say("")
}

func mustInt(s string) int { n, _ := strconv.Atoi(s); return n }
