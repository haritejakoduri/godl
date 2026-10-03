package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
}

func do(h http.Handler, method, target string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:8787"
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGuardLocalToken(t *testing.T) {
	tok := strings.Repeat("a", 32)
	h := Guard(Auth{Token: tok}, okHandler(), "/s/")

	if w := do(h, "GET", "/api/state", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no credentials: got %d, want 401", w.Code)
	}
	if w := do(h, "GET", "/api/state", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: "wrong"})
	}); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong cookie: got %d, want 401", w.Code)
	}
	if w := do(h, "GET", "/api/state", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	}); w.Code != http.StatusOK {
		t.Errorf("right cookie: got %d, want 200", w.Code)
	}

	// The link "godl web" opens trades the token for a cookie and
	// redirects to the same page without it.
	w := do(h, "GET", "/?token="+tok, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("token link: got %d -> %q, want 303 -> /", w.Code, w.Header().Get("Location"))
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != tok || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Errorf("token link set cookie %+v, want one HttpOnly SameSite=Strict cookie carrying the token", c)
	}
	if w := do(h, "GET", "/?token=nope", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token link: got %d, want 401", w.Code)
	}
	// Open prefixes carry their own credential.
	if w := do(h, "GET", "/s/abc/file.mkv", nil); w.Code != http.StatusOK {
		t.Errorf("open prefix: got %d, want 200", w.Code)
	}
}

func TestGuardNetworkBasicAuth(t *testing.T) {
	h := Guard(Auth{Username: "alice", Password: "pw"}, okHandler())
	if w := do(h, "GET", "/", nil); w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no credentials: got %d (%q), want 401 with a Basic challenge", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	if w := do(h, "GET", "/", func(r *http.Request) { r.SetBasicAuth("alice", "nope") }); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: got %d, want 401", w.Code)
	}
	if w := do(h, "GET", "/", func(r *http.Request) { r.SetBasicAuth("alice", "pw") }); w.Code != http.StatusOK {
		t.Errorf("right password: got %d, want 200", w.Code)
	}
}

func TestGuardRejectsForeignHostAndOrigin(t *testing.T) {
	h := Guard(Auth{Username: "alice", Password: "pw"}, okHandler(), "/s/")
	auth := func(r *http.Request) { r.SetBasicAuth("alice", "pw") }

	// DNS rebinding: a hostname the attacker controls, pointed here.
	if w := do(h, "GET", "/", func(r *http.Request) { auth(r); r.Host = "evil.example:8787" }); w.Code != http.StatusForbidden {
		t.Errorf("hostname Host: got %d, want 403", w.Code)
	}
	// Even an open link must not answer under a foreign hostname.
	if w := do(h, "GET", "/s/x/y", func(r *http.Request) { r.Host = "evil.example" }); w.Code != http.StatusForbidden {
		t.Errorf("hostname Host on an open prefix: got %d, want 403", w.Code)
	}
	for _, host := range []string{"localhost:8787", "192.168.1.5:8787", "[::1]:8787"} {
		if w := do(h, "GET", "/", func(r *http.Request) { auth(r); r.Host = host }); w.Code != http.StatusOK {
			t.Errorf("Host %s: got %d, want 200", host, w.Code)
		}
	}
	// CSRF: another site's page posting here.
	if w := do(h, "POST", "/api/jobs", func(r *http.Request) { auth(r); r.Header.Set("Origin", "http://evil.example") }); w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: got %d, want 403", w.Code)
	}
	if w := do(h, "POST", "/api/jobs", func(r *http.Request) { auth(r); r.Header.Set("Origin", "http://127.0.0.1:8787") }); w.Code != http.StatusOK {
		t.Errorf("same-origin POST: got %d, want 200", w.Code)
	}
	if w := do(h, "GET", "/", auth); w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("security headers missing")
	}
}
