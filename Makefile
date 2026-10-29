# bro-code build tooling — minimal, but complete.
#
# Build injects version/commit/date via ldflags (see cmd/brocode/main.go).

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

BINARY := bin/brocode
FOREMAN_BINARY := bin/foreman
BINDIR ?= $(shell $(GO) env GOPATH)/bin
FOREMAN_BINDIR ?= $(BINDIR)

.PHONY: all build build-foreman build-all test race vet fmt fmt-check lint tidy check run run-foreman coverage bench measure install install-foreman uninstall uninstall-foreman clean help

all: check build

## build: compile binary for current platform
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/brocode

## build-foreman: compile the Foreman local workspace CLI
build-foreman:
	CGO_ENABLED=0 $(GO) build -trimpath -o $(FOREMAN_BINARY) ./cmd/foreman

## build-all: cross-compile for Linux, Windows, and macOS
build-all:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/brocode-linux-amd64 ./cmd/brocode
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/brocode-linux-arm64 ./cmd/brocode
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/brocode-windows-amd64.exe ./cmd/brocode
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/brocode-darwin-amd64 ./cmd/brocode
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/brocode-darwin-arm64 ./cmd/brocode

## run-foreman: launch Foreman for the current project
run-foreman:
	$(GO) run ./cmd/foreman start

## test: run unit tests
test:
	$(GO) test ./...

## race: run tests with race detector
race:
	$(GO) test -race ./...

## vet: static analysis
vet:
	$(GO) vet ./...

## fmt: format all Go source files
fmt:
	gofmt -w ./cmd ./internal

## fmt-check: fail if any file is not formatted
fmt-check:
	@test -z "$$(gofmt -l ./cmd ./internal)" || (echo "gofmt needed:"; gofmt -l ./cmd ./internal; exit 1)

## lint: run golangci-lint (if installed)
lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || (echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; exit 1)

## tidy: clean up module dependencies
tidy:
	$(GO) mod tidy

## check: run vet + fmt-check (CI-friendly)
check: vet fmt-check

## run: launch the CLI directly
run:
	$(GO) run ./cmd/brocode

## coverage: run tests and open HTML coverage report
coverage:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

## bench: run benchmarks with memory stats
bench:
	$(GO) test -bench=. -benchmem ./...

## measure: binary size + startup performance (ritual from docs/TECH_STACK.md)
measure: build
	@ls -lh $(BINARY) | awk '{print "binary size:", $$5}'
	@/usr/bin/time -p ./$(BINARY) --search mcp >/dev/null

## install: build and install to $(BINDIR) (default: ~/go/bin)
install: build
	install -d $(BINDIR)
	install -m 0755 $(BINARY) $(BINDIR)/brocode

## install-foreman: build and install Foreman to $(FOREMAN_BINDIR)
install-foreman: build-foreman
	install -d $(FOREMAN_BINDIR)
	install -m 0755 $(FOREMAN_BINARY) $(FOREMAN_BINDIR)/foreman

## uninstall: remove installed BroCode CLI
uninstall:
	rm -f $(BINDIR)/brocode

## uninstall-foreman: remove installed Foreman CLI
uninstall-foreman:
	rm -f $(FOREMAN_BINDIR)/foreman

## clean: remove build artifacts and coverage files
clean:
	rm -rf bin coverage.out *.test *.prof *.pprof *.trace

## help: list available targets
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'