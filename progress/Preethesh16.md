# Progress log — Preethesh16 (HYDRA lead)

Branch: `preethesh/hydra` · Owns: `torture/**`

Rules (MASTER_PROMPT section 6):
- The AI appends one entry after **every** change set, before committing. Newest at the bottom; never edit old entries.
- Format:

```markdown
## YYYY-MM-DD HH:MM IST · <stage> · <title>
- **Files:**
- **What:**
- **Crash points touched:**
- **Tests:**
- **Status:**
- **NEEDS:**
- **Next:**
```

---

## 2026-11-06 15:35 IST · H0 · skeleton: flags, self-describing values, oracle
- **Files:** torture/main.go, torture/main_test.go, torture/values.go, torture/values_test.go, torture/oracle.go, torture/oracle_test.go
- **What:** `torture` parses all flags from SPEC 15.1 (a missing `--seed` is picked and printed; unknown or unfinished modes exit 2, never a silent pass). Values are self-describing per SPEC 14.3 (long form with CRC32C header, short form 12 to 31 bytes). The oracle records SENT/ACK/REJECT/UNKNOWN/KILL to an append-only `oracle.log`, keeps one shared event counter so "ACKed before invoked" is a plain comparison, assigns `ack_seq` in ACK arrival order (= commit order), and can parse its own log back for `--replay`.
- **Crash points touched:** none (HYDRA only observes)
- **Tests:** `go vet ./torture` + `go test ./torture` PASS (flip-every-byte, truncation, wrong-key, mixed-halves and FNV/splitmix reference vectors for values; lifecycle, per-iteration ack order and log round trip for the oracle). `make check` not run yet: M0 is not on main.
- **Status:** H0 done. Finding: a 12 to 31 byte value has no length field and its body is a PRNG prefix, so a truncated short value still decodes (same op id, smaller Size). A 12-byte value cannot detect a flipped op id either. This is inherent to SPEC 14.3; the checker (H1) compares the decoded Size with the size recorded for that op and requires the op id to be allowed for the key, so nothing slips through. Large-values mode uses the long form, which detects everything.
- **NEEDS:** from Chethan: M0 (go.mod, Makefile, .githooks, stub api.go, kvdb placeholders). I built locally with a temporary, **untracked** `go.mod`; I will delete it before merging M0. Expect an add/add conflict on `torture/main.go` against your placeholder: I will resolve it by keeping mine, as MASTER_PROMPT E0 says.
- **Next:** H1 checker (per-key rule 14.4 + commit-order rules 14.5) with unit tests, then the random-SIGKILL runner against a fake `kvdb` test double until the real engine and serve exist.
