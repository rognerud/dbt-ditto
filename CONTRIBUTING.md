# Contributing to dbt-ditto

Build, test, prove and release. Internals: [AGENTS.md](AGENTS.md). User
documentation: [docs/](docs/usage.md).

## Make targets

```sh
make                 # vet, test, build
make test            # includes the recorded parity proof
make features        # the Gherkin specifications, with output
make docs            # the Gherkin inside docs/, with output
make race            # the suite under the race detector
make parity          # run real dbt-osmosis, refresh the golden files
make bench           # head-to-head timing, then synthetic scaling
make fixture         # rebuild the DuckDB warehouse and artifacts
make matrix          # re-capture artifacts, once per dbt version
make matrix-test     # replay every captured version and adapter
make providers       # the providers' offline tests
make hooks           # install the lefthook git hooks
make dist            # cross-compile release archives
make wheels          # build the PyPI wheels
make verify-wheels   # build them, prove they install under pip and uv
```

`make parity`, `bench`, `fixture`, `providers` and the wheel targets need the
Python environment (`uv sync`); everything else needs only Go. Dependencies and
the build cache live in `.gocache/` inside the repository, so nothing depends on
writable state elsewhere. The first build populates it and needs the network.

Layout: [AGENTS.md](AGENTS.md#repository-layout).

## Documentation that is executed

[`docs/usage.md`](docs/usage.md) and
[`docs/configuration.md`](docs/configuration.md) carry ```gherkin blocks, and
`TestDocumentationIsTrue` ([`internal/features/docs_test.go`](internal/features/docs_test.go))
extracts every one of them into a feature file and runs it against a binary built
from the working tree. The markdown is the only copy: nothing is generated, so
nothing can drift. A claim whose block stops passing fails `go test`, and the
failure names the heading in the document it came from.

Changing behaviour therefore means changing the prose, and a new flag or config
key is documented by writing the scenario that shows it. Steps live in
[`internal/features/`](internal/features/): `features_test.go` builds the project
and asserts about the files, `cli_test.go` runs the command and reads its streams
and exit code, `upstream_test.go` puts a second project beside the first. Godog
runs in strict mode, so a step a block invents is a failure rather than a skip.

The hand-written specifications in [`features/`](features/) are the same
machinery aimed at behaviour that is not documentation: manifest ordering, the
canonicalisation the parity proof assumes.

Not executed, because no test process can prove it: the install commands in
`docs/usage.md`. `make verify-wheels` covers the packaging half of that claim.

## Proof that it matches dbt-osmosis

`scripts/parity.sh` copies the fixture in `testdata/projects` twice, runs the
**real** `dbt-osmosis yaml refactor --auto-apply` over one and `dbt-ditto
inherit` over the other, and diffs the schema YAML. Its output is committed to
`testdata/golden/osmosis`, so `TestParityWithDbtOsmosis` re-checks it on every
`go test`, with no dbt, Python or warehouse. The fixture is a real two-project
DuckDB build carrying the cases that break naive implementations:

| Case | Where |
| --- | --- |
| Two upstreams documenting one column differently | `stg_orders_enriched` |
| Documentation through an undocumented middle model | `int_orders_passthrough` |
| An undocumented ancestor shadowing a documented one | `int_customer_orders.customer_id` |
| Diamonds, and fan-out from one parent | `int_customer_orders`, `fct_orders` |
| Documented downstream, nothing upstream | `dim_regions` ← `stg_regions` |
| Local descriptions, placeholders and meta that must survive | `dim_customers` |
| Column case differing from upstream | `dim_customer_keys` |
| Columns the warehouse no longer has | `dim_customers.legacy_customer_grade` |
| `{{ doc() }}` references | `seeds/_seeds.yml` |
| Sources as roots, one documented nowhere in dbt | `crm.raw_orders`, `billing.raw_invoices` |
| A model moved between files by a path rule | `fct_customer_revenue` |
| Cross-project inheritance | `analytics.customer_report` |
| Aggregates, and columns packed into a struct | `fct_customer_totals`, `dim_customer_profile` |

On the cross-project half, dbt-osmosis does not produce different output. It
crashes, because it is not compatible with dbt-loom
(`AttributeError: 'LoomRunnableConfig' object has no attribute 'project_root'`,
traceback in `testdata/golden/osmosis-analytics-failure.log`). That
incompatibility is the reason this project exists.

## dbt versions

Five dbt versions, 1.8.9 to 1.12.4, run on every `go test` with no dbt
installed. `make matrix` runs real dbt once per version in a throwaway
environment and commits the artifacts; `make matrix-test` replays them. Adding
one is a line in `scripts/matrix.sh`.

They straddle dbt 1.9.6, where column-level `config:` arrived. Writing meta there
on an older dbt loses it silently, so where meta goes is the most
version-sensitive decision the tool makes — a test fails if the matrix stops
spanning that boundary.

## Four adapters, no cloud accounts

An adapter is exercised by the artifacts it writes, so all four are covered
without an account. `TestMatrixCoversEveryAdapter` fails if any stops being
captured; the gaps are in [`testdata/matrix/README.md`](testdata/matrix/README.md)
and [`testdata/bigquery/README.md`](testdata/bigquery/README.md).

| Adapter | Artifacts from |
| --- | --- |
| DuckDB | real dbt, real warehouse |
| Postgres | real dbt against `postgres:16-alpine` in Docker |
| Snowflake | real dbt-snowflake through `fakesnow`, DuckDB underneath |
| BigQuery | real manifest from `dbt parse`; catalog hand-built |

## Source providers, tested without an account

Providers talk to a warehouse, which no test run can do, so what crosses the
boundary is checked and the boundary itself is never crossed:

| What | Where |
| --- | --- |
| The wire format, both sides | `TestPythonProviderRoundTrip`, running `echo.py` through the real pipeline |
| What the request carries | `TestProviderIsToldWhereDbtKeepsItsCredentials` |
| Spawning, merging, cache, failures | `internal/sources/sources_test.go`, providers as shell one-liners |
| Every config key | one case each in `sources_settings_test.go` |
| Contract, profiles.yml, type rendering | `packaging/providers/test_providers.py` (`make providers`) |

`echo.py` uses the same shared module the real providers do, so it proves the
request dbt-ditto writes is the request a provider reads. Type rendering is
checked because a `data_type` differing from the dbt adapter's gives a project
running both tools a diff on every column. Not covered: that the warehouse SDKs
behave, or that the queries return what they claim. Those need an account.

## Git hooks

`make hooks` installs [lefthook](https://lefthook.dev): pre-commit runs the fast
checks (~3s), pre-push the whole Go suite, race detector and build (~12s).
Neither needs dbt, Docker or the network, which is what makes them safe to block
on ([`lefthook.yml`](lefthook.yml) has the jobs).

## Speed

`make bench` on an Apple M4, best of three, same project, same work:

| | wall clock |
| --- | --- |
| dbt-osmosis (platform project) | **1.507 s** |
| dbt-ditto (platform project) | **0.022 s** |
| dbt-ditto (both projects) | **0.023 s** |

68× on a project this small, and the gap widens with size: nearly all of
dbt-osmosis' time is fixed cost (Python start-up, loading dbt, parsing the
project, connecting to the warehouse) that dbt-ditto never pays. `go test -bench`
covers generated projects up to 5 000 models × 60 columns (1.41 s, by then
dominated by opening files). What must not regress:
[AGENTS.md](AGENTS.md#performance).

## CI and packaging

| Workflow | Trigger | What it does |
| --- | --- | --- |
| `ci.yml` | push to `main`, PR | gofmt, vet, tests on Linux and macOS, race, shellcheck, provider tests, live parity diff |
| `draft-release.yml`, `labeler.yml` | push to `main` | keep one draft release current, and its labels in sync |
| `release.yml` | draft release **published** | version bump + tag move, archives, wheels, PyPI |

`scripts/check-versions.sh` gates a release on the tag and `pyproject.toml`
agreeing. Publishing the draft creates the tag, and the tag is what ships; `main`
is what CI proves. Mechanics and the PyPI setup that cannot be done from inside
the repository: [AGENTS.md](AGENTS.md#releasing).

`packaging/pypi/build_wheels.py` produces one wheel per platform, each carrying
the binary in the wheel's `.data/scripts/` directory, using only the standard
library. `make verify-wheels` installs them under both **pip and uv** and checks
that the binary is executable and runs, that a wheel for another platform is
refused, that resolving by name picks the right one, and that uninstall removes
the binary; then `twine check`. Both installers are tested because an earlier
version shipped the binary without its executable bit: uv tolerated it, pip
turned it into `permission denied`.
