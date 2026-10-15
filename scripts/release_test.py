"""Release identity and fail-closed publication checks using real local Git refs."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from release_assets import archive_name, checksum

spec = importlib.util.spec_from_file_location("release", Path(__file__).with_name("release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.repo = self.directory / "repo"
        self.repo.mkdir()
        self.git("init", "-q", "--initial-branch=main")
        self.git("config", "user.name", "Release Test")
        self.git("config", "user.email", "release@example.invalid")
        (self.repo / "source").write_text("trusted source\n")
        self.git("add", "source")
        self.git("commit", "-qm", "test: seed main")
        self.git("clone", "-q", "--bare", ".", str(self.directory / "remote"))
        self.git("remote", "add", "origin", str(self.directory / "remote"))
        self.git("tag", "-a", "v0.1.0", "-m", "candidate")
        self.git("push", "-q", "origin", "main", "refs/tags/v0.1.0")
        env = {"GITHUB_REF": "refs/heads/main", "GITHUB_REPOSITORY": "owner/repo",
               "GITHUB_OUTPUT": str(self.directory / "output"),
               "CASKLET_MACOS_E2E_ENABLED": "1", "CHECKS_RESULT": "success", "MACOS_RESULT": "success"}
        self.patch = patch.dict(os.environ, env)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        self.root = patch.object(release, "ROOT", self.repo)
        self.root.start()
        self.addCleanup(self.root.stop)
        self.assets = self.directory / "assets"
        self.assets.mkdir()
        for arch in ("arm64", "amd64"):
            archive = self.assets / archive_name("v0.1.0", arch)
            archive.write_bytes(("archive " + arch).encode())
            archive.with_name(archive.name + ".sha256").write_text(
                checksum(archive))
        original_run = release.run
        self.uploads = []

        def run(*args):
            if args[:3] == ("gh", "release", "upload"):
                self.uploads.append(args)
                return ""
            return original_run(*args)

        patch.object(release, "run", side_effect=run).start()
        self.metadata = {"databaseId": 42, "isDraft": True, "tagName": "v0.1.0"}
        self.github = patch.object(release, "github", side_effect=self.api).start()
        self.addCleanup(patch.stopall)

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.repo, check=True, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.strip()

    def api(self, *args):
        return dict(self.metadata) if args[0] == "release" else {"draft": False}

    def select(self):
        selected = release.candidate("v0.1.0")
        os.environ.update({"RELEASE_REVISION": selected["revision"],
                           "RELEASE_TAG_OID": selected["tag_oid"], "RELEASE_ID": selected["release_id"]})
        self.github.reset_mock()
        return selected

    def assert_not_published(self):
        self.assertEqual(self.uploads, [])
        self.assertFalse(any(call.args[0] == "api" for call in self.github.call_args_list))

    def test_publish_only_the_validated_draft_and_commit(self):
        selected = self.select()
        self.assertEqual(selected["revision"], self.git("rev-parse", "HEAD"))
        release.prepare("v0.1.0")
        self.assertIn("revision=" + selected["revision"], (self.directory / "output").read_text())
        release.publish("v0.1.0", self.assets)
        self.assertEqual(len(self.uploads), 1)
        self.assertEqual(self.uploads[0][3], "v0.1.0")
        for name in ("casklet_v0.1.0_darwin_arm64.tar.gz", "casklet_v0.1.0_darwin_amd64.tar.gz", "SHA256SUMS"):
            self.assertIn(str(self.assets / name), self.uploads[0])
        self.github.assert_called_with("api", "--method", "PATCH", "repos/owner/repo/releases/42", "-F", "draft=false")

    def test_failed_cancelled_missing_or_skipped_checks_never_publish(self):
        self.select()
        for name in ("CHECKS_RESULT", "MACOS_RESULT"):
            for status in ("failure", "cancelled", "skipped", "", "in_progress"):
                with self.subTest(name=name, status=status), patch.dict(os.environ, {name: status}):
                    with self.assertRaises(ValueError):
                        release.publish("v0.1.0", self.assets)
                    self.assert_not_published()

    def test_disabled_gate_or_non_main_dispatch_never_publish(self):
        self.select()
        for env in ({"CASKLET_MACOS_E2E_ENABLED": ""}, {"GITHUB_REF": "refs/heads/untrusted"}):
            with patch.dict(os.environ, env), self.assertRaises(ValueError):
                release.publish("v0.1.0", self.assets)
            self.assert_not_published()

    def test_published_or_replaced_draft_never_publishes(self):
        self.select()
        for change in ({"isDraft": False}, {"databaseId": 43}, {"tagName": "other"}):
            with patch.dict(self.metadata, change), self.assertRaises(ValueError):
                release.publish("v0.1.0", self.assets)
            self.assert_not_published()

    def test_moved_tag_and_non_main_commit_never_publish(self):
        self.select()
        self.git("checkout", "-qb", "unmerged")
        (self.repo / "source").write_text("unmerged\n")
        self.git("commit", "-qam", "test: unmerged candidate")
        self.git("tag", "-f", "v0.1.0")
        self.git("push", "-q", "--force", "origin", "refs/tags/v0.1.0")
        with self.assertRaises(subprocess.CalledProcessError):
            release.publish("v0.1.0", self.assets)
        self.assert_not_published()
        self.git("push", "-q", "origin", "HEAD:refs/heads/main")
        with self.assertRaises(ValueError):
            release.publish("v0.1.0", self.assets)
        self.assert_not_published()

    def test_replaced_annotated_tag_for_same_commit_never_publishes(self):
        self.select()
        self.git("tag", "-fa", "v0.1.0", "-m", "replaced annotation")
        self.git("push", "-q", "--force", "origin", "refs/tags/v0.1.0")
        with self.assertRaises(ValueError):
            release.publish("v0.1.0", self.assets)
        self.assert_not_published()

    def test_missing_or_corrupt_packages_never_upload_or_publish(self):
        self.select()
        archive = self.assets / archive_name("v0.1.0", "amd64")
        archive.write_bytes(b"damaged transfer")
        with self.assertRaises(ValueError):
            release.publish("v0.1.0", self.assets)
        self.assert_not_published()
        archive.unlink()
        with self.assertRaises(FileNotFoundError):
            release.publish("v0.1.0", self.assets)
        self.assert_not_published()

    def test_upload_failure_or_replaced_draft_during_upload_never_publishes(self):
        self.select()
        original_run = release.run

        def fail_upload(*args):
            if args[:3] == ("gh", "release", "upload"):
                raise subprocess.CalledProcessError(1, args)
            return original_run(*args)

        with patch.object(release, "run", side_effect=fail_upload), self.assertRaises(subprocess.CalledProcessError):
            release.publish("v0.1.0", self.assets)
        self.assert_not_published()

        def replace_draft(*args):
            if args[:3] == ("gh", "release", "upload"):
                self.metadata["databaseId"] = 43
                return ""
            return original_run(*args)

        with patch.object(release, "run", side_effect=replace_draft), self.assertRaises(ValueError):
            release.publish("v0.1.0", self.assets)
        self.assert_not_published()


if __name__ == "__main__":
    unittest.main()
