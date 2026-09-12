//go:build !windows && !linux && !(darwin && cgo)

package tray

func run() error { return ErrUnsupported }
