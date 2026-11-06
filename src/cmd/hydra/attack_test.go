package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func st(kv ...string) state {
	s := state{}
	for i := 0; i+1 < len(kv); i += 2 {
		s[kv[i]] = kv[i+1]
	}
	return s
}

// The verifier must accept every legal outcome of a kill and reject every illegal one. Each rejection case is a
// mutation of a legal state, so a verifier that always says "fine" fails this test.
func TestVerifyAfterKillAcceptsLegalAndRejectsIllegal(t *testing.T) {
	original := st("student:1", "Asha", "student:2", "Ravi")
	acked := st("student:1", "Asha", "student:2", "Ravi", "hydra:1:1", "a")
	inflight := []attackOp{
		{kind: 'p', key: "hydra:1:2", val: "b"},
		{kind: 'p', key: "hydra:1:3", val: "c"},
	}
	legal := []state{
		acked,
		st("student:1", "Asha", "student:2", "Ravi", "hydra:1:1", "a", "hydra:1:2", "b"),
		st("student:1", "Asha", "student:2", "Ravi", "hydra:1:1", "a", "hydra:1:2", "b", "hydra:1:3", "c"),
	}
	for i, g := range legal {
		if msg := verifyAfterKill(original, acked, inflight, g); msg != "" {
			t.Errorf("legal state %d rejected: %s", i, msg)
		}
	}
	illegal := map[string]state{
		"original record missing":        st("student:1", "Asha", "hydra:1:1", "a"),
		"original record changed":        st("student:1", "Asha", "student:2", "RAVI", "hydra:1:1", "a"),
		"acknowledged op lost":           st("student:1", "Asha", "student:2", "Ravi"),
		"out-of-order (3rd without 2nd)": st("student:1", "Asha", "student:2", "Ravi", "hydra:1:1", "a", "hydra:1:3", "c"),
		"unknown extra record":           st("student:1", "Asha", "student:2", "Ravi", "hydra:1:1", "a", "hydra:9:9", "x"),
		"acknowledged value garbled":     st("student:1", "Asha", "student:2", "Ravi", "hydra:1:1", "A"),
	}
	for name, g := range illegal {
		if msg := verifyAfterKill(original, acked, inflight, g); msg == "" {
			t.Errorf("%s: the verifier accepted it", name)
		}
	}
}

func TestTransactionIsOneAtomicStep(t *testing.T) {
	o := attackOp{kind: 't', ts: []attackOp{{kind: 'd', key: "k"}, {kind: 'p', key: "k", val: "v"}}}
	s := st("k", "v")
	o.apply(s)
	if s["k"] != "v" {
		t.Fatal("delete+reinsert must leave the value")
	}
	if got := strings.Join(o.lines(), "|"); !strings.HasPrefix(got, "begin|del ") || !strings.HasSuffix(got, "|commit") {
		t.Fatal(got)
	}
}

func TestAttackAgainstTheRealDatabaseKeepsTheDataIdentical(t *testing.T) {
	kvdb, err := filepath.Abs("../../../bin/kvdb")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kvdb); err != nil {
		t.Skip("bin/kvdb not built: run `make build`")
	}
	db := filepath.Join(t.TempDir(), "s.db")
	for _, kv := range [][2]string{{"student:1", "name=Asha;marks=88"}, {"student:2", "name=Ravi with spaces;marks=74"}, {"config:x", "tab\there"}} {
		if out, err := exec.Command(kvdb, "put", db, kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	before, _ := exec.Command(kvdb, "dump", db).Output()
	var out bytes.Buffer
	res := attack(kvdb, db, 6, 42, &out)
	if res.Err != "" {
		t.Fatalf("%s\n%s", res.Err, out.String())
	}
	if res.Kills != 6 || res.Acked == 0 {
		t.Fatalf("expected 6 kills with acknowledged work: %+v", res)
	}
	after, _ := exec.Command(kvdb, "dump", db).Output()
	if string(before) != string(after) {
		t.Fatalf("data changed:\n%s\n--- now\n%s", before, after)
	}
	if chk, err := exec.Command(kvdb, "check", db).CombinedOutput(); err != nil || !strings.HasPrefix(string(chk), "OK") {
		t.Fatalf("check: %v %s", err, chk)
	}
	if r := attack(kvdb, filepath.Join(t.TempDir(), "nope.db"), 1, 1, &out); r.Err == "" {
		t.Fatal("a missing database must be an error, not a silent pass")
	}
}

func TestEnvironmentSendsRawFlagsToTortureAndSwitches(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.txt")
	fake := filepath.Join(dir, "torture")
	script := "#!/bin/sh\necho \"$@\" > " + log + "\necho 'PASS: fake torture'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Env{torture: fake, kvdb: "/bin/true", runDir: dir, seed: "123"}
	var out bytes.Buffer
	switched := ""
	h := &henv{e: e, out: &out, in: bufio.NewReader(strings.NewReader("")), port: 1}
	h.leapfrog = func(db string) error { switched = db; return nil }
	h.db = "students.db"
	if h.exec("./bin/torture --iterations 20 --seed 999 --mode large-values") {
		t.Fatal("running torture must not end the environment")
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--iterations 20 --seed 999 --mode large-values") {
		t.Fatalf("torture got: %s", b)
	}
	if !h.exec("/leapfrog") || switched != "students.db" {
		t.Fatalf("/leapfrog must end this environment and pass the database on (switched=%q)", switched)
	}
	if h.exec("--bogus-line-without-torture \"unterminated") {
		t.Fatal("a bad line must not end the environment")
	}
}

func TestQuickFlagAppliesToOneCommandOnly(t *testing.T) {
	h := &henv{e: &Env{}}
	undo := h.quickFor([]string{"--quick"})
	if !h.e.quick {
		t.Fatal("`all --quick` must turn quick mode on")
	}
	undo()
	if h.e.quick {
		t.Fatal("quick mode must go back off after the command")
	}
	h.quickFor(nil)()
	if h.e.quick {
		t.Fatal("no flag, no quick mode")
	}
}
