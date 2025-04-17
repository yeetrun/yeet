# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

{
  description = "A basic gomod2nix flake";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  inputs.flake-utils.url = "github:numtide/flake-utils";
  inputs.gomod2nix.url = "github:nix-community/gomod2nix";
  inputs.gomod2nix.inputs.nixpkgs.follows = "nixpkgs";
  inputs.gomod2nix.inputs.flake-utils.follows = "flake-utils";

  outputs = { self, nixpkgs, flake-utils, gomod2nix }:
    (flake-utils.lib.eachDefaultSystem
      (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};

          callPackage = pkgs.callPackage;
        in
        {
          packages = {
            yeet = callPackage ./. {
              inherit (gomod2nix.legacyPackages.${system}) buildGoApplication;
              pname = "yeet";
              subPackages = [ "./cmd/yeet" ];
            };
            catch = callPackage ./. {
              inherit (gomod2nix.legacyPackages.${system}) buildGoApplication;
              pname = "catch";
              subPackages = [ "./cmd/catch" ];
            };
            default = self.packages.${system}.yeet;
          };
          devShells.default = callPackage ./shell.nix {
            inherit (gomod2nix.legacyPackages.${system}) mkGoEnv gomod2nix;
          };
        })
    );
}
