/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Package fake provides an in-memory implementation of hyperv.Client for tests.
// It needs no PowerShell or Hyper-V host.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// Client is an in-memory hyperv.Client. It records the VMs it knows about and
// the addresses to report for them, and it counts the operations invoked so
// tests can assert on provider behavior (for example that an existing VM was
// adopted rather than re-created).
type Client struct {
	mu sync.Mutex

	// VMs is the set of known VMs keyed by name. Pre-populate it to simulate a
	// VM that an external tool created out of band (the adoption case).
	VMs map[string]*hyperv.VM
	// Addresses to report from GetIPAddresses, keyed by VM name.
	Addresses map[string][]string

	// Error injection: when set, the matching method returns this error.
	CreateErr error
	GetErr    error

	// Call counters for assertions.
	CreateCalls        int
	StartCalls         int
	StopCalls          int
	RemoveCalls        int
	FirmwareCfg        int
	AttachCloudInitCfg int
	VLANCfg            int
	// LastUserData records the cloud-init user-data from the most recent
	// AttachCloudInit call so tests can assert the bootstrap bytes crossed.
	LastUserData []byte
}

// New returns an empty fake client.
func New() *Client {
	return &Client{
		VMs:       map[string]*hyperv.VM{},
		Addresses: map[string][]string{},
	}
}

var _ hyperv.Client = (*Client)(nil)

// Exists reports whether a VM is known.
func (c *Client) Exists(_ context.Context, name string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.VMs[name]
	return ok, nil
}

// GetVM returns a copy of the named VM or hyperv.ErrVMNotFound.
func (c *Client) GetVM(_ context.Context, name string) (*hyperv.VM, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.GetErr != nil {
		return nil, c.GetErr
	}
	vm, ok := c.VMs[name]
	if !ok {
		return nil, hyperv.ErrVMNotFound
	}
	cp := *vm
	return &cp, nil
}

// CreateVM records a new VM in the Off state.
func (c *Client) CreateVM(_ context.Context, in hyperv.CreateVMInput) (*hyperv.VM, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.CreateErr != nil {
		return nil, c.CreateErr
	}
	if _, ok := c.VMs[in.Name]; ok {
		return nil, fmt.Errorf("fake: VM %q already exists", in.Name)
	}
	c.CreateCalls++
	vm := &hyperv.VM{
		Name:               in.Name,
		ID:                 uuid.NewString(),
		State:              hyperv.StateOff,
		Generation:         in.Generation,
		Notes:              in.Notes,
		CPUCount:           in.CPUCount,
		MemoryStartupBytes: in.MemoryStartupBytes,
	}
	c.VMs[in.Name] = vm
	cp := *vm
	return &cp, nil
}

// StartVM moves the VM to Running.
func (c *Client) StartVM(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	vm, ok := c.VMs[name]
	if !ok {
		return hyperv.ErrVMNotFound
	}
	c.StartCalls++
	vm.State = hyperv.StateRunning
	return nil
}

// StopVM moves the VM to Off.
func (c *Client) StopVM(_ context.Context, name string, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	vm, ok := c.VMs[name]
	if !ok {
		return hyperv.ErrVMNotFound
	}
	c.StopCalls++
	vm.State = hyperv.StateOff
	return nil
}

// RemoveVM forgets the VM.
func (c *Client) RemoveVM(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.VMs[name]; !ok {
		return hyperv.ErrVMNotFound
	}
	c.RemoveCalls++
	delete(c.VMs, name)
	delete(c.Addresses, name)
	return nil
}

// SetFirmware records that firmware was configured.
func (c *Client) SetFirmware(_ context.Context, in hyperv.FirmwareInput) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.VMs[in.Name]; !ok {
		return hyperv.ErrVMNotFound
	}
	c.FirmwareCfg++
	return nil
}

// AttachCloudInit records that cloud-init data was attached.
func (c *Client) AttachCloudInit(_ context.Context, name string, userData []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.VMs[name]; !ok {
		return hyperv.ErrVMNotFound
	}
	c.AttachCloudInitCfg++
	c.LastUserData = userData
	return nil
}

// SetVLAN records that a VLAN was configured.
func (c *Client) SetVLAN(_ context.Context, name string, _ int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.VMs[name]; !ok {
		return hyperv.ErrVMNotFound
	}
	c.VLANCfg++
	return nil
}

// GetIPAddresses returns the configured addresses for the VM.
func (c *Client) GetIPAddresses(_ context.Context, name string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Addresses[name], nil
}
