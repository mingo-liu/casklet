#!/usr/bin/env python3
"""Validate a trusted draft release and publish only its tested, unchanged tag."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from release_assets import validate_version, verify

ROOT = Path(__file__).resolve().parent.parent


def run(*args):
    return subprocess.run(args, cwd=ROOT, check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          timeout=60).stdout.strip()


def github(*args):
    return json.loads(run("gh", *args))


def candidate(tag):
    validate_version(tag)
    if os.environ.get("GITHUB_REF") != "refs/heads/main":
        raise ValueError("release validation must run from main")
    tested_revision = os.environ.get("MACOS_TESTED_REVISION", "")
    if re.fullmatch(r"[0-9a-f]{40}", tested_revision) is None:
        raise ValueError("provide the full commit SHA that passed the manual Apple silicon VM suite without skips")
    run("git", "check-ref-format", "refs/tags/" + tag)
    run("git", "fetch", "--no-tags", "origin",
        "+refs/heads/main:refs/remotes/origin/main",
        "+refs/tags/" + tag + ":refs/tags/" + tag)
    tag_oid = run("git", "rev-parse", "--verify", "refs/tags/" + tag)
    revision = run("git", "rev-parse", "--verify", "refs/tags/" + tag + "^{commit}")
    run("git", "merge-base", "--is-ancestor", revision, "origin/main")
    if revision != tested_revision:
        raise ValueError("manual Apple silicon VM validation must match the selected tag's commit")
    release = github("release", "view", tag, "--repo", os.environ["GITHUB_REPOSITORY"],
                     "--json", "databaseId,isDraft,tagName")
    if release.get("isDraft") is not True or release.get("tagName") != tag:
        raise ValueError("the selected release must exist as an unpublished draft for this tag")
    release_id = release.get("databaseId")
    if type(release_id) is not int or release_id <= 0:
        raise ValueError("invalid draft release identity")
    return {"revision": revision, "tag_oid": tag_oid, "release_id": str(release_id)}


def prepare(tag):
    selected = candidate(tag)
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        for name, value in selected.items():
            output.write(f"{name}={value}\n")
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
            summary.write(f"Manual Apple silicon VM validation declared for `{selected['revision']}`.\n\n"
                          "The maintainer confirms the complete suite passed without skips; "
                          "this workflow checks the revision and runs the hosted release checks.\n")


def publish(tag, assets):
    if os.environ.get("CHECKS_RESULT") != "success":
        raise ValueError("hosted checks must succeed before publishing")
    expected = {"revision": os.environ.get("RELEASE_REVISION", ""),
                "tag_oid": os.environ.get("RELEASE_TAG_OID", ""),
                "release_id": os.environ.get("RELEASE_ID", "")}
    if any(re.fullmatch(r"[0-9a-f]{40}", expected[name]) is None for name in ("revision", "tag_oid")) or not expected["release_id"].isdigit():
        raise ValueError("missing or invalid release validation identity")
    if candidate(tag) != expected:
        raise ValueError("draft release or tag changed after validation; start a new release run")
    paths = verify(assets, tag)
    run("gh", "release", "upload", tag, *map(str, paths), "--clobber",
        "--repo", os.environ["GITHUB_REPOSITORY"])
    if candidate(tag) != expected:
        raise ValueError("draft release or tag changed during asset upload; start a new release run")
    github("api", "--method", "PATCH",
           f"repos/{os.environ['GITHUB_REPOSITORY']}/releases/{expected['release_id']}",
           "-F", "draft=false")
    print(f"Published {tag} after hosted checks and declared manual Apple silicon VM validation.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "publish"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--assets", type=Path, help="verified architecture packages (required for publish)")
    args = parser.parse_args()
    try:
        if args.action == "publish":
            if args.assets is None:
                raise ValueError("publish requires --assets")
            publish(args.tag, args.assets)
        else:
            prepare(args.tag)
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"release: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
