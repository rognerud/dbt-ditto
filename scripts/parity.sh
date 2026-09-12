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

# The two tools learn a column's type by different routes, and only one of them
# reads a file that is in the repository. dbt-ditto reads the committed
# target/catalog.json; dbt-osmosis asks the adapter, which means the fixture has
# to exist as real tables in DuckDB. That file is generated and not committed,
# so on a clean checkout — CI, every time — it is simply absent, and DuckDB
# obligingly creates an empty database rather than failing. dbt-osmosis then
# finds no columns anywhere and writes bare `- name: <model>` scaffolding, and
# the diff reads as though dbt-ditto invented every column in the fixture.
if [[ ! -f "${DBT_DITTO_DB}" ]]; then
  echo "==> building the fixture warehouse"
  mkdir -p "${ROOT}/.gocache"

  # Only the warehouse is wanted from this. build-fixture.sh also rewrites the
  # committed target/manifest.json, and that must not survive, because dbt does
  # not order the nodes in a manifest deterministically: two parses of the same
  # project on the same machine can list two models in either order.
  #
  # It matters because selectNodes lays models out in manifest order, and when
  # several share one schema file that order is visible in the bytes. The
  # committed manifest is the one dbt-osmosis was recorded against, so keeping
  # it is what makes the comparison a comparison, rather than a coin toss
  # between two independent parses.
  SAVED="$(mktemp -d "${TMPDIR%/}/parity-artifacts.XXXXXX")"
  for project in platform analytics; do
    cp -R "${ROOT}/testdata/projects/${project}/target" "${SAVED}/${project}"
  done

  "${ROOT}/scripts/build-fixture.sh" >"${ROOT}/.gocache/parity-fixture.log" 2>&1 ||
    { cat "${ROOT}/.gocache/parity-fixture.log"; exit 1; }

  for project in platform analytics; do
    rm -rf "${ROOT}/testdata/projects/${project}/target"
    cp -R "${SAVED}/${project}" "${ROOT}/testdata/projects/${project}/target"
  done
  rm -rf "${SAVED}"
fi

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
#
# The replacement text spans two lines, and BSD sed will not take a literal
# newline in a `s###` replacement any other way than a backslash followed by
# one. Building the whole expression with $'...' keeps that escape inside a
# single quoting context, rather than stitching three of them together mid-word.
progenitor_off=$'s#^inheritance:$#inheritance:\\\n  progenitor: false#'
{
  sed -e 's#projects/#./#' -e "${progenitor_off}" "${ROOT}/testdata/dbt_ditto.yml"
  printf '\ncolumns:\n  comments: never\n\noutput:\n  comments: osmosis\n'
} >"${WORK}/dbt-ditto/dbt_ditto.yml"
GOFLAGS="${GOFLAGS:--mod=vendor}" GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}" \
  go run "${ROOT}/cmd/dbt-ditto" inherit \
  -c "${WORK}/dbt-ditto/dbt_ditto.yml" >"${WORK}/dbt-ditto.log" 2>&1 ||
  { cat "${WORK}/dbt-ditto.log"; exit 1; }

echo
echo "==> diff (platform schema YAML)"
# Both trees are copied and canonicalised first, so the diff does not depend on
# the order dbt happened to list its nodes in. Entries sharing a schema file are
# written in manifest order by both tools, but they learn that order by
# different routes: dbt-osmosis parses the project on every run, while dbt-ditto
# reads the committed target/manifest.json that the parse above was restored
# over. dbt makes no promise about that order, so it differs by machine — on the
# CI runner the platform seeds come out in the opposite order to the recorded
# manifest — and comparing it asserts something neither tool claims. Entry order
# within a file is proved separately, by the Go tests against the recorded
# manifest. The copies keep the uncanonicalised output for the golden refresh
# below.
rm -rf "${WORK}/canon"
mkdir -p "${WORK}/canon"
cp -R "${WORK}/osmosis/platform" "${WORK}/canon/osmosis"
cp -R "${WORK}/dbt-ditto/platform" "${WORK}/canon/dbt-ditto"
GOFLAGS="${GOFLAGS:--mod=vendor}" GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}" \
  go run "${ROOT}/scripts/canon" "${WORK}/canon/osmosis" "${WORK}/canon/dbt-ditto"

STATUS=0
diff -ru \
  --exclude=target --exclude=logs --exclude='*.sql' --exclude='*.csv' \
  --exclude='*.md' --exclude='.user.yml' --exclude='dbt_project.yml' \
  --exclude='profiles.yml' \
  "${WORK}/canon/osmosis" "${WORK}/canon/dbt-ditto" || STATUS=1

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
