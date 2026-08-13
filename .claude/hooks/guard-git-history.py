#!/usr/bin/env python3
"""PreToolUse(Bash) guard against the two git operations nothing else can see.

Subagent shell commands are not written to the session transcript — `isSidechain`
is false on all 80,452 records across this project's 135 transcripts — so a hook is
the only instrument that observes them at all. Hooks do fire inside subagents.

Deliberately narrow. It denies two things and nothing else:

  1. A FORCE PUSH, anywhere. It is the only git operation that destroys history
     someone else may already have read. Two reached this repo (PR #115 INVED-01,
     PR #140 BUG-04); both happened to land while the PR was still a draft, so
     neither orphaned a review. Nothing made that the safe outcome — it was luck,
     and the same command after `ready_for_review` rewrites reviewed history.
     ci.yml's `history-rewrite` job covers that case server-side; this covers it
     before it leaves the machine.

  2. A branch-moving git write aimed at the MAIN CHECKOUT from a worktree session.
     M4-10's executor ran `git -C "$MAIN" reset --hard <sha>` and silently moved
     the user's main branch. A worktree session has no reason to move main; that
     is what made the rule "always work in $WORKTREE_PATH" (RALPH_PROMPT.md:166,
     and the Anti-Patterns row) worth enforcing rather than restating.

What it does NOT deny, on purpose:
  - `git commit --amend`. On an unpushed commit it is useful and harmless; on a
    pushed one the follow-up push is non-fast-forward and fails on its own unless
    forced — and forcing is denied by rule 1.
  - `fetch`, `worktree add/remove/prune`, `branch -d/-D` against the main
    checkout. RALPH Phase 0 and /post-merge-cleanup need exactly these.
  - Anything at all when the session itself is running in the main checkout.
    That is the user's own space and their own session.

Exit 0 allows. Exit 2 denies and returns the message on stderr to the caller.
"""
import json
import os
import re
import subprocess
import sys

# `push` in any form that overwrites rather than appends.
FORCE_PUSH = re.compile(
    r"\bgit\b[^|;&\n]*?\bpush\b[^|;&\n]*?"
    r"(?:--force-with-lease|--force\b|(?<=\s)-f\b|\s\+[A-Za-z0-9_./*-]+:)"
)

# Moves a branch or the working tree. Everything else against main is read-only
# or is worktree/branch bookkeeping that the RALPH lifecycle depends on.
BRANCH_MOVING = ("reset", "merge", "pull", "rebase", "checkout", "switch", "push", "commit")

SEGMENT_SPLIT = re.compile(r"(?:&&|\|\||[;\n|])")
ASSIGNMENT = re.compile(r'\b([A-Za-z_][A-Za-z0-9_]*)=(?:"([^"\n]*)"|\'([^\'\n]*)\'|([^\s;&|]+))')
DASH_C = re.compile(r"\bgit\s+(?:-c\s+\S+\s+)*-C\s+(?:\"([^\"]+)\"|'([^']+)'|(\S+))")
CD = re.compile(r"^\s*cd\s+(?:\"([^\"]+)\"|'([^']+)'|(\S+))\s*$")


def expand(cmd: str) -> str:
    """Resolve the `MAIN=/path ... git -C "$MAIN" ...` shape, which is how the one
    real instance of rule 2 was actually written."""
    values = {}
    for m in ASSIGNMENT.finditer(cmd):
        values[m.group(1)] = m.group(2) or m.group(3) or m.group(4) or ""
    # Three passes resolve one variable defined in terms of another, which is the
    # usual shape here: MAIN=/repo then WT="$MAIN/.claude/worktrees/x".
    for _ in range(3):
        before = cmd
        for name, value in values.items():
            cmd = cmd.replace("${%s}" % name, value).replace("$%s" % name, value)
        if cmd == before:
            break
    return cmd


def main_checkout(cwd: str):
    """The repository's primary working tree, derived from git — never hardcoded."""
    try:
        proc = subprocess.run(
            ["git", "-C", cwd or ".", "rev-parse", "--path-format=absolute", "--git-common-dir"],
            capture_output=True, text=True, timeout=5,
        )
    except Exception:
        return None
    if proc.returncode != 0:
        return None
    common = proc.stdout.strip()
    if not common.endswith(os.sep + ".git"):
        return None
    return os.path.dirname(common)


def subcommand(segment: str):
    m = re.search(r"\bgit\b((?:\s+-[cC]\s+\S+)*)\s+([a-z][a-z-]*)", segment)
    return m.group(2) if m else None


def offending_main_write(cmd: str, cwd: str, main: str):
    """The first segment that moves a branch in `main`, or None."""
    here = os.path.abspath(cwd) if cwd else main
    for segment in SEGMENT_SPLIT.split(cmd):
        cd = CD.match(segment)
        if cd:
            target = cd.group(1) or cd.group(2) or cd.group(3)
            here = os.path.abspath(os.path.join(here, os.path.expanduser(target)))
            continue
        if "git" not in segment:
            continue
        dash_c = DASH_C.search(segment)
        if dash_c:
            path = dash_c.group(1) or dash_c.group(2) or dash_c.group(3)
            target = os.path.abspath(os.path.expanduser(path))
        else:
            target = here
        if os.path.normpath(target) != os.path.normpath(main):
            continue
        sub = subcommand(segment)
        if sub in BRANCH_MOVING:
            return segment.strip(), sub
    return None


def deny(message: str) -> int:
    sys.stderr.write(message)
    return 2


def run() -> int:
    raw = sys.stdin.read()
    if "git" not in raw:
        return 0
    try:
        payload = json.loads(raw)
    except Exception:
        return 0
    if payload.get("tool_name") != "Bash":
        return 0
    cmd = (payload.get("tool_input") or {}).get("command") or ""
    if "git" not in cmd:
        return 0
    cmd = expand(cmd)

    if FORCE_PUSH.search(cmd):
        return deny(
            "Blocked: force push. It overwrites history the PR's review and any other "
            "checkout already have.\n"
            "Push a new commit instead. If a commit truly must be undone, revert it — "
            "that keeps the history readable and needs no force.\n"
            "If a human decided this rewrite is necessary, they run it themselves.\n"
        )

    cwd = payload.get("cwd") or ""
    main = main_checkout(cwd)
    if not main or os.path.normpath(os.path.abspath(cwd or ".")) == os.path.normpath(main):
        return 0  # the session IS the main checkout: the user's own space
    hit = offending_main_write(cmd, cwd, main)
    if hit:
        segment, sub = hit
        return deny(
            f"Blocked: `git {sub}` against the main checkout ({main}) from a worktree session.\n"
            f"  {segment}\n"
            "That moves a branch or the working tree in the user's own checkout. Work in this "
            "worktree instead (RALPH_PROMPT.md:166).\n"
            "Reads, `fetch`, `worktree ...` and `branch -d` against the main checkout are allowed.\n"
        )
    return 0


if __name__ == "__main__":
    sys.exit(run())
