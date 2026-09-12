# Developing dbt-ditto

Build, test, prove and release. User-facing configuration is in
[usage.md](usage.md); architecture, conventions and traps are in
[AGENTS.md](../AGENTS.md).

## Make targets

```sh
make                 # vet, test, build
make test            # includes the recorded parity proof
make features        # the behaviour specifications in features/, with output
make race            # the same under the race detector
make parity          # runs the real dbt-osmosis and refreshes the golden files
make bench           # head-to-head timing, then synthetic scaling
make fixture         # rebuild the DuckDB warehouse and dbt artifacts
make matrix          # re-capture artifacts by running real dbt, once per version
make matrix-test     # replay every captured dbt version and adapter (Go only)
make providers       # the source providers' offline tests (no warehouse account)
make hooks           # install the git hooks in lefthook.yml
make dist            # cross-compile release archives into dist/
make wheels          # build the PyPI wheels into dist/pypi/
make verify-wheels   # build them and prove they install and run under pip and uv
```

`make parity`, `make bench`, `make fixture`, `make providers`
and the wheel targets need the Python environment (`uv sync`); everything else
needs only Go.
Dependencies are resolved against `go.sum` from a module cache kept inside the
repository (`.gocache/mod`), as is the build cache, so nothing depends on
writable state elsewhere. The first build populates the cache and needs the
network; later ones do not.

The repository layout is in [AGENTS.md](../AGENTS.md#repository-layout).

## Proof that it matches dbt-osmosis

`scripts/parity.sh` is the proof, and it does not take anyone's word for it:

1. Two identical copies of the fixture in `testdata/projects` are made.
2. The **real** `dbt-osmosis yaml refactor --auto-apply` runs over one.
3. `dbt-ditto inherit` runs over the other.
4. The resulting schema YAML is diffed.

```
$ make parity-check
==> dbt-osmosis: platform
==> dbt-osmosis: analytics (expected to fail: dbt-loom incompatibility)
    exit code 1
==> dbt-ditto: both projects

==> diff (platform schema YAML)
    identical
```

The dbt-osmosis output is committed to `testdata/golden/osmosis`, so
`TestParityWithDbtOsmosis` re-checks it on every `go test` without needing dbt,
Python, or a warehouse.

The fixture is not a toy. It is a real two-project dbt build against DuckDB, and
it deliberately contains the cases that break naive implementations:

| Case | Where |
| --- | --- |
| Two upstreams documenting the same column differently | `stg_orders_enriched` (a seed-backed model and a CRM source) |
| Documentation travelling through an undocumented middle model | `int_orders_passthrough` |
| An undocumented ancestor shadowing a documented one | `int_customer_orders.customer_id` |
| Diamond dependencies | `int_customer_orders`, `fct_customer_revenue` |
| Documented downstream, nothing documented upstream | `dim_regions` over `stg_regions` over `raw_regions` |
| Local descriptions that must survive, placeholders included | `dim_customers` (`TODO`, `Not documented`) |
| Local meta disagreeing with upstream meta | `dim_customers.signup_country` |
| Columns whose case differs from upstream | `dim_customer_keys` (`CUSTOMER_ID`, `Signup_Country`) |
| Columns the warehouse no longer has | `dim_customers.legacy_customer_grade` |
| `{{ doc() }}` references | `seeds/_seeds.yml` |
| Sources as inheritance roots | `crm.raw_orders` |
| A source documented nowhere in dbt | `billing.raw_invoices`, plus a real DuckDB `COMMENT` |
| A model documented in the wrong file, moved by a path rule | `fct_customer_revenue` → `finance/_finance_models.yml` |
| Parallel models fanning out from one parent | `fct_orders`, `fct_daily_revenue` |
| Cross-project inheritance | `analytics.customer_report` → `platform.dim_customers` |
| Aggregated columns | `fct_customer_totals` (`sum`, `max`, `count`) |
| Columns packed into a struct, and unpacked again | `dim_customer_profile`, `dim_customer_flat` |

### The one thing dbt-osmosis cannot do here

Running dbt-osmosis against the cross-project half of the fixture does not
produce different output — it **crashes**, because dbt-osmosis and dbt-loom are
not compatible:

```
AttributeError: 'LoomRunnableConfig' object has no attribute 'project_root'
```

The full traceback is kept in `testdata/golden/osmosis-analytics-failure.log`.
That incompatibility is the reason this project exists: dbt-ditto reads the
manifests directly, so a project running dbt-loom can still have its
documentation propagated.

## dbt versions

dbt-ditto is tested against five dbt versions, from 1.8.9 to 1.12.4, on every
`go test` — with no dbt installed. `make matrix` runs real dbt once per version
in a throwaway environment and commits the artifacts; `make matrix-test` replays
all of them.

The versions straddle dbt 1.9.6, where column-level `config:` arrived — writing
meta there on an older dbt loses it silently, so where meta goes is the most
version-sensitive decision the tool makes, and a test fails if the matrix stops
spanning that boundary. Adding a version is one line in `scripts/matrix.sh`; see
[`testdata/matrix/README.md`](../testdata/matrix/README.md).

## Four adapters, no cloud accounts

The matrix covers **DuckDB, Postgres, Snowflake and BigQuery** with no cloud
account, credentials or billing, because an adapter is exercised by the shape of
the artifacts it writes:

| Adapter | How the artifacts are produced | Fidelity |
| --- | --- | --- |
| DuckDB | real dbt, real warehouse | everything is real |
| Postgres | real dbt against `postgres:16-alpine` in Docker | everything is real |
| Snowflake | real dbt-snowflake through `fakesnow`, DuckDB underneath | real adapter and macros |
| BigQuery | manifest from `dbt parse`; catalog hand-built | real manifest, assembled catalog |

`TestMatrixCoversEveryAdapter` fails if any of the four stops being captured.
How each is obtained, and what it cannot cover, is in
[`testdata/matrix/README.md`](../testdata/matrix/README.md) and
[`testdata/bigquery/README.md`](../testdata/bigquery/README.md).

## Source providers, tested without an account

Providers document external sources by talking to a warehouse, which no test
run can do. The testing splits along the same line as everything else here: what
crosses a boundary is checked, and the boundary itself is never crossed.

| What | Where | Needs |
| --- | --- | --- |
| The wire format, both sides | `TestPythonProviderRoundTrip` runs `packaging/providers/echo.py` through the real pipeline | Go + python3 |
| What the request carries | `TestProviderIsToldWhereDbtKeepsItsCredentials` | Go |
| Spawning, merging, cache, failure handling | `internal/sources/sources_test.go`, with providers written as shell one-liners | Go |
| Every config key | one case each in `sources_settings_test.go`, as `TestEverySettingIsCovered` requires | Go |
| Contract parsing, profiles.yml, adapter type rendering | `packaging/providers/test_providers.py` (`make providers`) | Python |

`echo.py` earns its place twice: it is the shortest complete example for someone
writing a provider, and because it uses the same shared module the real
providers do, running it end to end proves that the request dbt-ditto writes is
the request a provider reads.

The type rendering is worth testing rather than eyeballing. Each provider has to
produce the `data_type` string its dbt adapter would have written into
`catalog.json` — ``STRUCT<`name` TYPE>`` and `ARRAY<...>` for BigQuery,
`VARCHAR(16777216)` and `NUMBER(38,0)` for Snowflake — or a project running both
tools gets a diff on every column.

What is *not* covered: that `google-cloud-bigquery` and
`snowflake-connector-python` behave as expected, and that the queries return
what they claim. Those need an account, and the fallback is the same one the
rest of this repository uses — capture real artifacts once, by hand, and replay
them.

## Git hooks

`make hooks` installs [lefthook](https://lefthook.dev): pre-commit is the fast
checks (~3s), pre-push the whole Go suite, race detector and build (~12s).
Neither needs dbt, Docker or the network, which is what makes them safe to block
on. The jobs are listed in [`lefthook.yml`](../lefthook.yml).

## Speed

Measured with `make bench` on an Apple M4, best of three, same project, same
work:

| | wall clock |
| --- | --- |
| dbt-osmosis (platform project) | **1.507 s** |
| dbt-ditto (platform project) | **0.022 s** |
| dbt-ditto (both projects) | **0.023 s** |

68× on a project this small, and the gap widens with project size: almost all of
dbt-osmosis' time is a fixed cost — starting Python, loading dbt, parsing the
project and connecting to the warehouse — that dbt-ditto never pays.

Scaling on generated projects (`go test -bench`, one goroutine per core):

| Project | First run | Re-run (`--check`) |
| --- | --- | --- |
| 100 models × 20 columns | 14 ms | 13 ms |
| 1 000 models × 20 columns | 125 ms | 97 ms |
| 5 000 models × 60 columns | 1.41 s | 1.23 s |

At that size the run is dominated by opening files, not by anything dbt-ditto
computes. What got it there, and what must not regress, is in
[AGENTS.md](../AGENTS.md#performance).

## CI

| Workflow | Trigger | What it does |
| --- | --- | --- |
| `ci.yml` | push to `main`, PR | gofmt, vet, tests on Linux and macOS, race detector, shellcheck, the source providers' offline tests, and the live dbt-osmosis parity diff |
| `draft-release.yml` | push to `main` | keeps one draft release up to date, version resolved from PR labels |
| `labeler.yml` | `.github/labels.yml` changes | syncs the labels release-drafter reads |
| `release.yml` | draft release **published** | version bump + tag move, cross-compiled archives, wheels, PyPI via trusted publishing, release assets |

`scripts/check-versions.sh` gates a release on the tag and `pyproject.toml`
agreeing. AGENTS.md covers the PyPI setup that cannot be done from inside the
repository.

A release is the act of publishing the draft release that `draft-release.yml`
maintains. Publishing creates the tag, and the tag is what ships; `main` is what
CI proves. The published artifacts are the source of truth for what a user has
installed.

## Packaging

`packaging/pypi/build_wheels.py` produces one wheel per platform, each carrying
the binary in the wheel's `.data/scripts/` directory. It uses only the standard
library, because a build tool that needs its own dependencies installed first is
a bootstrapping problem nobody needs.

The distribution name is `dbt-ditto`; PEP 427 escapes it to `dbt_ditto` inside
wheel filenames and the `.dist-info` / `.data` directories, which is what
installers match on.

`make verify-wheels` builds them and then proves they work: installs under both
**pip and uv**, asserts the installed file is executable and runs, checks that a
wheel for another platform is refused, that resolving by name picks the right
one, that uninstall removes the binary, and finally runs `twine check`. Testing
both installers is not belt and braces — an earlier version shipped the binary
without its executable bit, which uv silently tolerated and pip turned into
`permission denied`.
