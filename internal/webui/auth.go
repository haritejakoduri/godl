// Package webui is the HTTP side of godl's browser interface: who may
// talk to it (auth.go), the live job feed (events.go), the embedded
// page (static.go) and the in-page player's media plumbing (media.go,
// link.go, player.go).
//
// It deliberately knows nothing about the daemon. The daemon hands it
// an http.Handler for its API and a way to resolve what to play, and
// wraps the result in Guard — so this package can be tested, and
// reasoned about, as plain HTTP.
package webui

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// CookieName carries the local-mode token once the page has loaded.
const CookieName = "godl_web"

// Auth says who may use the interface. The page can start downloads
// into any folder and delete files, so nothing here is optional.
//
// With a Username it's network mode: every request needs HTTP Basic
// credentials. Without one it's local mode: the listener is on loopback
// and requests need Token, which "godl web" puts in the first URL it
// opens and the page then keeps as a cookie. Loopback alone isn't
// enough — any other user on the machine, and any web page the user
// happens to visit, can reach a loopback port.
type Auth struct {
	Token    string
	Username string
	Password string
}

func (a Auth) network() bool { return a.Username != "" }

// Guard wraps the whole interface: security headers, the Host and
// Origin checks, then authentication. open lists path prefixes that
// carry their own credential (the /s/ links other apps play, which
// can't send a cookie) and skip only the last step.
func Guard(a Auth, next http.Handler, open ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; media-src 'self' blob:; img-src 'self' data:; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		// The first URL carries the token; it must never leak to
		// another site through a Referer header.
		h.Set("Referrer-Policy", "no-referrer")

		// A page on another site can make the browser send requests
		// here, and with DNS rebinding can even make them same-origin
		// under a hostname it controls. Refusing every Host that isn't
		// an address (or localhost) closes that: an attacker can't own
		// an IP-literal origin that resolves to this machine.
		if !hostAllowed(r.Host) {
			http.Error(w, "godl: open this page by IP address or localhost", http.StatusForbidden)
			return
		}
		if !originAllowed(r) {
			http.Error(w, "godl: cross-site request refused", http.StatusForbidden)
			return
		}
		for _, prefix := range open {
			if strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}

		if a.network() {
			u, p, ok := r.BasicAuth()
			if !ok || !equal(u, a.Username) || !equal(p, a.Password) {
				h.Set("WWW-Authenticate", `Basic realm="godl"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		if c, err := r.Cookie(CookieName); err == nil && equal(c.Value, a.Token) {
			next.ServeHTTP(w, r)
			return
		}
		// The link "godl web" opens: trade the token for a cookie and
		// drop it from the address bar (and so from history).
		if tok := r.URL.Query().Get("token"); tok != "" && equal(tok, a.Token) && r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{
				Name: CookieName, Value: a.Token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("godl: this page needs the link \"godl web\" prints — run it in a terminal to open the web interface.\n"))
	})
}

func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || net.ParseIP(host) != nil
}

// originAllowed rejects a state-changing request another site made the
// browser send. Browsers attach Origin to every cross-site POST/PUT/
// DELETE; a request without one came from something that isn't a
// browser page (curl, a script) and still has to authenticate.
func originAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}
