"""Exercise the actual shared-VM shell runner with bounded fake systemd commands."""
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest


class SharedVMTests(unittest.TestCase):
    def exercise(self, scenario, expected):
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Path(temporary).resolve()
            tools = fixture / "tools"
            tools.mkdir()
            store = fixture / "store"
            service = fixture / "restart.service"
            enable = fixture / "enabled.service"
            transaction = fixture / "transaction"
            log = fixture / "commands.jsonl"
            active = fixture / "active"
            started = fixture / "started"
            if scenario != "absent":
                store.mkdir()
                (store / "daily-data").write_text("preserve daily data")
                service.write_text("daily restart unit")
                enable.symlink_to(service)
                active.touch()
            helper = '''#!/usr/bin/env python3
import json, os, pathlib, sys
fixture = pathlib.Path(os.environ['FIXTURE'])
mode = os.environ['SCENARIO']
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with (fixture / 'commands.jsonl').open('a') as log:
    log.write(json.dumps([name] + args) + '\\n')
if name == 'uname': print('Linux')
elif name == 'id': print('0')
elif name == 'mv': os.rename(args[-2], args[-1])
elif name == 'fixture-command':
    (fixture / 'started').touch()
    (fixture / 'store' / 'test-data').write_text('test result')
    sys.exit(9 if mode == 'test-failure' else 0)
elif name == 'systemctl':
    command = args[0]
    if command == 'show':
        if mode == 'inspection-failure': sys.exit(6)
        print('not-found' if mode == 'absent' else 'loaded')
    elif command == 'is-active':
        sys.exit(0 if (fixture / 'active').exists() else 3)
    elif command == 'list-units':
        if '--all' in args:
            if mode == 'cleanup-inspection-failure': sys.exit(6)
            if mode in ('loaded-test-units', 'test-stop-failure'):
                print('casklet-fixture.service loaded active running test')
    elif command == 'stop':
        # Simulate the real systemctl failure for a missing unit/wildcard.
        if args[1] in ('casklet-*', 'casklet-restarts.service') and mode == 'absent':
            sys.exit(5)
        if args[1] == 'casklet-restarts.service':
            if mode == 'manager-stop-failure': sys.exit(7)
            (fixture / 'active').unlink(missing_ok=True)
        elif mode == 'test-stop-failure': sys.exit(7)
    elif command == 'start': (fixture / 'active').touch()
'''
            for name in ("uname", "id", "flock", "systemctl", "ip", "nft", "findmnt", "mv", "fixture-command"):
                path = tools / name
                path.write_text(helper)
                path.chmod(0o700)
            source = Path(__file__).with_name("test-shared-vm.sh").read_text()
            # Relocate only fixed storage paths in a test copy. The complete
            # production control flow, command parsing and traps run unchanged.
            replacements = {
                "transaction=/var/lib/casklet-integration": "transaction=" + shlex.quote(str(transaction)),
                "store=/var/lib/casklet": "store=" + shlex.quote(str(store)),
                "service=/etc/systemd/system/casklet-restarts.service": "service=" + shlex.quote(str(service)),
                "enable=/etc/systemd/system/multi-user.target.wants/casklet-restarts.service": "enable=" + shlex.quote(str(enable)),
                "exec 9>/var/lib/.casklet-integration.lock": "exec 9>" + shlex.quote(str(fixture / "lock")),
            }
            for original, replacement in replacements.items():
                self.assertEqual(source.count(original), 1)
                source = source.replace(original, replacement)
            runner = fixture / "runner.sh"
            runner.write_text(source)
            env = os.environ.copy()
            env.update(PATH=str(tools) + os.pathsep + env["PATH"], FIXTURE=str(fixture), SCENARIO=scenario)
            result = subprocess.run(["sh", str(runner), "fixture-command"], env=env,
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, expected, result.stderr)
            commands = [json.loads(line) for line in log.read_text().splitlines()]
            stops = [args[2:] for args in commands if args[:2] == ["systemctl", "stop"]]
            self.assertNotIn(["casklet-*"], stops)
            if scenario == "absent":
                self.assertEqual(stops, [])
                self.assertFalse(store.exists())
                self.assertFalse(transaction.exists())
            elif scenario in ("test-stop-failure", "cleanup-inspection-failure"):
                # Unsafe cleanup preserves original data for operator recovery.
                self.assertEqual((transaction / "store" / "daily-data").read_text(), "preserve daily data")
                self.assertEqual((transaction / "restart-service").read_text(), "daily restart unit")
                self.assertTrue((transaction / "restart-enable").is_symlink())
            else:
                self.assertEqual((store / "daily-data").read_text(), "preserve daily data")
                self.assertFalse((store / "test-data").exists())
                self.assertEqual(service.read_text(), "daily restart unit")
                self.assertEqual(enable.resolve(), service)
                self.assertTrue(active.exists())
                self.assertEqual(transaction.exists(), scenario in ("test-failure", "manager-stop-failure"))
            if scenario == "inspection-failure":
                self.assertFalse(started.exists())
                self.assertEqual(stops, [])
                self.assertIn("cannot inspect", result.stderr)
            if scenario == "manager-stop-failure":
                self.assertFalse(started.exists())
            if scenario == "loaded-test-units":
                self.assertIn(["casklet-fixture.service"], stops)
            if scenario in ("test-stop-failure", "cleanup-inspection-failure"):
                self.assertIn("cannot stop test units", result.stderr)

    def test_disposable_and_daily_manager_lifecycle(self):
        for scenario, status in (("absent", 0), ("present", 0), ("loaded-test-units", 0),
                                 ("test-failure", 9), ("inspection-failure", 1),
                                 ("manager-stop-failure", 7), ("test-stop-failure", 1),
                                 ("cleanup-inspection-failure", 1)):
            with self.subTest(scenario=scenario):
                self.exercise(scenario, status)


if __name__ == "__main__":
    unittest.main()
