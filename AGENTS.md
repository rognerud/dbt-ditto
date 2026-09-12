# AGENTS.md — dbt-ditto

Working notes for any agent (or human) picking this repo up. Read this first; it
records the goal, the decisions already made, and the traps, so none of it has
to be re-derived. README.md is the front door (what this is, who owns it, scope);
`.agents/usage.md` holds the user-facing configuration reference,
`.agents/development.md` the build, test, parity and release detail, and
`.agents/source-providers.md` the design for documenting external sources from
the warehouse.

## Goal

A Go replacement for **dbt-osmosis** that also works across project boundaries:

1. dbt-osmosis' YAML management and inheritance, reproduced exactly.
2. Cross-project inheritance for projects that use **dbt-loom**. This is not a
   loom replacement: loom's runtime job (injecting upstream nodes into the
   manifest dbt is parsing, so cross-project `ref()` compiles) stays loom's.
   There is no plugin here, no manifest is ever written, and nothing is fetched
   over the network. Upstreams arrive three ways: already injected into the
   downstream manifest by loom, discovered from `dbt_loom.config.yml`
   (`type: file` entries only — see `internal/config/loom.go`), or listed in
   `dbt_ditto.yml` as `path:` / `manifest:`.
3. A real DuckDB two-project test fixture.
4. An automated proof of parity against the real dbt-osmosis.
5. Benchmarks, and it has to be fast.

All five are done. Status is at the bottom.

## Environment quirks (these will bite)

- Commands run in a sandbox. `TMPDIR` must point inside the repo:
  `export TMPDIR=$PWD/.gocache/tmp`. Shell heredocs fail without it.
- Go must run vendored with a local cache:
  `export GOFLAGS=-mod=vendor GOCACHE=$PWD/.gocache/go-build`.
  The `go: telemetry upload token` warning on stderr is harmless.
- `dbt-osmosis` writes logs to `$HOME/.dbt-osmosis`; set `HOME` to a scratch dir
  before invoking it or it dies on startup.
- The `Makefile` sets all of this. Prefer `make build|test|race|parity|bench`.

## dbt-osmosis reference behaviour

Verified against dbt-osmosis 1.5 by running it for real on the fixture, and by
reading `dbt_osmosis/core/{settings,inheritance,transforms,sync_operations}.py`.
These are the defaults `internal/config` must keep matching, and
`TestDefaultsMatchDbtOsmosis` pins them.

| Behaviour | dbt-osmosis |
| --- | --- |
| `data_type` written | yes |
| Column name / data type case | preserved |
| Column order | catalog order |
| Missing columns added, stale removed | yes, yes |
| meta / tags placement | `config.meta` / `config.tags` when the manifest's dbt >= 1.9.6 |
| Placeholders | `""`, `Pending further documentation`, `No description for this column`, `Not documented`, `Undefined`; compared **exactly**, no trim, no case fold |
| Node-level description inheritance | none |
| Progenitor annotation | off; key `osmosis_progenitor` — **dbt-ditto defaults this on**, see divergence 6 |
| Empty descriptions written | no |

The inheritance algorithm, which is the part that is easy to get subtly wrong:

- The refactor pipeline is: inject missing columns → remove columns not in the
  database → **inherit** → sort → synchronise data types. Injection happens
  *before* inheritance, on the in-memory manifest. That is why
  `Node.EffectiveColumn` consults the catalog: an undocumented ancestor still
  has to claim a column.
- Ancestors are grouped into generations by a **depth-first** walk with one
  shared visited set, so a node reached by several routes is filed under the
  depth the walk first reached it at — not its shortest path. Generations are
  processed **furthest first** and each is sorted by `unique_id`.
- Within a generation the first ancestor holding the column claims it; the rest
  of that generation is skipped entirely for that column, tags and meta included.
- Upstream meta **overwrites** local meta for the same key. Local key order is
  preserved. Tags are an order-preserving union, local first.
- A description is inherited only when the column's own description is empty.
  The placeholder list filters *upstream candidates*, not the local value.
- `{{ doc() }}` is written back rendered, because the description written is the
  manifest's.

### Known divergences (deliberate, documented, switchable)

1. Comments inside a column list. dbt-osmosis loses all but the first;
   dbt-ditto keeps them. `output.comments: osmosis` reproduces the loss and is
   what `scripts/parity.sh` uses so the diff is like for like.
2. dbt-ditto follows `snapshot.` dependencies when walking ancestors;
   dbt-osmosis only follows `model.`, `seed.` and `source.`.
3. `inheritance.derived` (default **off**) matches a column to an upstream one
   it no longer shares a name with. Off by default precisely so parity holds.
4. `inheritance.backfill` (default **off**) documents a source from its
   descendants. dbt-osmosis cannot do this; sources are DAG roots.
5. Warehouse comments. Both tools turn a column's `COMMENT` into its
   description — dbt-osmosis via the adapter, dbt-ditto via `catalog.json`.
   The DuckDB adapter reports no comments while the catalog carries them, so
   `scripts/parity.sh` sets `columns.comments: never`. **Any Go test comparing
   against `testdata/golden/osmosis` must set the same three options the harness
   does**, or it will fail on the `billing` source's `order_id`.
6. `inheritance.progenitor` defaults **on**, unlike everything else here: an
   inherited description records the node it came from in `meta`. This is the
   only default that changes the bytes an existing dbt-osmosis project ends up
   with, so `scripts/parity.sh` inserts `progenitor: false` into the fixture
   config (with `sed`, into the existing `inheritance:` block — a second
   top-level key would be a duplicate) and `TestParityWithDbtOsmosis` sets the
   same. The cross-project golden in `testdata/golden/dbt-ditto` *does* carry
   the annotation, which is what makes a cross-repository inheritance visible in
   the file itself.
7. `inheritance.directives` (default **on**) resolves an explicit
   `description: "Inherited: node.column"` pointer, the syntax
   [dbt-doc-inherit](https://github.com/tripleaceme/dbt-doc-inherit) uses. It
   outranks name matching, `force` and a local description, because it is the
   analyst stating the answer. An unresolvable one is warned about and left in
   the file verbatim. On by default is safe: such a string is not prose, and
   dbt-osmosis would have written it back unchanged anyway.
8. `inheritance.warn_ambiguous` (default **on**) reports a column that several
   ancestors *in the same generation* document differently — where the winner is
   the lowest `unique_id` and therefore arbitrary. Cross-generation overrides are
   not ambiguity and are never reported. Warnings go to stderr and change no
   output, so this cannot break parity.
9. `inheritance.ambiguity_meta` (default **off**) records that same disagreement
   in the column's meta, under `ambiguity_key`, so it survives the terminal
   scrolling. Independent of the warning: either can be on without the other.
10. `dbt_ditto_definitive` (`internal/inherit/definitive.go`) is a meta marker,
    not a setting. It declares a column's description settled, applies it to
    every column of that name in any direction, locks the declaring column, and
    fails the run when two declarations disagree. It has no effect until someone
    writes the marker, so parity holds for projects that never do.

### Settings that are not settings

Two things are derived from data and therefore have no config key: whether meta
goes under `config:` (the manifest's dbt version decides; offering a switch could
only agree with dbt or silently drop every column's meta) and a project's name
(`dbt_project.yml` or the manifest decides, and it is what `selectNodes` matches
`PackageName` against). `ProjectRef.Name` still exists as an internal field
because a dbt-loom entry names a manifest before anything has read it; the
duplicate it can create is dropped after loading, in `dropDuplicateManifests`.

### Struct expansion is parity, not an extension

`columns.expand_structs` defaults to **on**, and must. dbt-osmosis introspects
the warehouse through the adapter, and adapters that understand nested data
report `profile.first_name` as a column in its own right, so dbt-osmosis writes
an entry for it. dbt-ditto reads `catalog.json`, where the same thing is one
column with the type `STRUCT(first_name VARCHAR, ...)`, so it has to parse the
type to reach the same answer. `internal/dbt/structs.go` does that.

This was found by misreading a diff in the right direction eventually: the
expanded fields were on the *dbt-osmosis* side. Do not "fix" it by turning
expansion off.

### Derived matching (internal/inherit/match.go)

Both ends of a match need aliasing and independently: the upstream column may
itself be a struct field, and the downstream one may be aggregated, nested, or
both. So a match is a *pair* of derivations, and `rankPairs` orders the pairs by
total distance, cheapest first. Exact/exact costs zero and therefore always
wins, which is the property that keeps a real match from being replaced by a
guess.

The first attempt looked each rank's candidate keys up only in the *same* rank's
map, which cannot work: a stripped downstream name has to be looked up against
the ancestor's *exact* names. If the tests in `derived_test.go` start failing
with empty descriptions, that is the mistake to look for.

### dbt-osmosis cannot run with dbt-loom

`dbt-osmosis yaml refactor` on the analytics project crashes with
`AttributeError: 'LoomRunnableConfig' object has no attribute 'project_root'`.
The traceback is kept at `testdata/golden/osmosis-analytics-failure.log`. There
is therefore no dbt-osmosis reference for the cross-project half; the golden
file there is dbt-ditto' own output.

## Repo layout

```
cmd/dbt-ditto/       CLI
internal/config/      dbt_ditto.yml / pyproject.toml + the defaults that pin parity
internal/dbt/         streaming manifest reader, catalog, dbt_project.yml, OrderedMap
internal/inherit/     cross-project graph (generations) + resolver
internal/runner/      orchestration, selection, path templates, YAML writing
internal/sources/     source providers: contract, subprocess, cache, label routing
internal/yamlfile/    yaml.Node editing preserving comments and key order
packaging/providers/  the BigQuery and Snowflake providers, in Python
testdata/projects/    platform (upstream) + analytics (downstream, via dbt-loom)
testdata/golden/      recorded dbt-osmosis output + dbt-ditto' cross-project output
packaging/dbt-ditto/  the dbt package: run-operation macros + run.py launcher
packaging/pypi/       wheel builder carrying the binary
scripts/              build-fixture.sh, parity.sh, bench.sh, allow-sandbox.sh
```

## The dbt package

`packaging/dbt-ditto` is installable with `dbt deps` (`git:` + `subdirectory:`, or via
the hub once mirrored — see its README). Its hard constraint, which no amount of
cleverness gets around: **dbt's Jinja cannot open a file or execute a program**.
So the split is:

- `dbt_ditto_suggest` — pure Jinja over dbt's `graph`. Prints the YAML that
  would be inherited, plus directive and ambiguity warnings. Writes nothing.
  It duplicates the resolver's logic in Jinja, deliberately: it must keep
  working with no binary installed. It is a *preview*; the binary is the
  authority, and knows more (catalog, cross-project, column add/remove).
- `dbt_ditto_install` — prints the launcher command with the given args.
- `run.py` — finds `dbt-ditto` on `PATH`, else `uvx dbt-ditto`, else explains
  how to install. Runs from the project root, two levels above itself.

`make dbt-package` (`scripts/dbt-package.sh`) installs it into a scratch copy of
the platform fixture with a real `dbt deps`, appends two directive columns (one
resolvable, one not), runs both operations and greps the output. Needs `.venv`.
It is the only test of the macros: Go cannot reach them.

## Publishing

**A release is publishing a draft.** `draft-release.yml` runs
[release-drafter](https://github.com/release-drafter/release-drafter) on every
merge to `main`, keeping a draft release up to date: its notes come from the
pull requests merged since the last release, and its version from their labels
(`breaking` → major, `enhancement`/`removal` → minor, everything else including
unlabelled → patch, per `.github/release-drafter.yml`).

Nothing is tagged until someone presses Publish. That is what creates the tag,
and the tag is what `release.yml` turns into archives, wheels, a PyPI upload and
the mirror. So `main` is always releasable and never accidentally released, and
the release notes are written continuously rather than remembered at the end.

The labels are part of the machinery, not decoration, which is why they live in
`.github/labels.yml` and are synced by `labeler.yml` rather than being created
by hand and drifting from the resolver that reads them.

Two ordering facts that the workflows exist to work around:

- **The tag is created at `main`'s head, before the version bump.** `release.yml`
  rewrites `pyproject.toml` and `dbt_project.yml` from the tag, commits that to
  `main`, and then **force-moves the tag onto the bump commit** — otherwise the
  tag points at files claiming the previous version and everything downstream
  that checks out the tag disagrees with it. Moving it is safe only because it
  happens seconds after publication and before anything has been built.
- **A tag created by GitHub's own token does not fire `on: push: tags`.** So the
  mirror is a `workflow_call` invoked by `release.yml` rather than a
  tag-triggered workflow, which would silently never run.

The workflows:

| | trigger | does |
| --- | --- | --- |
| `ci.yml` | push, PR | gofmt, vet, test on Linux and macOS, race, shellcheck, provider tests, `dbt-package.sh`, live `parity.sh --check` |
| `draft-release.yml` | push to `main`, PR labelled | release-drafter: updates the draft's notes and next version. Tags nothing |
| `labeler.yml` | `.github/labels.yml` changes | syncs the labels release-drafter reads |
| `release.yml` | draft release **published** | version bump + tag move, `dist.sh` archives, wheels, PyPI via trusted publishing, attaches assets, calls the mirror |
| `mirror-dbt-ditto.yml` | called by `release.yml` | copies `packaging/dbt-ditto/` to the root of `rognerud/dbt_ditto` and tags it |
| `scripts/check-versions.sh` | called by both | tag vs `pyproject.toml` vs `dbt_project.yml` vs the wheel's normalised version |

**Order matters on the first release.** `run.py` falls back to
`uvx dbt-ditto`, so a dbt-ditto listing that lands before the wheel exists on
PyPI sends every user without the binary straight to an install hint. Publish
PyPI first, then open the hubcap PR.

Setup that cannot be done from inside the repository, and which the workflows
assume:

1. **PyPI trusted publishing** for `dbt-ditto`: workflow `release.yml`,
   environment `pypi`. No API token is stored anywhere; the `pypi` job requests
   `id-token: write` and PyPI verifies the OIDC claim.

   For the *first* release this has to be registered as a **pending publisher**,
   since the project does not exist on PyPI yet and the ordinary form is
   configured from a project's settings page. A GitHub environment called `pypi`
   has to exist on this repository too, or the job never runs.
2. **The mirror repo** `rognerud/dbt_ditto`, empty and public, plus a
   fine-grained PAT with Contents: read+write on it, stored as the secret
   `MIRROR_TOKEN`. `GITHUB_TOKEN` cannot push across repositories.

   The underscore is load-bearing twice over: dbt packages are named with one,
   and the mirror cannot be `rognerud/dbt-ditto` because that is this
   repository. An earlier draft of the workflow said `gislerognerud/dbt-ditto`,
   which is neither the right owner nor a name that can exist.
3. **The hubcap PR**: add `"rognerud": ["dbt_ditto"]` to `hub.json` in
   [dbt-labs/hubcap](https://github.com/dbt-labs/hubcap). Reviewed by a human at
   dbt Labs; indexing is hourly afterwards, so later releases need only a tag.

The mirror is deliberately a *derived* artefact: the whole tree is replaced on
each release, so a file deleted here disappears there. Never commit to the
mirror by hand.

## Performance notes

`make bench`: 1.507 s for dbt-osmosis vs 0.022 s for dbt-ditto on the fixture
(68×). Synthetic scaling: 5 000 models × 60 columns in ~1.4 s, at which point
the run is bound by opening files.

What matters, and why it must not regress:

- Manifest decoding is a **token stream** that skips `macros`, `child_map`,
  compiled SQL and so on. It also records each node's position, which is the
  order entries are written back in (see below).
- Resolve, file load, and file save all run through `runner.parallel`. Only the
  mutation step between them is serial, because two models can share a file.
- `Node.Column` builds a folded index once; `Catalog.Ordered`/`Folded` are
  cached. A linear scan here made the resolve step quadratic on wide models.
- `yamlfile.Encode` builds scalar nodes by hand. `yaml.Node.Encode` stands up a
  full emitter and parser per value and was half of all allocations.
- `yamlfile.File` fingerprints the document at load; an unchanged document is
  never serialised. This is the `--check` path.

## The version / adapter rig

The whole approach rests on one fact: **dbt-ditto reads manifest.json and
catalog.json and nothing else.** A captured pair is therefore a complete record
of a dbt version or an adapter, and testing splits cleanly in two:

- generating artifacts needs Python, dbt and a warehouse — run rarely, by hand
- testing against artifacts needs only Go — runs on every commit

Everything that crosses that line is committed. Three rigs use the pattern:

| Rig | Generator | Artifacts | Tests |
| --- | --- | --- | --- |
| dbt versions | `scripts/matrix.sh` | `testdata/matrix/artifacts/duckdb-*` | `matrix_test.go` |
| Snowflake | `scripts/matrix.sh` via fakesnow | `testdata/matrix/artifacts/snowflake-*` | `matrix_test.go` |
| Postgres | `scripts/matrix.sh` via Docker | `testdata/matrix/artifacts/postgres-*` | `matrix_test.go` |
| BigQuery (matrix) | `scripts/matrix.sh`, `dbt parse` only | `testdata/matrix/artifacts/bigquery-*` | `matrix_test.go` |
| dbt-osmosis parity | `scripts/parity.sh` | `testdata/golden/` | `golden_test.go` |
| BigQuery | hand-built from adapter source | `testdata/bigquery/` | `bigquery_test.go` |

**Snowflake needs no account.** `fakesnow` replaces `snowflake.connector` with a
DuckDB-backed implementation, and dbt-snowflake talks to Snowflake through
exactly that connector, so patching it before dbt starts
(`scripts/lib/dbt_fakesnow.py`) yields a real dbt run — real adapter, real
macros, real `docs generate`. The artifacts are the adapter's own work, so their
upper-cased identifiers and `NUMBER`/`TEXT` types are genuine rather than
guessed. Two gotchas: fakesnow's `db_path` is a *directory*, and `dbt docs
generate` must be passed as two arguments.

**BigQuery cannot be run locally, and this was tried properly.** Colima was up,
`bigquery-emulator` was pulled and `scripts/lib/dbt_fakebq.py` points the Python
client at it (the Go client honours `BIGQUERY_EMULATOR_HOST`; the Python one
does not, so the endpoint has to be injected). The emulator accepts connections
and answers `datasets`, but:

- it has no load-job support, so `dbt seed` fails with `not support sourceFormat`
- `dbt run` dies inside the adapter with `NoneType object has no attribute path`,
  because the job resources it returns are incomplete

So the BigQuery matrix row uses `dbt parse`, which needs no warehouse at all and
is still dbt-bigquery writing the manifest; the catalog is the hand-built
`testdata/matrix/bigquery/catalog.json`. `meta.json` records both facts. Deep
BigQuery coverage stays in `testdata/bigquery`. The wrapper is kept for when the
emulator improves.

**Postgres needs Docker and skips without it.** The row starts a throwaway
`postgres:16-alpine` on port 55432 (not 5432, so a local Postgres is never
touched), and prints a skip line when no daemon is reachable. On macOS that
means `colima start`.

Two bash traps in `scripts/matrix.sh`, both hit while adding these rows: macOS
ships bash 3.2, where `"${ARR[@]}"` on an *empty* array is an error under
`set -u` — hence the `${ARR[@]+"${ARR[@]}"}` idiom around the optional adapter
loops. `${#ARR[@]}` on an empty array is fine.

Traps found the hard way while building the matrix, all encoded in the script:

- Pinning the adapter does **not** pin dbt-core: `dbt-duckdb==1.9.0` resolves
  dbt-core 1.12, because the requirement is only `dbt-core>=1.9,<2`. Entries are
  `dbt-core:dbt-duckdb:python` and all three are pinned.
- uv installs the newest Python available, and dbt 1.8/1.9 do not run on 3.13 or
  3.14 — they die inside mashumaro at import time.
- dbt-core 1.12 pulls `dbt-core-experimental-parser`, whose build downloads a
  47 MB binary. That fails behind a filtering proxy; the `local` entry captures
  the repo's own `.venv` instead.

The matrix immediately earned its keep: it caught that `LoadCatalog` had no gzip
support while `LoadManifest` did, so a `.gz` catalog silently failed to load and
`LoadProject` swallowed the error, leaving a run that documented almost nothing.
Both are fixed — a catalog that exists but cannot be read is now a hard error.

## Warehouses other than DuckDB

dbt-ditto never connects to a warehouse, so covering a warehouse means covering
the shape of the artifacts its adapter writes. That needs no account and no
credentials: read the adapter's catalog macro and its `Column` class, then build
a faithful `manifest.json` / `catalog.json` by hand.

`testdata/bigquery` does this for BigQuery; its README records which adapter
file each detail came from. The important discovery, which a guessed fixture
would have missed:

- BigQuery's catalog macro joins `INFORMATION_SCHEMA.COLUMNS` to
  `COLUMN_FIELD_PATHS`, so a nested `RECORD` appears in `catalog.json` as the
  **parent and every dotted leaf**. DuckDB reports only the parent, with the
  fields inside its composite type. `trueColumns` therefore skips expanding any
  path the catalog already names, or BigQuery gets every nested field twice.
- `BigQueryColumn.flatten()` returns **leaves only**, so dbt-osmosis running
  live against BigQuery never writes a bare `profile` entry, while a
  catalog-driven tool does. That divergence is real and currently unaddressed.
- BigQuery renders record types as ``STRUCT<`field` TYPE, ...>`` with
  backtick-quoted field names, and wraps `REPEATED` in `ARRAY<...>`.

Snowflake and Databricks deserve the same treatment; Snowflake in particular
because `normalize_column_name` upper-cases for it, which dbt-ditto does not do
at all.

## Traps

- **Entry order in a shared schema file is manifest order**, not alphabetical.
  `selectNodes` sorts by `Node.Order`. Sorting by `unique_id` breaks parity when
  two models land in the same file.
- **Meta key order is load-bearing.** That is why `dbt.OrderedMap` exists;
  `map[string]any` loses it and the output stops matching.
- Placeholder comparison is exact. Lower-casing it breaks
  `TestPlaceholderMatchingIsExact`.
- `Catalog.Lookup` falls back to schema + relation name, which is what lets a
  catalog generated by another project still be used.
- **Folded scalars are reflowed.** A `description: >-` block comes back as one
  long line, because yaml.v3's emitter is not told to re-wrap it. Nothing in the
  fixture uses folded scalars any more, for that reason. Worth fixing if users
  hit noisy diffs.
- **Every setting has a case in `internal/runner/settings_test.go`**, which runs
  the tool twice — default and changed — and requires both that the output
  differs and that it differs in the documented way. `TestEverySettingIsCovered`
  reflects over the config structs and fails if a key has no case, so a new
  setting cannot be added without one. The README's tables are written from those
  claims; change one and change the other.
- **`dbt_project.yml` path rules are keyed by package name.** The keys under
  `models:` are packages and a node's fqn starts with its package, so
  `LoadProject` strips the project's own package before walking. Without that the
  fallback matched nothing, and it went unnoticed because dbt resolves
  `+dbt-ditto-path:` into each node's config, which `PathTemplate` checks first.
- **`pyproject.toml` is only a config if it carries `[tool.dbt-ditto]`.** The
  upward search skips one that does not, and one that will not parse at all,
  rather than stopping there — otherwise any Python project's packaging file
  would shadow the real config further up, and an unrelated TOML syntax error
  would surface as this tool failing. A `-c` pointing at one without the table is
  a hard error, because that is someone asking for it by name.
- **The TOML table is decoded to a map and re-read through the YAML decoder**
  (`internal/config/pyproject.go`). Giving the config structs a second set of
  `toml:` tags would be two sets to keep in step, and the one that drifts is the
  one nobody is testing.
- **The fixture profiles read `DBT_DITTO_DB`, and the scripts export it.** This
  project used to be called `loomsmosis`, and the three `profiles.yml` files
  under `testdata/` were still asking for `LOOMSMOSIS_DB` long after every
  script had been renamed to export `DBT_DITTO_DB`. Nothing caught it because
  the only things that read those profiles — `dbt-package.sh`, `parity.sh`,
  `matrix.sh` — need dbt, so `go test` is silent about it and the failure only
  appears the first time CI runs one of them:
  `Parsing Error: Env var required but not provided`. Rename both halves
  together.
- **`dbt.ExtraColumnKeys` is a package-level list read during decoding.** It has
  to be, because `encoding/json` gives an `UnmarshalJSON` method no way to be
  told anything. `runner.Run` sets it before `loadProjects`; setting it during a
  load would race, since projects are read in parallel. Empty is the fast path
  and the default — capturing every unknown key allocates a map per column.
- **The fixture's sources are backed by seeds**, so `Graph.ClassifySources`
  reports every one of them as shadowed. That is not a mistake — a seed is how
  a project stands up fake raw data — and `ShadowedSource.Mistake()` exists to
  keep the warning off it. A source shadowed by a *model* is the real finding.
- A YAML comment attached to an item of the `sources` or `tables` sequence is
  kept by dbt-ditto and dropped by dbt-osmosis; `output.comments: osmosis`
  only emulates the loss inside a `columns` list. Do not put explanatory
  comments on source list items in the fixture — use a `description`.

## Status

Done:

- [x] Config, streaming manifest/catalog readers, cross-project graph, resolver,
      YAML writer, CLI
- [x] Byte-for-byte parity with the real dbt-osmosis on the whole platform
      fixture (`make parity-check` prints "identical")
- [x] Test suite: ~70 tests over config, manifest decoding, graph shape,
      inheritance semantics, path templates, YAML writing, plus golden
      end-to-end and idempotence tests
- [x] Benchmarks: head-to-head against dbt-osmosis, plus synthetic scaling to
      5 000 models
- [x] Makefile, README
- [x] Derived inheritance: aggregates and struct fields, both directions,
      including from sources and across projects (`inheritance.derived`)
- [x] Version matrix: five dbt versions (1.8.9 → 1.12.4) captured as artifacts
      and replayed by `go test` with no dbt installed
- [x] Snowflake: real dbt-snowflake artifacts via fakesnow, no account needed
- [x] BigQuery artifact fixture built from adapter source
- [x] Source documentation: warehouse `COMMENT`s via catalog.json
      (`columns.comments`) and backfill from descendants
      (`inheritance.backfill`), with `Graph.Descendants` as the reverse walk
- [x] Distribution: `scripts/dist.sh` (static archives for six targets) and
      `packaging/pypi/build_wheels.py` (platform wheels carrying the binary),
      verified by `scripts/verify-wheels.sh` under both pip and uv
- [x] Directives (`Inherited: node.column`) and ambiguity warnings, in both the
      binary and the dbt package's macros
- [x] Provenance on by default (`inheritance.progenitor`)
- [x] dbt package in `packaging/dbt-ditto`, proved by `make dbt-package`
- [x] Source providers: external sources documented from the warehouse by an
      external program, with reference providers for **BigQuery and Snowflake**
      in `packaging/providers/`. The binary gains no dependency and still
      connects to nothing; providers reuse dbt's own `profiles.yml`. Design and
      status in [.agents/source-providers.md](.agents/source-providers.md)

- [x] CI/CD: `.github/workflows/{ci,release,mirror-dbt-ditto}.yml` and
      `scripts/check-versions.sh`

Not done, and needs a decision:

- [ ] **Nothing is released.** There is no tag, and CI has never run on anything
      past the initial commit. `pyproject.toml` and
      `packaging/dbt-ditto/dbt_project.yml` both say `0.1.0` and agree, so
      `check-versions.sh` passes and `v0.1.0` is taggable as it stands.
- [ ] The account-side setup the workflows depend on (see "Publishing" above):
      the PyPI pending publisher, the `pypi` GitHub environment, the
      `rognerud/dbt_ditto` mirror repo, `MIRROR_TOKEN`, the hubcap PR. None of
      it can be done from inside the repository.
- [ ] **The source providers are not distributed anywhere.** The wheel carries
      the binary and nothing else — that is `build_wheels.py`'s design — so
      `pip install dbt-ditto` gets a binary that can run providers and no
      providers to run. `packaging/providers/` only exists to someone who cloned
      the repository, which makes the `command:` lines in its README wrong for
      everyone else. Three ways out, none chosen:

      1. Ship them in the wheel's `.data/scripts/`, the same mechanism the
         binary uses. All four files land in `bin/`, and each provider's
         `sys.path.insert` on its own directory already finds the shared module
         there, so they work unmodified and `command: dbt-ditto-bigquery` means
         something. Needs `Provides-Extra` in the hand-rolled metadata so the
         warehouse SDKs are not hard dependencies.
      2. A separate `dbt-ditto-providers` distribution, with real extras.
         Cleanest dependencies, a second thing to version and release.
      3. Leave them here and document fetching them by URL. No packaging work;
         users depend on a raw.githubusercontent.com path.
- [ ] No Homebrew tap and no `.pre-commit-hooks.yaml`.

Worth doing next:

- [ ] Extend the matrix sideways to adapters, not just versions. dbt-postgres in
      Docker is free and has a different catalog macro, so it is the obvious
      next row; the generator already has the right shape for it, but
      `scripts/matrix.sh` hardcodes duckdb.
- [ ] A dbt-osmosis version matrix. Parity is asserted against 1.5.0 only, and
      the target moves.
- [ ] BigQuery as a generated row rather than a hand-built fixture, using
      `bigquery-emulator` under Docker. Docker was not available in the
      environment this was developed in.
- [ ] Providers for Databricks, Redshift and Glue. Each is a new file in
      `packaging/providers/` and no change to dbt-ditto; BigQuery and Snowflake
      are done.
- [ ] `inheritance.extra_keys` values inherit like meta — an ancestor's value
      overwrites the column's — and are not gated by match rank, so a policy tag
      carried as an extra key travels across an aggregate match where the same
      thing carried as a *label* would not. Worth reconciling if extra keys turn
      out to be used mostly for classifications.
- [ ] dbt-osmosis on BigQuery writes only the leaves of a record, never the
      parent; dbt-ditto writes both, because the catalog lists both.

- [ ] Snapshots in the fixture (nothing exercises the `snapshot` resource type
      end to end; the resolver handles it but it is untested against real dbt).
- [ ] Model versions (`v: 2` entries). dbt-osmosis has `model_versions.py`;
      dbt-ditto ignores versioned models' `v` key.
- [ ] `--select` does not support dbt's `+` graph operators.
- [ ] Source YAML creation for sources that have no file yet (dbt-osmosis'
      `create_missing_source_yamls`); dbt-ditto only enriches existing ones.
