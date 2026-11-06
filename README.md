# pineapple — LEAPFROG-R + HYDRA

Team entry for the **Kill It and Prove It** hackathon: a crash-safe, single-file key-value database (LEAPFROG-R) and the torture tool that proves it (HYDRA). Written in Go, standard library only.

> **Owner:** Tanushree-RD. Skeletons established at T0, expanded at T1-T3, finalized at T4.

---

## Start here

1. Read **[MASTER_PROMPT.md](MASTER_PROMPT.md)**: roles, branches, checkpoints, and rules.
2. Read **[docs/SPEC.md](docs/SPEC.md)**: the frozen technical specification.
3. Track the team in **[PROGRESS.md](PROGRESS.md)** (dashboard) and **[progress/](progress/)** (per-person logs).
4. Log every AI prompt in **[prompts/](prompts/)** (see [PROMPTS.md](PROMPTS.md)).

---

## Team

| GitHub | Role | Branch | Owns |
| --- | --- | --- | --- |
| `chethan-9817` | Engine lead + integrator | `chethan/engine` | `src/engine/**`, `go.mod`, `Makefile`, `.gitignore`, `.githooks/**`, `PROGRESS.md` |
| `Preethesh16` | HYDRA torture tool lead | `preethesh/hydra` | `torture/**` |
| `Tanushree-RD` | CLI, tests and proof docs lead | `tanushree/cli-docs` | `src/cmd/kvdb/**`, `tests/**`, `README.md`, `ARCHITECTURE.md`, `BREAKING.md`, `diagrams/**` |

---

## Build

```bash
make            # builds bin/kvdb and bin/torture
make check      # build + vet + tests
```

*Note on binary location:* Binaries are output to `./bin/` (`./bin/kvdb` and `./bin/torture`) to avoid name collisions with the `torture/` source directory at repo root.

---

## Commands and CLI Usage (`bin/kvdb`)

| Command | Stdout / Behavior | Exit Code |
| --- | --- | --- |
| `kvdb open <path> [--max-bytes N]` | `keys=<N> recovery_ms=<float> tail=<STATE> max_bytes=<N>` | 0 |
| `kvdb put <path> <key> <value>` | `OK` or `ERR FULL` | 0 / 2 |
| `kvdb get <path> <key>` | `<value>` or `NOTFOUND` | 0 |
| `kvdb del <path> <key>` | `OK` | 0 |
| `kvdb batch <path> <file>` | `OK` or `ERR FULL` | 0 / 2 |
| `kvdb dump <path>` | `<key>\t<value>` in ascending byte order | 0 |
| `kvdb check <path>` | `OK` + `INFO ...` or `CORRUPT <reason>` | 0 / 1 |
| `kvdb serve <path> [--max-bytes N] [--b64]` | Protocol replies (Section 8.2) | 0 (or 3 on IO error) |

Escaping rules:
- Keys and values beginning with `hex:` are hex-decoded on input.
- On output, printable ASCII (no `\t`, `\n`, `\r`, `\\`, not starting with `hex:`, and not `NOTFOUND`) is printed raw; otherwise formatted as `hex:<lowercase hex>`.

---

## Persisted Capacity (`--max-bytes`)

`--max-bytes N` sets and persists the physical file size cap into the superblock. Later commands (`put`, `del`, `batch`, `serve`) enforce this cap without requiring the flag again.

---

## Serve Protocol (`kvdb serve <path> [--max-bytes N] [--b64]`)

Ready handshake:
```text
READY keys=<N> recovery_ms=<float> max_bytes=<N>
```

### Text Mode (default)
- `put <key> <value>` -> `ACK <n>` or `ERR <n> FULL`
- `del <key>` -> `ACK <n>`
- `get <key>` -> `VAL <n> <value>` or `NOTFOUND <n>`
- `begin` ... `commit` -> `ACK <n>` or `ERR <n> FULL`
- `abort`
- `compact` -> `ACK <n>`

### Base64 Mode (`--b64`)
- `<id> <lane> PUT <k> <v>`
- `<id> <lane> DEL <k>`
- `<id> <lane> BATCH <n> <payload_bytes>`
- `<id> <lane> GET <k>`
- `<id> <lane> MGET <k1> <k2> ...`
- `<id> <lane> COMPACT`

ACKs are emitted directly by the committer's `OnCommit` hook in strict commit order.

---

## On-Disk Format (Byte-Exact)

### Layout
- Offset `0` to `4095`: Superblock slot A (4 KiB)
- Offset `4096` to `8191`: Superblock slot B (4 KiB)
- Offset `8192` onwards: Log-structured frames (seq 1, 2, 3...)

### Superblock (68 bytes used)
- Magic: `LFROG-SB` (8 bytes)
- Version: `1` (uint32)
- Generation, Epoch, Checkpoint Offset, Checkpoint Seq, Replay Start, Max Bytes, CRC32C.

### Frame (56 + L bytes)
- Header: `LFR1` (4 bytes), type (1=BATCH, 2=KEYDIR), version 1, epoch, seq, payload_len (L), header_crc (CRC32C).
- Payload: L bytes (BATCH or KEYDIR).
- Trailer: frame_crc (CRC64 over header + payload), end_magic `LF-END!!` (8 bytes).

### Real-File Verification (`kvdb put <db> a b`)

Byte dump verified via `xxd`:
- **Superblock Slot A (`0x00000000` to `0x00000043`, 68 bytes):**
  - `0x00..0x07` (8B): Magic `LFROG-SB` (`4c 46 52 4f 47 2d 53 42`)
  - `0x08..0x0b` (4B): Version `1` (`01 00 00 00`)
  - `0x0c..0x0f` (4B): Reserved (`00 00 00 00`)
  - `0x10..0x17` (8B): Generation `1` (`01 00 00 00 00 00 00 00`)
  - `0x18..0x1f` (8B): Epoch (`<random uint64>`)
  - `0x20..0x27` (8B): ckpt_offset (`0`)
  - `0x28..0x2f` (8B): ckpt_seq (`0`)
  - `0x30..0x37` (8B): replay_start (`0`)
  - `0x38..0x3f` (8B): max_bytes (`0`)
  - `0x40..0x43` (4B): sb_crc (CRC32C of bytes 0..63)
- **Log Frame 1 at `0x00002000` (8192 bytes, length = 69B):**
  - `0x2000..0x2003` (4B): Magic `LFR1` (`4c 46 52 31`)
  - `0x2004` (1B): Type `1` (OpBatch)
  - `0x2005` (1B): Version `1`
  - `0x2006..0x2007` (2B): Reserved
  - `0x2008..0x200f` (8B): Epoch matching superblock
  - `0x2010..0x2017` (8B): Seq `1`
  - `0x2018..0x201f` (8B): Payload length `13` (`0d 00 00 00 00 00 00 00`)
  - `0x2020..0x2023` (4B): Header CRC32C
  - `0x2024..0x2027` (4B): Reserved
  - `0x2028..0x202b` (4B): Op count `1` (`01 00 00 00`)
  - `0x202c` (1B): Kind `1` (OpPut)
  - `0x202d..0x202e` (2B): Key len `1` (`01 00`)
  - `0x202f..0x2032` (4B): Value len `1` (`01 00 00 00`)
  - `0x2033` (1B): Key `'a'` (`0x61`)
  - `0x2034` (1B): Value `'b'` (`0x62`)
  - `0x2035..0x203c` (8B): Frame CRC64-ECMA
  - `0x203d..0x2044` (8B): Trailer magic `LF-END!!` (`4c 46 2d 45 4e 44 21 21`)

---

## Recovery Algorithm

1. Acquire exclusive lock on `path.lock`.
2. Delete stale `.create.tmp` and `.compact.tmp` files.
3. Validate and compare superblocks A and B; select highest valid generation.
4. If checkpoint exists and binds, load in-memory index from KEYDIR and scan from `replay_start`. Otherwise, full scan from offset 8192.
5. Strict sequential frame validation up to first invalid byte.
6. Determine tail state (`COMPLETE`, `TORN`, or `SHORT_SENTINEL`).
7. Update persisted max_bytes if `--max-bytes` was explicitly provided.

---

## Compaction and Disk-Full Rewrite

- Compaction streams live keys in ascending order into `path.compact.tmp` in packed frames (≤ 4 MiB payload).
- Performs atomic replacement: `fsync(tmp)` -> `rename` -> `fsync(dir)` -> swap memory state.
- Disk-full admission uses `S_ub` upper-bound calculation with headroom guard.

---

## Guarantees and Concurrency

- **Durability:** ACK follows file `fsync`.
- **Atomicity:** Frames protected by CRC64 and sentinel; all-or-nothing recovery.
- **Recoverability:** Read-only recovery writes no bytes to disk.

---

## HYDRA Torture Testing (`bin/torture`)

HYDRA is the independent adversarial torture tool that verifies crash-safety and durability using an append-only oracle log.

### Modes and Commands

1. **Default Mode (Random SIGKILL):**
   ```bash
   ./bin/torture --iterations 500 --seed 12345
   ```
2. **Workload Stress Modes:**
   ```bash
   ./bin/torture --iterations 100 --seed 999 --mode large-values
   ./bin/torture --iterations 100 --seed 5   --mode large-batches
   ./bin/torture --iterations 100 --seed 7   --mode compaction
   ./bin/torture --iterations 100 --seed 42  --mode recovery-crash
   ./bin/torture --iterations 100 --seed 3   --mode disk-full
   ```
3. **Deterministic Sweep Mode (`--mode sweep [--cp CP] [--hits N]`):**
   Iterates through crash points `CP-W01` to `CP-S01` injecting process kills and torn writes at exact code execution points:
   ```bash
   ./bin/torture --mode sweep --seed 1
   ./bin/torture --mode sweep --seed 1 --cp CP-W05
   ```
   *Real Example Output:*
   ```text
   warm-up: 11406 keys in the shared database
   sweep CP-W05  n=1 [write]    reached     op 70437463654401 PRESENT tail=COMPLETE  check=OK PASS
   sweep CP-W05  n=2 [write]    reached     op 70506183131138 PRESENT tail=COMPLETE  check=OK PASS
   sweep CP-W05  n=3 [write]    reached     op 70574902607875 PRESENT tail=COMPLETE  check=OK PASS
   sweep CP-W05  n=4 [write]    reached     op 70643622084612 PRESENT tail=COMPLETE  check=OK PASS
   sweep CP-W05  n=5 [write]    reached     op 70712341561349 PRESENT tail=COMPLETE  check=OK PASS
   sweep CP-W05  n=6 [write]    reached     op 70781061038086 PRESENT tail=COMPLETE  check=OK PASS
   RESULT PASS 6 cases, 6 of them killed exactly at their crash point (seed=1, mode=sweep, 3s)
   ```
4. **Adversarial Micro Traces (`--mode micro`):**
   Executes 11 minimal adversarial crash traces (T1 to T11) with both crash-point sweep hooks and random kills:
   ```bash
   ./bin/torture --mode micro --seed 1
   ```
   *Real Example Output:*
   ```text
   micro T1  PUT A                                                 9 crash-point runs ( 9 distinct points), 20 random kills  PASS
   micro T2  PUT A; PUT A                                         15 crash-point runs ( 9 distinct points), 20 random kills  PASS
   micro T3  PUT A; DEL A                                         15 crash-point runs ( 9 distinct points), 20 random kills  PASS
   micro T4  BATCH(PUT A, PUT B)                                   9 crash-point runs ( 9 distinct points), 20 random kills  PASS
   micro T5  BATCH(PUT X, DEL X)                                   9 crash-point runs ( 9 distinct points), 20 random kills  PASS
   micro T6  PUT A; COMPACT                                       17 crash-point runs (15 distinct points), 20 random kills  PASS
   micro T7  DEL A; COMPACT; PUT A                                22 crash-point runs (15 distinct points), 20 random kills  PASS
   micro T8  write past the checkpoint threshold, kill inside the superblock flip  1 crash-point runs ( 1 distinct points), 0 random kills  PASS
   micro T9  F4: truncate 1 byte; PUT B                           11 crash-point runs (10 distinct points), 20 random kills  PASS
   micro T10 open --max-bytes N (cap change)                       4 crash-point runs ( 1 distinct points), 20 random kills  PASS
   micro T11 at the cap: DEL A (rewrite path)                     12 crash-point runs (11 distinct points), 20 random kills  PASS
   RESULT PASS micro traces (seed=1, 1m12s)
   ```
5. **Recovery Fixtures Matrix (`--mode fixtures`):**
   Evaluates 27 crafted database corruptions, cuts, and boundary conditions against strict expected verdicts:
   ```bash
   ./bin/torture --mode fixtures
   ```
   *Real Example Output:*
   ```text
   fixture clean-empty                        PASS
   fixture clean-many                         PASS
   fixture torn-header                        PASS
   fixture torn-payload                       PASS
   fixture missing-crc                        PASS
   fixture torn-in-frame-crc                  PASS
   fixture short-sentinel-1                   PASS
   ...
   fixture stale.compact-tmp                  PASS
   fixture stale.create-tmp                   PASS
   RESULT PASS 27/27 fixtures
   ```
6. **Behavior Coverage Reporting (`--coverage`):**
   Inspects saved traces in `failures/coverage-*.json` (run `sweep`, `micro`, and `fixtures` first) and reports hit counts across all 24 persistence transitions:
   ```bash
   ./bin/torture --coverage
   ```
   *Real Example Output:*
   ```text
   BEHAVIOR                 TRANSITION                                                  HITS  SOURCES
   W normal append          CP-W01 W0 admitted, nothing written                           18  micro:T1 x1, sweep:write x6...
   W normal append          CP-W03 W1 frame being streamed (torn write)                   17  micro:T1 x1, sweep:write x6...
   W normal append          CP-W04 W2 whole frame, fsync pending                          17  micro:T1 x1, sweep:write x6...
   W normal append          CP-W05 W3 fsync returned (durable)                            17  micro:T1 x1, sweep:write x6...
   W normal append          CP-W06 W4 published, ACK pending                              17  micro:T1 x1, sweep:write x6...
   W normal append          CP-W07 W5 ACK flushed                                         20  micro:T1 x1, sweep:write x6...
   R recovery               CP-R01 R1 superblock chosen                                   10  micro:T1 x1, sweep:recover x1...
   ...
   F4 tail repair           end_magic restored, fsync pending (CP-W02 after)               1  sweep:repair-short x1
   COVERAGE PASS: all 24 transitions were exercised by a kill or a crafted crash image.
   ```
7. **Deterministic Failure Replay (`--replay failures/<seed>-<iter>`):**
   Re-executes oracle verification deterministically on captured crash artifacts (`database.db`, `oracle.log`, `operations.log`, `metadata.json`):
   ```bash
   ./bin/torture --replay failures/12345-17
   ```

### Storage Advice (`--workdir`)

> [!WARNING]
> **Do NOT use tmpfs (RAM disk) for `--workdir`!**
> On Linux `tmpfs`, `fsync` is effectively a free no-op in memory. As a result, the database acknowledges tens of thousands of operations per second, causing the database file to grow hundreds of times faster and making runs 10x slower while undermining honest durability testing. Always run HYDRA on a real persistent filesystem (e.g. ext4, XFS, APFS, or standard physical disk directory).

---

## Supported Platforms

Linux and macOS. On Windows, use WSL2.
