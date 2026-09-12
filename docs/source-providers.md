# Source providers

Optional bolt-ons that document **external sources**: the raw tables nothing in
any loaded dbt project builds. A source is a DAG root, so inheritance can never
reach it. This is the reference for the contract and the propagation rules;
configuring them is in
[usage.md](usage.md#asking-the-warehouse-source-providers).

## What a provider is

A separate program. dbt-ditto writes the list of external sources it wants
answers for to the provider's stdin; the provider writes documentation to
stdout. Subprocess rather than a Go interface because `buildmode=plugin` is
unusable across the platforms this ships to; because the binary keeps its two
pure-Go dependencies and its "never connects to a warehouse" property; and
because providers can then be written in Python, where every warehouse SDK
lives. Reference providers for BigQuery and Snowflake, plus `echo.py`, are in
[`packaging/providers/`](../packaging/providers/README.md).

## The contract is dbt YAML, not warehouse metadata

Modelling BigQuery's vocabulary would break on Snowflake, which has no labels,
on Unity Catalog, which has two kinds of tag, and on Postgres, which has only
comments. So the contract is the shape of the *output*, which is dbt's own and
therefore already universal. The request and response JSON is in
[`packaging/providers/README.md`](../packaging/providers/README.md#the-contract):
`meta` and `tags` are a provider stating something outright, `labels` is the
neutral bucket dbt-ditto routes, and `version` is bumped only for a breaking
change. Connection configuration is dbt's own — the request carries each
project's root, `profile:`, target and profiles directory.

Provider output becomes a `dbt.CatalogNode` (`sources.Apply`) rather than a
second source of column truth: a catalog is already the thing that says what a
relation contains, in what order and with what types, and column injection,
stale removal, ordering and struct expansion all read one. What a catalog has no
room for — meta, tags, labels, a policy tag — is written onto the source's
manifest columns, which is what inheritance reads.

## Settings

```yaml
sources:
  strict: false           # a provider that fails stops the run
  providers:
    - command: "uv run --with google-cloud-bigquery packaging/providers/bigquery.py"
      match: { database: "bq-*" }   # database / schema / name globs
      labels: {}          # the block below, per provider
  labels:
    mode: meta            # meta | tags | both | ignore
    meta_key: labels      # nest under meta.labels; "" flattens into meta
    tag_format: "{key}:{value}"
    exclude: ["terraform_*"]
    propagate:
      column: true        # off stops labels at the source
      structs: true       # a struct pack or unpack is the same data, reshaped
      aggregates: warn    # inherit | warn | ignore
      on_conflict: warn   # warn | first | none
```

## Labels are the portable part

Key-value pairs attached to an object are the one piece of metadata every
warehouse has, so routing them belongs in core rather than in each provider:

| Warehouse | key-value feature |
| --- | --- |
| BigQuery | labels (table) |
| Snowflake | tag objects (table and column) |
| Databricks / Unity Catalog | tags (table and column) |
| Glue / Athena | table parameters |
| Postgres | none |

Policy tags, masking policies and row-access policies have no equivalent
anywhere else, so they stay provider-side: a provider reports them as column
labels and dbt-ditto never learns what they are.

The `labels` block is settable per provider as well as globally. `mode` defaults
to `meta` because a dbt tag is a **selector**, and making every warehouse label
selectable would silently change what `--select tag:...` matches. `meta_key`
defaults to `labels` so provider-supplied pairs stay distinguishable from meta
written by hand. A pair with an empty value renders as the bare key, since
BigQuery permits valueless labels. Keys are **not** normalised: warehouses
disagree about case and character set, and rewriting a key makes the YAML stop
matching what is on the table.

## Propagation

Whether metadata travels downstream keys off the **level it sits at**, which
gets both cases right without a per-key allowlist. Table-level labels (owner,
cost centre, Terraform stack) describe the physical object and are false the
moment they are copied onto a model in another dataset, so they stop at the
source; there is no `propagate.table` key, because node meta is not inherited by
anything here. Column-level labels and policy tags describe the data in the
column, so they travel like a description.

Propagation then follows the resolver's own matching, split by *kind* of
derivation:

| Match | Description | Labels |
| --- | --- | --- |
| Exact name | inherits | inherits |
| Struct pack / unpack | inherits | **inherits** — reshaping, not transforming: `profile.first_name` holds the same bytes the flat `first_name` did |
| Aggregate (`sum_`, `avg_`) | inherits | **does not** — the column still means "salary, averaged", so the wording survives; the value it classified does not |

Three consequences:

- **Packing is a merge; unpacking is not.** Unpacking `profile` into its fields
  is one origin to many columns, and safe. Packing several flat columns *into* a
  struct means the struct carries data from columns that may be classified
  differently, so the conflict rule applies. On BigQuery policy tags are applied
  at leaf level anyway, so the common direction is the safe one.
- **Dropping a classification is the dangerous direction.** A missing
  description is visibly incomplete; a missing policy tag reads as "not
  restricted", which is a false claim. So `aggregates` defaults to `warn`: the
  label is not written, but the run says `avg_salary` derives from a column
  marked `restricted`. `ignore` is there when that proves noisy.
- **Conflicts must not resolve silently.** Descriptions break ties by lowest
  `unique_id` and warn; for a classification an arbitrary choice between
  `restricted` and `public` has consequences, so `on_conflict` defaults to `warn`
  and writes nothing. Ranking restrictiveness needs an ordering core cannot learn.

A propagated entry records its origin, as `inheritance.progenitor` does for
descriptions, because writing `policy_tag: pii/high` onto a downstream column
does **not** apply that policy tag in the warehouse.

`propagate.column` is implemented by adding the label meta key to
`SkipMetaKeys`, which inheritance already consults. That is why labels are
nested under a key by default: flattening them into meta makes them
indistinguishable from meta somebody typed, and the propagation rules stop
applying. Labels routed to *tags* travel regardless, since dbt's own rules carry
tags downstream.

## Precedence

Provider output occupies the rung the warehouse comment occupies (`resolve.go`,
where `columnTruth.comment` seeds knowledge): it counts as the source's **own**
documentation.

```
hand-written YAML          wins over
provider output            wins over
backfill from descendants
```

`meta` and `tags` merge additively, with hand-written keys winning a collision.

## Determinism, and why `--check` never dials out

Providers make network calls, and `--check` runs in CI where a flaky API must
not fail a formatting check and the runner may hold no credentials. So providers
write to a cache — `target/ditto-sources.json` — and `--check` reads the cache
and never spawns anything. Refreshing is explicit (`inherit
--refresh-sources`). A missing cache is not an error; it means no source
enrichment, exactly as a missing catalog means no column reconciliation.
Providers run in parallel, and one failing is a warning unless `sources.strict`
is set.

## Which sources get sent

Only **external** ones: `source.*` nodes whose relation no loaded project
builds. N is tens, not thousands, which is what makes a refresh sub-second where
`dbt docs generate` takes minutes to reach the same handful. BigQuery's
`tables.get` is free metadata: no query job, no `jobUser` role, fully parallel.

Loom upstreams arrive as `model.*` nodes, so they are documented through the
graph and never sent. A `source:` pointing at a table another loaded project
builds as a model is indistinguishable from a genuine external table — both are
graph roots — but it is detectable by relation, and the response is a **warning,
not an API call**.

A source backed by a **seed** looks identical and is not a mistake: seeds are
how a project stands up fake raw data, and the DuckDB fixture does exactly this.
So `ShadowedSource.Mistake()` is false for a seed and no warning is printed —
but the source is still not external, because dbt already knows its columns.

## Where the parts live

| | |
| --- | --- |
| Classification, shadowed-source warning | `internal/inherit/sources.go`, `internal/runner/sources.go` |
| Contract, subprocess, cache | `internal/sources/` |
| Label routing and propagation | `internal/sources/apply.go`, `internal/inherit/labels.go` |
| Unknown column fields | `dbt.Column.Extra`, `dbt.ExtraColumnKeys`, `inheritance.extra_keys` |
| Reference providers | `packaging/providers/` |

Testing without an account:
[development.md](development.md#source-providers-tested-without-an-account).

## Not covered

- No provider for Databricks, Redshift or Glue. Each is a new file in
  `packaging/providers/` and no change to dbt-ditto.
- `extra_keys` values inherit like meta and are *not* gated by match rank, so a
  policy tag reported as an extra key rather than a label travels across an
  aggregate match where a label would not.
- The BigQuery provider emits the parent of a record as well as every dotted
  leaf, matching `catalog.json`; dbt-osmosis running live against BigQuery emits
  only the leaves. That divergence already existed and is unchanged.
