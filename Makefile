GO      ?= go
LDFLAGS := -X github.com/amyrm/antimage/internal/shared/version.Version=$(shell git describe --tags --always --dirty)
BUILD   := CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: test lint build check-imports clean

test:
	$(GO) test ./... -race -count=1

lint:
	$(GO) vet ./...
	golangci-lint run
	gosec -quiet ./...

check-imports:
	./scripts/check-imports.sh

build:
	$(BUILD) -o bin/antimage-panel ./cmd/antimage-panel
	$(BUILD) -o bin/antimage-node  ./cmd/antimage-node
	$(BUILD) -o bin/antimage-ctl   ./cmd/antimage-ctl

clean:
	rm -rf bin
