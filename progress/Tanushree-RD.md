# Progress log — Tanushree-RD (CLI, tests and proof docs lead)

Branch: `tanushree/cli-docs` · Owns: `src/cmd/kvdb/**`, `tests/**`, `README.md`, `ARCHITECTURE.md`, `BREAKING.md`, `diagrams/**`

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

## 2026-11-06 15:51 IST · T0 · skeletons: escaping and doc skeletons
- **Files:** src/cmd/kvdb/escape.go, src/cmd/kvdb/escape_test.go, README.md, ARCHITECTURE.md, BREAKING.md
- **What:** Implemented SPEC 8.3 escaping in `escape.go` with `DecodeInput` (hex prefix decoding) and `EncodeOutput` (rules for raw ASCII vs `hex:<lowercase hex>`, handling NOTFOUND, empty value, controls, and backslash). Added comprehensive table unit tests covering all required cases. Filled out document skeletons with section headings, persistence graphs, state machines, the 32-scenario proof table outline, and crash-point mappings.
- **Crash points touched:** none (escaping and documentation)
- **Tests:** `go vet ./src/cmd/kvdb` + `go test -v ./src/cmd/kvdb` PASS (all escape table tests and roundtrips).
- **Status:** T0 100% done.
- **NEEDS:** from Chethan: M0 (go.mod, Makefile, `.githooks/`, stub `src/engine/api.go`, `src/engine/crashpoint.go`).
- **Next:** T1 (CLI commands in `src/cmd/kvdb/main.go`, serve protocol in `src/cmd/kvdb/serve.go`, and `tests/cli_test.go`).

## 2026-11-06 16:38 IST · T1 · CLI commands and serve protocol
- **Files:** src/cmd/kvdb/main.go, src/cmd/kvdb/serve.go, tests/cli_test.go
- **What:** Implemented all 8 CLI commands (`open`, `put`, `get`, `del`, `batch`, `dump`, `check`, `serve`). Implemented `serve` with text mode and b64 mode, concurrency per lane using dedicated lane goroutines, single stdin reader reading with `bufio.Reader.ReadString`, ACKs emitted from the `OnCommit` hook (tag = id) in strict commit order under stdout mutex, CP-W07 hook triggered after each ACK, streamed BATCH handling over 64 MiB without holding whole batches in memory, and exit code 3 on ErrIO. Added integration tests in `tests/cli_test.go` exercising all commands, text mode, b64 mode, 1 MiB values through serve, and streamed batches over 64 MiB.
- **Crash points touched:** CP-W07 (in serve.go after flushing ACK)
- **Tests:** `make check` PASS (builds `bin/kvdb` & `bin/torture`, `go vet ./...` clean, `go test ./...` PASS).
- **Status:** T1 100% done. Ready for M1 integration.
- **NEEDS:** none for T1. For M1 gate, needs Chethan's E2 (durable commit path).
- **Next:** T2 (B5 batch tests, D4 concurrent reader test, ACK order test, binary roundtrip test, F3 benchmark harness, architecture diagram draft).

## 2026-11-06 16:44 IST · T2 · correctness tests, F3 harness, and architecture diagram
- **Files:** tests/batch_test.go, tests/concurrent_test.go, tests/binary_test.go, tests/benchmark_test.go, diagrams/architecture.mmd, prompts/Tanushree-RD.md
- **What:** Implemented B5 batch tests (in-batch overwrite ordering: PUT then DEL leaves key absent, DEL then PUT leaves key present; empty value []byte{} vs DELETE in Get and ForEach). Implemented D4 concurrent reader test hammering 10,000-op atomic batches with zero partial batch reads, plus ACK order test across two concurrent lanes writing to the same key verifying that the winning state reflects the last received ACK. Added binary round-trip tests covering null bytes, newlines, tabs, backslashes, arbitrary invalid UTF-8 bytes, 1024-byte keys, and 1 MiB values. Added F3 recovery benchmark harness for 1,000,000 keys (activated via `KVDB_BENCH=1`). Drafted full Mermaid architecture diagram in `diagrams/architecture.mmd` detailing write ordering 1-6, on-disk layout, memory index, recovery flow, and compaction flow.
- **Crash points touched:** none directly (correctness tests & architecture diagram)
- **Tests:** `make check` PASS (build + vet + all test suites pass in WSL).
- **Status:** T2 100% done.
- **NEEDS:** from Chethan: E2 (durable commit path). Preethesh's H1 needs E2 + T1 (our T1 is pushed and ready).
- **Next:** T3 failure tests (f4_test.go, compact_test.go, diskfull_test.go, and proof table draft).

## 2026-11-06 17:15 IST · T3/T4 · failure tests, MultiGet integration, and complete proof documentation
- **Files:** tests/f4_test.go, tests/diskfull_test.go, tests/compact_test.go, tests/cli_test.go, tests/concurrent_test.go, src/cmd/kvdb/serve.go, ARCHITECTURE.md, BREAKING.md, prompts/Tanushree-RD.md
- **What:** Merged `chethan/engine` (E2/E3 durable engine, read-ahead recovery, checkpoints). Updated `src/cmd/kvdb/serve.go` to use `db.MultiGet(keys)` for atomic single-read-lock MGET snapshots (addressing Chethan's request). Implemented F4 sentinel test (`tests/f4_test.go`) proving 1-byte truncation accepts the ACKed record under SHORT_SENTINEL and subsequent write repairs the sentinel. Implemented persisted cap tests (`tests/diskfull_test.go`) ensuring `--max-bytes` persists across process boundaries. Implemented compaction test (`tests/compact_test.go`). Filled in all 32 rows of the proof table in `ARCHITECTURE.md` with concrete file:function code locations. Filled in `BREAKING.md` crash points table with exact line numbers and logged real test learnings in Section 4.
- **Crash points touched:** CP-W02, CP-W07, CP-R01 to CP-R03, CP-S01
- **Tests:** `make check` PASS (builds `bin/kvdb` & `bin/torture`, `go vet ./...` clean, all unit and integration test suites pass in WSL).
- **Status:** T3 & T4 100% complete.
- **NEEDS:** none.
- **Next:** T5 release audit and demo rehearsal.




