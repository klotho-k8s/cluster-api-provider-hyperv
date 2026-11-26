/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package hyperv

import "fmt"

// Backend names a Client implementation. The agent selects one at startup.
type Backend string

const (
	// BackendWMI is the native WMI implementation (root\virtualization\v2,
	// Msvm_* classes) via github.com/microsoft/wmi. It is Windows-only and is
	// the default: it does not spawn a powershell.exe per operation.
	BackendWMI Backend = "wmi"
	// BackendPowerShell is the implementation backed by the Hyper-V PowerShell
	// module. It builds on every OS and is the fallback if WMI has a gap.
	BackendPowerShell Backend = "powershell"
)

// DefaultBackend is the backend used when none is requested: WMI.
const DefaultBackend = BackendWMI

// ParseBackend resolves a user-supplied backend name. An empty string selects
// DefaultBackend. "ps" is accepted as an alias for "powershell". Unknown names
// are an error rather than a silent fallback, so a typo in configuration is
// caught at startup instead of quietly changing which backend runs.
func ParseBackend(s string) (Backend, error) {
	switch s {
	case "":
		return DefaultBackend, nil
	case string(BackendWMI):
		return BackendWMI, nil
	case string(BackendPowerShell), "ps":
		return BackendPowerShell, nil
	default:
		return "", fmt.Errorf("unknown hyperv backend %q (want %q or %q)", s, BackendWMI, BackendPowerShell)
	}
}
