# pineapple — LEAPFROG-R + HYDRA

Team entry for the **Kill It and Prove It** hackathon: a crash-safe, single-file key-value database (LEAPFROG-R) and the torture tool that proves it (HYDRA). Go, standard library only.

> **Owner:** Tanushree-RD. This README is replaced at stage T4 with the judge-facing version: build, commands, byte-exact on-disk format, recovery algorithm.

## Start here

1. Read **[MASTER_PROMPT.md](MASTER_PROMPT.md)**: roles, branches, checkpoints and rules. Paste it into your AI tool.
2. Read **[docs/SPEC.md](docs/SPEC.md)**: the frozen technical specification.
3. Track the team in **[PROGRESS.md](PROGRESS.md)** (dashboard) and **[progress/](progress/)** (per-person logs).
4. Log every AI prompt in **[prompts/](prompts/)** (see [PROMPTS.md](PROMPTS.md)).

## Team

| GitHub | Role | Branch |
| --- | --- | --- |
| chethan-9817 | Engine lead + integrator | `chethan/engine` |
| Preethesh16 | HYDRA torture tool lead | `preethesh/hydra` |
| Tanushree-RD | CLI, tests and proof docs lead | `tanushree/cli-docs` |

## Build (after checkpoint M0)

```bash
make            # builds bin/kvdb and bin/torture
make check      # build + vet + tests
```
