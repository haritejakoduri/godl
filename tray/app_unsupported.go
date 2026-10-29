//go:build !windows && !linux && !(darwin && cgo)

package tray

func run(bool) error { return ErrUnsupported }
