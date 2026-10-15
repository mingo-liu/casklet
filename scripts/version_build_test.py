"""Verify Make's version precedence without compiling or modifying the checkout."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

MAKEFILE = Path(__file__).resolve().parent.parent / "Makefile"


class VersionBuildTests(unittest.TestCase):
    def test_source_tag_release_and_explicit_version_precedence(self):
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                subprocess.run(["git", *args], cwd=directory, check=True, capture_output=True)

            git("init", "-q", "--initial-branch=main")
            git("config", "user.name", "Version Test")
            git("config", "user.email", "version@example.invalid")
            Path(directory, "source").write_text("source\n")
            git("add", "source")
            git("commit", "-qm", "test: source version")

            def version(extra=None, arguments=()):
                env = os.environ.copy()
                env.pop("VERSION", None)
                env.pop("CASKLET_RELEASE_VERSION", None)
                env.update(extra or {})
                return subprocess.run(
                    ["make", "-s", "-f", str(MAKEFILE), "-f", "-", "show-version", *arguments],
                    cwd=directory, env=env, input="show-version:\n\t@printenv VERSION\n",
                    check=True, capture_output=True, text=True, timeout=30).stdout.strip()

            self.assertEqual(version(), "dev")
            git("tag", "v7.8.9")
            self.assertEqual(version(), "v7.8.9")
            selected = {"VERSION": "inherited-host-version", "CASKLET_RELEASE_VERSION": "v7.8.8"}
            self.assertEqual(version(selected), "v7.8.8")
            self.assertEqual(version(selected, ("VERSION=v0.0.0-ci",)), "v0.0.0-ci")


if __name__ == "__main__":
    unittest.main()
