# BREAKING — How we would break LEAPFROG-R

> **Owner:** Tanushree-RD (final at T4), with crash-point verification by chethan-9817. **Maximum two pages.** Every crash point is checked against the code by the judges: a row described wrongly costs more than a row left out.

---

## 1. Crash points table

| CP | Where (file:line) | On disk if killed here | What the next open() does | Sweep command |
| --- | --- | --- | --- | --- |
| CP-W01 | admission.go (TBD) | Unchanged | Same state as before T. T absent (not ACKed) | `./bin/torture --mode sweep --cp CP-W01 --seed 1` |
| CP-W02 | commit.go (TBD) | Either old tail or repaired; valid prefix unchanged | Same logical_end and index. TORN tail ignored; short sentinel accepted/restored | `./bin/torture --mode sweep --cp CP-W02 --seed 1` |
| CP-W03 | commit.go (TBD) | Valid prefix plus partial frame past logical_end | Partial frame fails length/CRC, scan stops before it, T absent | `./bin/torture --mode sweep --cp CP-W03 --seed 1` |
| CP-W04 | commit.go (TBD) | Full frame present in file (page cache survives process kill) | Frame validates; T is present though not ACKed (legal under rule 2) | `./bin/torture --mode sweep --cp CP-W04 --seed 1` |
| CP-W05 | commit.go (TBD) | Frame is durable on disk | T present | `./bin/torture --mode sweep --cp CP-W05 --seed 1` |
| CP-W06 | commit.go (TBD) | Frame is durable on disk | T present; client never heard back (legal) | `./bin/torture --mode sweep --cp CP-W06 --seed 1` |
| CP-W07 | serve.go (TBD) | Frame is durable on disk | T present. Required (rule 1) | `./bin/torture --mode sweep --cp CP-W07 --seed 1` |
| CP-R01 | recover.go (TBD) | Unchanged (recovery writes nothing) | Repeats the same selection | `./bin/torture --mode recovery-crash --seed 1` |
| CP-R02 | recover.go (TBD) | Unchanged | Same checkpoint validation, same index | `./bin/torture --mode recovery-crash --seed 1` |
| CP-R03 | recover.go (TBD) | Unchanged | Same scan, same logical_end. Idempotent | `./bin/torture --mode recovery-crash --seed 1` |
| CP-C01 | compact.go (TBD) | `path` untouched; empty temp | Old image; temp deleted | `./bin/torture --mode sweep --cp CP-C01 --seed 1` |
| CP-C02 | compact.go (TBD) | `path` untouched; partial temp | Old image; temp deleted | `./bin/torture --mode sweep --cp CP-C02 --seed 1` |
| CP-C03 | compact.go (TBD) | `path` = old; complete temp | Old image; temp deleted. Never promoted | `./bin/torture --mode sweep --cp CP-C03 --seed 1` |
| CP-C04 | compact.go (TBD) | Same as CP-C03 | Old image | `./bin/torture --mode sweep --cp CP-C04 --seed 1` |
| CP-C05 | compact.go (TBD) | `path` = complete new image | New image. On REWRITE, T is present but not ACKed (legal) | `./bin/torture --mode sweep --cp CP-C05 --seed 1` |
| CP-C06 | compact.go (TBD) | `path` = complete new image | New image | `./bin/torture --mode sweep --cp CP-C06 --seed 1` |
| CP-S01 | checkpoint.go / superblock.go (TBD) | The slot may be torn | Torn slot fails sb_crc. Other slot is used; same index | `./bin/torture --mode sweep --cp CP-S01 --seed 1` |

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

---

## 4. What we tried that failed

*Logged during live hackathon testing; only verified issues from progress logs and HYDRA failures.*
- TBD

---

## 5. If we had 24 more hours

1. **Concurrent background compaction:** Implement copy-on-write or split-log architecture so writes can continue during compaction.
2. **On-disk B-tree / Radix index:** Spill index pages to disk to eliminate RAM index footprint for multimillion-key databases.
3. **Dynamic group commit:** Adaptive batching on the committer channel during extreme concurrent write load without sacrificing individual fsync latency.
