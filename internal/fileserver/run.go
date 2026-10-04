package fileserver

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// MaxPortFallbackAttempts bounds how many consecutive ports
// ListenWithFallback will try past the one asked for — enough to get
// past a handful of stray listeners without silently wandering off to
// a port far away from what was requested.
const MaxPortFallbackAttempts = 20

// ListenWithFallback binds host:port, and if that exact port is already
// taken, tries host:port+1, host:port+2, ... up to maxAttempts tries
// (callers pass MaxPortFallbackAttempts) before giving up. Any bind failure that
// isn't specifically "address already in use" (permission denied on a
// privileged port, an invalid host, ...) is returned immediately —
// those aren't going to be fixed by picking a different port number.
// Port 0 asks the OS for any free port and never falls back. The port
// returned is read back from the listener, so it's right either way.
func ListenWithFallback(host string, port, maxAttempts int) (net.Listener, int, error) {
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		p := port
		if port != 0 {
			p = port + i
		}
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return l, l.Addr().(*net.TCPAddr).Port, nil
		}
		lastErr = err
		if port == 0 || !strings.Contains(err.Error(), "address already in use") {
			return nil, 0, err
		}
	}
	return nil, 0, fmt.Errorf("no free port found starting at %d after %d attempts: %w", port, maxAttempts, lastErr)
}

// IsLoopbackHost reports whether host only accepts connections from
// this machine.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsUnspecifiedHost reports whether host means "every interface"
// ("0.0.0.0" or "::") rather than one specific address — that's not
// itself something a client can connect to, so anything shown to a
// user needs the machine's real IPs instead.
func IsUnspecifiedHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// ReachableIPs lists this machine's own non-loopback IPv4 addresses —
// what listening on "every interface" actually resolves to from
// another device's point of view. IPv6 is skipped: a bare IPv6 literal
// needs bracket syntax in a URL ("http://[fd00::1]:8080/"), which is
// more likely to confuse in a quick-start message than help.
func ReachableIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var ips []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	sort.Strings(ips)
	return ips
}

// BannerAddrs resolves what address(es) to show for host: a specific
// host is used as-is; "every interface" is expanded to this machine's
// own LAN IPs, always alongside "127.0.0.1" — binding every interface
// really does include loopback, so it's a usable address too.
func BannerAddrs(host string) []string {
	if !IsUnspecifiedHost(host) {
		return []string{host}
	}
	return append([]string{"127.0.0.1"}, ReachableIPs()...)
}

// RunConfig is everything needed to start sharing a directory: Config
// plus where to listen.
type RunConfig struct {
	Config
	Host string
	Port int
	// SelfSigned serves https:// with a generated, untrusted certificate.
	SelfSigned bool
	// InsecureNoAuth allows a non-loopback Host without credentials.
	InsecureNoAuth bool
}

// Running is a started share. Close stops it.
type Running struct {
	Root       string
	Host       string
	Port       int // the port actually bound, after any fallback
	TLS        bool
	AllowWrite bool
	Username   string

	srv *http.Server
}

// URLs lists the addresses a client can reach this share at.
func (r *Running) URLs() []string {
	scheme := "http"
	if r.TLS {
		scheme = "https"
	}
	var out []string
	for _, a := range BannerAddrs(r.Host) {
		out = append(out, fmt.Sprintf("%s://%s/", scheme, net.JoinHostPort(a, strconv.Itoa(r.Port))))
	}
	return out
}

// Close shuts the server down immediately — no grace period, this is a
// casual sharing session — and releases its listener.
func (r *Running) Close() error { return r.srv.Close() }

// Start validates cfg and begins serving in the background. It applies
// the one policy every front end shares: a share reachable from other
// machines needs a username and password unless explicitly overridden,
// since anyone who can reach it could otherwise download everything
// under Root.
func Start(cfg RunConfig) (*Running, error) {
	abs, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", cfg.Root)
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("port must be between 0 and 65535")
	}
	hasAuth := cfg.Username != "" && cfg.Password != ""
	if !hasAuth && !IsLoopbackHost(cfg.Host) && !cfg.InsecureNoAuth {
		return nil, fmt.Errorf("refusing to share %s on %s without a username and password — anyone who can reach this address could download everything under it", abs, cfg.Host)
	}

	cfg.Root = abs
	handler, err := New(cfg.Config)
	if err != nil {
		return nil, err
	}
	listener, port, err := ListenWithFallback(cfg.Host, cfg.Port, MaxPortFallbackAttempts)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: handler}
	if cfg.SelfSigned {
		cert, err := SelfSignedCert([]string{cfg.Host})
		if err != nil {
			listener.Close()
			return nil, fmt.Errorf("generating self-signed certificate: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}
	go func() {
		if cfg.SelfSigned {
			_ = srv.ServeTLS(listener, "", "")
		} else {
			_ = srv.Serve(listener)
		}
	}()
	return &Running{
		Root: abs, Host: cfg.Host, Port: port, TLS: cfg.SelfSigned,
		AllowWrite: !cfg.ReadOnly, Username: cfg.Username, srv: srv,
	}, nil
}
