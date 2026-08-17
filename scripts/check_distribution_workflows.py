#!/usr/bin/env python3
"""Keep the release-to-install handoff explicit and independently gated."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def read(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def require(text: str, needle: str, path: str) -> None:
    if needle not in text:
        raise SystemExit(f"{path}: missing {needle!r}")


release_path = ".github/workflows/release.yml"
npm_path = ".github/workflows/publish-npm.yml"
pypi_path = ".github/workflows/publish-pypi.yml"
matrix_path = ".github/workflows/install-matrix.yml"

release = read(release_path)
npm = read(npm_path)
pypi = read(pypi_path)
matrix = read(matrix_path)

for path, workflow in ((npm_path, npm), (pypi_path, pypi)):
    require(workflow, "workflow_dispatch:", path)
    require(workflow, "ref: ${{ inputs.tag }}", path)
    if "\n  release:" in workflow:
        raise SystemExit(f"{path}: release events must flow through release.yml")
require(npm, '--tag "$dist_tag"', npm_path)

require(release, 'gh workflow run "$workflow"', release_path)
for dispatched in ("publish-npm.yml", "publish-pypi.yml"):
    require(release, dispatched, release_path)
require(release, "uses: ./.github/workflows/install-matrix.yml", release_path)
require(release, "Verify release asset set", release_path)
if "\n  release:" in matrix:
    raise SystemExit(f"{matrix_path}: release events must flow through release.yml")

for needle in (
    "workflow_call:",
    "windows-11-arm",
    "INSTALL_MATRIX_NPM_LIVE",
    "INSTALL_MATRIX_PYPI_LIVE",
    "INSTALL_MATRIX_HOMEBREW_LIVE",
    "INSTALL_MATRIX_SCOOP_LIVE",
    "PSScriptAnalyzer",
    "Build npm packages from release archives",
    "Build PyPI wheels from release archives",
    "Smoke-test Linux package",
    "Verify truncated install.sh prefixes are inert",
    "Verify truncated install.ps1 prefixes are inert",
):
    require(matrix, needle, matrix_path)

require(
    matrix,
    "scoop bucket add ferrolabs https://github.com/ferro-labs/homebrew-tap",
    matrix_path,
)
if "scoop-bucket" in matrix:
    raise SystemExit(f"{matrix_path}: stale standalone Scoop repository")

# The checks above are substring matches, which is enough to prove a step still
# exists but says nothing about ORDER — and order is the whole invariant here.
# Every one of them passed on a release.yml with its `needs:` edges deleted,
# which is exactly the "independently gated handoff" this script is named for.
# So the graph is parsed rather than grepped.
import yaml  # noqa: E402  (kept beside the check it serves)

graph = yaml.safe_load(read(release_path))["jobs"]

REQUIRED_EDGES = {
    # publishing waits for the asset set, so a release short an archive cannot
    # reach a registry
    "publish-packages": "verify-release-assets",
    # installs are proven only after the packages exist
    "verify-installation": "publish-packages",
    # the announcement waits for the installs
    "notify-discord": "verify-installation",
    "verify-release-assets": "release",
    "release": "ci",
}

for job, needed in REQUIRED_EDGES.items():
    if job not in graph:
        raise SystemExit(f"{release_path}: job {job!r} is gone")
    needs = graph[job].get("needs") or []
    if isinstance(needs, str):
        needs = [needs]
    if needed not in needs:
        raise SystemExit(
            f"{release_path}: {job!r} must wait on {needed!r} (needs: {needs}). "
            f"Without that edge the step still runs, just not in an order that "
            f"gates anything."
        )

print("distribution workflow invariants are present")
