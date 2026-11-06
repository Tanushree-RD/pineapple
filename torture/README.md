# HYDRA — the torture tool

HYDRA SIGKILLs `bin/kvdb` at random or exact moments and proves, against its own independent oracle,
that nothing acknowledged was lost. It never trusts the database: `oracle.log` (what HYDRA sent and
what it was ACKed) is the truth, `kvdb dump` is only an observation. Go standard library only.

```bash
make                                   # builds bin/kvdb and bin/torture
./bin/torture --iterations 200 --seed 12345      # the judge's run: default mode
```

Without `--workdir`, HYDRA creates its working directory on a real disk: the system temp directory, or the user cache
directory when that temp directory is RAM (tmpfs). On tmpfs fsync is free, runs get slower and files far larger, so it
prints a NOTE if you point `--workdir` at one yourself.

## Modes

| Command | What it does |
| --- | --- |
| `--mode default` (default) | 2 to 4 lanes (put, delete, batch with witness key, mixed, hot shared keys), seeded kill after 10 to 250 ms, then `open`, `check`, `dump` and the oracle. Purely a random timer: it never uses crash hooks. |
| `--mode large-values` | every value 256 KiB to 1 MiB |
| `--mode large-batches` | 10,000-op batches, buffered (payload <= 64 MiB) and streamed (> 64 MiB), plus concurrent singles |
| `--mode compaction` | the default workload plus one `COMPACT` at a seeded moment; every 10th iteration lets it finish and asserts the file shrank |
| `--mode recovery-crash` | builds 200,000 keys (`--big`: 1,000,000), then SIGKILLs `kvdb open` mid-recovery 1 to 3 times; the file must stay byte-identical and the final open take < 2 s |
| `--mode disk-full` | `open --max-bytes 4194304` once (persisted); fills to the cap with kills; every 5th iteration: full -> delete -> put must ACK again |
| `--mode sweep [--cp CP-W03] [--hits N]` | deterministic: `KVDB_CRASH=<cp>:<n>` kills the database at exactly that line of SPEC 12, with per-point expectations; prints state fingerprints |
| `--mode micro [--cp ...]` | the 11 tiny traces of SPEC 15.7, killed at every crash point they pass plus 20 random instants; each crash is followed by a write |
| `--mode fixtures` | 31 crafted crash images (torn header/payload/crc, F4 short sentinel 1 to 8 bytes, bad CRC, stale frames, torn superblock, checkpoints, stale temp files, clean close, refused compaction, failed write) |
| `--coverage` | table of every persistence transition (SPEC 15.9) with hit counts; a row with zero hits is a GAP (exit 1). Run sweep, micro and fixtures first, in the same directory. |
| `--replay failures/<seed>-<iter>` | re-runs the oracle check on a saved failure |

Other flags: `--seed` (if missing one is picked and printed), `--iterations` (500), `--writers` (2..4, 3),
`--kvdb` (`./bin/kvdb`), `--workdir`, `--keep`, `--max-bytes`, `--big`, `--hits` (sweep, 6).
Exit code: 0 on pass, 1 on the first failure, 2 on a usage or setup error.

## Reading the output

```
iter 200/200 seed=12345 mode=default kill_ms=150 acked=65 inflight=24 rejected=0 keys=8123 open_ms=40 tail=COMPLETE check=OK PASS
kill landing over 200 kills: 200 had operations in flight (avg 24 per kill); 173 left a durable but unACKed transaction (killed between fsync and ACK); 0 left a torn tail and 0 a short end marker (killed mid-write)
RESULT PASS 500/500 (seed=12345, mode=default, 3m37s)
```

`acked` ops must all be present; `inflight` ops (sent, no reply) may be present or not, never partly, and
at most ONE unACKed transaction may be present (the one the single committer was inside at the kill);
`rejected` (ERR FULL) ops must never appear. A failure prints the key (hex), the observed op id, the
allowed op ids with SENT and ACK times and the kill delay, and saves
`failures/<seed>-<iter>/{database.db, oracle.log, operations.log, metadata.json}`.

## How it decides (SPEC 14)

- Values are self-describing (`HYDR` header, op id, key hash, CRC, PRNG body), so any observed value names
  exactly one operation and proves it is neither torn nor from another key.
- Per key, the survivor must be the ACKed op with the highest ack_seq (ACK lines are printed by the
  committer in commit order), or the one unACKed in-flight transaction if it is complete on all its keys.
- After every kill HYDRA also checks: recovery wrote no bytes (CRC64 of the file before and after
  open/check/dump), `ERR INVALID` never happens, and the persisted cap is enforced without being repeated.
- `diskformat.go` is HYDRA's own reader/writer of the SPEC 4 on-disk format, used by fixtures, sweep and
  micro; it is written from the SPEC, not from the engine.

## What it cannot see

A SIGKILL leaves the page cache intact, so a missing `fsync` or a wrong write order (SPEC 15.6 mutants M1,
M2, M5) cannot be observed by any mode here; that needs a power-cut test. A seed reproduces the workload and
the requested kill delay, not OS scheduling or the exact instruction a real SIGKILL lands on; `sweep` is the
exactly reproducible path.

## Tests

`go test ./torture` includes unit tests (values, oracle, checker, dump parser, on-disk format), an
integration suite against `testdata/fakekvdb` (a test double with planted bugs that HYDRA must catch:
volatile, droplast, resurrect, nonatomic, garble, noshrink) and short end-to-end runs of every mode against
a freshly built real `kvdb`.
