# AGENTS.md

This project's agent and developer guide is **[`CLAUDE.md`](CLAUDE.md)** — it
covers the architecture, the repository map, the invariants you must not break,
and how to extend Momos safely.

Design rationale and the code-verified Hades contract are in **[`plan.md`](plan.md)**
(read §§10–12 first). Deep docs are in **[`docs/`](docs/)**.

Before changing anything load-bearing, check the invariants list in `CLAUDE.md`
— several are guarded by tests (`make test`).
