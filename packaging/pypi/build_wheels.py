#!/usr/bin/env python3
"""Build PyPI wheels that carry the dbt-ditto binary.

Why wheels at all, for a Go program: the people who run dbt-osmosis and dbt-loom
already have a Python environment with those two in it. Shipping wheels turns
adoption into a dependency edit rather than "install a second toolchain".

How it works: a wheel may contain a `<name>-<version>.data/scripts/` directory,
and installers copy anything in it straight into the environment's `bin/` and
mark it executable. So the wheel holds the binary and nothing else -- no Python
shim, no subprocess, no interpreter startup on the hot path. `dbt-ditto` simply
appears on PATH.

One wheel is produced per platform. A wheel is a zip with a prescribed layout,
so this script uses only the standard library: it has to run before anything is
installed, and a build tool that needs its own dependencies resolved first is a
bootstrapping problem nobody needs.

    python3 packaging/pypi/build_wheels.py --version 0.1.0
    python3 packaging/pypi/build_wheels.py --version 0.1.0 --targets darwin/arm64
"""

from __future__ import annotations

import argparse
import base64
import csv
import hashlib
import io
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import zipfile
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

DISTRIBUTION = "dbt-ditto"
# PEP 427 escapes the distribution name wherever it appears in the archive: runs
# of anything but letters, digits and `.` become a single underscore. Installers
# match on the escaped form, so a hyphen here would produce wheels pip refuses.
ESCAPED = re.sub(r"[^\w\d.]+", "_", DISTRIBUTION)
SUMMARY = "dbt documentation inheritance across projects: dbt-osmosis' YAML management, extended across dbt-loom project boundaries, in one static binary."
HOMEPAGE = "https://github.com/rognerud/dbt-ditto"
LICENSE_EXPRESSION = "MIT"

# Wheels are tagged `py3-none-<platform>`: the payload is a native binary, so it
# works on any Python 3 and has no ABI of its own.
PYTHON_TAG = "py3"
ABI_TAG = "none"


@dataclass(frozen=True)
class Target:
    """One published wheel: a Go build target and the platforms it serves."""

    goos: str
    goarch: str
    # Several platform tags in one filename form a "compressed tag set", which
    # installers read as "this wheel satisfies any of these".
    platform_tags: tuple[str, ...]
    extra_env: dict[str, str] = field(default_factory=dict)

    @property
    def go_target(self) -> str:
        return f"{self.goos}/{self.goarch}"

    @property
    def platform_tag(self) -> str:
        return ".".join(self.platform_tags)

    @property
    def binary_name(self) -> str:
        return "dbt-ditto.exe" if self.goos == "windows" else "dbt-ditto"


# A static Go binary has no libc to match, which is why one build serves both
# glibc and musl. They still need separate wheels: the tags are not aliases of
# one another, and pip only installs a wheel whose tag it recognises.
#
# The macOS minimums are the oldest releases each architecture ever shipped on,
# so the tag never excludes a machine the binary would actually run on.
TARGETS: tuple[Target, ...] = (
    Target("darwin", "arm64", ("macosx_11_0_arm64",)),
    Target("darwin", "amd64", ("macosx_10_9_x86_64",)),
    Target("linux", "amd64", ("manylinux_2_17_x86_64", "manylinux2014_x86_64")),
    Target("linux", "amd64", ("musllinux_1_1_x86_64",)),
    Target("linux", "arm64", ("manylinux_2_17_aarch64", "manylinux2014_aarch64")),
    Target("linux", "arm64", ("musllinux_1_1_aarch64",)),
    Target("windows", "amd64", ("win_amd64",)),
    Target("windows", "arm64", ("win_arm64",)),
)

# A fixed timestamp keeps the zip byte-identical between rebuilds of the same
# commit. Zip cannot represent anything before 1980.
ZIP_TIMESTAMP = (1980, 1, 1, 0, 0, 0)

MODE_EXECUTABLE = 0o755
MODE_REGULAR = 0o644


def normalise_version(raw: str) -> str:
    """Turn a git description into something PEP 440 accepts.

    `git describe` produces `v0.1.0`, or `v0.1.0-4-gabc1234` when the tag is not
    the current commit. PyPI rejects both spellings, so they become `0.1.0` and
    `0.1.0.post4+gabc1234`.
    """
    version = raw.strip()
    version = version.removeprefix("v")
    version = version.removesuffix("-dirty")

    match = re.fullmatch(r"(?P<base>.+?)-(?P<distance>\d+)-g(?P<commit>[0-9a-f]+)", version)
    if match:
        version = "{base}.post{distance}+g{commit}".format(**match.groupdict())

    if not re.fullmatch(r"[0-9]+(\.[0-9]+)*((a|b|rc)[0-9]+)?(\.post[0-9]+)?(\.dev[0-9]+)?(\+[a-zA-Z0-9.]+)?", version):
        raise SystemExit(
            f"version {raw!r} is not a valid PEP 440 version (normalised to {version!r}).\n"
            f"Tag the release as e.g. v0.1.0, or pass --version explicitly."
        )
    return version


def git_version() -> str:
    try:
        described = subprocess.run(
            ["git", "-C", str(ROOT), "describe", "--tags", "--always", "--dirty"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        described = ""
    if not described or not re.match(r"^v?[0-9]", described):
        # No tags yet: a development version that PyPI would accept but that
        # sorts below any real release.
        return "0.0.0.dev0"
    return normalise_version(described)


def git_commit() -> str:
    try:
        return subprocess.run(
            ["git", "-C", str(ROOT), "rev-parse", "-q", "--verify", "HEAD"],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return "unknown"


def build_binary(target: Target, version: str, commit: str, out_dir: Path) -> Path:
    """Cross-compile one static binary."""
    out = out_dir / target.binary_name
    env = {
        **os.environ,
        "CGO_ENABLED": "0",
        "GOOS": target.goos,
        "GOARCH": target.goarch,
        "GOFLAGS": os.environ.get("GOFLAGS", "-mod=vendor"),
        "GOCACHE": os.environ.get("GOCACHE", str(ROOT / ".gocache" / "go-build")),
        **target.extra_env,
    }
    ldflags = f"-s -w -X main.version={version} -X main.commit={commit}"
    subprocess.run(
        ["go", "build", "-trimpath", "-ldflags", ldflags, "-o", str(out), "./cmd/dbt-ditto"],
        cwd=ROOT,
        env=env,
        check=True,
    )
    return out


def metadata(version: str) -> str:
    readme = (ROOT / "README.md").read_text(encoding="utf-8")
    lines = [
        "Metadata-Version: 2.1",
        f"Name: {DISTRIBUTION}",
        f"Version: {version}",
        f"Summary: {SUMMARY}",
        f"Home-page: {HOMEPAGE}",
        f"Project-URL: Source, {HOMEPAGE}",
        f"Project-URL: Issues, {HOMEPAGE}/issues",
        f"License: {LICENSE_EXPRESSION}",
        # The binary is self-contained, so any Python that can run an installer
        # is new enough.
        "Requires-Python: >=3.8",
        "Description-Content-Type: text/markdown",
        "Classifier: Development Status :: 4 - Beta",
        "Classifier: Intended Audience :: Developers",
        "Classifier: License :: OSI Approved :: MIT License",
        "Classifier: Programming Language :: Go",
        "Classifier: Topic :: Database",
        "Classifier: Topic :: Software Development :: Documentation",
        "",
        readme,
    ]
    return "\n".join(lines)


def wheel_metadata(target: Target) -> str:
    return "\n".join(
        [
            "Wheel-Version: 1.0",
            f"Generator: {DISTRIBUTION}-build_wheels",
            # The payload is platform specific, so it is not pure Python and
            # must not be unpacked into purelib.
            "Root-Is-Purelib: false",
            f"Tag: {PYTHON_TAG}-{ABI_TAG}-{target.platform_tag}",
            "",
        ]
    )


def record_hash(data: bytes) -> str:
    digest = hashlib.sha256(data).digest()
    return "sha256=" + base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")


def build_wheel(target: Target, version: str, commit: str, out_dir: Path, work: Path) -> Path:
    binary = build_binary(target, version, commit, work)
    payload = binary.read_bytes()

    dist_info = f"{ESCAPED}-{version}.dist-info"
    data_scripts = f"{ESCAPED}-{version}.data/scripts"

    # (archive path, bytes, mode). The binary is the only executable.
    entries: list[tuple[str, bytes, int]] = [
        (f"{data_scripts}/{target.binary_name}", payload, MODE_EXECUTABLE),
        (f"{dist_info}/METADATA", metadata(version).encode("utf-8"), MODE_REGULAR),
        (f"{dist_info}/WHEEL", wheel_metadata(target).encode("utf-8"), MODE_REGULAR),
    ]
    licence = ROOT / "LICENSE"
    if licence.exists():
        entries.append(
            (f"{dist_info}/licenses/LICENSE", licence.read_bytes(), MODE_REGULAR)
        )

    # RECORD lists every file with its hash and size, and itself with neither.
    record = io.StringIO()
    writer = csv.writer(record, lineterminator="\n")
    for name, data, _ in entries:
        writer.writerow([name, record_hash(data), len(data)])
    writer.writerow([f"{dist_info}/RECORD", "", ""])
    entries.append((f"{dist_info}/RECORD", record.getvalue().encode("utf-8"), MODE_REGULAR))

    filename = f"{ESCAPED}-{version}-{PYTHON_TAG}-{ABI_TAG}-{target.platform_tag}.whl"
    path = out_dir / filename
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, data, mode in entries:
            info = zipfile.ZipInfo(name, date_time=ZIP_TIMESTAMP)
            # The whole Unix st_mode, file-type bits included, goes in the top
            # half of external_attr. The regular-file bit is not decoration:
            # pip decides whether to make a script executable with
            # `stat.S_ISREG(external_attr >> 16) and mode & 0o111`, so a mode
            # without S_IFREG installs the binary as rw-r--r-- and every pip
            # user gets "permission denied". uv only checks the execute bits and
            # installs it fine either way, which is a good way not to notice.
            info.external_attr = (mode | stat.S_IFREG) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            zf.writestr(info, data)
    return path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--version", help="release version (default: from git describe)")
    parser.add_argument("--out", default=str(ROOT / "dist" / "pypi"), help="output directory")
    parser.add_argument(
        "--targets",
        nargs="*",
        help="limit to these Go targets, e.g. darwin/arm64 linux/amd64",
    )
    args = parser.parse_args()

    if shutil.which("go") is None:
        print("error: go is not on PATH", file=sys.stderr)
        return 1

    version = normalise_version(args.version) if args.version else git_version()
    commit = git_commit()

    targets = TARGETS
    if args.targets:
        wanted = set(args.targets)
        targets = tuple(t for t in TARGETS if t.go_target in wanted)
        if not targets:
            print(f"error: no known targets matched {sorted(wanted)}", file=sys.stderr)
            print(f"known: {sorted({t.go_target for t in TARGETS})}", file=sys.stderr)
            return 1

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    print(f"{DISTRIBUTION} {version} ({commit[:12]})")
    with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as tmp:
        for target in targets:
            path = build_wheel(target, version, commit, out_dir, Path(tmp))
            size = path.stat().st_size / 1024
            print(f"  {target.go_target:<16} {target.platform_tag:<50} {size:7.0f} KiB")

    print(f"\nwheels in {out_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
