---
name: yeet
description: Homelab service manager and public manual for deploying services over Tailscale RPC.
colors:
  brand-green: "#38e17b"
  brand-green-hover: "#2ac36b"
  accent-cyan: "#4fd6d3"
  surface-root: "#0f0f11"
  surface-panel: "#16161a"
  surface-raised: "#242428"
  surface-border: "#34343a"
  text-muted: "#9ea1a8"
  text-soft: "#c0c4cc"
  text-readable: "#d7dbe2"
  text-bright: "#eaedf2"
  text-strong: "#f7f8fb"
  code-bg: "#101216"
  code-fg: "#e9eef5"
  syntax-blue: "#6aaed6"
  syntax-green: "#8bd5a2"
  syntax-yellow: "#f0c87a"
  syntax-purple: "#b89ae0"
  syntax-red: "#e6857b"
  syntax-orange: "#e1a36b"
  syntax-teal: "#69b9b3"
typography:
  display:
    fontFamily: "Space Grotesk, ui-sans-serif, system-ui, sans-serif"
    fontSize: "clamp(36px, 5vw, 56px)"
    fontWeight: 500
    lineHeight: 1.2
    letterSpacing: "0"
  headline:
    fontFamily: "Space Grotesk, ui-sans-serif, system-ui, sans-serif"
    fontSize: "32px"
    fontWeight: 500
    lineHeight: 1.12
    letterSpacing: "0"
  title:
    fontFamily: "Space Grotesk, ui-sans-serif, system-ui, sans-serif"
    fontSize: "18px"
    fontWeight: 500
    lineHeight: 1.2
    letterSpacing: "0"
  body:
    fontFamily: "Source Sans 3, ui-sans-serif, system-ui, sans-serif"
    fontSize: "16px"
    fontWeight: 300
    lineHeight: 1.6
    letterSpacing: "0"
  label:
    fontFamily: "Source Sans 3, ui-sans-serif, system-ui, sans-serif"
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.4
    letterSpacing: "0.08em"
  code:
    fontFamily: "IBM Plex Mono, ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, Liberation Mono, Courier New, monospace"
    fontSize: "16px"
    fontWeight: 400
    lineHeight: 1.55
    letterSpacing: "0"
rounded:
  inline-code: "4px"
  copy-button: "6px"
  code-block: "8px"
  control: "10px"
  brand-mark: "12px"
  card: "16px"
  panel: "18px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "16px"
  lg: "24px"
  xl: "32px"
  section: "72px"
  hero-top: "96px"
components:
  button-primary:
    backgroundColor: "{colors.brand-green}"
    textColor: "{colors.surface-root}"
    rounded: "{rounded.control}"
    padding: "10px 34px"
    typography: "{typography.body}"
  button-primary-hover:
    backgroundColor: "{colors.brand-green-hover}"
    textColor: "{colors.surface-root}"
    rounded: "{rounded.control}"
    padding: "10px 34px"
  button-neutral:
    backgroundColor: "{colors.surface-panel}"
    textColor: "{colors.text-bright}"
    rounded: "{rounded.control}"
    padding: "10px 34px"
    typography: "{typography.body}"
  code-block:
    backgroundColor: "{colors.code-bg}"
    textColor: "{colors.code-fg}"
    rounded: "{rounded.code-block}"
    padding: "16px"
    typography: "{typography.code}"
  card:
    backgroundColor: "{colors.surface-panel}"
    textColor: "{colors.text-soft}"
    rounded: "{rounded.card}"
    padding: "20px"
  hero-panel:
    backgroundColor: "{colors.surface-panel}"
    textColor: "{colors.text-soft}"
    rounded: "{rounded.panel}"
    padding: "24px"
---

# Design System: yeet

## 1. Overview

**Creative North Star: "The Maintainer's Workbench"**

The yeet website should feel like infrastructure documentation built by people
who operate the system themselves: dark, focused, command-forward, and
deliberately terse. It should make remote hosts, Tailscale RPC, containers, VMs,
ZFS, and networking feel tractable without making them look trivial.

The visual system is restrained and utilitarian. Pages can use a small amount
of green and cyan to signal freshness and speed, but examples, docs structure,
and copy-pasteable commands do the real work. Avoid generic SaaS persuasion,
toy-dashboard energy, and layouts that turn operational examples into clipped
decoration.

**Key Characteristics:**

- Dark workbench surface with crisp green actions and cool cyan support accents.
- Code examples are first-class content, given enough width to be read.
- Cards are used for discrete choices or links, not as a default page texture.
- Public copy is maintainer-plain, accurate, and written from the user's point
  of view.

## 2. Colors

The palette is dark neutral infrastructure with one decisive green action color
and a restrained cyan secondary accent.

### Primary

- **Catch Green** (`#38e17b`): Primary actions, brand mark fill, selected
  states, and the rare strongest accent on a page.
- **Catch Green Hover** (`#2ac36b`): Hover state for primary buttons.

### Secondary

- **Tailnet Cyan** (`#4fd6d3`): Secondary emphasis, diagrams, comparison
  accents, and supporting highlights. Use less often than green.

### Neutral

- **Root Blackened Surface** (`#0f0f11`): Page background and navbar/footer
  base.
- **Panel Charcoal** (`#16161a`): Cards, panels, footer, and raised sections.
- **Border Charcoal** (`#242428`): Default borders and dividers.
- **Readable Gray** (`#d7dbe2`): Strong body text and label text.
- **Bright Text** (`#f7f8fb`): Headings and high-emphasis text.
- **Muted Text** (`#9ea1a8`): Secondary explanatory copy.
- **Code Surface** (`#101216`): Code block background.
- **Code Text** (`#e9eef5`): Code block foreground.

### Named Rules

**The One Accent Rule.** Green is the action color. Cyan can support diagrams
and highlights, but do not create a multi-neon terminal palette.

**The Tinted Neutral Rule.** Use the existing gray scale for surfaces. Do not
introduce pure black or pure white as new UI colors even though legacy tokens
exist.

## 3. Typography

**Display Font:** Space Grotesk with system sans-serif fallback.
**Body Font:** Source Sans 3 with system sans-serif fallback.
**Label/Mono Font:** IBM Plex Mono for commands and code.

**Character:** The pairing is technical but not retro. Space Grotesk gives the
site a modern product identity, Source Sans 3 keeps docs readable, and IBM Plex
Mono makes commands feel precise.

### Hierarchy

- **Display** (500, `clamp(36px, 5vw, 56px)`, 1.2): Hero headings and major
  first-viewport statements only.
- **Headline** (500, `32px`, 1.12): Section headings and major landing-page
  modules.
- **Title** (500, `18px`, 1.2): Card titles, workflow names, compact panels.
- **Body** (300 to 400, `16px`, 1.6): Documentation and explanatory copy. Keep
  long prose around 65 to 75 characters per line.
- **Label** (400, `13px`, 0.08em letter spacing): Eyebrows and small uppercase
  labels. Use sparingly.
- **Code** (400 to 500, `16px`, 1.55): Commands and configuration. Prefer
  full-width blocks over cramped inline examples.

### Named Rules

**The Command Width Rule.** Code examples must be readable at common laptop
widths. Redesign the layout before shrinking command text below 13px or hiding
the important part behind horizontal scroll.

## 4. Elevation

Yeet uses a hybrid of tonal layering, borders, and a small number of ambient
shadows. Surfaces should be flat at rest unless they are a hero panel, a large
media frame, or an interactive link card responding to hover.

### Shadow Vocabulary

- **Hero Panel Shadow** (`0 20px 50px rgba(0, 0, 0, 0.4)`): Large first-viewport
  code or command panels.
- **Media Frame Shadow** (`0 22px 50px rgba(0, 0, 0, 0.38)`): Screenshots and
  product media.
- **Card Shadow** (`0 12px 30px rgba(0, 0, 0, 0.25)`): Feature cards when a
  section needs separation from the page background.
- **Hover Lift Shadow** (`0 16px 30px rgba(0, 0, 0, 0.35)`): Link cards on
  hover.

### Named Rules

**The Flat-By-Default Rule.** Borders and tonal changes are the normal depth
cue. Shadows are reserved for high-value framing or interaction.

## 5. Components

### Buttons

- **Shape:** Rounded rectangle with 10px radius.
- **Primary:** Catch Green background, Root Blackened Surface text, 10px 34px
  large padding.
- **Hover / Focus:** Primary hover changes to Catch Green Hover and may lift by
  1px. Maintain visible keyboard focus.
- **Neutral:** Panel Charcoal background, Border Charcoal stroke, Bright Text.

### Chips

- **Style:** Use chips for compact state or category labels only. Backgrounds
  should be tinted neutral or a low-alpha green wash, with 1px borders.
- **State:** Selected chips may use green text or border. Avoid saturated filled
  chip groups unless they are actual controls.

### Cards / Containers

- **Corner Style:** 16px for normal cards, 18px for hero panels, 8px for code
  and media frames.
- **Background:** Panel Charcoal on Root Blackened Surface.
- **Shadow Strategy:** Follow Elevation. Most cards need only a border.
- **Border:** 1px solid Border Charcoal or Surface Border.
- **Internal Padding:** 20px for normal cards, 24px for larger panels.

### Inputs / Fields

- **Style:** Dark neutral surface, 1px border, 10px radius.
- **Focus:** Use a clear border shift or green outline. Do not rely on glow
  alone.
- **Error / Disabled:** Pair color with text. Do not communicate state through
  red/yellow alone.

### Navigation

- **Desktop:** Sticky dark navbar with light blur, 72px height, simple links,
  and one primary "Get Started" call to action.
- **Mobile:** Replace links with the existing hamburger menu. Keep touch targets
  comfortable and avoid dense multi-column nav content.
- **Footer:** Repeat primary links and use AUTHORS attribution for copyright.

### Code Blocks

- **Style:** Code Surface background, Code Text foreground, 8px radius, 1px
  border, 16px padding.
- **Behavior:** Include copy affordance. Preserve horizontal scroll only for
  genuinely long commands, not because the surrounding layout is too narrow.
- **Homepage Use:** Prefer one prominent command block at a time over grids of
  cramped examples.

## 6. Do's and Don'ts

### Do:

- **Do** lead first-run flows with `yeet init root@<machine-host>` and explain
  optional flags after the normal path.
- **Do** give command examples enough width to be read without guessing.
- **Do** distinguish machine hosts, catch hosts, Tailscale tags, VM capability,
  LAN networking, and ZFS in copy where it matters.
- **Do** use green for primary actions and cyan only as a secondary support
  accent.
- **Do** keep cards purposeful: docs links, workflow choices, repeated payload
  types, or framed tools.

### Don't:

- **Don't** lead public onboarding with unattended install flags unless the
  section is explicitly about automation.
- **Don't** use identical four-card command grids when commands are too long for
  the available width.
- **Don't** write public docs from an internal maintainer perspective, such as
  "before publishing a public image" when the user story is "run your own image."
- **Don't** add purple-blue gradient marketing themes, neon terminal cliches,
  glassmorphism, decorative blobs, or side-stripe accent cards.
- **Don't** shrink command text below readability thresholds to make a layout
  work. Change the layout.
