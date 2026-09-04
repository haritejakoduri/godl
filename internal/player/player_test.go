package player

import (
	"reflect"
	"testing"
)

func TestMpvArgs(t *testing.T) {
	if got := mpvArgs("https://example.com/a.mp4", nil); !reflect.DeepEqual(got, []string{"https://example.com/a.mp4"}) {
		t.Errorf("mpvArgs(no auth) = %v", got)
	}

	got := mpvArgs("https://example.com/a.mp4", &Auth{Username: "alice", Password: "s3cret"})
	if len(got) != 2 || got[1] != "https://example.com/a.mp4" {
		t.Fatalf("mpvArgs(auth) = %v", got)
	}
	want := "--http-header-fields=Authorization: Basic " + "YWxpY2U6czNjcmV0" // base64("alice:s3cret")
	if got[0] != want {
		t.Errorf("mpvArgs(auth)[0] = %q, want %q", got[0], want)
	}
}

func TestVlcArgs(t *testing.T) {
	if got := vlcArgs("https://example.com/a.mp4", nil); !reflect.DeepEqual(got, []string{"https://example.com/a.mp4"}) {
		t.Errorf("vlcArgs(no auth) = %v", got)
	}

	got := vlcArgs("https://example.com/a.mp4", &Auth{Username: "alice", Password: "s3cret"})
	want := []string{"--http-user=alice", "--http-pwd=s3cret", "https://example.com/a.mp4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vlcArgs(auth) = %v, want %v", got, want)
	}
}

func TestAuthEmpty(t *testing.T) {
	cases := []struct {
		name string
		a    *Auth
		want bool
	}{
		{"nil", nil, true},
		{"zero value", &Auth{}, true},
		{"username only", &Auth{Username: "alice"}, false},
		{"both set", &Auth{Username: "alice", Password: "x"}, false},
	}
	for _, c := range cases {
		if got := c.a.empty(); got != c.want {
			t.Errorf("%s: empty() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInstallHints(t *testing.T) {
	if h := installHints(); h == "" {
		t.Error("installHints() returned empty string")
	}
}

func TestPlayWithUnknownBackend(t *testing.T) {
	if err := PlayWith("quicktime", "x", nil); err == nil {
		t.Error("PlayWith(unknown backend) returned no error")
	}
}
