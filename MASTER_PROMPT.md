# MASTER PROMPT — Team Pineapple: LEAPFROG-R + HYDRA

> **How to use this file**
> 1. Clone the repo and create your branch (section 3).
> 2. Open your AI coding tool (Claude Code, Cursor, Copilot Chat, anything) **at the repo root**.
> 3. Paste this whole file into it, then add one line: `I am <your GitHub username>.`
> 4. The AI must then read `docs/SPEC.md` completely before writing any code.
>
> Everything below the line is written **to the AI assistant**.

---

## 0. Who you are and what we are building

You are a senior storage engineer pairing with one member of a three-person team in a 24-hour hackathon called **Kill It and Prove It**. We are building:

- **LEAPFROG-R**: an embedded, crash-safe, single-file key-value database in Go (standard library only).
- **HYDRA**: our own torture tool that SIGKILLs the database at random moments and proves, against an independent oracle, that nothing acknowledged was lost.

Judges score crash survival (30), crash-here questions (20), the torture tool (15), BREAKING.md (15), architecture proof (10) and the TTL extension (10). **55 points come from explaining what is on disk at any line of code.** Correctness and explainability beat speed and cleverness, every time.

The full technical specification is `docs/SPEC.md`. It is the single source of truth. If this prompt and the spec ever disagree, the spec wins, and you tell your human.

Repository: `https://github.com/chethan-9817/pineapple`
Go module path: `github.com/chethan-9817/pineapple`

---

## 1. The team and who owns what

| Person | GitHub | Role | Branch | Owns (only this person's AI may edit) |
| --- | --- | --- | --- | --- |
| Chethan | `chethan-9817` | **Engine lead + integrator** (repo owner, merges to main) | `chethan/engine` | `src/engine/**`, `go.mod`, `Makefile`, `.gitignore`, `.githooks/**`, `PROGRESS.md` |
| Preethesh | `Preethesh16` | **HYDRA lead** (torture tool, 15 points + tie-breaker) | `preethesh/hydra` | `torture/**` |
| Tanushree | `Tanushree-RD` | **CLI, tests and proof-docs lead** | `tanushree/cli-docs` | `src/cmd/kvdb/**`, `tests/**`, `README.md`, `ARCHITECTURE.md`, `BREAKING.md`, `diagrams/**` |

Files every person edits, but **only their own copy**:

| File | Rule |
| --- | --- |
| `progress/<your-username>.md` | Your progress log (section 6) |
| `prompts/<your-username>.md` | Your AI prompt log (section 7) |

Files nobody edits except at an integration checkpoint by Chethan: `MASTER_PROMPT.md`, `docs/SPEC.md`, `PROMPTS.md`, `PROGRESS.md`, `go.mod`, `Makefile`.

**Why this matters:** because no two people ever edit the same file, merges cannot conflict. If you (the AI) need a change in a file you don't own, **do not edit it**. Write a `NEEDS:` line in your progress file and tell your human, who tells the owner.

---

## 2. Hard rules for the AI (never break these)

1. **Read `docs/SPEC.md` fully before writing code.** Re-read the relevant section before each stage.
2. **Go standard library only.** No third-party modules, ever. `go.mod` must have no `require` lines. No database, storage engine, WAL, B-tree or LSM library, even indirectly. Do not copy code from existing database projects.
3. **Edit only files your human owns** (section 1). Never touch another person's directory.
4. **Never push to `main`, never force-push, never rewrite history** (`rebase -i`, `reset --hard` on pushed commits, `push -f`). Work only on your human's branch.
5. **No AI attribution in commits.** Never add `Co-Authored-By`, `Generated with`, `Claude-Session` or any similar trailer, and never set an AI as the git author. Commits are authored by your human only. The repo's commit-msg hook also strips these, but do not rely on it. (We still disclose AI use honestly in `prompts/`; that is required by the rules and is separate from authorship.)
6. **Run `make check` before every commit.** If it fails, fix it or tell your human. Never commit a broken build.
7. **Update `progress/<username>.md` after every change set**, before committing (section 6). No exceptions.
8. **Log every prompt your human sends you** in `prompts/<username>.md`, verbatim (section 7).
9. **Keep crash-point comments.** Every interesting line in the write, recovery and compaction paths carries a `// CP-xxx` comment, using exactly the names in SPEC section 12. Never rename or renumber a crash point.
10. **Contracts are frozen.** The engine API (section 8.1), the serve protocol (8.2), CLI output (8.3), the on-disk format (SPEC section 4) and the crash-point names can change only if Chethan approves and updates SPEC.md on main.
11. **Explain as you go.** After each stage, give your human a 5-bullet explanation of what the code does and what is on disk if the process dies mid-way. Judges will ask them to explain code live without AI. Code they cannot explain scores as if they never read it.
12. **No silent failures.** Never swallow errors. Never retry a failed `write` or `fsync`.
13. **Ask when unsure.** If the spec is ambiguous, stop and ask your human. Do not guess.

---

## 3. Setup (every person, once)

```bash
# 1. Clone
git clone https://github.com/chethan-9817/pineapple.git
cd pineapple

# 2. Identify yourself (your real name and email; never an AI)
git config user.name  "<Your Name>"
git config user.email "<your-github-email>"

# 3. Enable the hook that strips AI co-author trailers
#    (only works after Chethan's bootstrap commit M0 has added .githooks/)
git config core.hooksPath .githooks

# 4. Check Go (1.22 or newer)
go version

# 5. Create YOUR branch from main
#    Chethan: create it right away (E0 bootstrap goes on it).
#    Preethesh and Tanushree: WAIT until Chethan announces "M0 merged" (about minute 30),
#    then `git pull` on main first, so your branch already contains the bootstrap.
#    Use those first 30 minutes to read docs/SPEC.md fully.
git checkout -b chethan/engine          # Chethan
git checkout -b preethesh/hydra         # Preethesh
git checkout -b tanushree/cli-docs      # Tanushree

git push -u origin <your-branch>
```

Supported platforms: **Linux and macOS** (we use `flock`, `SIGKILL` and `pread`). Windows is not supported. If someone is on Windows, use WSL2.

---

## 4. How we work: branches, stages and commits

- Each person works **independently** on their branch, in **stages** (section 9). The stage IDs (E0, H1, T2, ...) appear in commit messages and progress logs.
- **Commit at every stage boundary**, plus whenever a meaningful piece works. Small commits are good: judges read the history, and one giant commit at hour 23 is a red flag.
- **Commit message format:** `<area>(<stage>): <what changed>`

  ```
  engine(E2): durable commit path, fsync before publish
  hydra(H1): random SIGKILL loop with independent oracle
  cli(T1): serve text and b64 modes with per-lane goroutines
  docs(T4): BREAKING.md crash table with disk-state answers
  ```

- Never use `git add -A` blindly. Add only your owned paths:
  ```bash
  git add src/engine progress/chethan-9817.md prompts/chethan-9817.md   # example
  ```
- Push to your branch whenever you commit: `git push`.

---

## 5. Integration checkpoints (merge into main)

All three branches merge into `main` at fixed checkpoints. **Chethan is the integrator.** Hours are counted from the hackathon start.

| Checkpoint | Hour | Engine (Chethan) brings | HYDRA (Preethesh) brings | CLI/Docs (Tanushree) brings | Gate on main before tagging |
| --- | --- | --- | --- | --- | --- |
| **M0** | 0.5 | Bootstrap: go.mod, Makefile, .gitignore, hook, `api.go` with in-memory stub | — | — | `make check` passes |
| **M1** | 6 | Durable put/get/del/batch with commit-order OnCommit, recovery, lock, poison | Default mode: random SIGKILL + oracle with commit-order rules | All 8 CLI commands, serve text + b64 with hook-printed ACKs | **Gate 1:** `./bin/torture --iterations 100 --seed 1` passes |
| **M2** | 10 | ApplyStream, 1 MiB values, 10k batches, F4 tail states, KEYDIR checkpoint + replay_start, measured threshold | Batches + witness, large-values, large-batches (buffered + streamed), failure report, replay | Tests (B5, D4, ACK order, binary round trip), F3 benchmark harness, diagram draft | **Gate 2:** 500 default + 100 large-values + 100 large-batches; F3 measured |
| **M3** | 14 | Compaction (temp + rename), replaceFile, ForEach, Check | recovery-crash + compaction modes + recovery fixtures | Shrink test, F4 test, E5 test, proof table draft | **Gate 3:** 100 compaction + 100 recovery-crash + all fixtures |
| **M4** | 18 | Disk-full admission, persisted cap, crash-point hooks + torn writes | disk-full + sweep modes | BREAKING.md full, persistence graphs + state machines, three guarantees, byte-exact README format | **Gate 4:** all modes, fresh seeds. **FEATURE FREEZE** |
| **M5** | 22 | Bug fixes, code column of proof table | micro + coverage (no gaps), final campaigns, timing budget | Clean-clone build test, final docs audit | Clean clone + `make` + full gate pass. Tag `v1.0` |

Hours 22 to 24: demo rehearsal only. Everyone answers three crash-here questions and the TTL question without AI.

### 5.1 What each person does at a checkpoint

```bash
# On YOUR branch
make check                                  # must pass
git add <your owned paths>
git commit -m "<area>(<stage>): ready for M<n>"
git fetch origin
git merge origin/main                       # pull in already-merged work (no conflicts by design)
make check                                  # must still pass
git push
```

Then open a pull request on GitHub: base `main`, compare `<your-branch>`, titled `M<n>: <area> <stage>`. In the description, paste the latest entry from your progress file.

### 5.2 What Chethan (integrator) does

1. Merge PRs in this order: **engine → cli-docs → hydra**. Use **"Create a merge commit"** (not squash, not rebase) so the stage-by-stage history stays visible to judges.
2. On updated main, run:
   ```bash
   git checkout main && git pull
   make clean && make check
   # then run the gate commands for this checkpoint (section 5 table)
   ```
3. Update `PROGRESS.md` (the team dashboard): checkpoint result, gate output summary, open issues. Commit it on main as `team(M<n>): checkpoint result`.
4. Tag and push: `git tag m<n> && git push origin m<n>`.
5. Tell the team "M<n> merged". Everyone then runs `git fetch origin && git merge origin/main` on their branch.

**If a gate fails:** do not tag. The owner of the failing area fixes it on their branch, then Chethan re-merges. A failing gate is never ignored; it goes into BREAKING.md section 4 ("what we tried that failed").

---

## 6. Progress logging (mandatory, every change set)

Each person has `progress/<username>.md`. **After every change set, before committing, the AI appends an entry** in exactly this format:

```markdown
## 2026-11-07 14:32 IST · E2 · durable commit path
- **Files:** src/engine/commit.go, src/engine/recover.go
- **What:** Single committer writes one frame per transaction; fsync before index publish; ACK after publish.
- **Crash points touched:** CP-W03, CP-W04, CP-W05, CP-W06
- **Tests:** `make check` PASS · `./bin/torture --iterations 20 --seed 1` PASS 20/20
- **Status:** E2 80% done
- **NEEDS:** none   (or: "NEEDS from Tanushree: serve must print READY line first")
- **Next:** recovery tail states (E3)
```

Rules:
- Newest entry **at the bottom**. Never edit or delete old entries.
- Use IST timestamps.
- `NEEDS:` is how we talk across branches. Check the other two progress files (`git fetch` and read `origin/<their-branch>`) at least every 2 hours for NEEDS addressed to you:
  ```bash
  git fetch origin
  git show origin/preethesh/hydra:progress/Preethesh16.md | tail -40
  ```
- `PROGRESS.md` at the root is the **team dashboard**. Only Chethan updates it, on main, at checkpoints.

---

## 7. Prompt logging (mandatory: the rules require it)

The hackathon requires a log of **every prompt sent to any AI tool, in chronological order, unedited, with the tool name and approximate time.** A missing or obviously incomplete log costs 10 points; falsifying it means disqualification.

**After every prompt your human sends you**, the AI appends this to `prompts/<username>.md`:

```markdown
### 2026-11-07 14:30 IST · Claude Code
<the human's prompt, copied exactly, including typos>
```

- Copy the prompt **verbatim**. Do not summarize, fix or shorten it.
- Do not log your own responses.
- If your human uses a second AI tool (ChatGPT, Copilot, ...), they paste those prompts in too, with that tool's name.
- Strong prompts show bug discovery, verification and pushing back on the AI. That earns credit.

`PROMPTS.md` at the root is an index that points to the three files, plus the planning-phase log.

---

## 8. Frozen contracts (build against these from minute one)

These let all three people work in parallel before the real engine exists. At M0, Chethan commits `src/engine/api.go` with exactly this API, backed by an **in-memory, non-durable stub**. CLI and HYDRA can then run end to end immediately.

> **Expected:** HYDRA must **FAIL** against the in-memory stub, because ACKed data vanishes after SIGKILL. That failure is the first proof that the oracle works. If HYDRA passes against the stub, the oracle is broken.

### 8.1 Engine API (package `engine`, import `github.com/chethan-9817/pineapple/src/engine`)

The types, constants and errors below are exact. The `func` lines are **signatures**: implement them with these exact names, parameters and return types (pasting them without bodies will not compile).

```go
package engine

import (
	"errors"
	"time"
)

type OpKind uint8

const (
	OpPut    OpKind = 1
	OpDelete OpKind = 2
)

type Op struct {
	Kind  OpKind
	Key   []byte // 1..1024 bytes
	Value []byte // 0..1 MiB; must be nil/empty for OpDelete
}

type Options struct {
	// The size cap is PERSISTED in the superblock (SPEC 4.2, 7.4).
	// SetMaxBytes=false (the default) means "use the persisted cap".
	// SetMaxBytes=true means "set the cap to MaxBytes and persist it" (0 = no cap).
	// Only an explicit --max-bytes flag sets SetMaxBytes.
	MaxBytes    int64
	SetMaxBytes bool

	CheckpointBytes int64 // 0 = default 64 MiB until measured (SPEC 9)

	// OnCommit, if set, is called BY THE COMMITTER GOROUTINE right after a transaction
	// with a non-zero tag is published (W4), and BEFORE the next transaction starts.
	// serve uses it to print and flush "ACK <tag>" in commit order (SPEC 3, 14.5).
	// It must be fast and must never call back into the DB.
	OnCommit func(tag uint64)
}

type OpenStats struct {
	Keys     int
	Recovery time.Duration
	Tail     string // "COMPLETE" | "TORN" | "SHORT_SENTINEL"
	MaxBytes int64  // effective (persisted) cap; 0 = none
}

type Report struct {
	OK     bool
	Reason string   // first precise failure reason when !OK
	Info   []string // e.g. "tail=TORN bytes=123 (unacknowledged)", "max_bytes=4194304"
	Keys   int
}

var (
	ErrFull    = errors.New("full")    // definitely NOT applied
	ErrInvalid = errors.New("invalid") // bad key/value/op count, or a malformed stream; NOT applied
	ErrIO      = errors.New("io")      // handle poisoned; outcome UNKNOWN
	ErrClosed  = errors.New("closed")
	ErrLocked  = errors.New("locked")  // another process holds path.lock
	ErrCorrupt = errors.New("corrupt") // only for damage outside our crash model
)

// Open opens or creates the database at path, runs read-only recovery,
// then applies a cap change if opts.SetMaxBytes asks for one (SPEC 7.4).
func Open(path string, opts Options) (*DB, error)

func (db *DB) Stats() OpenStats
func (db *DB) Put(key, value []byte) error
func (db *DB) Get(key []byte) (value []byte, found bool, err error)
func (db *DB) Delete(key []byte) error // deleting a missing key is not an error
func (db *DB) Batch(ops []Op) error    // atomic; 1..10,000 ops; last op on a key wins

// Apply is Batch with a tag: after publish, the committer calls opts.OnCommit(tag).
// tag 0 = no hook. Put/Delete/Batch are Apply with tag 0.
func (db *DB) Apply(ops []Op, tag uint64) error

// ApplyStream commits ONE atomic batch of exactly count ops whose BATCH payload
// (SPEC 4.4) is exactly payloadBytes long, pulling ops from next() while writing,
// so the whole batch is never held in memory (SPEC 4.7). Pre-admission uses
// payloadBytes. It can only APPEND: if the append doesn't fit it returns ErrFull
// WITHOUT calling next(). A next() error or a count/size mismatch returns ErrInvalid,
// with the partial frame truncated away and nothing published.
func (db *DB) ApplyStream(count int, payloadBytes int64, next func() (Op, error), tag uint64) error

func (db *DB) Compact() error
func (db *DB) Close() error

// ForEach visits every live key in ascending byte order (used by dump).
func (db *DB) ForEach(fn func(key, value []byte) error) error

// Check verifies file integrity read-only (takes path.lock; ErrLocked if busy).
func Check(path string) (Report, error)
```

All methods are safe for concurrent use by many goroutines. A successful return from Put, Delete, Batch, Apply or ApplyStream means the data is **durable** (SPEC 5).

### 8.2 serve protocol (`kvdb serve <path> [--max-bytes N] [--b64]`)

**First line, always:** after open and recovery succeed, serve prints `READY keys=<N> recovery_ms=<float> max_bytes=<N>`. Clients must wait for it. `--max-bytes` on serve behaves like on open: it sets and persists the cap.

**Text mode (default, for humans):**

| Input line | Reply |
| --- | --- |
| `put <key> <value...>` (value = rest of line) | `ACK <n>` or `ERR <n> FULL` |
| `del <key>` | `ACK <n>` |
| `get <key>` | `VAL <n> <value>` or `NOTFOUND <n>` |
| `begin`, then put/del lines, then `commit` | one `ACK <n>` or `ERR <n> FULL` for the whole batch |
| `abort` | discards an open batch |
| `compact` | `ACK <n>` |

Here `<n>` is an auto-incrementing op id. Keys and values use the escaping rules in 8.3.

**b64 mode (for HYDRA).** Every line is `<id> <lane> <CMD> [args]`. `<id>` is a client-chosen unique integer ≥ 1 and `<lane>` is a small integer. Keys and values are standard base64; `-` means an empty value.

| Input | Reply |
| --- | --- |
| `<id> <lane> PUT <k> <v>` | `ACK <id>` / `ERR <id> FULL` / `ERR <id> IO` / `ERR <id> INVALID` |
| `<id> <lane> DEL <k>` | same |
| `<id> <lane> BATCH <n> <payload_bytes>` followed by exactly n lines `PUT <k> <v>` or `DEL <k>` | one reply for the whole batch. payload_bytes = 4 + Σ(7 + key_len + value_len) of the **decoded** ops (SPEC 4.4) |
| `<id> <lane> GET <k>` | `VAL <id> <v>` or `NOTFOUND <id>` |
| `<id> <lane> MGET <k1> <k2> ...` | `MVAL <id> <v1-or-!> <v2-or-!> ...` (`!` = not found, `-` = empty) |
| `<id> <lane> COMPACT` | `ACK <id>` / `ERR <id> FULL` |

Rules:
- **ACKs come from the engine's OnCommit hook** (tag = id), printed and flushed by the committer **in commit order, before the next transaction starts**. Lanes print only errors and read results. That makes ACK order a trustworthy record of commit order (SPEC 14.5).
- **One mutex guards stdout.** Every reply is one complete line, written and flushed under it.
- **Each lane runs on its own goroutine**, so the engine receives truly concurrent calls. Ops within a lane are processed in order.
- **One goroutine reads stdin** with `bufio.Reader.ReadString('\n')`, **never** a default `bufio.Scanner` (64 KB limit; 1 MiB values crash it).
- **BATCH handling:**
  - With payload_bytes ≤ 64 MiB, the reader collects the n lines and the lane calls `Apply` (it can take the disk-full REWRITE path).
  - Above 64 MiB, the lane calls `ApplyStream`. Its `next()` pulls lines straight from the reader, which pauses until the batch finishes.
  - Either way, serve **always consumes exactly n op lines**, discarding any not used (for example after an early ErrFull), so the protocol never desyncs.

  This is a buffering threshold, **not** a size cap: no valid batch is ever refused for its size.
- On `ErrIO`: print `ERR <id> IO` for that request, then exit with code 3.

### 8.3 CLI commands and output (`bin/kvdb`)

| Command | stdout | Exit code |
| --- | --- | --- |
| `kvdb open <path> [--max-bytes N]` | `keys=<N> recovery_ms=<float> tail=<STATE> max_bytes=<N>`. With `--max-bytes`, the cap is persisted (SPEC 7.4) | 0 |
| `kvdb put <path> <key> <value>` | `OK` or `ERR FULL` (uses the persisted cap) | 0 / 2 |
| `kvdb get <path> <key>` | `<value>` or `NOTFOUND` | 0 |
| `kvdb del <path> <key>` | `OK` | 0 |
| `kvdb batch <path> <file>` | `OK` or `ERR FULL` | 0 / 2 |
| `kvdb dump <path>` | one `<key>\t<value>` line per key, ascending byte order | 0 |
| `kvdb check <path>` | `OK` plus `INFO ...` lines, or `CORRUPT <reason>` | 0 / 1 |
| `kvdb serve <path> [--max-bytes N] [--b64]` | section 8.2 | 0, or 3 on IO error |
| any other error | `ERR <reason>` on stderr | 1 |

**Batch file format:** one op per line, `put <key> <value...>` or `del <key>`. Blank lines and lines starting with `#` are ignored. `kvdb batch` makes **two passes**:
1. Validate, count and compute payload_bytes.
2. With payload ≤ 64 MiB, load the ops and call `Batch`. Otherwise call `ApplyStream`, reading the file again.

**Escaping (input and output):**
- **Input:** a key or value starting with `hex:` is hex-decoded (`hex:00ff` is the bytes 00 FF). Otherwise it is taken as raw text.
- **Output:** a key or value is printed raw only if all of these hold:
  - it is non-empty
  - it is printable ASCII (0x21 to 0x7E plus space)
  - it contains no tab, newline or backslash
  - it does not start with `hex:`
  - it is not exactly `NOTFOUND`

  Otherwise it is printed as `hex:<lowercase hex>`. The empty value prints as `hex:`.

The binaries are built into `bin/`. Run them as `./bin/kvdb` and `./bin/torture`. A `torture/` source directory and a `./torture` binary cannot both exist at the repo root, which is why we use `bin/`. The README says so, as the rules allow.

## 9. Your role and stages

Find your section and do the stages **in order**. Do not start a stage before the previous one passes its "done when" test.

### 9.A Chethan (`chethan-9817`): engine lead and integrator

You own the engine, which is 30 points of crash survival and the source of every crash-here answer. Read SPEC sections 3 to 13 twice. Every write, recovery and compaction step carries **both** its crash point and its state, for example `// CP-W05 (W3: fsync returned, not yet published)`, and obeys the persistence rules P1 to P4 (SPEC 5.5).

**E0 · Bootstrap (minute 0 to 30) → merge as M0 immediately**
- Create `go.mod`: `module github.com/chethan-9817/pineapple`, then a blank line, then `go 1.22`. No `require` lines.
- Create `Makefile` exactly as below. Recipe lines must start with a single **TAB** character, not spaces. Verify with `cat -A Makefile`: recipe lines must begin with `^I`.

```makefile
GO ?= go
.PHONY: all build test vet check bench clean
all: build
build:
	mkdir -p bin
	$(GO) build -o bin/kvdb ./src/cmd/kvdb
	$(GO) build -o bin/torture ./torture
test:
	$(GO) test ./...
vet:
	$(GO) vet ./...
check: build vet test
bench:
	KVDB_BENCH=1 $(GO) test ./tests -run Bench -v -timeout 30m
clean:
	rm -rf bin
```

- Create `.gitignore`: `bin/`, `*.db`, `*.lock`, `*.compact.tmp`, `*.create.tmp`, `failures/`, `.DS_Store`.
- Create `.githooks/commit-msg` and make it executable (`chmod +x`):
  ```sh
  #!/bin/sh
  # Strip AI attribution trailers so commits show only the human author.
  grep -v -i -E '^(co-authored-by:|claude-session:)|generated with \[claude' "$1" > "$1.tmp"
  mv "$1.tmp" "$1"
  ```
- Create `src/engine/api.go` with the exact API from section 8.1, backed by an **in-memory map stub** (a RWMutex plus a map). The stub implements **every** function:
  - Batch, Apply and ApplyStream apply all ops under one lock.
  - Apply and ApplyStream call `OnCommit(tag)` for a non-zero tag after applying.
  - ApplyStream pulls exactly `count` ops from `next()`.

  Mark it `// TEMPORARY STUB — replaced in E2`.
- Create `src/engine/crashpoint.go` with an exported no-op `func Hit(name string) {}`, so the CLI can call `engine.Hit("CP-W07")` from day one. E5 makes it real.
- Create placeholder `package main` files so `make check` passes: `src/cmd/kvdb/main.go` and `torture/main.go`, each with an empty `main()`.

  > **Warning:** this is the **only** time you touch `src/cmd/kvdb` or `torture/`. After M0, those directories belong to Tanushree and Preethesh. Tell them they may replace the placeholders completely.
- **Done when:** `make check` passes on main, you push and tag `m0`, and you tell the team.

**E1 · Format and streaming frame writer (hour 0.5 to 3)**
- `format.go`, `frame.go`, `superblock.go`: constants, encode/decode for the 68-byte superblock (with replay_start and max_bytes), the frame, the BATCH payload and the KEYDIR payload (with checkpoint_seq), byte-exact per SPEC section 4. CRC32C uses `crc32.MakeTable(crc32.Castagnoli)`; CRC64 uses `crc64.MakeTable(crc64.ECMA)`.
- A **streaming frame writer**: header, then ops pushed one at a time, then trailer. It uses an incremental CRC64 (`crc64.New(table)`) and writes in chunks of at most 4 MiB, so it never holds a whole frame in memory (SPEC 4.7).
- Unit tests in `src/engine/*_test.go`:
  - round trips
  - every malformed case (bad magic, bad header CRC, payload_len overflow, short payload, trailing payload bytes, op_count mismatch, key_len 0, value on DELETE, KEYDIR checkpoint_seq ≠ frame seq, unsorted KEYDIR)
- **Done when:** the tests pass, and a hex dump of an encoded frame and superblock matches the SPEC tables by hand.

**E2 · Durable commit path (hour 3 to 6) → M1**
- `locking.go`: `flock(LOCK_EX|LOCK_NB)` on `path + ".lock"`; ErrLocked if it's held.
- File creation via `path.create.tmp` → fsync → rename → fsync dir (SPEC 6.1). Temp cleanup on open (6.2).
- `commit.go`: one committer goroutine fed by a channel, processing one transaction at a time:
  1. W0: validate, size, admission (append-only for now), tail repair
  2. W1: stream the frame
  3. W2: fsync running
  4. W3: fsync returned
  5. W4: publish under the index write lock
  6. W5: `OnCommit(tag)`
  7. reply to the caller
  8. **only then** take the next transaction

  Put, Delete and Batch call Apply with tag 0. Mark every CP-W and W-state comment.
- `recover.go`: read-only full scan per SPEC 7. `index.go`: map plus RWMutex; Get = RLock → lookup → pread → RUnlock.
- Poison state machine (SPEC 11).
- **Measure fsync on the demo laptop** (hour 3): time 1000 `f.Sync()` calls. If the laptop is a Mac and syncs are under about 200 per second, tell the team and record the decision (SPEC 5.3).
- **Test dir fsync** on the demo laptop: open the directory, call Sync. Record the result in your progress file.
- Replace the stub. **Done when:** Gate 1 passes with Preethesh's H1. Test on a throwaway local branch that is never pushed:
  ```bash
  git fetch origin
  git checkout -b tmp-gate && git merge --no-edit origin/preethesh/hydra
  make check && ./bin/torture --iterations 100 --seed 1
  git checkout chethan/engine && git branch -D tmp-gate
  ```

**E3 · Streaming, sizes, tails, checkpoint (hour 6 to 10) → M2**
- `ApplyStream` (SPEC 4.7): pre-admit on payloadBytes; pull ops with `next()` while writing; on a mismatch or a `next()` error, truncate to logical_end + fsync and return ErrInvalid.
- Verify the 1 MiB values and 10,000-op batches, both buffered and streamed.
- Tail states COMPLETE / TORN / SHORT_SENTINEL, and the exact F4 rule (SPEC 7.3). Repair happens on the first write (P4), never in open().
- `checkpoint.go`: a KEYDIR frame (checkpoint_seq) plus an inactive-superblock flip with ckpt_seq and replay_start, and the binding checks in recovery (SPEC 7.1 step 5, 9).
- **F3:**
  1. Measure full-scan replay throughput R (MiB/s) and set CheckpointBytes = R × 0.5 s (SPEC 9).
  2. Benchmark 1,000,000 keys (16-byte keys, 100-byte values, loaded in 10k-op batches): kill, then time open(). Target ≤ 1.4 s.
  3. Record both numbers.
- **Done when:** Gate 2 passes and F3 is recorded in your progress file.

**E4 · Compaction (hour 10 to 14) → M3**
- `compact.go`:
  - Build `path.compact.tmp` with live keys only. Stream values from the old file in chunks; never load all values into RAM. The persisted max_bytes is copied into the new superblock.
  - Then call `replaceFile()`: fsync tmp → rename → fsync dir → swap file handle + index + metadata under the write lock (P2, P3) → close the old file.
- `replaceFile()` is shared with the disk-full rewrite (E5). There must be only one implementation.
- ForEach (sorted) and Check (SPEC 13, including the checkpoint binding).
- **Done when:** Gate 3 passes, and 1,000,000 puts + 1,000,000 deletes + compact leaves the file at 8192 + 72 bytes.

**E5 · Disk-full, persisted cap, crash hooks (hour 14 to 18) → M4**
- `admission.go`: append-or-rewrite with the headroom guard (SPEC 8). Track live accounting incrementally. `Apply` can take the REWRITE branch; `ApplyStream` only appends.
- Persisted cap (SPEC 7.4): `Open` with `SetMaxBytes` writes the inactive superblock after recovery when the cap changes (CP-S01).
- `crashpoint.go`:
  - exported `func Hit(name string)` (called as `engine.Hit("CP-W03")`). Tanushree calls `engine.Hit("CP-W07")` in serve right after an ACK line is flushed.
  - `KVDB_CRASH=CP-W03:7` kills the process with SIGKILL on the 7th hit.
  - At CP-W03, first pwrite a seeded random prefix of the frame (a real torn write, seeded by `KVDB_CRASH_SEED`).
  - `KVDB_PROBE=<file>` writes the current CP name to an mmapped 64-byte file (stretch).
  - Everything is a no-op when the env vars are unset.
- Stretch: build-tagged mutants (`//go:build mutants`, selected by `KVDB_MUTANT=M1..M6`, SPEC 15.6).
- **Done when:** Gate 4 passes. **FEATURE FREEZE.**

**E6 · Freeze (hour 18 to 24)**
- Bug fixes only. No new storage-engine features after hour 18.
- Fill in the "Code" column (file:function) of the proof table for Tanushree.
- Rehearse the crash-here answers from SPEC 12 and 12.1.

### 9.B Preethesh (`Preethesh16`): HYDRA lead

You own the torture tool: 15 points directly, and the **tie-breaker**. Read SPEC sections 12, 14 and 15 twice. HYDRA must **never** trust the database as truth; only your own oracle log is truth.

**H0 · Skeleton (hour 0.5 to 2)**
- `torture/main.go`: flags
  - `--iterations` (default 500)
  - `--seed` (required; if missing, pick one and **print it**)
  - `--mode` (default `default`)
  - `--kvdb` (default `./bin/kvdb`)
  - `--workdir` (default a fresh temp dir)
  - `--writers` (2 to 4, default 3)
  - `--max-bytes`, `--keep`, `--replay <dir>`, `--cp <name>`, `--big`, `--coverage`
- `values.go`: self-describing values (SPEC 14.3) with encode/verify plus unit tests.
- `oracle.go`: the op log data model with op_id, lane, invoke_seq/time, ack_seq/time, keys/witness and state ACKED / IN_FLIGHT / REJECTED / DEAD (SPEC 14.4, 14.5). The oracle log file is append-only.

**H1 · Random SIGKILL, default mode (hour 2 to 6) → M1**
- `randomkill.go`, one iteration:
  1. spawn `bin/kvdb serve --b64 <db>`
  2. wait for `READY`
  3. start N lanes, each pipelining up to 8 ops (put, del, batch with witness keys, mixed)
  4. one reader goroutine parses complete reply lines only, assigning ack_seq in arrival order
  5. sleep a seeded random 10 to 250 ms
  6. SIGKILL
  7. **read stdout to EOF** (flushed ACKs are still in the pipe)
  8. run `kvdb open`, `kvdb check`, `kvdb dump`
  9. compare against the oracle using the per-key rule (14.4) **and** the commit-order rules (14.5), including "at most one unACKed transaction present"
- Print one line per iteration, then `RESULT PASS n/n` (exit 0) or the failure details (exit 1).
- **Expected:** this FAILS against the M0 stub. Record that as proof the oracle works.
- **Done when:** Gate 1 passes against Chethan's E2 engine.

**H2 · Batches, sizes, reports (hour 6 to 10) → M2**
- `workloads.go`:
  - batches over fresh keys plus a witness key (all-or-none check)
  - a shared hot-key set across lanes (D2)
  - mixed batches (D3)
  - BATCH lines must carry the exact decoded payload_bytes (MASTER 8.2)
- `largevalues.go`: 256 KiB to 1 MiB values.
- `largebatches.go`: 10,000-op batches, both under 64 MiB (buffered) and over 64 MiB (streamed).
- Failure report and `failures/<seed>-<iter>/` (db copy, oracle.log, operations.log, metadata.json). `replay.go`: `--replay <dir>` re-runs the check on the saved files.
- Measure iteration time: it must stay ≤ 0.6 s (the judge runs 200 iterations in about 3 minutes).
- **Done when:** Gate 2 passes.

**H3 · Recovery, compaction and fixtures (hour 10 to 14) → M3**
- `recovery.go`:
  - Build a DB with 200,000 keys (`--big` for 1,000,000) via 10k batches.
  - Measure the clean `kvdb open` duration.
  - Each iteration: start `kvdb open`, SIGKILL at a seeded random fraction of that duration, repeat 1 to 3 times, then run a full open + check + dump. The state must equal the oracle, and the final open must take < 2 s.
- `compaction.go`: a workload plus one `COMPACT` at a seeded random moment, then kill. Every 10th iteration, let compaction finish and assert the file size shrank.
- `recoveryfixtures.go`, `--mode fixtures`: the recovery matrix (SPEC 15.8). Build each fixture from a real DB, cut or corrupt it at offsets computed from SPEC 4, and assert the exact open/check/dump result.
- **Done when:** Gate 3 passes and every fixture passes.

**H4 · Disk-full and sweep (hour 14 to 18) → M4**
- `diskfull.go`:
  - Run `kvdb open <db> --max-bytes 4194304` once (the cap is persisted).
  - Put until `ERR FULL`, then delete, then puts must ACK again (E5).
  - Kills are interleaved.
  - After every iteration, assert the file size ≤ the cap.
  - REJECTED ops must never appear.
- `sweep.go`: for each CP in SPEC 12 and hit counts 1..N, run serve with `KVDB_CRASH=<cp>:<n>` until it self-kills, then verify.

  > **Warning:** sweep must be a separate mode. The default mode stays purely random-timer.
- **Done when:** Gate 4 passes in every mode with fresh seeds.

**H5 · Systematic layers and campaigns (hour 18 to 24; HYDRA additions are allowed after the freeze)**
- `micro.go`, `--mode micro`: the 11 tiny traces in SPEC 15.7.
- `behavior.go`, `--coverage`: the behavior-coverage table (SPEC 15.9). Every transition must be hit, or be explained in BREAKING.md.
- Run 500 default + 100 of each mode with new seeds, repeatedly. Report every failure to Chethan immediately via your progress file `NEEDS:`.
- Stretch, only if everything is green: state fingerprints (15.10), `--self-test` against mutants, a kill-landing histogram via `KVDB_PROBE`.

### 9.C Tanushree (`Tanushree-RD`): CLI, tests and proof-docs lead

You own the judge-facing surface: the CLI the judge types into, the tests, and the documents worth 35 points (BREAKING 15, architecture 10, plus README accuracy). Read SPEC sections 4, 5, 12, 13 and 16 twice.

**T0 · Skeletons (hour 0.5 to 2)**
- `src/cmd/kvdb/escape.go` implementing section 8.3 escaping, with table tests (empty value, `NOTFOUND`, tab, newline, `hex:` prefix, binary).
- Fill the skeletons of `README.md`, `ARCHITECTURE.md` and `BREAKING.md` (already on main) with section headings.

**T1 · CLI and serve (hour 2 to 6) → M1**
- `src/cmd/kvdb/main.go`: all 8 commands exactly as in section 8.3. `--max-bytes` sets `Options{MaxBytes: N, SetMaxBytes: true}`; every other command leaves SetMaxBytes false. `kvdb batch` uses two passes, buffered or streamed.
- `src/cmd/kvdb/serve.go`:
  - text and b64 modes per 8.2
  - **ACKs printed from the `OnCommit` hook**, with tag = id, under the stdout mutex, flushed immediately
  - `engine.Hit("CP-W07")` right after each ACK line is flushed (a no-op until Chethan's E5 makes it real)
  - per-lane goroutines and one stdin reader using `bufio.Reader`
  - BATCH with payload_bytes, buffered ≤ 64 MiB or streamed above, always consuming exactly n lines
  - the READY line with max_bytes
  - exit 3 on ErrIO
- Build against the M0 stub; it works end to end before the real engine exists.
- `tests/cli_test.go`: a black-box test that builds `bin/kvdb` in TestMain, then exercises every command, a 1 MiB value through serve, a typed batch through text mode, and a streamed b64 BATCH over 64 MiB.
- **Done when:** `make check` passes and Gate 1 passes on main after merge.

**T2 · Correctness tests (hour 6 to 10) → M2**
- `tests/batch_test.go`: B5 (put X then del X → absent; del X then put X → present), and empty value vs delete.
- `tests/concurrent_test.go`: D4 — a reader hammering MGET during 10k-op batches never sees half a batch.
- **ACK order test:** two lanes writing the same key through serve; after each run, the value in dump must come from the op whose ACK arrived **last**.
- Binary round trip: keys and values containing 0x00, `\n`, `\t` and invalid UTF-8, plus 1024-byte keys and 1 MiB values.
- `tests/benchmark_test.go`: the F3 harness (runs only with `KVDB_BENCH=1`).
- First draft of the architecture diagram (`diagrams/architecture.*`, Excalidraw or Mermaid) with the numbered write ordering.

**T3 · Failure tests (hour 10 to 14) → M3**
- `tests/f4_test.go`: write an ACKed put, close, truncate the file by exactly 1 byte, open → the key must be present (SPEC 7.3).
- `tests/compact_test.go`: puts then deletes then Compact → the file shrinks to the expected size.
- `tests/diskfull_test.go`: E1, E2 and E5 through the CLI, plus the **persisted cap**: `kvdb open --max-bytes N`, then a later plain `kvdb put` (no flag) must still return `ERR FULL` past the cap.
- Draft all 32 rows of the ARCHITECTURE.md proof table from SPEC 16.

**T4 · Proof documents (hour 14 to 18) → M4**
- `BREAKING.md`, all 5 sections:
  1. the crash table: copy SPEC 12, then **verify every row against the real code** with Chethan, and add file:line
  2. the ordering argument (SPEC 5.2)
  3. honest weaknesses (SPEC 17)
  4. real failed attempts and bugs HYDRA found, from all three progress files; never invent any
  5. the next-24-hours list

  Max 2 pages.
- `ARCHITECTURE.md`:
  - the final diagram
  - the **persistence-order graphs** for write, checkpoint, cap change and compaction (SPEC 5.5)
  - the **W/R/C/S state machines** (SPEC 12.1)
  - 32 rows: scenario, disk state, mechanism, code file/function, verified-by mode+seed or "not verified"
  - the three guarantees (SPEC 5.4)
- `README.md`:
  - one build command (`make`)
  - the bin/ note
  - every command, plus the persisted cap and serve protocol
  - the byte-exact on-disk format, **verified against a real file**: create a DB, `put a b`, then `xxd <db> | head -n 600` and check every offset in the tables
  - the recovery algorithm in plain English
  - compaction, disk-full, concurrency, platforms

**T5 · Release (hour 18 to 24)**
- Do a clean-clone test on a second laptop: `git clone`, `make`, run every judge command from the README with Wi-Fi off.
- Run the final docs audit: every claim in BREAKING and ARCHITECTURE matches the code. A wrong row costs more than a missing one.
- Run the demo rehearsal (SPEC 18).

## 10. Definition of done (whole team, by hour 22)

- [ ] `make` from a fresh clone works offline; it needs only Go.
- [ ] All 8 CLI commands behave exactly as in section 8.3.
- [ ] `./bin/torture --iterations 500 --seed <any>` → `RESULT PASS 500/500`.
- [ ] Modes large-values, large-batches, compaction, recovery-crash and disk-full each pass 100 iterations with fresh seeds.
- [ ] 1M-key recovery measured < 2 s on the demo laptop.
- [ ] File physically shrinks after deletes + compaction.
- [ ] Persisted max_bytes is enforced by later plain `put` and `serve` processes.
- [ ] All recovery fixtures pass; behavior coverage shows no unexplained gaps; sweep covers every crash point.
- [ ] F4 one-byte truncation keeps the ACKed last write.
- [ ] README, ARCHITECTURE (diagram + 32 rows + 3 guarantees), BREAKING (5 sections, ≤ 2 pages) and PROMPTS complete and accurate.
- [ ] No AI co-author trailers in `git log` (`git log --format=%B | grep -i co-authored` prints nothing).
- [ ] Every member can explain recovery, compaction, F4 and TTL with no AI and no internet.

---

## 11. First message the AI sends its human

After reading this file and `docs/SPEC.md`, reply with:
1. "I am working with <username> as <role> on branch <branch>."
2. The list of paths you may edit.
3. Your current stage and the plan for it (5 bullets max).
4. Anything ambiguous in the spec that blocks this stage.

Then append the human's first prompt to `prompts/<username>.md` and start.
