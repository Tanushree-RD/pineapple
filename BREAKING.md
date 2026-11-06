# BREAKING — How We Would Break LEAPFROG-R

> **Owner:** Tanushree-RD (T4/T5), crash points verified with chethan-9817. Maximum two pages. Verified against code; incorrect rows penalized heavily.

---

## 1. Crash Points Table

Verified deterministically via `./bin/torture --mode sweep --cp <CP> --seed 1` (or `--mode recovery-crash` for R-points).

| CP | File:Line | On Disk if Killed Here | Next `open()` Behavior |
| --- | --- | --- | --- |
| CP-W01 | `commit.go:92` | Unchanged | Same state; T absent (not ACKed) |
| CP-W02 | `commit.go:175,187` | Old tail or repaired; valid prefix intact | Same logical_end; TORN ignored, SHORT_SENTINEL restored |
| CP-W03 | `commit.go:71` | Valid prefix + torn bytes past logical_end | Partial frame fails CRC/length; scan stops before it; T absent |
| CP-W04 | `commit.go:149` | Full frame in OS page cache | Frame validates; T present though unACKed (legal per Rule 2) |
| CP-W05 | `commit.go:154` | Frame durable on disk (post-fsync) | T present; durability guaranteed |
| CP-W06 | `commit.go:162` | Frame durable; published to index | T present; client unACKed (legal per Rule 2) |
| CP-W07 | `serve.go:112` | Frame durable; ACK flushed to stdout | T present (strictly required per Rule 1) |
| CP-R01 | `db.go:100` | Unchanged (recovery never writes) | Repeats superblock selection identically |
| CP-R02 | `recover.go:69` | Unchanged | Same checkpoint validation, same index |
| CP-R03 | `recover.go:78,82`| Unchanged | Same scan, same logical_end; idempotent |
| CP-C01 | `compact.go:99` | `path` untouched; empty `.compact.tmp` | Old image authoritative; temp deleted on startup |
| CP-C02 | `compact.go:109` | `path` untouched; partial `.compact.tmp` | Old image authoritative; partial temp deleted |
| CP-C03 | `compact.go:198` | `path` = old; complete temp fsynced | Old image authoritative; temp never promoted; deleted |
| CP-C04 | `compact.go:199` | Same as CP-C03 (pre-rename) | Old image authoritative; temp deleted |
| CP-C05 | `compact.go:211` | `path` = complete new image (post-rename)| New image authoritative; on rewrite T present unACKed |
| CP-C06 | `compact.go:222` | `path` = new image; in-memory swap pending| New image authoritative |
| CP-S01 | `db.go:166` | Inactive slot torn | Torn slot fails sb_crc; older slot used with full scan |

---

## 2. The Ordering Argument

- **Durability:** ACK is emitted only after file `fsync` returns, guaranteeing acknowledged frames reside on persistent media.
- **Append Path:** Appends and `ftruncate` update existing file content and size; single-file `fsync` suffices on POSIX filesystems without directory fsync.
- **Atomic Replacement:** Database creation and compaction execute:
  `fsync(tmp) -> rename(tmp, path) -> fsync(dir)`
  Directory `fsync` ensures directory metadata persists before dependent transactions ACK.
- **Superblock Alternation:** The `KEYDIR` frame is fully written and fsynced *before* the inactive superblock slot is updated with `ckpt_seq` and `replay_start`. The slot is fsynced before promotion.

---

## 3. Known Weaknesses

1. **Stop-the-World Compaction:** Writes pause during live entry streaming.
2. **Headroom Guard:** Near `max_bytes`, puts reject with `ERR FULL` if $S + H > \text{max\_bytes}$ even if compaction would fit. Deletes always succeed.
3. **Transient Footprint:** Old file and temp file coexist during compaction, briefly exceeding `max_bytes` on the host filesystem.
4. **Residual CRC64 Collision:** Theoretical $2^{-64}$ probability of random bits matching CRC64.
5. **Streamed Batch Contention:** Batches > 64 MiB hold the committer lock for their full stream duration and cannot take the rewrite path.
6. **RAM-Resident Index:** Index consumes ~100 MB RAM per 1M keys. Damaged checkpoints fall back to full scan.
7. **Diagnostic Divergence (SPEC 7.3 vs 13.6):** On an in-place bit-flipped last frame, `open()` drops it as `TORN` to prioritize availability; `check` reports `CORRUPT` (exit 1) because process kills cannot create complete-but-invalid frames.
8. **Platform Support:** Linux and macOS only. Standard POSIX fsync provides process-crash durability; power-cut durability requires platform write-barrier controls.

---

## 4. What We Tried That Failed

1. **Iterative MGET (D4):** Initially looped `db.Get`, dropping and re-acquiring `RLock`. Heavy concurrent 10k batches committed mid-loop, exposing torn snapshots. Fixed with atomic `db.MultiGet` under a single `RLock`.
2. **Short-Value Parsing:** 12–31 byte values lack internal length headers. Truncated values decoded with identical `op_id`. Fixed by asserting exact value byte lengths against oracle logs.
3. **Directory Fsync Portability:** Directory `Sync()` fails on Windows (`Access is denied`). Fixed with build-tagged OS abstractions in `platform_unix.go` and `platform_windows.go`.
4. **Probabilistic Test Doubles:** Testing `--replay` with probabilistic write droppers flaked. Replaced with deterministic single-frame corruption doubles.

---

## 5. Next 24 Hours

1. **Concurrent Compaction:** Log-structured delta buffers to allow concurrent writes during compaction.
2. **Spillable B-Tree Index:** On-disk leaf pages to support multi-million key spaces within constrained RAM.
3. **Group Commit:** Committer queue batching to amortize `fsync` latency across concurrent lanes under high load.
