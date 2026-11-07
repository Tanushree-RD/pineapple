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

- **Durability (ACK after fsync):** We never return or print an `ACK` to the client until `fsync()` on the database file has successfully returned. If the process is killed before `fsync` finishes, no promise was made to the client. Once `fsync` returns, the frame and its new size are physically settled on storage; only then does the committer update the in-memory index and emit the ACK.
- **Append Path (Single-file fsync is enough):** Plain appends and `ftruncate` only update the file's data blocks and its inode size. On POSIX filesystems (ext4, XFS, APFS), calling `fsync()` directly on the open file descriptor flushes both the content and the updated size. An expensive directory `fsync` is not needed for appends.
- **Atomic Replacement (Compaction and Creation):** Whenever we replace the entire file—during compaction or an out-of-space rewrite—we follow a strict three-step protocol:
  1. Write the new database image to a temporary file and call `fsync(tmp)`.
  2. Call `rename(tmp, path)` to atomically swap the directory entry.
  3. Call `fsync(parent_directory)` so the directory update itself survives a power cut.
  A crash before `rename` leaves the original database untouched (the stale temp is cleaned up on the next startup). A crash after `rename` leaves the fully-synced new database authoritative.
- **Superblock Alternation:** When taking a checkpoint, we append and `fsync` the `KEYDIR` frame first. Only after that frame is durable do we write the new checkpoint sequence and replay offset into the *inactive* superblock slot, followed by an `fsync` on the database file. The previously active superblock remains untouched until the new one is completely durable.
- **fsync Trade-off (SPEC 5.3):** We use standard Go `File.Sync()`. On our Linux NVMe laptop, this sustains ~450 fsyncs/sec (plain `fsync` gives ~456/sec), comfortably above the 200/s threshold where barrier syncs would choke performance. On macOS machines where drive barriers might dip below 200/s, running with `KVDB_PLAIN_FSYNC=1` switches to plain `fsync` (providing full process-kill durability, though not power-loss guarantees).

---

## 3. Known Weaknesses (Honest Self-Assessment)

1. **Stop-the-world compaction:** While compaction is running, all incoming writes are paused while the committer streams live entries into the new temporary file.
2. **Headroom guard is intentionally conservative:** When storage is near `--max-bytes`, a `PUT` will be rejected with `ERR FULL` if the compacted live size plus safety headroom ($S + H$) exceeds the limit, even if an exact-fit rewrite might have narrowly squeezed in. We chose this margin deliberately to prevent the engine from thrashing in expensive rewrites on every single write. (Deletes, however, always succeed).
3. **Transient disk spike during compaction:** Because the existing database and the `.compact.tmp` file coexist on disk until the atomic rename, compaction temporarily uses extra disk space. If the host filesystem itself is completely out of space (hardware ENOSPC), compaction cannot complete.
4. **Theoretical CRC64 collision:** There is a minuscule mathematical probability ($2^{-64}$) that random noise from an interrupted write happens to match the 64-bit CRC of a torn frame.
5. **Streamed batches hold the committer:** Very large streaming batches (> 64 MiB) monopolize the committer goroutine for their entire duration and can only append; they cannot perform inline space-reclaiming rewrites.
6. **In-memory RAM index:** The key directory lives entirely in memory, taking roughly 100 MB per 1 million keys. If a damaged checkpoint forces a full replay on a massive log, startup has to scan forward from byte 8192.
7. **`open` vs `check` divergence on invalid trailing frames:** If a trailing frame has a complete header but corrupt data, `open()` drops it as an incomplete `TORN` tail to favor database availability. However, `kvdb check` flags it as `CORRUPT` (exit code 1) because an unassisted SIGKILL should produce a truncated tail, not modified interior bytes.
8. **Test limitations on physical power cuts:** Our automated test harness relies on `kill -9` (SIGKILL). While our fsync ordering mathematically guarantees durability under power failure, we could only test process kills in CI, not physical power cord pulls.

---

## 4. What We Tried That Failed (Real Bugs We Hit & Fixed)

1. **Iterative MGET exposed torn batch snapshots (Scenario D4):**
   Our first version of multi-key get simply looped over keys, acquiring and releasing `RLock` on each iteration. When a 10,000-key batch committed mid-loop, the reader saw half of the new batch and half of the old state! We fixed this by rewriting `MultiGet` to hold a single read lock across the entire batch lookup.
2. **Short-value decoding bug:**
   Values between 12 and 31 bytes don't include an explicit length prefix in their wire format. During crash injection, torn records with truncated values were mistakenly decoding as valid smaller operations. We fixed this by strictly validating expected record boundaries against the frame header length.
3. **Windows crashed on directory fsync:**
   When running our test suite on Windows, calling `Sync()` on a directory handle threw an `Access is denied` OS error. We fixed this by separating platform logic into `platform_unix.go` (which syncs directory descriptors) and `platform_windows.go` (which safely no-ops directory sync, as NTFS manages directory persistence automatically).
4. **Flaky probabilistic test doubles:**
   We initially tried testing crash recovery with a test double that randomly dropped writes based on a probability seed. It was flaky and impossible to reproduce reliably across machines. We scrapped it and built deterministic corruption fixtures that flip or truncate exact byte ranges.
5. **Slow recovery on growing logs:**
   Our earliest prototype scanned the entire log from byte zero on every startup. Over 100 HYDRA crash iterations, recovery time slowed from 57 ms down to over 4 seconds. We resolved this by adding periodic `KEYDIR` checkpoint frames and buffered read-ahead, bringing recovery time on 1M keys down to 351 ms.
6. **The elusive superblock crash:**
   When trying to test `CP-S01` (crash during superblock update), random SIGKILLs almost never hit the danger zone—only ~2% of kills tore the superblock because only 68 bytes out of the 4096-byte sector hold active metadata. We had to build a dedicated sweep test to consistently hit that exact 68-byte window.
7. **Our own test generator broke the batch limit:**
   HYDRA accidentally fired a 10,001-operation batch, which the engine rejected with `ERR INVALID`. It turned out HYDRA wasn't asserting validation error codes properly, so we updated HYDRA to fail immediately on any unexpected `ERR INVALID`.

---

## 5. What We Would Build in the Next 24 Hours

1. **Concurrent, non-blocking compaction:** Implement a copy-on-write delta buffer so new incoming writes can continue while background compaction streams the snapshot to disk.
2. **Spillable index (hybrid disk/RAM):** Store cold key offsets on disk in a lightweight B-tree or index page structure, keeping only hot keys and bloom filters in RAM to support databases far larger than available memory.
3. **Group commit:** Allow multiple concurrent client worker threads to queue their writes and flush them together in a single batched `fsync()`, multiplying throughput under heavy concurrent write loads.
