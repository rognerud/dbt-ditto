#!/usr/bin/env python3
"""Run dbt-ditto from inside a dbt project.

    python dbt_packages/dbt_ditto/run.py [flags]

dbt packages cannot ship or execute a binary, so this launcher is the bridge:
it finds `dbt-ditto` on PATH, falls back to `uvx dbt-ditto`, and otherwise
says how to install it. Arguments are passed straight through, so anything the
CLI accepts works here:

    python dbt_packages/dbt_ditto/run.py --check
    python dbt_packages/dbt_ditto/run.py --select 'stg_*' --verbose

The subcommand defaults to `inherit`.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys

COMMANDS = {"inherit", "version", "help"}

INSTALL_HINT = """dbt-ditto is not installed.

    uv tool install dbt-ditto      # or: pip install dbt-ditto

Then run `dbt-ditto inherit` directly, or this launcher again."""


def command_for(args: list[str]) -> list[str] | None:
    """Return the argv to execute, or None when nothing can run it."""
    binary = shutil.which("dbt-ditto")
    if binary:
        return [binary, *args]
    # uvx fetches the wheel into a cache and runs it without installing
    # anything into the project's environment, which is the right default for a
    # tool a dbt project only invokes now and then.
    uvx = shutil.which("uvx")
    if uvx:
        return [uvx, "dbt-ditto", *args]
    return None


def has_config(root: str, args: list[str]) -> bool:
    """True when dbt-ditto will find a configuration of its own."""
    if any(a in ("-c", "--config") or a.startswith("--config=") for a in args):
        return True
    directory = root
    while True:
        if os.path.exists(os.path.join(directory, "dbt_ditto.yml")):
            return True
        parent = os.path.dirname(directory)
        if parent == directory:
            return False
        directory = parent


def main(argv: list[str]) -> int:
    args = list(argv)
    if not args or args[0].startswith("-") or args[0] not in COMMANDS:
        args = ["inherit", *args]

    # dbt_packages/dbt_ditto/run.py is two directories below the project root,
    # which is where dbt-ditto expects to be run from: it searches upwards for
    # dbt_ditto.yml and reads ./target/manifest.json.
    root = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))

    # A dbt project usually has no dbt_ditto.yml, and does not need one: naming
    # the project directory tells dbt-ditto to use the `+dbt-osmosis:` rules
    # already in dbt_project.yml. Only add it when nothing else configures the
    # run, so a project that does have a config keeps multi-project inheritance.
    if args[0] == "inherit" and not has_config(root, args):
        args.append(".")

    command = command_for(args)
    if command is None:
        print(INSTALL_HINT, file=sys.stderr)
        return 1

    return subprocess.call(command, cwd=root)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
