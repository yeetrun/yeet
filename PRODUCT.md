# Product

## Register

brand

## Users

Yeet is for capable homelab operators, independent developers, and small-team
infrastructure maintainers who are comfortable with SSH, Linux hosts, Docker,
Tailscale, and systemd. They are usually working from a personal workstation
against one or more remote Linux hosts they control, and they want to ship real
services without building a full platform around the deployment workflow.

The website and docs serve people evaluating whether yeet fits their operating
model, new users bootstrapping their first catch host, and returning users
looking for exact commands, upgrade paths, networking details, VM behavior, and
troubleshooting guidance.

## Product Purpose

Yeet is open source homelab infrastructure tooling. The `yeet` CLI runs on a
workstation, the `catch` daemon runs on Linux hosts, and together they deploy
containers, compose stacks, Dockerfiles, binaries, scripts, cron jobs, and
Firecracker-backed Ubuntu VMs over Tailscale/tsnet RPC.

The public site should make the product feel understandable and trustworthy
without hiding that it is opinionated. Success means a moderate homelab
sysadmin can quickly understand the model, prepare Tailscale correctly,
bootstrap a host with `yeet init`, run a first service, and find deeper docs
without fighting marketing language or overly dense examples.

## Brand Personality

Precise, pragmatic, infrastructure-native.

The voice should sound like a competent maintainer explaining the shortest safe
path through real infrastructure. It should be direct and specific, with enough
context to prevent footguns, but not padded with sales copy. Yeet can feel fast
and modern, but it should not pretend that operating remote hosts, VMs, ZFS,
networking, and Tailscale ACLs is magic.

## Anti-references

- Generic SaaS landing pages with inflated value props, hero metric templates,
  and vague claims.
- Decorative card grids that make command examples too narrow to read.
- Internal maintainer process leaking into public copy, such as phrases written
  only from the perspective of publishing official images.
- Toy homelab dashboards that make yeet look simpler than its operational
  contract.
- Purple-blue gradient marketing themes, neon terminal cliches, glassmorphism,
  decorative blobs, and excessive glow effects.
- Dense documentation that leads with unattended flags when the normal path is
  interactive and should stay simple.

## Design Principles

- Lead with the shortest honest path. First-run examples should show the normal
  interactive command before advanced flags.
- Show commands at usable width. A command example is part of the interface; if
  it clips or requires horizontal scrolling in a cramped card, redesign the
  layout.
- Teach the operating model early. Distinguish yeet, catch, machine hosts, catch
  hosts, Tailscale tags, and optional VM/ZFS capabilities in plain language.
- Keep public copy user-centered. Describe what users can run, configure, or
  recover, not the maintainers' internal release or testing process.
- Prefer durable clarity over visual novelty. The site should feel modern and
  deliberate, but the primary value is accurate onboarding for real systems.

## Accessibility & Inclusion

Target WCAG AA contrast for text, controls, and code blocks. Preserve keyboard
access for navigation, copy buttons, tabs, and any interactive examples. Respect
reduced-motion preferences. Avoid color-only meaning, especially for status and
networking concepts. Keep examples copy-pasteable, generic, and free of private
hostnames, local usernames, and infrastructure-specific assumptions.
