# BREAKING — How we would break LEAPFROG-R

> **Owner:** Tanushree-RD (final at T4), with crash-point verification by chethan-9817. **Maximum two pages.** Every crash point is checked against the code by the judges: a row described wrongly costs more than a row left out.

## 1. Crash points table

<!-- Start from SPEC section 12. For each CP, add the exact file:line, verify "on disk" and "next open()" against the real code with Chethan, and add the HYDRA sweep command that exercises it. -->

| CP | Where (file:line) | On disk if killed here | What the next open() does | Sweep command |
| --- | --- | --- | --- | --- |
| CP-W01 | | | | `./bin/torture --mode sweep --cp CP-W01 --seed 1` |

## 2. The ordering argument

<!-- SPEC 5.2, adjusted to the real code: name every system call (pwrite, fsync, ftruncate, rename, directory fsync) and say why each ordering preserves the invariant. Record the SPEC 5.3 fsync decision made on the demo laptop. -->

## 3. Known weaknesses

<!-- At least three, honest. Start from SPEC 17 and keep only what is true of the final code. -->

## 4. What we tried that failed

<!-- Only REAL events from progress/*.md and HYDRA failures during the event. Never invent any. -->

## 5. If we had 24 more hours

<!-- Three concrete items, in priority order. -->
