// Copyright (c) 2025 AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package yeet

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func resolveCatchReleaseAsset(systemName, goarch string, nightly bool) (assetName, assetURL, shaURL, tag string, err error) {
	goos := strings.ToLower(strings.TrimSpace(systemName))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	if goos != "linux" {
		return "", "", "", "", fmt.Errorf("remote system is not Linux: %s", systemName)
	}
	if goarch != "amd64" && goarch != "arm64" {
		return "", "", "", "", fmt.Errorf("remote system has unsupported arch: %s", goarch)
	}

	assetName = fmt.Sprintf("catch-%s-%s.tar.gz", goos, goarch)
	shaName := assetName + ".sha256"

	rel, err := fetchGitHubRelease(nightly)
	if err != nil {
		return "", "", "", "", err
	}
	assetURL, err = findGitHubAssetURL(rel.Assets, assetName)
	if err != nil {
		return "", "", "", "", err
	}
	shaURL, err = findGitHubAssetURL(rel.Assets, shaName)
	if err != nil {
		return "", "", "", "", err
	}

	return assetName, assetURL, shaURL, rel.TagName, nil
}

func downloadCatchRelease(ui *initUI, userAtRemote, assetName, assetURL, shaURL string) (string, error) {
	shaName := assetName + ".sha256"
	binaryName := strings.TrimSuffix(assetName, ".tar.gz")
	script := fmt.Sprintf(`set -euo pipefail
TMP_DIR=$(mktemp -d)
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT
fetch() {
  url=$1
  out=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$out" "$url"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}
verify() {
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$TMP_DIR" && sha256sum -c "$1")
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$TMP_DIR" && shasum -a 256 -c "$1")
  else
    echo "sha256sum or shasum is required" >&2
    exit 1
  fi
}
fetch %q "$TMP_DIR/%s"
fetch %q "$TMP_DIR/%s"
expected=$(awk '{print $1}' "$TMP_DIR/%s")
if [ -z "$expected" ]; then
  echo "empty checksum file" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  computed=$(sha256sum "$TMP_DIR/%s" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  computed=$(shasum -a 256 "$TMP_DIR/%s" | awk '{print $1}')
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi
if [ "$expected" != "$computed" ]; then
  echo "checksum mismatch" >&2
  exit 1
fi
tar -xzf "$TMP_DIR/%s" -C "$TMP_DIR"
mv -f "$TMP_DIR/%s" ./catch
`, assetURL, assetName, shaURL, shaName, shaName, assetName, assetName, assetName, binaryName)

	cmd := exec.Command("ssh", userAtRemote, "bash", "-s")
	cmd.Stdin = bytes.NewBufferString(script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	ui.Suspend()
	if err := cmd.Run(); err != nil {
		ui.Resume()
		return "", err
	}
	ui.Resume()
	return assetName, nil
}
