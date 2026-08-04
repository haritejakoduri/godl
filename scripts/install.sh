#!/usr/bin/env bash
# Builds godl from source and installs it for the current user.
# No sudo, no system-wide changes. Safe to re-run to update.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
INSTALL_DIR="${GODL_INSTALL_DIR:-$HOME/.local/bin}"

if ! command -v go >/dev/null 2>&1; then
	echo "error: go is not on PATH." >&2
	echo "Install Go (https://go.dev/dl/) or, for a user-local install with no sudo:" >&2
	echo "  curl -sL https://go.dev/dl/go1.25.4.linux-amd64.tar.gz | tar -C ~/.local -xz" >&2
	echo "  export PATH=\$PATH:~/.local/go/bin" >&2
	exit 1
fi

NEW_VERSION="$(grep -oP '(?<=Version = ")[^"]+' "$REPO_DIR/internal/version/version.go" || true)"
OLD_VERSION=""
if [ -x "$INSTALL_DIR/godl" ]; then
	OLD_VERSION="$("$INSTALL_DIR/godl" --version 2>/dev/null | awk '{print $NF}' || true)"
fi

mkdir -p "$INSTALL_DIR"
echo "Building godl${NEW_VERSION:+ $NEW_VERSION}..."
(cd "$REPO_DIR" && CGO_ENABLED=0 go build -ldflags="-s -w" -o "$INSTALL_DIR/godl" .)
chmod +x "$INSTALL_DIR/godl"

if [ -z "$OLD_VERSION" ]; then
	echo "Installed godl${NEW_VERSION:+ $NEW_VERSION} to $INSTALL_DIR/godl"
elif [ "$OLD_VERSION" = "$NEW_VERSION" ]; then
	echo "godl $NEW_VERSION rebuilt at $INSTALL_DIR/godl (was already at this version; picks up any local source changes)"
else
	echo "Updated godl $OLD_VERSION -> $NEW_VERSION at $INSTALL_DIR/godl"
fi

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	echo
	echo "NOTE: $INSTALL_DIR isn't on your PATH yet. Add this to your shell rc"
	echo "(~/.bashrc, ~/.zshrc, etc.) and open a new terminal:"
	echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
	;;
esac

echo
echo "Done. Try: godl --help"
