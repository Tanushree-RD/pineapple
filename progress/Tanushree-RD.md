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


