# auraCP — Development Guide

**Status:** Draft v0.1 · 2026-05-27

How to build, run, and contribute to auraCP. See [ARCHITECTURE.md](ARCHITECTURE.md) for the design.

---

## 1. Prerequisites

| Tool | Version | For |
|---|---|---|
| Go | ≥ 1.23 | the `auracpd` daemon + `auracp` CLI |
| Node.js | **24 LTS** | building the Svelte UI (dev only; not needed at runtime) |
| npm | ≥ 10 | UI dependencies |
| Caddy / xcaddy | latest | local data-plane testing (optional on macOS) |
| A Debian 13 VM | — | full end-to-end testing (multipass, LXC, or a cloud box) |

The production server needs **none of the build tools** — auraCP ships as a single Go binary with the
UI embedded.

---

## 2. Repository layout

```
auraCP/
├── cmd/
│   ├── auracpd/        # daemon: API + embedded SPA (main package)
│   └── auracp/         # CLI
├── internal/           # all panel logic (see ARCHITECTURE §7)
│   ├── api/ site/ webserver/ php/ runtime/ db/ ssl/ cloudflare/
│   ├── osuser/ cron/ files/ logs/ store/ systemd/ exec/
├── web/                # Svelte UI (Vite). Built output is embedded by auracpd.
│   ├── package.json vite.config.js svelte.config.js
│   └── src/
├── templates/          # Caddyfile fragments + systemd unit templates
├── migrations/         # SQLite schema migrations
├── docs/               # SCOPE / ARCHITECTURE / PLAN / DEVELOPMENT (this set)
├── design/             # UI design prototype (auracp-prototype.html)
└── install.sh          # bootstrap installer (CloudPanel's, kept as reference until P1)
```

> Note: `install.sh` is currently CloudPanel's original installer, kept **for reference**. It will be
> replaced in P1 by auraCP's own minimal installer.

---

## 3. UI development (`web/`)

```bash
cd web
npm install
npm run dev        # Vite dev server with HMR
npm run build      # production build → web/dist (later embedded in the Go binary)
```

- **Svelte 5** + Vite. Keep the bundle lean — avoid heavy UI/icon libraries; prefer inline SVG and
  CSS. One CSS file of design tokens drives theming.
- **Theming:** light is the default; dark via `data-theme="dark"` on `<html>`. All colors come from
  CSS custom properties — never hard-code hex in components.
- The standalone prototype `design/auracp-prototype.html` is the visual source of truth for v0.

---

## 4. Daemon development (`cmd/auracpd`)

```bash
go run ./cmd/auracpd        # run the daemon locally
go build ./...              # build everything
go test ./...               # tests
```

- The Svelte build is embedded with `go:embed`; for local UI iteration, run the Vite dev server and
  point the daemon at it (a `-dev` flag proxies to `localhost:5173`), or rebuild `web/dist`.
- SQLite lives at a configurable path (default `/home/clp`-style data dir on a server; a local file
  in dev). Migrations run on startup.

### Privileged actions
System mutations go through `internal/exec` (the audited executor) and the typed command structs —
never build shell strings from user input. File operations on site content **drop to the site user**.

---

## 5. End-to-end testing on Debian 13

Because auraCP manages real Linux users, systemd units, and packages, full testing needs a Debian 13
host (a throwaway VM is ideal):

```bash
# on the VM, as root
./install.sh                       # (P1+) provisions Caddy, FrankenPHP, Node 24, MariaDB, Postgres
systemctl status auracpd
# panel: https://<vm-ip>:8443
```

Use Let's Encrypt **staging** during development to avoid rate limits.

---

## 6. Conventions

- **Match surrounding code** in style, naming, and comment density.
- Go: standard `gofmt`; small, focused packages; interfaces at consumer side. `SiteManager` has one
  implementation per site type (Create/Update/Delete).
- Config is **rendered from templates** in `templates/` then a service reload — never hand-edited.
- Everything that changes the system is **idempotent** and **audited**.
- **Minimal-package policy** (see SCOPE §6): justify every new package; PHP 8.3+ only.
- Keep desired state in SQLite; the filesystem/services are derived from it.

---

## 7. Building a release

```bash
cd web && npm run build && cd ..      # produce embedded UI
go build -trimpath -ldflags="-s -w" -o auracpd ./cmd/auracpd
go build -trimpath -ldflags="-s -w" -o auracp  ./cmd/auracp
```

Target packaging (P7): a Debian `.deb` + a one-line install script, x86-64 and ARM64.

---

## 8. Useful references

- CloudPanel architecture analysis & extracted feature design: see the working plan at
  `~/.claude/plans/does-this-file-help-humming-valiant.md`.
- UI prototype: [`design/auracp-prototype.html`](../design/auracp-prototype.html).
- Caddy + FrankenPHP + Souin docs; `xcaddy` for custom builds with `caddy-dns/cloudflare`.
