package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ObjectStoragePhasePending  = "Pending"
	ObjectStoragePhaseReady    = "Ready"
	ObjectStoragePhaseDeletion = "Deleting"

	ObjectStorageConditionAvailable     = "Available"
	ObjectStorageConditionBucketsReady  = "BucketsReady"
	ObjectStorageConditionTLSConfigured = "TLSConfigured"

	ObjectStorageSecretKeyAccessKey    = "RUSTFS_ACCESS_KEY"
	ObjectStorageSecretKeySecretKey    = "RUSTFS_SECRET_KEY"
	ObjectStorageSecretKeyAWSAccessKey = "AWS_ACCESS_KEY_ID"
	ObjectStorageSecretKeyAWSSecretKey = "AWS_SECRET_ACCESS_KEY"

	ObjectStorageContainerPort = 9000
)

// ObjectStorageSpec defines the desired state of ObjectStorage.
// +kubebuilder:validation:Required
type ObjectStorageSpec struct {
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="capacity is immutable"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Capacity string `json:"capacity"`

	// +optional
	// +kubebuilder:validation:MaxLength=100
	StorageClassName *string `json:"storageClassName,omitempty"`

	// +optional
	Buckets []BucketSpec `json:"buckets,omitempty"`

	// +optional
	Ingress *ObjectStorageIngressSpec `json:"ingress,omitempty"`

	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

type BucketSpec struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
}

type ObjectStorageIngressSpec struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`

	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// +optional
	// +kubebuilder:default=true
	TLS bool `json:"tls"`
}

// ObjectStorageStatus defines the observed state of ObjectStorage.
type ObjectStorageStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +kubebuilder:default=Pending
	Phase      string             `json:"phase,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	Endpoint              string         `json:"endpoint,omitempty"`
	ExternalEndpoint      string         `json:"externalEndpoint,omitempty"`
	CredentialsSecretName string         `json:"credentialsSecretName,omitempty"`
	VolumeName            string         `json:"volumeName,omitempty"`
	PVCName               string         `json:"pvcName,omitempty"`
	Buckets               []BucketStatus `json:"buckets,omitempty"`
}

type BucketStatus struct {
	Name    string `json:"name"`
	Created bool   `json:"created"`
	URL     string `json:"url,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ObjectStorage is the Schema for the objectstorages API.
type ObjectStorage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ObjectStorageSpec   `json:"spec,omitempty"`
	Status ObjectStorageStatus `json:"status,omitempty"`
}

// Helper methods for naming child resources.
func (o *ObjectStorage) VolumeName() string {
	return o.Name + "-objstore-vol"
}

func (o *ObjectStorage) CredentialsSecretName() string {
	return o.Name + "-objstore-credentials"
}

func (o *ObjectStorage) DeploymentName() string {
	return o.Name + "-objstore"
}

func (o *ObjectStorage) ServiceName() string {
	return o.Name + "-objstore"
}

func (o *ObjectStorage) IngressName() string {
	return o.Name + "-objstore"
}

// +kubebuilder:object:root=true

// ObjectStorageList contains a list of ObjectStorage.
type ObjectStorageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ObjectStorage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ObjectStorage{}, &ObjectStorageList{})
}
