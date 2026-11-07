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
./bin/hydra scenario B5          # one scenario
./bin/hydra scenario A1 A2 A3    # several
./bin/hydra all --quick          # all 32 at a tenth of the iterations (about 1 minute)
./bin/hydra all                  # all 32 at full counts, with a scoreboard
./bin/hydra judge                # the whole 15-minute path in one go
```

## 7. The one-prompt demo

```bash
./bin/pineapple
```

It prints the command list on start (`/help` prints it again). Open <http://localhost:8099> in a browser to watch the database
live. Database commands run `bin/kvdb`; a line starting with `-` (or `attack`, `judge`, `scenario`, ...) runs the torture tool.
`/exit` leaves.

Database commands:

```text
open demo.db              # create, run recovery, print key count and recovery time
put demo.db city Mumbai   # store a value
get demo.db city          # read it back
del demo.db city          # delete it
batch demo.db ops.txt     # a file of put/del lines as ONE atomic batch
dump demo.db              # every key and value, sorted
check demo.db             # OK or a reason
serve demo.db             # prints ACK after each durable op (kill -9 it from a second terminal)
compact                   # rewrite only the live keys; the file shrinks
inspect demo.db -v        # decode the file, label every byte of the first frame
```

Torture commands (the leading `--` is optional for `iterations`, `seed`, `mode`):

```text
--iterations 200 --seed 12345
--iterations 500 --seed 12345
--iterations 20  --seed 999   --mode large-values
--iterations 10  --seed 999   --mode large-batches
--iterations 100 --seed 12345 --mode compaction
--iterations 30  --seed 12345 --mode recovery-crash
--iterations 50  --seed 12345 --mode disk-full
--mode sweep --seed 1
--mode micro --seed 1
--mode fixtures
--coverage                    # run sweep, micro, fixtures first
--replay failures/12345-87
attack demo.db --rounds 10    # kill -9 a server rewriting YOUR data; it must come back identical
judge
scenarios
scenario B5 A4 F4
all --quick
all
```

Demos:

```text
walkthrough          # the whole working of the database on real files, step by step (about 40 s; `walkthrough fast` skips the pauses)
failure              # a planted bug (ACK before the write) is caught by HYDRA; the live page shows the gap in red
success              # the same test on the real engine passes; the live page shows "gap: none" in green
```

More: [ARCHITECTURE.md](ARCHITECTURE.md) · [BREAKING.md](BREAKING.md) · [torture/README.md](torture/README.md)
