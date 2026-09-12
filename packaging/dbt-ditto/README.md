# dbt-ditto

〃 — *same as above*. Column documentation inheritance for dbt: write a
description once, and every column downstream that means the same thing says
ditto.

Installable with `dbt deps`. It is the dbt-facing half of
[dbt-ditto](https://github.com/rognerud/dbt-ditto), which is the binary
that does the writing.

```yaml
# packages.yml
packages:
  - git: "https://github.com/rognerud/dbt-ditto.git"
    subdirectory: "packaging/dbt-ditto"
    revision: main
```

```bash
dbt deps
dbt run-operation dbt_ditto_suggest     # what would be inherited, printed
dbt run-operation dbt_ditto_install     # how to write it
```

## What the macros can and cannot do

dbt's Jinja cannot open a file or run a program. No dbt package can write your
schema YAML for you, and any that appears to is really printing a shell command
or shipping a Python script beside the macros. This package is explicit about
the split:

| | needs | writes files |
|---|---|---|
| `dbt_ditto_suggest` | nothing but dbt | no — prints YAML to paste |
| `dbt_ditto_install` | nothing but dbt | no — prints the command to run |
| `run.py` / `dbt-ditto inherit` | the binary | yes |

`dbt_ditto_suggest` reads the same manifest the binary does, so its conclusions
match. The binary knows strictly more: it also reads `catalog.json`, so it can
add columns the YAML has never listed, remove ones the warehouse no longer has,
write `data_type`, and inherit across separately parsed projects.

## The binary

```bash
uv tool install dbt-ditto        # or: pip install dbt-ditto
dbt-ditto inherit                # from the project root
dbt-ditto inherit --check        # exit 1 if anything is out of date, for CI
```

`run.py` is a launcher for projects that would rather not install anything: it
uses `dbt-ditto` from `PATH`, falls back to `uvx dbt-ditto`, and otherwise
prints how to install it.

```bash
python dbt_packages/dbt_ditto/run.py --check
```

## Directives

Inheritance matches on column name, which cannot follow a column that was
renamed. Say where the documentation lives instead:

```yaml
columns:
  - name: cust_id
    description: "Inherited: stg_customers.customer_id"
```

Both the macro and the binary resolve this, and both report a directive that
points at a node or column that does not exist, or at a column nobody has
documented. The unresolved text stays in the file: dropping it would lose the
instruction.

The node may be written as a bare name or as a full `unique_id`
(`model.jaffle.stg_customers.customer_id`), which is the way to be unambiguous
when two projects both have a model of that name.

## Ambiguity

When several parents document the same column differently, the winner is
whichever `unique_id` sorts first — arbitrary, and worth knowing about:

```
warning: model.shop.orders_joined.id documented differently by
         seed.shop.raw_orders; took seed.shop.raw_customers
```

Resolve it by documenting the column locally, or with a directive naming the
parent you meant.

## Publishing to the dbt package hub

The hub indexes repositories whose `dbt_project.yml` sits at the root, so the
hub listing is served from a mirror of this directory:

1. Push `packaging/dbt-ditto/` to `rognerud/dbt_ditto` as the repository root,
   and tag it (`v0.1.0`) — the hub takes versions from git tags.
   `mirror-dbt-ditto.yml` does this on every release.
2. Open a PR against [dbt-labs/hubcap](https://github.com/dbt-labs/hubcap)
   adding `"rognerud": ["dbt_ditto"]` to `hub.json`.
3. Once indexed, installation is by name:

   ```yaml
   packages:
     - package: rognerud/dbt_ditto
       version: 0.1.0
   ```

The mirror is `dbt_ditto` with an underscore for two reasons: dbt packages are
named that way, matching the `name:` in `dbt_project.yml` and what a user writes
in `packages.yml`; and the mirror cannot be called `rognerud/dbt-ditto`, because
that is the repository this directory lives in. The binary, the PyPI
distribution and this repository all use the dash.

Until then, the `git:` + `subdirectory:` form above installs from this
repository directly and needs no hub listing at all.
