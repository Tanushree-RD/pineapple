package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Integration tests: run the real harness against testdata/fakekvdb, a test double with
// switchable bugs. A correct fake must PASS; every planted bug must FAIL. If a planted bug
// passes, the oracle is blind to it and HYDRA is not trustworthy (SPEC 15.6, MASTER_PROMPT H1).

func buildFake(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakekvdb")
	out, err := exec.Command("go", "build", "-o", bin, "./testdata/fakekvdb").CombinedOutput()
	if err != nil {
		t.Fatalf("building fakekvdb: %v\n%s", err, out)
	}
	return bin
}

// runHarness runs the default mode against the fake in the given FAKE_MODE and returns the exit code.
// It works in a scratch directory so that failures/ never lands in the repository.
func runHarness(t *testing.T, bin, fakeMode string, seed uint64, iterations int) int {
	t.Helper()
	t.Setenv("FAKE_MODE", fakeMode)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	cfg := &Config{Iterations: iterations, Seed: seed, Mode: "default", KVDB: bin, Writers: 3,
		WorkDir: filepath.Join(t.TempDir(), "work")}
	return runRandomKill(cfg, DefaultParams(), nil)
}

func TestHarnessPassesCorrectFake(t *testing.T) {
	bin := buildFake(t)
	if code := runHarness(t, bin, "durable", 1, 15); code != 0 {
		t.Fatalf("a correct database must PASS, got exit %d", code)
	}
}

func TestHarnessCatchesBrokenFakes(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	bin := buildFake(t)
	for _, tc := range []struct {
		mode  string
		iters int
	}{
		{"volatile", 6},   // ACKed data vanishes after SIGKILL
		{"droplast", 6},   // newest ACKed record ignored by recovery
		{"resurrect", 8},  // deletes not honored by recovery
		{"nonatomic", 60}, // a batch written one op at a time: a kill lands mid-batch
	} {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			if code := runHarness(t, bin, tc.mode, 7, tc.iters); code != 1 {
				t.Fatalf("FAKE_MODE=%s must make HYDRA FAIL (exit 1), got exit %d: the oracle missed a planted bug", tc.mode, code)
			}
			if _, err := os.Stat(filepath.Join("failures")); err != nil {
				t.Fatalf("a failing run must save a failures/ directory: %v", err)
			}
		})
	}
}
