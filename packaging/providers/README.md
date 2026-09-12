# Source providers

Optional add-ons that document **external sources** — the raw tables no dbt
project builds — from the warehouse's own metadata.

Inheritance cannot reach a source. A source is a root of the DAG, so there is
nothing upstream to inherit from, and the only documentation that exists is
whatever somebody typed into the `sources:` YAML. The warehouse usually knows
more: a description on the table, a description on each column, labels or tags,
a policy tag. A provider fetches that.

| | |
|---|---|
| `bigquery.py` | table and column descriptions, labels, policy tags |
| `snowflake.py` | table and column comments, tags |
| `echo.py` | answers from a file — the shortest complete example, and what the Go test suite runs |
| `dbt_ditto_provider.py` | shared: the contract, and profiles.yml resolution |

## Why these are separate programs

dbt-ditto is a single static binary with two pure-Go dependencies that connects
to nothing. Bundling a warehouse SDK would cost all three properties, for a
feature most projects do not use. So dbt-ditto writes a request to a provider's
stdin and reads documentation from its stdout, and the provider can be written
in whatever language the warehouse's SDK is best in — which, for a dbt shop, is
Python they already have installed.

Adding a warehouse means writing one of these. It needs no change to dbt-ditto.

## Credentials come from dbt

A provider is told each project's root, its `profile:` and the profiles
directory, and resolves the connection from `profiles.yml` — the same entry,
the same target, the same method that `dbt run` uses for that project. Nothing
is configured twice, and there is no second copy of a secret to keep in step.

dbt's own loader is used when `dbt-core` is importable, because it is the only
thing guaranteed to agree with dbt about Jinja and `env_var` defaults. Failing
that, `profiles.yml` is read directly and `env_var` is resolved, so a CI job
that refreshes documentation needs the warehouse SDK but not dbt.

`--target` picks the target, falling back to `$DBT_TARGET` and then to whatever
the profile calls default. `$DBT_PROFILES_DIR` is honoured, as is a
`profiles.yml` beside `dbt_project.yml`.

## Using one

```yaml
# dbt_ditto.yml
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
dbt-ditto inherit --check             # likewise, so CI needs no credentials
```

The split matters. Providers make network calls, and `--check` runs in CI where
a flaky API must not fail a formatting check and the runner may hold no
warehouse credentials at all. So a refresh is something a person asks for, and
its answer is cached in `target/ditto-sources.json`. Commit it, or restore it
from a CI artifact — either way the ordinary run spawns nothing.

A provider that fails is a warning, not a failed run, unless `sources.strict`
is set.

## Cost

Only external sources are asked about — tens of tables, not thousands. Loom
upstreams arrive as models and are documented through the graph; a source
backed by a seed is already known to dbt. Neither is ever sent to a provider.

BigQuery's `tables.get` is free metadata: no query job, no `jobUser` role, and
the calls run in parallel. Snowflake is one INFORMATION_SCHEMA query per schema.
A refresh is a second or so, against minutes for `dbt docs generate`, which
walks every relation in the project to reach the same handful.

## The contract

Request on stdin:

```json
{ "version": 1,
  "projects": [
    { "name": "shop", "root": "/repo/shop", "profile": "shop",
      "target": "prod", "profiles_dir": "/home/u/.dbt" }
  ],
  "sources": [
    { "unique_id": "source.shop.crm.customers", "database": "bq-project",
      "schema": "raw", "identifier": "customers",
      "source_name": "crm", "name": "customers", "project": "shop" }
  ]}
```

Response on stdout:

```json
{ "version": 1,
  "sources": [
    { "unique_id": "source.shop.crm.customers",
      "description": "Customer master from the CRM.",
      "labels": { "owner": "crm-team", "pii": "high" },
      "columns": [
        { "name": "customer_id", "data_type": "INT64", "index": 1,
          "description": "Surrogate key assigned by the CRM.",
          "labels": { "classification": "restricted" },
          "extra": { "policy_tags": ["taxonomies/1/policyTags/2"] } }
      ]}
  ],
  "warnings": ["no access to project bq-other"] }
```

Unknown fields are ignored in both directions, so the contract can grow without
a version bump.

**`labels` is the neutral bucket.** Key-value pairs attached to an object are
the one piece of metadata every warehouse has — BigQuery labels, Snowflake and
Unity Catalog tags, Glue table parameters — so a provider reports them raw and
dbt-ditto routes them into `meta` or `tags` per the project's own configuration.
Do not decide that here; a provider that picked would be a provider each project
had to override.

**`extra` is for everything else**, under the key it is written with in dbt
YAML. A BigQuery policy tag goes here because nothing else has one. It is
carried only when the project lists the key in `inheritance.extra_keys`, so a
provider reporting it into a project that never asked writes nothing.

**stdout is the answer.** Progress and errors go to stderr; a provider that
prints anything else to stdout produces a response that will not parse. Exiting
non-zero with a reason on stderr is how a provider reports a fatal problem — the
last line is what dbt-ditto shows.

## Writing one

Start from `echo.py`; it is forty lines and uses the same shared module the real
providers do. `python packaging/providers/test_providers.py` runs the offline
tests — the contract, profiles.yml resolution, and the type rendering that has
to agree with what each dbt adapter writes into `catalog.json`. No account is
needed for any of it.

Rendering types to match the adapter is worth the trouble: a project that runs
both dbt-ditto and `dbt docs generate` otherwise gets a diff on every
`data_type:`. `bigquery.py` reproduces ``STRUCT<`name` TYPE>`` and
`ARRAY<...>`; `snowflake.py` reproduces `VARCHAR(16777216)` and `NUMBER(38,0)`.
