---
name: yeet-quality
description: Use when choosing verification commands, working on tests, quality tooling, race/fuzz/mutation checks, pre-commit failures, or release-quality gates.
---

# Yeet Quality

Use this skill to choose verification depth.

## Normal Flow

- Run targeted package tests while developing.
- Run `mise exec -- go test ./... -count=1` before broad completion.
- Run `mise exec -- pre-commit run --all-files` before commits that change
  code, tooling, docs examples, release surfaces, or agent context.

## Heavy Checks

```bash
mise run quality
mise run race
mise run fuzz
mise run mutation
mise run quality:goal
```

Use `mise run quality:goal` before releases and after meaningful
quality-tooling, parser, RPC, concurrency, or service-orchestration changes.

## Rules

- Do not set or manage `GOCACHE`.
- Do not refresh baselines just to get green.
- Treat race detector findings as bugs unless proven otherwise.
- Commit minimized fuzz corpus files for fuzz-discovered bugs.
