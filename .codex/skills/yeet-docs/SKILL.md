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
  `.codex/skills/yeet-cli/references/yeet-help-agent.md`.

## Rules

- Keep examples generic and free of private hostnames or service names.
- Keep changelog bullets user-facing and concise.
- The latest changelog entry must stand alone for someone installing or
  upgrading. If a screenshot showed only that version, it should still answer
  "what changed for me?" without requiring prior-release context.
- Never make release plumbing the changelog message: avoid bullets about git
  hashes, submodule pointers, source revisions, CI retries, tag repair, or
  website publication mechanics unless the user needs a concrete action.
- For a corrective release that supersedes a bad tag or artifact, carry forward
  the actual user-facing changes into the new latest version. If needed, mark
  the prior version as superseded in plain user terms, but do not make the
  corrective mechanics the only bullet.
- For CLI syntax changes, update parser tests, help text, README, and website
  docs together.
- Commit website changes inside the submodule before committing the root
  submodule pointer.

## Checks

```bash
git -C website diff --check
rg -n "private[-]host|/User[s]/" README.md website/docs .codex/skills
```
