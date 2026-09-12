#!/usr/bin/env bash
# Cross-compiles release archives into dist/.
#
# dbt-ditto has no cgo and two pure-Go dependencies, so every target is a single
# static binary that runs on a bare machine: no libc version to match, no Python,
# no dbt. That is the whole reason distribution is easy, and CGO_ENABLED=0 below
# is what keeps it true.
#
#   ./scripts/dist.sh            # version from `git describe`
#   VERSION=v0.2.0 ./scripts/dist.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="${ROOT}/dist"
VERSION="${VERSION:-$(git -C "${ROOT}" describe --tags --always --dirty 2>/dev/null || echo dev)}"
# `rev-parse HEAD` prints "HEAD" on a repository with no commits yet, so verify.
COMMIT="$(git -C "${ROOT}" rev-parse -q --verify HEAD 2>/dev/null || echo unknown)"
# Honour SOURCE_DATE_EPOCH so a rebuild of the same commit produces the same
# bytes, which is what lets anyone verify a published binary.
DATE="$(date -u -r "${SOURCE_DATE_EPOCH:-$(date +%s)}" +%Y-%m-%dT%H:%M:%SZ)"

export GOFLAGS="${GOFLAGS:--mod=vendor}"
export GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}"
export TMPDIR="${TMPDIR:-${ROOT}/.gocache/tmp}"
export CGO_ENABLED=0
mkdir -p "${TMPDIR}"

TARGETS=(
  darwin/arm64
  darwin/amd64
  linux/amd64
  linux/arm64
  windows/amd64
  windows/arm64
)

LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"

rm -rf "${DIST}"
mkdir -p "${DIST}"

echo "dbt-ditto ${VERSION} (${COMMIT})"
for target in "${TARGETS[@]}"; do
  os="${target%/*}"
  arch="${target#*/}"
  ext=""
  [[ "${os}" == windows ]] && ext=".exe"

  name="dbt-ditto_${VERSION}_${os}_${arch}"
  stage="${DIST}/${name}"
  mkdir -p "${stage}"

  GOOS="${os}" GOARCH="${arch}" go build \
    -trimpath -ldflags "${LDFLAGS}" \
    -o "${stage}/dbt-ditto${ext}" "${ROOT}/cmd/dbt-ditto"

  cp "${ROOT}/README.md" "${stage}/"
  [[ -f "${ROOT}/LICENSE" ]] && cp "${ROOT}/LICENSE" "${stage}/"

  ( cd "${DIST}"
    if [[ "${os}" == windows ]]; then
      zip -qr "${name}.zip" "${name}"
    else
      tar -czf "${name}.tar.gz" "${name}"
    fi )
  rm -rf "${stage}"

  # du rather than `ls -lh`: it reports the size directly instead of a column of
  # a listing, and -h is understood by both the BSD and GNU versions.
  printf '  %-24s %s\n' "${target}" \
    "$(du -h "${DIST}/${name}".* | awk '{print $1}')"
done

(
  cd "${DIST}" || exit 1
  # Missing archives are tolerated because not every run builds every target.
  shasum -a 256 ./*.tar.gz ./*.zip >checksums.txt 2>/dev/null || true
)
echo
echo "archives and checksums in ${DIST}"
