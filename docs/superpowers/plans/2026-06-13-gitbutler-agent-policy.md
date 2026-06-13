# GitButler Agent Policy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update repository agent instructions so GitButler sessions can checkpoint autonomously without noisy commits, safely update from `origin/main`, and finish through the repo's explicit no-PR mainline workflow.

**Architecture:** This is a documentation-only change. The root `AGENTS.md` remains the repo-wide policy source, and the new rules live inside the existing `## Version Control` section so future agents find workflow guidance before build, test, and release instructions.

**Tech Stack:** Markdown, GitButler CLI (`but`), existing pre-commit hooks.

---

### Task 1: Add GitButler Operating Policy To AGENTS.md

**Files:**
- Modify: `/Users/shayne/code/yeet/AGENTS.md`

- [ ] **Step 1: Read the current version-control section**

Run:

```bash
sed -n '25,56p' /Users/shayne/code/yeet/AGENTS.md
```

Expected: output starts with `## Version Control` and includes the existing GitButler, no-PR, and finish-to-main bullets.

- [ ] **Step 2: Insert the operating-policy bullets**

Edit `/Users/shayne/code/yeet/AGENTS.md` under `## Version Control`, after the dedicated-session-branch bullet and before the push/PR authorization bullet, so the section includes this text:

```markdown
- Agents may create local checkpoint commits autonomously after a coherent unit
  of work is complete. Do not create micro-commits for every small edit; prefer
  commits that match the current objective and would make sense when read later.
- Pre-commit hooks are intentionally expensive and should run normally. Avoid
  unnecessary checkpoint churn, and report any pre-commit failure with the fix
  or remaining blocker.
- Treat checkpoint commits as local savepoints, not final history. Before
  finishing to `main`, use GitButler to tidy, squash, reword, or amend
  unpublished session commits into a clean final shape.
- At safe boundaries, such as before starting substantial work, before a
  checkpoint commit, or before finishing to `main`, run `but pull --check`. If
  it is clean and affects only this session's branch, `but pull` is allowed. If
  it conflicts or touches another active branch, stop and ask.
- If follow-up fixes clearly belong to an unpublished local commit, amend or
  absorb them into that commit instead of creating tiny fixup commits.
- Before large history edits or branch restructuring, create a GitButler
  recovery point with `but oplog snapshot -m "before history cleanup"`.
- If another active branch or session touches the same files, generated output,
  or runtime state, call out the overlap before committing or finishing.
- After a session lands on `main`, run `but pull` so GitButler can mark the
  branch integrated, then preview cleanup with `but clean --dry-run` before
  running `but clean`. Delete non-empty branches or raw local `codex/*` refs
  only when they belong to this session and are confirmed merged; never clean up
  another agent's branch unless the user asks.
```

- [ ] **Step 3: Review the resulting section**

Run:

```bash
sed -n '25,75p' /Users/shayne/code/yeet/AGENTS.md
```

Expected: the section reads in this order:

```text
## Version Control
Use GitButler for normal agent write operations
Assume multiple agents may be working
Use a dedicated GitButler branch
Autonomous local checkpoint policy
Expensive pre-commit policy
Checkpoint commits are local savepoints
Safe but pull policy
Amend/absorb follow-up fixes
Oplog recovery point before large history edits
Overlap callout policy
Do not push or open PRs unless asked
Finish-to-main no-PR workflow
Do not use but merge as default finish command
Final direct update exception
Integrated branch cleanup policy
Succinct commit/PR descriptions
```

### Task 2: Verify Documentation Consistency

**Files:**
- Inspect: `/Users/shayne/code/yeet/AGENTS.md`
- Inspect: `/Users/shayne/code/yeet/docs/superpowers/specs/2026-06-13-gitbutler-agent-policy-design.md`

- [ ] **Step 1: Check for stale PR-default wording**

Run:

```bash
rg -n "before PR|PRs should|open pull requests by default|ship it|stacked pull requests" /Users/shayne/code/yeet/AGENTS.md
```

Expected: no output. The existing phrase `explicitly requested pull request descriptions` is acceptable if it appears in the commit-guidance line because it is not a default workflow.

- [ ] **Step 2: Check the no-PR mainline policy is still present**

Run:

```bash
rg -n "origin/main|no-PR|pull request|but merge|checkpoint|pre-commit|oplog|overlap|but clean|codex/\\*" /Users/shayne/code/yeet/AGENTS.md
```

Expected: output includes the no-PR finish path, the `origin/main` target note, the `but merge` warning, checkpoint guidance, pre-commit guidance, recovery-point guidance, overlap guidance, and integrated-branch cleanup guidance.

- [ ] **Step 3: Compare AGENTS.md against the approved design**

Run:

```bash
diff -u \
  <(rg -n "checkpoint|micro-commits|pre-commit|but pull --check|amend|absorb|oplog|overlap|origin/main|but clean|codex/\\*" /Users/shayne/code/yeet/docs/superpowers/specs/2026-06-13-gitbutler-agent-policy-design.md) \
  <(rg -n "checkpoint|micro-commits|pre-commit|but pull --check|amend|absorb|oplog|overlap|origin/main|but clean|codex/\\*" /Users/shayne/code/yeet/AGENTS.md)
```

Expected: line numbers and exact wording may differ, but every approved policy topic appears in `AGENTS.md`.

### Task 3: Final GitButler Verification

**Files:**
- Inspect: GitButler workspace state

- [ ] **Step 1: Check GitButler status**

Run:

```bash
BUT_PAGER=cat but status -fv
```

Expected: only the intended documentation changes appear as unassigned changes unless the execution session already committed them to its GitButler branch.

- [ ] **Step 2: If committing is explicitly authorized, commit the documentation changes**

Run only after explicit user authorization:

```bash
BUT_PAGER=cat but status -fv
but branch new codex/gitbutler-agent-policy
but commit codex/gitbutler-agent-policy -m "agents: tune GitButler workflow"
```

Expected: GitButler creates a local commit on `codex/gitbutler-agent-policy`, and the returned status has no unrelated changes. This command is safe only if the preceding status output shows that the workspace contains only this plan's documentation changes.

- [ ] **Step 3: If finishing to main is explicitly authorized, use the repo-specific finish path**

Run only after explicit user authorization and after checking `but status -fv`,
`but branch show`, `git ls-remote origin refs/heads/main`, and
`git merge-base --is-ancestor origin/main` with the session branch name or
single session commit shown by `but status -fv`:

```bash
but pull
```

Expected: this documentation task normally does not need a finish-to-main step unless the user asks. If the user does ask, follow the existing `AGENTS.md` finish-to-main policy exactly: verify a single session commit based on current `origin/main`, push that commit to `origin/main`, run `but pull`, update local `main` to the same commit, then confirm `origin/main`, local `main`, and `but config target` all point at the finished commit and `but status -fv` reports no unassigned changes.

- [ ] **Step 4: Preview integrated branch cleanup**

Run:

```bash
but clean --dry-run
```

Expected: GitButler reports which empty integrated branches would be removed, or reports that no empty branches were found. Do not delete raw local `codex/*` refs unless they belong to the current session and are confirmed merged.
