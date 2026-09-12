# The version matrix

dbt-ditto reads `manifest.json` and `catalog.json` and nothing else. A captured
pair is therefore a complete, faithful record of what one dbt version looks
like — which is what lets the rig be split in two:

| | needs | run |
| --- | --- | --- |
| `scripts/matrix.sh` | Python, uv, dbt, a warehouse | rarely, by hand |
| `go test ./...` | nothing but Go | every commit, everywhere |

`scripts/matrix.sh` builds a throwaway environment per dbt version, runs real
dbt over `project/`, and commits the resulting artifacts into
`artifacts/duckdb-core<version>/`. `internal/runner/matrix_test.go` then holds
every version at once without anybody needing dbt installed.

## What is captured

```
artifacts/duckdb-core1.9.8/
  manifest.json.gz     what dbt wrote
  catalog.json.gz      what `dbt docs generate` wrote
  meta.json            the versions that produced them
```

Gzipped because a manifest is mostly macro definitions dbt-ditto never reads;
both readers understand `.gz`, so this exercises that path too. The whole matrix
is a few hundred kilobytes.

`meta.json` records the dbt version taken **from the artifact**, not from the
directory name or the requested pin, because those can disagree.

## Adding a version

One line in the `VERSIONS` list in `scripts/matrix.sh`, then rerun it. The tests
pick up whatever is on disk.

Entries are `dbt-core:dbt-duckdb:python`, and all three are pinned deliberately:

- **dbt-core**, because an adapter only requires `dbt-core>=1.9,<2`. Installing
  `dbt-duckdb==1.9.0` on its own resolves dbt-core 1.12, so an unpinned matrix
  silently tests the same version several times.
- **python**, because uv installs the newest interpreter available and dbt 1.8
  and 1.9 do not run on 3.13 or 3.14. They fail deep inside mashumaro at import
  time, which is a confusing way to find out a row was never really tested.

The special entry `local` captures whatever the repository's own `.venv` pins,
without building an environment. It covers the version the parity proof is
written against, and rescues rows whose dependencies will not build locally.

## Why these versions

The matrix has to straddle the boundaries that change the artifacts, and
`TestMatrixSpansTheConfigBlockBoundary` fails if it stops doing so:

| Version | Why it is in the matrix |
| --- | --- |
| 1.8.9 | before column-level `config:` existed at all |
| 1.9.0 | column config exists, but below the 1.9.6 cutover |
| 1.9.8 | past the cutover: column meta belongs inside `config:` |
| 1.10.11 | current stable |
| `local` (1.12.4) | latest, and the version the parity proof uses |

## Snowflake, without Snowflake

`snowflake/` is the same project aimed at the Snowflake adapter, and it is built
with no account, no credentials and no network.

[`fakesnow`](https://pypi.org/project/fakesnow/) replaces
`snowflake.connector` with an implementation backed by DuckDB. dbt-snowflake
talks to Snowflake through exactly that connector, so patching it before dbt
starts (see `scripts/lib/dbt_fakesnow.py`) gives a real dbt run: the real
adapter, the real macros, a real `docs generate`.

That distinction matters. These artifacts are *generated* rather than written by
hand, so their Snowflake-isms are the adapter's own rather than a guess at what
the adapter would do:

```
catalog.json    ORDER_ID   NUMBER      <- Snowflake upper-cases unquoted names
                STATUS     TEXT           and has its own type vocabulary
manifest.json   order_id               <- the YAML documents it in lower case
```

Bridging that case difference is the one thing DuckDB can never exercise, and
`TestMatrixSnowflakeCaseHandling` pins all three parts of it: a new column takes
the warehouse's spelling, an entry already in the file keeps its own, and
inheritance matches across the two.

`meta.json` records `"emulated_by": "fakesnow"` so it is never mistaken for a
run against the real thing.

### What this does not cover

fakesnow is DuckDB wearing a Snowflake hat. It is faithful about identifier
casing, the connector API and the type vocabulary, which is most of what
dbt-ditto depends on. It is not Snowflake, so anything resting on genuine
Snowflake behaviour — `INFORMATION_SCHEMA` corners the emulator does not
implement, real `COMMENT` propagation, masking policies — still needs an
account.

BigQuery has no equivalent in-process fake. `testdata/bigquery` is therefore
hand-built, with each detail traced back to the adapter's source; see the README
there.

The closest approximation would be
[`bigquery-emulator`](https://github.com/goccy/bigquery-emulator) under Docker,
which would make BigQuery a generated row like this one. Two things stand in the
way, and both are known rather than guessed:

- Python's `google-cloud-bigquery` does not honour `BIGQUERY_EMULATOR_HOST` the
  way the Go client does, so pointing dbt-bigquery at an emulator needs the
  endpoint injected into the client — the same monkeypatching trick as
  `dbt_fakesnow.py`, in a `dbt_fakebq.py`.
- The catalog query joins `INFORMATION_SCHEMA.COLUMN_FIELD_PATHS`, which the
  emulator may not implement. If it does not, `dbt run` would still yield a real
  manifest and only the catalog would stay hand-built — which is still an
  improvement on today.

## Postgres, in a container

`postgres/` is the same project aimed at dbt's reference adapter, run against a
throwaway `postgres:16-alpine` container on port 55432 — deliberately not 5432,
so a Postgres already running on the machine is never touched.

It is worth a row for three reasons: a different catalog query, a different type
vocabulary (`integer`, `character varying`), and real `COMMENT ON` support, so
the warehouse-comment feature is exercised on a second adapter rather than only
on DuckDB. The seed carries a `post-hook` that comments a column for exactly
that purpose.

The row needs Docker. Without it the run prints

```
==> Postgres: skipped, no Docker daemon (try: colima start)
```

and carries on, so the matrix still works on a machine with no container
runtime. On macOS, `colima start` provides one.

## The project

`project/` is deliberately small and conservative: it is rebuilt against every
version in the matrix, so it may only use features that exist in all of them.
It is not trying to cover dbt's feature surface — the fixture in
`testdata/projects` does that, against one pinned version. This one exists to
compare *the shape of the artifacts each version writes*.
