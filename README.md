# godl

[![CI](https://github.com/haritejakoduri/godl/actions/workflows/ci.yml/badge.svg)](https://github.com/haritejakoduri/godl/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/haritejakoduri/godl)](https://github.com/haritejakoduri/godl/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/haritejakoduri/godl)](go.mod)
[![License: MIT](https://img.shields.io/github/license/haritejakoduri/godl)](LICENSE)

A terminal download manager for direct HTTP(S) links, BitTorrent, and
yt-dlp-supported social/media sites. Downloads run in a background daemon,
so they keep going after you close the terminal, and a full-screen TUI
dashboard shows live progress across all of them.

## Install

Grab the latest build for your OS from the
[Releases page](https://github.com/haritejakoduri/godl/releases/latest).

### Windows

Double-click `godl-setup-<version>.exe`. It installs godl to
`%LOCALAPPDATA%\Programs\godl`, adds that to your PATH (no reboot needed —
just open a new terminal), and adds an entry under **Settings → Apps** for
uninstalling later. Running a newer installer over an existing install
upgrades it in place.

**You'll likely see a "Windows protected your PC" SmartScreen warning** —
that's expected for any new app from a small/independent publisher and
isn't specific to godl; it happens because the file hasn't built up
download reputation yet, and only goes away for good with a paid
code-signing certificate. To proceed: click **More info**, then **Run
anyway**.

To uninstall: **Settings → Apps → godl → Uninstall**.

### Linux (Debian/Ubuntu)

Double-click `godl_<version>_amd64.deb` in your file manager, or:

```sh
sudo apt install ./godl_<version>_amd64.deb
```

Installs to `/usr/bin/godl`. To uninstall: `sudo apt remove godl` (or
`sudo apt purge godl` to also wipe job history and cached yt-dlp/ffmpeg).

### macOS / other Linux (no package manager, no sudo)

```sh
git clone https://github.com/haritejakoduri/godl.git
cd godl
./scripts/install.sh
```

Builds from source (requires Go 1.25+) and installs to `~/.local/bin`
(override with `GODL_INSTALL_DIR`). Re-run anytime to update.

```sh
./scripts/uninstall.sh            # keeps job history and cached yt-dlp/ffmpeg
./scripts/uninstall.sh --purge    # also wipes ~/.local/share/godl
```

Every uninstall path (this script, `apt remove`/`purge`, and the Windows
installer) checks whether the background daemon is running and asks
before stopping it — stopping it only pauses in-progress downloads, which
resume automatically on your next install.

## Usage

```sh
godl url https://example.com/big-file.iso -o out.iso -c 8
godl social https://example.com/watch?v=xyz -o ~/Videos -f "bv*+ba/b"
godl torrent "magnet:?xt=urn:btih:..." -o ~/Downloads

godl status                 # live TUI dashboard
godl list                   # one-shot table, for scripts
godl pause <job-id>
godl resume <job-id>
godl retry <job-id>         # re-run from scratch
godl cancel <job-id>
godl remove <job-id>        # drop from the list, keep the downloaded file
godl rm <job-id> --purge    # drop from the list AND delete the downloaded file
```

Job state and logs live under `~/.local/share/godl` (override with
`GODL_DATA_DIR`).

### `godl url` — direct HTTP(S) downloads

Resumable, and splits into concurrent chunks when the server supports
range requests (`-c`/`--connections`).

### `godl social` — yt-dlp-supported sites

Runs in the background like `url`/`torrent` and returns immediately, with
live progress/speed/ETA in `status`/`list`. Pass `--wait`/`-w` to instead
stay attached and stream yt-dlp's own output.

`-f`/`--format` picks a specific video/audio quality, passed straight
through to yt-dlp's format selector:

```sh
godl social <link>                                            # best combined quality (default)
godl social <link> -f worst                                   # lowest quality (quick preview/test)
godl social <link> -f "bv*+ba"                                 # best video + best audio, merged
godl social <link> -f "bv*[height<=1080]+ba"                   # cap at 1080p, best audio
godl social <link> -f "bv*[height<=720]+ba/b[height<=720]"     # 720p, falling back to combined
```

Not sure what's available for a link? List formats first, without
downloading anything:

```sh
godl social <link> --list-formats
```

[yt-dlp](https://github.com/yt-dlp/yt-dlp) and, if a format needs muxing
separate video/audio streams, [ffmpeg](https://ffmpeg.org) are both
auto-downloaded on first use if not already on your `PATH` — no manual
setup required. `godl url`, `godl torrent`, and job management all work
without either.

### `godl torrent` — BitTorrent downloads

Takes a magnet link or `.torrent` file.

### `godl status` — live TUI dashboard

Lists every job with progress bars, speed, and ETA.

Keybinds: `p` pause, `r` resume, `x` cancel, `R` retry, `d` remove, `D`
remove + delete downloaded file (both ask for confirmation), `↑`/`↓`
navigate, `q` quit (jobs keep running in the background).

## Building from source

```sh
go build -ldflags="-s -w" -o godl .
```

Requires Go 1.25+. To build every release artifact (cross-platform
binaries, the Windows installer, and the `.deb`) into `dist/`:

```sh
./scripts/build-all.sh
```

See `scripts/` for the individual build/install/uninstall scripts.
