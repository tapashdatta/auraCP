# Aura DB — Documentation Index

Aura DB is auraCP's native database administration tool for MariaDB /
MySQL and PostgreSQL. Designed to replace Adminer (currently bundled)
while staying compatible with auraCP's lightweight motto. Distributed
in two deployment modes from a single Go codebase:

- **Integrated** — built into `auracpd`; surfaces in the panel at `/dbadmin`.
- **Standalone** — separate `aura-db` binary with its own user database
  and audit log.

This directory holds the canonical specs that the implementation must
honor. Updates require an architectural decision record (ADR).

## Foundation documents

These three define what Aura DB IS. Every line of code that lands must
be consistent with them.

| Document                         | Purpose                                                       | Status |
| -------------------------------- | ------------------------------------------------------------- | ------ |
| [`SECURITY.md`](SECURITY.md)     | Threat model, defense layers, controls. Canonical.            | v0.1   |
| [`ADR-001-architecture.md`](ADR-001-architecture.md) | The master architecture decision record. Scope, two-binary split, package layout, phased timeline. | Accepted |
| [`SDK.md`](SDK.md)               | The Go interfaces (Auth / ConnectionStore / AuditSink) for embedding Aura DB. Stability guarantees, HTTP surface, reference implementations. | v0.1   |

## Architectural Decision Records

ADRs live in `docs/aura-db/ADR-NNN-<slug>.md`. Each ADR has a stable
number that never changes once assigned.

| ADR                                              | Title                       | Status |
| ------------------------------------------------ | --------------------------- | ------ |
| [ADR-001](ADR-001-architecture.md)               | Aura DB Architecture        | Accepted |

Future ADRs (planned but not yet drafted):

- ADR-002 — Query Classifier Internals (what each parser produces, how
  we map to classes, why pg_query_go is the only cgo dep).
- ADR-003 — Frontend State Model (tab persistence, undo/redo, dirty
  flags in the grid).
- ADR-004 — Audit Chain Verification Format.
- ADR-005 — Schema-aware Autocomplete Cache.
- ADR-006 — Standalone First-run Setup Flow.

## Operator-facing docs (drafted alongside v0.3.0)

These will be added as the implementation lands:

- `USING.md` — operator's UI guide. Keyboard shortcuts, common flows.
- `OPERATING.md` — install, configure, monitor Aura DB.
- `KEY-ROTATION.md` — rotate the encryption key.
- `REPRODUCIBLE-BUILDS.md` — verify a release against source.
- `BACKUP-AND-RESTORE.md` — back up Aura DB state itself.
- `STANDALONE-INSTALL.md` — standalone-mode deployment.
- `MIGRATING-FROM-ADMINER.md` — what's the same, what's different.

## Reading order

For a maintainer joining the project:

1. **`ADR-001`** first — understand WHAT we're building and WHY.
2. **`SECURITY.md`** second — understand the security constraints
   every code decision is checked against.
3. **`SDK.md`** third — understand the interface contracts before
   writing any glue code.

For a security reviewer:

1. **`SECURITY.md`** §2 (threat model) + §3 (principles) for context.
2. **`SECURITY.md`** §5-9 (controls) for the actual claims.
3. **`SDK.md`** §3 (Auth) + §5 (AuditSink) for how integrators are
   expected to wire these up.

For someone embedding Aura DB in a third-party product:

1. **`SDK.md`** in full.
2. **`ADR-001`** §3 for the package layout.
3. **`SECURITY.md`** §14 for security-relevant config.
