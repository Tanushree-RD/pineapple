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
| CP-S01 | `db.go:169` | Inactive slot torn | Torn slot fails sb_crc; older slot used with full scan |

---

## 2. The Ordering Argument

- **Durability:** ACK is emitted only after file `fsync` returns, guaranteeing acknowledged frames reside on persistent media.
- **Append Path:** Appends and `ftruncate` update existing file content and size; single-file `fsync` suffices on POSIX filesystems without directory fsync.
- **Atomic Replacement:** Database creation and compaction execute:
  `fsync(tmp) -> rename(tmp, path) -> fsync(dir)`
  Directory `fsync` ensures directory metadata persists before dependent transactions ACK.
- **Superblock Alternation:** The `KEYDIR` frame is fully written and fsynced *before* the inactive superblock slot is updated with `ckpt_seq` and `replay_start`. The slot is fsynced before promotion.
- **fsync (SPEC 5.3 decision):** default `File.Sync()`. Linux NVMe laptop: 450/s (plain `fsync` 456/s), above the ~200/s switch point; directory sync works. Not measured on macOS: if the demo laptop is a Mac below ~200/s, set `KVDB_PLAIN_FSYNC=1` (process-kill durability only).

---

## 3. Known Weaknesses

1. **Stop-the-world compaction:** writes pause while live entries stream.
2. **Headroom guard:** near `max_bytes` a put returns `ERR FULL` if $S + H > \text{max\_bytes}$ even when a rewrite would fit. Deletes always succeed.
3. **Transient footprint:** old file and temp coexist during compaction, briefly exceeding `max_bytes` on the filesystem.
4. **Residual CRC64 collision:** a torn frame matches its CRC64 with probability $2^{-64}$.
5. **Streamed batches (> 64 MiB)** hold the committer for their whole stream and can only append, never rewrite.
6. **RAM-resident index:** about 100 MB per 1M keys; a damaged checkpoint falls back to a full scan.
7. **`open` vs `check` (SPEC 7.3 vs 13.6):** a complete but invalid last frame is dropped by `open()` as `TORN` (availability) and reported `CORRUPT` (exit 1) by `check`, because a process kill cannot produce one.
8. **Platforms:** Linux and macOS. `kill -9` tests cannot observe a missing `fsync` or wrong write order; power-cut durability is argued (section 2), not tested.

---

## 4. What We Tried That Failed

1. **Iterative MGET (D4):** looped `db.Get`, re-taking `RLock` per key, so a 10k batch could commit mid-loop and expose a torn snapshot; fixed with atomic `db.MultiGet` under one `RLock`.
2. **Short-Value Parsing:** 12 to 31 byte values have no length header, so truncated ones decoded with the same `op_id`; fixed by asserting exact lengths against the oracle.
3. **Directory Fsync Portability:** directory `Sync()` fails on Windows (`Access is denied`); fixed with build-tagged `platform_unix.go` / `platform_windows.go`.
4. **Probabilistic Test Doubles:** `--replay` tested with a random write dropper flaked; replaced by a deterministic corruption double.
5. **Recovery Rescanned the Whole Log:** the first engine's `open` grew from 57 ms to 4 s over 100 HYDRA iterations; fixed with read-ahead and checkpoints (1M keys: 351 ms).
6. **Torn-Write Hook Too Gentle:** `CP-S01` tore the superblock in only ~2% of kills (68 of 4096 bytes matter); found by the sweep, fixed.
7. **Our Generator Broke the Limit:** a 10,001-op batch drew `ERR INVALID`; HYDRA now fails on any `ERR INVALID`.

---

## 5. Next 24 Hours

1. **Concurrent compaction:** delta buffers so writes continue while compacting.
2. **Spillable index:** on-disk leaf pages for key spaces beyond RAM.
3. **Group commit:** batch concurrent lanes behind one `fsync` under heavy load.
