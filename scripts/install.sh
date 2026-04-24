#!/usr/bin/env sh

set -eu

PROJECT_NAME='claude-switch'
COMMAND_NAME='cs'
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

case "$(uname -s)" in
  Darwin|Linux)
    ;;
  *)
    echo "Unsupported OS: $(uname -s)" >&2
    exit 1
    ;;
esac

INSTALL_DIR=${INSTALL_DIR:-"$HOME/.local/bin"}
BIN_PATH="$INSTALL_DIR/$COMMAND_NAME"

if ! command -v go >/dev/null 2>&1; then
  echo 'Go is required but was not found in PATH.' >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"

VERSION=${VERSION:-dev}
if [ "$VERSION" = "dev" ] && command -v git >/dev/null 2>&1; then
  VERSION=$(git -C "$REPO_ROOT" describe --tags --exact-match 2>/dev/null || echo dev)
fi

echo "Building $PROJECT_NAME..."
GOCACHE="${GOCACHE:-$REPO_ROOT/.gocache}" go build -ldflags="-X main.version=$VERSION" -o "$BIN_PATH" "$REPO_ROOT"
chmod +x "$BIN_PATH"

echo "Installed to: $BIN_PATH"
case ":${PATH:-}:" in
  *:"$INSTALL_DIR":*)
    ;;
  *)
    echo "Add this directory to PATH if needed: $INSTALL_DIR"
    ;;
esac
