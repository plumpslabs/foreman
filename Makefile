# Foreman build tooling.
#
# Build injects version/commit/date via ldflags (see internal/version).

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X github.com/plumpslabs/foreman/internal/version.Version=$(VERSION) -X github.com/plumpslabs/foreman/internal/version.Commit=$(COMMIT) -X github.com/plumpslabs/foreman/internal/version.Date=$(DATE)

BINARY := bin/foreman
BINDIR ?= $(shell $(GO) env GOPATH)/bin

.PHONY: all build web test race vet fmt fmt-check lint tidy check run install uninstall clean help

all: check build

## build: compile binary for current platform (builds web UI first if missing)
build: web
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/foreman

## web: build the embedded Preact dashboard into internal/foreman/web/dist
web:
	@if [ -f web/package.json ]; then \
		cd web && npm ci --no-audit --no-fund 2>/dev/null || npm install --no-audit --no-fund; \
		npm run build; \
	else \
		echo "web/package.json not found; skipping dashboard build"; \
	fi

## run: launch Foreman for the current project
run:
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

## coverage: run tests and open HTML coverage report
coverage:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

## install: build and install to $(BINDIR) (default: ~/go/bin)
install: build
	install -d $(BINDIR)
	install -m 0755 $(BINARY) $(BINDIR)/foreman

## uninstall: remove installed Foreman CLI
uninstall:
	rm -f $(BINDIR)/foreman

## clean: remove build artifacts and coverage files
clean:
	rm -rf bin coverage.out *.test *.prof *.pprof *.trace web/dist

## help: list available targets
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-12s\033[0m %s\n", $$1, $$2}'
