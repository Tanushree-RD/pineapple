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

- **Durability:** We only send an `ACK` after `fsync()` on the data file returns. If we crash before `fsync`, the client never got an ACK. If we crash after, the data is safely on disk.
- **Appends:** Appending to an existing file updates data and file size. On POSIX filesystems, calling `fsync()` on the file descriptor is enough—we don't need a directory fsync.
- **Compaction & File Replacement:** We write the new file completely, call `fsync(tmp)`, swap it in with an atomic `rename(tmp, path)`, and then call `fsync(dir)`. A crash before rename keeps the old file; a crash after keeps the new one.
- **Superblocks & Checkpoints:** The `KEYDIR` checkpoint frame is written and fsynced first. Only then do we write the new sequence number into the inactive superblock slot and `fsync` it. The active superblock is never touched until the new one is safe.
- **fsync Speed:** We use standard `File.Sync()`. On our Linux NVMe laptop, it hits ~450 fsyncs/sec (plain fsync does ~456/sec), well above the 200/s threshold. If running on a slow macOS drive, setting `KVDB_PLAIN_FSYNC=1` falls back to plain fsync for process-kill safety.

---

## 3. Known Weaknesses

1. **Stop-the-world compaction:** All writes pause while live keys are streamed into the new file.
2. **Conservative headroom guard:** Near `--max-bytes`, a `PUT` gets rejected with `ERR FULL` if live data plus headroom ($S + H$) exceeds the cap, even if a tight rewrite might fit. Deletes always succeed.
3. **Extra disk space during compaction:** The old file and the temp file exist together on disk until the rename, temporarily using extra space.
4. **CRC64 collision risk:** A torn write matching a valid 64-bit CRC has a $2^{-64}$ theoretical chance.
5. **Big streamed batches (>64 MiB):** They lock the committer for their whole upload and can only append, never rewrite.
6. **In-memory index:** Needs ~100 MB of RAM per 1 million keys. If a checkpoint is lost or corrupt, startup has to scan the whole log.
7. **`open` vs `check`:** If the last frame has a good header but garbage data, `open()` drops it to keep the database running, but `check` flags it as `CORRUPT` (exit 1) because a clean process kill cannot produce it.
8. **Power cuts vs kill -9:** We test with `kill -9`. Power-cut safety is guaranteed by our fsync ordering, but not tested with physical plug pulls.

---

## 4. What We Tried That Failed

1. **Iterative MGET had a race (D4):** We originally looped `db.Get` per key. A 10k batch committing mid-loop exposed a half-updated state. Fixed by holding one `RLock` across the whole `MultiGet`.
2. **Short values got parsed wrong:** Values between 12 and 31 bytes don't have length headers, so truncated records were decoded as valid smaller ops. Fixed by strictly checking frame length bounds.
3. **Windows directory sync crashed:** Calling `Sync()` on a directory gave `Access is denied` on Windows. Fixed with `platform_unix.go` (syncs directory) and `platform_windows.go` (no-op).
4. **Flaky random test double:** Testing recovery with random write drops was too unpredictable. Replaced it with deterministic byte-corruption fixtures.
5. **Slow recovery without checkpoints:** Opening the database rescanned the whole log, slowing from 57 ms to 4 seconds after 100 crashes. Fixed with `KEYDIR` checkpoints (1M keys opens in 351 ms).
6. **Hard-to-hit superblock crashes:** Random kills rarely hit `CP-S01` because only 68 bytes out of 4096 actually matter. Fixed by adding a targeted sweep test.
7. **Test generator broke batch limits:** HYDRA sent a 10,001-op batch and drew `ERR INVALID`. We fixed HYDRA to respect engine limits and fail on unexpected errors.

---

## 5. Next 24 Hours

1. **Concurrent compaction:** Use write delta buffers so writes keep going while compacting.
2. **Spillable index:** Put cold keys on disk pages so the database isn't limited by RAM.
3. **Group commit:** Batch concurrent writes behind a single `fsync` under heavy load.
