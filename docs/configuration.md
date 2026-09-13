# Configuration reference

Every key, as YAML in `dbt_ditto.yml` or under `[tool.dbt-ditto]` in
`pyproject.toml` ([where the file lives](usage.md#the-config-file)).

Defaults reproduce dbt-osmosis, except `inheritance.progenitor`. Every row below
is enforced by a case in
[`settings_test.go`](../internal/runner/settings_test.go);
[`config.go`](../internal/config/config.go) is the source of truth.

## Which projects take part

| Key | Default | Change it to… |
| --- | --- | --- |
| `projects[].path` | — | the directory holding `dbt_project.yml`, relative to the config file |
| `projects[].target` | `<path>/target` | read the artifacts from elsewhere, e.g. where CI put them |
| `projects[].manifest` | — | an upstream that is only a `manifest.json(.gz)`. Read-only |
| `projects[].upstream` | `false` | take documentation *from* this project, never write it |
| `loom` | `true` | `false` ignores `dbt_loom.config.yml`; on, its `type: file` manifests load as upstreams |

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

Source-provider settings (`sources.*`):
[source-providers.md](source-providers.md#settings).

## Not settings

- **Whether meta and tags nest under `config:`.** The manifest's dbt version
  decides: 1.9.6 and later read them there. A switch could only disagree with dbt
  and silently drop every column's meta.
- **`dbt_ditto_definitive`**, a meta marker written in a schema file
  ([usage.md](usage.md#settling-it-dbt_ditto_definitive)).
- **Command-line flags** (`dbt-ditto --help`).
