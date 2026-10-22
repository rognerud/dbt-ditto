#!/usr/bin/env python3
"""Build PyPI wheels that carry the dbt-ditto binary."""

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
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

DISTRIBUTION = "dbt-ditto"
# PEP 427 escapes the distribution name wherever it appears in the archive: runs of
# anything but letters, digits and `.` become a single underscore.
ESCAPED = re.sub(r"[^\w\d.]+", "_", DISTRIBUTION)
SUMMARY = "dbt documentation inheritance across projects: dbt-osmosis' YAML management, extended across dbt-loom project boundaries, in one static binary."
HOMEPAGE = "https://github.com/rognerud/dbt-ditto"
LICENSE_EXPRESSION = "MIT"
# PEP 639 puts licence files under dist-info/licenses/, keyed by this name.
LICENSE_FILENAME = "LICENSE"

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
# The macOS minimum is the Go toolchain's, not the architecture's. Go 1.23 and
# later require macOS 11, so an older tag would claim machines the binary cannot
# exec on — and "no matching distribution" is a better error than a SIGKILL from
# the kernel's loader.
TARGETS: tuple[Target, ...] = (
    Target("darwin", "arm64", ("macosx_11_0_arm64",)),
    Target("darwin", "amd64", ("macosx_11_0_x86_64",)),
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

# 3 is Unix, which is what makes the mode bits in external_attr meaningful.
CREATE_SYSTEM_UNIX = 3
COMPRESS_LEVEL = 9


def normalise_version(raw: str) -> str:
    """Turn a git description into something PEP 440 accepts."""
    version = raw.strip()
    version = version.removeprefix("v")
    version = version.removesuffix("-dirty")

    match = re.fullmatch(
        r"(?P<base>.+?)-(?P<distance>\d+)-g(?P<commit>[0-9a-f]+)", version
    )
    if match:
        version = "{base}.post{distance}+g{commit}".format(**match.groupdict())

    if not re.fullmatch(
        r"[0-9]+(\.[0-9]+)*((a|b|rc)[0-9]+)?(\.post[0-9]+)?(\.dev[0-9]+)?(\+[a-zA-Z0-9.]+)?",
        version,
    ):
        raise SystemExit(
            f"version {raw!r} is not a valid PEP 440 version (normalised to {version!r}).\n"
            f"Tag the release as e.g. v0.1.0, or pass --version explicitly."
        )
    return version


def git(*args: str, default: str = "") -> str:
    """Ask git about the repository, falling back when it cannot answer."""
    try:
        return subprocess.run(
            ["git", "-C", str(ROOT), *args],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return default


def git_version() -> str:
    described = git("describe", "--tags", "--always", "--dirty")
    if not described or not re.match(r"^v?[0-9]", described):
        # No tags yet: a development version that PyPI would accept but that
        # sorts below any real release.
        return "0.0.0.dev0"
    return normalise_version(described)


def git_commit() -> str:
    return git("rev-parse", "-q", "--verify", "HEAD", default="unknown")


def build_date() -> str:
    """The date stamped into the binary, chosen so a rebuild agrees with it.

    SOURCE_DATE_EPOCH first, as the reproducible-builds convention asks. Failing
    that the commit's own time, which is derivable from the repository and so
    still reproducible — unlike the wall clock, which would make every rebuild of
    a release differ from the published one.
    """
    epoch = os.environ.get("SOURCE_DATE_EPOCH") or git("show", "-s", "--format=%ct")
    if not epoch.isdigit():
        return ""
    return datetime.fromtimestamp(int(epoch), timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )


def build_binary(
    target: Target, version: str, commit: str, date: str, out_dir: Path
) -> Path:
    """Cross-compile one static binary."""
    out = out_dir / target.binary_name
    env = {
        **os.environ,
        "CGO_ENABLED": "0",
        "GOOS": target.goos,
        "GOARCH": target.goarch,
        # No default: the repository resolves dependencies from go.sum against the
        # module cache and has no vendor/ tree, so forcing -mod=vendor here would
        # fail every build.
        "GOFLAGS": os.environ.get("GOFLAGS", ""),
        "GOCACHE": os.environ.get("GOCACHE", str(ROOT / ".gocache" / "go-build")),
        "GOMODCACHE": os.environ.get("GOMODCACHE", str(ROOT / ".gocache" / "mod")),
        **target.extra_env,
    }
    # The same three stamps scripts/dist.sh uses, so the binary in a wheel and the
    # binary in the release archive for one target are byte-identical and report
    # the same thing.
    ldflags = (
        f"-s -w -X main.version={version} -X main.commit={commit} -X main.date={date}"
    )
    subprocess.run(
        [
            "go",
            "build",
            "-trimpath",
            # Without this the binary carries vcs.revision and vcs.modified, so a
            # wheel built from a dirty tree differs from one built from a clean
            # checkout of the same commit, and one built from a source tarball
            # differs from both. `go build` also *fails* outright when it finds a
            # .git it cannot read, which is the usual state inside a container.
            # The version and commit are injected below, so nothing is lost.
            "-buildvcs=false",
            "-ldflags",
            ldflags,
            "-o",
            str(out),
            "./cmd/dbt-ditto",
        ],
        cwd=ROOT,
        env=env,
        check=True,
    )
    return out


# The machine an executable declares in its own header, per Go target. A wheel
# whose tag and payload disagree installs cleanly and then fails to exec, which
# is the one packaging mistake no installer can catch for us.
#
# Mach-O cputype, ELF e_machine and PE Machine respectively.
MACHO_CPUTYPE = {"amd64": 0x01000007, "arm64": 0x0100000C}
ELF_MACHINE = {"amd64": 0x3E, "arm64": 0xB7}
PE_MACHINE = {"amd64": 0x8664, "arm64": 0xAA64}


def payload_machine(payload: bytes) -> tuple[str, int]:
    """Read the executable format and machine out of a binary's own header."""
    if payload[:4] == b"\xcf\xfa\xed\xfe":  # 64-bit Mach-O, little endian
        return "macho", int.from_bytes(payload[4:8], "little")
    if payload[:4] == b"\x7fELF":
        # e_machine is at 0x12, and every target here is little endian.
        return "elf", int.from_bytes(payload[0x12:0x14], "little")
    if payload[:2] == b"MZ":
        pe = int.from_bytes(payload[0x3C:0x40], "little")
        if payload[pe : pe + 4] != b"PE\0\0":
            raise SystemExit("payload claims to be a PE file but has no PE header")
        return "pe", int.from_bytes(payload[pe + 4 : pe + 6], "little")
    raise SystemExit(f"payload is not a known executable format: {payload[:4]!r}")


def check_payload(target: Target, payload: bytes) -> None:
    """Fail the build when the binary does not match the wheel's platform tag."""
    want = {
        "darwin": ("macho", MACHO_CPUTYPE),
        "linux": ("elf", ELF_MACHINE),
        "windows": ("pe", PE_MACHINE),
    }[target.goos]
    want_format, machines = want
    got_format, got_machine = payload_machine(payload)

    if got_format != want_format:
        raise SystemExit(
            f"{target.go_target}: built a {got_format} binary, "
            f"but {target.platform_tag} needs {want_format}"
        )
    if got_machine != machines[target.goarch]:
        raise SystemExit(
            f"{target.go_target}: binary declares machine {got_machine:#x}, "
            f"but {target.platform_tag} needs {machines[target.goarch]:#x}"
        )


def metadata(version: str) -> str:
    readme = (ROOT / "README.md").read_text(encoding="utf-8")
    lines = [
        # 2.4 is the version that defines License-Expression and License-File.
        # Declaring 2.1 while shipping dist-info/licenses/ was a contradiction:
        # under 2.1 there is no field that can name a licence file, so the LICENSE
        # was an unlisted extra that no tool read as the licence.
        "Metadata-Version: 2.4",
        f"Name: {DISTRIBUTION}",
        f"Version: {version}",
        f"Summary: {SUMMARY}",
        # Home-page is the deprecated spelling of this.
        f"Project-URL: Homepage, {HOMEPAGE}",
        f"Project-URL: Source, {HOMEPAGE}",
        f"Project-URL: Issues, {HOMEPAGE}/issues",
        f"License-Expression: {LICENSE_EXPRESSION}",
        f"License-File: {LICENSE_FILENAME}",
        # The binary is self-contained, so any Python that can run an installer
        # is new enough.
        "Requires-Python: >=3.8",
        "Description-Content-Type: text/markdown",
        "Keywords: dbt,documentation,yaml,dbt-osmosis,dbt-loom,data-catalog",
        "Classifier: Development Status :: 4 - Beta",
        "Classifier: Intended Audience :: Developers",
        # No `License :: OSI Approved ::` classifier: PEP 639 forbids pairing one
        # with License-Expression, since the two could disagree.
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


def build_wheel(
    target: Target, version: str, commit: str, date: str, out_dir: Path, work: Path
) -> Path:
    binary = build_binary(target, version, commit, date, work)
    payload = binary.read_bytes()
    check_payload(target, payload)

    dist_info = f"{ESCAPED}-{version}.dist-info"
    data_scripts = f"{ESCAPED}-{version}.data/scripts"

    # (archive path, bytes, mode). The binary is the only executable.
    entries: list[tuple[str, bytes, int]] = [
        (f"{data_scripts}/{target.binary_name}", payload, MODE_EXECUTABLE),
        (f"{dist_info}/METADATA", metadata(version).encode("utf-8"), MODE_REGULAR),
        (f"{dist_info}/WHEEL", wheel_metadata(target).encode("utf-8"), MODE_REGULAR),
    ]
    # Not optional: METADATA names the file in License-File, so a wheel without
    # it would be inconsistent with its own metadata rather than merely sparse.
    licence = ROOT / LICENSE_FILENAME
    entries.append(
        (
            f"{dist_info}/licenses/{LICENSE_FILENAME}",
            licence.read_bytes(),
            MODE_REGULAR,
        )
    )

    # RECORD lists every file with its hash and size, and itself with neither.
    record = io.StringIO()
    writer = csv.writer(record, lineterminator="\n")
    for name, data, _ in entries:
        writer.writerow([name, record_hash(data), len(data)])
    writer.writerow([f"{dist_info}/RECORD", "", ""])
    entries.append(
        (f"{dist_info}/RECORD", record.getvalue().encode("utf-8"), MODE_REGULAR)
    )

    filename = f"{ESCAPED}-{version}-{PYTHON_TAG}-{ABI_TAG}-{target.platform_tag}.whl"
    path = out_dir / filename
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, data, mode in entries:
            info = zipfile.ZipInfo(name, date_time=ZIP_TIMESTAMP)
            # The whole Unix st_mode, file-type bits included, goes in the top half of
            # external_attr.
            info.external_attr = (mode | stat.S_IFREG) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            # Pinned, both of them: ZipInfo defaults create_system to 0 on a
            # Windows host and 3 everywhere else, and the default compression
            # level is zlib's rather than a stated one. Either would make the
            # same commit produce different bytes on a different build machine.
            info.create_system = CREATE_SYSTEM_UNIX
            zf.writestr(info, data, compresslevel=COMPRESS_LEVEL)
    return path


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--version", help="release version (default: from git describe)"
    )
    parser.add_argument(
        "--out", default=str(ROOT / "dist" / "pypi"), help="output directory"
    )
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
    date = build_date()

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

    print(f"{DISTRIBUTION} {version} ({commit[:12]}, {date or 'no date'})")
    with tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR")) as tmp:
        for target in targets:
            path = build_wheel(target, version, commit, date, out_dir, Path(tmp))
            size = path.stat().st_size / 1024
            print(f"  {target.go_target:<16} {target.platform_tag:<50} {size:7.0f} KiB")

    print(f"\nwheels in {out_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
