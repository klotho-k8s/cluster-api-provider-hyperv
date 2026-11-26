/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Package hyperv provides a small, fakeable abstraction over the subset of
// Microsoft Hyper-V management used by the Cluster API provider. The default
// implementation shells out to the Hyper-V PowerShell cmdlets; tests inject a
// fake instead, so no real Hyper-V host is required to exercise the provider's
// logic.
package hyperv

import (
	"context"
	"errors"
)

// ErrVMNotFound is returned by GetVM when no VM with the requested name exists.
var ErrVMNotFound = errors.New("hyperv: virtual machine not found")

// Power states reported by Hyper-V (Get-VM .State), normalized to the strings
// the cmdlets use.
const (
	StateRunning  = "Running"
	StateOff      = "Off"
	StateStarting = "Starting"
	StateStopping = "Stopping"
	StatePaused   = "Paused"
	StateSaved    = "Saved"
)

// VM is the provider's view of a Hyper-V virtual machine.
type VM struct {
	// Name is the VM name on the host (unique within a host).
	Name string
	// ID is the Hyper-V VM Id, a GUID. It is the stable identity used to build
	// the Cluster API providerID.
	ID string
	// State is the Hyper-V power state, for example "Running" or "Off".
	State string
	// Generation is the VM generation (2 for UEFI/SCSI).
	Generation int
	// Notes is the free-form Hyper-V Notes field. The provider stamps an
	// ownership marker here so externally pre-created VMs can be recognized.
	Notes string
	// CPUCount is the number of virtual processors.
	CPUCount int
	// MemoryStartupBytes is the configured startup memory, in bytes.
	MemoryStartupBytes int64
}

// IsRunning reports whether the VM is in the Running state.
func (v *VM) IsRunning() bool {
	return v != nil && v.State == StateRunning
}

// CreateVMInput is the set of parameters used to create a new VM. All string
// values are treated as untrusted and are safely quoted before being placed in
// a PowerShell script.
type CreateVMInput struct {
	// Name is the VM name to create.
	Name string
	// Generation is the VM generation; only 2 is supported.
	Generation int
	// MemoryStartupBytes is the startup memory in bytes.
	MemoryStartupBytes int64
	// CPUCount is the number of virtual processors.
	CPUCount int
	// SwitchName is the Hyper-V virtual switch to connect the VM to.
	SwitchName string
	// VHDPath is the path, on the host, to the VM's OS disk (a VHDX that the
	// caller has already copied from the base image).
	VHDPath string
	// Notes is written to the VM's Notes field.
	Notes string
}

// FirmwareInput configures Generation 2 VM firmware: boot order and Secure Boot.
type FirmwareInput struct {
	// Name is the VM to configure.
	Name string
	// EnableSecureBoot toggles UEFI Secure Boot. When enabled the Microsoft UEFI
	// Certificate Authority template is selected so that most Linux cloud images
	// boot.
	EnableSecureBoot bool
}

// Client is the fakeable Hyper-V surface used by the provider.
type Client interface {
	// Exists reports whether a VM with the given name exists on the host.
	Exists(ctx context.Context, name string) (bool, error)
	// GetVM returns the VM with the given name, or ErrVMNotFound.
	GetVM(ctx context.Context, name string) (*VM, error)
	// CreateVM creates a VM (New-VM plus Set-VM/Set-VMMemory/Set-VMProcessor).
	CreateVM(ctx context.Context, in CreateVMInput) (*VM, error)
	// StartVM powers the VM on (Start-VM).
	StartVM(ctx context.Context, name string) error
	// StopVM powers the VM off (Stop-VM). When force is true the VM is turned
	// off without a graceful guest shutdown (Stop-VM -TurnOff).
	StopVM(ctx context.Context, name string, force bool) error
	// RemoveVM removes the VM definition (Remove-VM -Force). It does not delete
	// the backing VHDX files.
	RemoveVM(ctx context.Context, name string) error
	// SetFirmware sets Generation 2 firmware: it orders the OS disk first and
	// configures Secure Boot (Set-VMFirmware).
	SetFirmware(ctx context.Context, in FirmwareInput) error
	// AttachCloudInit builds a NoCloud cidata ISO from the given cloud-init
	// user-data and attaches it to a SCSI DVD drive (Add-VMDvdDrive). On
	// Generation 2 VMs this is how bootstrap data is presented to the guest.
	// The ISO is built and written on whichever host runs this client (the
	// agent, or the controller in local mode), so no ISO path ever crosses the
	// controller/host boundary. The meta-data (instance-id, local-hostname) is
	// derived from name.
	AttachCloudInit(ctx context.Context, name string, userData []byte) error
	// SetVLAN sets an access-mode VLAN ID on the VM's network adapter
	// (Set-VMNetworkAdapterVlan). A zero id clears any VLAN tagging.
	SetVLAN(ctx context.Context, name string, vlanID int) error
	// GetIPAddresses returns the guest IP addresses reported by the Hyper-V
	// integration services (Get-VMNetworkAdapter .IPAddresses).
	GetIPAddresses(ctx context.Context, name string) ([]string, error)
}
