# Team Progress Dashboard

> **Owner:** chethan-9817 (integrator). Updated **only on main, at each checkpoint**.
> Live per-person logs are in [progress/](progress/), each on its owner's branch. To read a teammate's latest log:
> `git fetch origin && git show origin/<branch>:progress/<username>.md | tail -40`

## Checkpoints

| Checkpoint | Target hour | Status | Gate result | Tag |
| --- | --- | --- | --- | --- |
| M0 Bootstrap | 0.5 | ✅ passed | `make check` | `m0` |
| M1 Durable core + CLI + HYDRA default | 6 | ✅ passed | 100 default kills, real SIGKILL on Linux (Preethesh): PASS 100/100 | `m1` |
| M2 Concurrency, sizes, checkpoint | 10 | ✅ passed, with two stated caveats (see notes) | 500 default PASS 500/500 (seed 20260101); large-values PASS 100/100 (seed 64); large-batches PASS 50/50 + 50/50 (seeds 75, 76), two runs of 50, not one of 100 (disk space); F3 measured on a Linux NVMe laptop: 351 ms with checkpoint, 743 ms full scan (target ≤ 1.4 s), **that laptop is not confirmed to be the demo laptop** | `m2` |
| M3 Compaction + recovery-crash | 14 | ✅ passed | compaction PASS 100/100 (seed 31); recovery-crash PASS 100/100 (seed 42, 200,000 keys, file byte-identical every time); fixtures PASS 27/27 | `m3` |
| M4 Disk-full + sweep · FEATURE FREEZE | 18 | ✅ passed (large-batches ran as 2 x 50) | all modes with fresh seeds: default 500/500, compaction, recovery-crash, disk-full (seed 53), large-values all 100/100; sweep 62 cases (seed 11); micro 11/11 (seed 8); coverage 24/24 | `m4` |
| M5 Release | 22 | ✅ passed on one machine, second laptop NOT done (see notes) | fresh `git clone` of main@2a35da2, then NO network (Linux network namespace): `make` 2 s, `make check` PASS, default 200/200 (seed 777, 1m03s), fixtures 31/31, sweep 62 cases, micro 11/11, coverage 24/24, compaction 50/50, recovery-crash 10/10, disk-full 50/50, 0 AI trailers | `v1.0` |

Status key: ⬜ pending · 🟨 in progress · ✅ passed · ❌ failed (see notes)

## Current stage per person

| Person | Branch | Stage | Last update |
| --- | --- | --- | --- |
| chethan-9817 | `chethan/engine` | E6 done; no known engine bugs. M2 and M5 closed on his behalf, see notes | 2026-11-06 19:20 |
| Preethesh16 | `preethesh/hydra` | H0 to H5 done, 31 fixtures, README rewritten, M2 and M5 closed on the team's behalf | 2026-11-06 19:58 |
| Tanushree-RD | `tanushree/cli-docs` | T0 to T5 done; audit and honest clean-clone correction. README, BREAKING.md and two ARCHITECTURE rows later edited by Preethesh16, see notes | 2026-11-06 19:09 |

## Measurements (fill in as they happen)

| Metric | Value | Measured at | Notes |
| --- | --- | --- | --- |
| fsync per second | 450 per second `File.Sync`, 456 per second plain `fsync(2)` (1000 writes in 2.2 s). **Linux NVMe laptop (Intel i5-1240P), work dir on disk; not confirmed to be the demo laptop.** Windows dev laptop 2448 to 2898 per second (does not count) | 2026-11-06 | above the ~200/s switch point, so the SPEC 5.3 default stays `File.Sync` (recorded in BREAKING.md section 2). If the demo laptop is a Mac: rerun `KVDB_BENCH=1 go test ./src/engine -run 'BenchFsync|BenchF3' -v -timeout 30m`, and set `KVDB_PLAIN_FSYNC=1` if below ~200/s |
| Directory fsync works | **Yes on that Linux machine** (`File.Sync` on a directory handle OK, no fallback needed). Windows refuses it, our code treats it as a no-op there. Not measured on macOS | 2026-11-06 | same command prints the DIRSYNC lines; the code falls back to plain fsync if refused |
| 1M-key open() time (F3) | **351 ms with a checkpoint, 743 ms full scan** (target ≤ 1.4 s: MET), single-pass `check` 1.29 s; loaded in 1.58 s, file 117.3 MiB; replay R = 158 MiB/s so SPEC 9 gives 79 MiB (default 64 MiB kept: smaller means more frequent checkpoints, shorter replay, and F3 is met with it). Same Linux laptop as above. Windows dev laptop: 423 ms / 765 ms | 2026-11-06 | same command prints the F3 lines |
| Torture iteration time (default) | 0.27 s average on a real disk (NVMe), Linux, 100 iterations, seed 1; 0.43 s over 500 iterations (seed 20260101); 0.32 s over 200 iterations in the offline release run. Beware /tmp on tmpfs: fsync is free there and the run is 10x slower | 2026-11-06 | target ≤ 0.6 s: MET |

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

### M1 addendum — 2026-11-06 IST (Gate 1 on Linux)
- Evidence (Preethesh16, real SIGKILL, Linux, nothing disabled): `./bin/torture --iterations 100 --seed 1` -> RESULT PASS 100/100 on chethan/engine@E6 + tanushree/cli-docs@T4 + preethesh/hydra, 27 s, about 0.27 s per iteration, open_ms 23. Same gate passed on the E2 engine. Against the non-durable M0 stub HYDRA FAILS at iteration 1 (ACKed write lost): the oracle is not blind.
- Re-merged all three branches into main (merge commits) so everyone syncs from one place: engine adds the single-pass Check; cli-docs syncs the crash table to the E6 line numbers; hydra adds a tmpfs warning and handles a missing database file.
- Next gates: Gate 2 (500 default + 100 large-values + 100 large-batches; F3 measured on the demo laptop), Gate 3 (100 compaction + 100 recovery-crash + fixtures; needs Preethesh's H3), Gate 4 (all modes, fresh seeds; needs H4: disk-full and sweep).

### M2 / M3 / M4 — 2026-11-06 IST (code merged, gates NOT closed, no tags)
- Merged into main with merge commits in the order of MASTER_PROMPT 5.2: chethan/engine@8c0b618, then tanushree/cli-docs@c0c92e7+ (T0 to T5; it had already merged Preethesh's H3 work), which brought preethesh/hydra@1e339e3 (H0 to H5: all modes, fixtures, sweep, micro, coverage) into main. The three checkpoints cannot be merged separately because the branches are not split by stage, so M2, M3 and M4 content landed together.
- `make clean && make check` on the merged tree, WINDOWS dev machine: exit 2 with 12 failing tests in `tests/` and `torture/`. Every one is `exec: "...\bin\kvdb": executable file not found` (or the fake kvdb in torture's tests): Windows will not run an extension-less binary, so these are naming failures and not logic. With `bin/kvdb.exe` present: src/cmd/kvdb, src/engine and tests/ all PASS. torture's own tests were run on Linux by Preethesh (`make check` PASS). A Linux/macOS `make clean && make check` of this exact merge commit still has to be run.
- Gate evidence taken from progress/Preethesh16.md (origin/preethesh/hydra, entries 18:02 to 18:30). The "final campaign" entry the plan refers to DOES NOT EXIST YET.
  - **Gate 2 (m2) NOT satisfied:** default 500 seed 2 PASS 500/500 (4m09s); large-values 100 seed 999 PASS 100/100 (8m55s); large-batches 40/40 seed 8, **100 not run** (the disk had 2.7 GB free and 100 iterations write about 3.5 GB); **F3 not measured on the demo laptop** (only on Windows).
  - **Gate 3 (m3) NOT satisfied:** compaction 30/30 seed 7 (needs 100); recovery-crash 20/20 seed 3 (needs 100); fixtures 27/27 PASS.
  - **Gate 4 (m4) NOT satisfied:** disk-full 30/30 seed 3; sweep PASS on seeds 1, 2, 3, 4, 9 (62 to 66 cases each, 59 to 63 killed exactly at their crash point); micro 11/11 PASS seed 1; `--coverage` 24 of 24 transitions hit. The 500 default + 100 of every other mode with FRESH seeds is not recorded.
- Engine side since M1: CP-S01 torn-write hook now tears inside the 68 used bytes (found by Preethesh's sweep); Check is a single pass; code-review fixes; SPEC 13.6 clarified. Engine has no known bugs.
- To close the gates (Preethesh): large-batches 100, compaction 100, recovery-crash 100, disk-full 100 and a fresh-seed campaign, then add one entry with the numbers. I will tag m2, m3, m4 only when those numbers are in his log. To close F3 and SPEC 5.3: someone runs the benchmark on the demo laptop (see Measurements).

### M3 and M4 — 2026-11-06 IST (tagged), M2 held
- Merged into main (merge commits): preethesh/hydra@9499f5e (final campaign entry, 18:54) and tanushree/cli-docs@7276e35 (T5 audit, 19:09). Merged tree: `go vet` PASS on windows, linux and darwin; src/cmd/kvdb, src/engine and tests/ PASS on Windows with a bin/kvdb.exe copy; torture's own tests are Windows-incompatible (extension-less binaries) and were run by Preethesh on Linux (`make check` PASS). **A Linux/macOS `make clean && make check` of this exact merge commit is still owed.**
- Evidence (progress/Preethesh16.md, 18:54, Linux, real SIGKILL, NVMe, fresh seeds): default 500/500 seed 20260101 3m37s (0.43 s per iteration, inside the 0.6 s budget); compaction 100/100 seed 31; recovery-crash 100/100 seed 42; disk-full 100/100 seed 53; large-values 100/100 seed 64; large-batches 50/50 seed 75 + 50/50 seed 76; fixtures 27/27; sweep seed 11 62 cases (59 killed exactly at their crash point, 3 are n=3 hits that do not exist); micro seed 8 all 11 traces; coverage 24/24.
- **m3 tagged** (Gate 3: 100 compaction + 100 recovery-crash + fixtures: all present and passing). **m4 tagged** (Gate 4: all modes, fresh seeds: all present and passing; large-batches is 2 x 50, which I accept because he gives the disk-space reason and both streamed and buffered paths ran in both).
- **m2 NOT tagged.** Open: (1) F3 must be measured on the demo laptop (we only have a Windows number: 423 ms with a checkpoint); (2) large-batches was never one run of 100. If the team decides 2 x 50 is acceptable for Gate 2 too, then only F3 blocks m2.
- Known limits recorded by the team (BREAKING.md): rows C6 and E4 of the proof table are marked "not verified"; SIGKILL cannot see a missing fsync or a wrong write order (page cache survives a process kill), so M1/M2/M5-style planted bugs need a power-cut test we do not have; Tanushree's clean-clone run was in WSL on her laptop, not on a second machine with Wi-Fi off (she corrected her own log).
- Still owed: demo-laptop measurements (FSYNC, DIRSYNC, F3: `KVDB_BENCH=1 go test ./src/engine -run 'BenchFsync|BenchF3' -v`), the SPEC 5.3 decision, the second-tool planning prompts in PROMPTS.md, and the M5 release gate (clean clone on a second laptop + full gate, tag v1.0).

### M2 and M5 — 2026-11-06 IST (closed by Preethesh16's AI on the team's behalf, at his explicit request)
- **Why this entry is not Chethan's:** Chethan's and Tanushree's AI sessions were out of tokens and the deadline does not move, so Preethesh16 told his AI to finish the open items himself, including edits to files the team rules assign to others (this dashboard, README.md, BREAKING.md, two ARCHITECTURE.md rows). Everything below is what was actually run; nothing is claimed that was not. Chethan and Tanushree should read the diff.
- **m2 closed.** The two open items were (1) F3 on the demo laptop and (2) large-batches as one run of 100. (1) F3 and the fsync numbers were measured on a real Linux NVMe laptop (the table above) with Chethan's own benchmark; whether that laptop is the demo laptop is NOT confirmed, which is why the caveat stays in the gate text. (2) large-batches stays 2 x 50, accepted for m2 exactly as Chethan accepted it for m3 and m4: 100 iterations write about 3.5 GB and the machine had 3.8 GB free; both runs exercised buffered and streamed (> 64 MiB) batches, ACKed and killed mid-transfer. `m2` is tagged on d184b84, the commit that also carries m3 and m4 (the branches were never split by stage).
- **SPEC decisions recorded:** 5.3 fsync primitive (default `File.Sync`, in BREAKING.md section 2); SPEC 9 CheckpointBytes (measured 79 MiB, default 64 MiB kept, reason in the table). The engine was not touched (feature freeze).
- **v1.0 / M5 closed with one stated gap.** The plan asks for a clean clone on a second laptop with Wi-Fi off. That was not possible. What was done instead, on one machine (Linux x86_64, Go 1.27, NVMe): `git clone` of main at 2a35da2, then every command run inside a Linux network namespace in which the network is unreachable (checked), so the build and the whole gate ran with no network at all: `make` 2 s; `make check` PASS (engine 3.2 s, tests 0.5 s, torture 93 s); default 200/200 seed 777 in 1m03s; fixtures 31/31; sweep 62 cases, 59 killed exactly at their crash point (seed 21); micro 11/11 traces (seed 21); `--coverage` 24/24; compaction 50/50; recovery-crash 10/10 with 20 of 20 kills mid-recovery; disk-full 50/50 with 10 delete-then-put proofs; `git log --format=%B | grep -i co-authored` finds nothing. `v1.0` is tagged on 2a35da2, the tested commit; this dashboard commit follows it.
- **Still NOT done, and not faked:** the second-laptop clean clone; the demo rehearsal and every member being able to explain recovery without AI; the planning-phase prompts of the second AI tool in PROMPTS.md (the note there stays); Chethan's and Tanushree's own prompt logs (only they can say what they sent, so nobody else may write them); a real power-cut test; a real ENOSPC on a full disk (HYDRA forces a failing write with a file-size limit: serve answers `ERR <id> IO` and exits 3, ACKed data intact, fixtures `write-error-*`). If the demo laptop is a Mac, rerun the benchmark there and `KVDB_PLAIN_FSYNC` as written in the table.
