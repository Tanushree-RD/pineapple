// Command torture is HYDRA: it SIGKILLs the database at random moments and proves, against its
// own independent oracle, that nothing acknowledged was lost (SPEC 14, 15).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config is the parsed command line (SPEC 15.1).
type Config struct {
	Iterations int
	Seed       uint64
	SeedPicked bool // true when --seed was missing and we chose one (it is printed)
	Mode       string
	KVDB       string
	KVDBSet    bool // --kvdb was given explicitly
	WorkDir    string
	Writers    int
	MaxBytes   int64
	Keep       bool
	Replay     string
	CP         string
	Big        bool
	Coverage   bool
	Hits       int // sweep: hit counts 1..Hits per crash point
}

// modes maps --mode to its runner. Each runner returns the process exit code.
// Modes are added stage by stage (MASTER_PROMPT 9.B); an unknown or unfinished mode is an error,
// never a silent pass.
var modes = map[string]func(*Config) int{}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr *os.File) int {
	cfg, err := parseFlags(args, stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintf(stderr, "torture: %v\n", err)
		return 2
	}
	if cfg.SeedPicked {
		fmt.Fprintf(stdout, "no --seed given, using seed=%d (pass --seed %d to reproduce)\n", cfg.Seed, cfg.Seed)
	}

	switch {
	case cfg.Replay != "":
		return runReplay(cfg)
	case cfg.Coverage:
		return runCoverage(cfg)
	}
	runner, ok := modes[cfg.Mode]
	if !ok {
		fmt.Fprintf(stderr, "torture: mode %q is not implemented in this build\n", cfg.Mode)
		return 2
	}
	return runner(cfg)
}

func parseFlags(args []string, stderr *os.File) (*Config, error) {
	fs := flag.NewFlagSet("torture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfg := &Config{}
	var seed int64
	fs.IntVar(&cfg.Iterations, "iterations", 500, "number of kill/verify iterations")
	fs.Int64Var(&seed, "seed", 0, "master seed (required for reproducibility; if missing one is picked and printed)")
	fs.StringVar(&cfg.Mode, "mode", "default", "default | large-values | large-batches | recovery-crash | compaction | disk-full | sweep | micro | fixtures")
	fs.StringVar(&cfg.KVDB, "kvdb", "./bin/kvdb", "path to the kvdb binary under test")
	fs.StringVar(&cfg.WorkDir, "workdir", "", "working directory (default: a fresh temporary directory)")
	fs.IntVar(&cfg.Writers, "writers", 3, "concurrent writer lanes (2 to 4)")
	fs.Int64Var(&cfg.MaxBytes, "max-bytes", 0, "size cap for disk-full mode (0 = mode default)")
	fs.BoolVar(&cfg.Keep, "keep", false, "keep the working directory after the run")
	fs.StringVar(&cfg.Replay, "replay", "", "re-run the check on a saved failures/<seed>-<iter> directory")
	fs.StringVar(&cfg.CP, "cp", "", "sweep mode: only this crash point (for example CP-W03)")
	fs.BoolVar(&cfg.Big, "big", false, "recovery-crash mode: 1,000,000 keys instead of 200,000")
	fs.IntVar(&cfg.Hits, "hits", 6, "sweep mode: try hit counts 1..N of each crash point")
	fs.BoolVar(&cfg.Coverage, "coverage", false, "print the behavior-coverage table")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	seedSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "seed":
			seedSet = true
		case "kvdb":
			cfg.KVDBSet = true
		}
	})
	if seedSet {
		cfg.Seed = uint64(seed)
	} else {
		cfg.Seed = uint64(time.Now().UnixNano()) & 0x7fffffffffffffff
		cfg.SeedPicked = true
	}

	if cfg.Iterations < 1 {
		return nil, fmt.Errorf("--iterations must be at least 1")
	}
	if cfg.Writers < 2 || cfg.Writers > 4 {
		return nil, fmt.Errorf("--writers must be between 2 and 4, got %d", cfg.Writers)
	}
	if cfg.Hits < 1 {
		return nil, fmt.Errorf("--hits must be at least 1")
	}
	if cfg.MaxBytes < 0 {
		return nil, fmt.Errorf("--max-bytes must not be negative")
	}
	if abs, err := filepath.Abs(cfg.KVDB); err == nil {
		cfg.KVDB = abs
	}
	return cfg, nil
}

// runCoverage is filled in by stage H5.
func runCoverage(cfg *Config) int {
	fmt.Fprintln(os.Stderr, "torture: --coverage is not implemented in this build")
	return 2
}
