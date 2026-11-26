//go:build !windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

// runAgent runs the agent in the foreground on non-Windows platforms. The
// Windows service control integration lives in service_windows.go.
func runAgent(cfg serveConfig) error {
	return runForeground(cfg)
}
