/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// HyperVMachineTemplateKind is the HyperVMachineTemplate kind.
	HyperVMachineTemplateKind = "HyperVMachineTemplate"
)

// HyperVMachineTemplateResource describes the data needed to create a
// HyperVMachine from a template.
type HyperVMachineTemplateResource struct {
	// metadata is the standard object metadata for the templated machine.
	// +optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the HyperVMachine spec to stamp out.
	// +required
	Spec HyperVMachineSpec `json:"spec"`
}

// HyperVMachineTemplateSpec defines the desired state of a HyperVMachineTemplate.
type HyperVMachineTemplateSpec struct {
	// template is the HyperVMachine template.
	// +required
	Template HyperVMachineTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=hypervmachinetemplates,scope=Namespaced,categories=cluster-api,shortName=hvmt
// +kubebuilder:storageversion
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1alpha1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta2=v1alpha1"

// HyperVMachineTemplate is the Schema for the hypervmachinetemplates API.
type HyperVMachineTemplate struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the HyperVMachineTemplate spec.
	// +optional
	Spec HyperVMachineTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// HyperVMachineTemplateList contains a list of HyperVMachineTemplate.
type HyperVMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HyperVMachineTemplate `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &HyperVMachineTemplate{}, &HyperVMachineTemplateList{})
}
