#!/usr/bin/env python3
"""Turn a published GoReleaser release into one wheel per platform.

The input is the **published release archives** — whatever `gh release
download` left in `--archive-dir` — rather than `dist/artifacts.json`. The
release workflow dispatches this builder only after GoReleaser finishes, by
which time the release job's `dist/` no longer exists; and the archives are the
bytes users already download, so a wheel built from them cannot disagree with
the tarball.

Nothing here is version-pinned. The version comes out of the archive filenames,
and the wheel set is derived from the archives that are present rather than
declared in this file — so the windows/arm64 wheel starts shipping by itself
the first release after `.goreleaser.yaml` drops its `ignore:` for that pair.
An archive for a platform this file has no wheel tag for is a hard
error instead: a newly built target must not disappear from PyPI quietly.

Usage:

    python3 build_wheels.py --archive-dir dist --dist-dir wheelhouse [--tag v1.4.2]
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from pathlib import Path
from typing import NoReturn

from packaging.version import InvalidVersion, Version

HERE = Path(__file__).resolve().parent

# Go's own names for a platform are not the wheel tags for it, and the two
# vocabularies disagree on both halves — amd64/x86_64, arm64/aarch64 — so the
# mapping is spelled out rather than derived.
WHEEL_TAGS: dict[tuple[str, str], tuple[str, ...]] = {
    # The binary is CGO_ENABLED=0, so it links no libc at all. Claiming glibc
    # *and* musl is therefore a statement of fact rather than optimism: Alpine
    # genuinely runs the same bytes, and without the musllinux tag pip on
    # Alpine finds no wheel and reports the package as unavailable.
    ("linux", "amd64"): ("manylinux2014_x86_64", "musllinux_1_1_x86_64"),
    ("linux", "arm64"): ("manylinux2014_aarch64", "musllinux_1_1_aarch64"),
    # The macOS floors are the oldest release each architecture can exist on —
    # Apple silicon starts at Big Sur (11.0), and 10.9 is Go's own amd64
    # minimum. A higher floor would only exclude machines the binary runs on.
    ("darwin", "amd64"): ("macosx_10_9_x86_64",),
    ("darwin", "arm64"): ("macosx_11_0_arm64",),
    ("windows", "amd64"): ("win_amd64",),
    ("windows", "arm64"): ("win_arm64",),
}

# ferrogw_1.4.2_linux_amd64.tar.gz — no `v` on the version, `.zip` on Windows.
# The os/arch groups exclude `_`, so the greedy version group cannot swallow
# them. `.sbom.json`, `checksums.txt` and the cosign material do not match.
ARCHIVE_RE = re.compile(
    r"^ferrogw_(?P<version>.+)_(?P<goos>[a-z0-9]+)_(?P<goarch>[a-z0-9]+)"
    r"\.(?P<ext>tar\.gz|zip)$"
)


def fail(message: str) -> NoReturn:
    raise SystemExit(f"build_wheels: {message}")


def pep440(go_version: str) -> str:
    """Normalise GoReleaser's version to one PyPI will accept.

    `v1.4.2` reaches us as `1.4.2` and needs nothing. A prerelease tag does:
    GoReleaser's `prerelease: auto` happily builds `v1.5.0-rc.1`, and
    `1.5.0-rc.1` is not a PEP 440 version — the canonical spelling is
    `1.5.0rc1`. Anything that cannot be expressed at all (a `-next` snapshot,
    say) stops the build here, because the alternative is a wheel whose version
    silently sorts somewhere nobody intended.
    """
    try:
        return str(Version(go_version))
    except InvalidVersion as exc:
        fail(
            f"release version {go_version!r} has no PEP 440 spelling, so no "
            f"wheel can carry it ({exc}). Tag with a version PyPI accepts."
        )


def discover(archive_dir: Path, allow_partial: bool = False) -> list[dict]:
    found = []
    for path in sorted(archive_dir.iterdir()):
        match = ARCHIVE_RE.match(path.name)
        if not match:
            continue
        goos, goarch = match["goos"], match["goarch"]
        if (goos, goarch) not in WHEEL_TAGS:
            fail(
                f"{path.name} builds for {goos}/{goarch}, which has no wheel "
                f"tag in WHEEL_TAGS. Add one — a platform the release ships "
                f"must not be missing from PyPI."
            )
        found.append(
            {
                "path": path,
                "version": match["version"],
                "goos": goos,
                "goarch": goarch,
                "binary": "ferrogw.exe" if goos == "windows" else "ferrogw",
                "tags": WHEEL_TAGS[(goos, goarch)],
            }
        )

    if not found:
        fail(f"no ferrogw release archives under {archive_dir}")

    # Every platform WHEEL_TAGS knows, or nothing. release.yml gates the normal
    # path behind its "Verify release asset set" job, but the workflow_dispatch
    # recovery path documented for a partial-publish failure goes straight to
    # this script -- so tolerating a subset here meant the one path taken when
    # something has already gone wrong was the one with no gate, and it would
    # publish a version silently missing platforms.
    #
    # --allow-partial exists for a deliberate reissue of an older tag that
    # genuinely shipped fewer archives; it is never correct on a current release.
    missing = sorted(
        f"{goos}/{goarch}"
        for (goos, goarch) in WHEEL_TAGS
        if not any(a["goos"] == goos and a["goarch"] == goarch for a in found)
    )
    if missing and not allow_partial:
        fail(
            f"{archive_dir} is missing archives for {', '.join(missing)}. "
            f"Publishing this set would leave the release without those "
            f"platforms. Pass --allow-partial only to reissue an older tag that "
            f"really shipped fewer."
        )
    if missing:
        print(f"WARNING: --allow-partial, building without {', '.join(missing)}")

    versions = {a["version"] for a in found}
    if len(versions) > 1:
        fail(f"archives carry more than one version: {sorted(versions)}")
    return found


def extract_member(archive: Path, member: str, dest_dir: Path, mode: int) -> Path:
    """Pull one root member out of the archive with an explicit mode.

    Neither container is trusted to carry the mode across: zip does not record
    a POSIX one at all, and for the binary the executable bit has to survive
    all the way into the wheel or pip installs a file nobody can run. The
    caller states the mode rather than the archive implying it, because the two
    members this is used for want different ones.
    """
    out = dest_dir / member
    try:
        if archive.name.endswith(".zip"):
            with zipfile.ZipFile(archive) as zf, zf.open(member) as src:
                with out.open("wb") as dst:
                    shutil.copyfileobj(src, dst)
        else:
            with tarfile.open(archive, "r:gz") as tf:
                src = tf.extractfile(member)
                if src is None:
                    fail(f"{member} in {archive.name} is not a regular file")
                with src, out.open("wb") as dst:
                    shutil.copyfileobj(src, dst)
    except KeyError:
        fail(f"{archive.name} has no {member} at its root")

    out.chmod(mode)
    return out


def build_wheel(
    binary: Path, license_file: Path, version: str, plat_tag: str, dist_dir: Path
) -> Path:
    """Run `setup.py bdist_wheel` for one platform tag, in a throwaway tree.

    Each platform builds in its own copy of setup.py/pyproject.toml so that a
    `build/`, `dist/` or `.egg-info` left by one target can never be picked up
    by the next — the failure that produces an aarch64 wheel containing an
    x86_64 binary, which nothing downstream would catch.
    """
    with tempfile.TemporaryDirectory(prefix=f"ferrogw-{plat_tag}-") as tmp:
        work = Path(tmp)
        for name in ("setup.py", "pyproject.toml"):
            shutil.copy2(HERE / name, work / name)

        # Apache-2.0 section 4(a) requires the licence to travel with the
        # distribution, and a wheel is a distribution. pyproject declares
        # license-files = ["LICENSE"], which setuptools resolves relative to
        # this build tree, so the file has to be here before setup.py runs --
        # otherwise the build fails rather than quietly shipping without it.
        # The npm packages carry it for the same reason.
        shutil.copy2(license_file, work / "LICENSE")

        subprocess.run(
            [sys.executable, "setup.py", "--quiet", "bdist_wheel",
             "--plat-name", plat_tag],
            cwd=work,
            check=True,
            env={
                **os.environ,
                "FERROGW_WHEEL_BINARY": str(binary),
                "FERROGW_WHEEL_VERSION": version,
            },
        )

        produced = sorted(p.name for p in (work / "dist").iterdir())
        # No sdist: there is nothing to build from source, and an sdist that
        # only raises is code for a path that does not occur. This
        # invocation cannot emit one — assert it rather than assume it.
        expected = f"ferrogw-{version}-py3-none-{plat_tag}.whl"
        if produced != [expected]:
            fail(f"expected exactly [{expected}], got {produced}")

        dist_dir.mkdir(parents=True, exist_ok=True)
        return Path(shutil.move(str(work / "dist" / expected), str(dist_dir)))


def verify_wheel(wheel: Path, version: str, plat_tag: str, member: str) -> None:
    """Assert the four things that make this wheel work, from the outside.

    All four are silent failures otherwise: a wheel with a cpython ABI tag
    installs on today's interpreter and vanishes on the next; a wheel whose
    script lost its executable bit installs and then cannot be run; a purelib
    wheel installs the wrong architecture without a word.
    """
    script = f"ferrogw-{version}.data/scripts/{member}"
    dist_info = f"ferrogw-{version}.dist-info/"

    with zipfile.ZipFile(wheel) as zf:
        try:
            info = zf.getinfo(script)
        except KeyError:
            fail(f"{wheel.name} has no {script} — pip would install nothing on PATH")

        if not (info.external_attr >> 16) & 0o111:
            fail(f"{wheel.name}: {script} lost its executable bit")

        metadata = zf.read(f"{dist_info}WHEEL").decode()
        for line in (f"Tag: py3-none-{plat_tag}", "Root-Is-Purelib: false"):
            if line not in metadata:
                fail(f"{wheel.name}: WHEEL metadata is missing {line!r}")

        stray = [
            n for n in zf.namelist()
            if not n.startswith((f"ferrogw-{version}.data/", dist_info))
        ]
        if stray:
            fail(f"{wheel.name} carries importable payload, which it must not: {stray}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive-dir", type=Path, required=True,
                        help="directory holding the downloaded release archives")
    parser.add_argument("--dist-dir", type=Path, default=Path("wheelhouse"),
                        help="where the built wheels are written")
    parser.add_argument("--allow-partial", action="store_true",
                        help="build even when the release is missing platforms "
                             "WHEEL_TAGS knows; only for reissuing an older tag")
    parser.add_argument("--tag", default=None,
                        help="release tag to cross-check the archives against")
    args = parser.parse_args(argv)

    archives = discover(args.archive_dir, args.allow_partial)
    go_version = archives[0]["version"]

    # Cheap guard against publishing the wrong release: the tag names what the
    # workflow meant to download, the filenames name what it actually got.
    if args.tag and go_version != args.tag.lstrip("v"):
        fail(f"tag {args.tag} does not match archive version {go_version}")

    version = pep440(go_version)
    if version != go_version:
        print(f"version {go_version} normalised to PEP 440 {version}")

    with tempfile.TemporaryDirectory(prefix="ferrogw-binaries-") as staging:
        for archive in archives:
            binary = extract_member(
                archive["path"], archive["binary"], Path(staging), 0o755
            )
            # Taken from the archive rather than the checkout, for the same
            # reason the binary is: these are the bytes the release published,
            # so the wheel cannot end up carrying a licence the tarball does
            # not. Every archive roots one (goreleaser archives.files).
            license_file = extract_member(
                archive["path"], "LICENSE", Path(staging), 0o644
            )
            for plat_tag in archive["tags"]:
                wheel = build_wheel(
                    binary, license_file, version, plat_tag, args.dist_dir
                )
                verify_wheel(wheel, version, plat_tag, archive["binary"])
                print(f"  {wheel.name}  <- {archive['path'].name}")
            binary.unlink()
            license_file.unlink()

    print(f"built {sum(len(a['tags']) for a in archives)} wheels in {args.dist_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
