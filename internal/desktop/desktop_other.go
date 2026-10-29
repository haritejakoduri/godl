//go:build !linux && !darwin && !windows

package desktop

import "errors"

// errUnsupported covers the platforms with no application menu to
// register with. Linux, Windows and macOS each have their own file.
var errUnsupported = errors.New("an application menu entry is not supported on this platform")

func install(string) error { return errUnsupported }
func remove() error        { return errUnsupported }

func status() (bool, string, error) { return false, "", errUnsupported }
