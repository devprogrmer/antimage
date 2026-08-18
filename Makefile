GO      ?= go
LDFLAGS := -X github.com/amyrm/antimage/internal/shared/version.Version=$(shell git describe --tags --always --dirty)
BUILD   := CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: test lint build check-imports check-rtl web proto proto-lint sync-install clean

# buf is installed with `go install github.com/bufbuild/buf/cmd/buf@latest`,
# which places it in $(go env GOPATH)/bin. Put that directory on PATH rather
# than installing buf system-wide.
BUF ?= buf

test:
	$(GO) test ./... -race -count=1

proto-lint:
	$(BUF) lint

# Regenerates internal/shared/proto/**. The `module=` option in buf.gen.yaml
# strips the Go module prefix from each go_package, so output lands next to
# the rest of the internal tree rather than beside the .proto sources.
# Generated files are committed: the build must not require buf.
proto: proto-lint
	$(BUF) generate

lint:
	$(GO) vet ./...
	golangci-lint run
	gosec -quiet ./...

check-imports:
	./scripts/check-imports.sh

check-rtl:
	./scripts/check-rtl.sh

# Vite writes into internal/panel/webui/dist, which is where go:embed reads
# from, so `build` depends on this: a released binary always carries a UI
# built from the sources in this tree, never a stale one.
web:
	cd web && npm ci && npm run build

# go:embed cannot reach outside internal/panel/httpapi, so the panel keeps its
# own copy of the bootstrap script. This refreshes it. The copy is not trusted
# to stay fresh on its own: TestEmbeddedScriptMatchesSource fails the test
# suite whenever the two files differ, so a direct edit to either one is
# caught by `go test`, not only by whoever remembers to build through make.
sync-install:
	cp scripts/install.sh internal/panel/httpapi/install.sh

build: sync-install web
	$(BUILD) -o bin/antimage-panel ./cmd/antimage-panel
	$(BUILD) -o bin/antimage-node  ./cmd/antimage-node
	$(BUILD) -o bin/antimage-ctl   ./cmd/antimage-ctl

clean:
	rm -rf bin

# Acceptance suite for the SP1 definition of done. It runs a real panel and a
# real agent over loopback with genuine mTLS, so it needs no Docker daemon.
e2e:
	$(GO) test ./test/e2e/... -tags e2e -count=1 -timeout 15m -v
