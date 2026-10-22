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
  Darwin/x86_64) NATIVE_TAG="macosx_11_0_x86_64" ;;
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
  # The wheel says one version and the binary it carries says another whenever
  # --version and the ldflags fall out of step, and nothing else notices: the
  # install succeeds and the user is told the wrong thing for the life of the
  # release.
  [[ "${out}" == *"${VERSION}"* ]] ||
    fail "${label}: wheel is ${VERSION} but the binary reports '${out}'"
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

echo "==> the same commit builds the same bytes"
# Reproducibility is the property that lets anybody check a published wheel
# against this repository. It is easy to lose by accident — a timestamp, the
# zip's create_system byte, Go's VCS stamping — and nothing else here would
# notice, since a non-reproducible wheel installs and runs perfectly.
"${PYTHON}" "${ROOT}/packaging/pypi/build_wheels.py" --version "${VERSION}" \
  --out "${WORK}/rebuild" >/dev/null
for wheel in "${DIST}"/*.whl; do
  name="$(basename "${wheel}")"
  a="$(shasum -a 256 "${wheel}" | cut -d' ' -f1)"
  b="$(shasum -a 256 "${WORK}/rebuild/${name}" | cut -d' ' -f1)"
  [[ "${a}" == "${b}" ]] || fail "${name} is not reproducible (${a} vs ${b})"
done
echo "    all $(find "${DIST}" -name '*.whl' | wc -l | tr -d ' ') wheels rebuilt byte-identical"

echo "==> every wheel's metadata agrees with its filename"
"${PYTHON}" - "${DIST}" "${VERSION}" <<'PY' || fail "metadata disagrees with the wheel"
import sys, zipfile
from pathlib import Path

dist, version = Path(sys.argv[1]), sys.argv[2]
bad = []
for wheel in sorted(dist.glob("*.whl")):
    # dist_name-version-pytag-abitag-platformtag.whl
    stem = wheel.name[: -len(".whl")].split("-")
    tag_from_name = "-".join(stem[2:])
    with zipfile.ZipFile(wheel) as zf:
        names = zf.namelist()
        meta = dict(
            line.split(": ", 1)
            for line in zf.read(f"dbt_ditto-{version}.dist-info/METADATA")
            .decode()
            .split("\n\n", 1)[0]
            .splitlines()
            if ": " in line
        )
        wheel_md = dict(
            line.split(": ", 1)
            for line in zf.read(f"dbt_ditto-{version}.dist-info/WHEEL")
            .decode()
            .splitlines()
            if ": " in line
        )
    if meta.get("Version") != version:
        bad.append(f"{wheel.name}: METADATA Version is {meta.get('Version')!r}")
    if wheel_md.get("Tag") != tag_from_name:
        bad.append(f"{wheel.name}: WHEEL Tag {wheel_md.get('Tag')!r} != filename tag")
    # License-File must name a file that is actually in dist-info/licenses/.
    licence = meta.get("License-File")
    if not licence:
        bad.append(f"{wheel.name}: no License-File")
    elif f"dbt_ditto-{version}.dist-info/licenses/{licence}" not in names:
        bad.append(f"{wheel.name}: License-File {licence!r} is not in the archive")
    if not any(n.endswith((".data/scripts/dbt-ditto", ".data/scripts/dbt-ditto.exe"))
               for n in names):
        bad.append(f"{wheel.name}: carries no binary under .data/scripts/")

for problem in bad:
    print(f"  {problem}", file=sys.stderr)
raise SystemExit(1 if bad else 0)
PY
echo "    filename, WHEEL tag, METADATA and payload all agree"

echo "==> the wheel and the release archive carry the same binary"
# Two scripts build the same release. If they stamp it differently, `dbt-ditto
# version` says different things depending on how it was installed, and neither
# artefact can be checked against the other.
DIST_TARGET="$(go env GOOS)/$(go env GOARCH)"
DIST="${WORK}/archives" TARGETS="${DIST_TARGET}" KEEP_STAGE=1 VERSION="${VERSION}" \
  "${ROOT}/scripts/dist.sh" >"${WORK}/dist.log" 2>&1 ||
  { cat "${WORK}/dist.log"; fail "dist.sh failed"; }

ARCHIVE_BIN="$(find "${WORK}/archives" -name dbt-ditto -type f | head -1)"
[[ -n "${ARCHIVE_BIN}" ]] || fail "dist.sh left no binary to compare"
unzip -o -q "${NATIVE_WHEEL}" -d "${WORK}/unpacked"
WHEEL_BIN="$(find "${WORK}/unpacked" -name dbt-ditto -type f | head -1)"
a="$(shasum -a 256 "${ARCHIVE_BIN}" | cut -d' ' -f1)"
b="$(shasum -a 256 "${WHEEL_BIN}" | cut -d' ' -f1)"
[[ "${a}" == "${b}" ]] ||
  fail "the archive and the wheel carry different binaries (${a} vs ${b})"
echo "    identical (${a:0:12}) for ${DIST_TARGET}"

echo "==> twine check (the validation PyPI runs on upload)"
if "${WORK}/pip-venv/bin/pip" install --quiet --no-cache-dir twine >/dev/null 2>&1; then
  "${WORK}/pip-venv/bin/twine" check "${DIST}"/*.whl || fail "twine check rejected a wheel"
else
  echo "    skipped: twine could not be installed (no network?)"
fi

echo
echo "all wheels verified:"
ls -1 "${DIST}"
