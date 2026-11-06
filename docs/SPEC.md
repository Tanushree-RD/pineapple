# LEAPFROG-R + HYDRA — Technical Specification

**Status:** frozen at M0. Changes only by the integrator (chethan-9817), on main, at a checkpoint.
**Basis:** "LEAPFROG-R + HYDRA Final Research-Reconciled Build Plan" (supersedes V2): persisted max_bytes, streamed batches with no extra byte cap, commit-order ACKs, checkpoint watermark (replay_start), persistence rules P1 to P4, W/R/C/S state machines, and the HYDRA micro, fixture and behavior-coverage layers. Plus the earlier review fixes:
- byte-exact superblock and KEYDIR
- crash table with on-disk and next-open answers
- directory sync on the rewrite path
- fsync and directory-sync decisions on macOS
- checkpoint bound to its own frame
- streamed compaction
- concurrent serve lanes
- serve batch cap

Section numbers are referenced from MASTER_PROMPT.md. **Do not renumber.**

---

## 1. The invariant (what every line of code must preserve)

After a crash at any point, and after `open()` has finished recovery:

1. Every operation that returned success before the crash is present.
2. Every operation that had not returned success is either fully present or fully absent.
3. A batch is one operation for rules 1 and 2. It is never partially present.
4. No key ever holds a value that was never written to it: no garbage, no torn values, no values from another key.
5. `open()` never fails on a file produced by our database, however it was killed.

**Crash model:** process death (SIGKILL) at any instruction, including during write, fsync, rename, recovery and compaction. Once fsync returns, the bytes are on disk. OS crash and power loss are not required.

## 2. Limits

| Item | Limit |
| --- | --- |
| Key | 1 to 1024 bytes |
| Value | 0 to 1,048,576 bytes |
| Ops per batch | 1 to 10,000 |
| Keys per database | ≥ 1,000,000 |
| MAX_PAYLOAD (frame payload_len accepted by recovery) | 4 + 10,000 × (7 + 1024 + 1,048,576) = 10,496,070,004 |
| Total batch bytes | **No extra cap.** Batches are streamed (4.7), so the only limits are the op count and the per-key/value sizes |
| Packed compaction frame payload | ≤ 4 MiB (4,194,304) |

## 3. Architecture

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

- **One code path:** a put is a one-op batch, and so is a delete. Group commit is **not** used: one transaction, one frame, one fsync.
- **ACK order = commit order.** The committer runs each transaction's OnCommit hook (serve prints and flushes the ACK) **before** it starts the next transaction. So at most one transaction can ever be durable-but-unacknowledged when the process dies, which HYDRA checks (14.5).

## 4. On-disk format (byte-exact)

All integers are **little-endian**. Checksums:
- **CRC32C** = `crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))`
- **CRC64** = `crc64.Checksum(b, crc64.MakeTable(crc64.ECMA))`

### 4.1 File layout

| Range | Content |
| --- | --- |
| 0 to 4095 | Superblock slot A |
| 4096 to 8191 | Superblock slot B |
| 8192 to logical_end | Frames: seq 1, 2, 3, ... |
| logical_end to EOF | Torn or uncommitted tail; ignored; removed by the first write |

### 4.2 Superblock slot (68 bytes used, rest of the 4 KiB slot is zero)

| Offset | Size | Field | Value / meaning |
| --- | --- | --- | --- |
| 0 | 8 | magic | ASCII `LFROG-SB` |
| 8 | 4 | version | 1 |
| 12 | 4 | reserved | 0 |
| 16 | 8 | generation | Higher valid generation wins. Overflow rejected |
| 24 | 8 | epoch | Random 64-bit id of this database image; every frame must carry it |
| 32 | 8 | ckpt_off | Absolute offset of the checkpoint KEYDIR frame, or 0 = none |
| 40 | 8 | ckpt_seq | seq of that KEYDIR frame (0 if none). The KEYDIR represents every transaction with seq < ckpt_seq |
| 48 | 8 | replay_start | Offset where recovery resumes after the KEYDIR (= ckpt_off + 56 + L of that frame; 0 if none) |
| 56 | 8 | max_bytes | Persisted size cap (0 = no cap). Later put/del/batch/serve processes enforce it |
| 64 | 4 | sb_crc | CRC32C of bytes 0 to 63 |

- A slot is **valid** when magic, version and sb_crc all match.
- A new image always has slot A valid with generation 1, and slot B all zeros (invalid).
- Every superblock update writes the **inactive** slot with generation + 1, copying all unchanged fields, then fsyncs. That covers checkpoints (9) and cap changes (7.4).

**Why persist max_bytes:** the mandatory CLI only takes `--max-bytes` on `open` (and our `serve`). Later `put`, `del`, `batch` and `serve` processes must enforce the same cap without being told it again.

### 4.3 Frame (header 40 bytes + payload + trailer 16 bytes = 56 + L)

| Offset | Size | Field | Value / meaning |
| --- | --- | --- | --- |
| 0 | 4 | magic | ASCII `LFR1` (4C 46 52 31) |
| 4 | 1 | type | 1 = BATCH, 2 = KEYDIR |
| 5 | 1 | version | 1 |
| 6 | 2 | reserved | 0 |
| 8 | 8 | epoch | Must equal the superblock epoch |
| 16 | 8 | seq | Must equal the expected next sequence number |
| 24 | 8 | payload_len (L) | ≤ MAX_PAYLOAD |
| 32 | 4 | header_crc | CRC32C of bytes 0 to 31. Checked **before** L is trusted |
| 36 | 4 | reserved | 0 |
| 40 | L | payload | BATCH or KEYDIR payload |
| 40+L | 8 | frame_crc | CRC64 of bytes 0 to (39 + L). **Transaction validity evidence** |
| 48+L | 8 | end_magic | ASCII `LF-END!!` (4C 46 2D 45 4E 44 21 21). Redundant structural sentinel |

### 4.4 BATCH payload (type 1)

```
op_count   u32            1 .. 10,000
repeat op_count times:
  kind       u8           1 = PUT, 2 = DELETE
  key_len    u16          1 .. 1024
  value_len  u32          0 .. 1,048,576; must be 0 for DELETE
  key        key_len bytes
  value      value_len bytes
```

- Size: 4 + Σ(7 + key_len + value_len).
- PUT with an empty value is **not** a DELETE.
- Ops apply in order, and the last op on a key wins: `PUT X; DEL X` leaves X absent, and `DEL X; PUT X` leaves it present.
- The payload must parse **exactly**: op_count ops, every field in bounds, zero bytes left over. Otherwise the whole frame is invalid, and none of its ops is applied.

### 4.5 KEYDIR payload (type 2: checkpoints and compaction output)

```
checkpoint_seq  u64       must equal this KEYDIR frame's own seq (binding check)
entry_count     u64
repeat entry_count times (keys strictly ascending, bytes.Compare):
  key_len    u16          1 .. 1024
  key        key_len bytes
  value_off  u64          ABSOLUTE file offset of the value bytes
  value_len  u32          0 .. 1,048,576
```

- Size: 16 + Σ(14 + key_len).
- Each entry must satisfy 8192 ≤ value_off and value_off + value_len ≤ (offset of this KEYDIR frame). Keys must be strictly ascending, which also rules out duplicates. Otherwise the KEYDIR is invalid.
- A KEYDIR seen during a normal scan is validated and **skipped**: it never changes the index during a scan.

### 4.6 Index (RAM only)

`map[string]entry`, where `entry = {off uint64 (absolute), len uint32}`. For a BATCH op at payload position p in a frame starting at F, the value offset is F + 40 + p + 7 + key_len.

### 4.7 Streaming rule for large batches

A batch may have 10,000 ops of 1 MiB each, so **no code path builds one giant encoded frame in memory**. A transaction is still one logical frame, but sizing, encoding, writing and CRC calculation are streamed in chunks of at most 4 MiB.

| Path | How it streams |
| --- | --- |
| Library `Batch(ops)` | Iterate the ops to validate and size them, then stream-encode them with an incremental CRC64 (the ops are already in the caller's memory; no second copy) |
| Library `ApplyStream(count, payloadBytes, next, tag)` | The caller announces the op count and exact payload bytes. The committer pre-admits on that size, then pulls ops one at a time with `next()` and writes them as it goes |
| CLI `kvdb batch <file>` | Pass 1 validates, counts and sizes the file. Pass 2: payload ≤ 64 MiB is loaded and goes through `Batch`; above that it streams through `ApplyStream` |
| serve b64 `BATCH` | The header announces the op count and payload bytes (MASTER_PROMPT 8.2). ≤ 64 MiB is buffered and goes through `Apply`; above that the op lines stream straight into `ApplyStream` |

**64 MiB is a buffering threshold, not a size cap.** No valid batch is ever refused for its size. Only the buffered path (`Apply`) can take the disk-full REWRITE branch; a streamed batch can only APPEND, so near the cap it returns FULL (documented in 17).

If the stream turns out malformed or short (wrong count, wrong byte total, bad line), the committer:
- never fsyncs that frame
- truncates the file back to logical_end and fsyncs
- returns ErrInvalid

Nothing is published. Under a kill, the partial frame is just a TORN tail. While a batch streams, the committer is busy, so other writers wait; that is correct, and simple.

## 5. Durability and ordering

### 5.1 Sacred ordering

```
pwrite(frame) → fsync(file) → publish to index → OnCommit hook (serve prints + flushes ACK) → return success → next transaction
```

Nothing is visible to readers and nothing is acknowledged before fsync returns.

### 5.2 Ordering argument (paste into BREAKING.md section 2)

- **Durability.** The ACK follows `fsync` of the data file, so an acknowledged frame is on disk.
- **No directory fsync for appends.** Appends and `ftruncate` change only the contents and size of an existing file, and `fsync` on the file covers its size.
- **Directory fsync after renames.** We create files with `path.create.tmp → fsync(tmp) → rename → fsync(dir)`, and replace them with `path.compact.tmp → fsync(tmp) → rename → fsync(dir)`. The directory fsync makes the rename itself durable, and it happens **before** any ACK that depends on the new file. (The rewrite path ACKs only after the directory fsync.)
- **Superblock flips.** We append the KEYDIR, `fsync`, write the inactive slot, then `fsync`. So a valid slot never points at unsynced bytes.

### 5.3 fsync primitive (decide at hour 3, record in BREAKING.md)

- **Default:** `(*os.File).Sync()`. That is `fsync(2)` on Linux and `fcntl(F_FULLFSYNC)` on macOS.
- **If the demo laptop is a Mac and `Sync` runs below about 200 per second:** use `syscall.Fsync(int(f.Fd()))` (plain `fsync`). That satisfies the spec's process-kill model; record it as a known power-loss weakness.
- **Directory sync:** `d, _ := os.Open(dir); d.Sync()`. If that returns an error on macOS, use `syscall.Fsync(int(d.Fd()))`. Test on the demo laptop and record the result.

### 5.4 The three guarantees (paste into ARCHITECTURE.md)

**Durability.**
- Put, Delete and Batch return success only after the committer's `pwrite` of the transaction's frame and a successful `fsync` of the data file.
- On the rewrite path, success comes only after the replacement file's `fsync`, `rename` and the directory `fsync`.
- serve prints `ACK` only from the committer's OnCommit hook, which runs after publish, in commit order.
- If any write, fsync or rename fails, the handle is poisoned and the caller gets ErrIO, never success, and we never retry an fsync.

**Atomicity.**
- A batch is exactly one frame. Recovery applies a frame's ops only after all of these succeed:
  - magic, type and version
  - header_crc
  - epoch
  - exact sequence number
  - length bounds
  - the CRC64 over the whole header and payload
  - the sentinel rule
  - an exact payload parse

  A frame with any missing or altered byte fails, so none of its ops apply.
- Live readers see a batch all at once, because publication applies every op under one index write-lock acquisition, and readers hold the read lock across lookup and pread.
- Residual risk: a 2^-64 chance that a torn frame matches its CRC64.

**Recoverability.**
- `open()` writes no bytes in the data file. It reads the superblocks, validates the checkpoint, scans frames and rebuilds RAM. A kill during `open()` leaves the file unchanged, and the next `open()` repeats the same deterministic computation.
- The tail is repaired by the first write, not by recovery.
- Compaction builds a complete, fsynced replacement and switches with one `rename`, so the path always names a complete image.
- New files are created through `rename` too, so a file at the real path always has a valid superblock.

### 5.5 Persistence rules P1 to P4 (every code review checks these)

| Rule | Statement | Where it bites |
| --- | --- | --- |
| **P1** | Never publish an index entry or file handle that points at bytes until those bytes are durable | Commit W3 before W4; replaceFile swap after fsync + rename |
| **P2** | Never retire the old authoritative image until the replacement is complete and durable | Compaction and rewrite: fsync(tmp) before rename; the old file is closed only after the swap |
| **P3** | Never expose a mixed generation: file handle, index offsets, epoch and logical metadata are published under **one** write lock | Commit publish; replaceFile swap |
| **P4** | Never append behind an invalid tail: truncate a TORN tail, or restore a SHORT_SENTINEL, and fsync first | Tail repair (CP-W02) |

**Persistence-order graphs (draw these in ARCHITECTURE.md):**

```
write:       frame bytes ──fsync──► index publish ──► OnCommit/ACK
checkpoint:  KEYDIR frame ──fsync──► inactive superblock (ckpt_off, ckpt_seq, replay_start) ──fsync──► slot is current
cap change:  (recovery done) ──► inactive superblock (max_bytes) ──fsync──► slot is current
compaction:  tmp image (superblock + frames + KEYDIR) ──fsync(tmp)──► rename ──fsync(dir)──► locked swap ──► close old
```

## 6. Files and creation

### 6.1 Names

| Path | Purpose |
| --- | --- |
| `path` | The one authoritative data file |
| `path.lock` | Process lock (`flock(LOCK_EX\|LOCK_NB)`); never deleted, never renamed |
| `path.create.tmp` | Used once, while creating a new database |
| `path.compact.tmp` | Used during compaction and rewrite |

**Creation, when `path` does not exist:**
1. Write an 8192-byte image to `path.create.tmp`: slot A (generation 1, random epoch, no checkpoint) and a zeroed slot B.
2. `fsync` the temp file.
3. `rename` it to `path`.
4. `fsync` the directory.

### 6.2 Temp files are never authoritative

After acquiring `path.lock`, `open()` deletes `path.create.tmp` and `path.compact.tmp` if present, **before** touching anything else. Deleting a temp file is not a write to the data file, so recovery is still "writes no bytes".

A temp file is never chosen because it exists or has a high generation. Only `path` is the database.

## 7. Recovery (`open`)

### 7.1 Algorithm

1. Acquire `path.lock`. If it is busy, return ErrLocked. (R0)
2. If `path` does not exist, create it (6.1).
3. Delete stale temp files (6.2).
4. Read both slots. Pick the valid slot with the highest generation (CP-R01). If no slot is valid, return **ErrCorrupt** and never reinitialize. This cannot happen under our crash model, because files are born through rename.
5. **Checkpoint** (CP-R02). If `ckpt_off ≠ 0`, validate the frame at `ckpt_off`. All of these must hold:
   - type KEYDIR
   - the slot's epoch
   - frame seq == ckpt_seq
   - payload checkpoint_seq == ckpt_seq
   - all frame checks (7.2)
   - every entry valid (4.5)
   - replay_start == ckpt_off + 56 + L

   If it is valid, load the index from it, set expected seq = ckpt_seq + 1, and scan from `replay_start`. If it is invalid, fall back to a **full scan** from offset 8192 with expected seq = 1.
6. **Scan** frames strictly (7.2, CP-R03). Apply each valid BATCH. Skip each valid KEYDIR.
7. Stop at the first invalid frame. Set logical_end, next_seq and the tail state (7.3).
8. Rebuild accounting (live record bytes, key bytes, key count). **No byte of `path` has been written.** (R-ready)
9. Only if the caller explicitly set a cap that differs from the persisted one: run the cap update (7.4). Then ready.

### 7.2 Frame checks, in order (also used by `Check`)

Let R = the bytes remaining from the frame start to EOF.

1. R ≥ 40; magic `LFR1`; type ∈ {1, 2}; version = 1; reserved fields = 0.
2. header_crc matches.
3. epoch == the chosen slot's epoch.
4. seq == expected (rejecting overflow).
5. L ≤ MAX_PAYLOAD, and 48 + L ≤ R, computed without overflow (header + payload + CRC64 present). **Never allocate before this check passes.**
6. frame_crc matches.
7. Sentinel: if R ≥ 56 + L, all 8 bytes must equal `LF-END!!`. Otherwise this is the **short-sentinel case**: the frame ends at physical EOF, and the 0 to 7 bytes present must equal the matching prefix of `LF-END!!`. Then the frame is accepted, and the tail state becomes SHORT_SENTINEL.
8. The payload parses exactly (4.4 / 4.5).

### 7.3 Tail states and the exact F4 rule

| Tail state | Condition | logical_end | First write after open does |
| --- | --- | --- | --- |
| COMPLETE | The last valid frame ends exactly at EOF | End of that frame | Nothing extra |
| TORN | Bytes exist after the last valid frame but don't form a valid frame | Start of the invalid bytes | `ftruncate(logical_end)`, then `fsync` (CP-W02) |
| SHORT_SENTINEL | The last frame passed checks 1 to 6 and 8, with 0 to 7 sentinel bytes missing at EOF | Frame start + 56 + L (past EOF) | pwrite the missing sentinel bytes at their offsets, then `fsync` (CP-W02) |

**F4 (judge truncates 1 byte):** if the last byte cut belongs to `end_magic`, the header, payload and CRC64 are all intact. The frame is accepted, so an **ACKed last write survives**, as the spec requires ("at most the last *unacknowledged* record is lost").

**Stale-byte answer for judges:**
- Recovery accepts only frames with the active epoch, the exact next sequence number and a valid whole-frame CRC64.
- The first write removes or repairs any invalid tail before appending, so stale bytes can never sit behind a new frame or join a later valid sequence.
- Compaction always writes into a freshly truncated temp file with a new epoch.

### 7.4 Persisted cap update (`open --max-bytes N`)

This runs after recovery has finished, as a separate step. It is not part of recovery.
1. If N equals the persisted max_bytes, do nothing.
2. Otherwise, write the **inactive** slot with generation + 1, every other field copied, and max_bytes = N (CP-S01). Then fsync.

**Kill during the update:** the torn slot fails sb_crc, so the old slot (old cap) is used. If it completes, the new cap is used. Data is identical either way.

A cap lower than the current file size is allowed. Puts then return FULL until a delete-through-rewrite or compaction shrinks the image.

## 8. Disk-full: append-or-rewrite admission

### 8.1 Semantics

`max_bytes` is **persisted in the superblock** (4.2, 7.4) and caps the **physical size of each database image**: the data file, and separately the temp replacement while one is being built. During a rewrite, old file + temp may together exceed `max_bytes` for a moment. This is documented in the README; ask a judge if unsure. `max_bytes = 0` means no cap.

### 8.2 Accounting (updated on every publish)

- `liveRec` = Σ over live keys of (7 + key_len + value_len)
- `liveKeyBytes` = Σ key_len
- `liveCount` = number of live keys

**Upper bound on the size S of a compacted image:**

```
maxEntry = 7 + 1024 + 1,048,576 = 1,049,607
nFrames  = ceil(liveRec / (4,194,304 − maxEntry)) + 1
S_ub     = 8192 + nFrames × 60 + liveRec + (72 + 14 × liveCount + liveKeyBytes)
```

Here 60 = 56 frame overhead + 4 for op_count, and 72 = 56 frame overhead + 16 for checkpoint_seq and entry_count. The compaction builder packs entries greedily into frames with payload ≤ 4 MiB, so the real size is always ≤ S_ub.

### 8.3 Decision for a transaction T with frame size F

```
E = logical_end                               (after the pending tail repair)
if max_bytes == 0 or E + F <= max_bytes:            → APPEND
S = S_ub computed for the state AFTER applying T
H = clamp(max_bytes / 10, 64 KiB, max_bytes / 2)    (near-cap headroom guard)
if T contains any PUT:   S + H <= max_bytes  → REWRITE  else → ErrFull
if T is delete-only:     S     <= max_bytes  → REWRITE  else → ErrFull
```

- **APPEND:** the normal write path (section 5).
- **REWRITE:** build a compacted image of the post-T state in `path.compact.tmp`, then `replaceFile()` (section 10). Return success only after the directory fsync and the swap.
- **ErrFull:** decided before any byte is written. T is definitely not applied (E1, E2).
- **Why E5 works:** at the cap, a delete needs no tombstone space, because the rewrite simply omits the key. The image shrinks, and later puts append again.
- **Why the guard:** without H, every small put near the cap would trigger a full rewrite.
- **Checkpoints obey the cap too:** a KEYDIR is appended only if E + its size ≤ max_bytes. Otherwise the checkpoint is skipped.

## 9. KEYDIR checkpoint and watermark (recovery speed, F3)

- **Trigger:** after a publish, if bytes appended since `replay_start` (or since image start) ≥ `CheckpointBytes`.
- **Threshold from measurement, not guesswork:**
  1. At E3, measure full-scan replay throughput R (MiB/s) on the demo laptop.
  2. Set CheckpointBytes = R × 0.5 s, the tail budget.

  For example, at 250 MiB/s that gives a tail of about 125 MiB. The default is 64 MiB until measured.
- **Steps** (in the committer):
  1. Append a KEYDIR frame (seq = K; payload checkpoint_seq = K) of the current index through the normal frame path.
  2. `fsync`.
  3. Write the **inactive** slot with generation + 1, the same epoch and max_bytes, ckpt_off = the KEYDIR offset, ckpt_seq = K, replay_start = the KEYDIR end (CP-S01).
  4. `fsync`.
  5. That slot is now current.
- **Crash between steps 2 and 4:** the old slot is still current, and recovery scans over the new KEYDIR as a valid, skipped frame. Same index.
- **Torn slot:** sb_crc fails, so the other slot is used.
- **Binding:** the slot's ckpt_seq must equal the KEYDIR frame's seq and its payload checkpoint_seq, and replay_start must equal that frame's end. A slot pointing at bytes that are now something else fails these checks, which triggers a full scan. Correct, just slower.
- **Cap:** the KEYDIR is appended only if logical_end + its size ≤ max_bytes. Otherwise the checkpoint is skipped.
- **F3 target:** 1,000,000 keys open in ≤ 1.4 s on the demo laptop.

## 10. Compaction and `replaceFile()` (one implementation, two callers)

**Callers:** `Compact()`, and the REWRITE branch of admission.

**Build** (committer goroutine; writes paused):
1. Precheck: S_ub ≤ max_bytes, else ErrFull (E4).
2. Open `path.compact.tmp` with `O_RDWR|O_CREATE|O_TRUNC`. (CP-C01)
3. Pick a new random epoch. Write slot A (generation 1, the new epoch, the **persisted max_bytes copied over**, checkpoint fields filled in at step 5) and slot B = zeros.
4. Write the live set in ascending key order as packed BATCH frames (seq 1, 2, ...; payload ≤ 4 MiB each). **Stream values from the old file with pread into a reused buffer; never hold all values in RAM.** For a REWRITE, T's new values come from memory, and keys T deletes are omitted. (CP-C02)
5. Append a KEYDIR frame for the new image, and set slot A's ckpt_off, ckpt_seq and replay_start to point at it.

**`replaceFile(tmp)`:**
1. `fsync(tmp)` (CP-C03)
2. CP-C04 (before rename)
3. `rename(tmp, path)` (CP-C05 after)
4. `fsync(dir)`
5. Under the index **write** lock: swap the file handle, index, logical_end, next_seq, epoch, current slot and accounting in one critical section (CP-C06).
6. Unlock, then close the old file handle.

**Invariant:** a reader sees OLD file + OLD index, or NEW file + NEW index, never a mix.

**Any error in build or replaceFile:** poison the handle and return ErrIO (11). Before the rename, the old file is untouched. After the rename, the new image is complete.

**Result:** the file is physically just 8192 + packed live data + KEYDIR. One million puts plus one million deletes plus Compact leaves 8192 + 72 bytes.

## 11. I/O errors: the poison state machine

```
OPEN --(any write / fsync / truncate / rename / dir-sync error)--> POISONED --Close--> CLOSED --Open--> recovery --> OPEN
```

| Operation | OPEN | POISONED |
| --- | --- | --- |
| Get / ForEach | works | works (the index holds only fsynced, published data) |
| Put / Delete / Batch / Compact / checkpoint | work | ErrIO |
| Close | stops committer, fsync, unlocks | unlocks only |
| serve | — | prints `ERR <id> IO`, exits 3 |

The failing transaction's outcome is **unknown** (no ACK), which the invariant allows. **Never retry** a failed write or fsync on the same handle.

## 12. Crash-point catalogue (the crash-here cheat sheet; BREAKING.md section 1)

Each name appears as a `// CP-xxx` comment in the code, in `engine.Hit("CP-xxx")` calls, in HYDRA sweep mode and in BREAKING.md. **Never rename.**

| CP | Where (file) | On disk if killed here | What the next `open()` does |
| --- | --- | --- | --- |
| CP-W01 | Admission decided, before any disk change (admission.go) | Unchanged | Same state as before T. T absent (not ACKed) |
| CP-W02 | Tail repair: ftruncate or sentinel restore, then fsync (commit.go) | Either the old tail or the repaired one; the valid prefix is unchanged | Same logical_end and same index. TORN tail ignored; short sentinel accepted or restored |
| CP-W03 | Mid pwrite of the frame (commit.go) | Valid prefix plus a partial frame past logical_end | The partial frame fails length/CRC, the scan stops before it, and T is absent. (If only sentinel bytes are missing, T is present via 7.3; legal, since it was never ACKed) |
| CP-W04 | Frame fully written, before fsync returns (commit.go) | The full frame is in the file (page cache survives a process kill) | The frame validates, so T is **present** though never ACKed. Legal under rule 2 |
| CP-W05 | fsync returned, before publish (commit.go) | The frame is durable | T present |
| CP-W06 | Published to the index, before success returns (commit.go) | The frame is durable | T present; the client never heard back. Legal |
| CP-W07 | ACK printed and flushed (serve.go) | The frame is durable | T present. **Required** (rule 1) |
| CP-R01 | Superblock selection (recover.go) | Unchanged (recovery writes nothing) | Repeats the same selection |
| CP-R02 | Checkpoint load (recover.go) | Unchanged | Same checkpoint validation, same index |
| CP-R03 | Tail scan (recover.go) | Unchanged | Same scan, same logical_end. Idempotent (C1, C2, F2) |
| CP-C01 | path.compact.tmp created (compact.go) | `path` untouched; empty temp | Old image; the temp is deleted |
| CP-C02 | Mid temp write (compact.go) | `path` untouched; partial temp | Old image; the temp is deleted (C3) |
| CP-C03 | Temp fully written and fsynced (compact.go) | `path` = old; complete temp | Old image; the temp is deleted. Never promoted (C3) |
| CP-C04 | Just before rename (compact.go) | Same as CP-C03 | Old image |
| CP-C05 | rename returned, before dir fsync and swap (compact.go) | `path` = complete new image | New image. On a REWRITE, T is present but not ACKed; legal (C4, C5) |
| CP-C06 | Swapping handle and index in memory (compact.go) | `path` = complete new image | New image (C5) |
| CP-S01 | Writing the inactive superblock slot, for a checkpoint (checkpoint.go) or a cap change (superblock.go, 7.4) | The slot may be torn | The torn slot fails sb_crc. The other slot is used: the older checkpoint (or a full scan, scanning over the new KEYDIR) and the older cap. Same index |

**Model answer (CP-W03):**
> "We're mid-pwrite. Say the header and part of the payload are on disk. Recovery checks magic, then header_crc, which passes. Then it checks that 48 + L fits in the file, which fails because the CRC64 isn't there. The scan stops at this frame's start; logical_end is set there and the tail state is TORN. None of the batch's ops are applied. The first write after open truncates to logical_end and fsyncs before appending."

### 12.1 State machines (crash-here answers in one word; draw these in ARCHITECTURE.md)

| Write state | Meaning | Crash point | After a kill |
| --- | --- | --- | --- |
| W0 | Validated and admitted; no bytes written (tail repair may run) | CP-W01, CP-W02 | Old state |
| W1 | Frame being streamed to disk | CP-W03 | Partial frame ignored |
| W2 | Whole frame present; fsync not yet returned | CP-W04 | May be present or absent; not ACKed |
| W3 | fsync returned; not yet published | CP-W05 | **Must** be present |
| W4 | Published to the live view; ACK not yet emitted | CP-W06 | Must be present; caller may not know |
| W5 | ACK flushed by the OnCommit hook | CP-W07 | Must be present |

| Recovery state | Meaning | Crash point | After a kill |
| --- | --- | --- | --- |
| R0 | Lock held, temps cleaned | — | File unchanged |
| R1 | Superblock chosen | CP-R01 | File unchanged |
| R2 | Checkpoint loaded or fallback chosen | CP-R02 | File unchanged |
| R3 | Tail scanned, index built | CP-R03 | File unchanged; next open is identical |

| Compaction state | Meaning | Crash point | Authoritative image |
| --- | --- | --- | --- |
| C0 | Old image only | — | Old |
| C1 | Temp partial or unsynced | CP-C01, CP-C02 | Old |
| C2 | Temp complete and fsynced | CP-C03, CP-C04 | Old until the rename |
| C3 | Rename done; live process still on the old view | CP-C05 | New pathname |
| C4 | Handle + index + epoch swapped together | CP-C06 | New |

| Superblock state | Meaning | Crash point | After a kill |
| --- | --- | --- | --- |
| S0 | KEYDIR fsynced (checkpoint) or recovery finished (cap change) | — | Old slot current |
| S1 | Inactive slot being written | CP-S01 | Torn slot rejected, so the old slot is used |
| S2 | New slot fsynced | — | New slot current |

## 13. `Check` and `ForEach`

**`Check(path)`** is read-only and takes `path.lock`.
1. Report both slots: valid or invalid, generation, epoch.
2. Pick the slot exactly as recovery does.
3. Run a **full scan** from 8192 with every check in 7.2, reporting the first failure precisely, for example `CORRUPT frame seq=1834 at offset 9123456: frame_crc mismatch`.
4. If a checkpoint exists, verify the binding (ckpt_seq == frame seq == payload checkpoint_seq, replay_start == frame end), and verify that the KEYDIR equals the index produced by the full scan of frames with seq < ckpt_seq. Report the persisted max_bytes and the file size as INFO.
5. Verify that every index entry lies within [8192, logical_end).
6. A TORN or SHORT_SENTINEL tail is **not** corruption. Report it as `INFO tail=TORN bytes=123 (unacknowledged)` and still print `OK`.

**`ForEach`:** under RLock, collect the keys, sort them with `bytes.Compare`, then pread and call fn for each one in order.

## 14. HYDRA: oracle and values

### 14.1 Truth

HYDRA's own append-only `oracle.log` is the only truth. The database's dump is the **observation**, never the truth.

### 14.2 Records

| Record | Written when |
| --- | --- |
| `SENT id lane kind keys… t` | Before the line is written to serve's stdin |
| `ACK id t` | A complete `ACK <id>\n` line is read. Partial lines are ignored |
| `REJECT id reason t` | `ERR <id> FULL` / `INVALID` |
| `UNKNOWN id t` | `ERR <id> IO` |

Times come from a monotonic clock.

### 14.3 Self-describing values

| Value size | Layout |
| --- | --- |
| ≥ 32 bytes | `HYDR`(4) · op_id u64 · key_hash u64 (FNV-1a 64 of the key) · writer u32 · total_len u32 · hdr_crc u32 (CRC32C of the previous 28 bytes) · then PRNG bytes seeded by op_id |
| 12 to 31 bytes | op_id u64 · low 32 bits of key_hash · PRNG bytes |
| empty | Used only by an explicit "empty value" op class; verified against that key's candidate ops |

Any observed value therefore names exactly one op, proves it belongs to this key, and proves it is not torn or mixed.

### 14.4 Op states and the per-key rule

| State | Meaning | After restart |
| --- | --- | --- |
| ACKED | ACK received | Must be reflected (unless a later op on that key legally overrides it) |
| IN_FLIGHT | Sent, no reply (or UNKNOWN) | May appear or not; never partially |
| REJECTED | FULL / INVALID | Must never appear |
| DEAD | Every op from an earlier iteration other than the one observed | Must never appear again ("lost stays lost") |

**Per key k**, with baseline = the state verified after the previous iteration, and candidates = baseline ∪ {ACKED or IN_FLIGHT ops on k in this iteration}:
1. The observed state must equal the result of some candidate o: a value carrying o's op_id, or absence from a delete. Baseline absence counts as an initial delete.
2. There must be no ACKED op o2 on k with `sent(o2) > acked(o)`. A later acknowledged op would have to win. The baseline counts as acked at iteration start.
3. After verifying, baseline = observed, and every other op of this iteration on k becomes DEAD.

**Batches:**
- Default-mode batches write fresh keys plus a witness key: either all keys carry the batch's op_id, or none of them do.
- Mixed batches (D3) are checked per key, plus the witness.

### 14.5 Commit-order ACKs: the strongest cheap check

serve prints each ACK from the committer's OnCommit hook **before** the next transaction starts (section 3), and HYDRA reads stdout until EOF after every kill (flushed ACKs stay in the pipe). Together that gives HYDRA three extra checks.

**Oracle fields per op:**

| Field | Purpose |
| --- | --- |
| op_id, lane | Identity |
| invoke_seq, invoke_time | When HYDRA wrote the request line: the real-time constraint |
| ack_seq, ack_time | The order ACKs were read = the durable commit order |
| keys, witness | Batch atomicity |
| state | ACKED / IN_FLIGHT / REJECTED / DEAD |

**Rules:**
1. **Commit order.** For two ACKED ops on the same key, the one with the higher ack_seq must win (unless an unACKed op legally committed after both; see rule 2).
2. **At most one surprise.** After recovery, **at most one** transaction that was not ACKed may be present. That is the one the committer was working on at the kill, which is necessarily later in commit order than every ACK. Two or more unACKed transactions present is a FAIL.
3. **Real time.** If op A was ACKed before op B was invoked, A must precede B in commit order (checked through ack_seq).

This is why no general linearizability solver is needed: the single committer plus commit-order ACKs make the legal histories a short prefix check.

## 15. HYDRA: commands, modes and reports

### 15.1 Commands

```
./bin/torture --iterations 500 --seed 12345
./bin/torture --iterations 100 --seed 999 --mode large-values
./bin/torture --iterations 100 --seed 5   --mode large-batches
./bin/torture --iterations 100 --seed 7   --mode compaction
./bin/torture --iterations 100 --seed 42  --mode recovery-crash
./bin/torture --iterations 100 --seed 3   --mode disk-full
./bin/torture --mode sweep    --seed 1 [--cp CP-W03]      # deterministic crash points
./bin/torture --mode micro    --seed 1                    # tiny adversarial traces
./bin/torture --mode fixtures                             # recovery matrix
./bin/torture --coverage                                  # behavior-coverage table (after sweep + micro)
./bin/torture --replay failures/<seed>-<iter>
```

### 15.2 Modes

| Mode | Workload | Targets |
| --- | --- | --- |
| default | 2 to 4 lanes; mixed put/del/batch; keyspace about 20k; a hot shared key set; seeded kill after 10 to 250 ms | A1 to A6, B1 to B5, D1 to D3, F1 |
| large-values | 256 KiB to 1 MiB values | A7 |
| large-batches | 10,000-op streamed batches plus concurrent single puts | B6, D3 |
| recovery-crash | Build a 200k-key DB (`--big`: 1M); SIGKILL `kvdb open` at a seeded fraction of its measured time, 1 to 3 times; final open < 2 s | C1, C2, F2, F3 |
| compaction | Workload plus one COMPACT at a seeded moment; every 10th iteration, let it finish and assert the file shrank | C3 to C6 |
| disk-full | `kvdb open --max-bytes 4194304` once (persisted); put until FULL, delete, puts must ACK again; file size ≤ cap after every iteration | E1 to E5 |
| sweep | For each CP in section 12 and hit n = 1..N: `KVDB_CRASH=<cp>:<n>` makes serve kill itself there; then verify | Every CP, deterministically |
| micro | The tiny adversarial traces in 15.7, each killed at every CP it touches (via sweep hooks) and at random | Ordering bugs |
| fixtures | The recovery matrix in 15.8: crafted files with an exact expected open/check/dump result | Recovery logic bugs |

**Required by the rules:** default, large-values, large-batches, compaction, recovery-crash, disk-full, and `--seed`. **Ours, after those pass:** sweep, micro, fixtures and coverage. The mandatory default mode is never changed by them; it stays purely random-timer.

### 15.3 Seeds

- iteration seed = splitmix64(seed + iter)
- lane seed = splitmix64(iterSeed + lane)
- kill delay = 10 + (rng mod 241) ms

**Seed honesty:** a seed reproduces the workload, the lane streams and the requested kill delay. It does **not** reproduce OS scheduling or the exact instruction a real SIGKILL lands on. Sweep mode is the exactly reproducible path.

### 15.4 Output

```
iter 17/500 seed=12345 mode=default kill_ms=134 acked=212 inflight=6 rejected=0 keys=8123 open_ms=41 check=OK PASS
...
RESULT PASS 500/500 (seed=12345, mode=default, 4m02s)
```

Exit code: 0 on pass, 1 on the first failure.

**On failure:** print the seed, iteration, key (hex), observed op_id, allowed op_ids with their SENT and ACK times, and the iteration's kill delay. Then save `failures/<seed>-<iter>/{database.db, oracle.log, operations.log, metadata.json}` and exit 1.

### 15.5 Timing budget

The judge runs 200 default iterations in about 3 minutes, so keep each iteration **≤ 0.6 s**, including open, check and dump. Measure every mode.

### 15.6 Crash hooks and mutants (engine side, via environment variables)

| Variable | Effect |
| --- | --- |
| `KVDB_CRASH=CP-W03:7` | SIGKILL self on the 7th hit of CP-W03. At CP-W03, first pwrite a seeded random prefix of the frame (a real torn write) |
| `KVDB_CRASH_SEED=n` | Seed for that prefix length |
| `KVDB_PROBE=<file>` | (stretch) mmapped 64-byte file holding the last CP name reached |
| `KVDB_MUTANT=M1..M6` | (stretch, only in a `-tags mutants` build) plants one known bug |

| Mutant | Planted bug | Should be caught by |
| --- | --- | --- |
| M1 | ACK/return before fsync | sweep at CP-W04 + power-cut (if built) |
| M2 | fsync removed | power-cut only (kill -9 cannot see it; document this) |
| M3 | Sequence or epoch check skipped | sweep + resurrection check |
| M4 | Batch published op by op without the lock | D4 concurrent test |
| M5 | rename before fsync(tmp) | sweep at CP-C05 |
| M6 | File/index swap without the write lock | concurrent reader test during compaction |

All hooks are **no-ops when the variables are unset**. The default mode never uses them; it is purely random-timer. A torture tool that only passes because of hooks counts as rigged and is a disqualification.

### 15.7 Micro traces (`--mode micro`)

Ordering bugs show up faster in tiny sequences than in huge workloads. Each trace runs on a fresh database and is killed at every crash point it passes (sweep hooks) plus 20 random kills. Then the oracle checks it.

```
T1  PUT A                                 → kill
T2  PUT A; PUT A                          → kill
T3  PUT A; DEL A                          → kill
T4  BATCH(PUT A, PUT B)                   → kill
T5  BATCH(PUT X, DEL X)                   → kill            (B5 ordering under crash)
T6  PUT A; COMPACT                        → kill
T7  DEL A; COMPACT; PUT A                 → kill
T8  (fill to checkpoint) checkpoint       → kill during superblock update (CP-S01)
T9  F4: truncate 1 byte; PUT B            → kill during sentinel repair (CP-W02)
T10 open --max-bytes N (cap change)       → kill during superblock update (CP-S01)
T11 at cap: DEL A (rewrite path)          → kill at each CP-C point
```

### 15.8 Recovery matrix / fixtures (`--mode fixtures`)

Each fixture is built from a real database made with `kvdb`, then cut or corrupted at exact offsets computed from section 4. Each one has an exact expected result.

| Fixture | Construction | Expected |
| --- | --- | --- |
| clean-empty | New DB | open OK, keys=0, check OK |
| clean-many | 3 frames | All keys, check OK |
| torn-header | Last frame cut inside its 40-byte header | Last frame absent, check OK + INFO TORN |
| torn-payload | Last frame cut inside its payload | Last frame absent |
| missing-crc | Last frame cut right after its payload | Last frame absent |
| short-sentinel | Last frame cut by 1 to 8 bytes inside end_magic | Last frame **present** (F4), INFO SHORT_SENTINEL |
| bad-crc | One payload byte of the last frame flipped | Last frame absent |
| torn-newest-sb | Newest slot's byte 20 flipped | Older slot used; same data |
| ckpt-plus-torn-tail | Checkpoint, then 2 frames, last one torn | Checkpoint + first frame |
| bad-ckpt-fallback | KEYDIR payload byte flipped | Full scan; same data |
| stale-compact-tmp | Valid DB + garbage `path.compact.tmp` | Old DB; temp deleted |
| stale-create-tmp | Valid DB + garbage `path.create.tmp` | Old DB; temp deleted |

### 15.9 Behavior coverage (`--coverage`)

HYDRA tracks which persistence transitions were actually exercised by a kill (sweep, micro and fixtures), and prints a table:

| Behavior | Transitions that must each be hit at least once |
| --- | --- |
| W, normal append | W0→W1 → W2 → W3 → W4 → W5 (CP-W01 to CP-W07) |
| R, recovery | R1 → R2 (checkpoint or fallback) → R3 |
| C, compaction | C1 → C2 → C3 → C4 |
| S, checkpoint/config | S1 for a checkpoint, S1 for a cap change |
| F4, tail repair | Detect a short sentinel → restore → fsync → append |
| E, disk-full rewrite | Admission fails → replacement built → durable → swapped |

Any transition with zero hits is a gap. It must be fixed or explained in BREAKING.md.

### 15.10 State fingerprints (stretch, after everything else passes)

For sweeps, record (generation, logical_end, last seq, tail state, key count, dump digest) per injection, and group injections with identical fingerprints in the report.

## 16. The 32 scenarios (ARCHITECTURE.md part 2; fill "Code" with file:function)

| ID | Disk state at kill | Mechanism | Verified by |
| --- | --- | --- | --- |
| A1 | Nothing new | Admission before any write (CP-W01) | sweep CP-W01 |
| A2 | Partial frame | Length/CRC64 check fails; whole frame ignored | sweep CP-W03 |
| A3 | Complete frame, not fsynced | Valid frame may appear; never ACKed | sweep CP-W04 |
| A4 | Frame fsynced, no return yet | Durable valid frame recovered | sweep CP-W05 |
| A5 | ACKed | Durable valid frame recovered | default |
| A6 | Delete frame in any state | Same as A1 to A5 (delete = one-op batch) | default |
| A7 | Partial 1 MiB frame | Header CRC bounds L; CRC64 covers every byte | large-values, sweep CP-W03 |
| B1 | 3 of 10 ops written | One frame, incomplete, so ignored | sweep CP-W03 |
| B2 | All ops, no CRC64 | Missing CRC64, so none visible | sweep CP-W03 |
| B3 | CRC64 written, not fsynced | Whole frame valid or torn: all or none | sweep CP-W04 |
| B4 | Batch ACKed | All ops recovered | default |
| B5 | PUT X then DEL X | Apply in order; last wins | tests/batch_test.go |
| B6 | Half of a 10k-op frame | Incomplete, so ignored; recovery timed | large-batches |
| C1 | Unchanged | Recovery writes no bytes | recovery-crash |
| C2 | Unchanged | Repair deferred to the first write | recovery-crash, sweep CP-W02 |
| C3 | Old file + partial or complete temp | Old file authoritative; temp deleted | compaction, sweep CP-C02/C03 |
| C4 | Old or new image at path | rename is atomic | compaction, sweep CP-C04/C05 |
| C5 | New image + no temp | New image authoritative | compaction, sweep CP-C05/C06 |
| C6 | Same as any write | close = stop committer + fsync | default |
| D1 | Frames from two lanes | One committer serializes whole transactions | default |
| D2 | Two frames, same key | Each frame atomic; value names exactly one writer | default (hot keys) |
| D3 | Big batch + singles | Independent serialized transactions | large-batches |
| D4 | No crash | One write-locked publication; RLock across pread | tests/concurrent_test.go |
| E1 | Nothing new | ErrFull before any write | disk-full |
| E2 | Nothing new | Whole batch admitted or rejected | disk-full |
| E3 | Old image intact | Same as A5 for prior data | disk-full |
| E4 | Old file + temp | Temp ≤ cap by precheck; ENOSPC means poison, old intact | disk-full + compaction |
| E5 | Image shrinks | Delete-through-rewrite | disk-full |
| F1 | Any | All of the above | default 500 |
| F2 | Unchanged | Read-only recovery is idempotent | recovery-crash |
| F3 | Checkpoint + short tail | KEYDIR bounds the scan | tests/benchmark_test.go |
| F4 | Last byte of end_magic missing | Short-sentinel rule keeps the ACKed frame | tests/f4_test.go |

## 17. Known weaknesses (BREAKING.md section 3; keep honest)

1. Compaction and rewrite are stop-the-world; writes wait.
2. Near the cap, a put may return FULL even though a rewrite would barely fit (the headroom guard). Deletes always reclaim.
3. Old file + temp can briefly exceed max_bytes on the filesystem as a whole.
4. Residual CRC64 collision probability on a torn frame (2^-64).
5. A streamed batch (> 64 MiB) holds the single committer for its whole duration, and can only append: near the cap it returns FULL instead of rewriting.
6. The whole index is in RAM (about 100 MB at 1M small keys).
7. A damaged checkpoint falls back to a full scan, which may exceed 2 s at 1M keys (measure it).
8. Linux and macOS only. If plain fsync was chosen on macOS (5.3), durability holds against process kills but not power loss.

## 18. Demo (15 minutes, judge drives, Wi-Fi off)

| Minute | Judge | Us |
| --- | --- | --- |
| 0 to 1 | `git clone` + `make` | Must just work |
| 1 to 4 | `./bin/torture --iterations 200 --seed X` | Point at the per-iteration lines |
| 4 to 6 | compaction and recovery-crash modes | Same |
| 6 to 8 | `./bin/kvdb serve`, types `begin/put/put/commit`, kills it 5 to 10 times, then check + dump | Text mode is easy to type; check prints OK plus a tail INFO line |
| 8 to 12 | Three crash-here questions | Read the `// CP-xxx` at that line and answer from section 12 |
| 12 to 15 | TTL extension | Section 19 |

## 19. TTL extension (prepared answer)

**Changes:**
- **Format:** new op kind 3 = PUT_TTL with `expires_at` u64 (ms). KEYDIR entries gain `expires_at` (version 2). Version-1 frames still parse.
- **Reads:** a key is NOTFOUND when the effective now ≥ expires_at. "Now" is evaluated once per call, so MGET and dump are consistent.
- **Recovery:** loads expiry and never purges, because the boot clock is untrusted.
- **Compaction:** omits expired keys. It writes only each key's newest version, so nothing older resurfaces.

**Subtle issues (raise unprompted):**
1. **Clock going backwards.** This would resurrect expired keys. Fix: each frame carries its write time, recovery keeps a high-water mark, and effective now = max(wall clock, high-water mark).
2. **Resurrection.** Dropping an expired newest version must never reveal an older value. That's safe here only because compaction rewrites the whole live set.
3. **Capacity.** Expired bytes still count until compaction. Admission's S_ub must exclude expired keys, otherwise E5 breaks.
4. **Batches.** `PUT_TTL X (already expired); PUT X` still follows last-wins exactly. An already-expired PUT_TTL is still logged, never dropped.

**Testing:** HYDRA injects `KVDB_CLOCK` and adds a clock-skew mode.

## 20. Sources

ALICE (OSDI '14) · CrashMonkey/ACE (OSDI '18) · "Can Applications Recover from fsync Failures?" (ATC '20) · PostgreSQL fsync errors wiki · LMDB meta-page design · Bitcask paper · SQLite WAL format · TigerBeetle VOPR. These are research inspiration only; all code is written by the team during the event.
