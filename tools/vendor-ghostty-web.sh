#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

ghostty_version=0.4.0
ghostty_integrity='sha512-0puDBik2qapbD/QQBW9o5ZHfXnZBqZWx/ctBiVtKZ6ZLds4NYb+wZuw1cRLXZk9zYovIQ908z3rvFhexAvc5Hg=='
ghostty_url="https://registry.npmjs.org/ghostty-web/-/ghostty-web-${ghostty_version}.tgz"
ghostty_tmp=$(mktemp -d -t yeet-ghostty-web.XXXXXX)
trap 'rm -rf "$ghostty_tmp"' EXIT

curl --fail --location --silent --show-error "$ghostty_url" -o "$ghostty_tmp/package.tgz"
ghostty_actual="sha512-$(openssl dgst -sha512 -binary "$ghostty_tmp/package.tgz" | openssl base64 -A)"
test "$ghostty_actual" = "$ghostty_integrity"
tar -xzf "$ghostty_tmp/package.tgz" -C "$ghostty_tmp"

cp "$ghostty_tmp/package/dist/ghostty-vt.wasm" pkg/yeet/web_run_assets/ghostty-vt.wasm
cp "$ghostty_tmp/package/LICENSE" pkg/yeet/web_run_assets/ghostty-web.LICENSE

node - "$ghostty_tmp/package/dist/ghostty-web.js" pkg/yeet/web_run_assets/ghostty-web.js <<'NODE'
const fs = require("node:fs");
const [sourcePath, targetPath] = process.argv.slice(2);
const source = fs.readFileSync(sourcePath, "utf8");
const pattern = /new URL\("data:application\/wasm;base64,[A-Za-z0-9+/=]+", self\.location\)/g;
const matches = source.match(pattern) || [];
if (matches.length !== 1) throw new Error(`expected one inline WASM URL, found ${matches.length}`);
const externalWasm = source.replace(pattern, 'new URL("./ghostty-vt.wasm", self.location)');
const selectAllPattern = /const A = this\.wasmTerm\.getDimensions\(\), B = this\.getViewportY\(\);\n    this\.selectionStart = \{ col: 0, absoluteRow: B \}, this\.selectionEnd = \{ col: A\.cols - 1, absoluteRow: B \+ A\.rows - 1 \}/g;
const selectAllMatches = externalWasm.match(selectAllPattern) || [];
if (selectAllMatches.length !== 1) {
  throw new Error(`expected one viewport-only selectAll implementation, found ${selectAllMatches.length}`);
}
const output = externalWasm.replace(
  selectAllPattern,
  "const A = this.wasmTerm.getDimensions(), B = this.wasmTerm.getScrollbackLength();\n    this.selectionStart = { col: 0, absoluteRow: 0 }, this.selectionEnd = { col: A.cols - 1, absoluteRow: B + A.rows - 1 }",
);
const constructorPattern = /    var I;\n    if \(this\.viewportBufferPtr = 0,/g;
const constructorMatches = output.match(constructorPattern) || [];
if (constructorMatches.length !== 1) {
  throw new Error(`expected one GhosttyTerminal constructor, found ${constructorMatches.length}`);
}
const limitedConstructor = output.replace(
  constructorPattern,
  "    var I;\n    this.scrollbackLimit = C ? C.scrollbackLimit ?? 1e4 : 1e4;\n    if (this.viewportBufferPtr = 0,",
);
const lengthPattern = /  getScrollbackLength\(\) \{\n    return this\.exports\.ghostty_terminal_get_scrollback_length\(this\.handle\);\n  \}/g;
const lengthMatches = limitedConstructor.match(lengthPattern) || [];
if (lengthMatches.length !== 1) {
  throw new Error(`expected one native scrollback length wrapper, found ${lengthMatches.length}`);
}
const limitedLength = limitedConstructor.replace(
  lengthPattern,
  `  getNativeScrollbackLength() {
    return this.exports.ghostty_terminal_get_scrollback_length(this.handle);
  }
  getScrollbackStart() {
    return Math.max(0, this.getNativeScrollbackLength() - this.scrollbackLimit);
  }
  getScrollbackLength() {
    return this.getNativeScrollbackLength() - this.getScrollbackStart();
  }`,
);
const linePattern = /this\.exports\.ghostty_terminal_get_scrollback_line\(\n      this\.handle,\n      A,/g;
const lineMatches = limitedLength.match(linePattern) || [];
if (lineMatches.length !== 1) {
  throw new Error(`expected one scrollback line accessor, found ${lineMatches.length}`);
}
const limitedLines = limitedLength.replace(
  linePattern,
  "this.exports.ghostty_terminal_get_scrollback_line(\n      this.handle,\n      A + this.getScrollbackStart(),",
);
const graphemePattern = /this\.exports\.ghostty_terminal_get_scrollback_grapheme\(\n      this\.handle,\n      A,/g;
const graphemeMatches = limitedLines.match(graphemePattern) || [];
if (graphemeMatches.length !== 1) {
  throw new Error(`expected one scrollback grapheme accessor, found ${graphemeMatches.length}`);
}
const limitedGraphemes = limitedLines.replace(
  graphemePattern,
  "this.exports.ghostty_terminal_get_scrollback_grapheme(\n      this.handle,\n      A + this.getScrollbackStart(),",
);
const resetPattern = /  reset\(\) \{\n    this\.assertOpen\(\), this\.wasmTerm && this\.wasmTerm\.free\(\);\n    const A = this\.buildWasmConfig\(\);\n    this\.wasmTerm = this\.ghostty\.createTerminal\(this\.cols, this\.rows, A\), this\.renderer\.clear\(\), this\.currentTitle = "";\n  \}/g;
const resetMatches = limitedGraphemes.match(resetPattern) || [];
if (resetMatches.length !== 1) {
  throw new Error(`expected one Ghostty Terminal reset implementation, found ${resetMatches.length}`);
}
const failureAtomicReset = limitedGraphemes.replace(
  resetPattern,
  `  reset() {
    this.assertOpen();
    const A = this.buildWasmConfig(), B = this.wasmTerm, g = this.ghostty.createTerminal(this.cols, this.rows, A);
    this.wasmTerm = g, this.selectionManager && (this.selectionManager.wasmTerm = g);
    this.renderer && (this.renderer.currentBuffer = g, this.renderer.currentSelectionCoords = null, this.renderer.hoveredHyperlinkId = 0, this.renderer.previousHoveredHyperlinkId = 0, this.renderer.hoveredLinkRange = null, this.renderer.previousHoveredLinkRange = null);
    this.linkDetector && this.linkDetector.invalidateCache(), this.currentHoveredLink = void 0, this.element && (this.element.style.cursor = "text");
    this.scrollAnimationFrame && cancelAnimationFrame(this.scrollAnimationFrame), this.scrollAnimationFrame = void 0, this.scrollAnimationStartTime = void 0, this.scrollAnimationStartY = void 0, this.targetViewportY = 0, this.viewportY = 0;
    B && B.free(), this.renderer.clear(), this.currentTitle = "";
  }`,
);
fs.writeFileSync(targetPath, failureAtomicReset);
NODE

node - pkg/yeet/web_run_assets/ghostty-web.manifest.json <<'NODE'
const fs = require("node:fs");
const targetPath = process.argv[2];
const manifest = {
  package: "ghostty-web",
  version: "0.4.0",
  tarball: "https://registry.npmjs.org/ghostty-web/-/ghostty-web-0.4.0.tgz",
  integrity: "sha512-0puDBik2qapbD/QQBW9o5ZHfXnZBqZWx/ctBiVtKZ6ZLds4NYb+wZuw1cRLXZk9zYovIQ908z3rvFhexAvc5Hg==",
  license: "MIT",
  inlineWasmReplacedWith: "./ghostty-vt.wasm",
  selectAllPatch: "absolute rows 0 through scrollback plus screen",
  scrollbackPatch: "expose newest configured line window",
  resetPatch: "failure-atomic owner swap with selection scroll and link reset",
};
fs.writeFileSync(targetPath, `${JSON.stringify(manifest, null, 2)}\n`);
NODE
