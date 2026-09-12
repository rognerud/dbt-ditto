#!/usr/bin/env bash
# Checks that everything claiming to be "the version" agrees.
#
# Three files carry a version string by hand and nothing forces them to match,
# so the first release would ship a wheel called 0.2.0 next to a dbt package
# still calling itself 0.1.0. This is the thing that stops that.
#
#   ./scripts/check-versions.sh           # against the current tag
#   ./scripts/check-versions.sh v0.2.0    # against a tag being prepared
#
# The wheel takes its version from the git tag (packaging/pypi/build_wheels.py),
# which is why the tag is the authority here rather than pyproject.toml.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

TAG="${1:-}"
if [[ -z "${TAG}" ]]; then
  TAG="$(git -C "${ROOT}" describe --tags --exact-match 2>/dev/null || true)"
fi
if [[ -z "${TAG}" ]]; then
  echo "error: no tag given and HEAD is not tagged" >&2
  echo "usage: $0 [vX.Y.Z]" >&2
  exit 2
fi

# `v0.2.0` and `0.2.0` are the same release; the hub accepts either spelling.
want="${TAG#v}"

status=0
check() {
  local what="$1" got="$2"
  if [[ "${got}" == "${want}" ]]; then
    printf '    ok: %-34s %s\n' "${what}" "${got}"
  else
    printf '    FAIL: %-32s %s (want %s)\n' "${what}" "${got:-<none>}" "${want}" >&2
    status=1
  fi
}

echo "==> versions against tag ${TAG}"

check "pyproject.toml" \
  "$(grep -m1 '^version *= *' "${ROOT}/pyproject.toml" | sed 's/.*"\(.*\)".*/\1/')"

check "packaging/dbt-ditto/dbt_project.yml" \
  "$(grep -m1 '^version:' "${ROOT}/packaging/dbt-ditto/dbt_project.yml" | sed 's/.*"\(.*\)".*/\1/')"

# The wheel builder normalises the tag itself (v0.2.0 → 0.2.0, and a PEP 440
# error for anything it cannot make sense of), so asking it is the real check
# that this tag can be published at all.
wheel_version="$(python3 - "${ROOT}" "${TAG}" <<'PY'
import runpy, sys
root, tag = sys.argv[1:3]
module = runpy.run_path(f"{root}/packaging/pypi/build_wheels.py")
print(module["normalise_version"](tag))
PY
)"
check "wheel version from the tag" "${wheel_version}"

if [[ ${status} -eq 0 ]]; then
  echo "==> versions agree"
fi
exit ${status}
