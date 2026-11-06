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

## Supported Platforms

Linux and macOS. On Windows, use WSL2.
