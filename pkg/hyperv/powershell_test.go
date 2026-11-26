/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package hyperv

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureRunner records the last script it was asked to run and returns canned
// output, so command building and output parsing are tested without PowerShell.
type captureRunner struct {
	script string
	out    []byte
	err    error
}

func (c *captureRunner) run(_ context.Context, script string) ([]byte, error) {
	c.script = script
	return c.out, c.err
}

// scriptRunner records every script it runs (so tests can assert on all of the
// steps of a multi-call method) and returns queued outputs in order. A shorter
// outs slice reuses the last entry for any further calls.
type scriptRunner struct {
	scripts []string
	outs    [][]byte
	errs    []error
}

func (r *scriptRunner) run(_ context.Context, script string) ([]byte, error) {
	i := len(r.scripts)
	r.scripts = append(r.scripts, script)
	var out []byte
	if len(r.outs) > 0 {
		if i < len(r.outs) {
			out = r.outs[i]
		} else {
			out = r.outs[len(r.outs)-1]
		}
	}
	var err error
	if i < len(r.errs) {
		err = r.errs[i]
	}
	return out, err
}

func TestPSQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"it's", "'it''s'"},
		{"'; Remove-VM -Name *; '", "'''; Remove-VM -Name *; '''"},
		{``, "''"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, psQuote(tt.in))
	}
}

func TestInjectionSafety(t *testing.T) {
	// A malicious VM name must stay inside a single-quoted literal and not add a
	// second statement. After quoting there must be no unescaped quote that would
	// terminate the literal early.
	evil := "x'; Remove-VM -Name '*"
	script := getVMScript(evil)
	// The evil string must appear only as a properly escaped single-quoted
	// literal (every embedded quote doubled), so it is inert data.
	assert.Contains(t, script, psQuote(evil))
	// If escaping had failed, the literal would be closed right after "x",
	// turning the rest into executable statements. That exact breakout sequence
	// must not be present.
	assert.NotContains(t, script, "-Name 'x'; Remove-VM")
}

func TestScriptBuilders(t *testing.T) {
	t.Run("getVM", func(t *testing.T) {
		s := getVMScript("node0")
		assert.Contains(t, s, "Get-VM -Name 'node0'")
		assert.Contains(t, s, "ConvertTo-Json")
	})

	t.Run("exists", func(t *testing.T) {
		s := existsScript("node0")
		assert.Contains(t, s, "Get-VM -Name 'node0' -ErrorAction SilentlyContinue")
		assert.Contains(t, s, "'true'")
	})

	t.Run("createVM", func(t *testing.T) {
		s := createVMScript(CreateVMInput{
			Name:               "node0",
			Generation:         2,
			MemoryStartupBytes: 2147483648,
			CPUCount:           4,
			SwitchName:         "External",
			VHDPath:            `C:\vms\node0.vhdx`,
			Notes:              "owned",
		})
		assert.Contains(t, s, "New-VM -Name 'node0'")
		assert.Contains(t, s, "-Generation 2")
		assert.Contains(t, s, "-MemoryStartupBytes 2147483648")
		assert.Contains(t, s, `-VHDPath 'C:\vms\node0.vhdx'`)
		assert.Contains(t, s, "-SwitchName 'External'")
		assert.Contains(t, s, "Set-VMProcessor -VMName 'node0' -Count 4")
		assert.Contains(t, s, "-DynamicMemoryEnabled $false")
		assert.Contains(t, s, "Set-VM -Name 'node0' -Notes 'owned'")
	})

	t.Run("stopVM force vs graceful", func(t *testing.T) {
		assert.Contains(t, stopVMScript("n", true), "-TurnOff -Force")
		assert.NotContains(t, stopVMScript("n", false), "-TurnOff")
	})

	t.Run("firmware secure boot on", func(t *testing.T) {
		s := setFirmwareScript(FirmwareInput{Name: "node0", EnableSecureBoot: true})
		assert.Contains(t, s, "-EnableSecureBoot On")
		assert.Contains(t, s, "MicrosoftUEFICertificateAuthority")
		assert.Contains(t, s, "-FirstBootDevice $vmhd")
	})

	t.Run("firmware secure boot off", func(t *testing.T) {
		s := setFirmwareScript(FirmwareInput{Name: "node0", EnableSecureBoot: false})
		assert.Contains(t, s, "-EnableSecureBoot Off")
		assert.NotContains(t, s, "MicrosoftUEFICertificateAuthority")
	})

	t.Run("attachISO uses DVD drive", func(t *testing.T) {
		s := attachISOScript("node0", `C:\cidata\node0.iso`)
		assert.Contains(t, s, "Add-VMDvdDrive -VMName 'node0'")
		assert.Contains(t, s, `-Path 'C:\cidata\node0.iso'`)
	})

	t.Run("vlan", func(t *testing.T) {
		assert.Contains(t, setVLANScript("n", 0), "-Untagged")
		assert.Contains(t, setVLANScript("n", 42), "-Access -VlanId 42")
	})
}

func TestCopyBaseDiskScript(t *testing.T) {
	s := copyBaseDiskScript(`C:\HyperV\base\debian.vhdx`, `C:\HyperV\vms\node0.vhdx`)
	// It creates the destination directory and copies the base to the per-VM disk.
	assert.Contains(t, s, `$dst = 'C:\HyperV\vms\node0.vhdx'`)
	assert.Contains(t, s, "New-Item -ItemType Directory")
	assert.Contains(t, s, `Copy-Item -LiteralPath 'C:\HyperV\base\debian.vhdx' -Destination $dst -Force`)
}

func TestRemoveVMScript(t *testing.T) {
	s := removeVMScript("node0", `C:\HyperV\vms\node0.vhdx`, `C:\data\cidata\node0-cidata.iso`)
	assert.Contains(t, s, "Remove-VM -Name 'node0' -Force")
	// Both the per-VM disk and the cidata ISO are removed, best-effort.
	assert.Contains(t, s, `Remove-Item -LiteralPath 'C:\HyperV\vms\node0.vhdx' -Force -ErrorAction SilentlyContinue`)
	assert.Contains(t, s, `Remove-Item -LiteralPath 'C:\data\cidata\node0-cidata.iso' -Force -ErrorAction SilentlyContinue`)
}

func TestDeleteVMScript(t *testing.T) {
	s := deleteVMScript("node0", `C:\HyperV\vms\node0.vhdx`, `C:\data\cidata\node0-cidata.iso`)
	// It is one atomic script: stop only if not already Off, then destroy, then
	// best-effort remove both files. Guarding the stop on state keeps it
	// idempotent for an already-stopped VM.
	assert.Contains(t, s, "Get-VM -Name 'node0' -ErrorAction SilentlyContinue")
	assert.Contains(t, s, "if ($vm.State -ne 'Off') { Stop-VM -Name 'node0' -TurnOff -Force }")
	assert.Contains(t, s, "Remove-VM -Name 'node0' -Force")
	assert.Contains(t, s, `Remove-Item -LiteralPath 'C:\HyperV\vms\node0.vhdx' -Force -ErrorAction SilentlyContinue`)
	assert.Contains(t, s, `Remove-Item -LiteralPath 'C:\data\cidata\node0-cidata.iso' -Force -ErrorAction SilentlyContinue`)
}

func TestDeleteVMOneCall(t *testing.T) {
	// DeleteVM is a single round-trip to the host: one script that does the whole
	// teardown, so the controller never needs a second call.
	r := &scriptRunner{}
	c := &PowerShellClient{runner: r, vmsDir: `C:\HyperV\vms`, isoDir: `C:\data\cidata`}
	require.NoError(t, c.DeleteVM(context.Background(), "node0"))
	require.Len(t, r.scripts, 1, "delete must be a single server-side operation")
	assert.Contains(t, r.scripts[0], "Stop-VM -Name 'node0' -TurnOff -Force")
	assert.Contains(t, r.scripts[0], "Remove-VM -Name 'node0' -Force")
	assert.Contains(t, r.scripts[0], `'C:\HyperV\vms\node0.vhdx'`)
	assert.Contains(t, r.scripts[0], `'C:\data\cidata\node0-cidata.iso'`)
}

func TestGetNeighborScript(t *testing.T) {
	s := getNeighborScript("node0")
	assert.Contains(t, s, "Get-VMNetworkAdapter -VMName 'node0'")
	assert.Contains(t, s, "MacAddress")
	assert.Contains(t, s, "Get-NetNeighbor -InterfaceAlias 'vEthernet (Default Switch)'")
	assert.Contains(t, s, "ConvertTo-Json")
}

func TestCreateVMCopiesBaseThenCreates(t *testing.T) {
	// The create script re-reads via GetVM, so the runner yields VM JSON for the
	// later calls; the copy step ignores output.
	vmJSON := []byte(`{"Name":"node0","Id":"guid-1","State":"Off","Generation":2}`)
	r := &scriptRunner{outs: [][]byte{nil, nil, vmJSON}}
	c := &PowerShellClient{runner: r, vmsDir: `C:\HyperV\vms`, isoDir: `C:\data\cidata`}
	vm, err := c.CreateVM(context.Background(), CreateVMInput{
		Name:       "node0",
		Generation: 2,
		SwitchName: "External",
		VHDPath:    `C:\HyperV\base\debian.vhdx`,
	})
	require.NoError(t, err)
	assert.Equal(t, "guid-1", vm.ID)
	require.GreaterOrEqual(t, len(r.scripts), 2)
	// First call copies the base image to the per-VM disk.
	assert.Contains(t, r.scripts[0], `Copy-Item -LiteralPath 'C:\HyperV\base\debian.vhdx'`)
	assert.Contains(t, r.scripts[0], `node0.vhdx`)
	// The base image is never passed to New-VM; the per-VM copy is attached.
	assert.Contains(t, r.scripts[1], "New-VM -Name 'node0'")
	assert.Contains(t, r.scripts[1], `-VHDPath 'C:\HyperV\vms\node0.vhdx'`)
	assert.NotContains(t, r.scripts[1], `-VHDPath 'C:\HyperV\base\debian.vhdx'`)
}

func TestCreateVMCopyFailureStops(t *testing.T) {
	// If copying the base fails, New-VM must not run (the base is never touched).
	r := &scriptRunner{errs: []error{errors.New("copy denied")}}
	c := &PowerShellClient{runner: r, vmsDir: `C:\HyperV\vms`}
	_, err := c.CreateVM(context.Background(), CreateVMInput{Name: "node0", VHDPath: `C:\base.vhdx`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "copy base image")
	assert.Len(t, r.scripts, 1, "New-VM should not run after a failed copy")
}

func TestRemoveVMCleansUp(t *testing.T) {
	r := &scriptRunner{}
	c := &PowerShellClient{runner: r, vmsDir: `C:\HyperV\vms`, isoDir: `C:\data\cidata`}
	require.NoError(t, c.RemoveVM(context.Background(), "node0"))
	require.Len(t, r.scripts, 1)
	assert.Contains(t, r.scripts[0], "Remove-VM -Name 'node0' -Force")
	assert.Contains(t, r.scripts[0], `'C:\HyperV\vms\node0.vhdx'`)
	assert.Contains(t, r.scripts[0], `'C:\data\cidata\node0-cidata.iso'`)
}

func TestNormalizeMAC(t *testing.T) {
	assert.Equal(t, "00155D0A0B0C", normalizeMAC("00-15-5D-0A-0B-0C"))
	assert.Equal(t, "00155D0A0B0C", normalizeMAC("00155d0a0b0c"))
	assert.Equal(t, "00155D0A0B0C", normalizeMAC("00:15:5d:0a:0b:0c"))
	assert.Equal(t, "", normalizeMAC(""))
}

func TestGetIPAddressesFallback(t *testing.T) {
	t.Run("kvp reports an address", func(t *testing.T) {
		// KVP returns an IP; the fallback is never consulted.
		r := &scriptRunner{outs: [][]byte{[]byte(`"192.168.1.5"`)}}
		c := &PowerShellClient{runner: r}
		ips, err := c.GetIPAddresses(context.Background(), "node0")
		require.NoError(t, err)
		assert.Equal(t, []string{"192.168.1.5"}, ips)
		assert.Len(t, r.scripts, 1, "fallback must not run when KVP has an address")
	})

	t.Run("falls back to neighbor table", func(t *testing.T) {
		// KVP empty -> fallback: the VM MAC matches a Reachable IPv4 neighbor.
		kvp := []byte(`null`)
		neigh := []byte(`{"Macs":"00155D0A0B0C","Neighbors":[{"IPAddress":"10.0.0.9","LinkLayerAddress":"00-15-5D-0A-0B-0C","State":"Reachable"}]}`)
		r := &scriptRunner{outs: [][]byte{kvp, neigh}}
		c := &PowerShellClient{runner: r}
		ips, err := c.GetIPAddresses(context.Background(), "node0")
		require.NoError(t, err)
		assert.Equal(t, []string{"10.0.0.9"}, ips)
		assert.Len(t, r.scripts, 2)
	})
}

func TestParseNeighborIP(t *testing.T) {
	t.Run("single mac and neighbor unwrapped by ConvertTo-Json", func(t *testing.T) {
		// ConvertTo-Json renders a one-element array as a bare object/scalar.
		in := `{"Macs":"00155D0A0B0C","Neighbors":{"IPAddress":"10.0.0.9","LinkLayerAddress":"00-15-5D-0A-0B-0C","State":"Reachable"}}`
		assert.Equal(t, []string{"10.0.0.9"}, parseNeighborIP([]byte(in)))
	})

	t.Run("prefers reachable over stale", func(t *testing.T) {
		in := `{"Macs":["00155D0A0B0C"],"Neighbors":[` +
			`{"IPAddress":"10.0.0.8","LinkLayerAddress":"00-15-5D-0A-0B-0C","State":"Stale"},` +
			`{"IPAddress":"10.0.0.9","LinkLayerAddress":"00-15-5D-0A-0B-0C","State":"Reachable"}]}`
		assert.Equal(t, []string{"10.0.0.9"}, parseNeighborIP([]byte(in)))
	})

	t.Run("takes stale when nothing better", func(t *testing.T) {
		in := `{"Macs":["00155D0A0B0C"],"Neighbors":[{"IPAddress":"10.0.0.8","LinkLayerAddress":"00-15-5D-0A-0B-0C","State":"Stale"}]}`
		assert.Equal(t, []string{"10.0.0.8"}, parseNeighborIP([]byte(in)))
	})

	t.Run("ignores IPv6 and unmatched/unreachable", func(t *testing.T) {
		in := `{"Macs":["00155D0A0B0C"],"Neighbors":[` +
			`{"IPAddress":"fe80::1","LinkLayerAddress":"00-15-5D-0A-0B-0C","State":"Reachable"},` +
			`{"IPAddress":"10.0.0.50","LinkLayerAddress":"AA-BB-CC-DD-EE-FF","State":"Reachable"},` +
			`{"IPAddress":"10.0.0.9","LinkLayerAddress":"00-15-5D-0A-0B-0C","State":"Unreachable"}]}`
		assert.Nil(t, parseNeighborIP([]byte(in)))
	})

	t.Run("no addresses yet", func(t *testing.T) {
		assert.Nil(t, parseNeighborIP([]byte(`{"Macs":"00155D0A0B0C","Neighbors":[]}`)))
		assert.Nil(t, parseNeighborIP([]byte("")))
		assert.Nil(t, parseNeighborIP([]byte("null")))
	})

	t.Run("unassigned all-zero mac matches nothing", func(t *testing.T) {
		in := `{"Macs":"000000000000","Neighbors":[{"IPAddress":"10.0.0.9","LinkLayerAddress":"00-00-00-00-00-00","State":"Reachable"}]}`
		assert.Nil(t, parseNeighborIP([]byte(in)))
	})
}

func TestGetVMParsing(t *testing.T) {
	t.Run("state as string", func(t *testing.T) {
		r := &captureRunner{out: []byte(`{"Name":"node0","Id":"abc-123","State":"Running","Generation":2,"ProcessorCount":4,"MemoryStartup":2147483648,"Notes":"owned"}`)}
		c := &PowerShellClient{runner: r}
		vm, err := c.GetVM(context.Background(), "node0")
		require.NoError(t, err)
		assert.Equal(t, "node0", vm.Name)
		assert.Equal(t, "abc-123", vm.ID)
		assert.Equal(t, StateRunning, vm.State)
		assert.Equal(t, 2, vm.Generation)
		assert.Equal(t, 4, vm.CPUCount)
		assert.Equal(t, int64(2147483648), vm.MemoryStartupBytes)
		assert.True(t, vm.IsRunning())
	})

	t.Run("state as enum int", func(t *testing.T) {
		r := &captureRunner{out: []byte(`{"Name":"n","Id":"x","State":3,"Generation":2}`)}
		c := &PowerShellClient{runner: r}
		vm, err := c.GetVM(context.Background(), "n")
		require.NoError(t, err)
		assert.Equal(t, StateOff, vm.State)
	})

	t.Run("not found on empty", func(t *testing.T) {
		r := &captureRunner{out: []byte("   ")}
		c := &PowerShellClient{runner: r}
		_, err := c.GetVM(context.Background(), "gone")
		assert.ErrorIs(t, err, ErrVMNotFound)
	})
}

func TestExistsParsing(t *testing.T) {
	c := &PowerShellClient{runner: &captureRunner{out: []byte("true\n")}}
	ok, err := c.Exists(context.Background(), "n")
	require.NoError(t, err)
	assert.True(t, ok)

	c = &PowerShellClient{runner: &captureRunner{out: []byte("false\n")}}
	ok, err = c.Exists(context.Background(), "n")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestParseIPAddresses(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"null", "null", nil},
		{"single", `"192.168.1.10"`, []string{"192.168.1.10"}},
		{"many", `["192.168.1.10","fe80::1"]`, []string{"192.168.1.10", "fe80::1"}},
		{"blanks filtered", `["192.168.1.10",""]`, []string{"192.168.1.10"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseIPAddresses([]byte(tt.in)))
		})
	}
}

func TestCreateVMRoundTrip(t *testing.T) {
	// CreateVM runs the create script then re-reads via GetVM. The capture runner
	// returns the same JSON for both calls, which is enough to prove CreateVM
	// returns the parsed VM.
	r := &captureRunner{out: []byte(`{"Name":"node0","Id":"guid-1","State":"Off","Generation":2}`)}
	c := &PowerShellClient{runner: r}
	vm, err := c.CreateVM(context.Background(), CreateVMInput{Name: "node0", Generation: 2, SwitchName: "External", VHDPath: "d.vhdx"})
	require.NoError(t, err)
	assert.Equal(t, "guid-1", vm.ID)
	// The last script executed was the GetVM re-read.
	assert.True(t, strings.Contains(r.script, "Get-VM -Name 'node0'"))
}
