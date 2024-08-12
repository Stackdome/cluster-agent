package v1alpha1

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkspaceUserPhase string

const (
	WorkspaceUserPhasePhasePending WorkspaceUserPhase = "Pending"
	WorkspaceUserPhasePhaseReady   WorkspaceUserPhase = "Ready"
)

const (
	WorkspaceUserAvailable = "Available"
)

// WorkspaceUserSpec defines the desired state of WorkspaceUser
type WorkspaceUserSpec struct {
	// +kubebuilder:validation:MinItems=1
	Namespaces  []string            `json:"namespaces"`
	Username    string              `json:"username"`
	AccessRules []rbacv1.PolicyRule `json:"accessRules"`
	// +optional
	BaseDomain string `json:"baseDomain"`
}

// WorkspaceUserStatus defines the observed state of WorkspaceUser
type WorkspaceUserStatus struct {
	ObservedStackdomeServerGeneration string `json:"observedStackdomeServerGeneration,omitempty"`
	// Conditions is a list of status conditions this object is in.
	Conditions          []metav1.Condition `json:"conditions,omitempty"`
	Namespaces          []string           `json:"namespaces,omitempty"`
	ServiceAccountName  string             `json:"serviceAccountName,omitempty"`
	ServiceAccountToken string             `json:"serviceAccountToken,omitempty"`
	// +kubebuilder:default=Pending
	Phase      WorkspaceUserPhase `json:"phase,omitempty"`
	StatusHash string             `json:"statusHash,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// WorkspaceUser is the Schema for the workspaceusers API
type WorkspaceUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceUserSpec   `json:"spec,omitempty"`
	Status WorkspaceUserStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceUserList contains a list of WorkspaceUser
type WorkspaceUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceUser{}, &WorkspaceUserList{})
}
