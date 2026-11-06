# BREAKING — How we would break LEAPFROG-R

> **Owner:** Tanushree-RD (final at T4), with crash-point verification by chethan-9817. **Maximum two pages.** Every crash point is checked against the code by the judges: a row described wrongly costs more than a row left out.

---

## 1. Crash points table

| CP | Where (file:line) | On disk if killed here | What the next open() does | Sweep command |
| --- | --- | --- | --- | --- |
| CP-W01 | `src/engine/commit.go:92` | Unchanged | Same state as before T. T absent (not ACKed) | `./bin/torture --mode sweep --cp CP-W01 --seed 1` |
| CP-W02 | `src/engine/commit.go:175, 187` | Either old tail or repaired; valid prefix unchanged | Same logical_end and index. TORN tail ignored; short sentinel accepted/restored | `./bin/torture --mode sweep --cp CP-W02 --seed 1` |
| CP-W03 | `src/engine/commit.go:71` | Valid prefix plus seeded torn prefix past logical_end | Partial frame fails length/CRC, scan stops before it, T absent | `./bin/torture --mode sweep --cp CP-W03 --seed 1` |
| CP-W04 | `src/engine/commit.go:149` | Full frame present in file (page cache survives process kill) | Frame validates; T is present though not ACKed (legal under rule 2) | `./bin/torture --mode sweep --cp CP-W04 --seed 1` |
| CP-W05 | `src/engine/commit.go:154` | Frame is durable on disk | T present | `./bin/torture --mode sweep --cp CP-W05 --seed 1` |
| CP-W06 | `src/engine/commit.go:162` | Frame is durable on disk | T present; client never heard back (legal) | `./bin/torture --mode sweep --cp CP-W06 --seed 1` |
| CP-W07 | `src/cmd/kvdb/serve.go:112` | Frame is durable on disk | T present. Required (rule 1) | `./bin/torture --mode sweep --cp CP-W07 --seed 1` |
| CP-R01 | `src/engine/db.go:100` | Unchanged (recovery writes nothing) | Repeats the same selection | `./bin/torture --mode recovery-crash --seed 1` |
| CP-R02 | `src/engine/recover.go:69` | Unchanged | Same checkpoint validation, same index | `./bin/torture --mode recovery-crash --seed 1` |
| CP-R03 | `src/engine/recover.go:78, 82` | Unchanged | Same scan, same logical_end. Idempotent | `./bin/torture --mode recovery-crash --seed 1` |
| CP-C01 | `src/engine/compact.go:99` | `path` untouched; empty temp | Old image; temp deleted | `./bin/torture --mode sweep --cp CP-C01 --seed 1` |
| CP-C02 | `src/engine/compact.go:109` | `path` untouched; partial temp with torn prefix | Old image; temp deleted | `./bin/torture --mode sweep --cp CP-C02 --seed 1` |
| CP-C03 | `src/engine/compact.go:198` | `path` = old; complete temp fsynced | Old image; temp deleted. Never promoted | `./bin/torture --mode sweep --cp CP-C03 --seed 1` |
| CP-C04 | `src/engine/compact.go:199` | Same as CP-C03 | Old image | `./bin/torture --mode sweep --cp CP-C04 --seed 1` |
| CP-C05 | `src/engine/compact.go:211` | `path` = complete new image | New image. On REWRITE, T is present but not ACKed (legal) | `./bin/torture --mode sweep --cp CP-C05 --seed 1` |
| CP-C06 | `src/engine/compact.go:222` | `path` = complete new image | New image | `./bin/torture --mode sweep --cp CP-C06 --seed 1` |
| CP-S01 | `src/engine/db.go:166` | The slot holds seeded torn prefix | Torn slot fails sb_crc. Other slot is used; same index | `./bin/torture --mode sweep --cp CP-S01 --seed 1` |

---

## 2. The ordering argument

- **Durability:** The ACK follows `fsync` of the data file, guaranteeing that an acknowledged frame is fully committed to non-volatile storage.
- **No directory fsync for appends:** Normal appends and `ftruncate` modify only existing file contents and length; `fsync` on the file descriptor covers metadata changes like file size on POSIX filesystems.
- **Directory fsync after renames:** New databases (`path.create.tmp`) and compactions (`path.compact.tmp`) follow:
  ```
  fsync(tmp) ──► rename(tmp, path) ──► fsync(dir)
  ```
  The directory `fsync` ensures the directory entry update is durable prior to any transaction acknowledgement dependent on the new file.
- **Superblock flips:** A KEYDIR frame is written and fsynced *before* the inactive superblock slot is updated with `ckpt_off`, `ckpt_seq`, and `replay_start`. Then the superblock slot is fsynced. An active superblock slot never points to unpersisted bytes.

---

## 3. Known weaknesses

1. **Stop-the-world compaction:** Compaction and rewrite pause write transactions while streaming live entries.
2. **Headroom guard near cap:** Near `max_bytes`, a put transaction may return `ERR FULL` if `S + H > max_bytes`, even if a compacted rewrite might have fit tightly.
3. **Transient disk footprint:** During compaction and rewrite, old file and temp file simultaneously exist on disk, temporarily exceeding `max_bytes` on the underlying filesystem.
4. **Residual CRC64 collision:** In theory, a 1-in-2^64 probability of an arbitrary bit error matching CRC64.
5. **Streamed batch locks committer:** Very large batches (> 64 MiB) stream directly through `ApplyStream`, holding the committer lock for their duration and refusing rewrites near capacity.
6. **RAM index:** Entire key-to-offset index is memory-resident (~100 MB per 1M keys).
7. **Diagnostic divergence on in-place corrupted last frame (SPEC 7.3 vs 13.3):** If a complete last frame suffers an in-place single-byte flip, `open` treats it as a non-fatal `TORN` tail per SPEC 7.3 ("bytes that do not form a valid frame") and recovers the intact prefix up to that point. In contrast, `check` performs a forensic inspection and prints `CORRUPT frame seq=... at offset ...: <reason>` (exit code 1) per SPEC 13.3 to alert the operator. As clarified in SPEC 13.6: flip one byte in the last record: `open` drops it, `check` reports `CORRUPT`, because a kill cannot produce a complete-but-invalid frame.

---

## 4. What we tried that failed

*Real issues encountered and resolved during implementation and testing:*
1. **Iterative MGET non-atomicity (D4):** The initial `serve MGET` implementation executed a loop over `db.Get`, releasing and re-acquiring the index `RLock` per key. Under heavy concurrent batch workloads, a 10,000-op atomic batch could commit between two key lookups, presenting a split batch state to the client. Resolved by introducing `db.MultiGet(keys)` which holds a single `RLock` across all keys.
2. **Short value length validation:** In HYDRA, 12-to-31 byte values lack an explicit length header (consisting only of op_id, key hash prefix, and PRNG payload). A truncated short value still decoded with the same op_id. Resolved by comparing the decoded size against the exact size recorded in the oracle.
3. **Directory fsync portability:** In POSIX environments, directory fsync ensures rename persistence. On Windows development environments, calling `Sync()` on a directory file descriptor returns `Access is denied`. Resolved by isolating OS-specific sync calls into `platform_unix.go` and `platform_windows.go`.
4. **Replay test flake with probabilistic drops:** Testing the `--replay` command with a probabilistic `droplast` test double failed to fail deterministically on iteration 1. Replaced with an explicit deterministic `garble` fault injection double.

---

## 5. If we had 24 more hours

1. **Concurrent background compaction:** Implement copy-on-write or split-log architecture so writes can continue during compaction.
2. **On-disk B-tree / Radix index:** Spill index pages to disk to eliminate RAM index footprint for multimillion-key databases.
3. **Dynamic group commit:** Adaptive batching on the committer channel during extreme concurrent write load without sacrificing individual fsync latency.
