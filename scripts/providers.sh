#!/usr/bin/env bash
# Runs the source providers' offline tests.
#
# Neither BigQuery nor Snowflake can be reached from a test run, and neither
# needs to be: what is checked here is the part that breaks silently — the
# contract, profiles.yml resolution, and the type rendering that has to agree
# with what each dbt adapter writes into catalog.json.
#
# That dbt-ditto and a provider agree about the wire format is proved in Go, by
# TestPythonProviderRoundTrip, which runs packaging/providers/echo.py through
# the real pipeline on every `go test`.
#
#   ./scripts/providers.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROVIDERS="${ROOT}/packaging/providers"

# The repository's own environment first: the shared module needs PyYAML to read
# profiles.yml, and a bare system python usually has not got it.
if [[ -x "${ROOT}/.venv/bin/python" ]]; then
  PYTHON="${ROOT}/.venv/bin/python"
elif command -v python3 >/dev/null 2>&1; then
  PYTHON="python3"
else
  echo "no python3 found; skipping the provider tests" >&2
  exit 0
fi

echo "==> provider tests (${PYTHON})"
"${PYTHON}" "${PROVIDERS}/test_providers.py"

echo "==> syntax"
for f in "${PROVIDERS}"/*.py; do
  "${PYTHON}" -m py_compile "${f}"
done

echo "==> ok"
