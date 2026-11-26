//go:build !windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import "errors"

func cmdInstall([]string) error {
	return errors.New("install is only supported on Windows (the agent manages Hyper-V)")
}

func cmdUninstall([]string) error {
	return errors.New("uninstall is only supported on Windows")
}
