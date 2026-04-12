# Website Agent Skill Install Docs Design

## Goal

Update the yeet docs website so people can discover the public `yeet` agent
skill repo and install it with `npx skills`.

The docs change should explain the skill in the place where install options
already live, while keeping the normal yeet CLI install flow clearly distinct.

## Context

The current docs site already has the right information architecture for this
change:

- docs home: `website/docs/index.mdx`
- install page: `website/docs/getting-started/installation.mdx`

The install page already explains:

- release install for the `yeet` CLI
- nightly install for the `yeet` CLI
- source build options
- host bootstrap with `yeet init`

That makes it the right canonical location for the agent skill install note.

The docs home page already acts as a discovery surface via the “Start here” and
“Documentation map” sections, so it only needs a small pointer rather than a
new top-level page.

## Requirements

### Functional

1. The website must tell readers that there is a public `yeet` agent skill
   repo.
2. The docs must show the `npx skills add yeetrun/skills --skill yeet` install
   command.
3. The docs must clearly distinguish:
   - installing the `yeet` CLI
   - installing the optional agent skill
4. The canonical location for this information must be the existing
   Installation page.
5. The docs home page must include a discoverability pointer that sends readers
   to Installation for the agent skill setup.

### Non-Goals

1. No new docs page for the skill repo.
2. No nav changes in `website/docs/nav.json`.
3. No broader “AI integrations” guide.
4. No attempt to document every editor or agent integration path.
5. No changes to the actual `yeet` install flow or host bootstrap flow.

## Recommended Approach

Use an integrated install-section approach.

Add a compact “Agent skill” section to the existing Installation page, and add
a smaller discovery pointer from the docs home page back to that install
section.

This is the right shape because:

- people already expect install variants on the Installation page
- the feature is discoverable without inflating the docs structure
- the docs stay focused on yeet instead of drifting into generic tool
  integration docs

## Installation Page Changes

Update `website/docs/getting-started/installation.mdx`
to add a short section after the local `yeet` install options and before the
“Install catch on a host” section.

That section should:

1. explain that there is a `yeet` agent skill repo for AI coding tools that
   support `npx skills`
2. show the install command:

```bash
npx skills add yeetrun/skills --skill yeet
```

3. clarify that this installs agent guidance for `yeet` and `catch` workflows,
   not the `yeet` binary itself
4. keep the explanation short and practical

The page should continue to present the CLI install as the default install
story. The agent skill should read as an optional companion tool, not a primary
path for getting started with yeet itself.

## Docs Home Page Changes

Update `website/docs/index.mdx`
to add a small discoverability pointer to the Installation page for the agent
skill setup.

The pointer should:

- be visible from the docs home page
- stay visually secondary to Quick Start and Installation
- direct readers to the existing Installation page instead of creating a new
  docs destination

This can be implemented as:

- a small note near the “Start here” section, or
- an extra card/link in the documentation map area

The key requirement is discoverability, not a new IA branch.

## Wording Rules

The copy should:

- distinguish “installing the CLI” from “installing the agent skill”
- frame the skill as optional tooling for agents/editors that support
  `npx skills`
- avoid broad compatibility promises beyond the documented install command
- stay practical rather than promotional

Target shape:

- one short paragraph
- one command block
- one short home-page pointer

## Maintenance

Treat the Installation page as the canonical docs location for this feature.

If the agent skill install command changes later, update:

1. the Installation page first
2. the smaller home-page pointer only if its wording also needs to change

That keeps the docs home page light while preserving one obvious source of
truth for the command itself.

## Verification

Acceptance should include:

1. confirm the edited MDX files remain syntactically valid
2. confirm the `npx skills add yeetrun/skills --skill yeet` command is spelled
   correctly
3. confirm the docs wording clearly distinguishes CLI install vs skill install
4. confirm the docs home page points readers to Installation for this feature

If practical during implementation, also run the website locally to ensure the
new section renders cleanly.

## Acceptance Criteria

This design is satisfied when:

1. the Installation page includes an agent skill section with the `npx skills`
   install command
2. the copy makes clear that the skill install is optional and separate from
   installing the `yeet` CLI
3. the docs home page includes a small discoverability pointer to Installation
   for the agent skill
4. no new page or nav entry is introduced
