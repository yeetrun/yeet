#!/bin/sh
set -e

REPO="shayne/yeet"
BASE_URL="https://github.com/${REPO}/releases"
CHANNEL="stable"
INSTALL_DIR=""

usage() {
  cat <<USAGE
yeet install script

Usage:
  curl -fsSL https://yeetrun.com/install.sh | sh
  curl -fsSL https://yeetrun.com/install.sh | sh -s -- --nightly

Options:
  --nightly           Install the nightly build
  --dir <path>        Install directory (default: /usr/local/bin, /opt/homebrew/bin on macOS)
  -h, --help          Show this help

Env:
  YEET_INSTALL_DIR    Install directory override
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --nightly)
      CHANNEL="nightly"
      shift
      ;;
    --dir)
      if [ $# -lt 2 ]; then
        echo "--dir requires a value" >&2
        exit 1
      fi
      INSTALL_DIR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [ -n "${YEET_INSTALL_DIR:-}" ]; then
  INSTALL_DIR="$YEET_INSTALL_DIR"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux)
    os="linux"
    ;;
  darwin)
    os="darwin"
    ;;
  *)
    echo "unsupported OS: $os" >&2
    exit 1
    ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64)
    arch="amd64"
    ;;
  arm64|aarch64)
    arch="arm64"
    ;;
  *)
    echo "unsupported arch: $arch" >&2
    exit 1
    ;;
esac

asset="yeet-${os}-${arch}.tar.gz"
sha="${asset}.sha256"

if [ "$CHANNEL" = "nightly" ]; then
  asset_url="${BASE_URL}/download/nightly/${asset}"
  sha_url="${BASE_URL}/download/nightly/${sha}"
else
  asset_url="${BASE_URL}/latest/download/${asset}"
  sha_url="${BASE_URL}/latest/download/${sha}"
fi

if [ -z "$INSTALL_DIR" ]; then
  if [ "$os" = "darwin" ] && [ -d "/opt/homebrew/bin" ]; then
    INSTALL_DIR="/opt/homebrew/bin"
  else
    INSTALL_DIR="/usr/local/bin"
  fi
fi

fetch() {
  url="$1"
  out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$out" "$url"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

mkdir -p "$INSTALL_DIR" 2>/dev/null || true

SUDO=""
if [ ! -w "$INSTALL_DIR" ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
  else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
  fi
fi

tmp_dir=$(mktemp -d)
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

fetch "$asset_url" "$tmp_dir/$asset"
fetch "$sha_url" "$tmp_dir/$sha"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$tmp_dir" && sha256sum -c "$sha")
elif command -v shasum >/dev/null 2>&1; then
  (cd "$tmp_dir" && shasum -a 256 -c "$sha")
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

tar -xzf "$tmp_dir/$asset" -C "$tmp_dir"

bin_name="yeet-${os}-${arch}"
if [ ! -f "$tmp_dir/$bin_name" ]; then
  echo "missing extracted binary: $bin_name" >&2
  exit 1
fi

install_target="$INSTALL_DIR/yeet"
if command -v install >/dev/null 2>&1; then
  $SUDO install -m 0755 "$tmp_dir/$bin_name" "$install_target"
else
  $SUDO cp "$tmp_dir/$bin_name" "$install_target"
  $SUDO chmod 0755 "$install_target"
fi

if [ "$INSTALL_DIR" = "$HOME/.local/bin" ]; then
  echo "Installed yeet to $install_target"
  echo "Ensure $INSTALL_DIR is in your PATH."
else
  echo "Installed yeet to $install_target"
fi
