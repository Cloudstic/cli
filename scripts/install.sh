#!/usr/bin/env sh

set -eu

REPO="Cloudstic/cli"
BIN_NAME="cloudstic"
VERSION="latest"
INSTALL_DIR="/usr/local/bin"
VERIFY_CHECKSUMS=1

usage() {
  cat <<EOF
Install Cloudstic from GitHub Releases.

Usage:
  install.sh [options]

Options:
  -v, --version <version>       Install a specific version (e.g. v1.2.3).
                                Defaults to latest.
  -d, --install-dir <path>      Destination directory for binary.
                                Defaults to /usr/local/bin.
      --no-verify               Skip SHA256 checksum verification.
  -h, --help                    Show this help.

Examples:
  curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh
  curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --version v1.2.3
  curl -fsSL https://raw.githubusercontent.com/Cloudstic/cli/main/scripts/install.sh | sh -s -- --install-dir "$HOME/.local/bin"
EOF
}

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Error: required command not found: $1" >&2
    exit 1
  fi
}

detect_os() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$os" in
    darwin) echo "darwin" ;;
    linux) echo "linux" ;;
    *)
      echo "Error: unsupported OS: $os (supported: darwin, linux)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      echo "Error: unsupported architecture: $arch (supported: amd64, arm64)" >&2
      exit 1
      ;;
  esac
}

sha256_file() {
  file="$1"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
    return
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return
  fi
  if command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
    return
  fi
  echo "Error: no checksum tool found (shasum/sha256sum/openssl)." >&2
  exit 1
}

parse_args() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -v|--version)
        [ "$#" -ge 2 ] || { echo "Error: missing value for $1" >&2; exit 1; }
        VERSION="$2"
        shift 2
        ;;
      -d|--install-dir)
        [ "$#" -ge 2 ] || { echo "Error: missing value for $1" >&2; exit 1; }
        INSTALL_DIR="$2"
        shift 2
        ;;
      --no-verify)
        VERIFY_CHECKSUMS=0
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        echo "Error: unknown option: $1" >&2
        usage
        exit 1
        ;;
    esac
  done
}

install_binary() {
  os="$1"
  arch="$2"

  if [ "$VERSION" = "latest" ]; then
    tag="latest"
  else
    tag="$VERSION"
  fi

  if [ "$tag" = "latest" ]; then
    base_url="https://github.com/$REPO/releases/latest/download"
    version_for_name="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | awk -F '"' '/tag_name/{gsub(/^v/,"",$4); print $4; exit}')"
    if [ -z "$version_for_name" ]; then
      echo "Error: failed to resolve latest release version." >&2
      exit 1
    fi
  else
    base_url="https://github.com/$REPO/releases/download/$tag"
    version_for_name="${tag#v}"
  fi

  archive_name="${BIN_NAME}_${version_for_name}_${os}_${arch}.tar.gz"
  archive_url="$base_url/$archive_name"
  checksums_url="$base_url/checksums.txt"

  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT INT TERM

  echo "Downloading $archive_name..."
  curl -fsSL "$archive_url" -o "$tmpdir/$archive_name"

  if [ "$VERIFY_CHECKSUMS" -eq 1 ]; then
    echo "Downloading checksums.txt..."
    curl -fsSL "$checksums_url" -o "$tmpdir/checksums.txt"

    expected="$(awk -v f="$archive_name" '$2 == f {print $1}' "$tmpdir/checksums.txt")"
    if [ -z "$expected" ]; then
      echo "Error: checksum entry not found for $archive_name" >&2
      exit 1
    fi
    actual="$(sha256_file "$tmpdir/$archive_name")"
    if [ "$actual" != "$expected" ]; then
      echo "Error: checksum mismatch for $archive_name" >&2
      echo "Expected: $expected" >&2
      echo "Actual:   $actual" >&2
      exit 1
    fi
    echo "Checksum verified."
  fi

  tar -xzf "$tmpdir/$archive_name" -C "$tmpdir"
  if [ ! -f "$tmpdir/$BIN_NAME" ]; then
    echo "Error: extracted archive does not contain $BIN_NAME" >&2
    exit 1
  fi

  mkdir -p "$INSTALL_DIR"
  target="$INSTALL_DIR/$BIN_NAME"
  if cp "$tmpdir/$BIN_NAME" "$target" 2>/dev/null; then
    chmod +x "$target"
  else
    echo "Permission denied writing to $INSTALL_DIR." >&2
    echo "Try running with sudo or choose a user-writable directory:" >&2
    echo "  sh -s -- --install-dir \"$HOME/.local/bin\"" >&2
    exit 1
  fi

  echo "Installed $BIN_NAME to $target"
  echo "Run: $BIN_NAME version"
}

main() {
  need_cmd curl
  need_cmd tar
  parse_args "$@"
  os="$(detect_os)"
  arch="$(detect_arch)"
  install_binary "$os" "$arch"
}

main "$@"
