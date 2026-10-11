"""Regression checks for runner isolation and cleanup without starting a VM."""
import contextlib
import importlib.util
import io
import os
from pathlib import Path
import signal
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("runner", Path(__file__).with_name("test-macos-ci.py"))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class RunnerTests(unittest.TestCase):
    def run_fake(self, mode):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        directory = Path(temporary.name)
        tools = directory / "tools"
        tools.mkdir()
        script = '''#!/usr/bin/env python3
import os, pathlib, sys, time
lima_home = pathlib.Path(os.environ['LIMA_HOME'])
assert str(lima_home) != os.environ['DAILY_LIMA']
assert os.environ['CASKLET_MACHINE_STATE_DIR'] != os.environ['DAILY_STATE']
assert os.environ['CASKLET_MACOS_RESIZE_GROW'] == '1'
state = pathlib.Path(os.environ['CASKLET_MACHINE_STATE_DIR'])
assert state.parent == pathlib.Path(os.environ['SHARED_HOME'])
(state / 'install-sentinel').write_text('isolated engine staging')
instance = lima_home / 'casklet-runtime'
mode = os.environ['TEST_MODE']
if pathlib.Path(sys.argv[0]).name == 'make':
    if mode == 'bootstrap-failure': sys.exit(13)
    instance.mkdir()
    (instance / 'serial.log').write_text('diagnostic')
    (instance / 'ssh_key').write_text('private-key-do-not-copy')
    if mode == 'timeout': time.sleep(30)
    if mode == 'signal': os.kill(os.getppid(), 15); time.sleep(30)
    sys.exit(7 if mode == 'failure' else 0)
if 'delete' in sys.argv:
    if mode == 'cleanup-failure': sys.exit(1)
    import shutil
    shutil.rmtree(instance)
print('fake VM diagnostics')
'''
        for name in ("make", "limactl"):
            executable = tools / name
            executable.write_text(script)
            executable.chmod(0o700)
        daily = directory / "daily"
        daily.mkdir()
        (daily / "sentinel").write_text("daily data")
        artifacts = directory / "artifacts"
        shared_home = directory / "home"
        shared_home.mkdir()
        env = {"PATH": str(tools) + os.pathsep + os.environ["PATH"],
               "LIMA_HOME": str(daily), "CASKLET_MACHINE_STATE_DIR": str(daily),
               "DAILY_LIMA": str(daily), "DAILY_STATE": str(daily), "TEST_MODE": mode,
               "SHARED_HOME": str(shared_home)}
        def interrupted(signum, _frame):
            raise runner.Interrupted(signum)
        old = signal.signal(signal.SIGTERM, interrupted)
        original_popen = runner.subprocess.Popen
        def launch(args, **kwargs):
            if mode == "launch-failure" and args[0] == "make":
                raise FileNotFoundError("make disappeared before launch")
            return original_popen(args, **kwargs)
        try:
            with patch.dict(os.environ, env), contextlib.redirect_stdout(io.StringIO()), \
                    patch.object(runner.subprocess, "Popen", side_effect=launch):
                result = runner.run_suite(directory, artifacts,
                                          timeout=0.2 if mode == "timeout" else 5,
                                          temporary_parent=directory, shared_home=shared_home)
        finally:
            signal.signal(signal.SIGTERM, old)
        self.assertEqual((daily / "sentinel").read_text(), "daily data")
        self.assertEqual(list(daily.iterdir()), [daily / "sentinel"])
        run = Path((artifacts / "run-directory.txt").read_text().strip())
        state = Path((artifacts / "state-directory.txt").read_text().strip())
        self.assertEqual(run.exists(), mode == "cleanup-failure")
        self.assertEqual(state.exists(), mode == "cleanup-failure")
        if mode == "cleanup-failure":
            self.assertEqual((state / 'install-sentinel').read_text(), 'isolated engine staging')
        if mode not in ("bootstrap-failure", "launch-failure"):
            self.assertEqual((artifacts / "lima-logs" / "serial.log").read_text(), "diagnostic")
        self.assertFalse((artifacts / "lima-logs" / "ssh_key").exists())
        return result, artifacts

    def test_success_failure_timeout_signal_and_cleanup_failure_preserve_daily_data(self):
        for mode, status in (("success", 0), ("failure", 7), ("timeout", 124),
                             ("signal", 143), ("cleanup-failure", 1),
                             ("bootstrap-failure", 13), ("launch-failure", 1)):
            with self.subTest(mode=mode):
                result, artifacts = self.run_fake(mode)
                self.assertEqual(result, status)
                self.assertTrue((artifacts / "suite.log").is_file())
                self.assertEqual((artifacts / "delete.log").is_file(),
                                 mode not in ("bootstrap-failure", "launch-failure"))
                if mode == "timeout":
                    self.assertIn("exceeded its deadline", (artifacts / "suite.log").read_text())
                if mode == "launch-failure":
                    self.assertIn("make disappeared", (artifacts / "suite-error.log").read_text())


if __name__ == "__main__":
    unittest.main()
