//go:build windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"errors"
	"os/exec"
)

func asExit(err error, target **exec.ExitError) bool { return errors.As(err, target) }
