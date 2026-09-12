//go:build windows || linux || (darwin && cgo)

// The tray needs a native toolkit on macOS (Cocoa's NSStatusItem, via
// cgo). Windows reaches the notification area through plain syscalls
// and Linux through the StatusNotifierItem D-Bus interface, so both
// build without cgo and cross-compile like the rest of godl. The
// darwin && cgo tag is what keeps a CGO_ENABLED=0 macOS build
// compiling — it gets the stub in app_unsupported.go instead.

package tray

import (
	"context"
	_ "embed"
	"log"
	"runtime"
	"time"

	"fyne.io/systray"

	"godl/internal/daemon"
)

var (
	//go:embed icon/godl.ico
	iconICO []byte
	//go:embed icon/godl.png
	iconPNG []byte
	//go:embed icon/godl-template.png
	iconTemplatePNG []byte
)

func run() error {
	// Checked before systray.Run, which otherwise blocks forever
	// waiting on a tray host that isn't there.
	if err := checkSession(); err != nil {
		return err
	}
	systray.Run(onReady, func() {})
	return nil
}

// applyIcon picks the form each platform wants: Windows needs an ICO,
// and macOS a template image it can recolor for a light or dark menu
// bar, falling back to the colored one where templates don't apply.
func applyIcon() {
	switch runtime.GOOS {
	case "windows":
		systray.SetIcon(iconICO)
	case "darwin":
		systray.SetTemplateIcon(iconTemplatePNG, iconPNG)
	default:
		systray.SetIcon(iconPNG)
	}
}

func onReady() {
	applyIcon()
	systray.SetTooltip("godl")

	m := newMenu()
	ctx, cancel := context.WithCancel(context.Background())
	go m.watch(ctx)
	go m.handle(ctx, cancel)
}

// menu is godl's tray menu. The header is a disabled item rather than
// the icon's title: a title is rendered inconsistently across desktops
// (and not at all on Windows), while a menu entry always shows.
type menu struct {
	header    *systray.MenuItem
	dashboard *systray.MenuItem
	pause     *systray.MenuItem
	resume    *systray.MenuItem
	start     *systray.MenuItem
	quit      *systray.MenuItem
	hide      *systray.MenuItem
}

func newMenu() *menu {
	m := &menu{header: systray.AddMenuItem("Connecting…", "")}
	m.header.Disable()
	systray.AddSeparator()
	m.dashboard = systray.AddMenuItem("Open status dashboard", "Run godl status in a terminal")
	m.pause = systray.AddMenuItem("Pause all", "Pause every active and queued job")
	m.resume = systray.AddMenuItem("Resume all", "Resume every paused job")
	systray.AddSeparator()
	m.start = systray.AddMenuItem("Start daemon", "Start the godl background daemon")
	m.quit = systray.AddMenuItem("Quit godl daemon", "Stop the daemon, then close this icon")
	m.hide = systray.AddMenuItem("Hide this icon", "Close this icon and leave the daemon running")
	return m
}

// apply puts one summary on screen. Items that cannot do anything right
// now are disabled rather than hidden, so the menu keeps a stable shape
// instead of jumping around as jobs start and finish.
func (m *menu) apply(s summary) {
	m.header.SetTitle(s.line())
	systray.SetTooltip(s.tooltip())

	setEnabled(m.start, !s.daemonUp)
	setEnabled(m.dashboard, s.daemonUp)
	setEnabled(m.pause, s.daemonUp && s.active+s.queued > 0)
	setEnabled(m.resume, s.daemonUp && s.paused > 0)
	setEnabled(m.quit, s.daemonUp)
}

func setEnabled(item *systray.MenuItem, on bool) {
	if on {
		item.Enable()
	} else {
		item.Disable()
	}
}

// watch keeps the menu in step with the daemon, which can come and go
// underneath the tray — it may not be running when the tray starts at
// login, and Quit stops it without stopping this icon's siblings. So
// this reconnects rather than subscribing once and giving up.
func (m *menu) watch(ctx context.Context) {
	for ctx.Err() == nil {
		if !daemon.Running() {
			m.apply(summary{})
			sleep(ctx, 2*time.Second)
			continue
		}
		subCtx, cancel := context.WithCancel(ctx)
		snapCh, _ := daemon.Subscribe(subCtx)
		for jobs := range snapCh {
			m.apply(summarize(jobs))
		}
		cancel()
		sleep(ctx, time.Second)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// handle runs the click loop until the icon is dismissed. Actions that
// touch the daemon can block on I/O, so each runs in its own goroutine
// rather than wedging the menu while it waits.
func (m *menu) handle(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.dashboard.ClickedCh:
			go report("opening the dashboard", openDashboard)
		case <-m.pause.ClickedCh:
			go report("pausing all jobs", pauseAll)
		case <-m.resume.ClickedCh:
			go report("resuming all jobs", resumeAll)
		case <-m.start.ClickedCh:
			go report("starting the daemon", daemon.EnsureRunning)
		case <-m.quit.ClickedCh:
			report("stopping the daemon", func() error { _, err := daemon.Stop(); return err })
			systray.Quit()
			return
		case <-m.hide.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// report logs what went wrong and carries on. A tray has nowhere good
// to show an error — a modal dialog from the notification area is worse
// than the failure — and the menu reflects the real state on the next
// snapshot either way.
func report(what string, fn func() error) {
	if err := fn(); err != nil {
		log.Printf("godl tray: %s: %v", what, err)
	}
}
