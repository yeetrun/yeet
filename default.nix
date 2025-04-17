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
, buildGoApplication ? pkgs.buildGoApplication
, pname ? "yeet"
, version ? "0.1"
, subPackages ? [ "./cmd/yeet" ]
}:

buildGoApplication {
  inherit pname version subPackages;
  pwd = ./.;
  src = ./.;
  modules = ./gomod2nix.toml;
}
