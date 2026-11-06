package main

// --replay <failures/seed-iter>: re-run the check on the files a failing run saved (SPEC 15.1).
//
// It rebuilds the baseline from the OBS records of the iterations that passed before the failing
// one, re-reads the saved database image through `kvdb dump`, and runs the same checker. Because
// every input is a saved file, the verdict is deterministic: it reproduces the failure exactly,
// or shows that a fixed database no longer fails on that crash image.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type failureMeta struct {
	Seed      uint64   `json:"seed"`
	Iteration int      `json:"iteration"`
	Mode      string   `json:"mode"`
	KillMS    int      `json:"kill_ms"`
	KVDB      string   `json:"kvdb"`
	Problems  []string `json:"problems"`
}

func runReplay(cfg *Config) int {
	dir := cfg.Replay
	raw, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	var meta failureMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		fmt.Fprintf(os.Stderr, "torture: bad metadata.json: %v\n", err)
		return 2
	}
	kvdb := cfg.KVDB
	if !cfg.KVDBSet && meta.KVDB != "" {
		if _, err := os.Stat(meta.KVDB); err == nil {
			kvdb = meta.KVDB
		}
	}

	lf, err := os.Open(filepath.Join(dir, "oracle.log"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	its, err := ParseOracleLog(lf)
	lf.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}

	// Baseline = the verified state after the last passing iteration before the failing one.
	base := Baseline{}
	prior := map[uint64]OpState{}
	var failing *LoggedIteration
	for _, it := range its {
		if it.Iter == meta.Iteration {
			failing = it
			break
		}
		for _, op := range it.Ops {
			if op.State == StateRejected {
				prior[op.ID] = StateRejected
			} else {
				prior[op.ID] = StateDead
			}
		}
		for k, st := range it.Obs {
			if st.Kind == KeyAbsent {
				delete(base, k)
			} else {
				base[k] = st
			}
		}
	}
	if failing == nil {
		fmt.Fprintf(os.Stderr, "torture: iteration %d not found in %s/oracle.log\n", meta.Iteration, dir)
		return 2
	}

	// Dump a COPY so that the saved crash image (and the lock file) are never touched.
	tmp, err := os.MkdirTemp("", "hydra-replay-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "torture: %v\n", err)
		return 2
	}
	defer os.RemoveAll(tmp)
	image := filepath.Join(tmp, "database.db")
	if err := copyFile(filepath.Join(dir, "database.db"), image); err != nil {
		fmt.Fprintf(os.Stderr, "torture: cannot copy the saved database: %v\n", err)
		return 2
	}

	fmt.Printf("replay %s: seed=%d iter=%d mode=%s kill_ms=%d, baseline %d keys, %d ops\n",
		dir, meta.Seed, meta.Iteration, meta.Mode, meta.KillMS, len(base), len(failing.Ops))
	if len(meta.Problems) > 0 {
		fmt.Println("recorded when the run failed:")
		for _, p := range meta.Problems {
			fmt.Println("  - " + p)
		}
	}

	var problems []string
	out, errOut, code, err := runKVDB(kvdb, 2*time.Minute, "check", image)
	switch {
	case err != nil:
		problems = append(problems, "kvdb check: "+err.Error())
	case code != 0 || !strings.HasPrefix(out, "OK"):
		problems = append(problems, fmt.Sprintf("kvdb check failed (exit %d): %s", code, truncate(strings.TrimSpace(out+errOut), 300)))
	}
	out, errOut, code, err = runKVDB(kvdb, 2*time.Minute, "dump", image)
	if err != nil || code != 0 {
		problems = append(problems, fmt.Sprintf("kvdb dump failed (exit %d): %v %s", code, err, truncate(strings.TrimSpace(errOut), 300)))
		return replayVerdict(problems, nil)
	}
	dump, perr := ParseDump(strings.NewReader(out))
	if perr != nil {
		problems = append(problems, "kvdb dump output is malformed: "+perr.Error())
		return replayVerdict(problems, nil)
	}

	res := CheckIteration(base, failing.Ops, dump, func(id uint64) (OpState, bool) {
		st, ok := prior[id]
		return st, ok
	})
	return replayVerdict(problems, res)
}

func replayVerdict(problems []string, res *CheckResult) int {
	bad := len(problems) > 0 || (res != nil && !res.OK())
	if !bad {
		fmt.Println("REPLAY PASS: the saved crash image satisfies every rule (the failure does not reproduce with this kvdb)")
		return 0
	}
	fmt.Println("REPLAY FAIL: the failure reproduces")
	for _, p := range problems {
		fmt.Println("  - " + p)
	}
	if res != nil {
		fmt.Printf("  oracle: acked=%d inflight=%d rejected=%d\n", res.Acked, res.InFlight, res.Rejected)
		for i, f := range res.Failures {
			if i == maxFailuresPrinted {
				fmt.Printf("  ... and %d more violations\n", len(res.Failures)-i)
				break
			}
			fmt.Println("  * " + strings.ReplaceAll(f.String(), "\n", "\n    "))
		}
	}
	return 1
}
