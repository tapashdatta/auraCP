# auraCP build. Pure-Go (no cgo) → trivial cross-compilation for both arches.
VERSION ?= 0.3.0
LDFLAGS := -s -w -X main.version=$(VERSION)
GO := go

.PHONY: all ui ui-dbadmin daemon cli build dist deb clean test vet fmt run

all: build

## ui: build BOTH the panel SPA and the Aura DB SPA and embed them.
ui: ui-dbadmin
	cd web && npm install && npm run build
	rm -rf internal/webui/dist
	cp -R web/dist internal/webui/dist

## ui-dbadmin: build the Aura DB Svelte SPA and embed it for /dbadmin/.
ui-dbadmin:
	cd web-aura-db && npm install && npm run build
	rm -rf internal/dbadmin/webui/dist
	mkdir -p internal/dbadmin/webui/dist
	cp -R web-aura-db/dist/. internal/dbadmin/webui/dist/

## build: native build of daemon + CLI.
## FIX-8 (PR #11): depends on ui-dbadmin so a fresh clone produces a
## working binary. Without this dependency, `go:embed internal/dbadmin/
## webui/dist/*` resolves to an empty directory and the daemon serves
## the embed-not-built sentinel for every /dbadmin/* request.
build: ui-dbadmin
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/auracpd ./cmd/auracpd
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/auracp  ./cmd/auracp

## dist: cross-compile release binaries for Debian/Ubuntu on x86-64 and ARM64
dist: ui
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracpd-linux-amd64 ./cmd/auracpd
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracpd-linux-arm64 ./cmd/auracpd
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracp-linux-amd64  ./cmd/auracp
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o dist/auracp-linux-arm64  ./cmd/auracp
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
