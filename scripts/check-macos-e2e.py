#!/usr/bin/env python3
"""Check dedicated Mac release prerequisites; optionally enable the existing gate."""
import argparse
import json
import os
import re
import subprocess
import sys


def run(*args):
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, timeout=60).stdout.strip()


def github(path, paginate=False):
    args = ["gh", "api", path]
    if paginate:
        args += ["--paginate", "--slurp"]
    return json.loads(run(*args))


def prerequisites(runners, environment, policies):
    matches = []
    required = {"self-hosted", "macos", "casklet-e2e", "arm64"}
    for runner in runners:
        labels = {label["name"].lower() for label in runner["labels"]}
        if runner.get("status") == "online" and required <= labels and "x64" not in labels:
            matches.append(runner["id"])
    checks = [{"name": "runner_arm64", "ok": bool(matches), "runner_ids": sorted(matches)}]
    policy = environment.get("deployment_branch_policy")
    checks.append({"name": "main_only_environment", "ok": (
        isinstance(policy, dict) and policy.get("custom_branch_policies") is True
        and policy.get("protected_branches") is False and len(policies) == 1
        and policies[0].get("name") == "main" and policies[0].get("type") == "branch")})
    return checks


def check(repo, enable=False):
    if re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo) is None:
        raise ValueError("--repo must be OWNER/REPOSITORY")
    root = "repos/" + repo
    runners = [runner for page in github(root + "/actions/runners?per_page=100", True)
               for runner in page["runners"]]
    environment = github(root + "/environments/macos-e2e")
    policies = [policy for page in github(
        root + "/environments/macos-e2e/deployment-branch-policies?per_page=100", True)
                for policy in page["branch_policies"]]
    checks = prerequisites(runners, environment, policies)
    ready = all(item["ok"] for item in checks)
    if enable and ready:
        run("gh", "variable", "set", "CASKLET_MACOS_E2E_ENABLED", "--repo", repo, "--body", "1")
    variables = [variable for page in github(root + "/actions/variables?per_page=100", True)
                 for variable in page["variables"]]
    enabled = any(item["name"] == "CASKLET_MACOS_E2E_ENABLED" and item["value"] == "1"
                  for item in variables)
    if enable and ready and not enabled:
        raise ValueError("the Mac gate variable did not retain the enabled value")
    return {"repository": repo, "ready": ready, "enabled": enabled, "checks": checks}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY"), required=not os.environ.get("GITHUB_REPOSITORY"))
    parser.add_argument("--enable", action="store_true", help="set the gate variable only after all prerequisites pass")
    args = parser.parse_args()
    try:
        report = check(args.repo, args.enable)
        print(json.dumps(report, sort_keys=True))
        if not report["ready"]:
            print("macOS gate: register an online ARM64 dedicated Mac runner and restrict macos-e2e to the main branch; no settings were changed", file=sys.stderr)
        return 0 if report["ready"] else 1
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError) as error:
        # gh receives authentication through its own environment/configuration;
        # do not print captured API response bodies or credential configuration.
        print(f"macOS gate: prerequisite inspection failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
