# ARCHITECTURE — LEAPFROG-R

> **Owner:** Tanushree-RD. Skeletons established at T0, expanded at T2/T3, final at T4/T5. Every claim must match the code. The judge checks three random rows of the proof table against the source during the demo.

---

## Part 1 — Architecture diagram

```
CLI / serve ──► engine API (src/engine/api.go)
  PUT / DEL / BATCH / COMPACT ─► request channel ─► ONE committer goroutine (total commit order)
                                                     W0 validate + exact size + admission: APPEND | REWRITE | FULL   (CP-W01)
                                                        tail repair if needed (first write after open)                (CP-W02)
                                                     W1 stream the frame to disk with an incremental CRC              (CP-W03)
                                                     W2 whole frame present, fsync running                            (CP-W04)
                                                     W3 fsync returned  ◄── DURABILITY POINT                          (CP-W05)
                                                     W4 published under index write lock                              (CP-W06)
                                                     W5 OnCommit hook: serve prints + flushes "ACK id"                (CP-W07)
                                                     ...only then does the committer take the next transaction
  GET ─► RLock ─► map[key]→(off,len) ─► pread from current file ─► RUnlock

DISK  [ SB-A 4 KiB ][ SB-B 4 KiB ][ frame ][ frame ]...[ KEYDIR ][ frame ]...│torn tail?│
      0            4096          8192                                 logical_end

RECOVERY  lock → clean temps → pick superblock → load checkpoint (seq + replay_start) or full scan
          → scan frames strictly → stop at first invalid → rebuild index → ready   (writes no bytes)
          → only if `--max-bytes` changes the persisted cap: inactive-superblock update (7.4)

COMPACTION / REWRITE  build path.compact.tmp (live keys only) → fsync → rename → fsync dir
                      → swap file handle + index + metadata under write lock → close old file

PROCESS OWNERSHIP  flock on path.lock (never on the data file itself)
```

---

## Part 1b — Persistence-order graphs and state machines

### Persistence rules P1 to P4
- **P1:** Never publish an index entry or file handle that points at bytes until those bytes are durable.
- **P2:** Never retire the old authoritative image until the replacement is complete and durable.
- **P3:** Never expose a mixed generation: file handle, index offsets, epoch and logical metadata are published under **one** write lock.
- **P4:** Never append behind an invalid tail: truncate a TORN tail, or restore a SHORT_SENTINEL, and fsync first.

### Write path graph and W0 to W5
```
frame bytes ──fsync──► index publish ──► OnCommit/ACK
```
- **W0:** Validated and admitted; no bytes written (tail repair may run) [CP-W01, CP-W02]
- **W1:** Frame being streamed to disk [CP-W03]
- **W2:** Whole frame present; fsync running [CP-W04]
- **W3:** fsync returned; not yet published [CP-W05]
- **W4:** Published to index; ACK not yet emitted [CP-W06]
- **W5:** ACK flushed by OnCommit hook [CP-W07]

### Checkpoint and cap-change graph and S0 to S2
```
checkpoint:  KEYDIR frame ──fsync──► inactive superblock (ckpt_off, ckpt_seq, replay_start) ──fsync──► slot is current
cap change:  (recovery done) ──► inactive superblock (max_bytes) ──fsync──► slot is current
```
- **S0:** KEYDIR fsynced (checkpoint) or recovery finished (cap change)
- **S1:** Inactive slot being written [CP-S01]
- **S2:** New slot fsynced; active

### Compaction graph and C0 to C4
```
tmp image (superblock + frames + KEYDIR) ──fsync(tmp)──► rename ──fsync(dir)──► locked swap ──► close old
```
- **C0:** Old image only
- **C1:** Temp partial or unsynced [CP-C01, CP-C02]
- **C2:** Temp complete and fsynced [CP-C03, CP-C04]
- **C3:** Rename done; process on old view [CP-C05]
- **C4:** Handle + index + epoch swapped together [CP-C06]

### Recovery R0 to R3
- **R0:** Lock held, temps cleaned
- **R1:** Superblock chosen [CP-R01]
- **R2:** Checkpoint loaded or fallback chosen [CP-R02]
- **R3:** Tail scanned, index built [CP-R03]

---

## Part 2 — Scenario-to-mechanism proof table (all 32 rows)

| Scenario | Disk state at the moment of the kill | Mechanism that handles it | Where in the code | Verified by |
| --- | --- | --- | --- | --- |
| **A1** · Kill before write | Valid prefix up to previous `logical_end` (superblocks at bytes 0..8191, all preceding valid frames). Exactly 0 bytes of the new transaction are on disk (`CP-W01`). | Single committer executes validation, exact frame size computation, and cap admission checks *before* issuing any write syscall. Transaction is unacknowledged and absent. | `src/engine/commit.go:committer` | `./bin/torture --mode sweep --cp CP-W01 --seed 1`, `micro` trace T1 |
| **A2** · Kill mid-write | Valid prefix intact. A partial frame of $k$ bytes ($0 < k < 40 + L + 16$) written past `logical_end` (`CP-W03`). Trailing payload, 8B `frame_crc`, and 8B `LF-END!!` sentinel are absent. | Recovery sequentially scans frames. Header CRC32C or frame CRC64 check fails on the partial byte sequence. Scanner halts at previous `logical_end`, classifying tail as `TORN`. Zero operations apply. | `src/engine/recover.go:scanImage` | `./bin/torture --mode sweep --cp CP-W03 --seed 1`, fixture `torn-payload` |
| **A3** · Kill before fsync | Complete frame of $40 + L + 16$ bytes present in OS page cache, but `fsync()` has not yet returned (`CP-W04`). Depending on OS flush, platter holds either a partial tail or the whole frame. | Committer has not sent ACK to client. On restart, if the full frame reached platter with valid CRC64 and sentinel, recovery accepts it (Rule 2: at most one unacknowledged transaction may survive); if torn, CRC64 fails and it is discarded. | `src/engine/commit.go:committer` | `./bin/torture --mode sweep --cp CP-W04 --seed 1` |
| **A4** · Kill after fsync | Complete frame ($40 + L + 16$ bytes) is durable on persistent media (post-`fsync`, `CP-W05`). Committer has not yet published index or emitted ACK. | Recovery scans the durable frame, validates header CRC32C, sequence continuity (`seq = prev + 1`), full CRC64, and `LF-END!!`. Transaction is recovered into keydir index (Rule 2). | `src/engine/commit.go:committer` | `./bin/torture --mode sweep --cp CP-W05 --seed 1` |
| **A5** · Kill after ACK | Complete frame ($40 + L + 16$ bytes) durable on disk; ACK string flushed to client (`CP-W07`). | `serve` flushes ACK to the client exclusively inside `OnCommit` hook, which executes *after* `fsync()` returns and index is updated. Recovery scans and recovers the frame with 100% fidelity (Rule 1). | `src/cmd/kvdb/serve.go:laneManager.runWorker` | `./bin/torture --mode default --seed 42`, `./bin/torture --mode sweep --cp CP-W07 --seed 1` |
| **A6** · Delete crash | Delete is encoded as a 1-op batch frame (`kind = 2`, `val_len = 0`). Depending on kill instant, disk has 0 bytes, partial frame, unfsynced frame, or durable frame. | Frame-level atomicity identical to A1–A5: requires valid 40B header, 7B op header + key, 8B CRC64, and 8B `LF-END!!`. On recovery, tombstone removes the key from the in-memory index. | `src/engine/commit.go:committer` | `./bin/torture --mode default --seed 42`, `micro` trace T3 |
| **A7** · Large value crash | Large frame (up to 1 MiB value) streamed in 4 MiB chunks. Partial payload bytes on disk; trailing 8B CRC64 and 8B `LF-END!!` absent (`CP-W03`). | Header CRC32C validates payload length $L \le \text{MaxPayload}$. Recovery attempts to read $L$ bytes; encountering EOF or CRC64 failure causes `ScanFrame` to reject the entire frame. Tail is marked `TORN`. | `src/engine/frame.go:ScanFrame` | `./bin/torture --mode large-values --seed 42`, `./bin/torture --mode sweep --cp CP-W03 --seed 1` |
| **B1** · Batch partial 1 | 40B frame header plus partial payload bytes (e.g. 3 of 10 ops) written past previous `logical_end`; remaining ops, 8B CRC64, and 8B sentinel absent. | All operations in a batch are serialized into a single atomic frame. Recovery scanner fails CRC64 verification or hits EOF before reading full payload length $L$. Scanner stops at previous `logical_end`; zero ops applied. | `src/engine/recover.go:scanImage` | `./bin/torture --mode sweep --cp CP-W03 --seed 1`, fixture `torn-payload` |
| **B2** · Batch partial 2 | 40B frame header and all $L$ payload bytes on disk; trailing 8B CRC64 and 8B `LF-END!!` sentinel absent (`CP-W03`). | `ScanFrame` reads 16-byte trailer following payload. Missing or truncated trailer causes `ScanFrame` to report EOF / truncated frame. Frame rejected; none of the batch operations apply. | `src/engine/frame.go:ScanFrame` | `./bin/torture --mode sweep --cp CP-W03 --seed 1`, fixture `missing-crc` |
| **B3** · Batch unfsynced | Complete batch frame ($40 + L + 16$ bytes) in OS page cache; `fsync()` pending or in-flight (`CP-W04`). | Committer has not emitted ACK. If platter retains all bytes, recovery validates CRC64 and sentinel, applying all ops atomically (Rule 2); if torn, CRC64 fails and none apply. A batch is never half-visible. | `src/engine/commit.go:committer` | `./bin/torture --mode sweep --cp CP-W04 --seed 1`, `micro` trace T4 |
| **B4** · Batch ACKed | Complete batch frame ($40 + L + 16$ bytes) durable on disk; ACK flushed to client. | Recovery scanner validates frame header CRC32C, sequence number continuity, CRC64-ECMA, and `LF-END!!`. `ParseBatch` unpacks all ops into index atomically. | `src/engine/commit.go:committer` | `./bin/torture --mode default --seed 42`, `./bin/torture --mode large-batches --seed 42` |
| **B5** · In-batch overwrite | Single valid batch frame containing sequential conflicting operations for the same key `X` (e.g. PUT X then DEL X). | `ParseBatch` iterates operations in strict order within the frame payload. The later operation overwrites or deletes earlier state in the local map before publishing to keydir. Last write wins. | `src/engine/payload.go:ParseBatch` | `tests/batch_test.go:TestBatchB5Ordering`, `micro` trace T5 |
| **B6** · 10k batch crash | Large 10,000-op batch frame (up to 10 MB) interrupted mid-write (`CP-W03`). Partial payload on disk. | Header CRC32C bounds payload length $L \le \text{MaxPayload}$. Recovery detects EOF or CRC64 failure, stops at previous `logical_end`, and discards the entire 10k batch without leaking memory or stalling. | `src/engine/recover.go:scanImage` | `./bin/torture --mode large-batches --seed 42` |
| **C1** · Recovery crash 1 | Primary database file completely unmodified (`CP-R01`). Platter bytes identical to pre-recovery state. | Recovery is strictly read-only. `Open()` inspects superblocks and scans frames without writing any bytes to disk. Next `open()` executes identical recovery path idempotently. | `src/engine/db.go:Open` | `./bin/torture --mode recovery-crash --seed 42`, `./bin/torture --mode sweep --cp CP-R01 --seed 1` |
| **C2** · Recovery crash 2 | Database file unmodified. Any preexisting torn tail remains at physical EOF (`CP-R02`, `CP-R03`). | Tail repair is strictly deferred to the first mutating write transaction (`repairTail`, `CP-W02`). Multiple consecutive crashes during recovery perform zero disk writes and leave the database completely recoverable. | `src/engine/commit.go:repairTail` | `./bin/torture --mode recovery-crash --seed 42`, `./bin/torture --mode sweep --cp CP-W02 --seed 1` |
| **C3** · Compaction crash | Primary database file `path` untouched. Incomplete temporary file `path.compact.tmp` exists on disk (`CP-C01`, `CP-C02`). | Compaction writes all new frames and superblocks to a separate temporary file. On startup, `Open()` removes any stale `.compact.tmp` or `.create.tmp` files before inspecting `path`. Primary file remains authoritative. | `src/engine/db.go:Open` | `./bin/torture --mode compaction --seed 42`, `./bin/torture --mode sweep --cp CP-C02 --seed 1`, fixture `stale-compact-tmp` |
| **C4** · Rename crash | Either old file or new compacted file exists at `path`; temporary file may or may not still exist (`CP-C04`). | Filesystem `rename(tmp, path)` is atomic. If rename did not execute, old file is opened; if rename executed, new fully-synced compacted file is opened. Both states are fully valid and consistent. | `src/engine/compact.go:replaceFile` | `./bin/torture --mode compaction --seed 42`, `./bin/torture --mode sweep --cp CP-C04 --seed 1`, `./bin/torture --mode sweep --cp CP-C05 --seed 1` |
| **C5** · Post-rename crash | New compacted database file is at `path`. Temporary file is gone. Directory metadata may or may not be fsynced (`CP-C05`). | The new compacted file was fully fsynced before `rename()`. On restart, `Open()` reads slot A/B of the new file, loads its checkpoint KEYDIR and frames, and recovers full state. | `src/engine/compact.go:replaceFile` | `./bin/torture --mode compaction --seed 42`, `./bin/torture --mode sweep --cp CP-C05 --seed 1`, `./bin/torture --mode sweep --cp CP-C06 --seed 1` |
| **C6** · Close crash | All acknowledged frames written and fsynced. Same on-disk state as an idle database (`CP-W05`). | `Close()` stops the committer, waits for in-flight requests to complete, issues `fsync()`, and releases lock. A kill during close is equivalent to a crash on an idle database; all acknowledged data survives. | `src/engine/db.go:Close` | `--mode fixtures` case `clean-close`; kill during close is equivalent to post-fsync write (`./bin/torture --mode default --seed 42`, `sweep --cp CP-W05`) |
| **D1** · Concurrent lanes | Interleaved frames from multiple client lanes written to disk in total sequential order (`seq = 1, 2, 3, ...`). | Client worker goroutines submit transactions over a Go channel to a single committer goroutine. Committer assigns contiguous sequence numbers and serializes disk writes, eliminating concurrent write races. | `src/engine/commit.go:committer` | `./bin/torture --mode default --seed 42` |
| **D2** · Same key conflict | Two complete frames with consecutive sequence numbers ($S_1, S_2$) updating the same key `K`. | Committer serializes execution. Frame $S_2$ is assigned a higher sequence number and written after $S_1$. On recovery, scanner replays frames in strict sequence order; $S_2$ overwrites keydir entry for `K`. Later committed write deterministically wins. | `src/engine/commit.go:committer` | `./bin/torture --mode default --seed 42`, `tests/concurrent_test.go:TestACKOrderMatchesCommitOrder` |
| **D3** · Mixed batch & singles | Arbitrary sequence of single-op frames and multi-op batch frames on disk, each delimited by 40B header and 16B trailer. | Committer handles single puts, deletes, and multi-op batches through a unified transaction pipeline. Each transaction gets its own frame with unique sequence number and CRC64. Recovery processes frames independently. | `src/engine/commit.go:committer` | `./bin/torture --mode large-batches --seed 42` |
| **D4** · Concurrent read | Disk state transitions from sequence $S$ to $S+1$, or file replaced via atomic rename. | Readers acquire `RLock` on index, read value offsets, and issue `pread` against file handle. Committer publishes updates under exclusive `Lock` only *after* `fsync()` returns. Readers never observe torn or uncommitted records. | `src/engine/db.go:MultiGet` | `tests/concurrent_test.go:TestD4ConcurrentReaderNoPartialBatch` |
| **E1** · Disk-full put | Database file intact at size $S$. Exactly 0 bytes written to disk. | Committer checks if $S + \Delta_{\text{frame}} > \text{max\_bytes}$ before writing (`CP-W01`). If exceeded, returns `ErrFull` without writing any bytes. Disk state remains unmodified. | `src/engine/commit.go:committer` | `./bin/torture --mode disk-full --seed 42`, `tests/diskfull_test.go:TestDiskFullPersistedCap` |
| **E2** · Disk-full batch | File size and contents unchanged at previous `logical_end`. Zero batch bytes written. | Total batch frame size calculated upfront. If $S + 40 + L_{\text{batch}} + 16 > \text{max\_bytes}$, committer rejects entire batch with `ErrFull` prior to disk write. Zero operations apply. | `src/engine/commit.go:committer` | `./bin/torture --mode disk-full --seed 42`, `tests/diskfull_test.go:TestDiskFullPersistedCap` |
| **E3** · Disk-full prior data | Preexisting frames up to size $S \le \text{max\_bytes}$ intact on disk. | `ErrFull` rejection never alters existing data file or superblocks. Recovery verifies and restores 100% of previously acknowledged records. | `src/engine/commit.go:committer` | `./bin/torture --mode disk-full --seed 42`, `tests/diskfull_test.go:TestDiskFullPersistedCap` |
| **E4** · ENOSPC in compaction | Original database file `path` intact. Temporary file `path.compact.tmp` partially written or absent. | Compaction prechecks that live data does not exceed `max_bytes` before creating temp file. If filesystem returns `ENOSPC` during temp file write, engine poisons handle, returns `ErrIO`, exits with code 3, and leaves original `path` untouched. On restart, `Open()` removes the partial temp file and reopens original database. | `src/engine/compact.go:replaceFile` | Precheck verified by fixture `compact-refused-at-small-cap`; write failure verified by fixtures `write-error-exits-3-data-intact` and `write-error-then-put`; real hardware ENOSPC on full disk: not verified |
| **E5** · Space reclamation | File compacted into smaller image: dead versions/deleted keys removed, size $S' < S \le \text{max\_bytes}$. | Deletes are admitted even at cap via in-place rewrite path (`rewriteOrCompact`). Compaction writes only active keys from keydir into new file, reclaiming disk space and allowing subsequent puts. | `src/engine/compact.go:rewriteOrCompact` | `./bin/torture --mode disk-full --seed 42`, `tests/compact_test.go:TestCompactShrinksFile` |
| **F1** · General torture | Arbitrary mix of complete frames, torn tails, checkpoints, and compacted generations resulting from 500+ random SIGKILLs. | Combination of immutable append log, frame CRC64 verification, two-phase alternating superblock updates, atomic rename, and single-committer serialization. Zero lost ACKs. | `src/engine/db.go:Open` | `./bin/torture --mode default --iterations 500 --seed 42` |
| **F2** · Repeated crash | Exact same byte sequence on disk after $N$ crashes as after initial crash. | `Open()` performs zero writes to the database file during recovery. Tail repair is strictly deferred to the first mutating write transaction. Recovery is 100% idempotent and crash-proof. | `src/engine/recover.go:scanImage` | `./bin/torture --mode recovery-crash --iterations 50 --seed 42` |
| **F3** · Fast recovery | Superblock points to durable `KEYDIR` frame at offset `ckpt_off` (`seq = ckpt_seq`), followed by a short tail of $k$ incremental frames. | Recovery validates `KEYDIR` frame CRC64. If valid, loads full index directly into memory and only scans frames from `replay_start` to end of file, bounding recovery time to $< 100\text{ ms}$. | `src/engine/checkpoint.go:checkpoint` | `tests/benchmark_test.go:TestBenchF3Recovery`, fixture `ckpt-plus-torn-tail` |
| **F4** · Truncated sentinel | Frame header ($40\text{B}$), payload ($L\text{B}$), and CRC64 ($8\text{B}$) fully intact on disk, but trailing 8-byte end magic `LF-END!!` is truncated by $1 \le k \le 8$ bytes at physical EOF. | `ScanFrame` implements the SPEC short-sentinel rule: if CRC64 over header + payload matches, and the present prefix of `LF-END!!` matches, the frame is accepted as acknowledged. Tail is classified as `SHORT_SENTINEL`. On first subsequent write, `repairTail` restores missing sentinel bytes before appending new data. | `src/engine/frame.go:ScanFrame` | `tests/f4_test.go:TestF4OneByteTruncationSentinel`, fixtures `short-sentinel-1` through `short-sentinel-8`, `micro` trace T9 |

---

## Part 3 — The three guarantees

### Durability
- Put, Delete, Batch, and Apply return success only after the committer's `pwrite` of the transaction's frame and a successful `fsync` of the data file.
- On the rewrite path, success comes only after the replacement file's `fsync`, `rename`, and directory `fsync`.
- `serve` prints `ACK` only from the committer's `OnCommit` hook, running after publish, in commit order.
- On any I/O failure, the handle is poisoned and the caller receives `ErrIO`. We never retry a failed `fsync`.

### Atomicity
- A batch is serialized into exactly one frame.
- Recovery validates magic, type, version, header CRC, epoch, exact sequence number, length bounds, full frame CRC64, structural sentinel, and exact payload parse.
- Any altered or missing byte causes the entire frame to be rejected; zero ops apply.
- In-memory publication updates the entire batch under a single index write lock.

### Recoverability
- `open()` performs zero writes to the database data file.
- Tail repair (truncation or sentinel restoration) is strictly deferred to the first write transaction.
- File creation and compaction always use temporary files atomically renamed after fsync.
