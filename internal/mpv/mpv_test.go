package mpv

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestInstallHint(t *testing.T) {
	// Just confirm every platform gets a non-empty, actionable hint —
	// the exact wording is allowed to change.
	if h := installHint(); h == "" {
		t.Error("installHint() returned empty string")
	}
}

func TestVLCCandidatePaths(t *testing.T) {
	env := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
	}
	getenv := func(k string) string { return env[k] }

	got := vlcCandidatePaths("windows", getenv)
	want := []string{
		`C:\Program Files\VideoLAN\VLC\vlc.exe`,
		`C:\Program Files (x86)\VideoLAN\VLC\vlc.exe`,
	}
	if len(got) != len(want) {
		t.Fatalf("vlcCandidatePaths(windows, ...) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("vlcCandidatePaths(windows, ...)[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if got := vlcCandidatePaths("linux", getenv); got != nil {
		t.Errorf("vlcCandidatePaths(linux, ...) = %v, want nil (Windows-only fallback)", got)
	}
	if got := vlcCandidatePaths("darwin", getenv); got != nil {
		t.Errorf("vlcCandidatePaths(darwin, ...) = %v, want nil (Windows-only fallback)", got)
	}

	// A missing env var is skipped rather than producing a bogus path
	// with an empty directory component.
	got = vlcCandidatePaths("windows", func(k string) string {
		if k == "ProgramFiles" {
			return `C:\Program Files`
		}
		return ""
	})
	if len(got) != 1 || got[0] != `C:\Program Files\VideoLAN\VLC\vlc.exe` {
		t.Errorf("vlcCandidatePaths with only ProgramFiles set = %v, want [C:\\Program Files\\VideoLAN\\VLC\\vlc.exe]", got)
	}
}

// TestPlayReportsNeitherPlayerFound points PATH at an empty directory
// so exec.LookPath fails for both mpv and vlc, reliably regardless of
// whether the machine actually running this test suite happens to
// have either installed — and since this test doesn't run on Windows,
// vlcCandidatePaths' fallback locations are never in play either, so
// findPlayer must come up empty and name both players in the error.
func TestPlayReportsNeitherPlayerFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := Play("/home/alice/Downloads/movie.mkv", nil)
	if err == nil {
		t.Fatal("Play with neither mpv nor vlc on PATH returned no error")
	}
	if !strings.Contains(err.Error(), "mpv") || !strings.Contains(err.Error(), "VLC") {
		t.Errorf("error %q doesn't mention both players — a user with neither installed should be told about both options", err.Error())
	}
}

// TestAuthArgsPerPlayer pins how each player is handed credentials.
// VLC used to be refused outright for an authenticated target, because
// it has no equivalent of mpv's --http-header-fields; it has its own
// user/password flags instead, so the WebDAV "o" action now works with
// either player rather than only with mpv installed.
//
// Neither form embeds the credential in the URL, which is the one
// option genuinely worse than the others: a player writes the URL it
// was given into its own recent-files list, where the password would
// outlive the process that was handed it.
func TestAuthArgsPerPlayer(t *testing.T) {
	auth := &Auth{Username: "alice", Password: "s3cret"}

	mpvArgs := authArgs(playerMPV, auth)
	// base64("alice:s3cret")
	want := "--http-header-fields=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if len(mpvArgs) != 1 || mpvArgs[0] != want {
		t.Errorf("authArgs(mpv) = %q, want [%q]", mpvArgs, want)
	}

	vlcArgs := authArgs(playerVLC, auth)
	wantVLC := []string{"--http-user=alice", "--http-pwd=s3cret"}
	if len(vlcArgs) != len(wantVLC) {
		t.Fatalf("authArgs(vlc) = %q, want %q", vlcArgs, wantVLC)
	}
	for i := range wantVLC {
		if vlcArgs[i] != wantVLC[i] {
			t.Errorf("authArgs(vlc)[%d] = %q, want %q", i, vlcArgs[i], wantVLC[i])
		}
	}

	for _, kind := range []playerKind{playerMPV, playerVLC} {
		if got := authArgs(kind, nil); got != nil {
			t.Errorf("authArgs(%v, nil) = %q, want no flags at all", kind, got)
		}
		if got := authArgs(kind, &Auth{}); got != nil {
			t.Errorf("authArgs(%v, empty) = %q, want no flags at all", kind, got)
		}
	}

	// Nothing may leak the password into the target-facing URL form.
	for _, a := range append(mpvArgs, vlcArgs...) {
		if strings.Contains(a, "alice:s3cret@") {
			t.Errorf("argument %q embeds credentials in URL form", a)
		}
	}
}

// TestNameReportsNoPlayer: with neither player on PATH, Name must fail
// rather than name one that isn't there.
func TestNameReportsNoPlayer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if name, err := Name(); err == nil {
		t.Errorf("Name() = %q with no player installed, want an error", name)
	}
}

func TestMPVLinkArgs(t *testing.T) {
	// A comma in the path is why this is --script-opts-append: plain
	// --script-opts would split the value there.
	got := mpvLinkArgs("/home/a,b/bin/yt-dlp", "")
	want := []string{"--script-opts-append=ytdl_hook-ytdl_path=/home/a,b/bin/yt-dlp", "--force-window=immediate"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("mpvLinkArgs(no format) = %q, want %q", got, want)
	}

	got = mpvLinkArgs("/bin/yt-dlp", "bv*[height<=720]+ba/b[height<=720]")
	if last := got[len(got)-1]; last != "--ytdl-format=bv*[height<=720]+ba/b[height<=720]" {
		t.Errorf("mpvLinkArgs(format) ends with %q, want the --ytdl-format flag", last)
	}
}

func TestVLCLinkArgs(t *testing.T) {
	cases := []struct {
		name string
		urls []string
		want []string
	}{
		{"one pre-merged stream", []string{"https://cdn/av"}, []string{"https://cdn/av"}},
		{"separate video and audio", []string{"https://cdn/v", "https://cdn/a"}, []string{"https://cdn/v", "--input-slave=https://cdn/a"}},
	}
	for _, c := range cases {
		got := vlcLinkArgs(c.urls)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: vlcLinkArgs(%q) = %q, want %q", c.name, c.urls, got, c.want)
		}
	}
}

// TestPlayLinkChecksForAPlayerFirst: with no player installed there's
// nothing to resolve a link for, so yt-dlp (a download, on first use)
// must not even be asked for.
func TestPlayLinkChecksForAPlayerFirst(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, k := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		t.Setenv(k, "")
	}
	orig := ensureYtdlp
	ensureYtdlp = func(context.Context, func(string)) (string, error) {
		t.Error("yt-dlp was fetched although no player is installed")
		return "", nil
	}
	t.Cleanup(func() { ensureYtdlp = orig })

	if err := PlayLink(context.Background(), "https://example.com/watch?v=xyz", "", nil); err == nil {
		t.Fatal("PlayLink with no player installed succeeded, want an error")
	}
}
