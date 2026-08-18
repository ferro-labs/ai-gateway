"""Builds one `ferrogw` wheel: a carrier for the Go binary, not a Python package.

The binary is declared as a `script`, which is what puts it in the wheel's
`ferrogw-<version>.data/scripts/` directory. pip installs that directory
straight into the environment's `bin/` (`Scripts\\` on Windows), so the
installed `ferrogw` *is* the gateway — there is no console entry point, no shim
module and no interpreter start-up between `uvx ferrogw` and the first request.

Both inputs come from the environment because there is nothing to hand-maintain:
`build_wheels.py` reads them out of the GoReleaser release archives and invokes
this file once per wheel tag. Running it directly is not supported.
"""

import os

from setuptools import setup
from setuptools.command.bdist_wheel import bdist_wheel

# setuptools does not re-export `build_scripts`, so it comes from distutils —
# which on Python 3.12+ is setuptools' own vendored copy, shimmed into place
# when setuptools is imported. Import it by this name and *after* setuptools,
# never as `setuptools._distutils.command...`: the private path loads distutils
# a second time under a second identity, which silently undoes setuptools'
# monkey-patch of `distutils.core.Distribution` and makes setup() fail inside
# its own `_install_setup_requires`.
from distutils.command.build_scripts import build_scripts

BINARY = os.environ["FERROGW_WHEEL_BINARY"]
VERSION = os.environ["FERROGW_WHEEL_VERSION"]


class CopyBinaryVerbatim(build_scripts):
    """Copy the script through untouched, because it is an ELF, not a program text.

    The stock command opens every script with `tokenize.open()` so it can
    rewrite a `#!` line onto the building interpreter. That decodes the file as
    UTF-8, which a cross-compiled binary is not, and the build dies with
    `SyntaxError: invalid or missing encoding declaration` several frames deep
    in distutils — an error message with nothing to do with what went wrong.

    Copying verbatim is also the only correct behaviour here: a shebang
    prepended to a Mach-O or PE image would corrupt it.
    """

    def copy_scripts(self):
        self.mkpath(self.build_dir)
        for script in self.scripts:
            target = os.path.join(self.build_dir, os.path.basename(script))
            self.copy_file(script, target)
            # Set here rather than left to `install_scripts`, which only ORs in
            # the execute bits on POSIX. The bit has to reach the wheel whatever
            # the build host is, or pip installs a file nobody can run.
            os.chmod(target, 0o755)


class BinaryCarrierWheel(bdist_wheel):
    """Platform-specific, interpreter-agnostic: `py3-none-<platform>`.

    The two halves are set separately because they mean different things.
    `root_is_pure = False` is what makes the wheel platform-tagged at all;
    without it the archive claims to run anywhere and pip would install the
    aarch64 binary on x86_64 without complaint.

    `get_tag` then undoes the interpreter and ABI tags that impurity normally
    implies. Those exist for extension modules linked against a particular
    CPython; this wheel contains no Python whatsoever, so a `cp313-cp313` stamp
    would only make it unavailable on the next CPython release — and would make
    `uvx ferrogw` fail on the interpreter uv happened to pick.
    """

    def finalize_options(self):
        super().finalize_options()
        self.root_is_pure = False

    def get_tag(self):
        return "py3", "none", self.plat_name


setup(
    version=VERSION,
    scripts=[BINARY],
    # There is no importable Python in this distribution. Saying so explicitly
    # keeps setuptools' auto-discovery from adopting whatever else happens to
    # sit in the per-target build directory.
    packages=[],
    py_modules=[],
    cmdclass={
        "bdist_wheel": BinaryCarrierWheel,
        "build_scripts": CopyBinaryVerbatim,
    },
)
