# Web Terminal Follow Mode Design

## Goal

Make the `yeet run --web` deploy terminal behave like a normal terminal:
live output stays pinned to the bottom, scrolling up pauses bottom-follow, and
returning to the bottom resumes it.

The fix must preserve the terminal profile received from the native terminal,
including its rows and columns, and must not change the existing 1,000-line
scrollback limit.

## Current Behavior and Root Cause

The durable web terminal has two viewport layers:

1. Ghostty renders the terminal profile into a fixed-size canvas and maintains
   terminal scrollback internally.
2. `.terminal-output` is a shorter DOM viewport with its own vertical overflow.

For a profile such as 132 columns by 44 rows, the Ghostty canvas is taller than
the collapsed 150-pixel terminal sheet. The outer DOM viewport starts at
`scrollTop = 0`, so the browser shows the top of the canvas and clips newer
rows below the sheet even though Ghostty is rendering them correctly.

Ghostty also moves its internal viewport to the bottom when new bytes are
written. Without coordination, that would make manual scrollback snap to live
output.

## Chosen Approach

Add a single follow-mode controller to the existing terminal adapter. It will
coordinate both Ghostty's internal scrollback viewport and the outer DOM
viewport while leaving terminal geometry unchanged.

Alternatives rejected:

- Always forcing both viewports to the bottom is simpler, but prevents users
  from reading scrollback while output is still arriving.
- Resizing the terminal rows to fit the collapsed sheet removes the outer
  clipping layer, but changes PTY geometry and can make output differ from the
  native terminal app.
- A CSS-only bottom crop can show the latest rows, but cannot preserve Ghostty's
  1,000-line interactive scrollback or reliably coordinate selection and
  scrollbar input.

## Follow State

The adapter starts in follow mode.

After Ghostty opens and creates its canvas, the adapter aligns both viewports
to their bottom positions before accepting user scroll state.

Follow mode is active only when both of these are true:

- Ghostty's internal viewport is at its live position.
- The outer DOM viewport is at its bottom, allowing a small pixel tolerance for
  browser rounding.

User scrolling in either layer pauses follow mode. Returning both layers to
their bottom positions resumes it automatically. This covers mouse wheel,
scrollbar, touch, and browser scrolling through the normal scroll events
exposed by Ghostty and the DOM viewport.

Programmatic scrolling performed by the adapter must not be mistaken for a user
request to change follow state.

## Writes

Before each queued Ghostty write, the adapter records whether follow mode is
active and captures the current internal and outer viewport positions.

After Ghostty renders the write:

- If follow mode was active, the adapter moves Ghostty to live output and
  scrolls the outer viewport to its bottom.
- If follow mode was paused, the adapter restores the internal scrollback
  position, adjusted for newly added scrollback lines, and leaves the outer DOM
  position unchanged.

Ghostty currently emits a temporary scroll-to-bottom event during a write.
The adapter ignores that event while applying the write so it cannot
accidentally re-enable follow mode before the prior scrollback position is
restored.

The existing write queue, byte ordering, batching, clear barriers, resize
barriers, and drain behavior remain unchanged.

## Resizing and Expand/Collapse

An element resize observer keeps a followed terminal aligned with the bottom
when the sheet expands, collapses, or changes size.

If follow mode is paused, resizing preserves the paused state and does not jump
to live output. Returning the terminal to both bottom positions resumes normal
following.

The observer and all scroll subscriptions are disposed with the adapter.

## Clear and Retry

Starting a second deploy from the same form clears the previous attempt before
the new stream can render output. The clear remains an ordered adapter barrier,
so bytes already queued for the old attempt cannot appear after bytes from the
new attempt.

Clearing resets both viewports to the bottom and re-enables follow mode. A new
deploy therefore begins with an empty terminal at live output rather than
inheriting transcript or scrollback state from the previous attempt.

## Testing

Extend the Playwright terminal regression suite to prove:

- a canvas taller than the collapsed sheet starts and remains pinned to the
  outer viewport bottom as output arrives;
- manual DOM scrolling pauses follow and returning to the bottom resumes it;
- manual Ghostty scrollback remains paused across incoming writes and resumes
  when returned to live output;
- expand/collapse preserves paused state and keeps followed output aligned;
- clear resets the terminal to followed live output;
- a failed deploy retried from the same form clears the first transcript before
  rendering the second deploy's output;
- existing byte fidelity, fixed geometry, 1,000-line scrollback, write
  batching, resize ordering, and lifecycle tests remain green.

This restores behavior already specified by the original web terminal design,
so no user manual or CLI help change is needed.
