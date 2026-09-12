//go:build !windows && !linux && !darwin

package autostart

import "errors"

var errUnsupported = errors.New("autostart is not supported on this platform")

func install() error   { return errUnsupported }
func uninstall() error { return errUnsupported }

func status() (bool, string, error) { return false, "", errUnsupported }
