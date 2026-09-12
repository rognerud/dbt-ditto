#!/usr/bin/env python3
"""Run the dbt CLI against a fake Snowflake.

`fakesnow` replaces `snowflake.connector` with an implementation backed by
DuckDB. dbt-snowflake talks to Snowflake through exactly that connector, so
patching it before dbt starts gives a real dbt run — real adapter, real macros,
real `docs generate` — against no infrastructure at all.

That matters because it makes the Snowflake artifacts *generated* rather than
hand-written: the catalog comes out of the adapter's own catalog macro, and its
identifier casing and type names are Snowflake's, not a guess at Snowflake's.

    python scripts/lib/dbt_fakesnow.py <db-path> run --quiet
"""

from __future__ import annotations

import sys
from pathlib import Path


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 2

    # fakesnow wants a directory it can keep one DuckDB file per database in,
    # not a single file.
    db_path = Path(sys.argv[1]).resolve()
    db_path.mkdir(parents=True, exist_ok=True)
    dbt_args = sys.argv[2:]

    import fakesnow

    # A file-backed database so state survives between the seed, run and docs
    # generate invocations, which are separate processes.
    with fakesnow.patch(db_path=str(db_path), create_database_on_connect=True,
                        create_schema_on_connect=True):
        from dbt.cli.main import dbtRunner

        result = dbtRunner().invoke(dbt_args)
        if result.success:
            return 0
        if result.exception is not None:
            print(f"dbt failed: {result.exception}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
