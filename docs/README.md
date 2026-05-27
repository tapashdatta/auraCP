# auraCP — Documentation

A lightweight, self-hosted server control panel — a modern, minimal alternative to heavyweight control panels.
Go control plane · Caddy + FrankenPHP data plane · Svelte UI · SQLite. PostgreSQL per site and
Node 24 LTS on every site. No email server. Target: Debian 13.

## Documents

| Doc | What it covers |
|---|---|
| [SCOPE.md](SCOPE.md) | Vision, in/out of scope, features, differentiators, minimal-package policy, success criteria |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Control vs data plane, tech choices, privilege & per-site model, data model, Go layout, UI, security |
| [PLAN.md](PLAN.md) | Status snapshot, phased milestones (P0–P7), exit criteria, verification |
| [DEVELOPMENT.md](DEVELOPMENT.md) | Prerequisites, repo layout, UI + daemon dev, E2E testing, conventions, release build |
| [TESTING.md](TESTING.md) | Debian/Ubuntu VM validation checklist (build → install → exercise every feature → teardown) |

## Quick links

- UI design prototype: [`../design/auracp-prototype.html`](../design/auracp-prototype.html)
- Working plan (detailed): `~/.claude/plans/does-this-file-help-humming-valiant.md`
