#!/usr/bin/env sh

set -eu

PROJECT_NAME=${PROJECT_NAME:-claude-switch}
COMMAND_NAME=${COMMAND_NAME:-cs}
REPO=${REPO:-doublepi123/claude_switch}
GITHUB_BASE_URL=${GITHUB_BASE_URL:-https://github.com}
TAG=${TAG:-latest}

if [ -z "${HOME:-}" ]; then
  echo 'HOME is required to choose a default install directory.' >&2
  exit 1
fi

INSTALL_DIR=${INSTALL_DIR:-"$HOME/.local/bin"}
BIN_PATH="$INSTALL_DIR/$COMMAND_NAME"

need_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

detect_os() {
  case "$(uname -s)" in
    Darwin)
      echo darwin
      ;;
    Linux)
      echo linux
      ;;
    *)
      echo "Unsupported OS: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)
      echo amd64
      ;;
    arm64|aarch64)
      echo arm64
      ;;
    *)
      echo "Unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

download_file() {
  url=$1
  dest=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
    return
  fi
  echo 'curl or wget is required to download claude-switch.' >&2
  exit 1
}

release_download_url() {
  asset=$1
  base_url=$(printf '%s' "$GITHUB_BASE_URL" | sed 's:/*$::')
  repo=$(printf '%s' "$REPO" | sed 's:^/*::; s:/*$::')
  if [ "$TAG" = latest ] || [ -z "$TAG" ]; then
    echo "$base_url/$repo/releases/latest/download/$asset"
  else
    echo "$base_url/$repo/releases/download/$TAG/$asset"
  fi
}

detect_profile() {
  if [ -n "${PROFILE:-}" ]; then
    echo "$PROFILE"
    return
  fi

  shell_name=$(basename "${SHELL:-sh}")
  case "$shell_name" in
    zsh)
      echo "$HOME/.zshrc"
      ;;
    bash)
      if [ "$(uname -s)" = Darwin ]; then
        echo "$HOME/.bash_profile"
      else
        echo "$HOME/.bashrc"
      fi
      ;;
    *)
      if [ -f "$HOME/.zshrc" ]; then
        echo "$HOME/.zshrc"
      elif [ -f "$HOME/.bashrc" ]; then
        echo "$HOME/.bashrc"
      else
        echo "$HOME/.profile"
      fi
      ;;
  esac
}

ensure_path_persisted() {
  case ":${PATH:-}:" in
    *:"$INSTALL_DIR":*)
      return
      ;;
  esac

  profile=$(detect_profile)
  mkdir -p "$(dirname "$profile")"
  if [ -f "$profile" ] && grep -F "$INSTALL_DIR" "$profile" >/dev/null 2>&1; then
    echo "PATH already appears to be configured in: $profile"
  elif {
    printf '\n# claude-switch\n'
    printf 'export PATH="%s:$PATH"\n' "$INSTALL_DIR"
  } >> "$profile" 2>/dev/null; then
    echo "Added $INSTALL_DIR to PATH in: $profile"
  else
    echo "Could not update PATH automatically in: $profile" >&2
  fi

  echo "For this terminal session, run:"
  echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
}

GOOS=$(detect_os)
GOARCH=$(detect_arch)
ASSET="$PROJECT_NAME-$GOOS-$GOARCH.tar.gz"
DOWNLOAD_URL=$(release_download_url "$ASSET")

need_command tar
need_command mktemp

TMP_DIR=$(mktemp -d)
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

ARCHIVE_PATH="$TMP_DIR/$ASSET"

echo "Downloading $PROJECT_NAME $TAG for $GOOS/$GOARCH..."
download_file "$DOWNLOAD_URL" "$ARCHIVE_PATH"

tar -xzf "$ARCHIVE_PATH" -C "$TMP_DIR"
if [ ! -f "$TMP_DIR/$COMMAND_NAME" ]; then
  echo "Release archive did not contain $COMMAND_NAME." >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
mv "$TMP_DIR/$COMMAND_NAME" "$BIN_PATH"
chmod +x "$BIN_PATH"

echo "Installed to: $BIN_PATH"
ensure_path_persisted
"$BIN_PATH" --version
