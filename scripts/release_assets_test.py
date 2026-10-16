"""Check release filenames, architecture completeness and transferred bytes."""
from pathlib import Path
import io
import tarfile
import tempfile
import types
from unittest.mock import patch
import unittest

import release_assets
from release_assets import archive_name, checksum, verify


class ReleaseAssetTests(unittest.TestCase):
    def test_downloaded_archive_contains_linked_documentation(self):
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
                self.assertEqual(set(package.getnames()), {"casklet", *documents})
                self.assertTrue(all(entry.isfile() for entry in package.getmembers()))
                for name, text in documents.items():
                    self.assertEqual(package.extractfile(name).read().decode(), text)
            self.assertEqual(run.call_count, 2)
            self.assertEqual(archive.with_name(archive.name + ".sha256").read_text(), checksum(archive))

    def test_packaged_links_reject_missing_and_escaping_targets(self):
        for link in ("guides/missing.md#run", "../outside.md", "guides/deleted%20guide.md"):
            with self.subTest(link=link), tempfile.TemporaryDirectory() as temporary:
                archive = Path(temporary) / "archive.tar.gz"
                with tarfile.open(archive, "w:gz") as package:
                    body = f"[Missing]({link})".encode()
                    entry = tarfile.TarInfo("README.md")
                    entry.size = len(body)
                    package.addfile(entry, io.BytesIO(body))
                with tarfile.open(archive) as package, self.assertRaisesRegex(ValueError, "broken packaged documentation link"):
                    release_assets.validate_documentation_links(package)

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
