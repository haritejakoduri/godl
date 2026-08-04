#!/usr/bin/env bash
# Removes the godl binary this user installed via install.sh.
# Job history and cached yt-dlp/ffmpeg binaries are kept by default
# (pass --purge to also remove them).
set -euo pipefail

INSTALL_DIR="${GODL_INSTALL_DIR:-$HOME/.local/bin}"
BIN_PATH="$INSTALL_DIR/godl"
DATA_DIR="${GODL_DATA_DIR:-$HOME/.local/share/godl}"

PURGE=0
for arg in "$@"; do
	case "$arg" in
	--purge) PURGE=1 ;;
	esac
done

DAEMON_PID="$(pgrep -f "$BIN_PATH __daemon" 2>/dev/null || true)"
if [ -n "$DAEMON_PID" ]; then
	echo "godl's background daemon is running (pid $DAEMON_PID)."
	echo "Stopping it will pause any in-progress downloads; they resume automatically"
	echo "if you reinstall and reuse the same data directory ($DATA_DIR)."
	read -r -p "Stop it now? [y/N] " reply
	case "$reply" in
	[yY]*)
		kill "$DAEMON_PID"
		echo "Stopped."
		;;
	*)
		echo "Leaving the daemon running; stop it manually later if you want."
		;;
	esac
fi

if [ -f "$BIN_PATH" ]; then
	rm "$BIN_PATH"
	echo "Removed $BIN_PATH"
else
	echo "$BIN_PATH not found; nothing to remove there."
fi

if [ "$PURGE" -eq 1 ]; then
	if [ -d "$DATA_DIR" ]; then
		rm -rf "$DATA_DIR"
		echo "Removed $DATA_DIR (job history, cached yt-dlp/ffmpeg, logs)."
	fi
elif [ -d "$DATA_DIR" ]; then
	echo "Left $DATA_DIR in place (job history, cached yt-dlp/ffmpeg, logs)."
	echo "Re-run with --purge to remove it too."
fi
