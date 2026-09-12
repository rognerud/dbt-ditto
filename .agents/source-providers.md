# Source providers

Optional bolt-ons that document **external sources**: the raw tables nothing in
any loaded dbt project builds. They are the one place inheritance can never
reach, because a source is a DAG root and there is nothing above it.

This is a design record. Nothing here is implemented yet except where the
Status section says otherwise. User-facing configuration belongs in
[usage.md](usage.md) once it exists; the traps belong in [AGENTS.md](../AGENTS.md).

## The problem

dbt-ditto propagates documentation downhill, which works because `manifest.json`
records what is built from what. Sources have no upstream, so today they are
documented three ways, all weak:

1. Whatever is written in the `sources:` YAML by hand.
2. The warehouse's own column comment, via `catalog.json` (`columns.comments`).
3. Backfill from the staging model downstream (`inheritance.backfill`) — a guess.

(2) is the only one that reaches metadata nobody typed into dbt, and it costs a
full `dbt docs generate`: dbt inspects every relation in the project to learn
about the handful that are external. It also carries only a description. Labels,
tags and policy tags have no slot in `catalog.json` at all.

## What a provider is

A separate program. dbt-ditto writes the list of external sources it wants
answers for to the provider's stdin; the provider writes documentation to
stdout.

Subprocess rather than a Go interface, for three reasons:

- Go's `buildmode=plugin` is unusable across the platforms this ships to.
- The binary keeps its two pure-Go dependencies and its "never connects to a
  warehouse" property. A locked-down CI runner still needs no credentials.
- Providers can be written in Python, which is where every warehouse SDK
  actually lives and what a dbt shop already has installed.

### The contract is dbt YAML, not warehouse metadata

The mistake to avoid is modelling BigQuery's vocabulary and then discovering
Snowflake has no labels, Unity Catalog has two kinds of tag, and Postgres has
nothing but comments. So the contract is the shape of the *output*, which is
already universal because it is dbt's own:

```json
// stdin
{ "version": 1,
  "sources": [
    { "unique_id": "source.bqshop.crm.raw_customers",
      "database": "bq-project", "schema": "raw", "identifier": "raw_customers" }
  ]}
```

```json
// stdout
{ "version": 1,
  "sources": [
    { "unique_id": "source.bqshop.crm.raw_customers",
      "description": "Customer master from the CRM.",
      "labels": { "owner": "crm-team", "pii": "high", "cost_centre": "" },
      "meta": { "refresh": "hourly" },
      "tags": ["governed"],
      "columns": [
        { "name": "customer_id", "data_type": "INT64", "index": 1,
          "description": "Surrogate key assigned by the CRM.",
          "labels": { "classification": "restricted" } }
      ]}
  ],
  "warnings": ["no access to project bq-other"] }
```

`meta` and `tags` are for a provider stating something outright. `labels` is the
neutral bucket dbt-ditto routes — see below. `index` populates catalog-order
sorting; omit it and the provider's own order is used.

Unknown keys are ignored in both directions, which is what lets the contract
grow without a version bump. `version` is bumped only for a breaking change.

### Labels are the portable part

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

## Configuration

```yaml
sources:
  providers:
    - command: dbt-ditto-bigquery
      match: { database: "bq-*" }
    - command: ./scripts/glue-catalog.py
      match: { schema: "legacy_*" }

  labels:
    mode: meta            # meta | tags | both | ignore
    meta_key: labels      # nest under meta.labels; "" flattens into meta
    tag_format: "{key}:{value}"
    include: []           # when non-empty, the only keys accepted
    exclude: ["terraform_*"]
    propagate:
      table: false
      column: true
      structs: true
      aggregates: warn    # inherit | warn | ignore
      on_conflict: warn   # warn | first | none
```

The `labels` block is settable per provider too: a Snowflake tag and a Glue
table parameter often deserve different treatment in the same project.

`mode` defaults to `meta` because a dbt tag is a **selector**. Making every
warehouse label selectable would silently change what `--select tag:...` matches
in a project that never asked for it. `meta_key` defaults to `labels` so
provider-supplied pairs stay distinguishable from meta written by hand.

A pair with an empty value renders as the bare key, not `key:`. BigQuery permits
valueless labels and they are common.

Keys are **not** normalised. Warehouses disagree about case and character set,
and rewriting a key makes the YAML stop matching what is actually on the table.

## Propagation

Whether metadata travels downstream keys off the **level it sits at**, which
gets the right answer for both cases without a per-key allowlist:

- **Table-level** labels (owner, cost centre, Terraform stack) describe the
  physical object. They are false the moment they are copied onto a model in a
  different dataset. Stop at the source.
- **Column-level** labels and policy tags describe the data in the column. An
  access rule has no reason to change as the column moves between projects, so
  they travel exactly like a description.

Propagation then follows the same matching the resolver already does, split by
*kind* of derivation, because the two kinds differ in what they preserve:

| Match | Description | Labels |
| --- | --- | --- |
| Exact name | inherits | inherits |
| Struct pack / unpack | inherits | **inherits** — reshaping, not transforming: `profile.first_name` holds the same bytes the flat `first_name` did |
| Aggregate (`sum_`, `avg_`) | inherits | **does not** — the column still means "salary, averaged", so the wording survives; the value it classified does not |

Three consequences worth writing down:

**Packing is a merge; unpacking is not.** Unpacking `profile` into its fields is
one origin to many columns, and safe. Packing several flat columns *into* a
struct means the struct carries data from columns that may be classified
differently — that is the conflict case, and the conflict rule applies. On
BigQuery policy tags are applied at leaf level anyway, so the common direction is
the safe one.

**Dropping a classification is the dangerous direction.** A missing description
is visibly incomplete and misleads nobody. A missing policy tag reads as "this
column is not restricted", which is a claim, and a false one. So `aggregates`
defaults to `warn`: the label is not written, but the run says that `avg_salary`
derives from a column marked `restricted`. Silence is the only option that can
mislead. `ignore` is there for when it proves noisy.

**Conflicts must not resolve silently.** Descriptions break ties by lowest
`unique_id` and warn (`inheritance.warn_ambiguous`). For a classification an
arbitrary choice between `restricted` and `public` is a failure with
consequences, so `on_conflict` defaults to `warn` and writes nothing. Core
cannot rank restrictiveness without being told an ordering, and inventing one
would be worse than declining to answer.

### Written metadata is a claim, not an enforcement

dbt-ditto writing `policy_tag: pii/high` onto a downstream column does **not**
apply that policy tag in the warehouse. Someone reading the YAML could
reasonably conclude the column is protected when nothing is protecting it.

So a propagated entry records its origin, the way `inheritance.progenitor`
already does for descriptions. It then reads as "upstream says this is
restricted" rather than "this is restricted", which is the distinction that
matters the first time it is wrong.

## Precedence

Provider output occupies exactly the rung the warehouse comment occupies today
(`resolve.go`, where `columnTruth.comment` seeds knowledge): it counts as the
source's **own** documentation.

```
hand-written YAML          wins over
provider output            wins over
backfill from descendants
```

That keeps the existing rule intact — a description written by a person is never
overwritten by a machine — and demotes the backfill guess to the last resort it
should always have been. `meta` and `tags` merge additively, with hand-written
keys winning a collision.

## Determinism, and why `--check` never dials out

Providers make network calls. `--check` runs in CI, where a flaky API must not
fail a formatting check, and where the runner may hold no warehouse credentials
at all.

So providers write to a cache — `target/ditto-sources.json` — and `--check`
reads the cache and never spawns anything. Refreshing is an explicit step
(`dbt-ditto sources refresh`, or `inherit --refresh-sources`). A missing cache
is not an error; it means no source enrichment, exactly as a missing catalog
means no column reconciliation today.

Providers run in parallel. One failing is a warning, not a failed run, unless
`sources.strict` is set: a documentation tool that cannot reach BigQuery should
still tidy the YAML.

## Which sources get sent

Only **external** ones: `source.*` nodes whose relation no loaded project
builds.

Loom upstreams arrive as `model.*` nodes — the downstream project `ref()`s them
and loom injects the manifest — so they are already documented through the graph
and are never sent to a provider.

The case that needs care is the pre-loom pattern where a team declares a
`source:` pointing at a table another loaded project builds as a model. Today
those are indistinguishable from genuine external tables: both are graph roots.
They are detectable by relation, and the right response is a **warning, not an
API call** — that table can be documented properly through the normal chain, and
reaching for the warehouse is the wrong fix.

A source backed by a **seed** looks identical to that and is not a mistake:
seeds are how a project stands up fake raw data, and the source declaration is
what the rest of the project reads it through. The DuckDB fixture does exactly
this, which is how the distinction was found. Both halves are meant to exist, so
`ShadowedSource.Mistake()` is false for a seed and no warning is printed —
but the source is still not external, because dbt already knows its columns.

## Cost

Answering only external sources is what makes this fast. N is tens, not
thousands. BigQuery's `tables.get` is free metadata: no query job, no `jobUser`
role, fully parallel. That is a sub-second refresh against minutes for
`dbt docs generate`, which walks every relation in the project to reach the same
handful.

It is also nearly free of parity risk. The fetched data covers only nodes that
currently have no documentation path at all, so `trueColumns` starts returning
`haveTruth` for sources and for nothing else; `Node.EffectiveColumn` shadowing is
untouched because sources shadow nothing; and the dbt-osmosis golden tests never
see it.

## Build order

1. **External-source predicate** and the shadowed-source warning. Useful on its
   own, no new configuration, no network.
2. **Catch-all for unknown column fields** in `dbt.Column`, so `policy_tags` and
   friends survive a round trip. Already a listed TODO
   (`add-inheritance-for-specified-keys`), and a prerequisite for propagating
   anything a provider reports.
3. **Provider runner**: spawn, pipe, parse, merge, cache.
4. **Label routing** into `meta` / `tags`.
5. **Propagation**, with the conflict and aggregate warnings.
6. **Reference BigQuery provider**, shipped separately so the binary's
   dependency count does not move.

## How it ended up being built

Two decisions were made during implementation that are worth recording, because
both replaced a mechanism the design above implied with one that already existed.

**Provider output becomes a catalog entry.** `sources.Apply` writes a
`dbt.CatalogNode` for each documented source rather than inventing a second
source of column truth. A catalog is already the thing that says what a relation
really contains, in what order, with what types — and column injection, stale
removal, ordering and struct expansion all read one. A provider is therefore a
catalog for the relations `dbt docs generate` would have walked the whole project
to reach. What a catalog has no room for (meta, tags, labels, a policy tag) is
written onto the source's manifest columns, which is what inheritance reads.

**`propagate.column` uses the skip list, not a second mechanism.** Turning it off
adds the label meta key to `SkipMetaKeys`, which inheritance already consults.
That is why labels are nested under a key by default: flattening them into meta
makes them indistinguishable from meta somebody typed, and the propagation rules
stop applying. Labels routed to *tags* travel regardless — a dbt tag is a
selector, and dbt's own rules carry those downstream.

Two smaller things that had to change:

- `OrderedMap` had no `MarshalYAML`, so a nested meta value serialised as `{}`.
  It appeared, it was empty, and nothing errored.
- A source's own description had nowhere to be written: `resolveNodeLevel` only
  ever wrote an *inherited* one, and that is off by default. It now writes the
  node's description when the YAML has none and the node has one — which for
  every node but a source is impossible, since the node's description came from
  that YAML in the first place.

There is no `propagate.table` key. A relation's own labels never travel, and
cannot: node meta is not inherited by anything in this tool. The level the
metadata sits at decides it, so there is nothing to configure.

## Status

All six steps are done.

- [x] 1 — external-source predicate (`internal/inherit/sources.go`,
      `Graph.ClassifySources`), with the shadowed-source warning in
      `internal/runner/sources.go`
- [x] 2 — unknown column fields (`dbt.Column.Extra`, `dbt.ExtraColumnKeys`,
      `inheritance.extra_keys`)
- [x] 3 — provider runner (`internal/sources`: contract, subprocess, cache),
      wired in `runner.applySources`, driven by `--refresh-sources`
- [x] 4 — label routing (`sources.labels.*`)
- [x] 5 — propagation, with the aggregate and conflict warnings
      (`internal/inherit/labels.go`)
- [x] 6 — reference providers for **BigQuery and Snowflake**, in
      `packaging/providers/`, plus `echo.py`, which is both the shortest
      complete example and what the Go contract test runs

Connection configuration is dbt's own: the request carries each project's root,
`profile:`, target and profiles directory, so a provider resolves credentials
from `profiles.yml` exactly as `dbt run` does for that project. dbt's loader is
used when `dbt-core` is importable; otherwise `profiles.yml` is read directly
with `env_var` resolved.

### Tests

| What | Where | Needs |
| --- | --- | --- |
| Classification, routing, propagation, extra keys | `internal/runner/settings_test.go` via `sources_settings_test.go` — one case per config key, as every setting requires | Go |
| Provider spawning, merging, cache, failure handling | `internal/sources/sources_test.go` | Go |
| The wire format, both sides | `internal/runner/provider_test.go` (`TestPythonProviderRoundTrip`, `TestProviderIsToldWhereDbtKeepsItsCredentials`), running `packaging/providers/echo.py` | Go + python3 |
| Contract parsing, profiles.yml, adapter type rendering | `packaging/providers/test_providers.py`, `make providers` | Python |

No account, credential or container is needed for any of it.

### Not done

- No provider for Databricks, Redshift or Glue. Each is a new file in
  `packaging/providers/` and no change to dbt-ditto.
- `extra_keys` values inherit like meta — an ancestor's value overwrites the
  column's. They are *not* gated by match rank, so a policy tag reported as an
  extra key rather than a label travels across an aggregate match where a label
  would not. Worth reconciling if extra keys turn out to be used mostly for
  classifications.
- The BigQuery provider emits the parent of a record as well as every dotted
  leaf, matching `catalog.json`. dbt-osmosis running live against BigQuery emits
  only the leaves. That divergence already existed and is unchanged.
