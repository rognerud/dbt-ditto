# AGENTS.md — dbt-ditto

Only the behaviour that must not change, the internals, and constraints not
visible from the code. No development state, no decision log.

User docs: `docs/`. Build, test and release: `CONTRIBUTING.md`.

## dbt-osmosis reference behaviour

Verified against dbt-osmosis 1.5 on the fixture and from
`dbt_osmosis/core/*.py`, pinned by `TestDefaultsMatchDbtOsmosis`.

| Behaviour | dbt-osmosis |
| --- | --- |
| `data_type` written | yes |
| Column name / data type case | preserved |
| Column order | catalog order |
| Missing columns added, stale removed | yes, yes |
| meta / tags placement | `config.meta` / `config.tags` when the manifest's dbt >= 1.9.6 |
| Placeholders | `""`, `Pending further documentation`, `No description for this column`, `Not documented`, `Undefined`; exact compare, no trim or case fold |
| Node-level description inheritance | none |
| Progenitor annotation | off; key `osmosis_progenitor`. dbt-ditto defaults it on |
| Empty descriptions written | no |

Resolution order is in `docs/usage.md#how-inheritance-resolves`. Not there:

- Pipeline: inject missing columns → remove columns not in the database →
  inherit → sort → synchronise data types. Injection happens before inheritance,
  on the in-memory manifest, which is why `Node.EffectiveColumn` consults the
  catalog: an undocumented ancestor still has to claim a column.
- A generation is skipped for a column once claimed, tags and meta included.
  Tags are an order-preserving union, local first.
- The placeholder list filters upstream candidates, not the local value.
- `{{ doc() }}` is written back rendered, as the manifest has it.

### Parity constraints

Users see the divergences in `docs/usage.md#differences-from-dbt-osmosis`. What
matters here:

- Only `inheritance.progenitor` changes the bytes of an existing dbt-osmosis
  project. Everything else defaults off or writes nothing.
- Any Go test comparing against `testdata/golden/osmosis` must set the three
  options `parity.sh` does (`output.comments: osmosis`, `columns.comments:
  never`, `inheritance.progenitor: false`), or it fails on the `billing` source's
  `order_id`. `parity.sh` `sed`s `progenitor: false` into the fixture config's
  existing `inheritance:` block, since a second top-level key would duplicate it.
- `columns.expand_structs` must stay on. dbt-osmosis introspects through the
  adapter, which reports `profile.first_name` as a column in its own right; in
  `catalog.json` that is one column typed `STRUCT(...)`, so
  `internal/dbt/structs.go` parses the type to reach the same answer.
- dbt-osmosis crashes on the analytics project
  ([CONTRIBUTING.md](CONTRIBUTING.md#proof-that-it-matches-dbt-osmosis)), so the
  golden file for the cross-project half is dbt-ditto's own output.

### Derived from data, not configurable

- Whether meta goes under `config:` is decided by the manifest's dbt version. A
  switch could only disagree with dbt and drop every column's meta.
- A project's name comes from `dbt_project.yml` or the manifest, and is what
  `selectNodes` matches `PackageName` against. `ProjectRef.Name` exists because a
  loom entry names a manifest before anything has read it; the duplicate that can
  create is dropped in `dropDuplicateManifests`.

### Derived matching (internal/inherit/match.go)

Both ends alias independently, so a match is a pair of derivations; `rankPairs`
orders them by total distance and exact/exact costs zero, so a real match is
never replaced by a guess. A stripped downstream name must be looked up against
the ancestor's exact names, not the same rank's map — the likely cause if
`derived_test.go` fails with empty descriptions.

## Repository layout

```
internal/config/      config + loom discovery, and the defaults that pin parity
internal/dbt/         streaming manifest reader, catalog, dbt_project.yml, OrderedMap
internal/inherit/     cross-project graph (generations) + resolver
internal/runner/      orchestration, selection, path templates, YAML writing
internal/sources/     source providers: contract, subprocess, cache, label routing
internal/yamlfile/    yaml.Node editing preserving comments and key order
internal/canon/       sorts a schema file's named entries, so two trees compare
                      without comparing dbt's node order
features/             Gherkin specifications (steps in internal/features/)
testdata/projects/    platform (upstream) + analytics (downstream, via dbt-loom)
testdata/golden/      recorded dbt-osmosis output + dbt-ditto's cross-project output
```

## Source providers

Behaviour and settings: `docs/source-providers.md`. Contract:
`packaging/providers/README.md`. Testing:
[CONTRIBUTING.md](CONTRIBUTING.md#source-providers-tested-without-an-account).
Code: `internal/sources/` (contract, subprocess, cache, label routing) and
`sources.go` in `internal/inherit` and `internal/runner` (classification, the
shadowed-source warning).

- A subprocess, not a Go interface: `buildmode=plugin` is unusable across the
  platforms this ships to, the binary keeps its two pure-Go dependencies and its
  "connects to nothing" property, and providers can be written in Python. The
  contract is the shape of dbt's YAML, not warehouse vocabulary, which would
  break on Snowflake, Unity Catalog and Postgres alike.
- Provider output becomes a `dbt.CatalogNode` (`sources.Apply`), not a second
  source of column truth: injection, stale removal, ordering and struct expansion
  all read a catalog. What a catalog has no room for goes onto the source's
  manifest columns, which is what inheritance reads. It seeds
  `columnTruth.comment` in `resolve.go`, the same rung as a warehouse comment.
- `propagate.column: false` adds the label meta key to `SkipMetaKeys`, which
  inheritance already consults. Hence labels nesting under a key by default:
  flattened into meta they are indistinguishable from meta somebody typed, and
  the propagation rules stop applying.

## Releasing

A release is publishing the draft
[release-drafter](https://github.com/release-drafter/release-drafter) maintains
on `main`, which creates the tag and starts `release.yml`. The version comes from
merged PR labels, hence `.github/labels.yml`.

The tag is created at `main`'s head, before the version bump, so `release.yml`
rewrites `pyproject.toml` from the tag, commits that to `main`, then force-moves
the tag onto the bump commit. Otherwise the tag points at a file claiming the
previous version. Moving a tag is acceptable only seconds after publication.

PyPI uses trusted publishing, configured outside the repository, for `dbt-ditto`
against workflow `release.yml` and environment `pypi`. No token is stored; the
`pypi` job requests `id-token: write`. A GitHub environment called `pypi` must
exist here or the job never runs.

## Performance

Numbers: [CONTRIBUTING.md](CONTRIBUTING.md#speed). What must not regress:

- Manifest decoding is a token stream that skips `macros`, `child_map`, compiled
  SQL and similar, recording each node's position — the order entries are written
  back in.
- Resolve, file load and file save run through `runner.parallel`. Only the
  mutation between them is serial, because two models can share a file.
- `Node.Column` builds a folded index once; `Catalog.Ordered`/`Folded` are
  cached. A linear scan here made resolve quadratic on wide models.
- `yamlfile.Encode` builds scalar nodes directly. `yaml.Node.Encode` stands up a
  full emitter and parser per value, half of all allocations.
- `yamlfile.File` fingerprints the document at load, so an unchanged one is never
  serialised. This is the `--check` path.

## The version and adapter rig

dbt-ditto reads `manifest.json` and `catalog.json` and nothing else, so a
captured pair is a complete record of a dbt version or an adapter. Capture needs
Python, dbt and a warehouse and is run by hand; replay needs only Go. Everything
crossing that line is committed. Why each rig exists is in
[`testdata/matrix/README.md`](testdata/matrix/README.md) and
[`testdata/bigquery/README.md`](testdata/bigquery/README.md). Not recorded there:

- `trueColumns` skips expanding any path the catalog already names. BigQuery's
  catalog macro reports a nested `RECORD` as the parent *and* every dotted leaf,
  DuckDB only the parent; without that BigQuery gets every field twice.
- Snowflake's `normalize_column_name` upper-cases; dbt-ditto does not.
- dbt-core 1.12 pulls `dbt-core-experimental-parser`, whose build downloads a
  47 MB binary and fails behind a filtering proxy, hence the `local` entry
  capturing the repository's own `.venv`.
- In `matrix.sh`: fakesnow's `db_path` is a directory, `dbt docs generate` must
  be passed as two arguments, and macOS bash 3.2 errors on `"${ARR[@]}"` for an
  empty array under `set -u`, hence `${ARR[@]+"${ARR[@]}"}`.

## Known pitfalls

- **Entry order in a shared schema file is manifest order**, not alphabetical;
  `selectNodes` sorts by `Node.Order`. dbt does not order manifest nodes
  deterministically across parses, so parity compares against the committed
  artifacts, and `parity.sh` restores them after rebuilding the warehouse.
- **The fixture warehouse is gitignored.** `testdata/warehouse.duckdb` never
  exists on a clean checkout. dbt-ditto reads the committed `catalog.json` and is
  unaffected; dbt-osmosis asks the adapter, gets nothing and writes bare
  scaffolding. Delete the file to reproduce CI.
- **Meta key order matters** (`dbt.OrderedMap`), and **placeholder comparison is
  exact** (lower-casing it breaks `TestPlaceholderMatchingIsExact`).
- **Folded scalars are reflowed.** A `description: >-` block comes back as one
  long line; yaml.v3's emitter is not told to re-wrap.
- **Every setting has a case in `internal/runner/settings_test.go`**, which runs
  the tool twice and requires that the output differs, and in the documented way.
  `TestEverySettingIsCovered` fails if a config key has no case, and
  `docs/configuration.md` is written from those claims.
- **`dbt_project.yml` path rules are keyed by package name.** A node's fqn starts
  with its package, so `LoadProject` strips the project's own package before
  walking. Easy to miss, since dbt resolves `+dbt-ditto-path:` into each node's
  config and `PathTemplate` checks that first.
- **`pyproject.toml` is only a config if it carries `[tool.dbt-ditto]`.** The
  upward search skips one that does not, and one that will not parse, rather than
  stopping there; a `-c` pointing at a file without the table is a hard error.
  The table is decoded to a map and re-read through the YAML decoder
  (`config/pyproject.go`), not a second set of `toml:` tags.
- **The fixture profiles read `DBT_DITTO_DB`, and the scripts export it.** Only
  `parity.sh` and `matrix.sh` read those profiles, so `go test` is silent about a
  mismatch; it surfaces as `Parsing Error: Env var required but not provided`.
- **`dbt.ExtraColumnKeys` is a package-level list read during decoding**, because
  `encoding/json` gives `UnmarshalJSON` no way to be told anything. `runner.Run`
  sets it before `loadProjects`; setting it during a load would race. Empty is
  the fast path: capturing every unknown key allocates a map per column.
- **The fixture's sources are backed by seeds**, so `Graph.ClassifySources`
  reports every one as shadowed and `ShadowedSource.Mistake()` keeps the warning
  off them. A source shadowed by a model is the real finding.
- `Catalog.Lookup` falls back to schema plus relation name, so a catalog from
  another project is still usable.
- A YAML comment on a `sources` or `tables` item is kept by dbt-ditto and dropped
  by dbt-osmosis; `output.comments: osmosis` emulates the loss only inside a
  `columns` list, so use a `description` in the fixture instead.
