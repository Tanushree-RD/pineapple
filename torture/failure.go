package main

// Failure reporting and the saved failure directory (SPEC 15.4):
//
//	failures/<seed>-<iter>/{database.db, oracle.log, operations.log, metadata.json}
//
// oracle.log is the whole run's append-only log, so --replay can rebuild the baseline from its
// OBS records and re-run the check on database.db.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxFailuresPrinted = 8

func (h *Harness) reportFailure(r *IterResult) {
	fmt.Printf("FAIL iter %d/%d seed=%d mode=%s kill_ms=%d\n", r.Iter, h.cfg.Iterations, h.cfg.Seed, h.cfg.Mode, r.KillMS)
	for _, p := range r.Problems {
		fmt.Printf("  - %s\n", p)
	}
	if r.Check != nil {
		fmt.Printf("  oracle: acked=%d inflight=%d rejected=%d keys=%d\n", r.Check.Acked, r.Check.InFlight, r.Check.Rejected, r.Keys)
		for i, f := range r.Check.Failures {
			if i == maxFailuresPrinted {
				fmt.Printf("  ... and %d more violations (see operations.log)\n", len(r.Check.Failures)-i)
				break
			}
			fmt.Println("  * " + strings.ReplaceAll(f.String(), "\n", "\n    "))
		}
	}
	dir, err := h.saveFailure(r)
	if err != nil {
		fmt.Printf("  could not save failure directory: %v\n", err)
	} else {
		fmt.Printf("failure saved to %s\n", dir)
		fmt.Printf("replay the check:  %s --replay %s\n", os.Args[0], dir)
	}
	fmt.Printf("reproduce the run: %s --iterations %d --seed %d --mode %s   (the workload is reproducible; the exact kill instant is not, see SPEC 15.3)\n",
		os.Args[0], r.Iter, h.cfg.Seed, h.cfg.Mode)
}

func (h *Harness) saveFailure(r *IterResult) (string, error) {
	dir := filepath.Join("failures", fmt.Sprintf("%d-%d", h.cfg.Seed, r.Iter))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := copyFile(h.dbPath, filepath.Join(dir, "database.db")); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := copyFile(filepath.Join(h.workdir, "oracle.log"), filepath.Join(dir, "oracle.log")); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "operations.log"), []byte(operationsLog(r)), 0o644); err != nil {
		return "", err
	}
	var violations []string
	if r.Check != nil {
		for _, f := range r.Check.Failures {
			violations = append(violations, f.String())
		}
	}
	meta := map[string]any{
		"seed": h.cfg.Seed, "iteration": r.Iter, "mode": h.cfg.Mode, "kill_ms": r.KillMS,
		"writers": h.cfg.Writers, "kvdb": h.cfg.KVDB, "serve_args_extra": h.serveExtra,
		"keys_observed": r.Keys, "open_ms": r.OpenMS, "tail": r.Tail, "check_output": r.CheckOut,
		"problems": r.Problems, "violations": violations,
		"time": time.Now().Format(time.RFC3339), "go": runtime.Version(), "os": runtime.GOOS + "/" + runtime.GOARCH,
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	return dir, os.WriteFile(filepath.Join(dir, "metadata.json"), append(b, '\n'), 0o644)
}

func operationsLog(r *IterResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# iteration %d, kill after %d ms; %d ops in SENT order\n", r.Iter, r.KillMS, len(r.Ops))
	for _, op := range r.Ops {
		fmt.Fprintf(&sb, "op %d lane=%d %s %s invoke_seq=%d sent@%dns", op.ID, op.Lane, op.Kind, op.State, op.InvokeSeq, op.InvokeNS)
		if op.State == StateAcked {
			fmt.Fprintf(&sb, " ack_seq=%d ack_event=%d ack@%dns", op.AckSeq, op.AckEvent, op.AckNS)
		}
		if op.Reason != "" {
			fmt.Fprintf(&sb, " reason=%s", op.Reason)
		}
		sb.WriteString(" parts:")
		for i, p := range op.Parts {
			if i == 12 {
				fmt.Fprintf(&sb, " ... (%d more)", len(op.Parts)-i)
				break
			}
			if p.Del {
				fmt.Fprintf(&sb, " DEL(%s)", hex.EncodeToString(p.Key))
			} else {
				fmt.Fprintf(&sb, " PUT(%s,%dB)", hex.EncodeToString(p.Key), p.Size)
			}
		}
		sb.WriteByte('\n')
	}
	if r.Check != nil {
		sb.WriteString("\n# violations\n")
		for _, f := range r.Check.Failures {
			sb.WriteString(f.String() + "\n")
		}
	}
	return sb.String()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
