# PyPI packaging — `ferrogw`

Makes `uvx ferrogw` and `pipx run ferrogw` run the gateway with nothing
pre-installed. The published distribution is a **binary carrier**: it contains
no importable Python at all.

> `ferrogw` (this) is the gateway. `ferrolabsai` is the client SDK and a
> different package entirely — nothing here touches it.

## How it works

The binary ships as a wheel *script*, so it lands in the wheel's
`ferrogw-<version>.data/scripts/` directory. pip installs that directory
straight into the environment's `bin/` (`Scripts\` on Windows), which means the
installed `ferrogw` **is** the Go binary:

```
$ pip install ferrogw
$ readlink -f "$(command -v ferrogw)"
.../venv/bin/ferrogw          # 42 MB of static Go, not a launcher script
```

No console entry point, no Python shim module, no interpreter start-up, no
import — `uvx ferrogw` costs one exec.

Two properties make that work, and both are set explicitly in `setup.py`:

| | |
|---|---|
| `root_is_pure = False` | makes the wheel platform-tagged at all. Without it pip installs the aarch64 binary on x86_64 without complaint |
| `get_tag() -> py3, none, <plat>` | strips the interpreter/ABI tags that impurity normally implies. There is no extension module here, so a `cp313-cp313` stamp would only make the wheel disappear on the next CPython — and break whichever interpreter uv happened to pick |

### Platform tags

One archive can produce more than one wheel. The binary is `CGO_ENABLED=0` and
links no libc, so claiming glibc *and* musl is a statement of fact rather than
optimism — Alpine genuinely runs the same bytes, and without the musllinux tag
pip on Alpine reports the package as unavailable.

| Release archive | Wheels |
|---|---|
| `linux_amd64.tar.gz` | `manylinux2014_x86_64`, `musllinux_1_1_x86_64` |
| `linux_arm64.tar.gz` | `manylinux2014_aarch64`, `musllinux_1_1_aarch64` |
| `darwin_amd64.tar.gz` | `macosx_10_9_x86_64` |
| `darwin_arm64.tar.gz` | `macosx_11_0_arm64` |
| `windows_amd64.zip` | `win_amd64` |
| `windows_arm64.zip` | `win_arm64` |

The wheel set is derived from the archives that are present, not declared, so
this table is the mapping rather than the promise: a release produces one wheel
per row whose archive it actually built. A release containing Windows arm64
produces all eight wheels; `v1.4.3` and earlier produce seven.

An archive for a platform `WHEEL_TAGS` has no entry for is a hard error rather
than a skip: a newly built target must not go missing from PyPI quietly.

### No sdist

Deliberate. There is nothing to build from source — the payload is a cross-compiled Go
binary — the platform matrix covers every target the release produces, and an
sdist whose only job is to raise an error is code for a path that does not
occur. `build_wheels.py` asserts each build emitted exactly one wheel and
nothing else.

## Layout

```
packaging/pypi/
├── pyproject.toml    static metadata; version is dynamic
├── setup.py          one wheel's worth of build: platform tag + binary copy
├── build_wheels.py   release archives -> every wheel, then verifies each one
└── README.md         this file
```

`setup.py` is invoked once per wheel tag by `build_wheels.py` and reads its two
inputs — `FERROGW_WHEEL_BINARY`, `FERROGW_WHEEL_VERSION` — from the
environment. Running it directly is not supported.

## How a release publishes

`.github/workflows/publish-pypi.yml`, after the GitHub release assets exist:

1. `gh release download` fetches the archives the release already published.
   Not `dist/artifacts.json` — that belongs to the release job and is gone by
   then, and these are the exact bytes users download, so a wheel built from
   them cannot disagree with the tarball.
2. `build_wheels.py` reads the version out of the **archive filenames**
   (`ferrogw_1.4.2_linux_amd64.tar.gz` → `1.4.2` — GoReleaser writes no `v`
   prefix), cross-checks it against the release tag, and builds every wheel.
3. Each wheel is verified before it leaves the job (see below).
4. `pypa/gh-action-pypi-publish` uploads over OIDC.

There is no hand-maintained version anywhere in this directory.

The release workflow dispatches this workflow after GoReleaser succeeds and
waits for it to finish. GitHub suppresses most follow-on events created with the
repository `GITHUB_TOKEN`, but explicitly allows `workflow_dispatch`. A separate
run is required here because PyPI does not accept a reusable workflow as a
Trusted Publisher identity. Both the automatic and manual paths check out the
requested tag before running versioned build code.

**Prerelease tags are normalised, not rejected.** GoReleaser runs
`prerelease: auto`, and `1.5.0-rc.1` is not a valid PEP 440 version — the
canonical spelling is `1.5.0rc1`. `build_wheels.py` converts it and prints the
change. A version with no PEP 440 spelling at all (a `-next` snapshot, say)
stops the build rather than shipping something that sorts where nobody
intended. Prereleases *are* published; `pip install ferrogw` ignores them
unless asked, which is the behaviour a prerelease should have.

### What each wheel is checked for, every build

These are all silent failures otherwise — the wheel installs and the problem
surfaces on a user's machine:

- filename is exactly `ferrogw-<version>-py3-none-<plat>.whl`
- `WHEEL` says `Root-Is-Purelib: false` and carries the expected tag
- `ferrogw-<version>.data/scripts/ferrogw` (`.exe` on Windows) exists **with
  its executable bit** — zip records no POSIX mode, so the bit has to be put
  back on extraction and survive all the way into the wheel
- nothing outside `.data/` and `.dist-info/`, i.e. no importable payload

## One-time PyPI setup (maintainer)

Publishing is over **Trusted Publishing (OIDC)** — no API token in repository
secrets, matching the keyless cosign posture the release already has. `ferrogw`
is unclaimed on PyPI, so this is registered as a *pending* publisher before the
first upload; PyPI creates the project on that upload.

1. **GitHub** → repository *Settings → Environments* → create an environment
   named exactly **`pypi`**. No secrets and no variables go in it. Add required
   reviewers only if you want releases to pause for a human before upload.
2. **PyPI** → *Your projects → Publishing → Add a new pending publisher*
   (GitHub), with exactly:

   | Field | Value |
   |---|---|
   | PyPI Project Name | `ferrogw` |
   | Owner | `ferro-labs` |
   | Repository name | `ai-gateway` |
   | Workflow name | `publish-pypi.yml` |
   | Environment name | `pypi` |

   All five are matched, the workflow *filename* included — renaming
   `publish-pypi.yml` breaks publishing until the publisher is updated.

3. Nothing else. Do not create a PyPI API token; there is nowhere to put one.

Uploads also carry PEP 740 attestations, which
`pypa/gh-action-pypi-publish` produces by default from the same OIDC identity.

## Building locally

Reproduces a release exactly, without publishing:

```sh
python3 -m pip install "setuptools==84.0.0" "packaging==26.3"
gh release download v1.4.2 --repo ferro-labs/ai-gateway --dir artifacts \
  --pattern 'ferrogw_*.tar.gz' --pattern 'ferrogw_*.zip'
python3 packaging/pypi/build_wheels.py --archive-dir artifacts --dist-dir wheelhouse

python3 -m venv /tmp/gw && /tmp/gw/bin/pip install \
  wheelhouse/ferrogw-1.4.2-py3-none-manylinux2014_x86_64.whl
/tmp/gw/bin/ferrogw version
```

Both pins are exact for the same reason `release.yml` pins GoReleaser: they
decide what the artifact *is*. setuptools in particular stamps the platform tag
and owns `setup.py bdist_wheel`, an entry point it has been deprecating for
years and for which no PEP 517 equivalent can express a per-wheel `--plat-name`.
Bump them by hand, in the workflow and here together.
