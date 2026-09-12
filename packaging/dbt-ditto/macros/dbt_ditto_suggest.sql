{#
  Print the documentation each undocumented column would inherit.

    dbt run-operation dbt_ditto_suggest
    dbt run-operation dbt_ditto_suggest --args '{select: stg_customers}'

  This writes nothing. dbt's Jinja cannot open a file, let alone run a binary,
  so a macro can only ever hand you YAML to paste — which is what dbt-doc-inherit
  does too. `dbt-ditto inherit` writes the same conclusions into the right files
  itself, and is the authority when the two ever disagree: it also reads
  catalog.json, so it knows about columns the manifest has never heard of.

  Args:
    select    only consider nodes whose name contains this string
    ambiguity report columns that several parents document differently
#}
{% macro dbt_ditto_suggest(select=none, ambiguity=true, directive_prefix="Inherited:") %}
  {% if not execute %}{{ return("") }}{% endif %}

  {% set nodes = dbt_ditto__all_nodes() %}
  {% set ns = namespace(columns=0, warnings=0, nodes=0) %}

  {% for unique_id in graph.nodes.keys() | sort %}
    {% set node = graph.nodes[unique_id] %}
    {% if dbt_ditto__is_documentable(unique_id)
          and node.package_name == project_name
          and (select is none or select in node.name) %}

      {% set suggestions = [] %}
      {% set warnings = [] %}
      {% set generations = dbt_ditto__generations(nodes, unique_id) %}

      {% for column_name in (node.columns or {}).keys() | sort %}
        {% set column = node.columns[column_name] %}
        {% set local = dbt_ditto__description(column) %}

        {% if local.lower().startswith(directive_prefix | lower) and "." in local[directive_prefix | length:] %}
          {# An explicit pointer: the analyst naming the column to copy from. #}
          {% set target = local[directive_prefix | length:] | trim %}
          {% set resolved = dbt_ditto__follow_directive(nodes, target) %}
          {% if resolved.ok %}
            {% do suggestions.append({
                 "name": column_name,
                 "description": resolved.description,
                 "from": resolved["from"]
               }) %}
          {% else %}
            {% do warnings.append(column_name ~ ": directive '" ~ target ~ "' " ~ resolved.error) %}
          {% endif %}

        {% elif dbt_ditto__is_placeholder(local) %}
          {# Undocumented: take the nearest generation that says anything. #}
          {% set found = namespace(description=none, source=none, rivals=[]) %}
          {% for generation in generations %}
            {% if found.description is none %}
              {% for ancestor_id in generation %}
                {% set ancestor = nodes[ancestor_id] %}
                {% set candidate = dbt_ditto__columns_by_key(ancestor).get(column_name | lower) %}
                {% set candidate_description = dbt_ditto__description(candidate) %}
                {% if not dbt_ditto__is_placeholder(candidate_description) %}
                  {% if found.description is none %}
                    {% set found.description = candidate_description %}
                    {% set found.source = ancestor_id %}
                  {% elif candidate_description != found.description %}
                    {% do found.rivals.append(ancestor_id) %}
                  {% endif %}
                {% endif %}
              {% endfor %}
            {% endif %}
          {% endfor %}

          {% if found.description is not none %}
            {% do suggestions.append({
                 "name": column_name,
                 "description": found.description,
                 "from": found.source
               }) %}
            {% if ambiguity and found.rivals %}
              {% do warnings.append(
                   column_name ~ ": also documented differently by "
                   ~ (found.rivals | join(", ")) ~ "; took " ~ found.source) %}
            {% endif %}
          {% endif %}
        {% endif %}
      {% endfor %}

      {% if suggestions or warnings %}
        {% set ns.nodes = ns.nodes + 1 %}
        {% set lines = [] %}
        {% if suggestions %}
          {% do lines.append("models:") %}
          {% do lines.append("  - name: " ~ node.name) %}
          {% do lines.append("    columns:") %}
          {% for s in suggestions %}
            {% set ns.columns = ns.columns + 1 %}
            {% do lines.append("      - name: " ~ s.name) %}
            {% do lines.append("        description: " ~ (s.description | tojson)) %}
            {% do lines.append("        meta:") %}
            {% do lines.append("          osmosis_progenitor: " ~ s["from"]) %}
          {% endfor %}
        {% endif %}
        {% for w in warnings %}
          {% set ns.warnings = ns.warnings + 1 %}
          {% do lines.append("# warning: " ~ node.name ~ "." ~ w) %}
        {% endfor %}
        {% do log("\n# " ~ (node.patch_path or node.original_file_path) ~ "\n" ~ (lines | join("\n")), info=true) %}
      {% endif %}
    {% endif %}
  {% endfor %}

  {% do log("\n# " ~ ns.columns ~ " columns would inherit across " ~ ns.nodes
            ~ " nodes, " ~ ns.warnings ~ " warnings."
            ~ "\n# Nothing was written. `dbt-ditto inherit` applies this;"
            ~ " see `dbt run-operation dbt_ditto_install`.", info=true) %}
{% endmacro %}
