# All Go work runs vendored, with the build cache and temporary files kept
# inside the repository so nothing depends on writable state elsewhere.
export GOFLAGS := -mod=vendor
export GOCACHE := $(CURDIR)/.gocache/go-build
export TMPDIR  := $(CURDIR)/.gocache/tmp

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse -q --verify HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: all build test test-short race vet bench parity parity-check fixture matrix matrix-test dbt-package providers hooks dist wheels verify-wheels clean help

all: vet test build

## build: compile the binary into bin/dbt-ditto
build: | $(TMPDIR)
	go build -ldflags "$(LDFLAGS)" -o bin/dbt-ditto ./cmd/dbt-ditto

## test: run the whole test suite, including the recorded dbt-osmosis parity proof
test: | $(TMPDIR)
	go test ./...

## test-short: the quick subset the pre-commit hook runs
test-short: | $(TMPDIR)
	go test -short ./internal/...

## hooks: install the git hooks in lefthook.yml
hooks:
	lefthook install

## race: run the test suite under the race detector
race: | $(TMPDIR)
	go test -race ./...

## vet: static checks
vet: | $(TMPDIR)
	go vet ./...

## bench: time dbt-ditto against the real dbt-osmosis, then scale up synthetically
bench: | $(TMPDIR)
	./scripts/bench.sh

## parity: run dbt-osmosis and dbt-ditto over the fixture, diff them, refresh the golden files
parity: | $(TMPDIR)
	./scripts/parity.sh

## parity-check: same, but fail instead of refreshing the golden files
parity-check: | $(TMPDIR)
	./scripts/parity.sh --check

## fixture: rebuild the DuckDB warehouse and the dbt artifacts under testdata
fixture:
	./scripts/build-fixture.sh

## matrix: re-capture artifacts by running real dbt once per version (needs uv)
matrix:
	./scripts/matrix.sh

## matrix-test: run every captured dbt version against the pipeline (Go only)
matrix-test: | $(TMPDIR)
	go test ./internal/runner/ -run Matrix -v

## dbt-package: install packaging/dbt-ditto into the fixture with dbt deps and run its macros
dbt-package:
	./scripts/dbt-package.sh

## providers: run the source providers' offline tests (no warehouse account needed)
providers:
	./scripts/providers.sh

## dist: cross-compile static release archives and checksums into dist/
dist: | $(TMPDIR)
	VERSION=$(VERSION) ./scripts/dist.sh

## wheels: build the PyPI wheels that carry the binary, into dist/pypi/
wheels: | $(TMPDIR)
	python3 packaging/pypi/build_wheels.py $(if $(WHEEL_VERSION),--version $(WHEEL_VERSION),)

## verify-wheels: build the wheels and prove they install and run under both pip and uv
verify-wheels: | $(TMPDIR)
	./scripts/verify-wheels.sh

$(TMPDIR):
	@mkdir -p $(TMPDIR)

clean:
	rm -rf bin dist .gocache/parity .gocache/bench .gocache/dist .gocache/dbt-package

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
