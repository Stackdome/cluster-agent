// +kubebuilder:validation:Required

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	NFSServerPhasePending = "Pending"
	NFSServerPhaseReady   = "Ready"

	NFSServerConditionTypeAvailable = "Available"
)

// NFSServerSpec defines the desired state of NFSServer.
// +kubebuilder:validation:Required
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.backingStorageClassName) || has(self.backingStorageClassName)", message="backingStorageClassName is required once set"
type NFSServerSpec struct {
	// +optional
	// +kubebuilder:default=/exports
	ExportDir string `json:"exportDir"`

	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="capacity is immutable"
	// +kubebuilder:validation:MaxLength=100
	Capacity string `json:"capacity"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="backingStorageClassName is immutable"
	// +kubebuilder:validation:MaxLength=100
	BackingStorageClassName *string `json:"backingStorageClassName"`
}

// NFSServerStatus defines the observed state of NFSServer.
type NFSServerStatus struct {
	NFSServerURL string `json:"nfsServerURL,omitempty"`
	// +kubebuilder:default=Pending
	Phase      string             `json:"phase,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NFSServer is the Schema for the nfsservers API.
type NFSServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NFSServerSpec   `json:"spec,omitempty"`
	Status NFSServerStatus `json:"status,omitempty"`
}

// backendPVCName returns the name of the PVC that should be created for the NFS server.
func (n *NFSServer) BackendPVCName() string {
	return n.Name + "-nfs-pvc"
}

// +kubebuilder:object:root=true

// NFSServerList contains a list of NFSServer.
type NFSServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NFSServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NFSServer{}, &NFSServerList{})
}
