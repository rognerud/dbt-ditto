# Configuration reference

Every key, as YAML in `dbt_ditto.yml` or under `[tool.dbt-ditto]` in
`pyproject.toml` ([where the file lives](usage.md#the-config-file)).

Defaults reproduce dbt-osmosis, except `inheritance.progenitor`. Every row below
is enforced by a case in
[`settings_test.go`](../internal/runner/settings_test.go);
[`config.go`](../internal/config/config.go) is the source of truth.

The ```gherkin blocks here run on every `go test`, against the real binary, so a
key whose documented effect changed fails the build
([how](usage.md#using-dbt-ditto)). Each block writes the config file it is about
and then reads what the run produced, and each is folded away until you open it.

<details>
<summary>The config file is what a run without a project directory uses</summary>

```gherkin
Scenario: the config file is what a run without a project directory uses
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """
```

</details>

## Which projects take part

| Key | Default | Change it to… |
| --- | --- | --- |
| `projects[].path` | — | the directory holding `dbt_project.yml`, relative to the config file |
| `projects[].target` | `<path>/target` | read the artifacts from elsewhere, e.g. where CI put them |
| `projects[].manifest` | — | an upstream that is only a `manifest.json(.gz)`. Read-only |
| `projects[].upstream` | `false` | take documentation *from* this project, never write it |
| `loom` | `true` | `false` ignores `dbt_loom.config.yml`; on, its `type: file` manifests load as upstreams |

<details>
<summary>Projects[].target reads the artifacts from where CI left them</summary>

```gherkin
Scenario: projects[].target reads the artifacts from where CI left them
  Given a dbt project
  And the artifacts are in "ci-artifacts"
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
        target: ci-artifacts
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """
```

</details>

## What travels between columns

| Key | Default | Change it to… |
| --- | --- | --- |
| `inheritance.columns` | `true` | `false` stops inheriting; only column sync and organisation remain |
| `inheritance.node_description` | `false` | `true` gives a model the description of the model above it |
| `inheritance.meta` | `true` | `false` leaves `meta` alone. On, an ancestor's keys merge in and win collisions |
| `inheritance.tags` | `true` | `false` leaves `tags` alone. On, ancestors' tags join the column's own |
| `inheritance.case_insensitive` | `true` | `false` stops `ID` matching an upstream `id`, e.g. on Snowflake |
| `inheritance.force` | `false` | `true` replaces hand-written descriptions; off, a documented column is never touched |
| `inheritance.placeholders` | dbt-osmosis' list | the wordings that count as undocumented upstream |
| `inheritance.skip_meta_keys` | none | meta keys that must stay put. `owner` is about the table it is on |
| `inheritance.extra_keys` | none | keys dbt-ditto does not model (`policy_tags`, say), carried down like meta. Explicit, since every unknown key costs a map per column |
| `inheritance.backfill.enabled` | `false` | `true` documents a column from the models *below* it — the only way to reach a source |
| `inheritance.backfill.sources_only` | `true` | `false` backfills models too, letting a mart's wording flow back into what feeds it |
| `inheritance.derived.enabled` | `false` | `true` follows a column through a rename: `sum(amount_cents) as total_amount_cents` keeps its docs |
| `inheritance.derived.structs` | `true` | `false` stops matching `profile.first_name` to a flat `first_name`, and the reverse |
| `inheritance.derived.aggregates` | `true` | `false` stops matching `total_amount_cents` to `amount_cents` |
| `inheritance.derived.prefixes` | `sum`, `total`, `avg`, … | words stripped from the front before matching |
| `inheritance.derived.suffixes` | `sum`, `count`, `cnt`, … | words stripped from the end. Dropping `count` stops `order_id_count` inheriting `order_id` |

<details>
<summary>Inheritance.columns false leaves the descriptions alone (+5 more)</summary>

```gherkin
Scenario: inheritance.columns false leaves the descriptions alone
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      columns: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" does not contain "Surrogate key for an order."

Scenario: inheritance.force replaces a description somebody wrote
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column   | description                     |
    | order_id | What this table means by an id. |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_id
            description: What this table means by an id.
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      force: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """

Scenario: inheritance.placeholders decides what counts as undocumented
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "TODO"
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      placeholders:
        - TODO
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" does not contain "TODO"

Scenario: inheritance.skip_meta_keys keeps a key on the table it was written on
  Given a dbt project
  And a seed "raw_orders" with columns:
    | column   | description                 | meta                                      |
    | order_id | Surrogate key for an order. | {owner: crm-team, domain: orders}         |
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      skip_meta_keys:
        - owner
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    domain: orders
    """
  And the file "models/_stg_orders.yml" does not contain "owner"

Scenario: inheritance.case_insensitive false stops ID matching id
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "ORDER_ID"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      case_insensitive: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" does not contain "Surrogate key for an order."

Scenario: inheritance.node_description gives a model the description above it
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the node description of "raw_orders" is "One row per order, as the CRM exports it."
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      node_description: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: One row per order, as the CRM exports it.
    """
```

</details>

## Pointing at an answer, and recording where it came from

| Key | Default | Change it to… |
| --- | --- | --- |
| `inheritance.directives` | `true` | `false` treats `description: "Inherited: …"` as prose, not a pointer |
| `inheritance.directive_prefix` | `Inherited:` | the marker that makes a description a pointer |
| `inheritance.progenitor` | `true` | `false` for byte parity. On, an inherited description records its origin |
| `inheritance.progenitor_key` | `osmosis_progenitor` | rename that meta key |
| `inheritance.warn_ambiguous` | `true` | `false` stops reporting columns whose parents disagree |
| `inheritance.ambiguity_meta` | `false` | `true` records the disagreement in the column's meta |
| `inheritance.ambiguity_key` | `dbt_ditto_ambiguous` | rename that meta key |

<details>
<summary>Inheritance.directives false makes a pointer into prose (+2 more)</summary>

```gherkin
Scenario: inheritance.directives false makes a pointer into prose
  Given a dbt project
  And a seed "raw_customers" documenting "customer_id" as "The account a thing belongs to."
  And a model "stg_orders" reading from "raw_customers" with columns:
    | column  | description                           |
    | cust_id | Inherited: raw_customers.customer_id  |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: cust_id
            description: "Inherited: raw_customers.customer_id"
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      directives: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And it warns about nothing
  And the file "models/_stg_orders.yml" contains:
    """
    description: "Inherited: raw_customers.customer_id"
    """

Scenario: inheritance.directive_prefix chooses the marker
  Given a dbt project
  And a seed "raw_customers" documenting "customer_id" as "The account a thing belongs to."
  And a model "stg_orders" reading from "raw_customers" with columns:
    | column  | description                        |
    | cust_id | See: raw_customers.customer_id     |
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: cust_id
            description: "See: raw_customers.customer_id"
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      directive_prefix: "See:"
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: The account a thing belongs to.
    """

Scenario: inheritance.progenitor_key renames the meta key that records the origin
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      progenitor_key: documented_by
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    documented_by: seed.demo.raw_orders
    """
  And the file "models/_stg_orders.yml" does not contain "osmosis_progenitor"
```

</details>

## What the column list looks like afterwards

| Key | Default | Change it to… |
| --- | --- | --- |
| `columns.add_missing` | `true` | `false` stops adding what the warehouse has |
| `columns.remove_stale` | `true` | `false` keeps columns the warehouse no longer reports |
| `columns.data_types` | `true` | `false` stops writing `data_type:` from the catalog |
| `columns.case` | `preserve` | `lower`/`upper` re-cases newly added names; existing ones are never churned |
| `columns.order` | `catalog` | `alphabetical`, or `yaml` to keep the file's own order |
| `columns.expand_structs` | `true` | `false` documents `profile` but not `profile.first_name` |
| `columns.comments` | `new` | `always` fills any undocumented column from the warehouse `COMMENT`; `never` ignores them |
| `organize.enabled` | `true` | `false` leaves every model where it is documented today |
| `organize.delete_empty` | `true` | `false` keeps a schema file whose last model moved out |
| `output.comments` | `follow` | `osmosis` reproduces dbt-osmosis' loss of all but the first comment in a column list |

<details>
<summary>By default the warehouse decides the column list, its types and its order (+7 more)</summary>

```gherkin
Scenario: by default the warehouse decides the column list, its types and its order
  Given a dbt project
  And a model "dim_orders" with columns:
    | column       | description |
    | order_id     |             |
    | legacy_grade |             |
  And the warehouse reports for "dim_orders":
    | column     | data_type | comment |
    | order_id   | integer   |         |
    | order_date | date      |         |
  And the file "models/_dim_orders.yml":
    """
    version: 2
    models:
      - name: dim_orders
        columns:
          - name: order_id
          - name: legacy_grade
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And the file "models/_dim_orders.yml" contains, in order:
    """
    - name: order_id
      data_type: integer
    - name: order_date
      data_type: date
    """
  And the file "models/_dim_orders.yml" does not contain "legacy_grade"

Scenario: add_missing, remove_stale and data_types off leave the list as written
  Given a dbt project
  And a model "dim_orders" with columns:
    | column       | description |
    | order_id     |             |
    | legacy_grade |             |
  And the warehouse reports for "dim_orders":
    | column     | data_type | comment |
    | order_id   | integer   |         |
    | order_date | date      |         |
  And the file "models/_dim_orders.yml":
    """
    version: 2
    models:
      - name: dim_orders
        columns:
          - name: order_id
          - name: legacy_grade
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    columns:
      add_missing: false
      remove_stale: false
      data_types: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_dim_orders.yml" contains:
    """
    - name: legacy_grade
    """
  And the file "models/_dim_orders.yml" does not contain "order_date"
  And the file "models/_dim_orders.yml" does not contain "data_type"

Scenario: columns.case lower re-cases a name being added, and columns.order alphabetical sorts
  Given a dbt project
  And a model "dim_orders" with columns:
    | column   | description |
    | order_id |             |
  And the warehouse reports for "dim_orders":
    | column     | data_type | comment |
    | order_id   | integer   |         |
    | ORDER_DATE | date      |         |
    | AMOUNT     | integer   |         |
  And the file "models/_dim_orders.yml":
    """
    version: 2
    models:
      - name: dim_orders
        columns:
          - name: order_id
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    columns:
      case: lower
      order: alphabetical
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_dim_orders.yml" contains, in order:
    """
    - name: amount
    - name: order_date
    - name: order_id
    """

Scenario: columns.expand_structs false documents the struct and not its fields
  Given a dbt project
  And a model "dim_customers" with columns:
    | column | description |
    | id     |             |
  And the warehouse reports for "dim_customers":
    | column  | data_type                  | comment |
    | id      | integer                    |         |
    | profile | STRUCT<first_name VARCHAR> |         |
  And the file "models/_dim_customers.yml":
    """
    version: 2
    models:
      - name: dim_customers
        columns:
          - name: id
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    columns:
      expand_structs: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_dim_customers.yml" contains:
    """
    - name: profile
    """
  And the file "models/_dim_customers.yml" does not contain "profile.first_name"

Scenario: organising moves a model to the file its path rule names, and can be told not to
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column   | description |
    | order_id |             |
  And the file "models/schema.yml":
    """
    version: 2
    models:
      - name: stg_orders
        columns:
          - name: order_id
    """
  When I run `dbt-ditto inherit .`
  Then the exit code is 0
  And it prints "wrote project/models/_stg_orders.yml"
  And the file "models/schema.yml" is gone
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """

Scenario: organize.enabled false documents a model where it already lives
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column   | description |
    | order_id |             |
  And the file "models/schema.yml":
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
    organize:
      enabled: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/schema.yml" contains:
    """
    description: Surrogate key for an order.
    """

Scenario: organize.delete_empty false keeps the file a model moved out of
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And a model "stg_orders" reading from "raw_orders" with columns:
    | column   | description |
    | order_id |             |
  And the file "models/schema.yml":
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
    organize:
      delete_empty: false
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    description: Surrogate key for an order.
    """
  And the file "models/schema.yml" does not contain "stg_orders"

Scenario: output.comments osmosis keeps only the first comment in a column list
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
          # the key the warehouse joins on
          - name: order_id
          # the clerk's own words
          - name: order_note
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    output:
      comments: osmosis
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    # the key the warehouse joins on
    """
  And the file "models/_stg_orders.yml" does not contain "the clerk's own words"
```

</details>

Source-provider settings (`sources.*`):
[source-providers.md](source-providers.md#settings).

## Not settings

- **Whether meta and tags nest under `config:`.** The manifest's dbt version
  decides: 1.9.6 and later read them there. A switch could only disagree with dbt
  and silently drop every column's meta.
- **`dbt_ditto_definitive`**, a meta marker written in a schema file
  ([usage.md](usage.md#settling-it-dbt_ditto_definitive)).
- **Command-line flags** (`dbt-ditto --help`).
