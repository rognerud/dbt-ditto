#!/usr/bin/env bash
# Materialises the two-project dbt fixture into a DuckDB file and generates the
# manifest.json / catalog.json that dbt-ditto reads.
#
# Run from the repository root:  ./scripts/build-fixture.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DBT="${ROOT}/.venv/bin/dbt"
FIXTURE="${ROOT}/testdata/projects"

export DBT_DITTO_DB="${ROOT}/testdata/warehouse.duckdb"
mkdir -p "${ROOT}/.dbt"

if [[ ! -x "${DBT}" ]]; then
  echo "error: ${DBT} not found; run 'uv sync' first" >&2
  exit 1
fi

rm -f "${DBT_DITTO_DB}"

build_project() {
  local name="$1"
  local dir="${FIXTURE}/${name}"

  # Seeds first and separately: sources point at the seeded tables, so they must
  # exist in the warehouse before any model that reads through a source runs.
  if [[ -d "${dir}/seeds" ]]; then
    echo "==> ${name}: seed"
    ( cd "${dir}" && DBT_PROFILES_DIR="${dir}" "${DBT}" seed --quiet )
  fi

  echo "==> ${name}: run"
  ( cd "${dir}" && DBT_PROFILES_DIR="${dir}" "${DBT}" run --quiet )

  echo "==> ${name}: docs generate"
  ( cd "${dir}" && DBT_PROFILES_DIR="${dir}" "${DBT}" docs generate --quiet )
}

# platform first: analytics reads its manifest through dbt-loom.
build_project platform
build_project analytics

echo
echo "warehouse: ${DBT_DITTO_DB}"
for p in platform analytics; do
  n=0
  for artifact in manifest catalog; do
    [[ -f "${FIXTURE}/${p}/target/${artifact}.json" ]] && n=$((n + 1))
  done
  echo "  ${p}: ${n} artifacts"
done
