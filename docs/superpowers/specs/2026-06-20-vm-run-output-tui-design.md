# VM Run Output TUI Design

## Goal

Make `yeet run <name> vm://...` feel as polished and informative as Docker
Compose deploys in an interactive terminal while preserving the existing
script-friendly output contract for non-TTY use.

VM provisioning is usually fast, but the current output is a sequence of plain
lines. Users should be able to see what yeet is doing, how long it has been
waiting, which steps completed, and what commands to run next.

## Current Behavior

Docker Compose deploys look good because catch runs Docker Compose inside the
remote PTY. Docker Compose renders its own progress UI, while yeet's `runUI`
wraps higher-level upload, detection, and install phases.

VM deploys already request a TTY from the local client, but the VM provisioner
writes ad hoc text through `vmProgressf`, for example:

```text
VM hermes
Image: vm://ubuntu/26.04 (ubuntu-26.04-amd64-v15)
Shape: 2 vCPU, 2.0 GB memory, 64.0 GB disk
Network: svc,lan
Preparing disk...
Injecting guest metadata...
Writing Firecracker config...
Configuring network...
Installing VM service...
Starting VM...
Waiting for guest readiness...
VM hermes is running.
SSH: yeet ssh hermes
Console: yeet vm console hermes
```

Non-TTY Docker deploys intentionally do not stream Docker Compose's TTY UI.
They use stable structured lines such as:

```text
action=run service=api@yeet-lab status=running step="Upload payload"
action=run service=api@yeet-lab status=ok step="Upload payload" detail="701.00 B @ 1.90 KB/s"
```

VM output should follow the same split:

- TTY mode: polished, interactive, human output.
- Plain/non-TTY mode: stable structured progress lines.
- Quiet mode: suppress progress output except errors.

## Architecture

Catch remains the source of truth for deploy progress because catch performs
the VM work: image selection, disk preparation, metadata injection, Firecracker
config generation, network setup, systemd installation, VM start, and guest
readiness checks.

The local `yeet` client should continue streaming remote command output through
the existing exec path. This keeps VM deploys aligned with Docker Compose
deploys and avoids a new RPC progress protocol for this first pass.

The core change is to move VM provisioning from direct progress printing to a
structured progress interface backed by `runUI`. This gives VM provisioning the
same TTY/plain/quiet behavior as Docker deploys and leaves room for future
typed progress events if web deploys or external subscribers need them later.

## Progress Model

VM provisioning reports named steps with optional detail and elapsed time:

- Resolve VM plan
- Download VM image, only when an image is missing or being updated
- Prepare disk
- Inject guest metadata
- Write Firecracker config
- Configure network
- Install VM service
- Start VM
- Wait for guest readiness

Completed steps keep a final detail when useful, such as image version, disk
size, network mode, or guest IP. Running steps update in place in TTY mode and
include an elapsed timer.

The provisioner should not parse its own printed text. Steps should be reported
through explicit calls so tests can assert behavior without depending on string
scraping.

## TTY Output

Interactive output should use the existing braille spinner, checkmarks, color,
and cursor cleanup conventions from `pkg/tui` and `runUI`.

Example success output:

```text
[+] yeet run hermes@yeet-lab

✔ Resolve VM plan (Ubuntu 26.04, cached)
✔ Prepare disk (64 GB)
✔ Inject guest metadata
✔ Write Firecracker config
✔ Configure network (svc,lan)
✔ Install VM service
✔ Start VM
✔ Wait for guest readiness (10.0.4.200)
✔ VM ready in 12.4s

hermes@yeet-lab
Image    Ubuntu 26.04
Shape    2 vCPU, 2 GB memory, 64 GB disk
Network  svc,lan

SSH      yeet ssh hermes
Console  yeet vm console hermes
```

Example running line:

```text
⠹ Wait for guest readiness 7.2s
```

The final commands should stand out through alignment and spacing rather than
heavy decoration. They should be easy to copy visually and should match the
actual service name.

## Plain And Quiet Output

Plain output remains structured and stable:

```text
action=run service=hermes@yeet-lab status=running step="Wait for guest readiness"
action=run service=hermes@yeet-lab status=ok step="Wait for guest readiness" detail=10.0.4.200
action=run service=hermes@yeet-lab status=info detail="SSH yeet ssh hermes"
action=run service=hermes@yeet-lab status=info detail="Console yeet vm console hermes"
```

Quiet mode suppresses progress and final command hints, preserving errors.

## Failure Behavior

On failure, the active step should stop and print a failed status with the
error detail. Completed steps should remain visible.

If failure happens after the VM has been committed but guest readiness fails,
yeet should print recovery-oriented next steps instead of pretending the VM did
not exist:

```text
✖ Wait for guest readiness (timeout)

VM service was created, but readiness did not complete.

Console  yeet vm console hermes
Logs     yeet logs hermes
```

Plain mode should express the same state through structured `err` and `info`
lines.

## Boundaries

This first pass should not add a new RPC progress protocol. The output still
travels over the existing exec stream.

This first pass should not change Docker Compose's native TTY output. Compose
continues to own its own progress UI when running in a PTY.

This first pass should not change web deploy terminal streaming. Improvements
to browser-side rendering can build on the structured progress model later.

## Testing

Add focused tests around catch-side rendering and VM provisioning:

- TTY VM run output includes step status, elapsed detail, summary footer, and
  next commands.
- Plain VM run output emits stable `action=... status=... step=...` lines.
- Quiet mode suppresses VM progress and footer output.
- Readiness failure after commit prints console/log recovery commands.
- VM image download progress continues to render through `ProgressUI`.
- Existing Docker Compose TTY and non-TTY behavior remains unchanged.

Use injectable timer/spinner seams so tests do not depend on wall-clock timing
or a real terminal.
