# Developing dbt-ditto

Build, test, prove and release. User-facing configuration is in
[usage.md](usage.md); architecture, conventions and traps are in
[AGENTS.md](../AGENTS.md).

## Make targets

```sh
make                 # vet, test, build
make test            # includes the recorded parity proof
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
All Go work runs vendored, with the build cache inside the repository.

## Repository layout

```
cmd/dbt-ditto/        the CLI
internal/config/      dbt_ditto.yml, [tool.dbt-ditto] in pyproject.toml,
                      dbt_loom.config.yml discovery, and the defaults that pin
                      dbt-osmosis parity
internal/dbt/         streaming manifest.json / catalog.json / dbt_project.yml readers
internal/inherit/     the cross-project graph and the inheritance resolver
internal/runner/      orchestration, selection, path templates, YAML writing
internal/sources/     source providers: the contract, the subprocess, the cache,
                      and label routing
internal/yamlfile/    yaml.Node editing that preserves comments and key order
testdata/projects/    the two-project DuckDB fixture
testdata/golden/      recorded dbt-osmosis output, and dbt-ditto's own for the
                      cross-project case
packaging/providers/  the BigQuery and Snowflake source providers, in Python
packaging/pypi/       the wheel builder that ships the binary
scripts/              fixture build, parity harness, benchmark harness
```

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
`go test` — with no dbt installed.

That works because dbt-ditto reads `manifest.json` and `catalog.json` and
nothing else, so a captured pair is a complete record of what a dbt version
looks like. `make matrix` runs real dbt once per version in a throwaway
environment and commits the artifacts; `make matrix-test` then replays all of
them. The whole matrix is a few hundred kilobytes.

The versions straddle dbt 1.9.6, where column-level `config:` arrived — writing
meta there on an older dbt loses it silently, so where meta goes is the most
version-sensitive decision the tool makes, and it is asserted against every
captured version rather than against a made-up version string. A test fails if
the matrix ever stops spanning that boundary.

Adding a version is one line in `scripts/matrix.sh`. See
[`testdata/matrix/README.md`](../testdata/matrix/README.md).

## Four adapters, no cloud accounts

The matrix covers **DuckDB, Postgres, Snowflake and BigQuery**, and none of it
needs a cloud account, credentials or billing. dbt-ditto never connects to a
warehouse, so an adapter is exercised by the shape of the artifacts it writes —
and each one can be obtained locally:

| Adapter | How the artifacts are produced | Fidelity |
| --- | --- | --- |
| DuckDB | real dbt, real warehouse | everything is real |
| Postgres | real dbt against `postgres:16-alpine` in Docker | everything is real |
| Snowflake | real dbt-snowflake through `fakesnow`, DuckDB underneath | real adapter and macros |
| BigQuery | manifest from `dbt parse`; catalog hand-built | real manifest, assembled catalog |

`TestMatrixCoversEveryAdapter` fails if any of the four stops being captured.

**Snowflake is a real dbt run.** [`fakesnow`](https://pypi.org/project/fakesnow/)
replaces `snowflake.connector` with an implementation backed by DuckDB, and
dbt-snowflake talks to Snowflake through exactly that connector. Patching it
before dbt starts gives the real adapter, the real macros and a real
`docs generate`, so the artifacts are generated rather than guessed. They come
out genuinely Snowflake-shaped: `ORDER_ID` where the YAML says `order_id`, and
`NUMBER`/`TEXT` rather than DuckDB's types. Bridging that case difference is the
one thing DuckDB can never exercise.

**BigQuery is a hand-built fixture**, since it has no in-process fake. Each
detail in `testdata/bigquery` is traced back to the adapter's own source —
`catalog.sql` for the artifact shape, `column.py` for the type text — rather
than guessed, and it covers nested and repeated `RECORD` columns,
`ARRAY<STRUCT<...>>`, `NUMERIC(38, 9)`, `GEOGRAPHY`, `JSON`, `policy_tags` and
warehouse-set column descriptions.

That fixture earned its keep immediately. BigQuery's catalog query joins through
`INFORMATION_SCHEMA.COLUMN_FIELD_PATHS`, so a nested record arrives as the
parent column *and* every dotted leaf, where DuckDB reports only the parent —
which means expanding struct types without checking writes every nested field
twice on BigQuery. It also surfaced a key-ordering bug that no DuckDB test could
have caught.

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

`make hooks` installs [lefthook](https://lefthook.dev). The split follows how
much each check costs:

- **pre-commit** (~3s): gofmt with the result staged back, `go vet`, the short
  test suite, `shellcheck` and `bash -n` on shell, `py_compile` on Python, and a
  guard that a matrix artifact is never committed uncompressed.
- **pre-push** (~12s): the whole Go suite — every captured dbt version and all
  four adapters, plus the recorded dbt-osmosis parity proof — then the race
  detector and a build.

Neither needs dbt, Docker or the network, which is what makes them safe to
block on. Regenerating artifacts (`make matrix`) and the live dbt-osmosis
comparison (`make parity`) are deliberately *not* hooks: they are slow, they
reach the network, and they rewrite committed fixtures.

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
computes. What got it there:

- The manifest is decoded as a **stream**, skipping `macros`, `child_map`,
  compiled SQL and every other key that inheritance does not read, instead of
  handing a hundred megabytes to reflection-based decoding.
- Inheritance is resolved for every node **in parallel**, as is loading and
  writing the YAML files; only the mutation step in between is serial, because
  two models can share a file.
- Column lookups go through a **folded index** built once per node, not a linear
  scan per ancestor per column.
- A document that a run leaves unchanged is recognised by **fingerprint** and
  never serialised. This is the path CI takes on a healthy repository.
- Column meta values are turned into YAML nodes **directly**; the obvious
  `yaml.Node.Encode` stands up a whole emitter and parser per value, which alone
  accounted for half of all allocations.

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
