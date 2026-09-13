#!/usr/bin/env python3
"""dbt-ditto source provider for BigQuery."""

from __future__ import annotations

import concurrent.futures
import json
import sys
from typing import Any

sys.path.insert(0, __file__.rsplit("/", 1)[0])

from dbt_ditto_provider import (
    Column,
    Doc,
    Source,
    each_project,
    read_request,
    write_response,
)

# How many tables.get calls are in flight at once.
MAX_PARALLEL = 16


def client_for(profile: dict[str, Any]):
    """Build a BigQuery client from a dbt profile block."""
    from google.cloud import bigquery

    method = (profile.get("method") or "oauth").lower()
    project = profile.get("project") or profile.get("database")
    location = profile.get("location")

    credentials = None
    if method == "service-account":
        from google.oauth2 import service_account

        credentials = service_account.Credentials.from_service_account_file(
            profile["keyfile"]
        )
    elif method == "service-account-json":
        from google.oauth2 import service_account

        # dbt accepts keyfile_json either inline as a mapping or, via env_var, as
        # the JSON text of one. from_service_account_info only takes the mapping.
        keyfile = profile["keyfile_json"]
        if isinstance(keyfile, str):
            keyfile = json.loads(keyfile)
        credentials = service_account.Credentials.from_service_account_info(keyfile)
    elif method == "oauth-secrets":
        from google.oauth2.credentials import Credentials

        credentials = Credentials(
            token=profile.get("token"),
            refresh_token=profile.get("refresh_token"),
            client_id=profile.get("client_id"),
            client_secret=profile.get("client_secret"),
            token_uri=profile.get("token_uri", "https://oauth2.googleapis.com/token"),
        )

    impersonate = profile.get("impersonate_service_account")
    if impersonate:
        from google.auth import default, impersonated_credentials

        base = credentials
        if base is None:
            base, _ = default()
        credentials = impersonated_credentials.Credentials(
            source_credentials=base,
            target_principal=impersonate,
            target_scopes=["https://www.googleapis.com/auth/cloud-platform"],
        )

    return bigquery.Client(project=project, credentials=credentials, location=location)


def render_type(field) -> str:
    """Render a field's type the way dbt-bigquery writes it into catalog.json."""
    if field.field_type in ("RECORD", "STRUCT"):
        inner = ", ".join(f"`{f.name}` {render_type(f)}" for f in field.fields)
        base = f"STRUCT<{inner}>"
    elif field.field_type == "NUMERIC" and field.precision is not None:
        base = f"NUMERIC({field.precision}, {field.scale or 0})"
    elif field.field_type == "BIGNUMERIC" and field.precision is not None:
        base = f"BIGNUMERIC({field.precision}, {field.scale or 0})"
    else:
        base = field.field_type

    if field.mode == "REPEATED":
        return f"ARRAY<{base}>"
    return base


def flatten(fields, prefix: str = "", start: int = 1) -> list[Column]:
    """Walk a BigQuery schema into the flat, dotted column list dbt reports."""
    out: list[Column] = []
    index = start
    for f in fields:
        name = f"{prefix}{f.name}"
        column = Column(
            name=name,
            data_type=render_type(f),
            description=f.description or "",
            index=index,
        )
        # Policy tags are BigQuery's access classification.
        tags = getattr(f, "policy_tags", None)
        if tags and getattr(tags, "names", None):
            column.extra["policy_tags"] = list(tags.names)
        out.append(column)
        index += 1

        if f.field_type in ("RECORD", "STRUCT") and f.fields:
            nested = flatten(f.fields, prefix=f"{name}.", start=index)
            out.extend(nested)
            index += len(nested)
    return out


def describe(client, source: Source) -> tuple[Doc | None, str | None]:
    """Fetch one relation. A missing table is a warning, not a failure."""
    from google.api_core import exceptions

    try:
        table = client.get_table(source.fqn)
    except exceptions.NotFound:
        return None, f"{source.unique_id}: {source.fqn} does not exist in BigQuery"
    except exceptions.Forbidden as err:
        return None, f"{source.unique_id}: no access to {source.fqn} ({err.message})"

    return (
        Doc(
            unique_id=source.unique_id,
            description=table.description or "",
            # BigQuery labels are the portable key-value metadata dbt-ditto
            # routes into meta or tags; what they mean in dbt terms is the
            # project's decision, not this provider's.
            labels={k: (v or "") for k, v in (table.labels or {}).items()},
            columns=flatten(table.schema),
        ),
        None,
    )


def main() -> None:
    request = read_request()
    docs: list[Doc] = []
    warnings: list[str] = []

    for profile, sources in each_project(request, "bigquery", warnings):
        client = client_for(profile)
        with concurrent.futures.ThreadPoolExecutor(max_workers=MAX_PARALLEL) as pool:
            # client is bound as a default argument rather than closed over: the
            # callable outlives this iteration of the loop, and a later project's
            # client must not be the one an in-flight call reaches for.
            def fetch(s: Source, c=client):
                return describe(c, s)

            for doc, warning in pool.map(fetch, sources):
                if doc is not None:
                    docs.append(doc)
                if warning:
                    warnings.append(warning)

    write_response(docs, warnings)


if __name__ == "__main__":
    main()
