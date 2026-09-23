package tui

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

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/fileserver"
	"godl/internal/paths"
)

type serveStep int

const (
	serveEditing serveStep = iota
	serveRunning
)

type serveFormField int

const (
	serveFieldDir serveFormField = iota
	serveFieldHost
	serveFieldPort
	serveFieldUsername
	serveFieldPassword
	serveFieldAllowWrite
	serveFieldSelfSigned
	serveFieldInsecureNoAuth
	serveFieldStart
)

var serveFormFieldLabels = map[serveFormField]string{
	serveFieldDir:            "Directory",
	serveFieldHost:           "Host",
	serveFieldPort:           "Port",
	serveFieldUsername:       "Username",
	serveFieldPassword:       "Password",
	serveFieldAllowWrite:     "Allow write (uploads/deletes)",
	serveFieldSelfSigned:     "Self-signed HTTPS",
	serveFieldInsecureNoAuth: "Insecure: allow no-auth",
	serveFieldStart:          "Start serving",
}

// serveState is the TUI's "godl serve" overlay, opened with 'S' — the
// same local-folder-over-HTTP(S)/WebDAV server "godl serve" starts
// from the CLI (cmd/serve.go), run in-process for the lifetime of this
// overlay instead of as its own foreground process. Leaving the
// overlay (esc/x while running) always stops the server: there's no
// dashboard indicator for "a server is still running in the
// background", so leaving it running invisibly would be a trap.
type serveState struct {
	step serveStep

	// step 1: the form. Field values are kept as plain strings/bools
	// (not read back from the daemon or persisted anywhere), edited one
	// at a time the same way connFormState is.
	cursor         serveFormField
	editing        bool
	input          textinput.Model
	dir            string
	host           string
	port           string
	username       string
	password       string
	allowWrite     bool
	selfSigned     bool
	insecureNoAuth bool
	err            string

	// step 2: running.
	srv         *http.Server
	root        string // absolute dir being served
	bannerLines []string
}

// newServeForm seeds the form with the same defaults "godl serve"
// itself uses (cmd/serve.go's flag defaults), so an unedited form
// behaves like plain "godl serve <downloads>".
func newServeForm() *serveState {
	dir, err := paths.DownloadsDir()
	if err != nil {
		dir = "."
	}
	return &serveState{
		step: serveEditing,
		dir:  dir,
		host: "0.0.0.0",
		port: "8080",
	}
}

func serveFormValue(s *serveState, field serveFormField) string {
	switch field {
	case serveFieldDir:
		return s.dir
	case serveFieldHost:
		return s.host
	case serveFieldPort:
		return s.port
	case serveFieldUsername:
		return s.username
	case serveFieldPassword:
		return s.password
	default:
		return ""
	}
}

func serveFormSetValue(s *serveState, field serveFormField, v string) {
	switch field {
	case serveFieldDir:
		s.dir = v
	case serveFieldHost:
		s.host = v
	case serveFieldPort:
		s.port = v
	case serveFieldUsername:
		s.username = v
	case serveFieldPassword:
		s.password = v
	}
}

func (m statusModel) updateServe(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.serve.step == serveRunning {
		return m.updateServeRunning(msg)
	}
	return m.updateServeForm(msg)
}

func (m statusModel) updateServeForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.serve

	if s.editing {
		switch msg.String() {
		case "esc":
			s.editing = false
			return m, nil
		case "enter":
			serveFormSetValue(s, s.cursor, s.input.Value())
			s.editing = false
			return m, nil
		default:
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "esc":
		m.serve = nil
		return m, nil
	case "up", "k":
		if s.cursor > serveFieldDir {
			s.cursor--
			s.err = ""
		}
		return m, nil
	case "down", "j":
		if s.cursor < serveFieldStart {
			s.cursor++
			s.err = ""
		}
		return m, nil
	case "enter", " ":
		switch s.cursor {
		case serveFieldAllowWrite:
			s.allowWrite = !s.allowWrite
			return m, nil
		case serveFieldSelfSigned:
			s.selfSigned = !s.selfSigned
			return m, nil
		case serveFieldInsecureNoAuth:
			s.insecureNoAuth = !s.insecureNoAuth
			return m, nil
		case serveFieldStart:
			return m.startServe()
		default:
			ti := textinput.New()
			ti.SetValue(serveFormValue(s, s.cursor))
			ti.CursorEnd()
			ti.Focus()
			ti.CharLimit = 512
			ti.Width = 50
			if s.cursor == serveFieldPassword {
				ti.EchoMode = textinput.EchoPassword
				ti.EchoCharacter = '•'
			}
			s.input = ti
			s.editing = true
			s.err = ""
			return m, nil
		}
	}
	return m, nil
}

// updateServeRunning handles the one meaningful keypress while a
// server is live: stop it. Any key other than the two spelled out in
// the footer is ignored rather than accidentally falling through to
// dashboard bindings.
func (m statusModel) updateServeRunning(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "x":
		return m.stopServe()
	}
	return m, nil
}

// startServe validates the form (mirroring cmd/serve.go's own
// validateServeFlags), starts listening, and — only once the listener
// is confirmed to be up — spins off the actual http.Server.Serve/
// ServeTLS loop in a goroutine that outlives this call. stopServe is
// the only thing that ever tears it back down.
func (m statusModel) startServe() (tea.Model, tea.Cmd) {
	s := m.serve

	dir := strings.TrimSpace(s.dir)
	abs, err := filepath.Abs(dir)
	if err != nil {
		s.err = err.Error()
		return m, nil
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		s.err = fmt.Sprintf("%s is not a directory", dir)
		return m, nil
	}

	host := strings.TrimSpace(s.host)
	if host == "" {
		s.err = "host is required"
		return m, nil
	}
	port, err := strconv.Atoi(strings.TrimSpace(s.port))
	if err != nil || port < 0 || port > 65535 {
		s.err = "port must be a number between 0 and 65535 (0 = let the OS pick a free port)"
		return m, nil
	}

	hasAuth := s.username != "" && s.password != ""
	if !hasAuth && !serveIsLoopbackHost(host) && !s.insecureNoAuth {
		s.err = fmt.Sprintf("refusing to serve on %s without a username/password — anyone who can reach this address could download everything under %s. Set both, use host 127.0.0.1, or turn on \"Insecure: allow no-auth on a reachable host\"", host, dir)
		return m, nil
	}

	handler, err := fileserver.New(fileserver.Config{
		Root:     abs,
		Username: s.username,
		Password: s.password,
		ReadOnly: !s.allowWrite,
	})
	if err != nil {
		s.err = err.Error()
		return m, nil
	}

	listener, actualPort, err := serveListenWithFallback(host, port)
	if err != nil {
		s.err = err.Error()
		return m, nil
	}

	srv := &http.Server{Addr: net.JoinHostPort(host, strconv.Itoa(actualPort)), Handler: handler}
	useTLS := s.selfSigned
	if useTLS {
		cert, err := fileserver.SelfSignedCert([]string{host})
		if err != nil {
			listener.Close()
			s.err = "generating self-signed certificate: " + err.Error()
			return m, nil
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	go func() {
		if useTLS {
			_ = srv.ServeTLS(listener, "", "")
		} else {
			_ = srv.Serve(listener)
		}
	}()

	s.srv = srv
	s.root = abs
	s.bannerLines = serveBannerLines(abs, host, actualPort, useTLS, s.allowWrite, s.username, hasAuth)
	s.step = serveRunning
	return m, nil
}

// stopServe shuts the running server down immediately — no grace
// period, this is a casual sharing session, not a production
// deployment — and closes the whole overlay, back to the dashboard.
// http.Server.Close also closes the listener it was Serve'd with, so
// there's nothing else to release.
func (m statusModel) stopServe() (tea.Model, tea.Cmd) {
	s := m.serve
	if s.srv != nil {
		_ = s.srv.Close()
	}
	m.serve = nil
	m.statusMsg = fmt.Sprintf("stopped serving %s", s.root)
	return m, nil
}

// serveIsLoopbackHost mirrors cmd/serve.go's own isLoopbackHost —
// duplicated rather than imported since tui can't depend on cmd (see
// boundary_test.go).
func serveIsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// serveListenMaxAttempts mirrors cmd/serve.go's maxPortFallbackAttempts.
const serveListenMaxAttempts = 20

// serveListenWithFallback mirrors cmd/serve.go's listenWithFallback:
// binds host:port, falling back to the next few ports if the exact one
// requested is already taken. Unlike port 0 itself (which asks the OS
// for any free port), the fallback only tries specific, predictable
// port numbers — so the "actual port" it returns is read back from the
// listener rather than assumed, correctly reporting whichever real
// port got bound either way.
func serveListenWithFallback(host string, port int) (net.Listener, int, error) {
	var lastErr error
	for i := 0; i < serveListenMaxAttempts; i++ {
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
	return nil, 0, fmt.Errorf("no free port found starting at %d after %d attempts: %w", port, serveListenMaxAttempts, lastErr)
}

// serveIsUnspecifiedHost mirrors cmd/serve.go's isUnspecifiedHost.
func serveIsUnspecifiedHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// serveReachableIPs mirrors cmd/serve.go's reachableIPs: this
// machine's own non-loopback IPv4 addresses, for expanding "every
// interface" into addresses another device can actually use.
func serveReachableIPs() []string {
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

// serveBannerAddrs mirrors cmd/serve.go's bannerAddrs.
func serveBannerAddrs(host string) []string {
	if !serveIsUnspecifiedHost(host) {
		return []string{host}
	}
	return append([]string{"127.0.0.1"}, serveReachableIPs()...)
}

// serveExampleConnectAddr mirrors cmd/serve.go's exampleConnectAddr.
func serveExampleConnectAddr(addrs []string) string {
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip == nil || !ip.IsLoopback() {
			return a
		}
	}
	return addrs[0]
}

// serveBannerLines mirrors cmd/serve.go's printServeBanner, as lines
// for the overlay to render instead of printing to stdout.
func serveBannerLines(dir, host string, port int, useTLS, allowWrite bool, username string, hasAuth bool) []string {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Serving %s", dir))

	addrs := serveBannerAddrs(host)
	if serveIsUnspecifiedHost(host) {
		lines = append(lines, "Reachable at (whichever address the other device can actually reach):")
		for _, ip := range addrs {
			hostPort := net.JoinHostPort(ip, strconv.Itoa(port))
			lines = append(lines, fmt.Sprintf("  %s://%s/   (WebDAV: %s://%s/dav/)", scheme, hostPort, scheme, hostPort))
		}
	} else {
		hostPort := net.JoinHostPort(host, strconv.Itoa(port))
		lines = append(lines, fmt.Sprintf("Browse:  %s://%s/", scheme, hostPort))
		lines = append(lines, fmt.Sprintf("WebDAV:  %s://%s/dav/", scheme, hostPort))
	}
	if allowWrite {
		lines = append(lines, "Read-write — uploads and deletes over WebDAV are allowed.")
	} else {
		lines = append(lines, "Read-only.")
	}
	if hasAuth {
		lines = append(lines, fmt.Sprintf("Auth: username %q required.", username))
	} else {
		lines = append(lines, "No authentication — anyone who can reach this address can download everything above.")
	}
	if useTLS {
		lines = append(lines, "Self-signed certificate — browsers and WebDAV clients will warn until you trust it.")
	}
	exampleAddr := net.JoinHostPort(serveExampleConnectAddr(addrs), strconv.Itoa(port))
	if hasAuth {
		lines = append(lines, fmt.Sprintf("Add as a connection: godl connection add <name> --url %s://%s/dav/ --username %s", scheme, exampleAddr, username))
	} else {
		lines = append(lines, fmt.Sprintf("Add as a connection: godl connection add <name> --url %s://%s/dav/", scheme, exampleAddr))
	}
	return lines
}

func (m statusModel) viewServe() string {
	s := m.serve
	if s.step == serveRunning {
		var b strings.Builder
		b.WriteString(m.wrapped(statStyle).Render("Serving — Ctrl+C-free, stop it from here:"))
		b.WriteString("\n")
		for _, line := range s.bannerLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString(m.helpView("esc/x stop serving and return"))
		return b.String()
	}

	var b strings.Builder
	b.WriteString(m.wrapped(statStyle).Render("Serve a local folder over HTTP(S)/WebDAV:"))
	b.WriteString("\n")

	fields := []serveFormField{
		serveFieldDir, serveFieldHost, serveFieldPort, serveFieldUsername,
		serveFieldPassword, serveFieldAllowWrite, serveFieldSelfSigned, serveFieldInsecureNoAuth, serveFieldStart,
	}
	for _, field := range fields {
		cursor := "  "
		if field == s.cursor {
			cursor = "> "
		}
		var value string
		switch field {
		case serveFieldAllowWrite:
			value = boolLabel(s.allowWrite)
		case serveFieldSelfSigned:
			value = boolLabel(s.selfSigned)
		case serveFieldInsecureNoAuth:
			value = boolLabel(s.insecureNoAuth)
		case serveFieldStart:
			value = ""
		case serveFieldPassword:
			if s.password != "" {
				value = strings.Repeat("•", len(s.password))
			}
		default:
			value = serveFormValue(s, field)
		}
		if field == s.cursor && s.editing {
			value = s.input.View()
		}
		fmt.Fprintf(&b, "%s%-30s %s\n", cursor, serveFormFieldLabels[field], value)
	}

	if s.err != "" {
		b.WriteString(m.wrapped(errStyle).Render("error: " + s.err))
		b.WriteString("\n")
	} else {
		b.WriteString(m.helpView("leaving username/password blank requires host 127.0.0.1, or turn on Insecure below"))
	}
	if s.editing {
		b.WriteString(m.helpView("enter save field  esc cancel edit"))
	} else {
		b.WriteString(m.helpView("↑/↓ select  enter edit/toggle/start  esc cancel"))
	}
	return b.String()
}
