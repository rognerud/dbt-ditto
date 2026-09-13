#!/usr/bin/env bash
# Runs a command with the Go caches pointed inside the repository.
#
#   ./scripts/go-env.sh go vet ./...
#
# The Makefile exports these itself; the git hooks cannot. lefthook 2 accepts
# `env` only on an individual job, does not expand `{root}` inside one, and Go
# rejects a relative GOCACHE — so the hooks call this instead, and nothing a hook
# runs writes to state outside the working tree.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Set rather than defaulted, as the Makefile sets them: TMPDIR in particular is
# already set by the OS, so honouring an inherited value would mean the hooks
# never used the repository's own.
export GOCACHE="${ROOT}/.gocache/go-build"
export GOMODCACHE="${ROOT}/.gocache/mod"
export TMPDIR="${ROOT}/.gocache/tmp"
mkdir -p "${TMPDIR}"

exec "$@"
