"""Check release filenames, architecture completeness and transferred bytes."""
from pathlib import Path
import tempfile
import unittest

from release_assets import archive_name, checksum, verify


class ReleaseAssetTests(unittest.TestCase):
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
