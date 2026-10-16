#!/usr/bin/env python3
"""Build native macOS release archives and verify their transfer checksums."""
import argparse
import hashlib
import os
from pathlib import Path
import platform
import posixpath
import re
import subprocess
import sys
import tarfile
import tempfile
from urllib.parse import unquote, urlsplit


def validate_version(version):
    if re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?", version) is None:
        raise ValueError("release tags must use vMAJOR.MINOR.PATCH with an optional suffix")


def archive_name(version, arch):
    validate_version(version)
    return f"casklet_{version}_darwin_{arch}.tar.gz"


def checksum(path):
    return hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.name + "\n"


def verify(directory, version):
    """Require both architectures and write the public, combined manifest."""
    archives = [directory / archive_name(version, arch) for arch in ("arm64", "amd64")]
    lines = []
    for archive in archives:
        line = checksum(archive)
        if archive.with_name(archive.name + ".sha256").read_text() != line:
            raise ValueError(f"checksum mismatch: {archive.name}")
        lines.append(line)
    manifest = directory / "SHA256SUMS"
    manifest.write_text("".join(lines))
    return [*archives, manifest]


def validate_documentation_links(package):
    """Require inline local Markdown links to resolve inside the download."""
    members = {entry.name for entry in package.getmembers() if entry.isfile()}
    for name in sorted(members):
        if not name.endswith(".md"):
            continue
        text = package.extractfile(name).read().decode("utf-8")
        for link in re.findall(r"\[[^\]]*\]\(([^\s)]+)\)", text):
            target = urlsplit(link)
            if target.scheme or target.netloc:
                continue
            path = unquote(target.path)
            destination = posixpath.normpath(posixpath.join(posixpath.dirname(name), path)) if path else name
            if destination not in members:
                raise ValueError(f"broken packaged documentation link in {name}: {link}")


def build(project, directory, version, arch):
    validate_version(version)
    if sys.platform != "darwin" or platform.machine() != {"arm64": "arm64", "amd64": "x86_64"}[arch]:
        raise ValueError("release packages must be built and checked on their native Mac architecture")
    if subprocess.check_output(["git", "status", "--porcelain"], cwd=project, text=True).strip():
        raise ValueError("release packages require a clean source checkout")
    subprocess.run(["make", "build", f"GOARCH={arch}", f"VERSION={version}"],
                   cwd=project, check=True, timeout=600)
    directory.mkdir(parents=True, exist_ok=True)
    archive = directory / archive_name(version, arch)
    members = {"casklet": project / "bin/casklet"}
    documents = ("LICENSE", "README.md", "README.zh-CN.md", "ARCHITECTURE.md",
                 "guides/usage.md", "guides/development.md")
    members.update({name: project / name for name in documents})
    with tarfile.open(archive, "w:gz") as package:
        for name, path in members.items():
            package.add(path, arcname=name, recursive=False)
    # Exercise the executable from the archive users actually download.
    with tempfile.TemporaryDirectory() as temporary, tarfile.open(archive, "r:gz") as package:
        entries = package.getmembers()
        if sorted(entry.name for entry in entries) != sorted(members) or not all(entry.isfile() for entry in entries):
            raise ValueError("unexpected release archive contents")
        validate_documentation_links(package)
        if package.getmember("casklet").mode & 0o111 == 0:
            raise ValueError("release executable is not executable")
        binary = Path(temporary) / "casklet"
        binary.write_bytes(package.extractfile("casklet").read())
        os.chmod(binary, 0o755)
        subprocess.run([sys.executable, str(Path(__file__).with_name("check-version-build.py")),
                        "--binary", str(binary), "--engine", str(project / "internal/machine/assets/casklet-engine"),
                        "--expected-version", version], cwd=project, check=True, timeout=60)
    archive.with_name(archive.name + ".sha256").write_text(checksum(archive))
    print(f"Built and checked {archive.name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", type=Path, default=Path.cwd())
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--arch", required=True, choices=("arm64", "amd64"))
    args = parser.parse_args()
    build(args.project.resolve(), args.output.resolve(), args.version, args.arch)


if __name__ == "__main__":
    main()
