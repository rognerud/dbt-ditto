# Source providers

Programs that fetch documentation for **external sources** — the raw tables no
dbt project builds — from the warehouse's own metadata. Supporting a new
warehouse means writing one of these and changing nothing in dbt-ditto. How the
output is merged and configured:
[docs/source-providers.md](../../docs/source-providers.md).

| | |
|---|---|
| `bigquery.py` | table and column descriptions, labels, policy tags |
| `snowflake.py` | table and column comments, tags |
| `echo.py` | answers from a file. The shortest example, and what the Go tests run |
| `dbt_ditto_provider.py` | shared: the contract, and profiles.yml resolution |

## Credentials come from dbt

The request carries each project's root, `profile:` and profiles directory. dbt's
own loader resolves the connection when `dbt-core` is importable, being the only
thing guaranteed to agree with dbt about Jinja and `env_var` defaults; failing
that, `profiles.yml` is read directly, so a CI job needs the warehouse SDK but
not dbt. `$DBT_PROFILES_DIR` and a `profiles.yml` beside `dbt_project.yml` are
both honoured.

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
  "warnings": ["no access to bq-other"] }
```

Unknown fields are ignored in both directions, so the contract can grow without a
version bump; `version` moves only for a breaking change.

- **`labels` is the neutral bucket.** Report key-value pairs raw; dbt-ditto
  routes them into `meta` or `tags` per the project's config. A provider that
  decided for itself would be one every project had to override.
- **`extra` is for everything else**, keyed by the name it takes in dbt YAML (a
  BigQuery policy tag, say), and carried only when the project lists that key in
  `inheritance.extra_keys`.
- **stdout is the answer.** Progress and errors go to stderr; anything else on
  stdout will not parse. Exit non-zero with a reason on stderr for a fatal
  problem — the last line is what dbt-ditto shows.

## Writing one

Start from `echo.py`: forty lines, on the same shared module the real providers
use. `python packaging/providers/test_providers.py` runs the offline tests.

Render `data_type` as the dbt adapter would, or a project running both dbt-ditto
and `dbt docs generate` gets a diff on every column. `bigquery.py` reproduces
``STRUCT<`name` TYPE>`` and `ARRAY<...>`; `snowflake.py` reproduces
`VARCHAR(16777216)` and `NUMBER(38,0)`.
