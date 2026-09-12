#!/usr/bin/env bash
# Proves the dbt package in packaging/dbt-ditto works inside a real dbt project.
#
# The macros are the one part of dbt-ditto that Go cannot test: they run inside
# dbt's own Jinja, against dbt's own `graph` object. So this installs the package
# into a scratch copy of the platform fixture with `dbt deps`, runs the
# run-operations, and checks what they printed.
#
#   ./scripts/dbt-package.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${ROOT}/.gocache/dbt-package"
DBT="${ROOT}/.venv/bin/dbt"

if [[ ! -x "${DBT}" ]]; then
  echo "error: ${DBT} not found; run 'uv sync' first" >&2
  exit 1
fi

export DBT_DITTO_DB="${ROOT}/testdata/warehouse.duckdb"
export TMPDIR="${TMPDIR:-${ROOT}/.gocache/tmp}"
mkdir -p "${TMPDIR}"

rm -rf "${WORK}"
mkdir -p "${WORK}/home"
cp -R "${ROOT}/testdata/projects/platform" "${WORK}/project"
cp -R "${ROOT}/packaging/dbt-ditto" "${WORK}/package"

# dbt writes logs and profile state under $HOME.
export HOME="${WORK}/home"

# A local package is installed by path, which is the same code path `dbt deps`
# takes for a git or hub package once it has fetched it.
printf 'packages:\n  - local: ../package\n' >"${WORK}/project/packages.yml"

# Two columns that exercise the directive: one that resolves, one that cannot.
# They are appended to the end of dim_customers' column list.
cat >>"${WORK}/project/models/marts/_dim_customers.yml" <<'YAML'
      - name: renamed_signup_country
        description: "Inherited: raw_customers.signup_country"
      - name: broken_pointer
        description: "Inherited: raw_customers.no_such_column"
YAML

cd "${WORK}/project"
export DBT_PROFILES_DIR="${PWD}"

echo "==> dbt deps"
"${DBT}" deps >"${WORK}/deps.log" 2>&1 || { cat "${WORK}/deps.log"; exit 1; }

echo "==> dbt run-operation dbt_ditto_suggest"
"${DBT}" run-operation dbt_ditto_suggest >"${WORK}/suggest.log" 2>&1 ||
  { cat "${WORK}/suggest.log"; exit 1; }

echo "==> dbt run-operation dbt_ditto_install"
"${DBT}" run-operation dbt_ditto_install --args '{select: "stg_*", dry_run: true}' \
  >"${WORK}/install.log" 2>&1 || { cat "${WORK}/install.log"; exit 1; }

status=0
expect() {
  local what="$1" file="$2" pattern="$3"
  if grep -qF -- "${pattern}" "${file}"; then
    echo "    ok: ${what}"
  else
    echo "    FAIL: ${what} (no ${pattern} in ${file})" >&2
    status=1
  fi
}

echo "==> checking what the macros printed"
expect "inherits an undocumented column" "${WORK}/suggest.log" \
  "osmosis_progenitor: seed.platform.raw_customers"
expect "follows a directive" "${WORK}/suggest.log" \
  "name: renamed_signup_country"
expect "reports an unresolvable directive" "${WORK}/suggest.log" \
  "has no column 'no_such_column'"
expect "prints the launcher command" "${WORK}/install.log" \
  "python dbt_packages/dbt_ditto/run.py"
expect "passes flags through to the launcher" "${WORK}/install.log" "--dry-run"

# The launcher itself: with no binary on PATH and no uvx, it must say how to
# install rather than fail obscurely.
echo "==> run.py without a binary"
if PATH="/usr/bin:/bin" python3 "${WORK}/project/dbt_packages/dbt_ditto/run.py" --check \
     >"${WORK}/launcher.log" 2>&1; then
  echo "    FAIL: launcher exited 0 with nothing to run" >&2
  status=1
else
  expect "launcher explains how to install" "${WORK}/launcher.log" "uv tool install dbt-ditto"
fi

# And with one: the project has no dbt_ditto.yml, so the launcher has to name
# the project directory itself or the run dies with "no dbt_ditto.yml found".
echo "==> run.py with the binary on PATH"
(
  cd "${ROOT}"
  GOFLAGS="${GOFLAGS:--mod=vendor}" GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}" \
    go build -o "${WORK}/bin/dbt-ditto" ./cmd/dbt-ditto
) >"${WORK}/build.log" 2>&1 || { cat "${WORK}/build.log"; exit 1; }

PATH="${WORK}/bin:${PATH}" python3 "${WORK}/project/dbt_packages/dbt_ditto/run.py" \
  --dry-run --verbose >"${WORK}/run.log" 2>&1 ||
  { cat "${WORK}/run.log"; exit 1; }

expect "launcher runs the binary against this project" "${WORK}/run.log" "nodes scanned"
expect "launcher writes nothing on --dry-run" "${WORK}/run.log" "would write"
expect "the binary follows the same directive" "${WORK}/run.log" \
  "renamed_signup_country"
expect "the binary reports the fixture's ambiguous column" "${WORK}/run.log" \
  "documented differently by"

if [[ ${status} -eq 0 ]]; then
  echo "==> dbt package OK"
fi
exit ${status}
