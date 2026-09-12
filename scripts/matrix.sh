#!/usr/bin/env bash
# Rebuilds testdata/matrix/artifacts by running real dbt, once per version.
#
# The rig is split in two on purpose:
#
#   this script          needs Python, dbt and a warehouse. Run rarely.
#   go test ./...        needs neither. Runs everywhere, on every commit.
#
# What crosses the line between them is a set of committed artifacts: the
# manifest.json and catalog.json each dbt version writes. dbt-ditto reads
# nothing else, so a captured pair is a complete, faithful record of what that
# version looks like — and the Go suite can then hold every version at once
# without anybody needing dbt installed.
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

# dbt writes logs and profile state under $HOME, so each row below redirects it
# to a scratch directory. Anything that needs the real one — finding the Docker
# socket, most of all — has to have taken a copy first.
REAL_HOME="${HOME}"

# Entries are `dbt-core:dbt-duckdb`. Both are pinned, and that matters: an
# adapter's requirement is only `dbt-core>=1.9,<2`, so installing dbt-duckdb 1.9
# on its own happily resolves dbt-core 1.12. It is dbt-core's version that
# decides the shape of the artifacts, so leaving it to the resolver would mean
# the matrix silently tested the same thing several times.
#
# The chosen versions straddle the boundaries that change the artifacts:
#
#   1.8.x   before column-level `config:` existed at all
#   1.9.0   column config exists, but below the 1.9.6 cutover
#   1.9.8   past the cutover: meta belongs inside `config:`
#   1.10.x  current stable
#   1.12.x  latest
# The Python version is pinned too, and has to be: uv installs the newest
# interpreter it can by default, and dbt 1.8 and 1.9 do not run on 3.13 or 3.14
# — they fail deep inside mashumaro at import time, which is a confusing way to
# discover that the matrix row was never really tested.
VERSIONS=(
  "1.8.9:1.8.4:3.11"
  "1.9.0:1.9.0:3.11"
  "1.9.8:1.9.3:3.11"
  "1.10.11:1.10.1:3.12"
  # `local` captures whatever the repository's own .venv pins, without building
  # an environment. It covers the version the parity proof is written against,
  # and rescues rows whose dependencies will not build here.
  "local"
)
if [[ $# -gt 0 ]]; then
  VERSIONS=("$@")
fi

# Locate uv by running it: a sandbox may allow exec while denying stat on the
# directory holding it, which makes `command -v` lie.
find_uv() {
  local candidate
  for candidate in "${UV:-}" uv "${HOME}/.local/bin/uv" /opt/homebrew/bin/uv \
                   /usr/local/bin/uv "${HOME}/.cargo/bin/uv"; do
    [[ -n "${candidate}" ]] || continue
    if "${candidate}" --version >/dev/null 2>&1; then
      printf '%s' "${candidate}"
      return 0
    fi
  done
  return 1
}
UV="$(find_uv)" || {
  echo "error: uv not found; install it (https://docs.astral.sh/uv/) or set UV=/path/to/uv" >&2
  exit 1
}

rm -rf "${WORK}"
mkdir -p "${WORK}"

built=()
skipped=()

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
  else
    IFS=: read -r core adapter python <<<"${entry}"
    python="${python:-3.12}"
    env_dir="${WORK}/env-${core}"
    printf '==> dbt-core %s with dbt-duckdb %s on Python %s\n' "${core}" "${adapter}" "${python}"
  fi

  version="${core}"
  id="duckdb-core${core}"
  run_dir="${WORK}/run-${core}"
  log="${WORK}/${id}.log"

  # Each version gets a throwaway environment. dbt pins dbt-core hard, so they
  # cannot share one.
  if [[ "${entry}" != "local" ]] &&
     { ! "${UV}" venv --python "${python}" "${env_dir}" >>"${log}" 2>&1 ||
       ! "${UV}" pip install --python "${env_dir}/bin/python" --quiet \
           "dbt-core==${core}" "dbt-duckdb==${adapter}" >>"${log}" 2>&1; }; then
    echo "    skipped: could not install (see ${log})"
    skipped+=("${entry} (install)")
    continue
  fi

  rm -rf "${run_dir}"
  cp -R "${PROJECT}" "${run_dir}"
  export DBT_DITTO_MATRIX_DB="${run_dir}/matrix.duckdb"

  # dbt writes logs and profile state under $HOME; keep it in the scratch dir.
  export HOME="${WORK}/home-${version}"
  mkdir -p "${HOME}"

  if ! (
    cd "${run_dir}"
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/dbt" seed --quiet
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/dbt" run --quiet
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/dbt" docs generate --quiet
  ) >>"${log}" 2>&1; then
    echo "    skipped: dbt failed (see ${log})"
    skipped+=("${entry} (dbt)")
    continue
  fi

  out="${ARTIFACTS}/${id}"
  rm -rf "${out}"
  mkdir -p "${out}"

  # Committed gzipped: a manifest is mostly macro definitions dbt-ditto never
  # reads, and both readers understand `.gz`.
  gzip -9 -c "${run_dir}/target/manifest.json" >"${out}/manifest.json.gz"
  gzip -9 -c "${run_dir}/target/catalog.json" >"${out}/catalog.json.gz"

  # Record what actually produced these, rather than trusting the directory
  # name: the adapter version requested is not always the dbt-core version used.
  dbt_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_version"])' \
    "${run_dir}/target/manifest.json")"
  schema_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_schema_version"])' \
    "${run_dir}/target/manifest.json")"

  "${env_dir}/bin/python" - "${out}/meta.json" "${adapter}" "${dbt_version}" "${schema_version}" <<'PY'
import json, sys
path, adapter_version, dbt_version, schema_version = sys.argv[1:5]
with open(path, "w") as fh:
    json.dump(
        {
            "adapter": "duckdb",
            "adapter_version": adapter_version,
            "dbt_version": dbt_version,
            "dbt_schema_version": schema_version,
        },
        fh,
        indent=2,
    )
    fh.write("\n")
PY

  size="$(du -kh "${out}" | tail -1 | awk '{print $1}')"
  printf '    dbt-core %-10s schema %-8s %s\n' "${dbt_version}" \
    "$(basename "${schema_version}" .json)" "${size}"
  built+=("${entry}")

  # Never delete the repository's own environment.
  if [[ "${entry}" != "local" && -z "${KEEP_ENVS:-}" ]]; then
    rm -rf "${env_dir}"
  fi
done

# --- Snowflake, with no Snowflake -------------------------------------------
#
# fakesnow replaces `snowflake.connector` with an implementation backed by
# DuckDB, and dbt-snowflake talks to Snowflake through exactly that connector.
# Patching it before dbt starts therefore gives a real dbt run — real adapter,
# real macros, real `docs generate` — with no account, no credentials and no
# network. The artifacts are genuinely Snowflake-shaped: upper-cased
# identifiers, and NUMBER/TEXT/FLOAT types.
SNOWFLAKE=("1.10.11:1.10.2:3.12")
if [[ -n "${SKIP_SNOWFLAKE:-}" ]]; then
  SNOWFLAKE=()
fi

for entry in ${SNOWFLAKE[@]+"${SNOWFLAKE[@]}"}; do
  IFS=: read -r core adapter python <<<"${entry}"
  id="snowflake-core${core}"
  env_dir="${WORK}/env-sf-${core}"
  run_dir="${WORK}/run-sf-${core}"
  log="${WORK}/${id}.log"

  printf '==> dbt-core %s with dbt-snowflake %s on Python %s (via fakesnow)\n' \
    "${core}" "${adapter}" "${python}"

  if ! "${UV}" venv --python "${python}" "${env_dir}" >>"${log}" 2>&1 ||
     ! "${UV}" pip install --python "${env_dir}/bin/python" --quiet \
         "dbt-core==${core}" "dbt-snowflake==${adapter}" fakesnow >>"${log}" 2>&1; then
    echo "    skipped: could not install (see ${log})"
    skipped+=("${entry} (snowflake install)")
    continue
  fi

  rm -rf "${run_dir}"
  cp -R "${ROOT}/testdata/matrix/snowflake" "${run_dir}"
  export HOME="${WORK}/home-sf-${core}"
  mkdir -p "${HOME}"

  if ! (
    cd "${run_dir}"
    for cmd in "seed" "run" "docs generate"; do
      # shellcheck disable=SC2086
      DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/python" \
        "${ROOT}/scripts/lib/dbt_fakesnow.py" "${WORK}/sf-db-${core}" ${cmd} --quiet
    done
  ) >>"${log}" 2>&1; then
    echo "    skipped: dbt failed (see ${log})"
    skipped+=("${entry} (snowflake dbt)")
    continue
  fi

  out="${ARTIFACTS}/${id}"
  rm -rf "${out}"
  mkdir -p "${out}"
  gzip -9 -c "${run_dir}/target/manifest.json" >"${out}/manifest.json.gz"
  gzip -9 -c "${run_dir}/target/catalog.json" >"${out}/catalog.json.gz"

  dbt_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_version"])' \
    "${run_dir}/target/manifest.json")"
  schema_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_schema_version"])' \
    "${run_dir}/target/manifest.json")"

  "${env_dir}/bin/python" - "${out}/meta.json" "${adapter}" "${dbt_version}" "${schema_version}" <<'PY'
import json, sys
path, adapter_version, dbt_version, schema_version = sys.argv[1:5]
with open(path, "w") as fh:
    json.dump(
        {
            "adapter": "snowflake",
            "adapter_version": adapter_version,
            "dbt_version": dbt_version,
            "dbt_schema_version": schema_version,
            "emulated_by": "fakesnow",
        },
        fh,
        indent=2,
    )
    fh.write("\n")
PY

  printf '    dbt-core %-10s schema %-8s %s\n' "${dbt_version}" \
    "$(basename "${schema_version}" .json)" \
    "$(du -kh "${out}" | tail -1 | awk '{print $1}')"
  built+=("${entry} (snowflake)")

  [[ -n "${KEEP_ENVS:-}" ]] || rm -rf "${env_dir}"
done

# --- Postgres, in a container ------------------------------------------------
#
# dbt's reference adapter, with its own catalog query, its own type names
# (`integer`, `character varying`) and real COMMENT ON support. It needs a
# container, so it is skipped wherever Docker is not running rather than failing
# the whole run.
POSTGRES=("1.10.11:1.10.0:3.12")
if [[ -n "${SKIP_POSTGRES:-}" ]]; then
  POSTGRES=()
fi

PG_CONTAINER="dbt-ditto-matrix-pg"
PG_PORT="${DBT_DITTO_PG_PORT:-55432}"

# Point the client straight at the daemon's socket. Relying on the default
# means relying on `~/.docker/contexts`, and on a Colima install the socket is
# not at /var/run/docker.sock anyway; naming it explicitly also keeps the script
# working when $HOME has been redirected, which it is below.
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
  IFS=: read -r core adapter python <<<"${entry}"
  id="postgres-core${core}"
  env_dir="${WORK}/env-pg-${core}"
  run_dir="${WORK}/run-pg-${core}"
  log="${WORK}/${id}.log"

  printf '==> dbt-core %s with dbt-postgres %s on Python %s (in Docker)\n' \
    "${core}" "${adapter}" "${python}"

  if ! "${UV}" venv --python "${python}" "${env_dir}" >>"${log}" 2>&1 ||
     ! "${UV}" pip install --python "${env_dir}/bin/python" --quiet \
         "dbt-core==${core}" "dbt-postgres==${adapter}" >>"${log}" 2>&1; then
    echo "    skipped: could not install (see ${log})"
    skipped+=("${entry} (postgres install)")
    continue
  fi

  stop_postgres
  trap stop_postgres EXIT
  if ! docker run -d --rm --name "${PG_CONTAINER}" \
       -e POSTGRES_USER=dbt-ditto -e POSTGRES_PASSWORD=dbt-ditto \
       -e POSTGRES_DB=dbt-ditto \
       -p "${PG_PORT}:5432" postgres:16-alpine >>"${log}" 2>&1; then
    echo "    skipped: could not start the container (see ${log})"
    skipped+=("${entry} (postgres container)")
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
    echo "    skipped: Postgres never became ready (see ${log})"
    skipped+=("${entry} (postgres ready)")
    stop_postgres
    continue
  fi

  rm -rf "${run_dir}"
  cp -R "${ROOT}/testdata/matrix/postgres" "${run_dir}"
  export HOME="${WORK}/home-pg-${core}"
  export DBT_DITTO_PG_HOST=127.0.0.1
  export DBT_DITTO_PG_PORT="${PG_PORT}"
  mkdir -p "${HOME}"

  if ! (
    cd "${run_dir}"
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/dbt" seed --quiet
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/dbt" run --quiet
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/dbt" docs generate --quiet
  ) >>"${log}" 2>&1; then
    echo "    skipped: dbt failed (see ${log})"
    skipped+=("${entry} (postgres dbt)")
    stop_postgres
    continue
  fi

  out="${ARTIFACTS}/${id}"
  rm -rf "${out}"
  mkdir -p "${out}"
  gzip -9 -c "${run_dir}/target/manifest.json" >"${out}/manifest.json.gz"
  gzip -9 -c "${run_dir}/target/catalog.json" >"${out}/catalog.json.gz"

  dbt_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_version"])' \
    "${run_dir}/target/manifest.json")"
  schema_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_schema_version"])' \
    "${run_dir}/target/manifest.json")"

  "${env_dir}/bin/python" - "${out}/meta.json" "${adapter}" "${dbt_version}" "${schema_version}" <<'PY'
import json, sys
path, adapter_version, dbt_version, schema_version = sys.argv[1:5]
with open(path, "w") as fh:
    json.dump(
        {
            "adapter": "postgres",
            "adapter_version": adapter_version,
            "dbt_version": dbt_version,
            "dbt_schema_version": schema_version,
        },
        fh,
        indent=2,
    )
    fh.write("\n")
PY

  printf '    dbt-core %-10s schema %-8s %s\n' "${dbt_version}" \
    "$(basename "${schema_version}" .json)" \
    "$(du -kh "${out}" | tail -1 | awk '{print $1}')"
  built+=("${entry} (postgres)")

  stop_postgres
  trap - EXIT
  [[ -n "${KEEP_ENVS:-}" ]] || rm -rf "${env_dir}"
done

# --- BigQuery, from a parse rather than a run --------------------------------
#
# BigQuery has no usable local stand-in. bigquery-emulator gets far enough to
# accept a connection but not to run dbt: it has no load-job support, so seeds
# fail outright, and `dbt run` dies in the adapter with
# `NoneType object has no attribute path` because the job resources it returns
# are incomplete. scripts/lib/dbt_fakebq.py is kept for when that improves.
#
# `dbt parse` needs no warehouse at all, though, and it is dbt-bigquery that
# writes the manifest. So the manifest here is genuinely the adapter own work
# while the catalog is the hand-built one committed alongside the project. That
# is an honest split, and it is recorded in meta.json.
#
# The deep BigQuery coverage -- nested RECORDs, ARRAY<STRUCT<...>>, policy tags
# -- lives in testdata/bigquery, which is hand-built from the adapter source.
BIGQUERY=("1.10.11:1.10.1:3.12")
if [[ -n "${SKIP_BIGQUERY:-}" ]]; then
  BIGQUERY=()
fi

for entry in ${BIGQUERY[@]+"${BIGQUERY[@]}"}; do
  IFS=: read -r core adapter python <<<"${entry}"
  id="bigquery-core${core}"
  env_dir="${WORK}/env-bq-${core}"
  run_dir="${WORK}/run-bq-${core}"
  log="${WORK}/${id}.log"

  printf '==> dbt-core %s with dbt-bigquery %s on Python %s (parse only)\n' \
    "${core}" "${adapter}" "${python}"

  if ! "${UV}" venv --python "${python}" "${env_dir}" >>"${log}" 2>&1 ||
     ! "${UV}" pip install --python "${env_dir}/bin/python" --quiet \
         "dbt-core==${core}" "dbt-bigquery==${adapter}" >>"${log}" 2>&1; then
    echo "    skipped: could not install (see ${log})"
    skipped+=("${entry} (bigquery install)")
    continue
  fi

  rm -rf "${run_dir}"
  cp -R "${ROOT}/testdata/matrix/bigquery" "${run_dir}"
  export HOME="${WORK}/home-bq-${core}"
  mkdir -p "${HOME}"

  # The endpoint is never reached: parsing does not open a connection. It is
  # passed so the client can be constructed without hunting for credentials.
  if ! (
    cd "${run_dir}"
    DBT_PROFILES_DIR="${run_dir}" "${env_dir}/bin/python" \
      "${ROOT}/scripts/lib/dbt_fakebq.py" http://127.0.0.1:9050 parse --quiet
  ) >>"${log}" 2>&1; then
    echo "    skipped: dbt parse failed (see ${log})"
    skipped+=("${entry} (bigquery parse)")
    continue
  fi

  out="${ARTIFACTS}/${id}"
  rm -rf "${out}"
  mkdir -p "${out}"
  gzip -9 -c "${run_dir}/target/manifest.json" >"${out}/manifest.json.gz"
  gzip -9 -c "${ROOT}/testdata/matrix/bigquery/catalog.json" >"${out}/catalog.json.gz"

  dbt_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_version"])' \
    "${run_dir}/target/manifest.json")"
  schema_version="$("${env_dir}/bin/python" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["metadata"]["dbt_schema_version"])' \
    "${run_dir}/target/manifest.json")"

  "${env_dir}/bin/python" - "${out}/meta.json" "${adapter}" "${dbt_version}" "${schema_version}" <<'PY'
import json, sys
path, adapter_version, dbt_version, schema_version = sys.argv[1:5]
with open(path, "w") as fh:
    json.dump(
        {
            "adapter": "bigquery",
            "adapter_version": adapter_version,
            "dbt_version": dbt_version,
            "dbt_schema_version": schema_version,
            "manifest_from": "dbt parse",
            "catalog_from": "hand-built, see testdata/matrix/bigquery/catalog.json",
        },
        fh,
        indent=2,
    )
    fh.write("\n")
PY

  printf '    dbt-core %-10s schema %-8s %s\n' "${dbt_version}" \
    "$(basename "${schema_version}" .json)" \
    "$(du -kh "${out}" | tail -1 | awk '{print $1}')"
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
