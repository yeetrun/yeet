---
name: yeet-docs
description: Use when user-facing CLI behavior, flags, workflows, README examples, website manual pages, or changelog entries need to be updated.
---

# Yeet Docs

Use this skill when a change affects what users run or read.

## Surfaces

- Root `README.md` for quickstart and common examples.
- `website/docs/` for the user manual.
- `website/docs/changelog.mdx` only for tagged releases.
- CLI help metadata in `pkg/cli/cli.go` and `cmd/yeet/cli.go`.
- Help reference:
  `.codex/skills/yeet-cli/references/yeet-help-llm.md`.

## Rules

- Keep examples generic and free of private hostnames or service names.
- Keep changelog bullets user-facing and concise.
- For CLI syntax changes, update parser tests, help text, README, and website
  docs together.
- Commit website changes inside the submodule before committing the root
  submodule pointer.

## Checks

```bash
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```
