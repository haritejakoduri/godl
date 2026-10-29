//go:build android || (!windows && !linux && !(darwin && cgo))

// android is named first because it also satisfies "linux"; see the
// build tag in app_supported.go.
package tray

func run(bool) error { return ErrUnsupported }
