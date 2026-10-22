"""Exercise baseline validation before the privileged Linux wrapper can run."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class LinuxEntryTests(unittest.TestCase):
    def exercise(self, scenario):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            scripts = root / "scripts"
            scripts.mkdir()
            tools = root / "tools"
            tools.mkdir()
            (root / "bin").mkdir()
            (root / "rootfs" / "busybox" / "bin").mkdir(parents=True)
            for binary in (root / "bin" / "casklet-engine", root / "rootfs" / "busybox" / "bin" / "busybox"):
                binary.write_text("arm64")
                binary.chmod(0o700)
            legacy = root / "baseline"
            if scenario != "missing":
                legacy.write_text("amd64" if scenario == "architecture" else "arm64")
                legacy.chmod(0o600 if scenario == "nonexecutable" else 0o700)
            runner = scripts / "test-linux.sh"
            runner.write_text(Path(__file__).with_name("test-linux.sh").read_text())
            runner.chmod(0o700)
            helper = '''#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys
root = pathlib.Path(os.environ['FIXTURE'])
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
if name == 'uname': print('Linux')
elif name == 'id': print('501')
elif name == 'realpath':
    path = pathlib.Path(args[-1])
    if not path.is_file() and not path.is_dir(): sys.exit(1)
    print(path.resolve())
elif name == 'readelf':
    print('AArch64' if pathlib.Path(args[-1]).read_text() == 'arm64' else 'X86-64')
elif name == 'go':
    if args == ['env', 'GOHOSTARCH']: print('arm64')
    else: (root / 'compiled').touch()
elif name == 'sudo':
    env = os.environ.copy()
    env.pop('CASKLET_REQUIRE_LEGACY_ENGINE', None)
    env.pop('CASKLET_LEGACY_ENGINE', None)
    sys.exit(subprocess.run(args, env=env).returncode)
elif name == 'test-shared-vm.sh':
    (root / 'wrapped').write_text(json.dumps({name: os.environ.get(name) for name in ('CASKLET_LEGACY_ENGINE', 'CASKLET_REQUIRE_LEGACY_ENGINE')}))
'''
            for name in ("uname", "id", "realpath", "readelf", "go", "sudo", "systemd-run"):
                tool = tools / name
                tool.write_text(helper)
                tool.chmod(0o700)
            wrapper = scripts / "test-shared-vm.sh"
            wrapper.write_text(helper)
            wrapper.chmod(0o700)
            env = dict(os.environ, FIXTURE=str(root), PATH=str(tools) + os.pathsep + os.environ["PATH"],
                       CASKLET_REQUIRE_LEGACY_ENGINE="1", CASKLET_LEGACY_ENGINE=str(legacy),
                       CASKLET_BINARY=str(root / "bin" / "casklet-engine"), CASKLET_ROOTFS=str(root / "rootfs" / "busybox"))
            result = subprocess.run([str(runner)], env=env, capture_output=True, text=True, timeout=15)
            if scenario == "valid":
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads((root / "wrapped").read_text()), {
                    "CASKLET_LEGACY_ENGINE": str(legacy), "CASKLET_REQUIRE_LEGACY_ENGINE": "1"})
            else:
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "compiled").exists(), result.stderr)
                self.assertFalse((root / "wrapped").exists(), result.stderr)

    def test_invalid_required_baselines_never_reach_compilation_or_vm_state(self):
        for scenario in ("missing", "nonexecutable", "architecture"):
            with self.subTest(scenario=scenario):
                self.exercise(scenario)

    def test_required_baseline_survives_sudo_environment_reset(self):
        self.exercise("valid")


if __name__ == "__main__":
    unittest.main()
