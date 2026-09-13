# Source providers

Optional add-ons that document **external sources**: the raw tables no loaded dbt
project builds. A source is a DAG root, so inheritance can never reach it, but
the warehouse usually knows more than your `sources:` YAML does.

A provider is a separate program: dbt-ditto writes it the list of external
sources, it writes documentation back. BigQuery, Snowflake and a worked example
ship in [`packaging/providers/`](../packaging/providers/README.md); a new one is
another file there and no change to dbt-ditto.

## Setting one up

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

**Credentials are dbt's own.** A provider resolves the connection from
`profiles.yml`, using the same entry, target and method `dbt run` uses.
`--target` picks the target, falling back to `$DBT_TARGET`.

**Only external sources are asked about**, so a refresh covers tens of tables
rather than thousands. A source pointing at a relation another loaded project
*builds* is reported, not fetched; a cross-project `ref()` is the fix.

**Answers are cached** in `target/ditto-sources.json`, so `--check` never dials
out: it runs in CI, where a flaky API must not fail a formatting check. A missing
cache means no enrichment, not an error. Providers run in parallel, and one
failing is a warning unless `sources.strict`.

## Settings

```yaml
sources:
  strict: false           # true: a provider that fails stops the run
  providers:
    - command: "uv run --with google-cloud-bigquery packaging/providers/bigquery.py"
      match: { database: "bq-*" }   # database / schema / name globs
      labels: {}          # the block below, overridden per provider
  labels:
    mode: meta            # meta | tags | both | ignore
    meta_key: labels      # nest under meta.labels; "" flattens into meta
    tag_format: "{key}:{value}"
    exclude: ["terraform_*"]
    propagate:
      column: true        # off stops labels at the source
      structs: true       # a pack or unpack is the same data, reshaped
      aggregates: warn    # inherit | warn | ignore
      on_conflict: warn   # warn | first | none
```

`mode` defaults to `meta` because a dbt tag is a **selector**, and making every
warehouse label selectable would change what `--select tag:...` matches.
`meta_key` defaults to `labels` so provider pairs stay distinguishable from
hand-written meta; the propagation rules below only apply while they are. An
empty value renders as the bare key. Keys are **not** normalised: rewriting one
makes the YAML stop matching the table.

## Labels

Key-value pairs on an object are the one kind of metadata most warehouses have
(BigQuery labels, Snowflake and Unity Catalog tags, Glue table parameters), so a
provider reports them raw and dbt-ditto routes them. Policy tags and masking
policies have no equivalent elsewhere, so they stay provider-side: reported as
column labels, and dbt-ditto never learns what they are.

## Propagation

Table-level labels (owner, cost centre, Terraform stack) describe the physical
object and are false the moment they are copied onto a model elsewhere, so they
stop at the source. Column-level labels and policy tags describe the data in the
column, so they travel like a description, following the resolver's matching:

| Match | Description | Labels |
| --- | --- | --- |
| Exact name | inherits | inherits |
| Struct pack / unpack | inherits | **inherits**. Reshaping, not transforming: `profile.first_name` holds the same bytes the flat `first_name` did |
| Aggregate (`sum_`, `avg_`) | inherits | **does not**. The column still means "salary, averaged", so the wording survives; the value it classified does not |

Unpacking a struct is one origin to many columns, and safe. Packing flat columns
*into* one means it carries data that may be classified differently, so
`on_conflict` applies; BigQuery applies policy tags at leaf level anyway, so the
common direction is the safe one.

Both `warn` defaults exist because dropping a classification is the dangerous
direction: a missing description is visibly incomplete, a missing policy tag
reads as "not restricted", a false claim. So nothing is written and the run says
what it declined to do. A propagated entry records its origin, because writing
`policy_tag: pii/high` downstream does **not** apply it in the warehouse. Labels
routed to *tags* travel regardless, as dbt carries tags.

## Precedence

Provider output counts as the source's **own** documentation: hand-written YAML
beats provider output, which beats backfill from descendants. `meta` and `tags`
merge additively, hand-written keys winning collisions.

## Known gaps

- No provider for Databricks, Redshift or Glue.
- `extra_keys` values inherit like meta and are *not* gated by match rank, so a
  policy tag reported as an extra key rather than a label travels across an
  aggregate match where a label would not.

Writing a provider: [`packaging/providers/README.md`](../packaging/providers/README.md).
Internals: [AGENTS.md](../AGENTS.md#source-providers).
