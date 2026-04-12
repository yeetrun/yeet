# Website Agent Skill Install Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add public docs for the `yeet` agent skill install flow to the website without creating a new page or changing the docs navigation.

**Architecture:** Keep the Installation page as the canonical location for the `npx skills` command, then add a smaller discoverability pointer on the docs home page that routes readers back to Installation. The change stays limited to existing MDX docs pages and one website build validation pass.

**Tech Stack:** MDX, Next.js docs site, npm scripts

---

## File Map

- Modify: `website/docs/getting-started/installation.mdx`
  - Add the canonical “Agent skill (optional)” install section.
- Modify: `website/docs/index.mdx`
  - Add a small home-page pointer to Installation for the skill install.
- Use for validation only: `website/package.json`
  - Source for the build command used to verify MDX still renders.

### Task 1: Add The Canonical Agent Skill Section To Installation

**Files:**
- Modify: `website/docs/getting-started/installation.mdx`

- [ ] **Step 1: Write the failing content check**

Run:

```bash
rg -n "Agent skill|npx skills add yeetrun/skills --skill yeet" website/docs/getting-started/installation.mdx
```

Expected: no matches, because the install page does not mention the skill yet.

- [ ] **Step 2: Run the check to verify it fails**

Run:

```bash
rg -n "Agent skill|npx skills add yeetrun/skills --skill yeet" website/docs/getting-started/installation.mdx
```

Expected: exit code `1`.

- [ ] **Step 3: Add the minimal installation-page section**

Insert this block after the existing `go install ./cmd/yeet` example and before
`## Install catch on a host` in `website/docs/getting-started/installation.mdx`:

```mdx
## Agent skill (optional)

If you use an AI coding tool that supports `npx skills`, you can install the
public `yeet` skill repo for product guidance, workflow selection, and
troubleshooting help:

```bash
npx skills add yeetrun/skills --skill yeet
```

This installs agent guidance for `yeet` and `catch` workflows. It does **not**
install the `yeet` CLI binary itself.
```

- [ ] **Step 4: Run the check to verify it passes**

Run:

```bash
rg -n "Agent skill \(optional\)|npx skills add yeetrun/skills --skill yeet|does \*\*not\*\* install the `yeet` CLI binary itself" website/docs/getting-started/installation.mdx
```

Expected: three matches from the new section.

- [ ] **Step 5: Commit**

Run:

```bash
git add website/docs/getting-started/installation.mdx
git commit -m "website: add agent skill install docs"
```

Expected: a commit is created with the new install-page section. If the repo
hooks update `cmd/yeet/depaware.txt` or `cmd/catch/depaware.txt`, stage those
generated files and rerun the same commit command.

### Task 2: Add The Docs Home Pointer And Validate The Site Build

**Files:**
- Modify: `website/docs/index.mdx`

- [ ] **Step 1: Write the failing content check**

Run:

```bash
rg -n "npx skills|agent skill repo install" website/docs/index.mdx
```

Expected: no matches, because the docs home page does not point to the skill
install yet.

- [ ] **Step 2: Run the check to verify it fails**

Run:

```bash
rg -n "npx skills|agent skill repo install" website/docs/index.mdx
```

Expected: exit code `1`.

- [ ] **Step 3: Add the minimal home-page pointer**

Insert this note after the `ButtonLinks` block in `website/docs/index.mdx` and
before `## Documentation map`:

```mdx
> [!TIP]
> If you use an AI coding tool that supports `npx skills`, see
> [Installation](/docs/getting-started/installation) for the optional `yeet`
> agent skill repo install.
```

- [ ] **Step 4: Verify the new pointer and build the website**

Run:

```bash
rg -n "npx skills|agent skill repo install|/docs/getting-started/installation" website/docs/index.mdx
cd website && npm run build:next
```

Expected:

- the `rg` command returns matches for the new note
- `npm run build:next` exits `0`

- [ ] **Step 5: Commit**

Run:

```bash
git add website/docs/index.mdx
git commit -m "website: add docs home pointer for agent skill"
```

Expected: a commit is created with the docs home-page pointer. If the repo
hooks update `cmd/yeet/depaware.txt` or `cmd/catch/depaware.txt`, stage those
generated files and rerun the same commit command.
