"""Fail-closed checks for operator-managed dedicated Mac release infrastructure."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("macos_gate", Path(__file__).with_name("check-macos-e2e.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class GateTests(unittest.TestCase):
    def setUp(self):
        self.runners = [self.runner(1, "ARM64"), self.runner(2, "X64")]
        self.environment = {"deployment_branch_policy": {"custom_branch_policies": True, "protected_branches": False}}
        self.policies = [{"name": "main", "type": "branch"}]
        self.variables = []

    def runner(self, identity, arch):
        return {"id": identity, "status": "online", "busy": True,
                "labels": [{"name": name} for name in ("self-hosted", "macOS", "casklet-e2e", arch)]}

    def api(self, path, paginate=False):
        if "/actions/runners" in path:
            # Exercise matches across pages, rather than only the first page.
            return [{"runners": self.runners[:1]}, {"runners": self.runners[1:]}]
        if "deployment-branch-policies" in path:
            return [{"branch_policies": self.policies}]
        if "/actions/variables" in path:
            return [{"variables": self.variables}]
        return self.environment

    def test_read_only_check_and_explicit_enable(self):
        def enable(*args):
            self.assertEqual(args, ("gh", "variable", "set", "CASKLET_MACOS_E2E_ENABLED", "--repo", "owner/repo", "--body", "1"))
            self.variables = [{"name": "CASKLET_MACOS_E2E_ENABLED", "value": "1"}]

        with patch.object(gate, "github", side_effect=self.api), patch.object(gate, "run", side_effect=enable) as run:
            report = gate.check("owner/repo")
            self.assertTrue(report["ready"])
            self.assertFalse(report["enabled"])
            run.assert_not_called()
            report = gate.check("owner/repo", enable=True)
            self.assertTrue(report["enabled"])
            run.assert_called_once()

    def test_incomplete_wrong_or_offline_runners_never_enable(self):
        for scenario in ("missing", "offline", "labels", "ambiguous"):
            with self.subTest(scenario=scenario):
                self.runners = [self.runner(1, "ARM64"), self.runner(2, "X64")]
                if scenario == "missing":
                    self.runners.pop()
                elif scenario == "offline":
                    self.runners[1]["status"] = "offline"
                elif scenario == "labels":
                    self.runners[1]["labels"] = [{"name": "X64"}]
                else:
                    self.runners[1]["labels"].append({"name": "ARM64"})
                with patch.object(gate, "github", side_effect=self.api), patch.object(gate, "run") as run:
                    self.assertFalse(gate.check("owner/repo", enable=True)["ready"])
                    run.assert_not_called()

    def test_unrestricted_wildcard_tag_or_extra_environment_rules_never_enable(self):
        for policy in (None, {"protected_branches": True, "custom_branch_policies": False}):
            checks = gate.prerequisites(self.runners, {"deployment_branch_policy": policy}, self.policies)
            self.assertFalse(all(item["ok"] for item in checks))
        for rules in ([], [{"name": "*", "type": "branch"}], [{"name": "main", "type": "tag"}], self.policies * 2):
            self.policies = rules
            with patch.object(gate, "github", side_effect=self.api), patch.object(gate, "run") as run:
                self.assertFalse(gate.check("owner/repo", enable=True)["ready"])
                run.assert_not_called()

    def test_api_failures_and_failed_variable_persistence_do_not_pass(self):
        with patch.object(gate, "github", side_effect=OSError("unavailable")), patch.object(gate, "run") as run:
            with self.assertRaises(OSError):
                gate.check("owner/repo", enable=True)
            run.assert_not_called()
        with patch.object(gate, "github", side_effect=self.api), patch.object(gate, "run"):
            with self.assertRaisesRegex(ValueError, "retain"):
                gate.check("owner/repo", enable=True)


if __name__ == "__main__":
    unittest.main()
