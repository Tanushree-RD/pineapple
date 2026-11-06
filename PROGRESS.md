# Team Progress Dashboard

> **Owner:** chethan-9817 (integrator). Updated **only on main, at each checkpoint**.
> Live per-person logs are in [progress/](progress/), each on its owner's branch. To read a teammate's latest log:
> `git fetch origin && git show origin/<branch>:progress/<username>.md | tail -40`

## Checkpoints

| Checkpoint | Target hour | Status | Gate result | Tag |
| --- | --- | --- | --- | --- |
| M0 Bootstrap | 0.5 | ✅ passed | `make check` | `m0` |
| M1 Durable core + CLI + HYDRA default | 6 | ✅ merged (Windows dry run; Linux/macOS pending) | 100 default kills | `m1` |
| M2 Concurrency, sizes, checkpoint | 10 | ⬜ pending | 500 default + 100 large-values + 100 large-batches; F3 measured | `m2` |
| M3 Compaction + recovery-crash | 14 | ⬜ pending | 100 compaction + 100 recovery-crash | `m3` |
| M4 Disk-full + sweep · FEATURE FREEZE | 18 | ⬜ pending | All modes, fresh seeds | `m4` |
| M5 Release | 22 | ⬜ pending | Clean clone + full gate | `v1.0` |

Status key: ⬜ pending · 🟨 in progress · ✅ passed · ❌ failed (see notes)

## Current stage per person

| Person | Branch | Stage | Last update |
| --- | --- | --- | --- |
| chethan-9817 | `chethan/engine` | E6 done, verification pending | 2026-11-06 |
| Preethesh16 | `preethesh/hydra` | H3 (started) | 2026-11-06 16:33 |
| Tanushree-RD | `tanushree/cli-docs` | T4 done | 2026-11-06 17:15 |

## Measurements (fill in as they happen)

| Metric | Value | Measured at | Notes |
| --- | --- | --- | --- |
| fsync per second | 2898 (Windows dev laptop, NOT the demo laptop) | 2026-11-06 | SPEC 5.3: re-measure on the demo laptop |
| Directory fsync works | refused on Windows (no-op there); Unix not yet tested | 2026-11-06 | re-measure on Linux/macOS |
| 1M-key open() time | 423 ms with checkpoint, 775 ms full scan (Windows, warm cache) | 2026-11-06 | target ≤ 1.4 s; replay R = 151 MiB/s |
| Torture iteration time (default) | avg 2.0 s over 100 iterations, growing with file size (measured before E3 read-ahead + checkpoints) | 2026-11-06 | target ≤ 0.6 s; re-measure on merged main |

## Checkpoint notes

<!-- Append one section per checkpoint:
### M1 — <date time IST>
- Merged: chethan/engine@<sha>, tanushree/cli-docs@<sha>, preethesh/hydra@<sha>
- Gate: `./bin/torture --iterations 100 --seed 1` → RESULT PASS 100/100
- Open issues: ...
-->

### M0 — 2026-11-06 IST
- Merged: chethan/engine@822e9cb (go.mod, Makefile, hook, in-memory API stub, no-op crashpoint, placeholders)
- Gate: `make clean && make check` PASS on main (built on Windows via msys2; Linux/macOS vet cross-checked)
- Open issues: Preethesh and Tanushree must merge origin/main and keep their own torture/main.go and src/cmd/kvdb/main.go on conflict. Their H3/H4 and T1 wait for E2.

### M1 — 2026-11-06 IST
- Merged (merge commits, order engine, cli-docs, hydra): chethan/engine@6febba1 (E1 to E6), tanushree/cli-docs@23fa560 (T0 to T4), preethesh/hydra@c698f6a (H0 to H3 prep). No conflicts. Tag `m1` points at 11fbf72 (the merges plus the SPEC correction); this dashboard commit follows it.
- Gate 1 (`./bin/torture --iterations 100 --seed 1`): **PASS 100/100** on a throwaway merge of the E2 engine with the other two branches, run on WINDOWS (hard process kills, not SIGKILL) with one Windows-only HYDRA check disabled locally. NOT yet run on Linux/macOS with real SIGKILL. The engine-level crash sweep (self-kill at every CP, up to 70 hits each, 410 kills) passes.
- `make check` on merged main (Windows): engine and kvdb unit tests PASS; tests/ PASS (4.5 s) once `bin/kvdb.exe` exists; torture's own go test fails on Windows only because its fake kvdb has no .exe suffix. Both failures are Windows file naming, not logic.
- SPEC corrections applied at this checkpoint: 8.2 frame-count bound now includes the 10,000-op limit; MultiGet added to the API (MASTER_PROMPT 8.1, SPEC 3).
- Open issues: (1) Gate 1 on Linux/macOS; (2) default-mode iteration time must be re-measured with the E3 engine (budget 0.6 s); (3) Preethesh has H3 (recovery-crash, compaction, fixtures), H4 (disk-full, sweep) and H5 left; (4) Tanushree's docs predate E4/E5 and the crash-point code map: re-verify every line number at M5; (5) fsync and directory-fsync decisions (SPEC 5.3) still need the demo laptop.
