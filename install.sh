#!/bin/sh
# Preserve the original install URL for existing bookmarks and scripts.
set -eu
installer=$(mktemp)
trap 'rm -f "$installer"' EXIT
url=https://github.com/yeetrun/yeet-os/releases/latest/download/install.sh
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$installer"
else
  wget -qO "$installer" "$url"
fi
sh "$installer" "$@"
