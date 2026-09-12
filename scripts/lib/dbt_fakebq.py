#!/usr/bin/env python3
"""Run the dbt CLI against a local BigQuery emulator."""

from __future__ import annotations

import sys


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2

    endpoint = sys.argv[1]
    dbt_args = sys.argv[2:]

    from google.api_core.client_options import ClientOptions
    from google.auth.credentials import AnonymousCredentials
    from google.cloud import bigquery

    real_client = bigquery.Client

    class EmulatorClient(real_client):  # type: ignore[misc, valid-type]
        """A BigQuery client aimed at the emulator instead of Google."""

        def __init__(self, *args, **kwargs):
            # The real signature is Client(project, credentials, _http, location, ...)
            # and dbt passes the first two positionally, so adding credentials as a
            # keyword on top of that is a TypeError.
            args = list(args)
            if len(args) >= 2:
                args[1] = AnonymousCredentials()
            else:
                kwargs["credentials"] = AnonymousCredentials()
            if not args:
                # The emulator has no notion of a billing project, but the
                # client insists on one being resolvable.
                kwargs.setdefault("project", "dbt-ditto")

            kwargs["client_options"] = ClientOptions(api_endpoint=endpoint)
            super().__init__(*args, **kwargs)

    bigquery.Client = EmulatorClient
    # dbt-bigquery imports the symbol directly in places, so the module it
    # imported from has to be patched too.
    try:
        from dbt.adapters.bigquery import clients as bq_clients

        bq_clients.google.cloud.bigquery.Client = EmulatorClient  # type: ignore[attr-defined]
    except Exception:  # noqa: BLE001 - older layouts simply do not have it
        pass

    from dbt.cli.main import dbtRunner

    result = dbtRunner().invoke(dbt_args)
    if result.success:
        return 0
    if result.exception is not None:
        print(f"dbt failed: {result.exception}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
