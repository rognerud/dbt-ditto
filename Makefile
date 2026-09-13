# The build cache, the module cache and temporary files are kept inside the
# repository, so nothing depends on writable state elsewhere. Dependencies are
# resolved against go.sum rather than vendored: the module cache below is
# populated once and every later build reads it without reaching the network.
export GOCACHE    := $(CURDIR)/.gocache/go-build
export GOMODCACHE := $(CURDIR)/.gocache/mod
export TMPDIR     := $(CURDIR)/.gocache/tmp

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse -q --verify HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: all build test test-short features docs race vet doctor lint lint-fix vuln actionlint tidy-check scan audit tools bench parity parity-check fixture matrix matrix-test providers hooks dist wheels verify-wheels clean help

all: vet test build

## build: compile the binary into bin/dbt-ditto
build: | $(TMPDIR)
	go build -ldflags "$(LDFLAGS)" -o bin/dbt-ditto ./cmd/dbt-ditto

## test: run the whole test suite, including the recorded dbt-osmosis parity proof
test: | $(TMPDIR)
	go test ./...

## features: run the behaviour specifications in features/, with their output
features: | $(TMPDIR)
	go test ./internal/features/ -run TestFeatures -v

## docs: run the Gherkin in docs/usage.md and docs/configuration.md
docs: | $(TMPDIR)
	go test ./internal/features/ -run TestDocumentationIsTrue -v

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

# Linters and scanners are installed into .gocache/tools, like every other piece
# of state this repository needs, so nothing is required to be on the PATH and
# nothing is written outside the working tree.
TOOLS := $(CURDIR)/.gocache/tools
export PATH := $(TOOLS):$(PATH)

GOLANGCI_VERSION  := v2.13.2
GOVULNCHECK_VERSION := latest
ACTIONLINT_VERSION  := latest
TRIVY_VERSION       := latest

$(TOOLS)/golangci-lint: | $(TMPDIR)
	GOBIN=$(TOOLS) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

$(TOOLS)/govulncheck: | $(TMPDIR)
	GOBIN=$(TOOLS) go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

$(TOOLS)/actionlint: | $(TMPDIR)
	GOBIN=$(TOOLS) go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

# Trivy needs encoding/json/v2, which is behind a build tag in Go 1.26.
$(TOOLS)/trivy: | $(TMPDIR)
	GOBIN=$(TOOLS) GOEXPERIMENT=jsonv2 go install github.com/aquasecurity/trivy/cmd/trivy@$(TRIVY_VERSION)

## tools: install the linters and scanners into .gocache/tools
tools: $(TOOLS)/golangci-lint $(TOOLS)/govulncheck $(TOOLS)/actionlint $(TOOLS)/trivy

## doctor: report which optional tools are present, and what is not run without them
# Every check that can be skipped is skipped silently, so that a missing linter
# never blocks a commit. This is the one place that says out loud what that
# costs, because "0 issues" and "never ran" otherwise look identical.
doctor:
	@printf '%-16s %-10s %s\n' TOOL STATE 'WHAT IS NOT CHECKED WITHOUT IT'
	@$(call probe,golangci-lint,make lint and the lefthook pre-push lint job)
	@$(call probe,govulncheck,make vuln and the lefthook pre-push vulnerability job)
	@$(call probe,actionlint,make actionlint: the GitHub workflows)
	@$(call probe,trivy,make scan: dependency CVEs in uv.lock plus secrets and misconfiguration)
	@$(call probe,shellcheck,the lefthook pre-commit shellcheck job)
	@$(call probe,uv,make providers and make parity and make matrix)
	@$(call probe,dbt-osmosis,make parity against the real dbt-osmosis and make bench)
	@$(call probe,lefthook,every git hook: run make hooks)
	@echo
	@echo 'Missing Go tools: make tools. shellcheck: brew install shellcheck.'
	@echo 'CI runs all of them regardless, so a gap here is local only.'

# probe prints one row: the tool, whether it can be run, and what goes unchecked.
define probe
	if command -v $(1) >/dev/null 2>&1; then \
		printf '%-16s %-10s %s\n' '$(1)' 'ok' ''; \
	else \
		printf '%-16s %-10s %s\n' '$(1)' 'MISSING' '$(2)'; \
	fi
endef

## lint: golangci-lint over everything, configured by .golangci.yml
lint: $(TOOLS)/golangci-lint
	$(TOOLS)/golangci-lint run ./...

## lint-fix: the same, applying the fixes it knows how to make
lint-fix: $(TOOLS)/golangci-lint
	$(TOOLS)/golangci-lint run --fix ./...

## vuln: report known vulnerabilities this code actually reaches
vuln: $(TOOLS)/govulncheck
	$(TOOLS)/govulncheck ./...

## actionlint: check the GitHub workflows
actionlint: $(TOOLS)/actionlint
	$(TOOLS)/actionlint

## tidy-check: fail if go.mod or go.sum is not what the source needs
tidy-check: | $(TMPDIR)
	go mod tidy
	git diff --exit-code go.mod go.sum
	go mod verify

## scan: Trivy over the working tree: dependencies, secrets and misconfiguration
# `make tools` installs trivy, and a system one on the PATH is used if there is
# one. Skipped rather than failed when absent — `make doctor` is what says so —
# because `make audit` is still worth running without it.
scan:
	@if command -v trivy >/dev/null 2>&1; then \
		trivy fs --scanners vuln,secret,misconfig \
			--severity CRITICAL,HIGH,MEDIUM --ignore-unfixed --exit-code 1 \
			--skip-dirs .gocache,.venv,dist,bin,testdata . ; \
	else \
		echo "trivy not installed, skipping (brew install trivy)"; \
	fi

## audit: everything CI checks that is not a test — lint, vulnerabilities, modules, workflows
audit: doctor lint vuln tidy-check actionlint scan

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
	rm -rf bin dist .gocache/parity .gocache/bench .gocache/dist .gocache/tools

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
