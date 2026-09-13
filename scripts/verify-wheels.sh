#!/usr/bin/env bash
# Builds the PyPI wheels and proves they actually work.
#
# This exists because of a specific bug: the binary was shipping without its
# executable bit, so `uv pip install` worked and `pip install` produced
# "permission denied". Anything that only tests one installer will miss it, so
# this tests both, and asserts the mode rather than trusting it.
#
#   ./scripts/verify-wheels.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${ROOT}/.gocache/wheel-verify"
DIST="${ROOT}/dist/pypi"
VERSION="${VERSION:-0.0.0.dev0}"

export TMPDIR="${TMPDIR:-${ROOT}/.gocache/tmp}"
export UV_CACHE_DIR="${UV_CACHE_DIR:-${ROOT}/.gocache/uv-cache}"
export GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}"
export GOMODCACHE="${GOMODCACHE:-${ROOT}/.gocache/mod}"
mkdir -p "${TMPDIR}"

# shellcheck source=lib/uv.sh
source "${ROOT}/scripts/lib/uv.sh"

PYTHON="${ROOT}/.venv/bin/python3"
[[ -x "${PYTHON}" ]] || PYTHON="$(command -v python3)"

# The wheel that matches this machine, and one that cannot possibly match it.
case "$(uname -s)/$(uname -m)" in
  Darwin/arm64)  NATIVE_TAG="macosx_11_0_arm64" ;;
  Darwin/x86_64) NATIVE_TAG="macosx_10_9_x86_64" ;;
  Linux/x86_64)  NATIVE_TAG="manylinux_2_17_x86_64.manylinux2014_x86_64" ;;
  Linux/aarch64) NATIVE_TAG="manylinux_2_17_aarch64.manylinux2014_aarch64" ;;
  *) echo "error: no wheel tag known for $(uname -s)/$(uname -m)" >&2; exit 1 ;;
esac
FOREIGN_TAG="win_amd64"

rm -rf "${WORK}" "${DIST}"
mkdir -p "${WORK}"

echo "==> building wheels"
"${PYTHON}" "${ROOT}/packaging/pypi/build_wheels.py" --version "${VERSION}" --out "${DIST}"

NATIVE_WHEEL="${DIST}/dbt_ditto-${VERSION}-py3-none-${NATIVE_TAG}.whl"
FOREIGN_WHEEL="${DIST}/dbt_ditto-${VERSION}-py3-none-${FOREIGN_TAG}.whl"
[[ -f "${NATIVE_WHEEL}" ]] || { echo "missing ${NATIVE_WHEEL}" >&2; exit 1; }

fail() { echo "  FAIL: $*" >&2; exit 1; }

# mode prints a file's permission string.
mode() {
  stat -f '%Sp' "$1" 2>/dev/null || stat -c '%A' "$1"
}

# check_install <label> <venv> <install command...>
check_install() {
  local label="$1" venv="$2"; shift 2
  echo "==> ${label}"
  "$@" >"${WORK}/${label}.log" 2>&1 || { cat "${WORK}/${label}.log"; fail "${label}: install failed"; }

  local bin="${venv}/bin/dbt-ditto"
  [[ -f "${bin}" ]] || fail "${label}: no binary in ${venv}/bin"
  [[ -x "${bin}" ]] || fail "${label}: binary is not executable ($(mode "${bin}"))"

  local out
  out="$("${bin}" version 2>&1 | head -1)" || fail "${label}: binary did not run"
  echo "    ${out}  [$(mode "${bin}")]"
}

"${UV}" venv --seed "${WORK}/uv-venv" >/dev/null 2>&1
check_install "uv-install" "${WORK}/uv-venv" \
  "${UV}" pip install --python "${WORK}/uv-venv/bin/python" --quiet "${NATIVE_WHEEL}"

"${UV}" venv --seed "${WORK}/pip-venv" >/dev/null 2>&1
check_install "pip-install" "${WORK}/pip-venv" \
  "${WORK}/pip-venv/bin/pip" install --quiet --no-cache-dir "${NATIVE_WHEEL}"

echo "==> a wheel for another platform is refused"
if "${UV}" pip install --python "${WORK}/uv-venv/bin/python" "${FOREIGN_WHEEL}" >/dev/null 2>&1; then
  fail "the ${FOREIGN_TAG} wheel installed on $(uname -s)"
fi
echo "    refused, as it should be"

echo "==> resolving by name picks this platform's wheel"
"${UV}" pip install --python "${WORK}/uv-venv/bin/python" --reinstall --quiet \
  --no-index --find-links "${DIST}" dbt-ditto >"${WORK}/resolve.log" 2>&1 ||
  { cat "${WORK}/resolve.log"; fail "resolution by name failed"; }
"${WORK}/uv-venv/bin/dbt-ditto" version >/dev/null || fail "resolved wheel does not run"
echo "    resolved and runs"

echo "==> uninstall removes the binary (RECORD is correct)"
"${UV}" pip uninstall --python "${WORK}/uv-venv/bin/python" --quiet dbt-ditto >/dev/null 2>&1
[[ ! -e "${WORK}/uv-venv/bin/dbt-ditto" ]] || fail "the binary survived uninstall"
echo "    clean"

echo "==> twine check (the validation PyPI runs on upload)"
if "${WORK}/pip-venv/bin/pip" install --quiet --no-cache-dir twine >/dev/null 2>&1; then
  "${WORK}/pip-venv/bin/twine" check "${DIST}"/*.whl || fail "twine check rejected a wheel"
else
  echo "    skipped: twine could not be installed (no network?)"
fi

echo
echo "all wheels verified:"
ls -1 "${DIST}"
