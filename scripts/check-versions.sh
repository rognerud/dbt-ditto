#!/usr/bin/env bash
# Checks that a tag can actually be released.
#
#   ./scripts/check-versions.sh           # against the current tag
#   ./scripts/check-versions.sh v0.2.0    # against a tag being prepared
#
# The tag is the only thing in this repository that states a version: the
# wheels take it from there (packaging/pypi/build_wheels.py) and pyproject.toml
# holds a permanent 0.0.0 placeholder it is not compared against. So there is
# nothing left to keep in agreement, and the one question worth asking before a
# release builds is whether PyPI would accept the name this tag implies.
#
# That question has a real answer: PEP 440. `v0.2.0` is fine, `v0.2` is fine,
# `release-2` is not, and a tag PyPI rejects is one that cannot be taken back.

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

echo "==> checking tag ${TAG}"

# The wheel builder normalises the tag itself (v0.2.0 → 0.2.0, and a PEP 440
# error for anything it cannot make sense of), so asking it is the real check
# rather than a second opinion about what a version looks like.
wheel_version="$(python3 - "${ROOT}" "${TAG}" <<'PY'
import runpy, sys
root, tag = sys.argv[1:3]
module = runpy.run_path(f"{root}/packaging/pypi/build_wheels.py")
print(module["normalise_version"](tag))
PY
)"

printf '    ok: %-34s %s\n' "wheel version from the tag" "${wheel_version}"
echo "==> ${TAG} is releasable"
