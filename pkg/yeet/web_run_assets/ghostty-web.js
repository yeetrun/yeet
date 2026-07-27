var H = /* @__PURE__ */ ((Q) => (Q[Q.CURSOR_KEY_APPLICATION = 0] = "CURSOR_KEY_APPLICATION", Q[Q.KEYPAD_KEY_APPLICATION = 1] = "KEYPAD_KEY_APPLICATION", Q[Q.IGNORE_KEYPAD_WITH_NUMLOCK = 2] = "IGNORE_KEYPAD_WITH_NUMLOCK", Q[Q.ALT_ESC_PREFIX = 3] = "ALT_ESC_PREFIX", Q[Q.MODIFY_OTHER_KEYS_STATE_2 = 4] = "MODIFY_OTHER_KEYS_STATE_2", Q[Q.KITTY_KEYBOARD_FLAGS = 5] = "KITTY_KEYBOARD_FLAGS", Q))(H || {}), b = /* @__PURE__ */ ((Q) => (Q[Q.RELEASE = 0] = "RELEASE", Q[Q.PRESS = 1] = "PRESS", Q[Q.REPEAT = 2] = "REPEAT", Q))(b || {}), o = /* @__PURE__ */ ((Q) => (Q[Q.UNIDENTIFIED = 0] = "UNIDENTIFIED", Q[Q.GRAVE = 1] = "GRAVE", Q[Q.BACKSLASH = 2] = "BACKSLASH", Q[Q.BRACKET_LEFT = 3] = "BRACKET_LEFT", Q[Q.BRACKET_RIGHT = 4] = "BRACKET_RIGHT", Q[Q.COMMA = 5] = "COMMA", Q[Q.ZERO = 6] = "ZERO", Q[Q.ONE = 7] = "ONE", Q[Q.TWO = 8] = "TWO", Q[Q.THREE = 9] = "THREE", Q[Q.FOUR = 10] = "FOUR", Q[Q.FIVE = 11] = "FIVE", Q[Q.SIX = 12] = "SIX", Q[Q.SEVEN = 13] = "SEVEN", Q[Q.EIGHT = 14] = "EIGHT", Q[Q.NINE = 15] = "NINE", Q[Q.EQUAL = 16] = "EQUAL", Q[Q.INTL_BACKSLASH = 17] = "INTL_BACKSLASH", Q[Q.INTL_RO = 18] = "INTL_RO", Q[Q.INTL_YEN = 19] = "INTL_YEN", Q[Q.A = 20] = "A", Q[Q.B = 21] = "B", Q[Q.C = 22] = "C", Q[Q.D = 23] = "D", Q[Q.E = 24] = "E", Q[Q.F = 25] = "F", Q[Q.G = 26] = "G", Q[Q.H = 27] = "H", Q[Q.I = 28] = "I", Q[Q.J = 29] = "J", Q[Q.K = 30] = "K", Q[Q.L = 31] = "L", Q[Q.M = 32] = "M", Q[Q.N = 33] = "N", Q[Q.O = 34] = "O", Q[Q.P = 35] = "P", Q[Q.Q = 36] = "Q", Q[Q.R = 37] = "R", Q[Q.S = 38] = "S", Q[Q.T = 39] = "T", Q[Q.U = 40] = "U", Q[Q.V = 41] = "V", Q[Q.W = 42] = "W", Q[Q.X = 43] = "X", Q[Q.Y = 44] = "Y", Q[Q.Z = 45] = "Z", Q[Q.MINUS = 46] = "MINUS", Q[Q.PERIOD = 47] = "PERIOD", Q[Q.QUOTE = 48] = "QUOTE", Q[Q.SEMICOLON = 49] = "SEMICOLON", Q[Q.SLASH = 50] = "SLASH", Q[Q.ALT_LEFT = 51] = "ALT_LEFT", Q[Q.ALT_RIGHT = 52] = "ALT_RIGHT", Q[Q.BACKSPACE = 53] = "BACKSPACE", Q[Q.CAPS_LOCK = 54] = "CAPS_LOCK", Q[Q.CONTEXT_MENU = 55] = "CONTEXT_MENU", Q[Q.CONTROL_LEFT = 56] = "CONTROL_LEFT", Q[Q.CONTROL_RIGHT = 57] = "CONTROL_RIGHT", Q[Q.ENTER = 58] = "ENTER", Q[Q.META_LEFT = 59] = "META_LEFT", Q[Q.META_RIGHT = 60] = "META_RIGHT", Q[Q.SHIFT_LEFT = 61] = "SHIFT_LEFT", Q[Q.SHIFT_RIGHT = 62] = "SHIFT_RIGHT", Q[Q.SPACE = 63] = "SPACE", Q[Q.TAB = 64] = "TAB", Q[Q.CONVERT = 65] = "CONVERT", Q[Q.KANA_MODE = 66] = "KANA_MODE", Q[Q.NON_CONVERT = 67] = "NON_CONVERT", Q[Q.DELETE = 68] = "DELETE", Q[Q.END = 69] = "END", Q[Q.HELP = 70] = "HELP", Q[Q.HOME = 71] = "HOME", Q[Q.INSERT = 72] = "INSERT", Q[Q.PAGE_DOWN = 73] = "PAGE_DOWN", Q[Q.PAGE_UP = 74] = "PAGE_UP", Q[Q.DOWN = 75] = "DOWN", Q[Q.LEFT = 76] = "LEFT", Q[Q.RIGHT = 77] = "RIGHT", Q[Q.UP = 78] = "UP", Q[Q.NUM_LOCK = 79] = "NUM_LOCK", Q[Q.KP_0 = 80] = "KP_0", Q[Q.KP_1 = 81] = "KP_1", Q[Q.KP_2 = 82] = "KP_2", Q[Q.KP_3 = 83] = "KP_3", Q[Q.KP_4 = 84] = "KP_4", Q[Q.KP_5 = 85] = "KP_5", Q[Q.KP_6 = 86] = "KP_6", Q[Q.KP_7 = 87] = "KP_7", Q[Q.KP_8 = 88] = "KP_8", Q[Q.KP_9 = 89] = "KP_9", Q[Q.KP_PLUS = 90] = "KP_PLUS", Q[Q.KP_BACKSPACE = 91] = "KP_BACKSPACE", Q[Q.KP_CLEAR = 92] = "KP_CLEAR", Q[Q.KP_CLEAR_ENTRY = 93] = "KP_CLEAR_ENTRY", Q[Q.KP_COMMA = 94] = "KP_COMMA", Q[Q.KP_PERIOD = 95] = "KP_PERIOD", Q[Q.KP_DIVIDE = 96] = "KP_DIVIDE", Q[Q.KP_ENTER = 97] = "KP_ENTER", Q[Q.KP_EQUAL = 98] = "KP_EQUAL", Q[Q.KP_MEMORY_ADD = 99] = "KP_MEMORY_ADD", Q[Q.KP_MEMORY_CLEAR = 100] = "KP_MEMORY_CLEAR", Q[Q.KP_MEMORY_RECALL = 101] = "KP_MEMORY_RECALL", Q[Q.KP_MEMORY_STORE = 102] = "KP_MEMORY_STORE", Q[Q.KP_MEMORY_SUBTRACT = 103] = "KP_MEMORY_SUBTRACT", Q[Q.KP_MULTIPLY = 104] = "KP_MULTIPLY", Q[Q.KP_PAREN_LEFT = 105] = "KP_PAREN_LEFT", Q[Q.KP_PAREN_RIGHT = 106] = "KP_PAREN_RIGHT", Q[Q.KP_MINUS = 107] = "KP_MINUS", Q[Q.KP_SEPARATOR = 108] = "KP_SEPARATOR", Q[Q.NUMPAD_UP = 109] = "NUMPAD_UP", Q[Q.NUMPAD_DOWN = 110] = "NUMPAD_DOWN", Q[Q.NUMPAD_RIGHT = 111] = "NUMPAD_RIGHT", Q[Q.NUMPAD_LEFT = 112] = "NUMPAD_LEFT", Q[Q.NUMPAD_BEGIN = 113] = "NUMPAD_BEGIN", Q[Q.NUMPAD_HOME = 114] = "NUMPAD_HOME", Q[Q.NUMPAD_END = 115] = "NUMPAD_END", Q[Q.NUMPAD_INSERT = 116] = "NUMPAD_INSERT", Q[Q.NUMPAD_DELETE = 117] = "NUMPAD_DELETE", Q[Q.NUMPAD_PAGE_UP = 118] = "NUMPAD_PAGE_UP", Q[Q.NUMPAD_PAGE_DOWN = 119] = "NUMPAD_PAGE_DOWN", Q[Q.ESCAPE = 120] = "ESCAPE", Q[Q.F1 = 121] = "F1", Q[Q.F2 = 122] = "F2", Q[Q.F3 = 123] = "F3", Q[Q.F4 = 124] = "F4", Q[Q.F5 = 125] = "F5", Q[Q.F6 = 126] = "F6", Q[Q.F7 = 127] = "F7", Q[Q.F8 = 128] = "F8", Q[Q.F9 = 129] = "F9", Q[Q.F10 = 130] = "F10", Q[Q.F11 = 131] = "F11", Q[Q.F12 = 132] = "F12", Q[Q.F13 = 133] = "F13", Q[Q.F14 = 134] = "F14", Q[Q.F15 = 135] = "F15", Q[Q.F16 = 136] = "F16", Q[Q.F17 = 137] = "F17", Q[Q.F18 = 138] = "F18", Q[Q.F19 = 139] = "F19", Q[Q.F20 = 140] = "F20", Q[Q.F21 = 141] = "F21", Q[Q.F22 = 142] = "F22", Q[Q.F23 = 143] = "F23", Q[Q.F24 = 144] = "F24", Q[Q.F25 = 145] = "F25", Q[Q.FN_LOCK = 146] = "FN_LOCK", Q[Q.PRINT_SCREEN = 147] = "PRINT_SCREEN", Q[Q.SCROLL_LOCK = 148] = "SCROLL_LOCK", Q[Q.PAUSE = 149] = "PAUSE", Q[Q.BROWSER_BACK = 150] = "BROWSER_BACK", Q[Q.BROWSER_FAVORITES = 151] = "BROWSER_FAVORITES", Q[Q.BROWSER_FORWARD = 152] = "BROWSER_FORWARD", Q[Q.BROWSER_HOME = 153] = "BROWSER_HOME", Q[Q.BROWSER_REFRESH = 154] = "BROWSER_REFRESH", Q[Q.BROWSER_SEARCH = 155] = "BROWSER_SEARCH", Q[Q.BROWSER_STOP = 156] = "BROWSER_STOP", Q[Q.EJECT = 157] = "EJECT", Q[Q.LAUNCH_APP_1 = 158] = "LAUNCH_APP_1", Q[Q.LAUNCH_APP_2 = 159] = "LAUNCH_APP_2", Q[Q.LAUNCH_MAIL = 160] = "LAUNCH_MAIL", Q[Q.MEDIA_PLAY_PAUSE = 161] = "MEDIA_PLAY_PAUSE", Q[Q.MEDIA_SELECT = 162] = "MEDIA_SELECT", Q[Q.MEDIA_STOP = 163] = "MEDIA_STOP", Q[Q.MEDIA_TRACK_NEXT = 164] = "MEDIA_TRACK_NEXT", Q[Q.MEDIA_TRACK_PREVIOUS = 165] = "MEDIA_TRACK_PREVIOUS", Q[Q.POWER = 166] = "POWER", Q[Q.SLEEP = 167] = "SLEEP", Q[Q.AUDIO_VOLUME_DOWN = 168] = "AUDIO_VOLUME_DOWN", Q[Q.AUDIO_VOLUME_MUTE = 169] = "AUDIO_VOLUME_MUTE", Q[Q.AUDIO_VOLUME_UP = 170] = "AUDIO_VOLUME_UP", Q[Q.WAKE_UP = 171] = "WAKE_UP", Q[Q.COPY = 172] = "COPY", Q[Q.CUT = 173] = "CUT", Q[Q.PASTE = 174] = "PASTE", Q))(o || {}), y = /* @__PURE__ */ ((Q) => (Q[Q.NONE = 0] = "NONE", Q[Q.SHIFT = 1] = "SHIFT", Q[Q.CTRL = 2] = "CTRL", Q[Q.ALT = 4] = "ALT", Q[Q.SUPER = 8] = "SUPER", Q[Q.CAPSLOCK = 16] = "CAPSLOCK", Q[Q.NUMLOCK = 32] = "NUMLOCK", Q))(y || {}), O = /* @__PURE__ */ ((Q) => (Q[Q.NONE = 0] = "NONE", Q[Q.PARTIAL = 1] = "PARTIAL", Q[Q.FULL = 2] = "FULL", Q))(O || {});
const d = 80;
var e = /* @__PURE__ */ ((Q) => (Q[Q.BOLD = 1] = "BOLD", Q[Q.ITALIC = 2] = "ITALIC", Q[Q.UNDERLINE = 4] = "UNDERLINE", Q[Q.STRIKETHROUGH = 8] = "STRIKETHROUGH", Q[Q.INVERSE = 16] = "INVERSE", Q[Q.INVISIBLE = 32] = "INVISIBLE", Q[Q.BLINK = 64] = "BLINK", Q[Q.FAINT = 128] = "FAINT", Q))(e || {});
class q {
  constructor(A) {
    this.exports = A.exports, this.memory = this.exports.memory;
  }
  createKeyEncoder() {
    return new V(this.exports);
  }
  createTerminal(A = 80, B = 24, g) {
    return new W(this.exports, this.memory, A, B, g);
  }
  static async load(A) {
    if (A)
      return q.loadFromPath(A);
    const B = new URL("./ghostty-vt.wasm", self.location), g = [];
    if (B.protocol === "file:") {
      let C = B.pathname;
      C.match(/^\/[A-Za-z]:\//) && (C = C.slice(1)), g.push(C);
    }
    g.push(B.href, "./ghostty-vt.wasm", "/ghostty-vt.wasm");
    let E = null;
    for (const C of g)
      try {
        return await q.loadFromPath(C);
      } catch (I) {
        E = I instanceof Error ? I : new Error(String(I));
      }
    throw E || new Error("Failed to load Ghostty WASM");
  }
  static async loadFromPath(A) {
    let B;
    if (typeof Bun < "u" && typeof Bun.file == "function")
      try {
        const C = Bun.file(A);
        await C.exists() && (B = await C.arrayBuffer());
      } catch {
      }
    if (!B)
      try {
        const I = await (await import("./__vite-browser-external-2447137e.js")).readFile(A);
        B = I.buffer.slice(I.byteOffset, I.byteOffset + I.byteLength);
      } catch {
      }
    if (!B) {
      const C = await fetch(A);
      if (!C.ok)
        throw new Error(`Failed to fetch WASM: ${C.status} ${C.statusText}`);
      if (B = await C.arrayBuffer(), B.byteLength === 0)
        throw new Error(`WASM file is empty (0 bytes). Check path: ${A}`);
    }
    if (!B)
      throw new Error(`Could not load WASM from path: ${A}`);
    const g = await WebAssembly.compile(B), E = await WebAssembly.instantiate(g, {
      env: {
        log: (C, I) => {
          const D = new Uint8Array(
            E.exports.memory.buffer,
            C,
            I
          );
          console.log("[ghostty-vt]", new TextDecoder().decode(D));
        }
      }
    });
    return new q(E);
  }
}
class V {
  constructor(A) {
    this.encoder = 0, this.exports = A;
    const B = this.exports.ghostty_wasm_alloc_opaque(), g = this.exports.ghostty_key_encoder_new(0, B);
    if (g !== 0)
      throw new Error(`Failed to create key encoder: ${g}`);
    const E = new DataView(this.exports.memory.buffer);
    this.encoder = E.getUint32(B, !0), this.exports.ghostty_wasm_free_opaque(B);
  }
  setOption(A, B) {
    const g = this.exports.ghostty_wasm_alloc_u8();
    new DataView(this.exports.memory.buffer).setUint8(g, typeof B == "boolean" ? B ? 1 : 0 : B), this.exports.ghostty_key_encoder_setopt(this.encoder, A, g), this.exports.ghostty_wasm_free_u8(g);
  }
  setKittyFlags(A) {
    this.setOption(H.KITTY_KEYBOARD_FLAGS, A);
  }
  encode(A) {
    const B = this.exports.ghostty_wasm_alloc_opaque(), g = this.exports.ghostty_key_event_new(0, B);
    if (g !== 0)
      throw new Error(`Failed to create key event: ${g}`);
    const E = new DataView(this.exports.memory.buffer), C = E.getUint32(B, !0);
    if (this.exports.ghostty_wasm_free_opaque(B), this.exports.ghostty_key_event_set_action(C, A.action), this.exports.ghostty_key_event_set_key(C, A.key), this.exports.ghostty_key_event_set_mods(C, A.mods), A.utf8) {
      const M = new TextEncoder().encode(A.utf8), a = this.exports.ghostty_wasm_alloc_u8_array(M.length);
      new Uint8Array(this.exports.memory.buffer).set(M, a), this.exports.ghostty_key_event_set_utf8(C, a, M.length), this.exports.ghostty_wasm_free_u8_array(a, M.length);
    }
    const I = 32, D = this.exports.ghostty_wasm_alloc_u8_array(I), i = this.exports.ghostty_wasm_alloc_usize(), w = this.exports.ghostty_key_encoder_encode(
      this.encoder,
      C,
      D,
      I,
      i
    );
    if (w !== 0)
      throw this.exports.ghostty_wasm_free_u8_array(D, I), this.exports.ghostty_wasm_free_usize(i), this.exports.ghostty_key_event_free(C), new Error(`Failed to encode key: ${w}`);
    const s = E.getUint32(i, !0), N = new Uint8Array(this.exports.memory.buffer, D, s).slice();
    return this.exports.ghostty_wasm_free_u8_array(D, I), this.exports.ghostty_wasm_free_usize(i), this.exports.ghostty_key_event_free(C), N;
  }
  dispose() {
    this.encoder && (this.exports.ghostty_key_encoder_free(this.encoder), this.encoder = 0);
  }
}
const z = class K {
  constructor(A, B, g = 80, E = 24, C) {
    var I;
    this.scrollbackLimit = C ? C.scrollbackLimit ?? 1e4 : 1e4;
    if (this.viewportBufferPtr = 0, this.viewportBufferSize = 0, this.cellPool = [], this.graphemeBuffer = null, this.graphemeBufferPtr = 0, this.exports = A, this.memory = B, this._cols = g, this._rows = E, C) {
      const D = this.exports.ghostty_wasm_alloc_u8_array(d);
      if (D === 0)
        throw new Error("Failed to allocate config (out of memory)");
      try {
        const i = new DataView(this.memory.buffer);
        let w = D;
        i.setUint32(w, C.scrollbackLimit ?? 1e4, !0), w += 4, i.setUint32(w, C.fgColor ?? 0, !0), w += 4, i.setUint32(w, C.bgColor ?? 0, !0), w += 4, i.setUint32(w, C.cursorColor ?? 0, !0), w += 4;
        for (let s = 0; s < 16; s++)
          i.setUint32(w, ((I = C.palette) == null ? void 0 : I[s]) ?? 0, !0), w += 4;
        this.handle = this.exports.ghostty_terminal_new_with_config(g, E, D);
      } finally {
        this.exports.ghostty_wasm_free_u8_array(D, d);
      }
    } else
      this.handle = this.exports.ghostty_terminal_new(g, E);
    if (!this.handle)
      throw new Error("Failed to create terminal");
    this.initCellPool();
  }
  get cols() {
    return this._cols;
  }
  get rows() {
    return this._rows;
  }
  // ==========================================================================
  // Lifecycle
  // ==========================================================================
  write(A) {
    const B = typeof A == "string" ? new TextEncoder().encode(A) : A, g = this.exports.ghostty_wasm_alloc_u8_array(B.length);
    new Uint8Array(this.memory.buffer).set(B, g), this.exports.ghostty_terminal_write(this.handle, g, B.length), this.exports.ghostty_wasm_free_u8_array(g, B.length);
  }
  resize(A, B) {
    A === this._cols && B === this._rows || (this._cols = A, this._rows = B, this.exports.ghostty_terminal_resize(this.handle, A, B), this.invalidateBuffers(), this.initCellPool());
  }
  free() {
    this.viewportBufferPtr && (this.exports.ghostty_wasm_free_u8_array(this.viewportBufferPtr, this.viewportBufferSize), this.viewportBufferPtr = 0), this.exports.ghostty_terminal_free(this.handle);
  }
  // ==========================================================================
  // RenderState API - The key performance optimization
  // ==========================================================================
  /**
   * Update render state from terminal.
   *
   * This syncs the RenderState with the current Terminal state.
   * The dirty state (full/partial/none) is stored in the WASM RenderState
   * and can be queried via isRowDirty(). When dirty==full, isRowDirty()
   * returns true for ALL rows.
   *
   * The WASM layer automatically detects screen switches (normal <-> alternate)
   * and returns FULL dirty state when switching screens (e.g., vim exit).
   *
   * Safe to call multiple times - dirty state persists until markClean().
   */
  update() {
    return this.exports.ghostty_render_state_update(this.handle);
  }
  /**
   * Get cursor state from render state.
   * Ensures render state is fresh by calling update().
   */
  getCursor() {
    return this.update(), {
      x: this.exports.ghostty_render_state_get_cursor_x(this.handle),
      y: this.exports.ghostty_render_state_get_cursor_y(this.handle),
      viewportX: this.exports.ghostty_render_state_get_cursor_x(this.handle),
      viewportY: this.exports.ghostty_render_state_get_cursor_y(this.handle),
      visible: this.exports.ghostty_render_state_get_cursor_visible(this.handle),
      blinking: !1,
      // TODO: Add blinking support
      style: "block"
      // TODO: Add style support
    };
  }
  /**
   * Get default colors from render state
   */
  getColors() {
    const A = this.exports.ghostty_render_state_get_bg_color(this.handle), B = this.exports.ghostty_render_state_get_fg_color(this.handle);
    return {
      background: {
        r: A >> 16 & 255,
        g: A >> 8 & 255,
        b: A & 255
      },
      foreground: {
        r: B >> 16 & 255,
        g: B >> 8 & 255,
        b: B & 255
      },
      cursor: null
      // TODO: Add cursor color support
    };
  }
  /**
   * Check if a specific row is dirty
   */
  isRowDirty(A) {
    return this.exports.ghostty_render_state_is_row_dirty(this.handle, A);
  }
  /**
   * Mark render state as clean (call after rendering)
   */
  markClean() {
    this.exports.ghostty_render_state_mark_clean(this.handle);
  }
  /**
   * Get ALL viewport cells in ONE WASM call - the key performance optimization!
   * Returns a reusable cell array (zero allocation after warmup).
   */
  getViewport() {
    const A = this._cols * this._rows, B = A * K.CELL_SIZE;
    return (!this.viewportBufferPtr || this.viewportBufferSize < B) && (this.viewportBufferPtr && this.exports.ghostty_wasm_free_u8_array(this.viewportBufferPtr, this.viewportBufferSize), this.viewportBufferPtr = this.exports.ghostty_wasm_alloc_u8_array(B), this.viewportBufferSize = B), this.exports.ghostty_render_state_get_viewport(
      this.handle,
      this.viewportBufferPtr,
      A
    ) < 0 ? this.cellPool : (this.parseCellsIntoPool(this.viewportBufferPtr, A), this.cellPool);
  }
  // ==========================================================================
  // Compatibility methods (delegate to render state)
  // ==========================================================================
  /**
   * Get line - for compatibility, extracts from viewport.
   * Ensures render state is fresh by calling update().
   * Returns a COPY of the cells to avoid pool reference issues.
   */
  getLine(A) {
    if (A < 0 || A >= this._rows)
      return null;
    this.update();
    const B = this.getViewport(), g = A * this._cols;
    return B.slice(g, g + this._cols).map((E) => ({ ...E }));
  }
  /** For compatibility with old API */
  isDirty() {
    return this.update() !== O.NONE;
  }
  /**
   * Check if a full redraw is needed (screen change, resize, etc.)
   * Note: This calls update() to ensure fresh state. Safe to call multiple times.
   */
  needsFullRedraw() {
    return this.update() === O.FULL;
  }
  /** Mark render state as clean after rendering */
  clearDirty() {
    this.markClean();
  }
  // ==========================================================================
  // Terminal modes
  // ==========================================================================
  isAlternateScreen() {
    return !!this.exports.ghostty_terminal_is_alternate_screen(this.handle);
  }
  hasBracketedPaste() {
    return this.getMode(2004, !1);
  }
  hasFocusEvents() {
    return this.getMode(1004, !1);
  }
  hasMouseTracking() {
    return this.exports.ghostty_terminal_has_mouse_tracking(this.handle) !== 0;
  }
  // ==========================================================================
  // Extended API (scrollback, modes, etc.)
  // ==========================================================================
  /** Get dimensions - for compatibility */
  getDimensions() {
    return { cols: this._cols, rows: this._rows };
  }
  /** Get number of scrollback lines (history, not including active screen) */
  getNativeScrollbackLength() {
    return this.exports.ghostty_terminal_get_scrollback_length(this.handle);
  }
  getScrollbackStart() {
    return Math.max(0, this.getNativeScrollbackLength() - this.scrollbackLimit);
  }
  getScrollbackLength() {
    return this.getNativeScrollbackLength() - this.getScrollbackStart();
  }
  /**
   * Get a line from the scrollback buffer.
   * Ensures render state is fresh by calling update().
   * @param offset 0 = oldest line, (length-1) = most recent scrollback line
   */
  getScrollbackLine(A) {
    const B = this._cols * K.CELL_SIZE;
    (!this.viewportBufferPtr || this.viewportBufferSize < B) && (this.viewportBufferPtr && this.exports.ghostty_wasm_free_u8_array(this.viewportBufferPtr, this.viewportBufferSize), this.viewportBufferPtr = this.exports.ghostty_wasm_alloc_u8_array(B), this.viewportBufferSize = B), this.update();
    const g = this.exports.ghostty_terminal_get_scrollback_line(
      this.handle,
      A + this.getScrollbackStart(),
      this.viewportBufferPtr,
      this._cols
    );
    if (g < 0)
      return null;
    const E = [], C = this.memory.buffer, I = new Uint8Array(C, this.viewportBufferPtr, g * K.CELL_SIZE), D = new DataView(C, this.viewportBufferPtr, g * K.CELL_SIZE);
    for (let i = 0; i < g; i++) {
      const w = i * K.CELL_SIZE;
      E.push({
        codepoint: D.getUint32(w, !0),
        fg_r: I[w + 4],
        fg_g: I[w + 5],
        fg_b: I[w + 6],
        bg_r: I[w + 7],
        bg_g: I[w + 8],
        bg_b: I[w + 9],
        flags: I[w + 10],
        width: I[w + 11],
        hyperlink_id: D.getUint16(w + 12, !0),
        grapheme_len: I[w + 14]
      });
    }
    return E;
  }
  /** Check if a row in the active screen is wrapped (soft-wrapped to next line) */
  isRowWrapped(A) {
    return this.exports.ghostty_terminal_is_row_wrapped(this.handle, A) !== 0;
  }
  /** Hyperlink URI not yet exposed in simplified API */
  getHyperlinkUri(A) {
    return null;
  }
  /**
   * Check if there are pending responses from the terminal.
   * Responses are generated by escape sequences like DSR (Device Status Report).
   */
  hasResponse() {
    return this.exports.ghostty_terminal_has_response(this.handle);
  }
  /**
   * Read pending responses from the terminal.
   * Returns the response string, or null if no responses pending.
   *
   * Responses are generated by escape sequences that require replies:
   * - DSR 6 (cursor position): Returns \x1b[row;colR
   * - DSR 5 (operating status): Returns \x1b[0n
   */
  readResponse() {
    if (!this.hasResponse())
      return null;
    const A = 256, B = this.exports.ghostty_wasm_alloc_u8_array(A);
    try {
      const g = this.exports.ghostty_terminal_read_response(this.handle, B, A);
      if (g <= 0)
        return null;
      const E = new Uint8Array(this.memory.buffer, B, g);
      return new TextDecoder().decode(E.slice());
    } finally {
      this.exports.ghostty_wasm_free_u8_array(B, A);
    }
  }
  /**
   * Query arbitrary terminal mode by number
   * @param mode Mode number (e.g., 25 for cursor visibility, 2004 for bracketed paste)
   * @param isAnsi True for ANSI modes, false for DEC modes (default: false)
   */
  getMode(A, B = !1) {
    return this.exports.ghostty_terminal_get_mode(this.handle, A, B) !== 0;
  }
  // ==========================================================================
  // Private helpers
  // ==========================================================================
  initCellPool() {
    const A = this._cols * this._rows;
    if (this.cellPool.length < A)
      for (let B = this.cellPool.length; B < A; B++)
        this.cellPool.push({
          codepoint: 0,
          fg_r: 204,
          fg_g: 204,
          fg_b: 204,
          bg_r: 0,
          bg_g: 0,
          bg_b: 0,
          flags: 0,
          width: 1,
          hyperlink_id: 0,
          grapheme_len: 0
        });
  }
  parseCellsIntoPool(A, B) {
    const g = this.memory.buffer, E = new Uint8Array(g, A, B * K.CELL_SIZE), C = new DataView(g, A, B * K.CELL_SIZE);
    for (let I = 0; I < B; I++) {
      const D = I * K.CELL_SIZE, i = this.cellPool[I];
      i.codepoint = C.getUint32(D, !0), i.fg_r = E[D + 4], i.fg_g = E[D + 5], i.fg_b = E[D + 6], i.bg_r = E[D + 7], i.bg_g = E[D + 8], i.bg_b = E[D + 9], i.flags = E[D + 10], i.width = E[D + 11], i.hyperlink_id = C.getUint16(D + 12, !0), i.grapheme_len = E[D + 14];
    }
  }
  /**
   * Get all codepoints for a grapheme cluster at the given position.
   * For most cells this returns a single codepoint, but for complex scripts
   * (Hindi, emoji with ZWJ, etc.) it returns multiple codepoints.
   * @returns Array of codepoints, or null on error
   */
  getGrapheme(A, B) {
    this.graphemeBuffer || (this.graphemeBufferPtr = this.exports.ghostty_wasm_alloc_u8_array(16 * 4), this.graphemeBuffer = new Uint32Array(this.memory.buffer, this.graphemeBufferPtr, 16));
    const g = this.exports.ghostty_render_state_get_grapheme(
      this.handle,
      A,
      B,
      this.graphemeBufferPtr,
      16
    );
    if (g < 0)
      return null;
    const E = new Uint32Array(this.memory.buffer, this.graphemeBufferPtr, g);
    return Array.from(E);
  }
  /**
   * Get a string representation of the grapheme at the given position.
   * This properly handles complex scripts like Hindi, emoji with ZWJ, etc.
   */
  getGraphemeString(A, B) {
    const g = this.getGrapheme(A, B);
    return !g || g.length === 0 ? " " : String.fromCodePoint(...g);
  }
  /**
   * Get all codepoints for a grapheme cluster in the scrollback buffer.
   * @param offset Scrollback line offset (0 = oldest)
   * @param col Column index
   * @returns Array of codepoints, or null on error
   */
  getScrollbackGrapheme(A, B) {
    this.graphemeBuffer || (this.graphemeBufferPtr = this.exports.ghostty_wasm_alloc_u8_array(16 * 4), this.graphemeBuffer = new Uint32Array(this.memory.buffer, this.graphemeBufferPtr, 16));
    const g = this.exports.ghostty_terminal_get_scrollback_grapheme(
      this.handle,
      A + this.getScrollbackStart(),
      B,
      this.graphemeBufferPtr,
      16
    );
    if (g < 0)
      return null;
    const E = new Uint32Array(this.memory.buffer, this.graphemeBufferPtr, g);
    return Array.from(E);
  }
  /**
   * Get a string representation of a grapheme in the scrollback buffer.
   */
  getScrollbackGraphemeString(A, B) {
    const g = this.getScrollbackGrapheme(A, B);
    return !g || g.length === 0 ? " " : String.fromCodePoint(...g);
  }
  invalidateBuffers() {
    this.viewportBufferPtr && (this.exports.ghostty_wasm_free_u8_array(this.viewportBufferPtr, this.viewportBufferSize), this.viewportBufferPtr = 0, this.viewportBufferSize = 0);
  }
};
z.CELL_SIZE = 16;
let W = z;
class J {
  constructor() {
    this.listeners = [], this.event = (A) => (this.listeners.push(A), {
      dispose: () => {
        const B = this.listeners.indexOf(A);
        B >= 0 && this.listeners.splice(B, 1);
      }
    });
  }
  fire(A) {
    for (const B of this.listeners)
      B(A);
  }
  dispose() {
    this.listeners = [];
  }
}
class Z {
  constructor(A) {
    this.bufferChangeEmitter = new J(), this.terminal = A;
  }
  get active() {
    const A = this.terminal.wasmTerm;
    return A ? A.isAlternateScreen() ? this.alternate : this.normal : this.normal;
  }
  get normal() {
    return this._normalBuffer || (this._normalBuffer = new m(this.terminal, "normal")), this._normalBuffer;
  }
  get alternate() {
    return this._alternateBuffer || (this._alternateBuffer = new m(this.terminal, "alternate")), this._alternateBuffer;
  }
  get onBufferChange() {
    return this.bufferChangeEmitter.event;
  }
  /**
   * Internal: Fire buffer change event when screen switches
   * Should be called by Terminal when detecting screen change
   */
  _fireBufferChange(A) {
    this.bufferChangeEmitter.fire(A);
  }
}
class m {
  constructor(A, B) {
    this.terminal = A, this.bufferType = B;
    const g = {
      codepoint: 0,
      fg_r: 204,
      fg_g: 204,
      fg_b: 204,
      bg_r: 0,
      bg_g: 0,
      bg_b: 0,
      flags: 0,
      width: 1,
      hyperlink_id: 0,
      grapheme_len: 0
    };
    this.nullCell = new l(g, 0);
  }
  get type() {
    return this.bufferType;
  }
  get cursorX() {
    const A = this.getWasmTerm();
    return A ? A.getCursor().x : 0;
  }
  get cursorY() {
    const A = this.getWasmTerm();
    return A ? A.getCursor().y : 0;
  }
  get viewportY() {
    return 0;
  }
  get baseY() {
    return 0;
  }
  get length() {
    const A = this.getWasmTerm();
    return A ? this.bufferType === "alternate" ? A.rows : A.getScrollbackLength() + A.rows : 0;
  }
  getLine(A) {
    const B = this.getWasmTerm();
    if (!B || A < 0 || A >= this.length)
      return;
    const g = B.getScrollbackLength();
    let E, C, I;
    if (this.bufferType === "normal" && A < g) {
      const D = A;
      E = B.getScrollbackLine(D), I = !1;
    } else
      C = this.bufferType === "normal" ? A - g : A, E = B.getLine(C), I = B.isRowWrapped(C);
    if (E)
      return new j(E, I, B.cols);
  }
  getNullCell() {
    return this.nullCell;
  }
  getWasmTerm() {
    return this.terminal.wasmTerm;
  }
}
class j {
  constructor(A, B, g) {
    this.cells = A, this._isWrapped = B, this._length = g;
  }
  get length() {
    return this._length;
  }
  get isWrapped() {
    return this._isWrapped;
  }
  getCell(A) {
    if (!(A < 0 || A >= this._length))
      return A >= this.cells.length ? new l(
        {
          codepoint: 0,
          fg_r: 204,
          fg_g: 204,
          fg_b: 204,
          bg_r: 0,
          bg_g: 0,
          bg_b: 0,
          flags: 0,
          width: 1,
          hyperlink_id: 0,
          grapheme_len: 0
        },
        A
      ) : new l(this.cells[A], A);
  }
  translateToString(A = !1, B = 0, g = this._length) {
    const E = Math.max(0, Math.min(B, this._length)), C = Math.max(E, Math.min(g, this._length));
    let I = "";
    for (let D = E; D < C; D++) {
      const i = this.getCell(D);
      if (i) {
        const w = i.getChars();
        I += w;
      }
    }
    return A && (I = I.trimEnd()), I;
  }
}
class l {
  constructor(A, B) {
    this.cell = A, this.x = B;
  }
  getChars() {
    const A = this.cell.codepoint;
    return A === 0 ? "" : A < 0 || A > 1114111 || A >= 55296 && A <= 57343 ? "�" : String.fromCodePoint(A);
  }
  getCode() {
    return this.cell.codepoint;
  }
  getWidth() {
    return this.cell.width;
  }
  getFgColorMode() {
    return -1;
  }
  getBgColorMode() {
    return -1;
  }
  getFgColor() {
    return this.cell.fg_r << 16 | this.cell.fg_g << 8 | this.cell.fg_b;
  }
  getBgColor() {
    return this.cell.bg_r << 16 | this.cell.bg_g << 8 | this.cell.bg_b;
  }
  isBold() {
    return this.cell.flags & e.BOLD ? 1 : 0;
  }
  isItalic() {
    return this.cell.flags & e.ITALIC ? 1 : 0;
  }
  isUnderline() {
    return this.cell.flags & e.UNDERLINE ? 1 : 0;
  }
  isStrikethrough() {
    return this.cell.flags & e.STRIKETHROUGH ? 1 : 0;
  }
  isBlink() {
    return this.cell.flags & e.BLINK ? 1 : 0;
  }
  isInverse() {
    return this.cell.flags & e.INVERSE ? 1 : 0;
  }
  isInvisible() {
    return this.cell.flags & e.INVISIBLE ? 1 : 0;
  }
  isFaint() {
    return this.cell.flags & e.FAINT ? 1 : 0;
  }
  /**
   * Get hyperlink ID for this cell (0 = no link)
   * Used by link detection system
   */
  getHyperlinkId() {
    return this.cell.hyperlink_id;
  }
  /**
   * Get the Unicode codepoint for this cell
   * Used by link detection system
   */
  getCodepoint() {
    return this.cell.codepoint;
  }
  /**
   * Check if cell has dim/faint attribute
   * Added for IBufferCell compatibility
   */
  isDim() {
    return (this.cell.flags & e.FAINT) !== 0;
  }
}
const u = {
  // Letters
  KeyA: o.A,
  KeyB: o.B,
  KeyC: o.C,
  KeyD: o.D,
  KeyE: o.E,
  KeyF: o.F,
  KeyG: o.G,
  KeyH: o.H,
  KeyI: o.I,
  KeyJ: o.J,
  KeyK: o.K,
  KeyL: o.L,
  KeyM: o.M,
  KeyN: o.N,
  KeyO: o.O,
  KeyP: o.P,
  KeyQ: o.Q,
  KeyR: o.R,
  KeyS: o.S,
  KeyT: o.T,
  KeyU: o.U,
  KeyV: o.V,
  KeyW: o.W,
  KeyX: o.X,
  KeyY: o.Y,
  KeyZ: o.Z,
  // Numbers
  Digit1: o.ONE,
  Digit2: o.TWO,
  Digit3: o.THREE,
  Digit4: o.FOUR,
  Digit5: o.FIVE,
  Digit6: o.SIX,
  Digit7: o.SEVEN,
  Digit8: o.EIGHT,
  Digit9: o.NINE,
  Digit0: o.ZERO,
  // Special keys
  Enter: o.ENTER,
  Escape: o.ESCAPE,
  Backspace: o.BACKSPACE,
  Tab: o.TAB,
  Space: o.SPACE,
  // Punctuation
  Minus: o.MINUS,
  Equal: o.EQUAL,
  BracketLeft: o.BRACKET_LEFT,
  BracketRight: o.BRACKET_RIGHT,
  Backslash: o.BACKSLASH,
  Semicolon: o.SEMICOLON,
  Quote: o.QUOTE,
  Backquote: o.GRAVE,
  Comma: o.COMMA,
  Period: o.PERIOD,
  Slash: o.SLASH,
  // Function keys
  CapsLock: o.CAPS_LOCK,
  F1: o.F1,
  F2: o.F2,
  F3: o.F3,
  F4: o.F4,
  F5: o.F5,
  F6: o.F6,
  F7: o.F7,
  F8: o.F8,
  F9: o.F9,
  F10: o.F10,
  F11: o.F11,
  F12: o.F12,
  // Special function keys
  PrintScreen: o.PRINT_SCREEN,
  ScrollLock: o.SCROLL_LOCK,
  Pause: o.PAUSE,
  Insert: o.INSERT,
  Home: o.HOME,
  PageUp: o.PAGE_UP,
  Delete: o.DELETE,
  End: o.END,
  PageDown: o.PAGE_DOWN,
  // Arrow keys
  ArrowRight: o.RIGHT,
  ArrowLeft: o.LEFT,
  ArrowDown: o.DOWN,
  ArrowUp: o.UP,
  // Keypad
  NumLock: o.NUM_LOCK,
  NumpadDivide: o.KP_DIVIDE,
  NumpadMultiply: o.KP_MULTIPLY,
  NumpadSubtract: o.KP_MINUS,
  NumpadAdd: o.KP_PLUS,
  NumpadEnter: o.KP_ENTER,
  Numpad1: o.KP_1,
  Numpad2: o.KP_2,
  Numpad3: o.KP_3,
  Numpad4: o.KP_4,
  Numpad5: o.KP_5,
  Numpad6: o.KP_6,
  Numpad7: o.KP_7,
  Numpad8: o.KP_8,
  Numpad9: o.KP_9,
  Numpad0: o.KP_0,
  NumpadDecimal: o.KP_PERIOD,
  // International
  IntlBackslash: o.INTL_BACKSLASH,
  ContextMenu: o.CONTEXT_MENU,
  // Additional function keys
  F13: o.F13,
  F14: o.F14,
  F15: o.F15,
  F16: o.F16,
  F17: o.F17,
  F18: o.F18,
  F19: o.F19,
  F20: o.F20,
  F21: o.F21,
  F22: o.F22,
  F23: o.F23,
  F24: o.F24
};
class P {
  /**
   * Create a new InputHandler
   * @param ghostty - Ghostty instance (for creating KeyEncoder)
   * @param container - DOM element to attach listeners to
   * @param onData - Callback for terminal data (escape sequences to send to PTY)
   * @param onBell - Callback for bell/beep event
   * @param onKey - Optional callback for raw key events
   * @param customKeyEventHandler - Optional custom key event handler
   * @param getMode - Optional callback to query terminal mode state (for application cursor mode)
   */
  constructor(A, B, g, E, C, I, D) {
    this.keydownListener = null, this.keypressListener = null, this.pasteListener = null, this.compositionStartListener = null, this.compositionUpdateListener = null, this.compositionEndListener = null, this.isComposing = !1, this.isDisposed = !1, this.encoder = A.createKeyEncoder(), this.container = B, this.onDataCallback = g, this.onBellCallback = E, this.onKeyCallback = C, this.customKeyEventHandler = I, this.getModeCallback = D, this.attach();
  }
  /**
   * Set custom key event handler (for runtime updates)
   */
  setCustomKeyEventHandler(A) {
    this.customKeyEventHandler = A;
  }
  /**
   * Attach keyboard event listeners to container
   */
  attach() {
    typeof this.container.hasAttribute == "function" && typeof this.container.setAttribute == "function" && (this.container.hasAttribute("tabindex") || this.container.setAttribute("tabindex", "0"), this.container.style && (this.container.style.outline = "none")), this.keydownListener = this.handleKeyDown.bind(this), this.container.addEventListener("keydown", this.keydownListener), this.pasteListener = this.handlePaste.bind(this), this.container.addEventListener("paste", this.pasteListener), this.compositionStartListener = this.handleCompositionStart.bind(this), this.container.addEventListener("compositionstart", this.compositionStartListener), this.compositionUpdateListener = this.handleCompositionUpdate.bind(this), this.container.addEventListener("compositionupdate", this.compositionUpdateListener), this.compositionEndListener = this.handleCompositionEnd.bind(this), this.container.addEventListener("compositionend", this.compositionEndListener);
  }
  /**
   * Map KeyboardEvent.code to USB HID Key enum value
   * @param code - KeyboardEvent.code value
   * @returns Key enum value or null if unmapped
   */
  mapKeyCode(A) {
    return u[A] ?? null;
  }
  /**
   * Extract modifier flags from KeyboardEvent
   * @param event - KeyboardEvent
   * @returns Mods flags
   */
  extractModifiers(A) {
    let B = y.NONE;
    return A.shiftKey && (B |= y.SHIFT), A.ctrlKey && (B |= y.CTRL), A.altKey && (B |= y.ALT), A.metaKey && (B |= y.SUPER), B;
  }
  /**
   * Check if this is a printable character with no special modifiers
   * @param event - KeyboardEvent
   * @returns true if printable character
   */
  isPrintableCharacter(A) {
    return A.ctrlKey && !A.altKey || A.altKey && !A.ctrlKey || A.metaKey ? !1 : A.key.length === 1;
  }
  /**
   * Handle keydown event
   * @param event - KeyboardEvent
   */
  handleKeyDown(A) {
    if (this.isDisposed || this.isComposing || A.isComposing || A.keyCode === 229)
      return;
    if (this.onKeyCallback && this.onKeyCallback({ key: A.key, domEvent: A }), this.customKeyEventHandler && this.customKeyEventHandler(A)) {
      A.preventDefault();
      return;
    }
    if ((A.ctrlKey || A.metaKey) && A.code === "KeyV" || A.metaKey && A.code === "KeyC")
      return;
    if (this.isPrintableCharacter(A)) {
      A.preventDefault(), this.onDataCallback(A.key);
      return;
    }
    const B = this.mapKeyCode(A.code);
    if (B === null)
      return;
    const g = this.extractModifiers(A);
    if (g === y.NONE || g === y.SHIFT) {
      let C = null;
      switch (B) {
        case o.ENTER:
          C = "\r";
          break;
        case o.TAB:
          C = "	";
          break;
        case o.BACKSPACE:
          C = "";
          break;
        case o.ESCAPE:
          C = "\x1B";
          break;
        case o.HOME:
          C = "\x1B[H";
          break;
        case o.END:
          C = "\x1B[F";
          break;
        case o.INSERT:
          C = "\x1B[2~";
          break;
        case o.DELETE:
          C = "\x1B[3~";
          break;
        case o.PAGE_UP:
          C = "\x1B[5~";
          break;
        case o.PAGE_DOWN:
          C = "\x1B[6~";
          break;
        case o.F1:
          C = "\x1BOP";
          break;
        case o.F2:
          C = "\x1BOQ";
          break;
        case o.F3:
          C = "\x1BOR";
          break;
        case o.F4:
          C = "\x1BOS";
          break;
        case o.F5:
          C = "\x1B[15~";
          break;
        case o.F6:
          C = "\x1B[17~";
          break;
        case o.F7:
          C = "\x1B[18~";
          break;
        case o.F8:
          C = "\x1B[19~";
          break;
        case o.F9:
          C = "\x1B[20~";
          break;
        case o.F10:
          C = "\x1B[21~";
          break;
        case o.F11:
          C = "\x1B[23~";
          break;
        case o.F12:
          C = "\x1B[24~";
          break;
      }
      if (C !== null) {
        A.preventDefault(), this.onDataCallback(C);
        return;
      }
    }
    const E = b.PRESS;
    try {
      if (this.getModeCallback) {
        const w = this.getModeCallback(1);
        this.encoder.setOption(H.CURSOR_KEY_APPLICATION, w);
      }
      const C = A.key.length === 1 && A.key.charCodeAt(0) < 128 ? A.key.toLowerCase() : void 0, I = this.encoder.encode({
        action: E,
        key: B,
        mods: g,
        utf8: C
      }), i = new TextDecoder().decode(I);
      A.preventDefault(), A.stopPropagation(), i.length > 0 && this.onDataCallback(i);
    } catch (C) {
      console.warn("Failed to encode key:", A.code, C);
    }
  }
  /**
   * Handle paste event from clipboard
   * @param event - ClipboardEvent
   */
  handlePaste(A) {
    if (this.isDisposed)
      return;
    A.preventDefault(), A.stopPropagation();
    const B = A.clipboardData;
    if (!B) {
      console.warn("No clipboard data available");
      return;
    }
    const g = B.getData("text/plain");
    if (!g) {
      console.warn("No text in clipboard");
      return;
    }
    this.onDataCallback(g);
  }
  /**
   * Handle compositionstart event
   */
  handleCompositionStart(A) {
    this.isDisposed || (this.isComposing = !0);
  }
  /**
   * Handle compositionupdate event
   */
  handleCompositionUpdate(A) {
    this.isDisposed;
  }
  /**
   * Handle compositionend event
   */
  handleCompositionEnd(A) {
    if (this.isDisposed)
      return;
    this.isComposing = !1;
    const B = A.data;
    if (B && B.length > 0 && this.onDataCallback(B), this.container && this.container.childNodes)
      for (let g = this.container.childNodes.length - 1; g >= 0; g--) {
        const E = this.container.childNodes[g];
        E.nodeType === 3 && this.container.removeChild(E);
      }
  }
  /**
   * Dispose the InputHandler and remove event listeners
   */
  dispose() {
    this.isDisposed || (this.keydownListener && (this.container.removeEventListener("keydown", this.keydownListener), this.keydownListener = null), this.keypressListener && (this.container.removeEventListener("keypress", this.keypressListener), this.keypressListener = null), this.pasteListener && (this.container.removeEventListener("paste", this.pasteListener), this.pasteListener = null), this.compositionStartListener && (this.container.removeEventListener("compositionstart", this.compositionStartListener), this.compositionStartListener = null), this.compositionUpdateListener && (this.container.removeEventListener("compositionupdate", this.compositionUpdateListener), this.compositionUpdateListener = null), this.compositionEndListener && (this.container.removeEventListener("compositionend", this.compositionEndListener), this.compositionEndListener = null), this.isDisposed = !0);
  }
  /**
   * Check if handler is disposed
   */
  isActive() {
    return !this.isDisposed;
  }
}
class v {
  // Terminal instance for buffer access
  constructor(A) {
    this.terminal = A, this.providers = [], this.linkCache = /* @__PURE__ */ new Map(), this.scannedRows = /* @__PURE__ */ new Set();
  }
  /**
   * Register a link provider
   */
  registerProvider(A) {
    this.providers.push(A), this.invalidateCache();
  }
  /**
   * Get link at the specified buffer position
   * @param col Column (0-based)
   * @param row Absolute row in buffer (0-based)
   * @returns Link at position, or undefined if none
   */
  async getLinkAt(A, B) {
    const g = this.terminal.buffer.active.getLine(B);
    if (!g || A < 0 || A >= g.length)
      return;
    const E = g.getCell(A);
    if (!E)
      return;
    const C = E.getHyperlinkId();
    if (C > 0) {
      const I = `h${C}`;
      if (this.linkCache.has(I))
        return this.linkCache.get(I);
    }
    if (this.scannedRows.has(B) || await this.scanRow(B), C > 0) {
      const I = `h${C}`, D = this.linkCache.get(I);
      if (D)
        return D;
    }
    for (const I of this.linkCache.values())
      if (this.isPositionInLink(A, B, I))
        return I;
  }
  /**
   * Scan a row for links using all registered providers
   */
  async scanRow(A) {
    this.scannedRows.add(A);
    const B = [];
    for (const g of this.providers) {
      const E = await new Promise((C) => {
        g.provideLinks(A, C);
      });
      E && B.push(...E);
    }
    for (const g of B)
      this.cacheLink(g);
  }
  /**
   * Cache a link for fast lookup
   */
  cacheLink(A) {
    const { start: B } = A.range, g = this.terminal.buffer.active.getLine(B.y);
    if (g) {
      const D = g.getCell(B.x);
      if (!D) {
        const { start: w, end: s } = A.range, N = `r${w.y}:${w.x}-${s.x}`;
        this.linkCache.set(N, A);
        return;
      }
      const i = D.getHyperlinkId();
      if (i > 0) {
        this.linkCache.set(`h${i}`, A);
        return;
      }
    }
    const { start: E, end: C } = A.range, I = `r${E.y}:${E.x}-${C.x}`;
    this.linkCache.set(I, A);
  }
  /**
   * Check if a position is within a link's range
   */
  isPositionInLink(A, B, g) {
    const { start: E, end: C } = g.range;
    return B < E.y || B > C.y ? !1 : E.y === C.y ? A >= E.x && A <= C.x : B === E.y ? A >= E.x : B === C.y ? A <= C.x : !0;
  }
  /**
   * Invalidate cache when terminal content changes
   * Should be called on terminal write, resize, or clear
   */
  invalidateCache() {
    this.linkCache.clear(), this.scannedRows.clear();
  }
  /**
   * Invalidate cache for specific rows
   * Used when only part of the terminal changed
   */
  invalidateRows(A, B) {
    for (let E = A; E <= B; E++)
      this.scannedRows.delete(E);
    const g = [];
    for (const [E, C] of this.linkCache.entries()) {
      const { start: I, end: D } = C.range;
      (I.y >= A && I.y <= B || D.y >= A && D.y <= B || I.y < A && D.y > B) && g.push(E);
    }
    for (const E of g)
      this.linkCache.delete(E);
  }
  /**
   * Dispose and cleanup
   */
  dispose() {
    var A;
    this.linkCache.clear(), this.scannedRows.clear();
    for (const B of this.providers)
      (A = B.dispose) == null || A.call(B);
    this.providers = [];
  }
}
class X {
  constructor(A) {
    this.terminal = A;
  }
  /**
   * Provide all OSC 8 links on the given row
   * Note: This may return links that span multiple rows
   */
  provideLinks(A, B) {
    const g = [], E = /* @__PURE__ */ new Set(), C = this.terminal.buffer.active.getLine(A);
    if (!C) {
      B(void 0);
      return;
    }
    for (let I = 0; I < C.length; I++) {
      const D = C.getCell(I);
      if (!D)
        continue;
      const i = D.getHyperlinkId();
      if (i === 0 || E.has(i))
        continue;
      E.add(i);
      const w = this.findLinkRange(i, A, I);
      if (!this.terminal.wasmTerm)
        continue;
      const s = this.terminal.wasmTerm.getHyperlinkUri(i);
      s && g.push({
        text: s,
        range: w,
        activate: (N) => {
          (N.ctrlKey || N.metaKey) && window.open(s, "_blank", "noopener,noreferrer");
        }
      });
    }
    B(g.length > 0 ? g : void 0);
  }
  /**
   * Find the full extent of a link by scanning for contiguous cells
   * with the same hyperlink_id. Handles multi-line links.
   */
  findLinkRange(A, B, g) {
    const E = this.terminal.buffer.active;
    let C = B, I = g;
    for (; I > 0; ) {
      const s = E.getLine(C);
      if (!s)
        break;
      const N = s.getCell(I - 1);
      if (!N || N.getHyperlinkId() !== A)
        break;
      I--;
    }
    if (I === 0 && C > 0) {
      let s = C - 1;
      for (; s >= 0; ) {
        const N = E.getLine(s);
        if (!N || N.length === 0)
          break;
        const k = N.getCell(N.length - 1);
        if (!k || k.getHyperlinkId() !== A)
          break;
        C = s, I = 0;
        for (let M = N.length - 1; M >= 0; M--) {
          const a = N.getCell(M);
          if (!a || a.getHyperlinkId() !== A) {
            I = M + 1;
            break;
          }
        }
        if (I === 0)
          s--;
        else
          break;
      }
    }
    let D = B, i = g;
    const w = E.getLine(D);
    if (w) {
      for (; i < w.length - 1; ) {
        const s = w.getCell(i + 1);
        if (!s || s.getHyperlinkId() !== A)
          break;
        i++;
      }
      if (i === w.length - 1) {
        let s = D + 1;
        const N = E.length;
        for (; s < N; ) {
          const k = E.getLine(s);
          if (!k || k.length === 0)
            break;
          const M = k.getCell(0);
          if (!M || M.getHyperlinkId() !== A)
            break;
          D = s, i = 0;
          for (let a = 0; a < k.length; a++) {
            const h = k.getCell(a);
            if (!h)
              break;
            if (h.getHyperlinkId() !== A) {
              i = a - 1;
              break;
            }
            i = a;
          }
          if (i === k.length - 1)
            s++;
          else
            break;
        }
      }
    }
    return {
      start: { x: I, y: C },
      end: { x: i, y: D }
    };
  }
  dispose() {
  }
}
const n = class r {
  constructor(A) {
    this.terminal = A;
  }
  /**
   * Provide all regex-detected URLs on the given row
   */
  provideLinks(A, B) {
    const g = [], E = this.terminal.buffer.active.getLine(A);
    if (!E) {
      B(void 0);
      return;
    }
    const C = this.lineToText(E);
    r.URL_REGEX.lastIndex = 0;
    let I = r.URL_REGEX.exec(C);
    for (; I !== null; ) {
      let D = I[0];
      const i = I.index;
      let w = I.index + D.length - 1;
      const s = D.replace(r.TRAILING_PUNCTUATION, "");
      s.length < D.length && (D = s, w = i + D.length - 1), D.length > 8 && g.push({
        text: D,
        range: {
          start: { x: i, y: A },
          end: { x: w, y: A }
        },
        activate: (N) => {
          (N.ctrlKey || N.metaKey) && window.open(D, "_blank", "noopener,noreferrer");
        }
      }), I = r.URL_REGEX.exec(C);
    }
    B(g.length > 0 ? g : void 0);
  }
  /**
   * Convert a buffer line to plain text string
   */
  lineToText(A) {
    const B = [];
    for (let g = 0; g < A.length; g++) {
      const E = A.getCell(g);
      if (!E) {
        B.push(" ");
        continue;
      }
      const C = E.getCodepoint();
      C === 0 || C < 32 ? B.push(" ") : B.push(String.fromCodePoint(C));
    }
    return B.join("");
  }
  dispose() {
  }
};
n.URL_REGEX = /(?:https?:\/\/|mailto:|ftp:\/\/|ssh:\/\/|git:\/\/|tel:|magnet:|gemini:\/\/|gopher:\/\/|news:)[\w\-.~:\/?#@!$&*+,;=%]+/gi;
n.TRAILING_PUNCTUATION = /[.,;!?)\]]+$/;
let _ = n;
const f = {
  foreground: "#d4d4d4",
  background: "#1e1e1e",
  cursor: "#ffffff",
  cursorAccent: "#1e1e1e",
  // Selection colors: solid colors that replace cell bg/fg when selected
  // Using Ghostty's approach: selection bg = default fg, selection fg = default bg
  selectionBackground: "#d4d4d4",
  selectionForeground: "#1e1e1e",
  black: "#000000",
  red: "#cd3131",
  green: "#0dbc79",
  yellow: "#e5e510",
  blue: "#2472c8",
  magenta: "#bc3fbc",
  cyan: "#11a8cd",
  white: "#e5e5e5",
  brightBlack: "#666666",
  brightRed: "#f14c4c",
  brightGreen: "#23d18b",
  brightYellow: "#f5f543",
  brightBlue: "#3b8eea",
  brightMagenta: "#d670d6",
  brightCyan: "#29b8db",
  brightWhite: "#ffffff"
};
class $ {
  constructor(A, B = {}) {
    this.cursorVisible = !0, this.lastCursorPosition = { x: 0, y: 0 }, this.lastViewportY = 0, this.currentBuffer = null, this.currentSelectionCoords = null, this.hoveredHyperlinkId = 0, this.previousHoveredHyperlinkId = 0, this.hoveredLinkRange = null, this.previousHoveredLinkRange = null, this.canvas = A;
    const g = A.getContext("2d", { alpha: !0 });
    if (!g)
      throw new Error("Failed to get 2D rendering context");
    this.ctx = g, this.fontSize = B.fontSize ?? 15, this.fontFamily = B.fontFamily ?? "monospace", this.cursorStyle = B.cursorStyle ?? "block", this.cursorBlink = B.cursorBlink ?? !1, this.theme = { ...f, ...B.theme }, this.devicePixelRatio = B.devicePixelRatio ?? window.devicePixelRatio ?? 1, this.palette = [
      this.theme.black,
      this.theme.red,
      this.theme.green,
      this.theme.yellow,
      this.theme.blue,
      this.theme.magenta,
      this.theme.cyan,
      this.theme.white,
      this.theme.brightBlack,
      this.theme.brightRed,
      this.theme.brightGreen,
      this.theme.brightYellow,
      this.theme.brightBlue,
      this.theme.brightMagenta,
      this.theme.brightCyan,
      this.theme.brightWhite
    ], this.metrics = this.measureFont(), this.cursorBlink && this.startCursorBlink();
  }
  // ==========================================================================
  // Font Metrics Measurement
  // ==========================================================================
  measureFont() {
    const B = document.createElement("canvas").getContext("2d");
    B.font = `${this.fontSize}px ${this.fontFamily}`;
    const g = B.measureText("M"), E = Math.ceil(g.width), C = g.actualBoundingBoxAscent || this.fontSize * 0.8, I = g.actualBoundingBoxDescent || this.fontSize * 0.2, D = Math.ceil(C + I) + 2, i = Math.ceil(C) + 1;
    return { width: E, height: D, baseline: i };
  }
  /**
   * Remeasure font metrics (call after font loads or changes)
   */
  remeasureFont() {
    this.metrics = this.measureFont();
  }
  // ==========================================================================
  // Color Conversion
  // ==========================================================================
  rgbToCSS(A, B, g) {
    return `rgb(${A}, ${B}, ${g})`;
  }
  // ==========================================================================
  // Canvas Sizing
  // ==========================================================================
  /**
   * Resize canvas to fit terminal dimensions
   */
  resize(A, B) {
    const g = A * this.metrics.width, E = B * this.metrics.height;
    this.canvas.style.width = `${g}px`, this.canvas.style.height = `${E}px`, this.canvas.width = g * this.devicePixelRatio, this.canvas.height = E * this.devicePixelRatio, this.ctx.scale(this.devicePixelRatio, this.devicePixelRatio), this.ctx.textBaseline = "alphabetic", this.ctx.textAlign = "left", this.ctx.fillStyle = this.theme.background, this.ctx.fillRect(0, 0, g, E);
  }
  // ==========================================================================
  // Main Rendering
  // ==========================================================================
  /**
   * Render the terminal buffer to canvas
   */
  render(A, B = !1, g = 0, E, C = 1) {
    var U;
    this.currentBuffer = A;
    const I = A.getCursor(), D = A.getDimensions(), i = E ? E.getScrollbackLength() : 0;
    (U = A.needsFullRedraw) != null && U.call(A) && (B = !0), (this.canvas.width !== D.cols * this.metrics.width * this.devicePixelRatio || this.canvas.height !== D.rows * this.metrics.height * this.devicePixelRatio) && (this.resize(D.cols, D.rows), B = !0), g !== this.lastViewportY && (B = !0, this.lastViewportY = g);
    const s = I.x !== this.lastCursorPosition.x || I.y !== this.lastCursorPosition.y;
    if (s || this.cursorBlink) {
      if (!B && !A.isRowDirty(I.y)) {
        const t = A.getLine(I.y);
        t && this.renderLine(t, I.y, D.cols);
      }
      if (s && this.lastCursorPosition.y !== I.y && !B && !A.isRowDirty(this.lastCursorPosition.y)) {
        const t = A.getLine(this.lastCursorPosition.y);
        t && this.renderLine(t, this.lastCursorPosition.y, D.cols);
      }
    }
    const N = this.selectionManager && this.selectionManager.hasSelection(), k = /* @__PURE__ */ new Set();
    if (this.currentSelectionCoords = N ? this.selectionManager.getSelectionCoords() : null, this.currentSelectionCoords) {
      const t = this.currentSelectionCoords;
      for (let c = t.startRow; c <= t.endRow; c++)
        k.add(c);
    }
    if (this.selectionManager) {
      const t = this.selectionManager.getDirtySelectionRows();
      if (t.size > 0) {
        for (const c of t)
          k.add(c);
        this.selectionManager.clearDirtySelectionRows();
      }
    }
    const M = /* @__PURE__ */ new Set(), a = this.hoveredHyperlinkId !== this.previousHoveredHyperlinkId, h = JSON.stringify(this.hoveredLinkRange) !== JSON.stringify(this.previousHoveredLinkRange);
    if (a) {
      for (let t = 0; t < D.rows; t++) {
        let c = null;
        if (g > 0)
          if (t < g && E) {
            const F = i - Math.floor(g) + t;
            c = E.getScrollbackLine(F);
          } else {
            const F = t - Math.floor(g);
            c = A.getLine(F);
          }
        else
          c = A.getLine(t);
        if (c) {
          for (const F of c)
            if (F.hyperlink_id === this.hoveredHyperlinkId || F.hyperlink_id === this.previousHoveredHyperlinkId) {
              M.add(t);
              break;
            }
        }
      }
      this.previousHoveredHyperlinkId = this.hoveredHyperlinkId;
    }
    if (h) {
      if (this.previousHoveredLinkRange)
        for (let t = this.previousHoveredLinkRange.startY; t <= this.previousHoveredLinkRange.endY; t++)
          M.add(t);
      if (this.hoveredLinkRange)
        for (let t = this.hoveredLinkRange.startY; t <= this.hoveredLinkRange.endY; t++)
          M.add(t);
      this.previousHoveredLinkRange = this.hoveredLinkRange;
    }
    const G = /* @__PURE__ */ new Set();
    for (let t = 0; t < D.rows; t++)
      (g > 0 ? !0 : B || A.isRowDirty(t) || k.has(t) || M.has(t)) && (G.add(t), t > 0 && G.add(t - 1), t < D.rows - 1 && G.add(t + 1));
    for (let t = 0; t < D.rows; t++) {
      if (!G.has(t))
        continue;
      let c = null;
      if (g > 0)
        if (t < g && E) {
          const F = i - Math.floor(g) + t;
          c = E.getScrollbackLine(F);
        } else {
          const F = g > 0 ? t - Math.floor(g) : t;
          c = A.getLine(F);
        }
      else
        c = A.getLine(t);
      c && this.renderLine(c, t, D.cols);
    }
    g === 0 && I.visible && this.cursorVisible && this.renderCursor(I.x, I.y), E && C > 0 && this.renderScrollbar(g, i, D.rows, C), this.lastCursorPosition = { x: I.x, y: I.y }, A.clearDirty();
  }
  /**
   * Render a single line using two-pass approach:
   * 1. First pass: Draw all cell backgrounds
   * 2. Second pass: Draw all cell text and decorations
   *
   * This two-pass approach is necessary for proper rendering of complex scripts
   * like Devanagari where diacritics (like vowel sign ि) can extend LEFT of the
   * base character into the previous cell's visual area. If we draw backgrounds
   * and text in a single pass (cell by cell), the background of cell N would
   * cover any left-extending portions of graphemes from cell N-1.
   */
  renderLine(A, B, g) {
    const E = B * this.metrics.height;
    this.ctx.fillStyle = this.theme.background, this.ctx.fillRect(0, E, g * this.metrics.width, this.metrics.height);
    for (let C = 0; C < A.length; C++) {
      const I = A[C];
      I.width !== 0 && this.renderCellBackground(I, C, B);
    }
    for (let C = 0; C < A.length; C++) {
      const I = A[C];
      I.width !== 0 && this.renderCellText(I, C, B);
    }
  }
  /**
   * Render a cell's background only (Pass 1 of two-pass rendering)
   * Selection highlighting is integrated here to avoid z-order issues with
   * complex glyphs (like Devanagari) that extend outside their cell bounds.
   */
  renderCellBackground(A, B, g) {
    const E = B * this.metrics.width, C = g * this.metrics.height, I = this.metrics.width * A.width;
    if (this.isInSelection(B, g)) {
      this.ctx.fillStyle = this.theme.selectionBackground, this.ctx.fillRect(E, C, I, this.metrics.height);
      return;
    }
    let i = A.bg_r, w = A.bg_g, s = A.bg_b;
    A.flags & e.INVERSE && (i = A.fg_r, w = A.fg_g, s = A.fg_b), i === 0 && w === 0 && s === 0 || (this.ctx.fillStyle = this.rgbToCSS(i, w, s), this.ctx.fillRect(E, C, I, this.metrics.height));
  }
  /**
   * Render a cell's text and decorations (Pass 2 of two-pass rendering)
   * Selection foreground color is applied here to match the selection background.
   */
  renderCellText(A, B, g) {
    var k;
    const E = B * this.metrics.width, C = g * this.metrics.height, I = this.metrics.width * A.width;
    if (A.flags & e.INVISIBLE)
      return;
    const D = this.isInSelection(B, g);
    let i = "";
    if (A.flags & e.ITALIC && (i += "italic "), A.flags & e.BOLD && (i += "bold "), this.ctx.font = `${i}${this.fontSize}px ${this.fontFamily}`, D)
      this.ctx.fillStyle = this.theme.selectionForeground;
    else {
      let M = A.fg_r, a = A.fg_g, h = A.fg_b;
      A.flags & e.INVERSE && (M = A.bg_r, a = A.bg_g, h = A.bg_b), this.ctx.fillStyle = this.rgbToCSS(M, a, h);
    }
    A.flags & e.FAINT && (this.ctx.globalAlpha = 0.5);
    const w = E, s = C + this.metrics.baseline;
    let N;
    if (A.grapheme_len > 0 && ((k = this.currentBuffer) != null && k.getGraphemeString) ? N = this.currentBuffer.getGraphemeString(g, B) : N = String.fromCodePoint(A.codepoint || 32), this.ctx.fillText(N, w, s), A.flags & e.FAINT && (this.ctx.globalAlpha = 1), A.flags & e.UNDERLINE) {
      const M = C + this.metrics.baseline + 2;
      this.ctx.strokeStyle = this.ctx.fillStyle, this.ctx.lineWidth = 1, this.ctx.beginPath(), this.ctx.moveTo(E, M), this.ctx.lineTo(E + I, M), this.ctx.stroke();
    }
    if (A.flags & e.STRIKETHROUGH) {
      const M = C + this.metrics.height / 2;
      this.ctx.strokeStyle = this.ctx.fillStyle, this.ctx.lineWidth = 1, this.ctx.beginPath(), this.ctx.moveTo(E, M), this.ctx.lineTo(E + I, M), this.ctx.stroke();
    }
    if (A.hyperlink_id > 0 && A.hyperlink_id === this.hoveredHyperlinkId) {
      const a = C + this.metrics.baseline + 2;
      this.ctx.strokeStyle = "#4A90E2", this.ctx.lineWidth = 1, this.ctx.beginPath(), this.ctx.moveTo(E, a), this.ctx.lineTo(E + I, a), this.ctx.stroke();
    }
    if (this.hoveredLinkRange) {
      const M = this.hoveredLinkRange;
      if (g === M.startY && B >= M.startX && (g < M.endY || B <= M.endX) || g > M.startY && g < M.endY || g === M.endY && B <= M.endX && (g > M.startY || B >= M.startX)) {
        const h = C + this.metrics.baseline + 2;
        this.ctx.strokeStyle = "#4A90E2", this.ctx.lineWidth = 1, this.ctx.beginPath(), this.ctx.moveTo(E, h), this.ctx.lineTo(E + I, h), this.ctx.stroke();
      }
    }
  }
  /**
   * Render cursor
   */
  renderCursor(A, B) {
    const g = A * this.metrics.width, E = B * this.metrics.height;
    switch (this.ctx.fillStyle = this.theme.cursor, this.cursorStyle) {
      case "block":
        this.ctx.fillRect(g, E, this.metrics.width, this.metrics.height);
        break;
      case "underline":
        const C = Math.max(2, Math.floor(this.metrics.height * 0.15));
        this.ctx.fillRect(
          g,
          E + this.metrics.height - C,
          this.metrics.width,
          C
        );
        break;
      case "bar":
        const I = Math.max(2, Math.floor(this.metrics.width * 0.15));
        this.ctx.fillRect(g, E, I, this.metrics.height);
        break;
    }
  }
  // ==========================================================================
  // Cursor Blinking
  // ==========================================================================
  startCursorBlink() {
    this.cursorBlinkInterval = window.setInterval(() => {
      this.cursorVisible = !this.cursorVisible;
    }, 530);
  }
  stopCursorBlink() {
    this.cursorBlinkInterval !== void 0 && (clearInterval(this.cursorBlinkInterval), this.cursorBlinkInterval = void 0), this.cursorVisible = !0;
  }
  // ==========================================================================
  // Public API
  // ==========================================================================
  /**
   * Update theme colors
   */
  setTheme(A) {
    this.theme = { ...f, ...A }, this.palette = [
      this.theme.black,
      this.theme.red,
      this.theme.green,
      this.theme.yellow,
      this.theme.blue,
      this.theme.magenta,
      this.theme.cyan,
      this.theme.white,
      this.theme.brightBlack,
      this.theme.brightRed,
      this.theme.brightGreen,
      this.theme.brightYellow,
      this.theme.brightBlue,
      this.theme.brightMagenta,
      this.theme.brightCyan,
      this.theme.brightWhite
    ];
  }
  /**
   * Update font size
   */
  setFontSize(A) {
    this.fontSize = A, this.metrics = this.measureFont();
  }
  /**
   * Update font family
   */
  setFontFamily(A) {
    this.fontFamily = A, this.metrics = this.measureFont();
  }
  /**
   * Update cursor style
   */
  setCursorStyle(A) {
    this.cursorStyle = A;
  }
  /**
   * Enable/disable cursor blinking
   */
  setCursorBlink(A) {
    A && !this.cursorBlink ? (this.cursorBlink = !0, this.startCursorBlink()) : !A && this.cursorBlink && (this.cursorBlink = !1, this.stopCursorBlink());
  }
  /**
   * Get current font metrics
   */
  /**
   * Render scrollbar (Phase 2)
   * Shows scroll position and allows click/drag interaction
   * @param opacity Opacity level (0-1) for fade in/out effect
   */
  renderScrollbar(A, B, g, E = 1) {
    const C = this.ctx, I = this.canvas.height / this.devicePixelRatio, D = this.canvas.width / this.devicePixelRatio, i = 8, w = D - i - 4, s = 4, N = I - s * 2;
    if (C.fillStyle = this.theme.background, C.fillRect(w - 2, 0, i + 6, I), E <= 0 || B === 0)
      return;
    const k = B + g, M = Math.max(20, g / k * N), a = A / B, h = s + (N - M) * (1 - a);
    C.fillStyle = `rgba(128, 128, 128, ${0.1 * E})`, C.fillRect(w, s, i, N);
    const U = A > 0 ? 0.5 : 0.3;
    C.fillStyle = `rgba(128, 128, 128, ${U * E})`, C.fillRect(w, h, i, M);
  }
  getMetrics() {
    return { ...this.metrics };
  }
  /**
   * Get canvas element (needed by SelectionManager)
   */
  getCanvas() {
    return this.canvas;
  }
  /**
   * Set selection manager (for rendering selection)
   */
  setSelectionManager(A) {
    this.selectionManager = A;
  }
  /**
   * Check if a cell at (x, y) is within the current selection.
   * Uses cached selection coordinates for performance.
   */
  isInSelection(A, B) {
    const g = this.currentSelectionCoords;
    if (!g)
      return !1;
    const { startCol: E, startRow: C, endCol: I, endRow: D } = g;
    return C === D ? B === C && A >= E && A <= I : B === C ? A >= E : B === D ? A <= I : B > C && B < D;
  }
  /**
   * Set the currently hovered hyperlink ID for rendering underlines
   */
  setHoveredHyperlinkId(A) {
    this.hoveredHyperlinkId = A;
  }
  /**
   * Set the currently hovered link range for rendering underlines (for regex-detected URLs)
   * Pass null to clear the hover state
   */
  setHoveredLinkRange(A) {
    this.hoveredLinkRange = A;
  }
  /**
   * Get character cell width (for coordinate conversion)
   */
  get charWidth() {
    return this.metrics.width;
  }
  /**
   * Get character cell height (for coordinate conversion)
   */
  get charHeight() {
    return this.metrics.height;
  }
  /**
   * Clear entire canvas
   */
  clear() {
    this.ctx.fillStyle = this.theme.background, this.ctx.fillRect(0, 0, this.canvas.width, this.canvas.height);
  }
  /**
   * Cleanup resources
   */
  dispose() {
    this.stopCursorBlink();
  }
}
const L = class Y {
  // ms between scroll steps
  constructor(A, B, g, E) {
    this.selectionStart = null, this.selectionEnd = null, this.isSelecting = !1, this.mouseDownTarget = null, this.dirtySelectionRows = /* @__PURE__ */ new Set(), this.selectionChangedEmitter = new J(), this.boundMouseUpHandler = null, this.boundContextMenuHandler = null, this.boundClickHandler = null, this.boundDocumentMouseMoveHandler = null, this.autoScrollInterval = null, this.autoScrollDirection = 0, this.terminal = A, this.renderer = B, this.wasmTerm = g, this.textarea = E, this.attachEventListeners();
  }
  // pixels from edge to trigger scroll
  /**
   * Get current viewport Y position (how many lines scrolled into history)
   */
  getViewportY() {
    const A = typeof this.terminal.getViewportY == "function" ? this.terminal.getViewportY() : this.terminal.viewportY || 0;
    return Math.max(0, Math.floor(A));
  }
  /**
   * Convert viewport row to absolute buffer row
   * Absolute row is an index into combined buffer: scrollback (0 to len-1) + screen (len to len+rows-1)
   */
  viewportRowToAbsolute(A) {
    const B = this.wasmTerm.getScrollbackLength(), g = this.getViewportY();
    return B + A - g;
  }
  /**
   * Convert absolute buffer row to viewport row (may be outside visible range)
   */
  absoluteRowToViewport(A) {
    const B = this.wasmTerm.getScrollbackLength(), g = this.getViewportY();
    return A - B + g;
  }
  // ==========================================================================
  // Public API
  // ==========================================================================
  /**
   * Get the selected text as a string
   */
  getSelection() {
    if (!this.selectionStart || !this.selectionEnd)
      return "";
    let { col: A, absoluteRow: B } = this.selectionStart, { col: g, absoluteRow: E } = this.selectionEnd;
    (B > E || B === E && A > g) && ([A, g] = [g, A], [B, E] = [E, B]);
    const C = this.wasmTerm.getScrollbackLength();
    let I = "";
    for (let D = B; D <= E; D++) {
      let i = null;
      if (D < C)
        i = this.wasmTerm.getScrollbackLine(D);
      else {
        const M = D - C;
        i = this.wasmTerm.getLine(M);
      }
      if (!i)
        continue;
      let w = -1;
      const s = D === B ? A : 0, N = D === E ? g : i.length - 1;
      let k = "";
      for (let M = s; M <= N; M++) {
        const a = i[M];
        if (a && a.codepoint !== 0) {
          let h;
          if (a.grapheme_len > 0)
            if (D < C)
              h = this.wasmTerm.getScrollbackGraphemeString(D, M);
            else {
              const G = D - C;
              h = this.wasmTerm.getGraphemeString(G, M);
            }
          else
            h = String.fromCodePoint(a.codepoint);
          k += h, h.trim() && (w = k.length);
        } else
          k += " ";
      }
      w >= 0 ? k = k.substring(0, w) : k = "", I += k, D < E && (I += `
`);
    }
    return I;
  }
  /**
   * Check if there's an active selection
   */
  hasSelection() {
    return !this.selectionStart || !this.selectionEnd ? !1 : !(this.selectionStart.col === this.selectionEnd.col && this.selectionStart.absoluteRow === this.selectionEnd.absoluteRow);
  }
  /**
   * Clear the selection
   */
  clearSelection() {
    if (!this.hasSelection())
      return;
    const A = this.normalizeSelection();
    if (A)
      for (let B = A.startRow; B <= A.endRow; B++)
        this.dirtySelectionRows.add(B);
    this.selectionStart = null, this.selectionEnd = null, this.isSelecting = !1, this.requestRender();
  }
  /**
   * Select all text in the terminal
   */
  selectAll() {
    const A = this.wasmTerm.getDimensions(), B = this.wasmTerm.getScrollbackLength();
    this.selectionStart = { col: 0, absoluteRow: 0 }, this.selectionEnd = { col: A.cols - 1, absoluteRow: B + A.rows - 1 }, this.requestRender(), this.selectionChangedEmitter.fire();
  }
  /**
   * Select text at specific column and row with length
   * xterm.js compatible API
   */
  select(A, B, g) {
    const E = this.wasmTerm.getDimensions();
    B = Math.max(0, Math.min(B, E.rows - 1)), A = Math.max(0, Math.min(A, E.cols - 1));
    let C = B, I = A + g - 1;
    for (; I >= E.cols; )
      I -= E.cols, C++;
    C = Math.min(C, E.rows - 1);
    const D = this.getViewportY();
    this.selectionStart = { col: A, absoluteRow: D + B }, this.selectionEnd = { col: I, absoluteRow: D + C }, this.requestRender(), this.selectionChangedEmitter.fire();
  }
  /**
   * Select entire lines from start to end
   * xterm.js compatible API
   */
  selectLines(A, B) {
    const g = this.wasmTerm.getDimensions();
    A = Math.max(0, Math.min(A, g.rows - 1)), B = Math.max(0, Math.min(B, g.rows - 1)), A > B && ([A, B] = [B, A]);
    const E = this.getViewportY();
    this.selectionStart = { col: 0, absoluteRow: E + A }, this.selectionEnd = { col: g.cols - 1, absoluteRow: E + B }, this.requestRender(), this.selectionChangedEmitter.fire();
  }
  /**
   * Get selection position as buffer range
   * xterm.js compatible API
   */
  getSelectionPosition() {
    const A = this.normalizeSelection();
    if (A)
      return {
        start: { x: A.startCol, y: A.startRow },
        end: { x: A.endCol, y: A.endRow }
      };
  }
  /**
   * Deselect all text
   * xterm.js compatible API
   */
  deselect() {
    this.clearSelection(), this.selectionChangedEmitter.fire();
  }
  /**
   * Focus the terminal (make it receive keyboard input)
   */
  focus() {
    const A = this.renderer.getCanvas();
    A.parentElement && A.parentElement.focus();
  }
  /**
   * Get current selection coordinates (for rendering)
   */
  getSelectionCoords() {
    return this.normalizeSelection();
  }
  /**
   * Get dirty selection rows that need redraw (for clearing old highlight)
   */
  getDirtySelectionRows() {
    return this.dirtySelectionRows;
  }
  /**
   * Clear the dirty selection rows tracking (after redraw)
   */
  clearDirtySelectionRows() {
    this.dirtySelectionRows.clear();
  }
  /**
   * Get selection change event accessor
   */
  get onSelectionChange() {
    return this.selectionChangedEmitter.event;
  }
  /**
   * Cleanup resources
   */
  dispose() {
    this.selectionChangedEmitter.dispose(), this.stopAutoScroll(), this.boundMouseUpHandler && (document.removeEventListener("mouseup", this.boundMouseUpHandler), this.boundMouseUpHandler = null), this.boundDocumentMouseMoveHandler && (document.removeEventListener("mousemove", this.boundDocumentMouseMoveHandler), this.boundDocumentMouseMoveHandler = null), this.boundContextMenuHandler && (this.renderer.getCanvas().removeEventListener("contextmenu", this.boundContextMenuHandler), this.boundContextMenuHandler = null), this.boundClickHandler && (document.removeEventListener("click", this.boundClickHandler), this.boundClickHandler = null);
  }
  // ==========================================================================
  // Private Methods
  // ==========================================================================
  /**
   * Attach mouse event listeners to canvas
   */
  attachEventListeners() {
    const A = this.renderer.getCanvas();
    A.addEventListener("mousedown", (B) => {
      if (B.button === 0) {
        A.parentElement && A.parentElement.focus();
        const g = this.pixelToCell(B.offsetX, B.offsetY);
        this.hasSelection() && this.clearSelection();
        const C = this.viewportRowToAbsolute(g.row);
        this.selectionStart = { col: g.col, absoluteRow: C }, this.selectionEnd = { col: g.col, absoluteRow: C }, this.isSelecting = !0;
      }
    }), A.addEventListener("mousemove", (B) => {
      if (this.isSelecting) {
        this.markCurrentSelectionDirty();
        const g = this.pixelToCell(B.offsetX, B.offsetY), E = this.viewportRowToAbsolute(g.row);
        this.selectionEnd = { col: g.col, absoluteRow: E }, this.requestRender(), this.updateAutoScroll(B.offsetY, A.clientHeight);
      }
    }), A.addEventListener("mouseleave", (B) => {
      if (this.isSelecting) {
        const g = A.getBoundingClientRect();
        B.clientY < g.top ? this.startAutoScroll(-1) : B.clientY > g.bottom && this.startAutoScroll(1);
      }
    }), A.addEventListener("mouseenter", () => {
      this.isSelecting && this.stopAutoScroll();
    }), this.boundDocumentMouseMoveHandler = (B) => {
      if (this.isSelecting) {
        const g = A.getBoundingClientRect(), E = Math.max(g.left, Math.min(B.clientX, g.right)), C = Math.max(g.top, Math.min(B.clientY, g.bottom)), I = E - g.left, D = C - g.top;
        if ((B.clientX < g.left || B.clientX > g.right || B.clientY < g.top || B.clientY > g.bottom) && (B.clientY < g.top ? this.startAutoScroll(-1) : B.clientY > g.bottom ? this.startAutoScroll(1) : this.stopAutoScroll(), this.autoScrollDirection === 0)) {
          this.markCurrentSelectionDirty();
          const i = this.pixelToCell(I, D), w = this.viewportRowToAbsolute(i.row);
          this.selectionEnd = { col: i.col, absoluteRow: w }, this.requestRender();
        }
      }
    }, document.addEventListener("mousemove", this.boundDocumentMouseMoveHandler), document.addEventListener("mousedown", (B) => {
      this.mouseDownTarget = B.target;
    }), this.boundMouseUpHandler = (B) => {
      if (this.isSelecting) {
        this.isSelecting = !1, this.stopAutoScroll();
        const g = this.getSelection();
        g && (this.copyToClipboard(g), this.selectionChangedEmitter.fire());
      }
    }, document.addEventListener("mouseup", this.boundMouseUpHandler), A.addEventListener("dblclick", (B) => {
      const g = this.pixelToCell(B.offsetX, B.offsetY), E = this.getWordAtCell(g.col, g.row);
      if (E) {
        const C = this.viewportRowToAbsolute(g.row);
        this.selectionStart = { col: E.startCol, absoluteRow: C }, this.selectionEnd = { col: E.endCol, absoluteRow: C }, this.requestRender();
        const I = this.getSelection();
        I && (this.copyToClipboard(I), this.selectionChangedEmitter.fire());
      }
    }), this.boundContextMenuHandler = (B) => {
      if (this.renderer.getCanvas().getBoundingClientRect(), this.textarea.style.position = "fixed", this.textarea.style.left = `${B.clientX}px`, this.textarea.style.top = `${B.clientY}px`, this.textarea.style.width = "1px", this.textarea.style.height = "1px", this.textarea.style.zIndex = "1000", this.textarea.style.opacity = "0", this.textarea.style.pointerEvents = "auto", this.hasSelection()) {
        const E = this.getSelection();
        this.textarea.value = E, this.textarea.select(), this.textarea.setSelectionRange(0, E.length);
      } else
        this.textarea.value = "";
      this.textarea.focus(), setTimeout(() => {
        const E = () => {
          this.textarea.style.pointerEvents = "none", this.textarea.style.zIndex = "-10", this.textarea.style.width = "0", this.textarea.style.height = "0", this.textarea.style.left = "0", this.textarea.style.top = "0", this.textarea.value = "", document.removeEventListener("click", E), document.removeEventListener("contextmenu", E), this.textarea.removeEventListener("blur", E);
        };
        document.addEventListener("click", E, { once: !0 }), document.addEventListener("contextmenu", E, { once: !0 }), this.textarea.addEventListener("blur", E, { once: !0 });
      }, 10);
    }, A.addEventListener("contextmenu", this.boundContextMenuHandler), this.boundClickHandler = (B) => {
      if (this.isSelecting || this.mouseDownTarget && A.contains(this.mouseDownTarget))
        return;
      const E = B.target;
      A.contains(E) || this.hasSelection() && this.clearSelection();
    }, document.addEventListener("click", this.boundClickHandler);
  }
  /**
   * Mark current selection rows as dirty for redraw
   */
  markCurrentSelectionDirty() {
    const A = this.normalizeSelection();
    if (A)
      for (let B = A.startRow; B <= A.endRow; B++)
        this.dirtySelectionRows.add(B);
  }
  /**
   * Update auto-scroll based on mouse Y position within canvas
   */
  updateAutoScroll(A, B) {
    const g = Y.AUTO_SCROLL_EDGE_SIZE;
    A < g ? this.startAutoScroll(-1) : A > B - g ? this.startAutoScroll(1) : this.stopAutoScroll();
  }
  /**
   * Start auto-scrolling in the given direction
   */
  startAutoScroll(A) {
    this.autoScrollInterval !== null && this.autoScrollDirection === A || (this.stopAutoScroll(), this.autoScrollDirection = A, this.autoScrollInterval = setInterval(() => {
      if (!this.isSelecting) {
        this.stopAutoScroll();
        return;
      }
      const B = Y.AUTO_SCROLL_SPEED * this.autoScrollDirection;
      if (this.terminal.scrollLines(B), this.selectionEnd) {
        const g = this.wasmTerm.getDimensions();
        if (this.autoScrollDirection < 0) {
          const E = this.viewportRowToAbsolute(0);
          E < this.selectionEnd.absoluteRow && (this.selectionEnd = { col: 0, absoluteRow: E });
        } else {
          const E = this.viewportRowToAbsolute(g.rows - 1);
          E > this.selectionEnd.absoluteRow && (this.selectionEnd = { col: g.cols - 1, absoluteRow: E });
        }
      }
      this.requestRender();
    }, Y.AUTO_SCROLL_INTERVAL));
  }
  /**
   * Stop auto-scrolling
   */
  stopAutoScroll() {
    this.autoScrollInterval !== null && (clearInterval(this.autoScrollInterval), this.autoScrollInterval = null), this.autoScrollDirection = 0;
  }
  /**
   * Convert pixel coordinates to terminal cell coordinates
   */
  pixelToCell(A, B) {
    const g = this.renderer.getMetrics(), E = Math.floor(A / g.width), C = Math.floor(B / g.height);
    return {
      col: Math.max(0, Math.min(E, this.terminal.cols - 1)),
      row: Math.max(0, Math.min(C, this.terminal.rows - 1))
    };
  }
  /**
   * Normalize selection coordinates (handle backward selection)
   * Returns coordinates in VIEWPORT space for rendering, clamped to visible area
   */
  normalizeSelection() {
    if (!this.selectionStart || !this.selectionEnd)
      return null;
    let { col: A, absoluteRow: B } = this.selectionStart, { col: g, absoluteRow: E } = this.selectionEnd;
    (B > E || B === E && A > g) && ([A, g] = [g, A], [B, E] = [E, B]);
    let C = this.absoluteRowToViewport(B), I = this.absoluteRowToViewport(E);
    const D = this.wasmTerm.getDimensions(), i = D.rows - 1;
    return I < 0 || C > i ? null : (C < 0 && (C = 0, A = 0), I > i && (I = i, g = D.cols - 1), { startCol: A, startRow: C, endCol: g, endRow: I });
  }
  /**
   * Get word boundaries at a cell position
   */
  getWordAtCell(A, B) {
    const g = this.wasmTerm.getLine(B);
    if (!g)
      return null;
    const E = (D) => {
      if (!D || D.codepoint === 0)
        return !1;
      const i = String.fromCodePoint(D.codepoint);
      return /[\w-]/.test(i);
    };
    if (!E(g[A]))
      return null;
    let C = A;
    for (; C > 0 && E(g[C - 1]); )
      C--;
    let I = A;
    for (; I < g.length - 1 && E(g[I + 1]); )
      I++;
    return { startCol: C, endCol: I };
  }
  /**
   * Copy text to clipboard
   */
  async copyToClipboard(A) {
    if (navigator.clipboard && navigator.clipboard.writeText)
      try {
        await navigator.clipboard.writeText(A);
        return;
      } catch {
      }
    const B = document.activeElement;
    try {
      const g = this.textarea;
      g.value = A, g.style.position = "fixed", g.style.left = "-9999px", g.style.top = "0", g.style.width = "1px", g.style.height = "1px", g.style.opacity = "0", g.focus(), g.select(), g.setSelectionRange(0, A.length);
      const E = document.execCommand("copy");
      B && B.focus(), E || console.error("❌ execCommand copy failed");
    } catch (g) {
      console.error("❌ Fallback copy failed:", g), B && B.focus();
    }
  }
  /**
   * Request a render update (triggers selection overlay redraw)
   */
  requestRender() {
  }
};
L.AUTO_SCROLL_EDGE_SIZE = 30;
L.AUTO_SCROLL_SPEED = 3;
L.AUTO_SCROLL_INTERVAL = 50;
let AA = L;
class IA {
  // 200ms fade animation
  constructor(A = {}) {
    this.unicode = {
      get activeVersion() {
        return "15.1";
      }
    }, this.dataEmitter = new J(), this.resizeEmitter = new J(), this.bellEmitter = new J(), this.selectionChangeEmitter = new J(), this.keyEmitter = new J(), this.titleChangeEmitter = new J(), this.scrollEmitter = new J(), this.renderEmitter = new J(), this.cursorMoveEmitter = new J(), this.onData = this.dataEmitter.event, this.onResize = this.resizeEmitter.event, this.onBell = this.bellEmitter.event, this.onSelectionChange = this.selectionChangeEmitter.event, this.onKey = this.keyEmitter.event, this.onTitleChange = this.titleChangeEmitter.event, this.onScroll = this.scrollEmitter.event, this.onRender = this.renderEmitter.event, this.onCursorMove = this.cursorMoveEmitter.event, this.isOpen = !1, this.isDisposed = !1, this.addons = [], this.currentTitle = "", this.viewportY = 0, this.targetViewportY = 0, this.lastCursorY = 0, this.isDraggingScrollbar = !1, this.scrollbarDragStart = null, this.scrollbarDragStartViewportY = 0, this.scrollbarVisible = !1, this.scrollbarOpacity = 0, this.SCROLLBAR_HIDE_DELAY_MS = 1500, this.SCROLLBAR_FADE_DURATION_MS = 200, this.animateScroll = () => {
      if (!this.wasmTerm || this.scrollAnimationStartTime === void 0)
        return;
      const g = this.options.smoothScrollDuration ?? 100, E = this.targetViewportY - this.viewportY;
      if (Math.abs(E) < 0.01) {
        this.viewportY = this.targetViewportY, this.scrollEmitter.fire(Math.floor(this.viewportY)), this.getScrollbackLength() > 0 && this.showScrollbar(), this.scrollAnimationFrame = void 0, this.scrollAnimationStartTime = void 0, this.scrollAnimationStartY = void 0;
        return;
      }
      const D = 1 - (1 / (g / 1e3 * 60)) ** 2;
      this.viewportY += E * D;
      const i = Math.floor(this.viewportY);
      this.scrollEmitter.fire(i), this.getScrollbackLength() > 0 && this.showScrollbar(), this.scrollAnimationFrame = requestAnimationFrame(this.animateScroll);
    }, this.handleMouseMove = (g) => {
      if (!(!this.canvas || !this.renderer || !this.wasmTerm)) {
        if (this.isDraggingScrollbar) {
          this.processScrollbarDrag(g);
          return;
        }
        if (this.linkDetector) {
          if (this.mouseMoveThrottleTimeout) {
            this.pendingMouseMove = g;
            return;
          }
          this.processMouseMove(g), this.mouseMoveThrottleTimeout = window.setTimeout(() => {
            if (this.mouseMoveThrottleTimeout = void 0, this.pendingMouseMove) {
              const E = this.pendingMouseMove;
              this.pendingMouseMove = void 0, this.processMouseMove(E);
            }
          }, 16);
        }
      }
    }, this.handleMouseLeave = () => {
      var g, E;
      this.renderer && this.wasmTerm && ((this.renderer.hoveredHyperlinkId || 0) > 0 && this.renderer.setHoveredHyperlinkId(0), this.renderer.setHoveredLinkRange(null)), this.currentHoveredLink && ((E = (g = this.currentHoveredLink).hover) == null || E.call(g, !1), this.currentHoveredLink = void 0, this.element && (this.element.style.cursor = "text"));
    }, this.handleClick = async (g) => {
      if (!this.canvas || !this.renderer || !this.linkDetector || !this.wasmTerm)
        return;
      const E = this.canvas.getBoundingClientRect(), C = Math.floor((g.clientX - E.left) / this.renderer.charWidth), D = Math.floor((g.clientY - E.top) / this.renderer.charHeight), i = this.wasmTerm.getScrollbackLength();
      let w;
      const s = this.getViewportY(), N = Math.max(0, Math.floor(s));
      if (N > 0)
        if (D < N)
          w = i - N + D;
        else {
          const M = D - N;
          w = i + M;
        }
      else
        w = i + D;
      const k = await this.linkDetector.getLinkAt(C, w);
      k && (k.activate(g), (g.ctrlKey || g.metaKey) && g.preventDefault());
    }, this.handleWheel = (g) => {
      var C, I, D;
      if (g.preventDefault(), g.stopPropagation(), this.customWheelEventHandler && this.customWheelEventHandler(g))
        return;
      if (((C = this.wasmTerm) == null ? void 0 : C.isAlternateScreen()) ?? !1) {
        const i = g.deltaY > 0 ? "down" : "up", w = Math.min(Math.abs(Math.round(g.deltaY / 33)), 5);
        for (let s = 0; s < w; s++)
          i === "up" ? this.dataEmitter.fire("\x1B[A") : this.dataEmitter.fire("\x1B[B");
      } else {
        let i;
        if (g.deltaMode === WheelEvent.DOM_DELTA_PIXEL) {
          const w = ((D = (I = this.renderer) == null ? void 0 : I.getMetrics()) == null ? void 0 : D.height) ?? 20;
          i = g.deltaY / w;
        } else
          g.deltaMode === WheelEvent.DOM_DELTA_LINE ? i = g.deltaY : g.deltaMode === WheelEvent.DOM_DELTA_PAGE ? i = g.deltaY * this.rows : i = g.deltaY / 33;
        if (i !== 0) {
          const w = this.viewportY - i;
          this.smoothScrollTo(w);
        }
      }
    }, this.handleMouseDown = (g) => {
      if (!this.canvas || !this.renderer || !this.wasmTerm)
        return;
      const E = this.wasmTerm.getScrollbackLength();
      if (E === 0)
        return;
      const C = this.canvas.getBoundingClientRect(), I = g.clientX - C.left, D = g.clientY - C.top, i = C.width, w = C.height, s = 8, N = i - s - 4, k = 4;
      if (I >= N && I <= N + s) {
        g.preventDefault(), g.stopPropagation(), g.stopImmediatePropagation();
        const M = w - k * 2, a = this.rows, h = E + a, G = Math.max(20, a / h * M), U = this.viewportY / E, t = k + (M - G) * (1 - U);
        if (D >= t && D <= t + G)
          this.isDraggingScrollbar = !0, this.scrollbarDragStart = D, this.scrollbarDragStartViewportY = this.viewportY, this.canvas && (this.canvas.style.userSelect = "none", this.canvas.style.webkitUserSelect = "none");
        else {
          const F = 1 - (D - k) / M, S = Math.round(F * E);
          this.scrollToLine(Math.max(0, Math.min(E, S)));
        }
      }
    }, this.handleMouseUp = () => {
      this.isDraggingScrollbar && (this.isDraggingScrollbar = !1, this.scrollbarDragStart = null, this.canvas && (this.canvas.style.userSelect = "", this.canvas.style.webkitUserSelect = ""), this.scrollbarVisible && this.getScrollbackLength() > 0 && this.showScrollbar());
    }, this.ghostty = A.ghostty ?? CA();
    const B = {
      cols: A.cols ?? 80,
      rows: A.rows ?? 24,
      cursorBlink: A.cursorBlink ?? !1,
      cursorStyle: A.cursorStyle ?? "block",
      theme: A.theme ?? {},
      scrollback: A.scrollback ?? 1e4,
      fontSize: A.fontSize ?? 15,
      fontFamily: A.fontFamily ?? "monospace",
      allowTransparency: A.allowTransparency ?? !1,
      convertEol: A.convertEol ?? !1,
      disableStdin: A.disableStdin ?? !1,
      smoothScrollDuration: A.smoothScrollDuration ?? 100
      // Default: 100ms smooth scroll
    };
    this.options = new Proxy(B, {
      set: (g, E, C) => {
        const I = g[E];
        return g[E] = C, this.isOpen && this.handleOptionChange(E, C, I), !0;
      }
    }), this.cols = this.options.cols, this.rows = this.options.rows, this.buffer = new Z(this);
  }
  // ==========================================================================
  // Option Change Handling (for mutable options)
  // ==========================================================================
  /**
   * Handle runtime option changes (called when options are modified after terminal is open)
   * This enables xterm.js compatibility where options can be changed at runtime
   */
  handleOptionChange(A, B, g) {
    if (B !== g)
      switch (A) {
        case "disableStdin":
          break;
        case "cursorBlink":
        case "cursorStyle":
          this.renderer && (this.renderer.setCursorStyle(this.options.cursorStyle), this.renderer.setCursorBlink(this.options.cursorBlink));
          break;
        case "theme":
          this.renderer && console.warn("ghostty-web: theme changes after open() are not yet fully supported");
          break;
        case "fontSize":
          this.renderer && (this.renderer.setFontSize(this.options.fontSize), this.handleFontChange());
          break;
        case "fontFamily":
          this.renderer && (this.renderer.setFontFamily(this.options.fontFamily), this.handleFontChange());
          break;
        case "cols":
        case "rows":
          this.resize(this.options.cols, this.options.rows);
          break;
      }
  }
  /**
   * Handle font changes (fontSize or fontFamily)
   * Updates canvas size to match new font metrics and forces a full re-render
   */
  handleFontChange() {
    if (!this.renderer || !this.wasmTerm || !this.canvas)
      return;
    this.selectionManager && this.selectionManager.clearSelection(), this.renderer.resize(this.cols, this.rows);
    const A = this.renderer.getMetrics();
    this.canvas.width = A.width * this.cols, this.canvas.height = A.height * this.rows, this.canvas.style.width = `${A.width * this.cols}px`, this.canvas.style.height = `${A.height * this.rows}px`, this.renderer.render(this.wasmTerm, !0, this.viewportY, this);
  }
  /**
   * Parse a CSS color string to 0xRRGGBB format.
   * Returns 0 if the color is undefined or invalid.
   */
  parseColorToHex(A) {
    if (!A)
      return 0;
    if (A.startsWith("#")) {
      let g = A.slice(1);
      g.length === 3 && (g = g[0] + g[0] + g[1] + g[1] + g[2] + g[2]);
      const E = Number.parseInt(g, 16);
      return Number.isNaN(E) ? 0 : E;
    }
    const B = A.match(/rgb\((\d+),\s*(\d+),\s*(\d+)\)/);
    if (B) {
      const g = Number.parseInt(B[1], 10), E = Number.parseInt(B[2], 10), C = Number.parseInt(B[3], 10);
      return g << 16 | E << 8 | C;
    }
    return 0;
  }
  /**
   * Convert terminal options to WASM terminal config.
   */
  buildWasmConfig() {
    const A = this.options.theme, B = this.options.scrollback;
    if (!A && B === 1e4)
      return;
    const g = [
      this.parseColorToHex(A == null ? void 0 : A.black),
      this.parseColorToHex(A == null ? void 0 : A.red),
      this.parseColorToHex(A == null ? void 0 : A.green),
      this.parseColorToHex(A == null ? void 0 : A.yellow),
      this.parseColorToHex(A == null ? void 0 : A.blue),
      this.parseColorToHex(A == null ? void 0 : A.magenta),
      this.parseColorToHex(A == null ? void 0 : A.cyan),
      this.parseColorToHex(A == null ? void 0 : A.white),
      this.parseColorToHex(A == null ? void 0 : A.brightBlack),
      this.parseColorToHex(A == null ? void 0 : A.brightRed),
      this.parseColorToHex(A == null ? void 0 : A.brightGreen),
      this.parseColorToHex(A == null ? void 0 : A.brightYellow),
      this.parseColorToHex(A == null ? void 0 : A.brightBlue),
      this.parseColorToHex(A == null ? void 0 : A.brightMagenta),
      this.parseColorToHex(A == null ? void 0 : A.brightCyan),
      this.parseColorToHex(A == null ? void 0 : A.brightWhite)
    ];
    return {
      scrollbackLimit: B,
      fgColor: this.parseColorToHex(A == null ? void 0 : A.foreground),
      bgColor: this.parseColorToHex(A == null ? void 0 : A.background),
      cursorColor: this.parseColorToHex(A == null ? void 0 : A.cursor),
      palette: g
    };
  }
  // ==========================================================================
  // Lifecycle Methods
  // ==========================================================================
  /**
   * Open terminal in a parent element
   *
   * Initializes all components and starts rendering.
   * Requires a pre-loaded Ghostty instance passed to the constructor.
   */
  open(A) {
    if (this.isOpen)
      throw new Error("Terminal is already open");
    if (this.isDisposed)
      throw new Error("Terminal has been disposed");
    this.element = A, this.isOpen = !0;
    try {
      A.hasAttribute("tabindex") || A.setAttribute("tabindex", "0"), A.setAttribute("contenteditable", "true"), A.addEventListener("beforeinput", (E) => E.preventDefault()), A.setAttribute("role", "textbox"), A.setAttribute("aria-label", "Terminal input"), A.setAttribute("aria-multiline", "true");
      const B = this.buildWasmConfig();
      this.wasmTerm = this.ghostty.createTerminal(this.cols, this.rows, B), this.canvas = document.createElement("canvas"), this.canvas.style.display = "block", A.appendChild(this.canvas), this.textarea = document.createElement("textarea"), this.textarea.setAttribute("autocorrect", "off"), this.textarea.setAttribute("autocapitalize", "off"), this.textarea.setAttribute("spellcheck", "false"), this.textarea.setAttribute("tabindex", "0"), this.textarea.setAttribute("aria-label", "Terminal input"), this.textarea.style.position = "absolute", this.textarea.style.left = "0", this.textarea.style.top = "0", this.textarea.style.width = "1px", this.textarea.style.height = "1px", this.textarea.style.padding = "0", this.textarea.style.border = "none", this.textarea.style.margin = "0", this.textarea.style.opacity = "0", this.textarea.style.clipPath = "inset(50%)", this.textarea.style.overflow = "hidden", this.textarea.style.whiteSpace = "nowrap", this.textarea.style.resize = "none", A.appendChild(this.textarea);
      const g = this.textarea;
      this.canvas.addEventListener("mousedown", (E) => {
        E.preventDefault(), g.focus();
      }), this.canvas.addEventListener("touchend", (E) => {
        E.preventDefault(), g.focus();
      }), this.renderer = new $(this.canvas, {
        fontSize: this.options.fontSize,
        fontFamily: this.options.fontFamily,
        cursorStyle: this.options.cursorStyle,
        cursorBlink: this.options.cursorBlink,
        theme: this.options.theme
      }), this.renderer.resize(this.cols, this.rows), this.inputHandler = new P(
        this.ghostty,
        A,
        (E) => {
          this.options.disableStdin || this.dataEmitter.fire(E);
        },
        () => {
          this.bellEmitter.fire();
        },
        (E) => {
          this.keyEmitter.fire(E);
        },
        this.customKeyEventHandler,
        (E) => {
          var C;
          return ((C = this.wasmTerm) == null ? void 0 : C.getMode(E, !1)) ?? !1;
        }
      ), this.selectionManager = new AA(
        this,
        this.renderer,
        this.wasmTerm,
        this.textarea
      ), this.renderer.setSelectionManager(this.selectionManager), this.selectionManager.onSelectionChange(() => {
        this.selectionChangeEmitter.fire();
      }), this.textarea.addEventListener("paste", (E) => {
        var I;
        E.preventDefault(), E.stopPropagation();
        const C = (I = E.clipboardData) == null ? void 0 : I.getData("text");
        C && this.paste(C);
      }), this.linkDetector = new v(this), this.linkDetector.registerProvider(new X(this)), this.linkDetector.registerProvider(new _(this)), A.addEventListener("mousedown", this.handleMouseDown, { capture: !0 }), A.addEventListener("mousemove", this.handleMouseMove), A.addEventListener("mouseleave", this.handleMouseLeave), A.addEventListener("click", this.handleClick), document.addEventListener("mouseup", this.handleMouseUp), A.addEventListener("wheel", this.handleWheel, { passive: !1, capture: !0 }), this.renderer.render(this.wasmTerm, !0, this.viewportY, this, this.scrollbarOpacity), this.startRenderLoop(), this.focus();
    } catch (B) {
      throw this.isOpen = !1, this.cleanupComponents(), new Error(`Failed to open terminal: ${B}`);
    }
  }
  /**
   * Write data to terminal
   */
  write(A, B) {
    this.assertOpen(), this.options.convertEol && typeof A == "string" && (A = A.replace(/\n/g, `\r
`)), this.writeInternal(A, B);
  }
  /**
   * Internal write implementation (extracted from write())
   */
  writeInternal(A, B) {
    var g;
    this.wasmTerm.write(A), this.processTerminalResponses(), typeof A == "string" && A.includes("\x07") ? this.bellEmitter.fire() : A instanceof Uint8Array && A.includes(7) && this.bellEmitter.fire(), (g = this.linkDetector) == null || g.invalidateCache(), this.viewportY !== 0 && this.scrollToBottom(), typeof A == "string" && A.includes("\x1B]") && this.checkForTitleChange(A), B && requestAnimationFrame(B);
  }
  /**
   * Write data with newline
   */
  writeln(A, B) {
    if (typeof A == "string")
      this.write(A + `\r
`, B);
    else {
      const g = new Uint8Array(A.length + 2);
      g.set(A), g[A.length] = 13, g[A.length + 1] = 10, this.write(g, B);
    }
  }
  /**
   * Paste text into terminal (triggers bracketed paste if supported)
   */
  paste(A) {
    this.assertOpen(), !this.options.disableStdin && (this.wasmTerm.hasBracketedPaste() ? this.dataEmitter.fire("\x1B[200~" + A + "\x1B[201~") : this.dataEmitter.fire(A));
  }
  /**
   * Input data into terminal (as if typed by user)
   *
   * @param data - Data to input
   * @param wasUserInput - If true, triggers onData event (default: false for compat with some apps)
   */
  input(A, B = !1) {
    this.assertOpen(), !this.options.disableStdin && (B ? this.dataEmitter.fire(A) : this.write(A));
  }
  /**
   * Resize terminal
   */
  resize(A, B) {
    if (this.assertOpen(), A === this.cols && B === this.rows)
      return;
    this.cols = A, this.rows = B, this.wasmTerm.resize(A, B), this.renderer.resize(A, B);
    const g = this.renderer.getMetrics();
    this.canvas.width = g.width * A, this.canvas.height = g.height * B, this.canvas.style.width = `${g.width * A}px`, this.canvas.style.height = `${g.height * B}px`, this.resizeEmitter.fire({ cols: A, rows: B }), this.renderer.render(this.wasmTerm, !0, this.viewportY, this);
  }
  /**
   * Clear terminal screen
   */
  clear() {
    this.assertOpen(), this.wasmTerm.write("\x1B[2J\x1B[H");
  }
  /**
   * Reset terminal state
   */
  reset() {
    this.assertOpen();
    const A = this.buildWasmConfig(), B = this.wasmTerm, g = this.ghostty.createTerminal(this.cols, this.rows, A);
    this.wasmTerm = g, this.selectionManager && (this.selectionManager.wasmTerm = g);
    this.renderer && (this.renderer.currentBuffer = g, this.renderer.currentSelectionCoords = null, this.renderer.hoveredHyperlinkId = 0, this.renderer.previousHoveredHyperlinkId = 0, this.renderer.hoveredLinkRange = null, this.renderer.previousHoveredLinkRange = null);
    this.linkDetector && this.linkDetector.invalidateCache(), this.currentHoveredLink = void 0, this.element && (this.element.style.cursor = "text");
    this.scrollAnimationFrame && cancelAnimationFrame(this.scrollAnimationFrame), this.scrollAnimationFrame = void 0, this.scrollAnimationStartTime = void 0, this.scrollAnimationStartY = void 0, this.targetViewportY = 0, this.viewportY = 0;
    B && B.free(), this.renderer.clear(), this.currentTitle = "";
  }
  /**
   * Focus terminal input
   */
  focus() {
    this.isOpen && this.element && (this.element.focus(), setTimeout(() => {
      var A;
      (A = this.element) == null || A.focus();
    }, 0));
  }
  /**
   * Blur terminal (remove focus)
   */
  blur() {
    this.isOpen && this.element && this.element.blur();
  }
  /**
   * Load an addon
   */
  loadAddon(A) {
    A.activate(this), this.addons.push(A);
  }
  // ==========================================================================
  // Selection API (xterm.js compatible)
  // ==========================================================================
  /**
   * Get the selected text as a string
   */
  getSelection() {
    var A;
    return ((A = this.selectionManager) == null ? void 0 : A.getSelection()) || "";
  }
  /**
   * Check if there's an active selection
   */
  hasSelection() {
    var A;
    return ((A = this.selectionManager) == null ? void 0 : A.hasSelection()) || !1;
  }
  /**
   * Clear the current selection
   */
  clearSelection() {
    var A;
    (A = this.selectionManager) == null || A.clearSelection();
  }
  /**
   * Select all text in the terminal
   */
  selectAll() {
    var A;
    (A = this.selectionManager) == null || A.selectAll();
  }
  /**
   * Select text at specific column and row with length
   */
  select(A, B, g) {
    var E;
    (E = this.selectionManager) == null || E.select(A, B, g);
  }
  /**
   * Select entire lines from start to end
   */
  selectLines(A, B) {
    var g;
    (g = this.selectionManager) == null || g.selectLines(A, B);
  }
  /**
   * Get selection position as buffer range
   */
  /**
   * Get the current viewport Y position.
   *
   * This is the number of lines scrolled back from the bottom of the
   * scrollback buffer. It may be fractional during smooth scrolling.
   */
  getViewportY() {
    return this.viewportY;
  }
  getSelectionPosition() {
    var A;
    return (A = this.selectionManager) == null ? void 0 : A.getSelectionPosition();
  }
  // ==========================================================================
  // Phase 1: Custom Event Handlers
  // ==========================================================================
  /**
   * Attach a custom keyboard event handler
   * Returns true to prevent default handling
   */
  attachCustomKeyEventHandler(A) {
    this.customKeyEventHandler = A, this.inputHandler && this.inputHandler.setCustomKeyEventHandler(A);
  }
  /**
   * Attach a custom wheel event handler (Phase 2)
   * Returns true to prevent default handling
   */
  attachCustomWheelEventHandler(A) {
    this.customWheelEventHandler = A;
  }
  // ==========================================================================
  // Link Detection Methods
  // ==========================================================================
  /**
   * Register a custom link provider
   * Multiple providers can be registered to detect different types of links
   *
   * @example
   * ```typescript
   * term.registerLinkProvider({
   *   provideLinks(y, callback) {
   *     // Detect URLs, file paths, etc.
   *     callback(detectedLinks);
   *   }
   * });
   * ```
   */
  registerLinkProvider(A) {
    if (!this.linkDetector)
      throw new Error("Terminal must be opened before registering link providers");
    this.linkDetector.registerProvider(A);
  }
  // ==========================================================================
  // Phase 2: Scrolling Methods
  // ==========================================================================
  /**
   * Scroll viewport by a number of lines
   * @param amount Number of lines to scroll (positive = down, negative = up)
   */
  scrollLines(A) {
    if (!this.wasmTerm)
      throw new Error("Terminal not open");
    const B = this.getScrollbackLength(), E = Math.max(0, Math.min(B, this.viewportY - A));
    E !== this.viewportY && (this.viewportY = E, this.scrollEmitter.fire(this.viewportY), B > 0 && this.showScrollbar());
  }
  /**
   * Scroll viewport by a number of pages
   * @param amount Number of pages to scroll (positive = down, negative = up)
   */
  scrollPages(A) {
    this.scrollLines(A * this.rows);
  }
  /**
   * Scroll viewport to the top of the scrollback buffer
   */
  scrollToTop() {
    const A = this.getScrollbackLength();
    A > 0 && this.viewportY !== A && (this.viewportY = A, this.scrollEmitter.fire(this.viewportY), this.showScrollbar());
  }
  /**
   * Scroll viewport to the bottom (current output)
   */
  scrollToBottom() {
    this.viewportY !== 0 && (this.viewportY = 0, this.scrollEmitter.fire(this.viewportY), this.getScrollbackLength() > 0 && this.showScrollbar());
  }
  /**
   * Scroll viewport to a specific line in the buffer
   * @param line Line number (0 = top of scrollback, scrollbackLength = bottom)
   */
  scrollToLine(A) {
    const B = this.getScrollbackLength(), g = Math.max(0, Math.min(B, A));
    g !== this.viewportY && (this.viewportY = g, this.scrollEmitter.fire(this.viewportY), B > 0 && this.showScrollbar());
  }
  /**
   * Smoothly scroll to a target viewport position
   * @param targetY Target viewport Y position (in lines, can be fractional)
   */
  smoothScrollTo(A) {
    if (!this.wasmTerm)
      return;
    const B = this.getScrollbackLength(), E = Math.max(0, Math.min(B, A));
    if ((this.options.smoothScrollDuration ?? 100) === 0) {
      this.viewportY = E, this.targetViewportY = E, this.scrollEmitter.fire(Math.floor(this.viewportY)), B > 0 && this.showScrollbar();
      return;
    }
    this.targetViewportY = E, !this.scrollAnimationFrame && (this.scrollAnimationStartTime = Date.now(), this.scrollAnimationStartY = this.viewportY, this.animateScroll());
  }
  // ==========================================================================
  // Lifecycle
  // ==========================================================================
  /**
   * Dispose terminal and clean up resources
   */
  dispose() {
    if (!this.isDisposed) {
      this.isDisposed = !0, this.isOpen = !1, this.animationFrameId && (cancelAnimationFrame(this.animationFrameId), this.animationFrameId = void 0), this.scrollAnimationFrame && (cancelAnimationFrame(this.scrollAnimationFrame), this.scrollAnimationFrame = void 0), this.mouseMoveThrottleTimeout && (clearTimeout(this.mouseMoveThrottleTimeout), this.mouseMoveThrottleTimeout = void 0), this.pendingMouseMove = void 0;
      for (const A of this.addons)
        A.dispose();
      this.addons = [], this.cleanupComponents(), this.dataEmitter.dispose(), this.resizeEmitter.dispose(), this.bellEmitter.dispose(), this.selectionChangeEmitter.dispose(), this.keyEmitter.dispose(), this.titleChangeEmitter.dispose(), this.scrollEmitter.dispose(), this.renderEmitter.dispose(), this.cursorMoveEmitter.dispose();
    }
  }
  // ==========================================================================
  // Private Methods
  // ==========================================================================
  /**
   * Start the render loop
   */
  startRenderLoop() {
    const A = () => {
      if (!this.isDisposed && this.isOpen) {
        this.renderer.render(this.wasmTerm, !1, this.viewportY, this, this.scrollbarOpacity);
        const B = this.wasmTerm.getCursor();
        B.y !== this.lastCursorY && (this.lastCursorY = B.y, this.cursorMoveEmitter.fire()), this.animationFrameId = requestAnimationFrame(A);
      }
    };
    A();
  }
  /**
   * Get a line from native WASM scrollback buffer
   * Implements IScrollbackProvider
   */
  getScrollbackLine(A) {
    return this.wasmTerm ? this.wasmTerm.getScrollbackLine(A) : null;
  }
  /**
   * Get scrollback length from native WASM
   * Implements IScrollbackProvider
   */
  getScrollbackLength() {
    return this.wasmTerm ? this.wasmTerm.getScrollbackLength() : 0;
  }
  /**
   * Clean up components (called on dispose or error)
   */
  cleanupComponents() {
    this.selectionManager && (this.selectionManager.dispose(), this.selectionManager = void 0), this.inputHandler && (this.inputHandler.dispose(), this.inputHandler = void 0), this.renderer && (this.renderer.dispose(), this.renderer = void 0), this.canvas && this.canvas.parentNode && (this.canvas.parentNode.removeChild(this.canvas), this.canvas = void 0), this.textarea && this.textarea.parentNode && (this.textarea.parentNode.removeChild(this.textarea), this.textarea = void 0), this.element && (this.element.removeEventListener("wheel", this.handleWheel), this.element.removeEventListener("mousedown", this.handleMouseDown, { capture: !0 }), this.element.removeEventListener("mousemove", this.handleMouseMove), this.element.removeEventListener("mouseleave", this.handleMouseLeave), this.element.removeEventListener("click", this.handleClick), this.element.removeAttribute("contenteditable"), this.element.removeAttribute("role"), this.element.removeAttribute("aria-label"), this.element.removeAttribute("aria-multiline")), this.isOpen && typeof document < "u" && document.removeEventListener("mouseup", this.handleMouseUp), this.scrollbarHideTimeout && (window.clearTimeout(this.scrollbarHideTimeout), this.scrollbarHideTimeout = void 0), this.linkDetector && (this.linkDetector.dispose(), this.linkDetector = void 0), this.wasmTerm && (this.wasmTerm.free(), this.wasmTerm = void 0), this.ghostty = void 0, this.element = void 0, this.textarea = void 0;
  }
  /**
   * Assert terminal is open (throw if not)
   */
  assertOpen() {
    if (this.isDisposed)
      throw new Error("Terminal has been disposed");
    if (!this.isOpen)
      throw new Error("Terminal must be opened before use. Call terminal.open(parent) first.");
  }
  /**
   * Process mouse move for link detection (internal, called by throttled handler)
   */
  processMouseMove(A) {
    if (!this.canvas || !this.renderer || !this.linkDetector || !this.wasmTerm)
      return;
    const B = this.canvas.getBoundingClientRect(), g = Math.floor((A.clientX - B.left) / this.renderer.charWidth), C = Math.floor((A.clientY - B.top) / this.renderer.charHeight);
    let I = 0, D = null;
    const i = this.getViewportY(), w = Math.max(0, Math.floor(i));
    if (w > 0) {
      const h = this.wasmTerm.getScrollbackLength();
      if (C < w) {
        const G = h - w + C;
        D = this.wasmTerm.getScrollbackLine(G);
      } else {
        const G = C - w;
        D = this.wasmTerm.getLine(G);
      }
    } else
      D = this.wasmTerm.getLine(C);
    D && g >= 0 && g < D.length && (I = D[g].hyperlink_id);
    const s = this.renderer.hoveredHyperlinkId || 0;
    I !== s && this.renderer.setHoveredHyperlinkId(I);
    const N = this.wasmTerm.getScrollbackLength();
    let k;
    const M = this.getViewportY(), a = Math.max(0, Math.floor(M));
    if (a > 0)
      if (C < a)
        k = N - a + C;
      else {
        const h = C - a;
        k = N + h;
      }
    else
      k = N + C;
    this.linkDetector.getLinkAt(g, k).then((h) => {
      var G, U, t, c;
      if (h !== this.currentHoveredLink && ((U = (G = this.currentHoveredLink) == null ? void 0 : G.hover) == null || U.call(G, !1), this.currentHoveredLink = h, (t = h == null ? void 0 : h.hover) == null || t.call(h, !0), this.element && (this.element.style.cursor = h ? "pointer" : "text"), this.renderer))
        if (h) {
          const F = ((c = this.wasmTerm) == null ? void 0 : c.getScrollbackLength()) || 0, S = this.getViewportY(), x = Math.max(0, Math.floor(S)), p = h.range.start.y - F + x, T = h.range.end.y - F + x;
          p < this.rows && T >= 0 ? this.renderer.setHoveredLinkRange({
            startX: h.range.start.x,
            startY: Math.max(0, p),
            endX: h.range.end.x,
            endY: Math.min(this.rows - 1, T)
          }) : this.renderer.setHoveredLinkRange(null);
        } else
          this.renderer.setHoveredLinkRange(null);
    }).catch((h) => {
      console.warn("Link detection error:", h);
    });
  }
  /**
   * Process scrollbar drag movement
   */
  processScrollbarDrag(A) {
    if (!this.canvas || !this.renderer || !this.wasmTerm || this.scrollbarDragStart === null)
      return;
    const B = this.wasmTerm.getScrollbackLength();
    if (B === 0)
      return;
    const g = this.canvas.getBoundingClientRect(), C = A.clientY - g.top - this.scrollbarDragStart, i = g.height - 4 * 2, w = this.rows, s = B + w, N = Math.max(20, w / s * i), k = -C / (i - N), M = Math.round(k * B), a = this.scrollbarDragStartViewportY + M;
    this.scrollToLine(Math.max(0, Math.min(B, a)));
  }
  /**
   * Show scrollbar with fade-in and schedule auto-hide
   */
  showScrollbar() {
    this.scrollbarHideTimeout && (window.clearTimeout(this.scrollbarHideTimeout), this.scrollbarHideTimeout = void 0), this.scrollbarVisible ? this.scrollbarOpacity = 1 : (this.scrollbarVisible = !0, this.scrollbarOpacity = 0, this.fadeInScrollbar()), this.isDraggingScrollbar || (this.scrollbarHideTimeout = window.setTimeout(() => {
      this.hideScrollbar();
    }, this.SCROLLBAR_HIDE_DELAY_MS));
  }
  /**
   * Hide scrollbar with fade-out
   */
  hideScrollbar() {
    this.scrollbarHideTimeout && (window.clearTimeout(this.scrollbarHideTimeout), this.scrollbarHideTimeout = void 0), this.scrollbarVisible && this.fadeOutScrollbar();
  }
  /**
   * Fade in scrollbar
   */
  fadeInScrollbar() {
    const A = Date.now(), B = () => {
      const g = Date.now() - A, E = Math.min(g / this.SCROLLBAR_FADE_DURATION_MS, 1);
      this.scrollbarOpacity = E, this.renderer && this.wasmTerm && this.renderer.render(this.wasmTerm, !1, this.viewportY, this, this.scrollbarOpacity), E < 1 && requestAnimationFrame(B);
    };
    B();
  }
  /**
   * Fade out scrollbar
   */
  fadeOutScrollbar() {
    const A = Date.now(), B = this.scrollbarOpacity, g = () => {
      const E = Date.now() - A, C = Math.min(E / this.SCROLLBAR_FADE_DURATION_MS, 1);
      this.scrollbarOpacity = B * (1 - C), this.renderer && this.wasmTerm && this.renderer.render(this.wasmTerm, !1, this.viewportY, this, this.scrollbarOpacity), C < 1 ? requestAnimationFrame(g) : (this.scrollbarVisible = !1, this.scrollbarOpacity = 0, this.renderer && this.wasmTerm && this.renderer.render(this.wasmTerm, !1, this.viewportY, this, 0));
    };
    g();
  }
  /**
   * Process any pending terminal responses and emit them via onData.
   *
   * This handles escape sequences that require the terminal to send a response
   * back to the PTY, such as:
   * - DSR 6 (cursor position): Shell sends \x1b[6n, terminal responds with \x1b[row;colR
   * - DSR 5 (operating status): Shell sends \x1b[5n, terminal responds with \x1b[0n
   *
   * Without this, shells like nushell that rely on cursor position queries
   * will hang waiting for a response that never comes.
   */
  processTerminalResponses() {
    if (!this.wasmTerm)
      return;
    const A = this.wasmTerm.readResponse();
    A && this.dataEmitter.fire(A);
  }
  /**
   * Check for title changes in written data (OSC sequences)
   * Simplified implementation - looks for OSC 0, 1, 2
   */
  checkForTitleChange(A) {
    const B = /\x1b\]([012]);([^\x07\x1b]*?)(?:\x07|\x1b\\)/g;
    let g = null;
    for (; (g = B.exec(A)) !== null; ) {
      const E = g[1], C = g[2];
      (E === "0" || E === "2") && C !== this.currentTitle && (this.currentTitle = C, this.titleChangeEmitter.fire(C));
    }
  }
  // ============================================================================
  // Terminal Modes
  // ============================================================================
  /**
   * Query terminal mode state
   *
   * @param mode Mode number (e.g., 2004 for bracketed paste)
   * @param isAnsi True for ANSI modes, false for DEC modes (default: false)
   * @returns true if mode is enabled
   */
  getMode(A, B = !1) {
    return this.assertOpen(), this.wasmTerm.getMode(A, B);
  }
  /**
   * Check if bracketed paste mode is enabled
   */
  hasBracketedPaste() {
    return this.assertOpen(), this.wasmTerm.hasBracketedPaste();
  }
  /**
   * Check if focus event reporting is enabled
   */
  hasFocusEvents() {
    return this.assertOpen(), this.wasmTerm.hasFocusEvents();
  }
  /**
   * Check if mouse tracking is enabled
   */
  hasMouseTracking() {
    return this.assertOpen(), this.wasmTerm.hasMouseTracking();
  }
}
const QA = 2, BA = 1, gA = 15, EA = 100;
class DA {
  constructor() {
    this._isResizing = !1;
  }
  /**
   * Activate the addon (called by Terminal.loadAddon)
   */
  activate(A) {
    this._terminal = A;
  }
  /**
   * Dispose the addon and clean up resources
   */
  dispose() {
    this._resizeObserver && (this._resizeObserver.disconnect(), this._resizeObserver = void 0), this._resizeDebounceTimer && (clearTimeout(this._resizeDebounceTimer), this._resizeDebounceTimer = void 0), this._lastCols = void 0, this._lastRows = void 0, this._terminal = void 0;
  }
  /**
   * Fit the terminal to its container
   *
   * Calculates optimal dimensions and resizes the terminal.
   * Does nothing if dimensions cannot be calculated or haven't changed.
   */
  fit() {
    if (this._isResizing)
      return;
    const A = this.proposeDimensions();
    if (!A || !this._terminal)
      return;
    const B = this._terminal, g = B.cols, E = B.rows;
    if (!(A.cols === this._lastCols && A.rows === this._lastRows || A.cols === g && A.rows === E)) {
      this._lastCols = A.cols, this._lastRows = A.rows, this._isResizing = !0;
      try {
        B.resize && typeof B.resize == "function" && B.resize(A.cols, A.rows);
      } finally {
        setTimeout(() => {
          this._isResizing = !1;
        }, 50);
      }
    }
  }
  /**
   * Propose dimensions to fit the terminal to its container
   *
   * Calculates cols and rows based on:
   * - Terminal container element dimensions (clientWidth/Height)
   * - Terminal element padding
   * - Font metrics (character cell size)
   * - Scrollbar width reservation
   *
   * @returns Proposed dimensions or undefined if cannot calculate
   */
  proposeDimensions() {
    var G;
    if (!((G = this._terminal) != null && G.element))
      return;
    const B = this._terminal.renderer;
    if (!B || typeof B.getMetrics != "function")
      return;
    const g = B.getMetrics();
    if (!g || g.width === 0 || g.height === 0)
      return;
    const E = this._terminal.element;
    if (typeof E.clientWidth > "u")
      return;
    const C = window.getComputedStyle(E), I = Number.parseInt(C.getPropertyValue("padding-top")) || 0, D = Number.parseInt(C.getPropertyValue("padding-bottom")) || 0, i = Number.parseInt(C.getPropertyValue("padding-left")) || 0, w = Number.parseInt(C.getPropertyValue("padding-right")) || 0, s = E.clientWidth, N = E.clientHeight;
    if (s === 0 || N === 0)
      return;
    const k = s - i - w - gA, M = N - I - D, a = Math.max(QA, Math.floor(k / g.width)), h = Math.max(BA, Math.floor(M / g.height));
    return { cols: a, rows: h };
  }
  /**
   * Observe the terminal's container for resize events
   *
   * Sets up a ResizeObserver to automatically call fit() when the
   * container size changes. Resize events are debounced to avoid
   * excessive calls during window drag operations.
   *
   * Call dispose() to stop observing.
   */
  observeResize() {
    var A;
    (A = this._terminal) != null && A.element && (this._resizeObserver || (this._resizeObserver = new ResizeObserver((B) => {
      this._isResizing || !B[0] || (this._resizeDebounceTimer && clearTimeout(this._resizeDebounceTimer), this._resizeDebounceTimer = setTimeout(() => {
        this.fit();
      }, EA));
    }), this._resizeObserver.observe(this._terminal.element)));
  }
}
let R = null;
async function oA() {
  R || (R = await q.load());
}
function CA() {
  if (!R)
    throw new Error(
      `ghostty-web not initialized. Call init() before creating Terminal instances.
Example:
  import { init, Terminal } from "ghostty-web";
  await init();
  const term = new Terminal();

For tests, pass a Ghostty instance directly:
  import { Ghostty, Terminal } from "ghostty-web";
  const ghostty = await Ghostty.load();
  const term = new Terminal({ ghostty });`
    );
  return R;
}
export {
  $ as CanvasRenderer,
  e as CellFlags,
  J as EventEmitter,
  DA as FitAddon,
  q as Ghostty,
  W as GhosttyTerminal,
  P as InputHandler,
  V as KeyEncoder,
  H as KeyEncoderOption,
  v as LinkDetector,
  X as OSC8LinkProvider,
  AA as SelectionManager,
  IA as Terminal,
  _ as UrlRegexProvider,
  CA as getGhostty,
  oA as init
};
