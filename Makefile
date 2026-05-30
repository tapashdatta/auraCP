# auraCP build. Pure-Go for cross-compilation; the Aura DB Postgres
# classifier (PR #2.5) pulls libpg_query through pg_query_go/v5 which
# requires cgo for the AST path. CGO_ENABLED=0 builds remain valid —
# they degrade Postgres to the PR #2 tokenizer fallback.
VERSION ?= 0.3.23
LDFLAGS := -s -w -X main.version=$(VERSION)
GO := go

# CGO_CFLAGS workaround for macOS SDK 15+: the SDK declares strchrnul,
# but libpg_query also declares its own when HAVE_STRCHRNUL is unset.
# Passing -DHAVE_STRCHRNUL=1 selects libpg_query's #else branch which
# defers to the SDK symbol. Harmless on Linux (where the same SDK
# symbol exists). Override with CGO_CFLAGS="" if your toolchain
# disagrees.
export CGO_CFLAGS ?= -DHAVE_STRCHRNUL=1

.PHONY: all ui ui-dbadmin daemon cli build dist deb clean test vet fmt run

all: build

## ui: build BOTH the panel SPA and the Aura DB SPA and embed them.
ui: ui-dbadmin
	cd web && npm install && npm run build
	rm -rf internal/webui/dist
	cp -R web/dist internal/webui/dist

## ui-dbadmin: build the Aura DB Svelte SPA and embed it for /dbadmin/.
## FIX (PR #11 INT-7): the previous recipe `rm -rf internal/dbadmin/
## webui/dist` wiped the `.gitkeep` sentinel on every build, which left
## git in a perpetually-dirty state. We now keep the dist directory and
## delete only the previous build's artifacts.
ui-dbadmin:
	cd web-aura-db && npm install && npm run build
	mkdir -p internal/dbadmin/webui/dist
	find internal/dbadmin/webui/dist -mindepth 1 -name '.gitkeep' -prune -o -exec rm -rf {} +
	cp -R web-aura-db/dist/. internal/dbadmin/webui/dist/

## build: native build of daemon + CLI.
## FIX-8 (PR #11): depends on ui-dbadmin so a fresh clone produces a
## working binary. Without this dependency, `go:embed internal/dbadmin/
## webui/dist/*` resolves to an empty directory and the daemon serves
## the embed-not-built sentinel for every /dbadmin/* request.
build: ui-dbadmin
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/auracpd ./cmd/auracpd
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/auracp  ./cmd/auracp
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/aura-db ./cmd/aura-db

## dist: cross-compile release binaries for Debian/Ubuntu on x86-64 and ARM64
## v0.3.2: aura-db standalone binary joins auracpd + auracp. CGO_ENABLED=0
## degrades the Postgres AST classifier to the tokenizer fallback (per PR
## #2.5 contract) — acceptable for the cross-compile path; the audit-verify
## tooling + MFA (TOTP/recovery) paths are all pure Go.
dist: ui
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracpd-linux-amd64 ./cmd/auracpd
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracpd-linux-arm64 ./cmd/auracpd
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracp-linux-amd64  ./cmd/auracp
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracp-linux-arm64  ./cmd/auracp
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/aura-db-linux-amd64 ./cmd/aura-db
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/aura-db-linux-arm64 ./cmd/aura-db
	@ls -lh dist/

## deb: build .deb packages for amd64 + arm64 (run after `make dist`)
deb: dist
	bash packaging/build-deb.sh amd64 $(VERSION)
	bash packaging/build-deb.sh arm64 $(VERSION)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

run:
	$(GO) run ./cmd/auracpd -provision=false -tls=false

clean:
	rm -rf bin dist
