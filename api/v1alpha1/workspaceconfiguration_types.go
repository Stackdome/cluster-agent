package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkspaceConfigurationPhase string

const (
	WorkspaceConfigurationPhasePending WorkspaceConfigurationPhase = "Pending"
	WorkspaceConfigurationPhaseReady   WorkspaceConfigurationPhase = "Ready"
)

const (
	WorkspaceConfigurationAvailable = "Available"
)

// WorkspaceConfigurationSpec defines the desired state of WorkspaceConfiguration
type WorkspaceConfigurationSpec struct {
	WorkspaceNamespace string `json:"workspaceNamespace"`
	Username           string `json:"username"`
}

// WorkspaceConfigurationStatus defines the observed state of WorkspaceConfiguration
type WorkspaceConfigurationStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions this object is in.
	Conditions          []metav1.Condition `json:"conditions,omitempty"`
	Namespace           *string            `json:"namespace,omitempty"`
	ServiceAccountName  *string            `json:"serviceAccountName,omitempty"`
	ServiceAccountToken *string            `json:"serviceAccountToken,omitempty"`
	// +kubebuilder:default=Pending
	Phase WorkspaceConfigurationPhase `json:"phase,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// WorkspaceConfiguration is the Schema for the workspaceconfigurations API
type WorkspaceConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceConfigurationSpec   `json:"spec,omitempty"`
	Status WorkspaceConfigurationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceConfigurationList contains a list of WorkspaceConfiguration
type WorkspaceConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceConfiguration{}, &WorkspaceConfigurationList{})
}
