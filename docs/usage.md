# Using dbt-ditto

Install it, point it at a dbt project, and it fills in your schema YAML. Settings:
[configuration.md](configuration.md).

## Install

```sh
uv add dbt-ditto      # or pip install dbt-ditto; the wheel carries the binary
go install github.com/rognerud/dbt-ditto/cmd/dbt-ditto@latest   # standalone
```

The Python package is the binary itself and pulls in nothing.

## Run it

```sh
dbt build                              # 1. dbt writes manifest.json
dbt docs generate                      # 2. dbt writes catalog.json
dbt-ditto inherit path/to/project      # 3. ditto edits your schema YAML
```

Step 3 reads those artifacts from `target/` and never touches the warehouse.
Given a project directory it needs no config file: the `+dbt-osmosis:` /
`+dbt-ditto-path:` rules in `dbt_project.yml` are used.

| Command | What it does |
| --- | --- |
| `dbt-ditto inherit ./project` | one project, no config file |
| `dbt-ditto inherit` | every project in the config file found upwards |
| `dbt-ditto inherit --check` | CI gate: exit 1 if out of date, write nothing |
| `dbt-ditto inherit --dry-run -v` | list every change it would make |
| `dbt-ditto inherit --select 'stg_*,tag:nightly'` | limit the nodes |

`--select` takes name globs, `tag:NAME` or `path:PREFIX`, comma-separated. Other
flags: `-c PATH`, `--no-organize`, `--refresh-sources`, `--target NAME`
(`dbt-ditto --help` lists all). Files written and the summary go to stdout,
warnings to stderr. Exit `0` means success.

## The config file

Needed only for more than one project, or to change a default. Searched upwards
from the working directory: `dbt_ditto.yml`, `.yaml`, `.dbt_ditto.yml`, then
`pyproject.toml` if it has a `[tool.dbt-ditto]` table (one without it is skipped,
so packaging files never shadow a real config). `-c PATH` names one directly.

```yaml
# dbt_ditto.yml
projects:
  - path: projects/platform
  - path: ../vendor-dbt       # inherit from it, never write to it
    upstream: true
  - manifest: ../artifacts/finance/manifest.json   # a bare artifact
```

The same keys work in `pyproject.toml` under `[tool.dbt-ditto]`, with projects as
a list of `[[tool.dbt-ditto.projects]]` tables. Both formats go through one
reader, so they cannot drift.

Projects are not named here: the name in `dbt_project.yml` or the manifest
decides which nodes belong to which project. Relative paths resolve against the
config file.

## Inheriting across projects

Loom's job at dbt runtime is untouched: dbt-ditto has no dbt plugin and fetches
nothing. Three ways an upstream reaches the graph:

1. **Loom already injected it**, so the upstream nodes are in the project's own
   `manifest.json`.
2. **`dbt_loom.config.yml` is read** (honouring `DBT_LOOM_CONFIG`): its
   `type: file` manifests load as upstreams. Remote backends (`dbt_cloud`, `s3`,
   `gcs`, `azure`) are **not** fetched, only reported on stderr — download the
   artifact and name it under `manifest:`, or set `loom: false`.
3. **You list it yourself** under `path:` or `manifest:`, which wins over loom.

The manifests then form one graph. Only models marked `access: public` are
eligible, as in dbt. Upstream projects donate metadata and are never written to.

## Documenting source tables

Inheritance runs downhill, so a source can never inherit. Three routes reach it,
and they compose:

**1. Warehouse comments.** A `COMMENT ON COLUMN` reaches `catalog.json` and
becomes the description. On by default for columns being added;
`columns.comments: always` also fills columns already listed but undocumented,
`never` ignores them.

**2. Backfill from downstream**, from the staging model that reads the source:

```yaml
inheritance:
  backfill:
    enabled: true
    sources_only: true   # default; false also backfills models
```

Descendants are searched nearest-first, through models that select the column
undocumented. It only fills blanks, the source's own warehouse comment wins, and
only sources are backfilled by default — otherwise a mart's wording flows
backwards into everything feeding it.

**3. Source providers** ask the warehouse directly. See
[source-providers.md](source-providers.md).

## Pointing at the right column

Name matching cannot follow a renamed column, so say where the documentation is:

```yaml
columns:
  - name: cust_id
    description: "Inherited: stg_customers.customer_id"
```

The directive outranks name matching, `force` and a local description, because it
is you stating the answer. The node may be a bare name or a full `unique_id`; use
the latter when two projects both have a model of that name. One naming something
that does not exist is reported on stderr and left in the file verbatim, since
deleting it would lose your instruction.

Syntax is [dbt-doc-inherit's](https://github.com/tripleaceme/dbt-doc-inherit).
Change the marker with `inheritance.directive_prefix`; turn it off with
`inheritance.directives: false`.

## When two parents disagree

Within a generation the first ancestor by `unique_id` claims the column, so a
disagreement is settled alphabetically. That is arbitrary, so it is reported:

```
warning: model.platform.stg_orders_enriched.order_id documented differently by
         source.platform.crm.raw_orders; took seed.platform.raw_orders
```

Warnings go to stderr, never fail the run, and never change what is written. A
nearer generation overriding a further one is normal and is not reported. Silence
them with `inheritance.warn_ambiguous: false`, or record them in the file with
`inheritance.ambiguity_meta`:

```yaml
- name: order_id
  description: Surrogate key for an order.
  meta:
    osmosis_progenitor: seed.platform.raw_orders    # the winner
    dbt_ditto_ambiguous:                            # the overruled
      - source.platform.crm.raw_orders
```

Only inherited descriptions are annotated; documenting the column yourself
removes it next run. `osmosis_progenitor` is written for every inherited
description, the one line in a schema file nobody wrote; set
`inheritance.progenitor: false` for byte parity with dbt-osmosis.

### Settling it: `dbt_ditto_definitive`

A warning cannot say who is right. That is a decision, not a fact about the DAG.
Write the decision down:

```yaml
columns:
  - name: customer_id
    description: The account the order was placed under.
    meta:
      dbt_ditto_definitive: true
```

Every column named `customer_id` in the run then says that: models below, the
seed above, the source it came from, the same column in other projects. A
decision has no direction. The rules:

- It outranks name matching, a directive, `force`, and hand-written prose.
- The declaring column is locked, and columns that take it record it in
  `osmosis_progenitor`. Ambiguity warnings stop for that column.
- The marker itself is never inherited, or every column downstream would claim to
  be the decision too.
- Declarations in a manifest-only upstream are ignored, since a loom manifest
  cannot be reviewed from your repository. A checked-out project counts, even one
  marked `upstream: true`.
- Two that disagree stop the run; the same wording in several places is one
  decision, not a conflict.

The key name is not configurable, unlike `progenitor_key` and `ambiguity_key`:
those name something this tool writes, this one is a contract between
repositories.

## Following a column through a rename

Name matching stops the moment a column is aggregated or packed into a struct.
`inheritance.derived.enabled: true` matches across the rename, both directions:

| Downstream column | Inherits from | Why |
| --- | --- | --- |
| `total_amount_cents` | `amount_cents` | leading/trailing aggregate word stripped |
| `profile.first_name` | `first_name` | the column was packed into a struct |
| `first_name` | `profile.first_name` | and the reverse, when unpacked |
| `totals.total_amount_cents` | `amount_cents` | both at once |

Works from any ancestor, sources included, across project boundaries. Two rules
keep it safe: an exact name match always wins, and only whole
underscore-separated words are stripped, one at each end, so `counterparty_id` is
not read as an aggregate of `erparty_id`. Nothing here parses SQL, so it is off
by default — `count(order_id) as order_id_count` inherits *"Surrogate key for an
order."*, describing the thing counted rather than the count. Drop `count` from
`inheritance.derived.suffixes` if that is a bad trade.

Struct *expansion* is separate and on by default: a `STRUCT` in `catalog.json` is
parsed so `profile.first_name` is documented as its own column, which is what
nested-data adapters report.

## How inheritance resolves

For each column, in the order dbt-osmosis does it:

1. The column's own metadata seeds the result.
2. Ancestors are walked **furthest generation first**, so nearer ancestors
   overwrite what further ones contributed.
3. Within a generation, the first ancestor by `unique_id` that *has* the column
   claims it and the rest of that generation is skipped — including ancestors
   holding it undocumented, which is how an undocumented intermediate model
   shadows the root behind it.
4. A placeholder description upstream never overwrites anything.
5. The description is applied only if the column has none of its own. Meta and
   tags are always merged, upstream winning collisions, local key order kept.

Generations come from a depth-first walk, so a node reachable by several routes
is filed under the depth first reached, not its shortest path — as in
dbt-osmosis, and that is what decides conflicts.

## Differences from dbt-osmosis

| Difference | Switch it off with |
| --- | --- |
| Provenance is recorded — the only default that changes an existing project's bytes | `inheritance.progenitor: false` |
| Comments in a column list survive; dbt-osmosis keeps only the first | `output.comments: osmosis` |
| Warehouse comments come from `catalog.json`, not the adapter | `columns.comments: never` |
| Snapshots are followed when walking ancestors | — |

`backfill`, `derived` and `ambiguity_meta` diverge too, but default off.
