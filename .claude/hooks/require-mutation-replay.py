#!/usr/bin/env python3
"""PreToolUse(Bash) gate: `gh pr create` and `gh pr ready` need a passing full mutation replay of HEAD.

Reads .ralph/mutations-<STORY>.jsonl and the .ralph/replay-<STORY>.json that a run of
`go run ./internal/tools/mutationreplay` (without --changed) writes beside it.
No mutations file, or an empty one, allows the PR. Any error in this script allows it.
Exit 0 allows. Exit 2 denies; stderr carries the reason.
"""
import glob
import hashlib
import json
import os
import re
import subprocess
import sys

# Ralph opens the draft before QA writes rows; the ready flip is when the replay must be clean.
PR_GATED = re.compile(r"\bgh\s+pr\s+(create|ready)\b")


def git(cwd, *args):
    out = subprocess.run(["git", "-C", cwd, *args], capture_output=True, text=True, timeout=5)
    return out.stdout.strip() if out.returncode == 0 else ""


def story_files(ralph, branch):
    """The branch's story file when the branch names one, else every mutations file."""
    files = sorted(glob.glob(os.path.join(ralph, "mutations-*.jsonl")))
    for f in files:
        story = os.path.basename(f)[len("mutations-"):-len(".jsonl")]
        if branch.startswith("feature/" + story.lower() + "-") or branch == "feature/" + story.lower():
            return [f]
    return files


def problem(path, head):
    """The reason this mutations file blocks a PR, or None."""
    with open(path, "rb") as fh:
        data = fh.read()
    rows = sum(1 for line in data.split(b"\n") if line.strip())  # as the replay counts
    if rows == 0:
        return None
    story = os.path.basename(path)[len("mutations-"):-len(".jsonl")]
    replay = os.path.join(os.path.dirname(path), "replay-%s.json" % story)
    run = "run `go run ./internal/tools/mutationreplay .ralph/%s` (no --changed)" % os.path.basename(path)
    try:
        with open(replay) as fh:
            s = json.load(fh)
    except FileNotFoundError:
        return "no replay proof for %s: %s" % (story, run)
    if s.get("sha") != head:
        return "replay proof for %s is for %s, not HEAD %s: %s" % (story, str(s.get("sha"))[:12], head[:12], run)
    if s.get("rows") != rows or s.get("rows_sha256") != hashlib.sha256(data).hexdigest():
        return "replay proof for %s does not cover the current %d rows: %s" % (story, rows, run)
    if s.get("not_proven") != 0 or s.get("invalid") != 0:
        return "replay of %s has %s NOT-PROVEN and %s INVALID rows; fix them and %s" % (
            story, s.get("not_proven"), s.get("invalid"), run)
    return None


def check(payload):
    if payload.get("tool_name") != "Bash":
        return None
    cmd = (payload.get("tool_input") or {}).get("command") or ""
    m = PR_GATED.search(cmd)
    if not m or (m.group(1) == "ready" and "--undo" in cmd):  # back to draft needs no proof
        return None
    cwd = payload.get("cwd") or "."
    root = git(cwd, "rev-parse", "--show-toplevel")
    head = git(cwd, "rev-parse", "HEAD")
    if not root or not head:
        return None
    files = story_files(os.path.join(root, ".ralph"), git(cwd, "branch", "--show-current"))
    for f in files:
        reason = problem(f, head)
        if reason:
            return "gh pr %s: %s" % (m.group(1), reason)
    return None


def main():
    raw = sys.stdin.read()
    if "pr" not in raw or ("create" not in raw and "ready" not in raw):
        return 0
    try:
        reason = check(json.loads(raw))
    except Exception as exc:  # fail open: a bug here must not block every PR
        sys.stderr.write("require-mutation-replay: skipped after error: %r\n" % exc)
        return 0
    if reason:
        sys.stderr.write("Blocked %s\n" % reason)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
