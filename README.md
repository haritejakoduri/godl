# godl

[![CI](https://github.com/haritejakoduri/godl/actions/workflows/ci.yml/badge.svg)](https://github.com/haritejakoduri/godl/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/haritejakoduri/godl)](https://github.com/haritejakoduri/godl/releases/latest)
[![Go version](https://img.shields.io/github/go-mod/go-version/haritejakoduri/godl)](go.mod)
[![License: MIT](https://img.shields.io/github/license/haritejakoduri/godl)](LICENSE)

A terminal download manager for direct HTTP(S) links, BitTorrent,
yt-dlp-supported social/media sites, and WebDAV servers — and it can
also serve a local folder the same way (`godl serve`), for other
devices to browse and download from. Downloads run in a background
daemon, so they keep going after you close the terminal, and a
full-screen TUI dashboard shows live progress across all of them.

## Demo

<video src="https://github.com/user-attachments/assets/44e35499-21d1-4fd0-b3b5-6e8662d461b5" controls muted title="godl demo"></video>


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
godl url https://example.com/big-file.iso -o out.iso -R 2M   # cap at 2MiB/s
godl url https://example.com/big-file.iso -o out.iso --sha256 <64-char-hex>   # verify on completion
godl social https://example.com/watch?v=xyz -o ~/Videos -p 1080p
godl torrent "magnet:?xt=urn:btih:..." -o ~/Downloads
godl connection add mynas --url https://dav.example.com/remote.php/dav/files/alice/ --username alice
godl webdav mynas /Photos -o ~/Photos     # a file or a whole folder, recursively
godl serve ~/Public -p 8080 --username alice   # share a folder, over WebDAV + browser

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

`-R`/`--limit-rate` (on `url`, `social`, `torrent`, and `webdav`) caps
that job's own transfer speed, e.g. `-R 500K` or `-R 2M` (accepts a bare
byte count too). Each job's cap is independent and survives pause/
resume/retry — with one exception: anacrolix/torrent, the BitTorrent
library godl uses, only supports a rate limit shared across its whole
client rather than one per torrent, so `-R` on `godl torrent` really
means "cap every currently-active torrent job at this combined rate,"
not just the one you passed it to. A job that doesn't pass `-R` at all
falls back to the Settings tab's **default rate limit** (see `godl
status` below), if one's set — `-R` always wins when both are present.

### `godl url` — direct HTTP(S) downloads

Resumable, and splits into concurrent chunks when the server supports
range requests (`-c`/`--connections`).

`--sha256 <hex>` is opt-in verification: without it, `godl url` behaves
exactly as before. When set, godl hashes the completed file and compares
it against the digest you passed, exactly once, after the download
reaches 100% (there's no per-chunk hashing — a plain HTTP source has no
manifest of per-chunk hashes to check against, unlike BitTorrent, which
gets that for free from the protocol). On a match, nothing changes. On a
mismatch, godl deletes the file and fails the job with an explanation —
this almost always means the source served corrupted or tampered data in
transit, not a godl bug, but because the digest covers the whole file
there's no way to know which part was bad, so `godl retry` has to
redownload the whole thing rather than repairing just the bad bytes (the
same tradeoff `curl`/`wget`/`aria2` make with whole-file checksums).

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

### `godl serve` — share a local folder over HTTP(S)/WebDAV

The other direction: instead of connecting to someone else's server,
`godl serve <dir>` turns a local directory into one:

```sh
godl serve ~/Public -p 8080 --username alice          # http://<this machine>:8080
godl serve ~/Public --self-signed --username alice     # https://, untrusted cert
godl serve ~/Public --host 127.0.0.1                   # this machine only, no auth needed
```

It exposes the directory two ways at once:

- **A real WebDAV endpoint** at `/dav/` — mount it as a network drive in
  Windows Explorer, macOS Finder, or a Linux file manager, point
  `rclone` at it, or add it as a `godl connection` (the command prints
  the exact `godl connection add ...` line to run) and browse/bulk-
  download from it with the same TUI browser — multi-select, `D`,
  `/` search — used for any other WebDAV connection.
- **A plain browser page** at `/` for anyone who'd rather just click
  links: check off any mix of files and folders and hit "Download
  selected (.zip)" to get them all in one archive, structure preserved,
  no mounting required.

By default it binds every network interface (`--host 0.0.0.0`), and
prints each interface's real, actually-connectable IP in the startup
banner — `0.0.0.0` itself isn't something another device can connect
to, so this saves hunting it down with `ip addr`/`ifconfig` yourself:

```
Serving /home/alice/Public
  Reachable at (use whichever address the other device can actually reach):
    http://127.0.0.1:8080/      (WebDAV: http://127.0.0.1:8080/dav/)
    http://192.168.1.42:8080/   (WebDAV: http://192.168.1.42:8080/dav/)
    http://10.0.0.5:8080/       (WebDAV: http://10.0.0.5:8080/dav/)
  ...
```

Read-only by default (pass `--allow-write` to also accept uploads/
deletes over WebDAV). Binding to anything other than `127.0.0.1`/
`localhost` refuses to start unless you pass `--username`/`--password`
or explicitly override with `--insecure-no-auth` — otherwise anyone who
can reach the address could download everything under `<dir>`.
`--tls-cert`/`--tls-key` serve a real certificate; `--self-signed`
generates a throwaway one for https:// without needing files (clients
will warn until you trust it — fine for your own devices on your own
network, not for anything wider). If `-p`'s port is already taken, it
tries the next few ports automatically and prints a warning saying
which one it actually picked — the printed URLs always reflect the
port it's really listening on.

### `godl status` — live TUI dashboard

Lists every job with progress bars, speed, ETA, its download destination
(`Path`), and its source link/magnet/file (`Source`) — newest job
first, so whatever you just started is right at the top instead of
pushed to the bottom behind everything already running. `Status` is
color-coded (gray for queued/canceled, amber for paused, yellow for
active, green for completed, red for failed) so a long list reads at a
glance instead of requiring you to read every word; the row your
cursor is on shows it in plain text instead, since that row is already
unambiguous from its own highlight. The `Path` and `Source` columns
are responsive — they take up whatever room is left over after the
other columns, so widening your terminal shows more of a long URL or
destination path instead of it staying hard-truncated.

Keybinds: `space` toggles a job for multi-select (its checkbox shows
`[x]`, and the title bar shows the running count), `p` pause, `r`
resume, `x` cancel, `R` retry, `d` remove, `D` remove + delete
downloaded file (both ask for confirmation), `n` start a new
url/social/torrent download, `w` browse a saved WebDAV connection,
`↑`/`↓` navigate, `q` quit (jobs keep running in the background). With
one or more jobs checked, `p`/`r`/`x`/`R`/`d`/`D` act on all of them at
once instead of just whatever the cursor happens to be on — the same
"selected, or current" rule the WebDAV browser's own `d` (below)
already uses.

`w` opens a file browser for one of your saved `godl connection`s:
`↑`/`↓` moves, `enter` opens a folder, `space` toggles a file or folder
for bulk selection, `/` searches the current folder by name (filters
live as you type; `enter` keeps the filter and returns to browsing,
`esc` clears it), `←`/backspace goes up a level (also clearing any
active search), `D` downloads the folder you're currently browsing in
full, and `d` starts downloading — whatever's selected, or just the
entry under the cursor if nothing is. Each selected file or folder
becomes its own background job (a folder job pulls it down
recursively, preserving that folder's own name and structure under
the destination), so a single `d` press can kick off any mix of
individual files and whole folders at once.

`s` opens the **Settings tab** — the daemon's configurable defaults,
edited in place and saved immediately on each change (nothing to lose by
navigating away or quitting mid-edit):

- **Max concurrent downloads** — caps how many jobs run at once, across
  every job type combined. 0 (the default) means unlimited, matching
  godl's behavior before this setting existed. Jobs beyond the cap show
  as `queued` and start automatically, oldest first, as running ones
  finish — nothing is dropped or needs to be manually resumed.
- **Default rate limit** — applied to a new job that doesn't pass its
  own `-R`/`--limit-rate`, in the same syntax that flag accepts (e.g.
  `2M`). Empty means unlimited. An explicit `-R` on a given job always
  overrides this.
- **Auto-retry on failure** — a job that fails (not one you paused or
  canceled) is automatically re-queued after a backoff delay (5s, 15s,
  45s, ... capped at 5 minutes) instead of sitting failed until you run
  `godl retry` by hand. **Auto-retry max attempts** caps how many times
  before it's left failed for good; a manual `godl retry` always resets
  that count, giving the job a fresh budget.
- **Notify on completion** — fires a best-effort desktop notification
  when a job finishes successfully (`notify-send` on Linux, `osascript`
  on macOS; no built-in mechanism on Windows, so it's a silent no-op
  there). Best-effort by design: the daemon has no guaranteed UI session
  to notify into, so a failure here never affects the download itself.

`↑`/`↓` moves between settings, `enter` edits a number/text field (a
second `enter` saves, `esc` cancels the edit) or toggles a checkbox
field immediately, and `esc` closes the tab.

### `godl update` — update everything godl manages, including itself

Forces an immediate check for a newer yt-dlp/ffmpeg build (godl checks
on its own too — see `godl social` above) *and* a newer godl release,
updating whichever it finds:

```sh
godl update
```

Self-update downloads and verifies the platform's raw release binary
(the same sha256-against-GitHub's-own-digest check described in
`internal/ghrelease`'s doc comment) and swaps it in for the currently
running one — safe to do even while other godl commands are running,
since replacing the file at a path doesn't disturb whatever already
has it open; only the *next* invocation sees the new binary. This only
works where there's a raw binary to swap in, though:

- **Windows** ships only the installer (`godl-setup-<version>.exe`),
  not a standalone binary — grab the newer installer from the
  [Releases page](https://github.com/haritejakoduri/godl/releases/latest)
  instead, same as a first install.
- **Installed via `apt`** (the `.deb` package) is left alone
  deliberately: dpkg owns `/usr/bin/godl`, and self-replacing it would
  desync the package database from what's actually on disk. Run
  `sudo apt update && sudo apt upgrade` instead.
- Any other platform without a published raw binary (currently just
  linux/amd64 and darwin/arm64 are built — see `scripts/build-all.sh`)
  falls back to pointing you at the Releases page too.

`godl update` prints which of these applies rather than silently doing
nothing.

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
