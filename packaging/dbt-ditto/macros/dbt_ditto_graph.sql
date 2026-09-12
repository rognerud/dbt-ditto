{#
  Graph helpers shared by the macros in this package.

  Everything here reads dbt's own `graph` object, which run-operation populates
  from the manifest. That is the same input the dbt-ditto binary reads, so the
  suggestions these macros print are computed from exactly the same facts — but
  in Jinja, where nothing can be written to disk. Writing is the binary's job.
#}

{# The resource types that can carry column documentation. #}
{% macro dbt_ditto__is_documentable(unique_id) %}
  {% for prefix in ["model.", "seed.", "source.", "snapshot."] %}
    {% if unique_id.startswith(prefix) %}{{ return(true) }}{% endif %}
  {% endfor %}
  {{ return(false) }}
{% endmacro %}


{# Every node in the manifest, models and sources alike, keyed by unique_id. #}
{% macro dbt_ditto__all_nodes() %}
  {% set nodes = {} %}
  {% do nodes.update(graph.nodes) %}
  {% do nodes.update(graph.sources) %}
  {{ return(nodes) }}
{% endmacro %}


{#
  Ancestors grouped by distance, nearest generation first.

  A node reached by several routes is filed under the first distance that
  reaches it, so each node appears exactly once. Within a generation the list is
  sorted by unique_id, which is the tie-break the binary uses too.
#}
{% macro dbt_ditto__generations(nodes, unique_id, limit=10) %}
  {% set ns = namespace(frontier=[unique_id]) %}
  {% set seen = [unique_id] %}
  {% set generations = [] %}

  {% for _ in range(limit) %}
    {% set next_frontier = [] %}
    {% for current in ns.frontier %}
      {% set node = nodes.get(current) %}
      {% if node %}
        {% for dep in (node.depends_on.nodes if node.depends_on else []) %}
          {% if dep not in seen and dbt_ditto__is_documentable(dep) and dep in nodes %}
            {% do seen.append(dep) %}
            {% do next_frontier.append(dep) %}
          {% endif %}
        {% endfor %}
      {% endif %}
    {% endfor %}
    {% if next_frontier %}
      {% do generations.append(next_frontier | sort) %}
    {% endif %}
    {% set ns.frontier = next_frontier %}
  {% endfor %}

  {{ return(generations) }}
{% endmacro %}


{# A node's columns keyed by lower-cased name, matching the binary's default
   case-insensitive matching. #}
{% macro dbt_ditto__columns_by_key(node) %}
  {% set out = {} %}
  {% for name, column in (node.columns or {}).items() %}
    {% do out.update({name | lower: column}) %}
  {% endfor %}
  {{ return(out) }}
{% endmacro %}


{# The description dbt recorded for a column, as a plain string. #}
{% macro dbt_ditto__description(column) %}
  {% if column is none %}{{ return("") }}{% endif %}
  {{ return((column.description or "") | trim) }}
{% endmacro %}


{# dbt-osmosis' placeholder list: text that means "not documented yet" and so is
   never worth inheriting. #}
{% macro dbt_ditto__is_placeholder(description) %}
  {% set placeholders = [
      "",
      "pending further documentation",
      "no description for this column",
      "not documented",
      "undefined"
  ] %}
  {{ return(description | lower | trim in placeholders) }}
{% endmacro %}


{#
  Resolve an `Inherited: node.column` directive to a description.

  Returns a dict: {"ok": bool, "description": str, "from": str, "error": str}.
#}
{% macro dbt_ditto__follow_directive(nodes, target) %}
  {% if "." not in target %}
    {{ return({"ok": false, "error": "is not `node.column`"}) }}
  {% endif %}

  {% set ref = target.rsplit(".", 1)[0] %}
  {% set column_name = target.rsplit(".", 1)[1] %}

  {% set matches = [] %}
  {% if ref in nodes %}
    {% do matches.append(ref) %}
  {% else %}
    {% for unique_id, node in nodes.items() %}
      {% if (node.name or "") | lower == ref | lower %}
        {% do matches.append(unique_id) %}
      {% endif %}
    {% endfor %}
  {% endif %}

  {% if matches | length != 1 %}
    {{ return({"ok": false, "error": "names no single node '" ~ ref ~ "'"}) }}
  {% endif %}

  {% set target_node = nodes[matches[0]] %}
  {% set column = dbt_ditto__columns_by_key(target_node).get(column_name | lower) %}
  {% if column is none %}
    {{ return({"ok": false, "error": "has no column '" ~ column_name ~ "'"}) }}
  {% endif %}

  {% set description = dbt_ditto__description(column) %}
  {% if dbt_ditto__is_placeholder(description) %}
    {{ return({"ok": false, "error": "points at an undocumented column"}) }}
  {% endif %}

  {{ return({"ok": true, "description": description, "from": matches[0], "error": ""}) }}
{% endmacro %}
