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
	return runHarnessWith(t, bin, fakeMode, seed, iterations, DefaultParams())
}

func runHarnessWith(t *testing.T, bin, fakeMode string, seed uint64, iterations int, params WorkloadParams) int {
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
	cfg.Mode = params.Profile
	return runRandomKill(cfg, params, nil)
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
		{"garble", 3},     // a corrupt value in the dump
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

func TestHarnessLargeModesAgainstFake(t *testing.T) {
	bin := buildFake(t)
	lv := LargeValuesParams()
	if code := runHarnessWith(t, bin, "durable", 2, 3, lv); code != 0 {
		t.Fatalf("large-values against a correct database: exit %d", code)
	}
	lb := LargeBatchesParams()
	lb.BigOps, lb.StreamThreshold = 500, 512<<10 // scaled down so the test stays fast
	if code := runHarnessWith(t, bin, "durable", 3, 4, lb); code != 0 {
		t.Fatalf("large-batches against a correct database: exit %d", code)
	}
	if testing.Short() {
		return
	}
	if code := runHarnessWith(t, bin, "droplast", 3, 4, lb); code != 1 {
		t.Fatalf("large-batches must catch a database that loses its last ACKed batch, got exit %d", code)
	}
}

func TestReplayReproducesAndClears(t *testing.T) {
	bin := buildFake(t)
	// A failing run in a scratch directory. "garble" corrupts a value in dump output, so it fails
	// deterministically at iteration 1, when the baseline is still empty.
	if code := runHarness(t, bin, "garble", 7, 4); code != 1 {
		t.Fatalf("garble must fail, got %d", code)
	}
	dirs, err := filepath.Glob("failures/7-1")
	if err != nil || len(dirs) != 1 {
		t.Fatalf("expected one failure directory, got %v (%v)", dirs, err)
	}
	for _, f := range []string{"database.db", "oracle.log", "operations.log", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(dirs[0], f)); err != nil {
			t.Fatalf("failure directory is missing %s: %v", f, err)
		}
	}
	cfg := &Config{Replay: dirs[0], KVDB: bin, KVDBSet: true}

	// Same buggy database: the failure reproduces.
	t.Setenv("FAKE_MODE", "garble")
	if code := runReplay(cfg); code != 1 {
		t.Fatalf("replay with the buggy database must reproduce the failure, got %d", code)
	}
	// "Fixed" database on the very same crash image: the replay passes.
	t.Setenv("FAKE_MODE", "durable")
	if code := runReplay(cfg); code != 0 {
		t.Fatalf("replay with a correct database must pass, got %d", code)
	}
}

func TestReplayRebuildsBaselineFromEarlierIterations(t *testing.T) {
	bin := buildFake(t)
	// resurrect fails only once deletes happen against existing keys; give it a few iterations
	// so the failing iteration has a non-empty baseline built from OBS records.
	t.Setenv("FAKE_MODE", "durable")
	wd, _ := os.Getwd()
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	cfg := &Config{Iterations: 3, Seed: 11, Mode: "default", KVDB: bin, Writers: 3, WorkDir: filepath.Join(scratch, "w")}
	if code := runRandomKill(cfg, DefaultParams(), nil); code != 0 {
		t.Fatalf("clean run failed: %d", code)
	}
	f, err := os.Open(filepath.Join(scratch, "w", "oracle.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	its, err := ParseOracleLog(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(its) != 3 || len(its[0].Obs) == 0 || len(its[1].Obs) == 0 {
		t.Fatalf("every passing iteration must log OBS records: %d iterations, obs0=%d", len(its), len(its[0].Obs))
	}
}

func TestCompactionModeAgainstFake(t *testing.T) {
	bin := buildFake(t)
	t.Setenv("FAKE_MODE", "durable")
	wd, _ := os.Getwd()
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	cfg := &Config{Iterations: 12, Seed: 21, Mode: "compaction", KVDB: bin, Writers: 3, WorkDir: filepath.Join(scratch, "w")}
	// Iteration 10 is the shrink iteration; the fake's COMPACT really rewrites the log, so it must pass.
	if code := runRandomKillOpts(cfg, DefaultParams(), nil, true); code != 0 {
		t.Fatalf("compaction mode against a correct database: exit %d", code)
	}
}

func TestCompactionModeCatchesAFileThatDoesNotShrink(t *testing.T) {
	bin := buildFake(t)
	t.Setenv("FAKE_MODE", "noshrink")
	wd, _ := os.Getwd()
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	cfg := &Config{Iterations: 10, Seed: 21, Mode: "compaction", KVDB: bin, Writers: 3, WorkDir: filepath.Join(scratch, "w")}
	if code := runRandomKillOpts(cfg, DefaultParams(), nil, true); code != 1 {
		t.Fatalf("a COMPACT that does not shrink the file must FAIL iteration 10, got exit %d", code)
	}
}

// The real engine and CLI must satisfy the whole recovery matrix (SPEC 15.8).
func TestFixturesAgainstRealKVDB(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kvdb")
	out, err := exec.Command("go", "build", "-o", bin, "../src/cmd/kvdb").CombinedOutput()
	if err != nil {
		t.Fatalf("building the real kvdb: %v\n%s", err, out)
	}
	cfg := &Config{KVDB: bin, WorkDir: filepath.Join(t.TempDir(), "fx")}
	if code := runFixtures(cfg); code != 0 {
		t.Fatalf("recovery fixtures failed against the real kvdb (exit %d)", code)
	}
}

func buildRealKVDB(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kvdb")
	out, err := exec.Command("go", "build", "-o", bin, "../src/cmd/kvdb").CombinedOutput()
	if err != nil {
		t.Fatalf("building the real kvdb: %v\n%s", err, out)
	}
	return bin
}

// Short end-to-end runs of the modes that need the real engine. The long gate runs are done by hand.
func TestRealKVDBModes(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	bin := buildRealKVDB(t)
	for _, tc := range []struct {
		mode  string
		iters int
	}{
		{"default", 8},
		{"compaction", 11},    // includes the shrink iteration (10)
		{"disk-full", 10},     // includes two ERR FULL probes (5 and 10)
		{"recovery-crash", 3}, // builds 200,000 keys, then kills `kvdb open`
		{"large-values", 3},
		{"large-batches", 5},
		{"sweep", 1}, // every crash point of SPEC 12, hit counts 1..2
		{"micro", 1}, // the 11 traces; narrowed to CP-W05 below to keep the test short
		{"fixtures", 1},
	} {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			wd, _ := os.Getwd()
			scratch := t.TempDir()
			if err := os.Chdir(scratch); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.Chdir(wd) }()
			cfg := &Config{Iterations: tc.iters, Seed: 13, Mode: tc.mode, KVDB: bin, Writers: 3, Hits: 2, WorkDir: filepath.Join(scratch, "w")}
			if tc.mode == "micro" {
				cfg.CP = "CP-W05"
			}
			run := modes[tc.mode]
			if run == nil {
				t.Fatalf("mode %q is not registered", tc.mode)
			}
			if code := run(cfg); code != 0 {
				t.Fatalf("mode %s failed against the real database (exit %d)", tc.mode, code)
			}
		})
	}
}

func TestCoverageNeedsDataAndFlagsGaps(t *testing.T) {
	wd, _ := os.Getwd()
	scratch := t.TempDir()
	if err := os.Chdir(scratch); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if code := runCoverage(&Config{}); code != 2 {
		t.Fatalf("no coverage data must exit 2, got %d", code)
	}
	// A sweep that only ever hit CP-W01 leaves every other transition at zero hits.
	if err := os.MkdirAll("failures", 0o755); err != nil {
		t.Fatal(err)
	}
	partial := `{"sweep":[{"scenario":"write","cp":"CP-W01","n":1,"reached":true}]}`
	if err := os.WriteFile(filepath.Join("failures", "coverage-sweep.json"), []byte(partial), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runCoverage(&Config{}); code != 1 {
		t.Fatalf("a partial run must report gaps and exit 1, got %d", code)
	}
}
