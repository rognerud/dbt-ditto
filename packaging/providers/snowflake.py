#!/usr/bin/env python3
"""dbt-ditto source provider for Snowflake.

Documents external sources from Snowflake's own metadata, using the credentials
dbt already has in profiles.yml.

    sources:
      providers:
        - command: "uv run --with snowflake-connector-python packaging/providers/snowflake.py"

One query per schema, not per table: INFORMATION_SCHEMA lives in the database
being described, so every source in one database and schema is answered
together. Tags are fetched separately and are allowed to fail — reading them
needs privileges a documentation job is often not granted, and losing the tags
is not a reason to lose the comments.
"""

from __future__ import annotations

import sys
from collections import defaultdict
from typing import Any

sys.path.insert(0, __file__.rsplit("/", 1)[0])

from dbt_ditto_provider import (  # noqa: E402
    Column,
    Doc,
    Project,
    Source,
    load_profile,
    read_request,
    write_response,
)


def connect(profile: dict[str, Any]):
    """Open a connection from a dbt profile block.

    dbt-snowflake talks to Snowflake through `snowflake.connector`, and so does
    this, with the same keys out of the same profile. Password, key pair and
    every `authenticator` value therefore work without being configured twice.
    """
    import snowflake.connector

    kwargs: dict[str, Any] = {
        "account": profile["account"],
        "user": profile.get("user"),
        "role": profile.get("role"),
        "warehouse": profile.get("warehouse"),
        "database": profile.get("database"),
        "schema": profile.get("schema"),
        "client_session_keep_alive": False,
    }

    if profile.get("password"):
        kwargs["password"] = profile["password"]
    if profile.get("authenticator"):
        kwargs["authenticator"] = profile["authenticator"]
    if profile.get("token"):
        kwargs["token"] = profile["token"]

    key_path = profile.get("private_key_path")
    key_content = profile.get("private_key")
    if key_path or key_content:
        kwargs["private_key"] = _load_private_key(
            key_path, key_content, profile.get("private_key_passphrase")
        )

    return snowflake.connector.connect(
        **{k: v for k, v in kwargs.items() if v is not None}
    )


def _load_private_key(path: str | None, content: str | None, passphrase: str | None):
    from cryptography.hazmat.backends import default_backend
    from cryptography.hazmat.primitives import serialization

    if path:
        with open(path, "rb") as fh:
            raw = fh.read()
    else:
        raw = (content or "").encode()

    key = serialization.load_pem_private_key(
        raw,
        password=passphrase.encode() if passphrase else None,
        backend=default_backend(),
    )
    return key.private_bytes(
        encoding=serialization.Encoding.DER,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )


def fetch_columns(cursor, database: str, schema: str, tables: list[str]) -> dict:
    """Column name, type, ordinal and comment for every named table."""
    placeholders = ", ".join(["%s"] * len(tables))
    cursor.execute(
        f"""
        select table_name, column_name, ordinal_position, comment,
               data_type, character_maximum_length, numeric_precision, numeric_scale
        from {quote_ident(database)}.information_schema.columns
        where table_schema = %s and table_name in ({placeholders})
        order by table_name, ordinal_position
        """,
        [schema.upper()] + [t.upper() for t in tables],
    )
    out: dict[str, list[Column]] = defaultdict(list)
    for row in cursor.fetchall():
        table, name, ordinal, comment, dtype, length, precision, scale = row
        out[table].append(
            Column(
                name=name,
                data_type=render_type(dtype, length, precision, scale),
                description=comment or "",
                index=int(ordinal or 0),
            )
        )
    return out


def fetch_table_comments(cursor, database: str, schema: str, tables: list[str]) -> dict:
    placeholders = ", ".join(["%s"] * len(tables))
    cursor.execute(
        f"""
        select table_name, comment
        from {quote_ident(database)}.information_schema.tables
        where table_schema = %s and table_name in ({placeholders})
        """,
        [schema.upper()] + [t.upper() for t in tables],
    )
    return {row[0]: (row[1] or "") for row in cursor.fetchall()}


def fetch_tags(cursor, database: str, schema: str, tables: list[str]):
    """Snowflake tags, which are this warehouse's key-value metadata.

    Read from ACCOUNT_USAGE because that is the only view carrying every tag on
    every object; it needs a grant a documentation job may not have, and it
    lags by up to two hours. Both are acceptable for documentation and neither
    is worth failing over, so the caller treats an error here as a warning.

    Returned as (table tags, column tags keyed by column).
    """
    placeholders = ", ".join(["%s"] * len(tables))
    cursor.execute(
        f"""
        select object_name, column_name, tag_name, tag_value
        from snowflake.account_usage.tag_references
        where object_database = %s and object_schema = %s
          and object_name in ({placeholders})
          and object_deleted is null
        """,
        [database.upper(), schema.upper()] + [t.upper() for t in tables],
    )
    table_tags: dict[str, dict[str, str]] = defaultdict(dict)
    column_tags: dict[str, dict[str, dict[str, str]]] = defaultdict(
        lambda: defaultdict(dict)
    )
    for object_name, column_name, tag_name, tag_value in cursor.fetchall():
        if column_name:
            column_tags[object_name][column_name][tag_name] = tag_value or ""
        else:
            table_tags[object_name][tag_name] = tag_value or ""
    return table_tags, column_tags


def render_type(dtype: str, length, precision, scale) -> str:
    """Render a type the way dbt-snowflake reports it.

    The adapter's Column class writes `VARCHAR(16777216)` and `NUMBER(38,0)`
    rather than the bare type name, and the point of matching it is that a
    project running both tools does not get a diff on every `data_type:`.
    """
    dtype = (dtype or "").upper()
    if dtype == "TEXT":
        dtype = "VARCHAR"
    if dtype in ("VARCHAR", "CHAR", "BINARY") and length is not None:
        return f"{dtype}({int(length)})"
    if dtype in ("NUMBER", "DECIMAL", "NUMERIC") and precision is not None:
        return f"{dtype}({int(precision)},{int(scale or 0)})"
    return dtype


def quote_ident(name: str) -> str:
    """Quote a database name for interpolation into a query.

    Identifiers cannot be bound as parameters, so this is the one place a value
    reaches the SQL text. The value comes from the project's own manifest — it
    is the database dbt itself writes to — but an embedded quote would still
    end the identifier, so it is doubled.
    """
    return '"' + name.replace('"', '""') + '"'


def describe_schema(cursor, database: str, schema: str, sources: list[Source]):
    """Document every source in one database and schema."""
    by_identifier = {s.identifier.upper(): s for s in sources}
    tables = list(by_identifier)

    columns = fetch_columns(cursor, database, schema, tables)
    comments = fetch_table_comments(cursor, database, schema, tables)

    warnings: list[str] = []
    try:
        table_tags, column_tags = fetch_tags(cursor, database, schema, tables)
    except Exception as err:  # noqa: BLE001 - tags are optional, comments are not
        table_tags, column_tags = {}, {}
        warnings.append(
            f"{database}.{schema}: tags not read ({err}); "
            "reading SNOWFLAKE.ACCOUNT_USAGE.TAG_REFERENCES needs a grant this role does not have"
        )

    docs = []
    for identifier, source in by_identifier.items():
        cols = columns.get(identifier, [])
        if not cols:
            warnings.append(
                f"{source.unique_id}: {source.fqn} has no columns in INFORMATION_SCHEMA"
            )
            continue
        for c in cols:
            for tag, value in (column_tags.get(identifier, {}).get(c.name, {})).items():
                c.labels[tag] = value
        docs.append(
            Doc(
                unique_id=source.unique_id,
                description=comments.get(identifier, ""),
                labels=dict(table_tags.get(identifier, {})),
                columns=cols,
            )
        )
    return docs, warnings


def main() -> None:
    request = read_request()
    docs: list[Doc] = []
    warnings: list[str] = []

    for project_name, sources in request.by_project().items():
        project = request.projects.get(project_name) or Project(
            name=project_name, root="", profile="", target="", profiles_dir=""
        )
        try:
            profile = load_profile(project)
        except SystemExit as err:
            warnings.append(f"{project_name}: {err}")
            continue

        if (profile.get("type") or "").lower() != "snowflake":
            warnings.append(
                f"{project_name}: profile target is {profile.get('type')!r}, not snowflake; skipped"
            )
            continue

        # Grouped because INFORMATION_SCHEMA lives in the database being
        # described: one query answers for every source that shares a schema.
        grouped: dict[tuple[str, str], list[Source]] = defaultdict(list)
        for s in sources:
            grouped[(s.database, s.schema)].append(s)

        connection = connect(profile)
        try:
            cursor = connection.cursor()
            for (database, schema), group in grouped.items():
                got, warned = describe_schema(cursor, database, schema, group)
                docs.extend(got)
                warnings.extend(warned)
        finally:
            connection.close()

    write_response(docs, warnings)


if __name__ == "__main__":
    main()
