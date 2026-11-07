# pineapple 🍍

**LEAPFROG-R** is a crash-safe, single-file key-value database. **HYDRA** is the torture tool that kills it with `kill -9`
and checks every answer. Go, standard library only, no dependencies.

Everything below is a command to run. Run it from the repository folder; replace `12345` with any seed.

## 1. Setup

```bash
git clone https://github.com/chethan-9817/pineapple.git && cd pineapple
make                  # builds bin/kvdb, bin/torture, bin/pineapple, bin/leapfrog, bin/hydra (needs only Go 1.22+)
make check            # build + go vet + every test (optional, about a minute)
make preflight        # short pass over every judge-facing check with a fresh seed (about 40 seconds)
```

## 2. The database: 8 commands

```bash
./bin/kvdb                                         # lists the commands
./bin/kvdb open  demo.db --max-bytes 4194304       # create/open, recovery stats, persist a size cap
./bin/kvdb put   demo.db city Mumbai               # OK
./bin/kvdb get   demo.db city                      # Mumbai
./bin/kvdb put   demo.db blob hex:00ff10           # binary-safe input
./bin/kvdb del   demo.db city                      # OK
./bin/kvdb get   demo.db city                      # NOTFOUND
printf 'put a 1\nput b 2\ndel a\n' > ops.txt
./bin/kvdb batch demo.db ops.txt                   # one atomic batch: OK
./bin/kvdb dump  demo.db                           # every key and value, sorted
./bin/kvdb check demo.db                           # OK (+ INFO lines)
printf 'put k 1\nbegin\nput x 2\ndel k\ncommit\nget x\ncompact\n' | ./bin/kvdb serve demo2.db   # ACK after each durable op
```

## 3. The torture tool

```bash
./bin/torture --iterations 200 --seed 12345                        # random kill -9 during writes: RESULT PASS 200/200
./bin/torture --iterations 20  --seed 999   --mode large-values    # 256 KiB to 1 MiB values
./bin/torture --iterations 10  --seed 999   --mode large-batches   # 10,000-op batches
./bin/torture --iterations 100 --seed 12345 --mode compaction      # COMPACT at a random moment; the file must shrink
./bin/torture --iterations 30  --seed 12345 --mode recovery-crash  # kill `kvdb open` in the middle of recovery
./bin/torture --iterations 50  --seed 12345 --mode disk-full       # size cap: full, delete, put again
./bin/torture --mode sweep    --seed 1                             # kill at exactly every named crash point
./bin/torture --mode fixtures                                      # 31 crafted crash images
./bin/torture --mode micro    --seed 1                             # 11 adversarial traces
./bin/torture --coverage                                           # run sweep, micro, fixtures first
./bin/torture --replay failures/12345-87                           # re-check a saved failure (only exists after a failed run)
```

## 4. Kill it by hand

```bash
( for i in $(seq 1 1000000); do echo "put key$i value$i"; done ) | ./bin/kvdb serve kill.db > acks.txt &
sleep 1; kill -9 $(pgrep -f "kvdb serve kill.db")        # run this whole block 5 to 10 times (same kill.db)
./bin/kvdb check kill.db                                 # OK, plus an INFO tail= line
echo "ACKs: $(grep -c -E '^ACK [0-9]+$' acks.txt)  keys on disk: $(./bin/kvdb dump kill.db | wc -l)"
```

## 5. "What if it crashes here?"

```bash
KVDB_CRASH=CP-W05:1 ./bin/kvdb put crash.db k v          # the process kills itself at that exact line
./bin/kvdb check crash.db
./bin/kvdb get crash.db k
```

Every `// CP-xxx` comment in `src/engine` has a row in [BREAKING.md](BREAKING.md).

## 6. The 32 scenarios

```bash
./bin/hydra scenarios            # list the 32
./bin/hydra scenario B5          # one scenario (use any ID from the table)
./bin/hydra scenario A1 A2 A3    # several
./bin/hydra all --quick          # all 32 at a tenth of the iterations (about 1 minute)
./bin/hydra all                  # all 32 at full counts, with a scoreboard
./bin/hydra judge                # the whole 15-minute path in one go
```

The plain commands for each scenario (`T` = `./bin/torture`; `go test` runs from the repo root):

| ID | What it proves | Commands |
| --- | --- | --- |
| A1 | nothing new on disk before any write | `T --mode sweep --cp CP-W01 --seed 1` |
| A2 | torn write (partial frame) | `T --mode sweep --cp CP-W03 --seed 1` · `T --mode fixtures` |
| A3 | frame complete, fsync not returned | `T --mode sweep --cp CP-W04 --seed 1` |
| A4 | fsync returned, no ACK yet: write survives | `T --mode sweep --cp CP-W05 --seed 1` · `T --mode sweep --cp CP-W06 --seed 1` |
| A5 | an ACKed write survives kill -9 | `T --iterations 100 --seed 12345` · section 4 |
| A6 | a delete in any state | `T --iterations 100 --seed 12345` · `T --mode micro --seed 1` |
| A7 | torn or whole 1 MiB value | `T --iterations 20 --seed 999 --mode large-values` |
| B1 | 3 of 10 ops written: whole batch ignored | `T --mode sweep --cp CP-W03 --seed 1` · `T --mode micro --seed 1` |
| B2 | all ops written, CRC64 missing | `T --mode fixtures` |
| B3 | CRC64 written, not fsynced: all or none | `T --mode sweep --cp CP-W04 --seed 1` |
| B4 | an ACKed batch is fully recovered | `T --iterations 100 --seed 12345` |
| B5 | PUT X then DEL X: last op wins | `go test ./tests -run 'TestBatchB5Ordering\|TestEmptyValueVsDelete' -count=1` |
| B6 | half of a 10,000-op frame | `T --iterations 10 --seed 999 --mode large-batches` |
| C1 | recovery writes no bytes | `T --iterations 10 --seed 12345 --mode recovery-crash` |
| C2 | tail repair deferred to the first write | `T --mode sweep --cp CP-W02 --seed 1` |
| C3 | old file + partial temp: temp deleted, never used | `T --iterations 50 --seed 12345 --mode compaction` · `T --mode sweep --cp CP-C02 --seed 1` · `T --mode sweep --cp CP-C03 --seed 1` |
| C4 | old or new image, never a mix | `T --mode sweep --cp CP-C04 --seed 1` · `T --mode sweep --cp CP-C05 --seed 1` |
| C5 | new image, no temp file left | `T --mode sweep --cp CP-C05 --seed 1` · `T --mode sweep --cp CP-C06 --seed 1` |
| C6 | a clean close keeps everything | `T --mode fixtures` |
| D1 | two lanes serialized whole; ACK order = commit order | `T --iterations 100 --seed 12345` · `go test ./tests -run TestACKOrderMatchesCommitOrder -count=1` |
| D2 | two frames on one key: exactly one writer wins | `T --iterations 100 --seed 12345` |
| D3 | a big batch and single writes together | `T --iterations 10 --seed 999 --mode large-batches` |
| D4 | no reader sees half a batch | `go test ./tests -run TestD4ConcurrentReaderNoPartialBatch -count=1` · `go test ./src/engine -run TestReadersNeverSeeHalfABatchD4 -count=1` |
| E1 | ERR FULL before any write | `T --iterations 50 --seed 12345 --mode disk-full` |
| E2 | a whole batch admitted or rejected | `T --iterations 50 --seed 12345 --mode disk-full` |
| E3 | the old image stays intact | `./bin/hydra scenario E3` |
| E4 | compaction precheck and write errors (ERR IO, exit 3) | `T --mode fixtures` · `./bin/hydra scenario E4` |
| E5 | delete at the cap reclaims space | `T --iterations 50 --seed 12345 --mode disk-full` |
| F1 | everything at once, 500 random kills | `T --iterations 500 --seed 12345` |
| F2 | read-only recovery is idempotent | `T --iterations 10 --seed 12345 --mode recovery-crash` |
| F3 | 1,000,000 keys recover well under 2 s | `KVDB_BENCH=1 go test ./src/engine -run BenchF3 -v -count=1` |
| F4 | ONE byte cut from the last frame: the ACKed write survives | `T --mode fixtures` · `go test ./tests -run TestF4OneByteTruncationSentinel -count=1` |

## 7. The one-prompt demo

```bash
git clone https://github.com/chethan-9817/pineapple.git && cd pineapple
make
./bin/pineapple
```

The 15-minute demo of the problem statement, typed at that prompt:

```text
pineapple ❯ --iterations 200 --seed 12345                 # 1-4 min: the judge's seed; one ✔ line per kill, then RESULT PASS 200/200
pineapple ❯ --mode compaction --seed 777                  # 4-6 min: COMPACT at a random moment; the file must shrink
pineapple ❯ --mode recovery-crash --seed 888              #          kill `kvdb open` in the middle of recovery
pineapple ❯ put demo.db city Mumbai                       # 6-8 min: by hand
pineapple ❯ batch demo.db ops.txt                         #          (a file of put/del lines: one atomic batch)
pineapple ❯ serve kill.db                                 #          then kill -9 it from a second terminal, 5-10 times
pineapple ❯ check kill.db                                 #          OK
pineapple ❯ dump kill.db                                  #          every acknowledged key is there
pineapple ❯ attack demo.db --rounds 10                    # kill -9 a server rewriting YOUR data; it comes back identical
```

**Every database command, typed at the same prompt** (each one runs the real `bin/kvdb`; after the first one names a file you may
leave `demo.db` out, so `get city` works):

```text
pineapple ❯ open demo.db                  # create the database, run recovery, print key count and recovery time
pineapple ❯ put demo.db city Mumbai       # store a value (overwrites if the key exists)
pineapple ❯ get demo.db city              # read it back (prints NOTFOUND if missing)
pineapple ❯ del demo.db city              # delete it (deleting a missing key is not an error)
pineapple ❯ batch demo.db ops.txt         # apply a file of put/del lines as ONE atomic batch (ops.txt must exist, see below)
pineapple ❯ dump demo.db                  # print every key and value, sorted by key
pineapple ❯ check demo.db                 # verify file integrity; prints OK or a reason
pineapple ❯ serve demo.db                 # long-running server; prints ACK after each durable op (this is what gets killed)
pineapple ❯ compact                       # rewrite only the live keys; the file shrinks (the PDF requires shrinking after deletes, minus 5 points if not)
pineapple ❯ inspect demo.db -v            # decode the file; -v labels every byte of the first frame (judges spot-check the format against a hex dump)
```

`ops.txt` is one operation per line, for example:

```text
put a 1
put b 2
del b
```

With the live page open, `put`, `del`, `batch` and `compact` show up as they happen (keys appear, the write log grows, the `+ ~ -`
change log fills in, and `compact` makes the file size drop).

**Every torture command, typed at the `pineapple ❯` prompt** (the leading `--` is optional for `iterations`, `seed`, `mode`). The
right-hand comment is what it proves; "page" is what <http://localhost:8099> shows while it runs (the page follows the database the
run is killing, then returns to yours).

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

`failures/` and the `coverage-*.json` files are written in the directory you started `./bin/pineapple` from, the same place the raw
`./bin/torture` commands use, so `--coverage` and `--replay failures/...` work whether you ran the earlier steps at this prompt
or with `./bin/torture`.

After the first `put demo.db ...` the path may be left out (`get city`), or choose one with `/use demo.db`.

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

More: [ARCHITECTURE.md](ARCHITECTURE.md) · [BREAKING.md](BREAKING.md) · [torture/README.md](torture/README.md)
