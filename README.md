# godl

A terminal download manager for direct HTTP(S) links, BitTorrent, and
yt-dlp-supported social/media sites. Jobs run in a background daemon, so
they keep going after you close the terminal, and a full-screen TUI
dashboard shows live progress across all of them.

## Features

- **`godl url <link>`** — direct HTTP(S) downloads. Resumable via range
  requests; splits the file into concurrent chunks when the server
  supports ranges, writing each chunk straight to its final offset (no
  merge step).
- **`godl social <link>`** — shells out to [yt-dlp](https://github.com/yt-dlp/yt-dlp).
  Runs in the background like `url`/`torrent` and returns immediately,
  with real progress/speed/ETA in `status`/`list` (parsed from yt-dlp's
  machine-readable progress output). Pass `--wait`/`-w` to instead stay
  attached in the foreground and stream yt-dlp's own output live.
  `-f`/`--format` picks a specific video/audio resolution or quality
  (passed straight through to yt-dlp's own format selector);
  `--list-formats`/`-F` lists what's actually available for a link
  before you choose. See "Picking a resolution" below.
- **`godl torrent <magnet-or-file>`** — BitTorrent downloads via
  [anacrolix/torrent](https://github.com/anacrolix/torrent) (pure Go, no
  cgo, no libtorrent dependency).
- **`godl status`** — full-screen TUI dashboard (bubbletea) listing every
  job with live progress bars, speed, and ETA.
  Keybinds: `p` pause, `r` resume, `x` cancel, `R` retry, `d` remove,
  `D` remove + delete downloaded file (both ask for confirmation),
  `↑`/`↓` navigate, `q` quit (jobs keep running in the background).
- **`godl pause/resume/retry/cancel/remove/list`** — scriptable CLI
  equivalents of the TUI actions. `remove` (alias `rm`) drops a job
  from the list — stopping it first if still running — and `--purge`
  also deletes what it downloaded: the exact file for `url` jobs, or
  the specific file(s) godl resolved during a `torrent`/`social`
  download (not the whole `-o` directory, which usually holds other
  things too).

## Architecture

```
cmd/               cobra command wiring + the bubbletea TUI (status.go)
internal/store     job state in sqlite (modernc.org/sqlite, pure Go)
internal/downloader  shared HTTP download logic: chunked+resumable
internal/torrentmgr  thin wrapper around anacrolix/torrent
internal/daemon    background process + Unix-socket protocol + client
internal/ytdlp     locates/auto-downloads a yt-dlp binary for godl social
internal/ffmpeg    locates/auto-downloads ffmpeg for merging yt-dlp streams
internal/paths     on-disk locations (db, socket, logs)
```

The first godl command run starts a daemon (re-execs itself with a hidden
`__daemon` flag, detached via `setsid`) that owns the sqlite job store,
the HTTP downloader goroutines, and one `anacrolix/torrent` client.
Every other `godl` invocation is a thin client talking to it over a Unix
domain socket (newline-delimited JSON). Torrent pause/resume doesn't
need a saved piece bitmap: dropping a torrent and re-adding it against
the same output directory makes anacrolix re-verify whatever's already
on disk. URL downloads resume from a saved byte offset (single-stream)
or a small JSON sidecar file tracking each chunk's progress (concurrent).

The daemon's Unix socket lives under `$XDG_RUNTIME_DIR` (or the OS temp
dir) rather than under the data directory, since socket paths are capped
at ~108 bytes on Linux and a data dir nested under a long `$HOME` can
blow past that.

## Build

Requires Go 1.25+ (or any Go new enough to honor the `go 1.25.0`
directive in `go.mod` and auto-fetch that toolchain).

```sh
go build -ldflags="-s -w" -o godl .
```

No cgo anywhere — it builds with `CGO_ENABLED=0` and produces a single
static binary (`modernc.org/sqlite` instead of `mattn/go-sqlite3`, and
`anacrolix/torrent`'s optional libutp cgo support is unused). That means
it cross-compiles cleanly, e.g.:

```sh
VERSION=$(grep -oP '(?<=Version = ")[^"]+' internal/version/version.go)
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags="-s -w" -o dist/godl-$VERSION-linux-amd64 .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o dist/godl-$VERSION-darwin-arm64 .
```

(or just `./scripts/build-all.sh`, which does exactly this plus both
installers — see "Versioning" below). Windows doesn't get a standalone
raw binary published — `godl-setup.exe` (below) embeds its own
windows/amd64 build and is a strictly better artifact for anyone on
Windows, so that's the only Windows output.

The daemon's own process-detachment call (so it survives the launching
terminal closing) is one of the only places that differs per OS —
`internal/daemon/exec_unix.go` / `exec_windows.go` split it behind a
build tag so the rest of the code stays platform-agnostic. On Windows,
the daemon's Unix-domain-socket IPC needs `AF_UNIX` support, available
since Windows 10 1803 / Windows Server 2019 (effectively all supported
Windows today).

**Estimated binary size: ~25 MB** stripped (`-s -w`), statically linked
(Windows comes out a bit larger, ~26 MB).
Most of that is the BitTorrent stack (DHT, uTP, WebRTC/PEX support pulled
in by `anacrolix/torrent`) rather than godl's own code.

### Versioning

`internal/version/version.go` is the single source of truth (`godl
--version` reads it directly, no ldflags injection needed — every
build just compiles in whatever's currently there). Bump it before
building a release:

```sh
# internal/version/version.go
var Version = "0.1.1"   # was "0.1.0"
```

Every build path reads this same value, so bumping it once is enough
for all of them to agree: it's baked into every `dist/` filename (`godl-
0.1.0-linux-amd64`, `godl-setup-0.1.0.exe`, `godl_0.1.0_amd64.deb`), the
`.deb`'s `Version:` control field (which is what gives it native `apt`
upgrade detection), the Windows installer's upgrade-vs-fresh-install
messaging, and `install.sh`'s install/rebuild/upgrade messaging.

```sh
./scripts/build-all.sh    # builds every artifact below in one go
```

or individually: the raw cross-compiles (see above), `./scripts/
build-windows-installer.sh`, `./scripts/build-deb.sh`. All land in
`dist/`.

## Install / uninstall

Everything below is safe to re-run to update — a newer version
installed over an older one always wins (dpkg-native for the `.deb`,
version-checked for `install.sh`, always-overwrite for the rest).

### Linux (Debian/Ubuntu): one-click `.deb`

Build it:

```sh
./scripts/build-deb.sh    # -> dist/godl_<version>_amd64.deb
```

Then **double-click it** in your file manager (opens GNOME
Software/GDebi to install) or `sudo apt install ./dist/godl_*.deb`.
This is the real Linux equivalent of the Windows one-click installer —
using `dpkg`/`apt` themselves rather than a bespoke mechanism means
upgrade detection is native: installing a `.deb` with a higher
`Version:` than what's currently installed just upgrades it, the same
way any other apt-managed package works, no custom version-check code
needed. `godl --version` shows what's currently installed if you want
to check.

Installs to `/usr/bin/godl` (system-wide, needs the sudo/root prompt
your package manager already gives you for that — same as installing
any other package). Uninstall the normal way: `sudo apt remove godl`
(keeps job history and cached yt-dlp/ffmpeg, all of which live
per-user under `~/.local/share/godl` regardless of how godl itself was
installed) or `sudo apt purge godl` (also removes them, for whichever
user ran the purge — it won't touch other users' data on a shared
machine). The package's `prerm`/`postrm` scripts (`packaging/debian/`)
handle stopping a running daemon and the purge cleanup.

Only built for amd64 right now; `lintian` isn't installed in this dev
environment to fully lint-check the package, so if you have it, running
it against the built `.deb` is worth doing before relying on this for
anything beyond personal use.

### Windows: one-click installer

Build it (from Linux/WSL, alongside the other cross-compiles):

```sh
./scripts/build-windows-installer.sh    # -> dist/godl-setup-<version>.exe
```

Then just **double-click `dist/godl-setup-<version>.exe`**. It installs
godl to `%LOCALAPPDATA%\Programs\godl`, adds that to your **user** PATH
(no reboot needed — open a new terminal), and registers a proper entry
in **Settings → Apps** so it can be uninstalled from there too, same as
any other Windows app. It also checks what (if anything) is already
installed first and says so — "Installing godl 0.1.0", "godl 0.1.0 is
already installed; reinstalling", or "Upgrading godl 0.1.0 -> 0.1.1" —
so running a newer build over an older install is exactly how you
upgrade.

The installer copies itself into the install dir as a stable
`godl-setup.exe` (unversioned, regardless of what the original
downloaded file was called) specifically so uninstalling later keeps
working: use Settings → Apps → godl → Uninstall, or double-click
`godl-setup.exe -uninstall` from inside the install dir. It stops the
daemon if running (asks first) and offers to also wipe job history and
cached yt-dlp/ffmpeg.

`installer/` is the source for this (a separate, `//go:build windows`
Go program). `dist/godl-setup-<version>.exe -selftest` exercises the
registry/process/version-detection mechanics against safe scratch
state — it never touches your real PATH or kills anything during that
check.

### Linux/macOS script (no package manager, no sudo)

The `.deb` above only covers Debian/Ubuntu. For macOS, other Linux
distros (Fedora, Arch, etc.), or a user-local install on Debian/Ubuntu
instead of the system-wide `.deb`:

```sh
./scripts/install.sh
./scripts/uninstall.sh            # keeps job history and cached yt-dlp/ffmpeg
./scripts/uninstall.sh --purge    # also wipes ~/.local/share/godl
```

Builds from source and installs to `~/.local/bin` (override with
`GODL_INSTALL_DIR`). Prints whether it's a fresh install, an update, or
a rebuild of the version you're already on (reads the old binary's
`--version`).

Every uninstall path in this project (this script, `apt remove`/`purge`
for the `.deb`, and the Windows installer) notices if the background
daemon is still running and asks before stopping it — stopping it just
pauses any in-progress downloads, which resume automatically next time
you reinstall and reuse the same data directory (thanks to the
daemon's crash-recovery, which picks up interrupted jobs on startup
regardless of why it stopped).

(There used to be Windows `install.ps1`/`uninstall.ps1` scripts here
too; removed once `godl-setup.exe` above covered everything they did,
plus proper Settings > Apps integration.)

### yt-dlp is a separate binary — auto-installed on first use

`godl social` shells out to [yt-dlp](https://github.com/yt-dlp/yt-dlp);
it is **not** bundled into the godl binary itself. If `yt-dlp` is
already on `PATH`, godl uses it. Otherwise, the first time `godl social`
actually runs, the daemon downloads the standalone (no-Python-required)
build for your OS/arch straight from yt-dlp's GitHub releases into
`~/.local/share/godl/bin/`, streaming the download status into the same
live output — no manual install step required. Later runs reuse that
cached copy.

To use a system install instead (e.g. to control the version, or on a
platform godl doesn't have a prebuilt yt-dlp for), install it yourself
and it'll be preferred automatically:

```sh
pip install -U yt-dlp
# or see https://github.com/yt-dlp/yt-dlp#installation
```

`godl url`, `godl torrent`, and the daemon/TUI/job-management commands
all work without yt-dlp at all; only `godl social` needs it.

### ffmpeg is also auto-installed, for merging separate streams

Some `-f`/`--format` selectors (e.g. `bv*+ba`, common for best-quality
downloads) make yt-dlp fetch video and audio as separate streams that
need muxing into one file — that requires ffmpeg. Same pattern as
yt-dlp: if `ffmpeg` is on `PATH`, godl uses it; otherwise the first time
a social download needs it, godl downloads a static build from
[BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds) (a
long-running community source of prebuilt ffmpeg binaries — ffmpeg.org
itself doesn't publish simple static downloads) into
`~/.local/share/godl/bin/` and reuses it after that. It's a bigger
one-time download than yt-dlp (a couple hundred MB, since ffmpeg bundles
a large set of codecs). If ffmpeg can't be auto-installed (e.g. an
unsupported OS/arch) or the download fails, godl logs a warning and
still runs the download — yt-dlp just leaves the streams unmerged, same
as if you didn't have ffmpeg at all.

## Usage

```sh
godl url https://example.com/big-file.iso -o out.iso -c 8
godl social https://example.com/watch?v=xyz -o ~/Videos -f "bv*+ba/b"
godl torrent "magnet:?xt=urn:btih:..." -o ~/Downloads
godl status                 # live dashboard
godl list                   # one-shot table, for scripts
godl pause <job-id>
godl resume <job-id>
godl retry <job-id>         # re-run from scratch
godl cancel <job-id>
godl remove <job-id>        # drop from the list, keep the downloaded file
godl rm <job-id> --purge    # drop from the list AND delete the downloaded file
```

Job state, the sqlite db, and the daemon log live under
`~/.local/share/godl` (override with `GODL_DATA_DIR`).

### Picking a resolution for `godl social`

`-f`/`--format` is passed straight through to yt-dlp's own format
selector, so anything yt-dlp accepts there works here too:

```sh
godl social <link>                                            # best combined quality (default)
godl social <link> -f worst                                   # lowest quality (quick preview/test)
godl social <link> -f "bv*+ba"                                 # best video + best audio, merged (needs ffmpeg — auto-installed)
godl social <link> -f "bv*[height<=1080]+ba"                   # cap at 1080p, best audio
godl social <link> -f "bv*[height<=720]+ba/b[height<=720]"     # 720p, falling back to a combined stream if separate ones aren't offered
```

Not sure what's actually available for a given link? List it first,
without downloading anything:

```sh
godl social <link> --list-formats
```

which prints yt-dlp's own format table (id, resolution, codec,
filesize, ...) — pick a code straight out of that (`-f 137+140`) or use
it to decide what height/filter to pass instead.
