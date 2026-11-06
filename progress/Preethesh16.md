# Progress log — Preethesh16 (HYDRA lead)

Branch: `preethesh/hydra` · Owns: `torture/**`

Rules (MASTER_PROMPT section 6):
- The AI appends one entry after **every** change set, before committing. Newest at the bottom; never edit old entries.
- Format:

```markdown
## YYYY-MM-DD HH:MM IST · <stage> · <title>
- **Files:**
- **What:**
- **Crash points touched:**
- **Tests:**
- **Status:**
- **NEEDS:**
- **Next:**
```

---

## 2026-11-06 15:34 IST · H0 · skeleton: flags, self-describing values, oracle
- **Files:** torture/main.go, torture/main_test.go, torture/values.go, torture/values_test.go, torture/oracle.go, torture/oracle_test.go
- **What:** `torture` parses all flags from SPEC 15.1 (a missing `--seed` is picked and printed; unknown or unfinished modes exit 2, never a silent pass). Values are self-describing per SPEC 14.3 (long form with CRC32C header, short form 12 to 31 bytes). The oracle records SENT/ACK/REJECT/UNKNOWN/KILL to an append-only `oracle.log`, keeps one shared event counter so "ACKed before invoked" is a plain comparison, assigns `ack_seq` in ACK arrival order (= commit order), and can parse its own log back for `--replay`.
- **Crash points touched:** none (HYDRA only observes)
- **Tests:** `go vet ./torture` + `go test ./torture` PASS (flip-every-byte, truncation, wrong-key, mixed-halves and FNV/splitmix reference vectors for values; lifecycle, per-iteration ack order and log round trip for the oracle). `make check` not run yet: M0 is not on main.
- **Status:** H0 done. Finding: a 12 to 31 byte value has no length field and its body is a PRNG prefix, so a truncated short value still decodes (same op id, smaller Size). A 12-byte value cannot detect a flipped op id either. This is inherent to SPEC 14.3; the checker (H1) compares the decoded Size with the size recorded for that op and requires the op id to be allowed for the key, so nothing slips through. Large-values mode uses the long form, which detects everything.
- **NEEDS:** from Chethan: M0 (go.mod, Makefile, .githooks, stub api.go, kvdb placeholders). I built locally with a temporary, **untracked** `go.mod`; I will delete it before merging M0. Expect an add/add conflict on `torture/main.go` against your placeholder: I will resolve it by keeping mine, as MASTER_PROMPT E0 says.
- **Next:** H1 checker (per-key rule 14.4 + commit-order rules 14.5) with unit tests, then the random-SIGKILL runner against a fake `kvdb` test double until the real engine and serve exist.

## 2026-11-06 15:36 IST · H1 · checker: per-key rule + commit-order rules
- **Files:** torture/check.go, torture/check_test.go
- **What:** `CheckIteration` compares the dump with the oracle. Per key the surviving state must equal the effect of the ACKed op with the highest `ack_seq` (or the baseline if no ACK touched the key); the only legal deviation is ONE unACKed transaction, which must then show its effect on every key it touches (batch atomicity, with the witness key named in the message). Two or more unACKed transactions present, a partial batch, a REJECTED op appearing, a DEAD op resurfacing, a lost ACKed write, a torn/foreign/garbage value and an out-of-order ACK pair each fail with a distinct message that lists the allowed ops with SENT/ACK times (SPEC 15.4). B5 (`PUT X; DEL X` absent, `DEL X; PUT X` present) is covered.
- **Crash points touched:** none (pure logic)
- **Tests:** `go vet` + `go test ./torture` PASS, 30+ hand-built histories. Mutation check: flipping the ack_seq comparison, disabling the two-surprise check and disabling the atomicity check each make tests fail.
- **Status:** H1 about 40% done (checker done; process runner, workload, dump parser still to do)
- **NEEDS:** none
- **Next:** workload generator, dump parser, `kvdb serve --b64` session runner (lanes + reply reader + SIGKILL + read-to-EOF), then run it against a fake kvdb test double until the real engine and CLI exist.

## 2026-11-06 15:44 IST · H1 · default mode: random SIGKILL runner + proof the oracle is not blind
- **Files:** torture/randomkill.go, torture/session.go, torture/workload.go, torture/dump.go, torture/failure.go, torture/oracle.go (OBS records), torture/check.go (sharper diagnoses), tests: torture/{randomkill,workload,dump,check,oracle}_test.go, torture/testdata/fakekvdb/main.go
- **What:** `--mode default` is complete against the frozen protocol (MASTER_PROMPT 8.2/8.3): spawns `kvdb serve <db> --b64`, waits for READY, runs 2 to 4 lanes (pipelining 8 requests, SENT recorded before each write), one reader that counts only complete lines and assigns `ack_seq` in arrival order, seeded 10 to 250 ms delay, SIGKILL, reads stdout to EOF, then `kvdb open` / `check` / `dump` and the oracle checker. Extra checks: READY keys == previous verified key count, `open` keys == dump count, dump is well formed and strictly ascending, `check` prints OK, serve died by our SIGKILL, and a CRC64 of the database file before and after open/check/dump proves **recovery wrote no bytes** (C1). Every op, key and value size is a pure function of (seed, iteration, lane); op ids are `iter<<36 | lane<<32 | seq`. On failure it saves `failures/<seed>-<iter>/{database.db, oracle.log, operations.log, metadata.json}`; `oracle.log` carries OBS records so `--replay` (H2) can rebuild the baseline.
- **Crash points touched:** none directly (random timer only; no hooks, by design)
- **Tests:** `go vet` + `go test ./torture` PASS. Integration tests run the real harness against `testdata/fakekvdb`, a test double with switchable bugs: FAKE_MODE=durable PASSES 15/15; volatile, droplast, resurrect and nonatomic (batch written one op per line) each make HYDRA FAIL, with precise messages (ACKed write lost, commit order violated, ACKed DELETE undone, in-batch B5 order violated, batch atomicity violated with the witness key named). This is the "oracle works" proof the plan asks for; the M0 in-memory stub test (expected FAIL) is still to do once M0 exists.
- **Status:** H1 about 80% done. Not done: Gate 1 (`./bin/torture --iterations 100 --seed 1` against the real engine) and the expected-FAIL run against the M0 stub.
- **NEEDS:** from Chethan: M0 on main (go.mod, Makefile, `.githooks/`, stub `src/engine/api.go`) and E2 (durable commit path). From Tanushree: T1 (`kvdb serve --b64` with ACKs printed from the OnCommit hook, `open`, `check`, `dump`). I cannot run Gate 1 or the stub-must-fail check without them. Also: measured iteration time with the fake is 1.4 s, but that fake does ~10k ops per iteration and re-reads a growing file, so it says nothing about the real engine; I will measure against the real one (budget 0.6 s, SPEC 15.5).
- **Next:** H2 pieces that do not depend on others: `--replay`, large-values mode, large-batches mode (buffered and streamed), tested against the fake. Then pause at the gate until M0/E2/T1 are merged.

## 2026-11-06 15:47 IST · H1 · correction to my own earlier timestamps
- **Files:** progress/Preethesh16.md
- **What:** I wrote guessed times in three headings above (15:35, 15:52, 16:25). I replaced them with the real commit times (15:34, 15:36, 15:44, from `git log`). Content is unchanged. From here on the time comes from the system clock before each entry is written.
- **Crash points touched:** none
- **Tests:** n/a
- **Status:** fixed
- **NEEDS:** none
- **Next:** H2 (replay, large-values, large-batches)

## 2026-11-06 15:50 IST · H2 · large-values, large-batches, replay, prefetch
- **Files:** torture/workload.go, torture/session.go, torture/randomkill.go, torture/replay.go, torture/main.go, torture/oracle.go, torture/testdata/fakekvdb/main.go, tests in torture/{workload,randomkill}_test.go
- **What:** `--mode large-values` (every PUT is 256 KiB to 1 MiB; keyspace of 48 so the post-kill dump stays readable) and `--mode large-batches` (lane 0 sends 10,000-op batches: buffered, payload well under 64 MiB, three out of four times, and streamed, payload just above 64 MiB, every 4th; the other lanes send small concurrent writes; keys are recycled over 3 slots so the file stays bounded). Requests are encoded by per-lane producers **before** READY, so a 70 MB batch is already in hand when the kill timer starts. `--replay failures/<seed>-<iter>` rebuilds the baseline from the OBS records of earlier passing iterations, dumps a copy of the saved image and re-runs the same checker: deterministic. Ops of passing iterations drop their parts after the check (a 10,000-part batch is about 1 MB).
- **Crash points touched:** none (random timer)
- **Tests:** `go vet` + `go test ./torture` PASS 3 runs in a row (about 35 s). Replay: a saved crash image reproduces the failure with the buggy fake and passes with the correct one. Large modes pass against the correct fake (batch sizes scaled down in the test; the real 10,000-op sizes are asserted separately: buffered payload far below 64 MiB, every 4th above it). Fixed a flaw in my own replay test (droplast does not fail at iteration 1 every time, so a "fixed" replay can legitimately differ; replaced by a deterministic `garble` fake).
- **Status:** H2 about 70% done. Honest limits: (1) droplast and nonatomic are detected probabilistically (6 and 60 iterations); they passed 3 of 3 runs. (2) Large modes have only run against the fake; real memory and disk use is unmeasured. A streamed batch is about 68 MB, so 100 large-batches iterations can write several GB to the temp dir.
- **NEEDS:** unchanged and blocking: Chethan M0 + E2 (durable engine, `ApplyStream`, 1 MiB values, 10k-op batches is E3), Tanushree T1 (serve/open/check/dump). Gate 2 needs E3 as well.
- **Next:** I am pausing the engine-dependent work here. H3 (recovery-crash, compaction, fixtures) needs the real file format, COMPACT in serve and a real `open` duration; H4 needs disk-full admission and the KVDB_CRASH hooks. Writing those blind would be unverifiable.
