package engine

// Engine-level crash sweep (the in-package cousin of HYDRA's sweep mode, SPEC 15.2).
//
// The test binary re-executes itself as a child (TestCrashHelper). The child runs a fixed,
// deterministic workload with KVDB_CRASH=<cp>:<n>, so it SIGKILLs itself on the n-th hit of
// a crash point, after writing a seeded torn prefix at write points. The parent then reopens
// the file and checks the SPEC 1 invariant against a model of the workload:
//
//	every op that was acknowledged is present; the one op in flight is fully present or
//	fully absent; nothing else; open() succeeds; check says OK.
//
// KVDB_SWEEP_MAX=N sets how many hits per crash point are swept (default 6).

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

const (
	sweepOps  = 70
	sweepCap  = 40_000
	sweepCkpt = 3000
)

// ---------- the workload, shared by the child and by the parent's model ----------

type step struct {
	compact bool
	ops     []Op
}

func bigVal(i int) []byte { return bytes.Repeat([]byte{byte(i)}, 1500) }

func workloadStep(i int) step {
	k := func(n int) []byte { return []byte(fmt.Sprintf("k%d", n)) }
	switch i % 7 {
	case 0:
		return step{ops: []Op{{Kind: OpPut, Key: k(i % 10), Value: []byte(fmt.Sprintf("v%d", i))}}}
	case 1: // a 3-key batch plus its witness: all or none
		return step{ops: []Op{
			{Kind: OpPut, Key: []byte(fmt.Sprintf("w%da", i)), Value: []byte(fmt.Sprint(i))},
			{Kind: OpPut, Key: []byte(fmt.Sprintf("w%db", i)), Value: []byte(fmt.Sprint(i))},
			{Kind: OpPut, Key: []byte(fmt.Sprintf("w%dc", i)), Value: []byte(fmt.Sprint(i))},
		}}
	case 2:
		return step{ops: []Op{{Kind: OpDelete, Key: k(i % 10)}}}
	case 3: // B5 inside a batch
		return step{ops: []Op{
			{Kind: OpPut, Key: []byte("X"), Value: []byte(fmt.Sprint(i))},
			{Kind: OpDelete, Key: []byte("X")},
			{Kind: OpPut, Key: []byte("Y"), Value: []byte(fmt.Sprint(i))},
		}}
	case 4:
		return step{compact: true}
	default: // 5, 6: 1.5 KB values; enough of them reach the cap and force REWRITE
		return step{ops: []Op{{Kind: OpPut, Key: []byte(fmt.Sprintf("big%d", i%20)), Value: bigVal(i)}}}
	}
}

func applyModel(m map[string]string, s step) {
	for _, op := range s.ops {
		if op.Kind == OpPut {
			m[string(op.Key)] = string(op.Value)
		} else {
			delete(m, string(op.Key))
		}
	}
}

func cloneModel(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// ---------- the child ----------

func TestCrashHelper(t *testing.T) {
	mode := os.Getenv("KVDB_HELPER")
	if mode == "" {
		t.Skip("child process of the crash sweep")
	}
	path := os.Getenv("KVDB_PATH")
	opts := Options{CheckpointBytes: sweepCkpt}
	switch mode {
	case "run":
		opts.MaxBytes, opts.SetMaxBytes = sweepCap, true // fresh create + cap change = CP-S01
		db, err := Open(path, opts)
		if err != nil {
			t.Fatal(err)
		}
		ack, _ := os.OpenFile(os.Getenv("KVDB_ACK"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		for i := 1; i <= sweepOps; i++ {
			s := workloadStep(i)
			var err error
			if s.compact {
				err = db.Compact()
			} else {
				err = db.Batch(s.ops)
			}
			switch {
			case err == nil:
				fmt.Fprintf(ack, "ACK %d\n", i)
			case err == ErrFull:
				fmt.Fprintf(ack, "FULL %d\n", i)
			default:
				t.Fatalf("op %d: %v", i, err)
			}
		}
		db.Close()
	case "open": // recovery crash points: open an existing file, optionally changing the cap
		if os.Getenv("KVDB_NEWCAP") != "" {
			opts.SetMaxBytes = true
			opts.MaxBytes, _ = strconv.ParseInt(os.Getenv("KVDB_NEWCAP"), 10, 64)
		}
		db, err := Open(path, opts)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	case "repair": // first write after open repairs a torn tail / short sentinel (CP-W02)
		db, err := Open(path, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Put([]byte("after-repair"), []byte("1")); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	fmt.Println("DONE")
}

// ---------- the parent ----------

type childResult struct {
	killed bool
	out    string
}

func runChild(t *testing.T, mode, path, ack string, crash string, extra ...string) childResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "KVDB_HELPER="+mode, "KVDB_PATH="+path, "KVDB_ACK="+ack, "KVDB_CRASH="+crash, "KVDB_CRASH_SEED=7")
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	res := childResult{out: string(out)}
	if strings.Contains(res.out, "DONE") {
		return res
	}
	if strings.Contains(res.out, "panic") || strings.Contains(res.out, "--- FAIL") {
		t.Fatalf("child failed (crash=%s): %v\n%s", crash, err, res.out)
	}
	res.killed = true
	return res
}

// readAcks returns the highest acknowledged op, the set of ops refused with FULL.
func readAcks(t *testing.T, ack string) (last int, full map[int]bool) {
	t.Helper()
	full = map[int]bool{}
	b, _ := os.ReadFile(ack)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var kind string
		var n int
		if _, err := fmt.Sscanf(line, "%s %d", &kind, &n); err != nil {
			continue
		}
		if n > last {
			last = n
		}
		if kind == "FULL" {
			full[n] = true
		}
	}
	return last, full
}

func dumpDB(t *testing.T, db *DB) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := db.ForEach(func(k, v []byte) error { m[string(k)] = string(v); return nil }); err != nil {
		t.Fatal(err)
	}
	return m
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// verifyRun checks the invariant after a killed "run" child.
func verifyRun(t *testing.T, label, path, ack string) {
	t.Helper()
	last, full := readAcks(t, ack)
	// Model after the last acknowledged op, and after the one op that may have been in flight.
	model := map[string]string{}
	for i := 1; i <= last; i++ {
		if !full[i] {
			applyModel(model, workloadStep(i))
		}
	}
	next := cloneModel(model)
	if last+1 <= sweepOps {
		applyModel(next, workloadStep(last+1))
	}

	db, err := Open(path, Options{})
	if err != nil {
		t.Fatalf("%s: open() failed after a kill: %v", label, err)
	}
	got := dumpDB(t, db)
	db.Close()
	if !sameMap(got, model) && !sameMap(got, next) {
		t.Fatalf("%s: state after the kill (acked through op %d) matches neither the acked state (%d keys) nor acked+in-flight (%d keys); got %d keys\n%s",
			label, last, len(model), len(next), len(got), diffMaps(got, model))
	}
	rep, err := Check(path)
	if err != nil || !rep.OK {
		t.Fatalf("%s: check after a kill: %+v %v", label, rep, err)
	}
	for _, tmp := range []string{path + ".compact.tmp", path + ".create.tmp"} {
		if _, err := os.Stat(tmp); err == nil {
			t.Fatalf("%s: stale temp %s survived open()", label, tmp)
		}
	}
	// The recovered database must accept writes.
	db, err = Open(path, Options{})
	if err != nil {
		t.Fatalf("%s: second open: %v", label, err)
	}
	if err := db.Put([]byte("post-crash"), []byte("ok")); err != nil && err != ErrFull {
		t.Fatalf("%s: write after recovery: %v", label, err)
	}
	db.Close()
	if rep, _ := Check(path); !rep.OK {
		t.Fatalf("%s: check after post-crash write: %+v", label, rep)
	}
}

func diffMaps(got, want map[string]string) string {
	var b strings.Builder
	n := 0
	for k, v := range want {
		if gv, ok := got[k]; !ok {
			fmt.Fprintf(&b, "  missing %q\n", k)
			n++
		} else if gv != v {
			fmt.Fprintf(&b, "  %q differs\n", k)
			n++
		}
		if n > 8 {
			break
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok && n <= 8 {
			fmt.Fprintf(&b, "  extra %q\n", k)
			n++
		}
	}
	return b.String()
}

func sweepMax() int {
	if s := os.Getenv("KVDB_SWEEP_MAX"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return 6
}

func TestCrashSweepWritePath(t *testing.T) {
	// Every write, compaction, rewrite and checkpoint crash point, hit by hit.
	points := []string{
		"CP-W01", "CP-W03", "CP-W04", "CP-W05", "CP-W06",
		"CP-C01", "CP-C02", "CP-C03", "CP-C04", "CP-C05", "CP-C06",
		"CP-S01",
	}
	killedAny := map[string]int{}
	for _, cp := range points {
		for n := 1; n <= sweepMax(); n++ {
			dir := t.TempDir()
			path, ack := dir+"/s.db", dir+"/ack"
			res := runChild(t, "run", path, ack, fmt.Sprintf("%s:%d", cp, n))
			if !res.killed {
				break // fewer than n hits in this workload
			}
			killedAny[cp]++
			verifyRun(t, fmt.Sprintf("%s hit %d", cp, n), path, ack)
		}
	}
	for _, cp := range points {
		if killedAny[cp] == 0 {
			t.Errorf("crash point %s was never hit by the workload", cp)
		}
	}
	t.Logf("swept: %v", killedAny)
}

// buildBase makes a database by running the whole workload with no crash.
func buildBase(t *testing.T) (path string, model map[string]string) {
	t.Helper()
	dir := t.TempDir()
	path = dir + "/base.db"
	res := runChild(t, "run", path, dir+"/ack", "")
	if res.killed {
		t.Fatalf("baseline run failed:\n%s", res.out)
	}
	last, full := readAcks(t, dir+"/ack")
	model = map[string]string{}
	for i := 1; i <= last; i++ {
		if !full[i] {
			applyModel(model, workloadStep(i))
		}
	}
	return path, model
}

func TestCrashSweepRecoveryIsReadOnlyAndIdempotent(t *testing.T) {
	base, _ := buildBase(t)
	orig, _ := os.ReadFile(base)
	for _, cp := range []string{"CP-R01", "CP-R02", "CP-R03"} {
		dir := t.TempDir()
		path := dir + "/r.db"
		os.WriteFile(path, orig, 0o644)
		res := runChild(t, "open", path, "", cp+":1")
		if !res.killed {
			t.Fatalf("%s never hit", cp)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(orig, after) {
			t.Fatalf("%s: a kill during recovery changed the data file (C1/F2)", cp)
		}
		db, err := Open(path, Options{})
		if err != nil {
			t.Fatalf("%s: reopen after a kill during recovery: %v", cp, err)
		}
		db.Close()
	}
}

func TestCrashSweepCapChangeAtOpen(t *testing.T) {
	// T10: kill while open --max-bytes rewrites the inactive superblock (CP-S01).
	base, model := buildBase(t)
	orig, _ := os.ReadFile(base)
	for seedVariant := 0; seedVariant < 3; seedVariant++ {
		dir := t.TempDir()
		path := dir + "/c.db"
		os.WriteFile(path, orig, 0o644)
		res := runChild(t, "open", path, "", "CP-S01:1", "KVDB_NEWCAP=77777", fmt.Sprintf("KVDB_CRASH_SEED=%d", 11+seedVariant))
		if !res.killed {
			t.Fatal("CP-S01 not hit")
		}
		db, err := Open(path, Options{})
		if err != nil {
			t.Fatalf("open after torn cap change: %v", err)
		}
		if c := db.Stats().MaxBytes; c != sweepCap && c != 77777 {
			t.Fatalf("cap is %d: neither the old (%d) nor the new cap", c, sweepCap)
		}
		got := dumpDB(t, db)
		db.Close()
		if !sameMap(got, model) {
			t.Fatalf("data differs after a torn superblock write\n%s", diffMaps(got, model))
		}
	}
}

func TestCrashSweepTailRepair(t *testing.T) {
	// T9 / CP-W02: a torn tail and a short sentinel, killed at each repair step.
	base, model := buildBase(t)
	orig, _ := os.ReadFile(base)
	cases := map[string][]byte{
		"torn":           append(append([]byte(nil), orig...), []byte("LFR1 garbage that is not a frame")...),
		"short-sentinel": orig[:len(orig)-1],
	}
	for name, content := range cases {
		for n := 1; n <= 2; n++ {
			dir := t.TempDir()
			path := dir + "/t.db"
			os.WriteFile(path, content, 0o644)
			res := runChild(t, "repair", path, "", fmt.Sprintf("CP-W02:%d", n))
			if !res.killed {
				t.Fatalf("%s: CP-W02 hit %d not reached", name, n)
			}
			db, err := Open(path, Options{})
			if err != nil {
				t.Fatalf("%s hit %d: open: %v", name, n, err)
			}
			got := dumpDB(t, db)
			db.Close()
			delete(got, "after-repair")
			// F4: even the short sentinel keeps the acknowledged last write.
			if !sameMap(got, model) {
				t.Fatalf("%s hit %d: data differs after a kill during tail repair\n%s", name, n, diffMaps(got, model))
			}
			if rep, _ := Check(path); !rep.OK {
				t.Fatalf("%s hit %d: %+v", name, n, rep)
			}
		}
	}
}
