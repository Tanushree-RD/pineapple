# pineapple 🍍 — a database you can `kill -9`, and the tool that tries to prove it wrong

**LEAPFROG-R** is a crash-safe, single-file key-value database. **HYDRA** is the torture tool that kills it
at random and at exact instructions, and checks every answer against an independent record.
Go, standard library only, **zero dependencies** (`go.mod` has no `require` lines).
Built for the *Kill It and Prove It* hackathon by a team of three.

```bash
git clone https://github.com/chethan-9817/pineapple.git && cd pineapple
make                                            # needs only Go 1.22+ and make
./bin/torture --iterations 200 --seed 12345     # kill it 200 times, verify every time
```

---

## At a glance

| Question | Answer | How to check it |
| --- | --- | --- |
| What survives a `kill -9`? | **Every acknowledged write.** An unacknowledged write is either fully there or fully gone. A batch is never half-there. | the demo below, or `./bin/torture` |
| How many unacknowledged writes can appear after a crash? | **At most one**, the transaction that was in flight at the kill. | demo below: keys on disk = ACKs, or ACKs + 1 |
| Does recovery change the file? | **No.** `open` writes zero bytes; HYDRA hashes the file before and after, every iteration. | `./bin/torture` (built-in check) |
| What can fail to open? | Nothing our own crashes can produce. The torn tail is ignored and the first write repairs it. | `./bin/torture --mode fixtures` |
| How is it tested? | 248 Go tests, 9 HYDRA modes plus `--coverage` and `--replay`, 11 adversarial traces, 31 crash images, and a sweep that kills it at every named crash point. | `make check`, then the commands below |
| How fast is recovery? | 1,000,000 keys: **351 ms** with a checkpoint, **743 ms** full scan (Linux, NVMe laptop). Target was 1.4 s. | `KVDB_BENCH=1 go test ./src/engine -run BenchF3 -v` |
| What does it not cover? | Power loss. `kill -9` cannot see a missing `fsync`. See [Honest limits](#honest-limits). | [BREAKING.md](BREAKING.md) |

---

## See it work in 30 seconds

Stream a million writes into the server, `kill -9` it mid-stream, and count. (`bash`; real output from this repo.)

```bash
( for i in $(seq 1 1000000); do echo "put key$i value$i"; done ) | ./bin/kvdb serve kill.db > acks.txt &
sleep 0.4; kill -9 $(pgrep -n -f "kvdb serve kill.db"); wait 2>/dev/null

echo "ACKs received before the kill: $(grep -c -E '^ACK [0-9]+$' acks.txt)"
./bin/kvdb open kill.db
./bin/kvdb check kill.db | head -1
echo "keys on disk after the kill:   $(./bin/kvdb dump kill.db | wc -l)"
```

```text
ACKs received before the kill: 84
keys=85 recovery_ms=0.475 tail=COMPLETE max_bytes=0
OK
keys on disk after the kill:   85
```

Three runs gave 84→85, 76→77 and 69→70. Every acknowledged write is on disk, plus **exactly one more**: the
transaction that was already durable when the process died but whose ACK had not been flushed yet. Never
zero, never two. That one-extra rule is the strongest check HYDRA makes, and the reason is the design below.

---

## Quickstart

```bash
make            # builds bin/kvdb, bin/torture and the front ends (bin/pineapple, bin/leapfrog, bin/hydra)
make check      # build + go vet + all tests
```

> Binaries go to `./bin/` (`./bin/kvdb`, `./bin/torture`) because a `torture/` source directory and a
> `./torture` binary cannot both live at the repo root.

```bash
./bin/kvdb                                  # no arguments (or help, --help): lists all 8 commands
./bin/kvdb put demo.db city Mumbai          # OK
./bin/kvdb get demo.db city                 # Mumbai
./bin/kvdb put demo.db blob hex:00ff10      # binary-safe: hex: prefix on input
./bin/kvdb dump demo.db                     # blob<TAB>hex:00ff10   city<TAB>Mumbai   (ascending byte order)
./bin/kvdb check demo.db                    # OK + INFO lines (superblocks, frames, tail state)
./bin/kvdb open demo.db --max-bytes 4194304 # persist a size cap; later commands enforce it
```

Interactive server, text mode (a batch is `begin … commit`, one atomic frame):

```text
$ printf 'put a 1\nbegin\nput b 2\ndel a\ncommit\nget b\nget a\ncompact\n' | ./bin/kvdb serve fresh.db
READY keys=0 recovery_ms=5.442 max_bytes=0
ACK 1
ACK 2
VAL 3 2
NOTFOUND 4
ACK 5
```

**Before the demo, on the demo laptop:** `make preflight` builds and then runs a short pass over every judge-facing check
(the 8 commands, random kills, compaction, recovery killed mid-way, the 31 crash images and the crash-point sweep) with a
fresh seed each time, and stops at the first failure. About 40 seconds. A pass there is a pass on that machine.

### The 15-minute judge path

| Step | Command | What you should see |
| --- | --- | --- |
| Build | `git clone … && make` | builds offline, needs only Go |
| Random kills | `./bin/torture --iterations 200 --seed X` | one `PASS` line per iteration, then `RESULT PASS 200/200` |
| Compaction, recovery | `./bin/torture --mode compaction --seed X` and `--mode recovery-crash --seed X` | `RESULT PASS`, file shrinks, recovery killed mid-way leaves the file byte-identical |
| By hand | the 30-second demo above, or `kvdb serve` text mode, `kill -9` it 5 to 10 times, then `check` and `dump` | `check` prints `OK` and an `INFO tail=…` line |
| Crash-here questions | open any `// CP-xxx` comment in `src/engine`, read the row in [BREAKING.md](BREAKING.md) | disk state and next-`open()` behaviour for that line |

HYDRA needs no flags for a faithful run: when you do not pass `--workdir`, it creates its working directory on a real
disk (the system temp directory, or your user cache directory if `/tmp` is RAM). `fsync` is free in RAM, so on a RAM disk
the database would acknowledge tens of thousands of writes per iteration and runs would be about ten times slower.
If you pass your own `--workdir` on tmpfs, HYDRA prints a `NOTE`.

---

## Judge copy-paste guide: setup, every command, all 32 scenarios

Everything below can be pasted into a terminal as it is, top to bottom, from the repository folder. Replace `12345` with any seed you like. Needs only Go (no network, no packages).

### 1. Setup

```bash
git clone https://github.com/chethan-9817/pineapple.git && cd pineapple
git checkout preethesh/hydra          # skip if the branch you were given is main
make                                  # builds ./bin/kvdb and ./bin/torture
make check                            # build + go vet + every Go test (optional, about a minute)
```

### 2. The 8 commands

```bash
./bin/kvdb                                         # lists all 8 commands
./bin/kvdb open  demo.db --max-bytes 4194304       # open/create, recovery stats, persist a size cap
./bin/kvdb put   demo.db city Mumbai               # OK
./bin/kvdb get   demo.db city                      # Mumbai
./bin/kvdb put   demo.db blob hex:00ff10           # binary-safe input
./bin/kvdb del   demo.db city                      # OK
./bin/kvdb get   demo.db city                      # NOTFOUND
printf 'put a 1\nput b 2\ndel a\n' > ops.txt
./bin/kvdb batch demo.db ops.txt                   # one atomic batch: OK
./bin/kvdb dump  demo.db                           # every key and value, sorted
./bin/kvdb check demo.db                           # OK (+ INFO lines)
printf 'put k 1\nbegin\nput x 2\ndel k\ncommit\nget x\ncompact\n' | ./bin/kvdb serve demo2.db   # ACK <id> after each durable op
```

### 3. The torture tool (scored part)

```bash
./bin/torture --iterations 200 --seed 12345                      # random kill -9 during writes + oracle check: RESULT PASS 200/200
./bin/torture --iterations 200 --seed 12345                      # same seed again: same kill delays and operation stream
./bin/torture --iterations 20  --seed 999 --mode large-values    # 256 KiB to 1 MiB values
./bin/torture --iterations 10  --seed 999 --mode large-batches   # 10,000-op batches
./bin/torture --iterations 100 --seed 12345 --mode compaction    # COMPACT at a random moment; the file must shrink
./bin/torture --iterations 30  --seed 12345 --mode recovery-crash # kill `kvdb open` in the middle of recovery
./bin/torture --iterations 50  --seed 12345 --mode disk-full     # size cap: full, delete, put again
./bin/torture --mode sweep    --seed 1                           # kill at EXACTLY every named crash point
./bin/torture --mode fixtures                                    # 31 crafted crash images
./bin/torture --mode micro    --seed 1                           # 11 adversarial traces
./bin/torture --coverage                                         # which persistence transitions were hit (run the three above first)
```

### 4. Kill it by hand

```bash
( for i in $(seq 1 1000000); do echo "put key$i value$i"; done ) | ./bin/kvdb serve kill.db > acks.txt &
sleep 1; kill -9 $(pgrep -f "kvdb serve kill.db")             # the kill; run this whole block again 5 to 10 times (same kill.db)
./bin/kvdb check kill.db                                      # OK, plus an INFO tail= line
echo "ACKs: $(grep -c -E '^ACK [0-9]+$' acks.txt)  keys on disk: $(./bin/kvdb dump kill.db | wc -l)"   # keys on disk >= ACKs (at most one extra durable batch)
```

Run the torture commands from the repository folder: `./bin/torture` finds `./bin/kvdb` there.

### 5. "What if it crashes here?"

Open any `// CP-xxx` comment in `src/engine` and read its row in [BREAKING.md](BREAKING.md). Or make it happen: `KVDB_CRASH=CP-W05:1 ./bin/kvdb put crash.db k v` kills the process at that exact line, then `./bin/kvdb check crash.db` and `./bin/kvdb get crash.db k` show the result.

### 6. All 32 scenarios

Fastest: after `make`, run **one command per scenario**, which runs every check that scenario needs and prints PASS/FAIL with the reasons:

```bash
./bin/hydra scenarios            # list the 32
./bin/hydra scenario B5          # one scenario (use any ID from the table)
./bin/hydra scenario A1 A2 A3    # several
./bin/hydra all                  # all 32 with a scoreboard (--quick for a tenth of the iterations)
```

The plain commands for each scenario are below (`T` = `./bin/torture`, `go test` runs from the repo root). The ones marked **hand** are the paste-ready snippets in section 7.

| ID | What it proves | Commands |
| --- | --- | --- |
| A1 | nothing new on disk before any write | `T --mode sweep --cp CP-W01 --seed 1` |
| A2 | torn write (partial frame) | `T --mode sweep --cp CP-W03 --seed 1` · `T --mode fixtures` |
| A3 | frame complete, fsync not returned | `T --mode sweep --cp CP-W04 --seed 1` |
| A4 | fsync returned, no ACK yet: write survives | `T --mode sweep --cp CP-W05 --seed 1` · `T --mode sweep --cp CP-W06 --seed 1` |
| A5 | an ACKed write survives kill -9 | `T --iterations 100 --seed 12345` · section 4 |
| A6 | a delete in any state | `T --iterations 100 --seed 12345` · `T --mode micro --seed 1` |
| A7 | torn or whole 1 MiB value | `T --iterations 20 --seed 999 --mode large-values` · **hand** A7 |
| B1 | 3 of 10 ops written: whole batch ignored | `T --mode sweep --cp CP-W03 --seed 1` · `T --mode micro --seed 1` |
| B2 | all ops written, CRC64 missing | `T --mode fixtures` |
| B3 | CRC64 written, not fsynced: all or none | `T --mode sweep --cp CP-W04 --seed 1` |
| B4 | an ACKed batch is fully recovered | `T --iterations 100 --seed 12345` |
| B5 | PUT X then DEL X: last op wins | **hand** B5 · `go test ./tests -run 'TestBatchB5Ordering\|TestEmptyValueVsDelete' -count=1` |
| B6 | half of a 10,000-op frame | `T --iterations 10 --seed 999 --mode large-batches` |
| C1 | recovery writes no bytes | `T --iterations 10 --seed 12345 --mode recovery-crash` · **hand** C1 |
| C2 | tail repair deferred to the first write | `T --mode sweep --cp CP-W02 --seed 1` · **hand** C1 |
| C3 | old file + partial temp: temp deleted, never used | `T --iterations 50 --seed 12345 --mode compaction` · `T --mode sweep --cp CP-C02 --seed 1` · `T --mode sweep --cp CP-C03 --seed 1` |
| C4 | old or new image, never a mix | `T --mode sweep --cp CP-C04 --seed 1` · `T --mode sweep --cp CP-C05 --seed 1` · **hand** C4 |
| C5 | new image, no temp file left | `T --mode sweep --cp CP-C05 --seed 1` · `T --mode sweep --cp CP-C06 --seed 1` |
| C6 | a clean close keeps everything | `T --mode fixtures` |
| D1 | two lanes serialized whole; ACK order = commit order | `T --iterations 100 --seed 12345` · `go test ./tests -run TestACKOrderMatchesCommitOrder -count=1` |
| D2 | two frames on one key: exactly one writer wins | `T --iterations 100 --seed 12345` |
| D3 | a big batch and single writes together | `T --iterations 10 --seed 999 --mode large-batches` |
| D4 | no reader sees half a batch | `go test ./tests -run TestD4ConcurrentReaderNoPartialBatch -count=1` · `go test ./src/engine -run TestReadersNeverSeeHalfABatchD4 -count=1` |
| E1 | ERR FULL before any write | **hand** E1 · `T --iterations 50 --seed 12345 --mode disk-full` |
| E2 | a whole batch admitted or rejected | **hand** E2 · `T --iterations 50 --seed 12345 --mode disk-full` |
| E3 | the old image stays intact | **hand** E1 (the `check` after ERR FULL) |
| E4 | compaction precheck and write errors (ERR IO, exit 3) | `T --mode fixtures` · `./bin/hydra scenario E4` |
| E5 | delete at the cap reclaims space | **hand** E1 (the delete, then put) · `T --iterations 50 --seed 12345 --mode disk-full` |
| F1 | everything at once, 500 random kills | `T --iterations 500 --seed 12345` |
| F2 | read-only recovery is idempotent | `T --iterations 10 --seed 12345 --mode recovery-crash` · **hand** C1 |
| F3 | 1,000,000 keys recover well under 2 s | `KVDB_BENCH=1 go test ./src/engine -run BenchF3 -v -count=1` |
| F4 | ONE byte cut from the last frame: the ACKed write survives | **hand** F4 · `T --mode fixtures` · `go test ./tests -run TestF4OneByteTruncationSentinel -count=1` |

### 7. By-hand snippets (**hand**)

```bash
# B5: PUT X then DEL X in one batch -> NOTFOUND; DEL Y then PUT Y -> value
printf 'begin\nput x 1\ndel x\ncommit\nget x\nbegin\ndel y\nput y 2\ncommit\nget y\n' | ./bin/kvdb serve b5.db      # ... NOTFOUND ... VAL 2

# C1: recovery writes no bytes (checksums before and after repeated opens are identical)
./bin/kvdb put c1.db a 1; sha256sum c1.db > before.txt; ./bin/kvdb open c1.db; ./bin/kvdb open c1.db; ./bin/kvdb check c1.db; sha256sum -c before.txt   # c1.db: OK

# F4: cut ONE byte from the last frame; the acknowledged write must still be there
./bin/kvdb put f4.db k1 v1; ./bin/kvdb put f4.db k2 v2; cp f4.db f4cut.db; truncate -s -1 f4cut.db
./bin/kvdb check f4cut.db; ./bin/kvdb dump f4cut.db          # INFO tail=SHORT_SENTINEL missing=1 (frame accepted); k1 and k2 both present

# A7: a 1 MiB value
python3 -c "print('put big ' + 'x'*1048576)" > big.txt; ./bin/kvdb batch a7.db big.txt; ./bin/kvdb get a7.db big | wc -c    # OK, then 1048577

# E1/E3/E5: size cap -> ERR FULL, file still valid, delete frees room
./bin/kvdb open cap.db --max-bytes 30000
i=0; while :; do i=$((i+1)); r=$(./bin/kvdb put cap.db k$i "$(head -c 2000 /dev/zero | tr '\0' v)"); [ "$r" != OK ] && { echo "put $i -> $r"; break; }; done
./bin/kvdb check cap.db                                      # OK: the old image is intact
./bin/kvdb del cap.db k1; ./bin/kvdb put cap.db k1 small     # OK, OK: the delete made room

# E2: a whole batch is refused at the cap, nothing of it is written (run after E1)
python3 -c "
for i in range(100): print('put b%d %s' % (i, 'z'*2000))" > bb.txt
./bin/kvdb batch cap.db bb.txt; echo "exit=$?"                # ERR FULL, exit=2
./bin/kvdb check cap.db; ./bin/kvdb get cap.db b0            # OK, NOTFOUND

# C4: compaction: the file shrinks and the data stays
for i in $(seq 1 300); do echo "put k $i"; done | ./bin/kvdb serve cmp.db > /dev/null; ls -l cmp.db      # about 29 KB
echo compact | ./bin/kvdb serve cmp.db > /dev/null; ls -l cmp.db; ./bin/kvdb get cmp.db k               # about 8 KB, value 300
```

The `T` in section 6 is shorthand: type `./bin/torture` (for example `./bin/torture --mode sweep --cp CP-W01 --seed 1`).

---

## The demo at one prompt: `./bin/pineapple`

`make` builds everything (`kvdb`, `torture` and the front ends; standard library only, offline). `./bin/pineapple` is one prompt
for the whole demo: database commands and the torture tool side by side, with nothing to switch. Database commands run the real
`bin/kvdb`; a line that starts with `-` (or a word such as `attack`, `judge`, `scenario`) runs the torture tool. ↑ recalls earlier
lines, Ctrl-C cancels a line, `/exit` leaves. The prompt starts by printing the whole command list, grouped (database, torture,
crash points, attack and the full run), one line per command; `/help` prints it again.

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

It runs the real `bin/kvdb` on scratch databases and reads every number back from the file; nothing is a drawing. The live page
follows the scratch database and shows a blue "STEP n" banner for the step being shown.

1. **The life of one write** (`put city Delhi`, over a file that holds `city = Mumbai`), in seven steps: check it fits, repair a torn
   tail, write the frame, whole frame written, fsync (durable), update the index, print ACK. The engine is killed at the end of each
   step (its own named crash points `CP-W01` to `CP-W07`) and the walkthrough shows what is on disk (size, tail state) and what
   recovery returns: the old value for a kill during steps 1 to 3, the new one after the frame is complete, an ACK only after fsync.
2. **Recovery writes nothing**: a file with a torn tail is opened and its fingerprint is identical before and after.
3. **A batch is all or nothing**: three puts killed mid-write leave none of them; the same batch allowed to finish leaves all three.
4. **kill -9 in a flood of writes**: every acknowledged write is on disk after the restart.
5. **Compaction**: 300 puts and 290 deletes leave a big file; `compact` shrinks it to the 10 live keys.

A kill is process death, so the OS still holds what was written; a power cut during step 4 could lose the frame, and recovery copes
with both. The walkthrough says so too.

**See HYDRA catch a bug, then the fix: `failure`, then `success`.**

```text
pineapple ❯ failure     # a throwaway copy of the engine gets ONE planted bug (ACK sent before the write); the normal random-kill test FAILS on it, naming the lost key
pineapple ❯ success     # the same test (same seed, 20 iterations) on the real engine: PASS, and why no ACK can get ahead of the data
```

The planted bug is 8 added lines in `src/engine/commit.go` of the copy (the repository is never touched): the database calls the ACK
callback first and sleeps 40 ms before writing the frame, like a write-behind cache. Everything looks fine until the process is
killed between the ACK and the write; then an acknowledged key is gone, and HYDRA says so from its own record of every ACK
("ACKed write LOST ... observed: absent"). The real engine commits in the order write, fsync, publish, ACK, from one committer
goroutine, with the ACK as a callback fired after publish, so the same kill cannot lose an acknowledged write. `failure` needs the
Go toolchain and this checkout (it compiles the copy), is timing-based (a demo, not a proof), and leaves its evidence in `failures/`
for `--replay`.

**The gap, highlighted on the live page.** While `failure` runs, the page at <http://localhost:8099> shows the broken database and,
after the kill, a red pulsing banner: "FAILURE: the database said OK, then lost the data", with a block "THE GAP: told OK, but not
there" naming each acknowledged key that is missing (and how many writes were acknowledged). `success` leaves the page on the
real engine's database (thousands of keys) with a green banner: "every acknowledged write survived every kill; gap: none".
Typing any other command clears the banner.

Concurrency (races) needs no demo of its own: it is already part of the system and its tests. A batch is published under one write
lock and readers hold the read lock across lookup and read, so a reader sees a whole batch or none of it (SPEC D4), checked by
`TestD4ConcurrentReaderNoPartialBatch` in `tests/concurrent_test.go` and `TestReadersNeverSeeHalfABatchD4` in `src/engine`
(both run by `make check`).

**The live data page.** `/live` prints an address (<http://localhost:8099>); open it in a browser. It starts empty and shows only
what is actually happening: the database you are typing commands against (every key and value, the file's write log, a `+ ~ -`
change log, size, frames and tail state), and, while a torture run is going, the database that run is killing and recovering,
then it returns to yours. It runs quietly in the background; the prompt never prints a link or opens a browser by itself. It reads
the file itself, read-only, with the same independent decoder as `leapfrog inspect`, so it takes no lock and cannot get in the
way of a write or a kill. (It lists the first 400 keys of a big database and says how many more there are.) `/live stop` ends it.

`attack <db>` is a check of its own, not one of the judged `torture` modes. A live `kvdb serve` rewrites your records with their own
values (also as atomic delete-and-reinsert transactions) plus its own `hydra:` records and is `SIGKILL`ed at a random moment each
round. After every kill the file must hold your records byte-identical and (acknowledged operations + a prefix of the ones in
flight); the `hydra:` records are removed at the end and the database must equal what it was before. It needs the database to
exist, is single-writer, and a kill is process death, not a power cut.

The separate programs still exist: `./bin/leapfrog` (a prompt for database commands, plus `leapfrog demo`, `leapfrog inspect demo.db -v`)
and `./bin/hydra` (a prompt for torture, plus `hydra judge`, `hydra scenario B5 A4 F4`, `hydra all --quick`, `hydra run sweep --cp CP-W05`).
HYDRA prints plain result lines, one per kill and one verdict:

```text
pineapple ❯ --iterations 4 --seed 9
  ✔ iter 1/4  kill 187ms  79 ACKed  192 keys
  ✔ iter 2/4  kill 220ms  101 ACKed  379 keys
  ✔ iter 3/4  kill 210ms  96 ACKed  532 keys
  ✔ iter 4/4  kill 194ms  89 ACKed  714 keys
✔ PASS  RESULT PASS 4/4 (seed=9, mode=default, 1s)
```

Both tools were themselves tested the way the database is: `hydra`'s by-hand checks were run against engines with planted bugs
(a short-sentinel bug broke `F4`, a missing size check broke `E1`) and failed with the exact reason. Working files go
under your user cache directory (a real disk), never `/tmp`.

---

## Why a crash cannot hurt it

*Is this approach unique?* The core is not: one writer thread, append to a log, fsync, then acknowledge is the classic
write-ahead / append-only-log design (Bitcask, the write-ahead logs of SQLite and LevelDB, group commit). What this project adds
is how provably it is held to that rule: the commit order is one committer goroutine with the ACK as a callback after publish, 17
named crash points that HYDRA kills the engine at one by one, recovery that writes no bytes (so a crash during recovery cannot
damage anything), a tail classifier (complete, torn, short sentinel), and an oracle that checks every answer. The claim is
"checked harder", not "invented".

One rule, followed on every write path:

```text
pwrite(frame) ──► fsync(file) ──► publish to the in-memory index ──► ACK to the client ──► next transaction
                  ▲ durability point: nothing is visible or acknowledged before this returns
```

- **One committer.** A single goroutine takes transactions one at a time, so commit order is total. The server
  prints each ACK from the committer *before* the next transaction starts. That makes ACK order equal commit
  order, and it is why at most one unacknowledged transaction can ever be durable.
- **One frame per transaction.** A put, a delete and a 10,000-op batch are each one frame with a CRC32C header
  check, a CRC64 over header and payload, and an end sentinel. A frame with any missing or altered byte is
  rejected whole, so none of its operations applies.
- **Recovery writes nothing.** `open` reads both superblocks, validates the checkpoint, scans frames strictly,
  and stops at the first invalid byte. The torn tail is repaired by the *first write*, never by `open`, so a
  crash during recovery leaves the file untouched and the next recovery computes the same thing.
- **Replacing files is atomic.** Compaction builds `path.compact.tmp`, `fsync`s it, `rename`s it over the
  database, then `fsync`s the directory. The path always names a complete image.
- **Errors poison, never retry.** Any failed write, `fsync` or rename returns `ErrIO` and refuses further writes.

The three guarantees (durability, atomicity, recoverability) with every system call named are in
[ARCHITECTURE.md](ARCHITECTURE.md). The 17 named crash points and what is on disk at each are in
[BREAKING.md](BREAKING.md); every one is a `// CP-xxx` comment in the code and a deterministic HYDRA case.

---

## HYDRA — how the torture tool proves it

HYDRA never believes the database. Its own append-only `oracle.log` (what it sent, what it was ACKed) is the
truth; `kvdb dump` is only an observation.

1. Start `kvdb serve`, run 2 to 4 concurrent lanes of puts, deletes, batches (with a witness key) and
   contended hot keys, each value **self-describing** (op id, key hash, CRC, pseudo-random body).
2. `SIGKILL` after a seeded delay, read stdout to the end, run `open`, `check`, `dump`.
3. **Per key** the survivor must be the ACKed write with the highest ACK order, or the single in-flight
   transaction if it is complete on every key it touches. Anything else fails: a lost ACKed write, a half
   batch, two unacknowledged transactions, a rejected (`ERR FULL`) write that appeared, an old value that came back,
   or a value that is torn or belongs to another key.
4. Also checked after every kill: the file's CRC64 is unchanged by `open`/`check`/`dump`, `ERR INVALID` never
   happens, and a persisted size cap is enforced without being repeated.

A seed reproduces the workload and the requested kill delay (not OS scheduling): the same seed twice gives identical kill delays and identical operation streams, while ACK counts differ a little with timing. `sweep` is exactly reproducible, and `--replay` re-runs the check on a saved failure deterministically.

**Where the kills land** is printed with `--verbose` (a `kill landing …` line before the result), measured from the recovered file and the oracle, never from a hook: in the default mode every kill has operations in flight (about 24) and most land between `fsync` and the ACK; torn tails from kills inside a write appear with `large-values` and `large-batches` (2 of 30 and 3 of 10 kills in our runs) and at exactly chosen points in `sweep`. The random-kill path never sets a crash hook (`KVDB_CRASH` appears only in `sweep` and `micro`).

| Mode | What it does |
| --- | --- |
| `default` | random-timer kills, the judge's mode. Never uses crash hooks. |
| `large-values` | every value 256 KiB to 1 MiB |
| `large-batches` | 10,000-op batches, buffered and streamed (> 64 MiB), plus concurrent writers |
| `compaction` | a `COMPACT` at a seeded moment; every 10th iteration asserts the file physically shrank |
| `recovery-crash` | 200,000 keys (`--big`: 1,000,000); kills `kvdb open` mid-recovery 1 to 3 times; file must stay byte-identical |
| `disk-full` | cap set once with `open --max-bytes`; full, then delete, then a put must ACK again |
| `sweep [--cp CP-W03] [--hits N]` | kills at **exactly** each crash point `CP-W01…CP-S01`, with a seeded torn prefix at write points, and groups the resulting disk states |
| `micro` | 11 tiny traces (`PUT A; DEL A`, `BATCH(PUT X, DEL X)`, F4, cap change, rewrite at the cap…) killed at every crash point they pass plus 20 random instants, then written to again |
| `fixtures` | 31 crafted crash images: torn header/payload/CRC, short sentinel (1 to 8 bytes), bad CRC, stale frames, torn superblock, checkpoints, stale temp files, clean close, refused compaction, failed write (`ERR IO`, exit 3) |
| `--coverage` | table of every persistence transition with hit counts; zero hits is a gap and exits 1 |
| `--replay failures/<seed>-<iter>` | re-runs the check on a saved failure, deterministically |

```text
iter 200/200 seed=12345 mode=default kill_ms=150 acked=65 inflight=24 rejected=0 keys=8123 open_ms=40 tail=COMPLETE check=OK PASS
sweep CP-W05  n=3 [write]    reached     op 70574902607875 PRESENT tail=COMPLETE  check=OK PASS
sweep CP-C05  n=1 [rewrite]  reached     new image, put present tail=COMPLETE  check=OK PASS
fixture short-sentinel-3                   PASS
RESULT PASS 200/200 (seed=12345, mode=default, 57s)
```

Full HYDRA documentation: [torture/README.md](torture/README.md).

### Does the tool actually have teeth?

A torture tool that never fails proves nothing, so HYDRA was run against deliberately broken databases and
engines. Each of these made it **fail**, with a precise message:

| Planted bug | Caught by | Message (abridged) |
| --- | --- | --- |
| database keeps everything in RAM | `default` | `ACKed write LOST` (10,419 ACKed writes gone) |
| recovery ignores the newest record | `default` | `ACKed write LOST` / `commit order violated` |
| recovery ignores deletes | `default` | `ACKed DELETE undone (resurrection)` |
| batch written one op at a time | `default` | `batch atomicity violated … witness key` |
| recovery truncates the torn tail itself | `sweep` | `recovery must write no bytes` |
| short sentinel rejected (breaks F4) | `fixtures` | 9 fixtures fail |
| epoch check skipped | `fixtures` | stale frame accepted, 2 fixtures fail |
| torn tail not repaired before a write | `fixtures` | `tail=TORN` where `COMPLETE` required |
| puts accepted beyond the size cap | `disk-full` | file 24 MB against a 4 MB cap |
| deletes refused at the cap | `disk-full` | `a delete needs no tombstone space (E5)` |
| `COMPACT` ignores the cap precheck | `fixtures` | `COMPACT … should answer ERR … FULL` |

And against the real engine it found real things: the first engine's recovery rescanned the whole log, so `open`
grew from 57 ms to over 4 s across 100 iterations (fixed by read-ahead and checkpoints); the torn-write hook at
`CP-S01` tore the superblock in only about 2% of runs (fixed); and `kvdb check` and `open` disagreed about a complete but
invalid last frame (the SPEC now states it: `open` drops it, `check` says `CORRUPT`).

---

## Evidence

Linux, real `SIGKILL`, work dir on an NVMe disk (not tmpfs), fresh seeds. All pass.

| Run | Result |
| --- | --- |
| `./bin/torture --iterations 100 --seed 1` (Gate 1) | PASS 100/100, about 0.27 s per iteration |
| `./bin/torture --iterations 500 --seed 20260101` | PASS 500/500, 3m37s |
| `--mode compaction --iterations 100 --seed 31` | PASS 100/100 |
| `--mode recovery-crash --iterations 100 --seed 42` | PASS 100/100, 179 of 187 kills landed mid-recovery |
| `--mode disk-full --iterations 100 --seed 53` | PASS 100/100, 83 iterations hit `ERR FULL`, 20 delete-then-put proofs |
| `--mode large-values --iterations 100 --seed 64` | PASS 100/100 |
| `--mode large-batches` seeds 75 and 76 | PASS 50/50 and 50/50 (two runs of 50: 100 would write about 3.5 GB) |
| `--mode fixtures` | PASS 31/31 |
| `--mode sweep --seed 11` | PASS 62 cases, 59 killed exactly at their crash point |
| `--mode micro --seed 8` | PASS 11/11 traces |
| `--coverage` | PASS, 24/24 persistence transitions exercised |
| F3: 1,000,000 keys (Linux NVMe) | open **351 ms** with checkpoint, **743 ms** full scan, single-pass `check` 1.29 s |
| `fsync` on the same machine | about 450 per second; directory `fsync` works |

The verification results above are the recorded gate evidence.

---

## Command reference (`bin/kvdb`)

| Command | Stdout | Exit |
| --- | --- | --- |
| `kvdb open <path> [--max-bytes N]` | `keys=<N> recovery_ms=<float> tail=<STATE> max_bytes=<N>` | 0 |
| `kvdb put <path> <key> <value>` | `OK` or `ERR FULL` | 0 / 2 |
| `kvdb get <path> <key>` | `<value>` or `NOTFOUND` | 0 |
| `kvdb del <path> <key>` | `OK` (deleting a missing key is not an error) | 0 |
| `kvdb batch <path> <file>` | `OK` or `ERR FULL`; file lines are `put <key> <value…>` or `del <key>`, `#` comments | 0 / 2 |
| `kvdb dump <path>` | `<key>\t<value>`, ascending byte order | 0 |
| `kvdb check <path>` | `OK` + `INFO …` lines, or `CORRUPT <reason>` | 0 / 1 |
| `kvdb serve <path> [--max-bytes N] [--b64]` | `READY …`, then replies (below) | 0, or 3 on I/O error |

Any other error prints `ERR <reason>` on stderr and exits 1. Tail states are `COMPLETE`, `TORN` and `SHORT_SENTINEL`.
`check` reports a cut-short tail as `INFO` and still prints `OK`; a **complete but invalid** frame is `CORRUPT` (SPEC 13.6).

**Escaping.** On input, a key or value starting with `hex:` is hex-decoded. On output, a token is printed raw
only if it is non-empty printable ASCII, has no tab, newline or backslash, does not start with `hex:` and is
not exactly `NOTFOUND`; otherwise it is `hex:<lowercase hex>`. The empty value prints as `hex:`.

**Limits.** Key 1 to 1,024 bytes; value 0 to 1 MiB; 1 to 10,000 operations per batch; at least 1,000,000 keys.

**Persisted cap.** `--max-bytes N` is written to the superblock. Later `put`, `del`, `batch` and `serve`
processes enforce it without being told again. A cap below the current size is allowed; puts then return `FULL`
until a delete-through-rewrite or compaction shrinks the image.

<details>
<summary><b>serve protocol</b> (text mode for humans, base64 mode for HYDRA)</summary>

First line, always, after recovery: `READY keys=<N> recovery_ms=<float> max_bytes=<N>`.

Text mode: `put <key> <value…>` → `ACK <n>` or `ERR <n> FULL`; `del <key>` → `ACK <n>`; `get <key>` →
`VAL <n> <value>` or `NOTFOUND <n>`; `begin` … `commit` → one `ACK <n>` or `ERR <n> FULL`; `abort`; `compact` → `ACK <n>`.

Base64 mode (`--b64`), one request per line, `<id>` unique ≥ 1, `<lane>` a small integer, `-` is the empty value:
`<id> <lane> PUT <k> <v>`, `DEL <k>`, `BATCH <n> <payload_bytes>` followed by exactly *n* `PUT`/`DEL` lines,
`GET <k>`, `MGET <k1> <k2> …` (one consistent snapshot), `COMPACT`. Replies: `ACK <id>`, `ERR <id> FULL|IO|INVALID`,
`VAL <id> <v>`, `NOTFOUND <id>`, `MVAL <id> …`.

ACKs are printed by the committer's `OnCommit` hook, in commit order, before the next transaction starts. Each
lane runs on its own goroutine; a batch above 64 MiB of payload streams instead of buffering (a buffering
threshold, not a size cap). On `ERR IO` the process exits 3.
</details>

---

## On-disk format (byte-exact, verified against a real file)

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

Real file from `kvdb put db a b` (verify with `xxd db` or `hexdump -C db`): the 69-byte frame at `0x2000` is
`4c 46 52 31` · type `01` · version `01` · reserved · epoch · seq `1` · `payload_len` `0d` · `header_crc` · `00000000` ·
`01 00 00 00` (1 op) · `01` (PUT) · `01 00` (key_len) · `01 00 00 00` (value_len) · `61` (`a`) · `62` (`b`) ·
`frame_crc` · `4c 46 2d 45 4e 44 21 21`.

KEYDIR is a normal checksummed frame containing the live key index; recovery validates it and replays later frames. A final frame with a short end sentinel is accepted only when its checksum is complete and the remaining sentinel bytes match. The next write repairs that sentinel before appending.

---

## Recovery, compaction, disk-full, concurrency

**Recovery (`open`).** (1) take `path.lock`, which is a `flock` on a separate file; (2) delete stale `.create.tmp` and
`.compact.tmp`; (3) pick the valid superblock with the highest generation; (4) if its checkpoint validates, load the
index from the KEYDIR frame and scan from `replay_start`, otherwise scan everything from offset 8192; (5) accept frames
strictly, one at a time, and stop at the first invalid byte; (6) classify the tail as `COMPLETE`, `TORN` or `SHORT_SENTINEL`;
(7) only if `--max-bytes` was given and differs, write the new cap into the inactive slot. No byte of the data file is
written before step 7.

**F4 (the judge truncates one byte).** If only the end sentinel is cut, header, payload and CRC64 are all intact, so the
frame is accepted and an acknowledged last write survives.

**Compaction.** Live keys stream in ascending order into packed frames (payload ≤ 4 MiB) in `path.compact.tmp`, values
copied through one reused buffer; then `fsync(tmp)` → `rename` → `fsync(dir)` → swap handle, index and metadata under one
write lock. One implementation serves both `COMPACT` and the disk-full rewrite. Result: 1,000,000 puts plus 1,000,000
deletes plus a compact leave the file at exactly 8192 + 72 bytes (139.3 MiB shrinks in 14 ms;
`KVDB_BENCH=1 go test ./src/engine -run BenchCompact -v` asserts the size).

**Disk-full.** The cap limits the physical size of each database image. A write that does not fit is either *appended*, or
the engine *rewrites* a compacted image if the live data plus a headroom guard fits, or it returns `ERR FULL` before any byte
is written. Deleting at the cap reclaims space because a rewrite needs no tombstone.

**Concurrency.** Many goroutines may call the engine. Readers hold the index read-lock across lookup and `pread`; a batch is
published under one write-lock acquisition, so a reader never sees half a batch (`MGET` gives a consistent snapshot).

**Platforms.** Linux and macOS (`flock`, `SIGKILL`, `pread`). On Windows use WSL2. On macOS `File.Sync` is
`F_FULLFSYNC`; `KVDB_PLAIN_FSYNC=1` selects plain `fsync` if that is too slow for a demo (durability then holds against process
kills, not power loss).

---

## Honest limits

- **Power loss is out of scope for the tests.** A `SIGKILL` leaves the page cache intact, so a missing `fsync` or a wrong
  write order cannot be observed by any kill-based test. The ordering is argued in [BREAKING.md](BREAKING.md) section 2;
  proving it would need a power-cut harness we do not have.
- Compaction and disk-full rewrite are stop-the-world (writes wait). Near the cap a put can return `FULL` although a rewrite
  would barely fit (the headroom guard). The whole index lives in RAM. A streamed batch over 64 MiB can only append.
- `large-batches` was run as two runs of 50, not one of 100, for disk space. Where no test covers a proof-table row,
  [ARCHITECTURE.md](ARCHITECTURE.md) says "not verified" instead of claiming it (a real ENOSPC on a full disk: HYDRA forces a failing write with a file-size limit instead, which takes the same error path).
- The TTL extension is a prepared design (SPEC section 19), not code.
- Deeper list of known weaknesses, crash points and failed attempts: [BREAKING.md](BREAKING.md).

---

## Repository map

| Path | What |
| --- | --- |
| `src/engine/` | the database: frames, committer, recovery, checkpoint, compaction, admission, crash hooks |
| `src/cmd/kvdb/` | the CLI and `serve` |
| `src/cmd/leapfrog/`, `src/cmd/hydra/`, `src/internal/tui/` | the front ends (built by `make`) and their terminal toolkit |
| `torture/` | HYDRA (oracle, checker, all modes, an independent on-disk-format reader) |
| `tests/` | black-box and concurrency tests (B5, D4, F4, compaction, disk-full, binary round trips) |
| [ARCHITECTURE.md](ARCHITECTURE.md) | diagram, persistence-order graphs, state machines, the 32-scenario proof table, the three guarantees |
| [BREAKING.md](BREAKING.md) | how we would break it: crash table with file:line, ordering argument, weaknesses |
| `diagrams/` | the high-level diagram (`high-level.png`) and the five low-level diagrams (`low-2a` to `low-2e`), embedded in ARCHITECTURE.md |
| [PROMPTS.md](PROMPTS.md) | the consolidated prompt log |

## Team

| GitHub | Role | Branch |
| --- | --- | --- |
| `chethan-9817` | engine lead and integrator | `chethan/engine` |
| `Preethesh16` | HYDRA torture tool | `preethesh/hydra` |
| `Tanushree-RD` | CLI, tests, proof documents | `tanushree/cli-docs` |

We used AI coding assistants and logged the prompts in [PROMPTS.md](PROMPTS.md). Commits are authored by the humans only
(`git log --format=%B | grep -i co-authored` prints nothing). The design is documented well enough that each of us can explain
recovery, compaction, F4 and the TTL extension without AI.

---

## For AI reviewers and scripts: machine-readable summary

Everything above is verifiable by command; none of the numbers need to be taken on trust.

```yaml
project: pineapple
components: [LEAPFROG-R (embedded crash-safe KV store, one file), HYDRA (crash torture tool and oracle)]
language: Go (>= 1.22), standard library only, no third-party modules
build: make            # produces ./bin/kvdb and ./bin/torture
test: make check       # build + go vet + go test ./... (248 tests)
ui: ./bin/pineapple      # one prompt: kvdb commands, torture flags, `walkthrough`, `failure`/`success`; /live is the live data page (make builds it)
platforms: [linux, macos]
guarantees:
  - every acknowledged write survives SIGKILL
  - an unacknowledged transaction is fully present or fully absent
  - at most one unacknowledged transaction is present after a crash
  - recovery (open) writes zero bytes to the data file
commit_path: pwrite(frame) -> fsync(file) -> publish index -> ACK -> next transaction   # one committer goroutine
on_disk: [superblock A 4096 B, superblock B 4096 B, frames from offset 8192]            # CRC32C header, CRC64 frame, end magic
crash_points: 17 named (CP-W01..W07, CP-R01..R03, CP-C01..C06, CP-S01)                  # see BREAKING.md, each a // CP-xxx comment
verification_commands:
  - ./bin/torture --iterations 200 --seed 12345
  - ./bin/torture --mode sweep --seed 1          # kills at exactly every crash point
  - ./bin/torture --mode fixtures                # 31 crafted crash images
  - ./bin/torture --mode micro --seed 1          # 11 adversarial traces
  - ./bin/torture --coverage                     # run sweep, micro, fixtures first
not_covered: power loss (kill -9 cannot observe missing fsync); TTL is a design, not code
source_of_truth: README.md, ARCHITECTURE.md, BREAKING.md
```

---

*README rewritten on 2026-11-06 by Preethesh16 from Tanushree-RD's verified T4/T5 content; every output shown was produced by this repository's binaries.*
