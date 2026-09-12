#!/usr/bin/env bash
# Proves that dbt-ditto reproduces dbt-osmosis byte for byte.
#
# Two pristine copies of testdata/projects are made. The real dbt-osmosis is run
# over one, dbt-ditto over the other, and the resulting schema YAML is diffed.
# The dbt-osmosis side is also copied into testdata/golden/osmosis so `go test`
# can assert the same thing without needing Python.
#
#   ./scripts/parity.sh            # run both, diff, refresh the golden files
#   ./scripts/parity.sh --check    # run both, diff, fail if they differ

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${ROOT}/.gocache/parity"
GOLDEN="${ROOT}/testdata/golden/osmosis"
OSMOSIS="${ROOT}/.venv/bin/dbt-osmosis"
CHECK=0
[[ "${1:-}" == "--check" ]] && CHECK=1

if [[ ! -x "${OSMOSIS}" ]]; then
  echo "error: ${OSMOSIS} not found; run 'uv sync' first" >&2
  exit 1
fi

export DBT_DITTO_DB="${ROOT}/testdata/warehouse.duckdb"
export TMPDIR="${TMPDIR:-${ROOT}/.gocache/tmp}"
mkdir -p "${TMPDIR}"

rm -rf "${WORK}"
mkdir -p "${WORK}/home"
cp -R "${ROOT}/testdata/projects" "${WORK}/osmosis"
cp -R "${ROOT}/testdata/projects" "${WORK}/dbt-ditto"

# dbt-osmosis insists on writing logs under $HOME.
export HOME="${WORK}/home"

echo "==> dbt-osmosis: platform"
(
  cd "${WORK}/osmosis/platform"
  DBT_PROFILES_DIR="${PWD}" "${OSMOSIS}" yaml refactor \
    --auto-apply --project-dir "${PWD}" --profiles-dir "${PWD}" \
    >"${WORK}/osmosis-platform.log" 2>&1
) || { echo "dbt-osmosis failed; see ${WORK}/osmosis-platform.log" >&2; exit 1; }

# dbt-osmosis cannot run against the cross-project (dbt-loom) project at all: the
# two plugins are incompatible. Record the failure as evidence rather than
# pretending the comparison is possible.
echo "==> dbt-osmosis: analytics (expected to fail: dbt-loom incompatibility)"
set +e
(
  cd "${WORK}/osmosis/analytics"
  DBT_PROFILES_DIR="${PWD}" DBT_LOOM_CONFIG="${PWD}/dbt_loom.config.yml" \
    "${OSMOSIS}" yaml refactor --auto-apply --project-dir "${PWD}" --profiles-dir "${PWD}" \
    >"${WORK}/osmosis-analytics.log" 2>&1
)
OSMOSIS_ANALYTICS_RC=$?
set -e
echo "    exit code ${OSMOSIS_ANALYTICS_RC}"

echo "==> dbt-ditto: both projects"
# The fixture config lives outside the project tree, so point a copy of it at
# the scratch copies.
# Two settings are turned off so the comparison is like for like:
#
#   output.comments: osmosis
#     reproduces dbt-osmosis' loss of comments inside a column list, which
#     dbt-ditto otherwise keeps.
#
#   columns.comments: never
#     stops dbt-ditto reading the warehouse's own column comments. Both tools
#     use them, but by different routes: dbt-osmosis asks the adapter, and the
#     DuckDB adapter does not report comments, while catalog.json does. On an
#     adapter that reports them, such as Snowflake, the two agree.
#
#   inheritance.progenitor: false
#     dbt-ditto records where each inherited description came from and
#     dbt-osmosis does not, so the annotation is turned off to compare like for
#     like. It is inserted into the existing `inheritance:` block rather than
#     appended, since a second top-level `inheritance:` key would be a duplicate.
{
  sed -e 's#projects/#./#' \
      -e 's#^inheritance:$#inheritance:\'$'\n''  progenitor: false#' \
      "${ROOT}/testdata/dbt_ditto.yml"
  printf '\ncolumns:\n  comments: never\n\noutput:\n  comments: osmosis\n'
} >"${WORK}/dbt-ditto/dbt_ditto.yml"
GOFLAGS="${GOFLAGS:--mod=vendor}" GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}" \
  go run "${ROOT}/cmd/dbt-ditto" inherit \
  -c "${WORK}/dbt-ditto/dbt_ditto.yml" >"${WORK}/dbt-ditto.log" 2>&1 ||
  { cat "${WORK}/dbt-ditto.log"; exit 1; }

echo
echo "==> diff (platform schema YAML)"
STATUS=0
diff -ru \
  --exclude=target --exclude=logs --exclude='*.sql' --exclude='*.csv' \
  --exclude='*.md' --exclude='.user.yml' --exclude='dbt_project.yml' \
  --exclude='profiles.yml' \
  "${WORK}/osmosis/platform" "${WORK}/dbt-ditto/platform" || STATUS=1

if [[ ${STATUS} -eq 0 ]]; then
  echo "    identical"
fi

if [[ ${CHECK} -eq 1 ]]; then
  exit ${STATUS}
fi

echo
echo "==> refreshing ${GOLDEN}"
rm -rf "${GOLDEN}"
mkdir -p "${GOLDEN}"
( cd "${WORK}/osmosis/platform" && find . -name '*.yml' -not -path './target/*' -not -path './logs/*' \
    -not -name 'profiles.yml' -not -name '.user.yml' -not -name 'dbt_project.yml' \
    -print0 | cpio -pd0m --quiet "${GOLDEN}" )
cp "${WORK}/osmosis-analytics.log" "${GOLDEN}/../osmosis-analytics-failure.log"

# The cross-project project has no dbt-osmosis reference to compare against, so
# dbt-ditto' own output is recorded instead. Regenerate it with default
# settings rather than the osmosis comment compatibility mode.
LOOM_GOLDEN="${ROOT}/testdata/golden/dbt-ditto"
echo "==> refreshing ${LOOM_GOLDEN}"
rm -rf "${WORK}/default" "${LOOM_GOLDEN}"
cp -R "${ROOT}/testdata/projects" "${WORK}/default"
sed 's#projects/#./#' "${ROOT}/testdata/dbt_ditto.yml" >"${WORK}/default/dbt_ditto.yml"
GOFLAGS="${GOFLAGS:--mod=vendor}" GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}" \
  go run "${ROOT}/cmd/dbt-ditto" inherit -c "${WORK}/default/dbt_ditto.yml" >/dev/null
mkdir -p "${LOOM_GOLDEN}/analytics"
( cd "${WORK}/default/analytics" && find . -name '*.yml' -not -path './target/*' -not -path './logs/*' \
    -not -name 'profiles.yml' -not -name '.user.yml' -not -name 'dbt_project.yml' \
    -not -name 'dbt_loom.config.yml' \
    -print0 | cpio -pd0m --quiet "${LOOM_GOLDEN}/analytics" )
echo "    done"
exit ${STATUS}
