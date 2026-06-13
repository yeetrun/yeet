# GitButler Agent Policy Design

## Context

The repository uses GitButler workspace mode for agent version-control work.
Agents should be able to work in parallel in one checkout, keep their changes on
dedicated GitButler branches, and avoid disturbing other sessions. The repository
targets `origin/main`, and the preferred finish path is no pull request: when
explicitly authorized, work should end up on both local `main` and
`origin/main`.

## Goals

- Keep agent work isolated by session while still allowing shared-workspace
  parallelism.
- Allow autonomous local checkpoint commits without creating noisy micro-commit
  history.
- Keep `main` release-grade and avoid unauthorized pushes or pull requests.
- Make the no-PR finish path explicit and compatible with GitButler's
  `origin/main` target model.
- Document safe update, overlap, and recovery behavior for future sessions.
- Document cleanup behavior for integrated GitButler session branches.

## Non-Goals

- Do not switch the repository to a pull-request-first workflow.
- Do not require granular commits for every edit or small follow-up.
- Do not replace existing quality gates or pre-commit behavior.
- Do not document a full GitButler command manual in `AGENTS.md`.

## Proposed AGENTS.md Changes

Add a compact policy block under `## Version Control`:

- Agents may create local checkpoint commits autonomously after a coherent unit
  of work is complete.
- Avoid micro-commits. Prefer checkpoint commits that match the current
  objective and would make sense when read later.
- Pre-commit hooks are intentionally expensive and should run normally, but
  agents should avoid unnecessary checkpoint churn.
- Treat checkpoint commits as local savepoints, not final history. Before
  finishing to `main`, use GitButler to tidy, squash, reword, or amend
  unpublished session commits into a clean final shape.
- At safe boundaries, run `but pull --check`; if it is clean and affects only
  this session's branch, `but pull` is allowed. If it conflicts or touches
  another active branch, stop and ask.
- If follow-up fixes clearly belong to an unpublished local commit, amend or
  absorb them into that commit instead of creating tiny fixup commits.
- Before large history edits or branch restructuring, create a GitButler
  recovery point with `but oplog snapshot`.
- If another active branch or session touches the same files, generated output,
  or runtime state, call out the overlap before committing or finishing.
- Preserve the repo-specific no-PR finish path: final work lands on local `main`
  and `origin/main` only when explicitly authorized.
- After a session lands on `main`, run `but pull`, preview cleanup with
  `but clean --dry-run`, then run `but clean` only for safe integrated
  GitButler branches. Delete raw local `codex/*` refs only when they belong to
  the current session and are confirmed merged.

## Operational Behavior

For ordinary work, agents should create or reuse a dedicated GitButler branch,
make coherent checkpoint commits as useful local savepoints, and keep unrelated
changes out of the session branch. Pre-commit remains the normal local gate and
should be allowed to run.

For updates from the target branch, agents should check first. Clean updates at
safe boundaries are acceptable because this repository is primarily maintained
by one developer and `main` is not expected to move quickly. Conflicts,
generated-output churn, dependency changes, or anything involving another active
branch should pause for user direction.

For finish-to-main requests, agents should first use GitButler to produce a
single coherent session commit when needed. Then they should verify the commit is
based on current `origin/main` and contains only the session's work. Only after
explicit user authorization should the agent perform the narrow final direct
update that makes local `main` and `origin/main` point at the finished commit.
After that update, agents should let GitButler recognize the integrated branch
with `but pull`, preview cleanup before deleting anything, and avoid removing
branches from other sessions unless the user asks.

## Testing And Verification

The `AGENTS.md` update is documentation-only. Verification should include:

- Review the diff for conflicts with existing `Version Control`, release, and
  commit guidance.
- Confirm GitButler status is clean before and after the edit, except for the
  intended documentation changes.
- Preview cleanup with `but clean --dry-run`; do not delete branches unless they
  are confirmed integrated and belong to the current session.
- If committing, let pre-commit run normally and report any failures.
