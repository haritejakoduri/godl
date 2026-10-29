package desktop

import (
	"fmt"
	"strings"
)

// The text each platform writes out, kept here rather than in the
// platform files so it is compiled and tested everywhere. Quoting is
// the part most likely to be wrong and the part that cannot be
// exercised on the machine that runs it: a path with a space or an
// apostrophe is ordinary (a home directory named O'Brien is enough),
// and getting it wrong produces a shortcut that silently does nothing.

// bundleID is the macOS app's reverse-DNS identifier. Launch Services
// keys its records on this, so it must stay stable across versions or
// macOS treats an upgrade as a different application.
const bundleID = "com.github.haritejakoduri.godl"

// infoPlistFor builds the macOS bundle's Info.plist. These keys are the
// minimum macOS needs to treat a directory as an application: a type,
// an identifier, the executable to run, and the icon to show.
//
// LSUIElement is deliberately absent. It would stop the brief Dock
// icon this launcher causes, but background-only apps are not reliably
// listed in Launchpad — and being listed is the entire point.
func infoPlistFor(version string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>godl</string>
	<key>CFBundleDisplayName</key><string>godl</string>
	<key>CFBundleIdentifier</key><string>%s</string>
	<key>CFBundleVersion</key><string>%[2]s</string>
	<key>CFBundleShortVersionString</key><string>%[2]s</string>
	<key>CFBundleExecutable</key><string>godl</string>
	<key>CFBundleIconFile</key><string>godl</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`, bundleID, version)
}

// launcherScript is the macOS bundle's executable. A shell script
// rather than a copy of the binary: copying would strand the bundle on
// an old godl after an update, and a symlink inside a bundle confuses
// Gatekeeper. This way the bundle is a pointer that always runs
// whatever godl sits at that path.
func launcherScript(exe string) string {
	return "#!/bin/sh\nexec " + shellQuote(exe) + " web\n"
}

// shellQuote wraps s for /bin/sh. Single quotes take everything
// literally, so the only character needing work is a single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shortcutScript builds the PowerShell that creates the Windows Start
// Menu shortcut.
//
// A .lnk is a binary structure (MS-SHLLINK), and hand-rolling it would
// mean shipping a serializer that cannot be exercised anywhere but
// Windows. WScript.Shell is the API Windows itself provides for this
// and has been there since forever, so the shortcut goes through that
// instead. It runs via -Command, an inline script, which PowerShell's
// execution policy does not restrict — that governs .ps1 files — so
// this works on a default machine.
//
// The icon comes from the exe's own resource, which go-winres embeds at
// build time (see scripts/build-windows-installer.sh), so no separate
// icon file has to be installed or kept in step.
func shortcutScript(link, exe, workDir string) string {
	return strings.Join([]string{
		"$s = (New-Object -ComObject WScript.Shell).CreateShortcut(" + psQuote(link) + ")",
		"$s.TargetPath = " + psQuote(exe),
		"$s.Arguments = 'web'",
		"$s.IconLocation = " + psQuote(exe+",0"),
		"$s.Description = 'Download files, videos and torrents in the background'",
		"$s.WorkingDirectory = " + psQuote(workDir),
		"$s.Save()",
	}, "; ")
}

// psQuote wraps s in a PowerShell single-quoted string, where the only
// character with meaning is the quote itself, doubled to escape it.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
