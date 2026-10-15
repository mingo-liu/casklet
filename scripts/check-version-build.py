#!/usr/bin/env python3
"""Check stamped native executable metadata and its embedded engine offline."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile


def output(args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, timeout=30, **kwargs).stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--engine", type=Path)
    parser.add_argument("--expected-version", required=True)
    args = parser.parse_args()
    binary = str(args.binary.resolve())
    with tempfile.TemporaryDirectory() as directory:
        env = dict(os.environ, PATH=directory, LIMA_HOME=directory + "/untouched",
                   CASKLET_MACHINE_STATE_DIR="invalid-relative-state")
        actual = output([binary, "version", "--json"], env=env)
        assert actual == output([binary, "--version", "--json"], env=env)
        info = json.loads(actual)
        assert info["version"] == args.expected_version, info
        assert info["commit"] == output(["git", "rev-parse", "HEAD"]).strip(), info
        assert info["dirty"] == bool(output(["git", "status", "--porcelain"]).strip()), info
        assert not Path(env["LIMA_HOME"]).exists()
        if args.engine:
            expected_hash = hashlib.sha256(args.engine.read_bytes()).hexdigest()
            assert info["engine"] == {"os": "linux", "arch": info["arch"], "sha256": expected_hash}, info
            metadata = json.loads(output(["go", "version", "-m", "-json", str(args.engine)]))
            settings = {setting["Key"]: setting["Value"] for setting in metadata["Settings"]}
            assert settings["vcs.revision"] == info["commit"]
        else:
            assert "engine" not in info and "engine_error" not in info, info
    print("Stamped native build and offline version identity checks passed.")


if __name__ == "__main__":
    main()
