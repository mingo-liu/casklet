"""Check release filenames, architecture completeness and transferred bytes."""
from pathlib import Path
import tarfile
import tempfile
import types
from unittest.mock import patch
import unittest

import release_assets
from release_assets import archive_name, checksum, verify


class ReleaseAssetTests(unittest.TestCase):
    def test_downloaded_archive_contains_only_the_executable(self):
        documents = {
            "LICENSE": "License",
            "README.md": "[Usage](guides/usage.md#run) [中文](README.zh-CN.md)\n",
            "README.zh-CN.md": "[架构](ARCHITECTURE.md)\n",
            "ARCHITECTURE.md": "[Development](guides/development.md)\n",
            "guides/usage.md": "# Run\n[Quick start](../README.md) [Development](development.md)\n",
            "guides/development.md": "[Architecture](../ARCHITECTURE.md)\n",
        }
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            (source / "bin").mkdir(parents=True)
            binary = source / "bin/casklet"
            binary.write_bytes(b"fake-native-client")
            binary.chmod(0o755)
            for name, text in documents.items():
                path = source / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(text)
            (source / "docs").mkdir()
            (source / "docs/private.md").write_text("private notes")
            with patch.object(release_assets, "sys", types.SimpleNamespace(platform="darwin", executable="python3")), \
                    patch.object(release_assets.platform, "machine", return_value="arm64"), \
                    patch.object(release_assets.subprocess, "check_output", return_value=""), \
                    patch.object(release_assets.subprocess, "run") as run:
                release_assets.build(source, root / "packages", "v1.2.3", "arm64")
            archive = root / "packages" / archive_name("v1.2.3", "arm64")
            with tarfile.open(archive) as package:
                self.assertEqual(package.getnames(), ["casklet"])
                entry = package.getmember("casklet")
                self.assertTrue(entry.isfile())
                self.assertNotEqual(entry.mode & 0o111, 0)
                self.assertEqual(package.extractfile(entry).read(), binary.read_bytes())
            self.assertEqual(run.call_count, 2)
            self.assertEqual(archive.with_name(archive.name + ".sha256").read_text(), checksum(archive))

    def test_manifest_matches_both_downloads(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            lines = []
            for arch in ("arm64", "amd64"):
                archive = directory / archive_name("v1.2.3-rc.1", arch)
                archive.write_bytes(arch.encode())
                line = checksum(archive)
                archive.with_name(archive.name + ".sha256").write_text(line)
                lines.append(line)
            assets = verify(directory, "v1.2.3-rc.1")
            self.assertEqual(len(assets), 3)
            self.assertEqual(assets[-1].read_text(), "".join(lines))

    def test_unsafe_or_non_version_tags_are_rejected(self):
        for version in ("../v1.0.0", "v1.0.0\nother", "main", "v1.0", "-v1.0.0", "v1.0.0/other"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                archive_name(version, "arm64")


if __name__ == "__main__":
    unittest.main()
