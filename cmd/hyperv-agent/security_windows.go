//go:build windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// secureDACL restricts a file or directory to LocalSystem and the local
// Administrators group with full control, and blocks inheritance from the parent
// so the broad ACLs higher in the tree (for example the Users-readable
// ProgramData or the exe directory) do not apply. Container and object ACEs are
// marked inheritable so files created inside a secured directory (the token
// file, the TLS key) inherit the same protection.
//
// SDDL: D:P => protected DACL (no inherited ACEs); (A;OICI;FA;;;SY) full control
// for SYSTEM, inheritable; (A;OICI;FA;;;BA) full control for Administrators,
// inheritable.
func secureDACL(path string) error {
	const sddl = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read dacl: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("set dacl on %s: %w", path, err)
	}
	return nil
}
