#!/usr/bin/env python3
"""Write an artifact meta.json: write_meta.py PATH ADAPTER VER DBT SCHEMA [k=v ...]."""

import json
import sys

path, adapter, adapter_version, dbt_version, schema_version = sys.argv[1:6]
meta = {
    "adapter": adapter,
    "adapter_version": adapter_version,
    "dbt_version": dbt_version,
    "dbt_schema_version": schema_version,
}
for extra in sys.argv[6:]:
    key, _, value = extra.partition("=")
    meta[key] = value

with open(path, "w") as fh:
    json.dump(meta, fh, indent=2)
    fh.write("\n")
