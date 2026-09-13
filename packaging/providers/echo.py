#!/usr/bin/env python3
"""A source provider that answers from a file instead of a warehouse."""

from __future__ import annotations

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from dbt_ditto_provider import Column, Doc, read_request, write_response


def main() -> None:
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} ANSWERS.json", file=sys.stderr)
        raise SystemExit(2)

    with open(sys.argv[1], encoding="utf-8") as fh:
        answers = {d["unique_id"]: d for d in json.load(fh)}

    request = read_request()
    docs, warnings = [], []
    for source in request.sources:
        answer = answers.get(source.unique_id)
        if answer is None:
            warnings.append(
                f"{source.unique_id}: {source.fqn} is not in the answer file"
            )
            continue
        # Asserted rather than assumed: a provider is given the relation to look
        # up, and one that ignored it would document the wrong table.
        if not source.database or not source.identifier:
            warnings.append(f"{source.unique_id}: the request named no relation")
            continue
        docs.append(
            Doc(
                unique_id=source.unique_id,
                description=answer.get("description", ""),
                labels=answer.get("labels", {}),
                columns=[
                    Column(
                        name=c["name"],
                        data_type=c.get("data_type", ""),
                        description=c.get("description", ""),
                        index=c.get("index", 0),
                        labels=c.get("labels", {}),
                        extra=c.get("extra", {}),
                    )
                    for c in answer.get("columns", [])
                ],
            )
        )

    write_response(docs, warnings)


if __name__ == "__main__":
    main()
