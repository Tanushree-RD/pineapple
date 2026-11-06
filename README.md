# pineapple 🍍 — a database you can `kill -9`, and the tool that tries to prove it wrong

**LEAPFROG-R** is a crash-safe, single-file key-value database. **HYDRA** is the torture tool that kills it
at random and at exact instructions, and checks every answer against an independent record.
Go, standard library only, **zero dependencies** (`go.mod` has no `require` lines).
Built for the *Kill It and Prove It* hackathon by a team of three.

```bash
git clone https://github.com/chethan-9817/pineapple.git && cd pineapple
make                                            # needs only Go 1.22+ and make
./bin/torture --iterations 200 --seed 12345     # kill it 200 times, verify every time
```

---

## At a glance

| Question | Answer | How to check it |
| --- | --- | --- |
| What survives a `kill -9`? | **Every acknowledged write.** An unacknowledged write is either fully there or fully gone. A batch is never half-there. | the demo below, or `./bin/torture` |
| How many unacknowledged writes can appear after a crash? | **At most one**, the transaction that was in flight at the kill. | demo below: keys on disk = ACKs, or ACKs + 1 |
| Does recovery change the file? | **No.** `open` writes zero bytes; HYDRA hashes the file before and after, every iteration. | `./bin/torture` (built-in check) |
| What can fail to open? | Nothing our own crashes can produce. The torn tail is ignored and the first write repairs it. | `./bin/torture --mode fixtures` |
| How is it tested? | 229 Go tests, 9 HYDRA modes plus `--coverage` and `--replay`, 11 adversarial traces, 31 crash images, and a sweep that kills it at every named crash point. | `make check`, then the commands below |
| How fast is recovery? | 1,000,000 keys: **351 ms** with a checkpoint, **743 ms** full scan (Linux, NVMe laptop). Target was 1.4 s. | `KVDB_BENCH=1 go test ./src/engine -run BenchF3 -v` |
| What does it not cover? | Power loss. `kill -9` cannot see a missing `fsync`. See [Honest limits](#honest-limits). | [BREAKING.md](BREAKING.md) |

---

## See it work in 30 seconds

Stream a million writes into the server, `kill -9` it mid-stream, and count. (`bash`; real output from this repo.)

```bash
( for i in $(seq 1 1000000); do echo "put key$i value$i"; done ) | ./bin/kvdb serve kill.db > acks.txt &
sleep 0.4; kill -9 $(pgrep -n -f "kvdb serve kill.db"); wait 2>/dev/null

echo "ACKs received before the kill: $(grep -c -E '^ACK [0-9]+$' acks.txt)"
./bin/kvdb open kill.db
./bin/kvdb check kill.db | head -1
echo "keys on disk after the kill:   $(./bin/kvdb dump kill.db | wc -l)"
```

```text
ACKs received before the kill: 84
keys=85 recovery_ms=0.475 tail=COMPLETE max_bytes=0
OK
keys on disk after the kill:   85
```

Three runs gave 84→85, 76→77 and 69→70. Every acknowledged write is on disk, plus **exactly one more**: the
transaction that was already durable when the process died but whose ACK had not been flushed yet. Never
zero, never two. That one-extra rule is the strongest check HYDRA makes, and the reason is the design below.

---

## Quickstart

```bash
make            # builds bin/kvdb and bin/torture
make check      # build + go vet + all tests
```

> Binaries go to `./bin/` (`./bin/kvdb`, `./bin/torture`) because a `torture/` source directory and a
> `./torture` binary cannot both live at the repo root.

```bash
./bin/kvdb put demo.db city Mumbai          # OK
./bin/kvdb get demo.db city                 # Mumbai
./bin/kvdb put demo.db blob hex:00ff10      # binary-safe: hex: prefix on input
./bin/kvdb dump demo.db                     # blob<TAB>hex:00ff10   city<TAB>Mumbai   (ascending byte order)
./bin/kvdb check demo.db                    # OK + INFO lines (superblocks, frames, tail state)
./bin/kvdb open demo.db --max-bytes 4194304 # persist a size cap; later commands enforce it
```

Interactive server, text mode (a batch is `begin … commit`, one atomic frame):

```text
$ printf 'put a 1\nbegin\nput b 2\ndel a\ncommit\nget b\nget a\ncompact\n' | ./bin/kvdb serve fresh.db
READY keys=0 recovery_ms=5.442 max_bytes=0
ACK 1
ACK 2
VAL 3 2
NOTFOUND 4
ACK 5
```

### The 15-minute judge path

| Step | Command | What you should see |
| --- | --- | --- |
| Build | `git clone … && make` | builds offline, needs only Go |
| Random kills | `./bin/torture --iterations 200 --seed X` | one `PASS` line per iteration, then `RESULT PASS 200/200` |
| Compaction, recovery | `./bin/torture --mode compaction --seed X` and `--mode recovery-crash --seed X` | `RESULT PASS`, file shrinks, recovery killed mid-way leaves the file byte-identical |
| By hand | the 30-second demo above, or `kvdb serve` text mode, `kill -9` it 5 to 10 times, then `check` and `dump` | `check` prints `OK` and an `INFO tail=…` line |
| Crash-here questions | open any `// CP-xxx` comment in `src/engine`, read the row in [BREAKING.md](BREAKING.md) | disk state and next-`open()` behaviour for that line |

Run HYDRA on a **real disk**, not tmpfs/RAM (`--workdir ~/somewhere`): `fsync` is free in RAM, so the database
acknowledges tens of thousands of writes per iteration and runs get 10× slower. HYDRA prints a `NOTE` if it
detects tmpfs.

---

## Two friendly front ends (optional)

`make` and `make check` are unchanged and build only `kvdb` and `torture`. If you want a nicer way to poke at the
database and to run the tests, `make ui` adds two more programs. Same rules: standard library only (the colors are plain
ANSI codes, arrow keys use `stty`), and everything falls back to plain text when output is piped or `NO_COLOR=1` is set.

```bash
make ui
./bin/leapfrog            # menu: database shell · live kill -9 demo · file inspector
./bin/hydra               # control room: judge path · any of the 32 scenarios · any mode · everything
```

**`leapfrog`** talks to the engine directly, so every `put` is a real write plus `fsync` and it shows how long that took.

| Command | What it does |
| --- | --- |
| `leapfrog shell demo.db` | `put`, `get`, `del`, `begin … commit` (one atomic frame), `dump`, `check`, `compact`, `cap N`, `fill N`, `stats`, `inspect`, `crash` |
| `leapfrog demo --rounds 5` | feeds a real `kvdb serve` as fast as it will go, `SIGKILL`s it at a random moment, opens the file and counts: every ACKed write on disk, plus at most one |
| `leapfrog inspect demo.db -v` | decodes the bytes with its own parser: both superblocks, every frame with its CRC64 check, the tail state, and a byte-by-byte label of the first frame |

**`hydra`** does not reimplement any test. It runs the real `bin/torture` and `bin/kvdb`, parses what they print, and draws it.

| Command | What it does |
| --- | --- |
| `hydra` | arrow-key menu: judge path, a scenario, a mode, everything, quick mode on or off |
| `hydra judge` | the 15-minute path of SPEC 18 in one command: random kills, compaction, recovery killed mid-way, a manual `kill -9` test, fixtures, sweep, micro, coverage |
| `hydra scenarios` / `hydra scenario B5 A4 F4` | the 32 scenarios of SPEC 16; each one runs exactly the HYDRA modes, `kvdb` checks and Go tests that exercise it |
| `hydra all` | all 32 with a scoreboard (`--quick` uses a tenth of the iterations: about a minute) |
| `hydra run sweep --cp CP-W05` | one mode with a live dashboard: progress bar, pass/fail counts, sparklines of kill timing, ACKs and key counts |
| `hydra replay failures/<seed>-<iter>` | re-run the check on a saved failure |

```text
╭─ HYDRA default · --iterations 30 --seed 75254 ───────────────────────────────╮
│ ✔ ██████████████████████████████████████████ 30/30  100%                     │
│ ✔ 30 passed   0 failed   5.1/s   elapsed 0:06                                │
│ recent ▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮▮                                        │
│ kill ms ▁▁▆▅▅▃█▅▂▁▅▇▂▁▄▂▇▆▅▂▄▇▇▇▂▆▂▂▇▇                                       │
│ ACKed   ▁▁▅▅▅▃▆▅▂▁▄▆▁▁▄▂▇▆▅▂▄▇▇▇▂▆▂▂▇█                                       │
│ keys    ▁▁▁▁▂▂▂▃▃▃▃▄▄▄▄▄▅▅▅▅▅▆▆▇▇▇▇▇▇█                                       │
│ last    iter 30 · killed at 249 ms · 127 ACKed · 24 in flight · 2977 keys ·… │
╰──────────────────────────────────────────────────────────────────────────────╯
```

Both tools were themselves tested the way the database is: `hydra`'s by-hand checks were run against engines with planted bugs
(a short-sentinel bug broke `F4`, a missing size check broke `E1`) and failed with the exact reason. Working files go
under your user cache directory (a real disk), never `/tmp`.

---

## Why a crash cannot hurt it

One rule, followed on every write path:

```text
pwrite(frame) ──► fsync(file) ──► publish to the in-memory index ──► ACK to the client ──► next transaction
                  ▲ durability point: nothing is visible or acknowledged before this returns
```

- **One committer.** A single goroutine takes transactions one at a time, so commit order is total. The server
  prints each ACK from the committer *before* the next transaction starts. That makes ACK order equal commit
  order, and it is why at most one unacknowledged transaction can ever be durable.
- **One frame per transaction.** A put, a delete and a 10,000-op batch are each one frame with a CRC32C header
  check, a CRC64 over header and payload, and an end sentinel. A frame with any missing or altered byte is
  rejected whole, so none of its operations applies.
- **Recovery writes nothing.** `open` reads both superblocks, validates the checkpoint, scans frames strictly,
  and stops at the first invalid byte. The torn tail is repaired by the *first write*, never by `open`, so a
  crash during recovery leaves the file untouched and the next recovery computes the same thing.
- **Replacing files is atomic.** Compaction builds `path.compact.tmp`, `fsync`s it, `rename`s it over the
  database, then `fsync`s the directory. The path always names a complete image.
- **Errors poison, never retry.** Any failed write, `fsync` or rename returns `ErrIO` and refuses further writes.

The three guarantees (durability, atomicity, recoverability) with every system call named are in
[ARCHITECTURE.md](ARCHITECTURE.md). The 17 named crash points and what is on disk at each are in
[BREAKING.md](BREAKING.md); every one is a `// CP-xxx` comment in the code and a deterministic HYDRA case.

---

## HYDRA — how the torture tool proves it

HYDRA never believes the database. Its own append-only `oracle.log` (what it sent, what it was ACKed) is the
truth; `kvdb dump` is only an observation.

1. Start `kvdb serve`, run 2 to 4 concurrent lanes of puts, deletes, batches (with a witness key) and
   contended hot keys, each value **self-describing** (op id, key hash, CRC, pseudo-random body).
2. `SIGKILL` after a seeded delay, read stdout to the end, run `open`, `check`, `dump`.
3. **Per key** the survivor must be the ACKed write with the highest ACK order, or the single in-flight
   transaction if it is complete on every key it touches. Anything else fails: a lost ACKed write, a half
   batch, two unacknowledged transactions, a rejected (`ERR FULL`) write that appeared, an old value that came back,
   or a value that is torn or belongs to another key.
4. Also checked after every kill: the file's CRC64 is unchanged by `open`/`check`/`dump`, `ERR INVALID` never
   happens, and a persisted size cap is enforced without being repeated.

A seed reproduces the workload and the requested kill delay (not OS scheduling). `sweep` is exactly reproducible.

| Mode | What it does |
| --- | --- |
| `default` | random-timer kills, the judge's mode. Never uses crash hooks. |
| `large-values` | every value 256 KiB to 1 MiB |
| `large-batches` | 10,000-op batches, buffered and streamed (> 64 MiB), plus concurrent writers |
| `compaction` | a `COMPACT` at a seeded moment; every 10th iteration asserts the file physically shrank |
| `recovery-crash` | 200,000 keys (`--big`: 1,000,000); kills `kvdb open` mid-recovery 1 to 3 times; file must stay byte-identical |
| `disk-full` | cap set once with `open --max-bytes`; full, then delete, then a put must ACK again |
| `sweep [--cp CP-W03] [--hits N]` | kills at **exactly** each crash point `CP-W01…CP-S01`, with a seeded torn prefix at write points, and groups the resulting disk states |
| `micro` | 11 tiny traces (`PUT A; DEL A`, `BATCH(PUT X, DEL X)`, F4, cap change, rewrite at the cap…) killed at every crash point they pass plus 20 random instants, then written to again |
| `fixtures` | 31 crafted crash images: torn header/payload/CRC, short sentinel (1 to 8 bytes), bad CRC, stale frames, torn superblock, checkpoints, stale temp files, clean close, refused compaction, failed write (`ERR IO`, exit 3) |
| `--coverage` | table of every persistence transition with hit counts; zero hits is a gap and exits 1 |
| `--replay failures/<seed>-<iter>` | re-runs the check on a saved failure, deterministically |

```text
iter 17/500 seed=12345 mode=default kill_ms=134 acked=212 inflight=6 rejected=0 keys=8123 open_ms=41 check=OK PASS
sweep CP-W05  n=3 [write]    reached     op 70574902607875 PRESENT tail=COMPLETE  check=OK PASS
sweep CP-C05  n=1 [rewrite]  reached     new image, put present tail=COMPLETE  check=OK PASS
fixture short-sentinel-3                   PASS
RESULT PASS 500/500 (seed=20260101, mode=default, 3m37s)
```

Full HYDRA documentation: [torture/README.md](torture/README.md).

### Does the tool actually have teeth?

A torture tool that never fails proves nothing, so HYDRA was run against deliberately broken databases and
engines. Each of these made it **fail**, with a precise message:

| Planted bug | Caught by | Message (abridged) |
| --- | --- | --- |
| database keeps everything in RAM | `default` | `ACKed write LOST` (10,419 ACKed writes gone) |
| recovery ignores the newest record | `default` | `ACKed write LOST` / `commit order violated` |
| recovery ignores deletes | `default` | `ACKed DELETE undone (resurrection)` |
| batch written one op at a time | `default` | `batch atomicity violated … witness key` |
| recovery truncates the torn tail itself | `sweep` | `recovery must write no bytes` |
| short sentinel rejected (breaks F4) | `fixtures` | 9 fixtures fail |
| epoch check skipped | `fixtures` | stale frame accepted, 2 fixtures fail |
| torn tail not repaired before a write | `fixtures` | `tail=TORN` where `COMPLETE` required |
| puts accepted beyond the size cap | `disk-full` | file 24 MB against a 4 MB cap |
| deletes refused at the cap | `disk-full` | `a delete needs no tombstone space (E5)` |
| `COMPACT` ignores the cap precheck | `fixtures` | `COMPACT … should answer ERR … FULL` |

And against the real engine it found real things: the first engine's recovery rescanned the whole log, so `open`
grew from 57 ms to over 4 s across 100 iterations (fixed by read-ahead and checkpoints); the torn-write hook at
`CP-S01` tore the superblock in only about 2% of runs (fixed); and `kvdb check` and `open` disagreed about a complete but
invalid last frame (the SPEC now states it: `open` drops it, `check` says `CORRUPT`).

---

## Evidence

Linux, real `SIGKILL`, work dir on an NVMe disk (not tmpfs), fresh seeds. All pass.

| Run | Result |
| --- | --- |
| `./bin/torture --iterations 100 --seed 1` (Gate 1) | PASS 100/100, about 0.27 s per iteration |
| `./bin/torture --iterations 500 --seed 20260101` | PASS 500/500, 3m37s |
| `--mode compaction --iterations 100 --seed 31` | PASS 100/100 |
| `--mode recovery-crash --iterations 100 --seed 42` | PASS 100/100, 179 of 187 kills landed mid-recovery |
| `--mode disk-full --iterations 100 --seed 53` | PASS 100/100, 83 iterations hit `ERR FULL`, 20 delete-then-put proofs |
| `--mode large-values --iterations 100 --seed 64` | PASS 100/100 |
| `--mode large-batches` seeds 75 and 76 | PASS 50/50 and 50/50 (two runs of 50: 100 would write about 3.5 GB) |
| `--mode fixtures` | PASS 31/31 |
| `--mode sweep --seed 11` | PASS 62 cases, 59 killed exactly at their crash point |
| `--mode micro --seed 8` | PASS 11/11 traces |
| `--coverage` | PASS, 24/24 persistence transitions exercised |
| F3: 1,000,000 keys (Linux NVMe) | open **351 ms** with checkpoint, **743 ms** full scan, single-pass `check` 1.29 s |
| `fsync` on the same machine | about 450 per second; directory `fsync` works |

The scoreboard with every gate and who ran it is in [PROGRESS.md](PROGRESS.md).

---

## Command reference (`bin/kvdb`)

| Command | Stdout | Exit |
| --- | --- | --- |
| `kvdb open <path> [--max-bytes N]` | `keys=<N> recovery_ms=<float> tail=<STATE> max_bytes=<N>` | 0 |
| `kvdb put <path> <key> <value>` | `OK` or `ERR FULL` | 0 / 2 |
| `kvdb get <path> <key>` | `<value>` or `NOTFOUND` | 0 |
| `kvdb del <path> <key>` | `OK` (deleting a missing key is not an error) | 0 |
| `kvdb batch <path> <file>` | `OK` or `ERR FULL`; file lines are `put <key> <value…>` or `del <key>`, `#` comments | 0 / 2 |
| `kvdb dump <path>` | `<key>\t<value>`, ascending byte order | 0 |
| `kvdb check <path>` | `OK` + `INFO …` lines, or `CORRUPT <reason>` | 0 / 1 |
| `kvdb serve <path> [--max-bytes N] [--b64]` | `READY …`, then replies (below) | 0, or 3 on I/O error |

Any other error prints `ERR <reason>` on stderr and exits 1. Tail states are `COMPLETE`, `TORN` and `SHORT_SENTINEL`.
`check` reports a cut-short tail as `INFO` and still prints `OK`; a **complete but invalid** frame is `CORRUPT` (SPEC 13.6).

**Escaping.** On input, a key or value starting with `hex:` is hex-decoded. On output, a token is printed raw
only if it is non-empty printable ASCII, has no tab, newline or backslash, does not start with `hex:` and is
not exactly `NOTFOUND`; otherwise it is `hex:<lowercase hex>`. The empty value prints as `hex:`.

**Limits.** Key 1 to 1,024 bytes; value 0 to 1 MiB; 1 to 10,000 operations per batch; at least 1,000,000 keys.

**Persisted cap.** `--max-bytes N` is written to the superblock. Later `put`, `del`, `batch` and `serve`
processes enforce it without being told again. A cap below the current size is allowed; puts then return `FULL`
until a delete-through-rewrite or compaction shrinks the image.

<details>
<summary><b>serve protocol</b> (text mode for humans, base64 mode for HYDRA)</summary>

First line, always, after recovery: `READY keys=<N> recovery_ms=<float> max_bytes=<N>`.

Text mode: `put <key> <value…>` → `ACK <n>` or `ERR <n> FULL`; `del <key>` → `ACK <n>`; `get <key>` →
`VAL <n> <value>` or `NOTFOUND <n>`; `begin` … `commit` → one `ACK <n>` or `ERR <n> FULL`; `abort`; `compact` → `ACK <n>`.

Base64 mode (`--b64`), one request per line, `<id>` unique ≥ 1, `<lane>` a small integer, `-` is the empty value:
`<id> <lane> PUT <k> <v>`, `DEL <k>`, `BATCH <n> <payload_bytes>` followed by exactly *n* `PUT`/`DEL` lines,
`GET <k>`, `MGET <k1> <k2> …` (one consistent snapshot), `COMPACT`. Replies: `ACK <id>`, `ERR <id> FULL|IO|INVALID`,
`VAL <id> <v>`, `NOTFOUND <id>`, `MVAL <id> …`.

ACKs are printed by the committer's `OnCommit` hook, in commit order, before the next transaction starts. Each
lane runs on its own goroutine; a batch above 64 MiB of payload streams instead of buffering (a buffering
threshold, not a size cap). On `ERR IO` the process exits 3.
</details>

---

## On-disk format (byte-exact, verified against a real file)

All integers are little-endian. Checks: CRC32C (Castagnoli) and CRC64 (ECMA).

```text
offset   0       4096      8192                                                      logical_end
         ├────────┼─────────┼─────────┬─────────┬─────┬──────────────────┬─────┬────────┤
         │ SB  A  │  SB  B  │ frame 1 │ frame 2 │ ... │ KEYDIR checkpoint│ ... │ torn?  │
         └────────┴─────────┴─────────┴─────────┴─────┴──────────────────┴─────┴────────┘
          4 KiB     4 KiB     seq 1     seq 2           (an ordinary frame)         ignored, repaired
                                                                                    by the first write
```

| Superblock offset | Size | Field |
| --- | --- | --- |
| 0 | 8 | magic `LFROG-SB` |
| 8 / 12 | 4 / 4 | version `1` / reserved |
| 16 | 8 | generation (higher valid generation wins) |
| 24 | 8 | epoch (random id of this image; every frame carries it) |
| 32 / 40 / 48 | 8 each | `ckpt_off`, `ckpt_seq`, `replay_start` (0 = no checkpoint) |
| 56 | 8 | `max_bytes` (persisted cap, 0 = none) |
| 64 | 4 | `sb_crc` CRC32C of bytes 0 to 63 |

A superblock update always writes the *inactive* slot with generation + 1, then `fsync`s: a torn slot fails its CRC and the
other slot is used.

| Frame offset | Size | Field |
| --- | --- | --- |
| 0 | 4 | magic `LFR1` |
| 4 / 5 / 6 | 1 / 1 / 2 | type (1 BATCH, 2 KEYDIR) / version `1` / reserved |
| 8 / 16 / 24 | 8 each | epoch, seq, `payload_len` L |
| 32 / 36 | 4 / 4 | `header_crc` CRC32C of bytes 0 to 31 (checked *before* L is trusted) / reserved |
| 40 | L | payload |
| 40+L | 8 | `frame_crc` CRC64 of bytes 0 to 39+L |
| 48+L | 8 | end magic `LF-END!!` |

BATCH payload: `op_count u32`, then per op `kind u8` (1 PUT, 2 DELETE), `key_len u16`, `value_len u32`, key, value.
Ops apply in order and the last op on a key wins (`PUT X; DEL X` leaves X absent).

Real file from `kvdb put db a b` (verify with `xxd db` or `hexdump -C db`): the 69-byte frame at `0x2000` is
`4c 46 52 31` · type `01` · version `01` · reserved · epoch · seq `1` · `payload_len` `0d` · `header_crc` · `00000000` ·
`01 00 00 00` (1 op) · `01` (PUT) · `01 00` (key_len) · `01 00 00 00` (value_len) · `61` (`a`) · `62` (`b`) ·
`frame_crc` · `4c 46 2d 45 4e 44 21 21`.

Full tables, the KEYDIR checkpoint layout and the sentinel/F4 rule: [docs/SPEC.md](docs/SPEC.md) section 4 and 7.

---

## Recovery, compaction, disk-full, concurrency

**Recovery (`open`).** (1) take `path.lock`, which is a `flock` on a separate file; (2) delete stale `.create.tmp` and
`.compact.tmp`; (3) pick the valid superblock with the highest generation; (4) if its checkpoint validates, load the
index from the KEYDIR frame and scan from `replay_start`, otherwise scan everything from offset 8192; (5) accept frames
strictly, one at a time, and stop at the first invalid byte; (6) classify the tail as `COMPLETE`, `TORN` or `SHORT_SENTINEL`;
(7) only if `--max-bytes` was given and differs, write the new cap into the inactive slot. No byte of the data file is
written before step 7.

**F4 (the judge truncates one byte).** If only the end sentinel is cut, header, payload and CRC64 are all intact, so the
frame is accepted and an acknowledged last write survives.

**Compaction.** Live keys stream in ascending order into packed frames (payload ≤ 4 MiB) in `path.compact.tmp`, values
copied through one reused buffer; then `fsync(tmp)` → `rename` → `fsync(dir)` → swap handle, index and metadata under one
write lock. One implementation serves both `COMPACT` and the disk-full rewrite. Result: 1,000,000 puts plus 1,000,000
deletes plus a compact leave the file at exactly 8192 + 72 bytes (139.3 MiB shrinks in 14 ms;
`KVDB_BENCH=1 go test ./src/engine -run BenchCompact -v` asserts the size).

**Disk-full.** The cap limits the physical size of each database image. A write that does not fit is either *appended*, or
the engine *rewrites* a compacted image if the live data plus a headroom guard fits, or it returns `ERR FULL` before any byte
is written. Deleting at the cap reclaims space because a rewrite needs no tombstone.

**Concurrency.** Many goroutines may call the engine. Readers hold the index read-lock across lookup and `pread`; a batch is
published under one write-lock acquisition, so a reader never sees half a batch (`MGET` gives a consistent snapshot).

**Platforms.** Linux and macOS (`flock`, `SIGKILL`, `pread`). On Windows use WSL2. On macOS `File.Sync` is
`F_FULLFSYNC`; `KVDB_PLAIN_FSYNC=1` selects plain `fsync` if that is too slow for a demo (durability then holds against process
kills, not power loss).

---

## Honest limits

- **Power loss is out of scope for the tests.** A `SIGKILL` leaves the page cache intact, so a missing `fsync` or a wrong
  write order cannot be observed by any kill-based test. The ordering is argued in [BREAKING.md](BREAKING.md) section 2;
  proving it would need a power-cut harness we do not have.
- Compaction and disk-full rewrite are stop-the-world (writes wait). Near the cap a put can return `FULL` although a rewrite
  would barely fit (the headroom guard). The whole index lives in RAM. A streamed batch over 64 MiB can only append.
- `large-batches` was run as two runs of 50, not one of 100, for disk space. Where no test covers a proof-table row,
  [ARCHITECTURE.md](ARCHITECTURE.md) says "not verified" instead of claiming it (a real ENOSPC on a full disk: HYDRA forces a failing write with a file-size limit instead, which takes the same error path).
- The TTL extension is a prepared design (SPEC section 19), not code.
- Deeper list of known weaknesses, crash points and failed attempts: [BREAKING.md](BREAKING.md).

---

## Repository map

| Path | What |
| --- | --- |
| `src/engine/` | the database: frames, committer, recovery, checkpoint, compaction, admission, crash hooks |
| `src/cmd/kvdb/` | the CLI and `serve` |
| `src/cmd/leapfrog/`, `src/cmd/hydra/`, `src/internal/tui/` | the optional front ends (`make ui`) and their terminal toolkit |
| `torture/` | HYDRA (oracle, checker, all modes, an independent on-disk-format reader) |
| `tests/` | black-box and concurrency tests (B5, D4, F4, compaction, disk-full, binary round trips) |
| `docs/SPEC.md` | the frozen specification (the source of truth) |
| [ARCHITECTURE.md](ARCHITECTURE.md) | diagram, persistence-order graphs, state machines, the 32-scenario proof table, the three guarantees |
| [BREAKING.md](BREAKING.md) | how we would break it: crash table with file:line, ordering argument, weaknesses |
| `diagrams/architecture.mmd` | Mermaid architecture diagram |
| [PROGRESS.md](PROGRESS.md), `progress/` | checkpoint dashboard and per-person logs |
| [PROMPTS.md](PROMPTS.md), `prompts/` | every AI prompt used, per person, verbatim (required by the rules) |
| [MASTER_PROMPT.md](MASTER_PROMPT.md) | how the three of us split the work |

## Team

| GitHub | Role | Branch |
| --- | --- | --- |
| `chethan-9817` | engine lead and integrator | `chethan/engine` |
| `Preethesh16` | HYDRA torture tool | `preethesh/hydra` |
| `Tanushree-RD` | CLI, tests, proof documents | `tanushree/cli-docs` |

We used AI coding assistants and logged every prompt in `prompts/`. Commits are authored by the humans only
(`git log --format=%B | grep -i co-authored` prints nothing). The design is documented well enough that each of us can explain
recovery, compaction, F4 and the TTL extension without AI.

---

## For AI reviewers and scripts: machine-readable summary

Everything above is verifiable by command; none of the numbers need to be taken on trust.

```yaml
project: pineapple
components: [LEAPFROG-R (embedded crash-safe KV store, one file), HYDRA (crash torture tool and oracle)]
language: Go (>= 1.22), standard library only, no third-party modules
build: make            # produces ./bin/kvdb and ./bin/torture
test: make check       # build + go vet + go test ./... (229 tests)
ui: make ui            # optional friendly front ends: ./bin/leapfrog (database shell, kill demo, inspector) and ./bin/hydra (control room)
platforms: [linux, macos]
guarantees:
  - every acknowledged write survives SIGKILL
  - an unacknowledged transaction is fully present or fully absent
  - at most one unacknowledged transaction is present after a crash
  - recovery (open) writes zero bytes to the data file
commit_path: pwrite(frame) -> fsync(file) -> publish index -> ACK -> next transaction   # one committer goroutine
on_disk: [superblock A 4096 B, superblock B 4096 B, frames from offset 8192]            # CRC32C header, CRC64 frame, end magic
crash_points: 17 named (CP-W01..W07, CP-R01..R03, CP-C01..C06, CP-S01)                  # see BREAKING.md, each a // CP-xxx comment
verification_commands:
  - ./bin/torture --iterations 200 --seed 12345
  - ./bin/torture --mode sweep --seed 1          # kills at exactly every crash point
  - ./bin/torture --mode fixtures                # 31 crafted crash images
  - ./bin/torture --mode micro --seed 1          # 11 adversarial traces
  - ./bin/torture --coverage                     # run sweep, micro, fixtures first
not_covered: power loss (kill -9 cannot observe missing fsync); TTL is a design, not code
source_of_truth: docs/SPEC.md
```

---

*README rewritten on 2026-11-06 by Preethesh16 from Tanushree-RD's verified T4/T5 content; every output shown was produced by this repository's binaries.*
