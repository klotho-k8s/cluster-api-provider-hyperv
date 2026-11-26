/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	// HyperVMachineKind is the HyperVMachine kind.
	HyperVMachineKind = "HyperVMachine"
)

// HyperVMachineSpec defines the desired state of a HyperVMachine.
type HyperVMachineSpec struct {
	// providerID is the Hyper-V VM identity formatted as hyperv://<vm-guid>,
	// where <vm-guid> is the Hyper-V virtual machine Id (a GUID) as reported by
	// Get-VM. It is set by the controller once the VM exists.
	// +optional
	ProviderID string `json:"providerID,omitempty"`

	// vmName is the name the VM has (or will have) on the Hyper-V host. When set,
	// the provider looks up an existing VM by this exact name and adopts it if it
	// already exists; otherwise it creates one with this name. This is the key to
	// the "boot node zero out of band, then let the provider adopt it" workflow.
	// When empty, the provider derives the name from the owning Machine's name.
	// +optional
	// +kubebuilder:validation:MaxLength=100
	VMName string `json:"vmName,omitempty"`

	// image is the path, on the Hyper-V host, to the cloud-init enabled base
	// VHDX used as the VM's operating system disk. The provider copies this image
	// for each VM it creates. The file must exist on the host; the provider does
	// not download or build it.
	// +required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// generation is the Hyper-V VM generation. Only Generation 2 (UEFI/SCSI) is
	// supported, because the NoCloud cidata ISO is attached to a SCSI DVD drive
	// and Generation 2 VMs have no IDE controller.
	// +optional
	// +kubebuilder:validation:Enum=2
	// +kubebuilder:default=2
	Generation int32 `json:"generation,omitempty"`

	// switchName is the name of the Hyper-V virtual switch to attach the VM's
	// network adapter to.
	// +required
	// +kubebuilder:validation:MinLength=1
	SwitchName string `json:"switchName"`

	// cpuCount is the number of virtual processors assigned to the VM.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	CPUCount int32 `json:"cpuCount,omitempty"`

	// memoryMiB is the startup memory of the VM, in MiB.
	// +optional
	// +kubebuilder:validation:Minimum=512
	// +kubebuilder:default=2048
	MemoryMiB int32 `json:"memoryMiB,omitempty"`

	// diskSizeGB optionally resizes the copied OS disk to this many gigabytes.
	// Hyper-V can only grow a VHDX, so the value must be larger than the base
	// image's virtual size. When unset the disk keeps the base image size.
	// +optional
	// +kubebuilder:validation:Minimum=1
	DiskSizeGB int32 `json:"diskSizeGB,omitempty"`

	// vlanID optionally sets an 802.1Q VLAN access ID on the VM's network adapter.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	VLANID int32 `json:"vlanID,omitempty"`

	// enableSecureBoot controls UEFI Secure Boot on the Generation 2 VM. Most
	// Linux cloud images require the Microsoft UEFI CA template, which the
	// provider selects when Secure Boot is enabled; many images boot more
	// reliably with it disabled. Defaults to disabled.
	// +optional
	// +kubebuilder:default=false
	EnableSecureBoot bool `json:"enableSecureBoot,omitempty"`
}

// HyperVMachineStatus defines the observed state of a HyperVMachine.
type HyperVMachineStatus struct {
	// conditions defines current service state of the HyperVMachine.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// initialization provides observations of the HyperVMachine initialization
	// process. NOTE: fields in this struct are part of the Cluster API contract.
	// +optional
	Initialization HyperVMachineInitializationStatus `json:"initialization,omitempty"`

	// addresses contains the associated addresses for the Hyper-V VM.
	// +optional
	// +listType=atomic
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// vmState is the last observed provider view of the VM state.
	// +optional
	VMState VirtualMachineState `json:"vmState,omitempty"`

	// adopted is true when the VM backing this machine was pre-existing on the
	// host and adopted by the provider rather than created by it.
	// +optional
	Adopted *bool `json:"adopted,omitempty"`
}

// HyperVMachineInitializationStatus provides observations of the HyperVMachine
// initialization process.
// +kubebuilder:validation:MinProperties=1
type HyperVMachineInitializationStatus struct {
	// provisioned is true when the infrastructure provider reports that the
	// Machine's infrastructure is fully provisioned.
	// NOTE: this field is part of the Cluster API contract.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=hypervmachines,scope=Namespaced,categories=cluster-api,shortName=hvm
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster to which this HyperVMachine belongs"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.initialization.provisioned",description="Machine ready status"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.vmState",description="Hyper-V VM state"
// +kubebuilder:printcolumn:name="ProviderID",type="string",JSONPath=".spec.providerID",description="Provider ID"
// +kubebuilder:printcolumn:name="Machine",type="string",JSONPath=".metadata.ownerReferences[?(@.kind==\"Machine\")].name",description="Machine object which owns this HyperVMachine"

// HyperVMachine is the Schema for the hypervmachines API.
type HyperVMachine struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the HyperVMachine spec.
	// +optional
	Spec HyperVMachineSpec `json:"spec,omitempty"`

	// status is the HyperVMachine status.
	// +optional
	Status HyperVMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HyperVMachineList contains a list of HyperVMachine.
type HyperVMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HyperVMachine `json:"items"`
}

// GetConditions returns the observations of the operational state of the
// HyperVMachine resource.
func (m *HyperVMachine) GetConditions() []metav1.Condition {
	return m.Status.Conditions
}

// SetConditions sets the underlying service state of the HyperVMachine.
func (m *HyperVMachine) SetConditions(conditions []metav1.Condition) {
	m.Status.Conditions = conditions
}

func init() {
	objectTypes = append(objectTypes, &HyperVMachine{}, &HyperVMachineList{})
}
