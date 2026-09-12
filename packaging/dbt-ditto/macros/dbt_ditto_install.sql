{#
  Print the command that actually writes the documentation.

    dbt run-operation dbt_ditto_install
    dbt run-operation dbt_ditto_install --args '{select: "stg_*", dry_run: true}'

  dbt's Jinja has no way to execute a program, so no macro in any dbt package
  can run a binary — the ones that appear to (dbt-doc-inherit included) hand you
  a shell command and let you run it. This macro does the same, composed with
  your arguments, and the launcher it points at finds or installs the binary.
#}
{% macro dbt_ditto_install(select=none, dry_run=false, check=false, config=none, verbose=false) %}
  {% if not execute %}{{ return("") }}{% endif %}

  {% set args = [] %}
  {% if select is not none %}{% do args.append("--select '" ~ select ~ "'") %}{% endif %}
  {% if dry_run %}{% do args.append("--dry-run") %}{% endif %}
  {% if check %}{% do args.append("--check") %}{% endif %}
  {% if config is not none %}{% do args.append("--config '" ~ config ~ "'") %}{% endif %}
  {% if verbose %}{% do args.append("--verbose") %}{% endif %}

  {% do log(
    "\ndbt-ditto writes files, and dbt cannot run a program from Jinja."
    ~ "\nRun this from the project root:"
    ~ "\n"
    ~ "\n    python dbt_packages/dbt_ditto/run.py " ~ (args | join(" ")) | trim
    ~ "\n"
    ~ "\nThe launcher uses `dbt-ditto` from PATH, or `uvx dbt-ditto` when uv is"
    ~ "\ninstalled. To skip it entirely: `uv tool install dbt-ditto` (or"
    ~ "\n`pip install dbt-ditto`), then `dbt-ditto inherit`."
    ~ "\n"
    ~ "\nFor a read-only preview that needs no binary at all:"
    ~ "\n"
    ~ "\n    dbt run-operation dbt_ditto_suggest"
    ~ "\n", info=true) %}
{% endmacro %}
