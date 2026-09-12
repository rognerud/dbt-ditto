#!/usr/bin/env bash
# Times dbt-ditto against the real dbt-osmosis on the same project.
#
# Both tools are given identical, freshly restored copies of the fixture and are
# asked to do the same job. Each is run several times and the best wall clock is
# reported, so a cold page cache does not decide the result.
#
#   ./scripts/bench.sh          # 3 runs each
#   ./scripts/bench.sh 10       # 10 runs each

set -euo pipefail

RUNS="${1:-3}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${ROOT}/.gocache/bench"
OSMOSIS="${ROOT}/.venv/bin/dbt-osmosis"
BIN="${ROOT}/bin/dbt-ditto"

export DBT_DITTO_DB="${ROOT}/testdata/warehouse.duckdb"
export TMPDIR="${TMPDIR:-${ROOT}/.gocache/tmp}"
mkdir -p "${TMPDIR}"

GOFLAGS="${GOFLAGS:--mod=vendor}" GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}" \
  go build -o "${BIN}" "${ROOT}/cmd/dbt-ditto"

rm -rf "${WORK}"
mkdir -p "${WORK}/home"
export HOME="${WORK}/home"

# py runs Python quietly: `date +%s.%N` does not exist on macOS, so the clock
# and the arithmetic go through Python, and the system interpreter is noisy on
# stderr when it cannot reach its cache.
py() {
  if [[ -x "${ROOT}/.venv/bin/python3" ]]; then
    "${ROOT}/.venv/bin/python3" "$@" 2>/dev/null
  else
    /usr/bin/python3 "$@" 2>/dev/null
  fi
}

# seconds prints a wall clock reading with millisecond resolution.
seconds() { py -c 'import time; print(f"{time.time():.4f}")'; }

# best runs a command RUNS times against a fresh copy of the fixture and prints
# the shortest wall clock in seconds.
best() {
  local label="$1"; shift
  local fastest=""
  for _ in $(seq "${RUNS}"); do
    rm -rf "${WORK}/run"
    cp -R "${ROOT}/testdata/projects" "${WORK}/run"
    sed 's#projects/#./#' "${ROOT}/testdata/dbt_ditto.yml" >"${WORK}/run/dbt_ditto.yml"

    local start end elapsed
    start="$(seconds)"
    "$@" >"${WORK}/${label}.log" 2>&1 || {
      echo "  ${label}: FAILED (see ${WORK}/${label}.log)"
      return 1
    }
    end="$(seconds)"
    elapsed="$(py -c "print(f'{${end} - ${start}:.3f}')")"
    if [[ -z "${fastest}" ]] || (( $(py -c "print(1 if ${elapsed} < ${fastest} else 0)") )); then
      fastest="${elapsed}"
    fi
  done
  printf '%s' "${fastest}"
}

run_osmosis() {
  cd "${WORK}/run/platform"
  DBT_PROFILES_DIR="${PWD}" "${OSMOSIS}" yaml refactor \
    --auto-apply --project-dir "${PWD}" --profiles-dir "${PWD}"
}

run_dbt_ditto_platform() {
  "${BIN}" inherit "${WORK}/run/platform"
}

run_dbt_ditto_both() {
  "${BIN}" inherit -c "${WORK}/run/dbt_ditto.yml"
}

echo "dbt-ditto vs dbt-osmosis, best of ${RUNS} on testdata/projects"
echo

if [[ -x "${OSMOSIS}" ]]; then
  OSMOSIS_TIME="$(best osmosis run_osmosis)"
  printf '  dbt-osmosis   (platform)        %8ss\n' "${OSMOSIS_TIME}"
else
  OSMOSIS_TIME=""
  echo "  dbt-osmosis   not installed, skipping"
fi

LOOM_TIME="$(best dbt-ditto run_dbt_ditto_platform)"
printf '  dbt-ditto    (platform)        %8ss\n' "${LOOM_TIME}"

BOTH_TIME="$(best dbt-ditto-both run_dbt_ditto_both)"
printf '  dbt-ditto    (both projects)   %8ss\n' "${BOTH_TIME}"

if [[ -n "${OSMOSIS_TIME}" ]]; then
  echo
  py -c "print(f'  dbt-ditto is {${OSMOSIS_TIME} / ${LOOM_TIME}:.0f}x faster on the same project')"
fi

echo
echo "Scaling (synthetic projects, Go benchmarks):"
GOFLAGS="${GOFLAGS:--mod=vendor}" GOCACHE="${GOCACHE:-${ROOT}/.gocache/go-build}" \
  go test -run '^$' -bench 'BenchmarkRun$|BenchmarkRunNoOp' -benchtime 5x \
  "${ROOT}/internal/runner" | grep -E '^Benchmark'
