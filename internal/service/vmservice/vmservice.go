/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Package vmservice contains the Hyper-V VM lifecycle logic used by the
// HyperVMachine controller: adopt-or-create on the normal path and stop-then-
// remove on delete. It operates on a Scope that bundles the Cluster API objects
// with a hyperv.Client, so it can be unit tested with a fake client and no real
// Hyper-V host.
package vmservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/klotho-k8s/cluster-api-provider-hyperv/api/v1alpha1"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// mibToBytes converts mebibytes to bytes.
const mibToBytes = 1024 * 1024

// Scope bundles everything the VM lifecycle needs. The controller builds it per
// reconcile; tests build it directly.
type Scope struct {
	// Client is the Hyper-V backend.
	Client hyperv.Client
	// Cluster is the owning Cluster API Cluster.
	Cluster *clusterv1.Cluster
	// HyperVCluster is the infrastructure cluster.
	HyperVCluster *infrav1.HyperVCluster
	// Machine is the owning Cluster API Machine.
	Machine *clusterv1.Machine
	// HyperVMachine is the infrastructure machine being reconciled; ReconcileVM
	// and DeleteVM mutate its spec/status in place for the controller to persist.
	HyperVMachine *infrav1.HyperVMachine
	// BootstrapData is the decoded bootstrap secret, delivered to the guest as
	// cloud-init user-data. It is handed to the Hyper-V client, which builds the
	// cidata ISO on the host.
	BootstrapData []byte
	// Logger is the scoped logger.
	Logger logr.Logger
}

// VMName returns the name the VM has (or will have) on the host. Precedence:
// explicit spec.vmName, then the owning Machine name, then the HyperVMachine
// name. This is the name used for adoption lookups, so an external tool that
// pre-creates "node zero" must name the VM to match.
func (s *Scope) VMName() string {
	if s.HyperVMachine.Spec.VMName != "" {
		return s.HyperVMachine.Spec.VMName
	}
	if s.Machine != nil && s.Machine.Name != "" {
		return s.Machine.Name
	}
	return s.HyperVMachine.Name
}

// ownershipNote is stamped into the VM's Notes field on VMs the provider
// creates, so the lineage is visible on the host.
func (s *Scope) ownershipNote() string {
	cluster := ""
	if s.Cluster != nil {
		cluster = s.Cluster.Name
	}
	return fmt.Sprintf("cluster-api-provider-hyperv cluster=%s machine=%s", cluster, s.HyperVMachine.Name)
}

// ReconcileVM brings the VM to the desired running state and updates the
// HyperVMachine in place. It LOOKS UP the VM by name first and adopts it if it
// already exists (the external-pre-create workflow), creating one only when it
// is absent. It returns the current VM view.
func ReconcileVM(ctx context.Context, s *Scope) (*hyperv.VM, error) {
	name := s.VMName()

	vm, err := s.Client.GetVM(ctx, name)
	switch {
	case err == nil:
		// A VM with this name already exists: adopt it. Record adoption the
		// first time we see it so the distinction survives in status.
		if s.HyperVMachine.Status.Adopted == nil {
			s.Logger.Info("adopting pre-existing Hyper-V VM", "vmName", name)
			s.HyperVMachine.Status.Adopted = ptr.To(true)
		}
	case errors.Is(err, hyperv.ErrVMNotFound):
		// No VM yet: create and provision it.
		vm, err = createAndProvision(ctx, s, name)
		if err != nil {
			return nil, err
		}
		s.HyperVMachine.Status.Adopted = ptr.To(false)
	default:
		return nil, fmt.Errorf("looking up VM %q: %w", name, err)
	}

	// Ensure the VM is powered on (covers a freshly created VM and an adopted
	// VM that happens to be stopped).
	if vm.State != hyperv.StateRunning {
		s.Logger.Info("starting Hyper-V VM", "vmName", name, "state", vm.State)
		if err := s.Client.StartVM(ctx, name); err != nil {
			return nil, fmt.Errorf("starting VM %q: %w", name, err)
		}
		if vm, err = s.Client.GetVM(ctx, name); err != nil {
			return nil, fmt.Errorf("re-reading VM %q after start: %w", name, err)
		}
	}

	// Record identity and observed state.
	s.HyperVMachine.Spec.ProviderID = infrav1.ProviderIDPrefix + vm.ID

	addrs, err := s.Client.GetIPAddresses(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("getting addresses for VM %q: %w", name, err)
	}
	s.HyperVMachine.Status.Addresses = toMachineAddresses(addrs)

	if vm.IsRunning() && len(addrs) > 0 {
		s.HyperVMachine.Status.VMState = infrav1.VirtualMachineStateReady
	} else {
		s.HyperVMachine.Status.VMState = infrav1.VirtualMachineStatePending
	}

	return vm, nil
}

// createAndProvision creates the VM and applies the one-time provisioning that
// must happen before first boot: cidata ISO, VLAN, firmware/boot order.
func createAndProvision(ctx context.Context, s *Scope, name string) (*hyperv.VM, error) {
	spec := s.HyperVMachine.Spec

	generation := int(spec.Generation)
	if generation == 0 {
		generation = infrav1.DefaultVMGeneration
	}

	s.Logger.Info("creating Hyper-V VM", "vmName", name, "image", spec.Image)
	vm, err := s.Client.CreateVM(ctx, hyperv.CreateVMInput{
		Name:               name,
		Generation:         generation,
		MemoryStartupBytes: int64(spec.MemoryMiB) * mibToBytes,
		CPUCount:           int(spec.CPUCount),
		SwitchName:         spec.SwitchName,
		VHDPath:            spec.Image,
		Notes:              s.ownershipNote(),
	})
	if err != nil {
		return nil, fmt.Errorf("creating VM %q: %w", name, err)
	}

	// Deliver bootstrap data via a NoCloud cidata ISO. The user-data bytes are
	// handed to the Hyper-V client, which builds and attaches the ISO on the host
	// (so nothing assumes a shared filesystem between controller and host).
	if err := s.Client.AttachCloudInit(ctx, name, s.BootstrapData); err != nil {
		return nil, fmt.Errorf("attaching cidata ISO to VM %q: %w", name, err)
	}

	if spec.VLANID != 0 {
		if err := s.Client.SetVLAN(ctx, name, int(spec.VLANID)); err != nil {
			return nil, fmt.Errorf("setting VLAN on VM %q: %w", name, err)
		}
	}

	if err := s.Client.SetFirmware(ctx, hyperv.FirmwareInput{
		Name:             name,
		EnableSecureBoot: spec.EnableSecureBoot,
	}); err != nil {
		return nil, fmt.Errorf("configuring firmware on VM %q: %w", name, err)
	}

	return vm, nil
}

// DeleteVM tears the VM down in a single call and returns done=true once it is
// gone. It delegates to the atomic hyperv.Client.DeleteVM, which stops the VM,
// destroys it, and removes its disk and cidata ISO server-side. This is a
// deliberate one-shot: in a self-managed cluster the controller runs on the very
// VM being deleted and dies the instant the agent stops it, so a stop-then-
// remove sequence spread across reconciles would strand the VM half-deleted. The
// agent runs on the host and finishes the teardown even after this call's
// connection drops. DeleteVM is idempotent, so an already-gone VM still reports
// done=true (the klotho host-side backstop may also have called it).
func DeleteVM(ctx context.Context, s *Scope) (bool, error) {
	name := s.VMName()
	s.HyperVMachine.Status.VMState = infrav1.VirtualMachineStateDeleting

	s.Logger.Info("deleting Hyper-V VM", "vmName", name)
	if err := s.Client.DeleteVM(ctx, name); err != nil {
		return false, fmt.Errorf("deleting VM %q: %w", name, err)
	}
	return true, nil
}

// toMachineAddresses converts raw IP strings to Cluster API machine addresses.
// Hyper-V reports guest addresses without distinguishing internal from external,
// so they are surfaced as InternalIP, which is the conventional choice for a
// private virtualization backend.
func toMachineAddresses(ips []string) []clusterv1.MachineAddress {
	if len(ips) == 0 {
		return nil
	}
	out := make([]clusterv1.MachineAddress, 0, len(ips))
	for _, ip := range ips {
		out = append(out, clusterv1.MachineAddress{
			Type:    clusterv1.MachineInternalIP,
			Address: ip,
		})
	}
	return out
}
