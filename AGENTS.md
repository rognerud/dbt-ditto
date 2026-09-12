# AGENTS.md — dbt-ditto

Downstream of README.md, never replicating it: only the behaviour that must not
change, the internals, and constraints not visible from the code. No
development state, no decision log. See also `docs/usage.md` (configuration),
`docs/development.md` (build, test, release), `docs/source-providers.md`.

## dbt-osmosis reference behaviour

Verified against dbt-osmosis 1.5 on the fixture and from
`dbt_osmosis/core/{settings,inheritance,transforms,sync_operations}.py`.
`TestDefaultsMatchDbtOsmosis` pins these.

| Behaviour | dbt-osmosis |
| --- | --- |
| `data_type` written | yes |
| Column name / data type case | preserved |
| Column order | catalog order |
| Missing columns added, stale removed | yes, yes |
| meta / tags placement | `config.meta` / `config.tags` when the manifest's dbt >= 1.9.6 |
| Placeholders | `""`, `Pending further documentation`, `No description for this column`, `Not documented`, `Undefined`; compared exactly, no trim, no case fold |
| Node-level description inheritance | none |
| Progenitor annotation | off; key `osmosis_progenitor`. dbt-ditto defaults this on |
| Empty descriptions written | no |

The algorithm:

- Pipeline: inject missing columns → remove columns not in the database →
  inherit → sort → synchronise data types. Injection happens before inheritance,
  on the in-memory manifest, which is why `Node.EffectiveColumn` consults the
  catalog: an undocumented ancestor still has to claim a column.
- Ancestors are grouped into generations by a depth-first walk with one shared
  visited set, so a node reached by several routes is filed under the depth the
  walk first reached it at, not its shortest path. Generations run furthest
  first, each sorted by `unique_id`.
- Within a generation the first ancestor holding the column claims it; the rest
  of that generation is skipped for that column, including tags and meta.
- Upstream meta overwrites local meta per key, local key order preserved. Tags
  are an order-preserving union, local first.
- A description is inherited only when the column's own is empty. The
  placeholder list filters upstream candidates, not the local value.
- `{{ doc() }}` is written back rendered: the description written is the
  manifest's.

### Where behaviour diverges

Every default reproduces dbt-osmosis except `inheritance.progenitor`. The
settings are documented in README.md and `docs/usage.md`; below is only what
each divergence means for parity.

| Divergence | Effect on parity |
| --- | --- |
| Comments inside a column list are kept, not lost | `output.comments: osmosis` reproduces the loss; `parity.sh` sets it |
| `snapshot.` dependencies followed when walking ancestors | dbt-osmosis follows only `model.`, `seed.`, `source.` |
| `inheritance.derived` | off by default so parity holds |
| `inheritance.backfill` | off by default; dbt-osmosis cannot document a source at all |
| Warehouse comments come from `catalog.json`, not the adapter | DuckDB's adapter reports none while the catalog carries them, so `parity.sh` sets `columns.comments: never` |
| `inheritance.progenitor` on by default | the only default that changes the bytes of an existing dbt-osmosis project |
| `inheritance.directives` | outranks name matching, `force` and a local description; unresolvable ones are warned about and left verbatim |
| `inheritance.warn_ambiguous` | stderr only, changes no output |
| `inheritance.ambiguity_meta` | off by default |
| `dbt_ditto_definitive` | a meta marker, not a setting; no effect until written |

Consequences:

- Any Go test comparing against `testdata/golden/osmosis` must set the same
  three options `parity.sh` does (`output.comments: osmosis`,
  `columns.comments: never`, `inheritance.progenitor: false`), or it fails on
  the `billing` source's `order_id`.
- `parity.sh` `sed`s `progenitor: false` into the fixture config's existing
  `inheritance:` block; a second top-level key would be a duplicate. The
  cross-project golden in `testdata/golden/dbt-ditto` does carry the annotation.

### Derived from data, not configurable

- Whether meta goes under `config:` is decided by the manifest's dbt version. A
  switch could only agree with dbt or silently drop every column's meta.
- A project's name comes from `dbt_project.yml` or the manifest, and is what
  `selectNodes` matches `PackageName` against. `ProjectRef.Name` exists because a
  dbt-loom entry names a manifest before anything has read it; the duplicate that
  can create is dropped in `dropDuplicateManifests`.

### Struct expansion is parity, not an extension

`columns.expand_structs` defaults to on, and must. dbt-osmosis introspects
through the adapter, and adapters understanding nested data report
`profile.first_name` as a column in its own right. dbt-ditto reads
`catalog.json`, where that is one column typed `STRUCT(first_name VARCHAR, ...)`,
so it parses the type to reach the same answer (`internal/dbt/structs.go`).
Turning expansion off breaks parity.

### Derived matching (internal/inherit/match.go)

Both ends of a match need aliasing independently: the upstream column may be a
struct field, the downstream one aggregated, nested, or both. A match is a pair
of derivations, and `rankPairs` orders pairs by total distance, cheapest first;
exact/exact costs zero and always wins, so a real match is never replaced by a
guess. A stripped downstream name must be looked up against the ancestor's exact
names, not the same rank's map — the likely cause if `derived_test.go` fails
with empty descriptions.

### dbt-osmosis cannot run with dbt-loom

`dbt-osmosis yaml refactor` on the analytics project fails with
`AttributeError: 'LoomRunnableConfig' object has no attribute 'project_root'`
(traceback at `testdata/golden/osmosis-analytics-failure.log`), so the golden
file for the cross-project half is dbt-ditto's own output.

## Repository layout

```
cmd/dbt-ditto/        CLI
internal/config/      dbt_ditto.yml / pyproject.toml / loom discovery + the
                      defaults that pin parity
internal/dbt/         streaming manifest reader, catalog, dbt_project.yml, OrderedMap
internal/inherit/     cross-project graph (generations) + resolver
internal/runner/      orchestration, selection, path templates, YAML writing
internal/sources/     source providers: contract, subprocess, cache, label routing
internal/yamlfile/    yaml.Node editing preserving comments and key order
internal/canon/       sorts a schema file's named entries, so two trees compare
                      without comparing dbt's node order
internal/features/    step definitions behind features/
features/             Gherkin behaviour specifications, run as tests
packaging/providers/  the BigQuery and Snowflake providers, in Python
packaging/pypi/       wheel builder carrying the binary
testdata/projects/    platform (upstream) + analytics (downstream, via dbt-loom)
testdata/golden/      recorded dbt-osmosis output + dbt-ditto's cross-project output
scripts/              build-fixture.sh, parity.sh, matrix.sh, bench.sh, dist.sh
docs/                 usage, development and source-provider documentation
```

## Releasing

A release is publishing the draft
[release-drafter](https://github.com/release-drafter/release-drafter) maintains
on `main`. Nothing is tagged until someone presses Publish, which creates the
tag and starts `release.yml`. The version comes from merged PR labels, which is
why they live in `.github/labels.yml` and are synced by `labeler.yml`. Workflows
are tabled in [docs/development.md](docs/development.md#ci).

The tag is created at `main`'s head, before the version bump, so `release.yml`
rewrites `pyproject.toml` from the tag, commits that to `main`, then force-moves
the tag onto the bump commit — otherwise the tag points at a file claiming the
previous version. Moving a tag is acceptable only because it happens seconds
after publication.

PyPI uses trusted publishing, configured outside the repository, for `dbt-ditto`
against workflow `release.yml` and environment `pypi`. No token is stored; the
`pypi` job requests `id-token: write`. A GitHub environment called `pypi` must
exist here or the job never runs.

## Performance

Numbers in [docs/development.md](docs/development.md#speed). What must not
regress:

- Manifest decoding is a token stream that skips `macros`, `child_map`, compiled
  SQL and similar, and records each node's position — the order entries are
  written back in.
- Resolve, file load and file save run through `runner.parallel`. Only the
  mutation step between them is serial, because two models can share a file.
- `Node.Column` builds a folded index once; `Catalog.Ordered`/`Folded` are
  cached. A linear scan here made resolve quadratic on wide models.
- `yamlfile.Encode` builds scalar nodes directly; `yaml.Node.Encode` stands up a
  full emitter and parser per value, half of all allocations.
- `yamlfile.File` fingerprints the document at load, so an unchanged document is
  never serialised. This is the `--check` path.

## The version and adapter rig

dbt-ditto reads `manifest.json` and `catalog.json` and nothing else, so a
captured pair is a complete record of a dbt version or an adapter. Generating
artifacts needs Python, dbt and a warehouse and is run rarely by hand; testing
against them needs only Go. Everything crossing that line is committed.

| Rig | Generator | Artifacts | Tests |
| --- | --- | --- | --- |
| dbt versions | `scripts/matrix.sh` | `testdata/matrix/artifacts/duckdb-*` | `matrix_test.go` |
| Snowflake | `matrix.sh` via fakesnow | `…/snowflake-*` | `matrix_test.go` |
| Postgres | `matrix.sh` via Docker | `…/postgres-*` | `matrix_test.go` |
| BigQuery (matrix) | `matrix.sh`, `dbt parse` only | `…/bigquery-*` | `matrix_test.go` |
| dbt-osmosis parity | `scripts/parity.sh` | `testdata/golden/` | `golden_test.go` |
| BigQuery (deep) | hand-built from adapter source | `testdata/bigquery/` | `bigquery_test.go` |

Why each row exists is in [`testdata/matrix/README.md`](testdata/matrix/README.md)
and [`testdata/bigquery/README.md`](testdata/bigquery/README.md). Not recorded
there:

- `trueColumns` skips expanding any path the catalog already names. BigQuery's
  catalog macro reports a nested `RECORD` as the parent *and* every dotted leaf,
  DuckDB only the parent; without that BigQuery gets every field twice.
- Snowflake's `normalize_column_name` upper-cases; dbt-ditto does not.
- fakesnow's `db_path` is a directory, and `dbt docs generate` must be passed as
  two arguments.
- dbt-core 1.12 pulls `dbt-core-experimental-parser`, whose build downloads a
  47 MB binary and fails behind a filtering proxy — hence the `local` entry
  capturing the repository's own `.venv`.
- macOS ships bash 3.2, where `"${ARR[@]}"` on an empty array errors under
  `set -u`, hence `${ARR[@]+"${ARR[@]}"}`. `${#ARR[@]}` is fine.

## Known pitfalls

- **Entry order in a shared schema file is manifest order**, not alphabetical;
  `selectNodes` sorts by `Node.Order`. dbt does not order manifest nodes
  deterministically across parses, so the committed artifacts are what parity
  compares against, and `parity.sh` restores them after rebuilding the warehouse.
- **The fixture warehouse is gitignored.** `testdata/warehouse.duckdb` never
  exists on a clean checkout. dbt-ditto reads the committed `catalog.json` and is
  unaffected; dbt-osmosis asks the adapter, gets nothing and writes bare
  scaffolding. Delete the file to reproduce CI.
- **Meta key order matters** — why `dbt.OrderedMap` exists.
- **Placeholder comparison is exact.** Lower-casing it breaks
  `TestPlaceholderMatchingIsExact`.
- `Catalog.Lookup` falls back to schema plus relation name, which lets a catalog
  generated by another project still be used.
- **Folded scalars are reflowed**: a `description: >-` block comes back as one
  long line, because yaml.v3's emitter is not told to re-wrap. Nothing in the
  fixture uses them.
- **Every setting has a case in `internal/runner/settings_test.go`**, which runs
  the tool twice and requires both that the output differs and that it differs in
  the documented way; `TestEverySettingIsCovered` reflects over the config structs
  and fails if a key has no case. The README's tables are written from those
  claims.
- **`dbt_project.yml` path rules are keyed by package name.** A node's fqn starts
  with its package, so `LoadProject` strips the project's own package before
  walking. Easy to miss, because dbt resolves `+dbt-ditto-path:` into each node's
  config and `PathTemplate` checks that first.
- **`pyproject.toml` is only a config if it carries `[tool.dbt-ditto]`.** The
  upward search skips one that does not, and one that will not parse, rather than
  stopping there. A `-c` pointing at a file without the table is a hard error.
- **The TOML table is decoded to a map and re-read through the YAML decoder**
  (`internal/config/pyproject.go`), rather than a second set of `toml:` tags to
  keep in step.
- **The fixture profiles read `DBT_DITTO_DB`, and the scripts export it.** Only
  `parity.sh` and `matrix.sh` read those profiles, so `go test` is silent about a
  mismatch and it surfaces as `Parsing Error: Env var required but not provided`.
  Rename both halves together.
- **`dbt.ExtraColumnKeys` is a package-level list read during decoding**, because
  `encoding/json` gives `UnmarshalJSON` no way to be told anything. `runner.Run`
  sets it before `loadProjects`; setting it during a load would race. Empty is
  the fast path, since capturing every unknown key allocates a map per column.
- **The fixture's sources are backed by seeds**, so `Graph.ClassifySources`
  reports every one as shadowed; `ShadowedSource.Mistake()` keeps the warning off
  them. A source shadowed by a model is the real finding.
- A YAML comment on an item of the `sources` or `tables` sequence is kept by
  dbt-ditto and dropped by dbt-osmosis; `output.comments: osmosis` emulates the
  loss only inside a `columns` list. Use a `description` in the fixture.
