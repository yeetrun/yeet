---
name: yeet-release
description: Use when preparing, validating, tagging, or pushing a yeet patch release.
---

# Yeet Release

Use this skill for release work. Follow root `AGENTS.md` exactly.

## Patch Release Flow

1. Find the latest `vX.Y.Z` tag and choose the next patch version.
2. Update `website/docs/changelog.mdx` with a date section, version heading, and
   1-3 user-facing bullets. Follow `.codex/skills/yeet-docs/SKILL.md` for
   changelog wording: the latest version must stand alone for users, and
   corrective release plumbing must not be the release note.
3. Commit and push the website changes inside `website/`.
4. Commit the updated `website` submodule pointer in the root repo.
5. Create an annotated tag with message equal to the version:

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
```

6. Push the root branch, then push the tag:

```bash
git push origin main
git push origin vX.Y.Z
```

## Verification

```bash
git status --short --branch
git -C website status --short --branch
git tag --list 'v*' --sort=-version:refname | sed -n '1,5p'
git ls-remote --tags origin vX.Y.Z
```

Do not commit, tag, or push without explicit user authorization.
