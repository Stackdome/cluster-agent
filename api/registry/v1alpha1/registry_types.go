package v1alpha1

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

type RegistryStatusConditionType string

const (
	RegistryReady RegistryStatusConditionType = "Ready"
)

type RegistryAuthSpec struct {
	// Basic authentication credentials for the registry
	// +kubebuilder:validation:Required
	HtPasswordCredentials *HtPasswordCredentialsSpec `json:"htPasswordCredentials"`
}

type HtPasswordCredentialsSpec struct {
	// +required
	CredentialsRef *corev1alpha1.CredentialSecretKeyPair `json:"credentialsRef,omitempty"`
}

type SecretRef struct {
	// name is unique within a namespace to reference a secret resource.
	// +required
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// namespace defines the space within which the secret name must be unique.
	// +required
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,2,opt,name=namespace"`
}

func (h *HtPasswordCredentialsSpec) HTPasswordFileName() string {
	return "htpasswd"
}

type RetentionPolicySpec struct {
	// MaxRepositoryCount is the maximum number of repositories to keep
	// +optional
	MaxRepositoryCount *int32 `json:"maxRepositoryCount,omitempty"`
	// Number of tags to keep per repository
	// +optional
	TagsPerRepo *int32 `json:"tagsPerRepo,omitempty"`
	// Whether to delete untagged manifests
	// +optional
	// +kubebuilder:default=true
	DeleteUntagged bool `json:"deleteUntagged,omitempty"`
}

type RegistryStatus struct {
	// The most recent generation observed by the controller
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is a list of status conditions for the registry
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Internal registry URL for workspaces to use
	// +optional
	InternalURL string `json:"internalUrl,omitempty"`

	// ServiceIP is the cluster IP of the registry svc.
	ServiceIP string `json:"serviceIp,omitempty"`

	// Phase represents the current state of the registry
	// +kubebuilder:default=Pending
	Phase RegistryPhase `json:"phase,omitempty"`
}

type RegistryPhase string

const (
	RegistryPhasePending RegistryPhase = "Pending"
	RegistryPhaseRunning RegistryPhase = "Running"
	RegistryPhaseFailed  RegistryPhase = "Failed"
)

// ClusterRegistrySpec defines the desired state of ClusterRegistry
type ClusterRegistrySpec struct {
	// Owner information
	// +kubebuilder:validation:Required
	Owner RegistryOwner `json:"owner"`

	// Storage configuration for the registry
	// +kubebuilder:validation:Required
	Storage RegistryStorageSpec `json:"storage"`

	// Authentication configuration
	// +optional
	Auth *RegistryAuthSpec `json:"auth"`

	// Retention policies for the registry
	// +optional
	RetentionPolicy *RetentionPolicySpec `json:"retentionPolicy,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1000
	// +kubebuilder:validation:Maximum=65535
	// Port on which the registry will be running
	Port int32 `json:"port"`
}

// RegistryOwner defines who owns this registry
type RegistryOwner struct {
	// Type of the owner (e.g., "Organization", "Team")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Organization;Team
	Type string `json:"type"`

	// ID of the owner
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
}

// Rest of the types remain same, just renamed from Organization to Cluster
type RegistryStorageSpec struct {
	// Size of the registry storage
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=^[0-9]+(Gi|Ti)$
	Size string `json:"size"`

	// StorageClass to use for registry volume
	// +optional
	StorageClass *string `json:"storageClass,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Owner Type",type="string",JSONPath=".spec.owner.type"
// +kubebuilder:printcolumn:name="Owner ID",type="string",JSONPath=".spec.owner.id"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Internal URL",type="string",JSONPath=".status.internalUrl"
// ClusterRegistry is the Schema for the cluster registries API
type ClusterRegistry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterRegistrySpec `json:"spec,omitempty"`
	Status RegistryStatus      `json:"status,omitempty"`
}

func truncateK8sName(name string) string {
	if len(name) <= 63 {
		return name
	}
	return strings.TrimRight(name[:63], "-")
}

func (c *ClusterRegistry) RegistryConfigMapName() string {
	return truncateK8sName(c.Name + "-config")
}

func (c *ClusterRegistry) RegistryPVCName() string {
	return truncateK8sName(c.Name + "-storage")
}

func (c *ClusterRegistry) RegistryStatefulSetName() string {
	return c.Name
}

func (c *ClusterRegistry) RegistryHeadlessServiceName() string {
	return truncateK8sName(c.Name + "-headless")
}

func (c *ClusterRegistry) RegistryAuthSecretName() string {
	return truncateK8sName(c.Name + "-auth")
}

// +kubebuilder:object:root=true
// ClusterRegistryList contains a list of ClusterRegistry
type ClusterRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRegistry `json:"items"`
}

func init() {
	// Register all types here
	SchemeBuilder.Register(&ClusterRegistry{}, &ClusterRegistryList{})
}
