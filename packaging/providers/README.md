# Source providers

Optional add-ons that document **external sources** — the raw tables no dbt
project builds — from the warehouse's own metadata. A source is a root of the
DAG, so inheritance cannot reach it; the warehouse usually knows more than the
`sources:` YAML does, and a provider fetches that.

| | |
|---|---|
| `bigquery.py` | table and column descriptions, labels, policy tags |
| `snowflake.py` | table and column comments, tags |
| `echo.py` | answers from a file — the shortest complete example, and what the Go test suite runs |
| `dbt_ditto_provider.py` | shared: the contract, and profiles.yml resolution |

Adding a warehouse means writing one of these, with no change to dbt-ditto. Why
they are separate programs and how their output is merged is in
[docs/source-providers.md](../../docs/source-providers.md); configuring them is
in [docs/usage.md](../../docs/usage.md#asking-the-warehouse-source-providers).

## Credentials come from dbt

A provider is told each project's root, its `profile:` and the profiles
directory, and resolves the connection from `profiles.yml` — the same entry,
target and method `dbt run` uses. dbt's own loader is used when `dbt-core` is
importable, being the only thing guaranteed to agree with dbt about Jinja and
`env_var` defaults; failing that, `profiles.yml` is read directly and `env_var`
resolved, so a CI job needs the warehouse SDK but not dbt.

`--target` picks the target, falling back to `$DBT_TARGET` and then to the
profile's default. `$DBT_PROFILES_DIR` is honoured, as is a `profiles.yml`
beside `dbt_project.yml`.

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

- **`labels` is the neutral bucket**: key-value pairs attached to an object are
  the one piece of metadata every warehouse has, so a provider reports them raw
  and dbt-ditto routes them into `meta` or `tags` per the project's config. A
  provider that decided would be one each project had to override.
- **`extra` is for everything else**, under the key it is written with in dbt
  YAML — a BigQuery policy tag, say. It is carried only when the project lists
  the key in `inheritance.extra_keys`.
- **stdout is the answer.** Progress and errors go to stderr; anything else on
  stdout produces a response that will not parse. Exiting non-zero with a reason
  on stderr reports a fatal problem, and the last line is what dbt-ditto shows.

## Writing one

Start from `echo.py`: forty lines, using the same shared module the real
providers do. `python packaging/providers/test_providers.py` runs the offline
tests — contract, profiles.yml resolution, and type rendering — with no account
needed.

Rendering types to match the adapter is worth the trouble, or a project running
both dbt-ditto and `dbt docs generate` gets a diff on every `data_type:`.
`bigquery.py` reproduces ``STRUCT<`name` TYPE>`` and `ARRAY<...>`;
`snowflake.py` reproduces `VARCHAR(16777216)` and `NUMBER(38,0)`.
