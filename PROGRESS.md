# Team Progress Dashboard

> **Owner:** chethan-9817 (integrator). Updated **only on main, at each checkpoint**.
> Live per-person logs are in [progress/](progress/), each on its owner's branch. To read a teammate's latest log:
> `git fetch origin && git show origin/<branch>:progress/<username>.md | tail -40`

## Checkpoints

| Checkpoint | Target hour | Status | Gate result | Tag |
| --- | --- | --- | --- | --- |
| M0 Bootstrap | 0.5 | ⬜ pending | `make check` | `m0` |
| M1 Durable core + CLI + HYDRA default | 6 | ⬜ pending | 100 default kills | `m1` |
| M2 Concurrency, sizes, checkpoint | 10 | ⬜ pending | 500 default + 100 large-values + 100 large-batches; F3 measured | `m2` |
| M3 Compaction + recovery-crash | 14 | ⬜ pending | 100 compaction + 100 recovery-crash | `m3` |
| M4 Disk-full + sweep · FEATURE FREEZE | 18 | ⬜ pending | All modes, fresh seeds | `m4` |
| M5 Release | 22 | ⬜ pending | Clean clone + full gate | `v1.0` |

Status key: ⬜ pending · 🟨 in progress · ✅ passed · ❌ failed (see notes)

## Current stage per person

| Person | Branch | Stage | Last update |
| --- | --- | --- | --- |
| chethan-9817 | `chethan/engine` | E0 | — |
| Preethesh16 | `preethesh/hydra` | H0 | — |
| Tanushree-RD | `tanushree/cli-docs` | T0 | — |

## Measurements (fill in as they happen)

| Metric | Value | Measured at | Notes |
| --- | --- | --- | --- |
| fsync per second (demo laptop) | | | SPEC 5.3 decision |
| Directory fsync works (demo laptop) | | | |
| 1M-key open() time | | | target ≤ 1.4 s |
| Torture iteration time (default) | | | target ≤ 0.6 s |

## Checkpoint notes

<!-- Append one section per checkpoint:
### M1 — <date time IST>
- Merged: chethan/engine@<sha>, tanushree/cli-docs@<sha>, preethesh/hydra@<sha>
- Gate: `./bin/torture --iterations 100 --seed 1` → RESULT PASS 100/100
- Open issues: ...
-->
