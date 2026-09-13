#!/usr/bin/env bash
# Rebuilds testdata/matrix/artifacts by running real dbt, once per version.
# Needs Python, uv, dbt and a warehouse; the Go tests that read the artifacts
# need none of it. See testdata/matrix/README.md for why each row exists.
#
#   ./scripts/matrix.sh                        # every version in the matrix
#   ./scripts/matrix.sh 1.9.8:1.9.3            # just this dbt-core:dbt-duckdb pair
#   KEEP_ENVS=1 ./scripts/matrix.sh            # keep the throwaway venvs to debug

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT="${ROOT}/testdata/matrix/project"
ARTIFACTS="${ROOT}/testdata/matrix/artifacts"
WORK="${ROOT}/.gocache/matrix"

export TMPDIR="${TMPDIR:-${ROOT}/.gocache/tmp}"
export UV_CACHE_DIR="${UV_CACHE_DIR:-${ROOT}/.gocache/uv-cache}"
mkdir -p "${TMPDIR}"

# dbt writes logs and profile state under $HOME, so each row below redirects it to a
# scratch directory.
REAL_HOME="${HOME}"

# Entries are `dbt-core:dbt-duckdb:python`.
VERSIONS=(
  "1.8.9:1.8.4:3.11"
  "1.9.0:1.9.0:3.11"
  "1.9.8:1.9.3:3.11"
  "1.10.11:1.10.1:3.12"
  "local"
)
if [[ $# -gt 0 ]]; then
  VERSIONS=("$@")
fi

# shellcheck source=lib/uv.sh
source "${ROOT}/scripts/lib/uv.sh"

built=()
skipped=()

# make_env builds a throwaway environment: dbt pins dbt-core hard, so no two
# rows can share one. Usage: make_env ENV_DIR PYTHON LOG PACKAGE...
make_env() {
  local env_dir=$1 python=$2 log=$3
  shift 3
  "${UV}" venv --python "${python}" "${env_dir}" >>"${log}" 2>&1 &&
    "${UV}" pip install --python "${env_dir}/bin/python" --quiet "$@" >>"${log}" 2>&1
}

# scratch_home redirects $HOME for a row and returns its run directory, freshly
# copied from the named fixture. Usage: scratch_home SUFFIX FIXTURE RUN_DIR
scratch_home() {
  rm -rf "$3"
  cp -R "$2" "$3"
  export HOME="${WORK}/home-$1"
  mkdir -p "${HOME}"
}

# start_row reads the `core:adapter:python` entry into the per-row variables every
# section below uses, and prints the header.
# Usage: start_row ADAPTER ID_PREFIX DIR_PREFIX [NOTE]
start_row() {
  IFS=: read -r core adapter python <<<"${entry}"
  python="${python:-3.12}"
  id="$2-core${core}"
  env_dir="${WORK}/env-$3${core}"
  run_dir="${WORK}/run-$3${core}"
  log="${WORK}/${id}.log"
  printf '==> dbt-core %s with dbt-%s %s on Python %s%s\n' \
    "${core}" "$1" "${adapter}" "${python}" "${4:-}"
}

# skip_row records a row that could not be built, and points at its log.
# Usage: skip_row DETAIL TAG
skip_row() {
  echo "    skipped: $1 (see ${log})"
  skipped+=("${entry} ($2)")
}

meta_field() { # ENV_DIR MANIFEST FIELD
  "$1/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"][sys.argv[2]])' "$2" "$3"
}

# capture commits one row's artifacts and reports what produced them, rather than
# trusting the directory name: the adapter version requested is not always the
# dbt-core version used.
capture() {
  local out=$1 env_dir=$2 manifest=$3 catalog=$4 adapter=$5 adapter_version=$6
  shift 6
  rm -rf "${out}"
  mkdir -p "${out}"
  gzip -9 -c "${manifest}" >"${out}/manifest.json.gz"
  gzip -9 -c "${catalog}" >"${out}/catalog.json.gz"

  local dbt_version schema_version
  dbt_version="$(meta_field "${env_dir}" "${manifest}" dbt_version)"
  schema_version="$(meta_field "${env_dir}" "${manifest}" dbt_schema_version)"
  "${env_dir}/bin/python" "${ROOT}/scripts/lib/write_meta.py" "${out}/meta.json" \
    "${adapter}" "${adapter_version}" "${dbt_version}" "${schema_version}" "$@"

  printf '    dbt-core %-10s schema %-8s %s\n' "${dbt_version}" \
    "$(basename "${schema_version}" .json)" \
    "$(du -kh "${out}" | tail -1 | awk '{print $1}')"
}

dbt_build() { # ENV_DIR RUN_DIR
  cd "$2"
  DBT_PROFILES_DIR="$2" "$1/bin/dbt" seed --quiet
  DBT_PROFILES_DIR="$2" "$1/bin/dbt" run --quiet
  DBT_PROFILES_DIR="$2" "$1/bin/dbt" docs generate --quiet
}

rm -rf "${WORK}"
mkdir -p "${WORK}"

for entry in "${VERSIONS[@]}"; do
  if [[ "${entry}" == "local" ]]; then
    env_dir="${ROOT}/.venv"
    if [[ ! -x "${env_dir}/bin/dbt" ]]; then
      echo "==> local environment: skipped, ${env_dir} has no dbt (run 'uv sync')"
      skipped+=("local (no dbt)")
      continue
    fi
    core="$("${env_dir}/bin/python" -c \
      'import importlib.metadata as m; print(m.version("dbt-core"))')"
    adapter="$("${env_dir}/bin/python" -c \
      'import importlib.metadata as m; print(m.version("dbt-duckdb"))')"
    printf '==> local environment: dbt-core %s with dbt-duckdb %s\n' "${core}" "${adapter}"
    id="duckdb-core${core}"
    run_dir="${WORK}/run-${core}"
    log="${WORK}/${id}.log"
  else
    start_row duckdb duckdb ""
  fi

  if [[ "${entry}" != "local" ]] &&
     ! make_env "${env_dir}" "${python}" "${log}" "dbt-core==${core}" "dbt-duckdb==${adapter}"; then
    skip_row "could not install" install
    continue
  fi

  scratch_home "${core}" "${PROJECT}" "${run_dir}"
  export DBT_DITTO_MATRIX_DB="${run_dir}/matrix.duckdb"

  if ! (dbt_build "${env_dir}" "${run_dir}") >>"${log}" 2>&1; then
    skip_row "dbt failed" dbt
    continue
  fi

  capture "${ARTIFACTS}/${id}" "${env_dir}" "${run_dir}/target/manifest.json" \
    "${run_dir}/target/catalog.json" duckdb "${adapter}"
  built+=("${entry}")

  # Never delete the repository's own environment.
  if [[ "${entry}" != "local" && -z "${KEEP_ENVS:-}" ]]; then
    rm -rf "${env_dir}"
  fi
done

# --- Snowflake, with no Snowflake -------------------------------------------
# A real dbt-snowflake run against fakesnow: see scripts/lib/dbt_fakesnow.py.
SNOWFLAKE=("1.10.11:1.10.2:3.12")
if [[ -n "${SKIP_SNOWFLAKE:-}" ]]; then
  SNOWFLAKE=()
fi

for entry in ${SNOWFLAKE[@]+"${SNOWFLAKE[@]}"}; do
  start_row snowflake snowflake sf- " (via fakesnow)"

  if ! make_env "${env_dir}" "${python}" "${log}" \
       "dbt-core==${core}" "dbt-snowflake==${adapter}" fakesnow; then
    skip_row "could not install" "snowflake install"
    continue
  fi

  scratch_home "sf-${core}" "${ROOT}/testdata/matrix/snowflake" "${run_dir}"

  if ! (
    cd "${run_dir}"
    for cmd in "seed" "run" "docs generate"; do
      # shellcheck disable=SC2086
      DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/python" \
        "${ROOT}/scripts/lib/dbt_fakesnow.py" "${WORK}/sf-db-${core}" ${cmd} --quiet
    done
  ) >>"${log}" 2>&1; then
    skip_row "dbt failed" "snowflake dbt"
    continue
  fi

  capture "${ARTIFACTS}/${id}" "${env_dir}" "${run_dir}/target/manifest.json" \
    "${run_dir}/target/catalog.json" snowflake "${adapter}" "emulated_by=fakesnow"
  built+=("${entry} (snowflake)")

  [[ -n "${KEEP_ENVS:-}" ]] || rm -rf "${env_dir}"
done

# --- Postgres, in a container ------------------------------------------------
# Skipped rather than failed wherever Docker is not running.
POSTGRES=("1.10.11:1.10.0:3.12")
if [[ -n "${SKIP_POSTGRES:-}" ]]; then
  POSTGRES=()
fi

PG_CONTAINER="dbt-ditto-matrix-pg"
PG_PORT="${DBT_DITTO_PG_PORT:-55432}"

# Point the client straight at the daemon's socket.
if [[ -z "${DOCKER_HOST:-}" ]]; then
  for candidate in \
      "${REAL_HOME}/.colima/default/docker.sock" \
      "${REAL_HOME}/.docker/run/docker.sock" \
      /var/run/docker.sock; do
    if [[ -S "${candidate}" ]]; then
      export DOCKER_HOST="unix://${candidate}"
      break
    fi
  done
fi

stop_postgres() {
  docker rm -f "${PG_CONTAINER}" >/dev/null 2>&1 || true
}

if [[ ${#POSTGRES[@]} -gt 0 ]] && ! docker info >/dev/null 2>&1; then
  echo "==> Postgres: skipped, no Docker daemon (try: colima start)"
  skipped+=("postgres (no docker)")
  POSTGRES=()
fi

for entry in ${POSTGRES[@]+"${POSTGRES[@]}"}; do
  start_row postgres postgres pg- " (in Docker)"

  if ! make_env "${env_dir}" "${python}" "${log}" \
       "dbt-core==${core}" "dbt-postgres==${adapter}"; then
    skip_row "could not install" "postgres install"
    continue
  fi

  stop_postgres
  trap stop_postgres EXIT
  if ! docker run -d --rm --name "${PG_CONTAINER}" \
       -e POSTGRES_USER=dbt-ditto -e POSTGRES_PASSWORD=dbt-ditto \
       -e POSTGRES_DB=dbt-ditto \
       -p "${PG_PORT}:5432" postgres:16-alpine >>"${log}" 2>&1; then
    skip_row "could not start the container" "postgres container"
    continue
  fi

  # Postgres accepts connections before it is ready to serve them.
  ready=0
  for _ in $(seq 60); do
    if docker exec "${PG_CONTAINER}" pg_isready -U dbt-ditto -d dbt-ditto >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 1
  done
  if [[ ${ready} -eq 0 ]]; then
    skip_row "Postgres never became ready" "postgres ready"
    stop_postgres
    continue
  fi

  scratch_home "pg-${core}" "${ROOT}/testdata/matrix/postgres" "${run_dir}"
  export DBT_DITTO_PG_HOST=127.0.0.1
  export DBT_DITTO_PG_PORT="${PG_PORT}"

  if ! (dbt_build "${env_dir}" "${run_dir}") >>"${log}" 2>&1; then
    skip_row "dbt failed" "postgres dbt"
    stop_postgres
    continue
  fi

  capture "${ARTIFACTS}/${id}" "${env_dir}" "${run_dir}/target/manifest.json" \
    "${run_dir}/target/catalog.json" postgres "${adapter}"
  built+=("${entry} (postgres)")

  stop_postgres
  trap - EXIT
  [[ -n "${KEEP_ENVS:-}" ]] || rm -rf "${env_dir}"
done

# --- BigQuery, from a parse rather than a run --------------------------------
# `dbt parse` needs no warehouse, so the manifest is genuinely dbt-bigquery's
# work while the catalog is hand-built. meta.json records the split; see
# testdata/matrix/README.md for why a run is not possible.
BIGQUERY=("1.10.11:1.10.1:3.12")
if [[ -n "${SKIP_BIGQUERY:-}" ]]; then
  BIGQUERY=()
fi

for entry in ${BIGQUERY[@]+"${BIGQUERY[@]}"}; do
  start_row bigquery bigquery bq- " (parse only)"

  if ! make_env "${env_dir}" "${python}" "${log}" \
       "dbt-core==${core}" "dbt-bigquery==${adapter}"; then
    skip_row "could not install" "bigquery install"
    continue
  fi

  scratch_home "bq-${core}" "${ROOT}/testdata/matrix/bigquery" "${run_dir}"

  # The endpoint is never reached: parsing does not open a connection. It is
  # passed so the client can be constructed without hunting for credentials.
  if ! (
    cd "${run_dir}"
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/python" \
      "${ROOT}/scripts/lib/dbt_fakebq.py" http://127.0.0.1:9050 parse --quiet
  ) >>"${log}" 2>&1; then
    skip_row "dbt parse failed" "bigquery parse"
    continue
  fi

  capture "${ARTIFACTS}/${id}" "${env_dir}" "${run_dir}/target/manifest.json" \
    "${ROOT}/testdata/matrix/bigquery/catalog.json" bigquery "${adapter}" \
    "manifest_from=dbt parse" \
    "catalog_from=hand-built, see testdata/matrix/bigquery/catalog.json"
  built+=("${entry} (bigquery)")

  [[ -n "${KEEP_ENVS:-}" ]] || rm -rf "${env_dir}"
done

echo
echo "captured: ${#built[@]} version(s) into ${ARTIFACTS}"
if [[ ${#skipped[@]} -gt 0 ]]; then
  echo "skipped:  ${skipped[*]}"
fi
echo
echo "Run 'go test ./internal/runner/ -run Matrix' to test against them."
