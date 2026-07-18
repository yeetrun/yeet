#!/usr/bin/env bash
# Copyright (c) 2025 AUTHORS All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "ISO packet-policy integration tests require Linux" >&2
  exit 1
fi

unshare_bin=$(command -v unshare)
task_tmp=$(mktemp -d)
trap 'rm -rf -- "$task_tmp"' EXIT

test_binary="$task_tmp/netns-integration.test"
endpoint_helper="$task_tmp/iso-endpoint"

mise exec -- env CGO_ENABLED=0 go test -c -tags=integration -o "$test_binary" ./pkg/netns
mise exec -- env CGO_ENABLED=0 go build -o "$endpoint_helper" ./pkg/netns/testdata/iso-endpoint

sudo env \
  "PATH=$PATH" \
  YEET_ISO_INTEGRATION=1 \
  YEET_ISO_INTEGRATION_ROOTNS=1 \
  "YEET_ISO_ENDPOINT_HELPER=$endpoint_helper" \
  "$unshare_bin" --net \
  "$test_binary" -test.run '^TestISOPacketPolicy$' -test.count=1 -test.v
