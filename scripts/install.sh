#!/bin/sh
# Installs the latest governor and governorctl release binaries into
# $HOME/.local/bin (or $INSTALL_DIR if set). Usage:
#   curl -sSL https://raw.githubusercontent.com/hadi-moustafa/Governor/master/scripts/install.sh | sh
set -eu

REPO="hadi-moustafa/Governor"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

os() {
  case "$(uname -s)" in
    Linux) echo "linux" ;;
    Darwin) echo "darwin" ;;
    *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
}

arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
}

OS="$(os)"
ARCH="$(arch)"

VERSION="${VERSION:-$(curl -sSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')}"
if [ -z "$VERSION" ]; then
  echo "could not determine the latest release version" >&2
  exit 1
fi
VERSION_NUM="${VERSION#v}"

ARCHIVE="Governor_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading ${URL}"
curl -sSL "$URL" -o "$TMPDIR/$ARCHIVE"
tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR"

mkdir -p "$INSTALL_DIR"
install -m 0755 "$TMPDIR/governor" "$INSTALL_DIR/governor"
install -m 0755 "$TMPDIR/governorctl" "$INSTALL_DIR/governorctl"

echo "Installed governor and governorctl ${VERSION} to ${INSTALL_DIR}"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: ${INSTALL_DIR} is not on your PATH — add it, e.g. export PATH=\"\$PATH:${INSTALL_DIR}\"" ;;
esac
