#!/usr/bin/env python3
"""dbt-ditto source provider for BigQuery.

Documents external sources from BigQuery's own metadata, using the credentials
dbt already has in profiles.yml.

    dbt-ditto inherit --refresh-sources

with, in dbt_ditto.yml:

    sources:
      providers:
        - command: "uv run --with google-cloud-bigquery packaging/providers/bigquery.py"

Why `tables.get` rather than INFORMATION_SCHEMA: it is free metadata rather than
a query job, so it needs no `jobUser` role and costs nothing, and the calls run
in parallel. Asked about tens of external sources instead of every relation in
the project, a refresh is a second or so — against minutes for
`dbt docs generate`, which walks the whole project to reach the same handful.
"""

from __future__ import annotations

import concurrent.futures
import sys
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

# How many tables.get calls are in flight at once. Metadata reads are cheap and
# the default quota is generous; this exists so a project with a thousand
# sources does not open a thousand sockets.
MAX_PARALLEL = 16


def client_for(profile: dict[str, Any]):
    """Build a BigQuery client from a dbt profile block.

    Every authentication method dbt-bigquery supports maps onto a credentials
    object here, so a project that already runs dbt needs no further setup. An
    unrecognised method falls through to application default credentials, which
    is what `method: oauth` means anyway.
    """
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

        credentials = service_account.Credentials.from_service_account_info(
            profile["keyfile_json"]
        )
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
    """Render a field's type the way dbt-bigquery writes it into catalog.json.

    A record becomes ``STRUCT<`name` TYPE, ...>`` with backtick-quoted field
    names, and a repeated field is wrapped in ``ARRAY<...>``. Matching this
    matters because the type dbt-ditto writes into `data_type:` should be the
    one `dbt docs generate` would have written, or a project that runs both
    gets a diff every time it switches.
    """
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
    """Walk a BigQuery schema into the flat, dotted column list dbt reports.

    dbt-bigquery's catalog query joins INFORMATION_SCHEMA.COLUMNS to
    COLUMN_FIELD_PATHS, so a nested record arrives as the parent column *and*
    every dotted leaf below it. Both are emitted here for the same reason: a
    project documenting `profile.first_name` needs the leaf to exist, and one
    documenting `profile` needs the parent.
    """
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
        # Policy tags are BigQuery's access classification. They have no
        # equivalent anywhere else, so they stay a provider concern and travel
        # as an extra key under dbt's own spelling.
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

    for project_name, sources in request.by_project().items():
        project = request.projects.get(project_name) or Project(
            name=project_name, root="", profile="", target="", profiles_dir=""
        )
        try:
            profile = load_profile(project)
        except SystemExit as err:
            warnings.append(f"{project_name}: {err}")
            continue

        if (profile.get("type") or "").lower() != "bigquery":
            warnings.append(
                f"{project_name}: profile target is {profile.get('type')!r}, not bigquery; skipped"
            )
            continue

        client = client_for(profile)
        with concurrent.futures.ThreadPoolExecutor(max_workers=MAX_PARALLEL) as pool:
            for doc, warning in pool.map(lambda s: describe(client, s), sources):
                if doc is not None:
                    docs.append(doc)
                if warning:
                    warnings.append(warning)

    write_response(docs, warnings)


if __name__ == "__main__":
    main()
