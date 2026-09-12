"""Shared plumbing for dbt-ditto source providers.

A provider is a program dbt-ditto runs to document its **external sources** —
the raw tables no dbt project builds, which inheritance can never reach because
a source is a root of the DAG. dbt-ditto writes a request to stdin and reads a
response from stdout; see .agents/source-providers.md for the contract.

Nothing here talks to a warehouse. This module resolves *how* to talk to one,
by reading the connection dbt itself uses: the request names each project's
root, its `profile:` and the profiles directory, so a provider authenticates
exactly the way `dbt run` does for the same project rather than asking for a
second copy of the same credentials.
"""

from __future__ import annotations

import json
import os
import sys
from dataclasses import dataclass, field
from typing import Any, Iterable

CONTRACT_VERSION = 1


@dataclass
class Source:
    """One relation dbt-ditto wants documented."""

    unique_id: str
    database: str
    schema: str
    identifier: str
    source_name: str = ""
    name: str = ""
    project: str = ""

    @property
    def fqn(self) -> str:
        return f"{self.database}.{self.schema}.{self.identifier}"


@dataclass
class Project:
    """The dbt project a source belongs to, and where its credentials live."""

    name: str
    root: str
    profile: str
    target: str
    profiles_dir: str


@dataclass
class Column:
    name: str
    data_type: str = ""
    description: str = ""
    index: int = 0
    labels: dict[str, str] = field(default_factory=dict)
    extra: dict[str, Any] = field(default_factory=dict)

    def to_json(self) -> dict[str, Any]:
        out: dict[str, Any] = {"name": self.name}
        if self.data_type:
            out["data_type"] = self.data_type
        if self.description:
            out["description"] = self.description
        if self.index:
            out["index"] = self.index
        if self.labels:
            out["labels"] = self.labels
        if self.extra:
            out["extra"] = self.extra
        return out


@dataclass
class Doc:
    unique_id: str
    description: str = ""
    labels: dict[str, str] = field(default_factory=dict)
    columns: list[Column] = field(default_factory=list)

    def to_json(self) -> dict[str, Any]:
        out: dict[str, Any] = {"unique_id": self.unique_id}
        if self.description:
            out["description"] = self.description
        if self.labels:
            out["labels"] = self.labels
        if self.columns:
            out["columns"] = [c.to_json() for c in self.columns]
        return out


@dataclass
class Request:
    projects: dict[str, Project]
    sources: list[Source]

    def by_project(self) -> dict[str, list[Source]]:
        """Sources grouped by the project whose credentials reach them."""
        out: dict[str, list[Source]] = {}
        for s in self.sources:
            out.setdefault(s.project, []).append(s)
        return out


def read_request(stream=None) -> Request:
    """Parse the request on stdin."""
    raw = json.load(stream or sys.stdin)
    version = raw.get("version", CONTRACT_VERSION)
    if version > CONTRACT_VERSION:
        raise SystemExit(
            f"request speaks contract version {version}, this provider understands {CONTRACT_VERSION}"
        )
    projects = {
        p["name"]: Project(
            name=p.get("name", ""),
            root=p.get("root", ""),
            profile=p.get("profile", ""),
            target=p.get("target", ""),
            profiles_dir=p.get("profiles_dir", ""),
        )
        for p in raw.get("projects") or []
    }
    sources = [
        Source(
            unique_id=s["unique_id"],
            database=s.get("database", ""),
            schema=s.get("schema", ""),
            identifier=s.get("identifier", ""),
            source_name=s.get("source_name", ""),
            name=s.get("name", ""),
            project=s.get("project", ""),
        )
        for s in raw.get("sources") or []
    ]
    return Request(projects=projects, sources=sources)


def write_response(docs: Iterable[Doc], warnings: Iterable[str] = ()) -> None:
    """Write the response to stdout.

    Only stdout carries the answer; anything a person should read goes to
    stderr, and anything dbt-ditto should report goes in `warnings`. A provider
    that prints progress to stdout produces a response that will not parse.
    """
    json.dump(
        {
            "version": CONTRACT_VERSION,
            "sources": [d.to_json() for d in docs],
            "warnings": list(warnings),
        },
        sys.stdout,
    )
    sys.stdout.write("\n")


# --- profiles.yml ---------------------------------------------------------


def load_profile(project: Project) -> dict[str, Any]:
    """Return the resolved `outputs.<target>` block for a project.

    dbt's own loader is used when dbt-core is importable, because it is the only
    thing guaranteed to agree with dbt about Jinja, `env_var` defaults and
    target selection. The hand-rolled fallback exists so a provider still works
    in an environment that has the warehouse SDK but not dbt — a CI job that
    only refreshes source documentation, say.
    """
    try:
        return _load_profile_with_dbt(project)
    except Exception:  # noqa: BLE001 - any dbt failure falls back to reading the file
        return _load_profile_from_yaml(project)


def _load_profile_with_dbt(project: Project) -> dict[str, Any]:
    from dbt.config.profile import read_profile  # type: ignore
    from dbt.config.renderer import ProfileRenderer  # type: ignore

    raw = read_profile(project.profiles_dir)
    entry = raw[project.profile]
    target = project.target or entry.get("target")
    rendered = ProfileRenderer({}).render_data(entry["outputs"][target])
    return dict(rendered)


def _load_profile_from_yaml(project: Project) -> dict[str, Any]:
    import yaml

    path = os.path.join(project.profiles_dir, "profiles.yml")
    with open(path, encoding="utf-8") as fh:
        raw = yaml.safe_load(fh) or {}

    entry = raw.get(project.profile)
    if entry is None:
        raise SystemExit(
            f"profiles.yml has no profile {project.profile!r} "
            f"(looked in {path}; dbt_project.yml names it)"
        )
    target = project.target or entry.get("target")
    outputs = entry.get("outputs") or {}
    if target not in outputs:
        raise SystemExit(
            f"profile {project.profile!r} has no target {target!r} in {path}"
        )
    return _render_env_vars(outputs[target])


def _render_env_vars(value: Any) -> Any:
    """Resolve the one Jinja call that appears in almost every profiles.yml.

    `{{ env_var('NAME') }}` and its two-argument form are what a profile uses to
    keep a secret out of the file. Full Jinja is dbt's job and is used when dbt
    is importable; this covers the case that matters without pulling in a
    template engine to read one string.
    """
    import re

    pattern = re.compile(
        r"\{\{\s*env_var\(\s*['\"]([^'\"]+)['\"]\s*(?:,\s*['\"]([^'\"]*)['\"]\s*)?\)\s*\}\}"
    )

    def render(v: Any) -> Any:
        if isinstance(v, dict):
            return {k: render(x) for k, x in v.items()}
        if isinstance(v, list):
            return [render(x) for x in v]
        if not isinstance(v, str):
            return v

        def sub(m: re.Match[str]) -> str:
            name, default = m.group(1), m.group(2)
            got = os.environ.get(name)
            if got is None:
                if default is None:
                    raise SystemExit(
                        f"profiles.yml needs the environment variable {name}, which is not set"
                    )
                return default
            return got

        return pattern.sub(sub, v)

    return render(value)


def fail(message: str) -> None:
    """Report a fatal problem the way dbt-ditto surfaces it.

    A non-zero exit with the reason on stderr becomes one warning naming this
    provider, and the run carries on documenting everything else — unless
    `sources.strict` is set, which is the project saying it would rather stop.
    """
    print(message, file=sys.stderr)
    raise SystemExit(1)
