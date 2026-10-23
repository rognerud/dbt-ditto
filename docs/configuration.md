# Configuration reference

Every key, as YAML in `dbt_ditto.yml` or under `[tool.dbt-ditto]` in
`pyproject.toml` ([where the file lives](usage.md#the-config-file)).

Defaults reproduce dbt-osmosis, except `inheritance.progenitor`. Every row below
is enforced by a case in
[`settings_test.go`](../internal/runner/settings_test.go);
[`config.go`](../internal/config/config.go) is the source of truth.

`dbt-ditto help config` prints the same list — every key, its default and one
sentence on what it does — without leaving the terminal.
[`settings.go`](../internal/config/settings.go) holds those sentences, and
`TestEverySettingIsExplained` compares them against the config structs, so the
two cannot drift. `dbt-ditto init` writes a starter file
([usage.md](usage.md#the-config-file)).

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

## What travels between tables

A table has meta and tags of its own, and dbt-osmosis touches neither: it
inherits column knowledge, and a node's `meta` is as likely to say who owns the
model as to describe the data. Both are therefore off.

| Key | Default | Change it to… |
| --- | --- | --- |
| `inheritance.node.meta` | `false` | `true` carries the table's own `meta` down the DAG |
| `inheritance.node.tags` | `false` | `true` carries the table's own `tags` down the DAG |
| `inheritance.node.from_parents` | `true` | `false` stops reading ancestor models, seeds and snapshots |
| `inheritance.node.from_folders` | `true` | `false` stops reading `+meta:` and `+tags:` from `dbt_project.yml` |
| `inheritance.node.from_sources` | `true` | `false` stops a raw table's classification travelling past the source entry |

The three origins are weakest-last: **what the table says about itself**, then
**the nearest ancestor** that says anything about a key, then **`dbt_project.yml`**,
which is a default for everything below it. Meta merges key by key, so a table
naming its own `owner` still inherits the `tier` it says nothing about; tags are
an order-preserving union with the table's own first. `inheritance.force` does
not apply: it is about a description somebody wrote, and replacing an owner with
an upstream owner would destroy the one thing node meta is used for.

A source is an ancestor like any other, so `from_sources` only decides whether
that ancestor counts — with provider labels on a raw table, this is how a
classification reaches the models built from it.

<details>
<summary>inheritance.node.meta carries the table's meta down, merged key by key (+2 more)</summary>

```gherkin
Scenario: inheritance.node.meta carries the table's meta down, merged key by key
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And the node meta of "raw_orders" is:
    """
    tier: gold
    owner: platform
    """
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "models/_stg_orders.yml":
    """
    version: 2
    models:
      - name: stg_orders
        meta:
          owner: analytics
        columns:
          - name: order_id
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      node:
        meta: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains:
    """
    owner: analytics
    tier: gold
    """

Scenario: the table's meta stays put by default
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And the node meta of "raw_orders" is:
    """
    tier: gold
    """
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" does not contain "tier: gold"

Scenario: inheritance.node.tags unions the table's tags, its own first
  Given a dbt project
  And a seed "raw_orders" documenting "order_id" as "Surrogate key for an order."
  And the node tags of "raw_orders" are "nightly,finance"
  And an undocumented model "stg_orders" reading from "raw_orders" with column "order_id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    inheritance:
      node:
        tags: true
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg_orders.yml" contains, in order:
    """
    - nightly
    - finance
    """
```

</details>

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

## Tags, and the labels that carry a vocabulary

`inheritance.tags` unions an ancestor's tags into the column's own, which is the
right answer for a tag that is a name — `pii`, `nightly`. It is the wrong answer
for a tag that is a *value*: a column fed by a public table and a confidential
one must not end up both `data-sensitivity:public` and
`data-sensitivity:confidential`.

A rule names one label key and its vocabulary, weakest first, and collapses that
key to a single value per column. The value comes from the whole set of
ancestors, not the first one of each generation that a description takes, since
picking the strongest is only meaningful across all of them. Rules read the key
off a `key:value` tag and off the nested label map
([`sources.labels`](source-providers.md#settings)) alike, so a project already
routing warehouse labels into meta needs no second spelling.

Three things outrank an inherited value, in this order:

1. **a folder**, via `+tags:` in `dbt_project.yml` — the coarsest statement
   anybody made on purpose;
2. **the column**, via a tag or label somebody wrote on it;
3. nothing else. Everything upstream is a *default*.

The difference is recorded, not guessed: a value this tool resolved is listed in
`meta: {dbt_ditto_label_defaults: [...]}`, and a value present on the column but
absent from that list is canonical. So a default can be raised, lowered, or
removed when upstream changes, and an explicit decision is never touched again.

### Altering a value halfway down

A column is often declassified on its way down the graph: an email is hashed, a
salary is bucketed into a band, a row count replaces the rows. The model that
does it is where the sensitivity changes, and writing the weaker value on that
column is how you say so.

By default (`inherit: cut`) that is also what everything *below* it inherits.
Each branch is followed upwards only as far as the nearest ancestor that
declares the key, because what fed that ancestor was the input to its decision,
not a second opinion about it. The cut is per branch: a second, unaltered branch
still carries its own source's classification the whole way down, and the ladder
still decides between the branches that survive.

Three consequences worth knowing:

- An alteration must be a *declaration* to cut. A value a previous run wrote —
  listed in `dbt_ditto_label_defaults` on that column — cuts nothing, so a stale
  default cannot pin the graph below it.
- A folder declaration cuts too, which is how `models/masked/` can declassify
  everything in it without a tag per column.
- `inherit: all` is the other reading: every ancestor is read, so a
  classification upstream is a floor nothing downstream can write its way out
  of. Choose it when the rule is a control rather than a description — and note
  that the altered column itself still keeps its explicit value; what changes is
  what the models below it see.

| Key | Default | Change it to… |
| --- | --- | --- |
| `tags.folders` | `off` | `rules` reads `+tags:` from `dbt_project.yml` and lets a folder decide its ruled keys; `all` also writes every other folder tag onto every column below it |
| `tags.rules[].key` | — | the label key the rule decides, e.g. `data-sensitivity` |
| `tags.rules[].values` | — | the vocabulary, **weakest first**. A value outside it is reported and left where it was written |
| `tags.rules[].resolve` | `strongest` | `weakest`, or `first` for the nearest branch, as a description is chosen |
| `tags.rules[].inherit` | `cut` | `all` reads every ancestor, so a value written halfway down cannot weaken what the models below it inherit |
| `tags.rules[].default` | none | the value to write when no ancestor carries the key at all. Still a default, so a later run may replace it |
| `tags.rules[].write` | `tags` | `meta` writes under the label meta key instead; `both` writes both |

A rule that cannot be acted on — no `values`, a `default` outside them, two
rules for one key — stops the run rather than being resolved into a guess: what
it would otherwise write is a claim about somebody's data.

Folder tags accumulate the way dbt's own do, outermost first, rather than the
nearest folder winning; `models:`, `seeds:`, `snapshots:` and `sources:` are
each read for the resource type they configure.

<details>
<summary>A rule takes the strongest value its ancestors carry (+5 more)</summary>

```gherkin
Scenario: a rule takes the strongest value its ancestors carry
  Given a dbt project
  And a seed "raw_public" with columns:
    | column | description          | tags                    |
    | id     | Identifier of a row. | data-sensitivity:public |
  And a seed "raw_private" with columns:
    | column | description | tags                          |
    | id     |             | data-sensitivity:confidential |
  And a model "stg" reading from "raw_public" and "raw_private" with columns:
    | column | description |
    | id     |             |
  And the file "models/_stg.yml":
    """
    version: 2
    models:
      - name: stg
        columns:
          - name: id
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    tags:
      rules:
        - key: data-sensitivity
          values: [public, internal, confidential, sensitive]
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg.yml" contains:
    """
    - data-sensitivity:confidential
    """
  And the file "models/_stg.yml" does not contain "data-sensitivity:public"
  And the file "models/_stg.yml" contains:
    """
    dbt_ditto_label_defaults:
    """

Scenario: a value written on the column outranks a stricter ancestor
  Given a dbt project
  And a seed "raw" with columns:
    | column | description          | tags                       |
    | id     | Identifier of a row. | data-sensitivity:sensitive |
  And a model "stg" reading from "raw" with columns:
    | column | description | tags                      |
    | id     |             | data-sensitivity:internal |
  And the file "models/_stg.yml":
    """
    version: 2
    models:
      - name: stg
        columns:
          - name: id
            tags:
              - data-sensitivity:internal
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    tags:
      rules:
        - key: data-sensitivity
          values: [public, internal, confidential, sensitive]
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg.yml" contains:
    """
    - data-sensitivity:internal
    """
  And the file "models/_stg.yml" does not contain "data-sensitivity:sensitive"
  And the file "models/_stg.yml" does not contain "dbt_ditto_label_defaults"

Scenario: tags.folders lets dbt_project.yml decide, over anything upstream
  Given a dbt project
  And a seed "raw" with columns:
    | column | description          | tags                       |
    | id     | Identifier of a row. | data-sensitivity:sensitive |
  And a model "stg" reading from "raw" with columns:
    | column | description |
    | id     |             |
  And the file "models/_stg.yml":
    """
    version: 2
    models:
      - name: stg
        columns:
          - name: id
    """
  And the file "dbt_project.yml":
    """
    name: demo
    version: "1.0.0"
    config-version: 2
    profile: demo
    model-paths: ["models"]
    seed-paths: ["seeds"]
    models:
      demo:
        +tags: ["data-sensitivity:confidential"]
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    tags:
      folders: rules
      rules:
        - key: data-sensitivity
          values: [public, internal, confidential, sensitive]
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg.yml" contains:
    """
    - data-sensitivity:confidential
    """
  And the file "models/_stg.yml" does not contain "data-sensitivity:sensitive"

Scenario: tags.rules[].default applies when nothing upstream says anything, and write routes it
  Given a dbt project
  And a seed "raw" documenting "id" as "Identifier of a row."
  And an undocumented model "stg" reading from "raw" with column "id"
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    tags:
      rules:
        - key: data-sensitivity
          values: [public, internal, confidential, sensitive]
          default: internal
          write: meta
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_stg.yml" contains:
    """
    data-sensitivity: internal
    """
  And the file "models/_stg.yml" does not contain "- data-sensitivity:internal"

Scenario: a value altered halfway down is what the models below it inherit
  Given a dbt project
  And a seed "raw" with columns:
    | column | description        | tags                       |
    | email  | The email address. | data-sensitivity:sensitive |
  And a model "stg_hashed" reading from "raw" with columns:
    | column | description | tags                      |
    | email  |             | data-sensitivity:internal |
  And a model "dim" reading from "stg_hashed" with columns:
    | column | description |
    | email  |             |
  And the file "models/_stg_hashed.yml":
    """
    version: 2
    models:
      - name: stg_hashed
        columns:
          - name: email
            tags:
              - data-sensitivity:internal
    """
  And the file "models/_dim.yml":
    """
    version: 2
    models:
      - name: dim
        columns:
          - name: email
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    tags:
      rules:
        - key: data-sensitivity
          values: [public, internal, confidential, sensitive]
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_dim.yml" contains:
    """
    - data-sensitivity:internal
    """
  And the file "models/_dim.yml" does not contain "data-sensitivity:sensitive"

Scenario: inherit all reads past the alteration, so a classification is a floor
  Given a dbt project
  And a seed "raw" with columns:
    | column | description        | tags                       |
    | email  | The email address. | data-sensitivity:sensitive |
  And a model "stg_hashed" reading from "raw" with columns:
    | column | description | tags                      |
    | email  |             | data-sensitivity:internal |
  And a model "dim" reading from "stg_hashed" with columns:
    | column | description |
    | email  |             |
  And the file "models/_stg_hashed.yml":
    """
    version: 2
    models:
      - name: stg_hashed
        columns:
          - name: email
            tags:
              - data-sensitivity:internal
    """
  And the file "models/_dim.yml":
    """
    version: 2
    models:
      - name: dim
        columns:
          - name: email
    """
  And the file "dbt_ditto.yml":
    """
    projects:
      - path: .
    tags:
      rules:
        - key: data-sensitivity
          values: [public, internal, confidential, sensitive]
          inherit: all
    """
  When I run `dbt-ditto inherit`
  Then the exit code is 0
  And the file "models/_dim.yml" contains:
    """
    - data-sensitivity:sensitive
    """
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
