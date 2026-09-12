# Using dbt-ditto

Install, configure and run. Build, test and release machinery is in
[development.md](development.md); architecture and conventions in
[AGENTS.md](../AGENTS.md).

## Install

```sh
# In a dbt project's environment, alongside (or instead of) dbt-osmosis:
uv add dbt-ditto
pip install dbt-ditto

# Or a standalone binary:
go install github.com/rognerud/dbt-ditto/cmd/dbt-ditto@latest
```

The Python package carries the binary itself, not a wrapper, and pulls in
nothing else.

## Quick start

```sh
# One project, no configuration: uses the +dbt-osmosis rules already in
# dbt_project.yml.
dbt-ditto inherit path/to/project

# Several projects, including ones you only read from.
dbt-ditto inherit -c dbt_ditto.yml

# In CI: fail if the documentation is out of date, change nothing.
dbt-ditto inherit --check
```

Configuration is searched for upwards from the working directory:
`dbt_ditto.yml`, `dbt_ditto.yaml`, `.dbt_ditto.yml`, then `pyproject.toml` if it
carries a `[tool.dbt-ditto]` table. One without that table, or one that will not
parse, is skipped and the search continues, so an unrelated packaging file never
shadows a real config. `-c PATH` names a file directly, in either format; a
`pyproject.toml` named that way but missing the table is an error.

```yaml
# dbt_ditto.yml
projects:
  - path: projects/platform
  # A project you inherit from but must not write to.
  - path: ../vendor-dbt
    upstream: true
  # An upstream that arrives as a bare artifact, with no checkout behind it.
  - manifest: ../artifacts/finance/manifest.json
```

A project is not named here: its name is whatever `dbt_project.yml` or the
manifest says, and that is what decides which nodes belong to it.

The same list in `pyproject.toml`, where a list of tables is how TOML spells a
list of projects. It is decoded and handed to the same reader the YAML goes
through, so the two formats cannot drift; `[tool.dbt_ditto]` is accepted too.

```toml
[tool.dbt-ditto]
loom = true

[[tool.dbt-ditto.projects]]
path = "projects/platform"

[[tool.dbt-ditto.projects]]
path = "../vendor-dbt"
upstream = true

[tool.dbt-ditto.inheritance]
progenitor = true
```

Every other key is optional and defaults to dbt-osmosis' behaviour;
[`internal/config/config.go`](../internal/config/config.go) is the source of
truth.

## Settings

Every key, with its default — as YAML in `dbt_ditto.yml`, or under
`[tool.dbt-ditto]` in `pyproject.toml`. Defaults reproduce dbt-osmosis, except
`inheritance.progenitor`. Each row is enforced by a test that runs the tool twice
and checks the described difference actually appears
([`internal/runner/settings_test.go`](../internal/runner/settings_test.go)).

**Which projects take part**

| Key | Default | Change it to… |
| --- | --- | --- |
| `projects[].path` | — | point at a dbt project directory — the one holding `dbt_project.yml`. Relative paths are resolved against the config file |
| `projects[].target` | `<path>/target` | read `manifest.json` / `catalog.json` from somewhere else, e.g. a directory CI downloaded them into |
| `projects[].manifest` | — | add an upstream that is only a `manifest.json(.gz)`, with no checkout. Always read-only |
| `projects[].upstream` | `false` | take documentation *from* this project but never write its YAML |
| `loom` | `true` | set `false` to ignore `dbt_loom.config.yml`. When on, its `type: file` manifests are loaded as upstreams |

**What travels between columns**

| Key | Default | Change it to… |
| --- | --- | --- |
| `inheritance.columns` | `true` | set `false` to stop inheriting altogether, leaving only column sync and file organisation |
| `inheritance.node_description` | `false` | set `true` to give a model the description of the model above it, not just its columns' |
| `inheritance.meta` | `true` | set `false` to leave `meta` alone; on, an ancestor's keys are merged in and win collisions |
| `inheritance.tags` | `true` | set `false` to leave `tags` alone; on, ancestors' tags are added to the column's own |
| `inheritance.case_insensitive` | `true` | set `false` to stop `ID` matching an upstream `id`, e.g. on Snowflake |
| `inheritance.force` | `false` | set `true` to replace descriptions already written by hand. Off, a documented column is never touched |
| `inheritance.placeholders` | dbt-osmosis' list | replace the wordings that count as "not really documented" upstream, and so do not get inherited |
| `inheritance.skip_meta_keys` | none | name meta keys that must stay put — `owner` on an upstream table is about that table |
| `inheritance.backfill.enabled` | `false` | set `true` to document a column from the models *below* it, which is the only way to reach a source |
| `inheritance.backfill.sources_only` | `true` | set `false` to backfill models as well, letting a mart's wording flow back up into what feeds it |
| `inheritance.derived.enabled` | `false` | set `true` to follow a column through a rename — `sum(amount_cents) as total_amount_cents` keeps the documentation |
| `inheritance.derived.structs` | `true` | set `false` to stop matching `profile.first_name` to a flat `first_name`, and the reverse |
| `inheritance.derived.aggregates` | `true` | set `false` to stop matching `total_amount_cents` to `amount_cents` |
| `inheritance.derived.prefixes` | `sum`, `total`, `avg`, … | replace the words stripped from the front of a name before matching |
| `inheritance.derived.suffixes` | `sum`, `count`, `cnt`, … | replace the words stripped from the end. Dropping `count` stops `order_id_count` inheriting from `order_id` |

**Pointing at an answer, and recording where it came from**

| Key | Default | Change it to… |
| --- | --- | --- |
| `inheritance.directives` | `true` | set `false` to treat `description: "Inherited: stg_customers.customer_id"` as ordinary prose instead of a pointer |
| `inheritance.directive_prefix` | `Inherited:` | change the marker that makes a description a pointer |
| `inheritance.progenitor` | `true` | set `false` for byte parity with dbt-osmosis. On, an inherited description records the node it came from |
| `inheritance.progenitor_key` | `osmosis_progenitor` | rename that meta key |
| `inheritance.warn_ambiguous` | `true` | set `false` to stop reporting columns whose parents disagree, where the winner is decided by `unique_id` order |
| `inheritance.ambiguity_meta` | `false` | set `true` to record that disagreement in the column's meta, where it stays after the warning has scrolled away |
| `inheritance.ambiguity_key` | `dbt_ditto_ambiguous` | rename that meta key |

**What the column list looks like afterwards**

| Key | Default | Change it to… |
| --- | --- | --- |
| `columns.add_missing` | `true` | set `false` to leave the YAML's column list alone instead of adding what the warehouse has |
| `columns.remove_stale` | `true` | set `false` to keep columns the warehouse no longer reports |
| `columns.data_types` | `true` | set `false` to stop writing `data_type:` from the catalog |
| `columns.case` | `preserve` | `lower` or `upper` to re-case newly added column names. An already-written name is never churned |
| `columns.order` | `catalog` | `alphabetical`, or `yaml` to keep the order the file already has |
| `columns.expand_structs` | `true` | set `false` to document `profile` but not `profile.first_name`. On is what an adapter that understands nested data reports |
| `columns.comments` | `new` | `always` to fill any undocumented column from the warehouse `COMMENT`, `never` to ignore comments |
| `organize.enabled` | `true` | set `false` to leave every model in the file it is documented in today, ignoring `+dbt-ditto-path:` |
| `organize.delete_empty` | `true` | set `false` to leave behind a schema file whose last model moved out |
| `output.comments` | `follow` | `osmosis` to reproduce dbt-osmosis' loss of every YAML comment inside a column list except the first |

Source-provider settings (`sources.*`) are tabled in
[source-providers.md](source-providers.md).

Whether meta and tags nest under `config:` is **not** a setting: dbt >= 1.9.6
reads them there and older dbt does not, so the manifest's dbt version decides.
Command-line flags — `--check`, `--dry-run`, `--select`, `-c`, `--verbose`,
`--no-organize` — are listed by `dbt-ditto --help`.

## Cross-project inheritance

**dbt-ditto is not a replacement for dbt-loom.** Loom's job at dbt runtime —
injecting upstream nodes into the manifest dbt is parsing — is untouched.
dbt-ditto has no dbt plugin and fetches nothing: it runs *after* dbt, over
artifacts on disk, and writes only schema YAML. Three ways an upstream reaches
the graph:

1. **Loom already injected it**, so the upstream nodes and their documentation
   are inside the project's own `manifest.json`.
2. **`dbt_loom.config.yml` is read.** Every `type: file` manifest in it is loaded
   as an upstream, honouring `DBT_LOOM_CONFIG`. The remote backends (`dbt_cloud`,
   `s3`, `gcs`, `azure`) are *not* fetched; each is reported on stderr, and the
   fix is to download the artifact and name it under `manifest:`. `loom: false`
   switches the discovery off.
3. **You list it yourself**, as `path:` or `manifest:`. An explicit entry always
   wins over what loom says.

Once loaded, the manifests become one graph: `depends_on` edges naming a
`unique_id` no manifest contains are repaired by resource name, and only models
marked `access: public` are eligible, matching dbt's own cross-project rules.
Upstream projects donate metadata and are never written to.

## Documenting source tables

Inheritance runs downhill, so a source — a root of the DAG — can never inherit.
Two routes reach it, and they compose.

**The warehouse's own comments.** A `COMMENT ON COLUMN` arrives in
`catalog.json` and is used as the description. On by default for columns being
added, as dbt-osmosis does; `columns.comments: always` also fills a column that
is already listed but undocumented, `never` turns it off.

**Backfill from downstream**, from the staging model that reads the source:

```yaml
inheritance:
  backfill:
    enabled: true
    sources_only: true   # the default; set false to backfill models too
```

The search walks descendants nearest-first and does not stop at a model that
merely selects the column through undocumented. Three rules keep it safe: it
only ever fills a blank, the source's own warehouse comment beats anything found
downstream, and by default only sources are backfilled — otherwise a mart's
wording flows backwards into everything that feeds it.

## Asking the warehouse: source providers

Often the documentation exists only in the warehouse: a BigQuery table
description, a Snowflake `COMMENT`, labels, a policy tag. A **provider** is a
separate program that fetches it; dbt-ditto writes it the list of external
sources and reads documentation back, which is what keeps the binary a single
static file that connects to nothing. Reference providers for BigQuery and
Snowflake ship in [`packaging/providers/`](../packaging/providers/README.md).

```yaml
sources:
  providers:
    - command: "uv run --with google-cloud-bigquery packaging/providers/bigquery.py"
      match: { database: "bq-*" }
```

```sh
dbt-ditto inherit --refresh-sources   # runs the providers, writes the cache
dbt-ditto inherit                     # reads the cache, connects to nothing
dbt-ditto inherit --check             # likewise: CI needs no credentials
```

**Credentials are dbt's own.** A provider is told each project's root,
`profile:` and profiles directory, and resolves the connection from
`profiles.yml` — the same entry, target and method `dbt run` uses. `--target`
picks the target, falling back to `$DBT_TARGET`.

**Only external sources are asked about**: the ones no loaded project builds, so
a refresh asks about tens of tables rather than thousands. A source pointing at
a relation another loaded project *builds* is reported rather than fetched; a
cross-project `ref()` is the fix.

**Refreshing is explicit, and its answer is cached** in
`target/ditto-sources.json`, because `--check` runs in CI where a flaky API must
not fail a formatting check. A provider that fails is a warning unless
`sources.strict: true`. Labels, tags, policy tags and how far they propagate are
covered in [source-providers.md](source-providers.md).

### Carrying keys dbt-ditto does not model

```yaml
inheritance:
  extra_keys: ["policy_tags"]
```

Named keys are carried down the DAG like meta — an ancestor's value wins — and
written back beside `name`. The list is explicit rather than a catch-all because
decoding every unknown key would allocate a map per column on a manifest that
may hold millions.

## Saying where the documentation lives

Name matching cannot follow a renamed column, and no heuristic should be trusted
to guess. Name the source instead:

```yaml
columns:
  - name: cust_id
    description: "Inherited: stg_customers.customer_id"
```

The directive outranks everything — name matching, `force`, a local description
— because it is the analyst saying what the answer is. The node may be a bare
name or a full `unique_id`, which is how to be unambiguous when two projects
both have a model of that name. One naming a node nobody has, a column that does
not exist, or a column nobody has documented is reported and **left in the
file**, since deleting it would lose the instruction and writing it back
silently would look like prose:

```
warning: model.shop.dim_customers.cust_id "stg_customers.cust_id" has no column "cust_id"
```

The syntax is [dbt-doc-inherit's](https://github.com/tripleaceme/dbt-doc-inherit).
Change the marker with `inheritance.directive_prefix`, or turn the feature off
with `inheritance.directives: false`.

## When parents disagree

Within a generation the first ancestor by `unique_id` claims a column, so two
parents documenting it differently are settled alphabetically. That is
arbitrary, and worth hearing about:

```
warning: model.platform.stg_orders_enriched.order_id documented differently by
         source.platform.crm.raw_orders; took seed.platform.raw_orders
```

Warnings go to stderr, never fail the run, and never change what is written. A
nearer generation overriding a further one is inheritance working, and is not
reported. Turn them off with `inheritance.warn_ambiguous: false`.

A warning is gone the moment the terminal scrolls. `inheritance.ambiguity_meta`
records it where it stays, the way `progenitor` records where a description came
from:

```yaml
- name: order_id
  description: Surrogate key for an order.
  meta:
    osmosis_progenitor: seed.platform.raw_orders
    dbt_ditto_ambiguous:
      - source.platform.crm.raw_orders
```

The value lists the ancestors that were overruled; the winner is already in the
progenitor key. The two switches are independent. Three rules keep the
annotation honest: only an inherited description is annotated (documenting the
column locally removes it on the next run), it is never itself inherited, and it
is off by default because it writes meta dbt-osmosis would not.

## Settling it: `dbt_ditto_definitive`

A warning says the parents disagree. It cannot say who is right, because that is
a decision rather than a fact about the DAG. Write the decision down:

```yaml
columns:
  - name: customer_id
    description: The account the order was placed under.
    meta:
      dbt_ditto_definitive: true
```

From then on every column called `customer_id` in the run says that — not just
the models below, but the seed above, the source it came from, and the same
column in the other projects. A decision has no direction. Precisely:

- **It outranks everything else**: name matching, a directive, `force`, a
  description written by hand.
- **The declaring column is locked**: nothing is inherited into it and nothing
  overwrites it.
- **The wording is attributed**: a column that takes it records the declaring
  node in `osmosis_progenitor`.
- **The ambiguity warning stops** for that column, and so does the
  `ambiguity_meta` annotation.
- **The marker itself is never inherited**, or every column downstream would
  claim to be the decision too.
- **A manifest-only upstream's declarations are ignored**, since a dbt-loom
  manifest cannot be reviewed or edited from this repository. Declarations in a
  checked-out project — including one marked `upstream: true` — do count.

Two declarations that disagree **stop the run**, because choosing between them
by rule is the exact thing the marker exists to avoid. The same wording declared
in several places is one decision, not a conflict.

```
conflicting dbt_ditto_definitive declarations for column "customer_id":
  model.platform.dim_customers.customer_id: "The account the order was placed under."
  model.analytics.customer_report.CUSTOMER_ID: "The customer who placed the order."
  (these are the same column because inheritance.case_insensitive is on)
  resolve it by leaving one declaration, or by making them agree word for word
```

The key name is not configurable, unlike `progenitor_key` and `ambiguity_key`:
those name something this tool writes, while this one is a contract between
projects that may live in different repositories.

## Where a description came from

Every inherited description records its origin, because it is the one line in a
schema file nobody wrote:

```yaml
- name: customer_id
  description: Surrogate key of the customer.
  meta:
    osmosis_progenitor: seed.platform.raw_customers
```

dbt-osmosis leaves this off, so a project compared against it byte for byte sets
`inheritance.progenitor: false` — which is what `scripts/parity.sh` does. Only
genuinely inherited descriptions get one.

## Following a column through a rename

dbt-osmosis matches columns by name, so documentation stops the moment a column
is aggregated or packed into a struct. Turning on `inheritance.derived` matches
across the rename, in both directions:

| Downstream column | Inherits from | Why |
| --- | --- | --- |
| `total_amount_cents` | `amount_cents` | a leading or trailing aggregate word is stripped |
| `profile.first_name` | `first_name` | the column was packed into a struct |
| `first_name` | `profile.first_name` | and the reverse, when a struct is unpacked |
| `totals.total_amount_cents` | `amount_cents` | both at once |

This works from any ancestor, sources included, and across project boundaries.
Two rules keep it from doing damage: an exact name match always wins, so a
derived match is only consulted when nothing upstream shares the name; and only
whole underscore-separated words are stripped, one at each end, so
`counterparty_id` is not read as an aggregate of `erparty_id`.

It is off by default, being a lexical heuristic that changes output dbt-osmosis
would leave alone. In the fixture, `count(order_id) as order_id_count` inherits
*"Surrogate key for an order."*, which describes the thing being counted rather
than the count; drop `count` from `inheritance.derived.suffixes` if that trade
is not worth it. Nothing here parses SQL.

Struct *expansion* is separate and on by default: adapters that understand
nested data report `profile.first_name` as a column in its own right, so
dbt-osmosis documents it and dbt-ditto has to as well. It reads the composite
type out of `catalog.json`, and understands DuckDB's `STRUCT(...)`, BigQuery's
`STRUCT<...>` and `ARRAY<STRUCT<...>>`, nested structs, and quoted field names.

## How inheritance resolves

For each column, in the order dbt-osmosis does it:

1. The column's own metadata seeds the result.
2. Ancestors are walked **furthest generation first**, so nearer ancestors
   overwrite what further ones contributed.
3. Within a generation, the first ancestor by `unique_id` that has the column
   claims it and the rest of that generation is skipped — including ancestors
   that have the column but no documentation, which is how an undocumented
   intermediate model shadows the root behind it.
4. An upstream description that is a placeholder never overwrites anything.
5. The description is applied only if the column has none of its own; meta and
   tags are always merged, with upstream values winning key collisions and local
   key order preserved.

Generations come from a depth-first walk with one shared visited set, so a node
reachable by several routes is filed under the depth the walk first reached it
at. That decides who wins a conflict, so it is reproduced exactly.

## Deliberate differences from dbt-osmosis

All documented, all switchable:

1. **Provenance is recorded.** `inheritance.progenitor` is on. This is the only
   default that changes what an existing dbt-osmosis project's YAML looks like;
   set it to `false` for byte parity.
2. **Comments inside a column list.** dbt-osmosis rebuilds the list from
   scratch, so ruamel drops every comment except the one above the first entry;
   dbt-ditto keeps each comment with the column it annotates. Set
   `output.comments: osmosis` for the lossy behaviour, which is what the parity
   run uses.
3. **Snapshots as inheritance sources.** dbt-osmosis' ancestor walk follows only
   `model.`, `seed.` and `source.`; dbt-ditto also follows `snapshot.`.
4. **Where warehouse comments come from.** dbt-osmosis asks the adapter,
   dbt-ditto reads `catalog.json`. On DuckDB the adapter reports none while the
   catalog has them, so dbt-ditto finds documentation dbt-osmosis cannot see. The
   parity run sets `columns.comments: never` to compare like for like.

`inheritance.backfill`, `inheritance.derived` and `inheritance.ambiguity_meta`
are also divergences, but all three are off by default. Directives and ambiguity
warnings are on, and neither changes output.
