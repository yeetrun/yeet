#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

check_mode=""
if [[ "${1:-}" == "--check" ]]; then
  check_mode="-check"
  shift
fi

# Apply/check BSD headers across tracked source files.
go run github.com/google/addlicense $check_mode \
  -l bsd \
  -c "AUTHORS" \
  -y 2025 \
  cmd pkg tools example default.nix flake.nix shell.nix
