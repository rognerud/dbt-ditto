# BigQuery artifacts

dbt-ditto never connects to a warehouse: it reads `manifest.json` and
`catalog.json`. Testing a warehouse therefore means testing the *shape of the
artifacts that warehouse's adapter produces*, which needs no project, no
credentials and no billing.

These files are hand-built to match what `dbt-bigquery` actually emits. The
shape is not guessed; it is taken from the adapter itself:

- `dbt/include/bigquery/macros/catalog/catalog.sql` joins
  `INFORMATION_SCHEMA.COLUMNS` to `INFORMATION_SCHEMA.COLUMN_FIELD_PATHS` on
  `column_name`. A `RECORD` column therefore produces **one catalog column per
  field path**: the record itself (`profile`, typed `STRUCT<...>`) *and* every
  nested leaf (`profile.first_name`). `column_index` is a `row_number()` over
  the joined rows, explicitly so that nested fields get their own position, and
  `column_comment` comes from `paths.description`.
- `dbt/adapters/bigquery/column.py` builds the type text:
  `BigQueryColumn.data_type` renders a record as ``STRUCT<`field` TYPE, ...>``
  with backtick-quoted field names, and wraps a `REPEATED` mode in
  `ARRAY<...>`. Its `flatten()` returns leaves only, which is why dbt-osmosis
  running against BigQuery never writes a bare `profile` entry while a
  catalog-driven tool sees one.

What the fixture deliberately covers:

| | |
| --- | --- |
| Nested `RECORD` reported as parent **and** dotted leaves | `customer`, `customer.first_name`, … |
| Doubly nested records | `customer.address.city` |
| Repeated record | `items` typed `ARRAY<STRUCT<...>>`, with `items.sku` |
| Backtick-quoted field names in the type text | `` STRUCT<`first_name` STRING> `` |
| BigQuery scalar types | `INT64`, `STRING`, `NUMERIC(38, 9)`, `TIMESTAMP`, `GEOGRAPHY`, `JSON` |
| Warehouse column descriptions | `column_comment` on the source's columns |
| `policy_tags` on a column | `stg_customers.email` in the manifest |
| dbt 1.9.6+ so meta belongs under `config:` | `metadata.dbt_version` |

`raw_customers` is a source with no dbt documentation at all, so it exercises
both routes to documenting a source: the warehouse's own column descriptions,
and backfill from the staging model below it.
