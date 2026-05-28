# auraCP build. Pure-Go (no cgo) → trivial cross-compilation for both arches.
VERSION ?= 0.1.10
LDFLAGS := -s -w -X main.version=$(VERSION)
GO := go

.PHONY: all ui daemon cli build dist deb clean test vet fmt run

all: build

## ui: build the Svelte SPA and embed it into the daemon package
ui:
	cd web && npm install && npm run build
	rm -rf internal/webui/dist
	cp -R web/dist internal/webui/dist

## build: native build of daemon + CLI (requires ui already embedded)
build:
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
