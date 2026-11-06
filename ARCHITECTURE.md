# ARCHITECTURE — LEAPFROG-R

> **Owner:** Tanushree-RD. Draft at T2/T3, final at T4. Every claim must match the code. The judge checks three random rows of the proof table against the source during the demo.

## Part 1 — Architecture diagram

<!-- T2: diagrams/architecture.(excalidraw|png|mmd). It must show:
- every component: client API, committer, frame writer, in-memory index + RWMutex, superblocks, KEYDIR checkpoint, recovery, compaction temp + rename, disk-full admission, path.lock
- the on-disk layout with field sizes (SPEC 4)
- the numbered write ordering: 1 admission · 2 tail repair · 3 pwrite · 4 fsync · 5 publish · 6 ACK
- the recovery flow: lock → clean temps → superblock → checkpoint → scan → stop at first invalid → ready
- the compaction flow: temp → fsync → rename → fsync dir → swap
-->

## Part 1b — Persistence-order graphs and state machines

<!-- T4: from SPEC 5.5 (rules P1 to P4 + graphs for write, checkpoint, cap change, compaction) and SPEC 12.1 (W/R/C/S state machines, each state mapped to its CP-xxx). Crash-here answers should point at a named state. -->

### Persistence rules P1 to P4

### Write path graph and W0 to W5

### Checkpoint and cap-change graph and S0 to S2

### Compaction graph and C0 to C4

### Recovery R0 to R3

## Part 2 — Scenario-to-mechanism proof table (all 32 rows)

<!-- T3: start from SPEC section 16. Columns: Scenario | Disk state at the kill | Mechanism | Where in the code (file:function) | Verified by (mode + seed, or "manual test" with steps, or "not verified") -->

| Scenario | Disk state at the kill | Mechanism | Where in the code | Verified by |
| --- | --- | --- | --- | --- |
| A1 | | | | |

## Part 3 — The three guarantees

<!-- T4: start from SPEC 5.4, then check every system call it names against the code. -->

### Durability

### Atomicity

### Recoverability
