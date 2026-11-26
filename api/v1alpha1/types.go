/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package v1alpha1

import (
	"fmt"
	"net"
	"time"
)

const (
	// DefaultReconcilerRequeue is the default value for the reconcile retry.
	DefaultReconcilerRequeue = 15 * time.Second

	// DefaultControlPlaneEndpointPort is the default Kubernetes API server port.
	DefaultControlPlaneEndpointPort = 6443

	// DefaultVMGeneration is the default Hyper-V VM generation. Generation 2 VMs
	// are UEFI/SCSI, which is required for attaching the NoCloud cidata ISO on a
	// SCSI DVD drive (Generation 2 has no IDE controller).
	DefaultVMGeneration = 2

	// ProviderIDPrefix is the scheme used for Hyper-V provider IDs. A machine's
	// providerID is formatted as hyperv://<vm-guid>.
	ProviderIDPrefix = "hyperv://"
)

// VirtualMachineState describes the known lifecycle states of a Hyper-V VM as
// observed by the provider. It is a provider-internal summary and not a 1:1
// mapping of every Hyper-V power state.
type VirtualMachineState string

const (
	// VirtualMachineStatePending means the VM is being created or is not yet
	// reconciled to the desired running state.
	VirtualMachineStatePending VirtualMachineState = "pending"

	// VirtualMachineStateReady means the VM exists, is running and has reported
	// at least one usable address.
	VirtualMachineStateReady VirtualMachineState = "ready"

	// VirtualMachineStateDeleting means the VM is being stopped and removed.
	VirtualMachineStateDeleting VirtualMachineState = "deleting"
)

// APIEndpoint represents a reachable Kubernetes API endpoint.
// +kubebuilder:validation:MinProperties=1
type APIEndpoint struct {
	// host is the hostname or IP address on which the API server is serving.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Host string `json:"host,omitempty"`

	// port is the port on which the API server is serving.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`
}

// IsZero returns true if both host and port are zero values.
func (v APIEndpoint) IsZero() bool {
	return v.Host == "" && v.Port == 0
}

// String returns a formatted HOST:PORT representation of this APIEndpoint.
func (v APIEndpoint) String() string {
	return net.JoinHostPort(v.Host, fmt.Sprintf("%d", v.Port))
}
