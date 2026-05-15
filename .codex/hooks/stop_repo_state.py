#!/usr/bin/env python3
# Copyright (c) 2026 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

"""Stop hook for repo-state and release checklist sanity checks.

This file is written as a small literate program: the comments describe the
policy first, then the Python implements it. The hook has one job at the end of
a Codex turn: compare the final assistant message with observable repository
state. If the assistant says something is done, clean, pushed, tagged, or
released, this hook asks git whether that claim is true.

The hook is intentionally narrow. It does not run tests, it does not decide
whether ordinary unfinished work is acceptable, and it does not block final
answers that accurately say work is staged or uncommitted. It only interrupts
when the final answer would otherwise overstate the state of the repository or
release.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


# Section 1: recognizing claims in prose.
#
# A Stop hook sees natural language, not a structured release object. The first
# problem is therefore rhetorical: distinguish "I did tag v0.2.9" from "the
# hook checks tagged v0.2.9 examples". These regular expressions are biased
# toward explicit final-status claims and away from documentation-like prose.
VERSION_RE = re.compile(r"\bv\d+\.\d+\.\d+\b")
EXPLANATORY_LINE_RE = re.compile(
    r"\b("
    r"checks?|checking|validates?|validation|looks? for|"
    r"for example|such as|examples?|tests?|"
    r"claims?|would block|should block|the hook|the script|"
    r"if the final answer|when the final answer"
    r")\b"
)
COMMITTED_CLAIM_RE = re.compile(
    r"\b(everything|all changes|changes|work|it|this|root|website|submodule)\s+"
    r"(is|are|'s|was|were)?\s*(committed|checked in)\b"
)
PUSHED_CLAIM_RE = re.compile(
    r"\b(everything|all changes|changes|work|it|this|root|website|submodule|main|tag|release)\s+"
    r"(is|are|'s|was|were)?\s*(pushed|published)\b"
)
COMMITTED_AND_PUSHED_RE = re.compile(r"\b(committed and pushed|pushed and committed)\b")
CLEAN_CLAIM_RE = re.compile(
    r"\b(working tree|worktree|repo|repository|root repo|root repository|website submodule|submodule|git status|status)\s+"
    r"(is|are|'s|was|were)?\s*(clean|up[- ]to[- ]date)\b"
)
TAGGED_VERSION_RE = re.compile(r"\b(tagged|tag|release|released|published)\s+v\d+\.\d+\.\d+\b")
VERSION_STATUS_RE = re.compile(r"\bv\d+\.\d+\.\d+\b\s+(is|was|has been)?\s*(tagged|released|published|pushed)\b")


@dataclass(frozen=True)
class GitSync:
    """The upstream relationship for one git repository.

    Git gives us two numbers for ``@{upstream}...HEAD``: how many commits the
    local branch is behind and how many commits it is ahead. Absence of an
    upstream is represented explicitly because a "pushed" claim cannot be
    verified without one.
    """

    ahead: int = 0
    behind: int = 0
    has_upstream: bool = True


def main() -> int:
    """Read the Codex hook payload, check this repo, and emit hook JSON.

    Codex treats a zero exit with no output as "continue". When issues exist we
    still exit zero, but print a ``decision: block`` payload. For Stop hooks
    that means "continue the agent loop with this reason" rather than "abort the
    process".
    """

    try:
        payload = json.load(sys.stdin)
    except json.JSONDecodeError:
        return 0

    cwd = Path(payload.get("cwd") or os.getcwd())
    root = git_root(cwd)
    if root is None or not (root / "go.mod").exists():
        return 0

    message = payload.get("last_assistant_message") or ""
    issues = stop_issues(root, message)
    if not issues:
        return 0

    reason = "Repo Stop hook found state that should be resolved or stated before finishing:\n"
    reason += "\n".join(f"- {issue}" for issue in issues)
    print(json.dumps({"decision": "block", "reason": reason}))
    return 0


def stop_issues(root: Path, message: str) -> list[str]:
    """Collect contradictions between the final answer and git state.

    The shape of the check is:

    1. Reduce the final answer to only lines that look like state assertions.
    2. Measure the root repository and website submodule.
    3. Compare clean/commit/push claims against that measured state.
    4. Run the heavier release checklist only when a concrete release claim is
       present, or when HEAD itself is tagged.

    The staged-hook use case matters here: "changes are staged; no commit made"
    is an honest final answer and should pass, even though the working tree is
    not clean.
    """

    claim_message = state_claim_text(message)
    lower = claim_message.lower()
    issues: list[str] = []

    # The root repo and the website submodule are both release surfaces. A root
    # push claim checks the root branch. A website push claim checks the
    # submodule separately; a plain "website is clean" claim should not require
    # the normally detached submodule checkout to have an upstream branch.
    root_status = git_lines(root, "status", "--porcelain")
    root_sync = git_sync(root)
    website = root / "website"
    website_status = git_lines(website, "status", "--porcelain") if website.exists() else []
    website_sync = git_sync(website) if website.exists() else GitSync()

    # Negative statements win. The final answer may truthfully say "not pushed"
    # or "staged only"; such wording should suppress the stricter claim checks.
    claims_clean = claims_clean_state(lower)
    claims_commit = claims_committed(lower)
    claims_push = claims_pushed(lower)
    mentions_dirty = claims_any(lower, ("dirty", "uncommitted", "not committed", "staged only", "not pushed"))

    if root_status and (claims_clean or claims_commit or claims_push) and not mentions_dirty:
        issues.append("root working tree is not clean, but the final answer claims committed/pushed/clean state")
    if website_status and (claims_clean or claims_commit or claims_push) and "website" not in lower and "submodule" not in lower:
        issues.append("website submodule has uncommitted changes that are not mentioned")
    if claims_push and (root_sync.ahead > 0 or root_sync.behind > 0 or not root_sync.has_upstream):
        issues.append(sync_issue("root branch", root_sync))
    if claims_website_pushed(claim_message) and website.exists():
        if website_sync.has_upstream and (website_sync.ahead > 0 or website_sync.behind > 0):
            issues.append(sync_issue("website branch", website_sync))
        elif not website_sync.has_upstream and not commit_reachable_from_origin(website):
            issues.append("website commit is not reachable from a fetched origin branch")

    # The release checklist is expensive in attention, not CPU. Only run it for
    # a real release assertion or an actual tag on HEAD. A bare version in an
    # explanation is not enough.
    versioned_release_claim = claims_release_or_tag(lower)
    release_versions = mentioned_versions(claim_message) if versioned_release_claim else set()
    head_versions = set(git_lines(root, "tag", "--points-at", "HEAD", "--list", "v*"))
    release_trigger = head_versions or versioned_release_claim
    if release_trigger:
        issues.extend(release_issues(root, website, sorted(release_versions | head_versions), message, root_status, root_sync, website_status, website_sync))

    return unique_issues(issues)


def unique_issues(issues: list[str]) -> list[str]:
    """Preserve first-seen hook issues while removing repeated findings.

    Some claims intentionally pass through more than one policy gate. For
    example, "committed and pushed, tagged vX.Y.Z" is both a push claim and a
    release claim, and both gates care whether the root branch is still ahead of
    origin. The final Stop prompt should stay terse, so duplicated prose is
    collapsed at the boundary.
    """

    seen: set[str] = set()
    unique: list[str] = []
    for issue in issues:
        if issue in seen:
            continue
        seen.add(issue)
        unique.append(issue)
    return unique


def release_issues(
    root: Path,
    website: Path,
    versions: list[str],
    message: str,
    root_status: list[str],
    root_sync: GitSync,
    website_status: list[str],
    website_sync: GitSync,
) -> list[str]:
    """Validate the repository's patch-release contract.

    ``AGENTS.md`` defines the release ceremony for this repo: update the website
    changelog, commit and push website, commit the submodule pointer, create an
    annotated patch tag, then push both branch and tag. This function turns that
    ceremony into observable checks.
    """

    issues: list[str] = []
    if not versions:
        return ["release/tag was mentioned, but no vX.Y.Z version was found in the final answer or on HEAD"]

    # Multiple versions usually mean the assistant is explaining examples or has
    # conflated old and new releases. When it happens in claim text, force a
    # clearer final answer.
    version = latest_version(versions)
    if len(versions) > 1:
        issues.append(f"multiple release versions are in scope ({', '.join(versions)}); expected one")

    if root_status:
        issues.append("release checklist requires a clean root working tree")
    if root_sync.ahead > 0 or root_sync.behind > 0 or not root_sync.has_upstream:
        issues.append(sync_issue("root branch", root_sync))
    if website.exists():
        if website_status:
            issues.append("release checklist requires a clean website submodule")
        if website_sync.has_upstream and (website_sync.ahead > 0 or website_sync.behind > 0):
            issues.append(sync_issue("website branch", website_sync))
        elif not website_sync.has_upstream and not commit_reachable_from_origin(website):
            issues.append("website commit is not reachable from a fetched origin branch")
        changelog = website / "docs" / "changelog.mdx"
        if not changelog_contains_version(changelog, version):
            issues.append(f"website/docs/changelog.mdx does not include a {version} release heading")
    else:
        issues.append("website submodule is missing; release checklist cannot validate docs")

    if not local_tag_exists(root, version):
        issues.append(f"local tag {version} does not exist")
    elif not local_tag_is_annotated(root, version):
        issues.append(f"local tag {version} is not annotated")
    elif not tag_points_at_head(root, version):
        issues.append(f"local tag {version} does not point at HEAD")

    if not remote_tag_exists(root, version):
        issues.append(f"remote tag {version} is not pushed to origin")

    previous = previous_version_tag(root, version)
    if previous is not None and not is_patch_bump(previous, version):
        issues.append(f"{version} is not the next patch release after {previous}")

    if version not in message and claims_any(message.lower(), ("release", "tagged", "tag pushed")):
        issues.append(f"final answer mentions a release/tag but does not name {version}")

    return issues


def claims_any(lower: str, needles: tuple[str, ...]) -> bool:
    """Return whether any simple phrase appears in already-lowercased text."""

    return any(needle in lower for needle in needles)


def claims_committed(lower: str) -> bool:
    """Detect positive "committed" claims after honoring negations."""

    if claims_any(lower, ("not committed", "uncommitted", "no commit")):
        return False
    return bool(COMMITTED_CLAIM_RE.search(lower) or COMMITTED_AND_PUSHED_RE.search(lower))


def claims_pushed(lower: str) -> bool:
    """Detect positive "pushed" claims after honoring negations."""

    if claims_any(lower, ("not pushed", "unpushed", "no push")):
        return False
    if COMMITTED_AND_PUSHED_RE.search(lower):
        return True
    if PUSHED_CLAIM_RE.search(lower):
        return True
    return claims_any(lower, ("matches origin", "matches origin/main", "remote tag is present", "remote tag exists"))


def claims_website_pushed(message: str) -> bool:
    """Return whether a website/submodule line itself claims pushed state."""

    for line in message.lower().splitlines():
        if ("website" in line or "submodule" in line) and claims_pushed(line):
            return True
    return False


def claims_clean_state(lower: str) -> bool:
    """Detect claims that git status or branch sync is already clean."""

    return bool(CLEAN_CLAIM_RE.search(lower)) or claims_any(lower, ("matches origin", "matches origin/main"))


def claims_release_or_tag(lower: str) -> bool:
    """Detect concrete versioned release/tag assertions.

    The version requirement is deliberate. "The hook validates releases" is not
    a release claim; "Tagged v0.2.9" is.
    """

    if claims_any(lower, ("no release", "not released", "not tagged", "no tag")):
        return False
    return bool(TAGGED_VERSION_RE.search(lower) or VERSION_STATUS_RE.search(lower))


def state_claim_text(message: str) -> str:
    """Return only lines likely to assert final state.

    This is the prose sieve. A final answer can explain the hook, include code
    blocks, or list examples such as "Tagged v1.2.3"; those lines are not claims
    about the current repository. We discard fenced code and explanatory lines,
    and when an explanatory line introduces bullets, we discard the bullets too.
    """

    lines: list[str] = []
    in_fence = False
    skip_explanatory_bullets = False
    for raw_line in message.splitlines():
        line = raw_line.strip()
        if line.startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence or not line:
            if not line:
                skip_explanatory_bullets = False
            continue

        lower = line.lower()
        if EXPLANATORY_LINE_RE.search(lower):
            if line.endswith(":"):
                skip_explanatory_bullets = True
            continue
        if skip_explanatory_bullets and re.match(r"^[-*]\s+", line):
            continue
        if not re.match(r"^[-*]\s+", line):
            skip_explanatory_bullets = False
        lines.append(line)
    return "\n".join(lines)


def mentioned_versions(message: str) -> set[str]:
    """Extract semver-style release tags from already-filtered claim text."""

    return set(VERSION_RE.findall(message))


def changelog_contains_version(path: Path, version: str) -> bool:
    """Check the website changelog for the release heading style used here."""

    try:
        data = path.read_text(encoding="utf-8")
    except OSError:
        return False
    return f"### {version}" in data


def local_tag_exists(root: Path, version: str) -> bool:
    """Return whether the release tag exists locally."""

    return git_ok(root, "rev-parse", "--verify", "--quiet", f"refs/tags/{version}")


def local_tag_is_annotated(root: Path, version: str) -> bool:
    """Return whether the local tag is annotated rather than lightweight."""

    return git_output(root, "cat-file", "-t", f"refs/tags/{version}") == "tag"


def tag_points_at_head(root: Path, version: str) -> bool:
    """Return whether the release tag resolves to the current commit."""

    tag_commit = git_output(root, "rev-list", "-n", "1", version)
    head_commit = git_output(root, "rev-parse", "HEAD")
    return bool(tag_commit and head_commit and tag_commit == head_commit)


def remote_tag_exists(root: Path, version: str) -> bool:
    """Return whether origin advertises the release tag."""

    out = git_output(root, "ls-remote", "--tags", "origin", f"refs/tags/{version}")
    return bool(out.strip())


def commit_reachable_from_origin(root: Path) -> bool:
    """Return whether HEAD is contained in one of the fetched origin refs.

    Submodules are often checked out detached at the exact commit recorded by
    the parent repository. In that normal state there is no ``@{upstream}``.
    For release validation, the useful question is instead whether that detached
    commit has been fetched from origin, which proves it is not merely local.
    """

    return bool(git_lines(root, "branch", "-r", "--contains", "HEAD", "--list", "origin/*"))


def previous_version_tag(root: Path, version: str) -> str | None:
    """Find the greatest local release tag lower than ``version``."""

    tags = [tag for tag in git_lines(root, "tag", "--list", "v*", "--sort=version:refname") if tag != version]
    parsed = [(parse_version(tag), tag) for tag in tags]
    parsed = [(value, tag) for value, tag in parsed if value is not None]
    target = parse_version(version)
    if target is None:
        return None
    lower_tags = [(value, tag) for value, tag in parsed if value < target]
    if not lower_tags:
        return None
    return max(lower_tags, key=lambda item: item[0])[1]


def latest_version(versions: list[str]) -> str:
    """Choose the greatest semantic version from a candidate list."""

    parsed = [(parse_version(version), version) for version in versions]
    parsed = [(value, version) for value, version in parsed if value is not None]
    if not parsed:
        return versions[-1]
    return max(parsed, key=lambda item: item[0])[1]


def parse_version(version: str) -> tuple[int, int, int] | None:
    """Parse ``vMAJOR.MINOR.PATCH`` into sortable integers."""

    match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", version)
    if not match:
        return None
    return tuple(int(part) for part in match.groups())


def is_patch_bump(previous: str, current: str) -> bool:
    """Return whether ``current`` is exactly one patch after ``previous``."""

    prev = parse_version(previous)
    cur = parse_version(current)
    if prev is None or cur is None:
        return True
    return cur == (prev[0], prev[1], prev[2] + 1)


def sync_issue(label: str, sync: GitSync) -> str:
    """Render a branch sync problem as a compact hook issue."""

    if not sync.has_upstream:
        return f"{label} has no upstream branch"
    parts = []
    if sync.ahead:
        parts.append(f"ahead by {sync.ahead}")
    if sync.behind:
        parts.append(f"behind by {sync.behind}")
    return f"{label} is {' and '.join(parts)}"


def git_root(cwd: Path) -> Path | None:
    """Resolve the repository root for the hook cwd."""

    out = run_git(cwd, "rev-parse", "--show-toplevel")
    if out is None:
        return None
    return Path(out.strip())


def git_sync(root: Path) -> GitSync:
    """Measure branch divergence from the configured upstream."""

    out = run_git(root, "rev-list", "--left-right", "--count", "@{upstream}...HEAD")
    if out is None:
        return GitSync(has_upstream=False)
    parts = out.split()
    if len(parts) != 2:
        return GitSync(has_upstream=False)
    try:
        behind, ahead = int(parts[0]), int(parts[1])
    except ValueError:
        return GitSync(has_upstream=False)
    return GitSync(ahead=ahead, behind=behind)


def git_lines(root: Path, *args: str) -> list[str]:
    """Run git and split non-empty stdout lines."""

    out = run_git(root, *args)
    if not out:
        return []
    return [line for line in out.splitlines() if line.strip()]


def git_output(root: Path, *args: str) -> str:
    """Run git and return stdout, or an empty string on failure."""

    return run_git(root, *args) or ""


def git_ok(root: Path, *args: str) -> bool:
    """Return whether a git command exits successfully."""

    return run_git(root, *args) is not None


def run_git(root: Path, *args: str) -> str | None:
    """The only place this hook shells out to git.

    Failures are soft. A hook that cannot inspect one detail should report that
    detail through the higher-level checks instead of crashing Codex.
    """

    try:
        result = subprocess.run(
            ["git", *args],
            cwd=root,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=10,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    return result.stdout.strip()


if __name__ == "__main__":
    raise SystemExit(main())
