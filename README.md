# godl

[![CI](https://github.com/haritejakoduri/godl/actions/workflows/ci.yml/badge.svg)](https://github.com/haritejakoduri/godl/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/haritejakoduri/godl)](https://github.com/haritejakoduri/godl/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/haritejakoduri/godl)](go.mod)
[![License: MIT](https://img.shields.io/github/license/haritejakoduri/godl)](LICENSE)

A terminal download manager for direct HTTP(S) links, BitTorrent,
yt-dlp-supported social/media sites, and WebDAV servers. Downloads run
in a background daemon, so they keep going after you close the
terminal, and a full-screen TUI dashboard shows live progress across
all of them.

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
godl social https://example.com/watch?v=xyz -o ~/Videos -p 1080p
godl torrent "magnet:?xt=urn:btih:..." -o ~/Downloads
godl connection add mynas --url https://dav.example.com/remote.php/dav/files/alice/ --username alice
godl webdav mynas /Photos -o ~/Photos     # a file or a whole folder, recursively

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

Every download — `url`, `social`, `torrent`, `webdav`, from the CLI or
the TUI's `n` wizard — defaults to your **actual system Downloads
folder** when you don't pass `-o`, the same place a browser or any
other download manager would put things: the real Windows
known-folder path (which a user can relocate to another drive via
Explorer) on Windows, your Linux desktop's configured XDG user-dirs
location (which can be relocated, or in a non-English locale renamed
entirely) on Linux, and `~/Downloads` — already correct there — on
macOS. `GODL_DOWNLOADS_DIR` overrides all of that if you want
downloads to land somewhere else by default.

### `godl url` — direct HTTP(S) downloads

Resumable, and splits into concurrent chunks when the server supports
range requests (`-c`/`--connections`).

### `godl social` — yt-dlp-supported sites

Runs in the background like `url`/`torrent` and returns immediately, with
live progress/speed/ETA in `status`/`list`. Pass `--wait`/`-w` to instead
stay attached and stream yt-dlp's own output.

`-p`/`--preset` picks a video/audio quality by name — the easiest way
to pick a resolution without knowing yt-dlp's format-selector syntax:

```sh
godl social <link>                 # best combined quality (default)
godl social <link> -p 1080p        # cap at 1080p, best audio
godl social <link> -p 720p         # cap at 720p, best audio
godl social <link> -p 480p         # cap at 480p, best audio
godl social <link> -p worst        # lowest quality (quick preview/test)
godl social <link> -p audio        # audio only, best available quality
```

`godl social --list-presets` prints the full list. For full control,
`-f`/`--format` instead passes a selector straight through to yt-dlp
(not together with `-p`):

```sh
godl social <link> -f "bv*+ba"                                 # best video + best audio, merged
godl social <link> -f "bv*[height<=1080]+ba"                   # cap at 1080p, best audio
godl social <link> -f "bv*[height<=720]+ba/b[height<=720]"     # 720p, falling back to combined
```

Not sure what's available for a link? List formats first, without
downloading anything:

```sh
godl social <link> --list-formats
```

The TUI's `n` "new download" wizard offers the same presets when you
pick the Social/media type.

[yt-dlp](https://github.com/yt-dlp/yt-dlp) and, if a format needs muxing
separate video/audio streams, [ffmpeg](https://ffmpeg.org) are both
auto-downloaded and self-managed by godl on first use — no manual setup
required, and no dependency on (or interference from) anything already
on your `PATH`; `godl update` keeps both current from then on. `godl
url`, `godl torrent`, and job management all work without either.

### `godl torrent` — BitTorrent downloads

Takes a magnet link or `.torrent` file.

### `godl connection` / `godl webdav` — WebDAV

`godl connection add <name> --url <http(s)://...> [--username ...] [--password ... | prompted]`
saves a named WebDAV connection (credentials live under godl's data
directory, readable only by your user account). `--insecure` skips TLS
certificate verification, for self-signed https servers.

```sh
godl connection add mynas --url https://dav.example.com/remote.php/dav/files/alice/ --username alice
godl connection list
godl connection remove mynas
```

`godl webdav <connection> <remote-path> [-o output-dir]` then downloads
from it: if `<remote-path>` is a file, just that file is fetched; if
it's a folder, the whole folder is downloaded recursively into
`-o/<folder name>`, preserving its own name and its full directory
structure underneath (so `godl webdav mynas /Photos` lands at
`<Downloads>/Photos/...`, not with `Photos` itself dropped and its
contents dumped straight into `-o`). Without `-o`, it uses the same
Downloads default as every other command above. The TUI's WebDAV
browser (`w` in `godl status`) shows exactly where files will land
right in the overlay, and confirms the destination again once a
download starts; `D` downloads the folder currently being browsed, in
full, regardless of the cursor position or what's individually
checked with space.

Connections are the first of what's meant to be a general "remote
storage" mechanism — Google Drive, OneDrive, and other cloud storage
providers are expected to become additional connection types the same
`godl connection` commands manage, alongside WebDAV.

### `godl status` — live TUI dashboard

Lists every job with progress bars, speed, ETA, its download destination
(`Path`), and its source link/magnet/file (`Source`). The `Path` and
`Source` columns are responsive — they take up whatever room is left
over after the other columns, so widening your terminal shows more of
a long URL or destination path instead of it staying hard-truncated.

Keybinds: `p` pause, `r` resume, `x` cancel, `R` retry, `d` remove, `D`
remove + delete downloaded file (both ask for confirmation), `n` start
a new url/social/torrent download, `w` browse a saved WebDAV
connection, `↑`/`↓` navigate, `q` quit (jobs keep running in the
background).

`w` opens a file browser for one of your saved `godl connection`s:
`↑`/`↓` moves, `enter` opens a folder, `space` toggles a file or folder
for bulk selection, `←`/backspace goes up a level, and `d` starts
downloading — whatever's selected, or just the entry under the cursor
if nothing is. Each selected file or folder becomes its own background
job (a folder job pulls it down recursively), so a single `d` press can
kick off any mix of individual files and whole folders at once.

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
