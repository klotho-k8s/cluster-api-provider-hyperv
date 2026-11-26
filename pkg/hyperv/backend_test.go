/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package hyperv

import (
	"strings"
	"testing"
)

func TestParseBackend(t *testing.T) {
	cases := []struct {
		in      string
		want    Backend
		wantErr bool
	}{
		{"", DefaultBackend, false},
		{"wmi", BackendWMI, false},
		{"powershell", BackendPowerShell, false},
		{"ps", BackendPowerShell, false},
		{"WMI", "", true},   // case-sensitive by design
		{"bogus", "", true}, // typo must fail, not silently fall back
	}
	for _, c := range cases {
		got, err := ParseBackend(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseBackend(%q): want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseBackend(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseBackend(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDefaultBackendIsWMI pins the default: WMI, with PowerShell as the opt-in
// fallback.
func TestDefaultBackendIsWMI(t *testing.T) {
	if DefaultBackend != BackendWMI {
		t.Fatalf("default backend = %q, want %q", DefaultBackend, BackendWMI)
	}
}

// TestSetBootOrderScript checks the boot-order builder orders the OS disk first
// and quotes the (untrusted) VM name safely.
func TestSetBootOrderScript(t *testing.T) {
	s := setBootOrderScript("my'vm")
	if !strings.Contains(s, "Get-VMHardDiskDrive -VMName 'my''vm'") {
		t.Errorf("boot order script must quote the VM name: %q", s)
	}
	if !strings.Contains(s, "-FirstBootDevice $vmhd") {
		t.Errorf("boot order script must set FirstBootDevice to the hard disk: %q", s)
	}
	if strings.Contains(s, "SecureBoot") {
		t.Errorf("boot order script must not touch Secure Boot (WMI does): %q", s)
	}
}
