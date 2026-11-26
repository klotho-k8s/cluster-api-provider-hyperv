/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package v1alpha1

const (
	// ClusterFinalizer allows cleaning up resources associated with a
	// HyperVCluster before removing it from the API server.
	ClusterFinalizer = "hypervcluster.infrastructure.cluster.x-k8s.io"

	// MachineFinalizer allows cleaning up resources associated with a
	// HyperVMachine before removing it from the API server.
	MachineFinalizer = "hypervmachine.infrastructure.cluster.x-k8s.io"
)

// Conditions and Reasons for HyperVCluster.
const (
	// HyperVClusterHyperVAvailableCondition documents the availability of the
	// underlying Hyper-V host used by the HyperVCluster.
	HyperVClusterHyperVAvailableCondition = "HyperVAvailable"

	// HyperVClusterHyperVAvailableMissingControlPlaneEndpointReason documents a
	// HyperVCluster that is waiting for a control plane endpoint to be provided.
	HyperVClusterHyperVAvailableMissingControlPlaneEndpointReason = "MissingControlPlaneEndpoint"

	// HyperVClusterHyperVAvailableDeletingReason documents a HyperVCluster being deleted.
	HyperVClusterHyperVAvailableDeletingReason = "Deleting"
)

// Conditions and Reasons for HyperVMachine.
const (
	// HyperVMachineVirtualMachineProvisionedCondition documents the status of the
	// provisioning of a HyperVMachine and its underlying virtual machine.
	HyperVMachineVirtualMachineProvisionedCondition = "VirtualMachineProvisioned"

	// HyperVMachineVirtualMachineProvisionedWaitingForClusterInfrastructureReason
	// documents a HyperVMachine waiting for the cluster infrastructure to be ready.
	HyperVMachineVirtualMachineProvisionedWaitingForClusterInfrastructureReason = "WaitingForClusterInfrastructure"

	// HyperVMachineVirtualMachineProvisionedWaitingForBootstrapDataReason documents
	// a HyperVMachine waiting for the bootstrap data secret to be available.
	HyperVMachineVirtualMachineProvisionedWaitingForBootstrapDataReason = "WaitingForBootstrapData"

	// HyperVMachineVirtualMachineProvisionedCreatingReason documents a HyperVMachine
	// whose VM is being created on the Hyper-V host.
	HyperVMachineVirtualMachineProvisionedCreatingReason = "Creating"

	// HyperVMachineVirtualMachineProvisionedAdoptedReason documents a HyperVMachine
	// that adopted a pre-existing VM found on the host by name.
	HyperVMachineVirtualMachineProvisionedAdoptedReason = "Adopted"

	// HyperVMachineVirtualMachineProvisionedWaitingForPowerOnReason documents a
	// HyperVMachine waiting for the VM to power on.
	HyperVMachineVirtualMachineProvisionedWaitingForPowerOnReason = "WaitingForPowerOn"

	// HyperVMachineVirtualMachineProvisionedWaitingForAddressesReason documents a
	// HyperVMachine waiting for the guest to report an address.
	HyperVMachineVirtualMachineProvisionedWaitingForAddressesReason = "WaitingForAddresses"

	// HyperVMachineVirtualMachineProvisionedProvisionFailedReason documents a
	// failure during virtual machine provisioning.
	HyperVMachineVirtualMachineProvisionedProvisionFailedReason = "VMProvisionFailed"

	// HyperVMachineVirtualMachineProvisionedDeletingReason documents a HyperVMachine
	// being deleted.
	HyperVMachineVirtualMachineProvisionedDeletingReason = "Deleting"
)
