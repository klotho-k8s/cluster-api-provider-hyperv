/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/cloudinit"
)

// commandRunner runs a PowerShell script and returns its stdout. It is an
// interface so tests can capture the generated script and return canned output
// without invoking a real PowerShell/Hyper-V host.
type commandRunner interface {
	run(ctx context.Context, script string) ([]byte, error)
}

// execRunner runs scripts with the real powershell.exe.
type execRunner struct{}

func (execRunner) run(ctx context.Context, script string) ([]byte, error) {
	// -NoProfile keeps user profile scripts out of the way; -NonInteractive
	// prevents any prompt from blocking the controller. The script is passed as
	// a single -Command argument; all dynamic values inside it are quoted with
	// psQuote so they cannot break out of their string literals.
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-NoLogo", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			return nil, fmt.Errorf("powershell: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("powershell: %w", err)
	}
	return out, nil
}

// asExit is a tiny errors.As helper kept local to avoid importing errors just
// for one call in this file.
func asExit(err error, target **exec.ExitError) bool {
	for err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// PowerShellClient is the production Client implementation backed by the
// Hyper-V PowerShell module.
type PowerShellClient struct {
	runner commandRunner
	// isoDir is the host directory where AttachCloudInit writes generated cidata
	// ISOs before attaching them. It is on the machine that runs this client (the
	// agent, or the controller in local mode), never on a remote host.
	isoDir string
	// vmsDir is the host directory where CreateVM writes each VM's own boot disk,
	// copied from the base image so the base is never attached or mutated. It is
	// on the machine that runs this client and is created on demand.
	vmsDir string
}

// NewPowerShellClient returns a Client that drives the real powershell.exe.
// isoDir is the host directory where generated cloud-init cidata ISOs are
// written; vmsDir is where each VM's own boot disk (a copy of the base image) is
// written. Both are created on demand.
func NewPowerShellClient(isoDir, vmsDir string) *PowerShellClient {
	return &PowerShellClient{runner: execRunner{}, isoDir: isoDir, vmsDir: vmsDir}
}

// vmDiskPath is the path of a VM's own boot disk, the per-VM copy of the base
// image under vmsDir.
func (c *PowerShellClient) vmDiskPath(name string) string {
	return filepath.Join(c.vmsDir, name+".vhdx")
}

// isoPathFor is the path of a VM's cidata ISO under isoDir. It must match the
// name AttachCloudInit writes so RemoveVM can clean it up.
func (c *PowerShellClient) isoPathFor(name string) string {
	return filepath.Join(c.isoDir, name+"-cidata.iso")
}

var _ Client = (*PowerShellClient)(nil)

// psQuote renders a Go string as a single-quoted PowerShell literal, doubling
// any embedded single quotes. This is the injection-safe way to place an
// arbitrary value into a script: inside single quotes PowerShell performs no
// variable expansion or sub-expression evaluation.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

const psPreamble = "$ErrorActionPreference = 'Stop'\n"

// vmJSON is the shape parsed from `Get-VM ... | ConvertTo-Json`. Field names
// match the Hyper-V VM object properties.
type vmJSON struct {
	Name               string `json:"Name"`
	ID                 string `json:"Id"`
	State              any    `json:"State"`
	Generation         int    `json:"Generation"`
	Notes              string `json:"Notes"`
	ProcessorCount     int    `json:"ProcessorCount"`
	MemoryStartupBytes int64  `json:"MemoryStartup"`
}

// stateString normalizes the State field, which ConvertTo-Json may render as
// either the enum name ("Running") or its integer value depending on host and
// PowerShell version.
func stateString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// Hyper-V VMState enum: 2 = Running, 3 = Off.
		switch int(t) {
		case 2:
			return StateRunning
		case 3:
			return StateOff
		default:
			return fmt.Sprintf("%d", int(t))
		}
	default:
		return ""
	}
}

func (j vmJSON) toVM() *VM {
	return &VM{
		Name:               j.Name,
		ID:                 j.ID,
		State:              stateString(j.State),
		Generation:         j.Generation,
		Notes:              j.Notes,
		CPUCount:           j.ProcessorCount,
		MemoryStartupBytes: j.MemoryStartupBytes,
	}
}

// --- script builders (pure, unit-tested) ---

func getVMScript(name string) string {
	// Select-Object narrows the object to a stable, small JSON shape.
	// -Depth 2 keeps nested enums from being truncated.
	return psPreamble +
		"Get-VM -Name " + psQuote(name) + " | " +
		"Select-Object Name,Id,State,Generation,Notes,ProcessorCount,MemoryStartup | " +
		"ConvertTo-Json -Depth 2 -Compress"
}

func existsScript(name string) string {
	// Returns the literal true/false so parsing never depends on object shape.
	return psPreamble +
		"if (Get-VM -Name " + psQuote(name) + " -ErrorAction SilentlyContinue) " +
		"{ 'true' } else { 'false' }"
}

// copyBaseDiskScript copies the base image at src to the per-VM boot disk at
// dst, creating dst's parent directory if missing. CreateVM runs this before
// New-VM so the shared base image is never attached or booted; the VM only ever
// touches its own copy. -Force overwrites a stale copy left by a prior failed
// attempt, making create retry-safe.
func copyBaseDiskScript(src, dst string) string {
	var b strings.Builder
	b.WriteString(psPreamble)
	b.WriteString("$dst = " + psQuote(dst) + "\n")
	b.WriteString("$dir = Split-Path -Parent $dst\n")
	b.WriteString("if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }\n")
	b.WriteString("Copy-Item -LiteralPath " + psQuote(src) + " -Destination $dst -Force")
	return b.String()
}

func createVMScript(in CreateVMInput) string {
	var b strings.Builder
	b.WriteString(psPreamble)
	// New-VM with the pre-copied VHDX as the existing boot disk.
	b.WriteString("New-VM -Name " + psQuote(in.Name))
	b.WriteString(fmt.Sprintf(" -Generation %d", in.Generation))
	b.WriteString(fmt.Sprintf(" -MemoryStartupBytes %d", in.MemoryStartupBytes))
	b.WriteString(" -VHDPath " + psQuote(in.VHDPath))
	b.WriteString(" -SwitchName " + psQuote(in.SwitchName))
	b.WriteString(" | Out-Null\n")
	// Fixed processor count (no dynamic memory, to keep behavior predictable).
	b.WriteString("Set-VMProcessor -VMName " + psQuote(in.Name) +
		fmt.Sprintf(" -Count %d\n", in.CPUCount))
	b.WriteString("Set-VMMemory -VMName " + psQuote(in.Name) + " -DynamicMemoryEnabled $false\n")
	if in.Notes != "" {
		b.WriteString("Set-VM -Name " + psQuote(in.Name) + " -Notes " + psQuote(in.Notes) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func startVMScript(name string) string {
	return psPreamble + "Start-VM -Name " + psQuote(name)
}

func stopVMScript(name string, force bool) string {
	if force {
		return psPreamble + "Stop-VM -Name " + psQuote(name) + " -TurnOff -Force"
	}
	return psPreamble + "Stop-VM -Name " + psQuote(name) + " -Force"
}

// removeVMScript removes the VM definition and then deletes the files the agent
// created for it: the per-VM boot disk (the copy made by CreateVM) and the
// cidata ISO built by AttachCloudInit. Remove-VM runs first so the disk is no
// longer attached; the file removals are best-effort (SilentlyContinue) so a VM
// created without cloud-init, or a disk already gone, does not fail the delete.
func removeVMScript(name, diskPath, isoPath string) string {
	var b strings.Builder
	b.WriteString(psPreamble)
	b.WriteString("Remove-VM -Name " + psQuote(name) + " -Force\n")
	b.WriteString("Remove-Item -LiteralPath " + psQuote(diskPath) + " -Force -ErrorAction SilentlyContinue\n")
	b.WriteString("Remove-Item -LiteralPath " + psQuote(isoPath) + " -Force -ErrorAction SilentlyContinue")
	return b.String()
}

func setFirmwareScript(in FirmwareInput) string {
	var b strings.Builder
	b.WriteString(psPreamble)
	// Order the OS hard disk first so the VM boots the guest image; the cidata
	// DVD only needs to be readable, not bootable.
	b.WriteString("$vmhd = Get-VMHardDiskDrive -VMName " + psQuote(in.Name) + "\n")
	if in.EnableSecureBoot {
		// Linux images need the Microsoft UEFI CA template, not the default
		// Microsoft Windows template.
		b.WriteString("Set-VMFirmware -VMName " + psQuote(in.Name) +
			" -EnableSecureBoot On -SecureBootTemplate 'MicrosoftUEFICertificateAuthority'" +
			" -FirstBootDevice $vmhd")
	} else {
		b.WriteString("Set-VMFirmware -VMName " + psQuote(in.Name) +
			" -EnableSecureBoot Off -FirstBootDevice $vmhd")
	}
	return b.String()
}

// setBootOrderScript orders the OS hard disk first without touching Secure Boot.
// The WMI backend uses it for the boot-order half of SetFirmware (it does Secure
// Boot itself in WMI); the cidata DVD only needs to be readable, not bootable.
func setBootOrderScript(name string) string {
	return psPreamble +
		"$vmhd = Get-VMHardDiskDrive -VMName " + psQuote(name) + "\n" +
		"Set-VMFirmware -VMName " + psQuote(name) + " -FirstBootDevice $vmhd"
}

func attachISOScript(name, isoPath string) string {
	// On a Generation 2 VM the DVD drive lands on the SCSI controller
	// automatically; there is no IDE controller to target.
	return psPreamble +
		"Add-VMDvdDrive -VMName " + psQuote(name) + " -Path " + psQuote(isoPath)
}

func setVLANScript(name string, vlanID int) string {
	if vlanID == 0 {
		return psPreamble +
			"Set-VMNetworkAdapterVlan -VMName " + psQuote(name) + " -Untagged"
	}
	return psPreamble +
		"Set-VMNetworkAdapterVlan -VMName " + psQuote(name) +
		fmt.Sprintf(" -Access -VlanId %d", vlanID)
}

func getIPAddressesScript(name string) string {
	// Flatten the IPAddresses arrays across all adapters into a JSON string
	// array so the result shape is stable regardless of adapter count.
	return psPreamble +
		"@(Get-VMNetworkAdapter -VMName " + psQuote(name) + " | " +
		"Select-Object -ExpandProperty IPAddresses) | ConvertTo-Json -Compress"
}

// defaultSwitchAlias is the host interface the Hyper-V "Default Switch" NATs
// through; its neighbor table maps guest MACs to the IPs the host handed out.
const defaultSwitchAlias = "vEthernet (Default Switch)"

// getNeighborScript emits the data for the host-side IP fallback: the VM's
// adapter MAC(s) plus the host neighbor (ARP) table on the Default Switch. It
// does no matching itself; parseNeighborIP correlates the two. This path does
// not depend on the guest running hyperv-daemons (KVP), so it works for generic
// cloud images once the guest has sent any traffic.
func getNeighborScript(name string) string {
	var b strings.Builder
	b.WriteString(psPreamble)
	b.WriteString("$macs = @(Get-VMNetworkAdapter -VMName " + psQuote(name) +
		" | Select-Object -ExpandProperty MacAddress)\n")
	b.WriteString("$neigh = @(Get-NetNeighbor -InterfaceAlias " + psQuote(defaultSwitchAlias) +
		" -ErrorAction SilentlyContinue | " +
		"Select-Object IPAddress, LinkLayerAddress, @{Name='State';Expression={ $_.State.ToString() }})\n")
	b.WriteString("[PSCustomObject]@{ Macs = $macs; Neighbors = $neigh } | ConvertTo-Json -Depth 3 -Compress")
	return b.String()
}

// --- Client implementation ---

// Exists reports whether a VM with the given name exists.
func (c *PowerShellClient) Exists(ctx context.Context, name string) (bool, error) {
	out, err := c.runner.run(ctx, existsScript(name))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// GetVM returns the named VM or ErrVMNotFound.
func (c *PowerShellClient) GetVM(ctx context.Context, name string) (*VM, error) {
	out, err := c.runner.run(ctx, getVMScript(name))
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" {
		return nil, ErrVMNotFound
	}
	var j vmJSON
	if err := json.Unmarshal([]byte(trimmed), &j); err != nil {
		return nil, fmt.Errorf("parsing Get-VM output: %w", err)
	}
	return j.toVM(), nil
}

// CreateVM copies the base image (in.VHDPath) to a per-VM boot disk under
// vmsDir and creates the VM against that copy, so the shared base image is never
// attached or booted. It then applies processor and memory settings.
func (c *PowerShellClient) CreateVM(ctx context.Context, in CreateVMInput) (*VM, error) {
	disk := c.vmDiskPath(in.Name)
	if _, err := c.runner.run(ctx, copyBaseDiskScript(in.VHDPath, disk)); err != nil {
		return nil, fmt.Errorf("hyperv: copy base image: %w", err)
	}
	in.VHDPath = disk
	if _, err := c.runner.run(ctx, createVMScript(in)); err != nil {
		return nil, err
	}
	return c.GetVM(ctx, in.Name)
}

// StartVM powers the VM on.
func (c *PowerShellClient) StartVM(ctx context.Context, name string) error {
	_, err := c.runner.run(ctx, startVMScript(name))
	return err
}

// StopVM powers the VM off, optionally forcibly.
func (c *PowerShellClient) StopVM(ctx context.Context, name string, force bool) error {
	_, err := c.runner.run(ctx, stopVMScript(name, force))
	return err
}

// RemoveVM removes the VM definition and cleans up the files the agent created
// for it: the per-VM boot disk and the cidata ISO.
func (c *PowerShellClient) RemoveVM(ctx context.Context, name string) error {
	_, err := c.runner.run(ctx, removeVMScript(name, c.vmDiskPath(name), c.isoPathFor(name)))
	return err
}

// SetFirmware configures Generation 2 boot order and Secure Boot.
func (c *PowerShellClient) SetFirmware(ctx context.Context, in FirmwareInput) error {
	_, err := c.runner.run(ctx, setFirmwareScript(in))
	return err
}

// AttachCloudInit builds the NoCloud cidata ISO from userData, writes it under
// isoDir on this host, and attaches it to a SCSI DVD drive. Building the ISO
// here (rather than accepting a path) is what lets the controller drive a remote
// agent: the user-data bytes cross the wire, the ISO is produced on the host.
func (c *PowerShellClient) AttachCloudInit(ctx context.Context, name string, userData []byte) error {
	data, err := cloudinit.Build(userData, cloudinit.Metadata{InstanceID: name, Hostname: name})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.isoDir, 0o700); err != nil {
		return fmt.Errorf("hyperv: cidata dir: %w", err)
	}
	isoPath := c.isoPathFor(name)
	if err := os.WriteFile(isoPath, data, 0o600); err != nil {
		return fmt.Errorf("hyperv: write cidata iso: %w", err)
	}
	_, err = c.runner.run(ctx, attachISOScript(name, isoPath))
	return err
}

// SetVLAN configures access-mode VLAN tagging on the VM's network adapter.
func (c *PowerShellClient) SetVLAN(ctx context.Context, name string, vlanID int) error {
	_, err := c.runner.run(ctx, setVLANScript(name, vlanID))
	return err
}

// GetIPAddresses returns the VM's IP addresses. It first tries the guest-
// reported path (Hyper-V KVP via integration services), which needs
// hyperv-daemons in the guest. When that reports nothing — as it does for a
// generic cloud image — it falls back to a host-side lookup: match the VM's
// adapter MAC against the host neighbor table on the Default Switch. An empty
// result is valid (the guest may not have sent traffic yet).
func (c *PowerShellClient) GetIPAddresses(ctx context.Context, name string) ([]string, error) {
	out, kvpErr := c.runner.run(ctx, getIPAddressesScript(name))
	if kvpErr == nil {
		if ips := parseIPAddresses(out); len(ips) > 0 {
			return ips, nil
		}
	}
	nout, nErr := c.runner.run(ctx, getNeighborScript(name))
	if nErr != nil {
		// The fallback failed. Surface an error only if the KVP path failed too;
		// otherwise the KVP path simply reported no addresses yet, which is valid.
		if kvpErr != nil {
			return nil, kvpErr
		}
		return nil, nil
	}
	return parseNeighborIP(nout), nil
}

// parseIPAddresses decodes the JSON produced by getIPAddressesScript, tolerating
// the three shapes ConvertTo-Json yields for zero, one, or many addresses.
func parseIPAddresses(out []byte) []string {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	// Many addresses: a JSON array.
	var many []string
	if err := json.Unmarshal([]byte(trimmed), &many); err == nil {
		return filterEmpty(many)
	}
	// Single address: a bare JSON string.
	var one string
	if err := json.Unmarshal([]byte(trimmed), &one); err == nil {
		return filterEmpty([]string{one})
	}
	return nil
}

func filterEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// neighborEntry is one row of the host neighbor (ARP) table as emitted by
// getNeighborScript.
type neighborEntry struct {
	IPAddress        string `json:"IPAddress"`
	LinkLayerAddress string `json:"LinkLayerAddress"`
	State            string `json:"State"`
}

// neighborStateRank scores a neighbor state so the most trustworthy IPv4 wins.
// Reachable is freshly confirmed; Stale was confirmed once and is usually still
// valid; Permanent is a static entry. Unreachable/Incomplete carry no usable
// address and are rejected (rank 0).
func neighborStateRank(state string) int {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "reachable":
		return 4
	case "stale":
		return 3
	case "permanent":
		return 2
	case "unreachable", "incomplete", "":
		return 0
	default:
		// Probe, Delay, Dynamic, etc.: usable but less certain than the above.
		return 1
	}
}

// normalizeMAC strips separators (":", "-", ".") and upper-cases a MAC so a
// Hyper-V MacAddress ("00155D0A0B0C") and a Get-NetNeighbor LinkLayerAddress
// ("00-15-5D-0A-0B-0C") compare equal.
func normalizeMAC(mac string) string {
	var b strings.Builder
	for _, r := range mac {
		switch r {
		case ':', '-', '.', ' ':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(b.String())
}

// decodeStringOrSlice tolerates ConvertTo-Json rendering a single-element array
// as a bare scalar: it decodes a JSON string array, or a single JSON string.
func decodeStringOrSlice(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	return nil
}

// decodeNeighbors tolerates the same single-element unwrapping for the neighbor
// rows: a JSON array of objects, or a single object.
func decodeNeighbors(raw json.RawMessage) []neighborEntry {
	if len(raw) == 0 {
		return nil
	}
	var many []neighborEntry
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	var one neighborEntry
	if err := json.Unmarshal(raw, &one); err == nil {
		return []neighborEntry{one}
	}
	return nil
}

// parseNeighborIP correlates the VM's MAC(s) with the host neighbor table (both
// emitted by getNeighborScript) and returns the best-ranked IPv4 the host has
// seen for the VM. It returns nil when no usable match exists; an empty result
// is normal before the guest has sent any traffic.
func parseNeighborIP(out []byte) []string {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var raw struct {
		Macs      json.RawMessage `json:"Macs"`
		Neighbors json.RawMessage `json:"Neighbors"`
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil
	}
	want := map[string]bool{}
	for _, m := range decodeStringOrSlice(raw.Macs) {
		if n := normalizeMAC(m); n != "" && n != "000000000000" {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	bestIP := ""
	bestRank := 0
	for _, n := range decodeNeighbors(raw.Neighbors) {
		if !want[normalizeMAC(n.LinkLayerAddress)] {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(n.IPAddress))
		if ip == nil || ip.To4() == nil {
			continue // IPv4 only
		}
		if r := neighborStateRank(n.State); r > bestRank {
			bestRank, bestIP = r, ip.String()
		}
	}
	if bestIP == "" {
		return nil
	}
	return []string{bestIP}
}
