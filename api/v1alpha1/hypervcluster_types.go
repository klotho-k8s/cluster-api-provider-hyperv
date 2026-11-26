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
	// HyperVClusterKind is the HyperVCluster kind.
	HyperVClusterKind = "HyperVCluster"
)

// HyperVClusterSpec defines the desired state of a HyperVCluster.
type HyperVClusterSpec struct {
	// controlPlaneEndpoint represents the endpoint used to communicate with the
	// control plane.
	//
	// Hyper-V has no load balancer. For v1 the endpoint is either a user-provided
	// static virtual IP (for example a kube-vip VIP hosted inside the control
	// plane) or the address of the first control plane machine. The provider does
	// not provision a load balancer; it only validates and stores the endpoint.
	// +optional
	ControlPlaneEndpoint APIEndpoint `json:"controlPlaneEndpoint,omitempty"`

	// server is the address of the Hyper-V host that the controller manages VMs
	// on. It is informational for single-host deployments where the controller
	// runs on (or next to) the Hyper-V host; it does not change where PowerShell
	// cmdlets are executed.
	// +optional
	Server string `json:"server,omitempty"`

	// failureDomains is an optional list of failure domain names that machines
	// may be placed into. Hyper-V failure domains are purely informational in
	// this provider (for example to spread machines across multiple hosts managed
	// out of band); the provider does not enforce placement.
	// +optional
	// +listType=set
	FailureDomains []string `json:"failureDomains,omitempty"`
}

// HyperVClusterStatus defines the observed state of a HyperVCluster.
type HyperVClusterStatus struct {
	// conditions represents the observations of a HyperVCluster's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// initialization provides observations of the HyperVCluster initialization
	// process. NOTE: fields in this struct are part of the Cluster API contract
	// and are used to orchestrate initial Cluster provisioning.
	// +optional
	Initialization HyperVClusterInitializationStatus `json:"initialization,omitempty"`

	// failureDomains is a map of failure domain information, keyed by failure
	// domain name, surfaced for the Cluster API topology.
	// +optional
	FailureDomains []clusterv1.FailureDomain `json:"failureDomains,omitempty"`
}

// HyperVClusterInitializationStatus provides observations of the HyperVCluster
// initialization process.
// +kubebuilder:validation:MinProperties=1
type HyperVClusterInitializationStatus struct {
	// provisioned is true when the infrastructure provider reports that the
	// Cluster's infrastructure is fully provisioned.
	// NOTE: this field is part of the Cluster API contract.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=hypervclusters,scope=Namespaced,categories=cluster-api,shortName=hvc
// +kubebuilder:storageversion
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1alpha1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta2=v1alpha1"
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.initialization.provisioned",description="Cluster infrastructure is ready"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".spec.controlPlaneEndpoint.host",description="API Endpoint host"

// HyperVCluster is the Schema for the hypervclusters API.
type HyperVCluster struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the HyperVCluster spec.
	// +optional
	Spec HyperVClusterSpec `json:"spec,omitempty"`

	// status is the HyperVCluster status.
	// +optional
	Status HyperVClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HyperVClusterList contains a list of HyperVCluster.
type HyperVClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HyperVCluster `json:"items"`
}

// GetConditions returns the observations of the operational state of the
// HyperVCluster resource.
func (c *HyperVCluster) GetConditions() []metav1.Condition {
	return c.Status.Conditions
}

// SetConditions sets the underlying service state of the HyperVCluster.
func (c *HyperVCluster) SetConditions(conditions []metav1.Condition) {
	c.Status.Conditions = conditions
}

func init() {
	objectTypes = append(objectTypes, &HyperVCluster{}, &HyperVClusterList{})
}
