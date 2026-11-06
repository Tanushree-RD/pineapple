package main

import (
	"os"
	"testing"
)

func TestParseFlagsDefaults(t *testing.T) {
	cfg, err := parseFlags([]string{"--seed", "42"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Iterations != 500 || cfg.Mode != "default" || cfg.Writers != 3 || cfg.Seed != 42 || cfg.SeedPicked {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
}

func TestParseFlagsPicksSeedWhenMissing(t *testing.T) {
	cfg, err := parseFlags(nil, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SeedPicked || cfg.Seed == 0 {
		t.Fatalf("a missing seed must be picked and flagged: %+v", cfg)
	}
}

func TestParseFlagsSeedZeroIsExplicit(t *testing.T) {
	cfg, err := parseFlags([]string{"--seed", "0"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SeedPicked || cfg.Seed != 0 {
		t.Fatalf("--seed 0 is a real seed: %+v", cfg)
	}
}

func TestParseFlagsValidation(t *testing.T) {
	bad := [][]string{
		{"--seed", "1", "--writers", "1"},
		{"--seed", "1", "--writers", "5"},
		{"--seed", "1", "--iterations", "0"},
		{"--seed", "1", "--max-bytes", "-1"},
		{"--seed", "1", "stray"},
	}
	for _, args := range bad {
		if _, err := parseFlags(args, os.Stderr); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestUnknownModeIsAnError(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	if code := run([]string{"--seed", "1", "--mode", "no-such-mode"}, devnull, devnull); code != 2 {
		t.Fatalf("unknown mode must exit 2, got %d", code)
	}
}

func TestWorkBaseIsAnExistingDirectoryAndPrefersARealDisk(t *testing.T) {
	b := workBase()
	st, err := os.Stat(b)
	if err != nil || !st.IsDir() {
		t.Fatalf("workBase() = %q: %v", b, err)
	}
	// If the system temp dir is RAM and a user cache dir exists, the default must not be RAM.
	if onTmpfs(os.TempDir()) {
		if c, err := os.UserCacheDir(); err == nil && !onTmpfs(c) && onTmpfs(b) {
			t.Fatalf("the system temp dir is tmpfs but workBase() still returned RAM: %s", b)
		}
	}
}

func TestRecoveryCrashMustActuallyKillDuringRecovery(t *testing.T) {
	if recoveryVerdict(0, 40) == "" {
		t.Fatal("0 kills landed: that run must FAIL, not pass")
	}
	if recoveryVerdict(0, 0) == "" {
		t.Fatal("no kills attempted must FAIL")
	}
	if recoveryVerdict(9, 40) == "" {
		t.Fatal("9 of 40 landing is too few")
	}
	if recoveryVerdict(20, 40) != "" || recoveryVerdict(175, 187) != "" || recoveryVerdict(40, 40) != "" {
		t.Fatal("half or more landing must pass")
	}
}
