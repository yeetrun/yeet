# Web Terminal Container Fit Design

## Goal

Make the `yeet run --web` terminal behave like a normal terminal embedded in
the deploy panel:

- the terminal grid fits the panel's current content box;
- live output remains visible at the bottom once the grid fills;
- manual scrollback pauses bottom-follow until the user returns to live output;
- expand, collapse, and browser resizing refit the grid;
- retrying a deployment clears the previous attempt; and
- Ghostty retains its 1,000-line scrollback limit.

The browser terminal should render the same ordered byte stream as the terminal
app, but its rows and columns belong to the browser panel rather than the native
terminal window.

## Root Cause

The current adapter creates Ghostty with the native terminal profile and leaves
that geometry fixed. In the reproduced desktop layout, a 132-column by 44-row
profile creates a 1320-by-704-pixel canvas inside a 1154-by-126-pixel panel
content box.

The previous bottom-follow fix treats the panel as a second scroll viewport and
sets its `scrollTop` to 578. When the deployment has emitted only a few lines,
those lines remain at the top of Ghostty's 44-row screen while the browser shows
the blank rows at its bottom. The dual viewport is therefore the cause of both
the missing output and the visible canvas-to-panel seams.

## Chosen Approach

Use Ghostty's bundled `FitAddon` to make the panel content box authoritative for
browser rows and columns.

The adapter will:

1. create and open Ghostty using the streamed profile as a bootstrap;
2. immediately fit the terminal grid to the panel before returning the adapter;
3. observe the panel content box and refit when its dimensions change; and
4. keep the canvas and unused partial-cell area on the same background.

This is preferred over scaling the native canvas, which makes text tiny or
distorted, and over cropping the native canvas, which preserves the broken
dual-scroll model.

## Geometry Ownership

The streamed terminal profile remains required and validated. It establishes
the replay contract and gives Ghostty valid bootstrap dimensions, but it does
not remain the browser's display geometry.

After Ghostty opens, fitted dimensions are calculated from:

- the panel's current client width and height;
- the panel's padding;
- Ghostty's measured cell width and height; and
- the scrollbar reservation used by `FitAddon`.

Rows and columns are whole cells, so a small remainder can exist on the right or
bottom. The container and Ghostty canvas will use the same terminal background,
making that remainder visually continuous with the terminal surface.

The canvas must never be wider or taller than the panel's content box. The
panel will not provide horizontal or vertical content scrolling.

## Source Resize Events

Native terminal resize events remain ordered in the replay stream, but they no
longer impose native rows and columns on the browser renderer.

The adapter keeps each resize event as a queue barrier:

1. bytes before the event finish rendering;
2. the adapter refits to the current browser panel; and
3. bytes after the event render into that fitted browser grid.

This preserves event ordering while preventing a native resize from restoring
an oversized canvas.

## Follow Mode and Scrollback

Ghostty's internal viewport becomes the only scroll layer.

Follow mode is active when Ghostty is at its live viewport. Incoming bytes keep
the terminal at live output while follow mode is active.

When the user scrolls Ghostty upward, follow mode pauses. Incoming writes
preserve the same logical scrollback anchor, including when the 1,000-line
buffer is full. Returning Ghostty to its live viewport resumes following.

The adapter will remove its DOM `scrollTop`, outer-bottom detection, and outer
scroll listener. Wheel, touch, selection, and Ghostty's own scrollbar continue
to use Ghostty's existing input handling.

If the panel refits while the user is reading scrollback, the adapter preserves
the paused scrollback anchor. If follow mode is active, the refitted terminal
remains at live output.

## Expand, Collapse, and Browser Resize

The existing Expand control continues to change only the panel height. The
adapter's element observer notices the resulting content-box change and refits
Ghostty to the expanded or collapsed grid.

The same path handles responsive width changes and browser resizing. Resize
observation is disposed with the terminal adapter.

## Clear and Retry

Starting another deployment in the same form continues to dispose the old
adapter and remove its canvas before the new request begins.

The new terminal begins fitted to the current panel. A queued clear resets
Ghostty, clears selection and scrollback, re-enables follow mode, and leaves the
terminal fitted at live output.

## Failure Handling

Failure behavior remains explicit:

- invalid profiles or malformed resize events still degrade the browser replay;
- fitting a zero-sized or hidden panel is a no-op until a usable size is
  observed;
- terminal, write, resize, and fit failures permanently fail the adapter and
  settle drain waiters through the existing error path; and
- disposal cancels scheduled work and disconnects all subscriptions.

## Testing

The real-browser regression suite will prove:

- a native 132-by-44 profile is synchronously replaced by panel-fitted
  dimensions;
- the canvas stays within the panel content box with no outer overflow;
- short output is visible instead of scrolling to blank source rows;
- expand, collapse, and width changes refit rows and columns;
- a streamed native resize cannot restore native canvas dimensions;
- the terminal background covers partial-cell remainders without seams;
- live output follows Ghostty's bottom;
- manual Ghostty scrollback remains anchored across writes and panel refits;
- returning to live output resumes following;
- clear and retry reset output and follow state; and
- byte fidelity, selection copying, queue barriers, bounded writes, lifecycle
  drains, accessibility semantics, and the 1,000-line scrollback limit remain
  green.

No CLI syntax, RPC permission mapping, or public manual behavior changes.
