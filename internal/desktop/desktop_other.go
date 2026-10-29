//go:build !linux

package desktop

import "errors"

// errUnsupported: the application-menu entry is an XDG concept. Windows
// gets its Start Menu shortcut from the installer, and macOS has no
// equivalent for a command-line tool that opens a browser.
var errUnsupported = errors.New("a desktop menu entry is only supported on Linux")

func install() error { return errUnsupported }
func remove() error  { return errUnsupported }

func status() (bool, string, error) { return false, "", errUnsupported }
