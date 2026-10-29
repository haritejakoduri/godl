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

Grab the latest build for your OS and architecture from the
[Releases page](https://github.com/haritejakoduri/godl/releases/latest).
Every binary is a single static file with no runtime dependencies.

| | x86-64 | ARM 64-bit | ARM 32-bit |
|---|---|---|---|
| **Linux** | `godl-<v>-linux-amd64`, `.deb`, `.rpm` | `godl-<v>-linux-arm64`, `.deb`, `.rpm` | `godl-<v>-linux-armv7` |
| **macOS** | `godl-<v>-darwin-amd64` | `godl-<v>-darwin-arm64` | — |
| **Windows** | `godl-setup-<v>.exe` | `godl-<v>-windows-arm64.exe` | — |

The ARM builds cover a Raspberry Pi, an ARM NAS or VPS, and a Windows
on ARM machine. They also cover **Android**: godl runs under
[Termux](https://termux.dev) using the `linux-arm64` binary, which
needs no glibc to link against. There is no Android NDK build and no
app — on a phone you would normally use godl's web interface instead,
pointed at a godl running on a computer or NAS (see
[`godl web`](#godl-web--the-web-interface)).

On a phone that page is an ordinary web page, and on **iOS** Safari's
*Add to Home Screen* turns it into a standalone app. On **Android**,
Chrome's *Install* only appears for a page a browser considers secure,
which means HTTPS or localhost — so over plain http the home-screen
entry is a shortcut that opens in the browser, not an installed app.
`godl web --network --https` fixes that (see below); without it,
installing properly only works on the machine running godl, where the
address is `127.0.0.1` and so counts as secure either way.

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

Double-click `godl_<version>_<arch>.deb` in your file manager, or:

```sh
sudo apt install ./godl_<version>_amd64.deb   # Intel/AMD
sudo apt install ./godl_<version>_arm64.deb   # Raspberry Pi, ARM server
```

Installs to `/usr/bin/godl`. To uninstall: `sudo apt remove godl` (or
`sudo apt purge godl` to also wipe job history and cached yt-dlp/ffmpeg).

### Linux (Fedora/RHEL/other RPM-based distros)

Double-click `godl-<version>-1.<arch>.rpm` in your file manager, or:

```sh
sudo dnf install ./godl-<version>-1.x86_64.rpm    # Intel/AMD
sudo dnf install ./godl-<version>-1.aarch64.rpm   # ARM 64-bit
```

Installs to `/usr/bin/godl`. To update later: `godl update` (it
downloads the newer `.rpm` and runs `dnf` for you). To uninstall:
`sudo dnf remove godl`.
Unlike `apt purge`, `dnf` has no separate "also wipe application data"
step — job history and cached yt-dlp/ffmpeg under
`~/.local/share/godl` are always left in place; remove that directory
yourself if you want it gone too.

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

Every uninstall path stops the background daemon if it's running —
this script asks first, while `apt remove`/`purge`, `dnf remove`, and
the Windows installer do it automatically, best-effort, with no
prompt (package/installer uninstalls typically can't prompt anyway).
Either way, stopping it only pauses in-progress downloads, which
resume automatically on your next install.

## Usage

New to godl? `godl guide` opens an illustrated, task-by-task user guide
in your browser. It is built into the binary, so it works offline
(`godl guide -o guide.html` saves it as a file instead). The rest of
this section is the full reference.

```sh
godl url https://example.com/big-file.iso -o out.iso -c 8
godl url https://example.com/big-file.iso -o out.iso -R 2M   # cap at 2MiB/s
godl url https://example.com/big-file.iso -o out.iso --sha256 <64-char-hex>   # verify on completion
godl url https://example.com/private.zip --cookies cookies.txt -H "Authorization: Bearer <token>"
godl url -i links.txt -o ~/Downloads/batch   # one job per line ("-" reads stdin)
godl social https://example.com/watch?v=xyz -o ~/Videos -p 1080p
godl social <link> --cookies-from-browser firefox   # login-only / age-restricted videos
godl torrent "magnet:?xt=urn:btih:..." -o ~/Downloads
godl torrent "magnet:?..." --list-files      # see what's inside first
godl torrent "magnet:?..." --files "*.mkv" --seed-ratio 1.0
godl stream <job-id>                          # watch a torrent while it downloads
godl play <link> -p 720p                      # watch a YouTube/social link, no download
godl connection add mynas --url https://dav.example.com/remote.php/dav/files/alice/ --username alice
godl webdav mynas /Photos -o ~/Photos     # a file or a whole folder, recursively
godl serve ~/Public -p 8080 --username alice   # share a folder, over WebDAV + browser

godl status                 # live TUI dashboard
godl web                    # the same, plus a video player, in its own window
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

#### Headers and cookies

For downloads behind a login, a token or a picky CDN:

```sh
godl url <link> -H "Authorization: Bearer abc"     # any header; repeatable
godl url <link> --user-agent "Mozilla/5.0 ..." --referer https://example.com/
godl url <link> --cookie "session=abc; theme=dark"
godl url <link> --cookies cookies.txt              # a browser's cookies.txt export
```

`--cookies` takes the Netscape `cookies.txt` format that browser
"export cookies" extensions, curl and yt-dlp all use. A whole-browser
export is safe to pass: each request only gets the cookies whose
domain, path and `Secure` flag match it, and expired ones are skipped.
Headers and cookies go on every request the job makes — the size
probe, the filename lookup and each chunk — and are saved with the job,
so pause/resume/retry keep sending them (the cookies file is re-read on
each start, so refreshing it and retrying just works). A redirect to a
different domain drops `Cookie`/`Authorization`, as Go's HTTP client
always does, so they can't leak to a third-party host.

#### Many links at once

`-i`/`--input-file` reads links from a file, one per line (blank lines
and `#` comments are skipped), or from stdin with `-i -`. Each link
becomes its own job, and one bad link doesn't stop the rest. With
several links `-o` is the directory they're saved into, and two links
that would produce the same filename get `name (2).ext` instead of
overwriting each other. `godl social` and `godl torrent` take `-i` too.

### `godl social` — yt-dlp-supported sites

Runs in the background like `url`/`torrent` and returns immediately, with
live progress/speed/ETA in `status`/`list`. Pass `--wait`/`-w` to instead
stay attached and stream yt-dlp's own output.

Login-only, private, members-only and age-restricted videos usually just
need your browser's cookies: `--cookies-from-browser firefox` (or
`chrome`, `edge`, `brave`, `safari`, ... — anything yt-dlp's own flag
accepts) reads them straight from the browser, and `--cookies
cookies.txt` takes an exported file. `-H`, `--user-agent` and
`--referer` work the same as on `godl url`. `--list-formats` uses them
too, so you can see what a logged-in account gets before downloading.

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

YouTube now hides its streams behind a small script the client has to
run, so yt-dlp needs a JavaScript runtime: godl hands it deno, node or
bun if one is installed, and otherwise downloads and verifies its own
copy of deno the first time it's needed.

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
pick the Social/media type. After the link, the wizard also asks for
an optional output path/directory and an optional rate limit (`2M`,
`500K`, ...) — leaving either blank keeps the same default the CLI
flags would (Downloads folder, unlimited), so a plain `n` → link →
enter → enter behaves exactly like the CLI with no `-o`/`-R`.

**Just watch it.** `godl play <link>` streams the link straight in
[mpv](https://mpv.io) (or [VLC](https://www.videolan.org/vlc/) if mpv
isn't installed) without creating a job or saving anything. `-p` and
`-f` pick the quality exactly as above:

```sh
godl play <link>              # best quality
godl play <link> -p 720p
godl play <link> -p audio     # audio only
```

One of the two players has to be installed already; yt-dlp doesn't —
godl hands the player its own copy. In `godl status`, the `n` wizard's
"Play" type does the same thing.

[yt-dlp](https://github.com/yt-dlp/yt-dlp) and, if a format needs muxing
separate video/audio streams, [ffmpeg](https://ffmpeg.org) are both
auto-downloaded and self-managed by godl on first use — no manual setup
required, and no dependency on (or interference from) anything already
on your `PATH`; `godl update` keeps both current from then on. `godl
url`, `godl torrent`, and job management all work without either.

### `godl torrent` — BitTorrent downloads

Takes a magnet link or `.torrent` file.

**Pick files.** `--list-files` shows what's inside, numbered, without
downloading any content (for a magnet link godl first fetches the file
list from peers, which can take a few seconds). `--files` then takes
numbers, ranges and glob patterns, comma-separated — patterns match
either the full path inside the torrent or just the file name:

```sh
godl torrent <magnet> --list-files
godl torrent <magnet> --files 2,5-7
godl torrent <magnet> --files "*.mkv,Extras/*"
```

Both the dashboard's `n` wizard and the web interface's Torrent tab do
this for you: once a torrent is entered (or a `.torrent` uploaded), its
whole file list appears with every file ticked, to untick, clear (`n` /
None) or tick all again (`a` / All).

Progress, ETA and completion then count only the selected files. (Data
at the very edge of a selected file can share a piece with its
neighbour, so a sliver of an unselected file may still land on disk —
that's how BitTorrent pieces work, not a bug.)

**Seed.** By default a torrent stops sharing the moment it finishes.
`--seed-ratio 1.5` keeps uploading until you've sent 1.5x its size,
`--seed-time 2h` for two hours — with both, whichever comes first. The
job shows as `seeding` (with its live upload speed and ratio) and then
settles to `completed`. Seeding doesn't take up one of the Settings
tab's "max concurrent downloads" slots. Pausing or canceling a seeding
job just stops the seeding; the download stays completed. Seeding
doesn't survive a daemon restart — the job is marked completed.

**Through TorBox.** With a [TorBox](https://torbox.app) account, a
torrent can be downloaded by TorBox on its servers first and then come
to you over plain HTTPS, split into several connections like any `godl
url` download — usually much faster than peers for a torrent with few
seeds, and immediate for one TorBox already has ("cached"). Save your
API key (torbox.app → Settings) in the Settings tab (`s` in `godl
status`, or the web interface), then choose per torrent:

```sh
godl torrent <magnet> --torbox
godl torrent <magnet> --p2p       # past TorBox, when it's the default
```

The dashboard's `n` wizard and the web interface's Torrent tab ask
"Download with: TorBox / This computer" as soon as a key is saved
(the web page also says whether TorBox already has the torrent), and
"Use TorBox for new torrents" makes TorBox the pre-picked choice.
While TorBox works the job shows its progress (`TorBox: downloading
40% · 12 MB/s · 30 seeds`); then the chosen files download into the
same place godl's own client would put them. `--files` works the same
way, and so does changing the choice later from Details. Pause/resume
picks up the same TorBox torrent and keeps finished files. Once the
files are here godl deletes the torrent from your TorBox account to
free the slot, unless "Keep torrents in TorBox" is on. TorBox torrents
aren't seeded or streamed from godl.

**Stream while downloading.** `godl stream <job-id>` (or `o` in `godl
status`) plays a running torrent in mpv/VLC before it's finished. godl
serves the file to the player over a loopback-only HTTP address with a
random per-session token, and fetches the pieces just ahead of wherever
the player is reading first — including after you seek — so playback
starts after a few MB rather than after the whole file. With several
files it picks the largest video/audio file; `--file N` picks another
(numbered as in `--list-files`), and `--url` just prints the address
for another player. A finished job plays from disk.

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

Every PROPFIND (browsing/walking a folder) and GET (downloading a
file) request against a connection retries on a `429 Too Many
Requests` response, honoring the server's own `Retry-After` header
when it sends one, otherwise backing off exponentially — some
cloud-storage-backed WebDAV services (TorBox in particular) rate-limit
aggressively enough that even a single request against the root can
get 429'd, especially right after browsing around, and that's worth
retrying rather than immediately failing the job.

Both kinds of request are also bounded so a connection that just goes
silent — accepts the request but never answers, or stops sending data
partway through a file — fails clearly instead of hanging forever: a
folder listing gives up after 3 minutes (well past the worst case of
the 429 retries above), and a download gives up after 90 seconds with
no new data, though a large file's overall transfer time is otherwise
unbounded. This matters most for a deep/multi-level folder, since
walking it fires off several listing requests at once — the more of
them there are, the higher the odds that one connection wedges, and
before this, that alone was enough to stall the whole folder's
download.

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
active, cyan for seeding, green for completed, red for failed) and
led by an icon, so a long list reads at a
glance instead of requiring you to read every word; the row your
cursor is on shows it in plain text instead, since that row is already
unambiguous from its own highlight. The `Path` and `Source` columns
are responsive — they take up whatever room is left over after the
other columns, so widening your terminal shows more of a long URL or
destination path instead of it staying hard-truncated.

It's animated, so you can tell at a glance what's alive. Progress bars
glide to each new value on a spring instead of jumping twice a second,
drawn to an eighth of a character so even a slow download visibly
creeps forward, with a highlight sweeping along any bar that's actively
moving. A download whose size isn't known yet shows a bouncing bar
instead of sitting at 0%. Running jobs spin, the Speed column draws the
last few seconds as a sparkline, and a job that finishes sparkles for a
moment. The header line sums it all up: jobs by state, the combined
download speed with its own sparkline, and one color bar for everything
still in flight. Frames only run while something is actually moving —
an idle dashboard uses no CPU. Set `GODL_NO_ANIMATION=1` to turn all of
it off (values then update in place, nothing spins); it's also off
when `TERM=dumb`.

Keybinds: `space` toggles a job for multi-select (its checkbox shows
`[x]`, and the title bar shows the running count), `p` pause, `r`
resume, `x` cancel, `R` retry, `d` remove, `D` remove + delete
downloaded file (both ask for confirmation), `o` play/stream a job (see
below), `t` change the sort order (newest, name, status, progress, size, speed,
time left; `T` reverses it), `i` (or `enter`) show a job's details — its full source and
destination and, for a torrent or WebDAV folder, every file with its own
progress (skipped torrent files marked), `n` start a new url/social/torrent download (or just play a
link, without downloading it), `w` browse a
saved WebDAV connection, `s` settings, `S` serve a local folder,
`↑`/`↓` navigate, `q` quit (jobs keep running
in the background). With one or more jobs checked, `p`/`r`/`x`/`R`/`d`/`D`
act on all of them at once instead of just whatever the cursor happens
to be on — the same "selected, or current" rule the WebDAV browser's
own `d` (below) already uses; `o` always acts on just the row under
the cursor.

`o` opens a job in [mpv](https://mpv.io) or, if that's not found,
[VLC](https://www.videolan.org/vlc/) — one of the two must already be
installed (checked on `PATH`, plus VLC's standard Windows install
locations, since its installer doesn't reliably add itself to `PATH`
there); godl doesn't manage/bundle either player the way it does
yt-dlp/ffmpeg, since playback is an optional convenience nothing else
depends on. A **completed** job always plays the file already on disk;
an in-progress `url`/`social`/`webdav` job instead streams straight
from its source (no local download needed — useful as a live preview
while something's still downloading). Either player handles all three:
a `webdav` file's credentials go to mpv as an HTTP header and to VLC
through its own `--http-user`/`--http-pwd`, and a `social` link plays
the way `godl play` plays it, at the job's own quality — mpv follows
the link itself using godl's yt-dlp, while for VLC godl resolves the
direct stream(s) with yt-dlp first. A running
`torrent` job streams through godl's own loopback server, fetching the
pieces the player needs next first — see `godl torrent` above.

`w` opens a file browser for one of your saved `godl connection`s. The
first screen picks a connection — `a` opens a form to add a new one
right there (name, URL, username, password, insecure/skip-TLS-verify;
same validation as `godl connection add`, saved the same way) and
`d` removes the one under the cursor (confirmed), so setting up and
managing WebDAV connections no longer needs a trip to the CLI. Once
inside a connection: `↑`/`↓` moves, `enter` opens a folder, `space`
toggles a file or folder
for bulk selection, `t` sorts it by name, size or date modified (`T` reverses;
folders always come first), `/` searches the current folder by name (filters
live as you type; `enter` keeps the filter and returns to browsing,
`esc` clears it), `←`/backspace goes up a level (also clearing any
active search), `D` downloads the folder you're currently browsing in
full, `o` plays/streams the file under the cursor straight from the
server (no download job, no waiting for one to finish — folders are
skipped, there being nothing to stream), and `d` starts downloading —
whatever's selected, or just the entry under the cursor if nothing is.
Each selected file or folder
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
  overrides this. **Per-job**: three jobs each falling back to this
  default can still add up to 3x it running together — see the next
  setting for a true combined ceiling.
- **Global bandwidth limit** — caps every currently-running job's
  transfer **combined**, not each one separately. For `url` and `webdav`
  jobs (godl's own in-process transfer code) this is a real shared cap:
  every concurrently active job of either type draws from the exact
  same token bucket, so total throughput across all of them together
  never exceeds this value no matter how many are running. `torrent`
  (anacrolix/torrent only exposes one client-wide limiter, not a
  per-job one — see `-R`'s own torrent caveat below) and `social`
  (yt-dlp, a subprocess capped via its own `--limit-rate`) can't share
  that bucket, so each such job is instead individually capped at this
  rate (or its own `-R`/default, if lower) — meaning a torrent or
  social job running alongside url/webdav ones can still push combined
  throughput over this ceiling, even though no single job exceeds it.
  Empty means unlimited.
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

`S` opens the **Serve tab** — the TUI equivalent of `godl serve`,
running for as long as the tab stays open instead of as its own
foreground process. A form asks for the directory to share (defaults
to your Downloads folder), host, port, an optional username/password,
whether to allow write access, whether to use a self-signed https://
certificate, and an "Insecure: allow no-auth" toggle (the TUI
equivalent of `--insecure-no-auth`); `enter` on "Start serving"
begins, and the same safety rail `godl serve` has applies here too —
a host other than `127.0.0.1` without both a username and password is
refused unless that toggle is on. Once
running, the tab shows the same reachable-address/WebDAV/auth banner
`godl serve` prints on startup, and `esc` or `x` stops the server and
returns to the dashboard — leaving the tab always stops it, since
there's no dashboard indicator for "a server is still running
unattended" that would make it safe to forget about.

### `godl web` — the web interface

Everything the dashboard does, in a window of its own: the live
download list with bulk pause/resume/retry/cancel/remove, new downloads
(links, videos, torrents with a file picker, `.torrent` upload), the
WebDAV browser, sharing a folder, and settings — plus a video player
built into the page.

```sh
godl web                                  # turn it on if needed, and open its window
godl web --browser                        # open it as a browser tab instead
godl web --network --username alice --https   # ...encrypted, for phones
godl web --off                            # turn it off
godl web --network --username alice       # also from your phone/other computers (asks for a password)
godl web --local                          # back to this machine only
```

**Its own window.** `godl web` opens with no tabs and no address bar,
and its own entry in the dock or taskbar. That window comes from a
Chromium-family browser (Chrome, Edge, Chromium, Brave, Vivaldi) in app
mode, which is why it may not be the browser you have set as default —
godl asks for a window, and only those can give one. There is no
embedded browser inside godl: shipping one would mean cgo on every
platform and a WebKitGTK dependency on Linux, and godl would stop being
a single file you can download and run. Where no such browser exists —
a Mac with only Safari, say — it opens as an ordinary tab, which is
what godl did everywhere before 0.9.2.

You can also **install it as an app**: in the page's address bar (or
its ⋮ menu) choose *Install godl*, and it gets a permanent window and a
launcher entry of its own, which survives without `godl web` being run
again.

It's **off by default**. When on, godl's background daemon serves it
(port 8787, `--port` to change), so it stays available after the
command returns and after a restart — the "Web interface" rows in the
Settings tab (`s` in the dashboard, or the page's own Settings) switch
it too. While it's off nothing is listening and nothing runs for it.

**Who can use it.** By default only this machine: the page is served on
`127.0.0.1`, and `godl web` opens it with a private token (stored in
godl's data directory, readable only by you) that the browser then
keeps as a cookie for 30 days — long enough that godl installed as an
app still opens when launched from the dock, which happens without a
token in the address — loopback alone isn't enough, since other users of
the machine and other websites can reach a loopback port. With
`--network` it's served on every interface and every device signs in
with the username and password you set; godl refuses network mode
without both.

**Encrypting it (`--https`).** Over the network the sign-in is HTTP
Basic, which puts the username and password in a header in the clear
— readable by anything else on the same Wi-Fi. `godl web --network
--https` serves TLS instead, with a certificate godl generates itself
and keeps in its data directory.

Being self-signed, no device trusts it until you say so, and until
then the page will not load there at all. godl prints a link to the
certificate (`https://<address>:<port>/godl.crt`, which is served
without a sign-in because a phone has to fetch it *before* it can get
in) — open that on the device and install it. On Android, Settings and
then a search for "CA certificate". Doing that is also what makes
Chrome offer to install the interface as an app, since the page then
counts as secure.

`--https` applies to `--network` only. On the machine running godl the
address is `127.0.0.1`, which browsers already treat as secure, and a
self-signed certificate there would buy a warning and nothing else.
The certificate is replaced automatically when it nears expiry or when
the machine gains an address it doesn't cover, so moving a laptop
between networks doesn't break it — though a device that trusted the
old one has to trust the new one. Either way it only answers to an IP address or
`localhost` in the address bar and refuses requests sent by other
websites. Network mode is plain http, like `godl serve` — use it on a
home network you trust, not across the internet.

**The player** plays finished downloads, torrents that are still
downloading (the pieces you're watching are fetched first), files on a
WebDAV server, and web links (YouTube and other yt-dlp sites) without
saving anything — with a menu of the audio languages the file or site
offers, and subtitles. godl never re-encodes the picture: files the
browser can read as they are are handed to it directly; anything else
(an MKV, a choice of audio track, a web link) is repackaged on the fly
by godl's own ffmpeg — picture copied, sound converted to AAC only when
it isn't already — and streamed straight to the page with no temporary
files. Changing language or seeking restarts that stream at the current
position. Sites that slow down long downloads (YouTube) are fetched in
10 MB pieces, as yt-dlp does, so playback stays well ahead. If a site
asks you to sign in (YouTube's "confirm you're not a bot"), enter the
browser you're signed in with under Watch a link — its login is read on
the godl machine by yt-dlp, the same as `--cookies-from-browser`. When the browser can't decode the picture (HEVC in some
browsers, or iPhone/iPad Safari), the player says so and offers a
**stream link** to paste into VLC or another app (the original file,
with every language and subtitle in it; for a web link, a Matroska
stream carrying every audio language) and a **download link**. Those
links work for 12 hours.

It's a control panel, not part of the download path: with nobody
looking at the page it does no work at all, one shared feed serves
every open tab, and a video playing in it costs one ffmpeg process
copying data, capped at a few at a time.

### The system tray icon

The daemon puts an icon in the notification area whenever it runs — on
Windows, Linux and macOS — so a background download manager is
something you can see and control, not an invisible process. There is
nothing to enable.

```
  godl — 2 active, 5 queued, 4.1 MiB/s
  ─────────────────────────────────────
  Open godl                  ← the web interface
  Open terminal dashboard
  ─────────────────────────────────────
  Pause all
  Resume all
  ─────────────────────────────────────
  Restart background service
  Check for updates
  Quit godl
  Hide this icon
```

Counts and speed update live. **Restart background service** stops and
starts the daemon with downloads carrying on from where they were, and
**Check for updates** installs a newer godl and restarts into it,
reporting the outcome as a desktop notification — between those and the
web interface, the tray reaches everything godl does without a
terminal. *Hide this icon* closes only the icon.

Turn it off in Settings ("Show tray icon"), or set `GODL_NO_TRAY=1`
where there is no database to configure. It is skipped automatically
where there is nowhere to show one — no desktop session, or a desktop
with no tray host — so nothing changes for headless use.

`godl tray` runs one by hand; that icon outlives the daemon and offers
to start it again. `godl tray --install-autostart` registers it at
login. Only one icon ever shows: whichever tray gets there first holds
it and the others stand down.

### `godl desktop` — the application menu entry

Puts godl in your system's application menu with its icon, so it can be
launched like any other app. Searching "godl" in the launcher opens the
web interface.

```sh
godl desktop install
godl desktop remove
godl desktop status
```

What that writes depends on the system:

| | |
|---|---|
| **Linux** | a `.desktop` entry plus hicolor icons at every standard size and a scalable SVG, under `~/.local/share` |
| **Windows** | a Start Menu shortcut under your own profile, taking its icon from `godl.exe`'s own resources |
| **macOS** | a `godl.app` bundle in `~/Applications`, with an `.icns` |

All three are per-user: no administrator rights, nothing outside your
own account.

Most installs do this for you — the `.deb` and `.rpm` ship the Linux
files system-wide, and the Windows installer adds the Start Menu
shortcut. Use the command when godl came from a raw binary or
`install.sh`, on macOS, or to point an existing entry at a binary you
have since moved.

### `godl update` — update everything godl manages, including itself

After an update, the background daemon from the old version is replaced
automatically the next time any godl command runs (its downloads carry
on in the new one). `godl restart` does the same by hand.

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
- **Installed from the `.rpm`** (Fedora/RHEL) updates through the
  package manager rather than by swapping the file: rpm owns
  `/usr/bin/godl`, and self-replacing it would desync the package
  database from what's actually on disk. `godl update` downloads and
  verifies the release's `.rpm` the same way, then runs
  `sudo dnf install` on it (`rpm -U` where there's no `dnf`), so expect
  a sudo password prompt.
- **Installed from the `.deb`** (Debian/Ubuntu) is left alone for the
  same reason, with no automatic path yet: run
  `sudo apt update && sudo apt upgrade` instead.
- Any other platform without a published raw binary falls back to
  pointing you at the Releases page too. The ones that do have one are
  listed in `rawAssets` in `internal/selfupdate`, which a test checks
  against the build scripts so the two cannot drift — that drift is
  what once left every Intel Mac being told no update existed.

`godl update` prints which of these applies rather than silently doing
nothing.

## How the code is laid out

godl runs as two processes: the `godl` binary you type commands into,
and a background daemon it starts on first use that owns every job's
lifetime — so downloads keep running after the terminal closes. They
talk over a Unix socket (newline-delimited JSON; see
`internal/daemon/protocol.go`).

```
main.go            → cmd.Execute()
cmd/               cobra commands and plain-CLI output only
tui/               the terminal dashboard (bubbletea)
internal/
  daemon/          the background daemon: job lifecycle, scheduling,
                   settings, and the socket protocol
  store/           sqlite persistence for jobs and settings
  downloader/      chunked HTTP(S) transfers, resume, checksums
  torrentmgr/      BitTorrent, wrapping anacrolix/torrent: file
                   selection, seeding stats, readers for streaming
  ytdlp/ ffmpeg/   the yt-dlp and ffmpeg binaries godl manages itself
  webdav/          WebDAV client: PROPFIND, recursive walk, download
  fileserver/      `godl serve` — the other direction: serve a local
                   directory over HTTP(S) and WebDAV
  connections/     saved WebDAV connection profiles
  format/ social/  helpers shared by every front end
  urlname/ paths/
  httpx/           HTTP clients, with the timeouts and pooling that
                   transfers and metadata fetches each need
  reqhdr/          a job's extra headers and cookies.txt cookies,
                   matched to each request's URL
  ratelimit/ mpv/ notify/ ghrelease/ selfupdate/ version/
```

The dependency direction is one-way and enforced by a test
(`tui/boundary_test.go`): **`cmd` → `tui` → `internal/…`**, and nothing
under `internal/` imports either front end. `tui` exposes exactly one
function, `tui.Run()`. That's what keeps a second front end — a GUI —
a matter of adding a sibling package that drives the same daemon client
and the same `internal/format` helpers, rather than untangling the
terminal UI from the core first.

## Building from source

```sh
go build -trimpath -ldflags="-s -w" -o godl .
```

Requires Go 1.25+. To build every release artifact (cross-platform
binaries, the Windows installer, the `.deb`, and the `.rpm`) into
`dist/`:

```sh
./scripts/build-all.sh
```

The macOS binaries are built separately, **on a Mac**, because the tray
needs Cocoa and therefore cgo and the macOS SDK:

```sh
./scripts/build-darwin.sh
```

It refuses to emit a binary whose tray resolved to the stub, so a macOS
build silently missing the tray can't ship. CI builds both halves on
every PR — Linux and the cross-compiles on `ubuntu-latest`, macOS on
`macos-latest`.

See `scripts/` for the individual build/install/uninstall scripts.
