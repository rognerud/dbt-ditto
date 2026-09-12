# Source providers

Optional bolt-ons that document **external sources**: the raw tables nothing in
any loaded dbt project builds. They are the one place inheritance can never
reach, because a source is a DAG root and there is nothing above it.

This is the reference for the provider contract and the propagation rules.
Configuring them is in [usage.md](usage.md#asking-the-warehouse-source-providers).

## What a provider is

A separate program. dbt-ditto writes the list of external sources it wants
answers for to the provider's stdin; the provider writes documentation to
stdout. Subprocess rather than a Go interface, for three reasons:

- Go's `buildmode=plugin` is unusable across the platforms this ships to.
- The binary keeps its two pure-Go dependencies and its "never connects to a
  warehouse" property. A locked-down CI runner still needs no credentials.
- Providers can be written in Python, which is where every warehouse SDK lives
  and what a dbt shop already has installed.

Reference providers for BigQuery and Snowflake, plus `echo.py`, are in
[`packaging/providers/`](../packaging/providers/README.md).

## The contract is dbt YAML, not warehouse metadata

Modelling BigQuery's vocabulary would break on Snowflake, which has no labels,
on Unity Catalog, which has two kinds of tag, and on Postgres, which has nothing
but comments. So the contract is the shape of the *output*, which is already
universal because it is dbt's own. The request and response JSON is in
[`packaging/providers/README.md`](../packaging/providers/README.md#the-contract);
`meta` and `tags` are a provider stating something outright, `labels` is the
neutral bucket dbt-ditto routes, and `version` is bumped only for a breaking
change.

Connection configuration is dbt's own: the request carries each project's root,
`profile:`, target and profiles directory, so a provider resolves credentials
from `profiles.yml` exactly as `dbt run` does.

Provider output becomes a `dbt.CatalogNode` (`sources.Apply`) rather than a
second source of column truth: a catalog is already the thing that says what a
relation contains, in what order, with what types, and column injection, stale
removal, ordering and struct expansion all read one. A provider is therefore a
catalog for the relations `dbt docs generate` would have walked the whole project
to reach. What a catalog has no room for — meta, tags, labels, a policy tag — is
written onto the source's manifest columns, which is what inheritance reads.

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

The `labels` block is settable per provider as well as globally — a Snowflake tag
and a Glue table parameter often deserve different treatment in one project.
`mode` defaults to `meta` because a dbt tag is a **selector**, and making every
warehouse label selectable would silently change what `--select tag:...` matches.
`meta_key` defaults to `labels` so provider-supplied pairs stay distinguishable
from meta written by hand. A pair with an empty value renders as the bare key,
not `key:`, since BigQuery permits valueless labels and they are common. Keys are
**not** normalised: warehouses disagree about case and character set, and
rewriting a key makes the YAML stop matching what is on the table.

## Propagation

Whether metadata travels downstream keys off the **level it sits at**, which gets
the right answer for both cases without a per-key allowlist:

- **Table-level** labels (owner, cost centre, Terraform stack) describe the
  physical object. They are false the moment they are copied onto a model in a
  different dataset. Stop at the source. There is no `propagate.table` key,
  because node meta is not inherited by anything in this tool.
- **Column-level** labels and policy tags describe the data in the column. An
  access rule has no reason to change as the column moves between projects, so
  they travel exactly like a description.

Propagation then follows the resolver's own matching, split by *kind* of
derivation, because the two kinds differ in what they preserve:

| Match | Description | Labels |
| --- | --- | --- |
| Exact name | inherits | inherits |
| Struct pack / unpack | inherits | **inherits** — reshaping, not transforming: `profile.first_name` holds the same bytes the flat `first_name` did |
| Aggregate (`sum_`, `avg_`) | inherits | **does not** — the column still means "salary, averaged", so the wording survives; the value it classified does not |

Three consequences:

**Packing is a merge; unpacking is not.** Unpacking `profile` into its fields is
one origin to many columns, and safe. Packing several flat columns *into* a
struct means the struct carries data from columns that may be classified
differently — the conflict rule applies. On BigQuery policy tags are applied at
leaf level anyway, so the common direction is the safe one.

**Dropping a classification is the dangerous direction.** A missing description
is visibly incomplete. A missing policy tag reads as "this column is not
restricted", which is a false claim. So `aggregates` defaults to `warn`: the
label is not written, but the run says `avg_salary` derives from a column marked
`restricted`. `ignore` is there for when that proves noisy.

**Conflicts must not resolve silently.** Descriptions break ties by lowest
`unique_id` and warn. For a classification, an arbitrary choice between
`restricted` and `public` has consequences, so `on_conflict` defaults to `warn`
and writes nothing; ranking restrictiveness needs an ordering core has no way to
learn.

A propagated entry records its origin, the way `inheritance.progenitor` does for
descriptions, because writing `policy_tag: pii/high` onto a downstream column
does **not** apply that policy tag in the warehouse. It reads as "upstream says
this is restricted" rather than "this is restricted".

`propagate.column` is implemented by adding the label meta key to
`SkipMetaKeys`, which inheritance already consults. That is why labels are nested
under a key by default: flattening them into meta makes them indistinguishable
from meta somebody typed, and the propagation rules stop applying. Labels routed
to *tags* travel regardless, since dbt's own rules carry tags downstream.

## Precedence

Provider output occupies the rung the warehouse comment occupies
(`resolve.go`, where `columnTruth.comment` seeds knowledge): it counts as the
source's **own** documentation.

```
hand-written YAML          wins over
provider output            wins over
backfill from descendants
```

A description written by a person is never overwritten by a machine, and the
backfill guess is the last resort. `meta` and `tags` merge additively, with
hand-written keys winning a collision.

## Determinism, and why `--check` never dials out

Providers make network calls. `--check` runs in CI, where a flaky API must not
fail a formatting check and where the runner may hold no warehouse credentials.

So providers write to a cache — `target/ditto-sources.json` — and `--check` reads
the cache and never spawns anything. Refreshing is explicit
(`inherit --refresh-sources`). A missing cache is not an error; it means no source
enrichment, exactly as a missing catalog means no column reconciliation.

Providers run in parallel. One failing is a warning, not a failed run, unless
`sources.strict` is set: a documentation tool that cannot reach BigQuery should
still tidy the YAML.

## Which sources get sent

Only **external** ones: `source.*` nodes whose relation no loaded project builds.
N is tens, not thousands, which is what makes a refresh sub-second where
`dbt docs generate` takes minutes to walk every relation for the same handful.
BigQuery's `tables.get` is free metadata: no query job, no `jobUser` role, fully
parallel.

Loom upstreams arrive as `model.*` nodes — the downstream project `ref()`s them
and loom injects the manifest — so they are documented through the graph and are
never sent to a provider.

A `source:` pointing at a table another loaded project builds as a model is
indistinguishable from a genuine external table: both are graph roots. It is
detectable by relation, and the response is a **warning, not an API call** — that
table can be documented through the normal chain, and reaching for the warehouse
is the wrong fix.

A source backed by a **seed** looks identical and is not a mistake: seeds are how
a project stands up fake raw data, and the source declaration is what the rest of
the project reads it through. The DuckDB fixture does exactly this. Both halves
are meant to exist, so `ShadowedSource.Mistake()` is false for a seed and no
warning is printed — but the source is still not external, because dbt already
knows its columns.

## Where the parts live

| | |
| --- | --- |
| Classification, shadowed-source warning | `internal/inherit/sources.go`, `internal/runner/sources.go` |
| Contract, subprocess, cache | `internal/sources/` |
| Label routing and propagation | `internal/sources/apply.go`, `internal/inherit/labels.go` |
| Unknown column fields | `dbt.Column.Extra`, `dbt.ExtraColumnKeys`, `inheritance.extra_keys` |
| Reference providers | `packaging/providers/` |

How all of it is tested without an account is in
[development.md](development.md#source-providers-tested-without-an-account).

## Not covered

- No provider for Databricks, Redshift or Glue. Each is a new file in
  `packaging/providers/` and no change to dbt-ditto.
- `extra_keys` values inherit like meta — an ancestor's value overwrites the
  column's — and are *not* gated by match rank, so a policy tag reported as an
  extra key rather than a label travels across an aggregate match where a label
  would not.
- The BigQuery provider emits the parent of a record as well as every dotted
  leaf, matching `catalog.json`. dbt-osmosis running live against BigQuery emits
  only the leaves. That divergence already existed and is unchanged.
