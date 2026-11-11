# make            build the page, then the binary with the page inside
# make test       Go and page tests
# make release    static binaries for Linux and macOS in dist/
# make pages      the live demo as a static site in pages/ (the engine as WebAssembly)
# make shots      the README's pictures: the running demo and real command-line sessions, staged as glass
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
TARGETS := linux/amd64 linux/arm64 darwin/arm64 darwin/amd64
export CGO_ENABLED = 0

.PHONY: all ui build test pages release shots

all: build

ui:
	cd web && npm ci --no-audit --no-fund && npm run build

build: ui
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/holdfast ./cmd/holdfast

test:
	go vet ./...
	CGO_ENABLED=1 go test -race ./...    # the race detector needs cgo; builds stay static
	cd web && npm run typecheck && npm test

# The page on its own, with the engine compiled for the browser where the
# server would be. wasm_exec.js is Go's loader and has to come from the Go
# that built the engine.
pages:
	cd web && npm ci --no-audit --no-fund && npm run build:pages
	GOOS=js GOARCH=wasm go build -trimpath -ldflags '$(LDFLAGS)' -o pages/holdfast.wasm ./cmd/holdfast-wasm
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" pages/wasm_exec.js
	cp docs/readme/social.png pages/social.png

release: ui
	mkdir -p dist
	set -e; for t in $(TARGETS); do \
		GOOS=$${t%/*} GOARCH=$${t#*/} go build -trimpath -ldflags '$(LDFLAGS)' -o dist/holdfast-$${t%/*}-$${t#*/} ./cmd/holdfast; \
	done
	cd dist && sha256sum holdfast-* > SHA256SUMS

shots: build
	cd docs/readme/src && npm ci --no-audit --no-fund && ./build.sh ../../../bin/holdfast
