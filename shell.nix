# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

{ pkgs ? (
    let
      inherit (builtins) fetchTree fromJSON readFile;
      inherit ((fromJSON (readFile ./flake.lock)).nodes) nixpkgs gomod2nix;
    in
    import (fetchTree nixpkgs.locked) {
      overlays = [
        (import "${fetchTree gomod2nix.locked}/overlay.nix")
      ];
    }
  )
, mkGoEnv ? pkgs.mkGoEnv
, gomod2nix ? pkgs.gomod2nix
}:

let
  goEnv = mkGoEnv { pwd = ./.; };
  zigCC = pkgs.writeShellScriptBin "zig-cc" ''
    if [ "$(uname -s)" = "Darwin" ] && [ "$GOOS" = "linux" ] && [ "$GOARCH" = "amd64" ]; then
      exec ${pkgs.zig}/bin/zig cc -target x86_64-linux-gnu "$@"
    fi
    if command -v clang >/dev/null 2>&1; then
      exec clang "$@"
    fi
    exec ${pkgs.zig}/bin/zig cc "$@"
  '';
  zigCXX = pkgs.writeShellScriptBin "zig-c++" ''
    if [ "$(uname -s)" = "Darwin" ] && [ "$GOOS" = "linux" ] && [ "$GOARCH" = "amd64" ]; then
      exec ${pkgs.zig}/bin/zig c++ -target x86_64-linux-gnu "$@"
    fi
    if command -v clang++ >/dev/null 2>&1; then
      exec clang++ "$@"
    fi
    exec ${pkgs.zig}/bin/zig c++ "$@"
  '';
in
pkgs.mkShell {
  packages = [
    goEnv
    gomod2nix
    pkgs.zig
    zigCC
    zigCXX
  ];
  shellHook = ''
    # Automatic cgo toolchain selection:
    # - Native builds use clang/clang++ when available.
    # - macOS -> linux/amd64 cross builds use zig with a linux target.
    export CC="${zigCC}/bin/zig-cc"
    export CXX="${zigCXX}/bin/zig-c++"
    export CGO_ENABLED=1
  '';
}
