/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// HyperVClusterTemplateKind is the HyperVClusterTemplate kind.
	HyperVClusterTemplateKind = "HyperVClusterTemplate"
)

// HyperVClusterTemplateResource describes the data needed to create a
// HyperVCluster from a template.
type HyperVClusterTemplateResource struct {
	// metadata is the standard object metadata for the templated cluster.
	// +optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the HyperVCluster spec to stamp out.
	// +required
	Spec HyperVClusterSpec `json:"spec"`
}

// HyperVClusterTemplateSpec defines the desired state of a HyperVClusterTemplate.
type HyperVClusterTemplateSpec struct {
	// template is the HyperVCluster template.
	// +required
	Template HyperVClusterTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=hypervclustertemplates,scope=Namespaced,categories=cluster-api,shortName=hvct
// +kubebuilder:storageversion

// HyperVClusterTemplate is the Schema for the hypervclustertemplates API.
type HyperVClusterTemplate struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the HyperVClusterTemplate spec.
	// +optional
	Spec HyperVClusterTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// HyperVClusterTemplateList contains a list of HyperVClusterTemplate.
type HyperVClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HyperVClusterTemplate `json:"items"`
}

func init() {
	objectTypes = append(objectTypes, &HyperVClusterTemplate{}, &HyperVClusterTemplateList{})
}
