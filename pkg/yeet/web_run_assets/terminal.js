// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

import { Ghostty, Terminal } from "./ghostty-web.js";

const maxWriteBatch = 64 * 1024;
let runtimePromise;

export function loadTerminalRuntime() {
  if (!runtimePromise) {
    runtimePromise = Ghostty.load(new URL("./ghostty-vt.wasm", import.meta.url).href);
  }
  return runtimePromise;
}

export async function createTerminalAdapter(element, profile) {
  const ghostty = await loadTerminalRuntime();
  const terminal = new Terminal({
    ghostty,
    cols: profile.cols,
    rows: profile.rows,
    scrollback: 1000,
    disableStdin: true,
    convertEol: false,
    cursorBlink: false,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
  });
  const viewportRole = element.getAttribute("role") || "log";
  const viewportLabel = element.getAttribute("aria-label") || "Deploy output";
  terminal.open(element);
  restoreViewportSemantics();

  const queue = [];
  const drainWaiters = [];
  let queueOffset = 0;
  let scheduledFrame = null;
  let writing = false;
  let disposed = false;
  let failure = null;
  let following = true;
  let suppressTerminalScroll = false;

  function elementAtBottom() {
    return element.scrollHeight - element.clientHeight - element.scrollTop <= 2;
  }

  function updateFollowState() {
    if (disposed || failure || suppressTerminalScroll) return;
    following = terminal.getViewportY() === 0 && elementAtBottom();
  }

  function alignFollowedViewport() {
    if (disposed || failure || !following) return;
    suppressTerminalScroll = true;
    try {
      terminal.scrollToBottom();
      element.scrollTop = element.scrollHeight;
    } finally {
      suppressTerminalScroll = false;
    }
  }

  function terminalScrollbackPosition() {
    return terminal.wasmTerm?.getNativeScrollbackLength?.() ?? terminal.getScrollbackLength();
  }

  function restorePausedTerminalViewport(viewportY, scrollbackPosition) {
    if (viewportY <= 0) return;
    const currentScrollbackLength = terminal.getScrollbackLength();
    const growth = Math.max(0, terminalScrollbackPosition() - scrollbackPosition);
    terminal.scrollToLine(Math.min(currentScrollbackLength, viewportY + growth));
  }

  function restoreViewportSemantics() {
    element.setAttribute("tabindex", "0");
    element.setAttribute("role", viewportRole);
    element.setAttribute("aria-label", viewportLabel);
    element.removeAttribute("contenteditable");
    element.removeAttribute("aria-multiline");
  }

  function settleDrains() {
    if (!disposed && (scheduledFrame !== null || writing || queue.length > 0)) return;
    const waiters = drainWaiters.splice(0);
    for (const waiter of waiters) {
      if (failure) waiter.reject(failure);
      else waiter.resolve();
    }
  }

  function fail(error) {
    if (!failure) failure = error;
    if (scheduledFrame !== null) {
      cancelAnimationFrame(scheduledFrame);
      scheduledFrame = null;
    }
    queue.length = 0;
    queueOffset = 0;
    writing = false;
    settleDrains();
    return failure;
  }

  function takeBatch() {
    let size = 0;
    for (let index = 0; index < queue.length && size < maxWriteBatch; index += 1) {
      const chunk = queue[index];
      if (!(chunk instanceof Uint8Array)) break;
      const offset = index === 0 ? queueOffset : 0;
      size += Math.min(chunk.length - offset, maxWriteBatch - size);
    }
    const batch = new Uint8Array(size);
    let batchOffset = 0;
    while (batchOffset < batch.length) {
      const chunk = queue[0];
      const available = chunk.length - queueOffset;
      const length = Math.min(available, batch.length - batchOffset);
      batch.set(chunk.subarray(queueOffset, queueOffset + length), batchOffset);
      batchOffset += length;
      queueOffset += length;
      if (queueOffset === chunk.length) {
        queue.shift();
        queueOffset = 0;
      }
    }
    return batch;
  }

  function flush() {
    scheduledFrame = null;
    if (disposed || failure || writing || queue.length === 0) {
      settleDrains();
      return;
    }
    const next = queue[0];
    if (!(next instanceof Uint8Array)) {
      queue.shift();
      try {
        if (next.type === "resize") {
          terminal.resize(next.cols, next.rows);
          alignFollowedViewport();
        } else {
          terminal.clearSelection();
          terminal.scrollToBottom();
          terminal.reset();
          terminal.clear();
          terminal.clearSelection();
          terminal.scrollToBottom();
          following = true;
          alignFollowedViewport();
        }
      } catch (error) {
        fail(error);
        return;
      }
      scheduleFlush();
      return;
    }
    writing = true;
    const batch = takeBatch();
    const viewportY = terminal.getViewportY();
    const scrollbackPosition = terminalScrollbackPosition();
    const preservePausedViewport = !following;
    try {
      suppressTerminalScroll = true;
      try {
        terminal.write(batch, () => {
          alignFollowedViewport();
          writing = false;
          if (disposed) {
            settleDrains();
            return;
          }
          scheduleFlush();
        });
        if (preservePausedViewport) {
          restorePausedTerminalViewport(viewportY, scrollbackPosition);
        }
      } finally {
        suppressTerminalScroll = false;
      }
    } catch (error) {
      fail(error);
    }
  }

  function scheduleFlush() {
    if (disposed || failure || writing || scheduledFrame !== null) return;
    if (queue.length === 0) {
      settleDrains();
      return;
    }
    scheduledFrame = requestAnimationFrame(flush);
  }

  const terminalScroll = terminal.onScroll(updateFollowState);
  element.addEventListener("scroll", updateFollowState, { passive: true });
  const resizeObserver = new ResizeObserver(alignFollowedViewport);
  resizeObserver.observe(element);
  alignFollowedViewport();

  return {
    get cols() {
      return terminal.cols;
    },
    get rows() {
      return terminal.rows;
    },
    write(bytes) {
      if (disposed) return;
      if (failure) throw failure;
      const copy = new Uint8Array(bytes);
      if (copy.length === 0) return;
      queue.push(copy);
      scheduleFlush();
    },
    resize(cols, rows) {
      if (disposed) return;
      if (failure) throw failure;
      if (!writing && queue.length === 0) {
        try {
          terminal.resize(cols, rows);
          alignFollowedViewport();
        } catch (error) {
          throw fail(error);
        }
        return;
      }
      queue.push({ type: "resize", cols, rows });
      scheduleFlush();
    },
    drain() {
      if (failure) return Promise.reject(failure);
      if (disposed || (scheduledFrame === null && !writing && queue.length === 0)) {
        return Promise.resolve();
      }
      return new Promise((resolve, reject) => {
        drainWaiters.push({ resolve, reject });
      });
    },
    clear() {
      if (disposed) return;
      if (failure) throw failure;
      queue.push({ type: "clear" });
      scheduleFlush();
    },
    copyText() {
      if (disposed) return "";
      if (failure) throw failure;
      const selection = terminal.getSelection();
      if (selection) return selection;
      terminal.selectAll();
      try {
        return terminal.getSelection();
      } finally {
        terminal.clearSelection();
      }
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      resizeObserver.disconnect();
      terminalScroll.dispose();
      element.removeEventListener("scroll", updateFollowState);
      if (scheduledFrame !== null) {
        cancelAnimationFrame(scheduledFrame);
        scheduledFrame = null;
      }
      queue.length = 0;
      queueOffset = 0;
      writing = false;
      try {
        terminal.dispose();
      } finally {
        restoreViewportSemantics();
        settleDrains();
      }
    },
  };
}
