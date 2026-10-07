# Bug Ralph Workflow — ASComply Africa (invoice-os)

## Overview

`/bug-ralph <BUG-ID>` fixes one researched bug story: a reproducing test, the smallest fix, one independent QA pass, then a PR into `main`, `CI`, the deploy gate and the user's merge. It has no planning phase. The researcher's `## Cause` is the plan.

`RALPH_PROMPT.md` owns the shared rules. This file names each rule that it uses and states only what differs.

| Shared rule in `RALPH_PROMPT.md` | Use in `/bug-ralph` |
|---|---|
| Agents and models; CRITICAL RULES 1, 2, 3 and 5 | As written. |
| Phase 0.5: Worktree bootstrap | As written. `BASE=main`. |
| Phase 1: the screen playbook | As written, with `STORY_SOURCE=obsidian` and `<STORY>` = `<BUG-ID>`. |
| Stage 3: the orientation rules and the suite run | As written. |
| Stage 4: the mutation rows, their replay, the comment sweep and the "Frontend" rules | As written, for the one fix. |
| CI Monitoring Protocol | As written. |
| Phase 3.5: steps 2, 3 and 5 | As written. |
| Phase 4: Worktree cleanup | As written. |

## Phases

The phase numbers match `/ralph`, so `hm worker phase` reports them. `/bug-ralph` has no Phase 0.6.

### Phase 0: Story resolution

1. Read `Simon Vault/Projects/ASComply Africa/User Stories/BUG/<BUG-ID>*.md`. Also read `User Stories/Archive/BUG/`. When no file exists, stop with the error "no story <BUG-ID>".
2. Read the `fix:` line of the front matter:
   - `code`: continue.
   - `large`: stop with the error "<BUG-ID> needs /ralph".
   - `config`: stop with the error "<BUG-ID> is a config fix: the user applies it in hm".
   - none: stop with the error "<BUG-ID> has no verdict: hm devops research <BUG-ID>".
3. Read `## Cause`, `## Core Acceptance Criteria` and `## Out of Scope`. The cause names the file or the function and its evidence.
4. **Branch slug:** `fix/<lowercase-id>-<kebab title>`. Example: `fix/bug-31-login-fails-for-a-new-tenant`.

### Phase 0.5: Worktree bootstrap

Follow `RALPH_PROMPT.md` Phase 0.5. Start the dev Postgres only when the fix touches Go DB code or a migration.

### Phase 1: Reproduce, fix, verify

Run one stage agent at a time. Every stage brief says **terse comments**, per `CLAUDE.md` "Code Comments". Before each spawn, run `git -C "$WORKTREE_PATH" status --short` and `git -C "$WORKTREE_PATH" log --oneline -3`. Before the first spawn, resolve the screen playbook. Paste `.ralph/screens.txt` into every stage brief.

#### Stage Test-Spec: the reproducing test

Run `hm worker phase 1 "Test-Spec"`. Spawn `product-qa-spec` (Mode A) with the story's path and these rules:

- Write one test that reproduces the fault of `## Cause`. Put it beside the tests of the code that `## Cause` names.
- Run it. It must fail on its target assertion, not on a compile or setup error.
- Commit the red test.

When no test can reproduce the fault, the verdict is wrong. Send the coordinator `question: <BUG-ID> · no test reproduces the fault: <why> · options: change the verdict to config or large (default), or fix without a test`. End the turn.

#### Stage Execution: the fix

Run `hm worker phase 1 "Execution"`. Spawn `product-executor` with the story's path, the red test and these rules:

- Make the smallest change that makes the red test pass. Do not weaken, skip or delete the test.
- Change nothing that `## Out of Scope` names.
- Follow the orientation rules of `RALPH_PROMPT.md` Stage 3.
- Commit, push the branch, and create the PR into `main`, ready for review: `gh pr create --base main`. The PR body holds the cause in one line, the name of the red test and `Fixes <BUG-ID>`.

Then run the suites per `RALPH_PROMPT.md` Stage 3.

#### Stage QA Verify: one independent pass

Run `hm worker phase 1 "QA Verify"`. Spawn a new `product-qa-spec` (Mode B). It did not write the fix. Pass these rules:

- Read the fix and try to break it. Add tests for the edge cases and the negative cases of the fault.
- Search for the same fault elsewhere: the siblings of the changed code, and every other caller of the changed function. Fix each other occurrence in this PR, or name it in the QA report as out of scope.
- Prove the reproducing test can fail: write one mutation row that reverts the fix, per `RALPH_PROMPT.md` Stage 4.
- Re-read each comment and doc that the fix made false, per `RALPH_PROMPT.md` Stage 4.
- For UI work, follow the "Frontend" rules of `RALPH_PROMPT.md` Stage 4.

When QA returns, replay the mutation rows yourself: `go run ./internal/tools/mutationreplay .ralph/mutations-<BUG-ID>.jsonl`. A `NOT-PROVEN` or `INVALID` row fails QA. When QA fails, spawn `product-executor` to fix, then run QA Verify again. Cap: 2 cycles. After 2 cycles, send the coordinator the open findings as one question.

### Phase 2: PR

Run `hm worker phase 2`. Push the QA commits. When `origin/main` has commits that the branch lacks, merge `origin/main` and push.

### Phase 3: CI

Run `hm worker phase 3`. Wait for `CI` with `hm ci wait <PR>`, per the CI Monitoring Protocol. When it fails, fix in the worktree, commit, push and wait again.

### Phase 3.5: Deploy gate

Run `hm worker phase 3.5`. Follow `RALPH_PROMPT.md` Phase 3.5 steps 2 and 3. Then check each Core Acceptance Criterion against the green run yourself:

- Quote each criterion beside its evidence: the passing test, the CI job or the E2E assertion.
- The criterion that the Sentry issues stop is checked after the merge. hm checks it. Mark it `after merge`.
- A failed criterion goes back to `product-executor`. Cap: 2 cycles, per `RALPH_PROMPT.md` Phase 3.5 step 5.

When every criterion passes, end with a short report in this order: **Needs you** (merge PR #N with M twice on hm's DevOps tab), **Changed** (the test, the fix, the PR), **Found** (other occurrences of the fault, what you could not confirm). Then output `<promise>ALL_TASKS_COMPLETE</promise>`.

### Phase 4: Worktree cleanup

After the merge, run `/post-merge-cleanup <BUG-ID>`.

## Completion Rules

1. `CI` and the deploy gate are green on the PR head.
2. The reproducing test failed before the fix and passes after it. The mutation replay proves it.
3. Output `<promise>ALL_TASKS_COMPLETE</promise>` only after Phase 3.5 passes.
