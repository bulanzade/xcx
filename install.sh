#!/usr/bin/env sh
# xcx install script — downloads the latest release binary for the current
# platform and installs it to a directory on PATH.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sh
#   # or, to install system-wide (needs sudo):
#   curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sudo sh
#
# Override the install prefix (default: ~/.local, or /usr/local when run as root):
#   PREFIX=/opt sh install.sh
#
# Uninstall the binary installed by this script:
#   curl -fsSL https://raw.githubusercontent.com/bulanzade/xcx/main/install.sh | sh -s -- --uninstall

set -eu

PREFIX="${PREFIX:-}"
FORCE="${FORCE:-0}"
UNINSTALL="${UNINSTALL:-0}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --force) FORCE=1 ;;
    --uninstall) UNINSTALL=1 ;;
    PREFIX=*) PREFIX="${1#PREFIX=}" ;;
    FORCE=*) FORCE="${1#FORCE=}" ;;
    UNINSTALL=*) UNINSTALL="${1#UNINSTALL=}" ;;
    *) echo "error: unknown option: $1" >&2; exit 1 ;;
  esac
  shift
done

# --- default install prefix -------------------------------------------------
if [ -z "$PREFIX" ]; then
  if [ "$(id -u)" -eq 0 ]; then
    PREFIX="/usr/local"
  else
    PREFIX="$HOME/.local"
  fi
fi
BINDIR="$PREFIX/bin"

# --- detect OS / arch -------------------------------------------------------
case "$(uname -s)" in
  Linux*)  GOOS=linux   ;;
  Darwin*) GOOS=darwin  ;;
  MINGW*|MSYS*|CYGWIN*|*Windows*) GOOS=windows ;;
  *) echo "error: unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64)   GOARCH=amd64 ;;
  arm64|aarch64)  GOARCH=arm64 ;;
  *) echo "error: unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac

if [ "$GOOS" = "windows" ]; then
  ARCHIVE="xcx-$GOOS-$GOARCH.zip"
  BIN="xcx.exe"
else
  ARCHIVE="xcx-$GOOS-$GOARCH.tar.gz"
  BIN="xcx"
fi

INSTALLED="$BINDIR/$BIN"

if [ "$UNINSTALL" = "1" ]; then
  if [ -e "$INSTALLED" ]; then
    rm -f "$INSTALLED"
    echo "Removed $INSTALLED"
  else
    echo "xcx is not installed at $INSTALLED"
  fi
  if [ -e "$INSTALLED.old" ]; then
    rm -f "$INSTALLED.old"
    echo "Removed $INSTALLED.old"
  fi
  echo "Configuration was left untouched: ~/.config/xcx"
  exit 0
fi

URL="https://github.com/bulanzade/xcx/releases/latest/download/$ARCHIVE"
API="https://api.github.com/repos/bulanzade/xcx/releases/latest"

# --- latest version ---------------------------------------------------------
# Query the GitHub API for the latest release tag. We tolerate API failure
# (rate limit, offline) by falling back to a forced upgrade.
fetch_tag() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$API" 2>/dev/null | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$API" 2>/dev/null | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1
  fi
}

LATEST_TAG="$(fetch_tag || true)"

# --- installed version + up-to-date check -----------------------------------
if [ "$FORCE" -eq 0 ] && [ -x "$INSTALLED" ] && [ -n "$LATEST_TAG" ]; then
  CURRENT="$("$INSTALLED" -version 2>/dev/null || echo "")"
  if [ -n "$CURRENT" ] && [ "$CURRENT" = "$LATEST_TAG" ]; then
    echo "xcx $CURRENT is already the latest ($LATEST_TAG). Nothing to do."
    echo "Re-run with FORCE=1 to reinstall anyway."
    exit 0
  fi
  if [ -n "$CURRENT" ]; then
    echo "Upgrading xcx $CURRENT -> $LATEST_TAG"
  else
    echo "Installing xcx $LATEST_TAG (current version unknown)"
  fi
elif [ "$FORCE" -eq 0 ] && [ -x "$INSTALLED" ] && [ -z "$LATEST_TAG" ]; then
  echo "Could not determine latest release tag; proceeding with reinstall."
else
  echo "Installing xcx ${LATEST_TAG:-latest}"
fi

# --- backup existing binary for rollback ------------------------------------
BACKUP=""
if [ -e "$INSTALLED" ]; then
  BACKUP="$INSTALLED.old"
  cp "$INSTALLED" "$BACKUP"
fi

# --- fetch ------------------------------------------------------------------
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading $URL"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o "$TMPDIR/$ARCHIVE"
elif command -v wget >/dev/null 2>&1; then
  wget -qO "$TMPDIR/$ARCHIVE" "$URL"
else
  echo "error: need curl or wget" >&2; exit 1
fi

# --- extract ----------------------------------------------------------------
case "$ARCHIVE" in
  *.tar.gz)
    tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR"
    ;;
  *.zip)
    if command -v unzip >/dev/null 2>&1; then
      unzip -oq "$TMPDIR/$ARCHIVE" -d "$TMPDIR"
    else
      echo "error: need unzip for Windows archive" >&2; exit 1
    fi
    ;;
esac

if [ ! -f "$TMPDIR/$BIN" ]; then
  echo "error: $BIN not found in archive" >&2; exit 1
fi

# --- install ----------------------------------------------------------------
mkdir -p "$BINDIR"
chmod +x "$TMPDIR/$BIN"
if ! mv "$TMPDIR/$BIN" "$INSTALLED"; then
  # restore the backup if the move failed
  if [ -n "$BACKUP" ]; then mv "$BACKUP" "$INSTALLED"; fi
  echo "error: install failed" >&2; exit 1
fi
# verify the new binary runs before declaring success; roll back if it doesn't
if ! "$INSTALLED" -version >/dev/null 2>&1; then
  if [ -n "$BACKUP" ]; then
    mv "$BACKUP" "$INSTALLED"
    echo "error: new binary failed to run; restored previous version" >&2
  fi
  exit 1
fi
# success — drop the backup
[ -n "$BACKUP" ] && rm -f "$BACKUP"
echo "Installed $INSTALLED"

# --- PATH hint --------------------------------------------------------------
case ":$PATH:" in
  *":$BINDIR:"*) ;;
  *)
    echo
    echo "note: $BINDIR is not on your PATH."
    echo "Add it by appending to your shell rc, e.g.:"
    echo "  export PATH=\"$BINDIR:\$PATH\""
    ;;
esac

echo
echo "Run: xcx"
