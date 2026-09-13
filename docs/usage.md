# Using dbt-ditto

Install it, point it at a dbt project, and it fills in your schema YAML. Settings:
[configuration.md](configuration.md).

Every ```gherkin block below is executed on every `go test`, against the real
binary, by `TestDocumentationIsTrue`
([`internal/features/docs_test.go`](../internal/features/docs_test.go)). The
blocks are the test suite — there is no second copy — so a sentence here whose
block stopped passing fails the build.

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

```gherkin
Scenario: a project directory is the whole invocation
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And it prints "wrote project/models/_stg_orders.yml"
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """
```

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

```gherkin
Scenario: --check is a gate, not an edit
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  When I run `dbt-ditto inherit --check .`
  Then the exit code is 1
  And it prints "would write project/models/_stg_orders.yml"
  And it warns "documentation is out of date"
  And no file on disk changed

Scenario: --check passes once the run has been made
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  When I run `dbt-ditto inherit .`
  And I run `dbt-ditto inherit --check .`
  Then the exit code is 0
  And it prints "0 files changed"

Scenario: --dry-run -v lists every change and writes none
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  When I run `dbt-ditto inherit --dry-run -v .`
  Then the exit code is 0
  And it prints:
    """
    model.demo.stg_orders
      models/_stg_orders.yml: order_id.description inherited from seed.demo.raw_orders
    """
  And no file on disk changed

Scenario: --select limits the run to the nodes named
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And an undocumented model "dim_orders" reading from "stg_orders" with column "order_id"
  When I run `dbt-ditto inherit --select 'stg_*' .`
  Then the exit code is 0
  And it prints "wrote project/models/_stg_orders.yml"
  And it does not print "_dim_orders.yml"
  And the file "models/_dim_orders.yml" does not contain "description:"
```

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

```gherkin
Scenario: the config file is found by searching upwards, and its paths are its own
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "../dbt_ditto.yml":
    """
    projects:
      - path: project
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """

Scenario: a pyproject.toml without the table does not shadow the real config
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "pyproject.toml":
    """
    [project]
    name = "not-a-ditto-config"
    """
  And the file "../dbt_ditto.yml":
    """
    projects:
      - path: project
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """

Scenario: the same configuration in pyproject.toml
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "pyproject.toml":
    """
    [tool.dbt-ditto]
    [[tool.dbt-ditto.projects]]
    path = "."
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """

Scenario: -c names a config file directly
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "../ci/ditto.yml":
    """
    projects:
      - path: ../project
    """
  When I run `dbt-ditto inherit -c ../ci/ditto.yml`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """
```

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

```gherkin
Scenario: a model inherits from a model in another repository
  Given a dbt project
  And an upstream project "vendor" documenting "vendor_orders.order_id" as "The order, as the vendor numbers it."
  And an undocumented model "stg_orders" reading from "vendor_orders" with column "order_id"
  And the file "../dbt_ditto.yml":
    """
    projects:
      - path: project
      - path: vendor
        upstream: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: The order, as the vendor numbers it.
    osmosis_progenitor: model.vendor.vendor_orders
    """
  And the upstream project "vendor" was not written to

Scenario: a bare artifact is enough to inherit from
  Given a dbt project
  And an upstream project "vendor" documenting "vendor_orders.order_id" as "The order, as the vendor numbers it."
  And an undocumented model "stg_orders" reading from "vendor_orders" with column "order_id"
  And the file "../dbt_ditto.yml":
    """
    projects:
      - path: project
      - manifest: vendor/target/manifest.json
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: The order, as the vendor numbers it.
    """
  And the upstream project "vendor" was not written to

Scenario: dbt-loom's own list of file manifests is read, and can be ignored
  Given a dbt project
  And an upstream project "vendor" documenting "vendor_orders.order_id" as "The order, as the vendor numbers it."
  And an undocumented model "stg_orders" reading from "vendor_orders" with column "order_id"
  And the file "dbt_loom.config.yml":
    """
    manifests:
      - name: vendor
        type: file
        config:
          path: ../vendor/target/manifest.json
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: The order, as the vendor numbers it.
    """

Scenario: a remote dbt-loom backend is reported rather than fetched
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_loom.config.yml":
    """
    manifests:
      - name: finance
        type: s3
        config:
          path: s3://artifacts/finance/manifest.json
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And it warns:
    """
    note: dbt_loom.config.yml: skipping dbt-loom manifest "finance" (type "s3"): only `type: file` is read; download the artifact and add it as `manifest:` in dbt_ditto.yml
    """
```

## Documenting source tables

Inheritance runs downhill, so a source can never inherit. Three routes reach it,
and they compose:

**1. Warehouse comments.** A `COMMENT ON COLUMN` reaches `catalog.json` and
becomes the description. On by default for columns being added;
`columns.comments: always` also fills columns already listed but undocumented,
`never` ignores them.

```gherkin
Scenario: a column added from the warehouse arrives with its COMMENT
  Given a dbt project
  And a source "crm.raw_orders" with columns:
    | column   | description |
    | order_id |             |
  And the warehouse reports for "crm.raw_orders":
    | column     | data_type | comment                     |
    | order_id   | integer   | Surrogate key for an order. |
    | order_date | date      | The day the order was made. |
  And the file "models/_sources.yml":
    """
    version: 2
    sources:
      - name: crm
        tables:
          - name: raw_orders
            columns:
              - name: order_id
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "models/_sources.yml" contains:
    """
    - name: order_date
      description: The day the order was made.
    """
  And the file "models/_sources.yml" does not contain "Surrogate key for an order."

Scenario: columns.comments always reaches a column already listed
  Given a dbt project
  And a source "crm.raw_orders" with columns:
    | column   | description |
    | order_id |             |
  And the warehouse reports for "crm.raw_orders":
    | column   | data_type | comment                     |
    | order_id | integer   | Surrogate key for an order. |
  And the file "models/_sources.yml":
    """
    version: 2
    sources:
      - name: crm
        tables:
          - name: raw_orders
            columns:
              - name: order_id
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    columns:
      comments: always
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_sources.yml" contains:
    """
    description: Surrogate key for an order.
    """
```

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

```gherkin
Scenario: a source takes the wording of the model below it
  Given a dbt project
  And a source "crm.raw_orders" with columns:
    | column   | description |
    | order_id |             |
  And a model "stg_orders" reading from source "crm.raw_orders" with columns:
    | column   | description                 |
    | order_id | Surrogate key for an order. |
  And the file "models/_sources.yml":
    """
    version: 2
    sources:
      - name: crm
        tables:
          - name: raw_orders
            columns:
              - name: order_id
    """
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_id
            description: Surrogate key for an order.
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      backfill:
        enabled: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_sources.yml" contains:
    """
    description: Surrogate key for an order.
    """

Scenario: a model is not backfilled unless sources_only is off
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column     | description |
    | order_note |             |
  And a model "dim_orders" reading from "stg_orders" with columns:
    | column     | description                     |
    | order_note | Whatever the clerk typed in. |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_note
    """
  And the file "models/_dim_orders.yml":
    """
    version: 2
    models:
      - name: dim_orders
        columns:
          - name: order_note
            description: Whatever the clerk typed in.
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      backfill:
        enabled: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" does not contain "Whatever the clerk typed in."
```

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

```gherkin
Scenario: a directive follows a renamed column
  Given a dbt project
  And a seed "raw_customers" documenting "customer_id" as "The account a thing belongs to."
  And a model "stg_customers" reading from "raw_customers" with columns:
    | column      | description                            |
    | customer_id | The account a thing belongs to.        |
  And a model "dim_orders" reading from "stg_customers" with columns:
    | column  | description                             |
    | cust_id | Inherited: stg_customers.customer_id    |
  And the file "models/_stg_customers.yml":
    """
    version: 2
    models:
      - name: stg_customers
        columns:
          - name: customer_id
    """
  And the file "models/_dim_orders.yml":
    """
    version: 2
    models:
      - name: dim_orders
        columns:
          - name: cust_id
            description: "Inherited: stg_customers.customer_id"
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "models/_dim_orders.yml" contains:
    """
    description: The account a thing belongs to.
    """

Scenario: a directive naming nothing is reported and left where it is
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column  | description                        |
    | cust_id | Inherited: no_such_model.customer_id |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: cust_id
            description: "Inherited: no_such_model.customer_id"
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And it warns "model.demo.stg_orders.cust_id"
  And the file "models/_stg_orders.yml" contains:
    """
    description: "Inherited: no_such_model.customer_id"
    """
```

## When two parents disagree

Within a generation the first ancestor by `unique_id` claims the column, so a
disagreement is settled alphabetically. That is arbitrary, so it is reported on
stderr:

```gherkin
Scenario: a disagreement is settled by unique_id, and said out loud
  Given a dbt project
  And a seed "orders_crm" documenting "order_id" as "Surrogate key for an order."
  And a seed "orders_erp" documenting "order_id" as "The ERP order number."
  And a model "stg_orders" reading from "orders_crm" and "orders_erp" with columns:
    | column   | description |
    | order_id |             |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_id
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And it warns:
    """
    warning: model.demo.stg_orders.order_id documented differently by seed.demo.orders_erp; took seed.demo.orders_crm
    """
  And it prints "1 warnings"
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """
```

Warnings go to stderr, never fail the run, and never change what is written. A
nearer generation overriding a further one is normal and is not reported. Silence
them with `inheritance.warn_ambiguous: false`, or record them in the file with
`inheritance.ambiguity_meta`:

```gherkin
Scenario: the disagreement can be written down instead of only reported
  Given a dbt project
  And a seed "orders_crm" documenting "order_id" as "Surrogate key for an order."
  And a seed "orders_erp" documenting "order_id" as "The ERP order number."
  And a model "stg_orders" reading from "orders_crm" and "orders_erp" with columns:
    | column   | description |
    | order_id |             |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_id
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      ambiguity_meta: true
      warn_ambiguous: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And it warns about nothing
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    meta:
      osmosis_progenitor: seed.demo.orders_crm
      dbt_ditto_ambiguous:
        - seed.demo.orders_erp
    """
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
decision has no direction.

```gherkin
Scenario: a decision outranks the DAG, upwards as well as downwards
  Given a dbt project
  And a seed "raw_customers" with columns:
    | column      | description                  |
    | customer_id | Whatever the CRM calls a row. |
  And a model "stg_customers" reading from "raw_customers" with columns:
    | column      | description                              | meta                           |
    | customer_id | The account the order was placed under.  | {dbt_ditto_definitive: true}   |
  And the file "seeds/_seeds.yml":
    """
    version: 2
    seeds:
      - name: raw_customers
        columns:
          - name: customer_id
            description: Whatever the CRM calls a row.
    """
  And the file "models/_stg_customers.yml":
    """
    version: 2
    models:
      - name: stg_customers
        columns:
          - name: customer_id
            description: The account the order was placed under.
            meta:
              dbt_ditto_definitive: true
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "seeds/_seeds.yml" contains:
    """
    description: The account the order was placed under.
    """
  And the file "seeds/_seeds.yml" does not contain "dbt_ditto_definitive"

Scenario: two decisions that disagree stop the run
  Given a dbt project
  And a seed "raw_customers" with columns:
    | column      | description               | meta                         |
    | customer_id | The account it belongs to. | {dbt_ditto_definitive: true} |
  And a model "stg_customers" reading from "raw_customers" with columns:
    | column      | description                             | meta                         |
    | customer_id | The account the order was placed under. | {dbt_ditto_definitive: true} |
  And the file "models/_stg_customers.yml":
    """
    version: 2
    models:
      - name: stg_customers
        columns:
          - name: customer_id
            description: The account the order was placed under.
            meta:
              dbt_ditto_definitive: true
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 1
  And it warns:
    """
    conflicting dbt_ditto_definitive declarations for column "customer_id":
    seed.demo.raw_customers.customer_id: "The account it belongs to."
    model.demo.stg_customers.customer_id: "The account the order was placed under."
    resolve it by leaving one declaration, or by making them agree word for word
    """
```

The rules:

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

```gherkin
Scenario: an aggregate keeps the documentation of what it aggregates
  Given a dbt project
  And a seed "raw_orders" documenting "amount_cents" as "Amount, in cents."
  And an undocumented model "fct_totals" reading from "raw_orders" with column "total_amount_cents"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      derived:
        enabled: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_fct_totals.yml" contains:
    """
    description: Amount, in cents.
    """

Scenario: a column packed into a struct is still the same column
  Given a dbt project
  And a seed "raw_customers" documenting "first_name" as "The customer's first name."
  And an undocumented model "dim_profile" reading from "raw_customers" with column "profile.first_name"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      derived:
        enabled: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_dim_profile.yml" contains:
    """
    description: The customer's first name.
    """

Scenario: only whole words are stripped, so an unrelated name is left alone
  Given a dbt project
  And a seed "raw_parties" documenting "erparty_id" as "Nothing to do with a counterparty."
  And an undocumented model "stg_parties" reading from "raw_parties" with column "counterparty_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      derived:
        enabled: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_parties.yml" does not contain "Nothing to do with a counterparty."
```

Struct *expansion* is separate and on by default: a `STRUCT` in `catalog.json` is
parsed so `profile.first_name` is documented as its own column, which is what
nested-data adapters report.

```gherkin
Scenario: a struct in the catalog becomes one column per field
  Given a dbt project
  And a model "dim_customers" with columns:
    | column | description |
    | id     |             |
  And the warehouse reports for "dim_customers":
    | column  | data_type                     | comment |
    | id      | integer                       |         |
    | profile | STRUCT<first_name VARCHAR>    |         |
  And the file "models/_dim_customers.yml":
    """
    version: 2
    models:
      - name: dim_customers
        columns:
          - name: id
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "models/_dim_customers.yml" contains:
    """
    - name: profile.first_name
    """
```

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

```gherkin
Scenario: the nearer ancestor wins, and a local description wins over both
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "What the seed says."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column   | description              |
    | order_id | What the staging model says. |
  And a model "int_orders" reading from "stg_orders" with columns:
    | column   | description |
    | order_id |             |
  And a model "fct_orders" reading from "int_orders" with columns:
    | column   | description                |
    | order_id | What this mart says itself. |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_id
            description: What the staging model says.
    """
  And the file "models/_int_orders.yml":
    """
    version: 2
    models:
      - name: int_orders
        columns:
          - name: order_id
    """
  And the file "models/_fct_orders.yml":
    """
    version: 2
    models:
      - name: fct_orders
        columns:
          - name: order_id
            description: What this mart says itself.
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "models/_int_orders.yml" contains:
    """
    description: What the staging model says.
    osmosis_progenitor: model.demo.stg_orders
    """
  And the file "models/_int_orders.yml" does not contain "What the seed says."
  And the file "models/_fct_orders.yml" contains:
    """
    description: What this mart says itself.
    """
  And the file "models/_fct_orders.yml" does not contain "osmosis_progenitor"

Scenario: a placeholder upstream is not an answer
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Not documented"
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" does not contain "Not documented"
```

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

```gherkin
Scenario: provenance is written by default and can be switched off
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  When I run `dbt-ditto inherit .`
  Then the file "models/_stg_orders.yml" contains:
    """
    osmosis_progenitor: seed.demo.raw_orders
    """

Scenario: inheritance.progenitor false leaves what dbt-osmosis would leave
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      progenitor: false
    """
  When I run `dbt-ditto inherit`
  Then the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """
  And the file "models/_stg_orders.yml" does not contain "osmosis_progenitor"
  And the file "models/_stg_orders.yml" does not contain "meta:"

Scenario: a comment in a column list survives the rewrite
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column     | description |
    | order_id   |             |
    | order_note |             |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_id
          # the clerk's own words, kept verbatim
          - name: order_note
    """
  When I run `dbt-ditto inherit .`
  Then the file "models/_stg_orders.yml" contains:
    """
    # the clerk's own words, kept verbatim
    - name: order_note
    """
```
