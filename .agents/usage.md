# Using dbt-ditto

Install, configure and run. For the repository's own build, tests and release
machinery see [development.md](development.md); for architecture and
conventions see [AGENTS.md](../AGENTS.md).

## Install

```sh
# In a dbt project's environment, alongside (or instead of) dbt-osmosis:
uv add dbt-ditto
pip install dbt-ditto

# Or a standalone binary:
go install github.com/rognerud/dbt-ditto/cmd/dbt-ditto@latest
```

The Python package contains the binary itself, not a wrapper: the installer
drops it into the environment's `bin/`, so there is no interpreter startup on
the way to the tool. Nothing else is pulled in — no dbt, no Python dependencies.

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
carries a `[tool.dbt-ditto]` table. A `pyproject.toml` without that table is
skipped and the search continues upwards, so an unrelated packaging file never
shadows a real config; one that cannot be parsed at all is skipped too, because
someone else's broken TOML is not this tool's error to raise. `-c PATH` names a
file directly, in either format, and a `pyproject.toml` named that way but
missing the table is an error rather than a silent empty config.

`dbt_ditto.yml`:

```yaml
projects:
  - path: projects/platform
  - path: projects/analytics
  # A project you inherit from but must not write to.
  - path: ../vendor-dbt
    upstream: true
  # An upstream that arrives as a bare artifact, with no checkout behind it.
  - manifest: ../artifacts/finance/manifest.json
```

A project is not named here. Its name is whatever `dbt_project.yml`, or the
manifest, says it is, and that name is what decides which nodes belong to it —
naming it again could only agree, or disagree and silently select nothing.

The same list in `pyproject.toml`, where a list of tables is how TOML spells a
list of projects:

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

[tool.dbt-ditto.inheritance.derived]
enabled = true
```

The table is decoded and then handed to the same reader the YAML goes through,
so the two formats cannot drift: every key, default and nested section has one
definition. `[tool.dbt_ditto]` is accepted as well, since the underscore and the
hyphen are the same name to anyone writing it.

Every other key is optional and defaults to dbt-osmosis' behaviour. The
[README](../README.md#settings) tables every key with its default and what
changing it does; [`internal/config/config.go`](../internal/config/config.go) is
the source of truth. The ones people actually reach for:

```yaml
inheritance:
  force: false            # overwrite descriptions a column already has
  progenitor: true        # record where a description came from, in meta
  directives: true        # honour `description: "Inherited: model.column"`
  warn_ambiguous: true    # report columns the parents document differently
  ambiguity_meta: false   # and record that in the column's meta (see below)
  skip_meta_keys: [owner] # meta keys that must never be inherited
  placeholders: ["", "TODO", "Not documented"]
  derived:
    enabled: false        # follow a column through a rename (see below)
  backfill:
    enabled: false        # document sources from downstream (see below)

columns:
  data_types: true        # write data_type from the catalog
  case: preserve          # preserve | lower | upper
  order: catalog          # catalog | yaml | alphabetical
  remove_stale: true      # drop columns the warehouse no longer has
  expand_structs: true    # document struct fields as `profile.first_name`
  comments: new           # use warehouse COMMENTs: new | always | never

organize:
  enabled: true           # move models into the file their path rule names

output:
  comments: follow        # follow | osmosis

loom: true                # read dbt_loom.config.yml for upstream manifests
```

## Cross-project inheritance

**dbt-ditto is not a replacement for dbt-loom.** Loom's job at dbt runtime —
injecting upstream nodes into the manifest dbt is parsing, so a cross-project
`ref()` compiles — is untouched, and dbt-ditto has no dbt plugin, writes no
manifest, and fetches nothing over the network. It runs *after* dbt, over
artifacts already on disk, and the only files it writes are schema YAML.

What it takes over is the documentation half. Three ways an upstream reaches the
graph, in the order you are likely to hit them:

1. **Loom already injected it.** A project parsed with dbt-loom active has the
   upstream nodes, and their documentation, inside its own `manifest.json`.
   Nothing extra is needed: point dbt-ditto at the project and inheritance
   crosses the boundary because the manifest already does.
2. **`dbt_loom.config.yml` is read.** Every `type: file` manifest in it is loaded
   as an upstream project, honouring `DBT_LOOM_CONFIG`. This covers a manifest
   parsed without the plugin, and keeps the upstream list in one place instead of
   copied into `dbt_ditto.yml`. The remote loom backends (`dbt_cloud`, `s3`,
   `gcs`, `azure`) are *not* fetched; each one is reported on stderr, and the fix
   is to download the artifact and name it under `manifest:`. Set `loom: false`
   to switch the discovery off.
3. **You list it yourself**, as `path:` (a checkout) or `manifest:` (a bare
   artifact, gzipped or not). An explicit entry always wins over what loom says.

Once loaded, the manifests become one graph: `depends_on` edges that name a
`unique_id` no manifest contains are repaired by resource name, and only models
marked `access: public` are eligible, matching dbt's own cross-project rules.
Upstream projects donate metadata and are never written to.

## Documenting source tables

Inheritance runs downhill, which leaves raw sources out entirely. A source is a
root of the DAG, so nothing upstream can ever document it, and it is usually the
least documented thing in the project. Two routes reach it, and they compose.

**The warehouse's own comments.** A `COMMENT ON COLUMN` in Snowflake, BigQuery,
Databricks or DuckDB arrives in `catalog.json`, and dbt-ditto uses it as the
column's description. This is on by default for columns being added, which is
what dbt-osmosis does. `columns.comments: always` also fills a column that is
already listed but undocumented; `never` turns it off.

**Backfill from downstream.** The description you want usually does exist, one
step below, in the staging model that reads the source:

```yaml
inheritance:
  backfill:
    enabled: true
    sources_only: true   # the default; set false to backfill models too
```

The search walks descendants nearest-first and does not stop at the first model
that merely selects the column through — a staging model that passes a column
along without describing it is not an answer, so it keeps going.

Three rules keep it safe:

- **It only ever fills a blank.** A column that already has a description keeps
  it, so backfill cannot overwrite anything written by hand.
- **The source's own documentation wins.** A warehouse comment beats anything
  found downstream, because it belongs to the source rather than to a consumer
  of it.
- **Sources only, by default.** Otherwise a mart's wording starts flowing
  backwards into every model that feeds it.

In the fixture, the `billing` source documents none of its four columns. After a
backfill run all four are documented: three carried up from `stg_invoices`, and
`order_id` — which nothing in dbt mentions — from the DuckDB `COMMENT`.

## Asking the warehouse: source providers

The two routes above need the documentation to exist somewhere dbt has already
written down. Often it exists only in the warehouse: a description set on the
table in BigQuery, a Snowflake `COMMENT`, labels, tags, a policy tag. A
**provider** fetches it.

A provider is a separate program. dbt-ditto writes it the list of external
sources and reads documentation back, which is what keeps the binary a single
static file with two pure-Go dependencies that connects to nothing. Reference
providers for **BigQuery and Snowflake** ship in
[`packaging/providers/`](../packaging/providers/README.md).

```yaml
sources:
  providers:
    - command: "uv run --with google-cloud-bigquery packaging/providers/bigquery.py"
      match: { database: "bq-*" }
    - command: "uv run --with snowflake-connector-python packaging/providers/snowflake.py"
      match: { database: "SNOWFLAKE_*" }
```

```sh
dbt-ditto inherit --refresh-sources   # runs the providers, writes the cache
dbt-ditto inherit                     # reads the cache, connects to nothing
dbt-ditto inherit --check             # likewise: CI needs no credentials
```

**Credentials are dbt's own.** A provider is told each project's root, its
`profile:` and the profiles directory, and resolves the connection from
`profiles.yml` — the same entry, the same target, the same method `dbt run`
uses. Nothing is configured twice. `--target` picks the target, falling back to
`$DBT_TARGET`.

**Only external sources are asked about**: the ones no loaded project builds.
A dbt-loom upstream arrives as a model and is documented through the graph; a
source backed by a seed is already known to dbt. Neither is ever sent to a
provider, so a refresh asks about tens of tables rather than thousands and takes
about a second — against minutes for `dbt docs generate`, which walks every
relation in the project to reach the same handful.

A source that points at a relation another loaded project *builds* is reported
rather than fetched. That table can be documented properly through the normal
chain, and a cross-project `ref()` is the fix.

**Refreshing is explicit, and its answer is cached** in
`target/ditto-sources.json`. Providers make network calls, and `--check` runs in
CI where a flaky API must not fail a formatting check. A provider that fails is
a warning, not a failed run, unless `sources.strict: true`.

### Labels, tags and meta

Key-value pairs attached to an object are the one piece of metadata every
warehouse has — BigQuery labels, Snowflake and Unity Catalog tags, Glue table
parameters — so providers report them raw and dbt-ditto routes them:

```yaml
sources:
  labels:
    mode: meta            # meta | tags | both | ignore
    meta_key: labels      # nest under meta.labels; "" flattens into meta
    tag_format: "{key}:{value}"
    exclude: ["terraform_*"]
```

`meta` is the default because a dbt tag is a **selector**: turning every
warehouse label into one would silently change what `--select tag:...` matches.
A pair with an empty value renders as the bare key, since BigQuery permits
valueless labels and `owner:` is not a useful tag.

### What travels downstream

Whether metadata follows the column keys off the level it sits at, which gets
the right answer without anyone maintaining a list of keys.

| | Travels? |
|---|---|
| The relation's own labels (owner, cost centre, Terraform stack) | **No.** They describe the physical object and are false the moment they are copied onto a model in another dataset. There is no setting; node meta is not inherited at all. |
| A column's labels and policy tags | **Yes**, like a description. An access rule has no reason to change as the column moves between projects. |

Among column labels, what the match preserved decides it:

```yaml
sources:
  labels:
    propagate:
      column: true        # off stops labels at the source
      structs: true       # a struct pack or unpack is the same data, reshaped
      aggregates: warn    # inherit | warn | ignore
      on_conflict: warn   # warn | first | none
```

An **aggregate** match is the interesting case. `avg_salary` still means salary,
averaged, so the description carries — but the value the label classified no
longer exists, so the label does not. It defaults to `warn` rather than `ignore`
because silence is the dangerous answer: a missing description is visibly
incomplete, while a missing classification reads as "this column is not
restricted", which is a claim, and a false one.

A **conflict** — two ancestors in one generation labelling a column differently
— writes nothing and says so. Descriptions break that tie by lowest `unique_id`;
for a choice between `restricted` and `public` that is not a decision this tool
can make, and ranking restrictiveness needs an ordering it has no way to learn.

> dbt-ditto writing `policy_tag: pii/high` onto a downstream column does **not**
> apply that policy tag in the warehouse. It records what upstream says, which
> is worth knowing and is not enforcement.

### Carrying keys dbt-ditto does not model

```yaml
inheritance:
  extra_keys: ["policy_tags"]
```

Named keys are carried down the DAG like meta — an ancestor's value wins — and
written back beside `name`, where dbt reads them. The list is explicit rather
than a catch-all because decoding every unknown key would allocate a map per
column on a manifest that may hold millions.

## Saying where the documentation lives

Name matching cannot follow a column that was renamed, and no heuristic should
be trusted to guess. Name the source instead:

```yaml
columns:
  - name: cust_id
    description: "Inherited: stg_customers.customer_id"
```

The directive outranks everything — ordinary name matching, `force`, a local
description — because it is the analyst saying what the answer is. The node may
be a bare name or a full `unique_id`
(`model.platform.stg_customers.customer_id`), which is how to be unambiguous
when two projects both have a model of that name.

A directive that names a node nobody has, a column that does not exist, or a
column nobody has documented is reported and **left in the file**:

```
warning: model.shop.dim_customers.cust_id "stg_customers.cust_id" has no column "cust_id"
```

Deleting it would lose the instruction; writing it back silently would look like
prose. The syntax is [dbt-doc-inherit's](https://github.com/tripleaceme/dbt-doc-inherit),
so a project already using those directives keeps working. Change the marker
with `inheritance.directive_prefix`, or turn the feature off with
`inheritance.directives: false`.

## When parents disagree

Within a generation the first ancestor by `unique_id` claims a column, so two
parents documenting it differently are settled alphabetically. That is
arbitrary, and worth hearing about:

```
warning: model.platform.stg_orders_enriched.order_id documented differently by
         source.platform.crm.raw_orders; took seed.platform.raw_orders
```

Warnings go to stderr, never fail the run, and never change what is written.
Settle one by documenting the column locally or with a directive. A nearer
generation overriding a further one is not a disagreement — that is inheritance
working — so it is not reported. Turn them off with
`inheritance.warn_ambiguous: false`.

A warning is gone the moment the terminal scrolls, and the person who later
reads the schema file has no way to tell that the wording was picked
arbitrarily. `inheritance.ambiguity_meta` records it where it stays, the way
`progenitor` records where the description came from:

```yaml
inheritance:
  ambiguity_meta: true
  ambiguity_key: dbt_ditto_ambiguous   # the default
```

```yaml
- name: order_id
  description: Surrogate key for an order.
  meta:
    osmosis_progenitor: seed.platform.raw_orders
    dbt_ditto_ambiguous:
      - source.platform.crm.raw_orders
```

The value lists the ancestors that were overruled; the one that won is already
in the progenitor key. The two switches are independent, so the annotation can
be written with the stderr warnings turned off, and the other way round.

Three rules keep the annotation honest:

- **Only an inherited description is annotated.** Documenting the column
  locally, or pointing at an answer with a directive, means nothing was chosen
  arbitrarily — and removes the annotation on the next run rather than leaving a
  stale claim behind.
- **It is never inherited.** An ancestor's annotation is about that ancestor's
  parents, so it is skipped like the progenitor key rather than copied down.
- **Off by default**, because it writes meta dbt-osmosis would not, and parity
  with an existing dbt-osmosis project is the promise.

## Settling it: `dbt_ditto_definitive`

A warning says the parents disagree. It cannot say who is right, because that is
not a fact about the DAG — it is a decision. Write the decision down:

```yaml
columns:
  - name: customer_id
    description: The account the order was placed under.
    meta:
      dbt_ditto_definitive: true
```

From then on, every column called `customer_id` in the run says that. Not just
the models below: the seed above, the source it came from, and the same column
in the other projects. A decision has no direction, so this one is applied in
all of them.

What the marker does, precisely:

- **It outranks everything else.** Name matching, a directive, `force`, a
  description written by hand — the settled wording replaces all of them.
- **The declaring column is locked.** It is the decision, so nothing is
  inherited into it and nothing overwrites it.
- **The wording is attributed.** A column that takes it records the declaring
  node in `osmosis_progenitor`, exactly as an ordinarily inherited description
  does, so the file still says where its documentation came from.
- **The ambiguity warning stops** for that column, and so does the
  `ambiguity_meta` annotation: nothing arbitrary is happening any more.
- **The marker itself is never inherited.** Only the column that declares the
  decision carries it; the columns that take the wording do not, or every column
  downstream would claim to be the decision too.
- **A manifest-only upstream's declarations are ignored.** A dbt-loom manifest
  cannot be read, reviewed or edited from this repository, so it does not get to
  rewrite documentation here. Declarations in a project you have checked out —
  including one marked `upstream: true` — do count.

Two declarations that disagree **stop the run**:

```
conflicting dbt_ditto_definitive declarations for column "customer_id":
  model.platform.dim_customers.customer_id: "The account the order was placed under."
  model.analytics.customer_report.CUSTOMER_ID: "The customer who placed the order."
  (these are the same column because inheritance.case_insensitive is on)
  resolve it by leaving one declaration, or by making them agree word for word
```

Choosing between them by rule is the exact thing the marker exists to avoid, so
nothing is written until a person settles it. The same wording declared in
several places is one decision, not a conflict.

The key name is not configurable, unlike `progenitor_key` and `ambiguity_key`.
Those name something this tool writes; this one is a contract between projects
that may live in different repositories, and a contract each side spells
differently is not one.

## Where a description came from

Every inherited description records its origin:

```yaml
- name: customer_id
  description: Surrogate key of the customer.
  meta:
    osmosis_progenitor: seed.platform.raw_customers
```

An inherited description is the one line in a schema file nobody wrote, so
leaving its origin out makes copied documentation indistinguishable from
reviewed documentation. It is also the only way to see, in the file itself, that
a description crossed a project boundary. dbt-osmosis leaves this off, so a
project being compared against it byte for byte sets `inheritance.progenitor:
false` — which is what `scripts/parity.sh` does. The key name is
`inheritance.progenitor_key`.

Only genuinely inherited descriptions get one: a column documented locally has
no progenitor, and does not gain a meta key claiming otherwise.

## Following a column through a rename

dbt-osmosis matches columns by name, so documentation stops the moment a column
is aggregated or packed into a struct. `sum(amount_cents) as total_amount_cents`
is the same quantity as `amount_cents`, but it arrives undocumented.

Turning on `inheritance.derived` matches across the rename, in both directions:

```yaml
inheritance:
  derived:
    enabled: true
```

| Downstream column | Inherits from | Why |
| --- | --- | --- |
| `total_amount_cents` | `amount_cents` | a leading or trailing aggregate word is stripped |
| `max_order_date` | `order_date` | same |
| `profile.first_name` | `first_name` | the column was packed into a struct |
| `first_name` | `profile.first_name` | and the reverse, when a struct is unpacked |
| `totals.total_amount_cents` | `amount_cents` | both at once |

This works from any ancestor, sources included, and across project boundaries.

Two rules keep it from doing damage:

- **An exact name match always wins.** A derived match is only ever consulted
  when nothing upstream shares the column's name, so a real match is never
  replaced by a guess.
- **Only whole underscore-separated words are stripped**, and only one at each
  end. `counterparty_id` is not read as an aggregate of `erparty_id`, and
  `max_order_count` does not reduce all the way to `order`.

It is off by default, because it is a lexical heuristic and it changes output
that dbt-osmosis would leave alone — the parity guarantee has to keep holding
for projects that want it. It is also worth knowing what a heuristic buys: in
the fixture, `count(order_id) as order_id_count` inherits *"Surrogate key for an
order."*, which is the description of the thing being counted rather than of the
count. Drop `count` from `inheritance.derived.suffixes` if that trade is not
worth it for your project. Nothing here parses SQL, so a column renamed for
semantic rather than lexical reasons — `lifetime_value_cents` from
`amount_cents` — is still not matched.

Struct *expansion* is separate and on by default: adapters that understand
nested data report `profile.first_name` as a column in its own right, so
dbt-osmosis documents it and dbt-ditto has to as well. It reads the composite
type out of `catalog.json` to get there, and understands DuckDB's
`STRUCT(...)`, BigQuery's `STRUCT<...>` and `ARRAY<STRUCT<...>>`, nested
structs, and quoted field names.

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
at. That is dbt-osmosis' behaviour and it decides who wins a conflict, so it is
reproduced exactly rather than "improved".

## Deliberate differences from dbt-osmosis

All documented, all switchable:

1. **Provenance is recorded.** `inheritance.progenitor` is on, so an inherited
   description carries the node it came from in `meta`. This is the only default
   that changes what an existing dbt-osmosis project's YAML looks like; set it
   to `false` for byte parity.
2. **Comments inside a column list.** dbt-osmosis rebuilds the list from
   scratch, so ruamel drops every comment except the one above the first entry.
   dbt-ditto keeps each comment with the column it annotates. Set
   `output.comments: osmosis` for the lossy behaviour — which is what the parity
   run uses, so the comparison is like for like.
3. **Snapshots as inheritance sources.** dbt-osmosis' ancestor walk only follows
   `model.`, `seed.` and `source.` dependencies. dbt-ditto also follows
   `snapshot.`.
4. **Where warehouse comments come from.** Both tools use them; dbt-osmosis asks
   the adapter, dbt-ditto reads `catalog.json`. On an adapter that reports
   comments, such as Snowflake, they agree. On DuckDB the adapter reports none
   while the catalog has them, so dbt-ditto finds documentation dbt-osmosis
   cannot see. The parity run sets `columns.comments: never` to compare like
   for like.

`inheritance.backfill`, `inheritance.derived` and `inheritance.ambiguity_meta`
are also divergences, but all three are off by default, so an existing
dbt-osmosis project is unaffected until you ask for them. Directives and
ambiguity warnings are on, and neither changes output: a directive only fires on
a description written to be one, and a warning is printed rather than written.

