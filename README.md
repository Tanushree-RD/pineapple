# pineapple 🍍

**LEAPFROG-R** is a crash-safe, single-file key-value database. **HYDRA** is the torture tool that kills it with `kill -9`
and checks every answer. Go, standard library only, no dependencies.

Everything is typed at one prompt, `./bin/pineapple`. Replace `12345` with any seed.

## 1. Setup

```bash
git clone https://github.com/chethan-9817/pineapple.git && cd pineapple
make                  # builds everything (needs only Go 1.22+ and make)
./bin/pineapple       # the prompt: prints the whole command list; /help prints it again, /exit leaves
```

Open <http://localhost:8099> in a browser to watch the database live while you type.

Optional, in the shell: `make check` (build + vet + every test, about a minute) and `make preflight` (a short pass over every judge-facing check with a fresh seed, about 40 seconds).

## 2. The 15-minute demo

Typed at the `pineapple ❯` prompt:

```text
pineapple ❯ --iterations 200 --seed 12345                 # 1-4 min: the judge's seed; one ✔ line per kill, then RESULT PASS 200/200
pineapple ❯ --mode compaction --seed 777                  # 4-6 min: COMPACT at a random moment; the file must shrink
pineapple ❯ --mode recovery-crash --seed 888              #          kill `kvdb open` in the middle of recovery
pineapple ❯ put demo.db city Mumbai                       # 6-8 min: by hand
pineapple ❯ batch demo.db ops.txt                         #          (a file of put/del lines: one atomic batch)
pineapple ❯ attack demo.db --rounds 10                    #          kill -9 a server rewriting your data, 10 times; it comes back identical
pineapple ❯ check demo.db                                 #          OK
pineapple ❯ dump demo.db                                  #          every acknowledged key is there
pineapple ❯ walkthrough                                   # 8-12 min: crash-here questions, shown step by step on real files
pineapple ❯ --mode sweep --cp CP-W05 --seed 1             #          the database kills itself at one exact crash point
pineapple ❯ judge                                         # the whole path above in one go
```

## 3. Database commands

Each one runs the real `bin/kvdb`; after the first one names a file you may leave `demo.db` out.

```text
pineapple ❯ open demo.db                  # create the database, run recovery, print key count and recovery time
pineapple ❯ put demo.db city Mumbai       # store a value (overwrites if the key exists)
pineapple ❯ get demo.db city              # read it back (prints NOTFOUND if missing)
pineapple ❯ del demo.db city              # delete it (deleting a missing key is not an error)
pineapple ❯ batch demo.db ops.txt         # apply a file of put/del lines as ONE atomic batch (ops.txt is in the repo)
pineapple ❯ dump demo.db                  # print every key and value, sorted by key
pineapple ❯ check demo.db                 # verify file integrity; prints OK or a reason
pineapple ❯ serve demo.db                 # long-running server; prints ACK after each durable op (this is what gets killed)
pineapple ❯ compact                       # rewrite only the live keys; the file shrinks (the PDF requires shrinking after deletes, minus 5 points if not)
pineapple ❯ inspect demo.db -v            # decode the file; -v labels every byte of the first frame (judges spot-check the format against a hex dump)
```


`ops.txt` (in the repo) has one operation per line:

```text
put a 1
put b 2
del b
```

With the live page open, `put`, `del`, `batch` and `compact` show up as they happen.

## 4. Torture commands

The leading `--` is optional for `iterations`, `seed`, `mode`. "Page" is what <http://localhost:8099> shows while it runs.

```text
pineapple ❯ --iterations 200 --seed 12345                    # random kill -9, 200 times; every ACKed write survives. Page: keys and file size climb, restart from durable state after each kill
pineapple ❯ --iterations 500 --seed 12345                    # the same, 500 kills (the default count)
pineapple ❯ --iterations 20 --seed 999 --mode large-values   # 1 MiB values never come back torn. Page: file grows in ~1 MiB steps; tail may flash TORN after a kill
pineapple ❯ --iterations 10 --seed 999 --mode large-batches  # a 10,000-op batch is all or nothing. Page: keys jump by whole batches (tens of thousands of keys)
pineapple ❯ --iterations 100 --seed 12345 --mode compaction  # file rewrite killed midway: old file or new file, never a mix. Page: size grows, then shrinks
pineapple ❯ --iterations 30 --seed 12345 --mode recovery-crash  # kill the database while it is recovering; file stays byte-identical. Page: 200,000 keys build up, then size and bytes stay fixed
pineapple ❯ --iterations 50 --seed 12345 --mode disk-full    # size cap: ERR FULL leaves no trace, delete frees room. Page: file size stops at the cap (4 MiB)
pineapple ❯ --mode sweep --seed 1                            # database kills itself at each of its named crash points. Page: many small databases; tail shows TORN / SHORT_SENTINEL, then recovers
pineapple ❯ --mode micro --seed 1                            # 11 tiny tricky traces, run exactly. Page: a new small database per trace
pineapple ❯ --mode fixtures                                  # 31 hand-damaged files all open correctly. Page: each damaged file in turn
pineapple ❯ --coverage                                       # proof every persistence step was hit; reads what sweep, micro and fixtures saved, so run those three first (no database, page stays empty)
pineapple ❯ --replay failures/12345-87                       # re-run the check on a saved failure; the directory exists only after a run failed (no database to watch)
pineapple ❯ attack demo.db --rounds 10                       # kill -9 a server rewriting YOUR data; it must come back identical. Page: your own database, every round (needs demo.db to exist)
pineapple ❯ judge                                            # the whole 15-minute path in one go, one line per step
pineapple ❯ scenarios                                        # list all 32 scenarios (A1 ... F4) with the kind of check each uses
pineapple ❯ scenario B5 A4 F4                                # run just those scenarios
pineapple ❯ all --quick                                      # all 32 scenarios at a tenth of the iterations (about 1 minute)
pineapple ❯ all                                              # all 32 at full counts, then a scoreboard
```

`failures/` and the `coverage-*.json` files are written in the directory you started `./bin/pineapple` from, so `--coverage` and `--replay failures/...` work after the earlier steps at this prompt.

After the first `put demo.db ...` the path may be left out (`get city`), or choose one with `/use demo.db`.

## 5. The 32 scenarios

```text
pineapple ❯ scenarios            # list the 32
pineapple ❯ scenario B5          # one scenario (use any ID from the table)
pineapple ❯ scenario A1 A2 A3    # several
pineapple ❯ all --quick          # all 32 at a tenth of the iterations (about 1 minute)
pineapple ❯ all                  # all 32 at full counts, with a scoreboard
```

The plain commands behind each scenario, typed at the same prompt (the `go test` lines run in the shell, from the repo root):

| ID | What it proves | Commands |
| --- | --- | --- |
| A1 | nothing new on disk before any write | `--mode sweep --cp CP-W01 --seed 1` |
| A2 | torn write (partial frame) | `--mode sweep --cp CP-W03 --seed 1` · `--mode fixtures` |
| A3 | frame complete, fsync not returned | `--mode sweep --cp CP-W04 --seed 1` |
| A4 | fsync returned, no ACK yet: write survives | `--mode sweep --cp CP-W05 --seed 1` · `--mode sweep --cp CP-W06 --seed 1` |
| A5 | an ACKed write survives kill -9 | `--iterations 100 --seed 12345` · section 4 |
| A6 | a delete in any state | `--iterations 100 --seed 12345` · `--mode micro --seed 1` |
| A7 | torn or whole 1 MiB value | `--iterations 20 --seed 999 --mode large-values` |
| B1 | 3 of 10 ops written: whole batch ignored | `--mode sweep --cp CP-W03 --seed 1` · `--mode micro --seed 1` |
| B2 | all ops written, CRC64 missing | `--mode fixtures` |
| B3 | CRC64 written, not fsynced: all or none | `--mode sweep --cp CP-W04 --seed 1` |
| B4 | an ACKed batch is fully recovered | `--iterations 100 --seed 12345` |
| B5 | PUT X then DEL X: last op wins | `go test ./tests -run 'TestBatchB5Ordering\|TestEmptyValueVsDelete' -count=1` |
| B6 | half of a 10,000-op frame | `--iterations 10 --seed 999 --mode large-batches` |
| C1 | recovery writes no bytes | `--iterations 10 --seed 12345 --mode recovery-crash` |
| C2 | tail repair deferred to the first write | `--mode sweep --cp CP-W02 --seed 1` |
| C3 | old file + partial temp: temp deleted, never used | `--iterations 50 --seed 12345 --mode compaction` · `--mode sweep --cp CP-C02 --seed 1` · `--mode sweep --cp CP-C03 --seed 1` |
| C4 | old or new image, never a mix | `--mode sweep --cp CP-C04 --seed 1` · `--mode sweep --cp CP-C05 --seed 1` |
| C5 | new image, no temp file left | `--mode sweep --cp CP-C05 --seed 1` · `--mode sweep --cp CP-C06 --seed 1` |
| C6 | a clean close keeps everything | `--mode fixtures` |
| D1 | two lanes serialized whole; ACK order = commit order | `--iterations 100 --seed 12345` · `go test ./tests -run TestACKOrderMatchesCommitOrder -count=1` |
| D2 | two frames on one key: exactly one writer wins | `--iterations 100 --seed 12345` |
| D3 | a big batch and single writes together | `--iterations 10 --seed 999 --mode large-batches` |
| D4 | no reader sees half a batch | `go test ./tests -run TestD4ConcurrentReaderNoPartialBatch -count=1` · `go test ./src/engine -run TestReadersNeverSeeHalfABatchD4 -count=1` |
| E1 | ERR FULL before any write | `--iterations 50 --seed 12345 --mode disk-full` |
| E2 | a whole batch admitted or rejected | `--iterations 50 --seed 12345 --mode disk-full` |
| E3 | the old image stays intact | `scenario E3` |
| E4 | compaction precheck and write errors (ERR IO, exit 3) | `--mode fixtures` · `scenario E4` |
| E5 | delete at the cap reclaims space | `--iterations 50 --seed 12345 --mode disk-full` |
| F1 | everything at once, 500 random kills | `--iterations 500 --seed 12345` |
| F2 | read-only recovery is idempotent | `--iterations 10 --seed 12345 --mode recovery-crash` |
| F3 | 1,000,000 keys recover well under 2 s | `KVDB_BENCH=1 go test ./src/engine -run BenchF3 -v -count=1` |
| F4 | ONE byte cut from the last frame: the ACKed write survives | `--mode fixtures` · `go test ./tests -run TestF4OneByteTruncationSentinel -count=1` |

## 6. Demos

**See how the database works, on real files: `walkthrough`.**

```text
pineapple ❯ walkthrough          # about 40 seconds, paced so you can watch the live page; `walkthrough fast` prints it at once
```

**See HYDRA catch a bug, then the fix: `failure`, then `success`.**

```text
pineapple ❯ failure     # a throwaway copy of the engine gets ONE planted bug (ACK sent before the write); the normal random-kill test FAILS on it, naming the lost key
pineapple ❯ success     # the same test (same seed, 20 iterations) on the real engine: PASS, and why no ACK can get ahead of the data
```

---

## 7. On-disk format and recovery (what the judge spot-checks)

### On-disk format

All integers are little-endian. Checks: CRC32C (Castagnoli) and CRC64 (ECMA).

```text
offset   0       4096      8192                                                      logical_end
         ├────────┼─────────┼─────────┬─────────┬─────┬──────────────────┬─────┬────────┤
         │ SB  A  │  SB  B  │ frame 1 │ frame 2 │ ... │ KEYDIR checkpoint│ ... │ torn?  │
         └────────┴─────────┴─────────┴─────────┴─────┴──────────────────┴─────┴────────┘
          4 KiB     4 KiB     seq 1     seq 2           (an ordinary frame)         ignored, repaired
                                                                                    by the first write
```

| Superblock offset | Size | Field |
| --- | --- | --- |
| 0 | 8 | magic `LFROG-SB` |
| 8 / 12 | 4 / 4 | version `1` / reserved |
| 16 | 8 | generation (higher valid generation wins) |
| 24 | 8 | epoch (random id of this image; every frame carries it) |
| 32 / 40 / 48 | 8 each | `ckpt_off`, `ckpt_seq`, `replay_start` (0 = no checkpoint) |
| 56 | 8 | `max_bytes` (persisted cap, 0 = none) |
| 64 | 4 | `sb_crc` CRC32C of bytes 0 to 63 |

A superblock update always writes the *inactive* slot with generation + 1, then `fsync`s: a torn slot fails its CRC and the
other slot is used.

| Frame offset | Size | Field |
| --- | --- | --- |
| 0 | 4 | magic `LFR1` |
| 4 / 5 / 6 | 1 / 1 / 2 | type (1 BATCH, 2 KEYDIR) / version `1` / reserved |
| 8 / 16 / 24 | 8 each | epoch, seq, `payload_len` L |
| 32 / 36 | 4 / 4 | `header_crc` CRC32C of bytes 0 to 31 (checked *before* L is trusted) / reserved |
| 40 | L | payload |
| 40+L | 8 | `frame_crc` CRC64 of bytes 0 to 39+L |
| 48+L | 8 | end magic `LF-END!!` |

BATCH payload: `op_count u32`, then per op `kind u8` (1 PUT, 2 DELETE), `key_len u16`, `value_len u32`, key, value.
Ops apply in order and the last op on a key wins (`PUT X; DEL X` leaves X absent).

Real file from `put db a b` (verify with `inspect db -v` at the prompt, or `xxd db` in the shell): the 69-byte frame at `0x2000` is
`4c 46 52 31` · type `01` · version `01` · reserved · epoch · seq `1` · `payload_len` `0d` · `header_crc` · `00000000` ·
`01 00 00 00` (1 op) · `01` (PUT) · `01 00` (key_len) · `01 00 00 00` (value_len) · `61` (`a`) · `62` (`b`) ·
`frame_crc` · `4c 46 2d 45 4e 44 21 21`.

KEYDIR is a normal checksummed frame containing the live key index; recovery validates it and replays later frames. A final frame with a short end sentinel is accepted only when its checksum is complete and the remaining sentinel bytes match. The next write repairs that sentinel before appending.

### Recovery algorithm in plain English

`open` never writes to the data file; it only reads and decides where the valid log ends.

1. Take the lock (`flock` on `path.lock`, a separate file).
2. Delete leftover temporary files from a half-done compaction (`.create.tmp`, `.compact.tmp`).
3. Read both superblocks and use the valid one with the higher generation (a torn superblock fails its CRC, so the other is used).
4. If that superblock points to a valid checkpoint (a KEYDIR frame), load the index from it and scan from `replay_start`; otherwise scan every frame from offset 8192.
5. Accept frames one at a time and strictly: header CRC, then payload CRC64, then the end magic. Stop at the first invalid byte; everything after it is ignored.
6. Classify the tail: `COMPLETE` (nothing after the last frame), `TORN` (partial frame), or `SHORT_SENTINEL` (only the end magic is cut; the frame is still accepted).
7. Only if `--max-bytes` was passed and differs, write the new cap into the inactive superblock slot.

The torn tail is repaired by the next write, never by `open`, so a crash during recovery leaves the file unchanged and the next `open` computes the same state. F4: if one byte is cut from the end, only the end sentinel is short, header, payload and CRC64 are intact, so the acknowledged last write survives.

### B5: a batch that puts and deletes the same key

Operations in a batch apply in the order given and the last operation on a key wins: `PUT X; DEL X` leaves X absent, and `DEL Y; PUT Y 2` leaves Y = 2. The whole batch is one frame, so after a crash it is all there or not there at all.

---

More: [ARCHITECTURE.md](ARCHITECTURE.md) · [BREAKING.md](BREAKING.md) · [torture/README.md](torture/README.md)
