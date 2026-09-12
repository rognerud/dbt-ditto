# AGENTS.md — dbt-ditto

**This file is downstream of README.md and never replicates it.** README.md says
what dbt-ditto is, what it does and how it is configured; if something belongs
there, it goes there and is not repeated here. This file covers only what the
README does not: the behaviour that must not change, the internals, and the
constraints that are not visible from the code.

No development state and no decision log. The repository speaks for itself.

Further documentation: `docs/usage.md` (configuration reference),
`docs/development.md` (build, test, release), `docs/source-providers.md`
(documenting external sources from the warehouse).

## dbt-osmosis reference behaviour

Verified against dbt-osmosis 1.5 by running it on the fixture and by reading
`dbt_osmosis/core/{settings,inheritance,transforms,sync_operations}.py`. These
are the defaults `internal/config` must match, and `TestDefaultsMatchDbtOsmosis`
pins them.

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

The inheritance algorithm:

- The refactor pipeline is: inject missing columns → remove columns not in the
  database → inherit → sort → synchronise data types. Injection happens before
  inheritance, on the in-memory manifest, which is why `Node.EffectiveColumn`
  consults the catalog: an undocumented ancestor still has to claim a column.
- Ancestors are grouped into generations by a depth-first walk with one shared
  visited set, so a node reached by several routes is filed under the depth the
  walk first reached it at, not its shortest path. Generations are processed
  furthest first, each sorted by `unique_id`.
- Within a generation the first ancestor holding the column claims it. The rest
  of that generation is skipped for that column, including tags and meta.
- Upstream meta overwrites local meta for the same key. Local key order is
  preserved. Tags are an order-preserving union, local first.
- A description is inherited only when the column's own description is empty.
  The placeholder list filters upstream candidates, not the local value.
- `{{ doc() }}` is written back rendered, because the description written is the
  manifest's.

### Where behaviour diverges

Every default reproduces dbt-osmosis except `inheritance.progenitor`. The
settings themselves are documented in README.md and `docs/usage.md`; what
follows is only what the divergence means for parity.

| Divergence | Effect on parity |
| --- | --- |
| Comments inside a column list are kept, not lost | `output.comments: osmosis` reproduces the loss; `parity.sh` sets it |
| `snapshot.` dependencies are followed when walking ancestors | dbt-osmosis follows only `model.`, `seed.`, `source.` |
| `inheritance.derived` | off by default so parity holds |
| `inheritance.backfill` | off by default; dbt-osmosis cannot document a source at all |
| Warehouse comments come from `catalog.json`, not the adapter | DuckDB's adapter reports none while the catalog carries them, so `parity.sh` sets `columns.comments: never` |
| `inheritance.progenitor` is on by default | the only default that changes the bytes of an existing dbt-osmosis project |
| `inheritance.directives` | outranks name matching, `force` and a local description; unresolvable ones are warned about and left verbatim |
| `inheritance.warn_ambiguous` | stderr only, changes no output |
| `inheritance.ambiguity_meta` | off by default |
| `dbt_ditto_definitive` | a meta marker, not a setting; no effect until written |

Two consequences worth stating explicitly:

- **Any Go test comparing against `testdata/golden/osmosis` must set the same
  three options `parity.sh` does** (`output.comments: osmosis`,
  `columns.comments: never`, `inheritance.progenitor: false`), or it fails on
  the `billing` source's `order_id`.
- `parity.sh` inserts `progenitor: false` into the fixture config with `sed`,
  into the existing `inheritance:` block — a second top-level key would be a
  duplicate. The cross-project golden in `testdata/golden/dbt-ditto` does carry
  the annotation, which is what makes a cross-repository inheritance visible in
  the file.

### Derived from data, not configurable

Whether meta goes under `config:` is decided by the manifest's dbt version. A
switch could only agree with dbt or silently drop every column's meta.

A project's name comes from `dbt_project.yml` or the manifest, and is what
`selectNodes` matches `PackageName` against. `ProjectRef.Name` exists as an
internal field because a dbt-loom entry names a manifest before anything has read
it; the duplicate that can create is dropped after loading, in
`dropDuplicateManifests`.

### Struct expansion is parity, not an extension

`columns.expand_structs` defaults to on, and must. dbt-osmosis introspects
through the adapter, and adapters that understand nested data report
`profile.first_name` as a column in its own right. dbt-ditto reads
`catalog.json`, where the same thing is one column typed
`STRUCT(first_name VARCHAR, ...)`, so it parses the type to reach the same answer
(`internal/dbt/structs.go`). The expanded fields are on the dbt-osmosis side.
Turning expansion off breaks parity.

### Derived matching (internal/inherit/match.go)

Both ends of a match need aliasing, independently: the upstream column may be a
struct field, and the downstream one may be aggregated, nested, or both. A match
is therefore a pair of derivations, and `rankPairs` orders pairs by total
distance, cheapest first. Exact/exact costs zero and always wins, which keeps a
real match from being replaced by a guess.

A stripped downstream name must be looked up against the ancestor's exact names,
not the same rank's map. If `derived_test.go` fails with empty descriptions, that
is the likely cause.

### dbt-osmosis cannot run with dbt-loom

`dbt-osmosis yaml refactor` on the analytics project fails with
`AttributeError: 'LoomRunnableConfig' object has no attribute 'project_root'`.
The traceback is kept at `testdata/golden/osmosis-analytics-failure.log`. There
is no dbt-osmosis reference for the cross-project half, so the golden file there
is dbt-ditto's own output.

## Repository layout

```
cmd/dbt-ditto/        CLI
internal/config/      dbt_ditto.yml / pyproject.toml + the defaults that pin parity
internal/dbt/         streaming manifest reader, catalog, dbt_project.yml, OrderedMap
internal/inherit/     cross-project graph (generations) + resolver
internal/runner/      orchestration, selection, path templates, YAML writing
internal/sources/     source providers: contract, subprocess, cache, label routing
internal/yamlfile/    yaml.Node editing preserving comments and key order
packaging/providers/  the BigQuery and Snowflake providers, in Python
packaging/pypi/       wheel builder carrying the binary
testdata/projects/    platform (upstream) + analytics (downstream, via dbt-loom)
testdata/golden/      recorded dbt-osmosis output + dbt-ditto's cross-project output
scripts/              build-fixture.sh, parity.sh, matrix.sh, bench.sh, dist.sh
docs/                 usage, development and source-provider documentation
```

## Releasing

A release is the act of publishing a draft. `draft-release.yml` runs
[release-drafter](https://github.com/release-drafter/release-drafter) on every
merge to `main` and keeps one draft up to date: notes from the pull requests
merged since the last release, version resolved from their labels (`breaking` →
major, `enhancement`/`removal` → minor, everything else including unlabelled →
patch). Nothing is tagged until someone presses Publish, which is what creates
the tag and starts `release.yml`.

The labels are part of the machinery, so they live in `.github/labels.yml` and
are synced by `labeler.yml`; created by hand they drift from the resolver that
reads them.

The tag is created at `main`'s head, before the version bump. `release.yml`
rewrites `pyproject.toml` from the tag, commits that to `main`, then force-moves
the tag onto the bump commit; otherwise the tag points at a file claiming the
previous version and everything downstream disagrees with it. Moving a tag is
acceptable only because it happens seconds after publication, before anything has
been built from it.

| | trigger | does |
| --- | --- | --- |
| `ci.yml` | push, PR | gofmt, vet, test on Linux and macOS, race, shellcheck, provider tests, live `parity.sh --check` |
| `draft-release.yml` | push to `main`, PR labelled | release-drafter: updates the draft's notes and next version. Tags nothing |
| `labeler.yml` | `.github/labels.yml` changes | syncs the labels release-drafter reads |
| `release.yml` | draft release published | version bump + tag move, `dist.sh` archives, wheels, PyPI via trusted publishing, attaches assets |
| `scripts/check-versions.sh` | called by `release.yml` | tag vs `pyproject.toml` vs the wheel's normalised version |

PyPI publishing is configured outside the repository: trusted publishing for
`dbt-ditto` against workflow `release.yml` and environment `pypi`. No API token
is stored anywhere; the `pypi` job requests `id-token: write` and PyPI verifies
the OIDC claim. A GitHub environment called `pypi` must exist here, or the job
never runs. Nothing else needs a secret.

## Performance

`make bench`: 1.507 s for dbt-osmosis against 0.022 s for dbt-ditto on the
fixture. Synthetic scaling: 5 000 models × 60 columns in about 1.4 s, at which
point the run is bound by opening files.

What must not regress:

- Manifest decoding is a token stream that skips `macros`, `child_map`, compiled
  SQL and similar. It also records each node's position, which is the order
  entries are written back in.
- Resolve, file load and file save run through `runner.parallel`. Only the
  mutation step between them is serial, because two models can share a file.
- `Node.Column` builds a folded index once; `Catalog.Ordered`/`Folded` are
  cached. A linear scan here made resolve quadratic on wide models.
- `yamlfile.Encode` builds scalar nodes directly. `yaml.Node.Encode` stands up a
  full emitter and parser per value, and accounted for half of all allocations.
- `yamlfile.File` fingerprints the document at load, so an unchanged document is
  never serialised. This is the `--check` path.

## The version and adapter rig

Everything rests on one fact: dbt-ditto reads `manifest.json` and `catalog.json`
and nothing else. A captured pair is therefore a complete record of a dbt version
or an adapter, and testing splits in two — generating artifacts needs Python, dbt
and a warehouse and is run rarely by hand, while testing against them needs only
Go and runs on every commit. Everything crossing that line is committed.

| Rig | Generator | Artifacts | Tests |
| --- | --- | --- | --- |
| dbt versions | `scripts/matrix.sh` | `testdata/matrix/artifacts/duckdb-*` | `matrix_test.go` |
| Snowflake | `matrix.sh` via fakesnow | `…/snowflake-*` | `matrix_test.go` |
| Postgres | `matrix.sh` via Docker | `…/postgres-*` | `matrix_test.go` |
| BigQuery (matrix) | `matrix.sh`, `dbt parse` only | `…/bigquery-*` | `matrix_test.go` |
| dbt-osmosis parity | `scripts/parity.sh` | `testdata/golden/` | `golden_test.go` |
| BigQuery (deep) | hand-built from adapter source | `testdata/bigquery/` | `bigquery_test.go` |

Snowflake needs no account. `fakesnow` replaces `snowflake.connector` with a
DuckDB-backed implementation, and dbt-snowflake talks to Snowflake through
exactly that connector, so patching it before dbt starts
(`scripts/lib/dbt_fakesnow.py`) produces a real dbt run: real adapter, real
macros, real `docs generate`. The upper-cased identifiers and `NUMBER`/`TEXT`
types in the artifacts are genuine rather than guessed. Two details: fakesnow's
`db_path` is a directory, and `dbt docs generate` must be passed as two
arguments.

BigQuery cannot be run locally. `bigquery-emulator` answers `datasets`, but has
no load-job support (`dbt seed` fails with `not support sourceFormat`) and
returns incomplete job resources (`dbt run` fails in the adapter with
`NoneType object has no attribute path`). The matrix row therefore uses
`dbt parse`, which needs no warehouse and is still dbt-bigquery writing the
manifest; the catalog is the hand-built
`testdata/matrix/bigquery/catalog.json`, and `meta.json` records both facts.
`scripts/lib/dbt_fakebq.py` is kept for when the emulator improves: the Go
BigQuery client honours `BIGQUERY_EMULATOR_HOST` and the Python one does not, so
the endpoint has to be injected.

Postgres needs Docker and skips without it. The row starts a throwaway
`postgres:16-alpine` on port 55432 rather than 5432, so a local Postgres is never
touched. On macOS that means `colima start`.

Constraints encoded in `scripts/matrix.sh`:

- Pinning the adapter does not pin dbt-core: `dbt-duckdb==1.9.0` resolves
  dbt-core 1.12, because the requirement is only `dbt-core>=1.9,<2`. Entries are
  `dbt-core:dbt-duckdb:python` and all three are pinned.
- uv installs the newest Python available, and dbt 1.8 and 1.9 do not run on
  3.13 or 3.14; they fail inside mashumaro at import time.
- dbt-core 1.12 pulls `dbt-core-experimental-parser`, whose build downloads a
  47 MB binary. That fails behind a filtering proxy, so the `local` entry
  captures the repository's own `.venv` instead.
- macOS ships bash 3.2, where `"${ARR[@]}"` on an empty array is an error under
  `set -u`, hence the `${ARR[@]+"${ARR[@]}"}` idiom. `${#ARR[@]}` is fine.

## Warehouses other than DuckDB

Covering a warehouse means covering the shape of the artifacts its adapter
writes, which needs no account: read the adapter's catalog macro and its `Column`
class, then build a faithful `manifest.json` and `catalog.json` by hand.
`testdata/bigquery` does this, and its README records which adapter file each
detail came from.

- BigQuery's catalog macro joins `INFORMATION_SCHEMA.COLUMNS` to
  `COLUMN_FIELD_PATHS`, so a nested `RECORD` appears as the parent and every
  dotted leaf. DuckDB reports only the parent, with the fields inside its
  composite type. `trueColumns` therefore skips expanding any path the catalog
  already names, or BigQuery gets every nested field twice.
- `BigQueryColumn.flatten()` returns leaves only, so dbt-osmosis running against
  BigQuery never writes a bare `profile` entry while a catalog-driven tool does.
- BigQuery renders records as ``STRUCT<`field` TYPE, ...>`` and wraps `REPEATED`
  in `ARRAY<...>`.

Snowflake's `normalize_column_name` upper-cases, which dbt-ditto does not do.

## Known pitfalls

- **Entry order in a shared schema file is manifest order**, not alphabetical.
  `selectNodes` sorts by `Node.Order`. Sorting by `unique_id` breaks parity when
  two models land in the same file. dbt does not order manifest nodes
  deterministically across parses, so the committed artifacts are what parity
  compares against; `parity.sh` restores them after rebuilding the warehouse.
- **The fixture warehouse is gitignored.** `testdata/warehouse.duckdb` exists
  locally and never on a clean checkout. dbt-ditto reads the committed
  `catalog.json` and is unaffected; dbt-osmosis asks the adapter, gets nothing
  and writes bare scaffolding. No local check catches this, because no hook runs
  `parity.sh` and `go test` replays the recorded golden. Delete the file to
  reproduce CI.
- **Meta key order matters.** That is why `dbt.OrderedMap` exists;
  `map[string]any` loses it and the output stops matching.
- **Placeholder comparison is exact.** Lower-casing it breaks
  `TestPlaceholderMatchingIsExact`.
- `Catalog.Lookup` falls back to schema plus relation name, which is what lets a
  catalog generated by another project still be used.
- **Folded scalars are reflowed.** A `description: >-` block comes back as one
  long line, because yaml.v3's emitter is not told to re-wrap it. Nothing in the
  fixture uses folded scalars for that reason.
- **Every setting has a case in `internal/runner/settings_test.go`**, which runs
  the tool twice, default and changed, and requires both that the output differs
  and that it differs in the documented way. `TestEverySettingIsCovered` reflects
  over the config structs and fails if a key has no case. The README's tables are
  written from those claims; changing one means changing the other.
- **`dbt_project.yml` path rules are keyed by package name.** The keys under
  `models:` are packages and a node's fqn starts with its package, so
  `LoadProject` strips the project's own package before walking. Without that the
  fallback matches nothing, which is easy to miss because dbt resolves
  `+dbt-ditto-path:` into each node's config and `PathTemplate` checks that
  first.
- **`pyproject.toml` is only a config if it carries `[tool.dbt-ditto]`.** The
  upward search skips one that does not, and one that will not parse, rather than
  stopping there; otherwise any Python project's packaging file would shadow the
  real config further up, and an unrelated TOML syntax error would surface as
  this tool failing. A `-c` pointing at a file without the table is a hard error,
  because that is someone asking for it by name.
- **The TOML table is decoded to a map and re-read through the YAML decoder**
  (`internal/config/pyproject.go`), rather than giving the config structs a
  second set of `toml:` tags that would have to be kept in step.
- **The fixture profiles read `DBT_DITTO_DB`, and the scripts export it.** Only
  `parity.sh` and `matrix.sh` read those profiles, and both need dbt, so `go
  test` is silent about a mismatch and it surfaces as
  `Parsing Error: Env var required but not provided` the first time CI runs one
  of them. Rename both halves together.
- **`dbt.ExtraColumnKeys` is a package-level list read during decoding.** It has
  to be: `encoding/json` gives an `UnmarshalJSON` method no way to be told
  anything. `runner.Run` sets it before `loadProjects`; setting it during a load
  would race, because projects are read in parallel. Empty is the fast path and
  the default, since capturing every unknown key allocates a map per column.
- **The fixture's sources are backed by seeds**, so `Graph.ClassifySources`
  reports every one as shadowed. `ShadowedSource.Mistake()` keeps the warning off
  them; a source shadowed by a model is the real finding.
- A YAML comment attached to an item of the `sources` or `tables` sequence is
  kept by dbt-ditto and dropped by dbt-osmosis; `output.comments: osmosis` only
  emulates the loss inside a `columns` list. Use a `description` rather than a
  comment on source list items in the fixture.
