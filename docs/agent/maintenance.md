# Agent Context Maintenance

Review the repo-local Codex setup every three to six months and after major
Codex/tooling changes.

## Checklist

- Root `AGENTS.md` is still lean and repo-wide.
- Any subdirectory `AGENTS.md` files describe local invariants, not broad
  policy.
- Existing skills trigger for the tasks they describe and stay compact.
- The Stop hook catches real final-state drift without frequent false positives.
- `docs/agent/codebase-map.md` points to the current starting files.
- `.codex/skills/yeet-cli/references/yeet-help-llm.md` matches current help
  output for changed commands.
- Release, docs, and quality workflows still match `AGENTS.md` and any
  dedicated skills that have landed.
- New Codex capabilities have not made local guidance obsolete.

## When To Update

- Add or update local `AGENTS.md` when agents repeatedly miss subsystem-specific
  rules.
- Add or update a skill when a workflow is useful but too specific for
  always-loaded instructions.
- Update the codebase map when a common task has a better starting point.
- Tune hooks only with a replayable sample message that proves the false
  positive or false negative.

## Verification

Run these checks after agent-context changes:

```bash
python3 -m json.tool .codex/hooks.json >/dev/null
python3 -m py_compile .codex/hooks/stop_repo_state.py
rm -rf .codex/hooks/__pycache__
pre-commit run --all-files
```
